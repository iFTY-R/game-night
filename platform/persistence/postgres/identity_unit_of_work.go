package postgres

import (
	"context"

	"github.com/iFTY-R/game-night/platform/audit"
	identityDomain "github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IdentityUnitOfWork commits complete user-side identity workflows under the identity error contract.
type IdentityUnitOfWork struct {
	runner   *TransactionRunner
	verifier audit.IntegrityVerifier
}

// NewIdentityUnitOfWork preserves challenge and non-audited identity workflows over one PostgreSQL transaction runner.
func NewIdentityUnitOfWork(pool *pgxpool.Pool) *IdentityUnitOfWork {
	return &IdentityUnitOfWork{runner: NewTransactionRunner(pool)}
}

// NewIdentityUnitOfWorkWithAudit enables complete recovery workflows that append verified audit records.
func NewIdentityUnitOfWorkWithAudit(pool *pgxpool.Pool, verifier audit.IntegrityVerifier) *IdentityUnitOfWork {
	if verifier == nil {
		panic("PostgreSQL identity unit of work requires an audit integrity verifier")
	}
	return &IdentityUnitOfWork{runner: NewTransactionRunner(pool), verifier: verifier}
}

// Run commits the repository subset used by established identity challenge workflows.
func (unitOfWork *IdentityUnitOfWork) Run(ctx context.Context, work identityDomain.ChallengeTransactionWork) error {
	if work == nil {
		return identityDomain.ErrInvalidIdentityRequest
	}
	err := unitOfWork.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		return work(ctx, newIdentityChallengeTransaction(queries))
	})
	return unitOfWork.mapIdentityError(err)
}

// RunIdentity commits recovery, audit, outbox, and the shared identity repositories atomically.
func (unitOfWork *IdentityUnitOfWork) RunIdentity(ctx context.Context, work identityDomain.IdentityTransactionWork) error {
	if work == nil {
		return identityDomain.ErrInvalidIdentityRequest
	}
	err := unitOfWork.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		return work(ctx, newCompleteIdentityTransaction(queries, unitOfWork.verifier))
	})
	return unitOfWork.mapIdentityError(err)
}

// mapIdentityError handles deferred constraints before filtering callback and lifecycle errors.
func (unitOfWork *IdentityUnitOfWork) mapIdentityError(err error) error {
	if mapped := mapIdentityCommitFailure(err); mapped != nil {
		return mapped
	}
	return mapUnitOfWorkError(err, identityDomain.ErrIdentityRepositoryUnavailable, identityTransactionDomainErrors...)
}

type identityTransaction struct {
	identityChallengeTransaction
	recoveryAttempts       identityDomain.RecoveryAttemptRepository
	assistedRecoveryGrants identityDomain.AssistedRecoveryGrantRepository
	audit                  audit.Repository
	auditCheckpoints       audit.CheckpointRepository
	outboxEvents           outbox.EventRepository
}

// newCompleteIdentityTransaction binds every security workflow participant to the same database transaction.
func newCompleteIdentityTransaction(queries QueryHandle, verifier audit.IntegrityVerifier) identityTransaction {
	return identityTransaction{
		identityChallengeTransaction: newIdentityChallengeTransaction(queries),
		recoveryAttempts:             &identityRecoveryAttemptRepository{queries: queries},
		assistedRecoveryGrants:       &identityAssistedRecoveryRepository{queries: queries},
		audit:                        newAuditRepository(queries, verifier),
		auditCheckpoints:             newAuditCheckpointRepository(queries, verifier),
		outboxEvents:                 newOutboxEventRepository(queries),
	}
}

func (transaction identityTransaction) RecoveryAttempts() identityDomain.RecoveryAttemptRepository {
	return transaction.recoveryAttempts
}

func (transaction identityTransaction) AssistedRecoveryGrants() identityDomain.AssistedRecoveryGrantRepository {
	return transaction.assistedRecoveryGrants
}

func (transaction identityTransaction) Audit() audit.Repository {
	return transaction.audit
}

func (transaction identityTransaction) AuditCheckpoints() audit.CheckpointRepository {
	return transaction.auditCheckpoints
}

func (transaction identityTransaction) OutboxEvents() outbox.EventRepository {
	return transaction.outboxEvents
}

var _ identityDomain.IdentityUnitOfWork = (*IdentityUnitOfWork)(nil)
