package secretresult

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/secretaccess"
	"github.com/iFTY-R/game-night/platform/security"
)

// Service centralizes result preparation, replay authorization, decryption, confirmation, and expiry.
type Service struct {
	cipher         *EnvelopeCipher
	clock          clock.Clock
	deviceAccess   *security.HMACKeyring[security.DeviceHMACKeyPurpose]
	recoveryAccess *security.HMACKeyring[security.UserChallengeKeyPurpose]
	adminAccess    *security.HMACKeyring[security.AdminSessionKeyPurpose]
}

// NewServiceWithAdminAccess enables authenticated administrator sessions to replay and confirm exact secret results.
func NewServiceWithAdminAccess(
	cipher *EnvelopeCipher,
	serviceClock clock.Clock,
	adminAccess *security.HMACKeyring[security.AdminSessionKeyPurpose],
) (*Service, error) {
	if cipher == nil || serviceClock == nil || adminAccess == nil {
		return nil, ErrInvalidInput
	}
	return &Service{cipher: cipher, clock: serviceClock, adminAccess: adminAccess}, nil
}

// NewServiceWithIdentityAccess enables both authenticated-device and consumed-recovery result authority.
func NewServiceWithIdentityAccess(
	cipher *EnvelopeCipher,
	serviceClock clock.Clock,
	deviceAccess *security.HMACKeyring[security.DeviceHMACKeyPurpose],
	recoveryAccess *security.HMACKeyring[security.UserChallengeKeyPurpose],
) (*Service, error) {
	if cipher == nil || serviceClock == nil || deviceAccess == nil || recoveryAccess == nil {
		return nil, ErrInvalidInput
	}
	return &Service{
		cipher: cipher, clock: serviceClock, deviceAccess: deviceAccess, recoveryAccess: recoveryAccess,
	}, nil
}

// NewService requires explicit cryptography and time dependencies so tests never use hidden wall-clock state.
func NewService(cipher *EnvelopeCipher, serviceClock clock.Clock) (*Service, error) {
	if cipher == nil || serviceClock == nil {
		return nil, ErrInvalidInput
	}
	return &Service{cipher: cipher, clock: serviceClock}, nil
}

// NewServiceWithDeviceAccess enables authenticated user-device result replay with signed concrete grants.
func NewServiceWithDeviceAccess(
	cipher *EnvelopeCipher,
	serviceClock clock.Clock,
	deviceAccess *security.HMACKeyring[security.DeviceHMACKeyPurpose],
) (*Service, error) {
	if cipher == nil || serviceClock == nil || deviceAccess == nil {
		return nil, ErrInvalidInput
	}
	return &Service{cipher: cipher, clock: serviceClock, deviceAccess: deviceAccess}, nil
}

// PrepareAvailable seals one plaintext before the caller's larger business transaction inserts it.
func (service *Service) PrepareAvailable(resultID uuid.UUID, binding Binding, plaintext []byte, secretTTL time.Duration) (Result, error) {
	if resultID == uuid.Nil || secretTTL <= 0 || secretTTL > MaximumSecretTTL {
		return Result{}, ErrInvalidInput
	}
	completedAt := canonicalTime(service.clock.Now())
	secretExpiresAt := completedAt.Add(secretTTL)
	payload, err := service.cipher.Seal(plaintext, binding, secretExpiresAt)
	if err != nil {
		return Result{}, err
	}
	return NewAvailable(resultID, binding, payload, completedAt, secretExpiresAt, secretExpiresAt.Add(MinimumTombstoneRetention))
}

// Resolve is an observation helper. A first execution must still insert its result inside the caller's business UoW.
func (service *Service) Resolve(ctx context.Context, unitOfWork UnitOfWork, binding Binding) (Resolution, error) {
	if unitOfWork == nil || binding.Validate() != nil {
		return Resolution{}, ErrInvalidInput
	}
	var resolution Resolution
	err := unitOfWork.Run(ctx, func(ctx context.Context, repository Repository) error {
		result, getErr := repository.GetByOperationForUpdate(ctx, binding.Key)
		if errors.Is(getErr, ErrNotFound) {
			resolution = Resolution{Kind: ExecuteNew}
			return nil
		}
		if getErr != nil {
			return getErr
		}
		resolved, resolveErr := result.Resolve(binding, service.clock.Now())
		resolution = resolved
		return resolveErr
	})
	if err != nil {
		return Resolution{}, err
	}
	return resolution, nil
}

// Open locks and rereads the exact result so a committed confirm or expiry can never be bypassed by a stale value.
func (service *Service) Open(
	ctx context.Context,
	unitOfWork UnitOfWork,
	resultID uuid.UUID,
	binding Binding,
	capability challenge.ReplayCapability,
) ([]byte, error) {
	if unitOfWork == nil || resultID == uuid.Nil || binding.Validate() != nil {
		return nil, ErrInvalidInput
	}
	if !challenge.AuthorizesReplay(capability, resultID, service.clock.Now()) {
		return nil, ErrReplayUnauthorized
	}
	var plaintext []byte
	err := unitOfWork.Run(ctx, func(ctx context.Context, repository Repository) error {
		current, getErr := repository.GetByOperationForUpdate(ctx, binding.Key)
		if errors.Is(getErr, ErrNotFound) {
			return ErrReplayUnauthorized
		}
		if getErr != nil {
			return getErr
		}
		snapshot := current.Snapshot()
		now := service.clock.Now()
		if snapshot.ID != resultID || !challenge.AuthorizesReplay(capability, resultID, now) {
			return ErrReplayUnauthorized
		}
		resolution, resolveErr := current.Resolve(binding, now)
		if resolveErr != nil {
			return resolveErr
		}
		if resolution.Kind != ReplayAvailable {
			return ErrSecretNoLongerAvailable
		}
		plaintext, resolveErr = service.cipher.open(snapshot.Payload, binding, snapshot.SecretExpiresAt)
		return resolveErr
	})
	if err != nil {
		clear(plaintext)
		return nil, err
	}
	return plaintext, nil
}

// OpenAuthorizedResult verifies and decrypts a result already locked by an outer actor transaction.
// The caller must retain the device and result row locks until its enclosing UnitOfWork commits.
func (service *Service) OpenAuthorizedResult(
	result Result,
	binding Binding,
	grant secretaccess.DeviceGrant,
) ([]byte, error) {
	if service == nil || service.cipher == nil || service.clock == nil || binding.Validate() != nil {
		return nil, ErrInvalidInput
	}
	snapshot := result.Snapshot()
	now := service.clock.Now()
	if snapshot.ID == uuid.Nil ||
		!secretaccess.VerifyDeviceGrant(service.deviceAccess, grant, snapshot.ID, binding.Key.ActorID, now) {
		return nil, ErrReplayUnauthorized
	}
	resolution, err := result.Resolve(binding, now)
	if err != nil {
		return nil, err
	}
	if resolution.Kind != ReplayAvailable {
		return nil, ErrSecretNoLongerAvailable
	}
	return service.cipher.open(snapshot.Payload, binding, snapshot.SecretExpiresAt)
}

// OpenRecoveryAuthorizedResult decrypts only the exact result authorized by a consumed recovery attempt.
func (service *Service) OpenRecoveryAuthorizedResult(
	result Result,
	binding Binding,
	grant secretaccess.RecoveryGrant,
) ([]byte, error) {
	if service == nil || service.cipher == nil || service.clock == nil || binding.Validate() != nil {
		return nil, ErrInvalidInput
	}
	snapshot := result.Snapshot()
	now := service.clock.Now()
	if snapshot.ID == uuid.Nil ||
		!secretaccess.VerifyRecoveryGrant(service.recoveryAccess, grant, snapshot.ID, binding.Key.ActorID, now) {
		return nil, ErrReplayUnauthorized
	}
	resolution, err := result.Resolve(binding, now)
	if err != nil {
		return nil, err
	}
	if resolution.Kind != ReplayAvailable {
		return nil, ErrSecretNoLongerAvailable
	}
	return service.cipher.open(snapshot.Payload, binding, snapshot.SecretExpiresAt)
}

// OpenAdminAuthorizedResult decrypts only the result bound to the authenticated administrator grant.
func (service *Service) OpenAdminAuthorizedResult(result Result, binding Binding, grant secretaccess.AdminGrant) ([]byte, error) {
	if service == nil || service.cipher == nil || service.clock == nil || binding.Validate() != nil {
		return nil, ErrInvalidInput
	}
	snapshot := result.Snapshot()
	now := service.clock.Now()
	if snapshot.ID == uuid.Nil || !secretaccess.VerifyAdminGrant(service.adminAccess, grant, snapshot.ID, binding.Key.ActorID, now) {
		return nil, ErrReplayUnauthorized
	}
	resolution, err := result.Resolve(binding, now)
	if err != nil {
		return nil, err
	}
	if resolution.Kind != ReplayAvailable {
		return nil, ErrSecretNoLongerAvailable
	}
	return service.cipher.open(snapshot.Payload, binding, snapshot.SecretExpiresAt)
}

// ConfirmDeviceAuthorizedResult erases a locked exact result after current-device authorization is revalidated.
func (service *Service) ConfirmDeviceAuthorizedResult(
	ctx context.Context,
	repository Repository,
	result Result,
	binding Binding,
	grant secretaccess.DeviceGrant,
) (Result, error) {
	snapshot := result.Snapshot()
	authorized := service != nil && service.clock != nil &&
		secretaccess.VerifyDeviceGrant(service.deviceAccess, grant, snapshot.ID, binding.Key.ActorID, service.clock.Now())
	return service.confirmAuthorizedResult(ctx, repository, result, binding, authorized)
}

// ConfirmRecoveryAuthorizedResult erases a locked exact result after the consumed attempt is reauthenticated.
func (service *Service) ConfirmRecoveryAuthorizedResult(
	ctx context.Context,
	repository Repository,
	result Result,
	binding Binding,
	grant secretaccess.RecoveryGrant,
) (Result, error) {
	snapshot := result.Snapshot()
	authorized := service != nil && service.clock != nil &&
		secretaccess.VerifyRecoveryGrant(service.recoveryAccess, grant, snapshot.ID, binding.Key.ActorID, service.clock.Now())
	return service.confirmAuthorizedResult(ctx, repository, result, binding, authorized)
}

// ConfirmAdminAuthorizedResult erases an exact result after revalidating the administrator capability.
func (service *Service) ConfirmAdminAuthorizedResult(
	ctx context.Context,
	repository Repository,
	result Result,
	binding Binding,
	grant secretaccess.AdminGrant,
) (Result, error) {
	snapshot := result.Snapshot()
	authorized := service != nil && service.clock != nil &&
		secretaccess.VerifyAdminGrant(service.adminAccess, grant, snapshot.ID, binding.Key.ActorID, service.clock.Now())
	return service.confirmAuthorizedResult(ctx, repository, result, binding, authorized)
}

func (service *Service) confirmAuthorizedResult(
	ctx context.Context,
	repository Repository,
	result Result,
	binding Binding,
	authorized bool,
) (Result, error) {
	if service == nil || service.clock == nil || ctx == nil || repository == nil || binding.Validate() != nil || !authorized {
		return Result{}, ErrReplayUnauthorized
	}
	now := service.clock.Now()
	snapshot := result.Snapshot()
	if snapshot.ID == uuid.Nil {
		return Result{}, ErrReplayUnauthorized
	}
	if _, err := result.Resolve(binding, now); err != nil {
		return Result{}, err
	}
	if snapshot.Status == StatusConfirmed {
		return result, nil
	}
	if snapshot.Status != StatusAvailable || !now.Before(snapshot.SecretExpiresAt) {
		return Result{}, ErrSecretNoLongerAvailable
	}
	return repository.ConfirmCAS(ctx, Confirmation{ResultID: snapshot.ID, Binding: binding, ConfirmedAt: now})
}

// Confirm erases secret material only for the exact result authorized by a consumed challenge.
func (service *Service) Confirm(
	ctx context.Context,
	unitOfWork UnitOfWork,
	resultID uuid.UUID,
	binding Binding,
	capability challenge.ReplayCapability,
) (Result, error) {
	if unitOfWork == nil || resultID == uuid.Nil || binding.Validate() != nil {
		return Result{}, ErrInvalidInput
	}
	if !challenge.AuthorizesReplay(capability, resultID, service.clock.Now()) {
		return Result{}, ErrReplayUnauthorized
	}
	var confirmed Result
	err := unitOfWork.Run(ctx, func(ctx context.Context, repository Repository) error {
		current, getErr := repository.GetByOperationForUpdate(ctx, binding.Key)
		if errors.Is(getErr, ErrNotFound) {
			return ErrReplayUnauthorized
		}
		if getErr != nil {
			return getErr
		}
		now := service.clock.Now()
		if !challenge.AuthorizesReplay(capability, resultID, now) {
			return ErrReplayUnauthorized
		}
		resolution, resolveErr := current.Resolve(binding, now)
		if resolveErr != nil {
			return resolveErr
		}
		snapshot := current.Snapshot()
		if snapshot.ID != resultID {
			return ErrReplayUnauthorized
		}
		if snapshot.Status == StatusConfirmed {
			confirmed = current
			return nil
		}
		if resolution.Kind != ReplayAvailable {
			return ErrSecretNoLongerAvailable
		}
		updated, updateErr := repository.ConfirmCAS(ctx, Confirmation{ResultID: resultID, Binding: binding, ConfirmedAt: now})
		confirmed = updated
		return updateErr
	})
	return confirmed, err
}

// Expire erases one due available result under the same transaction lock used by replay and confirmation.
func (service *Service) Expire(ctx context.Context, unitOfWork UnitOfWork, key Key) (Result, error) {
	if unitOfWork == nil || key.Validate() != nil {
		return Result{}, ErrInvalidInput
	}
	var expired Result
	err := unitOfWork.Run(ctx, func(ctx context.Context, repository Repository) error {
		current, getErr := repository.GetByOperationForUpdate(ctx, key)
		if getErr != nil {
			return getErr
		}
		snapshot := current.Snapshot()
		if snapshot.Status != StatusAvailable {
			expired = current
			return nil
		}
		now := service.clock.Now()
		if now.Before(snapshot.SecretExpiresAt) {
			return ErrConcurrentTransition
		}
		updated, updateErr := repository.ExpireCAS(ctx, current, now)
		expired = updated
		return updateErr
	})
	return expired, err
}
