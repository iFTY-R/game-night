package admin

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/secretresult"
)

// AccountRepository owns singleton account row locks and generation-aware security CAS operations.
type AccountRepository interface {
	GetForUpdate(context.Context) (Account, error)
	BootstrapPasswordCAS(context.Context, Account, string, string, string, time.Time) (Account, error)
	UpdatePasswordCAS(context.Context, Account, string, string, string, time.Time) (Account, error)
	TransitionStatusCAS(context.Context, Account, AccountStatus, time.Time) (Account, error)
	RecordMFAChangeCAS(context.Context, Account, time.Time) (Account, error)
}

// EnrollmentRepository persists encrypted TOTP seeds with one pending/active row per account.
type EnrollmentRepository interface {
	CreatePending(context.Context, Enrollment) (Enrollment, error)
	GetPendingForUpdate(context.Context, uuid.UUID) (Enrollment, error)
	GetActiveForUpdate(context.Context, uuid.UUID) (Enrollment, error)
	ActivateCAS(context.Context, Enrollment, int64, int64, time.Time) (Enrollment, error)
	AcceptTOTPCAS(context.Context, Enrollment, int64, time.Time) (Enrollment, error)
	DisableCAS(context.Context, Enrollment, int64, time.Time) (Enrollment, error)
}

// SessionRepository stores only hashed bearer material and applies version/expiry CAS checks.
type SessionRepository interface {
	Insert(context.Context, Session) error
	GetForUpdate(context.Context, string) (Session, error)
	GetByIDForUpdate(context.Context, uuid.UUID) (Session, error)
	ListActiveForAdmin(context.Context, uuid.UUID, time.Time) ([]Session, error)
	TouchCAS(context.Context, Session, time.Time, time.Duration) (Session, error)
	RevokeCAS(context.Context, Session, string, time.Time) (Session, error)
	RevokeOtherActiveCAS(context.Context, uuid.UUID, uuid.UUID, int64, int64, string, time.Time) ([]Session, error)
}

// ElevationRepository stores short-lived scope grants that bind one session to exact security versions.
type ElevationRepository interface {
	UpsertLive(context.Context, Elevation) (Elevation, error)
	GetForSessionScope(context.Context, uuid.UUID, ElevationScope, time.Time) (Elevation, error)
	ListLiveForSessions(context.Context, uuid.UUID, []uuid.UUID, time.Time) ([]Elevation, error)
	RevokeCAS(context.Context, Elevation, time.Time) (Elevation, error)
}

// CommandReceipt records one dangerous command result behind a stable request digest and operation ID.
type CommandReceipt struct {
	AdminID                 uuid.UUID
	OperationID             idempotency.OperationID
	RequestDigest           idempotency.Digest
	Command                 string
	TargetType              string
	TargetID                string
	ResultAdminVersion      int64
	ResultPasswordVersion   int64
	ResultSessionVersion    int64
	ResultEnrollmentVersion int64
	AuditEventID            uuid.UUID
	CreatedAt               time.Time
}

// CommandReceiptRepository replays already-committed security commands without leaking transport details.
type CommandReceiptRepository interface {
	Save(context.Context, CommandReceipt) (CommandReceipt, error)
	Get(context.Context, uuid.UUID, idempotency.OperationID) (CommandReceipt, error)
}

// RecoveryCodeSetState is the authoritative version and remaining active-code count for the latest issued set.
type RecoveryCodeSetState struct {
	SetVersion      int64
	RemainingActive int64
}

// RecoveryCodeRepository serializes one-time code consumption and set rotation.
type RecoveryCodeRepository interface {
	Insert(context.Context, RecoveryCode) error
	FindActiveBySelector(context.Context, string) (RecoveryCode, error)
	GetActiveSetState(context.Context, uuid.UUID) (RecoveryCodeSetState, error)
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
	Elevations() ElevationRepository
	CommandReceipts() CommandReceiptRepository
	RecoveryCodes() RecoveryCodeRepository
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
