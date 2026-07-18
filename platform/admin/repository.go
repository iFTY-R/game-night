package admin

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/secretresult"
)

// ChallengeRepository persists validated admin aggregates and applies generation-aware CAS transitions.
type ChallengeRepository interface {
	Insert(context.Context, Challenge) error
	GetForUpdate(context.Context, identifier.Selector) (Challenge, error)
	RecordFailureCAS(context.Context, Challenge, time.Time) (Challenge, error)
	ConsumeCAS(context.Context, Challenge) (Challenge, error)
	RevokeActiveByAdminID(context.Context, uuid.UUID, time.Time) (int64, error)
}

// ChallengeTransaction exposes repositories bound to one database transaction.
// Admin generation checks, challenge consumption, and result creation must commit or roll back together.
type ChallengeTransaction interface {
	Challenges() ChallengeRepository
	SecretResults() secretresult.Repository
}

// ChallengeTransactionWork receives transaction-scoped ports and must not retain them after returning.
type ChallengeTransactionWork func(context.Context, ChallengeTransaction) error

// AuthorizedChallengeCompletion carries either a no-result terminal decision or the exact persisted replay result.
// Private fields force application workflows through the reviewed constructors.
type AuthorizedChallengeCompletion struct {
	withoutReplay bool
	result        secretresult.Result
}

// NoReplayCompletion terminates a successful administrator operation that returns no one-time secret.
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

// AuthorizedChallengeWork runs under the account-first locks that authenticated the challenge.
// First-use work returns a reviewed completion; the service owns the generation-aware terminal transition.
type AuthorizedChallengeWork func(
	context.Context,
	ChallengeTransaction,
	Challenge,
	challenge.Authorization,
) (AuthorizedChallengeCompletion, error)

// ChallengeUnitOfWork owns commit and rollback without exposing pgx or generated queries to admin services.
type ChallengeUnitOfWork interface {
	Run(context.Context, ChallengeTransactionWork) error
}
