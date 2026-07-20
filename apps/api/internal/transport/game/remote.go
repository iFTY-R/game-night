package game

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	commonv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/common/v1"
	gamev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1"
	realtimev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/realtime/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/realtime/v1/realtimev1connect"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/idempotency"
	redisstore "github.com/iFTY-R/game-night/platform/persistence/redis"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
	gameSDK "github.com/iFTY-R/game-night/sdk/go/game"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const internalTokenHeader = "X-Game-Night-Internal-Token"

// RemoteRuntimeConfig prevents Redis-controlled routes from escaping an operator-owned peer set.
type RemoteRuntimeConfig struct {
	BootstrapURL  string
	PeerURLs      []string
	InternalToken string
}

// RemoteRuntime adapts the private OwnerService to the user transport's domain-facing runtime port.
type RemoteRuntime struct {
	httpClient    *http.Client
	bootstrapURL  string
	peerURLs      []string
	allowedPeers  map[string]struct{}
	internalToken string

	mu      sync.Mutex
	clients map[string]realtimev1connect.OwnerServiceClient
}

// NewRemoteRuntime validates static routes without opening a connection.
func NewRemoteRuntime(httpClient *http.Client, config RemoteRuntimeConfig) (*RemoteRuntime, error) {
	if httpClient == nil || config.BootstrapURL == "" || len(config.PeerURLs) == 0 ||
		len(config.InternalToken) < 32 || len(config.InternalToken) > 256 {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	allowed := make(map[string]struct{}, len(config.PeerURLs))
	peers := make([]string, 0, len(config.PeerURLs))
	for _, peer := range config.PeerURLs {
		if peer == "" {
			return nil, gameruntime.ErrInvalidSessionInput
		}
		if _, exists := allowed[peer]; !exists {
			allowed[peer] = struct{}{}
			peers = append(peers, peer)
		}
	}
	if _, exists := allowed[config.BootstrapURL]; !exists {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	return &RemoteRuntime{
		httpClient: httpClient, bootstrapURL: config.BootstrapURL, peerURLs: peers,
		allowedPeers: allowed, internalToken: config.InternalToken,
		clients: make(map[string]realtimev1connect.OwnerServiceClient),
	}, nil
}

func (runtime *RemoteRuntime) Start(ctx context.Context, command gameruntime.StartCommand) (roomDomain.Room, gameruntime.Session, error) {
	if runtime == nil || ctx == nil || command.RequestDigest == nil {
		return roomDomain.Room{}, gameruntime.Session{}, gameruntime.ErrInvalidSessionInput
	}
	requestMessage := &realtimev1.StartSessionRequest{
		ActorUserId: command.ActorUserID.String(), RoomId: command.RoomID.String(), GameId: string(command.GameID),
		ExpectedRoomVersion: command.Expected.Room, ExpectedMembershipVersion: command.Expected.Membership,
		Config: &gamev1.GameConfig{
			GameId: string(command.GameID), SchemaVersion: command.Config.SchemaVersion,
			MessageType: string(command.Config.MessageType), Payload: append([]byte(nil), command.Config.Payload...),
		},
		OperationId: command.OperationID.Value(), RequestDigest: command.RequestDigest.Bytes(),
	}
	var lastErr error
	for _, peer := range runtime.peerURLs {
		request := connect.NewRequest(requestMessage)
		runtime.authorize(request)
		response, err := runtime.client(peer).StartSession(ctx, request)
		if err != nil {
			lastErr = mapRemoteError(err)
			if retryableRemote(lastErr) {
				continue
			}
			return roomDomain.Room{}, gameruntime.Session{}, lastErr
		}
		room, err := roomFromRemote(response.Msg.GetRoom())
		if err != nil {
			return roomDomain.Room{}, gameruntime.Session{}, err
		}
		session, err := sessionFromRemote(response.Msg.GetSession())
		if err != nil || session.Snapshot().RoomID != command.RoomID {
			return roomDomain.Room{}, gameruntime.Session{}, gameruntime.ErrGameSessionIntegrity
		}
		return room, session, nil
	}
	return roomDomain.Room{}, gameruntime.Session{}, remoteUnavailable(lastErr)
}

func (runtime *RemoteRuntime) HandleAction(ctx context.Context, command gameruntime.ActionCommand) (gameruntime.ActionResult, error) {
	if runtime == nil || ctx == nil || command.RequestDigest == nil {
		return gameruntime.ActionResult{}, gameruntime.ErrInvalidSessionInput
	}
	for attempt := 0; attempt < 2; attempt++ {
		client, err := runtime.resolveOwner(ctx, command.SessionID)
		if err != nil {
			return gameruntime.ActionResult{}, err
		}
		request := connect.NewRequest(&realtimev1.GameActionRequest{
			ActorUserId: command.ActorUserID.String(), SessionId: command.SessionID.String(),
			ActionId: string(command.ActionID), ExpectedStateVersion: command.ExpectedStateVersion,
			Command: envelopeWire(command.VersionKey, command.Command), RequestDigest: command.RequestDigest.Bytes(),
		})
		runtime.authorize(request)
		response, callErr := client.GameAction(ctx, request)
		if callErr != nil {
			mapped := mapRemoteError(callErr)
			if attempt == 0 && retryableOwner(mapped) {
				continue
			}
			return gameruntime.ActionResult{}, mapped
		}
		return actionResultFromRemote(response.Msg)
	}
	return gameruntime.ActionResult{}, redisstore.ErrCoordinationUnavailable
}

func (runtime *RemoteRuntime) HandleSystem(ctx context.Context, command gameruntime.SystemCommand) (gameruntime.SystemCommitResult, error) {
	if runtime == nil || ctx == nil || command.RequestDigest == nil {
		return gameruntime.SystemCommitResult{}, gameruntime.ErrInvalidSessionInput
	}
	for attempt := 0; attempt < 2; attempt++ {
		client, err := runtime.resolveOwner(ctx, command.SessionID)
		if err != nil {
			return gameruntime.SystemCommitResult{}, err
		}
		request := connect.NewRequest(&realtimev1.GameSystemRequest{
			SessionId: command.SessionID.String(), OperationId: command.OperationID.Value(),
			SourceKind: string(command.Source.Kind), SourceEventId: command.Source.EventID.String(),
			RequestedByUserId:    optionalRemoteUUID(command.Source.RequestedByUserID),
			ExpectedStateVersion: command.ExpectedStateVersion,
			Command:              envelopeWire(command.VersionKey, command.Message), RequestDigest: command.RequestDigest.Bytes(),
		})
		runtime.authorize(request)
		response, callErr := client.GameSystem(ctx, request)
		if callErr != nil {
			mapped := mapRemoteError(callErr)
			if attempt == 0 && retryableOwner(mapped) {
				continue
			}
			return gameruntime.SystemCommitResult{}, mapped
		}
		return systemResultFromRemote(response.Msg)
	}
	return gameruntime.SystemCommitResult{}, redisstore.ErrCoordinationUnavailable
}

func (runtime *RemoteRuntime) ProjectCurrent(ctx context.Context, sessionID uuid.UUID, viewer gameSDK.Viewer) (gameruntime.Session, gameSDK.Projection, error) {
	requestMessage := &realtimev1.GetProjectionRequest{SessionId: sessionID.String(), Viewer: viewerToRemote(viewer)}
	for _, peer := range runtime.peerURLs {
		request := connect.NewRequest(requestMessage)
		runtime.authorize(request)
		response, err := runtime.client(peer).GetProjection(ctx, request)
		if err != nil {
			mapped := mapRemoteError(err)
			if retryableRemote(mapped) {
				continue
			}
			return gameruntime.Session{}, gameSDK.Projection{}, mapped
		}
		return projectionResponseFromRemote(response.Msg.GetSession(), response.Msg.GetProjection())
	}
	return gameruntime.Session{}, gameSDK.Projection{}, redisstore.ErrCoordinationUnavailable
}

func (runtime *RemoteRuntime) ProjectReplayCurrent(
	ctx context.Context,
	sessionID uuid.UUID,
	viewer gameSDK.Viewer,
	policy gameSDK.ReplayAccessPolicy,
) (gameruntime.Session, gameSDK.Projection, error) {
	requestMessage := &realtimev1.GetReplayProjectionRequest{
		SessionId: sessionID.String(), Viewer: viewerToRemote(viewer), AccessPolicy: string(policy),
	}
	for _, peer := range runtime.peerURLs {
		request := connect.NewRequest(requestMessage)
		runtime.authorize(request)
		response, err := runtime.client(peer).GetReplayProjection(ctx, request)
		if err != nil {
			mapped := mapRemoteError(err)
			if retryableRemote(mapped) {
				continue
			}
			return gameruntime.Session{}, gameSDK.Projection{}, mapped
		}
		return projectionResponseFromRemote(response.Msg.GetSession(), response.Msg.GetProjection())
	}
	return gameruntime.Session{}, gameSDK.Projection{}, redisstore.ErrCoordinationUnavailable
}

func (runtime *RemoteRuntime) resolveOwner(ctx context.Context, sessionID uuid.UUID) (realtimev1connect.OwnerServiceClient, error) {
	var lastErr error
	for _, peer := range runtime.peerURLs {
		request := connect.NewRequest(&realtimev1.ResolveOwnerRequest{SessionId: sessionID.String(), AllowOwnerAcquire: true})
		runtime.authorize(request)
		response, err := runtime.client(peer).ResolveOwner(ctx, request)
		if err != nil {
			lastErr = mapRemoteError(err)
			if retryableRemote(lastErr) {
				continue
			}
			return nil, lastErr
		}
		address := response.Msg.GetAddress()
		if _, allowed := runtime.allowedPeers[address]; !allowed || response.Msg.GetOwnershipEpoch() == 0 || response.Msg.GetInstanceId() == "" {
			return nil, redisstore.ErrCoordinationUnavailable
		}
		return runtime.client(address), nil
	}
	return nil, remoteUnavailable(lastErr)
}

func (runtime *RemoteRuntime) client(address string) realtimev1connect.OwnerServiceClient {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if client := runtime.clients[address]; client != nil {
		return client
	}
	client := realtimev1connect.NewOwnerServiceClient(runtime.httpClient, address)
	runtime.clients[address] = client
	return client
}

func (runtime *RemoteRuntime) authorize(request connect.AnyRequest) {
	request.Header().Set(internalTokenHeader, runtime.internalToken)
}

func roomFromRemote(value *realtimev1.RoomSnapshot) (roomDomain.Room, error) {
	if value == nil || value.GetCreatedAt() == nil || value.GetUpdatedAt() == nil {
		return roomDomain.Room{}, gameruntime.ErrGameSessionIntegrity
	}
	roomID, hostUserID, err := remoteUUIDPair(value.GetRoomId(), value.GetHostUserId())
	if err != nil {
		return roomDomain.Room{}, err
	}
	activeSessionID, err := optionalRemoteUUIDFromWire(value.GetActiveSessionId())
	if err != nil {
		return roomDomain.Room{}, err
	}
	members := make([]roomDomain.MemberSnapshot, 0, len(value.GetMembers()))
	for _, member := range value.GetMembers() {
		if member == nil || member.GetJoinedAt() == nil || member.GetLastSeenAt() == nil {
			return roomDomain.Room{}, gameruntime.ErrGameSessionIntegrity
		}
		userID, err := remoteUUID(member.GetUserId())
		if err != nil {
			return roomDomain.Room{}, gameruntime.ErrGameSessionIntegrity
		}
		members = append(members, roomDomain.MemberSnapshot{
			UserID: userID, Role: roomDomain.MemberRole(member.GetRole()), RequestedRole: roomDomain.MemberRole(member.GetRequestedRole()),
			SeatIndex: member.GetSeatIndex(), JoinedAt: member.GetJoinedAt().AsTime().Round(0).UTC(),
			LastSeenAt: member.GetLastSeenAt().AsTime().Round(0).UTC(),
		})
	}
	room, err := roomDomain.Restore(roomDomain.RoomSnapshot{
		ID: roomID, RoomCode: value.GetRoomCode(), Visibility: roomDomain.Visibility(value.GetVisibility()),
		Status: roomDomain.RoomStatus(value.GetStatus()), HostUserID: hostUserID,
		ParticipantCapacity: value.GetParticipantCapacity(), ParticipantAdmission: roomDomain.AdmissionMode(value.GetParticipantAdmission()),
		SpectatorAdmission: roomDomain.AdmissionMode(value.GetSpectatorAdmission()), Members: members,
		ActiveSessionID: activeSessionID, ActiveGameID: value.GetActiveGameId(),
		RoomVersion: value.GetRoomVersion(), MembershipVersion: value.GetMembershipVersion(),
		CreatedAt: value.GetCreatedAt().AsTime().Round(0).UTC(), UpdatedAt: value.GetUpdatedAt().AsTime().Round(0).UTC(),
	})
	if err != nil {
		return roomDomain.Room{}, gameruntime.ErrGameSessionIntegrity
	}
	return room, nil
}

func sessionFromRemote(value *realtimev1.SessionSnapshot) (gameruntime.Session, error) {
	if value == nil || value.GetVersion() == nil || value.GetAuthoritativeState() == nil ||
		value.GetStartedAt() == nil || value.GetUpdatedAt() == nil {
		return gameruntime.Session{}, gameruntime.ErrGameSessionIntegrity
	}
	sessionID, roomID, err := remoteUUIDPair(value.GetSessionId(), value.GetRoomId())
	if err != nil {
		return gameruntime.Session{}, err
	}
	version, state, err := envelopeFromRemote(value.GetAuthoritativeState())
	if err != nil || version.GameID != gameSDK.GameID(value.GetGameId()) || version != versionFromTuple(value.GetGameId(), value.GetVersion()) {
		return gameruntime.Session{}, gameruntime.ErrGameSessionIntegrity
	}
	participants := make([]gameruntime.Participant, 0, len(value.GetParticipants()))
	for _, participant := range value.GetParticipants() {
		if participant == nil {
			return gameruntime.Session{}, gameruntime.ErrGameSessionIntegrity
		}
		userID, err := remoteUUID(participant.GetUserId())
		if err != nil {
			return gameruntime.Session{}, err
		}
		participants = append(participants, gameruntime.Participant{UserID: userID, SeatIndex: participant.GetSeatIndex()})
	}
	timers := make([]gameruntime.TimerSnapshot, 0, len(value.GetTimers()))
	for _, timer := range value.GetTimers() {
		if timer == nil || timer.GetDueAt() == nil {
			return gameruntime.Session{}, gameruntime.ErrGameSessionIntegrity
		}
		timerVersion, message, err := envelopeFromRemote(timer.GetMessage())
		if err != nil || timerVersion != version {
			return gameruntime.Session{}, gameruntime.ErrGameSessionIntegrity
		}
		timers = append(timers, gameruntime.TimerSnapshot{
			TimerID: gameSDK.Identifier(timer.GetTimerId()), ExpectedStateVersion: timer.GetExpectedStateVersion(),
			DueAt: timer.GetDueAt().AsTime().Round(0).UTC(), Message: message,
		})
	}
	session, err := gameruntime.RestoreSession(gameruntime.SessionSnapshot{
		ID: sessionID, RoomID: roomID, VersionKey: version, OwnershipEpoch: value.GetOwnershipEpoch(),
		Participants: participants,
		State: gameSDK.Snapshot{
			SnapshotVersion: value.GetSnapshotVersion(), StateVersion: value.GetStateVersion(), State: state,
		},
		Timers: timers, NextDeadlineAt: remoteOptionalTime(value.GetNextDeadlineAt()),
		Status: statusFromRemote(value.GetStatus()), StartedAt: value.GetStartedAt().AsTime().Round(0).UTC(),
		UpdatedAt: value.GetUpdatedAt().AsTime().Round(0).UTC(), EndedAt: remoteOptionalTime(value.GetEndedAt()),
	})
	if err != nil {
		return gameruntime.Session{}, gameruntime.ErrGameSessionIntegrity
	}
	return session, nil
}

func actionResultFromRemote(value *realtimev1.GameActionResponse) (gameruntime.ActionResult, error) {
	if value == nil {
		return gameruntime.ActionResult{}, gameruntime.ErrGameSessionIntegrity
	}
	session, projection, err := projectionResponseFromRemote(value.GetSession(), value.GetProjection())
	if err != nil {
		return gameruntime.ActionResult{}, err
	}
	receipt, err := actionReceiptFromRemote(value.GetReceipt())
	if err != nil || receipt.Snapshot().StateVersion != session.Snapshot().State.StateVersion {
		return gameruntime.ActionResult{}, gameruntime.ErrGameSessionIntegrity
	}
	return gameruntime.ActionResult{Session: session, Receipt: receipt, Projection: projection, Replayed: value.GetReplayed()}, nil
}

func systemResultFromRemote(value *realtimev1.GameSystemResponse) (gameruntime.SystemCommitResult, error) {
	if value == nil {
		return gameruntime.SystemCommitResult{}, gameruntime.ErrGameSessionIntegrity
	}
	session, err := sessionFromRemote(value.GetSession())
	if err != nil {
		return gameruntime.SystemCommitResult{}, err
	}
	receipt, err := systemReceiptFromRemote(value.GetReceipt())
	if err != nil || receipt.Snapshot().StateVersion != session.Snapshot().State.StateVersion {
		return gameruntime.SystemCommitResult{}, gameruntime.ErrGameSessionIntegrity
	}
	return gameruntime.SystemCommitResult{Session: session, Receipt: receipt, Replayed: value.GetReplayed(), Retry: value.GetRetry()}, nil
}

func projectionResponseFromRemote(sessionValue *realtimev1.SessionSnapshot, projectionValue *gamev1.GameProjection) (gameruntime.Session, gameSDK.Projection, error) {
	session, err := sessionFromRemote(sessionValue)
	if err != nil {
		return gameruntime.Session{}, gameSDK.Projection{}, err
	}
	if projectionValue == nil || projectionValue.GetSessionId() != session.Snapshot().ID.String() ||
		projectionValue.GetStateVersion() != session.Snapshot().State.StateVersion {
		return gameruntime.Session{}, gameSDK.Projection{}, gameruntime.ErrGameSessionIntegrity
	}
	version, view, err := envelopeFromRemote(projectionValue.GetView())
	if err != nil || version != session.Snapshot().VersionKey {
		return gameruntime.Session{}, gameSDK.Projection{}, gameruntime.ErrGameSessionIntegrity
	}
	actions := make([]gameSDK.Identifier, 0, len(projectionValue.GetAllowedActions()))
	for _, raw := range projectionValue.GetAllowedActions() {
		action, err := gameSDK.ParseIdentifier(raw)
		if err != nil {
			return gameruntime.Session{}, gameSDK.Projection{}, gameruntime.ErrGameSessionIntegrity
		}
		actions = append(actions, action)
	}
	projection := gameSDK.Projection{View: view, AllowedActions: actions}
	if !projection.Valid() {
		return gameruntime.Session{}, gameSDK.Projection{}, gameruntime.ErrProjectionUnsafe
	}
	return session, projection, nil
}

func actionReceiptFromRemote(value *realtimev1.ActionReceipt) (gameruntime.ActionReceipt, error) {
	if value == nil || value.GetCommittedAt() == nil {
		return gameruntime.ActionReceipt{}, gameruntime.ErrGameSessionIntegrity
	}
	sessionID, actorUserID, err := remoteUUIDPair(value.GetSessionId(), value.GetActorUserId())
	if err != nil {
		return gameruntime.ActionReceipt{}, err
	}
	operationID, requestDigest, resultDigest, err := remoteReceiptFields(
		value.GetActionId(), value.GetRequestDigest(), value.GetResultDigest(),
	)
	if err != nil {
		return gameruntime.ActionReceipt{}, err
	}
	return gameruntime.NewActionReceipt(gameruntime.ActionReceiptSnapshot{
		Key:           gameruntime.ActionKey{SessionID: sessionID, ActorUserID: actorUserID, ActionID: operationID},
		RequestDigest: requestDigest, ResultCode: gameruntime.ResultCode(value.GetResultCode()), ResultDigest: resultDigest,
		StateVersion: value.GetStateVersion(), CommittedAt: value.GetCommittedAt().AsTime().Round(0).UTC(),
	})
}

func systemReceiptFromRemote(value *realtimev1.SystemReceipt) (gameruntime.SystemReceipt, error) {
	if value == nil || value.GetCommittedAt() == nil {
		return gameruntime.SystemReceipt{}, gameruntime.ErrGameSessionIntegrity
	}
	sessionID, err := remoteUUID(value.GetSessionId())
	if err != nil {
		return gameruntime.SystemReceipt{}, err
	}
	operationID, requestDigest, resultDigest, err := remoteReceiptFields(
		value.GetOperationId(), value.GetRequestDigest(), value.GetResultDigest(),
	)
	if err != nil {
		return gameruntime.SystemReceipt{}, err
	}
	sourceEventID, err := remoteUUID(value.GetSourceEventId())
	if err != nil {
		return gameruntime.SystemReceipt{}, err
	}
	requestedBy, err := optionalRemoteUUIDFromWire(value.GetRequestedByUserId())
	if err != nil {
		return gameruntime.SystemReceipt{}, err
	}
	return gameruntime.NewSystemReceipt(gameruntime.SystemReceiptSnapshot{
		Key: gameruntime.SystemKey{
			SessionID: sessionID, OperationID: operationID,
			Source: gameruntime.SystemSource{Kind: gameSDK.Identifier(value.GetSourceKind()), EventID: sourceEventID, RequestedByUserID: requestedBy},
		},
		RequestDigest: requestDigest, ResultCode: gameruntime.ResultCode(value.GetResultCode()), ResultDigest: resultDigest,
		StateVersion: value.GetStateVersion(), CommittedAt: value.GetCommittedAt().AsTime().Round(0).UTC(),
	})
}

func envelopeFromRemote(value *gamev1.GameEnvelope) (gameSDK.VersionKey, gameSDK.Message, error) {
	if value == nil || value.GetVersion() == nil {
		return gameSDK.VersionKey{}, gameSDK.Message{}, gameruntime.ErrGameSessionIntegrity
	}
	version := versionFromTuple(value.GetGameId(), value.GetVersion())
	message := gameSDK.Message{
		MessageType: gameSDK.Identifier(value.GetMessageType()), SchemaVersion: value.GetSchemaVersion(),
		Payload: append([]byte(nil), value.GetPayload()...),
	}
	if !version.Valid() || !message.Valid() {
		return gameSDK.VersionKey{}, gameSDK.Message{}, gameruntime.ErrGameSessionIntegrity
	}
	return version, message, nil
}

func versionFromTuple(gameID string, value *gamev1.VersionTuple) gameSDK.VersionKey {
	if value == nil {
		return gameSDK.VersionKey{}
	}
	return gameSDK.VersionKey{
		GameID: gameSDK.GameID(gameID), Engine: gameSDK.Version(value.GetEngine()),
		Protocol: gameSDK.Version(value.GetProtocol()), Client: gameSDK.Version(value.GetClient()),
	}
}

func viewerToRemote(viewer gameSDK.Viewer) *realtimev1.Viewer {
	return &realtimev1.Viewer{Kind: viewerKindWire(viewer.Kind), UserId: string(viewer.UserID), SeatIndex: viewer.SeatIndex}
}

func statusFromRemote(status gamev1.GameSessionStatus) gameruntime.Status {
	switch status {
	case gamev1.GameSessionStatus_GAME_SESSION_STATUS_ACTIVE:
		return gameruntime.StatusActive
	case gamev1.GameSessionStatus_GAME_SESSION_STATUS_SUSPENDED:
		return gameruntime.StatusSuspended
	case gamev1.GameSessionStatus_GAME_SESSION_STATUS_FINISHED:
		return gameruntime.StatusFinished
	case gamev1.GameSessionStatus_GAME_SESSION_STATUS_CANCELLED:
		return gameruntime.StatusCancelled
	default:
		return ""
	}
}

func remoteReceiptFields(operationRaw string, requestRaw, resultRaw []byte) (idempotency.OperationID, idempotency.Digest, idempotency.Digest, error) {
	operationID, err := idempotency.ParseOperationID(operationRaw)
	if err != nil {
		return idempotency.OperationID{}, idempotency.Digest{}, idempotency.Digest{}, gameruntime.ErrGameSessionIntegrity
	}
	requestDigest, err := idempotency.NewDigest(requestRaw)
	if err != nil {
		return idempotency.OperationID{}, idempotency.Digest{}, idempotency.Digest{}, gameruntime.ErrGameSessionIntegrity
	}
	resultDigest, err := idempotency.NewDigest(resultRaw)
	if err != nil {
		return idempotency.OperationID{}, idempotency.Digest{}, idempotency.Digest{}, gameruntime.ErrGameSessionIntegrity
	}
	return operationID, requestDigest, resultDigest, nil
}

func remoteUUIDPair(first, second string) (uuid.UUID, uuid.UUID, error) {
	firstID, err := remoteUUID(first)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	secondID, err := remoteUUID(second)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return firstID, secondID, nil
}

func remoteUUID(value string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.String() != value {
		return uuid.Nil, gameruntime.ErrGameSessionIntegrity
	}
	return parsed, nil
}

func optionalRemoteUUID(value uuid.UUID) string {
	if value == uuid.Nil {
		return ""
	}
	return value.String()
}

func optionalRemoteUUIDFromWire(value string) (uuid.UUID, error) {
	if value == "" {
		return uuid.Nil, nil
	}
	return remoteUUID(value)
}

func remoteOptionalTime(value *timestamppb.Timestamp) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.AsTime().Round(0).UTC()
}

func mapRemoteError(err error) error {
	var connectError *connect.Error
	if !errors.As(err, &connectError) {
		return redisstore.ErrCoordinationUnavailable
	}
	for _, detail := range connectError.Details() {
		message, detailErr := detail.Value()
		if detailErr != nil {
			continue
		}
		business, ok := message.(*commonv1.BusinessErrorDetail)
		if !ok {
			continue
		}
		switch business.GetCode() {
		case commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_IDEMPOTENCY_CONFLICT:
			return idempotency.ErrConflict
		case commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_VERSION_CONFLICT:
			return roomDomain.ErrRoomVersionConflict
		case commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_HOST_REQUIRED:
			return roomDomain.ErrHostRequired
		case commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_SESSION_NOT_FOUND:
			return gameruntime.ErrSessionNotFound
		case commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_STATE_VERSION_CONFLICT:
			return gameruntime.ErrStateVersionConflict
		case commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_OWNERSHIP_LOST:
			return gameruntime.ErrOwnershipLost
		case commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_SESSION_SUSPENDED:
			return gameruntime.ErrSessionSuspended
		case commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_SESSION_TERMINAL:
			return gameruntime.ErrSessionTerminal
		case commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_PARTICIPANT_NOT_ACTIVE:
			return gameruntime.ErrParticipantNotActive
		case commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_MODULE_UNAVAILABLE:
			return gameruntime.ErrModuleUnavailable
		case commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_PROJECTION_UNSAFE:
			return gameruntime.ErrProjectionUnsafe
		case commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_REPLAY_FORBIDDEN:
			return gameruntime.ErrReplayUnavailable
		case commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_SERVICE_TEMPORARILY_UNAVAILABLE:
			return redisstore.ErrCoordinationUnavailable
		}
	}
	if connectError.Code() == connect.CodeInvalidArgument {
		return gameruntime.ErrInvalidSessionInput
	}
	return redisstore.ErrCoordinationUnavailable
}

func retryableOwner(err error) bool {
	return errors.Is(err, gameruntime.ErrOwnershipLost) || errors.Is(err, redisstore.ErrCoordinationUnavailable)
}

func retryableRemote(err error) bool {
	return errors.Is(err, redisstore.ErrCoordinationUnavailable)
}

func remoteUnavailable(err error) error {
	if err != nil {
		return err
	}
	return redisstore.ErrCoordinationUnavailable
}

var _ Runtime = (*RemoteRuntime)(nil)
