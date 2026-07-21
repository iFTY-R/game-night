// Package game adapts authenticated game commands to the generated Connect service.
package game

import (
	"context"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/cookies"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/csrf"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/origin"
	gamev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1/gamev1connect"
	"github.com/iFTY-R/game-night/platform/clock"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	identityDomain "github.com/iFTY-R/game-night/platform/identity"
	redisstore "github.com/iFTY-R/game-night/platform/persistence/redis"
	"github.com/iFTY-R/game-night/platform/replay"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
	gameSDK "github.com/iFTY-R/game-night/sdk/go/game"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Runtime is the viewer-safe command surface exposed by the authoritative GameSession service.
type Runtime interface {
	Start(context.Context, gameruntime.StartCommand) (roomDomain.Room, gameruntime.Session, error)
	HandleAction(context.Context, gameruntime.ActionCommand) (gameruntime.ActionResult, error)
	HandleSystem(context.Context, gameruntime.SystemCommand) (gameruntime.SystemCommitResult, error)
	ProjectCurrent(context.Context, uuid.UUID, gameSDK.Viewer) (gameruntime.Session, gameSDK.Projection, error)
	ProjectReplayCurrent(context.Context, uuid.UUID, gameSDK.Viewer, gameSDK.ReplayAccessPolicy) (gameruntime.Session, gameSDK.Projection, error)
}

// SessionReader loads only metadata needed to derive ownership and viewer authorization.
type SessionReader interface {
	Get(context.Context, uuid.UUID) (gameruntime.Session, error)
}

// RoomReader loads current membership, host authority, and the active-session pointer.
type RoomReader interface {
	GetByID(context.Context, uuid.UUID) (roomDomain.Room, error)
}

// TicketIssuer stores one opaque grant behind a short-lived, one-time Redis ticket.
type TicketIssuer interface {
	IssueConnectionTicket(context.Context, []byte) (string, error)
}

// FanoutPublisher publishes only committed session/version notifications.
type FanoutPublisher interface {
	PublishSessionFanout(context.Context, redisstore.SessionFanoutEvent) error
}

// Service keeps identity, room authorization, protocol mapping, and Redis coordination outside pure game modules.
type Service struct {
	runtime       Runtime
	sessions      SessionReader
	rooms         RoomReader
	replays       replay.Repository
	authenticator PrincipalAuthenticator
	origins       *origin.UserValidator
	csrf          *csrf.UserValidator
	tickets       TicketIssuer
	fanout        FanoutPublisher
	clock         clock.Clock
	ticketTTL     time.Duration
}

// NewService validates the complete user-facing game transport before it is mounted.
func NewService(
	runtime Runtime,
	sessions SessionReader,
	rooms RoomReader,
	replays replay.Repository,
	authenticator PrincipalAuthenticator,
	originValidator *origin.UserValidator,
	csrfValidator *csrf.UserValidator,
	tickets TicketIssuer,
	fanout FanoutPublisher,
	source clock.Clock,
	ticketTTL time.Duration,
) (*Service, error) {
	if runtime == nil || sessions == nil || rooms == nil || replays == nil || authenticator == nil || originValidator == nil ||
		csrfValidator == nil || tickets == nil || fanout == nil || source == nil ||
		ticketTTL < redisstore.MinimumTicketTTL || ticketTTL > redisstore.MaximumTicketTTL {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	return &Service{
		runtime: runtime, sessions: sessions, rooms: rooms, replays: replays, authenticator: authenticator,
		origins: originValidator, csrf: csrfValidator, tickets: tickets, fanout: fanout,
		clock: source, ticketTTL: ticketTTL,
	}, nil
}

// StartSession creates a trusted room/session transition; the client cannot select module versions or starting seats.
func (service *Service) StartSession(ctx context.Context, request *connect.Request[gamev1.StartSessionRequest]) (*connect.Response[gamev1.StartSessionResponse], error) {
	actor, _, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil || request.Msg.GetExpectedRoomVersion() == 0 || request.Msg.GetExpectedMembershipVersion() == 0 {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	operationID, err := parseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, err
	}
	requestDigest, err := parseRequestDigest(request.Msg.GetRequestDigest())
	if err != nil {
		return nil, err
	}
	roomID, err := parseUUID(request.Msg.GetRoomId())
	if err != nil {
		return nil, err
	}
	gameID, err := gameSDK.ParseGameID(request.Msg.GetGameId())
	if err != nil {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	config, err := configMessage(request.Msg.GetConfig(), gameID)
	if err != nil {
		return nil, err
	}
	room, session, err := service.runtime.Start(ctx, gameruntime.StartCommand{
		ActorUserID: actor, RoomID: roomID, GameID: gameID,
		Expected: roomDomain.Version{
			Room: request.Msg.GetExpectedRoomVersion(), Membership: request.Msg.GetExpectedMembershipVersion(),
		},
		Config: config, OperationID: operationID, RequestDigest: requestDigest,
	})
	if err != nil {
		return nil, err
	}
	viewer, err := playerViewer(session, actor)
	if err != nil {
		return nil, err
	}
	projectedSession, projection, err := service.runtime.ProjectCurrent(ctx, session.Snapshot().ID, viewer)
	if err != nil {
		return nil, err
	}
	if projectedSession.Snapshot().ID != session.Snapshot().ID || projectedSession.Snapshot().RoomID != roomID {
		return nil, gameruntime.ErrGameSessionIntegrity
	}
	session = projectedSession
	// A missed start notification cannot expose stale game state because every subscriber begins from PostgreSQL.
	_ = service.publish(ctx, session)
	return connect.NewResponse(&gamev1.StartSessionResponse{
		Session: sessionWire(session), Projection: projectionWire(session, viewer.Kind, projection, room.Snapshot().HostUserID == actor),
	}), nil
}

// GameAction commits one exact-version player command and republishes only its committed session/version cursor.
func (service *Service) GameAction(ctx context.Context, request *connect.Request[gamev1.GameActionRequest]) (*connect.Response[gamev1.GameActionResponse], error) {
	actor, _, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil || request.Msg.GetExpectedStateVersion() == 0 {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	roomID, sessionID, err := parseRoomSession(request.Msg.GetRoomId(), request.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	operationID, err := parseOperationID(request.Msg.GetActionId())
	if err != nil {
		return nil, err
	}
	requestDigest, err := parseRequestDigest(request.Msg.GetRequestDigest())
	if err != nil {
		return nil, err
	}
	versionKey, command, err := envelopeMessage(request.Msg.GetCommand())
	if err != nil {
		return nil, err
	}
	authorized, err := service.authorizeLive(ctx, actor, roomID, sessionID, gamev1.ViewerKind_VIEWER_KIND_PLAYER)
	if err != nil {
		return nil, err
	}
	if authorized.session.Snapshot().VersionKey != versionKey {
		return nil, gameruntime.ErrStateVersionConflict
	}
	result, err := service.runtime.HandleAction(ctx, gameruntime.ActionCommand{
		SessionID: sessionID, ActorUserID: actor, ActionID: gameSDK.ActionID(operationID.Value()),
		ExpectedStateVersion: request.Msg.GetExpectedStateVersion(), OwnershipEpoch: authorized.session.Snapshot().OwnershipEpoch,
		VersionKey: versionKey, Command: command, RequestDigest: requestDigest,
	})
	if err != nil {
		return nil, err
	}
	if err := service.publish(ctx, result.Session); err != nil {
		// The durable receipt makes this response safely retryable when Redis fanout is unavailable.
		return nil, err
	}
	receipt := actionReceiptWire(result.Receipt, result.Replayed)
	projection := projectionWire(
		result.Session, authorized.viewer.Kind, result.Projection,
		authorized.room.Snapshot().HostUserID == actor,
	)
	return connect.NewResponse(&gamev1.GameActionResponse{
		SessionId: sessionID.String(), StateVersion: receipt.GetStateVersion(), ResultCode: receipt.GetResultCode(),
		ResultDigest: append([]byte(nil), receipt.GetResultDigest()...), Projection: projection,
		Replayed: result.Replayed, Receipt: receipt,
	}), nil
}

// GetProjection returns only a current active-session projection for the caller's current room role.
func (service *Service) GetProjection(ctx context.Context, request *connect.Request[gamev1.GetProjectionRequest]) (*connect.Response[gamev1.GetProjectionResponse], error) {
	actor, err := service.authenticate(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	roomID, sessionID, err := parseRoomSession(request.Msg.GetRoomId(), request.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	authorized, err := service.authorizeLive(ctx, actor, roomID, sessionID, request.Msg.GetViewerKind())
	if err != nil {
		return nil, err
	}
	projectedSession, projection, err := service.runtime.ProjectCurrent(ctx, sessionID, authorized.viewer)
	if err != nil {
		return nil, err
	}
	if projectedSession.Snapshot().RoomID != roomID {
		return nil, gameruntime.ErrGameSessionIntegrity
	}
	return connect.NewResponse(&gamev1.GetProjectionResponse{
		Projection: projectionWire(
			projectedSession, authorized.viewer.Kind, projection,
			authorized.room.Snapshot().HostUserID == actor,
		),
		Session: sessionWire(projectedSession),
	}), nil
}

// GetReplayProjection resolves persisted resource authorization before delegating field disclosure to the module.
func (service *Service) GetReplayProjection(ctx context.Context, request *connect.Request[gamev1.GetReplayProjectionRequest]) (*connect.Response[gamev1.GetReplayProjectionResponse], error) {
	actor, err := service.authenticate(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil || request.Msg.GetViewerKind() != gamev1.ViewerKind_VIEWER_KIND_REPLAY {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	roomID, sessionID, err := parseRoomSession(request.Msg.GetRoomId(), request.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	authorized, policy, err := service.authorizeReplay(ctx, actor, roomID, sessionID)
	if err != nil {
		return nil, err
	}
	stateVersion := authorized.session.Snapshot().State.StateVersion
	if request.Msg.GetThroughStateVersion() != 0 && request.Msg.GetThroughStateVersion() != stateVersion {
		return nil, gameruntime.ErrReplayUnavailable
	}
	projectedSession, projection, err := service.runtime.ProjectReplayCurrent(ctx, sessionID, authorized.viewer, policy)
	if err != nil {
		return nil, err
	}
	if projectedSession.Snapshot().RoomID != roomID {
		return nil, gameruntime.ErrGameSessionIntegrity
	}
	return connect.NewResponse(&gamev1.GetReplayProjectionResponse{
		Projection: projectionWire(projectedSession, authorized.viewer.Kind, projection, false),
		Session:    sessionWire(projectedSession), Complete: true,
	}), nil
}

// GetReplayAccess returns one terminal session's host-controlled resource policy to the current room host.
func (service *Service) GetReplayAccess(ctx context.Context, request *connect.Request[gamev1.GetReplayAccessRequest]) (*connect.Response[gamev1.GetReplayAccessResponse], error) {
	actor, err := service.authenticate(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil {
		return nil, replay.ErrInvalidInput
	}
	roomID, sessionID, err := parseRoomSession(request.Msg.GetRoomId(), request.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	access, err := service.replays.Get(ctx, actor, roomID, sessionID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&gamev1.GetReplayAccessResponse{Access: replayAccessWire(access)}), nil
}

// SetReplayAccess updates one finished session's policy under Origin, CSRF, host, and policy-version authority.
func (service *Service) SetReplayAccess(ctx context.Context, request *connect.Request[gamev1.SetReplayAccessRequest]) (*connect.Response[gamev1.SetReplayAccessResponse], error) {
	actor, _, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil || request.Msg.GetExpectedPolicyVersion() == 0 {
		return nil, replay.ErrInvalidInput
	}
	roomID, sessionID, err := parseRoomSession(request.Msg.GetRoomId(), request.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	policy := replayPolicyDomain(request.Msg.GetPolicy())
	if !policy.Valid() {
		return nil, replay.ErrInvalidInput
	}
	access, err := service.replays.SetPolicy(ctx, replay.SetPolicyCommand{
		ActorUserID: actor, RoomID: roomID, SessionID: sessionID, Policy: policy,
		ExpectedVersion: request.Msg.GetExpectedPolicyVersion(), UpdatedAt: service.clock.Now().Round(0).UTC(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&gamev1.SetReplayAccessResponse{Access: replayAccessWire(access)}), nil
}

// FinishSession executes the module's host finish command under current PartyRoom host and exact-version authority.
func (service *Service) FinishSession(ctx context.Context, request *connect.Request[gamev1.FinishSessionRequest]) (*connect.Response[gamev1.FinishSessionResponse], error) {
	actor, _, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil || request.Msg.GetExpectedRoomVersion() == 0 ||
		request.Msg.GetExpectedMembershipVersion() == 0 || request.Msg.GetExpectedStateVersion() == 0 {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	roomID, sessionID, err := parseRoomSession(request.Msg.GetRoomId(), request.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	operationID, err := parseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, err
	}
	sourceEventID, err := parseUUID(request.Msg.GetSourceEventId())
	if err != nil {
		return nil, err
	}
	requestDigest, err := parseRequestDigest(request.Msg.GetRequestDigest())
	if err != nil {
		return nil, err
	}
	versionKey, command, err := envelopeMessage(request.Msg.GetCommand())
	if err != nil || command.MessageType != finishAction {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	authorized, err := service.authorizeLive(ctx, actor, roomID, sessionID, gamev1.ViewerKind_VIEWER_KIND_PLAYER)
	if err != nil {
		return nil, err
	}
	roomSnapshot, sessionSnapshot := authorized.room.Snapshot(), authorized.session.Snapshot()
	if roomSnapshot.HostUserID != actor {
		return nil, roomDomain.ErrHostRequired
	}
	if roomSnapshot.RoomVersion != request.Msg.GetExpectedRoomVersion() ||
		roomSnapshot.MembershipVersion != request.Msg.GetExpectedMembershipVersion() {
		return nil, roomDomain.ErrRoomVersionConflict
	}
	if sessionSnapshot.VersionKey != versionKey {
		return nil, gameruntime.ErrStateVersionConflict
	}
	result, err := service.runtime.HandleSystem(ctx, gameruntime.SystemCommand{
		SessionID: sessionID, OperationID: operationID,
		Source: gameruntime.SystemSource{
			Kind: gameruntime.SystemSourceHostAPI, EventID: sourceEventID, RequestedByUserID: actor,
		},
		ExpectedStateVersion: request.Msg.GetExpectedStateVersion(), OwnershipEpoch: sessionSnapshot.OwnershipEpoch,
		VersionKey: versionKey, Message: command, RequestDigest: requestDigest,
	})
	if err != nil {
		return nil, err
	}
	if err := service.publish(ctx, result.Session); err != nil {
		return nil, err
	}
	projectedSession, projection, err := service.runtime.ProjectCurrent(ctx, sessionID, authorized.viewer)
	if err != nil {
		return nil, err
	}
	if projectedSession.Snapshot().State.StateVersion != result.Session.Snapshot().State.StateVersion {
		return nil, gameruntime.ErrStateVersionConflict
	}
	receipt := systemReceiptWire(result.Receipt, result.Replayed)
	return connect.NewResponse(&gamev1.FinishSessionResponse{
		Session: sessionWire(result.Session), Receipt: receipt,
		Projection: projectionWire(projectedSession, authorized.viewer.Kind, projection, false), Replayed: result.Replayed,
	}), nil
}

// OpenSubscription exchanges browser write authorization for a short-lived, one-time WebSocket grant.
func (service *Service) OpenSubscription(ctx context.Context, request *connect.Request[gamev1.OpenSubscriptionRequest]) (*connect.Response[gamev1.OpenSubscriptionResponse], error) {
	actor, requestOrigin, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil || request.Msg.GetLastEventOrdinal() != 0 {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	roomID, sessionID, err := parseRoomSession(request.Msg.GetRoomId(), request.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	authorized, err := service.authorizeLive(ctx, actor, roomID, sessionID, request.Msg.GetViewerKind())
	if err != nil {
		return nil, err
	}
	if request.Msg.GetLastStateVersion() > authorized.session.Snapshot().State.StateVersion {
		return nil, gameruntime.ErrStateVersionConflict
	}
	projectedSession, projection, err := service.runtime.ProjectCurrent(ctx, sessionID, authorized.viewer)
	if err != nil {
		return nil, err
	}
	stateVersion := projectedSession.Snapshot().State.StateVersion
	expiresAt := service.clock.Now().Round(0).UTC().Add(service.ticketTTL)
	grant := &gamev1.SubscriptionGrant{
		UserId: actor.String(), RoomId: roomID.String(), SessionId: sessionID.String(),
		ViewerKind: viewerKindWire(authorized.viewer.Kind), SeatIndex: authorized.viewer.SeatIndex,
		LastStateVersion: stateVersion, LastEventOrdinal: 0,
		Origin: requestOrigin, ExpiresAt: timestamppb.New(expiresAt),
	}
	grantBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(grant)
	if err != nil {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	ticket, err := service.tickets.IssueConnectionTicket(ctx, grantBytes)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&gamev1.OpenSubscriptionResponse{
		Ticket: []byte(ticket), Grant: grantBytes, ExpiresAt: timestamppb.New(expiresAt),
		Session: sessionWire(projectedSession),
		Projection: projectionWire(
			projectedSession, authorized.viewer.Kind, projection,
			authorized.room.Snapshot().HostUserID == actor,
		),
	}), nil
}

type authorizedViewer struct {
	room    roomDomain.Room
	session gameruntime.Session
	viewer  gameSDK.Viewer
}

func (service *Service) authorizeLive(
	ctx context.Context,
	actor, roomID, sessionID uuid.UUID,
	requested gamev1.ViewerKind,
) (authorizedViewer, error) {
	session, room, err := service.loadRoomSession(ctx, roomID, sessionID)
	if err != nil {
		return authorizedViewer{}, err
	}
	var requestedViewer gameSDK.ViewerKind
	switch requested {
	case gamev1.ViewerKind_VIEWER_KIND_PLAYER:
		requestedViewer = gameSDK.ViewerPlayer
	case gamev1.ViewerKind_VIEWER_KIND_SPECTATOR:
		requestedViewer = gameSDK.ViewerSpectator
	default:
		return authorizedViewer{}, gameruntime.ErrInvalidSessionInput
	}
	authorization, err := gameruntime.AuthorizeLiveViewer(room, session, actor, requestedViewer)
	if err != nil {
		return authorizedViewer{}, err
	}
	return authorizedViewer{room: room, session: session, viewer: authorization.Viewer}, nil
}

func (service *Service) authorizeReplay(
	ctx context.Context,
	actor, roomID, sessionID uuid.UUID,
) (authorizedViewer, gameSDK.ReplayAccessPolicy, error) {
	session, room, err := service.loadRoomSession(ctx, roomID, sessionID)
	if err != nil {
		return authorizedViewer{}, "", err
	}
	if session.Snapshot().Status != gameruntime.StatusFinished {
		return authorizedViewer{}, "", gameruntime.ErrReplayUnavailable
	}
	policy, err := service.replays.Authorize(ctx, actor, roomID, sessionID)
	if err != nil {
		return authorizedViewer{}, "", err
	}
	viewer := gameSDK.Viewer{Kind: gameSDK.ViewerReplay, UserID: gameSDK.Identifier(actor.String())}
	return authorizedViewer{room: room, session: session, viewer: viewer}, policy, nil
}

func replayPolicyDomain(value gamev1.ReplayAccessPolicy) replay.Policy {
	switch value {
	case gamev1.ReplayAccessPolicy_REPLAY_ACCESS_POLICY_PARTICIPANT:
		return replay.PolicyParticipant
	case gamev1.ReplayAccessPolicy_REPLAY_ACCESS_POLICY_ROOM_MEMBER:
		return replay.PolicyRoomMember
	case gamev1.ReplayAccessPolicy_REPLAY_ACCESS_POLICY_PUBLIC:
		return replay.PolicyPublic
	default:
		return ""
	}
}

func replayPolicyWire(value replay.Policy) gamev1.ReplayAccessPolicy {
	switch value {
	case replay.PolicyParticipant:
		return gamev1.ReplayAccessPolicy_REPLAY_ACCESS_POLICY_PARTICIPANT
	case replay.PolicyRoomMember:
		return gamev1.ReplayAccessPolicy_REPLAY_ACCESS_POLICY_ROOM_MEMBER
	case replay.PolicyPublic:
		return gamev1.ReplayAccessPolicy_REPLAY_ACCESS_POLICY_PUBLIC
	default:
		return gamev1.ReplayAccessPolicy_REPLAY_ACCESS_POLICY_UNSPECIFIED
	}
}

func replayAccessWire(value replay.Access) *gamev1.ReplayAccess {
	wire := &gamev1.ReplayAccess{
		SessionId: value.SessionID.String(), RoomId: value.RoomID.String(), Policy: replayPolicyWire(value.Policy),
		PolicyVersion: value.Version, UpdatedAt: timestamppb.New(value.UpdatedAt),
	}
	if !value.MemberSnapshotCompletedAt.IsZero() {
		wire.MemberSnapshotCompletedAt = timestamppb.New(value.MemberSnapshotCompletedAt)
	}
	return wire
}

func (service *Service) loadRoomSession(
	ctx context.Context,
	roomID, sessionID uuid.UUID,
) (gameruntime.Session, roomDomain.Room, error) {
	session, err := service.sessions.Get(ctx, sessionID)
	if err != nil {
		return gameruntime.Session{}, roomDomain.Room{}, err
	}
	if session.Snapshot().RoomID != roomID {
		return gameruntime.Session{}, roomDomain.Room{}, gameruntime.ErrParticipantNotActive
	}
	room, err := service.rooms.GetByID(ctx, roomID)
	if err != nil {
		return gameruntime.Session{}, roomDomain.Room{}, err
	}
	return session, room, nil
}

func playerViewer(session gameruntime.Session, actor uuid.UUID) (gameSDK.Viewer, error) {
	for _, participant := range session.Snapshot().Participants {
		if participant.UserID == actor {
			return gameSDK.Viewer{
				Kind: gameSDK.ViewerPlayer, UserID: gameSDK.Identifier(actor.String()), SeatIndex: participant.SeatIndex,
			}, nil
		}
	}
	return gameSDK.Viewer{}, gameruntime.ErrParticipantNotActive
}

func parseRoomSession(roomValue, sessionValue string) (uuid.UUID, uuid.UUID, error) {
	roomID, err := parseUUID(roomValue)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	sessionID, err := parseUUID(sessionValue)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return roomID, sessionID, nil
}

func (service *Service) publish(ctx context.Context, session gameruntime.Session) error {
	snapshot := session.Snapshot()
	return service.fanout.PublishSessionFanout(ctx, redisstore.SessionFanoutEvent{
		SessionID: snapshot.ID, StateVersion: snapshot.State.StateVersion,
	})
}

func (service *Service) authenticate(ctx context.Context, request *http.Request) (uuid.UUID, error) {
	credentials, err := cookies.ReadUserDevice(request)
	if err != nil {
		return uuid.Nil, identityDomain.ErrDeviceAuthentication
	}
	return service.authenticator.Authenticate(ctx, credentials.CookieToken(), credentials.CSRFToken())
}

func (service *Service) authenticateWrite(ctx context.Context, request *http.Request) (uuid.UUID, string, error) {
	validatedOrigin, err := service.origins.Validate(request)
	if err != nil {
		return uuid.Nil, "", err
	}
	if _, err := service.csrf.Validate(request); err != nil {
		return uuid.Nil, "", err
	}
	actor, err := service.authenticate(ctx, request)
	if err != nil {
		return uuid.Nil, "", err
	}
	return actor, validatedOrigin.Canonical(), nil
}

func requestHTTP[T any](request *connect.Request[T]) *http.Request {
	if request == nil {
		return nil
	}
	return &http.Request{Header: request.Header(), RemoteAddr: request.Peer().Addr}
}

var (
	_ gamev1connect.GameServiceHandler = (*Service)(nil)
	_ Runtime                          = (*gameruntime.Service)(nil)
)
