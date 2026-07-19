-- name: GetSingletonAdminForUpdate :one
SELECT singleton_id, admin_id, username, status, password_hash, password_algorithm,
       password_parameters, password_version, admin_version, last_accepted_totp_step,
       created_at, updated_at
FROM admin_accounts
WHERE singleton_id = 1
FOR UPDATE;

-- name: BootstrapAdminPasswordCAS :one
UPDATE admin_accounts
SET status = 'setup_required',
    password_hash = sqlc.arg(password_hash),
    password_algorithm = sqlc.arg(password_algorithm),
    password_parameters = sqlc.arg(password_parameters),
    password_version = password_version + 1,
    admin_version = admin_version + 1,
    updated_at = sqlc.arg(changed_at)
WHERE singleton_id = 1
  AND status = 'bootstrap_pending'
  AND password_hash IS NULL
  AND admin_version = sqlc.arg(expected_admin_version)
RETURNING singleton_id, admin_id, status, password_version, admin_version, updated_at;

-- name: UpdateAdminPasswordCAS :one
UPDATE admin_accounts
SET password_hash = sqlc.arg(password_hash),
    password_algorithm = sqlc.arg(password_algorithm),
    password_parameters = sqlc.arg(password_parameters),
    password_version = password_version + 1,
    admin_version = admin_version + 1,
    updated_at = sqlc.arg(changed_at)
WHERE singleton_id = 1
  AND admin_id = sqlc.arg(admin_id)
  AND status = sqlc.arg(expected_status)
  AND password_version = sqlc.arg(expected_password_version)
  AND admin_version = sqlc.arg(expected_admin_version)
RETURNING singleton_id, admin_id, status, password_version, admin_version, updated_at;

-- name: TransitionAdminStatusCAS :one
UPDATE admin_accounts
SET status = sqlc.arg(next_status),
    admin_version = admin_version + 1,
    updated_at = sqlc.arg(changed_at)
WHERE singleton_id = 1
  AND admin_id = sqlc.arg(admin_id)
  AND status = sqlc.arg(expected_status)
  AND admin_version = sqlc.arg(expected_admin_version)
RETURNING singleton_id, admin_id, status, password_version, admin_version, updated_at;

-- name: AcceptAdminTotpStepCAS :one
UPDATE admin_accounts
SET last_accepted_totp_step = sqlc.arg(totp_step),
    updated_at = sqlc.arg(accepted_at)
WHERE singleton_id = 1
  AND admin_id = sqlc.arg(admin_id)
  AND status IN ('setup_required', 'recovery_pending', 'active')
  AND admin_version = sqlc.arg(expected_admin_version)
  AND (last_accepted_totp_step IS NULL OR last_accepted_totp_step < sqlc.arg(totp_step))
RETURNING admin_id, admin_version, last_accepted_totp_step, updated_at;

-- name: CreateAdminChallenge :one
INSERT INTO admin_challenges (
    challenge_id,
    admin_id,
    selector,
    secret_hash,
    secret_key_version,
    purpose,
    audience,
    admin_version,
    password_version,
    origin_hash,
    request_flow_id,
    attempt_count,
    max_attempts,
    status,
    created_at,
    expires_at
) VALUES (
    sqlc.arg(challenge_id),
    sqlc.arg(admin_id),
    sqlc.arg(selector),
    sqlc.arg(secret_hash),
    sqlc.arg(secret_key_version),
    sqlc.arg(purpose),
    sqlc.arg(audience),
    sqlc.arg(admin_version),
    sqlc.arg(password_version),
    sqlc.arg(origin_hash),
    sqlc.arg(request_flow_id),
    0,
    sqlc.arg(max_attempts),
    'active',
    sqlc.arg(created_at),
    sqlc.arg(expires_at)
)
RETURNING challenge_id, admin_id, selector, secret_hash, secret_key_version, purpose,
          audience, admin_version, password_version, origin_hash, request_flow_id,
          attempt_count, max_attempts, status, created_at, expires_at, consumed_at,
          revoked_at, replay_until, operation_id, request_digest, result_id;

-- name: GetAdminChallengeForUpdate :one
WITH current_admin AS MATERIALIZED (
    -- Lock the account generation before the challenge to match every security-state transition.
    SELECT admin_id, admin_version, password_version
    FROM admin_accounts
    WHERE singleton_id = 1
    FOR UPDATE
)
SELECT challenge.challenge_id, challenge.admin_id, challenge.selector, challenge.secret_hash,
       challenge.secret_key_version, challenge.purpose, challenge.audience,
       challenge.admin_version, challenge.password_version, challenge.origin_hash,
       challenge.request_flow_id, challenge.attempt_count, challenge.max_attempts,
       challenge.status, challenge.created_at, challenge.expires_at, challenge.consumed_at,
       challenge.revoked_at, challenge.replay_until, challenge.operation_id,
       challenge.request_digest, challenge.result_id
FROM current_admin
JOIN admin_challenges AS challenge ON challenge.admin_id = current_admin.admin_id
WHERE challenge.selector = sqlc.arg(selector)
  AND challenge.admin_version = current_admin.admin_version
  AND challenge.password_version = current_admin.password_version
FOR UPDATE OF challenge;

-- name: RecordAdminChallengeFailureCAS :one
UPDATE admin_challenges
SET attempt_count = attempt_count + 1
WHERE challenge_id = sqlc.arg(challenge_id)
  AND status = 'active'
  AND expires_at > sqlc.arg(attempted_at)
  AND attempt_count < max_attempts
RETURNING challenge_id, attempt_count, max_attempts, status, expires_at;

-- name: ConsumeAdminChallengeCAS :one
WITH current_admin AS MATERIALIZED (
    -- Direct CAS callers must acquire the same account-first lock as GetAdminChallengeForUpdate.
    SELECT admin_id, admin_version, password_version
    FROM admin_accounts
    WHERE singleton_id = 1
    FOR UPDATE
)
UPDATE admin_challenges AS challenge
SET status = 'consumed',
    consumed_at = sqlc.arg(consumed_at),
    replay_until = sqlc.arg(replay_until),
    operation_id = sqlc.arg(operation_id),
    request_digest = sqlc.arg(request_digest),
    result_id = sqlc.arg(result_id)
FROM current_admin
WHERE challenge.challenge_id = sqlc.arg(challenge_id)
  AND challenge.admin_id = current_admin.admin_id
  AND challenge.status = 'active'
  AND challenge.expires_at > sqlc.arg(consumed_at)
  AND challenge.attempt_count < challenge.max_attempts
  AND challenge.admin_version = sqlc.arg(expected_admin_version)
  AND challenge.password_version = sqlc.arg(expected_password_version)
  AND challenge.admin_version = current_admin.admin_version
  AND challenge.password_version = current_admin.password_version
RETURNING challenge.challenge_id, challenge.status, challenge.consumed_at,
          challenge.replay_until, challenge.operation_id, challenge.request_digest,
          challenge.result_id;

-- name: RevokeAdminChallenges :execrows
UPDATE admin_challenges
SET status = 'revoked',
    revoked_at = sqlc.arg(revoked_at)
WHERE admin_id = sqlc.arg(admin_id)
  AND status = 'active';

-- name: CreatePendingAdminTotpEnrollment :one
INSERT INTO admin_totp_enrollments (
    enrollment_id,
    admin_id,
    ciphertext,
    nonce,
    key_version,
    status,
    admin_version,
    operation_id,
    created_at,
    expires_at
) VALUES (
    sqlc.arg(enrollment_id),
    sqlc.arg(admin_id),
    sqlc.arg(ciphertext),
    sqlc.arg(nonce),
    sqlc.arg(key_version),
    'pending',
    sqlc.arg(admin_version),
    sqlc.arg(operation_id),
    sqlc.arg(created_at),
    sqlc.arg(expires_at)
)
ON CONFLICT (admin_id) WHERE status = 'pending' DO NOTHING
RETURNING enrollment_id, admin_id, ciphertext, nonce, key_version, status, admin_version,
          operation_id, created_at, expires_at, activated_at, disabled_at;

-- name: GetPendingAdminTotpEnrollmentForUpdate :one
SELECT enrollment_id, admin_id, ciphertext, nonce, key_version, status, admin_version,
       operation_id, created_at, expires_at, activated_at, disabled_at
FROM admin_totp_enrollments
WHERE admin_id = sqlc.arg(admin_id)
  AND status = 'pending'
FOR UPDATE;

-- name: GetActiveAdminTotpEnrollmentForUpdate :one
SELECT enrollment_id, admin_id, ciphertext, nonce, key_version, status, admin_version,
       operation_id, created_at, expires_at, activated_at, disabled_at
FROM admin_totp_enrollments
WHERE admin_id = sqlc.arg(admin_id)
  AND status = 'active'
FOR UPDATE;

-- name: DisableActiveAdminTotpEnrollmentCAS :one
UPDATE admin_totp_enrollments
SET status = 'disabled',
    ciphertext = NULL,
    nonce = NULL,
    disabled_at = sqlc.arg(disabled_at)
WHERE admin_id = sqlc.arg(admin_id)
  AND enrollment_id = sqlc.arg(enrollment_id)
  AND status = 'active'
  AND admin_version = sqlc.arg(expected_admin_version)
RETURNING enrollment_id, admin_id, status, disabled_at;

-- name: ActivatePendingAdminTotpEnrollmentCAS :one
UPDATE admin_totp_enrollments
SET status = 'active',
    expires_at = NULL,
    activated_at = sqlc.arg(activated_at)
WHERE admin_id = sqlc.arg(admin_id)
  AND enrollment_id = sqlc.arg(enrollment_id)
  AND status = 'pending'
  AND admin_version = sqlc.arg(expected_admin_version)
  AND expires_at > sqlc.arg(activated_at)
RETURNING enrollment_id, admin_id, ciphertext, nonce, key_version, status, admin_version,
          operation_id, created_at, expires_at, activated_at, disabled_at;

-- name: CreateAdminSession :one
INSERT INTO admin_sessions (
    session_id,
    admin_id,
    selector,
    secret_hash,
    secret_key_version,
    csrf_hash,
    kind,
    admin_version,
    password_version,
    attempt_count,
    max_attempts,
    created_at,
    last_seen_at,
    idle_expires_at,
    absolute_expires_at
) VALUES (
    sqlc.arg(session_id),
    sqlc.arg(admin_id),
    sqlc.arg(selector),
    sqlc.arg(secret_hash),
    sqlc.arg(secret_key_version),
    sqlc.arg(csrf_hash),
    sqlc.arg(kind),
    sqlc.arg(admin_version),
    sqlc.arg(password_version),
    0,
    sqlc.arg(max_attempts),
    sqlc.arg(created_at),
    sqlc.arg(created_at),
    sqlc.arg(idle_expires_at),
    sqlc.arg(absolute_expires_at)
)
RETURNING session_id, admin_id, selector, secret_hash, secret_key_version, csrf_hash,
          kind, admin_version, password_version, attempt_count, max_attempts, created_at,
          last_seen_at, idle_expires_at, absolute_expires_at, revoked_at, revoke_reason;

-- name: GetAdminSessionForUpdate :one
SELECT session_id, admin_id, selector, secret_hash, secret_key_version, csrf_hash,
       kind, admin_version, password_version, attempt_count, max_attempts, created_at,
       last_seen_at, idle_expires_at, absolute_expires_at, revoked_at, revoke_reason
FROM admin_sessions
WHERE selector = sqlc.arg(selector)
FOR UPDATE;

-- name: GetAdminRecoveryCodeForUpdate :one
SELECT recovery_code_id, admin_id, selector, secret_hash, set_version, status,
       created_at, consumed_at, revoked_at
FROM admin_recovery_codes
WHERE selector = sqlc.arg(selector)
FOR UPDATE;

-- name: TouchAdminSessionCAS :one
UPDATE admin_sessions
SET last_seen_at = sqlc.arg(seen_at),
    idle_expires_at = LEAST(sqlc.arg(idle_expires_at)::timestamptz, absolute_expires_at)
WHERE session_id = sqlc.arg(session_id)
  AND admin_version = sqlc.arg(expected_admin_version)
  AND password_version = sqlc.arg(expected_password_version)
  AND revoked_at IS NULL
  AND idle_expires_at > sqlc.arg(seen_at)
  AND absolute_expires_at > sqlc.arg(seen_at)
RETURNING session_id, kind, last_seen_at, idle_expires_at, absolute_expires_at;

-- name: RevokeAdminSessionCAS :one
UPDATE admin_sessions
SET revoked_at = sqlc.arg(revoked_at),
    revoke_reason = sqlc.arg(revoke_reason)
WHERE session_id = sqlc.arg(session_id)
  AND revoked_at IS NULL
RETURNING session_id, revoked_at, revoke_reason;

-- name: RevokeAllAdminSessions :execrows
UPDATE admin_sessions
SET revoked_at = sqlc.arg(revoked_at),
    revoke_reason = sqlc.arg(revoke_reason)
WHERE admin_id = sqlc.arg(admin_id)
  AND revoked_at IS NULL;

-- name: CreateAdminRecoveryCode :one
INSERT INTO admin_recovery_codes (
    recovery_code_id,
    admin_id,
    selector,
    secret_hash,
    set_version,
    status,
    created_at
) VALUES (
    sqlc.arg(recovery_code_id),
    sqlc.arg(admin_id),
    sqlc.arg(selector),
    sqlc.arg(secret_hash),
    sqlc.arg(set_version),
    'active',
    sqlc.arg(created_at)
)
RETURNING recovery_code_id, admin_id, selector, secret_hash, set_version, status,
          created_at, consumed_at, revoked_at;

-- name: ConsumeAdminRecoveryCodeCAS :one
UPDATE admin_recovery_codes
SET status = 'consumed',
    consumed_at = sqlc.arg(consumed_at)
WHERE recovery_code_id = sqlc.arg(recovery_code_id)
  AND admin_id = sqlc.arg(admin_id)
  AND set_version = sqlc.arg(expected_set_version)
  AND status = 'active'
RETURNING recovery_code_id, admin_id, set_version, status, consumed_at;

-- name: RevokeAdminRecoveryCodeSet :execrows
UPDATE admin_recovery_codes
SET status = 'revoked',
    revoked_at = sqlc.arg(revoked_at)
WHERE admin_id = sqlc.arg(admin_id)
  AND set_version = sqlc.arg(set_version)
  AND status = 'active';

-- name: RevokeAllAdminRecoveryCodeSets :execrows
UPDATE admin_recovery_codes
SET status = 'revoked',
    revoked_at = sqlc.arg(revoked_at)
WHERE admin_id = sqlc.arg(admin_id)
  AND status = 'active';

-- name: CreateAdminAssistedRecoveryGrant :one
INSERT INTO admin_assisted_recovery_grants (
    assisted_grant_id,
    user_id,
    selector,
    secret_hash,
    purpose,
    status,
    attempt_count,
    max_attempts,
    created_by_admin_id,
    created_at,
    expires_at
) VALUES (
    sqlc.arg(assisted_grant_id),
    sqlc.arg(user_id),
    sqlc.arg(selector),
    sqlc.arg(secret_hash),
    sqlc.arg(purpose),
    'active',
    0,
    sqlc.arg(max_attempts),
    sqlc.arg(created_by_admin_id),
    sqlc.arg(created_at),
    sqlc.arg(expires_at)
)
RETURNING assisted_grant_id, user_id, selector, secret_hash, purpose, status,
          attempt_count, max_attempts, created_by_admin_id, created_at, expires_at,
          consumed_at, revoked_at, result_id;

-- name: GetAdminAssistedRecoveryGrantBySelector :one
SELECT assisted_grant_id, user_id, selector, secret_hash, purpose, status,
       attempt_count, max_attempts, created_by_admin_id, created_at, expires_at,
       consumed_at, revoked_at, result_id
FROM admin_assisted_recovery_grants
WHERE selector = sqlc.arg(selector);

-- name: GetAdminAssistedRecoveryGrantForUpdate :one
WITH locked_user AS MATERIALIZED (
    SELECT target.user_id
    FROM users AS target
    WHERE target.user_id = sqlc.arg(target_user_id)
    FOR UPDATE OF target
)
SELECT grants.assisted_grant_id, grants.user_id, grants.selector, grants.secret_hash,
       grants.purpose, grants.status, grants.attempt_count, grants.max_attempts,
       grants.created_by_admin_id, grants.created_at, grants.expires_at,
       grants.consumed_at, grants.revoked_at, grants.result_id
FROM admin_assisted_recovery_grants AS grants
JOIN locked_user AS users ON users.user_id = grants.user_id
WHERE grants.assisted_grant_id = sqlc.arg(assisted_grant_id)
FOR UPDATE OF grants;

-- name: RecordAdminAssistedRecoveryFailureCAS :one
UPDATE admin_assisted_recovery_grants
SET attempt_count = sqlc.arg(next_attempt_count),
    status = sqlc.arg(next_status)
WHERE assisted_grant_id = sqlc.arg(assisted_grant_id)
  AND status = 'active'
  AND attempt_count = sqlc.arg(expected_attempt_count)
RETURNING assisted_grant_id, user_id, selector, secret_hash, purpose, status,
          attempt_count, max_attempts, created_by_admin_id, created_at, expires_at,
          consumed_at, revoked_at, result_id;

-- name: ConsumeAdminAssistedRecoveryGrantCAS :one
UPDATE admin_assisted_recovery_grants
SET status = 'consumed',
    consumed_at = sqlc.arg(consumed_at),
    result_id = sqlc.arg(result_id)
WHERE assisted_grant_id = sqlc.arg(assisted_grant_id)
  AND user_id = sqlc.arg(user_id)
  AND status = 'active'
  AND attempt_count = sqlc.arg(expected_attempt_count)
  AND expires_at > sqlc.arg(consumed_at)
  AND attempt_count < max_attempts
RETURNING assisted_grant_id, user_id, selector, secret_hash, purpose, status,
          attempt_count, max_attempts, created_by_admin_id, created_at, expires_at,
          consumed_at, revoked_at, result_id;

-- name: RevokeActiveAdminAssistedRecoveryGrantsForUser :many
UPDATE admin_assisted_recovery_grants
SET status = 'revoked',
    revoked_at = sqlc.arg(revoked_at)
WHERE user_id = sqlc.arg(user_id)
  AND assisted_grant_id <> sqlc.arg(preserved_assisted_grant_id)
  AND status = 'active'
RETURNING assisted_grant_id;
