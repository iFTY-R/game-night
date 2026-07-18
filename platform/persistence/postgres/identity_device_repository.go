package postgres

import (
	"bytes"
	"context"
	"math"
	"reflect"
	"time"

	"github.com/google/uuid"
	identityDomain "github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
)

type identityDeviceQueries interface {
	CreateDeviceCredential(context.Context, sqlcgen.CreateDeviceCredentialParams) (sqlcgen.DeviceCredential, error)
	GetDeviceIdentityForUpdate(context.Context, sqlcgen.GetDeviceIdentityForUpdateParams) (sqlcgen.GetDeviceIdentityForUpdateRow, error)
	GetDeviceCredentialForUpdate(context.Context, sqlcgen.GetDeviceCredentialForUpdateParams) (sqlcgen.DeviceCredential, error)
	ListUserDeviceCredentials(context.Context, sqlcgen.ListUserDeviceCredentialsParams) ([]sqlcgen.ListUserDeviceCredentialsRow, error)
	TouchDeviceCredentialCAS(context.Context, sqlcgen.TouchDeviceCredentialCASParams) (sqlcgen.TouchDeviceCredentialCASRow, error)
	RotateDeviceCredentialCAS(context.Context, sqlcgen.RotateDeviceCredentialCASParams) (sqlcgen.DeviceCredential, error)
	RevokeDeviceCredentialCAS(context.Context, sqlcgen.RevokeDeviceCredentialCASParams) (sqlcgen.DeviceCredential, error)
	RevokeOtherDeviceCredentialsForRecovery(context.Context, sqlcgen.RevokeOtherDeviceCredentialsForRecoveryParams) ([]sqlcgen.RevokeOtherDeviceCredentialsForRecoveryRow, error)
}

func (repository *identityDeviceRepository) GetForUpdate(
	ctx context.Context,
	credentialID uuid.UUID,
) (identityDomain.DeviceCredential, error) {
	if credentialID == uuid.Nil {
		return identityDomain.DeviceCredential{}, identityDomain.ErrInvalidDeviceInput
	}
	row, err := repository.queries.GetDeviceCredentialForUpdate(ctx, sqlcgen.GetDeviceCredentialForUpdateParams{
		CredentialID: uuidToPG(credentialID),
	})
	if err != nil {
		return identityDomain.DeviceCredential{}, mapIdentityQueryError(ctx, err, identityDomain.ErrDeviceAuthentication)
	}
	return identityDeviceFromRow(row)
}

func (repository *identityDeviceRepository) List(
	ctx context.Context,
	request identityDomain.DeviceListRequest,
) ([]identityDomain.DeviceSummary, error) {
	if request.UserID == uuid.Nil || request.PageSize == 0 || request.PageSize > identityDomain.MaximumDevicePageSize ||
		request.ListedAt.IsZero() {
		return nil, identityDomain.ErrInvalidDeviceInput
	}
	rows, err := repository.queries.ListUserDeviceCredentials(ctx, sqlcgen.ListUserDeviceCredentialsParams{
		UserID: uuidToPG(request.UserID), IncludeRevoked: request.IncludeRevoked,
		AfterCreatedAt: timeToPG(request.After.CreatedAt), AfterCredentialID: uuidToPG(request.After.CredentialID),
		PageSize: int32(request.PageSize),
	})
	if err != nil {
		return nil, mapIdentityQueryError(ctx, err, identityDomain.ErrIdentityRepositoryUnavailable)
	}
	devices := make([]identityDomain.DeviceSummary, 0, len(rows))
	for _, row := range rows {
		summary, mapErr := restoreIdentityDeviceSummary(
			row.CredentialID, row.UserID, row.Label, row.CreatedAt, row.LastSeenAt,
			row.IdleExpiresAt, row.AbsoluteExpiresAt, row.RevokedAt, request.ListedAt,
		)
		if mapErr != nil || summary.UserID() != request.UserID {
			return nil, identityDomain.ErrIdentityIntegrity
		}
		devices = append(devices, summary)
	}
	return devices, nil
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

func (repository *identityDeviceRepository) RevokeCAS(
	ctx context.Context,
	current, next identityDomain.DeviceCredential,
) (identityDomain.DeviceCredential, error) {
	before, after := current.Snapshot(), next.Snapshot()
	expected, err := current.Revoke(after.RevokeReason, after.RevokedAt)
	if err != nil || !deviceSnapshotsEqual(expected.Snapshot(), after) {
		return identityDomain.DeviceCredential{}, identityDomain.ErrInvalidDeviceInput
	}
	row, err := repository.queries.RevokeDeviceCredentialCAS(ctx, sqlcgen.RevokeDeviceCredentialCASParams{
		RevokedAt: timeToPG(after.RevokedAt), RevokeReason: textToPG(string(after.RevokeReason)),
		CredentialID: uuidToPG(before.CredentialID), UserID: uuidToPG(before.UserID),
		ExpectedGeneration: int64(before.Generation),
	})
	if err != nil {
		return identityDomain.DeviceCredential{}, mapIdentityQueryError(ctx, err, identityDomain.ErrDeviceConcurrentTransition)
	}
	return identityDeviceFromRow(row)
}

func (repository *identityDeviceRepository) RevokeOtherActiveForRecovery(
	ctx context.Context,
	userID, preservedCredentialID uuid.UUID,
	revokedAt time.Time,
) ([]identityDomain.DeviceSummary, error) {
	if userID == uuid.Nil || preservedCredentialID == uuid.Nil || revokedAt.IsZero() {
		return nil, identityDomain.ErrInvalidDeviceInput
	}
	rows, err := repository.queries.RevokeOtherDeviceCredentialsForRecovery(
		ctx, sqlcgen.RevokeOtherDeviceCredentialsForRecoveryParams{
			RevokedAt: timeToPG(revokedAt), UserID: uuidToPG(userID),
			PreservedCredentialID: uuidToPG(preservedCredentialID),
		},
	)
	if err != nil {
		return nil, mapIdentityQueryError(ctx, err, identityDomain.ErrRecoveryConcurrentTransition)
	}
	devices := make([]identityDomain.DeviceSummary, 0, len(rows))
	for _, row := range rows {
		summary, mapErr := restoreIdentityDeviceSummary(
			row.CredentialID, row.UserID, row.Label, row.CreatedAt, row.LastSeenAt,
			row.IdleExpiresAt, row.AbsoluteExpiresAt, row.RevokedAt, revokedAt,
		)
		if mapErr != nil || summary.UserID() != userID || summary.Status != identityDomain.DeviceStateRevoked {
			return nil, identityDomain.ErrIdentityIntegrity
		}
		devices = append(devices, summary)
	}
	return devices, nil
}

func deviceSnapshotsEqual(left, right identityDomain.DeviceCredentialSnapshot) bool {
	return reflect.DeepEqual(left, right)
}

var _ identityDomain.DeviceRepository = (*identityDeviceRepository)(nil)
