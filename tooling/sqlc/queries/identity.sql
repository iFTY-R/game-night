-- name: CreateUser :one
INSERT INTO users (
    user_id,
    status,
    created_at,
    updated_at
) VALUES (
    sqlc.arg(user_id),
    'onboarding',
    sqlc.arg(created_at),
    sqlc.arg(created_at)
)
RETURNING user_id, status, username, current_username_key, username_changed_at, created_at, updated_at;

-- name: GetUserByID :one
SELECT user_id, status, username, current_username_key, username_changed_at, created_at, updated_at
FROM users
WHERE user_id = sqlc.arg(user_id);

-- name: GetUserForUpdate :one
SELECT user_id, status, username, current_username_key, username_changed_at, created_at, updated_at
FROM users
WHERE user_id = sqlc.arg(user_id)
FOR UPDATE;

-- name: GetDeviceIdentityForUpdate :one
WITH selected_device AS MATERIALIZED (
    SELECT selected.user_id
    FROM device_credentials AS selected
    WHERE selected.credential_id = sqlc.arg(target_credential_id)
),
locked_user AS MATERIALIZED (
    SELECT u.user_id, u.status, u.username, u.current_username_key,
           u.username_changed_at, u.created_at, u.updated_at
    FROM users AS u
    JOIN selected_device AS selected ON selected.user_id = u.user_id
    FOR UPDATE OF u
),
locked_device AS MATERIALIZED (
    SELECT d.credential_id, d.user_id, d.secret_hash, d.secret_key_version,
           d.previous_secret_hash, d.previous_secret_key_version, d.previous_valid_until,
           d.csrf_hash, d.generation, d.label, d.created_at, d.last_seen_at, d.rotated_at,
           d.idle_expires_at, d.absolute_expires_at, d.revoked_at, d.revoke_reason
    FROM device_credentials AS d
    JOIN locked_user AS locked ON locked.user_id = d.user_id
    WHERE d.credential_id = sqlc.arg(target_credential_id)
    FOR UPDATE OF d
)
SELECT u.user_id AS user_id,
       u.status AS user_status,
       u.username AS user_username,
       u.current_username_key AS user_current_username_key,
       u.username_changed_at AS user_username_changed_at,
       u.created_at AS user_created_at,
       u.updated_at AS user_updated_at,
       d.credential_id,
       d.secret_hash,
       d.secret_key_version,
       d.previous_secret_hash,
       d.previous_secret_key_version,
       d.previous_valid_until,
       d.csrf_hash,
       d.generation,
       d.label,
       d.created_at AS device_created_at,
       d.last_seen_at,
       d.rotated_at,
       d.idle_expires_at,
       d.absolute_expires_at,
       d.revoked_at,
       d.revoke_reason
FROM locked_user AS u
JOIN locked_device AS d ON d.user_id = u.user_id;

-- name: ClaimUsername :one
INSERT INTO username_claims (
    username_key,
    display_username,
    status,
    owner_user_id,
    reserved_until,
    created_at,
    updated_at
) VALUES (
    sqlc.arg(username_key),
    sqlc.arg(display_username),
    'active',
    sqlc.arg(owner_user_id),
    NULL,
    sqlc.arg(claimed_at),
    sqlc.arg(claimed_at)
)
ON CONFLICT (username_key) DO UPDATE
SET display_username = EXCLUDED.display_username,
    status = 'active',
    owner_user_id = EXCLUDED.owner_user_id,
    reserved_until = NULL,
    updated_at = EXCLUDED.updated_at
WHERE username_claims.status = 'reserved'
  AND username_claims.reserved_until <= sqlc.arg(claimed_at)
RETURNING username_key, display_username, status, owner_user_id, reserved_until, created_at, updated_at;

-- name: GetUsernameClaimForUpdate :one
SELECT username_key, display_username, status, owner_user_id, reserved_until, created_at, updated_at
FROM username_claims
WHERE username_key = sqlc.arg(username_key)
FOR UPDATE;

-- name: CompleteOnboardingUserCAS :one
UPDATE users
SET status = 'active',
    username = sqlc.arg(display_username),
    current_username_key = sqlc.arg(username_key),
    username_changed_at = sqlc.arg(changed_at),
    updated_at = sqlc.arg(changed_at)
WHERE user_id = sqlc.arg(user_id)
  AND status = 'onboarding'
  AND username IS NULL
  AND current_username_key IS NULL
  AND username_changed_at IS NULL
  AND updated_at = sqlc.arg(expected_updated_at)
  AND created_at = sqlc.arg(expected_created_at)
  AND created_at <= sqlc.arg(changed_at)
  AND sqlc.arg(changed_at) >= sqlc.arg(expected_updated_at)
  AND created_at > sqlc.arg(changed_at) - INTERVAL '86400 seconds'
RETURNING user_id, status, username, current_username_key, username_changed_at, created_at, updated_at;

-- name: ChangeCurrentUsernameCAS :one
UPDATE users
SET username = sqlc.arg(display_username),
    current_username_key = sqlc.arg(username_key),
    username_changed_at = sqlc.arg(changed_at),
    updated_at = sqlc.arg(changed_at)
WHERE user_id = sqlc.arg(user_id)
  AND status = 'active'
  AND username = sqlc.arg(expected_display_username)
  AND current_username_key = sqlc.arg(expected_username_key)
  AND username_changed_at = sqlc.arg(expected_username_changed_at)
  AND updated_at = sqlc.arg(expected_updated_at)
  AND username_changed_at <= sqlc.arg(cooldown_cutoff)
RETURNING user_id, status, username, current_username_key, username_changed_at, created_at, updated_at;

-- name: SetCurrentUsernameCAS :one
UPDATE users
SET status = sqlc.arg(next_status),
    username = sqlc.arg(display_username),
    current_username_key = sqlc.arg(username_key),
    username_changed_at = sqlc.arg(changed_at),
    updated_at = sqlc.arg(changed_at)
WHERE user_id = sqlc.arg(user_id)
  AND status = sqlc.arg(expected_status)
  AND current_username_key IS NOT DISTINCT FROM sqlc.narg(expected_username_key)::text
RETURNING user_id, status, username, current_username_key, username_changed_at, created_at, updated_at;

-- name: ReserveUsernameClaimCAS :one
UPDATE username_claims
SET status = 'reserved',
    reserved_until = sqlc.arg(reserved_until),
    updated_at = sqlc.arg(changed_at)
WHERE username_key = sqlc.arg(username_key)
  AND owner_user_id = sqlc.arg(owner_user_id)
  AND status = 'active'
RETURNING username_key, display_username, status, owner_user_id, reserved_until, created_at, updated_at;

-- name: CreateDeviceCredential :one
INSERT INTO device_credentials (
    credential_id,
    user_id,
    secret_hash,
    secret_key_version,
    csrf_hash,
    generation,
    label,
    created_at,
    last_seen_at,
    rotated_at,
    idle_expires_at,
    absolute_expires_at
) VALUES (
    sqlc.arg(credential_id),
    sqlc.arg(user_id),
    sqlc.arg(secret_hash),
    sqlc.arg(secret_key_version),
    sqlc.arg(csrf_hash),
    1,
    sqlc.arg(label),
    sqlc.arg(created_at),
    sqlc.arg(created_at),
    sqlc.arg(created_at),
    sqlc.arg(idle_expires_at),
    sqlc.arg(absolute_expires_at)
)
RETURNING credential_id, user_id, secret_hash, secret_key_version, previous_secret_hash,
          previous_secret_key_version, previous_valid_until, csrf_hash, generation, label,
          created_at, last_seen_at, rotated_at, idle_expires_at, absolute_expires_at,
          revoked_at, revoke_reason;

-- name: GetDeviceCredentialForUpdate :one
SELECT credential_id, user_id, secret_hash, secret_key_version, previous_secret_hash,
       previous_secret_key_version, previous_valid_until, csrf_hash, generation, label,
       created_at, last_seen_at, rotated_at, idle_expires_at, absolute_expires_at,
       revoked_at, revoke_reason
FROM device_credentials
WHERE credential_id = sqlc.arg(credential_id)
FOR UPDATE;

-- name: RotateDeviceCredentialCAS :one
UPDATE device_credentials
SET previous_secret_hash = secret_hash,
    previous_secret_key_version = secret_key_version,
    previous_valid_until = sqlc.arg(previous_valid_until),
    secret_hash = sqlc.arg(secret_hash),
    secret_key_version = sqlc.arg(secret_key_version),
    csrf_hash = sqlc.arg(csrf_hash),
    generation = generation + 1,
    last_seen_at = sqlc.arg(rotated_at),
    rotated_at = sqlc.arg(rotated_at),
    idle_expires_at = sqlc.arg(idle_expires_at)
WHERE credential_id = sqlc.arg(credential_id)
  AND user_id = sqlc.arg(user_id)
  AND generation = sqlc.arg(expected_generation)
  AND secret_hash = sqlc.arg(expected_secret_hash)
  AND secret_key_version = sqlc.arg(expected_secret_key_version)
  AND previous_secret_hash IS NOT DISTINCT FROM sqlc.narg(expected_previous_secret_hash)::bytea
  AND previous_secret_key_version IS NOT DISTINCT FROM sqlc.narg(expected_previous_secret_key_version)::integer
  AND previous_valid_until IS NOT DISTINCT FROM sqlc.narg(expected_previous_valid_until)::timestamptz
  AND csrf_hash = sqlc.arg(expected_csrf_hash)
  AND last_seen_at = sqlc.arg(expected_last_seen_at)
  AND rotated_at = sqlc.arg(expected_rotated_at)
  AND idle_expires_at = sqlc.arg(expected_idle_expires_at)
  AND absolute_expires_at = sqlc.arg(expected_absolute_expires_at)
  AND revoked_at IS NULL
  AND absolute_expires_at > sqlc.arg(rotated_at)
RETURNING credential_id, user_id, secret_hash, secret_key_version, previous_secret_hash,
          previous_secret_key_version, previous_valid_until, csrf_hash, generation, label,
          created_at, last_seen_at, rotated_at, idle_expires_at, absolute_expires_at,
          revoked_at, revoke_reason;

-- name: TouchDeviceCredentialCAS :one
UPDATE device_credentials
SET last_seen_at = sqlc.arg(seen_at),
    idle_expires_at = LEAST(sqlc.arg(idle_expires_at)::timestamptz, absolute_expires_at)
WHERE credential_id = sqlc.arg(credential_id)
  AND generation = sqlc.arg(expected_generation)
  AND secret_hash = sqlc.arg(expected_secret_hash)
  AND secret_key_version = sqlc.arg(expected_secret_key_version)
  AND previous_secret_hash IS NOT DISTINCT FROM sqlc.narg(expected_previous_secret_hash)::bytea
  AND previous_secret_key_version IS NOT DISTINCT FROM sqlc.narg(expected_previous_secret_key_version)::integer
  AND previous_valid_until IS NOT DISTINCT FROM sqlc.narg(expected_previous_valid_until)::timestamptz
  AND csrf_hash = sqlc.arg(expected_csrf_hash)
  AND last_seen_at = sqlc.arg(expected_last_seen_at)
  AND rotated_at = sqlc.arg(expected_rotated_at)
  AND idle_expires_at = sqlc.arg(expected_idle_expires_at)
  AND absolute_expires_at = sqlc.arg(expected_absolute_expires_at)
  AND revoked_at IS NULL
  AND idle_expires_at > sqlc.arg(seen_at)
  AND absolute_expires_at > sqlc.arg(seen_at)
  AND last_seen_at < sqlc.arg(seen_at)
RETURNING credential_id, generation, last_seen_at, idle_expires_at, absolute_expires_at;

-- name: RevokeDeviceCredentialCAS :one
UPDATE device_credentials
SET revoked_at = sqlc.arg(revoked_at),
    revoke_reason = sqlc.arg(revoke_reason),
    generation = generation + 1
WHERE credential_id = sqlc.arg(credential_id)
  AND user_id = sqlc.arg(user_id)
  AND generation = sqlc.arg(expected_generation)
  AND revoked_at IS NULL
RETURNING credential_id, user_id, secret_hash, secret_key_version, previous_secret_hash,
          previous_secret_key_version, previous_valid_until, csrf_hash, generation, label,
          created_at, last_seen_at, rotated_at, idle_expires_at, absolute_expires_at,
          revoked_at, revoke_reason;

-- name: ListUserDeviceCredentials :many
SELECT credential_id, user_id, generation, label, created_at, last_seen_at, rotated_at,
       idle_expires_at, absolute_expires_at, revoked_at, revoke_reason
FROM device_credentials
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.arg(include_revoked)::boolean OR revoked_at IS NULL)
  AND (created_at, credential_id) > (
      sqlc.arg(after_created_at)::timestamptz,
      sqlc.arg(after_credential_id)::uuid
  )
ORDER BY created_at, credential_id
LIMIT sqlc.arg(page_size);

-- name: RevokeOtherDeviceCredentialsForRecovery :many
UPDATE device_credentials
SET revoked_at = sqlc.arg(revoked_at),
    revoke_reason = 'recovery',
    generation = generation + 1
WHERE user_id = sqlc.arg(user_id)
  AND credential_id <> sqlc.arg(preserved_credential_id)
  AND revoked_at IS NULL
RETURNING credential_id, user_id, generation, label, created_at, last_seen_at, rotated_at,
          idle_expires_at, absolute_expires_at, revoked_at, revoke_reason;

-- name: CreateUserRecoveryCredential :one
INSERT INTO user_recovery_credentials (
    recovery_credential_id,
    user_id,
    selector,
    secret_hash,
    version,
    status,
    created_at
) VALUES (
    sqlc.arg(recovery_credential_id),
    sqlc.arg(user_id),
    sqlc.arg(selector),
    sqlc.arg(secret_hash),
    sqlc.arg(version),
    'active',
    sqlc.arg(created_at)
)
RETURNING recovery_credential_id, user_id, selector, secret_hash, version, status,
          created_at, consumed_at, revoked_at, revoke_reason;

-- name: GetUserRecoveryCredentialBySelector :one
SELECT recovery_credential_id, user_id, selector, secret_hash, version, status,
       created_at, consumed_at, revoked_at, revoke_reason
FROM user_recovery_credentials
WHERE selector = sqlc.arg(selector);

-- name: GetUserRecoveryCredentialForUpdate :one
SELECT recovery_credential_id, user_id, selector, secret_hash, version, status,
       created_at, consumed_at, revoked_at, revoke_reason
FROM user_recovery_credentials
WHERE recovery_credential_id = sqlc.arg(recovery_credential_id)
  AND user_id = sqlc.arg(user_id)
  AND version = sqlc.arg(expected_version)
FOR UPDATE;

-- name: GetActiveUserRecoveryCredentialForUpdate :one
SELECT recovery_credential_id, user_id, selector, secret_hash, version, status,
       created_at, consumed_at, revoked_at, revoke_reason
FROM user_recovery_credentials
WHERE user_id = sqlc.arg(user_id)
  AND status = 'active'
FOR UPDATE;

-- name: ConsumeUserRecoveryCredentialCAS :one
UPDATE user_recovery_credentials
SET status = 'consumed',
    consumed_at = sqlc.arg(consumed_at)
WHERE recovery_credential_id = sqlc.arg(recovery_credential_id)
  AND user_id = sqlc.arg(user_id)
  AND version = sqlc.arg(expected_version)
  AND status = 'active'
RETURNING recovery_credential_id, user_id, selector, secret_hash, version, status,
          created_at, consumed_at, revoked_at, revoke_reason;

-- name: RevokeUserRecoveryCredentialCAS :one
UPDATE user_recovery_credentials
SET status = 'revoked',
    revoked_at = sqlc.arg(revoked_at),
    revoke_reason = sqlc.arg(revoke_reason)
WHERE recovery_credential_id = sqlc.arg(recovery_credential_id)
  AND user_id = sqlc.arg(user_id)
  AND version = sqlc.arg(expected_version)
  AND status = 'active'
RETURNING recovery_credential_id, user_id, selector, secret_hash, version, status,
          created_at, consumed_at, revoked_at, revoke_reason;

-- name: TransitionUserStatusCAS :one
UPDATE users
SET status = sqlc.arg(next_status),
    username = CASE WHEN sqlc.arg(next_status)::text = 'deleted' THEN NULL ELSE username END,
    current_username_key = CASE WHEN sqlc.arg(next_status)::text = 'deleted' THEN NULL ELSE current_username_key END,
    updated_at = sqlc.arg(changed_at)
WHERE user_id = sqlc.arg(user_id)
  AND status = sqlc.arg(expected_status)
RETURNING user_id, status, username, current_username_key, username_changed_at, created_at, updated_at;
