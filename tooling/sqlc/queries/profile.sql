-- name: GetUserProfile :one
SELECT user_id, real_name_ciphertext, real_name_nonce, real_name_key_version,
       profile_version, real_name_updated_at, real_name_updated_by
FROM user_profiles
WHERE user_id = sqlc.arg(user_id);

-- name: GetUserProfileForUpdate :one
SELECT user_id, real_name_ciphertext, real_name_nonce, real_name_key_version,
       profile_version, real_name_updated_at, real_name_updated_by
FROM user_profiles
WHERE user_id = sqlc.arg(user_id)
FOR UPDATE;

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
