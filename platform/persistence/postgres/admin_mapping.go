package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	adminDomain "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/iFTY-R/game-night/platform/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func adminAccountFromRow(row sqlcgen.AdminAccount) (adminDomain.Account, error) {
	if !row.AdminID.Valid || !row.CreatedAt.Valid || !row.UpdatedAt.Valid || row.SingletonID != 1 {
		return adminDomain.Account{}, adminDomain.ErrIntegrity
	}
	snapshot := adminDomain.AccountSnapshot{
		ID: uuid.UUID(row.AdminID.Bytes), Username: row.Username, Status: adminDomain.AccountStatus(row.Status),
		PasswordVersion: row.PasswordVersion, AdminVersion: row.AdminVersion, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
	if row.PasswordHash.Valid {
		snapshot.PasswordHash = row.PasswordHash.String
	}
	if row.PasswordAlgorithm.Valid {
		snapshot.PasswordAlgorithm = row.PasswordAlgorithm.String
	}
	if row.PasswordParameters.Valid {
		snapshot.PasswordParameters = row.PasswordParameters.String
	}
	account, err := adminDomain.RestoreAccount(snapshot)
	if err != nil {
		return adminDomain.Account{}, adminDomain.ErrIntegrity
	}
	return account, nil
}

func accountAfterCAS(previous adminDomain.Account, status string, passwordVersion, adminVersion int64, updatedAt time.Time, password *adminDomain.PasswordRecord) (adminDomain.Account, error) {
	snapshot := previous.Snapshot()
	snapshot.Status = adminDomain.AccountStatus(status)
	snapshot.PasswordVersion, snapshot.AdminVersion, snapshot.UpdatedAt = passwordVersion, adminVersion, updatedAt
	if password != nil {
		snapshot.PasswordHash, snapshot.PasswordAlgorithm, snapshot.PasswordParameters = password.Hash, password.Algorithm, password.Parameters
	}
	return adminDomain.RestoreAccount(snapshot)
}

func adminEnrollmentFromRow(row sqlcgen.AdminTotpEnrollment) (adminDomain.Enrollment, error) {
	if !row.EnrollmentID.Valid || !row.AdminID.Valid || row.KeyVersion <= 0 || !row.CreatedAt.Valid {
		return adminDomain.Enrollment{}, adminDomain.ErrIntegrity
	}
	snapshot := adminDomain.EnrollmentSnapshot{
		ID: uuid.UUID(row.EnrollmentID.Bytes), AdminID: uuid.UUID(row.AdminID.Bytes), Ciphertext: append([]byte(nil), row.Ciphertext...), Nonce: append([]byte(nil), row.Nonce...),
		KeyVersion: uint32(row.KeyVersion), Status: adminDomain.EnrollmentStatus(row.Status), AdminVersion: row.AdminVersion,
		EnrollmentVersion: row.EnrollmentVersion, OperationID: row.OperationID, CreatedAt: row.CreatedAt.Time,
	}
	if row.ReplayFloor.Valid {
		replayFloor := row.ReplayFloor.Int64
		snapshot.ReplayFloor = &replayFloor
	}
	if row.ExpiresAt.Valid {
		snapshot.ExpiresAt = row.ExpiresAt.Time
	}
	if row.ActivatedAt.Valid {
		snapshot.ActivatedAt = row.ActivatedAt.Time
	}
	if row.DisabledAt.Valid {
		snapshot.DisabledAt = row.DisabledAt.Time
	}
	enrollment, err := adminDomain.RestoreEnrollment(snapshot)
	if err != nil {
		return adminDomain.Enrollment{}, adminDomain.ErrIntegrity
	}
	return enrollment, nil
}

func adminSessionFromRow(row sqlcgen.AdminSession) (adminDomain.Session, error) {
	if !row.SessionID.Valid || !row.AdminID.Valid || row.SecretKeyVersion <= 0 || row.MaxAttempts <= 0 || row.AttemptCount < 0 ||
		!row.CreatedAt.Valid || !row.LastSeenAt.Valid || !row.IdleExpiresAt.Valid || !row.AbsoluteExpiresAt.Valid {
		return adminDomain.Session{}, adminDomain.ErrIntegrity
	}
	snapshot := adminDomain.SessionSnapshot{
		ID: uuid.UUID(row.SessionID.Bytes), AdminID: uuid.UUID(row.AdminID.Bytes), Selector: row.Selector,
		SecretMAC: security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: uint32(row.SecretKeyVersion), Value: append([]byte(nil), row.SecretHash...)},
		CSRFHash:  security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: uint32(row.SecretKeyVersion), Value: append([]byte(nil), row.CsrfHash...)},
		Kind:      adminDomain.SessionKind(row.Kind), AdminVersion: row.AdminVersion, PasswordVersion: row.PasswordVersion, SessionVersion: row.SessionVersion,
		ClientIP: row.ClientIp, UserAgent: row.UserAgent,
		AttemptCount: uint32(row.AttemptCount), MaxAttempts: uint32(row.MaxAttempts), CreatedAt: row.CreatedAt.Time,
		LastSeenAt: row.LastSeenAt.Time, IdleExpiresAt: row.IdleExpiresAt.Time, AbsoluteExpiresAt: row.AbsoluteExpiresAt.Time,
	}
	if row.RevokedAt.Valid {
		snapshot.RevokedAt = row.RevokedAt.Time
	}
	if row.RevokeReason.Valid {
		snapshot.RevokeReason = row.RevokeReason.String
	}
	session, err := adminDomain.RestoreSession(snapshot)
	if err != nil {
		return adminDomain.Session{}, adminDomain.ErrIntegrity
	}
	return session, nil
}

func adminRecoveryCodeFromRow(row sqlcgen.AdminRecoveryCode) (adminDomain.RecoveryCode, error) {
	if !row.RecoveryCodeID.Valid || !row.AdminID.Valid || !row.CreatedAt.Valid {
		return adminDomain.RecoveryCode{}, adminDomain.ErrIntegrity
	}
	snapshot := adminDomain.RecoveryCodeSnapshot{
		ID: uuid.UUID(row.RecoveryCodeID.Bytes), AdminID: uuid.UUID(row.AdminID.Bytes), Selector: row.Selector,
		SecretHash: row.SecretHash, SetVersion: row.SetVersion, Status: adminDomain.RecoveryCodeStatus(row.Status), CreatedAt: row.CreatedAt.Time,
	}
	if row.ConsumedAt.Valid {
		snapshot.ConsumedAt = row.ConsumedAt.Time
	}
	if row.RevokedAt.Valid {
		snapshot.RevokedAt = row.RevokedAt.Time
	}
	code, err := adminDomain.RestoreRecoveryCode(snapshot)
	if err != nil {
		return adminDomain.RecoveryCode{}, adminDomain.ErrIntegrity
	}
	return code, nil
}

func adminElevationFromRow(row sqlcgen.AdminElevationGrant) (adminDomain.Elevation, error) {
	if !row.AdminID.Valid || !row.SessionID.Valid || row.AdminVersion <= 0 || row.PasswordVersion < 0 ||
		row.SessionVersion <= 0 || row.EnrollmentVersion < 0 || !row.GrantedAt.Valid || !row.ExpiresAt.Valid {
		return adminDomain.Elevation{}, adminDomain.ErrIntegrity
	}
	elevation, err := adminDomain.RestoreElevation(adminDomain.ElevationSnapshot{
		AdminID:           uuid.UUID(row.AdminID.Bytes),
		SessionID:         uuid.UUID(row.SessionID.Bytes),
		Scope:             adminDomain.ElevationScope(row.Scope),
		AdminVersion:      row.AdminVersion,
		PasswordVersion:   row.PasswordVersion,
		SessionVersion:    row.SessionVersion,
		EnrollmentVersion: row.EnrollmentVersion,
		GrantedAt:         row.GrantedAt.Time,
		ExpiresAt:         row.ExpiresAt.Time,
		RevokedAt:         row.RevokedAt.Time,
	})
	if err != nil {
		return adminDomain.Elevation{}, adminDomain.ErrIntegrity
	}
	return elevation, nil
}

func adminCommandReceiptFromRow(row sqlcgen.AdminCommandReceipt) (adminDomain.CommandReceipt, error) {
	if !row.AdminID.Valid || row.Command == "" || row.TargetType == "" || row.TargetID == "" ||
		row.ResultAdminVersion <= 0 || row.ResultPasswordVersion < 0 || row.ResultSessionVersion < 0 ||
		row.ResultEnrollmentVersion < 0 || !row.AuditEventID.Valid || !row.CreatedAt.Valid {
		return adminDomain.CommandReceipt{}, adminDomain.ErrIntegrity
	}
	operationID, err := idempotency.ParseOperationID(row.OperationID)
	if err != nil {
		return adminDomain.CommandReceipt{}, adminDomain.ErrIntegrity
	}
	requestDigest, err := idempotency.NewDigest(row.RequestDigest)
	if err != nil {
		return adminDomain.CommandReceipt{}, adminDomain.ErrIntegrity
	}
	return adminDomain.CommandReceipt{
		AdminID:                 uuid.UUID(row.AdminID.Bytes),
		OperationID:             operationID,
		RequestDigest:           requestDigest,
		Command:                 row.Command,
		TargetType:              row.TargetType,
		TargetID:                row.TargetID,
		ResultAdminVersion:      row.ResultAdminVersion,
		ResultPasswordVersion:   row.ResultPasswordVersion,
		ResultSessionVersion:    row.ResultSessionVersion,
		ResultEnrollmentVersion: row.ResultEnrollmentVersion,
		AuditEventID:            uuid.UUID(row.AuditEventID.Bytes),
		CreatedAt:               row.CreatedAt.Time,
	}, nil
}

func mapAdminQueryError(err, notFound error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return adminDomain.ErrRepositoryUnavailable
}

func pgAdminUUID(value uuid.UUID) pgtype.UUID { return uuidToPG(value) }
