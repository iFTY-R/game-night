package postgres

import (
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/idempotency"
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

func identityRecoveryAttemptFromRow(row sqlcgen.UserRecoveryAttempt) (identityDomain.RecoveryAttempt, error) {
	return restoreIdentityRecoveryAttempt(
		row.RecoveryAttemptID, row.GrantSelector, row.GrantSecretHash, row.GrantKeyVersion,
		row.UserID, row.RecoveryCredentialID, row.RecoveryCredentialVersion, row.AssistedGrantID,
		row.ChallengeID, row.OriginHash, row.Purpose, row.RequestDigest, row.AttemptCount,
		row.MaxAttempts, row.Status, row.CreatedAt, row.ExpiresAt, row.ConsumedAt, row.RevokedAt, row.ResultID,
	)
}

func identityRecoveryAttemptFromLockedRow(
	row sqlcgen.GetUserRecoveryAttemptForUpdateRow,
) (identityDomain.RecoveryAttempt, error) {
	return restoreIdentityRecoveryAttempt(
		row.RecoveryAttemptID, row.GrantSelector, row.GrantSecretHash, row.GrantKeyVersion,
		row.UserID, row.RecoveryCredentialID, row.RecoveryCredentialVersion, row.AssistedGrantID,
		row.ChallengeID, row.OriginHash, row.Purpose, row.RequestDigest, row.AttemptCount,
		row.MaxAttempts, row.Status, row.CreatedAt, row.ExpiresAt, row.ConsumedAt, row.RevokedAt, row.ResultID,
	)
}

func restoreIdentityRecoveryAttempt(
	id pgtype.UUID,
	selectorValue string,
	grantHash []byte,
	grantKeyVersion int32,
	userID, recoveryCredentialID pgtype.UUID,
	recoveryCredentialVersion pgtype.Int8,
	assistedGrantID, challengeID pgtype.UUID,
	originHash []byte,
	purpose string,
	requestDigest []byte,
	attemptCount, maxAttempts int32,
	status string,
	createdAt, expiresAt, consumedAt, revokedAt pgtype.Timestamptz,
	resultID pgtype.UUID,
) (identityDomain.RecoveryAttempt, error) {
	selector, err := identifier.ParseSelector(selectorValue)
	if err != nil || !id.Valid || !userID.Valid || !challengeID.Valid || !createdAt.Valid || !expiresAt.Valid ||
		grantKeyVersion <= 0 || len(grantHash) != 32 || len(originHash) != len(challenge.OriginDigest{}) ||
		(len(requestDigest) != 0 && len(requestDigest) != len(idempotency.Digest{})) || purpose != "identity.recovery" ||
		recoveryCredentialID.Valid != recoveryCredentialVersion.Valid || recoveryCredentialID.Valid == assistedGrantID.Valid {
		return identityDomain.RecoveryAttempt{}, identityDomain.ErrIdentityIntegrity
	}
	var origin challenge.OriginDigest
	copy(origin[:], originHash)
	var digest idempotency.Digest
	copy(digest[:], requestDigest)
	requestDigestSet := len(requestDigest) == len(idempotency.Digest{})
	if requestDigestSet != (status == string(identityDomain.RecoveryAttemptConsumed)) {
		return identityDomain.RecoveryAttempt{}, identityDomain.ErrIdentityIntegrity
	}
	binding := identityDomain.RecoveryAttemptBinding{
		UserID: uuid.UUID(userID.Bytes), ChallengeID: uuid.UUID(challengeID.Bytes),
		Origin: origin, RequestDigestSet: requestDigestSet, RequestDigest: digest,
	}
	if recoveryCredentialID.Valid {
		binding.RecoveryCredentialID = uuid.UUID(recoveryCredentialID.Bytes)
		binding.RecoveryCredentialVersion = uint64(recoveryCredentialVersion.Int64)
	} else {
		binding.AssistedGrantID = uuid.UUID(assistedGrantID.Bytes)
	}
	snapshot := identityDomain.RecoveryAttemptSnapshot{
		ID: uuid.UUID(id.Bytes), Selector: selector,
		GrantMAC: security.MAC[security.UserChallengeKeyPurpose]{
			KeyVersion: uint32(grantKeyVersion), Value: grantHash,
		},
		Binding: binding, AttemptCount: uint32(attemptCount), MaxAttempts: uint32(maxAttempts),
		Status: identityDomain.RecoveryAttemptStatus(status), CreatedAt: createdAt.Time, ExpiresAt: expiresAt.Time,
	}
	if consumedAt.Valid {
		snapshot.ConsumedAt = consumedAt.Time
	}
	if revokedAt.Valid {
		snapshot.RevokedAt = revokedAt.Time
	}
	if resultID.Valid {
		snapshot.ResultID = uuid.UUID(resultID.Bytes)
	}
	attempt, err := identityDomain.RestoreRecoveryAttempt(snapshot)
	if err != nil {
		return identityDomain.RecoveryAttempt{}, identityDomain.ErrIdentityIntegrity
	}
	return attempt, nil
}

func identityAssistedRecoveryGrantFromRow(
	row sqlcgen.AdminAssistedRecoveryGrant,
) (identityDomain.AssistedRecoveryGrant, error) {
	selector, err := identifier.ParseSelector(row.Selector)
	if err != nil || !row.AssistedGrantID.Valid || !row.UserID.Valid || !row.CreatedByAdminID.Valid ||
		!row.CreatedAt.Valid || !row.ExpiresAt.Valid {
		return identityDomain.AssistedRecoveryGrant{}, identityDomain.ErrIdentityIntegrity
	}
	snapshot := identityDomain.AssistedRecoveryGrantSnapshot{
		ID: uuid.UUID(row.AssistedGrantID.Bytes), UserID: uuid.UUID(row.UserID.Bytes), Selector: selector,
		SecretHash: row.SecretHash, Purpose: row.Purpose,
		Status:       identityDomain.AssistedRecoveryGrantStatus(row.Status),
		AttemptCount: uint32(row.AttemptCount), MaxAttempts: uint32(row.MaxAttempts),
		CreatedByAdminID: uuid.UUID(row.CreatedByAdminID.Bytes), CreatedAt: row.CreatedAt.Time, ExpiresAt: row.ExpiresAt.Time,
	}
	if row.ConsumedAt.Valid {
		snapshot.ConsumedAt = row.ConsumedAt.Time
	}
	if row.RevokedAt.Valid {
		snapshot.RevokedAt = row.RevokedAt.Time
	}
	if row.ResultID.Valid {
		snapshot.ResultID = uuid.UUID(row.ResultID.Bytes)
	}
	grant, err := identityDomain.RestoreAssistedRecoveryGrant(snapshot)
	if err != nil {
		return identityDomain.AssistedRecoveryGrant{}, identityDomain.ErrIdentityIntegrity
	}
	return grant, nil
}

func restoreIdentityDeviceSummary(
	credentialID, userID pgtype.UUID,
	label string,
	createdAt, lastSeenAt, idleExpiresAt, absoluteExpiresAt, revokedAt pgtype.Timestamptz,
	listedAt time.Time,
) (identityDomain.DeviceSummary, error) {
	if !credentialID.Valid || !userID.Valid || !createdAt.Valid || !lastSeenAt.Valid ||
		!idleExpiresAt.Valid || !absoluteExpiresAt.Valid {
		return identityDomain.DeviceSummary{}, identityDomain.ErrIdentityIntegrity
	}
	snapshot := identityDomain.DeviceSummarySnapshot{
		CredentialID: uuid.UUID(credentialID.Bytes), UserID: uuid.UUID(userID.Bytes), Label: label,
		CreatedAt: createdAt.Time, LastSeenAt: lastSeenAt.Time,
		IdleExpiresAt: idleExpiresAt.Time, AbsoluteExpiresAt: absoluteExpiresAt.Time,
	}
	if revokedAt.Valid {
		snapshot.RevokedAt = revokedAt.Time
	}
	summary, err := identityDomain.RestoreDeviceSummary(snapshot, listedAt)
	if err != nil {
		return identityDomain.DeviceSummary{}, identityDomain.ErrIdentityIntegrity
	}
	return summary, nil
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
