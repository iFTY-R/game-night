// Package admin owns administrator authentication, MFA, recovery, and privileged identity rules.
package admin

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/secretresult"
	"github.com/iFTY-R/game-night/platform/security"
)

// ChallengePurpose is closed so login and each privilege-elevation flow remain cryptographically distinct.
type ChallengePurpose uint8

const (
	ChallengePurposeLogin ChallengePurpose = iota + 1
	ChallengePurposeSetupPassword
	ChallengePurposeTOTPEnrollment
	ChallengePurposeMFA
	ChallengePurposeRecovery
)

const (
	// ChallengeAudience binds administrator challenges to the isolated AdminAuthService boundary.
	ChallengeAudience challenge.Audience = "admin_auth_api"
)

// String returns the stable database and proof claim representation of a valid purpose.
func (purpose ChallengePurpose) String() string {
	switch purpose {
	case ChallengePurposeLogin:
		return "admin.login"
	case ChallengePurposeSetupPassword:
		return "admin.setup_password"
	case ChallengePurposeTOTPEnrollment:
		return "admin.totp_enrollment"
	case ChallengePurposeMFA:
		return "admin.mfa"
	case ChallengePurposeRecovery:
		return "admin.recovery"
	default:
		return ""
	}
}

// Valid reports whether purpose belongs to the reviewed administrator challenge protocol.
func (purpose ChallengePurpose) Valid() bool {
	return purpose.String() != ""
}

// ParseChallengePurpose restores only values in the administrator purpose closure.
func ParseChallengePurpose(value string) (ChallengePurpose, error) {
	for _, purpose := range [...]ChallengePurpose{
		ChallengePurposeLogin,
		ChallengePurposeSetupPassword,
		ChallengePurposeTOTPEnrollment,
		ChallengePurposeMFA,
		ChallengePurposeRecovery,
	} {
		if purpose.String() == value {
			return purpose, nil
		}
	}
	return 0, challenge.ErrInvalidInput
}

// Challenge is the administrator-keyring instantiation of the shared challenge aggregate.
type Challenge = challenge.Challenge[security.AdminChallengeKeyPurpose]

// ChallengeSnapshot is the persistence representation accepted by RestoreChallenge.
type ChallengeSnapshot = challenge.Snapshot[security.AdminChallengeKeyPurpose]

// IssuedChallenge contains the new aggregate plus cookie token and response-body proof.
type IssuedChallenge = challenge.Issued[security.AdminChallengeKeyPurpose]

// ChallengeService fixes the HMAC purpose and audience for every administrator authentication flow.
type ChallengeService struct {
	core *challenge.Service[security.AdminChallengeKeyPurpose]
	// source timestamps failed attempts inside the same transaction that owns the administrator row lock.
	source clock.Clock
}

// NewChallengeService prevents a user challenge keyring from being wired into administrator flows.
func NewChallengeService(keyring *security.HMACKeyring[security.AdminChallengeKeyPurpose], source clock.Clock) (*ChallengeService, error) {
	core, err := challenge.NewService(keyring, source)
	if err != nil {
		return nil, err
	}
	return &ChallengeService{core: core, source: source}, nil
}

// Issue creates a five-minute administrator challenge bound to account and credential generations.
func (service *ChallengeService) Issue(
	purpose ChallengePurpose,
	adminID uuid.UUID,
	adminVersion int64,
	passwordVersion int64,
	canonicalOrigin string,
	requestFlowID challenge.RequestFlowID,
	maxAttempts uint32,
) (IssuedChallenge, error) {
	if service == nil || service.core == nil {
		return IssuedChallenge{}, challenge.ErrInvalidInput
	}
	binding, err := adminChallengeBinding(
		purpose, adminID, adminVersion, passwordVersion, canonicalOrigin, requestFlowID,
	)
	if err != nil {
		return IssuedChallenge{}, err
	}
	return service.core.Issue(binding, maxAttempts)
}

// AuthorizePersistent locks the account-bound challenge and commits active-row failures before returning them.
func (service *ChallengeService) AuthorizePersistent(
	ctx context.Context,
	unitOfWork ChallengeUnitOfWork,
	purpose ChallengePurpose,
	adminID uuid.UUID,
	adminVersion int64,
	passwordVersion int64,
	canonicalOrigin string,
	requestFlowID challenge.RequestFlowID,
	credentials challenge.Credentials,
	operationID idempotency.OperationID,
	requestDigest idempotency.Digest,
	work AuthorizedChallengeWork,
) (challenge.Authorization, error) {
	if service == nil || service.core == nil || service.source == nil || unitOfWork == nil || work == nil {
		return challenge.Authorization{}, challenge.ErrInvalidInput
	}
	binding, err := adminChallengeBinding(
		purpose, adminID, adminVersion, passwordVersion, canonicalOrigin, requestFlowID,
	)
	if err != nil {
		return challenge.Authorization{}, challenge.ErrAuthentication
	}
	selector, err := challenge.SelectorFromCredentials(credentials)
	if err != nil {
		return challenge.Authorization{}, challenge.ErrAuthentication
	}

	var authorization challenge.Authorization
	var authorizationErr error
	err = unitOfWork.Run(ctx, func(ctx context.Context, transaction ChallengeTransaction) error {
		repository := transaction.Challenges()
		record, getErr := repository.GetForUpdate(ctx, selector)
		if errors.Is(getErr, challenge.ErrNotFound) {
			// Missing selectors are indistinguishable from submitted credential mismatches.
			authorizationErr = challenge.ErrAuthentication
			return nil
		}
		if getErr != nil {
			return getErr
		}

		authorization, authorizationErr = service.core.Authorize(
			record, binding, credentials, operationID, requestDigest,
		)
		if authorizationErr == nil {
			completion, workErr := work(ctx, transaction, record, authorization)
			if workErr != nil {
				return workErr
			}
			if authorization.Kind() == challenge.AuthorizeExactReplay {
				return nil
			}
			return service.completeFirstUse(ctx, transaction, repository, record, operationID, requestDigest, completion)
		}
		attemptedAt := service.source.Now()
		if !errors.Is(authorizationErr, challenge.ErrAuthentication) ||
			record.State(attemptedAt) != challenge.StateActive {
			return nil
		}
		// Returning nil after the CAS is intentional: the outer API releases the auth error only after commit.
		_, failureErr := repository.RecordFailureCAS(ctx, record, attemptedAt)
		return failureErr
	})
	if err != nil {
		return challenge.Authorization{}, err
	}
	if authorizationErr != nil {
		return challenge.Authorization{}, authorizationErr
	}
	return authorization, nil
}

// completeFirstUse verifies the persisted result contract before generation-aware challenge consumption.
func (service *ChallengeService) completeFirstUse(
	ctx context.Context,
	transaction ChallengeTransaction,
	repository ChallengeRepository,
	record Challenge,
	operationID idempotency.OperationID,
	requestDigest idempotency.Digest,
	completion AuthorizedChallengeCompletion,
) error {
	coreCompletion := challenge.FirstUseCompletion{}
	if completion.withoutReplay {
		if completion.result.Snapshot().ID != uuid.Nil {
			return challenge.ErrInvalidInput
		}
		coreCompletion = challenge.NoReplayCompletion()
	} else {
		provided := completion.result.Snapshot()
		recordID := record.Snapshot().ID
		if provided.ID == uuid.Nil || provided.Status != secretresult.StatusAvailable || !provided.Binding.Key.Scope.IsAdmin() ||
			provided.Binding.Key.ActorID != recordID || provided.Binding.Key.OperationID != operationID ||
			provided.Binding.RequestDigest != requestDigest {
			return challenge.ErrInvalidInput
		}
		stored, err := transaction.SecretResults().GetByOperationForUpdate(ctx, provided.Binding.Key)
		if err != nil {
			return err
		}
		resolution, err := stored.Resolve(provided.Binding, service.source.Now())
		storedSnapshot := stored.Snapshot()
		if err != nil || resolution.Kind != secretresult.ReplayAvailable || storedSnapshot.ID != provided.ID ||
			!storedSnapshot.SecretExpiresAt.Equal(provided.SecretExpiresAt) {
			return challenge.ErrInvalidInput
		}
		coreCompletion, err = challenge.NewReplayCompletion(storedSnapshot.ID, storedSnapshot.SecretExpiresAt)
		if err != nil {
			return err
		}
	}
	consumed, err := service.core.CompleteFirstUse(record, operationID, requestDigest, coreCompletion)
	if err != nil {
		return err
	}
	_, err = repository.ConsumeCAS(ctx, consumed)
	return err
}

// RestoreChallenge validates administrator challenge rows and their subject generations.
func RestoreChallenge(snapshot ChallengeSnapshot) (Challenge, error) {
	if !snapshot.Binding.Subject.Bound() || snapshot.Binding.Audience != ChallengeAudience {
		return Challenge{}, challenge.ErrInvalidInput
	}
	if _, err := ParseChallengePurpose(string(snapshot.Binding.Purpose)); err != nil {
		return Challenge{}, challenge.ErrInvalidInput
	}
	return challenge.Restore(snapshot)
}

func adminChallengeBinding(
	purpose ChallengePurpose,
	adminID uuid.UUID,
	adminVersion int64,
	passwordVersion int64,
	canonicalOrigin string,
	requestFlowID challenge.RequestFlowID,
) (challenge.Binding, error) {
	if !purpose.Valid() || adminID == uuid.Nil || adminVersion <= 0 || passwordVersion < 0 {
		return challenge.Binding{}, challenge.ErrInvalidInput
	}
	origin, err := challenge.DigestOrigin(canonicalOrigin)
	if err != nil {
		return challenge.Binding{}, err
	}
	binding := challenge.Binding{
		Purpose: challenge.Purpose(purpose.String()), Audience: ChallengeAudience,
		Origin: origin, RequestFlowID: requestFlowID,
		Subject: challenge.SubjectBinding{
			ID: adminID, Version: adminVersion, CredentialVersion: passwordVersion,
		},
	}
	if err := binding.Validate(); err != nil {
		return challenge.Binding{}, err
	}
	return binding, nil
}
