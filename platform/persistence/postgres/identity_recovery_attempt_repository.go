package postgres

import (
	"context"
	"math"
	"reflect"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/identifier"
	identityDomain "github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/jackc/pgx/v5/pgtype"
)

// identityRecoveryAttemptPurpose is the persisted protocol discriminator checked by migration 10.
const identityRecoveryAttemptPurpose = "identity.recovery"

type identityRecoveryAttemptQueries interface {
	CreateUserRecoveryAttempt(context.Context, sqlcgen.CreateUserRecoveryAttemptParams) (sqlcgen.UserRecoveryAttempt, error)
	GetUserRecoveryAttemptBySelector(context.Context, sqlcgen.GetUserRecoveryAttemptBySelectorParams) (sqlcgen.UserRecoveryAttempt, error)
	GetUserRecoveryAttemptForUpdate(context.Context, sqlcgen.GetUserRecoveryAttemptForUpdateParams) (sqlcgen.GetUserRecoveryAttemptForUpdateRow, error)
	RecordUserRecoveryAttemptFailureCAS(context.Context, sqlcgen.RecordUserRecoveryAttemptFailureCASParams) (sqlcgen.UserRecoveryAttempt, error)
	ConsumeUserRecoveryAttemptCAS(context.Context, sqlcgen.ConsumeUserRecoveryAttemptCASParams) (sqlcgen.UserRecoveryAttempt, error)
	RevokeUserRecoveryAttemptCAS(context.Context, sqlcgen.RevokeUserRecoveryAttemptCASParams) (sqlcgen.UserRecoveryAttempt, error)
}

type identityRecoveryAttemptRepository struct {
	queries identityRecoveryAttemptQueries
}

func (repository *identityRecoveryAttemptRepository) Insert(
	ctx context.Context,
	attempt identityDomain.RecoveryAttempt,
) (identityDomain.RecoveryAttempt, error) {
	snapshot := attempt.Snapshot()
	if snapshot.Status != identityDomain.RecoveryAttemptActive || snapshot.AttemptCount != 0 ||
		snapshot.Binding.RequestDigestSet || snapshot.Binding.RequestDigest != (idempotency.Digest{}) ||
		snapshot.GrantMAC.KeyVersion == 0 || snapshot.GrantMAC.KeyVersion > math.MaxInt32 ||
		snapshot.MaxAttempts == 0 || snapshot.MaxAttempts > math.MaxInt32 {
		return identityDomain.RecoveryAttempt{}, identityDomain.ErrInvalidRecoveryAttempt
	}
	params := sqlcgen.CreateUserRecoveryAttemptParams{
		RecoveryAttemptID: uuidToPG(snapshot.ID), GrantSelector: snapshot.Selector.Value(),
		GrantSecretHash: snapshot.GrantMAC.Value, GrantKeyVersion: int32(snapshot.GrantMAC.KeyVersion),
		UserID: uuidToPG(snapshot.Binding.UserID), ChallengeID: uuidToPG(snapshot.Binding.ChallengeID),
		OriginHash: snapshot.Binding.Origin[:], Purpose: identityRecoveryAttemptPurpose,
		MaxAttempts: int32(snapshot.MaxAttempts), CreatedAt: timeToPG(snapshot.CreatedAt),
		ExpiresAt: timeToPG(snapshot.ExpiresAt),
	}
	if snapshot.Binding.RecoveryCredentialID != uuid.Nil {
		params.RecoveryCredentialID = uuidToPG(snapshot.Binding.RecoveryCredentialID)
		params.RecoveryCredentialVersion = pgtype.Int8{
			Int64: int64(snapshot.Binding.RecoveryCredentialVersion), Valid: true,
		}
	} else {
		params.AssistedGrantID = uuidToPG(snapshot.Binding.AssistedGrantID)
	}
	row, err := repository.queries.CreateUserRecoveryAttempt(ctx, params)
	if err != nil {
		return identityDomain.RecoveryAttempt{}, mapIdentityQueryError(ctx, err, identityDomain.ErrRecoveryConcurrentTransition)
	}
	return identityRecoveryAttemptFromRow(row)
}

func (repository *identityRecoveryAttemptRepository) GetBySelector(
	ctx context.Context,
	selector identifier.Selector,
) (identityDomain.RecoveryAttempt, error) {
	if selector.ByteLength() != identityDomain.RecoveryGrantSelectorBytes {
		return identityDomain.RecoveryAttempt{}, identityDomain.ErrRecoveryInvalid
	}
	row, err := repository.queries.GetUserRecoveryAttemptBySelector(ctx, sqlcgen.GetUserRecoveryAttemptBySelectorParams{
		GrantSelector: selector.Value(),
	})
	if err != nil {
		return identityDomain.RecoveryAttempt{}, mapIdentityQueryError(ctx, err, identityDomain.ErrRecoveryInvalid)
	}
	return identityRecoveryAttemptFromRow(row)
}

func (repository *identityRecoveryAttemptRepository) GetForUpdate(
	ctx context.Context,
	selector identifier.Selector,
) (identityDomain.RecoveryAttempt, error) {
	if selector.ByteLength() != identityDomain.RecoveryGrantSelectorBytes {
		return identityDomain.RecoveryAttempt{}, identityDomain.ErrRecoveryInvalid
	}
	row, err := repository.queries.GetUserRecoveryAttemptForUpdate(ctx, sqlcgen.GetUserRecoveryAttemptForUpdateParams{
		GrantSelector: selector.Value(),
	})
	if err != nil {
		return identityDomain.RecoveryAttempt{}, mapIdentityQueryError(ctx, err, identityDomain.ErrRecoveryInvalid)
	}
	return identityRecoveryAttemptFromLockedRow(row)
}

func (repository *identityRecoveryAttemptRepository) RecordFailureCAS(
	ctx context.Context,
	current, next identityDomain.RecoveryAttempt,
) (identityDomain.RecoveryAttempt, error) {
	before, after := current.Snapshot(), next.Snapshot()
	normalized := after
	normalized.AttemptCount = before.AttemptCount
	normalized.Status = before.Status
	wantStatus := identityDomain.RecoveryAttemptActive
	if before.AttemptCount+1 == before.MaxAttempts {
		wantStatus = identityDomain.RecoveryAttemptExpired
	}
	if before.Status != identityDomain.RecoveryAttemptActive || after.AttemptCount != before.AttemptCount+1 ||
		after.Status != wantStatus || !reflect.DeepEqual(before, normalized) {
		return identityDomain.RecoveryAttempt{}, identityDomain.ErrInvalidRecoveryAttempt
	}
	row, err := repository.queries.RecordUserRecoveryAttemptFailureCAS(ctx, sqlcgen.RecordUserRecoveryAttemptFailureCASParams{
		NextAttemptCount: int32(after.AttemptCount), NextStatus: string(after.Status),
		RecoveryAttemptID: uuidToPG(before.ID), ExpectedAttemptCount: int32(before.AttemptCount),
	})
	if err != nil {
		return identityDomain.RecoveryAttempt{}, mapIdentityQueryError(ctx, err, identityDomain.ErrRecoveryConcurrentTransition)
	}
	return identityRecoveryAttemptFromRow(row)
}

func (repository *identityRecoveryAttemptRepository) ConsumeCAS(
	ctx context.Context,
	current, next identityDomain.RecoveryAttempt,
) (identityDomain.RecoveryAttempt, error) {
	before, after := current.Snapshot(), next.Snapshot()
	normalized := after
	normalized.Status = before.Status
	normalized.ConsumedAt = before.ConsumedAt
	normalized.ResultID = before.ResultID
	normalized.Binding.RequestDigestSet = before.Binding.RequestDigestSet
	normalized.Binding.RequestDigest = before.Binding.RequestDigest
	if before.Status != identityDomain.RecoveryAttemptActive || after.Status != identityDomain.RecoveryAttemptConsumed ||
		before.Binding.RequestDigestSet || !after.Binding.RequestDigestSet || after.ResultID == uuid.Nil ||
		after.ConsumedAt.IsZero() || !reflect.DeepEqual(before, normalized) {
		return identityDomain.RecoveryAttempt{}, identityDomain.ErrInvalidRecoveryAttempt
	}
	row, err := repository.queries.ConsumeUserRecoveryAttemptCAS(ctx, sqlcgen.ConsumeUserRecoveryAttemptCASParams{
		ConsumedAt: timeToPG(after.ConsumedAt), RequestDigest: after.Binding.RequestDigest[:], ResultID: uuidToPG(after.ResultID),
		RecoveryAttemptID: uuidToPG(before.ID), ExpectedAttemptCount: int32(before.AttemptCount),
	})
	if err != nil {
		return identityDomain.RecoveryAttempt{}, mapIdentityQueryError(ctx, err, identityDomain.ErrRecoveryConcurrentTransition)
	}
	stored, mapErr := identityRecoveryAttemptFromRow(row)
	if mapErr != nil {
		return identityDomain.RecoveryAttempt{}, mapErr
	}
	if !reflect.DeepEqual(stored.Snapshot(), after) {
		return identityDomain.RecoveryAttempt{}, identityDomain.ErrIdentityIntegrity
	}
	return stored, nil
}

func (repository *identityRecoveryAttemptRepository) RevokeCAS(
	ctx context.Context,
	current, next identityDomain.RecoveryAttempt,
) (identityDomain.RecoveryAttempt, error) {
	before, after := current.Snapshot(), next.Snapshot()
	normalized := after
	normalized.Status = before.Status
	normalized.RevokedAt = before.RevokedAt
	if before.Status != identityDomain.RecoveryAttemptActive || after.Status != identityDomain.RecoveryAttemptRevoked ||
		after.RevokedAt.IsZero() || !reflect.DeepEqual(before, normalized) {
		return identityDomain.RecoveryAttempt{}, identityDomain.ErrInvalidRecoveryAttempt
	}
	row, err := repository.queries.RevokeUserRecoveryAttemptCAS(ctx, sqlcgen.RevokeUserRecoveryAttemptCASParams{
		RevokedAt: timeToPG(after.RevokedAt), RecoveryAttemptID: uuidToPG(before.ID),
		ExpectedAttemptCount: int32(before.AttemptCount),
	})
	if err != nil {
		return identityDomain.RecoveryAttempt{}, mapIdentityQueryError(ctx, err, identityDomain.ErrRecoveryConcurrentTransition)
	}
	return identityRecoveryAttemptFromRow(row)
}

var _ identityDomain.RecoveryAttemptRepository = (*identityRecoveryAttemptRepository)(nil)
