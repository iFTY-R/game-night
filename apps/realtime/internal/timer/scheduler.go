// Package timer recovers persisted game timers and dispatches them through ownership fencing.
package timer

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/apps/realtime/internal/owner"
	"github.com/iFTY-R/game-night/platform/clock"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
)

const maximumBatchSize uint32 = 1024

var ErrInvalidConfig = errors.New("invalid realtime timer scheduler configuration")

// CandidateStore returns non-locking PostgreSQL candidates; authoritative validation remains in HandleTimer.
type CandidateStore interface {
	ListDueTimers(context.Context, time.Time, uint32) ([]gameruntime.DueTimer, error)
}

// Ownership claims a session and executes a timer with its process-local PostgreSQL fencing epoch.
type Ownership interface {
	EnsureOwned(context.Context, uuid.UUID) (uint64, error)
	HandleTimer(context.Context, gameruntime.DueTimer) (gameruntime.TimerCommitResult, error)
}

// Config limits recovery scan pressure and prevents one stalled command from blocking later candidates indefinitely.
type Config struct {
	ScanInterval     time.Duration
	OperationTimeout time.Duration
	BatchSize        uint32
}

// Scheduler periodically recovers due PostgreSQL timers; it never treats Redis as timer authority.
type Scheduler struct {
	store     CandidateStore
	ownership Ownership
	clock     clock.Clock
	logger    *slog.Logger
	config    Config
}

// NewScheduler validates all process-owned dependencies and resource bounds.
func NewScheduler(store CandidateStore, ownership Ownership, source clock.Clock, logger *slog.Logger, config Config) (*Scheduler, error) {
	if store == nil || ownership == nil || source == nil || logger == nil ||
		config.ScanInterval < 10*time.Millisecond || config.ScanInterval > time.Minute ||
		config.OperationTimeout < 100*time.Millisecond || config.OperationTimeout > time.Minute ||
		config.BatchSize == 0 || config.BatchSize > maximumBatchSize {
		return nil, ErrInvalidConfig
	}
	return &Scheduler{store: store, ownership: ownership, clock: source, logger: logger, config: config}, nil
}

// Run performs an immediate recovery scan and then scans serially so slow database calls cannot overlap indefinitely.
func (scheduler *Scheduler) Run(ctx context.Context) error {
	if scheduler == nil || ctx == nil {
		return ErrInvalidConfig
	}
	ticker := time.NewTicker(scheduler.config.ScanInterval)
	defer ticker.Stop()
	scheduler.scan(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			scheduler.scan(ctx)
		}
	}
}

// scan isolates every candidate so a stale timer or another instance's lease cannot starve later due work.
func (scheduler *Scheduler) scan(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	due, err := scheduler.store.ListDueTimers(ctx, scheduler.clock.Now(), scheduler.config.BatchSize)
	if err != nil {
		if ctx.Err() == nil {
			scheduler.logger.ErrorContext(ctx, "list due game timers", "error", err)
		}
		return
	}
	for _, candidate := range due {
		if ctx.Err() != nil {
			return
		}
		operationCtx, cancel := context.WithTimeout(ctx, scheduler.config.OperationTimeout)
		_, claimErr := scheduler.ownership.EnsureOwned(operationCtx, candidate.SessionID)
		if claimErr == nil {
			_, claimErr = scheduler.ownership.HandleTimer(operationCtx, candidate)
		}
		cancel()
		if claimErr != nil && !expectedCandidateError(claimErr) && ctx.Err() == nil {
			scheduler.logger.WarnContext(ctx, "dispatch due game timer",
				"session_id", candidate.SessionID.String(), "timer_id", candidate.TimerID, "error", claimErr)
		}
	}
}

func expectedCandidateError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, owner.ErrOwnedElsewhere) || errors.Is(err, owner.ErrOwnershipUnavailable) ||
		errors.Is(err, owner.ErrLeaseLost) || errors.Is(err, owner.ErrClosed) ||
		errors.Is(err, gameruntime.ErrSessionTerminal) || errors.Is(err, gameruntime.ErrSessionSuspended) ||
		errors.Is(err, gameruntime.ErrOwnershipLost) || errors.Is(err, gameruntime.ErrStateVersionConflict) ||
		errors.Is(err, gameruntime.ErrTimerNotFound)
}
