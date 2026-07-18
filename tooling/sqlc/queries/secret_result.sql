-- name: CreateSecretOperationResult :one
INSERT INTO secret_operation_results (
    result_id,
    operation_scope,
    actor_or_challenge_id,
    operation_id,
    request_digest,
    result_type,
    result_version,
    ciphertext,
    nonce,
    wrapped_data_key,
    key_version,
    status,
    secret_expires_at,
    completed_at,
    tombstone_expires_at
) VALUES (
    sqlc.arg(result_id),
    sqlc.arg(operation_scope),
    sqlc.arg(actor_or_challenge_id),
    sqlc.arg(operation_id),
    sqlc.arg(request_digest),
    sqlc.arg(result_type),
    sqlc.arg(result_version),
    sqlc.arg(ciphertext),
    sqlc.arg(nonce),
    sqlc.arg(wrapped_data_key),
    sqlc.arg(key_version),
    'available',
    sqlc.arg(secret_expires_at),
    sqlc.arg(completed_at),
    sqlc.arg(tombstone_expires_at)
)
ON CONFLICT (operation_scope, actor_or_challenge_id, operation_id) DO NOTHING
RETURNING result_id, operation_scope, actor_or_challenge_id, operation_id, request_digest,
          result_type, result_version, ciphertext, nonce, wrapped_data_key, key_version,
          status, secret_expires_at, confirmed_at, completed_at, tombstone_expires_at;

-- name: GetSecretOperationResultByOperation :one
SELECT result_id, operation_scope, actor_or_challenge_id, operation_id, request_digest,
       result_type, result_version, ciphertext, nonce, wrapped_data_key, key_version,
       status, secret_expires_at, confirmed_at, completed_at, tombstone_expires_at
FROM secret_operation_results
WHERE operation_scope = sqlc.arg(operation_scope)
  AND actor_or_challenge_id = sqlc.arg(actor_or_challenge_id)
  AND operation_id = sqlc.arg(operation_id);

-- name: ConfirmSecretOperationResultCAS :one
UPDATE secret_operation_results
SET status = 'confirmed',
    ciphertext = NULL,
    nonce = NULL,
    wrapped_data_key = NULL,
    confirmed_at = sqlc.arg(confirmed_at)
WHERE result_id = sqlc.arg(result_id)
  AND operation_scope = sqlc.arg(operation_scope)
  AND actor_or_challenge_id = sqlc.arg(actor_or_challenge_id)
  AND operation_id = sqlc.arg(operation_id)
  AND request_digest = sqlc.arg(request_digest)
  AND result_type = sqlc.arg(result_type)
  AND status = 'available'
  AND secret_expires_at > sqlc.arg(confirmed_at)
RETURNING result_id, status, confirmed_at, tombstone_expires_at;

-- name: ExpireSecretOperationResultCAS :one
UPDATE secret_operation_results
SET status = 'expired',
    ciphertext = NULL,
    nonce = NULL,
    wrapped_data_key = NULL
WHERE result_id = sqlc.arg(result_id)
  AND status = 'available'
  AND secret_expires_at <= sqlc.arg(expired_at)
RETURNING result_id, status, tombstone_expires_at;

-- name: CreateAnonymousChallenge :one
INSERT INTO anonymous_challenges (
    challenge_id,
    selector,
    secret_hash,
    secret_key_version,
    purpose,
    audience,
    origin_hash,
    request_flow_id,
    attempt_count,
    max_attempts,
    created_at,
    expires_at
) VALUES (
    sqlc.arg(challenge_id),
    sqlc.arg(selector),
    sqlc.arg(secret_hash),
    sqlc.arg(secret_key_version),
    sqlc.arg(purpose),
    sqlc.arg(audience),
    sqlc.arg(origin_hash),
    sqlc.arg(request_flow_id),
    0,
    sqlc.arg(max_attempts),
    sqlc.arg(created_at),
    sqlc.arg(expires_at)
)
RETURNING challenge_id, selector, secret_hash, secret_key_version, purpose, audience,
          origin_hash, request_flow_id, attempt_count, max_attempts, created_at, expires_at,
          consumed_at, replay_until, operation_id, request_digest, result_id;

-- name: GetAnonymousChallengeForUpdate :one
SELECT challenge_id, selector, secret_hash, secret_key_version, purpose, audience,
       origin_hash, request_flow_id, attempt_count, max_attempts, created_at, expires_at,
       consumed_at, replay_until, operation_id, request_digest, result_id
FROM anonymous_challenges
WHERE selector = sqlc.arg(selector)
FOR UPDATE;

-- name: RecordAnonymousChallengeFailureCAS :one
UPDATE anonymous_challenges
SET attempt_count = attempt_count + 1
WHERE challenge_id = sqlc.arg(challenge_id)
  AND consumed_at IS NULL
  AND expires_at > sqlc.arg(attempted_at)
  AND attempt_count < max_attempts
RETURNING challenge_id, attempt_count, max_attempts, expires_at;

-- name: ConsumeAnonymousChallengeCAS :one
UPDATE anonymous_challenges
SET consumed_at = sqlc.arg(consumed_at),
    replay_until = sqlc.arg(replay_until),
    operation_id = sqlc.arg(operation_id),
    request_digest = sqlc.arg(request_digest),
    result_id = sqlc.arg(result_id)
WHERE challenge_id = sqlc.arg(challenge_id)
  AND consumed_at IS NULL
  AND expires_at > sqlc.arg(consumed_at)
  AND attempt_count < max_attempts
RETURNING challenge_id, consumed_at, replay_until, operation_id, request_digest, result_id;

-- name: CreateUserRecoveryAttempt :one
INSERT INTO user_recovery_attempts (
    recovery_attempt_id,
    grant_selector,
    grant_secret_hash,
    grant_key_version,
    user_id,
    recovery_credential_id,
    recovery_credential_version,
    assisted_grant_id,
    challenge_id,
    origin_hash,
    purpose,
    request_digest,
    attempt_count,
    max_attempts,
    status,
    created_at,
    expires_at
) VALUES (
    sqlc.arg(recovery_attempt_id),
    sqlc.arg(grant_selector),
    sqlc.arg(grant_secret_hash),
    sqlc.arg(grant_key_version),
    sqlc.arg(user_id),
    sqlc.narg(recovery_credential_id),
    sqlc.narg(recovery_credential_version),
    sqlc.narg(assisted_grant_id),
    sqlc.arg(challenge_id),
    sqlc.arg(origin_hash),
    sqlc.arg(purpose),
    sqlc.narg(request_digest),
    0,
    sqlc.arg(max_attempts),
    'active',
    sqlc.arg(created_at),
    sqlc.arg(expires_at)
)
RETURNING recovery_attempt_id, grant_selector, grant_secret_hash, grant_key_version,
          user_id, recovery_credential_id, recovery_credential_version, assisted_grant_id,
          challenge_id, origin_hash, purpose, request_digest, attempt_count, max_attempts,
          status, created_at, expires_at, consumed_at, revoked_at, result_id;

-- name: GetUserRecoveryAttemptForUpdate :one
SELECT recovery_attempt_id, grant_selector, grant_secret_hash, grant_key_version,
       user_id, recovery_credential_id, recovery_credential_version, assisted_grant_id,
       challenge_id, origin_hash, purpose, request_digest, attempt_count, max_attempts,
       status, created_at, expires_at, consumed_at, revoked_at, result_id
FROM user_recovery_attempts
WHERE grant_selector = sqlc.arg(grant_selector)
FOR UPDATE;

-- name: RecordUserRecoveryAttemptFailureCAS :one
UPDATE user_recovery_attempts
SET attempt_count = attempt_count + 1
WHERE recovery_attempt_id = sqlc.arg(recovery_attempt_id)
  AND status = 'active'
  AND expires_at > sqlc.arg(attempted_at)
  AND attempt_count < max_attempts
RETURNING recovery_attempt_id, attempt_count, max_attempts, status, expires_at;

-- name: ConsumeUserRecoveryAttemptCAS :one
UPDATE user_recovery_attempts
SET status = 'consumed',
    consumed_at = sqlc.arg(consumed_at),
    result_id = sqlc.arg(result_id)
WHERE recovery_attempt_id = sqlc.arg(recovery_attempt_id)
  AND status = 'active'
  AND expires_at > sqlc.arg(consumed_at)
  AND attempt_count < max_attempts
RETURNING recovery_attempt_id, user_id, status, consumed_at, result_id;

-- name: RevokeUserRecoveryAttemptCAS :one
UPDATE user_recovery_attempts
SET status = 'revoked',
    revoked_at = sqlc.arg(revoked_at)
WHERE recovery_attempt_id = sqlc.arg(recovery_attempt_id)
  AND status = 'active'
RETURNING recovery_attempt_id, user_id, status, revoked_at;
