package postgres

import (
	"math"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/identifier"
	identityDomain "github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/iFTY-R/game-night/platform/security"
	"github.com/jackc/pgx/v5/pgtype"
)

func identityUserFromRow(row sqlcgen.User) (identityDomain.User, error) {
	return restoreIdentityUser(
		row.UserID, row.Status, row.Username, row.CurrentUsernameKey, row.UsernameChangedAt, row.CreatedAt, row.UpdatedAt,
	)
}

func identityUserFromJoinedRow(row sqlcgen.GetDeviceIdentityForUpdateRow) (identityDomain.User, error) {
	return restoreIdentityUser(
		row.UserID, row.UserStatus, row.UserUsername, row.UserCurrentUsernameKey,
		row.UserUsernameChangedAt, row.UserCreatedAt, row.UserUpdatedAt,
	)
}

func restoreIdentityUser(
	id pgtype.UUID,
	status string,
	username pgtype.Text,
	usernameKey pgtype.Text,
	usernameChangedAt, createdAt, updatedAt pgtype.Timestamptz,
) (identityDomain.User, error) {
	if !id.Valid || !createdAt.Valid || !updatedAt.Valid || username.Valid != usernameKey.Valid {
		return identityDomain.User{}, identityDomain.ErrIdentityIntegrity
	}
	snapshot := identityDomain.UserSnapshot{
		ID: uuid.UUID(id.Bytes), Status: identityDomain.UserStatus(status),
		CreatedAt: createdAt.Time, UpdatedAt: updatedAt.Time,
	}
	if username.Valid {
		snapshot.Username = username.String
		snapshot.CurrentUsernameKey = usernameKey.String
	}
	if usernameChangedAt.Valid {
		snapshot.UsernameChangedAt = usernameChangedAt.Time
	}
	user, err := identityDomain.RestoreUser(snapshot)
	if err != nil {
		return identityDomain.User{}, identityDomain.ErrIdentityIntegrity
	}
	return user, nil
}

func identityClaimFromRow(row sqlcgen.UsernameClaim) (identityDomain.UsernameClaim, error) {
	if !row.OwnerUserID.Valid || !row.CreatedAt.Valid || !row.UpdatedAt.Valid {
		return identityDomain.UsernameClaim{}, identityDomain.ErrIdentityIntegrity
	}
	snapshot := identityDomain.UsernameClaimSnapshot{
		UsernameKey: row.UsernameKey, DisplayUsername: row.DisplayUsername,
		Status: identityDomain.UsernameClaimStatus(row.Status), OwnerUserID: uuid.UUID(row.OwnerUserID.Bytes),
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
	if row.ReservedUntil.Valid {
		snapshot.ReservedUntil = row.ReservedUntil.Time
	}
	claim, err := identityDomain.RestoreUsernameClaim(snapshot)
	if err != nil {
		return identityDomain.UsernameClaim{}, identityDomain.ErrIdentityIntegrity
	}
	return claim, nil
}

func identityDeviceFromRow(row sqlcgen.DeviceCredential) (identityDomain.DeviceCredential, error) {
	return restoreIdentityDevice(
		row.CredentialID, row.UserID, row.SecretHash, row.SecretKeyVersion,
		row.PreviousSecretHash, row.PreviousSecretKeyVersion, row.PreviousValidUntil,
		row.CsrfHash, row.Generation, row.Label, row.CreatedAt, row.LastSeenAt, row.RotatedAt,
		row.IdleExpiresAt, row.AbsoluteExpiresAt, row.RevokedAt, row.RevokeReason,
	)
}

func identityDeviceFromJoinedRow(row sqlcgen.GetDeviceIdentityForUpdateRow) (identityDomain.DeviceCredential, error) {
	return restoreIdentityDevice(
		row.CredentialID, row.UserID, row.SecretHash, row.SecretKeyVersion,
		row.PreviousSecretHash, row.PreviousSecretKeyVersion, row.PreviousValidUntil,
		row.CsrfHash, row.Generation, row.Label, row.DeviceCreatedAt, row.LastSeenAt, row.RotatedAt,
		row.IdleExpiresAt, row.AbsoluteExpiresAt, row.RevokedAt, row.RevokeReason,
	)
}

func restoreIdentityDevice(
	credentialID, userID pgtype.UUID,
	secretHash []byte,
	secretKeyVersion int32,
	previousSecretHash []byte,
	previousSecretKeyVersion pgtype.Int4,
	previousValidUntil pgtype.Timestamptz,
	csrfHash []byte,
	generation int64,
	label string,
	createdAt, lastSeenAt, rotatedAt, idleExpiresAt, absoluteExpiresAt, revokedAt pgtype.Timestamptz,
	revokeReason pgtype.Text,
) (identityDomain.DeviceCredential, error) {
	if !credentialID.Valid || !userID.Valid || secretKeyVersion <= 0 || generation <= 0 ||
		!createdAt.Valid || !lastSeenAt.Valid || !rotatedAt.Valid || !idleExpiresAt.Valid || !absoluteExpiresAt.Valid ||
		(len(previousSecretHash) > 0) != previousSecretKeyVersion.Valid ||
		previousSecretKeyVersion.Valid != previousValidUntil.Valid || revokedAt.Valid != revokeReason.Valid {
		return identityDomain.DeviceCredential{}, identityDomain.ErrIdentityIntegrity
	}
	snapshot := identityDomain.DeviceCredentialSnapshot{
		CredentialID: uuid.UUID(credentialID.Bytes), UserID: uuid.UUID(userID.Bytes),
		SecretMAC: security.MAC[security.DeviceHMACKeyPurpose]{
			KeyVersion: uint32(secretKeyVersion), Value: secretHash,
		},
		CSRFMAC: csrfHash, Generation: uint64(generation), Label: label,
		CreatedAt: createdAt.Time, LastSeenAt: lastSeenAt.Time, RotatedAt: rotatedAt.Time,
		IdleExpiresAt: idleExpiresAt.Time, AbsoluteExpiresAt: absoluteExpiresAt.Time,
	}
	if previousSecretKeyVersion.Valid {
		previous := security.MAC[security.DeviceHMACKeyPurpose]{
			KeyVersion: uint32(previousSecretKeyVersion.Int32), Value: previousSecretHash,
		}
		snapshot.PreviousSecretMAC = &previous
		snapshot.PreviousSecretValidUntil = previousValidUntil.Time
	}
	if revokedAt.Valid {
		snapshot.RevokedAt = revokedAt.Time
		snapshot.RevokeReason = identityDomain.DeviceRevokeReason(revokeReason.String)
	}
	device, err := identityDomain.RestoreDeviceCredential(snapshot)
	if err != nil {
		return identityDomain.DeviceCredential{}, identityDomain.ErrIdentityIntegrity
	}
	return device, nil
}

func identityRecoveryFromRow(row sqlcgen.UserRecoveryCredential) (identityDomain.RecoveryCredential, error) {
	if !row.RecoveryCredentialID.Valid || !row.UserID.Valid || row.Version <= 0 || !row.CreatedAt.Valid {
		return identityDomain.RecoveryCredential{}, identityDomain.ErrIdentityIntegrity
	}
	selector, err := identifier.ParseSelector(row.Selector)
	if err != nil || selector.ByteLength() != identityDomain.RecoverySelectorBytes {
		return identityDomain.RecoveryCredential{}, identityDomain.ErrIdentityIntegrity
	}
	snapshot := identityDomain.RecoveryCredentialSnapshot{
		ID: uuid.UUID(row.RecoveryCredentialID.Bytes), UserID: uuid.UUID(row.UserID.Bytes),
		Selector: selector, SecretHash: row.SecretHash, Version: uint64(row.Version),
		Status: identityDomain.RecoveryCredentialStatus(row.Status), CreatedAt: row.CreatedAt.Time,
	}
	if row.ConsumedAt.Valid {
		snapshot.ConsumedAt = row.ConsumedAt.Time
	}
	if row.RevokedAt.Valid {
		snapshot.RevokedAt = row.RevokedAt.Time
	}
	if row.RevokeReason.Valid {
		snapshot.RevokeReason = row.RevokeReason.String
	}
	record, err := identityDomain.RestoreRecoveryCredential(snapshot)
	if err != nil {
		return identityDomain.RecoveryCredential{}, identityDomain.ErrIdentityIntegrity
	}
	return record, nil
}

func optionalDeviceMACValues(snapshot identityDomain.DeviceCredentialSnapshot) ([]byte, pgtype.Int4, pgtype.Timestamptz) {
	if snapshot.PreviousSecretMAC == nil {
		return nil, pgtype.Int4{}, pgtype.Timestamptz{}
	}
	return snapshot.PreviousSecretMAC.Value,
		pgtype.Int4{Int32: int32(snapshot.PreviousSecretMAC.KeyVersion), Valid: true},
		timeToPG(snapshot.PreviousSecretValidUntil)
}

func validIdentityPersistenceWidths(device identityDomain.DeviceCredentialSnapshot) bool {
	return device.SecretMAC.KeyVersion > 0 && device.SecretMAC.KeyVersion <= math.MaxInt32 &&
		device.Generation > 0 && device.Generation <= math.MaxInt64 &&
		(device.PreviousSecretMAC == nil ||
			(device.PreviousSecretMAC.KeyVersion > 0 && device.PreviousSecretMAC.KeyVersion <= math.MaxInt32))
}
