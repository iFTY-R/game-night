-- name: GetAdminMaintenanceState :one
SELECT *
FROM admin_maintenance_state
WHERE singleton_id = 1;

-- name: UpdateAdminMaintenanceStateCAS :one
UPDATE admin_maintenance_state
SET enabled = sqlc.arg(enabled),
    scope = sqlc.arg(scope),
    reason = sqlc.arg(reason),
    planned_end_at = sqlc.narg(planned_end_at),
    version = version + 1,
    changed_by_admin_id = sqlc.arg(changed_by_admin_id),
    changed_at = sqlc.arg(changed_at)
WHERE singleton_id = 1
  AND version = sqlc.arg(expected_version)
RETURNING *;

-- name: UpsertAdminServiceInstance :one
INSERT INTO admin_service_instances (
    service_kind, instance_id, build_version, started_at, last_heartbeat_at,
    status, components, maintenance_version
) VALUES (
    sqlc.arg(service_kind), sqlc.arg(instance_id), sqlc.arg(build_version), sqlc.arg(started_at), sqlc.arg(last_heartbeat_at),
    sqlc.arg(status), sqlc.arg(components), sqlc.arg(maintenance_version)
)
ON CONFLICT (service_kind, instance_id) DO UPDATE
SET build_version = EXCLUDED.build_version,
    started_at = EXCLUDED.started_at,
    last_heartbeat_at = EXCLUDED.last_heartbeat_at,
    status = EXCLUDED.status,
    components = EXCLUDED.components,
    maintenance_version = EXCLUDED.maintenance_version
WHERE admin_service_instances.last_heartbeat_at <= EXCLUDED.last_heartbeat_at
RETURNING *;

-- name: ListAdminServiceInstances :many
SELECT *
FROM admin_service_instances
ORDER BY service_kind, instance_id
LIMIT sqlc.arg(page_size);

-- name: UpsertAdminMetricBucket :one
INSERT INTO admin_metric_buckets (
    metric_name, bucket_width, bucket_start, value, sampled_at, source_watermark
) VALUES (
    sqlc.arg(metric_name), sqlc.arg(bucket_width), sqlc.arg(bucket_start), sqlc.arg(value), sqlc.arg(sampled_at), sqlc.arg(source_watermark)
)
ON CONFLICT (metric_name, bucket_width, bucket_start) DO UPDATE
SET value = EXCLUDED.value,
    sampled_at = EXCLUDED.sampled_at,
    source_watermark = EXCLUDED.source_watermark
WHERE admin_metric_buckets.source_watermark <= EXCLUDED.source_watermark
RETURNING *;

-- name: ListAdminMetricBuckets :many
SELECT *
FROM admin_metric_buckets
WHERE metric_name = ANY(sqlc.arg(metric_names)::text[])
  AND bucket_width = sqlc.arg(bucket_width)
  AND bucket_start >= sqlc.arg(window_start)
  AND bucket_start < sqlc.arg(window_end)
ORDER BY metric_name, bucket_start
LIMIT sqlc.arg(page_size);

-- name: GetAdminCacheGeneration :one
SELECT *
FROM admin_cache_generations
WHERE namespace = sqlc.arg(namespace);

-- name: IncrementAdminCacheGenerationCAS :one
UPDATE admin_cache_generations
SET generation = generation + 1,
    updated_by_admin_id = sqlc.arg(updated_by_admin_id),
    updated_at = sqlc.arg(updated_at)
WHERE namespace = sqlc.arg(namespace)
  AND generation = sqlc.arg(expected_generation)
RETURNING *;

-- name: GetAdminOperationsRetryReceipt :one
SELECT *
FROM admin_operations_retry_receipts
WHERE actor_admin_id = sqlc.arg(actor_admin_id)
  AND operation_id = sqlc.arg(operation_id);

-- name: CreateAdminOperationsPreview :one
INSERT INTO admin_operations_previews (
    preview_digest, actor_admin_id, command_kind, reason_digest, expected_version,
    maintenance_enabled, maintenance_planned_end_at, cache_namespace, task_kind,
    task_id, sampled_at, expires_at
) VALUES (
    sqlc.arg(preview_digest), sqlc.arg(actor_admin_id), sqlc.arg(command_kind), sqlc.arg(reason_digest), sqlc.arg(expected_version),
    sqlc.narg(maintenance_enabled), sqlc.narg(maintenance_planned_end_at), sqlc.narg(cache_namespace), sqlc.narg(task_kind),
    sqlc.narg(task_id), sqlc.arg(sampled_at), sqlc.arg(expires_at)
)
ON CONFLICT (preview_digest) DO NOTHING
RETURNING *;

-- name: GetAdminOperationsPreview :one
SELECT *
FROM admin_operations_previews
WHERE preview_digest = sqlc.arg(preview_digest)
  AND actor_admin_id = sqlc.arg(actor_admin_id);

-- name: ConsumeAdminOperationsPreviewCAS :one
UPDATE admin_operations_previews
SET consumed_at = sqlc.arg(consumed_at),
    version = version + 1
WHERE preview_digest = sqlc.arg(preview_digest)
  AND actor_admin_id = sqlc.arg(actor_admin_id)
  AND version = sqlc.arg(expected_version)
  AND consumed_at IS NULL
  AND expires_at > sqlc.arg(consumed_at)
RETURNING *;

-- name: GetAdminOperationsCommandReceipt :one
SELECT *
FROM admin_operations_command_receipts
WHERE actor_admin_id = sqlc.arg(actor_admin_id)
  AND operation_id = sqlc.arg(operation_id);

-- name: CreateAdminOperationsCommandReceipt :one
INSERT INTO admin_operations_command_receipts (
    actor_admin_id, operation_id, request_digest, command_kind, target, outcome,
    previous_version, current_version, maintenance_enabled, maintenance_reason,
    maintenance_planned_end_at, maintenance_changed_by_admin_id, maintenance_changed_at,
    audit_event_id, completed_at
) VALUES (
    sqlc.arg(actor_admin_id), sqlc.arg(operation_id), sqlc.arg(request_digest), sqlc.arg(command_kind), sqlc.arg(target), sqlc.arg(outcome),
    sqlc.arg(previous_version), sqlc.arg(current_version), sqlc.narg(maintenance_enabled), sqlc.arg(maintenance_reason),
    sqlc.narg(maintenance_planned_end_at), sqlc.narg(maintenance_changed_by_admin_id), sqlc.narg(maintenance_changed_at),
    sqlc.arg(audit_event_id), sqlc.arg(completed_at)
)
ON CONFLICT (actor_admin_id, operation_id) DO UPDATE
SET operation_id = EXCLUDED.operation_id
WHERE admin_operations_command_receipts.request_digest = EXCLUDED.request_digest
RETURNING *;

-- name: CreateAdminOperationsRetryReceipt :one
INSERT INTO admin_operations_retry_receipts (
    actor_admin_id, operation_id, request_digest, task_kind, task_id,
    expected_task_version, outcome, task_version, manual_retry_count, task_state, original_error_code,
    audit_event_id, completed_at
) VALUES (
    sqlc.arg(actor_admin_id), sqlc.arg(operation_id), sqlc.arg(request_digest), sqlc.arg(task_kind), sqlc.arg(task_id),
    sqlc.arg(expected_task_version), sqlc.arg(outcome), sqlc.arg(task_version), sqlc.arg(manual_retry_count), sqlc.arg(task_state), sqlc.arg(original_error_code),
    sqlc.arg(audit_event_id), sqlc.arg(completed_at)
)
ON CONFLICT (actor_admin_id, operation_id) DO UPDATE
SET operation_id = EXCLUDED.operation_id
WHERE admin_operations_retry_receipts.request_digest = EXCLUDED.request_digest
RETURNING *;

-- name: GetAdminOperationsBatchTask :one
SELECT batch_job_id AS task_id, state, error_message_key AS stable_error_code, version, updated_at
FROM admin_batch_jobs
WHERE batch_job_id = sqlc.arg(task_id);

-- name: GetAdminOperationsBatchTaskForUpdate :one
SELECT batch_job_id AS task_id, state, error_message_key AS stable_error_code, version, updated_at
FROM admin_batch_jobs
WHERE batch_job_id = sqlc.arg(task_id)
FOR UPDATE;

-- name: GetAdminOperationsErasureTask :one
SELECT erasure_job_id AS task_id, state, error_message_key AS stable_error_code, attempt_count, version, updated_at
FROM admin_user_erasure_jobs
WHERE erasure_job_id = sqlc.arg(task_id);

-- name: GetAdminOperationsErasureTaskForUpdate :one
SELECT erasure_job_id AS task_id, state, error_message_key AS stable_error_code, attempt_count, version, updated_at
FROM admin_user_erasure_jobs
WHERE erasure_job_id = sqlc.arg(task_id)
FOR UPDATE;

-- name: CountAdminOperationsTaskRetries :one
SELECT count(*)::bigint
FROM admin_operations_retry_receipts
WHERE task_kind = sqlc.arg(task_kind)
  AND task_id = sqlc.arg(task_id)
  AND outcome = 'applied';

-- name: RetryAdminOperationsBatchTaskCAS :one
WITH retried_items AS (
    UPDATE admin_batch_job_items
    SET state = 'queued',
        lease_owner = NULL,
        lease_until = NULL,
        completed_at = NULL,
        version = version + 1,
        updated_at = sqlc.arg(changed_at)
    WHERE batch_job_id = sqlc.arg(task_id)
      AND state = 'failed'
    RETURNING item_id
), totals AS (
    SELECT count(*)::bigint AS requeued_count FROM retried_items
)
UPDATE admin_batch_jobs
SET queued_count = admin_batch_jobs.queued_count + (SELECT requeued_count FROM totals),
    failed_count = admin_batch_jobs.failed_count - (SELECT requeued_count FROM totals),
    state = 'running',
    completed_at = NULL,
    version = admin_batch_jobs.version + 1,
    updated_at = sqlc.arg(changed_at)
WHERE admin_batch_jobs.batch_job_id = sqlc.arg(task_id)
  AND admin_batch_jobs.version = sqlc.arg(expected_version)
  AND admin_batch_jobs.state = 'failed'
  AND (SELECT requeued_count FROM totals) > 0
RETURNING *;

-- name: RetryAdminOperationsErasureTaskCAS :one
UPDATE admin_user_erasure_jobs
SET state = 'queued',
    lease_owner = NULL,
    lease_until = NULL,
    completed_at = NULL,
    version = version + 1,
    updated_at = sqlc.arg(changed_at)
WHERE erasure_job_id = sqlc.arg(task_id)
  AND version = sqlc.arg(expected_version)
  AND state = 'failed'
RETURNING *;

-- name: ListAdminOperationsBacklogs :many
SELECT 'audit_outbox'::text AS backlog_kind,
       (SELECT count(*)::bigint FROM outbox_events WHERE event_sequence > COALESCE((SELECT last_acked_sequence FROM outbox_consumers WHERE consumer_id = 'audit.checkpoint'), 0) AND event_type = ANY(COALESCE((SELECT subscriptions FROM outbox_consumers WHERE consumer_id = 'audit.checkpoint'), ARRAY[]::text[]))) AS pending_count,
       (SELECT count(*)::bigint FROM outbox_consumers WHERE consumer_id = 'audit.checkpoint' AND lease_owner IS NOT NULL AND lease_until > sqlc.arg(sampled_at)::timestamptz) AS running_count,
       (SELECT count(*)::bigint FROM outbox_consumers WHERE consumer_id = 'audit.checkpoint' AND last_error_code IS NOT NULL) AS failed_count,
       (SELECT min(available_at)::timestamptz FROM outbox_events WHERE event_sequence > COALESCE((SELECT last_acked_sequence FROM outbox_consumers WHERE consumer_id = 'audit.checkpoint'), 0) AND event_type = ANY(COALESCE((SELECT subscriptions FROM outbox_consumers WHERE consumer_id = 'audit.checkpoint'), ARRAY[]::text[]))) AS oldest_pending_at
UNION ALL
SELECT 'room_outbox'::text,
       (SELECT count(*)::bigint FROM outbox_events WHERE event_sequence > COALESCE((SELECT last_acked_sequence FROM outbox_consumers WHERE consumer_id = 'realtime.game_fanout'), 0) AND event_type = ANY(COALESCE((SELECT subscriptions FROM outbox_consumers WHERE consumer_id = 'realtime.game_fanout'), ARRAY[]::text[]))),
       (SELECT count(*)::bigint FROM outbox_consumers WHERE consumer_id = 'realtime.game_fanout' AND lease_owner IS NOT NULL AND lease_until > sqlc.arg(sampled_at)::timestamptz),
       (SELECT count(*)::bigint FROM outbox_consumers WHERE consumer_id = 'realtime.game_fanout' AND last_error_code IS NOT NULL),
       (SELECT min(available_at)::timestamptz FROM outbox_events WHERE event_sequence > COALESCE((SELECT last_acked_sequence FROM outbox_consumers WHERE consumer_id = 'realtime.game_fanout'), 0) AND event_type = ANY(COALESCE((SELECT subscriptions FROM outbox_consumers WHERE consumer_id = 'realtime.game_fanout'), ARRAY[]::text[])))
UNION ALL
SELECT 'realtime_timer'::text,
       count(*) FILTER (WHERE due_at <= sqlc.arg(sampled_at)::timestamptz)::bigint,
       0::bigint,
       0::bigint,
       min(due_at) FILTER (WHERE due_at <= sqlc.arg(sampled_at)::timestamptz)::timestamptz
FROM game_session_timers
UNION ALL
SELECT 'user_batch'::text,
       count(*) FILTER (WHERE state = 'queued')::bigint,
       count(*) FILTER (WHERE state IN ('running', 'canceling'))::bigint,
       count(*) FILTER (WHERE state = 'failed')::bigint,
       min(created_at) FILTER (WHERE state = 'queued')::timestamptz
FROM admin_batch_jobs
UNION ALL
SELECT 'user_erasure'::text,
       count(*) FILTER (WHERE state = 'queued')::bigint,
       count(*) FILTER (WHERE state = 'running')::bigint,
       count(*) FILTER (WHERE state = 'failed')::bigint,
       min(created_at) FILTER (WHERE state = 'queued')::timestamptz
FROM admin_user_erasure_jobs
ORDER BY backlog_kind;
