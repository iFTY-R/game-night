package postgres

import (
	"context"

	adminDomain "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/challenge"
	identityDomain "github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/outbox"
	profileDomain "github.com/iFTY-R/game-night/platform/profile"
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

// NewAdminUnitOfWorkWithAudit enables PII and governance transactions that must append verified audit events.
func NewAdminUnitOfWorkWithAudit(pool *pgxpool.Pool, verifier audit.IntegrityVerifier) *AdminUnitOfWork {
	if verifier == nil {
		panic("PostgreSQL admin identity unit of work requires an audit integrity verifier")
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
	if mapped := mapIdentityCommitFailure(err); mapped != nil {
		return mapped
	}
	return mapUnitOfWorkError(err, adminDomain.ErrRepositoryUnavailable, adminDomainTransactionErrors...)
}

type adminTransaction struct {
	adminChallengeTransaction
	accounts         adminDomain.AccountRepository
	enrollments      adminDomain.EnrollmentRepository
	sessions         adminDomain.SessionRepository
	recoveryCodes    adminDomain.RecoveryCodeRepository
	identityUsers    adminDomain.IdentityUserRepository
	identityClaims   identityDomain.UsernameClaimRepository
	identityDevices  identityDomain.DeviceRepository
	identityRecovery identityDomain.RecoveryCredentialRepository
	profiles         profileDomain.Repository
	profileExports   profileDomain.ExportRepository
	assistedRecovery adminDomain.AssistedRecoveryGrantRepository
	audit            audit.Repository
	auditCheckpoints audit.CheckpointRepository
	outboxEvents     outbox.EventRepository
}

func newAdminTransaction(queries QueryHandle, verifier audit.IntegrityVerifier) adminTransaction {
	profiles := &profileRepository{queries: queries}
	return adminTransaction{
		adminChallengeTransaction: adminChallengeTransaction{challenges: &adminChallengeRepository{queries: queries}, results: newSecretResultRepository(queries)},
		accounts:                  &adminAccountRepository{queries: queries}, enrollments: &adminEnrollmentRepository{queries: queries},
		sessions: &adminSessionRepository{queries: queries}, recoveryCodes: &adminRecoveryCodeRepository{queries: queries},
		identityUsers: &identityUserRepository{queries: queries}, identityClaims: &identityClaimRepository{queries: queries},
		identityDevices: &identityDeviceRepository{queries: queries}, identityRecovery: &identityRecoveryRepository{queries: queries},
		profiles: profiles, profileExports: profiles, assistedRecovery: &identityAssistedRecoveryRepository{queries: queries},
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
func (transaction adminTransaction) RecoveryCodes() adminDomain.RecoveryCodeRepository {
	return transaction.recoveryCodes
}
func (transaction adminTransaction) IdentityUsers() adminDomain.IdentityUserRepository {
	return transaction.identityUsers
}
func (transaction adminTransaction) IdentityUsernameClaims() identityDomain.UsernameClaimRepository {
	return transaction.identityClaims
}
func (transaction adminTransaction) IdentityDevices() identityDomain.DeviceRepository {
	return transaction.identityDevices
}
func (transaction adminTransaction) IdentityRecoveryCredentials() identityDomain.RecoveryCredentialRepository {
	return transaction.identityRecovery
}
func (transaction adminTransaction) Profiles() profileDomain.Repository { return transaction.profiles }
func (transaction adminTransaction) ProfileExports() profileDomain.ExportRepository {
	return transaction.profileExports
}
func (transaction adminTransaction) AssistedRecoveryGrants() adminDomain.AssistedRecoveryGrantRepository {
	return transaction.assistedRecovery
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
	adminDomain.ErrSessionExpired, adminDomain.ErrSessionRevoked, adminDomain.ErrPermissionDenied, adminDomain.ErrRecoveryInvalid,
	adminDomain.ErrIdempotencyConflict, adminDomain.ErrBootstrapSecretMismatch, adminDomain.ErrNotFound,
}

var adminDomainTransactionErrors = append(append(append([]error{}, adminDomainErrors...), identityTransactionDomainErrors...), []error{
	challenge.ErrInvalidInput, challenge.ErrAuthentication, challenge.ErrUnavailable, challenge.ErrConcurrentTransition,
	challenge.ErrNotFound, challenge.ErrRepositoryUnavailable, challenge.ErrIntegrity,
	secretresult.ErrInvalidInput, secretresult.ErrNotFound, secretresult.ErrRepositoryUnavailable, secretresult.ErrIntegrity,
	profileDomain.ErrInvalidProfileInput, profileDomain.ErrProfileNotFound, profileDomain.ErrProfileRepositoryUnavailable,
	profileDomain.ErrProfileIntegrity, profileDomain.ErrPIIAuthentication, profileDomain.ErrPIIKeyUnavailable,
	profileDomain.ErrProfileConcurrentTransition, profileDomain.ErrProfileExportClosed, profileDomain.ErrProfileExportExpired,
	profileDomain.ErrProfileExportNotExpired, profileDomain.ErrProfileExportCursor,
}...)

var _ adminDomain.UnitOfWork = (*AdminUnitOfWork)(nil)
