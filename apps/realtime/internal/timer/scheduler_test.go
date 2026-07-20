package timer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/apps/realtime/internal/owner"
	"github.com/iFTY-R/game-night/platform/clock"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

func TestSchedulerClaimsAndDispatchesIndependentDueCandidates(t *testing.T) {
	now := time.Date(2026, time.July, 20, 16, 0, 0, 0, time.UTC)
	ownedElsewhere, stale, accepted := timerCandidate(now, 1), timerCandidate(now, 2), timerCandidate(now, 3)
	store := &fakeTimerStore{due: []gameruntime.DueTimer{ownedElsewhere, stale, accepted}}
	ownership := &fakeTimerOwnership{
		ensureErrors: map[uuid.UUID]error{ownedElsewhere.SessionID: owner.ErrOwnedElsewhere},
		handleErrors: map[uuid.UUID]error{stale.SessionID: gameruntime.ErrStateVersionConflict},
	}
	scheduler := newTestScheduler(t, store, ownership, clock.NewFake(now))

	scheduler.scan(t.Context())

	if store.calls != 1 || !store.lastDueAt.Equal(now) || store.lastLimit != 8 {
		t.Fatalf("store calls=%d due=%v limit=%d", store.calls, store.lastDueAt, store.lastLimit)
	}
	if len(ownership.ensured) != 3 || len(ownership.handled) != 2 || ownership.handled[1].SessionID != accepted.SessionID {
		t.Fatalf("ensured=%v handled=%v", ownership.ensured, ownership.handled)
	}
}

func TestSchedulerBoundsEachCandidateOperation(t *testing.T) {
	now := time.Date(2026, time.July, 20, 16, 30, 0, 0, time.UTC)
	candidate := timerCandidate(now, 4)
	store := &fakeTimerStore{due: []gameruntime.DueTimer{candidate}}
	ownership := &fakeTimerOwnership{blockHandle: true}
	scheduler := newTestScheduler(t, store, ownership, clock.NewFake(now))
	scheduler.config.OperationTimeout = 100 * time.Millisecond

	startedAt := time.Now()
	scheduler.scan(t.Context())
	if elapsed := time.Since(startedAt); elapsed < 90*time.Millisecond || elapsed > time.Second {
		t.Fatalf("operation elapsed = %v", elapsed)
	}
	if len(ownership.handled) != 1 {
		t.Fatalf("handled = %v", ownership.handled)
	}
}

func TestSchedulerRunStopsWithContext(t *testing.T) {
	now := time.Date(2026, time.July, 20, 17, 0, 0, 0, time.UTC)
	store := &fakeTimerStore{called: make(chan struct{}, 1)}
	scheduler := newTestScheduler(t, store, &fakeTimerOwnership{}, clock.NewFake(now))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()
	select {
	case <-store.called:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not perform initial scan")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
}

func TestNewSchedulerRejectsInvalidConfiguration(t *testing.T) {
	validStore := &fakeTimerStore{}
	validOwnership := &fakeTimerOwnership{}
	validClock := clock.NewFake(time.Now())
	validLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	validConfig := Config{ScanInterval: time.Second, OperationTimeout: time.Second, BatchSize: 1}
	tests := []struct {
		name      string
		store     CandidateStore
		ownership Ownership
		clock     clock.Clock
		logger    *slog.Logger
		config    Config
	}{
		{name: "nil store", ownership: validOwnership, clock: validClock, logger: validLogger, config: validConfig},
		{name: "nil ownership", store: validStore, clock: validClock, logger: validLogger, config: validConfig},
		{name: "short interval", store: validStore, ownership: validOwnership, clock: validClock, logger: validLogger,
			config: Config{ScanInterval: time.Millisecond, OperationTimeout: time.Second, BatchSize: 1}},
		{name: "zero batch", store: validStore, ownership: validOwnership, clock: validClock, logger: validLogger,
			config: Config{ScanInterval: time.Second, OperationTimeout: time.Second}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := NewScheduler(testCase.store, testCase.ownership, testCase.clock, testCase.logger, testCase.config)
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func newTestScheduler(t testing.TB, store CandidateStore, ownership Ownership, source clock.Clock) *Scheduler {
	t.Helper()
	scheduler, err := NewScheduler(store, ownership, source, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{
		ScanInterval: time.Second, OperationTimeout: time.Second, BatchSize: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	return scheduler
}

func timerCandidate(now time.Time, marker byte) gameruntime.DueTimer {
	return gameruntime.DueTimer{
		SessionID: uuid.NewSHA1(uuid.NameSpaceOID, []byte{marker}), TimerID: game.Identifier("turn.timeout"),
		ExpectedStateVersion: 1, DueAt: now, Message: game.Message{MessageType: "turn.timeout", SchemaVersion: 1},
	}
}

type fakeTimerStore struct {
	due       []gameruntime.DueTimer
	err       error
	calls     int
	lastDueAt time.Time
	lastLimit uint32
	called    chan struct{}
}

func (store *fakeTimerStore) ListDueTimers(_ context.Context, dueAt time.Time, limit uint32) ([]gameruntime.DueTimer, error) {
	store.calls++
	store.lastDueAt, store.lastLimit = dueAt, limit
	if store.called != nil {
		select {
		case store.called <- struct{}{}:
		default:
		}
	}
	return append([]gameruntime.DueTimer(nil), store.due...), store.err
}

type fakeTimerOwnership struct {
	ensureErrors map[uuid.UUID]error
	handleErrors map[uuid.UUID]error
	ensured      []uuid.UUID
	handled      []gameruntime.DueTimer
	blockHandle  bool
}

func (ownership *fakeTimerOwnership) EnsureOwned(_ context.Context, sessionID uuid.UUID) (uint64, error) {
	ownership.ensured = append(ownership.ensured, sessionID)
	return 1, ownership.ensureErrors[sessionID]
}

func (ownership *fakeTimerOwnership) HandleTimer(ctx context.Context, due gameruntime.DueTimer) (gameruntime.TimerCommitResult, error) {
	ownership.handled = append(ownership.handled, due)
	if ownership.blockHandle {
		<-ctx.Done()
		return gameruntime.TimerCommitResult{}, ctx.Err()
	}
	return gameruntime.TimerCommitResult{}, ownership.handleErrors[due.SessionID]
}
