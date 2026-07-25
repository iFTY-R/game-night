package checkpoint

import (
	"context"
	"errors"
	"time"

	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/clock"
)

var (
	// ErrScheduleUnavailable hides persistence and signing details from the worker log boundary.
	ErrScheduleUnavailable = errors.New("checkpoint schedule unavailable")
)

// ScheduleResult reports whether one durable checkpoint was enqueued for delivery.
type ScheduleResult struct {
	Enqueued bool
	Idle     bool
}

type checkpointPreparer interface {
	PrepareCheckpoint(audit.Head, time.Time) (audit.Checkpoint, error)
}

// Scheduler closes the low-traffic gap by creating due checkpoints without requiring another business write.
type Scheduler struct {
	unitOfWork audit.UnitOfWork
	preparer   checkpointPreparer
	policy     *audit.CheckpointHealthPolicy
	clock      clock.Clock
}

// NewScheduler binds the same signer and health policy used by sensitive-write guards to the worker loop.
func NewScheduler(
	unitOfWork audit.UnitOfWork,
	preparer checkpointPreparer,
	policy *audit.CheckpointHealthPolicy,
	source clock.Clock,
) (*Scheduler, error) {
	if unitOfWork == nil || preparer == nil || policy == nil || source == nil {
		return nil, ErrInvalidConfig
	}
	return &Scheduler{unitOfWork: unitOfWork, preparer: preparer, policy: policy, clock: source}, nil
}

// RunOnce atomically snapshots the current chain head and enqueues an immutable anchor when its hard limit is due.
func (scheduler *Scheduler) RunOnce(ctx context.Context) (ScheduleResult, error) {
	if scheduler == nil || scheduler.unitOfWork == nil || scheduler.preparer == nil || scheduler.policy == nil ||
		scheduler.clock == nil || ctx == nil {
		return ScheduleResult{}, ErrInvalidConfig
	}

	now := scheduler.clock.Now()
	result := ScheduleResult{Idle: true}
	err := scheduler.unitOfWork.Run(ctx, func(ctx context.Context, transaction audit.Transaction) error {
		head, err := transaction.Audit().ReadHead(ctx, audit.ChainAdmin)
		if err != nil {
			return err
		}
		if head.Sequence() == 0 {
			return nil
		}
		progress, err := transaction.Checkpoints().ReadCheckpointProgress(ctx, audit.ChainAdmin)
		if err != nil {
			return err
		}
		health, err := scheduler.policy.Evaluate(ctx, head.Sequence(), progress, now)
		if err != nil || !health.CheckpointDue() {
			return err
		}
		checkpoint, err := scheduler.preparer.PrepareCheckpoint(head, now)
		if err != nil {
			return err
		}
		if err = transaction.Checkpoints().AppendPendingCheckpoint(ctx, checkpoint); err != nil {
			return err
		}
		result = ScheduleResult{Enqueued: true}
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return ScheduleResult{}, ctx.Err()
		}
		return ScheduleResult{}, ErrScheduleUnavailable
	}
	return result, nil
}
