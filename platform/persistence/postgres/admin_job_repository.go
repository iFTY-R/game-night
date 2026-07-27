package postgres

import (
	"bytes"
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
	// adminJobMinimumLease prevents hot reclaim loops when a worker briefly stalls.
	adminJobMinimumLease = time.Second
	// adminJobMaximumLease bounds how long a failed worker can hide one target.
	adminJobMaximumLease = 5 * time.Minute
)

// AdminJobRepository keeps preview consumption, job creation, item leases, and erasure leases transactional.
type AdminJobRepository struct {
	queries *sqlcgen.Queries
	runner  *TransactionRunner
}

// NewAdminJobRepository binds durable user-center jobs to one pool.
func NewAdminJobRepository(pool *pgxpool.Pool) *AdminJobRepository {
	return &AdminJobRepository{queries: sqlcgen.New(pool), runner: NewTransactionRunner(pool)}
}

// CreateBatchPreview persists only validated, versioned JSON selection snapshots with a short explicit TTL.
func (repository *AdminJobRepository) CreateBatchPreview(ctx context.Context, command adminuser.CreateBatchPreviewCommand) (adminuser.BatchPreview, error) {
	preview := command.Preview
	if repository == nil || repository.queries == nil || ctx == nil || !validBatchPreview(preview) {
		return adminuser.BatchPreview{}, adminuser.ErrInvalidInput
	}
	row, err := repository.queries.CreateAdminBatchPreview(ctx, sqlcgen.CreateAdminBatchPreviewParams{
		PreviewID: uuidToPG(preview.ID), ActorAdminID: uuidToPG(preview.ActorAdminID), Command: preview.Command,
		SelectionSchemaVersion: int32(preview.SelectionSchemaVersion), SelectionSnapshot: append([]byte(nil), preview.SelectionSnapshot...),
		SelectionDigest: preview.SelectionDigest[:], PreviewDigest: preview.PreviewDigest[:], TargetCount: preview.TargetCount,
		ExecutableCount: preview.ExecutableCount, BlockedCount: preview.BlockedCount,
		SampledAt: timeToPG(preview.SampledAt), ExpiresAt: timeToPG(preview.ExpiresAt),
	})
	if err != nil {
		return adminuser.BatchPreview{}, mapAdminUserQueryError(ctx, err, adminuser.ErrConflict)
	}
	return adminBatchPreviewFromRow(row)
}

// GetBatchPreview reloads one persisted preview so the service can reconstruct the exact frozen target set before execution.
func (repository *AdminJobRepository) GetBatchPreview(ctx context.Context, previewID, actorAdminID uuid.UUID) (adminuser.BatchPreview, error) {
	if repository == nil || repository.queries == nil || ctx == nil || previewID == uuid.Nil || actorAdminID == uuid.Nil {
		return adminuser.BatchPreview{}, adminuser.ErrInvalidInput
	}
	row, err := repository.queries.GetAdminBatchPreview(ctx, sqlcgen.GetAdminBatchPreviewParams{
		PreviewID: uuidToPG(previewID), ActorAdminID: uuidToPG(actorAdminID),
	})
	if err != nil {
		return adminuser.BatchPreview{}, mapAdminUserQueryError(ctx, err, adminuser.ErrNotFound)
	}
	return adminBatchPreviewFromRow(row)
}

// StartBatchJob atomically consumes a live preview, creates the job, and freezes every executable target.
func (repository *AdminJobRepository) StartBatchJob(ctx context.Context, command adminuser.StartBatchJobCommand) (adminuser.BatchJob, error) {
	if repository == nil || repository.runner == nil || ctx == nil || !validStartBatchJob(command) {
		return adminuser.BatchJob{}, adminuser.ErrInvalidInput
	}
	var stored sqlcgen.AdminBatchJob
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		existing, err := queries.GetAdminBatchJobByOperation(ctx, sqlcgen.GetAdminBatchJobByOperationParams{
			ActorAdminID: uuidToPG(command.ActorAdminID), OperationID: command.OperationID,
		})
		if err == nil {
			if !bytes.Equal(existing.RequestDigest, command.RequestDigest[:]) {
				return adminuser.ErrIdempotencyConflict
			}
			stored = existing
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		preview, err := queries.GetUsableAdminBatchPreviewForUpdate(ctx, sqlcgen.GetUsableAdminBatchPreviewForUpdateParams{
			PreviewID: uuidToPG(command.PreviewID), ActorAdminID: uuidToPG(command.ActorAdminID),
		})
		if err != nil {
			return err
		}
		if preview.Version != int64(command.ExpectedPreviewVersion) || !bytes.Equal(preview.PreviewDigest, command.PreviewDigest[:]) ||
			preview.ExecutableCount != int64(len(command.Targets)) {
			return adminuser.ErrConflict
		}
		stored, err = queries.CreateAdminBatchJob(ctx, sqlcgen.CreateAdminBatchJobParams{
			BatchJobID: uuidToPG(command.BatchJobID), ActorAdminID: uuidToPG(command.ActorAdminID), OperationID: command.OperationID,
			RequestDigest: command.RequestDigest[:], PreviewID: uuidToPG(command.PreviewID), Command: preview.Command,
			SelectionSchemaVersion: preview.SelectionSchemaVersion, SelectionSnapshot: preview.SelectionSnapshot,
			SelectionDigest: preview.SelectionDigest, Reason: strings.TrimSpace(command.Reason),
			TargetCount: int64(len(command.Targets)), CreatedAt: timeToPG(command.CreatedAt),
		})
		if err != nil {
			return err
		}
		for _, target := range command.Targets {
			if _, err = queries.CreateAdminBatchJobItem(ctx, sqlcgen.CreateAdminBatchJobItemParams{
				ItemID: uuidToPG(target.ItemID), BatchJobID: uuidToPG(command.BatchJobID), UserID: uuidToPG(target.UserID),
				ExpectedUserVersion: int64(target.ExpectedUserVersion), RequestDigest: target.RequestDigest[:], CreatedAt: timeToPG(command.CreatedAt),
			}); err != nil {
				return err
			}
		}
		_, err = queries.ConsumeAdminBatchPreviewCAS(ctx, sqlcgen.ConsumeAdminBatchPreviewCASParams{
			ConsumedAt: timeToPG(command.CreatedAt), PreviewID: uuidToPG(command.PreviewID), ExpectedVersion: preview.Version,
		})
		return err
	})
	if err != nil {
		if errors.Is(err, adminuser.ErrIdempotencyConflict) {
			return adminuser.BatchJob{}, err
		}
		return adminuser.BatchJob{}, mapAdminUserQueryError(ctx, err, adminuser.ErrConflict)
	}
	return adminBatchJobFromRow(stored)
}

// GetBatchJob reads one durable batch job without mutating any lease or aggregate state.
func (repository *AdminJobRepository) GetBatchJob(ctx context.Context, batchJobID uuid.UUID) (adminuser.BatchJob, error) {
	if repository == nil || repository.queries == nil || ctx == nil || batchJobID == uuid.Nil {
		return adminuser.BatchJob{}, adminuser.ErrInvalidInput
	}
	row, err := repository.queries.GetAdminBatchJobByID(ctx, sqlcgen.GetAdminBatchJobByIDParams{BatchJobID: uuidToPG(batchJobID)})
	if err != nil {
		return adminuser.BatchJob{}, mapAdminUserQueryError(ctx, err, adminuser.ErrNotFound)
	}
	return adminBatchJobFromRow(row)
}

// ListBatchJobs reads a bounded job history page; pagination tokens are handled at the service layer.
func (repository *AdminJobRepository) ListBatchJobs(ctx context.Context, query adminuser.BatchJobListQuery) ([]adminuser.BatchJob, error) {
	if repository == nil || repository.queries == nil || ctx == nil || query.PageSize == 0 || query.PageSize > adminuser.MaximumBatchPageSize {
		return nil, adminuser.ErrInvalidInput
	}
	commands := make([]string, 0, len(query.Commands))
	for _, command := range query.Commands {
		commands = append(commands, string(command))
	}
	rows, err := repository.queries.ListAdminBatchJobs(ctx, sqlcgen.ListAdminBatchJobsParams{
		States: query.States, Commands: commands, CreatedFrom: adminOptionalTimeToPG(query.CreatedFrom), CreatedTo: adminOptionalTimeToPG(query.CreatedTo),
		SortField: string(query.SortField), SortDirection: string(query.Direction), PageSize: int32(query.PageSize),
		AfterSortTime: adminOptionalTimeToPG(query.After.SortTime), AfterBatchJobID: optionalUUID(query.After.BatchJobID),
	})
	if err != nil {
		return nil, mapAdminUserQueryError(ctx, err, adminuser.ErrRepositoryUnavailable)
	}
	result := make([]adminuser.BatchJob, 0, len(rows))
	for _, row := range rows {
		job, mapErr := adminBatchJobFromRow(row)
		if mapErr != nil {
			return nil, mapErr
		}
		result = append(result, job)
	}
	return result, nil
}

// ClaimBatchItem leases the oldest runnable target and advances aggregate counters in the same transaction.
func (repository *AdminJobRepository) ClaimBatchItem(ctx context.Context, batchJobID uuid.UUID, owner string, lease time.Duration) (adminuser.BatchItem, error) {
	if repository == nil || repository.runner == nil || ctx == nil || batchJobID == uuid.Nil || !validLease(owner, lease) {
		return adminuser.BatchItem{}, adminuser.ErrInvalidInput
	}
	var claimed sqlcgen.ClaimAdminBatchJobItemRow
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		var err error
		claimed, err = queries.ClaimAdminBatchJobItem(ctx, sqlcgen.ClaimAdminBatchJobItemParams{
			LeaseOwner: pgtype.Text{String: owner, Valid: true}, LeaseSeconds: int64(lease / time.Second), TargetBatchJobID: uuidToPG(batchJobID),
		})
		if err != nil {
			return err
		}
		if claimed.StartedNow {
			_, err = queries.MarkAdminBatchJobItemClaimed(ctx, sqlcgen.MarkAdminBatchJobItemClaimedParams{BatchJobID: claimed.BatchJobID})
		}
		return err
	})
	if err != nil {
		return adminuser.BatchItem{}, mapAdminUserQueryError(ctx, err, adminuser.ErrNotFound)
	}
	return adminBatchItemFromValues(
		claimed.ItemID, claimed.BatchJobID, claimed.UserID, claimed.ExpectedUserVersion, claimed.RequestDigest,
		claimed.State, claimed.AttemptCount, claimed.LeaseOwner, claimed.LeaseUntil, claimed.ErrorMessageKey, claimed.AuditEventID,
		claimed.StartedAt, claimed.CompletedAt, claimed.Version, claimed.CreatedAt, claimed.UpdatedAt,
	)
}

// ClaimNextBatchItem leases the oldest runnable item across all durable batch jobs. The worker never scans jobs
// in application memory, so another process can resume abandoned work after the lease expires.
func (repository *AdminJobRepository) ClaimNextBatchItem(ctx context.Context, owner string, lease time.Duration) (adminuser.BatchItem, error) {
	if repository == nil || repository.runner == nil || ctx == nil || !validLease(owner, lease) {
		return adminuser.BatchItem{}, adminuser.ErrInvalidInput
	}
	var claimed sqlcgen.ClaimNextAdminBatchJobItemRow
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		var claimErr error
		claimed, claimErr = queries.ClaimNextAdminBatchJobItem(ctx, sqlcgen.ClaimNextAdminBatchJobItemParams{
			LeaseOwner: pgtype.Text{String: owner, Valid: true}, LeaseSeconds: int64(lease / time.Second),
		})
		if claimErr != nil {
			return claimErr
		}
		if claimed.StartedNow {
			_, claimErr = queries.MarkAdminBatchJobItemClaimed(ctx, sqlcgen.MarkAdminBatchJobItemClaimedParams{BatchJobID: claimed.BatchJobID})
		}
		return claimErr
	})
	if err != nil {
		return adminuser.BatchItem{}, mapAdminUserQueryError(ctx, err, adminuser.ErrNotFound)
	}
	return adminBatchItemFromValues(
		claimed.ItemID, claimed.BatchJobID, claimed.UserID, claimed.ExpectedUserVersion, claimed.RequestDigest,
		claimed.State, claimed.AttemptCount, claimed.LeaseOwner, claimed.LeaseUntil, claimed.ErrorMessageKey, claimed.AuditEventID,
		claimed.StartedAt, claimed.CompletedAt, claimed.Version, claimed.CreatedAt, claimed.UpdatedAt,
	)
}

// ListBatchItems reads one job's durable item states in creation order.
func (repository *AdminJobRepository) ListBatchItems(ctx context.Context, query adminuser.BatchItemListQuery) ([]adminuser.BatchItem, error) {
	if repository == nil || repository.queries == nil || ctx == nil || query.BatchJobID == uuid.Nil || query.PageSize == 0 || query.PageSize > adminuser.MaximumBatchPageSize {
		return nil, adminuser.ErrInvalidInput
	}
	rows, err := repository.queries.ListAdminBatchJobItems(ctx, sqlcgen.ListAdminBatchJobItemsParams{
		BatchJobID: uuidToPG(query.BatchJobID), States: query.States, PageSize: int32(query.PageSize),
		AfterCreatedAt: adminOptionalTimeToPG(query.After.CreatedAt), AfterItemID: optionalUUID(query.After.ItemID),
	})
	if err != nil {
		return nil, mapAdminUserQueryError(ctx, err, adminuser.ErrRepositoryUnavailable)
	}
	result := make([]adminuser.BatchItem, 0, len(rows))
	for _, row := range rows {
		item, mapErr := adminBatchItemFromRow(row)
		if mapErr != nil {
			return nil, mapErr
		}
		result = append(result, item)
	}
	return result, nil
}

// CompleteBatchItem commits the item result and aggregate progress together while the lease is still valid.
func (repository *AdminJobRepository) CompleteBatchItem(
	ctx context.Context,
	item adminuser.BatchItem,
	nextState, errorMessageKey string,
	auditEventID uuid.UUID,
	completedAt time.Time,
) (adminuser.BatchItem, error) {
	if repository == nil || repository.runner == nil || ctx == nil || !validBatchCompletion(item, nextState, errorMessageKey, completedAt) {
		return adminuser.BatchItem{}, adminuser.ErrInvalidInput
	}
	var stored sqlcgen.AdminBatchJobItem
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		var err error
		stored, err = queries.CompleteAdminBatchJobItemCAS(ctx, sqlcgen.CompleteAdminBatchJobItemCASParams{
			NextState: nextState, ErrorMessageKey: optionalText(errorMessageKey), AuditEventID: optionalUUID(auditEventID),
			CompletedAt: timeToPG(completedAt), ItemID: uuidToPG(item.ID),
			ExpectedLeaseOwner: pgtype.Text{String: item.LeaseOwner, Valid: true}, ExpectedVersion: int64(item.Version),
		})
		if err != nil {
			return err
		}
		_, err = queries.ApplyAdminBatchJobItemCompletion(ctx, sqlcgen.ApplyAdminBatchJobItemCompletionParams{
			NextState: nextState, CompletedAt: timeToPG(completedAt), BatchJobID: stored.BatchJobID,
		})
		return err
	})
	if err != nil {
		return adminuser.BatchItem{}, mapAdminUserQueryError(ctx, err, adminuser.ErrConflict)
	}
	return adminBatchItemFromRow(stored)
}

// CancelBatchJob marks every still-queued item as canceled and leaves running items to close the job naturally.
func (repository *AdminJobRepository) CancelBatchJob(ctx context.Context, batchJobID uuid.UUID, expectedVersion uint64, changedAt time.Time) (adminuser.BatchJob, error) {
	if repository == nil || repository.queries == nil || ctx == nil || batchJobID == uuid.Nil || expectedVersion == 0 || changedAt.IsZero() {
		return adminuser.BatchJob{}, adminuser.ErrInvalidInput
	}
	row, err := repository.queries.CancelAdminBatchJobCAS(ctx, sqlcgen.CancelAdminBatchJobCASParams{
		ChangedAt: timeToPG(changedAt), BatchJobID: uuidToPG(batchJobID), ExpectedVersion: int64(expectedVersion),
	})
	if err != nil {
		return adminuser.BatchJob{}, mapAdminUserQueryError(ctx, err, adminuser.ErrConflict)
	}
	return adminBatchJobFromRow(row)
}

// RetryBatchJob requeues selected terminal items and reopens the parent aggregate for immediate processing.
func (repository *AdminJobRepository) RetryBatchJob(
	ctx context.Context,
	batchJobID uuid.UUID,
	itemIDs []uuid.UUID,
	expectedVersion uint64,
	changedAt time.Time,
) (adminuser.BatchJob, int64, error) {
	if repository == nil || repository.queries == nil || ctx == nil || batchJobID == uuid.Nil || expectedVersion == 0 || changedAt.IsZero() {
		return adminuser.BatchJob{}, 0, adminuser.ErrInvalidInput
	}
	pgItemIDs, ok := uniqueUUIDs(itemIDs)
	if !ok {
		return adminuser.BatchJob{}, 0, adminuser.ErrInvalidInput
	}
	before, err := repository.GetBatchJob(ctx, batchJobID)
	if err != nil {
		return adminuser.BatchJob{}, 0, err
	}
	row, err := repository.queries.RetryAdminBatchJobCAS(ctx, sqlcgen.RetryAdminBatchJobCASParams{
		ChangedAt: timeToPG(changedAt), BatchJobID: uuidToPG(batchJobID), ExpectedVersion: int64(expectedVersion), ItemIds: pgItemIDs,
	})
	if err != nil {
		return adminuser.BatchJob{}, 0, mapAdminUserQueryError(ctx, err, adminuser.ErrConflict)
	}
	job, err := adminBatchJobFromRow(row)
	if err != nil {
		return adminuser.BatchJob{}, 0, err
	}
	requeued := job.QueuedCount - before.QueuedCount
	if requeued < 0 {
		requeued = 0
	}
	return job, requeued, nil
}

// CreateErasureJob provides idempotent creation and rejects digest changes for the same operation ID.
func (repository *AdminJobRepository) CreateErasureJob(ctx context.Context, command adminuser.CreateErasureJobCommand) (adminuser.ErasureJob, error) {
	job := command.Job
	if repository == nil || repository.queries == nil || ctx == nil || !validNewErasureJob(job) {
		return adminuser.ErasureJob{}, adminuser.ErrInvalidInput
	}
	row, err := repository.queries.CreateAdminUserErasureJob(ctx, sqlcgen.CreateAdminUserErasureJobParams{
		ErasureJobID: uuidToPG(job.ID), UserID: uuidToPG(job.UserID), ActorAdminID: uuidToPG(job.ActorAdminID),
		OperationID: job.OperationID, RequestDigest: job.RequestDigest[:], Reason: strings.TrimSpace(job.Reason), CreatedAt: timeToPG(job.CreatedAt),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return adminuser.ErasureJob{}, adminuser.ErrIdempotencyConflict
	}
	if err != nil {
		return adminuser.ErasureJob{}, mapAdminUserQueryError(ctx, err, adminuser.ErrConflict)
	}
	return adminErasureJobFromRow(row)
}

// ClaimErasureJob leases one queued or abandoned workflow using database time.
func (repository *AdminJobRepository) ClaimErasureJob(ctx context.Context, owner string, lease time.Duration) (adminuser.ErasureJob, error) {
	if repository == nil || repository.queries == nil || ctx == nil || !validLease(owner, lease) {
		return adminuser.ErasureJob{}, adminuser.ErrInvalidInput
	}
	row, err := repository.queries.ClaimAdminUserErasureJob(ctx, sqlcgen.ClaimAdminUserErasureJobParams{
		LeaseOwner: pgtype.Text{String: owner, Valid: true}, LeaseSeconds: int64(lease / time.Second),
	})
	if err != nil {
		return adminuser.ErasureJob{}, mapAdminUserQueryError(ctx, err, adminuser.ErrNotFound)
	}
	return adminErasureJobFromRow(row)
}

// AdvanceErasureJob moves exactly one reviewed step while the caller still owns the live lease.
func (repository *AdminJobRepository) AdvanceErasureJob(
	ctx context.Context,
	job adminuser.ErasureJob,
	nextStep string,
	changedAt time.Time,
) (adminuser.ErasureJob, error) {
	if repository == nil || repository.queries == nil || ctx == nil || job.ID == uuid.Nil || job.State != "running" ||
		job.LeaseOwner == "" || job.Version == 0 || changedAt.IsZero() || !validErasureStepTransition(job.Step, nextStep) {
		return adminuser.ErasureJob{}, adminuser.ErrInvalidInput
	}
	row, err := repository.queries.AdvanceAdminUserErasureStepCAS(ctx, sqlcgen.AdvanceAdminUserErasureStepCASParams{
		NextStep: nextStep, ChangedAt: timeToPG(changedAt), ErasureJobID: uuidToPG(job.ID),
		ExpectedLeaseOwner: pgtype.Text{String: job.LeaseOwner, Valid: true}, ExpectedVersion: int64(job.Version),
	})
	if err != nil {
		return adminuser.ErasureJob{}, mapAdminUserQueryError(ctx, err, adminuser.ErrConflict)
	}
	return adminErasureJobFromRow(row)
}

// CompleteErasureJob permits only running-to-terminal transitions owned by the current lease holder.
func (repository *AdminJobRepository) CompleteErasureJob(
	ctx context.Context,
	job adminuser.ErasureJob,
	nextState, errorMessageKey string,
	completedAt time.Time,
) (adminuser.ErasureJob, error) {
	if repository == nil || repository.queries == nil || ctx == nil || job.ID == uuid.Nil || job.State != "running" || job.LeaseOwner == "" ||
		job.Version == 0 || completedAt.IsZero() || (nextState != "succeeded" && nextState != "failed") ||
		(nextState == "succeeded" && (job.Step != "complete" || errorMessageKey != "")) ||
		(nextState == "failed" && !validAdminErrorMessageKey(errorMessageKey)) {
		return adminuser.ErasureJob{}, adminuser.ErrInvalidInput
	}
	row, err := repository.queries.CompleteAdminUserErasureJobCAS(ctx, sqlcgen.CompleteAdminUserErasureJobCASParams{
		NextState: nextState, ErrorMessageKey: optionalText(errorMessageKey), CompletedAt: timeToPG(completedAt),
		ErasureJobID: uuidToPG(job.ID), ExpectedLeaseOwner: pgtype.Text{String: job.LeaseOwner, Valid: true}, ExpectedVersion: int64(job.Version),
	})
	if err != nil {
		return adminuser.ErasureJob{}, mapAdminUserQueryError(ctx, err, adminuser.ErrConflict)
	}
	return adminErasureJobFromRow(row)
}

func validBatchPreview(preview adminuser.BatchPreview) bool {
	return preview.ID != uuid.Nil && preview.ActorAdminID != uuid.Nil && validBatchCommand(preview.Command) &&
		preview.SelectionSchemaVersion > 0 && json.Valid(preview.SelectionSnapshot) && len(preview.SelectionSnapshot) <= 64*1024 &&
		preview.TargetCount >= 0 && preview.ExecutableCount >= 0 && preview.BlockedCount >= 0 &&
		preview.TargetCount == preview.ExecutableCount+preview.BlockedCount && !preview.SampledAt.IsZero() &&
		preview.ExpiresAt.After(preview.SampledAt) && preview.ExpiresAt.Sub(preview.SampledAt) <= 10*time.Minute
}

func validStartBatchJob(command adminuser.StartBatchJobCommand) bool {
	if command.BatchJobID == uuid.Nil || command.ActorAdminID == uuid.Nil || command.PreviewID == uuid.Nil ||
		command.ExpectedPreviewVersion == 0 || len(command.OperationID) == 0 || len(command.OperationID) > 128 ||
		!validAdminReason(command.Reason) || command.CreatedAt.IsZero() ||
		len(command.Targets) == 0 || len(command.Targets) > 100000 {
		return false
	}
	seenItems := make(map[uuid.UUID]struct{}, len(command.Targets))
	seenUsers := make(map[uuid.UUID]struct{}, len(command.Targets))
	for _, target := range command.Targets {
		if target.ItemID == uuid.Nil || target.UserID == uuid.Nil || target.ExpectedUserVersion == 0 {
			return false
		}
		if _, exists := seenItems[target.ItemID]; exists {
			return false
		}
		if _, exists := seenUsers[target.UserID]; exists {
			return false
		}
		seenItems[target.ItemID] = struct{}{}
		seenUsers[target.UserID] = struct{}{}
	}
	return true
}

func validBatchCommand(command string) bool {
	return command == "suspend" || command == "unsuspend" || command == "remove_from_current_room"
}

func validLease(owner string, lease time.Duration) bool {
	return strings.TrimSpace(owner) == owner && owner != "" && len(owner) <= 128 && lease >= adminJobMinimumLease && lease <= adminJobMaximumLease && lease%time.Second == 0
}

func validBatchCompletion(item adminuser.BatchItem, nextState, errorMessageKey string, completedAt time.Time) bool {
	if item.ID == uuid.Nil || item.BatchJobID == uuid.Nil || item.State != "running" || item.LeaseOwner == "" || item.Version == 0 || completedAt.IsZero() {
		return false
	}
	switch nextState {
	case "failed", "skipped":
		return validAdminErrorMessageKey(errorMessageKey)
	case "succeeded", "canceled":
		return errorMessageKey == ""
	default:
		return false
	}
}

func validNewErasureJob(job adminuser.ErasureJob) bool {
	return job.ID != uuid.Nil && job.UserID != uuid.Nil && job.ActorAdminID != uuid.Nil && len(job.OperationID) > 0 && len(job.OperationID) <= 128 &&
		validAdminReason(job.Reason) && !job.CreatedAt.IsZero() && (job.State == "" || job.State == "queued") && (job.Step == "" || job.Step == "queued")
}

func validErasureStepTransition(current, next string) bool {
	return current == "revoke_credentials" && next == "erase_profile" ||
		current == "erase_profile" && next == "enqueue_room_cleanup" ||
		current == "enqueue_room_cleanup" && next == "complete"
}

func adminBatchPreviewFromRow(row sqlcgen.AdminBatchPreview) (adminuser.BatchPreview, error) {
	selectionDigest, ok := bytesToDigest(row.SelectionDigest)
	if !ok {
		return adminuser.BatchPreview{}, adminuser.ErrIntegrity
	}
	previewDigest, ok := bytesToDigest(row.PreviewDigest)
	if !ok || !row.PreviewID.Valid || row.PreviewID.Bytes == uuid.Nil || !row.ActorAdminID.Valid || row.ActorAdminID.Bytes == uuid.Nil ||
		row.SelectionSchemaVersion <= 0 || !json.Valid(row.SelectionSnapshot) || row.Version <= 0 || !row.SampledAt.Valid || !row.ExpiresAt.Valid {
		return adminuser.BatchPreview{}, adminuser.ErrIntegrity
	}
	return adminuser.BatchPreview{
		ID: row.PreviewID.Bytes, ActorAdminID: row.ActorAdminID.Bytes, Command: row.Command,
		SelectionSchemaVersion: uint32(row.SelectionSchemaVersion), SelectionSnapshot: append([]byte(nil), row.SelectionSnapshot...),
		SelectionDigest: selectionDigest, PreviewDigest: previewDigest, TargetCount: row.TargetCount,
		ExecutableCount: row.ExecutableCount, BlockedCount: row.BlockedCount,
		SampledAt: canonicalPostgresTime(row.SampledAt), ExpiresAt: canonicalPostgresTime(row.ExpiresAt),
		ConsumedAt: canonicalPostgresTime(row.ConsumedAt), Version: uint64(row.Version),
	}, nil
}

func adminBatchJobFromRow(row sqlcgen.AdminBatchJob) (adminuser.BatchJob, error) {
	digest, ok := bytesToDigest(row.RequestDigest)
	if !ok || !row.BatchJobID.Valid || row.BatchJobID.Bytes == uuid.Nil || !row.ActorAdminID.Valid || row.ActorAdminID.Bytes == uuid.Nil ||
		!row.PreviewID.Valid || row.PreviewID.Bytes == uuid.Nil || row.Version <= 0 || !row.CreatedAt.Valid || !row.UpdatedAt.Valid {
		return adminuser.BatchJob{}, adminuser.ErrIntegrity
	}
	selectionDigest, ok := bytesToDigest(row.SelectionDigest)
	if !ok || row.SelectionSchemaVersion <= 0 || !json.Valid(row.SelectionSnapshot) || !validAdminReason(row.Reason) {
		return adminuser.BatchJob{}, adminuser.ErrIntegrity
	}
	return adminuser.BatchJob{
		ID: row.BatchJobID.Bytes, ActorAdminID: row.ActorAdminID.Bytes, OperationID: row.OperationID, RequestDigest: digest,
		PreviewID: row.PreviewID.Bytes, Command: row.Command, SelectionSchemaVersion: uint32(row.SelectionSchemaVersion),
		SelectionSnapshot: append([]byte(nil), row.SelectionSnapshot...), SelectionDigest: selectionDigest, Reason: row.Reason,
		State: row.State, TargetCount: row.TargetCount,
		QueuedCount: row.QueuedCount, RunningCount: row.RunningCount, SucceededCount: row.SucceededCount,
		FailedCount: row.FailedCount, SkippedCount: row.SkippedCount, CanceledCount: row.CanceledCount,
		ErrorMessageKey: row.ErrorMessageKey.String,
		Version:         uint64(row.Version), CreatedAt: canonicalPostgresTime(row.CreatedAt), StartedAt: canonicalPostgresTime(row.StartedAt),
		CompletedAt: canonicalPostgresTime(row.CompletedAt), UpdatedAt: canonicalPostgresTime(row.UpdatedAt),
	}, nil
}

func adminBatchItemFromRow(row sqlcgen.AdminBatchJobItem) (adminuser.BatchItem, error) {
	return adminBatchItemFromValues(
		row.ItemID, row.BatchJobID, row.UserID, row.ExpectedUserVersion, row.RequestDigest,
		row.State, row.AttemptCount, row.LeaseOwner, row.LeaseUntil, row.ErrorMessageKey, row.AuditEventID,
		row.StartedAt, row.CompletedAt, row.Version, row.CreatedAt, row.UpdatedAt,
	)
}

func adminBatchItemFromValues(
	itemID, batchJobID, userID pgtype.UUID,
	expectedUserVersion int64,
	requestDigest []byte,
	state string,
	attemptCount int32,
	leaseOwner pgtype.Text,
	leaseUntil pgtype.Timestamptz,
	errorMessageKey pgtype.Text,
	auditEventID pgtype.UUID,
	startedAt, completedAt pgtype.Timestamptz,
	version int64,
	createdAt, updatedAt pgtype.Timestamptz,
) (adminuser.BatchItem, error) {
	digest, ok := bytesToDigest(requestDigest)
	if !ok || !itemID.Valid || itemID.Bytes == uuid.Nil || !batchJobID.Valid || batchJobID.Bytes == uuid.Nil ||
		!userID.Valid || userID.Bytes == uuid.Nil || expectedUserVersion <= 0 || attemptCount < 0 || version <= 0 || !createdAt.Valid || !updatedAt.Valid {
		return adminuser.BatchItem{}, adminuser.ErrIntegrity
	}
	return adminuser.BatchItem{
		ID: itemID.Bytes, BatchJobID: batchJobID.Bytes, UserID: userID.Bytes, ExpectedUserVersion: uint64(expectedUserVersion),
		RequestDigest: digest, State: state, AttemptCount: uint32(attemptCount), LeaseOwner: leaseOwner.String,
		LeaseUntil: canonicalPostgresTime(leaseUntil), ErrorMessageKey: errorMessageKey.String, AuditEventID: auditEventID.Bytes,
		StartedAt: canonicalPostgresTime(startedAt), CompletedAt: canonicalPostgresTime(completedAt), Version: uint64(version),
		CreatedAt: canonicalPostgresTime(createdAt), UpdatedAt: canonicalPostgresTime(updatedAt),
	}, nil
}

func adminErasureJobFromRow(row sqlcgen.AdminUserErasureJob) (adminuser.ErasureJob, error) {
	digest, ok := bytesToDigest(row.RequestDigest)
	if !ok || !row.ErasureJobID.Valid || row.ErasureJobID.Bytes == uuid.Nil || !row.UserID.Valid || row.UserID.Bytes == uuid.Nil ||
		!row.ActorAdminID.Valid || row.ActorAdminID.Bytes == uuid.Nil || row.AttemptCount < 0 || row.Version <= 0 || !row.CreatedAt.Valid || !row.UpdatedAt.Valid {
		return adminuser.ErasureJob{}, adminuser.ErrIntegrity
	}
	return adminuser.ErasureJob{
		ID: row.ErasureJobID.Bytes, UserID: row.UserID.Bytes, ActorAdminID: row.ActorAdminID.Bytes,
		OperationID: row.OperationID, RequestDigest: digest, State: row.State, Step: row.Step, Reason: row.Reason,
		AttemptCount: uint32(row.AttemptCount), LeaseOwner: row.LeaseOwner.String, LeaseUntil: canonicalPostgresTime(row.LeaseUntil),
		ErrorMessageKey: row.ErrorMessageKey.String, Version: uint64(row.Version), CreatedAt: canonicalPostgresTime(row.CreatedAt), StartedAt: canonicalPostgresTime(row.StartedAt),
		CompletedAt: canonicalPostgresTime(row.CompletedAt), UpdatedAt: canonicalPostgresTime(row.UpdatedAt),
	}, nil
}

func bytesToDigest(value []byte) ([32]byte, bool) {
	var digest [32]byte
	if len(value) != len(digest) {
		return digest, false
	}
	copy(digest[:], value)
	return digest, true
}

func optionalText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

var _ adminuser.JobRepository = (*AdminJobRepository)(nil)
