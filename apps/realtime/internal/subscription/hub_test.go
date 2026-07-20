package subscription

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	redisstore "github.com/iFTY-R/game-night/platform/persistence/redis"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

func TestHubProjectsInitialGapAndCoalescesFanoutWakeups(t *testing.T) {
	authorization := hubAuthorization()
	authorizer := &fakeHubAuthorizer{}
	runtime := &fakeHubRuntime{
		session: hubSession(t, authorization, 2),
		delta:   game.EventProjection{Messages: []game.Message{{MessageType: "viewer.delta", SchemaVersion: 1, Payload: []byte("safe")}}},
	}
	hub := newTestHub(t, authorizer, runtime)
	sink := newFakeHubSink()
	_, err := hub.Register(authorization, sink)
	if err != nil {
		t.Fatal(err)
	}
	update := receiveHubUpdate(t, sink)
	_, eventCalls := runtime.calls()
	if update.Snapshot() || !update.Delta.Valid() || update.StateVersion != 2 || eventCalls != 1 {
		t.Fatalf("update=%+v event calls=%d", update, eventCalls)
	}

	runtime.mu.Lock()
	runtime.session = hubSession(t, authorization, 3)
	runtime.mu.Unlock()
	sink.block = make(chan struct{})
	event := redisstore.SessionFanoutEvent{SessionID: authorization.SessionID, StateVersion: 3}
	for range 100 {
		if err := hub.Notify(event); err != nil {
			t.Fatal(err)
		}
	}
	close(sink.block)
	update = receiveHubUpdate(t, sink)
	_, eventCalls = runtime.calls()
	if update.StateVersion != 3 || eventCalls > 3 {
		t.Fatalf("update=%+v event calls=%d", update, eventCalls)
	}
}

func TestHubForcesSnapshotWhenAuthorizationMetadataChanges(t *testing.T) {
	authorization := hubAuthorization()
	refreshed := authorization
	refreshed.Host = false
	refreshed.RoomVersion++
	authorizer := &fakeHubAuthorizer{result: RefreshResult{Authorization: refreshed, SnapshotRequired: true}}
	runtime := &fakeHubRuntime{
		session: hubSession(t, authorization, 1),
		projection: game.Projection{
			View: game.Message{MessageType: "viewer.state", SchemaVersion: 1}, AllowedActions: []game.Identifier{"round.roll"},
		},
	}
	hub := newTestHub(t, authorizer, runtime)
	sink := newFakeHubSink()
	_, err := hub.Register(authorization, sink)
	if err != nil {
		t.Fatal(err)
	}
	update := receiveHubUpdate(t, sink)
	projectCalls, eventCalls := runtime.calls()
	if !update.Snapshot() || update.Host || projectCalls != 1 || eventCalls != 0 {
		t.Fatalf("update=%+v project calls=%d event calls=%d", update, projectCalls, eventCalls)
	}
}

func TestHubClosesSinkWhenPeriodicAuthorizationIsRevoked(t *testing.T) {
	authorization := hubAuthorization()
	authorizer := &fakeHubAuthorizer{err: ErrUnauthorized}
	runtime := &fakeHubRuntime{session: hubSession(t, authorization, 1)}
	hub := newTestHub(t, authorizer, runtime)
	sink := newFakeHubSink()
	_, err := hub.Register(authorization, sink)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case cause := <-sink.closed:
		if !errors.Is(cause, ErrUnauthorized) {
			t.Fatalf("close cause = %v", cause)
		}
	case <-time.After(time.Second):
		t.Fatal("revoked subscription did not close")
	}
}

func TestHubRunReconcilesWithoutRedisAndDrainsWorkers(t *testing.T) {
	authorization := hubAuthorization()
	authorizer := &fakeHubAuthorizer{}
	runtime := &fakeHubRuntime{
		session: hubSession(t, authorization, 1),
	}
	hub, err := NewHub(authorizer, runtime, HubConfig{ReconcileInterval: 10 * time.Millisecond, ProjectionTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	sink := newFakeHubSink()
	_, err = hub.Register(authorization, sink)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- hub.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for authorizer.callCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if authorizer.callCount() < 2 {
		t.Fatal("periodic reconciliation did not run")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
	defer closeCancel()
	if err := hub.Close(closeCtx, ErrHubClosed); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sink.closed:
	default:
		t.Fatal("sink was not closed during drain")
	}
}

func newTestHub(t testing.TB, authorizer AuthorizationRefresher, runtime ProjectionRuntime) *Hub {
	t.Helper()
	hub, err := NewHub(authorizer, runtime, HubConfig{ReconcileInterval: time.Minute, ProjectionTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = hub.Close(ctx, ErrHubClosed)
	})
	return hub
}

func hubAuthorization() Authorization {
	userID := uuid.New()
	return Authorization{
		UserID: userID, RoomID: uuid.New(), SessionID: uuid.New(),
		Viewer: game.Viewer{Kind: game.ViewerPlayer, UserID: game.Identifier(userID.String()), SeatIndex: 2},
		Cursor: 1, CurrentVersion: 1, RoomVersion: 2, MembershipVersion: 2, Host: true,
	}
}

func hubSession(t testing.TB, authorization Authorization, stateVersion uint64) gameruntime.Session {
	t.Helper()
	now := time.Date(2026, time.July, 20, 20, 0, 0, 0, time.UTC)
	session, err := gameruntime.RestoreSession(gameruntime.SessionSnapshot{
		ID: authorization.SessionID, RoomID: authorization.RoomID,
		VersionKey:     game.VersionKey{GameID: "liars-dice", Engine: "1.0.0", Protocol: "1.0.0", Client: "1.0.0"},
		OwnershipEpoch: 1,
		Participants:   []gameruntime.Participant{{UserID: authorization.UserID, SeatIndex: authorization.Viewer.SeatIndex}},
		State: game.Snapshot{
			SnapshotVersion: 1, StateVersion: stateVersion,
			State: game.Message{MessageType: "round.state", SchemaVersion: 1},
		},
		Status: gameruntime.StatusActive, StartedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

type fakeHubAuthorizer struct {
	mu     sync.Mutex
	result RefreshResult
	err    error
	calls  int
}

func (authorizer *fakeHubAuthorizer) Refresh(_ context.Context, previous Authorization) (RefreshResult, error) {
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	authorizer.calls++
	if authorizer.err != nil {
		return RefreshResult{}, authorizer.err
	}
	if authorizer.result.Authorization.UserID == uuid.Nil {
		return RefreshResult{Authorization: previous}, nil
	}
	return authorizer.result, nil
}

func (authorizer *fakeHubAuthorizer) callCount() int {
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	return authorizer.calls
}

type fakeHubRuntime struct {
	mu           sync.Mutex
	session      gameruntime.Session
	projection   game.Projection
	delta        game.EventProjection
	projectCalls int
	eventCalls   int
}

func (runtime *fakeHubRuntime) ProjectCurrent(context.Context, uuid.UUID, game.Viewer) (gameruntime.Session, game.Projection, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.projectCalls++
	return runtime.session, cloneProjection(runtime.projection), nil
}

func (runtime *fakeHubRuntime) ProjectEventsCurrent(_ context.Context, _ uuid.UUID, after uint64, _ game.Viewer) (gameruntime.Session, game.EventProjection, game.Projection, bool, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.eventCalls++
	if runtime.session.Snapshot().State.StateVersion == after {
		return runtime.session, game.EventProjection{}, game.Projection{}, false, nil
	}
	return runtime.session, cloneEventProjection(runtime.delta), game.Projection{}, false, nil
}

func (runtime *fakeHubRuntime) calls() (int, int) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.projectCalls, runtime.eventCalls
}

type fakeHubSink struct {
	updates chan Update
	closed  chan error
	block   chan struct{}
}

func newFakeHubSink() *fakeHubSink {
	return &fakeHubSink{updates: make(chan Update, 8), closed: make(chan error, 1)}
}

func (sink *fakeHubSink) Send(ctx context.Context, update Update) error {
	if sink.block != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-sink.block:
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case sink.updates <- update:
		return nil
	}
}

func (sink *fakeHubSink) Close(cause error) {
	select {
	case sink.closed <- cause:
	default:
	}
}

func receiveHubUpdate(t testing.TB, sink *fakeHubSink) Update {
	t.Helper()
	select {
	case update := <-sink.updates:
		return update
	case <-time.After(time.Second):
		t.Fatal("subscription update was not delivered")
		return Update{}
	}
}
