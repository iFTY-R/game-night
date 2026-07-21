package room

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/clock"
)

const (
	// maximumRoomCodeAttempts bounds creation latency during invitation-code collisions or entropy exhaustion.
	maximumRoomCodeAttempts = 8
)

// RoomSelector addresses one room by authoritative ID or invitation code, never both.
type RoomSelector struct {
	ID   uuid.UUID
	Code string
}

// CreateRoomCommand contains the authenticated host and explicit pregame policy.
type CreateRoomCommand struct {
	ActorUserID          uuid.UUID
	Visibility           Visibility
	ParticipantCapacity  uint32
	ParticipantAdmission AdmissionMode
	SpectatorAdmission   AdmissionMode
}

// GetRoomCommand requires an authenticated actor before room visibility is evaluated.
type GetRoomCommand struct {
	ActorUserID uuid.UUID
	Selector    RoomSelector
}

// ListPublicRoomsCommand discovers active public rooms through bounded, actor-aware keyset pagination.
type ListPublicRoomsCommand struct {
	ActorUserID uuid.UUID
	Filter      PublicRoomFilter
	After       PublicRoomPageCursor
	PageSize    uint32
}

// JoinRoomCommand admits or queues an authenticated actor under one current aggregate version.
type JoinRoomCommand struct {
	ActorUserID uuid.UUID
	Selector    RoomSelector
	Intent      JoinIntent
	Expected    Version
}

// ApproveMemberCommand promotes one waiting member under host authority.
type ApproveMemberCommand struct {
	ActorUserID uuid.UUID
	RoomID      uuid.UUID
	UserID      uuid.UUID
	Expected    Version
}

// SetAdmissionCommand changes participant and spectator policy together.
type SetAdmissionCommand struct {
	ActorUserID uuid.UUID
	RoomID      uuid.UUID
	Participant AdmissionMode
	Spectator   AdmissionMode
	Expected    Version
}

// RemoveMemberCommand removes a non-host member and returns any required runtime revocation signal.
type RemoveMemberCommand struct {
	ActorUserID uuid.UUID
	RoomID      uuid.UUID
	UserID      uuid.UUID
	Expected    Version
}

// CloseRoomCommand permanently closes a lobby after active-session cancellation has completed.
type CloseRoomCommand struct {
	ActorUserID uuid.UUID
	RoomID      uuid.UUID
	Expected    Version
}

// Service owns room authorization and repository orchestration outside transport and PostgreSQL adapters.
type Service struct {
	repository Repository
	lobby      PublicRoomRepository
	codes      CodeGenerator
	clock      clock.Clock
}

// NewService requires all dependencies so room creation and mutation fail closed when wiring is incomplete.
func NewService(store Store, codes CodeGenerator, source clock.Clock) (*Service, error) {
	if store == nil || codes == nil || source == nil {
		return nil, ErrInvalidRoomInput
	}
	return &Service{repository: store, lobby: store, codes: codes, clock: source}, nil
}

// CreateRoom generates server-owned identifiers and retries only invitation-code uniqueness races.
func (service *Service) CreateRoom(ctx context.Context, command CreateRoomCommand) (Room, error) {
	if service == nil || ctx == nil || command.ActorUserID == uuid.Nil {
		return Room{}, ErrInvalidRoomInput
	}
	roomID, err := uuid.NewV7()
	if err != nil {
		return Room{}, ErrInvalidRoomInput
	}
	for range maximumRoomCodeAttempts {
		code, generateErr := service.codes.Generate()
		if generateErr != nil {
			return Room{}, ErrInvalidRoomInput
		}
		candidate, newErr := NewWithAdmission(
			roomID, command.ActorUserID, code, command.Visibility, command.ParticipantCapacity,
			command.ParticipantAdmission, command.SpectatorAdmission, service.clock.Now(),
		)
		if newErr != nil {
			return Room{}, newErr
		}
		created, createErr := service.repository.Create(ctx, candidate)
		if createErr == nil {
			return created, nil
		}
		if !errors.Is(createErr, ErrRoomCodeUnavailable) {
			return Room{}, createErr
		}
	}
	return Room{}, ErrRoomCodeUnavailable
}

// GetRoom permits public discovery and current-member reads without exposing private rooms to arbitrary IDs.
func (service *Service) GetRoom(ctx context.Context, command GetRoomCommand) (Room, error) {
	if service == nil || ctx == nil || command.ActorUserID == uuid.Nil {
		return Room{}, ErrInvalidRoomInput
	}
	room, err := service.resolve(ctx, command.Selector)
	if err != nil {
		return Room{}, err
	}
	if _, member := room.Member(command.ActorUserID); !member && room.Snapshot().Visibility != VisibilityPublic {
		return Room{}, ErrRoomNotFound
	}
	return room, nil
}

// ListPublicRooms returns only validated public projections and emits a cursor when a lookahead row exists.
func (service *Service) ListPublicRooms(ctx context.Context, command ListPublicRoomsCommand) (PublicRoomPage, error) {
	if service == nil || service.lobby == nil || ctx == nil {
		return PublicRoomPage{}, ErrInvalidRoomInput
	}
	request, err := NewPublicRoomListRequest(command.ActorUserID, command.Filter, command.After, command.PageSize)
	if err != nil {
		return PublicRoomPage{}, err
	}
	rooms, err := service.lobby.ListPublicRooms(ctx, request)
	if err != nil {
		return PublicRoomPage{}, err
	}
	if len(rooms) > int(request.Limit) {
		return PublicRoomPage{}, ErrRoomIntegrity
	}
	page := PublicRoomPage{Rooms: rooms}
	if len(rooms) > int(request.PageSize) {
		page.Rooms = append([]PublicRoomCard(nil), rooms[:request.PageSize]...)
		last := page.Rooms[len(page.Rooms)-1].Snapshot()
		page.NextCursor = PublicRoomPageCursor{UpdatedAt: last.UpdatedAt, RoomID: last.RoomID}
	}
	return page, nil
}

// JoinRoom accepts a public room ID or invitation code and relies on repository CAS for concurrent capacity decisions.
func (service *Service) JoinRoom(ctx context.Context, command JoinRoomCommand) (Room, JoinResult, error) {
	if service == nil || ctx == nil || command.ActorUserID == uuid.Nil || !command.Intent.Valid() {
		return Room{}, JoinResult{}, ErrInvalidRoomInput
	}
	room, err := service.resolve(ctx, command.Selector)
	if err != nil {
		return Room{}, JoinResult{}, err
	}
	if _, member := room.Member(command.ActorUserID); !member && room.Snapshot().Visibility == VisibilityPrivate && command.Selector.Code == "" {
		return Room{}, JoinResult{}, ErrRoomNotFound
	}
	expected, err := optionalExpectedVersion(room.Version(), command.Expected)
	if err != nil {
		return Room{}, JoinResult{}, err
	}
	next, result, err := room.Join(command.ActorUserID, command.Intent, expected, service.clock.Now())
	if err != nil {
		return Room{}, JoinResult{}, err
	}
	if !result.Created {
		return room, result, nil
	}
	stored, err := service.repository.UpdateCAS(ctx, room, next)
	if err != nil {
		return Room{}, JoinResult{}, err
	}
	member, ok := stored.Member(command.ActorUserID)
	if !ok {
		return Room{}, JoinResult{}, ErrRoomIntegrity
	}
	result.Member = member
	return stored, result, nil
}

// ApproveMember promotes one waiting member after exact host and version checks.
func (service *Service) ApproveMember(ctx context.Context, command ApproveMemberCommand) (Room, ApprovalResult, error) {
	if service == nil || ctx == nil || command.ActorUserID == uuid.Nil || command.RoomID == uuid.Nil || command.UserID == uuid.Nil {
		return Room{}, ApprovalResult{}, ErrInvalidRoomInput
	}
	if !requiredVersion(command.Expected) {
		return Room{}, ApprovalResult{}, ErrRoomVersionConflict
	}
	room, err := service.repository.GetByID(ctx, command.RoomID)
	if err != nil {
		return Room{}, ApprovalResult{}, err
	}
	next, result, err := room.ApproveWaiting(command.ActorUserID, command.UserID, command.Expected, service.clock.Now())
	if err != nil {
		return Room{}, ApprovalResult{}, err
	}
	stored, err := service.repository.UpdateCAS(ctx, room, next)
	if err != nil {
		return Room{}, ApprovalResult{}, err
	}
	result.Version = stored.Version()
	return stored, result, nil
}

// SetAdmission commits both admission modes under one host-controlled room version.
func (service *Service) SetAdmission(ctx context.Context, command SetAdmissionCommand) (Room, error) {
	if service == nil || ctx == nil || command.ActorUserID == uuid.Nil || command.RoomID == uuid.Nil {
		return Room{}, ErrInvalidRoomInput
	}
	if !requiredVersion(command.Expected) {
		return Room{}, ErrRoomVersionConflict
	}
	room, err := service.repository.GetByID(ctx, command.RoomID)
	if err != nil {
		return Room{}, err
	}
	next, err := room.SetAdmission(command.ActorUserID, command.Participant, command.Spectator, command.Expected, service.clock.Now())
	if err != nil {
		return Room{}, err
	}
	return service.repository.UpdateCAS(ctx, room, next)
}

// RemoveMember commits membership removal before returning any active-session revocation signal.
func (service *Service) RemoveMember(ctx context.Context, command RemoveMemberCommand) (Room, RemovalResult, error) {
	if service == nil || ctx == nil || command.ActorUserID == uuid.Nil || command.RoomID == uuid.Nil || command.UserID == uuid.Nil {
		return Room{}, RemovalResult{}, ErrInvalidRoomInput
	}
	if !requiredVersion(command.Expected) {
		return Room{}, RemovalResult{}, ErrRoomVersionConflict
	}
	room, err := service.repository.GetByID(ctx, command.RoomID)
	if err != nil {
		return Room{}, RemovalResult{}, err
	}
	next, result, err := room.RemoveMember(command.ActorUserID, command.UserID, command.Expected, service.clock.Now())
	if err != nil {
		return Room{}, RemovalResult{}, err
	}
	stored, err := service.repository.UpdateCAS(ctx, room, next)
	if err != nil {
		return Room{}, RemovalResult{}, err
	}
	result.Version = stored.Version()
	return stored, result, nil
}

// CloseRoom permanently closes an idle room under exact host and version authority.
func (service *Service) CloseRoom(ctx context.Context, command CloseRoomCommand) (Room, error) {
	if service == nil || ctx == nil || command.ActorUserID == uuid.Nil || command.RoomID == uuid.Nil {
		return Room{}, ErrInvalidRoomInput
	}
	if !requiredVersion(command.Expected) {
		return Room{}, ErrRoomVersionConflict
	}
	room, err := service.repository.GetByID(ctx, command.RoomID)
	if err != nil {
		return Room{}, err
	}
	next, err := room.Close(command.ActorUserID, command.Expected, service.clock.Now())
	if err != nil {
		return Room{}, err
	}
	if next.Version() == room.Version() {
		return room, nil
	}
	return service.repository.UpdateCAS(ctx, room, next)
}

func (service *Service) resolve(ctx context.Context, selector RoomSelector) (Room, error) {
	selector.Code = strings.TrimSpace(selector.Code)
	if (selector.ID == uuid.Nil) == (selector.Code == "") {
		return Room{}, ErrInvalidRoomInput
	}
	if selector.ID != uuid.Nil {
		return service.repository.GetByID(ctx, selector.ID)
	}
	if err := ValidateRoomCode(selector.Code); err != nil {
		return Room{}, err
	}
	return service.repository.GetByCode(ctx, selector.Code)
}

func optionalExpectedVersion(current, expected Version) (Version, error) {
	if expected.Room == 0 && expected.Membership == 0 {
		return current, nil
	}
	if !requiredVersion(expected) {
		return Version{}, ErrRoomVersionConflict
	}
	return expected, nil
}

func requiredVersion(value Version) bool {
	return value.Room > 0 && value.Membership > 0
}
