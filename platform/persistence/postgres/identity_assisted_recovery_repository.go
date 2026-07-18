package postgres

import (
	"context"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/identifier"
	identityDomain "github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/jackc/pgx/v5/pgtype"
)

type identityAssistedRecoveryQueries interface {
	GetAdminAssistedRecoveryGrantBySelector(context.Context, sqlcgen.GetAdminAssistedRecoveryGrantBySelectorParams) (sqlcgen.AdminAssistedRecoveryGrant, error)
	GetAdminAssistedRecoveryGrantForUpdate(context.Context, sqlcgen.GetAdminAssistedRecoveryGrantForUpdateParams) (sqlcgen.AdminAssistedRecoveryGrant, error)
	RecordAdminAssistedRecoveryFailureCAS(context.Context, sqlcgen.RecordAdminAssistedRecoveryFailureCASParams) (sqlcgen.AdminAssistedRecoveryGrant, error)
	ConsumeAdminAssistedRecoveryGrantCAS(context.Context, sqlcgen.ConsumeAdminAssistedRecoveryGrantCASParams) (sqlcgen.AdminAssistedRecoveryGrant, error)
	RevokeActiveAdminAssistedRecoveryGrantsForUser(context.Context, sqlcgen.RevokeActiveAdminAssistedRecoveryGrantsForUserParams) ([]pgtype.UUID, error)
}

type identityAssistedRecoveryRepository struct {
	queries identityAssistedRecoveryQueries
}

func (repository *identityAssistedRecoveryRepository) GetBySelector(
	ctx context.Context,
	selector identifier.Selector,
) (identityDomain.AssistedRecoveryGrant, error) {
	if selector.ByteLength() != identityDomain.AssistedRecoverySelectorBytes {
		return identityDomain.AssistedRecoveryGrant{}, identityDomain.ErrRecoveryInvalid
	}
	row, err := repository.queries.GetAdminAssistedRecoveryGrantBySelector(
		ctx, sqlcgen.GetAdminAssistedRecoveryGrantBySelectorParams{Selector: selector.Value()},
	)
	if err != nil {
		return identityDomain.AssistedRecoveryGrant{}, mapIdentityQueryError(ctx, err, identityDomain.ErrRecoveryInvalid)
	}
	return identityAssistedRecoveryGrantFromRow(row)
}

func (repository *identityAssistedRecoveryRepository) GetForUpdate(
	ctx context.Context,
	grantID, userID uuid.UUID,
) (identityDomain.AssistedRecoveryGrant, error) {
	if grantID == uuid.Nil || userID == uuid.Nil {
		return identityDomain.AssistedRecoveryGrant{}, identityDomain.ErrInvalidAssistedRecoveryGrant
	}
	row, err := repository.queries.GetAdminAssistedRecoveryGrantForUpdate(
		ctx, sqlcgen.GetAdminAssistedRecoveryGrantForUpdateParams{
			AssistedGrantID: uuidToPG(grantID), TargetUserID: uuidToPG(userID),
		},
	)
	if err != nil {
		return identityDomain.AssistedRecoveryGrant{}, mapIdentityQueryError(ctx, err, identityDomain.ErrRecoveryConcurrentTransition)
	}
	return identityAssistedRecoveryGrantFromRow(row)
}

func (repository *identityAssistedRecoveryRepository) RecordFailureCAS(
	ctx context.Context,
	current, next identityDomain.AssistedRecoveryGrant,
) (identityDomain.AssistedRecoveryGrant, error) {
	before, after := current.Snapshot(), next.Snapshot()
	normalized := after
	normalized.AttemptCount = before.AttemptCount
	normalized.Status = before.Status
	wantStatus := identityDomain.AssistedRecoveryGrantActive
	if before.AttemptCount+1 == before.MaxAttempts {
		wantStatus = identityDomain.AssistedRecoveryGrantExpired
	}
	if before.Status != identityDomain.AssistedRecoveryGrantActive || after.AttemptCount != before.AttemptCount+1 ||
		after.Status != wantStatus || !reflect.DeepEqual(before, normalized) {
		return identityDomain.AssistedRecoveryGrant{}, identityDomain.ErrInvalidAssistedRecoveryGrant
	}
	row, err := repository.queries.RecordAdminAssistedRecoveryFailureCAS(
		ctx, sqlcgen.RecordAdminAssistedRecoveryFailureCASParams{
			NextAttemptCount: int32(after.AttemptCount), NextStatus: string(after.Status),
			AssistedGrantID: uuidToPG(before.ID), ExpectedAttemptCount: int32(before.AttemptCount),
		},
	)
	if err != nil {
		return identityDomain.AssistedRecoveryGrant{}, mapIdentityQueryError(ctx, err, identityDomain.ErrRecoveryConcurrentTransition)
	}
	return identityAssistedRecoveryGrantFromRow(row)
}

func (repository *identityAssistedRecoveryRepository) ConsumeCAS(
	ctx context.Context,
	current, next identityDomain.AssistedRecoveryGrant,
) (identityDomain.AssistedRecoveryGrant, error) {
	before, after := current.Snapshot(), next.Snapshot()
	normalized := after
	normalized.Status = before.Status
	normalized.ConsumedAt = before.ConsumedAt
	normalized.ResultID = before.ResultID
	if before.Status != identityDomain.AssistedRecoveryGrantActive || after.Status != identityDomain.AssistedRecoveryGrantConsumed ||
		after.ConsumedAt.IsZero() || after.ResultID == uuid.Nil || !reflect.DeepEqual(before, normalized) {
		return identityDomain.AssistedRecoveryGrant{}, identityDomain.ErrInvalidAssistedRecoveryGrant
	}
	row, err := repository.queries.ConsumeAdminAssistedRecoveryGrantCAS(
		ctx, sqlcgen.ConsumeAdminAssistedRecoveryGrantCASParams{
			ConsumedAt: timeToPG(after.ConsumedAt), ResultID: uuidToPG(after.ResultID),
			AssistedGrantID: uuidToPG(before.ID), UserID: uuidToPG(before.UserID),
			ExpectedAttemptCount: int32(before.AttemptCount),
		},
	)
	if err != nil {
		return identityDomain.AssistedRecoveryGrant{}, mapIdentityQueryError(ctx, err, identityDomain.ErrRecoveryConcurrentTransition)
	}
	return identityAssistedRecoveryGrantFromRow(row)
}

func (repository *identityAssistedRecoveryRepository) RevokeActiveForUser(
	ctx context.Context,
	userID, preservedGrantID uuid.UUID,
	revokedAt time.Time,
) ([]uuid.UUID, error) {
	if userID == uuid.Nil || revokedAt.IsZero() {
		return nil, identityDomain.ErrInvalidAssistedRecoveryGrant
	}
	rows, err := repository.queries.RevokeActiveAdminAssistedRecoveryGrantsForUser(
		ctx, sqlcgen.RevokeActiveAdminAssistedRecoveryGrantsForUserParams{
			RevokedAt: timeToPG(revokedAt), UserID: uuidToPG(userID),
			PreservedAssistedGrantID: uuidToPG(preservedGrantID),
		},
	)
	if err != nil {
		return nil, mapIdentityQueryError(ctx, err, identityDomain.ErrRecoveryConcurrentTransition)
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		if !row.Valid {
			return nil, identityDomain.ErrIdentityIntegrity
		}
		ids = append(ids, uuid.UUID(row.Bytes))
	}
	return ids, nil
}

var _ identityDomain.AssistedRecoveryGrantRepository = (*identityAssistedRecoveryRepository)(nil)
