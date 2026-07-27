package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/admin/operations"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AdminOperationsRepository maps the closed operations model to generated PostgreSQL queries.
type AdminOperationsRepository struct{ queries QueryHandle }

// NewAdminOperationsRepository binds operational state and overview queries to one pool.
func NewAdminOperationsRepository(pool *pgxpool.Pool) *AdminOperationsRepository {
	return &AdminOperationsRepository{queries: sqlcgen.New(pool)}
}

// GetMaintenanceState reads the singleton mutation-admission authority.
func (repository *AdminOperationsRepository) GetMaintenanceState(ctx context.Context) (operations.MaintenanceState, error) {
	if repository == nil || repository.queries == nil || ctx == nil {
		return operations.MaintenanceState{}, operations.ErrInvalidInput
	}
	row, err := repository.queries.GetAdminMaintenanceState(ctx)
	if err != nil {
		return operations.MaintenanceState{}, mapAdminOperationsError(ctx, err, operations.ErrNotFound)
	}
	return adminMaintenanceStateFromRow(row)
}

// UpdateMaintenanceState advances the singleton only when the expected version is current.
func (repository *AdminOperationsRepository) UpdateMaintenanceState(ctx context.Context, change operations.MaintenanceChange) (operations.MaintenanceState, error) {
	if repository == nil || repository.queries == nil || ctx == nil || !operations.ValidMaintenanceChange(change) {
		return operations.MaintenanceState{}, operations.ErrInvalidInput
	}
	row, err := repository.queries.UpdateAdminMaintenanceStateCAS(ctx, sqlcgen.UpdateAdminMaintenanceStateCASParams{
		Enabled: change.Enabled, Scope: string(change.Scope), Reason: change.Reason, PlannedEndAt: optionalTimeToPG(change.PlannedEndAt),
		ChangedByAdminID: uuidToPG(change.ChangedByAdminID), ChangedAt: timeToPG(change.ChangedAt), ExpectedVersion: int64(change.ExpectedVersion),
	})
	if err != nil {
		return operations.MaintenanceState{}, mapAdminOperationsError(ctx, err, operations.ErrConflict)
	}
	return adminMaintenanceStateFromRow(row)
}

// UpsertServiceInstance keeps the newest server-timestamped heartbeat for one process instance.
func (repository *AdminOperationsRepository) UpsertServiceInstance(ctx context.Context, instance operations.ServiceInstance) (operations.ServiceInstance, error) {
	if repository == nil || repository.queries == nil || ctx == nil || !operations.ValidServiceInstance(instance) {
		return operations.ServiceInstance{}, operations.ErrInvalidInput
	}
	components := make(map[string]string, len(instance.Components))
	for code, status := range instance.Components {
		components[code] = string(status)
	}
	encoded, err := json.Marshal(components)
	if err != nil {
		return operations.ServiceInstance{}, operations.ErrInvalidInput
	}
	row, err := repository.queries.UpsertAdminServiceInstance(ctx, sqlcgen.UpsertAdminServiceInstanceParams{
		ServiceKind: string(instance.Kind), InstanceID: instance.InstanceID, BuildVersion: instance.BuildVersion,
		StartedAt: timeToPG(instance.StartedAt), LastHeartbeatAt: timeToPG(instance.LastHeartbeatAt), Status: string(instance.Status),
		Components: encoded, MaintenanceVersion: int64(instance.MaintenanceVersion),
	})
	if err != nil {
		return operations.ServiceInstance{}, mapAdminOperationsError(ctx, err, operations.ErrConflict)
	}
	return adminServiceInstanceFromRow(row)
}

// ListServiceInstances returns a bounded deterministic instance list.
func (repository *AdminOperationsRepository) ListServiceInstances(ctx context.Context, limit uint32) ([]operations.ServiceInstance, error) {
	if repository == nil || repository.queries == nil || ctx == nil || limit == 0 || limit > operations.MaximumServiceInstances {
		return nil, operations.ErrInvalidInput
	}
	rows, err := repository.queries.ListAdminServiceInstances(ctx, sqlcgen.ListAdminServiceInstancesParams{PageSize: int32(limit)})
	if err != nil {
		return nil, mapAdminOperationsError(ctx, err, operations.ErrRepositoryUnavailable)
	}
	result := make([]operations.ServiceInstance, 0, len(rows))
	for _, row := range rows {
		instance, mapErr := adminServiceInstanceFromRow(row)
		if mapErr != nil {
			return nil, mapErr
		}
		result = append(result, instance)
	}
	return result, nil
}

// UpsertMetricBucket idempotently replaces a bucket only at an equal or newer source watermark.
func (repository *AdminOperationsRepository) UpsertMetricBucket(ctx context.Context, bucket operations.MetricBucket) (operations.MetricBucket, error) {
	if repository == nil || repository.queries == nil || ctx == nil || !operations.ValidMetricBucket(bucket) || bucket.Value > uint64(^uint64(0)>>1) || bucket.SourceWatermark > uint64(^uint64(0)>>1) {
		return operations.MetricBucket{}, operations.ErrInvalidInput
	}
	row, err := repository.queries.UpsertAdminMetricBucket(ctx, sqlcgen.UpsertAdminMetricBucketParams{
		MetricName: string(bucket.Name), BucketWidth: string(bucket.Width), BucketStart: timeToPG(bucket.Start),
		Value: int64(bucket.Value), SampledAt: timeToPG(bucket.SampledAt), SourceWatermark: int64(bucket.SourceWatermark),
	})
	if err != nil {
		return operations.MetricBucket{}, mapAdminOperationsError(ctx, err, operations.ErrConflict)
	}
	return adminMetricBucketFromRow(row)
}

// ListMetricBuckets returns only the requested fixed metrics inside one half-open time window.
func (repository *AdminOperationsRepository) ListMetricBuckets(ctx context.Context, query operations.MetricQuery) ([]operations.MetricBucket, error) {
	if repository == nil || repository.queries == nil || ctx == nil || len(query.Names) == 0 || len(query.Names) > 8 ||
		query.WindowStart.IsZero() || !query.WindowEnd.After(query.WindowStart) || query.Limit == 0 || query.Limit > operations.MaximumMetricBuckets {
		return nil, operations.ErrInvalidInput
	}
	if _, ok := query.Width.Duration(); !ok {
		return nil, operations.ErrInvalidInput
	}
	names := make([]string, 0, len(query.Names))
	seen := make(map[operations.MetricName]struct{}, len(query.Names))
	for _, name := range query.Names {
		if !name.Valid() {
			return nil, operations.ErrInvalidInput
		}
		if _, exists := seen[name]; exists {
			return nil, operations.ErrInvalidInput
		}
		seen[name] = struct{}{}
		names = append(names, string(name))
	}
	rows, err := repository.queries.ListAdminMetricBuckets(ctx, sqlcgen.ListAdminMetricBucketsParams{
		MetricNames: names, BucketWidth: string(query.Width), WindowStart: timeToPG(query.WindowStart),
		WindowEnd: timeToPG(query.WindowEnd), PageSize: int32(query.Limit),
	})
	if err != nil {
		return nil, mapAdminOperationsError(ctx, err, operations.ErrRepositoryUnavailable)
	}
	return adminMetricBucketsFromRows(rows)
}

// GetCacheGeneration reads one fixed projection generation.
func (repository *AdminOperationsRepository) GetCacheGeneration(ctx context.Context, namespace operations.CacheNamespace) (operations.CacheGeneration, error) {
	if repository == nil || repository.queries == nil || ctx == nil || !namespace.Valid() {
		return operations.CacheGeneration{}, operations.ErrInvalidInput
	}
	row, err := repository.queries.GetAdminCacheGeneration(ctx, sqlcgen.GetAdminCacheGenerationParams{Namespace: string(namespace)})
	if err != nil {
		return operations.CacheGeneration{}, mapAdminOperationsError(ctx, err, operations.ErrNotFound)
	}
	return adminCacheGenerationFromRow(row)
}

// AdvanceCacheGeneration increments one reviewed projection through CAS.
func (repository *AdminOperationsRepository) AdvanceCacheGeneration(ctx context.Context, namespace operations.CacheNamespace, expected uint64, actor uuid.UUID, changedAt time.Time) (operations.CacheGeneration, error) {
	if repository == nil || repository.queries == nil || ctx == nil || !namespace.Valid() || expected == 0 || expected > uint64(^uint64(0)>>1) || actor == uuid.Nil || changedAt.IsZero() {
		return operations.CacheGeneration{}, operations.ErrInvalidInput
	}
	row, err := repository.queries.IncrementAdminCacheGenerationCAS(ctx, sqlcgen.IncrementAdminCacheGenerationCASParams{
		UpdatedByAdminID: uuidToPG(actor), UpdatedAt: timeToPG(changedAt), Namespace: string(namespace), ExpectedGeneration: int64(expected),
	})
	if err != nil {
		return operations.CacheGeneration{}, mapAdminOperationsError(ctx, err, operations.ErrConflict)
	}
	return adminCacheGenerationFromRow(row)
}

// CreateCommandPreview persists one server-generated, actor-bound preview without plaintext reason content.
func (repository *AdminOperationsRepository) CreateCommandPreview(ctx context.Context, preview operations.CommandPreview) (operations.CommandPreview, error) {
	if repository == nil || repository.queries == nil || ctx == nil || !validOperationsPreview(preview) {
		return operations.CommandPreview{}, operations.ErrInvalidInput
	}
	row, err := repository.queries.CreateAdminOperationsPreview(ctx, sqlcgen.CreateAdminOperationsPreviewParams{
		PreviewDigest: preview.Digest[:], ActorAdminID: uuidToPG(preview.ActorAdminID), CommandKind: string(preview.Kind),
		ReasonDigest: preview.ReasonDigest[:], ExpectedVersion: int64(preview.ExpectedVersion),
		MaintenanceEnabled: optionalBool(preview.MaintenanceEnabled), MaintenancePlannedEndAt: optionalTimeToPG(preview.MaintenancePlannedEndAt),
		CacheNamespace: optionalText(string(preview.CacheNamespace)), TaskKind: optionalText(string(preview.TaskKind)),
		TaskID: optionalUUIDToPG(preview.TaskID), SampledAt: timeToPG(preview.SampledAt), ExpiresAt: timeToPG(preview.ExpiresAt),
	})
	if err != nil {
		return operations.CommandPreview{}, mapAdminOperationsError(ctx, err, operations.ErrConflict)
	}
	return adminOperationsPreviewFromRow(row)
}

// GetCommandPreview restores one preview only for its authenticated administrator.
func (repository *AdminOperationsRepository) GetCommandPreview(ctx context.Context, actorID uuid.UUID, digest [sha256.Size]byte) (operations.CommandPreview, error) {
	if repository == nil || repository.queries == nil || ctx == nil || actorID == uuid.Nil || digest == [sha256.Size]byte{} {
		return operations.CommandPreview{}, operations.ErrInvalidInput
	}
	row, err := repository.queries.GetAdminOperationsPreview(ctx, sqlcgen.GetAdminOperationsPreviewParams{PreviewDigest: digest[:], ActorAdminID: uuidToPG(actorID)})
	if err != nil {
		return operations.CommandPreview{}, mapAdminOperationsError(ctx, err, operations.ErrNotFound)
	}
	return adminOperationsPreviewFromRow(row)
}

// ConsumeCommandPreview prevents the same reviewed snapshot from authorizing a second operation ID.
func (repository *AdminOperationsRepository) ConsumeCommandPreview(ctx context.Context, actorID uuid.UUID, digest [sha256.Size]byte, expectedVersion uint64, consumedAt time.Time) (operations.CommandPreview, error) {
	if repository == nil || repository.queries == nil || ctx == nil || actorID == uuid.Nil || digest == [sha256.Size]byte{} || expectedVersion == 0 || consumedAt.IsZero() {
		return operations.CommandPreview{}, operations.ErrInvalidInput
	}
	row, err := repository.queries.ConsumeAdminOperationsPreviewCAS(ctx, sqlcgen.ConsumeAdminOperationsPreviewCASParams{
		ConsumedAt: timeToPG(consumedAt), PreviewDigest: digest[:], ActorAdminID: uuidToPG(actorID), ExpectedVersion: int64(expectedVersion),
	})
	if err != nil {
		return operations.CommandPreview{}, mapAdminOperationsError(ctx, err, operations.ErrPreviewExpired)
	}
	return adminOperationsPreviewFromRow(row)
}

// GetCommandReceipt restores the first maintenance/cache result for operation-ID replay.
func (repository *AdminOperationsRepository) GetCommandReceipt(ctx context.Context, actorID uuid.UUID, operationID string) (operations.CommandReceipt, error) {
	if repository == nil || repository.queries == nil || ctx == nil || actorID == uuid.Nil || !validOperationsIdentifier(operationID, 128) {
		return operations.CommandReceipt{}, operations.ErrInvalidInput
	}
	row, err := repository.queries.GetAdminOperationsCommandReceipt(ctx, sqlcgen.GetAdminOperationsCommandReceiptParams{ActorAdminID: uuidToPG(actorID), OperationID: operationID})
	if err != nil {
		return operations.CommandReceipt{}, mapAdminOperationsError(ctx, err, operations.ErrNotFound)
	}
	return adminOperationsCommandReceiptFromRow(row)
}

// CreateCommandReceipt inserts an immutable binding or restores the row when the digest matches.
func (repository *AdminOperationsRepository) CreateCommandReceipt(ctx context.Context, receipt operations.CommandReceipt) (operations.CommandReceipt, error) {
	if repository == nil || repository.queries == nil || ctx == nil || !validOperationsCommandReceipt(receipt) {
		return operations.CommandReceipt{}, operations.ErrInvalidInput
	}
	row, err := repository.queries.CreateAdminOperationsCommandReceipt(ctx, sqlcgen.CreateAdminOperationsCommandReceiptParams{
		ActorAdminID: uuidToPG(receipt.ActorAdminID), OperationID: receipt.OperationID, RequestDigest: receipt.RequestDigest[:],
		CommandKind: string(receipt.Kind), Target: receipt.Target, Outcome: string(receipt.Outcome), PreviousVersion: int64(receipt.PreviousVersion),
		CurrentVersion: int64(receipt.CurrentVersion), MaintenanceEnabled: optionalBool(receipt.MaintenanceEnabled),
		MaintenanceReason: receipt.MaintenanceReason, MaintenancePlannedEndAt: optionalTimeToPG(receipt.MaintenancePlannedEndAt),
		MaintenanceChangedByAdminID: optionalUUIDToPG(receipt.MaintenanceChangedByAdminID), MaintenanceChangedAt: optionalTimeToPG(receipt.MaintenanceChangedAt),
		AuditEventID: uuidToPG(receipt.AuditEventID), CompletedAt: timeToPG(receipt.CompletedAt),
	})
	if err != nil {
		return operations.CommandReceipt{}, mapAdminOperationsError(ctx, err, operations.ErrConflict)
	}
	return adminOperationsCommandReceiptFromRow(row)
}

// GetRetryReceipt restores the first result for one administrator operation ID.
func (repository *AdminOperationsRepository) GetRetryReceipt(ctx context.Context, actor uuid.UUID, operationID string) (operations.RetryReceipt, error) {
	if repository == nil || repository.queries == nil || ctx == nil || actor == uuid.Nil || !validOperationsIdentifier(operationID, 128) {
		return operations.RetryReceipt{}, operations.ErrInvalidInput
	}
	row, err := repository.queries.GetAdminOperationsRetryReceipt(ctx, sqlcgen.GetAdminOperationsRetryReceiptParams{ActorAdminID: uuidToPG(actor), OperationID: operationID})
	if err != nil {
		return operations.RetryReceipt{}, mapAdminOperationsError(ctx, err, operations.ErrNotFound)
	}
	return adminRetryReceiptFromRow(row)
}

// GetRetryTask reads one redacted durable task without accepting an arbitrary table or query selector.
func (repository *AdminOperationsRepository) GetRetryTask(ctx context.Context, kind operations.RetryTaskKind, taskID uuid.UUID) (operations.RetryTask, error) {
	return repository.getRetryTask(ctx, kind, taskID, false)
}

// GetRetryTaskForUpdate serializes the retry-count check and state transition in a command transaction.
func (repository *AdminOperationsRepository) GetRetryTaskForUpdate(ctx context.Context, kind operations.RetryTaskKind, taskID uuid.UUID) (operations.RetryTask, error) {
	return repository.getRetryTask(ctx, kind, taskID, true)
}

func (repository *AdminOperationsRepository) getRetryTask(ctx context.Context, kind operations.RetryTaskKind, taskID uuid.UUID, forUpdate bool) (operations.RetryTask, error) {
	if repository == nil || repository.queries == nil || ctx == nil || !kind.Valid() || taskID == uuid.Nil {
		return operations.RetryTask{}, operations.ErrInvalidInput
	}
	switch kind {
	case operations.RetryUserBatch:
		if forUpdate {
			row, err := repository.queries.GetAdminOperationsBatchTaskForUpdate(ctx, sqlcgen.GetAdminOperationsBatchTaskForUpdateParams{TaskID: uuidToPG(taskID)})
			if err != nil {
				return operations.RetryTask{}, mapAdminOperationsError(ctx, err, operations.ErrNotFound)
			}
			return retryTaskFromValues(kind, row.TaskID, row.State, row.StableErrorCode, 0, row.Version, row.UpdatedAt)
		}
		row, err := repository.queries.GetAdminOperationsBatchTask(ctx, sqlcgen.GetAdminOperationsBatchTaskParams{TaskID: uuidToPG(taskID)})
		if err != nil {
			return operations.RetryTask{}, mapAdminOperationsError(ctx, err, operations.ErrNotFound)
		}
		return retryTaskFromValues(kind, row.TaskID, row.State, row.StableErrorCode, 0, row.Version, row.UpdatedAt)
	case operations.RetryUserErasure:
		if forUpdate {
			row, err := repository.queries.GetAdminOperationsErasureTaskForUpdate(ctx, sqlcgen.GetAdminOperationsErasureTaskForUpdateParams{TaskID: uuidToPG(taskID)})
			if err != nil {
				return operations.RetryTask{}, mapAdminOperationsError(ctx, err, operations.ErrNotFound)
			}
			return retryTaskFromValues(kind, row.TaskID, row.State, row.StableErrorCode, row.AttemptCount, row.Version, row.UpdatedAt)
		}
		row, err := repository.queries.GetAdminOperationsErasureTask(ctx, sqlcgen.GetAdminOperationsErasureTaskParams{TaskID: uuidToPG(taskID)})
		if err != nil {
			return operations.RetryTask{}, mapAdminOperationsError(ctx, err, operations.ErrNotFound)
		}
		return retryTaskFromValues(kind, row.TaskID, row.State, row.StableErrorCode, row.AttemptCount, row.Version, row.UpdatedAt)
	default:
		return operations.RetryTask{}, operations.ErrInvalidInput
	}
}

// CountTaskRetries returns the committed manual-retry count used by the fixed cap.
func (repository *AdminOperationsRepository) CountTaskRetries(ctx context.Context, kind operations.RetryTaskKind, taskID uuid.UUID) (uint32, error) {
	if repository == nil || repository.queries == nil || ctx == nil || !kind.Valid() || taskID == uuid.Nil {
		return 0, operations.ErrInvalidInput
	}
	count, err := repository.queries.CountAdminOperationsTaskRetries(ctx, sqlcgen.CountAdminOperationsTaskRetriesParams{TaskKind: string(kind), TaskID: uuidToPG(taskID)})
	if err != nil {
		return 0, mapAdminOperationsError(ctx, err, operations.ErrRepositoryUnavailable)
	}
	if count < 0 || count > int64(^uint32(0)) {
		return 0, operations.ErrIntegrity
	}
	return uint32(count), nil
}

// RetryTask requeues only a failed batch or erasure job through expected-version CAS.
func (repository *AdminOperationsRepository) RetryTask(ctx context.Context, kind operations.RetryTaskKind, taskID uuid.UUID, expectedVersion uint64, changedAt time.Time) (operations.RetryTask, error) {
	if repository == nil || repository.queries == nil || ctx == nil || !kind.Valid() || taskID == uuid.Nil || expectedVersion == 0 || changedAt.IsZero() {
		return operations.RetryTask{}, operations.ErrInvalidInput
	}
	switch kind {
	case operations.RetryUserBatch:
		row, err := repository.queries.RetryAdminOperationsBatchTaskCAS(ctx, sqlcgen.RetryAdminOperationsBatchTaskCASParams{
			ChangedAt: timeToPG(changedAt), TaskID: uuidToPG(taskID), ExpectedVersion: int64(expectedVersion),
		})
		if err != nil {
			return operations.RetryTask{}, mapAdminOperationsError(ctx, err, operations.ErrConflict)
		}
		return retryTaskFromValues(kind, row.BatchJobID, row.State, row.ErrorMessageKey, 0, row.Version, row.UpdatedAt)
	case operations.RetryUserErasure:
		row, err := repository.queries.RetryAdminOperationsErasureTaskCAS(ctx, sqlcgen.RetryAdminOperationsErasureTaskCASParams{
			ChangedAt: timeToPG(changedAt), TaskID: uuidToPG(taskID), ExpectedVersion: int64(expectedVersion),
		})
		if err != nil {
			return operations.RetryTask{}, mapAdminOperationsError(ctx, err, operations.ErrConflict)
		}
		return retryTaskFromValues(kind, row.ErasureJobID, row.State, row.ErrorMessageKey, row.AttemptCount, row.Version, row.UpdatedAt)
	default:
		return operations.RetryTask{}, operations.ErrInvalidInput
	}
}

// CreateRetryReceipt inserts one immutable idempotency binding or restores the same digest.
func (repository *AdminOperationsRepository) CreateRetryReceipt(ctx context.Context, receipt operations.RetryReceipt) (operations.RetryReceipt, error) {
	if repository == nil || repository.queries == nil || ctx == nil || !validRetryReceipt(receipt) {
		return operations.RetryReceipt{}, operations.ErrInvalidInput
	}
	row, err := repository.queries.CreateAdminOperationsRetryReceipt(ctx, sqlcgen.CreateAdminOperationsRetryReceiptParams{
		ActorAdminID: uuidToPG(receipt.ActorAdminID), OperationID: receipt.OperationID, RequestDigest: receipt.RequestDigest[:],
		TaskKind: string(receipt.TaskKind), TaskID: uuidToPG(receipt.TaskID), ExpectedTaskVersion: int64(receipt.ExpectedTaskVersion),
		Outcome: receipt.Outcome, TaskVersion: int64(receipt.TaskVersion), ManualRetryCount: int32(receipt.ManualRetryCount),
		TaskState: receipt.TaskState, OriginalErrorCode: receipt.OriginalErrorCode,
		AuditEventID: uuidToPG(receipt.AuditEventID), CompletedAt: timeToPG(receipt.CompletedAt),
	})
	if err != nil {
		return operations.RetryReceipt{}, mapAdminOperationsError(ctx, err, operations.ErrConflict)
	}
	return adminRetryReceiptFromRow(row)
}

// ListBacklogs samples five real durable mechanisms at one caller-supplied database-aligned time.
func (repository *AdminOperationsRepository) ListBacklogs(ctx context.Context, sampledAt time.Time) ([]operations.BacklogSummary, error) {
	if repository == nil || repository.queries == nil || ctx == nil || sampledAt.IsZero() {
		return nil, operations.ErrInvalidInput
	}
	rows, err := repository.queries.ListAdminOperationsBacklogs(ctx, sqlcgen.ListAdminOperationsBacklogsParams{SampledAt: timeToPG(sampledAt)})
	if err != nil {
		return nil, mapAdminOperationsError(ctx, err, operations.ErrRepositoryUnavailable)
	}
	result := make([]operations.BacklogSummary, 0, len(rows))
	for _, row := range rows {
		kind := operations.BacklogKind(row.BacklogKind)
		if !kind.Valid() || row.PendingCount < 0 || row.RunningCount < 0 || row.FailedCount < 0 {
			return nil, operations.ErrIntegrity
		}
		result = append(result, operations.BacklogSummary{
			Kind: kind, Pending: uint64(row.PendingCount), Running: uint64(row.RunningCount), Failed: uint64(row.FailedCount),
			OldestPendingAt: canonicalPostgresTime(row.OldestPendingAt), SampledAt: sampledAt.UTC(),
		})
	}
	return result, nil
}

// GetOverviewCounts reads current authoritative room, game, and account values for one window.
func (repository *AdminOperationsRepository) GetOverviewCounts(ctx context.Context, start, end, sampledAt time.Time) (operations.OverviewCounts, error) {
	if repository == nil || repository.queries == nil || ctx == nil || start.IsZero() || !end.After(start) || sampledAt.IsZero() {
		return operations.OverviewCounts{}, operations.ErrInvalidInput
	}
	row, err := repository.queries.GetAdminOverviewCurrentCounts(ctx, sqlcgen.GetAdminOverviewCurrentCountsParams{WindowStart: timeToPG(start), WindowEnd: timeToPG(end)})
	if err != nil {
		return operations.OverviewCounts{}, mapAdminOperationsError(ctx, err, operations.ErrRepositoryUnavailable)
	}
	if row.ActiveRooms < 0 || row.RunningGames < 0 || row.NewUsers < 0 || row.SuspendedUsers < 0 ||
		row.UnsuspendedUsers < 0 || row.AbnormalTerminations < 0 || row.EmergencyRepairs < 0 {
		return operations.OverviewCounts{}, operations.ErrIntegrity
	}
	return operations.OverviewCounts{
		ActiveRooms: uint64(row.ActiveRooms), RunningGames: uint64(row.RunningGames), NewUsers: uint64(row.NewUsers),
		SuspendedUsers: uint64(row.SuspendedUsers), UnsuspendedUsers: uint64(row.UnsuspendedUsers),
		AbnormalTerminations: uint64(row.AbnormalTerminations), EmergencyRepairs: uint64(row.EmergencyRepairs),
		WindowStart: start.UTC(), WindowEnd: end.UTC(), SampledAt: sampledAt.UTC(),
	}, nil
}

// ListAttentionItems returns bounded room/game link anomalies without exposing snapshots or player data.
func (repository *AdminOperationsRepository) ListAttentionItems(ctx context.Context, limit uint32) ([]operations.AttentionItem, error) {
	if repository == nil || repository.queries == nil || ctx == nil || limit == 0 || limit > operations.MaximumOverviewAttentionItems {
		return nil, operations.ErrInvalidInput
	}
	rows, err := repository.queries.ListAdminOverviewAttentionItems(ctx, sqlcgen.ListAdminOverviewAttentionItemsParams{PageSize: int32(limit)})
	if err != nil {
		return nil, mapAdminOperationsError(ctx, err, operations.ErrRepositoryUnavailable)
	}
	result := make([]operations.AttentionItem, 0, len(rows))
	for _, row := range rows {
		item := operations.AttentionItem{
			Kind: operations.AttentionKind(row.AttentionKind), ResourceID: row.ResourceID.Bytes, RoomID: row.RoomID.Bytes,
			StatusCode: row.StatusCode, ReasonCodes: append([]string(nil), row.ReasonCodes...), ObservedAt: canonicalPostgresTime(row.ObservedAt),
		}
		if !item.Kind.Valid() || !row.ResourceID.Valid || item.ResourceID == uuid.Nil || !row.RoomID.Valid || item.RoomID == uuid.Nil ||
			strings.TrimSpace(item.StatusCode) != item.StatusCode || item.StatusCode == "" || !row.ObservedAt.Valid || len(item.ReasonCodes) == 0 || len(item.ReasonCodes) > 8 {
			return nil, operations.ErrIntegrity
		}
		for _, reason := range item.ReasonCodes {
			if !validStableErrorCode(reason) {
				return nil, operations.ErrIntegrity
			}
		}
		result = append(result, item)
	}
	return result, nil
}

// ListFailedTasks merges two bounded real job tables into stable newest-first attention rows.
func (repository *AdminOperationsRepository) ListFailedTasks(ctx context.Context, limit uint32) ([]operations.FailedTask, error) {
	if repository == nil || repository.queries == nil || ctx == nil || limit == 0 || limit > operations.MaximumOverviewFailedTasks {
		return nil, operations.ErrInvalidInput
	}
	batches, err := repository.queries.ListAdminOverviewFailedBatchJobs(ctx, sqlcgen.ListAdminOverviewFailedBatchJobsParams{PageSize: int32(limit)})
	if err != nil {
		return nil, mapAdminOperationsError(ctx, err, operations.ErrRepositoryUnavailable)
	}
	erasures, err := repository.queries.ListAdminOverviewFailedErasureJobs(ctx, sqlcgen.ListAdminOverviewFailedErasureJobsParams{PageSize: int32(limit)})
	if err != nil {
		return nil, mapAdminOperationsError(ctx, err, operations.ErrRepositoryUnavailable)
	}
	result := make([]operations.FailedTask, 0, len(batches)+len(erasures))
	for _, row := range batches {
		if !row.TaskID.Valid || row.TaskID.Bytes == uuid.Nil || row.Version <= 0 || !row.UpdatedAt.Valid || !validStableErrorCode(row.StableErrorCode) {
			return nil, operations.ErrIntegrity
		}
		result = append(result, operations.FailedTask{Kind: operations.RetryUserBatch, ID: row.TaskID.Bytes, State: row.State, StableErrorCode: row.StableErrorCode, Version: uint64(row.Version), UpdatedAt: canonicalPostgresTime(row.UpdatedAt)})
	}
	for _, row := range erasures {
		if !row.TaskID.Valid || row.TaskID.Bytes == uuid.Nil || row.AttemptCount < 0 || row.Version <= 0 || !row.UpdatedAt.Valid || !validStableErrorCode(row.StableErrorCode) {
			return nil, operations.ErrIntegrity
		}
		result = append(result, operations.FailedTask{Kind: operations.RetryUserErasure, ID: row.TaskID.Bytes, State: row.State, StableErrorCode: row.StableErrorCode, Attempts: uint32(row.AttemptCount), Version: uint64(row.Version), UpdatedAt: canonicalPostgresTime(row.UpdatedAt)})
	}
	sort.Slice(result, func(left, right int) bool {
		if !result[left].UpdatedAt.Equal(result[right].UpdatedAt) {
			return result[left].UpdatedAt.After(result[right].UpdatedAt)
		}
		if result[left].Kind != result[right].Kind {
			return result[left].Kind < result[right].Kind
		}
		return result[left].ID.String() < result[right].ID.String()
	})
	if len(result) > int(limit) {
		result = result[:limit]
	}
	return result, nil
}

func adminMaintenanceStateFromRow(row sqlcgen.AdminMaintenanceState) (operations.MaintenanceState, error) {
	state := operations.MaintenanceState{
		Enabled: row.Enabled, Scope: operations.MaintenanceScope(row.Scope), Reason: row.Reason,
		PlannedEndAt: canonicalPostgresTime(row.PlannedEndAt), Version: uint64(row.Version),
		ChangedByAdminID: row.ChangedByAdminID.Bytes, ChangedAt: canonicalPostgresTime(row.ChangedAt),
	}
	if row.SingletonID != 1 || state.Scope != operations.MaintenanceUserMutations || row.Version <= 0 || !row.ChangedAt.Valid ||
		(row.Enabled && strings.TrimSpace(row.Reason) == "") || (row.PlannedEndAt.Valid && !state.PlannedEndAt.After(state.ChangedAt)) {
		return operations.MaintenanceState{}, operations.ErrIntegrity
	}
	return state, nil
}

func adminServiceInstanceFromRow(row sqlcgen.AdminServiceInstance) (operations.ServiceInstance, error) {
	encoded := make(map[string]string)
	if !json.Valid(row.Components) || json.Unmarshal(row.Components, &encoded) != nil {
		return operations.ServiceInstance{}, operations.ErrIntegrity
	}
	components := make(map[string]operations.HealthStatus, len(encoded))
	for code, status := range encoded {
		components[code] = operations.HealthStatus(status)
	}
	instance := operations.ServiceInstance{
		Kind: operations.ServiceKind(row.ServiceKind), InstanceID: row.InstanceID, BuildVersion: row.BuildVersion,
		StartedAt: canonicalPostgresTime(row.StartedAt), LastHeartbeatAt: canonicalPostgresTime(row.LastHeartbeatAt),
		Status: operations.HealthStatus(row.Status), Components: components, MaintenanceVersion: uint64(row.MaintenanceVersion),
	}
	if !row.StartedAt.Valid || !row.LastHeartbeatAt.Valid || row.MaintenanceVersion <= 0 || !operations.ValidServiceInstance(instance) {
		return operations.ServiceInstance{}, operations.ErrIntegrity
	}
	return instance, nil
}

func adminMetricBucketFromRow(row sqlcgen.AdminMetricBucket) (operations.MetricBucket, error) {
	if row.Value < 0 || row.SourceWatermark < 0 || !row.BucketStart.Valid || !row.SampledAt.Valid {
		return operations.MetricBucket{}, operations.ErrIntegrity
	}
	bucket := operations.MetricBucket{
		Name: operations.MetricName(row.MetricName), Width: operations.BucketWidth(row.BucketWidth), Start: canonicalPostgresTime(row.BucketStart),
		Value: uint64(row.Value), SampledAt: canonicalPostgresTime(row.SampledAt), SourceWatermark: uint64(row.SourceWatermark),
	}
	if !operations.ValidMetricBucket(bucket) {
		return operations.MetricBucket{}, operations.ErrIntegrity
	}
	return bucket, nil
}

func adminMetricBucketsFromRows(rows []sqlcgen.AdminMetricBucket) ([]operations.MetricBucket, error) {
	result := make([]operations.MetricBucket, 0, len(rows))
	for _, row := range rows {
		bucket, err := adminMetricBucketFromRow(row)
		if err != nil {
			return nil, err
		}
		result = append(result, bucket)
	}
	return result, nil
}

func adminCacheGenerationFromRow(row sqlcgen.AdminCacheGeneration) (operations.CacheGeneration, error) {
	result := operations.CacheGeneration{Namespace: operations.CacheNamespace(row.Namespace), Generation: uint64(row.Generation), UpdatedByAdminID: row.UpdatedByAdminID.Bytes, UpdatedAt: canonicalPostgresTime(row.UpdatedAt)}
	if !result.Namespace.Valid() || row.Generation <= 0 || !row.UpdatedAt.Valid {
		return operations.CacheGeneration{}, operations.ErrIntegrity
	}
	return result, nil
}

func adminOperationsPreviewFromRow(row sqlcgen.AdminOperationsPreview) (operations.CommandPreview, error) {
	digest, digestOK := bytesToDigest(row.PreviewDigest)
	reason, reasonOK := bytesToDigest(row.ReasonDigest)
	if !digestOK || !reasonOK || !row.ActorAdminID.Valid || row.ExpectedVersion <= 0 || !row.SampledAt.Valid || !row.ExpiresAt.Valid || row.Version <= 0 {
		return operations.CommandPreview{}, operations.ErrIntegrity
	}
	preview := operations.CommandPreview{
		Digest: digest, ActorAdminID: row.ActorAdminID.Bytes, Kind: operations.CommandKind(row.CommandKind), ReasonDigest: reason,
		ExpectedVersion: uint64(row.ExpectedVersion), MaintenancePlannedEndAt: canonicalPostgresTime(row.MaintenancePlannedEndAt),
		CacheNamespace: operations.CacheNamespace(row.CacheNamespace.String), TaskKind: operations.RetryTaskKind(row.TaskKind.String),
		TaskID: row.TaskID.Bytes, SampledAt: canonicalPostgresTime(row.SampledAt), ExpiresAt: canonicalPostgresTime(row.ExpiresAt),
		ConsumedAt: canonicalPostgresTime(row.ConsumedAt), Version: uint64(row.Version),
	}
	if row.MaintenanceEnabled.Valid {
		preview.MaintenanceEnabled = boolPointerPG(row.MaintenanceEnabled.Bool)
	}
	if !validOperationsPreview(preview) {
		return operations.CommandPreview{}, operations.ErrIntegrity
	}
	return preview, nil
}

func adminOperationsCommandReceiptFromRow(row sqlcgen.AdminOperationsCommandReceipt) (operations.CommandReceipt, error) {
	digest, ok := bytesToDigest(row.RequestDigest)
	if !ok || !row.ActorAdminID.Valid || !row.AuditEventID.Valid || row.PreviousVersion <= 0 || row.CurrentVersion <= 0 || !row.CompletedAt.Valid {
		return operations.CommandReceipt{}, operations.ErrIntegrity
	}
	receipt := operations.CommandReceipt{
		ActorAdminID: row.ActorAdminID.Bytes, OperationID: row.OperationID, RequestDigest: digest, Kind: operations.CommandKind(row.CommandKind),
		Target: row.Target, Outcome: operations.CommandOutcome(row.Outcome), PreviousVersion: uint64(row.PreviousVersion), CurrentVersion: uint64(row.CurrentVersion),
		MaintenanceReason: row.MaintenanceReason, MaintenancePlannedEndAt: canonicalPostgresTime(row.MaintenancePlannedEndAt),
		MaintenanceChangedByAdminID: row.MaintenanceChangedByAdminID.Bytes, MaintenanceChangedAt: canonicalPostgresTime(row.MaintenanceChangedAt),
		AuditEventID: row.AuditEventID.Bytes, CompletedAt: canonicalPostgresTime(row.CompletedAt),
	}
	if row.MaintenanceEnabled.Valid {
		receipt.MaintenanceEnabled = boolPointerPG(row.MaintenanceEnabled.Bool)
	}
	if !validOperationsCommandReceipt(receipt) {
		return operations.CommandReceipt{}, operations.ErrIntegrity
	}
	return receipt, nil
}

func adminRetryReceiptFromRow(row sqlcgen.AdminOperationsRetryReceipt) (operations.RetryReceipt, error) {
	digest, ok := bytesToDigest(row.RequestDigest)
	if !ok || !row.ActorAdminID.Valid || !row.TaskID.Valid || !row.AuditEventID.Valid || row.ExpectedTaskVersion <= 0 || row.TaskVersion <= 0 || row.ManualRetryCount <= 0 || !row.CompletedAt.Valid {
		return operations.RetryReceipt{}, operations.ErrIntegrity
	}
	result := operations.RetryReceipt{
		ActorAdminID: row.ActorAdminID.Bytes, OperationID: row.OperationID, RequestDigest: digest, TaskKind: operations.RetryTaskKind(row.TaskKind),
		TaskID: row.TaskID.Bytes, ExpectedTaskVersion: uint64(row.ExpectedTaskVersion), Outcome: row.Outcome, TaskVersion: uint64(row.TaskVersion),
		ManualRetryCount: uint32(row.ManualRetryCount), TaskState: row.TaskState, OriginalErrorCode: row.OriginalErrorCode,
		AuditEventID: row.AuditEventID.Bytes, CompletedAt: canonicalPostgresTime(row.CompletedAt),
	}
	if !validRetryReceipt(result) {
		return operations.RetryReceipt{}, operations.ErrIntegrity
	}
	return result, nil
}

func validRetryReceipt(receipt operations.RetryReceipt) bool {
	return receipt.ActorAdminID != uuid.Nil && validOperationsIdentifier(receipt.OperationID, 128) && receipt.TaskKind.Valid() && receipt.TaskID != uuid.Nil &&
		receipt.ExpectedTaskVersion > 0 && (receipt.Outcome == "applied" || receipt.Outcome == "no_change" || receipt.Outcome == "rejected") &&
		receipt.TaskVersion > 0 && receipt.ManualRetryCount > 0 && receipt.ManualRetryCount <= operations.MaximumManualTaskRetries && receipt.TaskState == "queued" &&
		validStableErrorCode(receipt.OriginalErrorCode) && receipt.AuditEventID != uuid.Nil && !receipt.CompletedAt.IsZero()
}

func validOperationsPreview(preview operations.CommandPreview) bool {
	if preview.Digest == [sha256.Size]byte{} || preview.ReasonDigest == [sha256.Size]byte{} || preview.ActorAdminID == uuid.Nil ||
		preview.ExpectedVersion == 0 || preview.SampledAt.IsZero() || !preview.ExpiresAt.After(preview.SampledAt) ||
		preview.ExpiresAt.Sub(preview.SampledAt) > operations.CommandPreviewTTL || preview.Version == 0 ||
		(!preview.ConsumedAt.IsZero() && preview.ConsumedAt.Before(preview.SampledAt)) {
		return false
	}
	switch preview.Kind {
	case operations.CommandMaintenanceChange:
		return preview.MaintenanceEnabled != nil && preview.CacheNamespace == "" && preview.TaskKind == "" && preview.TaskID == uuid.Nil
	case operations.CommandCacheRefresh:
		return preview.MaintenanceEnabled == nil && preview.MaintenancePlannedEndAt.IsZero() && preview.CacheNamespace.Valid() && preview.TaskKind == "" && preview.TaskID == uuid.Nil
	case operations.CommandTaskRetry:
		return preview.MaintenanceEnabled == nil && preview.MaintenancePlannedEndAt.IsZero() && preview.CacheNamespace == "" && preview.TaskKind.Valid() && preview.TaskID != uuid.Nil
	default:
		return false
	}
}

func validOperationsCommandReceipt(receipt operations.CommandReceipt) bool {
	if receipt.ActorAdminID == uuid.Nil || !validOperationsIdentifier(receipt.OperationID, 128) || receipt.RequestDigest == [sha256.Size]byte{} ||
		(receipt.Outcome != operations.CommandOutcomeApplied && receipt.Outcome != operations.CommandOutcomeNoChange && receipt.Outcome != operations.CommandOutcomeRejected) ||
		receipt.PreviousVersion == 0 || receipt.CurrentVersion == 0 || receipt.AuditEventID == uuid.Nil || receipt.CompletedAt.IsZero() {
		return false
	}
	switch receipt.Kind {
	case operations.CommandMaintenanceChange:
		return receipt.Target == string(operations.MaintenanceUserMutations) && receipt.MaintenanceEnabled != nil && len(receipt.MaintenanceReason) <= 512 &&
			!receipt.MaintenanceChangedAt.IsZero()
	case operations.CommandCacheRefresh:
		return operations.CacheNamespace(receipt.Target).Valid() && receipt.MaintenanceEnabled == nil && receipt.MaintenanceReason == "" && receipt.MaintenancePlannedEndAt.IsZero()
	default:
		return false
	}
}

func retryTaskFromValues(kind operations.RetryTaskKind, taskID pgtype.UUID, state string, errorCode pgtype.Text, attempts int32, version int64, updatedAt pgtype.Timestamptz) (operations.RetryTask, error) {
	if !kind.Valid() || !taskID.Valid || taskID.Bytes == uuid.Nil || attempts < 0 || version <= 0 || !updatedAt.Valid ||
		(errorCode.Valid && !validStableErrorCode(errorCode.String)) {
		return operations.RetryTask{}, operations.ErrIntegrity
	}
	return operations.RetryTask{
		Kind: kind, ID: taskID.Bytes, State: state, StableErrorCode: errorCode.String, Attempts: uint32(attempts),
		Version: uint64(version), UpdatedAt: canonicalPostgresTime(updatedAt),
	}, nil
}

func optionalBool(value *bool) pgtype.Bool {
	if value == nil {
		return pgtype.Bool{}
	}
	return pgtype.Bool{Bool: *value, Valid: true}
}

func boolPointerPG(value bool) *bool { return &value }

func validOperationsIdentifier(value string, maximum int) bool {
	return strings.TrimSpace(value) == value && value != "" && len(value) <= maximum
}

func validStableErrorCode(value string) bool {
	return adminErrorMessageKeyPattern.MatchString(value) && len(value) <= 128
}

func mapAdminOperationsError(ctx context.Context, err, noRows error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return noRows
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return operations.ErrRepositoryUnavailable
}

var _ operations.Repository = (*AdminOperationsRepository)(nil)
var _ operations.CommandRepository = (*AdminOperationsRepository)(nil)

// AdminOperationsUnitOfWork binds operations CAS, signed audit, checkpoint scheduling, and outbox insertion.
type AdminOperationsUnitOfWork struct {
	runner   *TransactionRunner
	verifier audit.IntegrityVerifier
}

// NewAdminOperationsUnitOfWork requires the same audit verifier used to construct the signing service.
func NewAdminOperationsUnitOfWork(pool *pgxpool.Pool, verifier audit.IntegrityVerifier) *AdminOperationsUnitOfWork {
	if verifier == nil {
		panic("PostgreSQL operations unit of work requires an audit integrity verifier")
	}
	return &AdminOperationsUnitOfWork{runner: NewTransactionRunner(pool), verifier: verifier}
}

// Run commits every operations command participant or rolls them all back on any callback error.
func (unitOfWork *AdminOperationsUnitOfWork) Run(ctx context.Context, work operations.CommandTransactionWork) error {
	if unitOfWork == nil || unitOfWork.runner == nil || unitOfWork.verifier == nil || work == nil {
		return operations.ErrInvalidInput
	}
	err := unitOfWork.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		transaction := adminOperationsTransaction{
			operations: &AdminOperationsRepository{queries: queries}, audit: newAuditRepository(queries, unitOfWork.verifier),
			checkpoints: newAuditCheckpointRepository(queries, unitOfWork.verifier), outboxEvents: newOutboxEventRepository(queries),
		}
		return work(ctx, transaction)
	})
	return mapUnitOfWorkError(err, operations.ErrRepositoryUnavailable, adminOperationsTransactionErrors...)
}

type adminOperationsTransaction struct {
	operations   operations.CommandRepository
	audit        audit.Repository
	checkpoints  audit.CheckpointRepository
	outboxEvents outbox.EventRepository
}

func (transaction adminOperationsTransaction) Operations() operations.CommandRepository {
	return transaction.operations
}
func (transaction adminOperationsTransaction) Audit() audit.Repository { return transaction.audit }
func (transaction adminOperationsTransaction) Checkpoints() audit.CheckpointRepository {
	return transaction.checkpoints
}
func (transaction adminOperationsTransaction) OutboxEvents() outbox.EventRepository {
	return transaction.outboxEvents
}

var adminOperationsTransactionErrors = append([]error{
	operations.ErrInvalidInput, operations.ErrNotFound, operations.ErrConflict, operations.ErrIntegrity,
	operations.ErrRepositoryUnavailable, operations.ErrPermissionDenied, operations.ErrElevationRequired,
	operations.ErrPreviewExpired, operations.ErrIdempotencyConflict, operations.ErrAuditUnavailable, operations.ErrRetryLimit,
}, auditOutboxDomainErrors...)

var _ operations.CommandUnitOfWork = (*AdminOperationsUnitOfWork)(nil)
