package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/internal/integrationtest"
	"github.com/iFTY-R/game-night/platform/admin/operations"
)

func TestAdminOperationsRepositoryCASHeartbeatMetricsAndRetryReceipt(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	now := databaseIntegrationTime(t, ctx, fixture)
	adminID, _ := seedAdminUserCenterPrincipal(t, ctx, fixture, now)
	repository := NewAdminOperationsRepository(fixture.Pool)

	initial, err := repository.GetMaintenanceState(ctx)
	if err != nil || initial.Enabled || initial.Version != 1 || initial.Scope != operations.MaintenanceUserMutations {
		t.Fatalf("initial maintenance: state=%+v err=%v", initial, err)
	}
	changed, err := repository.UpdateMaintenanceState(ctx, operations.MaintenanceChange{
		Enabled: true, Scope: operations.MaintenanceUserMutations, Reason: "integration maintenance", PlannedEndAt: now.Add(time.Hour),
		ExpectedVersion: initial.Version, ChangedByAdminID: adminID, ChangedAt: now.Add(time.Second),
	})
	if err != nil || !changed.Enabled || changed.Version != 2 {
		t.Fatalf("change maintenance: state=%+v err=%v", changed, err)
	}
	if _, err = repository.UpdateMaintenanceState(ctx, operations.MaintenanceChange{
		Scope: operations.MaintenanceUserMutations, Reason: "stale change", ExpectedVersion: 1, ChangedByAdminID: adminID, ChangedAt: now.Add(2 * time.Second),
	}); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("stale maintenance error = %v", err)
	}

	instance := operations.ServiceInstance{
		Kind: operations.ServiceAPI, InstanceID: "api-integration-1", BuildVersion: "integration", StartedAt: now,
		LastHeartbeatAt: now.Add(3 * time.Second), Status: operations.HealthHealthy,
		Components: map[string]operations.HealthStatus{"postgresql": operations.HealthHealthy}, MaintenanceVersion: changed.Version,
	}
	storedInstance, err := repository.UpsertServiceInstance(ctx, instance)
	if err != nil || storedInstance.InstanceID != instance.InstanceID {
		t.Fatalf("upsert instance: instance=%+v err=%v", storedInstance, err)
	}
	instance.LastHeartbeatAt = now.Add(2 * time.Second)
	if _, err = repository.UpsertServiceInstance(ctx, instance); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("older heartbeat error = %v", err)
	}
	instances, err := repository.ListServiceInstances(ctx, 10)
	if err != nil || len(instances) != 1 {
		t.Fatalf("list instances: instances=%+v err=%v", instances, err)
	}

	bucketStart := now.UTC().Truncate(time.Hour)
	bucket := operations.MetricBucket{Name: operations.MetricActiveRooms, Width: operations.BucketHour, Start: bucketStart, Value: 3, SampledAt: now, SourceWatermark: 7}
	if _, err = repository.UpsertMetricBucket(ctx, bucket); err != nil {
		t.Fatal(err)
	}
	bucket.Value = 9
	bucket.SourceWatermark = 6
	if _, err = repository.UpsertMetricBucket(ctx, bucket); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("older metric watermark error = %v", err)
	}
	buckets, err := repository.ListMetricBuckets(ctx, operations.MetricQuery{
		Names: []operations.MetricName{operations.MetricActiveRooms}, Width: operations.BucketHour,
		WindowStart: bucketStart, WindowEnd: bucketStart.Add(time.Hour), Limit: 10,
	})
	if err != nil || len(buckets) != 1 || buckets[0].Value != 3 {
		t.Fatalf("metric buckets: buckets=%+v err=%v", buckets, err)
	}

	generation, err := repository.GetCacheGeneration(ctx, operations.CacheOverviewProjection)
	if err != nil || generation.Generation != 1 {
		t.Fatalf("initial generation: generation=%+v err=%v", generation, err)
	}
	advanced, err := repository.AdvanceCacheGeneration(ctx, operations.CacheOverviewProjection, generation.Generation, adminID, now.Add(4*time.Second))
	if err != nil || advanced.Generation != 2 {
		t.Fatalf("advance generation: generation=%+v err=%v", advanced, err)
	}
	if _, err = repository.AdvanceCacheGeneration(ctx, operations.CacheOverviewProjection, 1, adminID, now.Add(5*time.Second)); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("stale generation error = %v", err)
	}

	auditEventID := seedOperationsAuditEvent(t, ctx, fixture, now.Add(6*time.Second))
	preview := operations.CommandPreview{
		Digest: sha256.Sum256([]byte("operations-preview")), ActorAdminID: adminID, Kind: operations.CommandCacheRefresh,
		ReasonDigest: sha256.Sum256([]byte("integration refresh")), ExpectedVersion: advanced.Generation,
		CacheNamespace: operations.CacheOverviewProjection, SampledAt: now.Add(5 * time.Second), ExpiresAt: now.Add(5*time.Minute + 5*time.Second), Version: 1,
	}
	storedPreview, err := repository.CreateCommandPreview(ctx, preview)
	if err != nil || storedPreview.Digest != preview.Digest {
		t.Fatalf("create preview: preview=%+v err=%v", storedPreview, err)
	}
	consumedPreview, err := repository.ConsumeCommandPreview(ctx, adminID, preview.Digest, preview.Version, now.Add(6*time.Second))
	if err != nil || consumedPreview.ConsumedAt.IsZero() {
		t.Fatalf("consume preview: preview=%+v err=%v", consumedPreview, err)
	}
	commandReceipt := operations.CommandReceipt{
		ActorAdminID: adminID, OperationID: "cache-integration-1", RequestDigest: sha256.Sum256([]byte("cache-request")),
		Kind: operations.CommandCacheRefresh, Target: string(operations.CacheOverviewProjection), Outcome: operations.CommandOutcomeApplied,
		PreviousVersion: 1, CurrentVersion: advanced.Generation, AuditEventID: auditEventID, CompletedAt: now.Add(6 * time.Second),
	}
	if _, err = repository.CreateCommandReceipt(ctx, commandReceipt); err != nil {
		t.Fatalf("create command receipt: %v", err)
	}
	receipt := operations.RetryReceipt{
		ActorAdminID: adminID, OperationID: "retry-integration-1", TaskKind: operations.RetryUserBatch, TaskID: uuid.New(),
		ExpectedTaskVersion: 1, Outcome: "applied", TaskVersion: 2, ManualRetryCount: 1, TaskState: "queued",
		OriginalErrorCode: "admin.batch.failed", AuditEventID: auditEventID, CompletedAt: now.Add(6 * time.Second),
	}
	receipt.RequestDigest[0] = 1
	createdReceipt, err := repository.CreateRetryReceipt(ctx, receipt)
	if err != nil || createdReceipt.OperationID != receipt.OperationID {
		t.Fatalf("create retry receipt: receipt=%+v err=%v", createdReceipt, err)
	}
	if _, err = repository.CreateRetryReceipt(ctx, receipt); err != nil {
		t.Fatalf("idempotent retry receipt: %v", err)
	}
	conflict := receipt
	conflict.RequestDigest[0] = 2
	if _, err = repository.CreateRetryReceipt(ctx, conflict); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("retry digest conflict error = %v", err)
	}

	backlogs, err := repository.ListBacklogs(ctx, now.Add(7*time.Second))
	if err != nil || len(backlogs) != 5 {
		t.Fatalf("backlogs: values=%+v err=%v", backlogs, err)
	}
	counts, err := repository.GetOverviewCounts(ctx, now.Add(-24*time.Hour), now.Add(time.Second), now.Add(7*time.Second))
	if err != nil || counts.WindowStart.IsZero() || counts.WindowEnd.IsZero() {
		t.Fatalf("overview counts: counts=%+v err=%v", counts, err)
	}
}

func TestAdminOperationsBacklogsCountOnlyConsumerSubscriptions(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	now := databaseIntegrationTime(t, ctx, fixture)

	if _, err := fixture.Pool.Exec(ctx, `
		INSERT INTO outbox_consumers (consumer_id, subscriptions, last_acked_sequence, created_at, updated_at)
		VALUES
			('audit.checkpoint', ARRAY['audit.checkpoint.pending'], 0, $1, $1),
			('realtime.game_fanout', ARRAY['game.session.transitioned.v1'], 0, $1, $1)
		ON CONFLICT (consumer_id) DO UPDATE
		SET subscriptions = EXCLUDED.subscriptions,
		    last_acked_sequence = 0,
		    lease_owner = NULL,
		    lease_until = NULL,
		    retry_count = 0,
		    next_attempt_at = NULL,
		    last_error_code = NULL,
		    updated_at = EXCLUDED.updated_at`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Pool.Exec(ctx, `
		INSERT INTO outbox_events (event_id, event_type, aggregate_type, aggregate_id, payload, created_at, available_at)
		VALUES
			($1, 'audit.checkpoint.pending', 'audit.chain', $2, $3, $4, $4),
			($5, 'game.session.transitioned.v1', 'game.session', $6, $7, $4, $4)`,
		uuid.New(), uuid.New(), []byte("checkpoint"), now, uuid.New(), uuid.New(), []byte("transition")); err != nil {
		t.Fatal(err)
	}

	backlogs, err := NewAdminOperationsRepository(fixture.Pool).ListBacklogs(ctx, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[operations.BacklogKind]uint64, len(backlogs))
	for _, backlog := range backlogs {
		counts[backlog.Kind] = backlog.Pending
	}
	if counts[operations.BacklogAuditOutbox] != 1 || counts[operations.BacklogRoomOutbox] != 1 {
		t.Fatalf("subscription-filtered pending counts = %+v, want one event per consumer", counts)
	}
}

func seedOperationsAuditEvent(t *testing.T, ctx context.Context, fixture *integrationtest.PostgresSchema, createdAt time.Time) uuid.UUID {
	t.Helper()
	var sequence int64
	if err := fixture.Pool.QueryRow(ctx, "SELECT COALESCE(max(sequence), 0) + 1 FROM audit_events WHERE chain_id = 'admin'").Scan(&sequence); err != nil {
		t.Fatal(err)
	}
	eventID := uuid.New()
	if _, err := fixture.Pool.Exec(ctx, `
		INSERT INTO audit_events (
			chain_id, sequence, event_id, previous_hash, canonical_event, event_hash, signature, signing_key_version, created_at
		) VALUES ('admin', $1, $2, $3, $4, $5, $6, 1, $7)
	`, sequence, eventID, make([]byte, 32), []byte("operations integration receipt"), make([]byte, 32), make([]byte, 64), createdAt); err != nil {
		t.Fatal(err)
	}
	return eventID
}
