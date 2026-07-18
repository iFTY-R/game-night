package postgres

import (
	"context"

	adminDomain "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/challenge"
	identityDomain "github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/secretresult"
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
		transaction := identityChallengeTransaction{
			challenges: &identityChallengeRepository{queries: queries},
			results:    newSecretResultRepository(queries),
		}
		return work(ctx, transaction)
	})
	return mapUnitOfWorkError(err, challenge.ErrRepositoryUnavailable, challengeTransactionDomainErrors...)
}

type identityChallengeTransaction struct {
	challenges identityDomain.ChallengeRepository
	results    secretresult.Repository
}

func (transaction identityChallengeTransaction) Challenges() identityDomain.ChallengeRepository {
	return transaction.challenges
}

func (transaction identityChallengeTransaction) SecretResults() secretresult.Repository {
	return transaction.results
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
