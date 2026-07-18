-- name: CreateKeyRotationJob :one
INSERT INTO key_rotation_jobs (
    job_id,
    purpose,
    source_key_version,
    target_key_version,
    status,
    cursor_scope,
    processed_count,
    conflict_count,
    created_at,
    updated_at
) VALUES (
    sqlc.arg(job_id),
    sqlc.arg(purpose),
    sqlc.arg(source_key_version),
    sqlc.arg(target_key_version),
    'pending',
    sqlc.arg(cursor_scope),
    0,
    0,
    sqlc.arg(created_at),
    sqlc.arg(created_at)
)
ON CONFLICT (purpose) WHERE status IN ('pending', 'running') DO NOTHING
RETURNING job_id, purpose, source_key_version, target_key_version, status, cursor_scope,
          cursor_id, cursor_ordinal, processed_count, conflict_count, lease_owner,
          lease_until, last_error_code, created_at, started_at, updated_at, completed_at;

-- name: GetKeyRotationJob :one
SELECT job_id, purpose, source_key_version, target_key_version, status, cursor_scope,
       cursor_id, cursor_ordinal, processed_count, conflict_count, lease_owner,
       lease_until, last_error_code, created_at, started_at, updated_at, completed_at
FROM key_rotation_jobs
WHERE job_id = sqlc.arg(job_id);

-- name: AcquireKeyRotationJobLease :one
WITH candidate AS (
    SELECT job_id
    FROM key_rotation_jobs
    WHERE status IN ('pending', 'running')
      AND (lease_owner IS NULL OR lease_until <= sqlc.arg(acquired_at))
    ORDER BY created_at, job_id
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE key_rotation_jobs AS job
SET status = 'running',
    started_at = COALESCE(job.started_at, sqlc.arg(acquired_at)),
    lease_owner = sqlc.arg(lease_owner),
    lease_until = sqlc.arg(lease_until),
    updated_at = sqlc.arg(acquired_at)
FROM candidate
WHERE job.job_id = candidate.job_id
RETURNING job.job_id, job.purpose, job.source_key_version, job.target_key_version,
          job.status, job.cursor_scope, job.cursor_id, job.cursor_ordinal,
          job.processed_count, job.conflict_count, job.lease_owner, job.lease_until,
          job.last_error_code, job.created_at, job.started_at, job.updated_at, job.completed_at;

-- name: RenewKeyRotationJobLeaseCAS :one
UPDATE key_rotation_jobs
SET lease_until = sqlc.arg(lease_until),
    updated_at = sqlc.arg(renewed_at)
WHERE job_id = sqlc.arg(job_id)
  AND status = 'running'
  AND lease_owner = sqlc.arg(lease_owner)
  AND lease_until > sqlc.arg(renewed_at)
RETURNING job_id, status, cursor_scope, cursor_id, cursor_ordinal, lease_owner,
          lease_until, updated_at;

-- name: ListUserProfilesForKeyRotation :many
SELECT user_id, real_name_ciphertext, real_name_nonce, real_name_key_version,
       profile_version, real_name_updated_at, real_name_updated_by
FROM user_profiles
WHERE real_name_key_version = sqlc.arg(source_key_version)
  AND (sqlc.narg(after_user_id)::uuid IS NULL OR user_id > sqlc.narg(after_user_id)::uuid)
ORDER BY user_id
LIMIT sqlc.arg(batch_size);

-- name: RotateUserProfileCiphertextCAS :one
UPDATE user_profiles
SET real_name_ciphertext = sqlc.arg(real_name_ciphertext),
    real_name_nonce = sqlc.arg(real_name_nonce),
    real_name_key_version = sqlc.arg(target_key_version)
WHERE user_id = sqlc.arg(user_id)
  AND profile_version = sqlc.arg(expected_profile_version)
  AND real_name_key_version = sqlc.arg(source_key_version)
RETURNING user_id, real_name_key_version, profile_version;

-- name: ListProfileExportItemsForKeyRotation :many
SELECT export_id, ordinal, user_id, profile_version, real_name_ciphertext,
       real_name_nonce, real_name_key_version
FROM profile_export_items
WHERE real_name_key_version = sqlc.arg(source_key_version)
  AND (
      sqlc.narg(after_export_id)::uuid IS NULL
      OR (export_id, ordinal) > (
          sqlc.narg(after_export_id)::uuid,
          sqlc.narg(after_ordinal)::bigint
      )
  )
ORDER BY export_id, ordinal
LIMIT sqlc.arg(batch_size);

-- name: RotateProfileExportItemCiphertextCAS :one
UPDATE profile_export_items
SET real_name_ciphertext = sqlc.arg(real_name_ciphertext),
    real_name_nonce = sqlc.arg(real_name_nonce),
    real_name_key_version = sqlc.arg(target_key_version)
WHERE export_id = sqlc.arg(export_id)
  AND ordinal = sqlc.arg(ordinal)
  AND real_name_key_version = sqlc.arg(source_key_version)
RETURNING export_id, ordinal, user_id, real_name_key_version;

-- name: ListAdminTotpEnrollmentsForKeyRotation :many
SELECT enrollment_id, admin_id, ciphertext, nonce, key_version, status, admin_version,
       operation_id, created_at, expires_at, activated_at, disabled_at
FROM admin_totp_enrollments
WHERE key_version = sqlc.arg(source_key_version)
  AND status IN ('pending', 'active')
  AND (sqlc.narg(after_enrollment_id)::uuid IS NULL OR enrollment_id > sqlc.narg(after_enrollment_id)::uuid)
ORDER BY enrollment_id
LIMIT sqlc.arg(batch_size);

-- name: RotateAdminTotpEnrollmentCiphertextCAS :one
UPDATE admin_totp_enrollments
SET ciphertext = sqlc.arg(ciphertext),
    nonce = sqlc.arg(nonce),
    key_version = sqlc.arg(target_key_version)
WHERE enrollment_id = sqlc.arg(enrollment_id)
  AND admin_version = sqlc.arg(expected_admin_version)
  AND key_version = sqlc.arg(source_key_version)
  AND status IN ('pending', 'active')
RETURNING enrollment_id, admin_id, key_version, status, admin_version;

-- name: AdvanceKeyRotationCursorCAS :one
UPDATE key_rotation_jobs
SET cursor_scope = sqlc.arg(next_cursor_scope),
    cursor_id = sqlc.narg(next_cursor_id),
    cursor_ordinal = sqlc.narg(next_cursor_ordinal),
    processed_count = processed_count + sqlc.arg(processed_delta),
    conflict_count = conflict_count + sqlc.arg(conflict_delta),
    updated_at = sqlc.arg(advanced_at)
WHERE job_id = sqlc.arg(job_id)
  AND status = 'running'
  AND lease_owner = sqlc.arg(lease_owner)
  AND lease_until > sqlc.arg(advanced_at)
  AND cursor_scope = sqlc.arg(expected_cursor_scope)
  AND cursor_id IS NOT DISTINCT FROM sqlc.narg(expected_cursor_id)::uuid
  AND cursor_ordinal IS NOT DISTINCT FROM sqlc.narg(expected_cursor_ordinal)::bigint
RETURNING job_id, status, cursor_scope, cursor_id, cursor_ordinal, processed_count,
          conflict_count, lease_owner, lease_until, updated_at;

-- name: CompleteKeyRotationJobCAS :one
UPDATE key_rotation_jobs
SET status = 'completed',
    lease_owner = NULL,
    lease_until = NULL,
    completed_at = sqlc.arg(completed_at),
    updated_at = sqlc.arg(completed_at)
WHERE job_id = sqlc.arg(job_id)
  AND status = 'running'
  AND lease_owner = sqlc.arg(lease_owner)
  AND lease_until > sqlc.arg(completed_at)
RETURNING job_id, status, processed_count, conflict_count, completed_at;

-- name: FailKeyRotationJobCAS :one
UPDATE key_rotation_jobs
SET status = 'failed',
    lease_owner = NULL,
    lease_until = NULL,
    last_error_code = sqlc.arg(error_code),
    updated_at = sqlc.arg(failed_at)
WHERE job_id = sqlc.arg(job_id)
  AND status = 'running'
  AND lease_owner = sqlc.arg(lease_owner)
RETURNING job_id, status, processed_count, conflict_count, last_error_code, updated_at;

-- name: CountPIIKeyReferences :one
SELECT (
    (SELECT count(*) FROM user_profiles AS profile WHERE profile.real_name_key_version = sqlc.arg(key_version))
    +
    (SELECT count(*) FROM profile_export_items AS item WHERE item.real_name_key_version = sqlc.arg(key_version))
)::bigint AS reference_count;

-- name: CountTotpKeyReferences :one
SELECT count(*)::bigint AS reference_count
FROM admin_totp_enrollments
WHERE key_version = sqlc.arg(key_version)
  AND status IN ('pending', 'active');
