-- +goose Up

CREATE TABLE admin_accounts (
    singleton_id smallint PRIMARY KEY CHECK (singleton_id = 1),
    admin_id uuid NOT NULL UNIQUE,
    username text NOT NULL UNIQUE CHECK (username = 'admin'),
    status text NOT NULL CHECK (status IN ('bootstrap_pending', 'setup_required', 'active', 'recovery_pending')),
    password_hash text,
    password_algorithm text,
    password_parameters text,
    password_version bigint NOT NULL DEFAULT 0 CHECK (password_version >= 0),
    admin_version bigint NOT NULL DEFAULT 1 CHECK (admin_version > 0),
    last_accepted_totp_step bigint,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (
        (status = 'bootstrap_pending' AND password_hash IS NULL AND password_algorithm IS NULL AND password_parameters IS NULL)
        OR (
            status <> 'bootstrap_pending'
            AND password_hash IS NOT NULL
            AND password_algorithm IS NOT NULL
            AND password_parameters IS NOT NULL
        )
    )
);

INSERT INTO admin_accounts (
    singleton_id,
    admin_id,
    username,
    status,
    created_at,
    updated_at
) VALUES (
    1,
    gen_random_uuid(),
    'admin',
    'bootstrap_pending',
    transaction_timestamp(),
    transaction_timestamp()
);

CREATE TABLE admin_challenges (
    challenge_id uuid PRIMARY KEY,
    admin_id uuid NOT NULL REFERENCES admin_accounts (admin_id),
    selector text NOT NULL UNIQUE,
    secret_hash bytea NOT NULL CHECK (octet_length(secret_hash) = 32),
    secret_key_version integer NOT NULL CHECK (secret_key_version > 0),
    purpose text NOT NULL,
    audience text NOT NULL,
    admin_version bigint NOT NULL CHECK (admin_version > 0),
    password_version bigint NOT NULL CHECK (password_version >= 0),
    origin_hash bytea NOT NULL CHECK (octet_length(origin_hash) = 32),
    request_flow_id text NOT NULL,
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    max_attempts integer NOT NULL CHECK (max_attempts > 0),
    status text NOT NULL CHECK (status IN ('active', 'consumed', 'expired', 'revoked')),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    revoked_at timestamptz,
    replay_until timestamptz,
    operation_id text,
    request_digest bytea,
    result_id uuid REFERENCES secret_operation_results (result_id),
    CHECK (expires_at > created_at),
    CHECK (request_digest IS NULL OR octet_length(request_digest) = 32),
    CHECK (
        (status = 'active' AND consumed_at IS NULL AND revoked_at IS NULL)
        OR (status = 'consumed' AND consumed_at IS NOT NULL AND revoked_at IS NULL)
        OR (status = 'expired' AND consumed_at IS NULL AND revoked_at IS NULL)
        OR (status = 'revoked' AND consumed_at IS NULL AND revoked_at IS NOT NULL)
    ),
    CHECK (
        (replay_until IS NULL AND operation_id IS NULL AND request_digest IS NULL AND result_id IS NULL)
        OR (
            status = 'consumed'
            AND replay_until IS NOT NULL
            AND operation_id IS NOT NULL
            AND request_digest IS NOT NULL
            AND result_id IS NOT NULL
        )
    )
);

CREATE INDEX admin_challenges_expiry_idx ON admin_challenges (expires_at)
    WHERE status = 'active';
CREATE INDEX admin_challenges_admin_idx ON admin_challenges (admin_id, purpose, created_at);

CREATE TABLE admin_totp_enrollments (
    enrollment_id uuid PRIMARY KEY,
    admin_id uuid NOT NULL REFERENCES admin_accounts (admin_id),
    ciphertext bytea,
    nonce bytea,
    key_version integer NOT NULL CHECK (key_version > 0),
    status text NOT NULL CHECK (status IN ('pending', 'active', 'disabled', 'expired')),
    admin_version bigint NOT NULL CHECK (admin_version > 0),
    operation_id text NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz,
    activated_at timestamptz,
    disabled_at timestamptz,
    CHECK (
        (status IN ('pending', 'active') AND ciphertext IS NOT NULL AND nonce IS NOT NULL)
        OR (status IN ('disabled', 'expired') AND ciphertext IS NULL AND nonce IS NULL)
    ),
    CHECK (
        (status = 'pending' AND expires_at IS NOT NULL AND activated_at IS NULL AND disabled_at IS NULL)
        OR (status = 'active' AND expires_at IS NULL AND activated_at IS NOT NULL AND disabled_at IS NULL)
        OR (status = 'disabled' AND disabled_at IS NOT NULL)
        OR (status = 'expired' AND expires_at IS NOT NULL)
    )
);

CREATE UNIQUE INDEX admin_totp_enrollments_one_pending_idx
    ON admin_totp_enrollments (admin_id)
    WHERE status = 'pending';
CREATE UNIQUE INDEX admin_totp_enrollments_one_active_idx
    ON admin_totp_enrollments (admin_id)
    WHERE status = 'active';
CREATE UNIQUE INDEX admin_totp_enrollments_operation_idx
    ON admin_totp_enrollments (admin_id, operation_id);

CREATE TABLE admin_sessions (
    session_id uuid PRIMARY KEY,
    admin_id uuid NOT NULL REFERENCES admin_accounts (admin_id),
    selector text NOT NULL UNIQUE,
    secret_hash bytea NOT NULL CHECK (octet_length(secret_hash) = 32),
    secret_key_version integer NOT NULL CHECK (secret_key_version > 0),
    csrf_hash bytea NOT NULL CHECK (octet_length(csrf_hash) = 32),
    kind text NOT NULL CHECK (kind IN ('setup_password_pending', 'totp_enrollment_pending', 'mfa_pending', 'recovery_pending', 'full')),
    admin_version bigint NOT NULL CHECK (admin_version > 0),
    password_version bigint NOT NULL CHECK (password_version >= 0),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    max_attempts integer NOT NULL CHECK (max_attempts > 0),
    created_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    idle_expires_at timestamptz NOT NULL,
    absolute_expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    revoke_reason text,
    CHECK (idle_expires_at <= absolute_expires_at),
    CHECK (
        (revoked_at IS NULL AND revoke_reason IS NULL)
        OR (revoked_at IS NOT NULL AND revoke_reason IS NOT NULL)
    )
);

CREATE INDEX admin_sessions_active_idx
    ON admin_sessions (admin_id, kind, absolute_expires_at)
    WHERE revoked_at IS NULL;
CREATE INDEX admin_sessions_expiry_idx
    ON admin_sessions (idle_expires_at, absolute_expires_at)
    WHERE revoked_at IS NULL;

CREATE TABLE admin_recovery_codes (
    recovery_code_id uuid PRIMARY KEY,
    admin_id uuid NOT NULL REFERENCES admin_accounts (admin_id),
    selector text NOT NULL UNIQUE,
    secret_hash text NOT NULL,
    set_version bigint NOT NULL CHECK (set_version > 0),
    status text NOT NULL CHECK (status IN ('active', 'consumed', 'revoked')),
    created_at timestamptz NOT NULL,
    consumed_at timestamptz,
    revoked_at timestamptz,
    CHECK (
        (status = 'active' AND consumed_at IS NULL AND revoked_at IS NULL)
        OR (status = 'consumed' AND consumed_at IS NOT NULL AND revoked_at IS NULL)
        OR (status = 'revoked' AND consumed_at IS NULL AND revoked_at IS NOT NULL)
    )
);

CREATE INDEX admin_recovery_codes_active_set_idx
    ON admin_recovery_codes (admin_id, set_version)
    WHERE status = 'active';

CREATE TABLE admin_assisted_recovery_grants (
    assisted_grant_id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users (user_id),
    selector text NOT NULL UNIQUE,
    secret_hash text NOT NULL,
    purpose text NOT NULL,
    status text NOT NULL CHECK (status IN ('active', 'consumed', 'revoked', 'expired')),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    max_attempts integer NOT NULL CHECK (max_attempts > 0),
    created_by_admin_id uuid NOT NULL REFERENCES admin_accounts (admin_id),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    revoked_at timestamptz,
    result_id uuid REFERENCES secret_operation_results (result_id),
    CHECK (expires_at > created_at),
    CHECK (
        (status = 'active' AND consumed_at IS NULL AND revoked_at IS NULL AND result_id IS NULL)
        OR (status = 'consumed' AND consumed_at IS NOT NULL AND revoked_at IS NULL AND result_id IS NOT NULL)
        OR (status = 'revoked' AND consumed_at IS NULL AND revoked_at IS NOT NULL AND result_id IS NULL)
        OR (status = 'expired' AND consumed_at IS NULL AND revoked_at IS NULL AND result_id IS NULL)
    )
);

CREATE UNIQUE INDEX admin_assisted_recovery_grants_one_active_idx
    ON admin_assisted_recovery_grants (user_id)
    WHERE status = 'active';
CREATE INDEX admin_assisted_recovery_grants_expiry_idx
    ON admin_assisted_recovery_grants (expires_at)
    WHERE status = 'active';

ALTER TABLE user_recovery_attempts
    ADD CONSTRAINT user_recovery_attempts_assisted_grant_fk
    FOREIGN KEY (assisted_grant_id)
    REFERENCES admin_assisted_recovery_grants (assisted_grant_id)
    DEFERRABLE INITIALLY DEFERRED;

-- +goose Down

ALTER TABLE IF EXISTS user_recovery_attempts
    DROP CONSTRAINT IF EXISTS user_recovery_attempts_assisted_grant_fk;
DROP TABLE IF EXISTS admin_assisted_recovery_grants;
DROP TABLE IF EXISTS admin_recovery_codes;
DROP TABLE IF EXISTS admin_sessions;
DROP TABLE IF EXISTS admin_totp_enrollments;
DROP TABLE IF EXISTS admin_challenges;
DROP TABLE IF EXISTS admin_accounts;
