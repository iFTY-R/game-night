package postgres

import (
	"context"
	"math"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/identifier"
	identityDomain "github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
)

type identityRecoveryQueries interface {
	CreateUserRecoveryCredential(context.Context, sqlcgen.CreateUserRecoveryCredentialParams) (sqlcgen.UserRecoveryCredential, error)
	GetUserRecoveryCredentialBySelector(context.Context, sqlcgen.GetUserRecoveryCredentialBySelectorParams) (sqlcgen.UserRecoveryCredential, error)
	GetUserRecoveryCredentialForUpdate(context.Context, sqlcgen.GetUserRecoveryCredentialForUpdateParams) (sqlcgen.UserRecoveryCredential, error)
	GetActiveUserRecoveryCredentialForUpdate(context.Context, sqlcgen.GetActiveUserRecoveryCredentialForUpdateParams) (sqlcgen.UserRecoveryCredential, error)
	ConsumeUserRecoveryCredentialCAS(context.Context, sqlcgen.ConsumeUserRecoveryCredentialCASParams) (sqlcgen.UserRecoveryCredential, error)
	RevokeUserRecoveryCredentialCAS(context.Context, sqlcgen.RevokeUserRecoveryCredentialCASParams) (sqlcgen.UserRecoveryCredential, error)
}

func (repository *identityRecoveryRepository) GetBySelector(
	ctx context.Context,
	selector identifier.Selector,
) (identityDomain.RecoveryCredential, error) {
	if selector.ByteLength() != identityDomain.RecoverySelectorBytes {
		return identityDomain.RecoveryCredential{}, identityDomain.ErrRecoveryInvalid
	}
	row, err := repository.queries.GetUserRecoveryCredentialBySelector(ctx, sqlcgen.GetUserRecoveryCredentialBySelectorParams{
		Selector: selector.Value(),
	})
	if err != nil {
		return identityDomain.RecoveryCredential{}, mapIdentityQueryError(ctx, err, identityDomain.ErrRecoveryInvalid)
	}
	return identityRecoveryFromRow(row)
}

func (repository *identityRecoveryRepository) GetForUpdate(
	ctx context.Context,
	credentialID, userID uuid.UUID,
	version uint64,
) (identityDomain.RecoveryCredential, error) {
	if credentialID == uuid.Nil || userID == uuid.Nil || version == 0 || version > math.MaxInt64 {
		return identityDomain.RecoveryCredential{}, identityDomain.ErrInvalidRecoveryCredential
	}
	row, err := repository.queries.GetUserRecoveryCredentialForUpdate(ctx, sqlcgen.GetUserRecoveryCredentialForUpdateParams{
		RecoveryCredentialID: uuidToPG(credentialID), UserID: uuidToPG(userID), ExpectedVersion: int64(version),
	})
	if err != nil {
		return identityDomain.RecoveryCredential{}, mapIdentityQueryError(ctx, err, identityDomain.ErrRecoveryConcurrentTransition)
	}
	return identityRecoveryFromRow(row)
}

func (repository *identityRecoveryRepository) GetActiveForUserForUpdate(
	ctx context.Context,
	userID uuid.UUID,
) (identityDomain.RecoveryCredential, error) {
	if userID == uuid.Nil {
		return identityDomain.RecoveryCredential{}, identityDomain.ErrInvalidRecoveryCredential
	}
	row, err := repository.queries.GetActiveUserRecoveryCredentialForUpdate(
		ctx, sqlcgen.GetActiveUserRecoveryCredentialForUpdateParams{UserID: uuidToPG(userID)},
	)
	if err != nil {
		return identityDomain.RecoveryCredential{}, mapIdentityQueryError(ctx, err, identityDomain.ErrRecoveryInvalid)
	}
	return identityRecoveryFromRow(row)
}

func (repository *identityRecoveryRepository) ConsumeCAS(
	ctx context.Context,
	current, next identityDomain.RecoveryCredential,
) (identityDomain.RecoveryCredential, error) {
	before, after := current.Snapshot(), next.Snapshot()
	expected, err := current.Consume(after.ConsumedAt)
	if err != nil || expected.Snapshot() != after {
		return identityDomain.RecoveryCredential{}, identityDomain.ErrInvalidRecoveryCredential
	}
	row, err := repository.queries.ConsumeUserRecoveryCredentialCAS(ctx, sqlcgen.ConsumeUserRecoveryCredentialCASParams{
		ConsumedAt: timeToPG(after.ConsumedAt), RecoveryCredentialID: uuidToPG(before.ID),
		UserID: uuidToPG(before.UserID), ExpectedVersion: int64(before.Version),
	})
	if err != nil {
		return identityDomain.RecoveryCredential{}, mapIdentityQueryError(ctx, err, identityDomain.ErrRecoveryConcurrentTransition)
	}
	return identityRecoveryFromRow(row)
}

func (repository *identityRecoveryRepository) RevokeCAS(
	ctx context.Context,
	current, next identityDomain.RecoveryCredential,
) (identityDomain.RecoveryCredential, error) {
	before, after := current.Snapshot(), next.Snapshot()
	expected, err := current.Revoke(after.RevokeReason, after.RevokedAt)
	if err != nil || expected.Snapshot() != after {
		return identityDomain.RecoveryCredential{}, identityDomain.ErrInvalidRecoveryCredential
	}
	row, err := repository.queries.RevokeUserRecoveryCredentialCAS(ctx, sqlcgen.RevokeUserRecoveryCredentialCASParams{
		RevokedAt: timeToPG(after.RevokedAt), RevokeReason: textToPG(after.RevokeReason),
		RecoveryCredentialID: uuidToPG(before.ID), UserID: uuidToPG(before.UserID),
		ExpectedVersion: int64(before.Version),
	})
	if err != nil {
		return identityDomain.RecoveryCredential{}, mapIdentityQueryError(ctx, err, identityDomain.ErrRecoveryConcurrentTransition)
	}
	return identityRecoveryFromRow(row)
}

type identityRecoveryRepository struct{ queries identityRecoveryQueries }

func (repository *identityRecoveryRepository) Insert(
	ctx context.Context,
	credential identityDomain.RecoveryCredential,
) (identityDomain.RecoveryCredential, error) {
	snapshot := credential.Snapshot()
	if snapshot.Status != identityDomain.RecoveryCredentialActive || snapshot.Version == 0 || snapshot.Version > math.MaxInt64 {
		return identityDomain.RecoveryCredential{}, identityDomain.ErrInvalidRecoveryCredential
	}
	row, err := repository.queries.CreateUserRecoveryCredential(ctx, sqlcgen.CreateUserRecoveryCredentialParams{
		RecoveryCredentialID: uuidToPG(snapshot.ID), UserID: uuidToPG(snapshot.UserID),
		Selector: snapshot.Selector.Value(), SecretHash: snapshot.SecretHash,
		Version: int64(snapshot.Version), CreatedAt: timeToPG(snapshot.CreatedAt),
	})
	if err != nil {
		return identityDomain.RecoveryCredential{}, mapIdentityQueryError(ctx, err, identityDomain.ErrIdentityConcurrentTransition)
	}
	return identityRecoveryFromRow(row)
}

var _ identityDomain.RecoveryCredentialRepository = (*identityRecoveryRepository)(nil)
