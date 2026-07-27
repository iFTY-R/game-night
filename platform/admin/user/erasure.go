package user

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

const (
	// erasureStepRevokeCredentials records that synchronous credential revocation already completed before the worker claim.
	erasureStepRevokeCredentials = "revoke_credentials"
	// erasureStepEraseProfile is the only step that still needs an external side effect from the worker.
	erasureStepEraseProfile = "erase_profile"
	// erasureStepEnqueueRoomCleanup preserves the durable workflow boundary after the synchronous delete command cleared any waiting-room membership.
	erasureStepEnqueueRoomCleanup = "enqueue_room_cleanup"
	// erasureStepComplete marks the last durable checkpoint before the worker closes the job.
	erasureStepComplete = "complete"
	// erasureExecutionFailedMessageKey keeps terminal erasure failures stable for operators and retry tooling.
	erasureExecutionFailedMessageKey = "admin.user.erasure.execution_failed"
)

// ProcessNextErasureJob claims one durable erasure workflow and drives it to a terminal state inside the current lease.
func (service *Service) ProcessNextErasureJob(ctx context.Context, workerID string) (bool, error) {
	if service == nil || service.jobs == nil || service.singleGovernance == nil || service.clock == nil || ctx == nil || !validBatchWorkerID(workerID) {
		return false, ErrInvalidInput
	}
	job, err := service.jobs.ClaimErasureJob(ctx, batchLeaseOwner(workerID, uuid.Nil), batchExecutionLease)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err = service.processClaimedErasureJob(ctx, job); err != nil {
		return false, err
	}
	return true, nil
}

func (service *Service) processClaimedErasureJob(ctx context.Context, job ErasureJob) error {
	current := job
	for {
		switch current.Step {
		case erasureStepRevokeCredentials:
			// DeleteUser already revoked credentials before it created this durable erasure job, so the worker only advances the checkpoint.
			next, err := service.jobs.AdvanceErasureJob(ctx, current, erasureStepEraseProfile, service.clock.Now())
			if err != nil {
				return service.failClaimedErasureJob(ctx, current)
			}
			current = next
		case erasureStepEraseProfile:
			if err := service.singleGovernance.EraseUserProfile(ctx, current.UserID); err != nil {
				return service.failClaimedErasureJob(ctx, current)
			}
			next, err := service.jobs.AdvanceErasureJob(ctx, current, erasureStepEnqueueRoomCleanup, service.clock.Now())
			if err != nil {
				return service.failClaimedErasureJob(ctx, current)
			}
			current = next
		case erasureStepEnqueueRoomCleanup:
			// The synchronous command already cleared the reviewed waiting-room membership; retain this checkpoint for future asynchronous room cleanup work.
			next, err := service.jobs.AdvanceErasureJob(ctx, current, erasureStepComplete, service.clock.Now())
			if err != nil {
				return service.failClaimedErasureJob(ctx, current)
			}
			current = next
		case erasureStepComplete:
			_, err := service.jobs.CompleteErasureJob(ctx, current, "succeeded", "", service.clock.Now())
			return err
		default:
			return service.failClaimedErasureJob(ctx, current)
		}
	}
}

func (service *Service) failClaimedErasureJob(ctx context.Context, job ErasureJob) error {
	_, err := service.jobs.CompleteErasureJob(ctx, job, "failed", erasureExecutionFailedMessageKey, service.clock.Now())
	return err
}
