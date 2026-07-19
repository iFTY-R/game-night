package admin

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/profile"
	"github.com/iFTY-R/game-night/platform/secretresult"
)

// IdentityUserRepository exposes governance-only CAS transitions without widening the normal user service port.
type IdentityUserRepository interface {
	GetByID(context.Context, uuid.UUID) (identity.User, error)
	GetByUsernameKey(context.Context, string) (identity.User, error)
	GetForUpdate(context.Context, uuid.UUID) (identity.User, error)
	TransitionStatusCAS(context.Context, identity.User, identity.User) (identity.User, error)
	ForceChangeUsernameCAS(context.Context, identity.User, identity.User) (identity.User, error)
}

// AssistedRecoveryGrantRepository owns administrator issuance while the identity service owns proof verification.
type AssistedRecoveryGrantRepository interface {
	Create(context.Context, identity.AssistedRecoveryGrant) (identity.AssistedRecoveryGrant, error)
	RevokeActiveForUser(context.Context, uuid.UUID, uuid.UUID, time.Time) ([]uuid.UUID, error)
}

// AccountRepository owns singleton account row locks and generation-aware security CAS operations.
type AccountRepository interface {
	GetForUpdate(context.Context) (Account, error)
	BootstrapPasswordCAS(context.Context, Account, string, string, string, time.Time) (Account, error)
	UpdatePasswordCAS(context.Context, Account, string, string, string, time.Time) (Account, error)
	TransitionStatusCAS(context.Context, Account, AccountStatus, time.Time) (Account, error)
	AcceptTOTPStepCAS(context.Context, Account, int64, time.Time) (Account, error)
}

// EnrollmentRepository persists encrypted TOTP seeds with one pending/active row per account.
type EnrollmentRepository interface {
	CreatePending(context.Context, Enrollment) (Enrollment, error)
	GetPendingForUpdate(context.Context, uuid.UUID) (Enrollment, error)
	GetActiveForUpdate(context.Context, uuid.UUID) (Enrollment, error)
	ActivateCAS(context.Context, Enrollment, time.Time) (Enrollment, error)
	DisableCAS(context.Context, Enrollment, time.Time) (Enrollment, error)
}

// SessionRepository stores only hashed bearer material and applies version/expiry CAS checks.
type SessionRepository interface {
	Insert(context.Context, Session) error
	GetForUpdate(context.Context, string) (Session, error)
	TouchCAS(context.Context, Session, time.Time, time.Duration) (Session, error)
	RevokeCAS(context.Context, Session, string, time.Time) (Session, error)
	RevokeAll(context.Context, uuid.UUID, string, time.Time) (int64, error)
}

// RecoveryCodeRepository serializes one-time code consumption and set rotation.
type RecoveryCodeRepository interface {
	Insert(context.Context, RecoveryCode) error
	FindActiveBySelector(context.Context, string) (RecoveryCode, error)
	ConsumeCAS(context.Context, RecoveryCode, time.Time) (RecoveryCode, error)
	RevokeSet(context.Context, uuid.UUID, int64, time.Time) (int64, error)
	RevokeAllSets(context.Context, uuid.UUID, time.Time) (int64, error)
}

// Transaction exposes every repository that must commit with challenge/result state.
type Transaction interface {
	ChallengeTransaction
	Accounts() AccountRepository
	Enrollments() EnrollmentRepository
	Sessions() SessionRepository
	RecoveryCodes() RecoveryCodeRepository
	IdentityUsers() IdentityUserRepository
	IdentityUsernameClaims() identity.UsernameClaimRepository
	IdentityDevices() identity.DeviceRepository
	IdentityRecoveryCredentials() identity.RecoveryCredentialRepository
	Profiles() profile.Repository
	ProfileExports() profile.ExportRepository
	AssistedRecoveryGrants() AssistedRecoveryGrantRepository
	Audit() audit.Repository
	AuditCheckpoints() audit.CheckpointRepository
	OutboxEvents() outbox.EventRepository
}

// TransactionWork is scoped to one database transaction and must not retain repository values.
type TransactionWork func(context.Context, Transaction) error

// UnitOfWork is the only cross-domain write boundary available to admin services.
type UnitOfWork interface {
	Run(context.Context, TransactionWork) error
}

// ChallengeUnitOfWorkAdapter allows the existing challenge service to be reused by isolated tests.
type ChallengeUnitOfWorkAdapter interface {
	Run(context.Context, ChallengeTransactionWork) error
}

// SecretOperationFactory keeps result envelope creation behind a narrow dependency in service tests.
type SecretOperationFactory interface {
	PrepareAvailable(uuid.UUID, secretresult.Binding, []byte, time.Duration) (secretresult.Result, error)
}

// AdminChallengeRequest is the transport-neutral input for beginning an admin login flow.
type AdminChallengeRequest struct {
	CanonicalOrigin string
	RequestFlowID   challenge.RequestFlowID
	MaxAttempts     uint32
}

// AdminChallengeCredentials are supplied to password/MFA completion after BeginAdminLogin.
type AdminChallengeCredentials struct {
	CookieToken string
	BodyProof   string
}

// AdminRequestBinding is the exact operation digest used by replayable operations.
type AdminRequestBinding struct {
	OperationID   idempotency.OperationID
	RequestDigest idempotency.Digest
}
