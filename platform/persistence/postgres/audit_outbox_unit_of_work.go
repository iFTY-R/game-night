package postgres

import (
	"context"

	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AuditOutboxUnitOfWork atomically combines audit append, checkpoint progress, and arbitrary durable outbox events.
type AuditOutboxUnitOfWork struct {
	runner   *TransactionRunner
	verifier audit.IntegrityVerifier
}

// NewAuditOutboxUnitOfWork binds all participants to transactions created from the supplied runtime pool.
func NewAuditOutboxUnitOfWork(pool *pgxpool.Pool, verifier audit.IntegrityVerifier) *AuditOutboxUnitOfWork {
	if verifier == nil {
		panic("PostgreSQL audit unit of work requires an integrity verifier")
	}
	return &AuditOutboxUnitOfWork{runner: NewTransactionRunner(pool), verifier: verifier}
}

// Run commits every participant only after the callback succeeds.
func (unitOfWork *AuditOutboxUnitOfWork) Run(ctx context.Context, work audit.TransactionWork) error {
	if work == nil {
		return audit.ErrInvalidInput
	}
	err := unitOfWork.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		return work(ctx, newAuditOutboxTransaction(queries, unitOfWork.verifier))
	})
	return mapUnitOfWorkError(err, audit.ErrRepositoryUnavailable, auditOutboxDomainErrors...)
}

type auditOutboxTransaction struct {
	audit        audit.Repository
	checkpoints  audit.CheckpointRepository
	outboxEvents outbox.EventRepository
}

func newAuditOutboxTransaction(queries QueryHandle, verifier audit.IntegrityVerifier) audit.Transaction {
	return auditOutboxTransaction{
		audit:        newAuditRepository(queries, verifier),
		checkpoints:  newAuditCheckpointRepository(queries, verifier),
		outboxEvents: newOutboxEventRepository(queries),
	}
}

func (transaction auditOutboxTransaction) Audit() audit.Repository {
	return transaction.audit
}

func (transaction auditOutboxTransaction) Checkpoints() audit.CheckpointRepository {
	return transaction.checkpoints
}

func (transaction auditOutboxTransaction) OutboxEvents() outbox.EventRepository {
	return transaction.outboxEvents
}

// OutboxUnitOfWork provides the dispatcher with independent consumer transactions.
type OutboxUnitOfWork struct {
	runner *TransactionRunner
}

// NewOutboxUnitOfWork binds event and consumer repositories to one worker transaction at a time.
func NewOutboxUnitOfWork(pool *pgxpool.Pool) *OutboxUnitOfWork {
	return &OutboxUnitOfWork{runner: NewTransactionRunner(pool)}
}

// Run commits consumer lease, ack, or retry transitions atomically with any dispatcher-side outbox work.
func (unitOfWork *OutboxUnitOfWork) Run(ctx context.Context, work outbox.TransactionWork) error {
	if work == nil {
		return outbox.ErrInvalidInput
	}
	err := unitOfWork.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		return work(ctx, newOutboxTransaction(queries))
	})
	return mapUnitOfWorkError(err, outbox.ErrRepositoryUnavailable, outboxDomainErrors...)
}

type outboxTransaction struct {
	events    outbox.EventRepository
	consumers outbox.ConsumerRepository
}

func newOutboxTransaction(queries QueryHandle) outbox.Transaction {
	return outboxTransaction{
		events: newOutboxEventRepository(queries), consumers: newOutboxConsumerRepository(queries),
	}
}

func (transaction outboxTransaction) Events() outbox.EventRepository {
	return transaction.events
}

func (transaction outboxTransaction) Consumers() outbox.ConsumerRepository {
	return transaction.consumers
}

var auditOutboxDomainErrors = append([]error{
	audit.ErrInvalidInput,
	audit.ErrIntegrity,
	audit.ErrChainDiscontinuity,
	audit.ErrHeadConflict,
	audit.ErrNotFound,
	audit.ErrRepositoryUnavailable,
	audit.ErrCheckpointUnavailable,
	audit.ErrSensitiveWriteBlocked,
}, outboxDomainErrors...)

var outboxDomainErrors = []error{
	outbox.ErrInvalidInput,
	outbox.ErrNotFound,
	outbox.ErrAlreadyExists,
	outbox.ErrLeaseUnavailable,
	outbox.ErrLeaseNotOwned,
	outbox.ErrLeaseExpired,
	outbox.ErrBackoffActive,
	outbox.ErrInvalidAcknowledgement,
	outbox.ErrRetryExhausted,
	outbox.ErrConcurrentTransition,
	outbox.ErrRepositoryUnavailable,
	outbox.ErrIntegrity,
}

var _ audit.UnitOfWork = (*AuditOutboxUnitOfWork)(nil)
var _ outbox.UnitOfWork = (*OutboxUnitOfWork)(nil)
