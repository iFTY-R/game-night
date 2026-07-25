package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	adminuser "github.com/iFTY-R/game-night/platform/admin/user"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// adminExportMaximumResultTTL matches the automatic deletion upper bound in the approved design.
	adminExportMaximumResultTTL = 24 * time.Hour
	// adminExportMaximumGrantTTL prevents download grants from becoming durable bearer credentials.
	adminExportMaximumGrantTTL = 5 * time.Minute
)

// AdminExportRepository owns durable jobs and single-use grants without exposing object-store URLs.
type AdminExportRepository struct {
	queries *sqlcgen.Queries
	runner  *TransactionRunner
}

// NewAdminExportRepository binds export job and grant state to one PostgreSQL pool.
func NewAdminExportRepository(pool *pgxpool.Pool) *AdminExportRepository {
	return &AdminExportRepository{queries: sqlcgen.New(pool), runner: NewTransactionRunner(pool)}
}

// CreateExportJob stores a canonical filter snapshot and returns the original row on same-digest replay.
func (repository *AdminExportRepository) CreateExportJob(ctx context.Context, command adminuser.CreateExportJobCommand) (adminuser.ExportJob, error) {
	job := command.Job
	if repository == nil || repository.queries == nil || ctx == nil || !validNewExportJob(job) {
		return adminuser.ExportJob{}, adminuser.ErrInvalidInput
	}
	row, err := repository.queries.CreateAdminExportJob(ctx, sqlcgen.CreateAdminExportJobParams{
		ExportID: uuidToPG(job.ID), ActorAdminID: uuidToPG(job.ActorAdminID), OperationID: job.OperationID,
		RequestDigest: job.RequestDigest[:], FilterSchemaVersion: int32(job.FilterSchemaVersion),
		FilterSnapshot: append([]byte(nil), job.FilterSnapshot...), FilterDigest: job.FilterDigest[:],
		FieldNames: append([]string(nil), job.Fields...), MaskingPolicy: job.MaskingPolicy,
		ResultSchemaVersion: int32(job.ResultSchemaVersion), ResultExpiresAt: timeToPG(job.ResultExpiresAt), CreatedAt: timeToPG(job.CreatedAt),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return adminuser.ExportJob{}, adminuser.ErrIdempotencyConflict
	}
	if err != nil {
		return adminuser.ExportJob{}, mapAdminUserQueryError(ctx, err, adminuser.ErrConflict)
	}
	return adminExportJobFromRow(row)
}

// GetExportJob returns one durable export without exposing its download token or object contents.
func (repository *AdminExportRepository) GetExportJob(ctx context.Context, exportID uuid.UUID) (adminuser.ExportJob, error) {
	if repository == nil || repository.queries == nil || ctx == nil || exportID == uuid.Nil {
		return adminuser.ExportJob{}, adminuser.ErrInvalidInput
	}
	row, err := repository.queries.GetAdminExportJob(ctx, sqlcgen.GetAdminExportJobParams{ExportID: uuidToPG(exportID)})
	if err != nil {
		return adminuser.ExportJob{}, mapAdminUserQueryError(ctx, err, adminuser.ErrNotFound)
	}
	return adminExportJobFromRow(row)
}

// ClaimExportJob leases the oldest queued or abandoned export using database time.
func (repository *AdminExportRepository) ClaimExportJob(ctx context.Context, owner string, lease time.Duration) (adminuser.ExportJob, error) {
	if repository == nil || repository.queries == nil || ctx == nil || !validLease(owner, lease) {
		return adminuser.ExportJob{}, adminuser.ErrInvalidInput
	}
	row, err := repository.queries.ClaimAdminExportJob(ctx, sqlcgen.ClaimAdminExportJobParams{
		LeaseOwner: pgtype.Text{String: owner, Valid: true}, LeaseSeconds: int64(lease / time.Second),
	})
	if err != nil {
		return adminuser.ExportJob{}, mapAdminUserQueryError(ctx, err, adminuser.ErrNotFound)
	}
	return adminExportJobFromRow(row)
}

// CompleteExportJob publishes only encrypted-object metadata while the current worker lease remains valid.
func (repository *AdminExportRepository) CompleteExportJob(ctx context.Context, command adminuser.CompleteExportJobCommand) (adminuser.ExportJob, error) {
	if repository == nil || repository.queries == nil || ctx == nil || !validExportCompletion(command) {
		return adminuser.ExportJob{}, adminuser.ErrInvalidInput
	}
	row, err := repository.queries.CompleteAdminExportJobCAS(ctx, sqlcgen.CompleteAdminExportJobCASParams{
		NextState: command.NextState, MatchedUsers: command.MatchedUsers, ExportedUsers: command.ExportedUsers, FailedUsers: command.FailedUsers,
		ResultObjectKey: pgtype.Text{String: command.ResultObjectKey, Valid: true}, ResultDigest: command.ResultDigest[:],
		ResultKeyVersion: pgtype.Int4{Int32: int32(command.ResultKeyVersion), Valid: true}, CompletedAt: timeToPG(command.CompletedAt),
		ExportID: uuidToPG(command.Job.ID), ExpectedLeaseOwner: pgtype.Text{String: command.Job.LeaseOwner, Valid: true},
		ExpectedVersion: int64(command.Job.Version),
	})
	if err != nil {
		return adminuser.ExportJob{}, mapAdminUserQueryError(ctx, err, adminuser.ErrConflict)
	}
	return adminExportJobFromRow(row)
}

// FailExportJob records a stable worker failure and clears the lease without publishing partial object metadata.
func (repository *AdminExportRepository) FailExportJob(ctx context.Context, command adminuser.FailExportJobCommand) (adminuser.ExportJob, error) {
	if repository == nil || repository.queries == nil || ctx == nil || !validExportFailure(command) {
		return adminuser.ExportJob{}, adminuser.ErrInvalidInput
	}
	row, err := repository.queries.FailAdminExportJobCAS(ctx, sqlcgen.FailAdminExportJobCASParams{
		MatchedUsers: command.MatchedUsers, ExportedUsers: command.ExportedUsers, FailedUsers: command.FailedUsers,
		ErrorMessageKey: pgtype.Text{String: command.ErrorMessageKey, Valid: true}, CompletedAt: timeToPG(command.CompletedAt),
		ExportID: uuidToPG(command.Job.ID), ExpectedLeaseOwner: pgtype.Text{String: command.Job.LeaseOwner, Valid: true},
		ExpectedVersion: int64(command.Job.Version),
	})
	if err != nil {
		return adminuser.ExportJob{}, mapAdminUserQueryError(ctx, err, adminuser.ErrConflict)
	}
	return adminExportJobFromRow(row)
}

// ExpireExportResults clears object metadata exactly once without extending any result TTL.
func (repository *AdminExportRepository) ExpireExportResults(ctx context.Context, boundary time.Time) ([]uuid.UUID, error) {
	if repository == nil || repository.queries == nil || ctx == nil || boundary.IsZero() {
		return nil, adminuser.ErrInvalidInput
	}
	rows, err := repository.queries.ExpireAdminExportResults(ctx, sqlcgen.ExpireAdminExportResultsParams{Boundary: timeToPG(boundary)})
	if err != nil {
		return nil, mapAdminUserQueryError(ctx, err, adminuser.ErrRepositoryUnavailable)
	}
	result := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		if !row.Valid || row.Bytes == uuid.Nil {
			return nil, adminuser.ErrIntegrity
		}
		result = append(result, row.Bytes)
	}
	return result, nil
}

// DeleteExportResult clears object metadata only for the exact result version already deleted from object storage.
func (repository *AdminExportRepository) DeleteExportResult(ctx context.Context, command adminuser.DeleteExportResultCommand) (adminuser.ExportJob, error) {
	if repository == nil || repository.queries == nil || ctx == nil || command.ExportID == uuid.Nil || command.ExpectedVersion == 0 || command.DeletedAt.IsZero() {
		return adminuser.ExportJob{}, adminuser.ErrInvalidInput
	}
	row, err := repository.queries.DeleteAdminExportResultCAS(ctx, sqlcgen.DeleteAdminExportResultCASParams{
		DeletedAt: timeToPG(command.DeletedAt), ExportID: uuidToPG(command.ExportID), ExpectedVersion: int64(command.ExpectedVersion),
	})
	if err != nil {
		return adminuser.ExportJob{}, mapAdminUserQueryError(ctx, err, adminuser.ErrConflict)
	}
	return adminExportJobFromRow(row)
}

// CreateDownloadGrant locks the ready export before storing a five-minute, session-bound token digest.
func (repository *AdminExportRepository) CreateDownloadGrant(ctx context.Context, grant adminuser.DownloadGrant) (adminuser.DownloadGrant, error) {
	if repository == nil || repository.runner == nil || ctx == nil || !validNewDownloadGrant(grant) {
		return adminuser.DownloadGrant{}, adminuser.ErrInvalidInput
	}
	var stored sqlcgen.AdminExportDownloadGrant
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		export, err := queries.GetAdminExportJobForDownloadGrant(ctx, sqlcgen.GetAdminExportJobForDownloadGrantParams{ExportID: uuidToPG(grant.ExportID)})
		if err != nil {
			return err
		}
		if !export.ActorAdminID.Valid || export.ActorAdminID.Bytes != grant.ActorAdminID || export.Version != int64(grant.ExpectedExportVersion) ||
			export.MaskingPolicy != grant.MaskingPolicy || (export.State != "succeeded" && export.State != "partially_succeeded") ||
			!export.ResultExpiresAt.Valid || !export.ResultExpiresAt.Time.After(grant.CreatedAt) || grant.ExpiresAt.After(export.ResultExpiresAt.Time) ||
			!export.ResultObjectKey.Valid {
			return adminuser.ErrConflict
		}
		stored, err = queries.CreateAdminExportDownloadGrant(ctx, sqlcgen.CreateAdminExportDownloadGrantParams{
			GrantID: uuidToPG(grant.ID), ExportID: uuidToPG(grant.ExportID), ActorAdminID: uuidToPG(grant.ActorAdminID),
			SessionID: uuidToPG(grant.SessionID), OperationID: grant.OperationID, RequestDigest: grant.RequestDigest[:],
			TokenDigest: grant.TokenDigest[:], TokenKeyVersion: int32(grant.TokenKeyVersion),
			ExpectedExportVersion: int64(grant.ExpectedExportVersion), MaskingPolicy: grant.MaskingPolicy,
			CreatedAt: timeToPG(grant.CreatedAt), ExpiresAt: timeToPG(grant.ExpiresAt),
		})
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return adminuser.DownloadGrant{}, adminuser.ErrIdempotencyConflict
	}
	if err != nil {
		return adminuser.DownloadGrant{}, mapAdminUserQueryError(ctx, err, adminuser.ErrConflict)
	}
	return adminDownloadGrantFromRow(stored)
}

// ConsumeDownloadGrant atomically wins one active token only when its export/session/version binding is still current.
func (repository *AdminExportRepository) ConsumeDownloadGrant(
	ctx context.Context,
	tokenKeyVersion uint32,
	tokenDigest [32]byte,
	actorAdminID, sessionID uuid.UUID,
) (adminuser.ConsumedExport, error) {
	if repository == nil || repository.queries == nil || ctx == nil || tokenKeyVersion == 0 || actorAdminID == uuid.Nil || sessionID == uuid.Nil {
		return adminuser.ConsumedExport{}, adminuser.ErrInvalidInput
	}
	row, err := repository.queries.ConsumeAdminExportDownloadGrantCAS(ctx, sqlcgen.ConsumeAdminExportDownloadGrantCASParams{
		TokenKeyVersion: int32(tokenKeyVersion), TokenDigest: tokenDigest[:], ActorAdminID: uuidToPG(actorAdminID), SessionID: uuidToPG(sessionID),
	})
	if err != nil {
		return adminuser.ConsumedExport{}, mapAdminUserQueryError(ctx, err, adminuser.ErrConflict)
	}
	grant, err := adminDownloadGrantFromValues(
		row.GrantID, row.ExportID, row.ActorAdminID, row.SessionID, row.OperationID, row.RequestDigest, row.TokenDigest,
		row.TokenKeyVersion, row.ExpectedExportVersion, row.MaskingPolicy, row.State, row.CreatedAt, row.ExpiresAt, row.ConsumedAt, row.RevokedAt, row.Version,
	)
	if err != nil {
		return adminuser.ConsumedExport{}, err
	}
	resultDigest, ok := bytesToDigest(row.ResultDigest)
	if !ok || !row.ResultObjectKey.Valid || row.ResultObjectKey.String == "" || !row.ResultKeyVersion.Valid || row.ResultKeyVersion.Int32 <= 0 ||
		row.ResultSchemaVersion <= 0 || !row.ResultExpiresAt.Valid || row.ExportVersion <= 0 {
		return adminuser.ConsumedExport{}, adminuser.ErrIntegrity
	}
	return adminuser.ConsumedExport{
		Grant: grant, ResultObjectKey: row.ResultObjectKey.String, ResultDigest: resultDigest,
		ResultKeyVersion: uint32(row.ResultKeyVersion.Int32), ResultSchemaVersion: uint32(row.ResultSchemaVersion),
		ResultExpiresAt: canonicalPostgresTime(row.ResultExpiresAt), MaskingPolicy: row.MaskingPolicy, ExportVersion: uint64(row.ExportVersion),
	}, nil
}

func validNewExportJob(job adminuser.ExportJob) bool {
	if job.ID == uuid.Nil || job.ActorAdminID == uuid.Nil || len(job.OperationID) == 0 || len(job.OperationID) > 128 ||
		job.FilterSchemaVersion == 0 || !json.Valid(job.FilterSnapshot) || len(job.FilterSnapshot) > 64*1024 ||
		job.ResultSchemaVersion == 0 || job.CreatedAt.IsZero() || !job.ResultExpiresAt.After(job.CreatedAt) ||
		job.ResultExpiresAt.Sub(job.CreatedAt) > adminExportMaximumResultTTL || !validMaskingPolicy(job.MaskingPolicy) ||
		(job.State != "" && job.State != "queued") || len(job.Fields) == 0 || len(job.Fields) > 32 {
		return false
	}
	seen := make(map[string]struct{}, len(job.Fields))
	for _, field := range job.Fields {
		if strings.TrimSpace(field) != field || field == "" || len(field) > 64 {
			return false
		}
		if _, exists := seen[field]; exists {
			return false
		}
		seen[field] = struct{}{}
	}
	return true
}

func validExportCompletion(command adminuser.CompleteExportJobCommand) bool {
	return command.Job.ID != uuid.Nil && command.Job.State == "running" && command.Job.LeaseOwner != "" && command.Job.Version > 0 &&
		(command.NextState == "succeeded" || command.NextState == "partially_succeeded") && command.MatchedUsers >= 0 &&
		command.ExportedUsers >= 0 && command.FailedUsers >= 0 && command.ExportedUsers+command.FailedUsers <= command.MatchedUsers &&
		strings.TrimSpace(command.ResultObjectKey) == command.ResultObjectKey && command.ResultObjectKey != "" && len(command.ResultObjectKey) <= 1024 &&
		command.ResultKeyVersion > 0 && !command.CompletedAt.IsZero()
}

func validExportFailure(command adminuser.FailExportJobCommand) bool {
	return command.Job.ID != uuid.Nil && command.Job.State == "running" && command.Job.LeaseOwner != "" && command.Job.Version > 0 &&
		command.MatchedUsers >= 0 && command.ExportedUsers >= 0 && command.FailedUsers >= 0 &&
		command.ExportedUsers+command.FailedUsers <= command.MatchedUsers && validAdminErrorMessageKey(command.ErrorMessageKey) &&
		!command.CompletedAt.IsZero()
}

func validNewDownloadGrant(grant adminuser.DownloadGrant) bool {
	return grant.ID != uuid.Nil && grant.ExportID != uuid.Nil && grant.ActorAdminID != uuid.Nil && grant.SessionID != uuid.Nil &&
		len(grant.OperationID) > 0 && len(grant.OperationID) <= 128 && grant.TokenKeyVersion > 0 && grant.ExpectedExportVersion > 0 &&
		validMaskingPolicy(grant.MaskingPolicy) && (grant.State == "" || grant.State == "active") && !grant.CreatedAt.IsZero() &&
		grant.ExpiresAt.After(grant.CreatedAt) && grant.ExpiresAt.Sub(grant.CreatedAt) <= adminExportMaximumGrantTTL
}

func validMaskingPolicy(policy string) bool {
	return policy == "redact_pii" || policy == "include_authorized_pii"
}

func adminExportJobFromRow(row sqlcgen.AdminExportJob) (adminuser.ExportJob, error) {
	requestDigest, ok := bytesToDigest(row.RequestDigest)
	if !ok {
		return adminuser.ExportJob{}, adminuser.ErrIntegrity
	}
	filterDigest, ok := bytesToDigest(row.FilterDigest)
	if !ok || !row.ExportID.Valid || row.ExportID.Bytes == uuid.Nil || !row.ActorAdminID.Valid || row.ActorAdminID.Bytes == uuid.Nil ||
		row.FilterSchemaVersion <= 0 || !json.Valid(row.FilterSnapshot) || row.ResultSchemaVersion <= 0 || !row.ResultExpiresAt.Valid ||
		row.Version <= 0 || !row.CreatedAt.Valid || !row.UpdatedAt.Valid {
		return adminuser.ExportJob{}, adminuser.ErrIntegrity
	}
	job := adminuser.ExportJob{
		ID: row.ExportID.Bytes, ActorAdminID: row.ActorAdminID.Bytes, OperationID: row.OperationID, RequestDigest: requestDigest,
		FilterSchemaVersion: uint32(row.FilterSchemaVersion), FilterSnapshot: append([]byte(nil), row.FilterSnapshot...), FilterDigest: filterDigest,
		Fields: append([]string(nil), row.FieldNames...), MaskingPolicy: row.MaskingPolicy, State: row.State,
		MatchedUsers: row.MatchedUsers, ExportedUsers: row.ExportedUsers, FailedUsers: row.FailedUsers,
		ResultSchemaVersion: uint32(row.ResultSchemaVersion), ResultExpiresAt: canonicalPostgresTime(row.ResultExpiresAt),
		ErrorMessageKey: row.ErrorMessageKey.String, LeaseOwner: row.LeaseOwner.String, LeaseUntil: canonicalPostgresTime(row.LeaseUntil), Version: uint64(row.Version),
		CreatedAt: canonicalPostgresTime(row.CreatedAt), StartedAt: canonicalPostgresTime(row.StartedAt),
		CompletedAt: canonicalPostgresTime(row.CompletedAt), UpdatedAt: canonicalPostgresTime(row.UpdatedAt),
	}
	if row.ResultObjectKey.Valid {
		resultDigest, digestOK := bytesToDigest(row.ResultDigest)
		if !digestOK || !row.ResultKeyVersion.Valid || row.ResultKeyVersion.Int32 <= 0 {
			return adminuser.ExportJob{}, adminuser.ErrIntegrity
		}
		job.ResultObjectKey = row.ResultObjectKey.String
		job.ResultDigest = resultDigest
		job.ResultKeyVersion = uint32(row.ResultKeyVersion.Int32)
	}
	return job, nil
}

func adminDownloadGrantFromRow(row sqlcgen.AdminExportDownloadGrant) (adminuser.DownloadGrant, error) {
	return adminDownloadGrantFromValues(
		row.GrantID, row.ExportID, row.ActorAdminID, row.SessionID, row.OperationID, row.RequestDigest, row.TokenDigest,
		row.TokenKeyVersion, row.ExpectedExportVersion, row.MaskingPolicy, row.State, row.CreatedAt, row.ExpiresAt, row.ConsumedAt, row.RevokedAt, row.Version,
	)
}

func adminDownloadGrantFromValues(
	grantID, exportID, actorAdminID, sessionID pgtype.UUID,
	operationID string,
	requestDigest, tokenDigest []byte,
	tokenKeyVersion int32,
	expectedExportVersion int64,
	maskingPolicy, state string,
	createdAt, expiresAt, consumedAt, revokedAt pgtype.Timestamptz,
	version int64,
) (adminuser.DownloadGrant, error) {
	request, ok := bytesToDigest(requestDigest)
	if !ok {
		return adminuser.DownloadGrant{}, adminuser.ErrIntegrity
	}
	token, ok := bytesToDigest(tokenDigest)
	if !ok || !grantID.Valid || grantID.Bytes == uuid.Nil || !exportID.Valid || exportID.Bytes == uuid.Nil ||
		!actorAdminID.Valid || actorAdminID.Bytes == uuid.Nil || !sessionID.Valid || sessionID.Bytes == uuid.Nil ||
		tokenKeyVersion <= 0 || expectedExportVersion <= 0 || !validMaskingPolicy(maskingPolicy) || version <= 0 || !createdAt.Valid || !expiresAt.Valid {
		return adminuser.DownloadGrant{}, adminuser.ErrIntegrity
	}
	return adminuser.DownloadGrant{
		ID: grantID.Bytes, ExportID: exportID.Bytes, ActorAdminID: actorAdminID.Bytes, SessionID: sessionID.Bytes,
		OperationID: operationID, RequestDigest: request, TokenDigest: token, TokenKeyVersion: uint32(tokenKeyVersion),
		ExpectedExportVersion: uint64(expectedExportVersion), MaskingPolicy: maskingPolicy, State: state,
		CreatedAt: canonicalPostgresTime(createdAt), ExpiresAt: canonicalPostgresTime(expiresAt),
		ConsumedAt: canonicalPostgresTime(consumedAt), RevokedAt: canonicalPostgresTime(revokedAt), Version: uint64(version),
	}, nil
}

var _ adminuser.ExportRepository = (*AdminExportRepository)(nil)
