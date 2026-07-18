package postgres

import (
	"context"
	"errors"

	adminDomain "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/idempotency"
	identityDomain "github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/secretresult"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IdentityChallengeUnitOfWork atomically combines anonymous challenge and secret-result repositories.
type IdentityChallengeUnitOfWork struct {
	runner *TransactionRunner
}

// NewIdentityChallengeUnitOfWork binds user challenge workflows to the runtime PostgreSQL pool.
func NewIdentityChallengeUnitOfWork(pool *pgxpool.Pool) *IdentityChallengeUnitOfWork {
	return &IdentityChallengeUnitOfWork{runner: NewTransactionRunner(pool)}
}

// Run commits challenge transitions and result insertion together.
func (unitOfWork *IdentityChallengeUnitOfWork) Run(ctx context.Context, work identityDomain.ChallengeTransactionWork) error {
	if work == nil {
		return challenge.ErrInvalidInput
	}
	err := unitOfWork.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		return work(ctx, newIdentityChallengeTransaction(queries))
	})
	return mapUnitOfWorkError(err, challenge.ErrRepositoryUnavailable, challengeTransactionDomainErrors...)
}

type identityChallengeTransaction struct {
	challenges identityDomain.ChallengeRepository
	results    secretresult.Repository
	users      identityDomain.UserRepository
	claims     identityDomain.UsernameClaimRepository
	devices    identityDomain.DeviceRepository
	recovery   identityDomain.RecoveryCredentialRepository
}

// newIdentityChallengeTransaction builds the repository subset shared by legacy challenge and full security workflows.
func newIdentityChallengeTransaction(queries QueryHandle) identityChallengeTransaction {
	return identityChallengeTransaction{
		challenges: &identityChallengeRepository{queries: queries},
		results:    newSecretResultRepository(queries),
		users:      &identityUserRepository{queries: queries},
		claims:     &identityClaimRepository{queries: queries},
		devices:    &identityDeviceRepository{queries: queries},
		recovery:   &identityRecoveryRepository{queries: queries},
	}
}

func (transaction identityChallengeTransaction) Challenges() identityDomain.ChallengeRepository {
	return transaction.challenges
}

func (transaction identityChallengeTransaction) SecretResults() secretresult.Repository {
	return transaction.results
}

func (transaction identityChallengeTransaction) Users() identityDomain.UserRepository {
	return transaction.users
}

func (transaction identityChallengeTransaction) UsernameClaims() identityDomain.UsernameClaimRepository {
	return transaction.claims
}

func (transaction identityChallengeTransaction) Devices() identityDomain.DeviceRepository {
	return transaction.devices
}

func (transaction identityChallengeTransaction) RecoveryCredentials() identityDomain.RecoveryCredentialRepository {
	return transaction.recovery
}

// AdminChallengeUnitOfWork atomically combines administrator challenge and secret-result repositories.
type AdminChallengeUnitOfWork struct {
	runner *TransactionRunner
}

// NewAdminChallengeUnitOfWork binds administrator authentication workflows to the runtime PostgreSQL pool.
func NewAdminChallengeUnitOfWork(pool *pgxpool.Pool) *AdminChallengeUnitOfWork {
	return &AdminChallengeUnitOfWork{runner: NewTransactionRunner(pool)}
}

// Run commits generation-aware challenge transitions and result insertion together.
func (unitOfWork *AdminChallengeUnitOfWork) Run(ctx context.Context, work adminDomain.ChallengeTransactionWork) error {
	if work == nil {
		return challenge.ErrInvalidInput
	}
	err := unitOfWork.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		transaction := adminChallengeTransaction{
			challenges: &adminChallengeRepository{queries: queries},
			results:    newSecretResultRepository(queries),
		}
		return work(ctx, transaction)
	})
	return mapUnitOfWorkError(err, challenge.ErrRepositoryUnavailable, challengeTransactionDomainErrors...)
}

// challengeTransactionDomainErrors includes both challenge transitions and the secret-result
// operations committed by the same transaction.
var challengeTransactionDomainErrors = append([]error{
	challenge.ErrInvalidInput,
	challenge.ErrAuthentication,
	challenge.ErrUnavailable,
	challenge.ErrConcurrentTransition,
	challenge.ErrNotFound,
	challenge.ErrRepositoryUnavailable,
	challenge.ErrIntegrity,
}, secretResultDomainErrors...)

// identityTransactionDomainErrors preserves reviewed callback sentinels while hiding transaction diagnostics.
var identityTransactionDomainErrors = append(append(challengeTransactionDomainErrors, []error{
	identityDomain.ErrInvalidIdentityRequest,
	identityDomain.ErrInvalidUserInput,
	identityDomain.ErrUserStatus,
	identityDomain.ErrOnboardingExpired,
	identityDomain.ErrUsernameChangeCooldown,
	identityDomain.ErrUsernameUnchanged,
	identityDomain.ErrUsernameUnavailable,
	identityDomain.ErrUserNotFound,
	identityDomain.ErrIdentityConcurrentTransition,
	identityDomain.ErrIdentityRepositoryUnavailable,
	identityDomain.ErrIdentityIntegrity,
	identityDomain.ErrInvalidDeviceInput,
	identityDomain.ErrDeviceAuthentication,
	identityDomain.ErrDeviceUnavailable,
	identityDomain.ErrDeviceRotationNotDue,
	identityDomain.ErrDeviceConcurrentTransition,
	identityDomain.ErrDeviceIntegrity,
	identityDomain.ErrInvalidRecoveryCredential,
	identityDomain.ErrRecoveryInvalid,
	identityDomain.ErrRecoveryConcurrentTransition,
	identityDomain.ErrInvalidRecoveryAttempt,
	identityDomain.ErrInvalidAssistedRecoveryGrant,
	idempotency.ErrInvalidInput,
	idempotency.ErrConflict,
}...), auditOutboxDomainErrors...)

// mapIdentityCommitFailure translates deferred PostgreSQL constraint and serialization failures at commit.
func mapIdentityCommitFailure(err error) error {
	var lifecycleFailure *transactionLifecycleError
	if !errors.As(err, &lifecycleFailure) {
		return nil
	}
	if lifecycleFailure.operation != transactionCommitOperation {
		return nil
	}
	var pgError *pgconn.PgError
	if !errors.As(lifecycleFailure.cause, &pgError) {
		return nil
	}
	switch pgError.Code {
	case "23503", "23514":
		return identityDomain.ErrIdentityIntegrity
	case "23505", "40001", "40P01":
		return identityDomain.ErrIdentityConcurrentTransition
	default:
		return nil
	}
}

type adminChallengeTransaction struct {
	challenges adminDomain.ChallengeRepository
	results    secretresult.Repository
}

func (transaction adminChallengeTransaction) Challenges() adminDomain.ChallengeRepository {
	return transaction.challenges
}

func (transaction adminChallengeTransaction) SecretResults() secretresult.Repository {
	return transaction.results
}

var _ identityDomain.ChallengeUnitOfWork = (*IdentityChallengeUnitOfWork)(nil)
var _ adminDomain.ChallengeUnitOfWork = (*AdminChallengeUnitOfWork)(nil)
