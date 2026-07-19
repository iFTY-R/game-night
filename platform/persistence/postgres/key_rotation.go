package postgres

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/keyrotation"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/iFTY-R/game-night/platform/profile"
	"github.com/iFTY-R/game-night/platform/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// KeyRotationUnitOfWork binds rotation queries and signed audit participants to one worker transaction.
type KeyRotationUnitOfWork struct {
	runner   *TransactionRunner
	verifier audit.IntegrityVerifier
}

// NewKeyRotationUnitOfWork creates the worker adapter without exposing pgx transaction handles to the domain.
func NewKeyRotationUnitOfWork(pool *pgxpool.Pool, verifier audit.IntegrityVerifier) *KeyRotationUnitOfWork {
	if verifier == nil {
		panic("PostgreSQL key rotation unit of work requires an integrity verifier")
	}
	return &KeyRotationUnitOfWork{runner: NewTransactionRunner(pool), verifier: verifier}
}

// Run commits ciphertext changes, job state, audit events, and pending checkpoints as one unit.
func (unitOfWork *KeyRotationUnitOfWork) Run(ctx context.Context, work keyrotation.TransactionWork) error {
	if work == nil {
		return keyrotation.ErrInvalidInput
	}
	err := unitOfWork.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		return work(ctx, newKeyRotationTransaction(queries, unitOfWork.verifier))
	})
	return mapUnitOfWorkError(err, keyrotation.ErrRepositoryUnavailable, rotationDomainErrors...)
}

type keyRotationTransaction struct {
	queries     QueryHandle
	audit       audit.Repository
	checkpoints audit.CheckpointRepository
}

func newKeyRotationTransaction(queries QueryHandle, verifier audit.IntegrityVerifier) keyrotation.Transaction {
	return &keyRotationTransaction{
		queries: queries, audit: newAuditRepository(queries, verifier),
		checkpoints: newAuditCheckpointRepository(queries, verifier),
	}
}

func (transaction *keyRotationTransaction) Audit() audit.Repository { return transaction.audit }

func (transaction *keyRotationTransaction) Checkpoints() audit.CheckpointRepository {
	return transaction.checkpoints
}

func (transaction *keyRotationTransaction) ListReferencedVersions(ctx context.Context, purpose keyrotation.Purpose) ([]uint32, error) {
	var (
		rows []int32
		err  error
	)
	switch purpose {
	case keyrotation.PurposePII:
		rows, err = transaction.queries.ListPIIKeyVersionsWithReferences(ctx)
	case keyrotation.PurposeTOTP:
		rows, err = transaction.queries.ListTotpKeyVersionsWithReferences(ctx)
	default:
		return nil, keyrotation.ErrInvalidInput
	}
	if err != nil {
		return nil, mapRotationQueryError(ctx, err)
	}
	versions := make([]uint32, len(rows))
	for index, row := range rows {
		if row <= 0 {
			return nil, keyrotation.ErrIntegrity
		}
		versions[index] = uint32(row)
	}
	return versions, nil
}

func (transaction *keyRotationTransaction) CreateJob(ctx context.Context, request keyrotation.CreateRequest) (bool, error) {
	if request.JobID == uuid.Nil || request.SourceVersion == 0 || request.SourceVersion > math.MaxInt32 ||
		request.TargetVersion == 0 || request.TargetVersion > math.MaxInt32 || request.SourceVersion == request.TargetVersion ||
		request.CreatedAt.IsZero() || !validInitialScope(request.Purpose, request.InitialScope) {
		return false, keyrotation.ErrInvalidInput
	}
	_, err := transaction.queries.CreateKeyRotationJob(ctx, sqlcgen.CreateKeyRotationJobParams{
		JobID: uuidToPG(request.JobID), Purpose: string(request.Purpose), SourceKeyVersion: int32(request.SourceVersion),
		TargetKeyVersion: int32(request.TargetVersion), CursorScope: string(request.InitialScope),
		CreatedAt: timeToPG(request.CreatedAt),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, mapRotationQueryError(ctx, err)
	}
	return true, nil
}

func (transaction *keyRotationTransaction) AcquireJob(
	ctx context.Context,
	owner outbox.LeaseOwner,
	acquiredAt, leaseUntil time.Time,
) (*keyrotation.AcquiredJob, error) {
	if !owner.Valid() || acquiredAt.IsZero() || !leaseUntil.After(acquiredAt) {
		return nil, keyrotation.ErrInvalidInput
	}
	row, err := transaction.queries.AcquireKeyRotationJobLease(ctx, sqlcgen.AcquireKeyRotationJobLeaseParams{
		AcquiredAt: timeToPG(acquiredAt), LeaseOwner: pgtype.Text{String: owner.Value(), Valid: true},
		LeaseUntil: timeToPG(leaseUntil),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, mapRotationQueryError(ctx, err)
	}
	job, err := rotationJobFromAcquiredRow(row)
	if err != nil {
		return nil, err
	}
	return &keyrotation.AcquiredJob{Job: job, StartedNow: row.StartedNow}, nil
}

func (transaction *keyRotationTransaction) ListUserProfiles(
	ctx context.Context,
	sourceVersion uint32,
	after uuid.UUID,
	batchSize uint32,
) ([]keyrotation.UserProfileCiphertext, error) {
	if !validQueryBounds(sourceVersion, batchSize) {
		return nil, keyrotation.ErrInvalidInput
	}
	rows, err := transaction.queries.ListUserProfilesForKeyRotation(ctx, sqlcgen.ListUserProfilesForKeyRotationParams{
		SourceKeyVersion: int32(sourceVersion), AfterUserID: optionalUUID(after), BatchSize: int32(batchSize),
	})
	if err != nil {
		return nil, mapRotationQueryError(ctx, err)
	}
	result := make([]keyrotation.UserProfileCiphertext, 0, len(rows))
	for _, row := range rows {
		encrypted, restoreErr := profile.RestoreEncryptedValue(profile.EncryptedValue{
			KeyVersion: uint32(row.RealNameKeyVersion), Nonce: row.RealNameNonce, Ciphertext: row.RealNameCiphertext,
		})
		if restoreErr != nil || !row.UserID.Valid || row.UserID.Bytes == uuid.Nil || row.ProfileVersion <= 0 {
			return nil, keyrotation.ErrIntegrity
		}
		result = append(result, keyrotation.UserProfileCiphertext{
			UserID: row.UserID.Bytes, ProfileVersion: row.ProfileVersion, Encrypted: encrypted,
		})
	}
	return result, nil
}

func (transaction *keyRotationTransaction) RotateUserProfile(
	ctx context.Context,
	row keyrotation.UserProfileCiphertext,
	rotated profile.EncryptedValue,
	sourceVersion uint32,
) (bool, error) {
	if row.UserID == uuid.Nil || row.ProfileVersion <= 0 || !validVersionForPostgres(sourceVersion) ||
		rotated.KeyVersion == 0 || rotated.KeyVersion > math.MaxInt32 {
		return false, keyrotation.ErrInvalidInput
	}
	_, err := transaction.queries.RotateUserProfileCiphertextCAS(ctx, sqlcgen.RotateUserProfileCiphertextCASParams{
		RealNameCiphertext: rotated.Ciphertext, RealNameNonce: rotated.Nonce, TargetKeyVersion: int32(rotated.KeyVersion),
		UserID: uuidToPG(row.UserID), ExpectedProfileVersion: row.ProfileVersion, SourceKeyVersion: int32(sourceVersion),
	})
	return rotationCASResult(ctx, err)
}

func (transaction *keyRotationTransaction) ListProfileExportItems(
	ctx context.Context,
	sourceVersion uint32,
	afterExportID uuid.UUID,
	afterOrdinal int64,
	batchSize uint32,
) ([]keyrotation.ProfileExportCiphertext, error) {
	if !validQueryBounds(sourceVersion, batchSize) ||
		((afterExportID == uuid.Nil) != (afterOrdinal == 0)) || afterOrdinal < 0 {
		return nil, keyrotation.ErrInvalidInput
	}
	rows, err := transaction.queries.ListProfileExportItemsForKeyRotation(ctx, sqlcgen.ListProfileExportItemsForKeyRotationParams{
		SourceKeyVersion: pgtype.Int4{Int32: int32(sourceVersion), Valid: true}, AfterExportID: optionalUUID(afterExportID),
		AfterOrdinal: optionalOrdinal(afterOrdinal), BatchSize: int32(batchSize),
	})
	if err != nil {
		return nil, mapRotationQueryError(ctx, err)
	}
	result := make([]keyrotation.ProfileExportCiphertext, 0, len(rows))
	for _, row := range rows {
		if !row.ExportID.Valid || row.ExportID.Bytes == uuid.Nil || row.Ordinal <= 0 || !row.UserID.Valid ||
			row.UserID.Bytes == uuid.Nil || !row.RealNameKeyVersion.Valid || row.RealNameKeyVersion.Int32 <= 0 {
			return nil, keyrotation.ErrIntegrity
		}
		encrypted, restoreErr := profile.RestoreEncryptedValue(profile.EncryptedValue{
			KeyVersion: uint32(row.RealNameKeyVersion.Int32), Nonce: row.RealNameNonce, Ciphertext: row.RealNameCiphertext,
		})
		if restoreErr != nil {
			return nil, keyrotation.ErrIntegrity
		}
		result = append(result, keyrotation.ProfileExportCiphertext{
			ExportID: row.ExportID.Bytes, Ordinal: row.Ordinal, UserID: row.UserID.Bytes, Encrypted: encrypted,
		})
	}
	return result, nil
}

func (transaction *keyRotationTransaction) RotateProfileExportItem(
	ctx context.Context,
	row keyrotation.ProfileExportCiphertext,
	rotated profile.EncryptedValue,
	sourceVersion uint32,
) (bool, error) {
	if row.ExportID == uuid.Nil || row.Ordinal <= 0 || !validVersionForPostgres(sourceVersion) ||
		rotated.KeyVersion == 0 || rotated.KeyVersion > math.MaxInt32 {
		return false, keyrotation.ErrInvalidInput
	}
	_, err := transaction.queries.RotateProfileExportItemCiphertextCAS(ctx, sqlcgen.RotateProfileExportItemCiphertextCASParams{
		RealNameCiphertext: rotated.Ciphertext, RealNameNonce: rotated.Nonce,
		TargetKeyVersion: pgtype.Int4{Int32: int32(rotated.KeyVersion), Valid: true}, ExportID: uuidToPG(row.ExportID),
		Ordinal: row.Ordinal, SourceKeyVersion: pgtype.Int4{Int32: int32(sourceVersion), Valid: true},
	})
	return rotationCASResult(ctx, err)
}

func (transaction *keyRotationTransaction) ListTOTPEnrollments(
	ctx context.Context,
	sourceVersion uint32,
	after uuid.UUID,
	batchSize uint32,
) ([]keyrotation.TOTPEnrollmentCiphertext, error) {
	if !validQueryBounds(sourceVersion, batchSize) {
		return nil, keyrotation.ErrInvalidInput
	}
	rows, err := transaction.queries.ListAdminTotpEnrollmentsForKeyRotation(ctx, sqlcgen.ListAdminTotpEnrollmentsForKeyRotationParams{
		SourceKeyVersion: int32(sourceVersion), AfterEnrollmentID: optionalUUID(after), BatchSize: int32(batchSize),
	})
	if err != nil {
		return nil, mapRotationQueryError(ctx, err)
	}
	result := make([]keyrotation.TOTPEnrollmentCiphertext, 0, len(rows))
	for _, row := range rows {
		if !row.EnrollmentID.Valid || row.EnrollmentID.Bytes == uuid.Nil || !row.AdminID.Valid || row.AdminID.Bytes == uuid.Nil ||
			row.AdminVersion <= 0 || row.KeyVersion <= 0 || len(row.Nonce) == 0 || len(row.Ciphertext) == 0 {
			return nil, keyrotation.ErrIntegrity
		}
		result = append(result, keyrotation.TOTPEnrollmentCiphertext{
			EnrollmentID: row.EnrollmentID.Bytes, AdminID: row.AdminID.Bytes, AdminVersion: row.AdminVersion,
			Encrypted: security.Encrypted[security.TOTPKeyPurpose]{
				KeyVersion: uint32(row.KeyVersion), Nonce: row.Nonce, Ciphertext: row.Ciphertext,
			},
		})
	}
	return result, nil
}

func (transaction *keyRotationTransaction) RotateTOTPEnrollment(
	ctx context.Context,
	row keyrotation.TOTPEnrollmentCiphertext,
	rotated security.Encrypted[security.TOTPKeyPurpose],
	sourceVersion uint32,
) (bool, error) {
	if row.EnrollmentID == uuid.Nil || row.AdminVersion <= 0 || !validVersionForPostgres(sourceVersion) ||
		rotated.KeyVersion == 0 || rotated.KeyVersion > math.MaxInt32 || len(rotated.Nonce) == 0 || len(rotated.Ciphertext) == 0 {
		return false, keyrotation.ErrInvalidInput
	}
	_, err := transaction.queries.RotateAdminTotpEnrollmentCiphertextCAS(ctx, sqlcgen.RotateAdminTotpEnrollmentCiphertextCASParams{
		Ciphertext: rotated.Ciphertext, Nonce: rotated.Nonce, TargetKeyVersion: int32(rotated.KeyVersion),
		EnrollmentID: uuidToPG(row.EnrollmentID), ExpectedAdminVersion: row.AdminVersion, SourceKeyVersion: int32(sourceVersion),
	})
	return rotationCASResult(ctx, err)
}

func (transaction *keyRotationTransaction) AdvanceCursor(ctx context.Context, request keyrotation.AdvanceRequest) error {
	if request.JobID == uuid.Nil || !request.Owner.Valid() || request.AdvancedAt.IsZero() ||
		request.ProcessedDelta < 0 || request.ConflictDelta < 0 || !validCursor(request.ExpectedCursor) || !validCursor(request.NextCursor) {
		return keyrotation.ErrInvalidInput
	}
	_, err := transaction.queries.AdvanceKeyRotationCursorCAS(ctx, sqlcgen.AdvanceKeyRotationCursorCASParams{
		NextCursorScope: string(request.NextCursor.Scope), NextCursorID: optionalUUID(request.NextCursor.ID),
		NextCursorOrdinal: optionalOrdinal(request.NextCursor.Ordinal), ProcessedDelta: request.ProcessedDelta,
		ConflictDelta: request.ConflictDelta, AdvancedAt: timeToPG(request.AdvancedAt), JobID: uuidToPG(request.JobID),
		LeaseOwner: pgtype.Text{String: request.Owner.Value(), Valid: true}, ExpectedCursorScope: string(request.ExpectedCursor.Scope),
		ExpectedCursorID: optionalUUID(request.ExpectedCursor.ID), ExpectedCursorOrdinal: optionalOrdinal(request.ExpectedCursor.Ordinal),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return keyrotation.ErrConcurrentTransition
	}
	return mapRotationQueryError(ctx, err)
}

func (transaction *keyRotationTransaction) CountReferences(ctx context.Context, purpose keyrotation.Purpose, version uint32) (int64, error) {
	if !validVersionForPostgres(version) {
		return 0, keyrotation.ErrInvalidInput
	}
	var (
		count int64
		err   error
	)
	switch purpose {
	case keyrotation.PurposePII:
		count, err = transaction.queries.CountPIIKeyReferences(ctx, sqlcgen.CountPIIKeyReferencesParams{KeyVersion: int32(version)})
	case keyrotation.PurposeTOTP:
		count, err = transaction.queries.CountTotpKeyReferences(ctx, sqlcgen.CountTotpKeyReferencesParams{KeyVersion: int32(version)})
	default:
		return 0, keyrotation.ErrInvalidInput
	}
	if err != nil {
		return 0, mapRotationQueryError(ctx, err)
	}
	if count < 0 {
		return 0, keyrotation.ErrIntegrity
	}
	return count, nil
}

func (transaction *keyRotationTransaction) CompleteJob(
	ctx context.Context,
	job keyrotation.Job,
	owner outbox.LeaseOwner,
	completedAt time.Time,
) (keyrotation.Job, error) {
	if job.ID == uuid.Nil || !owner.Valid() || completedAt.IsZero() {
		return keyrotation.Job{}, keyrotation.ErrInvalidInput
	}
	row, err := transaction.queries.CompleteKeyRotationJobCAS(ctx, sqlcgen.CompleteKeyRotationJobCASParams{
		CompletedAt: timeToPG(completedAt), JobID: uuidToPG(job.ID),
		LeaseOwner: pgtype.Text{String: owner.Value(), Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return keyrotation.Job{}, keyrotation.ErrConcurrentTransition
	}
	if err != nil {
		return keyrotation.Job{}, mapRotationQueryError(ctx, err)
	}
	if row.ProcessedCount < 0 || row.ConflictCount < 0 {
		return keyrotation.Job{}, keyrotation.ErrIntegrity
	}
	job.ProcessedCount = row.ProcessedCount
	job.ConflictCount = row.ConflictCount
	return job, nil
}

func (transaction *keyRotationTransaction) FailJob(
	ctx context.Context,
	jobID uuid.UUID,
	owner outbox.LeaseOwner,
	errorCode string,
	failedAt time.Time,
) error {
	if jobID == uuid.Nil || !owner.Valid() || errorCode == "" || len(errorCode) > outbox.MaximumNameLength || failedAt.IsZero() {
		return keyrotation.ErrInvalidInput
	}
	_, err := transaction.queries.FailKeyRotationJobCAS(ctx, sqlcgen.FailKeyRotationJobCASParams{
		ErrorCode: pgtype.Text{String: errorCode, Valid: true}, FailedAt: timeToPG(failedAt), JobID: uuidToPG(jobID),
		LeaseOwner: pgtype.Text{String: owner.Value(), Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return keyrotation.ErrConcurrentTransition
	}
	return mapRotationQueryError(ctx, err)
}

func (transaction *keyRotationTransaction) ReleaseJob(
	ctx context.Context,
	jobID uuid.UUID,
	owner outbox.LeaseOwner,
	releasedAt time.Time,
) error {
	if jobID == uuid.Nil || !owner.Valid() || releasedAt.IsZero() {
		return keyrotation.ErrInvalidInput
	}
	_, err := transaction.queries.ReleaseKeyRotationJobLeaseCAS(ctx, sqlcgen.ReleaseKeyRotationJobLeaseCASParams{
		ReleasedAt: timeToPG(releasedAt), JobID: uuidToPG(jobID),
		LeaseOwner: pgtype.Text{String: owner.Value(), Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return keyrotation.ErrConcurrentTransition
	}
	return mapRotationQueryError(ctx, err)
}

func rotationJobFromAcquiredRow(row sqlcgen.AcquireKeyRotationJobLeaseRow) (keyrotation.Job, error) {
	if !row.JobID.Valid || row.JobID.Bytes == uuid.Nil || row.SourceKeyVersion <= 0 || row.TargetKeyVersion <= 0 ||
		row.ProcessedCount < 0 || row.ConflictCount < 0 || !row.StartedAt.Valid {
		return keyrotation.Job{}, keyrotation.ErrIntegrity
	}
	job := keyrotation.Job{
		ID: row.JobID.Bytes, Purpose: keyrotation.Purpose(row.Purpose), SourceVersion: uint32(row.SourceKeyVersion),
		TargetVersion: uint32(row.TargetKeyVersion), Cursor: keyrotation.Cursor{Scope: keyrotation.Scope(row.CursorScope)},
		ProcessedCount: row.ProcessedCount, ConflictCount: row.ConflictCount, StartedAt: row.StartedAt.Time.UTC(),
	}
	if row.CursorID.Valid {
		job.Cursor.ID = row.CursorID.Bytes
	}
	if row.CursorOrdinal.Valid {
		job.Cursor.Ordinal = row.CursorOrdinal.Int64
	}
	if !validPurposeCursor(job.Purpose, job.Cursor) {
		return keyrotation.Job{}, keyrotation.ErrIntegrity
	}
	return job, nil
}

func validPurposeCursor(purpose keyrotation.Purpose, cursor keyrotation.Cursor) bool {
	if !validCursor(cursor) {
		return false
	}
	switch purpose {
	case keyrotation.PurposePII:
		return cursor.Scope == keyrotation.ScopeUserProfiles || cursor.Scope == keyrotation.ScopeProfileExportItems
	case keyrotation.PurposeTOTP:
		return cursor.Scope == keyrotation.ScopeAdminTOTPEnrollments
	default:
		return false
	}
}

func validInitialScope(purpose keyrotation.Purpose, scope keyrotation.Scope) bool {
	return purpose == keyrotation.PurposePII && scope == keyrotation.ScopeUserProfiles ||
		purpose == keyrotation.PurposeTOTP && scope == keyrotation.ScopeAdminTOTPEnrollments
}

func validCursor(cursor keyrotation.Cursor) bool {
	switch cursor.Scope {
	case keyrotation.ScopeUserProfiles, keyrotation.ScopeAdminTOTPEnrollments:
		return cursor.Ordinal == 0
	case keyrotation.ScopeProfileExportItems:
		return cursor.ID == uuid.Nil && cursor.Ordinal == 0 || cursor.ID != uuid.Nil && cursor.Ordinal > 0
	default:
		return false
	}
}

func validQueryBounds(version, batchSize uint32) bool {
	return validVersionForPostgres(version) && batchSize > 0 && batchSize <= math.MaxInt32
}

func validVersionForPostgres(version uint32) bool { return version > 0 && version <= math.MaxInt32 }

func optionalUUID(value uuid.UUID) pgtype.UUID {
	if value == uuid.Nil {
		return pgtype.UUID{}
	}
	return uuidToPG(value)
}

func optionalOrdinal(value int64) pgtype.Int8 {
	if value == 0 {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: value, Valid: true}
}

func rotationCASResult(ctx context.Context, err error) (bool, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, mapRotationQueryError(ctx, err)
	}
	return true, nil
}

func mapRotationQueryError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	return keyrotation.ErrRepositoryUnavailable
}

var rotationDomainErrors = append([]error{
	keyrotation.ErrInvalidInput,
	keyrotation.ErrRepositoryUnavailable,
	keyrotation.ErrConcurrentTransition,
	keyrotation.ErrIntegrity,
}, auditOutboxDomainErrors...)

var _ keyrotation.UnitOfWork = (*KeyRotationUnitOfWork)(nil)
