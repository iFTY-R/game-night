package room

import (
	"context"
	"errors"

	"github.com/google/uuid"
	admin "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/outbox"
	roomdomain "github.com/iFTY-R/game-night/platform/room"
)

// RoomStore is the narrow write port for aggregate-safe ordinary admin room controls.
type RoomStore interface {
	GetByID(context.Context, uuid.UUID) (roomdomain.Room, error)
	UpdateCAS(context.Context, roomdomain.Room, roomdomain.Room) (roomdomain.Room, error)
	CommitRemoval(context.Context, roomdomain.Room, roomdomain.Room, outbox.Event) (roomdomain.Room, error)
}

// GameController routes force-terminate commands to the current realtime owner instead of updating PostgreSQL directly.
type GameController interface {
	TerminateGame(context.Context, ForceTerminateGameCommand) (GameSummary, bool, error)
}

type SetAdmissionCommand struct {
	RoomID               uuid.UUID
	ParticipantAdmission roomdomain.AdmissionMode
	SpectatorAdmission   roomdomain.AdmissionMode
	ExpectedRoomVersion  uint64
}

type RemoveMemberCommand struct {
	RoomID                    uuid.UUID
	UserID                    uuid.UUID
	ExpectedRoomVersion       uint64
	ExpectedMembershipVersion uint64
}

type ForceCloseRoomCommand struct {
	RoomID              uuid.UUID
	ExpectedRoomVersion uint64
}

type ForceTerminateGameCommand struct {
	SessionID              uuid.UUID
	Reason                 string
	ExpectedStateVersion   uint64
	ExpectedOwnershipEpoch uint64
}

// SetRoomAdmission applies ordinary admission changes through the room aggregate and exact room-version CAS.
func (service *Service) SetRoomAdmission(ctx context.Context, actor admin.ActorContext, command SetAdmissionCommand) (RoomCommandResult, error) {
	if service == nil || service.rooms == nil || service.repository == nil || service.clock == nil || ctx == nil ||
		command.RoomID == uuid.Nil || command.ExpectedRoomVersion == 0 {
		return RoomCommandResult{}, ErrInvalidInput
	}
	if err := actor.Require(admin.PermissionRoomsControl); err != nil {
		return RoomCommandResult{}, ErrPermissionDenied
	}
	room, err := service.rooms.GetByID(ctx, command.RoomID)
	if err != nil {
		return RoomCommandResult{}, mapRoomDomainError(err)
	}
	snapshot := room.Snapshot()
	if snapshot.ParticipantAdmission == command.ParticipantAdmission && snapshot.SpectatorAdmission == command.SpectatorAdmission {
		return service.roomCommandResult(ctx, command.RoomID, CommandOutcomeNoChange, 0)
	}
	next, err := room.SetAdmissionByAdmin(
		roomdomain.AdminActor{ID: actor.AdminID()}, command.ParticipantAdmission, command.SpectatorAdmission,
		roomdomain.Version{Room: command.ExpectedRoomVersion, Membership: snapshot.MembershipVersion}, service.clock.Now(),
	)
	if errors.Is(err, roomdomain.ErrRoomVersionConflict) {
		return service.roomVersionConflictResult(ctx, command.RoomID)
	}
	if err != nil {
		return RoomCommandResult{}, mapRoomDomainError(err)
	}
	if _, err = service.rooms.UpdateCAS(ctx, room, next); err != nil {
		if errors.Is(err, roomdomain.ErrRoomVersionConflict) {
			return service.roomVersionConflictResult(ctx, command.RoomID)
		}
		return RoomCommandResult{}, mapRoomDomainError(err)
	}
	return service.roomCommandResult(ctx, command.RoomID, CommandOutcomeExecuted, 0)
}

// RemoveRoomMember removes a non-host member and emits the existing revocation outbox fact when needed.
func (service *Service) RemoveRoomMember(ctx context.Context, actor admin.ActorContext, command RemoveMemberCommand) (RoomCommandResult, error) {
	if service == nil || service.rooms == nil || service.repository == nil || service.clock == nil || ctx == nil ||
		command.RoomID == uuid.Nil || command.UserID == uuid.Nil || command.ExpectedRoomVersion == 0 || command.ExpectedMembershipVersion == 0 {
		return RoomCommandResult{}, ErrInvalidInput
	}
	if err := actor.Require(admin.PermissionRoomsControl); err != nil {
		return RoomCommandResult{}, ErrPermissionDenied
	}
	before, err := service.rooms.GetByID(ctx, command.RoomID)
	if err != nil {
		return RoomCommandResult{}, mapRoomDomainError(err)
	}
	now := service.clock.Now()
	next, removal, err := before.RemoveMemberByAdmin(
		roomdomain.AdminActor{ID: actor.AdminID()}, command.UserID,
		roomdomain.Version{Room: command.ExpectedRoomVersion, Membership: command.ExpectedMembershipVersion}, now,
	)
	if errors.Is(err, roomdomain.ErrRoomVersionConflict) {
		return service.roomVersionConflictResult(ctx, command.RoomID)
	}
	if err != nil {
		return RoomCommandResult{}, mapRoomDomainError(err)
	}
	var event outbox.Event
	if removal.ParticipantRevoked {
		eventID, idErr := uuid.NewV7()
		if idErr != nil {
			return RoomCommandResult{}, ErrInvalidInput
		}
		event, err = roomdomain.NewParticipantRevokedEvent(roomdomain.ParticipantRevocationFact{
			EventID: eventID, RoomID: command.RoomID, SessionID: removal.SessionID, UserID: command.UserID,
			ActorKind: roomdomain.RemovalActorAdmin, ActorID: actor.AdminID(), Reason: roomdomain.RemovalReasonAdminRemoved,
			MembershipVersion: removal.Version.Membership, OccurredAt: now,
		})
		if err != nil {
			return RoomCommandResult{}, mapRoomDomainError(err)
		}
	}
	if removal.ParticipantRevoked {
		_, err = service.rooms.CommitRemoval(ctx, before, next, event)
	} else {
		_, err = service.rooms.UpdateCAS(ctx, before, next)
	}
	if errors.Is(err, roomdomain.ErrRoomVersionConflict) {
		return service.roomVersionConflictResult(ctx, command.RoomID)
	}
	if err != nil {
		return RoomCommandResult{}, mapRoomDomainError(err)
	}
	return service.roomCommandResult(ctx, command.RoomID, CommandOutcomeExecuted, revokedConnectionCount(removal.ParticipantRevoked))
}

// ForceCloseRoom closes only non-playing rooms; active games must use ForceTerminateGame through owner routing.
func (service *Service) ForceCloseRoom(ctx context.Context, actor admin.ActorContext, command ForceCloseRoomCommand) (RoomCommandResult, error) {
	if service == nil || service.rooms == nil || service.repository == nil || service.clock == nil || ctx == nil ||
		command.RoomID == uuid.Nil || command.ExpectedRoomVersion == 0 {
		return RoomCommandResult{}, ErrInvalidInput
	}
	if err := actor.Require(admin.PermissionRoomsControl); err != nil {
		return RoomCommandResult{}, ErrPermissionDenied
	}
	before, err := service.rooms.GetByID(ctx, command.RoomID)
	if err != nil {
		return RoomCommandResult{}, mapRoomDomainError(err)
	}
	snapshot := before.Snapshot()
	if snapshot.Status == roomdomain.RoomStatusClosed {
		return service.roomCommandResult(ctx, command.RoomID, CommandOutcomeNoChange, 0)
	}
	next, err := before.CloseWaitingByAdmin(
		roomdomain.AdminActor{ID: actor.AdminID()},
		roomdomain.Version{Room: command.ExpectedRoomVersion, Membership: snapshot.MembershipVersion},
		service.clock.Now(),
	)
	if errors.Is(err, roomdomain.ErrRoomVersionConflict) {
		return service.roomVersionConflictResult(ctx, command.RoomID)
	}
	if err != nil {
		return RoomCommandResult{}, mapRoomDomainError(err)
	}
	if _, err = service.rooms.UpdateCAS(ctx, before, next); err != nil {
		if errors.Is(err, roomdomain.ErrRoomVersionConflict) {
			return service.roomVersionConflictResult(ctx, command.RoomID)
		}
		return RoomCommandResult{}, mapRoomDomainError(err)
	}
	return service.roomCommandResult(ctx, command.RoomID, CommandOutcomeExecuted, 0)
}

// ForceTerminateGame delegates to the current owner-facing controller and never falls back to direct DB mutation.
func (service *Service) ForceTerminateGame(ctx context.Context, actor admin.ActorContext, command ForceTerminateGameCommand) (GameCommandResult, error) {
	if service == nil || service.games == nil || service.clock == nil || ctx == nil || command.SessionID == uuid.Nil ||
		command.ExpectedStateVersion == 0 || command.ExpectedOwnershipEpoch == 0 {
		return GameCommandResult{}, ErrInvalidInput
	}
	if err := actor.Require(admin.PermissionGamesControl); err != nil {
		return GameCommandResult{}, ErrPermissionDenied
	}
	game, repairRequired, err := service.games.TerminateGame(ctx, command)
	if err != nil {
		if errors.Is(err, ErrRepositoryUnavailable) {
			return GameCommandResult{Outcome: CommandOutcomeOwnerUnreachable, RepairRequired: true}, nil
		}
		return GameCommandResult{}, err
	}
	outcome := CommandOutcomeExecuted
	if repairRequired {
		outcome = CommandOutcomeRepairRequired
	}
	return GameCommandResult{Outcome: outcome, Game: game, RepairRequired: repairRequired}, nil
}

func (service *Service) roomCommandResult(ctx context.Context, roomID uuid.UUID, outcome CommandOutcome, revoked uint32) (RoomCommandResult, error) {
	detail, err := service.roomDetailForCommand(ctx, roomID)
	if err != nil {
		return RoomCommandResult{}, err
	}
	return RoomCommandResult{Outcome: outcome, Room: detail.Summary, RevokedConnections: revoked}, nil
}

func (service *Service) roomDetailForCommand(ctx context.Context, roomID uuid.UUID) (RoomDetail, error) {
	detail, err := service.repository.GetRoom(ctx, roomID)
	if err != nil {
		return RoomDetail{}, err
	}
	sampledAt := detail.SampledAt
	if sampledAt.IsZero() {
		sampledAt = service.clock.Now()
	}
	rooms := service.enrichRooms(ctx, []RoomSummary{detail.Summary}, sampledAt)
	detail.Summary = rooms[0]
	detail.ActiveGames = service.enrichGames(ctx, detail.ActiveGames, sampledAt)
	if allRoomMembersOffline(detail.Members) {
		detail.Summary.Anomalies = appendRoomAnomaly(detail.Summary.Anomalies, RoomAnomalyAllPlayersOffline)
	}
	detail.SampledAt = sampledAt
	return detail, nil
}

func (service *Service) roomVersionConflictResult(ctx context.Context, roomID uuid.UUID) (RoomCommandResult, error) {
	detail, err := service.repository.GetRoom(ctx, roomID)
	if err != nil {
		return RoomCommandResult{}, err
	}
	return RoomCommandResult{
		Outcome: CommandOutcomeVersionConflict, Room: detail.Summary,
		CurrentRoomVersion: detail.Summary.RoomVersion, CurrentMemberVersion: detail.Summary.MembershipVersion,
	}, nil
}

func mapRoomDomainError(err error) error {
	switch {
	case errors.Is(err, roomdomain.ErrInvalidRoomInput):
		return ErrInvalidInput
	case errors.Is(err, roomdomain.ErrRoomNotFound):
		return ErrNotFound
	case errors.Is(err, roomdomain.ErrRoomVersionConflict):
		return ErrConflict
	case errors.Is(err, roomdomain.ErrRoomRepositoryUnavailable):
		return ErrRepositoryUnavailable
	default:
		return err
	}
}

func revokedConnectionCount(revoked bool) uint32 {
	if revoked {
		return 1
	}
	return 0
}
