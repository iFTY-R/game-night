-- name: GetUserProfile :one
SELECT user_id, real_name_ciphertext, real_name_nonce, real_name_key_version,
       profile_version, real_name_updated_at, real_name_updated_by
FROM user_profiles
WHERE user_id = sqlc.arg(user_id);

-- name: CreateUserProfile :one
INSERT INTO user_profiles (
    user_id,
    real_name_ciphertext,
    real_name_nonce,
    real_name_key_version,
    profile_version,
    real_name_updated_at,
    real_name_updated_by
) VALUES (
    sqlc.arg(user_id),
    sqlc.arg(real_name_ciphertext),
    sqlc.arg(real_name_nonce),
    sqlc.arg(real_name_key_version),
    1,
    sqlc.arg(updated_at),
    sqlc.arg(updated_by)
)
ON CONFLICT (user_id) DO NOTHING
RETURNING user_id, real_name_ciphertext, real_name_nonce, real_name_key_version,
          profile_version, real_name_updated_at, real_name_updated_by;

-- name: UpdateUserProfileCAS :one
UPDATE user_profiles
SET real_name_ciphertext = sqlc.arg(real_name_ciphertext),
    real_name_nonce = sqlc.arg(real_name_nonce),
    real_name_key_version = sqlc.arg(real_name_key_version),
    profile_version = profile_version + 1,
    real_name_updated_at = sqlc.arg(updated_at),
    real_name_updated_by = sqlc.arg(updated_by)
WHERE user_id = sqlc.arg(user_id)
  AND profile_version = sqlc.arg(expected_profile_version)
RETURNING user_id, real_name_ciphertext, real_name_nonce, real_name_key_version,
          profile_version, real_name_updated_at, real_name_updated_by;

-- name: CreateProfileExportContext :one
INSERT INTO profile_export_contexts (
    export_id,
    created_by_admin_id,
    filter_digest,
    requested_fields,
    schema_version,
    item_count,
    status,
    reason,
    created_at,
    expires_at
) VALUES (
    sqlc.arg(export_id),
    sqlc.arg(created_by_admin_id),
    sqlc.arg(filter_digest),
    sqlc.arg(requested_fields),
    sqlc.arg(schema_version),
    sqlc.arg(item_count),
    'active',
    sqlc.arg(reason),
    sqlc.arg(created_at),
    sqlc.arg(expires_at)
)
RETURNING export_id, created_by_admin_id, filter_digest, requested_fields, schema_version,
          item_count, status, reason, created_at, expires_at, completed_at, aborted_at, expired_at;

-- name: CreateProfileExportItem :one
INSERT INTO profile_export_items (
    export_id,
    ordinal,
    user_id,
    username,
    profile_version,
    real_name_ciphertext,
    real_name_nonce,
    real_name_key_version
) VALUES (
    sqlc.arg(export_id),
    sqlc.arg(ordinal),
    sqlc.arg(user_id),
    sqlc.arg(username),
    sqlc.narg(profile_version),
    sqlc.narg(real_name_ciphertext),
    sqlc.narg(real_name_nonce),
    sqlc.narg(real_name_key_version)
)
RETURNING export_id, ordinal, user_id, username, profile_version,
          real_name_ciphertext, real_name_nonce, real_name_key_version;

-- name: GetProfileExportContextForUpdate :one
SELECT export_id, created_by_admin_id, filter_digest, requested_fields, schema_version,
       item_count, status, reason, created_at, expires_at, completed_at, aborted_at, expired_at
FROM profile_export_contexts
WHERE export_id = sqlc.arg(export_id)
FOR UPDATE;

-- name: ListProfileExportItems :many
SELECT export_id, ordinal, user_id, username, profile_version,
       real_name_ciphertext, real_name_nonce, real_name_key_version
FROM profile_export_items
WHERE export_id = sqlc.arg(export_id)
  AND ordinal > sqlc.arg(after_ordinal)
ORDER BY ordinal
LIMIT sqlc.arg(page_size);

-- name: CompleteProfileExportContextCAS :one
UPDATE profile_export_contexts
SET status = 'completed',
    completed_at = sqlc.arg(completed_at)
WHERE export_id = sqlc.arg(export_id)
  AND created_by_admin_id = sqlc.arg(created_by_admin_id)
  AND status = 'active'
  AND expires_at > sqlc.arg(completed_at)
RETURNING export_id, status, completed_at;

-- name: AbortProfileExportContextCAS :one
UPDATE profile_export_contexts
SET status = 'aborted',
    aborted_at = sqlc.arg(aborted_at)
WHERE export_id = sqlc.arg(export_id)
  AND created_by_admin_id = sqlc.arg(created_by_admin_id)
  AND status = 'active'
RETURNING export_id, status, aborted_at;

-- name: ExpireProfileExportContextCAS :one
UPDATE profile_export_contexts
SET status = 'expired',
    expired_at = sqlc.arg(expired_at)
WHERE export_id = sqlc.arg(export_id)
  AND status = 'active'
  AND expires_at <= sqlc.arg(expired_at)
RETURNING export_id, status, expired_at;
