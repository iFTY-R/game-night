package internalgame

import (
	"bytes"
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	realtimev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/realtime/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/realtime/v1/realtimev1connect"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/idempotency"
	redisstore "github.com/iFTY-R/game-night/platform/persistence/redis"
	roomdomain "github.com/iFTY-R/game-night/platform/room"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

func TestOwnerServiceActionAcquiresOwnerAndNeverAcceptsCallerEpoch(t *testing.T) {
	fixture := newInternalGameFixture(t)
	operationID := internalOperationID(t, 3)
	digest := sha256.Sum256([]byte("internal-action"))
	request := connect.NewRequest(&realtimev1.GameActionRequest{
		ActorUserId: fixture.actorID.String(), SessionId: fixture.session.Snapshot().ID.String(),
		ActionId: operationID.Value(), ExpectedStateVersion: 1, RequestDigest: digest[:], AllowOwnerAcquire: true,
		Command: envelopeToWire(fixture.session.Snapshot().VersionKey, internalMessage("round.roll", []byte("roll"))),
	})
	response, err := fixture.service.GameAction(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.ownership.ensureCalls != 1 || fixture.ownership.lastAction.OwnershipEpoch != 0 {
		t.Fatalf("ensure calls=%d action=%+v", fixture.ownership.ensureCalls, fixture.ownership.lastAction)
	}
	if response.Msg.GetSession().GetSessionId() != fixture.session.Snapshot().ID.String() ||
		response.Msg.GetProjection().GetView().GetMessageType() != "viewer.state" ||
		response.Msg.GetReceipt().GetActionId() != operationID.Value() {
		t.Fatalf("action response = %+v", response.Msg)
	}
}

func TestOwnerServiceReturnsOnlyReadyRoute(t *testing.T) {
	fixture := newInternalGameFixture(t)
	response, err := fixture.service.ResolveOwner(t.Context(), connect.NewRequest(&realtimev1.ResolveOwnerRequest{
		SessionId: fixture.session.Snapshot().ID.String(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if response.Msg.GetAddress() != "http://realtime-a.internal:8091" ||
		response.Msg.GetOwnershipEpoch() != fixture.session.Snapshot().OwnershipEpoch {
		t.Fatalf("owner route = %+v", response.Msg)
	}
}

func TestOwnerServiceEventProjectionUsesAtomicRuntimeCursor(t *testing.T) {
	fixture := newInternalGameFixture(t)
	response, err := fixture.service.GetEventProjection(t.Context(), connect.NewRequest(&realtimev1.GetEventProjectionRequest{
		SessionId: fixture.session.Snapshot().ID.String(), AfterStateVersion: 0,
		Viewer: viewerToWire(game.Viewer{Kind: game.ViewerPlayer, UserID: game.Identifier(fixture.actorID.String()), SeatIndex: 2}),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if fixture.sessions.getCalls != 0 || response.Msg.GetSession().GetStateVersion() != fixture.session.Snapshot().State.StateVersion ||
		len(response.Msg.GetMessages()) != 1 || response.Msg.GetMessages()[0].GetMessageType() != "viewer.delta" {
		t.Fatalf("repository reads=%d response=%+v", fixture.sessions.getCalls, response.Msg)
	}
}

func TestPrivateOwnerHandlerRequiresExactInternalToken(t *testing.T) {
	fixture := newInternalGameFixture(t)
	token := string(bytes.Repeat([]byte{'t'}, 32))
	auth, err := NewTokenInterceptor(token)
	if err != nil {
		t.Fatal(err)
	}
	path, handler := realtimev1connect.NewOwnerServiceHandler(
		fixture.service,
		connect.WithInterceptors(auth, ErrorInterceptor()),
	)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := realtimev1connect.NewOwnerServiceClient(server.Client(), server.URL)
	requestMessage := &realtimev1.GetProjectionRequest{
		SessionId: fixture.session.Snapshot().ID.String(),
		Viewer:    viewerToWire(game.Viewer{Kind: game.ViewerPlayer, UserID: game.Identifier(fixture.actorID.String()), SeatIndex: 2}),
	}

	for _, supplied := range []string{"", "wrong-credential"} {
		request := connect.NewRequest(requestMessage)
		request.Header().Set(InternalTokenHeader, supplied)
		if _, err := client.GetProjection(t.Context(), request); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("credential %q error = %v", supplied, err)
		}
	}
	authorized := connect.NewRequest(requestMessage)
	authorized.Header().Set(InternalTokenHeader, token)
	response, err := client.GetProjection(t.Context(), authorized)
	if err != nil || response.Msg.GetProjection().GetStateVersion() != 1 {
		t.Fatalf("authorized projection=%+v error=%v", response, err)
	}
}

type internalGameFixture struct {
	service   *Service
	runtime   *fakeInternalRuntime
	ownership *fakeInternalOwnership
	sessions  *fakeInternalSessions
	session   gameruntime.Session
	actorID   uuid.UUID
}

func newInternalGameFixture(t testing.TB) internalGameFixture {
	t.Helper()
	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	actorID := uuid.New()
	session, err := gameruntime.RestoreSession(gameruntime.SessionSnapshot{
		ID: uuid.New(), RoomID: uuid.New(),
		VersionKey:     game.VersionKey{GameID: "liars-dice", Engine: "1.0.0", Protocol: "1.0.0", Client: "1.0.0"},
		OwnershipEpoch: 7, Participants: []gameruntime.Participant{{UserID: actorID, SeatIndex: 2}},
		State: game.Snapshot{
			SnapshotVersion: 1, StateVersion: 1, State: internalMessage("round.state", []byte("authoritative")),
		},
		Status: gameruntime.StatusActive, StartedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	projection := game.Projection{
		View: internalMessage("viewer.state", []byte("safe")), AllowedActions: []game.Identifier{"round.roll"},
	}
	runtime := &fakeInternalRuntime{session: session, projection: projection}
	ownership := &fakeInternalOwnership{session: session, projection: projection}
	routes := &fakeInternalRoutes{lease: redisstore.SessionLease{
		SessionID: session.Snapshot().ID, Owner: "realtime-a", Address: "http://realtime-a.internal:8091",
		Ready: true, OwnershipEpoch: session.Snapshot().OwnershipEpoch,
	}}
	sessions := &fakeInternalSessions{session: session}
	service, err := NewService(runtime, ownership, sessions, routes)
	if err != nil {
		t.Fatal(err)
	}
	return internalGameFixture{service: service, runtime: runtime, ownership: ownership, sessions: sessions, session: session, actorID: actorID}
}

type fakeInternalRuntime struct {
	session    gameruntime.Session
	projection game.Projection
}

func (*fakeInternalRuntime) Start(context.Context, gameruntime.StartCommand) (roomdomain.Room, gameruntime.Session, error) {
	return roomdomain.Room{}, gameruntime.Session{}, gameruntime.ErrInvalidSessionInput
}

func (runtime *fakeInternalRuntime) ProjectCurrent(context.Context, uuid.UUID, game.Viewer) (gameruntime.Session, game.Projection, error) {
	return runtime.session, runtime.projection, nil
}

func (runtime *fakeInternalRuntime) ProjectEventsCurrent(context.Context, uuid.UUID, uint64, game.Viewer) (gameruntime.Session, game.EventProjection, game.Projection, bool, error) {
	return runtime.session, game.EventProjection{Messages: []game.Message{internalMessage("viewer.delta", nil)}}, game.Projection{}, false, nil
}

func (runtime *fakeInternalRuntime) ProjectReplayCurrent(context.Context, uuid.UUID, game.Viewer, game.ReplayAccessPolicy) (gameruntime.Session, game.Projection, error) {
	return runtime.session, runtime.projection, nil
}

type fakeInternalOwnership struct {
	session     gameruntime.Session
	projection  game.Projection
	ensureCalls int
	lastAction  gameruntime.ActionCommand
}

func (ownership *fakeInternalOwnership) EnsureOwned(context.Context, uuid.UUID) (uint64, error) {
	ownership.ensureCalls++
	return ownership.session.Snapshot().OwnershipEpoch, nil
}

func (ownership *fakeInternalOwnership) HandleAction(_ context.Context, command gameruntime.ActionCommand) (gameruntime.ActionResult, error) {
	ownership.lastAction = command
	operationID, err := idempotency.ParseOperationID(string(command.ActionID))
	if err != nil {
		return gameruntime.ActionResult{}, err
	}
	receipt, err := gameruntime.NewActionReceipt(gameruntime.ActionReceiptSnapshot{
		Key: gameruntime.ActionKey{
			SessionID: command.SessionID, ActorUserID: command.ActorUserID, ActionID: operationID,
		},
		RequestDigest: *command.RequestDigest, ResultCode: gameruntime.ResultCodeAccepted,
		ResultDigest: idempotency.Digest(sha256.Sum256([]byte("accepted"))),
		StateVersion: ownership.session.Snapshot().State.StateVersion, CommittedAt: ownership.session.Snapshot().UpdatedAt,
	})
	if err != nil {
		return gameruntime.ActionResult{}, err
	}
	return gameruntime.ActionResult{Session: ownership.session, Receipt: receipt, Projection: ownership.projection}, nil
}

func (*fakeInternalOwnership) HandleTimer(context.Context, gameruntime.DueTimer) (gameruntime.TimerCommitResult, error) {
	return gameruntime.TimerCommitResult{}, gameruntime.ErrInvalidSessionInput
}

func (*fakeInternalOwnership) HandleSystem(context.Context, gameruntime.SystemCommand) (gameruntime.SystemCommitResult, error) {
	return gameruntime.SystemCommitResult{}, gameruntime.ErrInvalidSessionInput
}

type fakeInternalSessions struct {
	session  gameruntime.Session
	getCalls int
}

func (sessions *fakeInternalSessions) Get(context.Context, uuid.UUID) (gameruntime.Session, error) {
	sessions.getCalls++
	return sessions.session, nil
}

type fakeInternalRoutes struct{ lease redisstore.SessionLease }

func (routes *fakeInternalRoutes) LookupSessionLease(context.Context, uuid.UUID) (redisstore.SessionLease, error) {
	return routes.lease, nil
}

func internalMessage(messageType game.Identifier, payload []byte) game.Message {
	return game.Message{MessageType: messageType, SchemaVersion: 1, Payload: append([]byte(nil), payload...)}
}

func internalOperationID(t testing.TB, marker byte) idempotency.OperationID {
	t.Helper()
	operationID, err := idempotency.NewOperationID(bytes.Repeat([]byte{marker}, 16))
	if err != nil {
		t.Fatal(err)
	}
	return operationID
}

var _ Runtime = (*fakeInternalRuntime)(nil)
var _ Ownership = (*fakeInternalOwnership)(nil)
