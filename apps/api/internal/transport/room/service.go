// Package room adapts authenticated PartyRoom commands to the generated Connect service.
package room

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	gameRules "github.com/iFTY-R/game-night/apps/api/internal/gamerules"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/cookies"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/csrf"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/origin"
	commonv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/common/v1"
	roomv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/room/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/room/v1/roomv1connect"
	"github.com/iFTY-R/game-night/platform/clock"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/idempotency"
	identityDomain "github.com/iFTY-R/game-night/platform/identity"
	redisstore "github.com/iFTY-R/game-night/platform/persistence/redis"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
	gameSDK "github.com/iFTY-R/game-night/sdk/go/game"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// publicRoomCursorVersion permits future cursor format changes without ambiguous decoding.
	publicRoomCursorVersion byte = 1
	// publicRoomCursorBytes stores one version byte, Unix nanoseconds, and a UUID.
	publicRoomCursorBytes = 1 + 8 + 16
	// myRoomCursorVersion separates host-first member cursors from public-lobby cursors.
	myRoomCursorVersion byte = 1
	// myRoomCursorBytes adds one host-priority byte to the stable timestamp and UUID position.
	myRoomCursorBytes = 1 + 1 + 8 + 16
	// finishAction is the platform-owned system command that every registered module must implement.
	finishAction gameSDK.Identifier = "session.finish"
)

// GameRuntime is the authoritative cross-aggregate lifecycle surface shared with GameService.
type GameRuntime interface {
	Start(context.Context, gameruntime.StartCommand) (roomDomain.Room, gameruntime.Session, error)
	HandleSystem(context.Context, gameruntime.SystemCommand) (gameruntime.SystemCommitResult, error)
	Cancel(context.Context, gameruntime.CancelCommand) (roomDomain.Room, gameruntime.Session, error)
}

// GameSessionReader supplies exact version and ownership data needed by a host finish command.
type GameSessionReader interface {
	Get(context.Context, uuid.UUID) (gameruntime.Session, error)
}

// RoomReader reloads the atomically committed post-game room returned to RoomService clients.
type RoomReader interface {
	GetByID(context.Context, uuid.UUID) (roomDomain.Room, error)
	ListRoomMemberUsernames(context.Context, uuid.UUID) (map[uuid.UUID]string, error)
	RecordRoomPresence(context.Context, uuid.UUID, uuid.UUID) (time.Time, error)
}

// FanoutPublisher publishes only committed session cursors; PostgreSQL remains authoritative.
type FanoutPublisher interface {
	PublishSessionFanout(context.Context, redisstore.SessionFanoutEvent) error
}

// Service keeps Cookie, Origin, CSRF, and wire mapping outside the room domain.
type Service struct {
	domain        *roomDomain.Service
	catalog       roomDomain.GameCatalog
	runtime       GameRuntime
	sessions      GameSessionReader
	rooms         RoomReader
	fanout        FanoutPublisher
	authenticator PrincipalAuthenticator
	origins       *origin.UserValidator
	csrf          *csrf.UserValidator
	ruleRepo      roomDomain.RuleRepository
	ruleCatalog   *gameRules.Catalog
	ruleClock     clock.Clock
}

// ServiceOption injects rule persistence and deterministic time without
// changing the existing room transport constructor call sites.
type ServiceOption func(*Service) error

// WithRuleRepository replaces the local memory fallback with durable storage.
func WithRuleRepository(repository roomDomain.RuleRepository) ServiceOption {
	return func(service *Service) error {
		if repository == nil {
			return roomDomain.ErrInvalidRoomInput
		}
		service.ruleRepo = repository
		return nil
	}
}

// WithRuleCatalog replaces the built-in three-game codec catalog for tests or
// future module registration without moving rule semantics into the platform.
func WithRuleCatalog(catalog *gameRules.Catalog) ServiceOption {
	return func(service *Service) error {
		if catalog == nil {
			return roomDomain.ErrInvalidRoomInput
		}
		service.ruleCatalog = catalog
		return nil
	}
}

// WithRuleClock supplies the same calibrated clock used by room persistence.
func WithRuleClock(source clock.Clock) ServiceOption {
	return func(service *Service) error {
		if source == nil {
			return roomDomain.ErrInvalidRoomInput
		}
		service.ruleClock = source
		return nil
	}
}

// NewService validates complete room transport wiring before the generated handler is mounted.
func NewService(
	domainService *roomDomain.Service,
	catalog roomDomain.GameCatalog,
	runtime GameRuntime,
	sessions GameSessionReader,
	rooms RoomReader,
	fanout FanoutPublisher,
	authenticator PrincipalAuthenticator,
	originValidator *origin.UserValidator,
	csrfValidator *csrf.UserValidator,
	options ...ServiceOption,
) (*Service, error) {
	if domainService == nil || catalog == nil || runtime == nil || sessions == nil || rooms == nil || fanout == nil ||
		authenticator == nil || originValidator == nil || csrfValidator == nil {
		return nil, roomDomain.ErrInvalidRoomInput
	}
	service := &Service{
		domain: domainService, catalog: catalog, runtime: runtime, sessions: sessions, rooms: rooms, fanout: fanout,
		authenticator: authenticator, origins: originValidator, csrf: csrfValidator,
		ruleRepo: roomDomain.NewMemoryRuleRepository(), ruleCatalog: gameRules.NewCatalog(), ruleClock: clock.System{},
	}
	for _, option := range options {
		if option == nil {
			return nil, roomDomain.ErrInvalidRoomInput
		}
		if err := option(service); err != nil {
			return nil, err
		}
	}
	return service, nil
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
	wireRoom, err := service.roomWire(ctx, created)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.CreateRoomResponse{Room: wireRoom}), nil
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
	wireRoom, err := service.roomWire(ctx, loaded)
	if err != nil {
		return nil, err
	}
	if _, member := loaded.Member(actor); !member {
		// Public discovery is intentionally redacted. Rule drafts and pending
		// tokens are member-only even though the room itself is discoverable.
		wireRoom.GameConfigDrafts = nil
		wireRoom.PendingStart = nil
		wireRoom.OwnershipEpoch = 0
	}
	return connect.NewResponse(&roomv1.GetRoomResponse{Room: wireRoom}), nil
}

// HeartbeatRoom renews the authenticated member's room lease without changing optimistic room versions.
func (service *Service) HeartbeatRoom(ctx context.Context, request *connect.Request[roomv1.HeartbeatRoomRequest]) (*connect.Response[roomv1.HeartbeatRoomResponse], error) {
	actor, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	roomID, err := parseUUID(request.Msg.GetRoomId())
	if err != nil {
		return nil, err
	}
	observedAt, err := service.rooms.RecordRoomPresence(ctx, roomID, actor)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.HeartbeatRoomResponse{ObservedAt: timestamppb.New(observedAt)}), nil
}

// ListMyRooms authenticates a private member read and returns host-owned rooms before joined rooms.
func (service *Service) ListMyRooms(ctx context.Context, request *connect.Request[roomv1.ListMyRoomsRequest]) (*connect.Response[roomv1.ListMyRoomsResponse], error) {
	actor, err := service.authenticate(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	after, pageSize, err := myRoomPageRequest(request.Msg.GetPage())
	if err != nil {
		return nil, err
	}
	page, err := service.domain.ListMyRooms(ctx, roomDomain.ListMyRoomsCommand{ActorUserID: actor, After: after, PageSize: pageSize})
	if err != nil {
		return nil, err
	}
	rooms := make([]*roomv1.MyRoomCard, 0, len(page.Rooms))
	for _, card := range page.Rooms {
		rooms = append(rooms, myRoomCardWire(card))
	}
	nextToken, err := encodeMyRoomCursor(page.NextCursor)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.ListMyRoomsResponse{
		Rooms: rooms, Page: &commonv1.PageInfo{NextPageToken: nextToken},
	}), nil
}

// ListPublicRooms authenticates a safe lobby read and returns only redacted actor-aware cards.
func (service *Service) ListPublicRooms(ctx context.Context, request *connect.Request[roomv1.ListPublicRoomsRequest]) (*connect.Response[roomv1.ListPublicRoomsResponse], error) {
	actor, err := service.authenticate(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	after, pageSize, err := publicRoomPageRequest(request.Msg.GetPage())
	if err != nil {
		return nil, err
	}
	page, err := service.domain.ListPublicRooms(ctx, roomDomain.ListPublicRoomsCommand{
		ActorUserID: actor, Filter: publicRoomFilterDomain(request.Msg.GetFilter()), After: after, PageSize: pageSize,
	})
	if err != nil {
		return nil, err
	}
	rooms := make([]*roomv1.PublicRoomCard, 0, len(page.Rooms))
	for _, card := range page.Rooms {
		rooms = append(rooms, publicRoomCardWire(card))
	}
	nextToken, err := encodePublicRoomCursor(page.NextCursor)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.ListPublicRoomsResponse{
		Rooms: rooms, Page: &commonv1.PageInfo{NextPageToken: nextToken},
	}), nil
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
	wireRoom, err := service.roomWire(ctx, joined)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.JoinRoomResponse{
		Room: wireRoom, Member: roomMemberWire(wireRoom, result.Member), Created: result.Created, Changed: result.Changed,
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
	wireRoom, err := service.roomWire(ctx, updated)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.ApproveMemberResponse{Room: wireRoom, Member: roomMemberWire(wireRoom, result.Member)}), nil
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
	wireRoom, err := service.roomWire(ctx, updated)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.SetAdmissionResponse{Room: wireRoom}), nil
}

// StartGame validates the opaque config and publishes PartyRoom plus GameSession atomically.
func (service *Service) StartGame(ctx context.Context, request *connect.Request[roomv1.StartGameRequest]) (*connect.Response[roomv1.StartGameResponse], error) {
	actor, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil || request.Msg.GetExpectedVersion() == nil {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	roomID, err := parseUUID(request.Msg.GetRoomId())
	if err != nil {
		return nil, err
	}
	gameID, config, operationID, requestDigest, frozenConfig, configRevision, err := service.prepareStart(ctx, actor, roomID, request.Msg)
	if err != nil {
		return nil, err
	}
	participantLimits, err := service.catalog.ParticipantLimits(ctx, string(gameID))
	if err != nil {
		return nil, err
	}
	updated, session, err := service.runtime.Start(ctx, gameruntime.StartCommand{
		ActorUserID: actor, RoomID: roomID, GameID: gameID,
		Expected: versionDomain(request.Msg.GetExpectedVersion()), Config: config,
		OperationID: operationID, RequestDigest: &requestDigest,
	})
	if err != nil {
		return nil, err
	}
	if usesPendingStart(request.Msg) {
		pendingID, parseErr := parseUUID(request.Msg.GetPendingStartId())
		if parseErr != nil {
			return nil, parseErr
		}
		// The session commit is the point of no return. Consuming only afterwards
		// keeps a failed module/runtime creation retryable with the same countdown.
		if _, consumeErr := service.ruleRepo.ConsumePendingStart(
			ctx,
			roomID,
			pendingID,
			request.Msg.GetCancelToken(),
			operationID.Value(),
			[32]byte(requestDigest),
			service.ruleNow(),
		); consumeErr != nil {
			return nil, consumeErr
		}
	}
	snapshot := session.Snapshot()
	roomSnapshot := updated.Snapshot()
	participantCount := uint32(len(snapshot.Participants))
	if snapshot.RoomID != roomID || snapshot.VersionKey.GameID != gameID || roomSnapshot.ActiveSessionID != snapshot.ID ||
		participantCount < participantLimits.Minimum || participantCount > participantLimits.Maximum {
		return nil, gameruntime.ErrGameSessionIntegrity
	}
	if err := service.publish(ctx, session); err != nil {
		return nil, err
	}
	participants := make([]*roomv1.FrozenParticipant, len(snapshot.Participants))
	for index, participant := range snapshot.Participants {
		participants[index] = &roomv1.FrozenParticipant{UserId: participant.UserID.String(), SeatIndex: participant.SeatIndex}
	}
	wireRoom, err := service.roomWire(ctx, updated)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.StartGameResponse{
		Room: wireRoom, SessionId: snapshot.ID.String(), GameId: string(snapshot.VersionKey.GameID), Participants: participants,
		FrozenConfig: configEnvelopeToWire(frozenConfig), ConfigRevision: configRevision,
	}), nil
}

// prepareStart resolves the authoritative room draft and pending token before
// the runtime is allowed to mutate room/session state. Empty pending fields are
// retained as a compatibility path for clients predating the countdown API.
func (service *Service) prepareStart(ctx context.Context, actor, roomID uuid.UUID, request *roomv1.StartGameRequest) (
	gameSDK.GameID, gameSDK.Message, idempotency.OperationID, idempotency.Digest, roomDomain.ConfigEnvelope, uint64, error,
) {
	usesPending := usesPendingStart(request)
	if !usesPending {
		legacyGameID, legacyConfig, operationID, requestDigest, err := startGameInput(request)
		if err != nil {
			return "", gameSDK.Message{}, idempotency.OperationID{}, idempotency.Digest{}, roomDomain.ConfigEnvelope{}, 0, err
		}
		return legacyGameID, legacyConfig, operationID, requestDigest, legacyConfigEnvelope(request.GetConfig(), legacyGameID), 0, nil
	}
	if request.GetPendingStartId() == "" || request.GetCancelToken() == "" || request.GetConfigRevision() == 0 || request.GetOwnershipEpoch() == 0 {
		return "", gameSDK.Message{}, idempotency.OperationID{}, idempotency.Digest{}, roomDomain.ConfigEnvelope{}, 0, gameruntime.ErrInvalidSessionInput
	}
	pendingID, err := parseUUID(request.GetPendingStartId())
	if err != nil {
		return "", gameSDK.Message{}, idempotency.OperationID{}, idempotency.Digest{}, roomDomain.ConfigEnvelope{}, 0, err
	}
	gameID, err := gameSDK.ParseGameID(strings.TrimSpace(request.GetGameId()))
	if err != nil {
		return "", gameSDK.Message{}, idempotency.OperationID{}, idempotency.Digest{}, roomDomain.ConfigEnvelope{}, 0, gameruntime.ErrInvalidSessionInput
	}
	operationID, requestDigest, err := operationBinding(request.GetOperationId(), request.GetRequestDigest())
	if err != nil {
		return "", gameSDK.Message{}, idempotency.OperationID{}, idempotency.Digest{}, roomDomain.ConfigEnvelope{}, 0, err
	}
	current, err := service.authorizeRuleHost(ctx, actor, roomID, versionDomain(request.GetExpectedVersion()), request.GetOwnershipEpoch())
	if err != nil {
		return "", gameSDK.Message{}, idempotency.OperationID{}, idempotency.Digest{}, roomDomain.ConfigEnvelope{}, 0, err
	}
	if current.Snapshot().SelectedGameID != string(gameID) {
		return "", gameSDK.Message{}, idempotency.OperationID{}, idempotency.Digest{}, roomDomain.ConfigEnvelope{}, 0, roomDomain.ErrGameSelectionConflict
	}
	pending, err := service.ruleRepo.GetPendingStart(ctx, roomID)
	if err != nil || pending.ID != pendingID || pending.CancelToken != request.GetCancelToken() || pending.GameID != string(gameID) ||
		pending.ConfigRevision != request.GetConfigRevision() || pending.Expected != current.Version() || pending.OwnershipEpoch != current.Snapshot().OwnershipEpoch {
		return "", gameSDK.Message{}, idempotency.OperationID{}, idempotency.Digest{}, roomDomain.ConfigEnvelope{}, 0, roomDomain.ErrPendingStartInvalid
	}
	// A pending record carries a server timestamp, so clients cannot race or
	// locally shorten the visible countdown by calling StartGame early.
	if service.ruleNow().Before(pending.Deadline) {
		return "", gameSDK.Message{}, idempotency.OperationID{}, idempotency.Digest{}, roomDomain.ConfigEnvelope{}, 0, roomDomain.ErrPendingStartInvalid
	}
	draft, err := service.ruleRepo.GetDraft(ctx, roomID, string(gameID))
	if err != nil || draft.Revision != pending.ConfigRevision {
		return "", gameSDK.Message{}, idempotency.OperationID{}, idempotency.Digest{}, roomDomain.ConfigEnvelope{}, 0, roomDomain.ErrRuleRevisionConflict
	}
	config := gameSDK.Message{MessageType: gameSDK.Identifier(draft.Config.MessageType), SchemaVersion: draft.Config.SchemaVersion, Payload: append([]byte(nil), draft.Config.Payload...)}
	if request.GetConfig() != nil {
		provided, providedErr := configEnvelopeFromGameConfig(request.GetConfig(), gameID)
		if providedErr != nil || provided.Digest() != draft.Config.Digest() {
			return "", gameSDK.Message{}, idempotency.OperationID{}, idempotency.Digest{}, roomDomain.ConfigEnvelope{}, 0, roomDomain.ErrRuleRevisionConflict
		}
	}
	return gameID, config, operationID, requestDigest, draft.Config, draft.Revision, nil
}

// usesPendingStart distinguishes the new countdown-fenced contract from the
// legacy direct-start request without treating a partial new request as legacy.
func usesPendingStart(request *roomv1.StartGameRequest) bool {
	if request == nil {
		return false
	}
	return request.GetPendingStartId() != "" || request.GetCancelToken() != "" || request.GetConfigRevision() != 0 || request.GetOwnershipEpoch() != 0
}

// FinishGame submits the module-owned terminal transition and clears the room pointer in the same transaction.
func (service *Service) FinishGame(ctx context.Context, request *connect.Request[roomv1.FinishGameRequest]) (*connect.Response[roomv1.FinishGameResponse], error) {
	actor, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil || request.Msg.GetExpectedVersion() == nil || request.Msg.GetExpectedStateVersion() == 0 {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	roomID, sessionID, err := twoUUIDs(request.Msg.GetRoomId(), request.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	operationID, sourceEventID, versionKey, command, requestDigest, err := finishGameInput(request.Msg)
	if err != nil {
		return nil, err
	}
	roomBefore, sessionBefore, err := service.authorizeFinish(ctx, actor, roomID, sessionID, request.Msg)
	if err != nil {
		return nil, err
	}
	if sessionBefore.Snapshot().VersionKey != versionKey {
		return nil, gameruntime.ErrStateVersionConflict
	}
	result, err := service.runtime.HandleSystem(ctx, gameruntime.SystemCommand{
		SessionID: sessionID, OperationID: operationID,
		Source: gameruntime.SystemSource{
			Kind: gameruntime.SystemSourceHostAPI, EventID: sourceEventID, RequestedByUserID: actor,
		},
		ExpectedStateVersion: request.Msg.GetExpectedStateVersion(), OwnershipEpoch: sessionBefore.Snapshot().OwnershipEpoch,
		VersionKey: versionKey, Message: command, RequestDigest: &requestDigest,
	})
	if err != nil {
		return nil, err
	}
	if result.Session.Snapshot().Status != gameruntime.StatusFinished {
		return nil, gameruntime.ErrInvalidSystemCommit
	}
	if err := service.publish(ctx, result.Session); err != nil {
		return nil, err
	}
	updated, err := service.rooms.GetByID(ctx, roomBefore.Snapshot().ID)
	if err != nil {
		return nil, err
	}
	updatedSnapshot := updated.Snapshot()
	if updatedSnapshot.Status != roomDomain.RoomStatusPostGame || updatedSnapshot.ActiveSessionID != uuid.Nil {
		return nil, gameruntime.ErrGameSessionIntegrity
	}
	wireRoom, err := service.roomWire(ctx, updated)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.FinishGameResponse{Room: wireRoom}), nil
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
		ActorUserID: actor, RoomID: roomID, UserID: userID, Reason: roomDomain.RemovalReasonHostRemoved,
		Expected: versionDomain(request.Msg.GetExpectedVersion()),
	})
	if err != nil {
		return nil, err
	}
	activeSessionID := ""
	if result.SessionID != uuid.Nil {
		activeSessionID = result.SessionID.String()
	}
	wireRoom, err := service.roomWire(ctx, updated)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.RemoveMemberResponse{
		Room: wireRoom, Removed: memberWire(result.Removed, ""), ParticipantRevoked: result.ParticipantRevoked,
		ActiveSessionId: activeSessionID, SourceEventId: optionalUUIDString(result.SourceEventID),
	}), nil
}

// CloseRoom permanently closes an idle room or atomically cancels its active session under host authority.
func (service *Service) CloseRoom(ctx context.Context, request *connect.Request[roomv1.CloseRoomRequest]) (*connect.Response[roomv1.CloseRoomResponse], error) {
	actor, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil || request.Msg.GetExpectedVersion() == nil {
		return nil, roomDomain.ErrInvalidRoomInput
	}
	roomID, err := parseUUID(request.Msg.GetRoomId())
	if err != nil {
		return nil, err
	}
	expected := versionDomain(request.Msg.GetExpectedVersion())
	current, err := service.rooms.GetByID(ctx, roomID)
	if err != nil {
		return nil, err
	}
	currentSnapshot := current.Snapshot()
	// A playing room owns live timers and session state, so only the runtime transaction may clear its pointer and close it.
	if currentSnapshot.Status == roomDomain.RoomStatusPlaying {
		if currentSnapshot.HostUserID != actor {
			return nil, roomDomain.ErrHostRequired
		}
		if current.Version() != expected || currentSnapshot.ActiveSessionID == uuid.Nil {
			return nil, roomDomain.ErrRoomVersionConflict
		}
		session, err := service.sessions.Get(ctx, currentSnapshot.ActiveSessionID)
		if err != nil {
			return nil, err
		}
		sessionSnapshot := session.Snapshot()
		if sessionSnapshot.RoomID != roomID || sessionSnapshot.ID != currentSnapshot.ActiveSessionID ||
			string(sessionSnapshot.VersionKey.GameID) != currentSnapshot.ActiveGameID || sessionSnapshot.OwnershipEpoch == 0 ||
			sessionSnapshot.Status.Terminal() {
			return nil, gameruntime.ErrGameSessionIntegrity
		}
		updated, cancelled, err := service.runtime.Cancel(ctx, gameruntime.CancelCommand{
			RoomID: roomID, SessionID: sessionSnapshot.ID, ExpectedRoom: expected,
			OwnershipEpoch: sessionSnapshot.OwnershipEpoch, CloseRoom: true,
		})
		if err != nil {
			return nil, err
		}
		updatedSnapshot, cancelledSnapshot := updated.Snapshot(), cancelled.Snapshot()
		if updatedSnapshot.Status != roomDomain.RoomStatusClosed || updatedSnapshot.ActiveSessionID != uuid.Nil ||
			updatedSnapshot.ParticipantAdmission != roomDomain.AdmissionClosed || updatedSnapshot.SpectatorAdmission != roomDomain.AdmissionClosed ||
			cancelledSnapshot.ID != sessionSnapshot.ID || cancelledSnapshot.RoomID != roomID || cancelledSnapshot.Status != gameruntime.StatusCancelled {
			return nil, gameruntime.ErrGameSessionIntegrity
		}
		if err := service.publish(ctx, cancelled); err != nil {
			return nil, err
		}
		wireRoom, err := service.roomWire(ctx, updated)
		if err != nil {
			return nil, err
		}
		return connect.NewResponse(&roomv1.CloseRoomResponse{Room: wireRoom}), nil
	}
	updated, err := service.domain.CloseRoom(ctx, roomDomain.CloseRoomCommand{
		ActorUserID: actor, RoomID: roomID, Expected: expected,
	})
	if err != nil {
		return nil, err
	}
	wireRoom, err := service.roomWire(ctx, updated)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.CloseRoomResponse{Room: wireRoom}), nil
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

func publicRoomPageRequest(page *commonv1.PageRequest) (roomDomain.PublicRoomPageCursor, uint32, error) {
	if page == nil {
		return roomDomain.PublicRoomPageCursor{}, 0, nil
	}
	if page.GetPageSize() < 0 || page.GetPageSize() > int32(roomDomain.MaximumPublicRoomPageSize) {
		return roomDomain.PublicRoomPageCursor{}, 0, roomDomain.ErrInvalidRoomInput
	}
	cursor, err := decodePublicRoomCursor(page.GetPageToken())
	return cursor, uint32(page.GetPageSize()), err
}

func encodePublicRoomCursor(cursor roomDomain.PublicRoomPageCursor) (string, error) {
	if cursor.UpdatedAt.IsZero() && cursor.RoomID == uuid.Nil {
		return "", nil
	}
	if cursor.UpdatedAt.IsZero() || cursor.RoomID == uuid.Nil || cursor.UpdatedAt.UnixNano() <= 0 {
		return "", roomDomain.ErrInvalidRoomInput
	}
	raw := make([]byte, publicRoomCursorBytes)
	raw[0] = publicRoomCursorVersion
	binary.BigEndian.PutUint64(raw[1:9], uint64(cursor.UpdatedAt.UnixNano()))
	copy(raw[9:], cursor.RoomID[:])
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodePublicRoomCursor(value string) (roomDomain.PublicRoomPageCursor, error) {
	if value == "" {
		return roomDomain.PublicRoomPageCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(raw) != publicRoomCursorBytes || raw[0] != publicRoomCursorVersion ||
		base64.RawURLEncoding.EncodeToString(raw) != value {
		return roomDomain.PublicRoomPageCursor{}, roomDomain.ErrInvalidRoomInput
	}
	roomID, err := uuid.FromBytes(raw[9:])
	if err != nil || roomID == uuid.Nil {
		return roomDomain.PublicRoomPageCursor{}, roomDomain.ErrInvalidRoomInput
	}
	updatedAt := time.Unix(0, int64(binary.BigEndian.Uint64(raw[1:9]))).UTC()
	if updatedAt.UnixNano() <= 0 {
		return roomDomain.PublicRoomPageCursor{}, roomDomain.ErrInvalidRoomInput
	}
	return roomDomain.PublicRoomPageCursor{UpdatedAt: updatedAt, RoomID: roomID}, nil
}

func myRoomPageRequest(page *commonv1.PageRequest) (roomDomain.MyRoomPageCursor, uint32, error) {
	if page == nil {
		return roomDomain.MyRoomPageCursor{}, 0, nil
	}
	if page.GetPageSize() < 0 || page.GetPageSize() > int32(roomDomain.MaximumPublicRoomPageSize) {
		return roomDomain.MyRoomPageCursor{}, 0, roomDomain.ErrInvalidRoomInput
	}
	cursor, err := decodeMyRoomCursor(page.GetPageToken())
	return cursor, uint32(page.GetPageSize()), err
}

func encodeMyRoomCursor(cursor roomDomain.MyRoomPageCursor) (string, error) {
	if cursor.UpdatedAt.IsZero() && cursor.RoomID == uuid.Nil {
		return "", nil
	}
	if cursor.UpdatedAt.IsZero() || cursor.RoomID == uuid.Nil || cursor.UpdatedAt.UnixNano() <= 0 {
		return "", roomDomain.ErrInvalidRoomInput
	}
	raw := make([]byte, myRoomCursorBytes)
	raw[0] = myRoomCursorVersion
	if cursor.IsHost {
		raw[1] = 1
	}
	binary.BigEndian.PutUint64(raw[2:10], uint64(cursor.UpdatedAt.UnixNano()))
	copy(raw[10:], cursor.RoomID[:])
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeMyRoomCursor(value string) (roomDomain.MyRoomPageCursor, error) {
	if value == "" {
		return roomDomain.MyRoomPageCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(raw) != myRoomCursorBytes || raw[0] != myRoomCursorVersion || raw[1] > 1 ||
		base64.RawURLEncoding.EncodeToString(raw) != value {
		return roomDomain.MyRoomPageCursor{}, roomDomain.ErrInvalidRoomInput
	}
	roomID, err := uuid.FromBytes(raw[10:])
	if err != nil || roomID == uuid.Nil {
		return roomDomain.MyRoomPageCursor{}, roomDomain.ErrInvalidRoomInput
	}
	updatedAt := time.Unix(0, int64(binary.BigEndian.Uint64(raw[2:10]))).UTC()
	if updatedAt.UnixNano() <= 0 {
		return roomDomain.MyRoomPageCursor{}, roomDomain.ErrInvalidRoomInput
	}
	return roomDomain.MyRoomPageCursor{IsHost: raw[1] == 1, UpdatedAt: updatedAt, RoomID: roomID}, nil
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

// roomWire resolves mutable identity names at response time so every viewer receives the same member projection.
func (service *Service) roomWire(ctx context.Context, room roomDomain.Room) (*roomv1.Room, error) {
	snapshot := room.Snapshot()
	usernames, err := service.rooms.ListRoomMemberUsernames(ctx, snapshot.ID)
	if err != nil {
		return nil, err
	}
	wire := roomWire(room, usernames)
	wire.SelectedGameId = snapshot.SelectedGameID
	wire.OwnershipEpoch = snapshot.OwnershipEpoch
	if service.ruleRepo != nil {
		drafts, draftErr := service.ruleRepo.ListDrafts(ctx, snapshot.ID)
		if draftErr != nil {
			return nil, draftErr
		}
		wire.GameConfigDrafts = make([]*roomv1.RoomGameConfigDraft, 0, len(drafts))
		for _, draft := range drafts {
			wire.GameConfigDrafts = append(wire.GameConfigDrafts, ruleDraftWire(draft))
		}
		pending, pendingErr := service.ruleRepo.GetPendingStart(ctx, snapshot.ID)
		if pendingErr == nil && !pending.Cancelled && !pending.Consumed && pending.Deadline.After(service.ruleNow()) {
			wire.PendingStart = pendingStartWire(pending)
		} else if pendingErr != nil && !errors.Is(pendingErr, roomDomain.ErrRuleNotFound) {
			return nil, pendingErr
		}
	}
	return wire, nil
}

func roomWire(room roomDomain.Room, usernames map[uuid.UUID]string) *roomv1.Room {
	snapshot := room.Snapshot()
	members := make([]*roomv1.RoomMember, len(snapshot.Members))
	for index, member := range snapshot.Members {
		members[index] = memberWire(member, usernames[member.UserID])
	}
	activeSessionID := ""
	if snapshot.ActiveSessionID != uuid.Nil {
		activeSessionID = snapshot.ActiveSessionID.String()
	}
	lastFinishedSessionID := ""
	if snapshot.LastFinishedSessionID != uuid.Nil {
		lastFinishedSessionID = snapshot.LastFinishedSessionID.String()
	}
	return &roomv1.Room{
		RoomId: snapshot.ID.String(), RoomCode: snapshot.RoomCode, Visibility: visibilityWire(snapshot.Visibility),
		Status: statusWire(snapshot.Status), HostUserId: snapshot.HostUserID.String(),
		ParticipantCapacity: snapshot.ParticipantCapacity, ParticipantAdmission: admissionWire(snapshot.ParticipantAdmission),
		SpectatorAdmission: admissionWire(snapshot.SpectatorAdmission), Members: members,
		ActiveSessionId: activeSessionID, ActiveGameId: snapshot.ActiveGameID,
		LastFinishedSessionId: lastFinishedSessionID, LastFinishedGameId: snapshot.LastFinishedGameID,
		SelectedGameId: snapshot.SelectedGameID, OwnershipEpoch: snapshot.OwnershipEpoch,
		Version:   &roomv1.RoomVersion{RoomVersion: snapshot.RoomVersion, MembershipVersion: snapshot.MembershipVersion},
		CreatedAt: timestamppb.New(snapshot.CreatedAt), UpdatedAt: timestamppb.New(snapshot.UpdatedAt),
	}
}

func publicRoomCardWire(card roomDomain.PublicRoomCard) *roomv1.PublicRoomCard {
	snapshot := card.Snapshot()
	return &roomv1.PublicRoomCard{
		RoomId: snapshot.RoomID.String(), HostUsername: snapshot.HostUsername, Status: statusWire(snapshot.Status),
		ParticipantCapacity: snapshot.ParticipantCapacity, ParticipantCount: snapshot.ParticipantCount,
		SpectatorCount: snapshot.SpectatorCount, WaitingCount: snapshot.WaitingCount,
		ParticipantAdmission: admissionWire(snapshot.ParticipantAdmission), SpectatorAdmission: admissionWire(snapshot.SpectatorAdmission),
		ActiveGameId: snapshot.ActiveGameID, ViewerRole: memberRoleWire(snapshot.ViewerRole),
		ViewerRequestedRole: memberRoleWire(snapshot.ViewerRequestedRole), PrimaryAction: publicRoomPrimaryActionWire(card.PrimaryAction()),
		UpdatedAt: timestamppb.New(snapshot.UpdatedAt),
	}
}

func myRoomCardWire(card roomDomain.MyRoomCard) *roomv1.MyRoomCard {
	snapshot := card.Snapshot()
	return &roomv1.MyRoomCard{
		RoomId: snapshot.RoomID.String(), RoomCode: snapshot.RoomCode, Visibility: visibilityWire(snapshot.Visibility),
		HostUsername: snapshot.HostUsername, Status: statusWire(snapshot.Status), IsHost: snapshot.IsHost,
		ParticipantCapacity: snapshot.ParticipantCapacity, ParticipantCount: snapshot.ParticipantCount,
		SpectatorCount: snapshot.SpectatorCount, WaitingCount: snapshot.WaitingCount,
		ParticipantAdmission: admissionWire(snapshot.ParticipantAdmission), SpectatorAdmission: admissionWire(snapshot.SpectatorAdmission),
		ActiveGameId: snapshot.ActiveGameID, LastFinishedGameId: snapshot.LastFinishedGameID,
		ViewerRole: memberRoleWire(snapshot.ViewerRole), ViewerRequestedRole: memberRoleWire(snapshot.ViewerRequestedRole),
		UpdatedAt: timestamppb.New(snapshot.UpdatedAt),
	}
}

func memberWire(member roomDomain.MemberSnapshot, username string) *roomv1.RoomMember {
	return &roomv1.RoomMember{
		UserId: member.UserID.String(), Role: memberRoleWire(member.Role), RequestedRole: memberRoleWire(member.RequestedRole),
		SeatIndex: member.SeatIndex, JoinedAt: timestamppb.New(member.JoinedAt), LastSeenAt: timestamppb.New(member.LastSeenAt),
		Username: username,
	}
}

// roomMemberWire reuses the already projected member so command-specific responses cannot diverge from Room.members.
func roomMemberWire(room *roomv1.Room, member roomDomain.MemberSnapshot) *roomv1.RoomMember {
	for _, candidate := range room.GetMembers() {
		if candidate.GetUserId() == member.UserID.String() {
			return candidate
		}
	}
	return memberWire(member, "")
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
	case roomDomain.RoomStatusPostGame:
		return roomv1.RoomStatus_ROOM_STATUS_POST_GAME
	case roomDomain.RoomStatusClosed:
		return roomv1.RoomStatus_ROOM_STATUS_CLOSED
	default:
		return roomv1.RoomStatus_ROOM_STATUS_UNSPECIFIED
	}
}

func statusDomain(value roomv1.RoomStatus) roomDomain.RoomStatus {
	switch value {
	case roomv1.RoomStatus_ROOM_STATUS_LOBBY:
		return roomDomain.RoomStatusLobby
	case roomv1.RoomStatus_ROOM_STATUS_PLAYING:
		return roomDomain.RoomStatusPlaying
	case roomv1.RoomStatus_ROOM_STATUS_POST_GAME:
		return roomDomain.RoomStatusPostGame
	case roomv1.RoomStatus_ROOM_STATUS_CLOSED:
		return roomDomain.RoomStatusClosed
	default:
		return ""
	}
}

func publicRoomFilterDomain(value *roomv1.PublicRoomFilter) roomDomain.PublicRoomFilter {
	if value == nil {
		return roomDomain.PublicRoomFilter{}
	}
	statuses := make([]roomDomain.RoomStatus, 0, len(value.GetStatuses()))
	for _, status := range value.GetStatuses() {
		statuses = append(statuses, statusDomain(status))
	}
	return roomDomain.PublicRoomFilter{
		Statuses: statuses, GameID: value.GetGameId(), ParticipantJoinableOnly: value.GetParticipantJoinableOnly(),
	}
}

func publicRoomPrimaryActionWire(value roomDomain.PublicRoomPrimaryAction) roomv1.PublicRoomPrimaryAction {
	switch value {
	case roomDomain.PublicRoomPrimaryActionEnterRoom:
		return roomv1.PublicRoomPrimaryAction_PUBLIC_ROOM_PRIMARY_ACTION_ENTER_ROOM
	case roomDomain.PublicRoomPrimaryActionJoin:
		return roomv1.PublicRoomPrimaryAction_PUBLIC_ROOM_PRIMARY_ACTION_JOIN
	case roomDomain.PublicRoomPrimaryActionRequestJoin:
		return roomv1.PublicRoomPrimaryAction_PUBLIC_ROOM_PRIMARY_ACTION_REQUEST_JOIN
	case roomDomain.PublicRoomPrimaryActionSpectate:
		return roomv1.PublicRoomPrimaryAction_PUBLIC_ROOM_PRIMARY_ACTION_SPECTATE
	case roomDomain.PublicRoomPrimaryActionRequestSpectate:
		return roomv1.PublicRoomPrimaryAction_PUBLIC_ROOM_PRIMARY_ACTION_REQUEST_SPECTATE
	case roomDomain.PublicRoomPrimaryActionWaitForHost:
		return roomv1.PublicRoomPrimaryAction_PUBLIC_ROOM_PRIMARY_ACTION_WAIT_FOR_HOST
	case roomDomain.PublicRoomPrimaryActionInProgress:
		return roomv1.PublicRoomPrimaryAction_PUBLIC_ROOM_PRIMARY_ACTION_IN_PROGRESS
	case roomDomain.PublicRoomPrimaryActionFull:
		return roomv1.PublicRoomPrimaryAction_PUBLIC_ROOM_PRIMARY_ACTION_FULL
	default:
		return roomv1.PublicRoomPrimaryAction_PUBLIC_ROOM_PRIMARY_ACTION_UNSPECIFIED
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

func optionalUUIDString(value uuid.UUID) string {
	if value == uuid.Nil {
		return ""
	}
	return value.String()
}

func requestHTTP[T any](request *connect.Request[T]) *http.Request {
	if request == nil {
		return nil
	}
	return &http.Request{Header: request.Header(), RemoteAddr: request.Peer().Addr}
}

var _ roomv1connect.RoomServiceHandler = (*Service)(nil)
