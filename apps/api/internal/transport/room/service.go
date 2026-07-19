// Package room adapts authenticated PartyRoom commands to the generated Connect service.
package room

import (
	"context"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/cookies"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/csrf"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/origin"
	roomv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/room/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/room/v1/roomv1connect"
	identityDomain "github.com/iFTY-R/game-night/platform/identity"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Service keeps Cookie, Origin, CSRF, and wire mapping outside the room domain.
type Service struct {
	domain        *roomDomain.Service
	authenticator PrincipalAuthenticator
	origins       *origin.UserValidator
	csrf          *csrf.UserValidator
}

// NewService validates complete room transport wiring before the generated handler is mounted.
func NewService(
	domainService *roomDomain.Service,
	authenticator PrincipalAuthenticator,
	originValidator *origin.UserValidator,
	csrfValidator *csrf.UserValidator,
) (*Service, error) {
	if domainService == nil || authenticator == nil || originValidator == nil || csrfValidator == nil {
		return nil, roomDomain.ErrInvalidRoomInput
	}
	return &Service{domain: domainService, authenticator: authenticator, origins: originValidator, csrf: csrfValidator}, nil
}

// CreateRoom creates a server-owned room ID/code after write authorization.
func (service *Service) CreateRoom(ctx context.Context, request *connect.Request[roomv1.CreateRoomRequest]) (*connect.Response[roomv1.CreateRoomResponse], error) {
	actor, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	created, err := service.domain.CreateRoom(ctx, roomDomain.CreateRoomCommand{
		ActorUserID: actor, Visibility: visibilityDomain(request.Msg.GetVisibility()),
		ParticipantCapacity:  request.Msg.GetParticipantCapacity(),
		ParticipantAdmission: admissionDomain(request.Msg.GetParticipantAdmission()),
		SpectatorAdmission:   admissionDomain(request.Msg.GetSpectatorAdmission()),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.CreateRoomResponse{Room: roomWire(created)}), nil
}

// GetRoom authenticates a safe read without requiring an Origin header or double-submit header.
func (service *Service) GetRoom(ctx context.Context, request *connect.Request[roomv1.GetRoomRequest]) (*connect.Response[roomv1.GetRoomResponse], error) {
	actor, err := service.authenticate(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	selector, err := selectorDomain(request.Msg.GetRoomId(), request.Msg.GetRoomCode())
	if err != nil {
		return nil, err
	}
	loaded, err := service.domain.GetRoom(ctx, roomDomain.GetRoomCommand{ActorUserID: actor, Selector: selector})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.GetRoomResponse{Room: roomWire(loaded)}), nil
}

// JoinRoom admits or queues the current principal through a public ID or private invitation code.
func (service *Service) JoinRoom(ctx context.Context, request *connect.Request[roomv1.JoinRoomRequest]) (*connect.Response[roomv1.JoinRoomResponse], error) {
	actor, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	selector, err := selectorDomain(request.Msg.GetRoomId(), request.Msg.GetRoomCode())
	if err != nil {
		return nil, err
	}
	joined, result, err := service.domain.JoinRoom(ctx, roomDomain.JoinRoomCommand{
		ActorUserID: actor, Selector: selector, Intent: joinIntentDomain(request.Msg.GetIntent()),
		Expected: versionDomain(request.Msg.GetExpectedVersion()),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.JoinRoomResponse{
		Room: roomWire(joined), Member: memberWire(result.Member), Created: result.Created,
	}), nil
}

// ApproveMember promotes one waiting member under host and version authority.
func (service *Service) ApproveMember(ctx context.Context, request *connect.Request[roomv1.ApproveMemberRequest]) (*connect.Response[roomv1.ApproveMemberResponse], error) {
	actor, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	roomID, userID, err := twoUUIDs(request.Msg.GetRoomId(), request.Msg.GetUserId())
	if err != nil {
		return nil, err
	}
	updated, result, err := service.domain.ApproveMember(ctx, roomDomain.ApproveMemberCommand{
		ActorUserID: actor, RoomID: roomID, UserID: userID, Expected: versionDomain(request.Msg.GetExpectedVersion()),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.ApproveMemberResponse{Room: roomWire(updated), Member: memberWire(result.Member)}), nil
}

// SetAdmission changes both role policies in one host command.
func (service *Service) SetAdmission(ctx context.Context, request *connect.Request[roomv1.SetAdmissionRequest]) (*connect.Response[roomv1.SetAdmissionResponse], error) {
	actor, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	roomID, err := parseUUID(request.Msg.GetRoomId())
	if err != nil {
		return nil, err
	}
	updated, err := service.domain.SetAdmission(ctx, roomDomain.SetAdmissionCommand{
		ActorUserID: actor, RoomID: roomID, Participant: admissionDomain(request.Msg.GetParticipantAdmission()),
		Spectator: admissionDomain(request.Msg.GetSpectatorAdmission()), Expected: versionDomain(request.Msg.GetExpectedVersion()),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.SetAdmissionResponse{Room: roomWire(updated)}), nil
}

// StartGame creates the server-owned session identifier and returns the frozen seats.
func (service *Service) StartGame(ctx context.Context, request *connect.Request[roomv1.StartGameRequest]) (*connect.Response[roomv1.StartGameResponse], error) {
	actor, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	roomID, err := parseUUID(request.Msg.GetRoomId())
	if err != nil {
		return nil, err
	}
	updated, start, err := service.domain.StartGame(ctx, roomDomain.StartGameCommand{
		ActorUserID: actor, RoomID: roomID, GameID: request.Msg.GetGameId(),
		Expected: versionDomain(request.Msg.GetExpectedVersion()),
	})
	if err != nil {
		return nil, err
	}
	participants := make([]*roomv1.FrozenParticipant, len(start.Participants))
	for index, participant := range start.Participants {
		participants[index] = &roomv1.FrozenParticipant{UserId: participant.UserID.String(), SeatIndex: participant.SeatIndex}
	}
	return connect.NewResponse(&roomv1.StartGameResponse{
		Room: roomWire(updated), SessionId: start.SessionID.String(), GameId: start.GameID, Participants: participants,
	}), nil
}

// FinishGame clears only the matching active session and leaves participant admission closed.
func (service *Service) FinishGame(ctx context.Context, request *connect.Request[roomv1.FinishGameRequest]) (*connect.Response[roomv1.FinishGameResponse], error) {
	actor, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	roomID, sessionID, err := twoUUIDs(request.Msg.GetRoomId(), request.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	updated, err := service.domain.FinishGame(ctx, roomDomain.FinishGameCommand{
		ActorUserID: actor, RoomID: roomID, SessionID: sessionID, Expected: versionDomain(request.Msg.GetExpectedVersion()),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.FinishGameResponse{Room: roomWire(updated)}), nil
}

// RemoveMember returns the runtime revocation flag alongside the committed room state.
func (service *Service) RemoveMember(ctx context.Context, request *connect.Request[roomv1.RemoveMemberRequest]) (*connect.Response[roomv1.RemoveMemberResponse], error) {
	actor, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	roomID, userID, err := twoUUIDs(request.Msg.GetRoomId(), request.Msg.GetUserId())
	if err != nil {
		return nil, err
	}
	updated, result, err := service.domain.RemoveMember(ctx, roomDomain.RemoveMemberCommand{
		ActorUserID: actor, RoomID: roomID, UserID: userID, Expected: versionDomain(request.Msg.GetExpectedVersion()),
	})
	if err != nil {
		return nil, err
	}
	activeSessionID := ""
	if result.SessionID != uuid.Nil {
		activeSessionID = result.SessionID.String()
	}
	return connect.NewResponse(&roomv1.RemoveMemberResponse{
		Room: roomWire(updated), Removed: memberWire(result.Removed), ParticipantRevoked: result.ParticipantRevoked,
		ActiveSessionId: activeSessionID,
	}), nil
}

// CloseRoom permanently closes an idle room under host authority.
func (service *Service) CloseRoom(ctx context.Context, request *connect.Request[roomv1.CloseRoomRequest]) (*connect.Response[roomv1.CloseRoomResponse], error) {
	actor, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	roomID, err := parseUUID(request.Msg.GetRoomId())
	if err != nil {
		return nil, err
	}
	updated, err := service.domain.CloseRoom(ctx, roomDomain.CloseRoomCommand{
		ActorUserID: actor, RoomID: roomID, Expected: versionDomain(request.Msg.GetExpectedVersion()),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.CloseRoomResponse{Room: roomWire(updated)}), nil
}

func (service *Service) authenticate(ctx context.Context, request *http.Request) (uuid.UUID, error) {
	credentials, err := cookies.ReadUserDevice(request)
	if err != nil {
		return uuid.Nil, identityDomain.ErrDeviceAuthentication
	}
	return service.authenticator.Authenticate(ctx, credentials.CookieToken(), credentials.CSRFToken())
}

func (service *Service) authenticateWrite(ctx context.Context, request *http.Request) (uuid.UUID, error) {
	if _, err := service.origins.Validate(request); err != nil {
		return uuid.Nil, err
	}
	if _, err := service.csrf.Validate(request); err != nil {
		return uuid.Nil, err
	}
	return service.authenticate(ctx, request)
}

func selectorDomain(roomID, roomCode string) (roomDomain.RoomSelector, error) {
	roomID, roomCode = strings.TrimSpace(roomID), strings.TrimSpace(roomCode)
	if (roomID == "") == (roomCode == "") {
		return roomDomain.RoomSelector{}, roomDomain.ErrInvalidRoomInput
	}
	if roomID != "" {
		parsed, err := parseUUID(roomID)
		if err != nil {
			return roomDomain.RoomSelector{}, err
		}
		return roomDomain.RoomSelector{ID: parsed}, nil
	}
	if err := roomDomain.ValidateRoomCode(roomCode); err != nil {
		return roomDomain.RoomSelector{}, err
	}
	return roomDomain.RoomSelector{Code: roomCode}, nil
}

func versionDomain(value *roomv1.RoomVersion) roomDomain.Version {
	if value == nil {
		return roomDomain.Version{}
	}
	return roomDomain.Version{Room: value.GetRoomVersion(), Membership: value.GetMembershipVersion()}
}

func parseUUID(value string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil || parsed == uuid.Nil {
		return uuid.Nil, roomDomain.ErrInvalidRoomInput
	}
	return parsed, nil
}

func twoUUIDs(first, second string) (uuid.UUID, uuid.UUID, error) {
	firstID, err := parseUUID(first)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	secondID, err := parseUUID(second)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return firstID, secondID, nil
}

func roomWire(room roomDomain.Room) *roomv1.Room {
	snapshot := room.Snapshot()
	members := make([]*roomv1.RoomMember, len(snapshot.Members))
	for index, member := range snapshot.Members {
		members[index] = memberWire(member)
	}
	activeSessionID := ""
	if snapshot.ActiveSessionID != uuid.Nil {
		activeSessionID = snapshot.ActiveSessionID.String()
	}
	return &roomv1.Room{
		RoomId: snapshot.ID.String(), RoomCode: snapshot.RoomCode, Visibility: visibilityWire(snapshot.Visibility),
		Status: statusWire(snapshot.Status), HostUserId: snapshot.HostUserID.String(),
		ParticipantCapacity: snapshot.ParticipantCapacity, ParticipantAdmission: admissionWire(snapshot.ParticipantAdmission),
		SpectatorAdmission: admissionWire(snapshot.SpectatorAdmission), Members: members,
		ActiveSessionId: activeSessionID, ActiveGameId: snapshot.ActiveGameID,
		Version:   &roomv1.RoomVersion{RoomVersion: snapshot.RoomVersion, MembershipVersion: snapshot.MembershipVersion},
		CreatedAt: timestamppb.New(snapshot.CreatedAt), UpdatedAt: timestamppb.New(snapshot.UpdatedAt),
	}
}

func memberWire(member roomDomain.MemberSnapshot) *roomv1.RoomMember {
	return &roomv1.RoomMember{
		UserId: member.UserID.String(), Role: memberRoleWire(member.Role), RequestedRole: memberRoleWire(member.RequestedRole),
		SeatIndex: member.SeatIndex, JoinedAt: timestamppb.New(member.JoinedAt), LastSeenAt: timestamppb.New(member.LastSeenAt),
	}
}

func visibilityDomain(value roomv1.RoomVisibility) roomDomain.Visibility {
	switch value {
	case roomv1.RoomVisibility_ROOM_VISIBILITY_PRIVATE:
		return roomDomain.VisibilityPrivate
	case roomv1.RoomVisibility_ROOM_VISIBILITY_PUBLIC:
		return roomDomain.VisibilityPublic
	default:
		return ""
	}
}

func visibilityWire(value roomDomain.Visibility) roomv1.RoomVisibility {
	if value == roomDomain.VisibilityPublic {
		return roomv1.RoomVisibility_ROOM_VISIBILITY_PUBLIC
	}
	return roomv1.RoomVisibility_ROOM_VISIBILITY_PRIVATE
}

func admissionDomain(value roomv1.AdmissionMode) roomDomain.AdmissionMode {
	switch value {
	case roomv1.AdmissionMode_ADMISSION_MODE_OPEN:
		return roomDomain.AdmissionOpen
	case roomv1.AdmissionMode_ADMISSION_MODE_APPROVAL:
		return roomDomain.AdmissionApproval
	case roomv1.AdmissionMode_ADMISSION_MODE_CLOSED:
		return roomDomain.AdmissionClosed
	default:
		return ""
	}
}

func admissionWire(value roomDomain.AdmissionMode) roomv1.AdmissionMode {
	switch value {
	case roomDomain.AdmissionOpen:
		return roomv1.AdmissionMode_ADMISSION_MODE_OPEN
	case roomDomain.AdmissionApproval:
		return roomv1.AdmissionMode_ADMISSION_MODE_APPROVAL
	case roomDomain.AdmissionClosed:
		return roomv1.AdmissionMode_ADMISSION_MODE_CLOSED
	default:
		return roomv1.AdmissionMode_ADMISSION_MODE_UNSPECIFIED
	}
}

func statusWire(value roomDomain.RoomStatus) roomv1.RoomStatus {
	switch value {
	case roomDomain.RoomStatusLobby:
		return roomv1.RoomStatus_ROOM_STATUS_LOBBY
	case roomDomain.RoomStatusPlaying:
		return roomv1.RoomStatus_ROOM_STATUS_PLAYING
	case roomDomain.RoomStatusClosed:
		return roomv1.RoomStatus_ROOM_STATUS_CLOSED
	default:
		return roomv1.RoomStatus_ROOM_STATUS_UNSPECIFIED
	}
}

func joinIntentDomain(value roomv1.JoinIntent) roomDomain.JoinIntent {
	switch value {
	case roomv1.JoinIntent_JOIN_INTENT_PARTICIPANT:
		return roomDomain.JoinIntentParticipant
	case roomv1.JoinIntent_JOIN_INTENT_SPECTATOR:
		return roomDomain.JoinIntentSpectator
	default:
		return ""
	}
}

func memberRoleWire(value roomDomain.MemberRole) roomv1.MemberRole {
	switch value {
	case roomDomain.MemberRoleParticipant:
		return roomv1.MemberRole_MEMBER_ROLE_PARTICIPANT
	case roomDomain.MemberRoleSpectator:
		return roomv1.MemberRole_MEMBER_ROLE_SPECTATOR
	case roomDomain.MemberRoleWaiting:
		return roomv1.MemberRole_MEMBER_ROLE_WAITING
	default:
		return roomv1.MemberRole_MEMBER_ROLE_UNSPECIFIED
	}
}

func requestHTTP[T any](request *connect.Request[T]) *http.Request {
	if request == nil {
		return nil
	}
	return &http.Request{Header: request.Header(), RemoteAddr: request.Peer().Addr}
}

var _ roomv1connect.RoomServiceHandler = (*Service)(nil)
