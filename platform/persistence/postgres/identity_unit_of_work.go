package postgres

import (
	"context"

	identityDomain "github.com/iFTY-R/game-night/platform/identity"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IdentityUnitOfWork commits complete user-side identity workflows under the identity error contract.
type IdentityUnitOfWork struct {
	runner *TransactionRunner
}

// NewIdentityUnitOfWork exposes all identity repositories over one PostgreSQL transaction runner.
func NewIdentityUnitOfWork(pool *pgxpool.Pool) *IdentityUnitOfWork {
	return &IdentityUnitOfWork{runner: NewTransactionRunner(pool)}
}

// Run commits user, device, claim, challenge, recovery, and secret-result changes atomically.
func (unitOfWork *IdentityUnitOfWork) Run(ctx context.Context, work identityDomain.IdentityTransactionWork) error {
	if work == nil {
		return identityDomain.ErrInvalidIdentityRequest
	}
	err := unitOfWork.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		return work(ctx, newIdentityTransaction(queries))
	})
	if mapped := mapIdentityCommitFailure(err); mapped != nil {
		return mapped
	}
	return mapUnitOfWorkError(err, identityDomain.ErrIdentityRepositoryUnavailable, identityTransactionDomainErrors...)
}

var _ identityDomain.IdentityUnitOfWork = (*IdentityUnitOfWork)(nil)
