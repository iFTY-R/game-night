package identity

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/secretresult"
)

// ChallengeRepository persists only validated identity aggregates and performs transitions with CAS semantics.
type ChallengeRepository interface {
	Insert(context.Context, Challenge) error
	GetForUpdate(context.Context, identifier.Selector) (Challenge, error)
	RecordFailureCAS(context.Context, Challenge, time.Time) (Challenge, error)
	ConsumeCAS(context.Context, Challenge) (Challenge, error)
}

// ChallengeTransaction exposes repositories bound to one database transaction.
// Challenge consumption and secret result insertion must use the same value for exact replay to be authoritative.
type ChallengeTransaction interface {
	Challenges() ChallengeRepository
	SecretResults() secretresult.Repository
	Users() UserRepository
	UsernameClaims() UsernameClaimRepository
	Devices() DeviceRepository
	RecoveryCredentials() RecoveryCredentialRepository
}

// ChallengeTransactionWork receives transaction-scoped ports and must not retain them after returning.
type ChallengeTransactionWork func(context.Context, ChallengeTransaction) error

// AuthorizedChallengeCompletion carries either a no-result terminal decision or the exact persisted replay result.
// Private fields force application workflows through the reviewed constructors.
type AuthorizedChallengeCompletion struct {
	withoutReplay bool
	result        secretresult.Result
}

// NoReplayCompletion terminates a successful identity operation that returns no one-time secret.
func NoReplayCompletion() AuthorizedChallengeCompletion {
	return AuthorizedChallengeCompletion{withoutReplay: true}
}

// NewReplayCompletion binds first-use completion to a validated secret result value.
// The service still rereads and verifies the persisted row before consuming the challenge.
func NewReplayCompletion(result secretresult.Result) (AuthorizedChallengeCompletion, error) {
	snapshot := result.Snapshot()
	if snapshot.ID == uuid.Nil || snapshot.Status != secretresult.StatusAvailable || snapshot.Binding.Validate() != nil {
		return AuthorizedChallengeCompletion{}, challenge.ErrInvalidInput
	}
	return AuthorizedChallengeCompletion{result: result}, nil
}

// AuthorizedChallengeWork runs under the row lock that authenticated the challenge.
// First-use work returns a reviewed completion; the service owns the terminal database transition.
type AuthorizedChallengeWork func(
	context.Context,
	ChallengeTransaction,
	Challenge,
	challenge.Authorization,
) (AuthorizedChallengeCompletion, error)

// ChallengeUnitOfWork owns commit and rollback without exposing pgx or generated queries to identity services.
type ChallengeUnitOfWork interface {
	Run(context.Context, ChallengeTransactionWork) error
}

// UserRepository persists the user aggregate and uses operation-specific CAS updates for status transitions.
type UserRepository interface {
	Insert(context.Context, User) (User, error)
	GetByID(context.Context, uuid.UUID) (User, error)
	GetForUpdate(context.Context, uuid.UUID) (User, error)
	CompleteOnboardingCAS(context.Context, User, User) (User, error)
	ChangeUsernameCAS(context.Context, User, User) (User, error)
}

// UsernameClaimRepository owns the single global current/history username registry.
type UsernameClaimRepository interface {
	Claim(context.Context, UsernameClaim, time.Time) (UsernameClaim, error)
	GetForUpdate(context.Context, string) (UsernameClaim, error)
	ReserveCAS(context.Context, UsernameClaim, UsernameClaim) (UsernameClaim, error)
}

// DeviceRepository fixes authenticated transaction lock order and generation-aware persistence.
type DeviceRepository interface {
	Insert(context.Context, DeviceCredential) (DeviceCredential, error)
	GetIdentityForUpdate(context.Context, uuid.UUID) (User, DeviceCredential, error)
	GetForUpdate(context.Context, uuid.UUID) (DeviceCredential, error)
	List(context.Context, DeviceListRequest) ([]DeviceSummary, error)
	TouchCAS(context.Context, DeviceCredential, DeviceCredential) (DeviceCredential, error)
	RotateCAS(context.Context, DeviceCredential, DeviceCredential) (DeviceCredential, error)
	RevokeCAS(context.Context, DeviceCredential, DeviceCredential) (DeviceCredential, error)
	RevokeOtherActiveForRecovery(context.Context, uuid.UUID, uuid.UUID, time.Time) ([]DeviceSummary, error)
}

// RecoveryCredentialRepository owns selector lookup and source consumption/rotation under explicit CAS.
type RecoveryCredentialRepository interface {
	Insert(context.Context, RecoveryCredential) (RecoveryCredential, error)
	GetBySelector(context.Context, identifier.Selector) (RecoveryCredential, error)
	GetForUpdate(context.Context, uuid.UUID, uuid.UUID, uint64) (RecoveryCredential, error)
	GetActiveForUserForUpdate(context.Context, uuid.UUID) (RecoveryCredential, error)
	ConsumeCAS(context.Context, RecoveryCredential, RecoveryCredential) (RecoveryCredential, error)
	RevokeCAS(context.Context, RecoveryCredential, RecoveryCredential) (RecoveryCredential, error)
}

// RecoveryAttemptRepository persists the short-lived HMAC grant and its failure/consumption transitions.
type RecoveryAttemptRepository interface {
	Insert(context.Context, RecoveryAttempt) (RecoveryAttempt, error)
	GetBySelector(context.Context, identifier.Selector) (RecoveryAttempt, error)
	GetForUpdate(context.Context, identifier.Selector) (RecoveryAttempt, error)
	RecordFailureCAS(context.Context, RecoveryAttempt, RecoveryAttempt) (RecoveryAttempt, error)
	ConsumeCAS(context.Context, RecoveryAttempt, RecoveryAttempt) (RecoveryAttempt, error)
	RevokeCAS(context.Context, RecoveryAttempt, RecoveryAttempt) (RecoveryAttempt, error)
}

// AssistedRecoveryGrantRepository exposes only identity-side authentication and terminal transitions.
type AssistedRecoveryGrantRepository interface {
	GetBySelector(context.Context, identifier.Selector) (AssistedRecoveryGrant, error)
	GetForUpdate(context.Context, uuid.UUID, uuid.UUID) (AssistedRecoveryGrant, error)
	RecordFailureCAS(context.Context, AssistedRecoveryGrant, AssistedRecoveryGrant) (AssistedRecoveryGrant, error)
	ConsumeCAS(context.Context, AssistedRecoveryGrant, AssistedRecoveryGrant) (AssistedRecoveryGrant, error)
	RevokeActiveForUser(context.Context, uuid.UUID, uuid.UUID, time.Time) ([]uuid.UUID, error)
}

// IdentityTransaction adds security-event and recovery participants to the narrower challenge transaction.
type IdentityTransaction interface {
	ChallengeTransaction
	RecoveryAttempts() RecoveryAttemptRepository
	AssistedRecoveryGrants() AssistedRecoveryGrantRepository
	Audit() audit.Repository
	AuditCheckpoints() audit.CheckpointRepository
	OutboxEvents() outbox.EventRepository
}

// IdentityTransactionWork receives all repositories bound to one database transaction.
type IdentityTransactionWork func(context.Context, IdentityTransaction) error

// IdentityUnitOfWork supports existing challenge callbacks and the complete security transaction surface.
type IdentityUnitOfWork interface {
	ChallengeUnitOfWork
	RunIdentity(context.Context, IdentityTransactionWork) error
}
