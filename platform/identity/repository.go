package identity

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/identifier"
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
	TouchCAS(context.Context, DeviceCredential, DeviceCredential) (DeviceCredential, error)
	RotateCAS(context.Context, DeviceCredential, DeviceCredential) (DeviceCredential, error)
}

// RecoveryCredentialRepository stores the initial active credential during onboarding.
type RecoveryCredentialRepository interface {
	Insert(context.Context, RecoveryCredential) (RecoveryCredential, error)
}

// IdentityTransaction is the full user-side transaction surface; it aliases the challenge transaction intentionally.
type IdentityTransaction = ChallengeTransaction

// IdentityTransactionWork receives all repositories bound to one database transaction.
type IdentityTransactionWork = ChallengeTransactionWork

// IdentityUnitOfWork commits user, device, challenge, claim, recovery, and result changes atomically.
type IdentityUnitOfWork = ChallengeUnitOfWork
