package postgres

import (
	"bytes"
	"context"
	"math"

	"github.com/google/uuid"
	identityDomain "github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
)

type identityDeviceQueries interface {
	CreateDeviceCredential(context.Context, sqlcgen.CreateDeviceCredentialParams) (sqlcgen.DeviceCredential, error)
	GetDeviceIdentityForUpdate(context.Context, sqlcgen.GetDeviceIdentityForUpdateParams) (sqlcgen.GetDeviceIdentityForUpdateRow, error)
	TouchDeviceCredentialCAS(context.Context, sqlcgen.TouchDeviceCredentialCASParams) (sqlcgen.TouchDeviceCredentialCASRow, error)
	RotateDeviceCredentialCAS(context.Context, sqlcgen.RotateDeviceCredentialCASParams) (sqlcgen.DeviceCredential, error)
}

type identityDeviceRepository struct{ queries identityDeviceQueries }

func (repository *identityDeviceRepository) Insert(
	ctx context.Context,
	device identityDomain.DeviceCredential,
) (identityDomain.DeviceCredential, error) {
	snapshot := device.Snapshot()
	if !validIdentityPersistenceWidths(snapshot) || snapshot.Generation != 1 || snapshot.PreviousSecretMAC != nil ||
		!snapshot.RevokedAt.IsZero() {
		return identityDomain.DeviceCredential{}, identityDomain.ErrInvalidDeviceInput
	}
	row, err := repository.queries.CreateDeviceCredential(ctx, sqlcgen.CreateDeviceCredentialParams{
		CredentialID: uuidToPG(snapshot.CredentialID), UserID: uuidToPG(snapshot.UserID),
		SecretHash: snapshot.SecretMAC.Value, SecretKeyVersion: int32(snapshot.SecretMAC.KeyVersion),
		CsrfHash: snapshot.CSRFMAC, Label: snapshot.Label, CreatedAt: timeToPG(snapshot.CreatedAt),
		IdleExpiresAt: timeToPG(snapshot.IdleExpiresAt), AbsoluteExpiresAt: timeToPG(snapshot.AbsoluteExpiresAt),
	})
	if err != nil {
		return identityDomain.DeviceCredential{}, mapIdentityQueryError(ctx, err, identityDomain.ErrIdentityConcurrentTransition)
	}
	return identityDeviceFromRow(row)
}

func (repository *identityDeviceRepository) GetIdentityForUpdate(
	ctx context.Context,
	credentialID uuid.UUID,
) (identityDomain.User, identityDomain.DeviceCredential, error) {
	if credentialID == uuid.Nil {
		return identityDomain.User{}, identityDomain.DeviceCredential{}, identityDomain.ErrInvalidDeviceInput
	}
	row, err := repository.queries.GetDeviceIdentityForUpdate(ctx, sqlcgen.GetDeviceIdentityForUpdateParams{
		TargetCredentialID: uuidToPG(credentialID),
	})
	if err != nil {
		return identityDomain.User{}, identityDomain.DeviceCredential{},
			mapIdentityQueryError(ctx, err, identityDomain.ErrDeviceAuthentication)
	}
	user, err := identityUserFromJoinedRow(row)
	if err != nil {
		return identityDomain.User{}, identityDomain.DeviceCredential{}, err
	}
	device, err := identityDeviceFromJoinedRow(row)
	if err != nil || device.Snapshot().UserID != user.Snapshot().ID {
		return identityDomain.User{}, identityDomain.DeviceCredential{}, identityDomain.ErrIdentityIntegrity
	}
	return user, device, nil
}

func (repository *identityDeviceRepository) TouchCAS(
	ctx context.Context,
	current, next identityDomain.DeviceCredential,
) (identityDomain.DeviceCredential, error) {
	before, after := current.Snapshot(), next.Snapshot()
	if before.CredentialID != after.CredentialID || before.Generation != after.Generation ||
		before.UserID != after.UserID || !validIdentityPersistenceWidths(before) || !validIdentityPersistenceWidths(after) {
		return identityDomain.DeviceCredential{}, identityDomain.ErrInvalidDeviceInput
	}
	previousHash, previousVersion, previousUntil := optionalDeviceMACValues(before)
	row, err := repository.queries.TouchDeviceCredentialCAS(ctx, sqlcgen.TouchDeviceCredentialCASParams{
		SeenAt: timeToPG(after.LastSeenAt), IdleExpiresAt: timeToPG(after.IdleExpiresAt),
		CredentialID: uuidToPG(before.CredentialID), ExpectedGeneration: int64(before.Generation),
		ExpectedSecretHash: before.SecretMAC.Value, ExpectedSecretKeyVersion: int32(before.SecretMAC.KeyVersion),
		ExpectedPreviousSecretHash: previousHash, ExpectedPreviousSecretKeyVersion: previousVersion,
		ExpectedPreviousValidUntil: previousUntil, ExpectedCsrfHash: before.CSRFMAC,
		ExpectedLastSeenAt: timeToPG(before.LastSeenAt), ExpectedRotatedAt: timeToPG(before.RotatedAt),
		ExpectedIdleExpiresAt: timeToPG(before.IdleExpiresAt), ExpectedAbsoluteExpiresAt: timeToPG(before.AbsoluteExpiresAt),
	})
	if err != nil {
		return identityDomain.DeviceCredential{}, mapIdentityQueryError(ctx, err, identityDomain.ErrIdentityConcurrentTransition)
	}
	if !row.CredentialID.Valid || uuid.UUID(row.CredentialID.Bytes) != after.CredentialID ||
		row.Generation != int64(after.Generation) || !row.LastSeenAt.Valid || !row.LastSeenAt.Time.Equal(after.LastSeenAt) ||
		!row.IdleExpiresAt.Valid || !row.IdleExpiresAt.Time.Equal(after.IdleExpiresAt) ||
		!row.AbsoluteExpiresAt.Valid || !row.AbsoluteExpiresAt.Time.Equal(after.AbsoluteExpiresAt) {
		return identityDomain.DeviceCredential{}, identityDomain.ErrIdentityIntegrity
	}
	return next, nil
}

func (repository *identityDeviceRepository) RotateCAS(
	ctx context.Context,
	current, next identityDomain.DeviceCredential,
) (identityDomain.DeviceCredential, error) {
	before, after := current.Snapshot(), next.Snapshot()
	if before.CredentialID != after.CredentialID || before.UserID != after.UserID ||
		before.Generation == math.MaxInt64 || after.Generation != before.Generation+1 ||
		!validIdentityPersistenceWidths(before) || !validIdentityPersistenceWidths(after) || after.PreviousSecretMAC == nil ||
		after.PreviousSecretMAC.KeyVersion != before.SecretMAC.KeyVersion ||
		!bytes.Equal(after.PreviousSecretMAC.Value, before.SecretMAC.Value) {
		return identityDomain.DeviceCredential{}, identityDomain.ErrInvalidDeviceInput
	}
	previousHash, previousVersion, previousUntil := optionalDeviceMACValues(before)
	row, err := repository.queries.RotateDeviceCredentialCAS(ctx, sqlcgen.RotateDeviceCredentialCASParams{
		PreviousValidUntil: timeToPG(after.PreviousSecretValidUntil), SecretHash: after.SecretMAC.Value,
		SecretKeyVersion: int32(after.SecretMAC.KeyVersion), CsrfHash: after.CSRFMAC,
		RotatedAt: timeToPG(after.RotatedAt), IdleExpiresAt: timeToPG(after.IdleExpiresAt),
		CredentialID: uuidToPG(before.CredentialID), UserID: uuidToPG(before.UserID),
		ExpectedGeneration: int64(before.Generation), ExpectedSecretHash: before.SecretMAC.Value,
		ExpectedSecretKeyVersion: int32(before.SecretMAC.KeyVersion), ExpectedPreviousSecretHash: previousHash,
		ExpectedPreviousSecretKeyVersion: previousVersion, ExpectedPreviousValidUntil: previousUntil,
		ExpectedCsrfHash: before.CSRFMAC, ExpectedLastSeenAt: timeToPG(before.LastSeenAt),
		ExpectedRotatedAt: timeToPG(before.RotatedAt), ExpectedIdleExpiresAt: timeToPG(before.IdleExpiresAt),
		ExpectedAbsoluteExpiresAt: timeToPG(before.AbsoluteExpiresAt),
	})
	if err != nil {
		return identityDomain.DeviceCredential{}, mapIdentityQueryError(ctx, err, identityDomain.ErrIdentityConcurrentTransition)
	}
	stored, err := identityDeviceFromRow(row)
	if err != nil {
		return identityDomain.DeviceCredential{}, err
	}
	if stored.Snapshot().Generation != after.Generation || stored.Snapshot().CredentialID != after.CredentialID ||
		!bytes.Equal(stored.Snapshot().SecretMAC.Value, after.SecretMAC.Value) ||
		!bytes.Equal(stored.Snapshot().CSRFMAC, after.CSRFMAC) {
		return identityDomain.DeviceCredential{}, identityDomain.ErrIdentityIntegrity
	}
	return stored, nil
}

var _ identityDomain.DeviceRepository = (*identityDeviceRepository)(nil)
