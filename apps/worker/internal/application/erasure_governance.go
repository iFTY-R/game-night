package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	adminuser "github.com/iFTY-R/game-night/platform/admin/user"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/profile"
)

const (
	// workerErasedRealNameTombstone keeps profile erasure idempotent even though the profile aggregate forbids an empty plaintext value.
	workerErasedRealNameTombstone = "deleted profile"
)

// workerErasureGovernance supplies only the asynchronous profile-scrubbing primitive needed by the worker runtime.
// Synchronous delete and device flows stay on the reviewed request path and deliberately return unavailable here.
type workerErasureGovernance struct {
	adminuser.GovernanceExecutor
	profiles  profile.Repository
	protector *profile.PIIProtector
	clock     clock.Clock
}

func newWorkerErasureGovernance(
	governance adminuser.GovernanceExecutor,
	profiles profile.Repository,
	protector *profile.PIIProtector,
	source clock.Clock,
) adminuser.SingleUserGovernanceExecutor {
	if governance == nil || profiles == nil || protector == nil || source == nil {
		return nil
	}
	return &workerErasureGovernance{
		GovernanceExecutor: governance,
		profiles:           profiles,
		protector:          protector,
		clock:              source,
	}
}

func (governance *workerErasureGovernance) CountActiveDevices(context.Context, uuid.UUID, time.Time) (int32, error) {
	return 0, adminuser.ErrRepositoryUnavailable
}

func (governance *workerErasureGovernance) RevokeAllDevices(context.Context, uuid.UUID, time.Time) (int32, error) {
	return 0, adminuser.ErrRepositoryUnavailable
}

func (governance *workerErasureGovernance) HasPendingExport(context.Context, uuid.UUID) (bool, error) {
	return false, adminuser.ErrRepositoryUnavailable
}

func (governance *workerErasureGovernance) DeleteUser(context.Context, adminuser.DeleteUserCommand) (adminuser.DeleteUserResult, error) {
	return adminuser.DeleteUserResult{}, adminuser.ErrRepositoryUnavailable
}

func (governance *workerErasureGovernance) EraseUserProfile(ctx context.Context, userID uuid.UUID) error {
	if governance == nil || governance.profiles == nil || governance.protector == nil || governance.clock == nil || ctx == nil || userID == uuid.Nil {
		return adminuser.ErrInvalidInput
	}
	current, err := governance.profiles.GetByID(ctx, userID)
	if errors.Is(err, profile.ErrProfileNotFound) {
		return nil
	}
	if err != nil {
		return mapWorkerProfileErasureError(err)
	}
	// The domain forbids empty plaintext, so erasure overwrites the ciphertext with a non-identifying tombstone instead of leaving recoverable content.
	tombstone, err := governance.protector.EncryptRealName(userID, workerErasedRealNameTombstone)
	if err != nil {
		return mapWorkerProfileErasureError(err)
	}
	next, err := current.UpdateEncrypted(current.ProfileVersion(), tombstone, governance.clock.Now(), current.RealNameUpdatedBy())
	if err != nil {
		return mapWorkerProfileErasureError(err)
	}
	if _, err = governance.profiles.UpdateCAS(ctx, current, next); err != nil {
		return mapWorkerProfileErasureError(err)
	}
	return nil
}

func mapWorkerProfileErasureError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, profile.ErrInvalidProfileInput):
		return adminuser.ErrInvalidInput
	case errors.Is(err, profile.ErrProfileConcurrentTransition):
		return adminuser.ErrConflict
	case errors.Is(err, profile.ErrProfileIntegrity), errors.Is(err, profile.ErrPIIAuthentication), errors.Is(err, profile.ErrPIIKeyUnavailable):
		return adminuser.ErrIntegrity
	case errors.Is(err, profile.ErrProfileRepositoryUnavailable):
		return adminuser.ErrRepositoryUnavailable
	default:
		return adminuser.ErrRepositoryUnavailable
	}
}
