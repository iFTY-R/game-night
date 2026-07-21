package owner

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/clock"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	redisstore "github.com/iFTY-R/game-night/platform/persistence/redis"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

func TestEnsureOwnedClaimsRedisBeforePostgreSQLEpochAndCoalesces(t *testing.T) {
	t.Parallel()
	fixture := newOwnerFixture(t)
	fixture.coordinator.acquireGate = make(chan struct{})

	results := make(chan uint64, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			epoch, err := fixture.manager.EnsureOwned(context.Background(), fixture.sessionID)
			results <- epoch
			errs <- err
		}()
	}
	<-fixture.coordinator.acquireStarted
	close(fixture.coordinator.acquireGate)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("EnsureOwned() error = %v", err)
		}
		if epoch := <-results; epoch != 1 {
			t.Fatalf("EnsureOwned() epoch = %d, want 1", epoch)
		}
	}
	if fixture.coordinator.acquireCalls != 1 {
		t.Fatalf("AcquireSessionLease() calls = %d, want 1", fixture.coordinator.acquireCalls)
	}
	if fixture.sessions.acquireCalls != 1 {
		t.Fatalf("AcquireOwnershipCAS() calls = %d, want 1", fixture.sessions.acquireCalls)
	}
	if got := fixture.order.snapshot(); len(got) != 3 || got[0] != "redis.acquire" ||
		got[1] != "postgres.acquire" || got[2] != "redis.promote" {
		t.Fatalf("claim order = %v, want Redis claim, PostgreSQL fence, then Redis ready", got)
	}
}

func TestHandleActionUsesHeldEpochAndPublishesCommittedVersion(t *testing.T) {
	t.Parallel()
	fixture := newOwnerFixture(t)
	if _, err := fixture.manager.EnsureOwned(context.Background(), fixture.sessionID); err != nil {
		t.Fatalf("EnsureOwned() error = %v", err)
	}

	result, err := fixture.manager.HandleAction(context.Background(), gameruntime.ActionCommand{
		SessionID: fixture.sessionID, OwnershipEpoch: 999,
	})
	if err != nil {
		t.Fatalf("HandleAction() error = %v", err)
	}
	if result.Session.Snapshot().ID != fixture.sessionID {
		t.Fatalf("HandleAction() session = %s", result.Session.Snapshot().ID)
	}
	if fixture.runtime.actionEpoch != 1 {
		t.Fatalf("runtime action epoch = %d, want held epoch 1", fixture.runtime.actionEpoch)
	}
	if len(fixture.publisher.events) != 1 || fixture.publisher.events[0].SessionID != fixture.sessionID ||
		fixture.publisher.events[0].StateVersion != 1 {
		t.Fatalf("fanout events = %+v", fixture.publisher.events)
	}
}

func TestRenewalLossCancelsInFlightActionAndRejectsLaterCommands(t *testing.T) {
	t.Parallel()
	fixture := newOwnerFixture(t)
	fixture.runtime.blockAction = true
	fixture.coordinator.renewed = false
	if _, err := fixture.manager.EnsureOwned(context.Background(), fixture.sessionID); err != nil {
		t.Fatalf("EnsureOwned() error = %v", err)
	}

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	runDone := make(chan error, 1)
	go func() { runDone <- fixture.manager.Run(runCtx) }()
	actionDone := make(chan error, 1)
	go func() {
		_, err := fixture.manager.HandleAction(context.Background(), gameruntime.ActionCommand{SessionID: fixture.sessionID})
		actionDone <- err
	}()
	select {
	case <-fixture.runtime.actionStarted:
	case <-time.After(time.Second):
		t.Fatal("runtime action did not start")
	}
	select {
	case err := <-actionDone:
		if !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("in-flight action error = %v, want ErrLeaseLost", err)
		}
	case <-time.After(time.Second):
		t.Fatal("lease loss did not cancel in-flight action")
	}
	if _, err := fixture.manager.HandleAction(context.Background(), gameruntime.ActionCommand{SessionID: fixture.sessionID}); !errors.Is(err, ErrOwnershipUnavailable) {
		t.Fatalf("later action error = %v, want ErrOwnershipUnavailable", err)
	}
	cancelRun()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop")
	}
}

func TestCloseCancelsCommandsAndReleasesExactLeaseOnce(t *testing.T) {
	t.Parallel()
	fixture := newOwnerFixture(t)
	if _, err := fixture.manager.EnsureOwned(context.Background(), fixture.sessionID); err != nil {
		t.Fatalf("EnsureOwned() error = %v", err)
	}
	if err := fixture.manager.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := fixture.manager.Close(context.Background()); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if fixture.coordinator.releaseCalls != 1 {
		t.Fatalf("ReleaseSessionLease() calls = %d, want 1", fixture.coordinator.releaseCalls)
	}
	if _, err := fixture.manager.EnsureOwned(context.Background(), fixture.sessionID); !errors.Is(err, ErrClosed) {
		t.Fatalf("EnsureOwned() after close error = %v, want ErrClosed", err)
	}
}

func TestHandleSystemForTerminalSessionUsesPersistedEpochWithoutClaim(t *testing.T) {
	t.Parallel()
	fixture := newOwnerFixture(t)
	snapshot := fixture.sessions.session.Snapshot()
	endedAt := snapshot.UpdatedAt.Add(time.Second)
	snapshot.Status = gameruntime.StatusFinished
	snapshot.OwnershipEpoch = 7
	snapshot.Timers = nil
	snapshot.NextDeadlineAt = time.Time{}
	snapshot.UpdatedAt = endedAt
	snapshot.EndedAt = endedAt
	terminal, err := gameruntime.RestoreSession(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	fixture.sessions.session = terminal
	fixture.runtime.session = terminal

	result, err := fixture.manager.HandleSystem(t.Context(), gameruntime.SystemCommand{
		SessionID: fixture.sessionID, OwnershipEpoch: 999,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Session.Snapshot().Status != gameruntime.StatusFinished || fixture.runtime.systemCalls != 1 ||
		fixture.runtime.systemEpoch != 7 || fixture.coordinator.acquireCalls != 0 || len(fixture.publisher.events) != 0 {
		t.Fatalf(
			"result=%+v system calls=%d epoch=%d acquire calls=%d fanout=%+v",
			result, fixture.runtime.systemCalls, fixture.runtime.systemEpoch, fixture.coordinator.acquireCalls, fixture.publisher.events,
		)
	}
}

func TestHandleSystemDoesNotBypassClosedManagerForTerminalSession(t *testing.T) {
	t.Parallel()
	fixture := newOwnerFixture(t)
	snapshot := fixture.sessions.session.Snapshot()
	endedAt := snapshot.UpdatedAt.Add(time.Second)
	snapshot.Status = gameruntime.StatusFinished
	snapshot.Timers = nil
	snapshot.NextDeadlineAt = time.Time{}
	snapshot.UpdatedAt = endedAt
	snapshot.EndedAt = endedAt
	terminal, err := gameruntime.RestoreSession(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	fixture.sessions.session = terminal
	fixture.runtime.session = terminal
	if err := fixture.manager.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.manager.HandleSystem(t.Context(), gameruntime.SystemCommand{SessionID: fixture.sessionID}); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed manager error=%v", err)
	}
	if fixture.runtime.systemCalls != 0 {
		t.Fatalf("runtime system calls=%d, want 0", fixture.runtime.systemCalls)
	}
}

type ownerFixture struct {
	sessionID   uuid.UUID
	order       *callOrder
	coordinator *fakeCoordinator
	sessions    *fakeSessionStore
	runtime     *fakeRuntime
	publisher   *fakePublisher
	manager     *Manager
}

func newOwnerFixture(t *testing.T) ownerFixture {
	t.Helper()
	sessionID := uuid.MustParse("018f4f8e-7f1b-7cc4-8c43-2b561acb9081")
	startedAt := time.Date(2026, 7, 20, 1, 0, 0, 0, time.UTC)
	session, err := gameruntime.RestoreSession(gameruntime.SessionSnapshot{
		ID:         sessionID,
		RoomID:     uuid.MustParse("018f4f8e-7f1b-7cc4-8c43-2b561acb9082"),
		VersionKey: game.VersionKey{GameID: "dice", Engine: "1.0.0", Protocol: "1.0.0", Client: "1.0.0"},
		Participants: []gameruntime.Participant{{
			UserID: uuid.MustParse("018f4f8e-7f1b-7cc4-8c43-2b561acb9083"), SeatIndex: 0,
		}},
		State: game.Snapshot{
			SnapshotVersion: 1, StateVersion: 1,
			State: game.Message{MessageType: "state", SchemaVersion: 1, Payload: []byte{1}},
		},
		Status: gameruntime.StatusActive, StartedAt: startedAt, UpdatedAt: startedAt,
	})
	if err != nil {
		t.Fatalf("RestoreSession() error = %v", err)
	}
	order := &callOrder{}
	coordinator := &fakeCoordinator{
		order: order, acquired: true, promoted: true, renewed: true, released: true,
		acquireStarted: make(chan struct{}, 1),
	}
	sessions := &fakeSessionStore{order: order, session: session}
	runtime := &fakeRuntime{session: session, actionStarted: make(chan struct{}, 1)}
	publisher := &fakePublisher{}
	manager, err := NewManager(
		coordinator, sessions, runtime, publisher, clock.NewFake(startedAt.Add(time.Second)),
		Config{InstanceID: "realtime-a", Address: "http://realtime-a.internal:8091", LeaseTTL: time.Second, RenewInterval: 10 * time.Millisecond},
	)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return ownerFixture{
		sessionID: sessionID, order: order, coordinator: coordinator,
		sessions: sessions, runtime: runtime, publisher: publisher, manager: manager,
	}
}

type callOrder struct {
	mu     sync.Mutex
	values []string
}

func (order *callOrder) add(value string) {
	order.mu.Lock()
	defer order.mu.Unlock()
	order.values = append(order.values, value)
}

func (order *callOrder) snapshot() []string {
	order.mu.Lock()
	defer order.mu.Unlock()
	return append([]string(nil), order.values...)
}

type fakeCoordinator struct {
	mu             sync.Mutex
	order          *callOrder
	acquired       bool
	promoted       bool
	renewed        bool
	released       bool
	acquireCalls   int
	releaseCalls   int
	acquireStarted chan struct{}
	acquireGate    chan struct{}
}

func (coordinator *fakeCoordinator) PromoteSessionLease(
	_ context.Context,
	lease redisstore.SessionLease,
	epoch uint64,
) (redisstore.SessionLease, bool, error) {
	coordinator.order.add("redis.promote")
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if !coordinator.promoted {
		return redisstore.SessionLease{}, false, nil
	}
	lease.Ready = true
	lease.OwnershipEpoch = epoch
	return lease, true, nil
}

func (coordinator *fakeCoordinator) AcquireSessionLease(
	ctx context.Context,
	sessionID uuid.UUID,
	ownerID, address string,
) (redisstore.SessionLease, bool, error) {
	coordinator.mu.Lock()
	coordinator.acquireCalls++
	coordinator.mu.Unlock()
	coordinator.order.add("redis.acquire")
	select {
	case coordinator.acquireStarted <- struct{}{}:
	default:
	}
	if coordinator.acquireGate != nil {
		select {
		case <-ctx.Done():
			return redisstore.SessionLease{}, false, ctx.Err()
		case <-coordinator.acquireGate:
		}
	}
	return redisstore.SessionLease{
		SessionID: sessionID, Owner: ownerID, Address: address,
		Token: "AAAAAAAAAAAAAAAAAAAAAA",
	}, coordinator.acquired, nil
}

func (coordinator *fakeCoordinator) RenewSessionLease(context.Context, redisstore.SessionLease) (bool, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.renewed, nil
}

func (coordinator *fakeCoordinator) ReleaseSessionLease(context.Context, redisstore.SessionLease) (bool, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.releaseCalls++
	return coordinator.released, nil
}

type fakeSessionStore struct {
	mu           sync.Mutex
	order        *callOrder
	session      gameruntime.Session
	acquireCalls int
}

func (store *fakeSessionStore) Get(context.Context, uuid.UUID) (gameruntime.Session, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.session, nil
}

func (store *fakeSessionStore) AcquireOwnershipCAS(
	_ context.Context,
	_ gameruntime.Session,
	next gameruntime.Session,
) (gameruntime.Session, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.acquireCalls++
	store.order.add("postgres.acquire")
	store.session = next
	return next, nil
}

type fakeRuntime struct {
	mu            sync.Mutex
	session       gameruntime.Session
	actionEpoch   uint64
	systemEpoch   uint64
	systemCalls   int
	blockAction   bool
	actionStarted chan struct{}
}

func (runtime *fakeRuntime) HandleAction(ctx context.Context, command gameruntime.ActionCommand) (gameruntime.ActionResult, error) {
	runtime.mu.Lock()
	runtime.actionEpoch = command.OwnershipEpoch
	block := runtime.blockAction
	runtime.mu.Unlock()
	select {
	case runtime.actionStarted <- struct{}{}:
	default:
	}
	if block {
		<-ctx.Done()
		return gameruntime.ActionResult{}, context.Cause(ctx)
	}
	return gameruntime.ActionResult{Session: runtime.session}, nil
}

func (runtime *fakeRuntime) HandleTimer(context.Context, gameruntime.DueTimer, uint64) (gameruntime.TimerCommitResult, error) {
	return gameruntime.TimerCommitResult{Session: runtime.session}, nil
}

func (runtime *fakeRuntime) HandleSystem(_ context.Context, command gameruntime.SystemCommand) (gameruntime.SystemCommitResult, error) {
	runtime.mu.Lock()
	runtime.systemEpoch = command.OwnershipEpoch
	runtime.systemCalls++
	runtime.mu.Unlock()
	return gameruntime.SystemCommitResult{Session: runtime.session}, nil
}

type fakePublisher struct {
	mu     sync.Mutex
	events []redisstore.SessionFanoutEvent
}

func (publisher *fakePublisher) PublishSessionFanout(_ context.Context, event redisstore.SessionFanoutEvent) error {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	publisher.events = append(publisher.events, event)
	return nil
}
