package postgres

import (
	"context"

	adminDomain "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/secretresult"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AdminUnitOfWork binds account, challenge, MFA, session, recovery, and result repositories to one transaction.
type AdminUnitOfWork struct {
	runner *TransactionRunner
}

func NewAdminUnitOfWork(pool *pgxpool.Pool) *AdminUnitOfWork {
	return &AdminUnitOfWork{runner: NewTransactionRunner(pool)}
}

func (unitOfWork *AdminUnitOfWork) Run(ctx context.Context, work adminDomain.TransactionWork) error {
	if work == nil {
		return adminDomain.ErrInvalidInput
	}
	err := unitOfWork.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		return work(ctx, newAdminTransaction(queries))
	})
	return mapUnitOfWorkError(err, adminDomain.ErrRepositoryUnavailable, adminDomainTransactionErrors...)
}

type adminTransaction struct {
	adminChallengeTransaction
	accounts      adminDomain.AccountRepository
	enrollments   adminDomain.EnrollmentRepository
	sessions      adminDomain.SessionRepository
	recoveryCodes adminDomain.RecoveryCodeRepository
}

func newAdminTransaction(queries QueryHandle) adminTransaction {
	return adminTransaction{
		adminChallengeTransaction: adminChallengeTransaction{challenges: &adminChallengeRepository{queries: queries}, results: newSecretResultRepository(queries)},
		accounts:                  &adminAccountRepository{queries: queries}, enrollments: &adminEnrollmentRepository{queries: queries},
		sessions: &adminSessionRepository{queries: queries}, recoveryCodes: &adminRecoveryCodeRepository{queries: queries},
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
func (transaction adminTransaction) RecoveryCodes() adminDomain.RecoveryCodeRepository {
	return transaction.recoveryCodes
}

var adminDomainErrors = []error{
	adminDomain.ErrInvalidInput, adminDomain.ErrAuthentication, adminDomain.ErrUnavailable, adminDomain.ErrConcurrentTransition,
	adminDomain.ErrRepositoryUnavailable, adminDomain.ErrIntegrity, adminDomain.ErrPasswordPolicy, adminDomain.ErrTOTPInvalid,
	adminDomain.ErrSessionExpired, adminDomain.ErrSessionRevoked, adminDomain.ErrPermissionDenied, adminDomain.ErrRecoveryInvalid,
	adminDomain.ErrIdempotencyConflict, adminDomain.ErrBootstrapSecretMismatch, adminDomain.ErrNotFound,
}

var adminDomainTransactionErrors = append(append([]error{}, adminDomainErrors...), []error{
	challenge.ErrInvalidInput, challenge.ErrAuthentication, challenge.ErrUnavailable, challenge.ErrConcurrentTransition,
	challenge.ErrNotFound, challenge.ErrRepositoryUnavailable, challenge.ErrIntegrity,
	secretresult.ErrInvalidInput, secretresult.ErrNotFound, secretresult.ErrRepositoryUnavailable, secretresult.ErrIntegrity,
}...)

var _ adminDomain.UnitOfWork = (*AdminUnitOfWork)(nil)
