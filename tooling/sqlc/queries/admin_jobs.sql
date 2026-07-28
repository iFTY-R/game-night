-- name: CreateAdminBatchPreview :one
INSERT INTO admin_batch_previews (
    preview_id, actor_admin_id, command, selection_schema_version, selection_snapshot,
    selection_digest, preview_digest, target_count, executable_count, blocked_count,
    sampled_at, expires_at, version
) VALUES (
    sqlc.arg(preview_id), sqlc.arg(actor_admin_id), sqlc.arg(command), sqlc.arg(selection_schema_version), sqlc.arg(selection_snapshot),
    sqlc.arg(selection_digest), sqlc.arg(preview_digest), sqlc.arg(target_count), sqlc.arg(executable_count), sqlc.arg(blocked_count),
    sqlc.arg(sampled_at), sqlc.arg(expires_at), 1
)
RETURNING *;

-- name: GetUsableAdminBatchPreviewForUpdate :one
SELECT *
FROM admin_batch_previews
WHERE preview_id = sqlc.arg(preview_id)
  AND actor_admin_id = sqlc.arg(actor_admin_id)
  AND consumed_at IS NULL
  AND expires_at > pg_catalog.clock_timestamp()
FOR UPDATE;

-- name: GetAdminBatchPreview :one
SELECT *
FROM admin_batch_previews
WHERE preview_id = sqlc.arg(preview_id)
  AND actor_admin_id = sqlc.arg(actor_admin_id);

-- name: ConsumeAdminBatchPreviewCAS :one
UPDATE admin_batch_previews
SET consumed_at = sqlc.arg(consumed_at),
    version = version + 1
WHERE preview_id = sqlc.arg(preview_id)
  AND version = sqlc.arg(expected_version)
  AND consumed_at IS NULL
  AND expires_at > sqlc.arg(consumed_at)
RETURNING *;

-- name: CreateAdminBatchJob :one
INSERT INTO admin_batch_jobs (
    batch_job_id, actor_admin_id, operation_id, request_digest, preview_id, command,
    selection_schema_version, selection_snapshot, selection_digest, reason, state,
    target_count, queued_count, running_count, succeeded_count, failed_count,
    skipped_count, canceled_count, version, created_at, updated_at
) VALUES (
    sqlc.arg(batch_job_id), sqlc.arg(actor_admin_id), sqlc.arg(operation_id), sqlc.arg(request_digest), sqlc.arg(preview_id), sqlc.arg(command),
    sqlc.arg(selection_schema_version), sqlc.arg(selection_snapshot), sqlc.arg(selection_digest), sqlc.arg(reason), 'queued',
    sqlc.arg(target_count), sqlc.arg(target_count), 0, 0, 0, 0, 0, 1, sqlc.arg(created_at), sqlc.arg(created_at)
)
ON CONFLICT (actor_admin_id, operation_id) DO UPDATE
SET operation_id = EXCLUDED.operation_id
WHERE admin_batch_jobs.request_digest = EXCLUDED.request_digest
RETURNING *;

-- name: GetAdminBatchJobByOperation :one
SELECT *
FROM admin_batch_jobs
WHERE actor_admin_id = sqlc.arg(actor_admin_id)
  AND operation_id = sqlc.arg(operation_id);

-- name: GetAdminBatchJobByID :one
SELECT *
FROM admin_batch_jobs
WHERE batch_job_id = sqlc.arg(batch_job_id);

-- name: ListAdminBatchJobs :many
SELECT *
FROM admin_batch_jobs
WHERE (cardinality(sqlc.arg(states)::text[]) = 0 OR state = ANY(sqlc.arg(states)::text[]))
  AND (cardinality(sqlc.arg(commands)::text[]) = 0 OR command = ANY(sqlc.arg(commands)::text[]))
  AND (sqlc.narg(created_from)::timestamptz IS NULL OR created_at >= sqlc.narg(created_from))
  AND (sqlc.narg(created_to)::timestamptz IS NULL OR created_at <= sqlc.narg(created_to))
  AND (
      sqlc.narg(after_batch_job_id)::uuid IS NULL
      OR (
          sqlc.arg(sort_field)::text = 'created_at'
          AND (
              (sqlc.arg(sort_direction)::text = 'ascending' AND (created_at, batch_job_id) > (sqlc.narg(after_sort_time)::timestamptz, sqlc.narg(after_batch_job_id)::uuid))
              OR (sqlc.arg(sort_direction)::text = 'descending' AND (created_at, batch_job_id) < (sqlc.narg(after_sort_time)::timestamptz, sqlc.narg(after_batch_job_id)::uuid))
          )
      )
      OR (
          sqlc.arg(sort_field)::text = 'updated_at'
          AND (
              (sqlc.arg(sort_direction)::text = 'ascending' AND (updated_at, batch_job_id) > (sqlc.narg(after_sort_time)::timestamptz, sqlc.narg(after_batch_job_id)::uuid))
              OR (sqlc.arg(sort_direction)::text = 'descending' AND (updated_at, batch_job_id) < (sqlc.narg(after_sort_time)::timestamptz, sqlc.narg(after_batch_job_id)::uuid))
          )
      )
      OR (
          sqlc.arg(sort_field)::text = 'batch_job_id'
          AND (
              (sqlc.arg(sort_direction)::text = 'ascending' AND batch_job_id > sqlc.narg(after_batch_job_id)::uuid)
              OR (sqlc.arg(sort_direction)::text = 'descending' AND batch_job_id < sqlc.narg(after_batch_job_id)::uuid)
          )
      )
  )
ORDER BY
    CASE WHEN sqlc.arg(sort_field)::text = 'created_at' AND sqlc.arg(sort_direction)::text = 'ascending' THEN created_at END ASC,
    CASE WHEN sqlc.arg(sort_field)::text = 'created_at' AND sqlc.arg(sort_direction)::text = 'descending' THEN created_at END DESC,
    CASE WHEN sqlc.arg(sort_field)::text = 'updated_at' AND sqlc.arg(sort_direction)::text = 'ascending' THEN updated_at END ASC,
    CASE WHEN sqlc.arg(sort_field)::text = 'updated_at' AND sqlc.arg(sort_direction)::text = 'descending' THEN updated_at END DESC,
    CASE WHEN sqlc.arg(sort_field)::text = 'batch_job_id' AND sqlc.arg(sort_direction)::text = 'ascending' THEN batch_job_id END ASC,
    CASE WHEN sqlc.arg(sort_field)::text = 'batch_job_id' AND sqlc.arg(sort_direction)::text = 'descending' THEN batch_job_id END DESC,
    CASE WHEN sqlc.arg(sort_direction)::text = 'ascending' THEN batch_job_id END ASC,
    CASE WHEN sqlc.arg(sort_direction)::text = 'descending' THEN batch_job_id END DESC
LIMIT sqlc.arg(page_size);

-- name: CreateAdminBatchJobItem :one
INSERT INTO admin_batch_job_items (
    item_id, batch_job_id, user_id, expected_user_version, request_digest,
    state, attempt_count, version, created_at, updated_at
) VALUES (
    sqlc.arg(item_id), sqlc.arg(batch_job_id), sqlc.arg(user_id), sqlc.arg(expected_user_version), sqlc.arg(request_digest),
    'queued', 0, 1, sqlc.arg(created_at), sqlc.arg(created_at)
)
ON CONFLICT (batch_job_id, user_id) DO UPDATE
SET user_id = EXCLUDED.user_id
WHERE admin_batch_job_items.request_digest = EXCLUDED.request_digest
RETURNING *;

-- name: ClaimAdminBatchJobItem :one
WITH candidate AS (
    SELECT queued.item_id, queued.state AS previous_state
    FROM admin_batch_job_items AS queued
    WHERE queued.batch_job_id = sqlc.arg(target_batch_job_id)
      AND (
          queued.state = 'queued'
          OR (queued.state = 'running' AND queued.lease_until <= pg_catalog.clock_timestamp())
      )
    ORDER BY queued.created_at, queued.item_id
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE admin_batch_job_items AS item
SET state = 'running',
    attempt_count = item.attempt_count + 1,
    lease_owner = sqlc.arg(lease_owner),
    lease_until = pg_catalog.clock_timestamp() + (sqlc.arg(lease_seconds)::bigint * interval '1 second'),
    started_at = COALESCE(item.started_at, pg_catalog.clock_timestamp()),
    completed_at = NULL,
    error_message_key = NULL,
    version = item.version + 1,
    updated_at = pg_catalog.clock_timestamp()
FROM candidate
WHERE item.item_id = candidate.item_id
RETURNING item.*, candidate.previous_state = 'queued' AS started_now;

-- name: ClaimNextAdminBatchJobItem :one
WITH candidate AS (
    SELECT queued.item_id, queued.state AS previous_state
    FROM admin_batch_job_items AS queued
    JOIN admin_batch_jobs AS job ON job.batch_job_id = queued.batch_job_id
    WHERE job.state IN ('queued', 'running', 'canceling')
      AND (
          queued.state = 'queued'
          OR (queued.state = 'running' AND queued.lease_until <= pg_catalog.clock_timestamp())
      )
    ORDER BY job.created_at, job.batch_job_id, queued.created_at, queued.item_id
    FOR UPDATE OF queued SKIP LOCKED
    LIMIT 1
)
UPDATE admin_batch_job_items AS item
SET state = 'running',
    attempt_count = item.attempt_count + 1,
    lease_owner = sqlc.arg(lease_owner),
    lease_until = pg_catalog.clock_timestamp() + (sqlc.arg(lease_seconds)::bigint * interval '1 second'),
    started_at = COALESCE(item.started_at, pg_catalog.clock_timestamp()),
    completed_at = NULL,
    error_message_key = NULL,
    version = item.version + 1,
    updated_at = pg_catalog.clock_timestamp()
FROM candidate
WHERE item.item_id = candidate.item_id
RETURNING item.*, candidate.previous_state = 'queued' AS started_now;

-- name: ListAdminBatchJobItems :many
SELECT *
FROM admin_batch_job_items
WHERE batch_job_id = sqlc.arg(batch_job_id)
  AND (cardinality(sqlc.arg(states)::text[]) = 0 OR state = ANY(sqlc.arg(states)::text[]))
  AND (
      sqlc.narg(after_item_id)::uuid IS NULL
      OR (created_at, item_id) > (sqlc.narg(after_created_at)::timestamptz, sqlc.narg(after_item_id)::uuid)
  )
ORDER BY created_at, item_id
LIMIT sqlc.arg(page_size);

-- name: MarkAdminBatchJobItemClaimed :one
UPDATE admin_batch_jobs
SET state = 'running',
    queued_count = queued_count - 1,
    running_count = running_count + 1,
    started_at = COALESCE(started_at, pg_catalog.clock_timestamp()),
    version = version + 1,
    updated_at = pg_catalog.clock_timestamp()
WHERE batch_job_id = sqlc.arg(batch_job_id)
  AND state IN ('queued', 'running')
  AND queued_count > 0
RETURNING *;

-- name: CompleteAdminBatchJobItemCAS :one
UPDATE admin_batch_job_items
SET state = sqlc.arg(next_state),
    lease_owner = NULL,
    lease_until = NULL,
    error_message_key = sqlc.narg(error_message_key),
    audit_event_id = sqlc.narg(audit_event_id),
    completed_at = sqlc.arg(completed_at),
    version = version + 1,
    updated_at = sqlc.arg(completed_at)
WHERE item_id = sqlc.arg(item_id)
  AND state = 'running'
  AND lease_owner = sqlc.arg(expected_lease_owner)
  AND version = sqlc.arg(expected_version)
  AND lease_until > sqlc.arg(completed_at)
  AND sqlc.arg(next_state) IN ('succeeded', 'failed', 'skipped', 'canceled')
RETURNING *;

-- name: ApplyAdminBatchJobItemCompletion :one
UPDATE admin_batch_jobs
SET running_count = running_count - 1,
    succeeded_count = succeeded_count + CASE WHEN sqlc.arg(next_state)::text = 'succeeded' THEN 1 ELSE 0 END,
    failed_count = failed_count + CASE WHEN sqlc.arg(next_state)::text = 'failed' THEN 1 ELSE 0 END,
    skipped_count = skipped_count + CASE WHEN sqlc.arg(next_state)::text = 'skipped' THEN 1 ELSE 0 END,
    canceled_count = canceled_count + CASE WHEN sqlc.arg(next_state)::text = 'canceled' THEN 1 ELSE 0 END,
    state = CASE
        WHEN queued_count = 0 AND running_count = 1 THEN CASE
            WHEN canceled_count + CASE WHEN sqlc.arg(next_state)::text = 'canceled' THEN 1 ELSE 0 END = target_count THEN 'canceled'
            WHEN failed_count + skipped_count + canceled_count
                 + CASE WHEN sqlc.arg(next_state)::text IN ('failed', 'skipped', 'canceled') THEN 1 ELSE 0 END = 0 THEN 'succeeded'
            WHEN succeeded_count + CASE WHEN sqlc.arg(next_state)::text = 'succeeded' THEN 1 ELSE 0 END > 0 THEN 'partially_succeeded'
            ELSE 'failed'
        END
        -- A running item may finish after cancellation begins; unfinished work must not reopen the parent job.
        ELSE CASE WHEN state = 'canceling' THEN 'canceling' ELSE 'running' END
    END,
    completed_at = CASE WHEN queued_count = 0 AND running_count = 1 THEN sqlc.arg(completed_at)::timestamptz ELSE NULL END,
    version = version + 1,
    updated_at = sqlc.arg(completed_at)
WHERE batch_job_id = sqlc.arg(batch_job_id)
  AND state IN ('running', 'canceling')
  AND running_count > 0
  AND sqlc.arg(next_state)::text IN ('succeeded', 'failed', 'skipped', 'canceled')
RETURNING *;

-- name: CancelAdminBatchJobCAS :one
WITH canceled_items AS (
    UPDATE admin_batch_job_items
    SET state = 'canceled',
        lease_owner = NULL,
        lease_until = NULL,
        completed_at = sqlc.arg(changed_at),
        version = version + 1,
        updated_at = sqlc.arg(changed_at)
    WHERE batch_job_id = sqlc.arg(batch_job_id)
      AND state = 'queued'
    RETURNING 1
), totals AS (
    SELECT count(*)::bigint AS canceled_count
    FROM canceled_items
)
UPDATE admin_batch_jobs
SET queued_count = admin_batch_jobs.queued_count - COALESCE((SELECT canceled_count FROM totals), 0),
    canceled_count = admin_batch_jobs.canceled_count + COALESCE((SELECT canceled_count FROM totals), 0),
    state = CASE
        WHEN admin_batch_jobs.running_count > 0 THEN 'canceling'
        ELSE 'canceled'
    END,
    started_at = COALESCE(admin_batch_jobs.started_at, sqlc.arg(changed_at)),
    completed_at = CASE
        WHEN admin_batch_jobs.running_count > 0 THEN NULL
        ELSE sqlc.arg(changed_at)
    END,
    version = admin_batch_jobs.version + 1,
    updated_at = sqlc.arg(changed_at)
WHERE admin_batch_jobs.batch_job_id = sqlc.arg(batch_job_id)
  AND admin_batch_jobs.version = sqlc.arg(expected_version)
  AND admin_batch_jobs.state IN ('queued', 'running', 'canceling')
RETURNING *;

-- name: RetryAdminBatchJobCAS :one
WITH selected_items AS (
    SELECT item_id, state
    FROM admin_batch_job_items
    WHERE batch_job_id = sqlc.arg(batch_job_id)
      AND state IN ('failed', 'skipped', 'canceled')
      AND (
          cardinality(sqlc.arg(item_ids)::uuid[]) = 0
          OR item_id = ANY(sqlc.arg(item_ids)::uuid[])
      )
    FOR UPDATE
), retried_items AS (
    UPDATE admin_batch_job_items AS item
    SET state = 'queued',
        lease_owner = NULL,
        lease_until = NULL,
        error_message_key = NULL,
        audit_event_id = NULL,
        completed_at = NULL,
        version = item.version + 1,
        updated_at = sqlc.arg(changed_at)
    FROM selected_items
    WHERE item.item_id = selected_items.item_id
    RETURNING selected_items.state AS previous_state
), totals AS (
    SELECT count(*)::bigint AS requeued_count,
           count(*) FILTER (WHERE previous_state = 'failed')::bigint AS failed_count,
           count(*) FILTER (WHERE previous_state = 'skipped')::bigint AS skipped_count,
           count(*) FILTER (WHERE previous_state = 'canceled')::bigint AS canceled_count
    FROM retried_items
)
UPDATE admin_batch_jobs
SET queued_count = admin_batch_jobs.queued_count + COALESCE((SELECT requeued_count FROM totals), 0),
    failed_count = admin_batch_jobs.failed_count - COALESCE((SELECT failed_count FROM totals), 0),
    skipped_count = admin_batch_jobs.skipped_count - COALESCE((SELECT skipped_count FROM totals), 0),
    canceled_count = admin_batch_jobs.canceled_count - COALESCE((SELECT canceled_count FROM totals), 0),
    state = CASE
        WHEN COALESCE((SELECT requeued_count FROM totals), 0) > 0 THEN 'running'
        ELSE admin_batch_jobs.state
    END,
    completed_at = CASE
        WHEN COALESCE((SELECT requeued_count FROM totals), 0) > 0 THEN NULL
        ELSE admin_batch_jobs.completed_at
    END,
    error_message_key = NULL,
    version = admin_batch_jobs.version + 1,
    updated_at = sqlc.arg(changed_at)
WHERE admin_batch_jobs.batch_job_id = sqlc.arg(batch_job_id)
  AND admin_batch_jobs.version = sqlc.arg(expected_version)
  AND admin_batch_jobs.state IN ('succeeded', 'partially_succeeded', 'failed', 'canceled')
RETURNING *;

-- name: CreateAdminUserErasureJob :one
INSERT INTO admin_user_erasure_jobs (
    erasure_job_id, user_id, actor_admin_id, operation_id, request_digest,
    state, step, reason, attempt_count, version, created_at, updated_at
) VALUES (
    sqlc.arg(erasure_job_id), sqlc.arg(user_id), sqlc.arg(actor_admin_id), sqlc.arg(operation_id), sqlc.arg(request_digest),
    'queued', 'queued', sqlc.arg(reason), 0, 1, sqlc.arg(created_at), sqlc.arg(created_at)
)
ON CONFLICT (actor_admin_id, operation_id) DO UPDATE
SET operation_id = EXCLUDED.operation_id
WHERE admin_user_erasure_jobs.request_digest = EXCLUDED.request_digest
RETURNING *;

-- name: ClaimAdminUserErasureJob :one
WITH candidate AS (
    SELECT queued.erasure_job_id
    FROM admin_user_erasure_jobs AS queued
    WHERE queued.state = 'queued'
       OR (queued.state = 'running' AND queued.lease_until <= pg_catalog.clock_timestamp())
    ORDER BY queued.created_at, queued.erasure_job_id
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE admin_user_erasure_jobs AS job
SET state = 'running',
    step = CASE WHEN job.state = 'queued' THEN 'revoke_credentials' ELSE job.step END,
    attempt_count = job.attempt_count + 1,
    lease_owner = sqlc.arg(lease_owner),
    lease_until = pg_catalog.clock_timestamp() + (sqlc.arg(lease_seconds)::bigint * interval '1 second'),
    started_at = COALESCE(job.started_at, pg_catalog.clock_timestamp()),
    version = job.version + 1,
    updated_at = pg_catalog.clock_timestamp()
FROM candidate
WHERE job.erasure_job_id = candidate.erasure_job_id
RETURNING job.*;

-- name: AdvanceAdminUserErasureStepCAS :one
UPDATE admin_user_erasure_jobs
SET step = sqlc.arg(next_step),
    version = version + 1,
    updated_at = sqlc.arg(changed_at)
WHERE erasure_job_id = sqlc.arg(erasure_job_id)
  AND state = 'running'
  AND lease_owner = sqlc.arg(expected_lease_owner)
  AND version = sqlc.arg(expected_version)
  AND lease_until > sqlc.arg(changed_at)
  AND (
      (step = 'revoke_credentials' AND sqlc.arg(next_step) = 'erase_profile')
      OR (step = 'erase_profile' AND sqlc.arg(next_step) = 'enqueue_room_cleanup')
      OR (step = 'enqueue_room_cleanup' AND sqlc.arg(next_step) = 'complete')
  )
RETURNING *;

-- name: CompleteAdminUserErasureJobCAS :one
UPDATE admin_user_erasure_jobs
SET state = sqlc.arg(next_state),
    lease_owner = NULL,
    lease_until = NULL,
    error_message_key = sqlc.narg(error_message_key),
    completed_at = sqlc.arg(completed_at),
    version = version + 1,
    updated_at = sqlc.arg(completed_at)
WHERE erasure_job_id = sqlc.arg(erasure_job_id)
  AND state = 'running'
  AND lease_owner = sqlc.arg(expected_lease_owner)
  AND version = sqlc.arg(expected_version)
  AND lease_until > sqlc.arg(completed_at)
  AND sqlc.arg(next_state) IN ('succeeded', 'failed')
  AND (sqlc.arg(next_state) = 'failed' OR step = 'complete')
RETURNING *;
