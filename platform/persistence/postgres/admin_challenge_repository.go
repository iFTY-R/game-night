package postgres

import (
	"context"
	"math"
	"time"

	"github.com/google/uuid"
	adminDomain "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/jackc/pgx/v5/pgtype"
)

type adminChallengeQueries interface {
	CreateAdminChallenge(context.Context, sqlcgen.CreateAdminChallengeParams) (sqlcgen.AdminChallenge, error)
	GetAdminChallengeForUpdate(context.Context, sqlcgen.GetAdminChallengeForUpdateParams) (sqlcgen.AdminChallenge, error)
	RecordAdminChallengeFailureCAS(context.Context, sqlcgen.RecordAdminChallengeFailureCASParams) (sqlcgen.RecordAdminChallengeFailureCASRow, error)
	ConsumeAdminChallengeCAS(context.Context, sqlcgen.ConsumeAdminChallengeCASParams) (sqlcgen.ConsumeAdminChallengeCASRow, error)
	RevokeAdminChallenges(context.Context, sqlcgen.RevokeAdminChallengesParams) (int64, error)
}

type adminChallengeRepository struct {
	queries adminChallengeQueries
}

// Insert persists one generation-bound administrator challenge.
func (repository *adminChallengeRepository) Insert(ctx context.Context, record adminDomain.Challenge) error {
	snapshot := record.Snapshot()
	if record.State(snapshot.CreatedAt) != challenge.StateActive || snapshot.AttemptCount != 0 || !snapshot.Binding.Subject.Bound() ||
		validateChallengeCounterRange(snapshot.AttemptCount, snapshot.MaxAttempts) != nil || snapshot.SecretMAC.KeyVersion > math.MaxInt32 {
		return challenge.ErrInvalidInput
	}
	_, err := repository.queries.CreateAdminChallenge(ctx, sqlcgen.CreateAdminChallengeParams{
		ChallengeID: uuidToPG(snapshot.ID), AdminID: uuidToPG(snapshot.Binding.Subject.ID),
		Selector: snapshot.Selector.Value(), SecretHash: snapshot.SecretMAC.Value, SecretKeyVersion: int32(snapshot.SecretMAC.KeyVersion),
		Purpose: string(snapshot.Binding.Purpose), Audience: string(snapshot.Binding.Audience),
		AdminVersion: snapshot.Binding.Subject.Version, PasswordVersion: snapshot.Binding.Subject.CredentialVersion,
		OriginHash: snapshot.Binding.Origin.Bytes(), RequestFlowID: string(snapshot.Binding.RequestFlowID),
		MaxAttempts: int32(snapshot.MaxAttempts), CreatedAt: timeToPG(snapshot.CreatedAt), ExpiresAt: timeToPG(snapshot.ExpiresAt),
	})
	return mapChallengeQueryError(ctx, err, challenge.ErrRepositoryUnavailable)
}

// GetForUpdate locks the current account generation before its challenge so security-state changes serialize first.
func (repository *adminChallengeRepository) GetForUpdate(ctx context.Context, selector identifier.Selector) (adminDomain.Challenge, error) {
	if selector.ByteLength() != challenge.SelectorBytes {
		return adminDomain.Challenge{}, challenge.ErrInvalidInput
	}
	row, err := repository.queries.GetAdminChallengeForUpdate(ctx, sqlcgen.GetAdminChallengeForUpdateParams{Selector: selector.Value()})
	if err != nil {
		return adminDomain.Challenge{}, mapChallengeQueryError(ctx, err, challenge.ErrNotFound)
	}
	return adminChallengeFromRow(row)
}

// RecordFailureCAS increments an active administrator challenge without changing its account binding.
func (repository *adminChallengeRepository) RecordFailureCAS(ctx context.Context, current adminDomain.Challenge, attemptedAt time.Time) (adminDomain.Challenge, error) {
	next, err := current.RecordFailure(attemptedAt)
	if err != nil {
		return adminDomain.Challenge{}, err
	}
	row, err := repository.queries.RecordAdminChallengeFailureCAS(ctx, sqlcgen.RecordAdminChallengeFailureCASParams{
		ChallengeID: uuidToPG(current.Snapshot().ID), AttemptedAt: timeToPG(attemptedAt),
	})
	if err != nil {
		return adminDomain.Challenge{}, mapChallengeQueryError(ctx, err, challenge.ErrConcurrentTransition)
	}
	nextSnapshot := next.Snapshot()
	if !row.ChallengeID.Valid || uuid.UUID(row.ChallengeID.Bytes) != nextSnapshot.ID || row.Status != "active" ||
		row.AttemptCount != int32(nextSnapshot.AttemptCount) || row.MaxAttempts != int32(nextSnapshot.MaxAttempts) {
		return adminDomain.Challenge{}, challenge.ErrIntegrity
	}
	return next, nil
}

// ConsumeCAS applies live account-generation and status CAS for first use, with optional exact-result replay metadata.
func (repository *adminChallengeRepository) ConsumeCAS(ctx context.Context, consumed adminDomain.Challenge) (adminDomain.Challenge, error) {
	snapshot := consumed.Snapshot()
	if snapshot.ConsumedAt.IsZero() || !snapshot.RevokedAt.IsZero() || !snapshot.Binding.Subject.Bound() {
		return adminDomain.Challenge{}, challenge.ErrInvalidInput
	}
	params := sqlcgen.ConsumeAdminChallengeCASParams{
		ConsumedAt: timeToPG(snapshot.ConsumedAt), ChallengeID: uuidToPG(snapshot.ID),
		ExpectedAdminVersion: snapshot.Binding.Subject.Version, ExpectedPasswordVersion: snapshot.Binding.Subject.CredentialVersion,
	}
	if snapshot.Replay != nil {
		params.ReplayUntil = timeToPG(snapshot.Replay.ReplayUntil)
		params.OperationID = pgtype.Text{String: snapshot.Replay.OperationID.Value(), Valid: true}
		params.RequestDigest = snapshot.Replay.RequestDigest.Bytes()
		params.ResultID = uuidToPG(snapshot.Replay.ResultID)
	}
	row, err := repository.queries.ConsumeAdminChallengeCAS(ctx, params)
	if err != nil {
		return adminDomain.Challenge{}, mapChallengeQueryError(ctx, err, challenge.ErrConcurrentTransition)
	}
	if !row.ChallengeID.Valid || uuid.UUID(row.ChallengeID.Bytes) != snapshot.ID || row.Status != "consumed" {
		return adminDomain.Challenge{}, challenge.ErrIntegrity
	}
	return consumed, nil
}

// RevokeActiveByAdminID invalidates every pending challenge when account security state changes.
func (repository *adminChallengeRepository) RevokeActiveByAdminID(ctx context.Context, adminID uuid.UUID, revokedAt time.Time) (int64, error) {
	if adminID == uuid.Nil || revokedAt.IsZero() {
		return 0, challenge.ErrInvalidInput
	}
	count, err := repository.queries.RevokeAdminChallenges(ctx, sqlcgen.RevokeAdminChallengesParams{
		RevokedAt: timeToPG(revokedAt), AdminID: uuidToPG(adminID),
	})
	if err != nil {
		return 0, mapChallengeQueryError(ctx, err, challenge.ErrRepositoryUnavailable)
	}
	return count, nil
}

var _ adminDomain.ChallengeRepository = (*adminChallengeRepository)(nil)
