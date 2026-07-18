package postgres

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/identifier"
	identityDomain "github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type identityChallengeQueries interface {
	CreateAnonymousChallenge(context.Context, sqlcgen.CreateAnonymousChallengeParams) (sqlcgen.AnonymousChallenge, error)
	GetAnonymousChallengeForUpdate(context.Context, sqlcgen.GetAnonymousChallengeForUpdateParams) (sqlcgen.AnonymousChallenge, error)
	RecordAnonymousChallengeFailureCAS(context.Context, sqlcgen.RecordAnonymousChallengeFailureCASParams) (sqlcgen.RecordAnonymousChallengeFailureCASRow, error)
	ConsumeAnonymousChallengeCAS(context.Context, sqlcgen.ConsumeAnonymousChallengeCASParams) (sqlcgen.ConsumeAnonymousChallengeCASRow, error)
	ConsumeAnonymousChallengeWithoutReplayCAS(context.Context, sqlcgen.ConsumeAnonymousChallengeWithoutReplayCASParams) (sqlcgen.ConsumeAnonymousChallengeWithoutReplayCASRow, error)
}

type identityChallengeRepository struct {
	queries identityChallengeQueries
}

// Insert persists a newly issued identity challenge and rejects malformed restored state.
func (repository *identityChallengeRepository) Insert(ctx context.Context, record identityDomain.Challenge) error {
	snapshot := record.Snapshot()
	if record.State(snapshot.CreatedAt) != challenge.StateActive || snapshot.AttemptCount != 0 ||
		validateChallengeCounterRange(snapshot.AttemptCount, snapshot.MaxAttempts) != nil || snapshot.SecretMAC.KeyVersion > math.MaxInt32 {
		return challenge.ErrInvalidInput
	}
	_, err := repository.queries.CreateAnonymousChallenge(ctx, sqlcgen.CreateAnonymousChallengeParams{
		ChallengeID: uuidToPG(snapshot.ID), Selector: snapshot.Selector.Value(),
		SecretHash: snapshot.SecretMAC.Value, SecretKeyVersion: int32(snapshot.SecretMAC.KeyVersion),
		Purpose: string(snapshot.Binding.Purpose), Audience: string(snapshot.Binding.Audience),
		OriginHash: snapshot.Binding.Origin.Bytes(), RequestFlowID: string(snapshot.Binding.RequestFlowID),
		MaxAttempts: int32(snapshot.MaxAttempts), CreatedAt: timeToPG(snapshot.CreatedAt), ExpiresAt: timeToPG(snapshot.ExpiresAt),
	})
	return mapChallengeQueryError(ctx, err, challenge.ErrRepositoryUnavailable)
}

// GetForUpdate locks an identity challenge by its public selector for verification and transition.
func (repository *identityChallengeRepository) GetForUpdate(ctx context.Context, selector identifier.Selector) (identityDomain.Challenge, error) {
	if selector.ByteLength() != challenge.SelectorBytes {
		return identityDomain.Challenge{}, challenge.ErrInvalidInput
	}
	row, err := repository.queries.GetAnonymousChallengeForUpdate(ctx, sqlcgen.GetAnonymousChallengeForUpdateParams{Selector: selector.Value()})
	if err != nil {
		return identityDomain.Challenge{}, mapChallengeQueryError(ctx, err, challenge.ErrNotFound)
	}
	return identityChallengeFromRow(row)
}

// RecordFailureCAS increments exactly the aggregate version supplied by the transaction's locked read.
func (repository *identityChallengeRepository) RecordFailureCAS(ctx context.Context, current identityDomain.Challenge, attemptedAt time.Time) (identityDomain.Challenge, error) {
	next, err := current.RecordFailure(attemptedAt)
	if err != nil {
		return identityDomain.Challenge{}, err
	}
	row, err := repository.queries.RecordAnonymousChallengeFailureCAS(ctx, sqlcgen.RecordAnonymousChallengeFailureCASParams{
		ChallengeID: uuidToPG(current.Snapshot().ID), AttemptedAt: timeToPG(attemptedAt),
	})
	if err != nil {
		return identityDomain.Challenge{}, mapChallengeQueryError(ctx, err, challenge.ErrConcurrentTransition)
	}
	nextSnapshot := next.Snapshot()
	if !row.ChallengeID.Valid || uuid.UUID(row.ChallengeID.Bytes) != nextSnapshot.ID ||
		row.AttemptCount != int32(nextSnapshot.AttemptCount) || row.MaxAttempts != int32(nextSnapshot.MaxAttempts) {
		return identityDomain.Challenge{}, challenge.ErrIntegrity
	}
	return next, nil
}

// ConsumeCAS records either a terminal no-result use or an exact-result replay authorization.
func (repository *identityChallengeRepository) ConsumeCAS(ctx context.Context, consumed identityDomain.Challenge) (identityDomain.Challenge, error) {
	snapshot := consumed.Snapshot()
	if snapshot.ConsumedAt.IsZero() || !snapshot.RevokedAt.IsZero() {
		return identityDomain.Challenge{}, challenge.ErrInvalidInput
	}
	if snapshot.Replay == nil {
		row, err := repository.queries.ConsumeAnonymousChallengeWithoutReplayCAS(ctx, sqlcgen.ConsumeAnonymousChallengeWithoutReplayCASParams{
			ConsumedAt: timeToPG(snapshot.ConsumedAt), ChallengeID: uuidToPG(snapshot.ID),
		})
		if err != nil {
			return identityDomain.Challenge{}, mapChallengeQueryError(ctx, err, challenge.ErrConcurrentTransition)
		}
		if !row.ChallengeID.Valid || uuid.UUID(row.ChallengeID.Bytes) != snapshot.ID || !row.ConsumedAt.Valid {
			return identityDomain.Challenge{}, challenge.ErrIntegrity
		}
		return consumed, nil
	}
	replay := snapshot.Replay
	row, err := repository.queries.ConsumeAnonymousChallengeCAS(ctx, sqlcgen.ConsumeAnonymousChallengeCASParams{
		ConsumedAt: timeToPG(snapshot.ConsumedAt), ReplayUntil: timeToPG(replay.ReplayUntil),
		OperationID: pgtype.Text{String: replay.OperationID.Value(), Valid: true}, RequestDigest: replay.RequestDigest.Bytes(),
		ResultID: uuidToPG(replay.ResultID), ChallengeID: uuidToPG(snapshot.ID),
	})
	if err != nil {
		return identityDomain.Challenge{}, mapChallengeQueryError(ctx, err, challenge.ErrConcurrentTransition)
	}
	if !row.ChallengeID.Valid || uuid.UUID(row.ChallengeID.Bytes) != snapshot.ID || !row.ResultID.Valid || uuid.UUID(row.ResultID.Bytes) != replay.ResultID {
		return identityDomain.Challenge{}, challenge.ErrIntegrity
	}
	return consumed, nil
}

func mapChallengeQueryError(ctx context.Context, err, noRowsError error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return noRowsError
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	return challenge.ErrRepositoryUnavailable
}

var _ identityDomain.ChallengeRepository = (*identityChallengeRepository)(nil)
