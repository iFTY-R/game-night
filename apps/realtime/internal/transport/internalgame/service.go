package internalgame

import (
	"context"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/apps/realtime/internal/owner"
	gamev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1"
	realtimev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/realtime/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/realtime/v1/realtimev1connect"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/idempotency"
	redisstore "github.com/iFTY-R/game-night/platform/persistence/redis"
	roomdomain "github.com/iFTY-R/game-night/platform/room"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

// Runtime is the complete authoritative service surface used behind the private owner protocol.
type Runtime interface {
	Start(context.Context, gameruntime.StartCommand) (roomdomain.Room, gameruntime.Session, error)
	ProjectCurrent(context.Context, uuid.UUID, game.Viewer) (gameruntime.Session, game.Projection, error)
	ProjectEventsCurrent(context.Context, uuid.UUID, uint64, game.Viewer) (gameruntime.Session, game.EventProjection, game.Projection, bool, error)
	ProjectReplayCurrent(context.Context, uuid.UUID, game.Viewer, game.ReplayAccessPolicy) (gameruntime.Session, game.Projection, error)
}

// Ownership owns Redis readiness, PostgreSQL fencing, and command execution for this process.
type Ownership interface {
	EnsureOwned(context.Context, uuid.UUID) (uint64, error)
	HandleAction(context.Context, gameruntime.ActionCommand) (gameruntime.ActionResult, error)
	HandleTimer(context.Context, gameruntime.DueTimer) (gameruntime.TimerCommitResult, error)
	HandleSystem(context.Context, gameruntime.SystemCommand) (gameruntime.SystemCommitResult, error)
}

// SessionReader reloads the post-claim epoch and projection metadata from PostgreSQL.
type SessionReader interface {
	Get(context.Context, uuid.UUID) (gameruntime.Session, error)
}

// RouteResolver exposes only ready Redis routes; the API still validates addresses against its static peer allowlist.
type RouteResolver interface {
	LookupSessionLease(context.Context, uuid.UUID) (redisstore.SessionLease, error)
}

// Service implements the private protocol; it must never be mounted on the public realtime listener.
type Service struct {
	runtime   Runtime
	ownership Ownership
	sessions  SessionReader
	routes    RouteResolver
}

// NewService requires independent runtime, ownership, and read authority before exposing private RPCs.
func NewService(runtime Runtime, ownership Ownership, sessions SessionReader, routes RouteResolver) (*Service, error) {
	if runtime == nil || ownership == nil || sessions == nil || routes == nil {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	return &Service{runtime: runtime, ownership: ownership, sessions: sessions, routes: routes}, nil
}

func (service *Service) ResolveOwner(ctx context.Context, request *connect.Request[realtimev1.ResolveOwnerRequest]) (*connect.Response[realtimev1.ResolveOwnerResponse], error) {
	if request == nil || request.Msg == nil {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	sessionID, err := requiredUUID(request.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	lease, err := service.routes.LookupSessionLease(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if !lease.Routable() && request.Msg.GetAllowOwnerAcquire() {
		if _, err := service.ownership.EnsureOwned(ctx, sessionID); err != nil {
			return nil, err
		}
		lease, err = service.routes.LookupSessionLease(ctx, sessionID)
		if err != nil {
			return nil, err
		}
	}
	if !lease.Routable() {
		return nil, owner.ErrOwnershipUnavailable
	}
	return connect.NewResponse(&realtimev1.ResolveOwnerResponse{
		InstanceId: lease.Owner, Address: lease.Address, OwnershipEpoch: lease.OwnershipEpoch,
	}), nil
}

func (service *Service) StartSession(ctx context.Context, request *connect.Request[realtimev1.StartSessionRequest]) (*connect.Response[realtimev1.StartSessionResponse], error) {
	if request == nil || request.Msg == nil || request.Msg.GetExpectedRoomVersion() == 0 || request.Msg.GetExpectedMembershipVersion() == 0 {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	actorUserID, err := requiredUUID(request.Msg.GetActorUserId())
	if err != nil {
		return nil, err
	}
	roomID, err := requiredUUID(request.Msg.GetRoomId())
	if err != nil {
		return nil, err
	}
	gameID, err := game.ParseGameID(request.Msg.GetGameId())
	if err != nil {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	operationID, requestDigest, err := operationBinding(request.Msg.GetOperationId(), request.Msg.GetRequestDigest())
	if err != nil {
		return nil, err
	}
	config, err := configFromWire(request.Msg.GetConfig(), gameID)
	if err != nil {
		return nil, err
	}
	room, session, err := service.runtime.Start(ctx, gameruntime.StartCommand{
		ActorUserID: actorUserID, RoomID: roomID, GameID: gameID,
		Expected: roomdomain.Version{Room: request.Msg.GetExpectedRoomVersion(), Membership: request.Msg.GetExpectedMembershipVersion()},
		Config:   config, OperationID: operationID, RequestDigest: &requestDigest,
	})
	if err != nil {
		return nil, err
	}
	if _, err := service.ownership.EnsureOwned(ctx, session.Snapshot().ID); err != nil {
		return nil, err
	}
	session, err = service.sessions.Get(ctx, session.Snapshot().ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&realtimev1.StartSessionResponse{Room: roomToWire(room), Session: sessionToWire(session)}), nil
}

func (service *Service) GameAction(ctx context.Context, request *connect.Request[realtimev1.GameActionRequest]) (*connect.Response[realtimev1.GameActionResponse], error) {
	if request == nil || request.Msg == nil || request.Msg.GetExpectedStateVersion() == 0 {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	sessionID, actorUserID, err := requiredPair(request.Msg.GetSessionId(), request.Msg.GetActorUserId())
	if err != nil {
		return nil, err
	}
	actionID, requestDigest, err := operationBinding(request.Msg.GetActionId(), request.Msg.GetRequestDigest())
	if err != nil {
		return nil, err
	}
	version, command, err := envelopeFromWire(request.Msg.GetCommand())
	if err != nil {
		return nil, err
	}
	if request.Msg.GetAllowOwnerAcquire() {
		if _, err := service.ownership.EnsureOwned(ctx, sessionID); err != nil {
			return nil, err
		}
	}
	result, err := service.ownership.HandleAction(ctx, gameruntime.ActionCommand{
		SessionID: sessionID, ActorUserID: actorUserID, ActionID: game.ActionID(actionID.Value()),
		ExpectedStateVersion: request.Msg.GetExpectedStateVersion(), VersionKey: version,
		Command: command, RequestDigest: &requestDigest,
	})
	if err != nil {
		return nil, err
	}
	viewer, err := playerViewer(result.Session, actorUserID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&realtimev1.GameActionResponse{
		Session: sessionToWire(result.Session), Receipt: actionReceiptToWire(result.Receipt),
		Projection: projectionToWire(result.Session, viewer, result.Projection), Replayed: result.Replayed,
	}), nil
}

func (service *Service) GameSystem(ctx context.Context, request *connect.Request[realtimev1.GameSystemRequest]) (*connect.Response[realtimev1.GameSystemResponse], error) {
	if request == nil || request.Msg == nil || request.Msg.GetExpectedStateVersion() == 0 {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	sessionID, err := requiredUUID(request.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	operationID, requestDigest, err := operationBinding(request.Msg.GetOperationId(), request.Msg.GetRequestDigest())
	if err != nil {
		return nil, err
	}
	sourceEventID, err := requiredUUID(request.Msg.GetSourceEventId())
	if err != nil {
		return nil, err
	}
	requestedByUserID, err := optionalUUIDFromWire(request.Msg.GetRequestedByUserId())
	if err != nil {
		return nil, err
	}
	version, command, err := envelopeFromWire(request.Msg.GetCommand())
	if err != nil {
		return nil, err
	}
	if request.Msg.GetAllowOwnerAcquire() {
		if _, err := service.ownership.EnsureOwned(ctx, sessionID); err != nil {
			return nil, err
		}
	}
	result, err := service.ownership.HandleSystem(ctx, gameruntime.SystemCommand{
		SessionID: sessionID, OperationID: operationID,
		Source: gameruntime.SystemSource{
			Kind: game.Identifier(request.Msg.GetSourceKind()), EventID: sourceEventID, RequestedByUserID: requestedByUserID,
		},
		ExpectedStateVersion: request.Msg.GetExpectedStateVersion(), VersionKey: version, Message: command, RequestDigest: &requestDigest,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&realtimev1.GameSystemResponse{
		Session: sessionToWire(result.Session), Receipt: systemReceiptToWire(result.Receipt),
		Replayed: result.Replayed, Retry: result.Retry,
	}), nil
}

func (service *Service) GameTimer(ctx context.Context, request *connect.Request[realtimev1.GameTimerRequest]) (*connect.Response[realtimev1.GameTimerResponse], error) {
	if request == nil || request.Msg == nil || request.Msg.GetExpectedStateVersion() == 0 {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	sessionID, err := requiredUUID(request.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	timerID, err := game.ParseIdentifier(request.Msg.GetTimerId())
	if err != nil {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	dueAt, err := requiredTime(request.Msg.GetDueAt())
	if err != nil {
		return nil, err
	}
	_, timer, err := envelopeFromWire(request.Msg.GetTimer())
	if err != nil {
		return nil, err
	}
	if request.Msg.GetAllowOwnerAcquire() {
		if _, err := service.ownership.EnsureOwned(ctx, sessionID); err != nil {
			return nil, err
		}
	}
	result, err := service.ownership.HandleTimer(ctx, gameruntime.DueTimer{
		SessionID: sessionID, TimerID: timerID, ExpectedStateVersion: request.Msg.GetExpectedStateVersion(),
		DueAt: dueAt, Message: timer,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&realtimev1.GameTimerResponse{
		Session: sessionToWire(result.Session), Receipt: timerReceiptToWire(result.Receipt), Replayed: result.Replayed,
	}), nil
}

func (service *Service) GetProjection(ctx context.Context, request *connect.Request[realtimev1.GetProjectionRequest]) (*connect.Response[realtimev1.GetProjectionResponse], error) {
	sessionID, viewer, err := projectionRequest(request)
	if err != nil {
		return nil, err
	}
	session, projection, err := service.runtime.ProjectCurrent(ctx, sessionID, viewer)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&realtimev1.GetProjectionResponse{
		Session: sessionToWire(session), Projection: projectionToWire(session, viewer, projection),
	}), nil
}

func (service *Service) GetEventProjection(ctx context.Context, request *connect.Request[realtimev1.GetEventProjectionRequest]) (*connect.Response[realtimev1.GetEventProjectionResponse], error) {
	if request == nil || request.Msg == nil {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	sessionID, err := requiredUUID(request.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	viewer, err := viewerFromWire(request.Msg.GetViewer())
	if err != nil {
		return nil, err
	}
	session, delta, snapshot, fallback, err := service.runtime.ProjectEventsCurrent(ctx, sessionID, request.Msg.GetAfterStateVersion(), viewer)
	if err != nil {
		return nil, err
	}
	messages := make([]*gamev1.GameEnvelope, 0, len(delta.Messages))
	for _, message := range delta.Messages {
		messages = append(messages, envelopeToWire(session.Snapshot().VersionKey, message))
	}
	response := &realtimev1.GetEventProjectionResponse{
		Session: sessionToWire(session), Messages: messages, UsedSnapshotFallback: fallback,
	}
	if fallback {
		response.SnapshotFallback = projectionToWire(session, viewer, snapshot)
	}
	return connect.NewResponse(response), nil
}

func (service *Service) GetReplayProjection(ctx context.Context, request *connect.Request[realtimev1.GetReplayProjectionRequest]) (*connect.Response[realtimev1.GetReplayProjectionResponse], error) {
	if request == nil || request.Msg == nil {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	sessionID, err := requiredUUID(request.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	viewer, err := viewerFromWire(request.Msg.GetViewer())
	if err != nil {
		return nil, err
	}
	session, projection, err := service.runtime.ProjectReplayCurrent(ctx, sessionID, viewer, game.ReplayAccessPolicy(request.Msg.GetAccessPolicy()))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&realtimev1.GetReplayProjectionResponse{
		Session: sessionToWire(session), Projection: projectionToWire(session, viewer, projection),
	}), nil
}

func operationBinding(operationRaw string, digestRaw []byte) (idempotency.OperationID, idempotency.Digest, error) {
	operationID, err := idempotency.ParseOperationID(operationRaw)
	if err != nil {
		return idempotency.OperationID{}, idempotency.Digest{}, gameruntime.ErrInvalidSessionInput
	}
	digest, err := idempotency.NewDigest(digestRaw)
	if err != nil {
		return idempotency.OperationID{}, idempotency.Digest{}, gameruntime.ErrInvalidSessionInput
	}
	return operationID, digest, nil
}

func configFromWire(value *gamev1.GameConfig, expected game.GameID) (game.Message, error) {
	if value == nil || value.GetGameId() != string(expected) {
		return game.Message{}, gameruntime.ErrInvalidSessionInput
	}
	message := game.Message{
		MessageType: game.Identifier(value.GetMessageType()), SchemaVersion: value.GetSchemaVersion(),
		Payload: append([]byte(nil), value.GetPayload()...),
	}
	if !message.Valid() {
		return game.Message{}, gameruntime.ErrInvalidSessionInput
	}
	return message, nil
}

func requiredPair(first, second string) (uuid.UUID, uuid.UUID, error) {
	firstID, err := requiredUUID(first)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	secondID, err := requiredUUID(second)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return firstID, secondID, nil
}

func playerViewer(session gameruntime.Session, actorUserID uuid.UUID) (game.Viewer, error) {
	for _, participant := range session.Snapshot().Participants {
		if participant.UserID == actorUserID {
			return game.Viewer{Kind: game.ViewerPlayer, UserID: game.Identifier(actorUserID.String()), SeatIndex: participant.SeatIndex}, nil
		}
	}
	return game.Viewer{}, gameruntime.ErrParticipantNotActive
}

func projectionRequest(request *connect.Request[realtimev1.GetProjectionRequest]) (uuid.UUID, game.Viewer, error) {
	if request == nil || request.Msg == nil {
		return uuid.Nil, game.Viewer{}, gameruntime.ErrInvalidSessionInput
	}
	sessionID, err := requiredUUID(request.Msg.GetSessionId())
	if err != nil {
		return uuid.Nil, game.Viewer{}, err
	}
	viewer, err := viewerFromWire(request.Msg.GetViewer())
	return sessionID, viewer, err
}

var _ realtimev1connect.OwnerServiceHandler = (*Service)(nil)
