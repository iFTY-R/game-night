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
