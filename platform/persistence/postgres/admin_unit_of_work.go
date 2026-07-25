package postgres

import (
	"context"

	adminDomain "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/secretresult"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AdminUnitOfWork binds account, challenge, MFA, session, recovery, and result repositories to one transaction.
type AdminUnitOfWork struct {
	runner   *TransactionRunner
	verifier audit.IntegrityVerifier
}

func NewAdminUnitOfWork(pool *pgxpool.Pool) *AdminUnitOfWork {
	return &AdminUnitOfWork{runner: NewTransactionRunner(pool)}
}

// NewAdminUnitOfWorkWithAudit enables administrator security transactions that must append verified audit events.
func NewAdminUnitOfWorkWithAudit(pool *pgxpool.Pool, verifier audit.IntegrityVerifier) *AdminUnitOfWork {
	if verifier == nil {
		panic("PostgreSQL admin unit of work requires an audit integrity verifier")
	}
	return &AdminUnitOfWork{runner: NewTransactionRunner(pool), verifier: verifier}
}

func (unitOfWork *AdminUnitOfWork) Run(ctx context.Context, work adminDomain.TransactionWork) error {
	if work == nil {
		return adminDomain.ErrInvalidInput
	}
	err := unitOfWork.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		return work(ctx, newAdminTransaction(queries, unitOfWork.verifier))
	})
	return mapUnitOfWorkError(err, adminDomain.ErrRepositoryUnavailable, adminDomainTransactionErrors...)
}

type adminTransaction struct {
	adminChallengeTransaction
	accounts         adminDomain.AccountRepository
	enrollments      adminDomain.EnrollmentRepository
	sessions         adminDomain.SessionRepository
	elevations       adminDomain.ElevationRepository
	commandReceipts  adminDomain.CommandReceiptRepository
	recoveryCodes    adminDomain.RecoveryCodeRepository
	audit            audit.Repository
	auditCheckpoints audit.CheckpointRepository
	outboxEvents     outbox.EventRepository
}

func newAdminTransaction(queries QueryHandle, verifier audit.IntegrityVerifier) adminTransaction {
	return adminTransaction{
		adminChallengeTransaction: adminChallengeTransaction{challenges: &adminChallengeRepository{queries: queries}, results: newSecretResultRepository(queries)},
		accounts:                  &adminAccountRepository{queries: queries}, enrollments: &adminEnrollmentRepository{queries: queries},
		sessions: &adminSessionRepository{queries: queries}, elevations: &adminElevationRepository{queries: queries},
		commandReceipts: &adminCommandReceiptRepository{queries: queries}, recoveryCodes: &adminRecoveryCodeRepository{queries: queries},
		audit: newAuditRepository(queries, verifier), auditCheckpoints: newAuditCheckpointRepository(queries, verifier),
		outboxEvents: newOutboxEventRepository(queries),
	}
}

func (transaction adminTransaction) Accounts() adminDomain.AccountRepository {
	return transaction.accounts
}
func (transaction adminTransaction) Enrollments() adminDomain.EnrollmentRepository {
	return transaction.enrollments
}
func (transaction adminTransaction) Sessions() adminDomain.SessionRepository {
	return transaction.sessions
}
func (transaction adminTransaction) Elevations() adminDomain.ElevationRepository {
	return transaction.elevations
}
func (transaction adminTransaction) CommandReceipts() adminDomain.CommandReceiptRepository {
	return transaction.commandReceipts
}
func (transaction adminTransaction) RecoveryCodes() adminDomain.RecoveryCodeRepository {
	return transaction.recoveryCodes
}
func (transaction adminTransaction) Audit() audit.Repository { return transaction.audit }
func (transaction adminTransaction) AuditCheckpoints() audit.CheckpointRepository {
	return transaction.auditCheckpoints
}
func (transaction adminTransaction) OutboxEvents() outbox.EventRepository {
	return transaction.outboxEvents
}

var adminDomainErrors = []error{
	adminDomain.ErrInvalidInput, adminDomain.ErrAuthentication, adminDomain.ErrUnavailable, adminDomain.ErrConcurrentTransition,
	adminDomain.ErrRepositoryUnavailable, adminDomain.ErrIntegrity, adminDomain.ErrPasswordPolicy, adminDomain.ErrTOTPInvalid,
	adminDomain.ErrSessionExpired, adminDomain.ErrSessionRevoked, adminDomain.ErrPermissionDenied, adminDomain.ErrElevationDenied,
	adminDomain.ErrElevationExpired, adminDomain.ErrRecoveryInvalid,
	adminDomain.ErrIdempotencyConflict, adminDomain.ErrBootstrapSecretMismatch, adminDomain.ErrNotFound,
}

var adminDomainTransactionErrors = append(append([]error{}, adminDomainErrors...), []error{
	challenge.ErrInvalidInput, challenge.ErrAuthentication, challenge.ErrUnavailable, challenge.ErrConcurrentTransition,
	challenge.ErrNotFound, challenge.ErrRepositoryUnavailable, challenge.ErrIntegrity,
	secretresult.ErrInvalidInput, secretresult.ErrNotFound, secretresult.ErrRepositoryUnavailable, secretresult.ErrIntegrity,
}...)

var _ adminDomain.UnitOfWork = (*AdminUnitOfWork)(nil)
