-- name: CreateAdminExportJob :one
INSERT INTO admin_export_jobs (
    export_id, actor_admin_id, operation_id, request_digest, filter_schema_version,
    filter_snapshot, filter_digest, field_names, masking_policy, state,
    matched_users, exported_users, failed_users, result_schema_version,
    result_expires_at, version, created_at, updated_at
) VALUES (
    sqlc.arg(export_id), sqlc.arg(actor_admin_id), sqlc.arg(operation_id), sqlc.arg(request_digest), sqlc.arg(filter_schema_version),
    sqlc.arg(filter_snapshot), sqlc.arg(filter_digest), sqlc.arg(field_names), sqlc.arg(masking_policy), 'queued',
    0, 0, 0, sqlc.arg(result_schema_version), sqlc.arg(result_expires_at), 1, sqlc.arg(created_at), sqlc.arg(created_at)
)
ON CONFLICT (actor_admin_id, operation_id) DO UPDATE
SET operation_id = EXCLUDED.operation_id
WHERE admin_export_jobs.request_digest = EXCLUDED.request_digest
RETURNING *;

-- name: GetAdminExportJob :one
SELECT *
FROM admin_export_jobs
WHERE export_id = sqlc.arg(export_id);

-- name: GetAdminExportJobForDownloadGrant :one
SELECT *
FROM admin_export_jobs
WHERE export_id = sqlc.arg(export_id)
FOR SHARE;

-- name: GetAdminExportJobByOperation :one
SELECT *
FROM admin_export_jobs
WHERE actor_admin_id = sqlc.arg(actor_admin_id)
  AND operation_id = sqlc.arg(operation_id);

-- name: ClaimAdminExportJob :one
WITH candidate AS (
    SELECT export_id
    FROM admin_export_jobs
    WHERE state = 'queued'
       OR (state = 'running' AND lease_until <= pg_catalog.clock_timestamp())
    ORDER BY created_at, export_id
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE admin_export_jobs AS job
SET state = 'running',
    lease_owner = sqlc.arg(lease_owner),
    lease_until = pg_catalog.clock_timestamp() + (sqlc.arg(lease_seconds)::bigint * interval '1 second'),
    started_at = COALESCE(job.started_at, pg_catalog.clock_timestamp()),
    version = job.version + 1,
    updated_at = pg_catalog.clock_timestamp()
FROM candidate
WHERE job.export_id = candidate.export_id
RETURNING job.*;

-- name: CompleteAdminExportJobCAS :one
UPDATE admin_export_jobs
SET state = sqlc.arg(next_state),
    matched_users = sqlc.arg(matched_users),
    exported_users = sqlc.arg(exported_users),
    failed_users = sqlc.arg(failed_users),
    result_object_key = sqlc.arg(result_object_key),
    result_digest = sqlc.arg(result_digest),
    result_key_version = sqlc.arg(result_key_version),
    lease_owner = NULL,
    lease_until = NULL,
    completed_at = sqlc.arg(completed_at),
    version = version + 1,
    updated_at = sqlc.arg(completed_at)
WHERE export_id = sqlc.arg(export_id)
  AND state = 'running'
  AND lease_owner = sqlc.arg(expected_lease_owner)
  AND version = sqlc.arg(expected_version)
  AND lease_until > sqlc.arg(completed_at)
  AND result_expires_at > sqlc.arg(completed_at)
  AND sqlc.arg(next_state) IN ('succeeded', 'partially_succeeded')
RETURNING *;

-- name: FailAdminExportJobCAS :one
UPDATE admin_export_jobs
SET state = 'failed',
    matched_users = sqlc.arg(matched_users),
    exported_users = sqlc.arg(exported_users),
    failed_users = sqlc.arg(failed_users),
    error_message_key = sqlc.arg(error_message_key),
    lease_owner = NULL,
    lease_until = NULL,
    completed_at = sqlc.arg(completed_at),
    version = version + 1,
    updated_at = sqlc.arg(completed_at)
WHERE export_id = sqlc.arg(export_id)
  AND state = 'running'
  AND lease_owner = sqlc.arg(expected_lease_owner)
  AND version = sqlc.arg(expected_version)
  AND lease_until > sqlc.arg(completed_at)
RETURNING *;

-- name: ExpireAdminExportResults :many
UPDATE admin_export_jobs
SET state = 'expired',
    result_object_key = NULL,
    result_digest = NULL,
    result_key_version = NULL,
    completed_at = COALESCE(completed_at, sqlc.arg(boundary)),
    version = version + 1,
    updated_at = sqlc.arg(boundary)
WHERE state IN ('succeeded', 'partially_succeeded')
  AND result_expires_at <= sqlc.arg(boundary)
RETURNING export_id;

-- name: DeleteAdminExportResultCAS :one
UPDATE admin_export_jobs
SET state = 'deleted',
    result_object_key = NULL,
    result_digest = NULL,
    result_key_version = NULL,
    version = version + 1,
    updated_at = sqlc.arg(deleted_at)
WHERE export_id = sqlc.arg(export_id)
  AND version = sqlc.arg(expected_version)
  AND state IN ('succeeded', 'partially_succeeded', 'expired')
RETURNING *;

-- name: CreateAdminExportDownloadGrant :one
INSERT INTO admin_export_download_grants (
    grant_id, export_id, actor_admin_id, session_id, operation_id, request_digest,
    token_digest, token_key_version, expected_export_version, state,
    masking_policy, created_at, expires_at, version
) VALUES (
    sqlc.arg(grant_id), sqlc.arg(export_id), sqlc.arg(actor_admin_id), sqlc.arg(session_id), sqlc.arg(operation_id), sqlc.arg(request_digest),
    sqlc.arg(token_digest), sqlc.arg(token_key_version), sqlc.arg(expected_export_version), 'active', sqlc.arg(masking_policy),
    sqlc.arg(created_at), sqlc.arg(expires_at), 1
)
ON CONFLICT (actor_admin_id, operation_id) DO UPDATE
SET operation_id = EXCLUDED.operation_id
WHERE admin_export_download_grants.request_digest = EXCLUDED.request_digest
RETURNING *;

-- name: ConsumeAdminExportDownloadGrantCAS :one
UPDATE admin_export_download_grants AS download_grant
SET state = 'consumed',
    consumed_at = pg_catalog.clock_timestamp(),
    version = download_grant.version + 1
FROM admin_export_jobs AS export
WHERE download_grant.token_key_version = sqlc.arg(token_key_version)
  AND download_grant.token_digest = sqlc.arg(token_digest)
  AND download_grant.state = 'active'
  AND download_grant.created_at <= pg_catalog.clock_timestamp()
  AND download_grant.expires_at > pg_catalog.clock_timestamp()
  AND download_grant.actor_admin_id = sqlc.arg(actor_admin_id)
  AND download_grant.session_id = sqlc.arg(session_id)
  AND export.export_id = download_grant.export_id
  AND export.version = download_grant.expected_export_version
  AND export.masking_policy = download_grant.masking_policy
  AND export.state IN ('succeeded', 'partially_succeeded')
  AND export.result_expires_at > pg_catalog.clock_timestamp()
RETURNING download_grant.*, export.result_object_key, export.result_digest,
          export.result_key_version, export.result_schema_version, export.result_expires_at,
          export.version AS export_version;
