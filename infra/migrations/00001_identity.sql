-- +goose Up

CREATE TABLE users (
    user_id uuid PRIMARY KEY,
    status text NOT NULL CHECK (status IN ('onboarding', 'active', 'suspended', 'deleted')),
    username text,
    current_username_key text,
    username_changed_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (
        (status = 'onboarding' AND username IS NULL AND current_username_key IS NULL)
        OR (status IN ('active', 'suspended') AND username IS NOT NULL AND current_username_key IS NOT NULL)
        OR status = 'deleted'
    )
);

CREATE TABLE username_claims (
    username_key text PRIMARY KEY,
    display_username text NOT NULL,
    status text NOT NULL CHECK (status IN ('active', 'reserved')),
    owner_user_id uuid NOT NULL REFERENCES users (user_id),
    reserved_until timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (username_key, owner_user_id),
    CHECK (
        (status = 'active' AND reserved_until IS NULL)
        OR (status = 'reserved' AND reserved_until IS NOT NULL)
    )
);

ALTER TABLE users
    ADD CONSTRAINT users_current_username_claim_fk
    FOREIGN KEY (current_username_key)
    REFERENCES username_claims (username_key)
    DEFERRABLE INITIALLY DEFERRED;

CREATE INDEX username_claims_owner_idx ON username_claims (owner_user_id);
CREATE INDEX username_claims_reservation_cleanup_idx
    ON username_claims (reserved_until)
    WHERE status = 'reserved';

-- +goose StatementBegin
CREATE FUNCTION enforce_username_claim_invariants()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    invalid_state boolean;
BEGIN
    EXECUTE format(
        $query$
        SELECT EXISTS (
            SELECT 1
            FROM %1$I.users AS u
            LEFT JOIN %1$I.username_claims AS c
              ON c.username_key = u.current_username_key
            WHERE (
                u.status IN ('active', 'suspended')
                AND (
                    c.username_key IS NULL
                    OR c.owner_user_id <> u.user_id
                    OR c.status <> 'active'
                    OR c.display_username <> u.username
                )
            ) OR (
                u.status IN ('onboarding', 'deleted')
                AND u.current_username_key IS NOT NULL
            )
            UNION ALL
            SELECT 1
            FROM %1$I.username_claims AS c
            LEFT JOIN %1$I.users AS u
              ON u.user_id = c.owner_user_id
             AND u.current_username_key = c.username_key
            WHERE c.status = 'active'
              AND (u.user_id IS NULL OR u.status NOT IN ('active', 'suspended'))
        )
        $query$,
        TG_TABLE_SCHEMA
    ) INTO invalid_state;

    IF invalid_state THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'username claim invariant violated';
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER users_username_claim_invariants
AFTER INSERT OR UPDATE OR DELETE ON users
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_username_claim_invariants();

CREATE CONSTRAINT TRIGGER username_claims_user_invariants
AFTER INSERT OR UPDATE OR DELETE ON username_claims
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_username_claim_invariants();

CREATE TABLE device_credentials (
    credential_id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users (user_id),
    secret_hash bytea NOT NULL CHECK (octet_length(secret_hash) = 32),
    secret_key_version integer NOT NULL CHECK (secret_key_version > 0),
    previous_secret_hash bytea,
    previous_secret_key_version integer,
    previous_valid_until timestamptz,
    csrf_hash bytea NOT NULL CHECK (octet_length(csrf_hash) = 32),
    generation bigint NOT NULL DEFAULT 1 CHECK (generation > 0),
    label text NOT NULL,
    created_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    rotated_at timestamptz NOT NULL,
    idle_expires_at timestamptz NOT NULL,
    absolute_expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    revoke_reason text,
    CHECK (idle_expires_at <= absolute_expires_at),
    CHECK (
        (previous_secret_hash IS NULL AND previous_secret_key_version IS NULL AND previous_valid_until IS NULL)
        OR (
            octet_length(previous_secret_hash) = 32
            AND previous_secret_key_version > 0
            AND previous_valid_until IS NOT NULL
        )
    ),
    CHECK (
        (revoked_at IS NULL AND revoke_reason IS NULL)
        OR (revoked_at IS NOT NULL AND revoke_reason IS NOT NULL)
    )
);

CREATE INDEX device_credentials_user_idx ON device_credentials (user_id, created_at, credential_id);
CREATE INDEX device_credentials_expiry_idx ON device_credentials (idle_expires_at, absolute_expires_at)
    WHERE revoked_at IS NULL;

CREATE TABLE user_recovery_credentials (
    recovery_credential_id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users (user_id),
    selector text NOT NULL UNIQUE,
    secret_hash text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    status text NOT NULL CHECK (status IN ('active', 'consumed', 'revoked')),
    created_at timestamptz NOT NULL,
    consumed_at timestamptz,
    revoked_at timestamptz,
    revoke_reason text,
    CHECK (
        (status = 'active' AND consumed_at IS NULL AND revoked_at IS NULL)
        OR (status = 'consumed' AND consumed_at IS NOT NULL AND revoked_at IS NULL)
        OR (status = 'revoked' AND consumed_at IS NULL AND revoked_at IS NOT NULL AND revoke_reason IS NOT NULL)
    )
);

CREATE UNIQUE INDEX user_recovery_credentials_one_active_idx
    ON user_recovery_credentials (user_id)
    WHERE status = 'active';

CREATE TABLE anonymous_challenges (
    challenge_id uuid PRIMARY KEY,
    selector text NOT NULL UNIQUE,
    secret_hash bytea NOT NULL CHECK (octet_length(secret_hash) = 32),
    secret_key_version integer NOT NULL CHECK (secret_key_version > 0),
    purpose text NOT NULL,
    audience text NOT NULL,
    origin_hash bytea NOT NULL CHECK (octet_length(origin_hash) = 32),
    request_flow_id text NOT NULL,
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    max_attempts integer NOT NULL CHECK (max_attempts > 0),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    replay_until timestamptz,
    operation_id text,
    request_digest bytea,
    result_id uuid,
    CHECK (expires_at > created_at),
    CHECK (
        (consumed_at IS NULL AND replay_until IS NULL AND operation_id IS NULL AND request_digest IS NULL AND result_id IS NULL)
        OR (
            consumed_at IS NOT NULL
            AND replay_until IS NOT NULL
            AND operation_id IS NOT NULL
            AND octet_length(request_digest) = 32
            AND result_id IS NOT NULL
        )
    )
);

CREATE INDEX anonymous_challenges_expiry_idx ON anonymous_challenges (expires_at);

CREATE TABLE secret_operation_results (
    result_id uuid PRIMARY KEY,
    operation_scope text NOT NULL,
    actor_or_challenge_id uuid NOT NULL,
    operation_id text NOT NULL,
    request_digest bytea NOT NULL CHECK (octet_length(request_digest) = 32),
    result_type text NOT NULL,
    result_version integer NOT NULL CHECK (result_version > 0),
    ciphertext bytea,
    nonce bytea,
    wrapped_data_key bytea,
    key_version integer NOT NULL CHECK (key_version > 0),
    status text NOT NULL CHECK (status IN ('available', 'confirmed', 'expired')),
    secret_expires_at timestamptz NOT NULL,
    confirmed_at timestamptz,
    completed_at timestamptz NOT NULL,
    tombstone_expires_at timestamptz NOT NULL,
    UNIQUE (operation_scope, actor_or_challenge_id, operation_id),
    CHECK (tombstone_expires_at > secret_expires_at),
    CHECK (
        (status = 'available' AND ciphertext IS NOT NULL AND nonce IS NOT NULL AND wrapped_data_key IS NOT NULL AND confirmed_at IS NULL)
        OR (
            status IN ('confirmed', 'expired')
            AND ciphertext IS NULL
            AND nonce IS NULL
            AND wrapped_data_key IS NULL
        )
    ),
    CHECK (
        (status = 'confirmed' AND confirmed_at IS NOT NULL)
        OR (status IN ('available', 'expired') AND confirmed_at IS NULL)
    )
);

ALTER TABLE anonymous_challenges
    ADD CONSTRAINT anonymous_challenges_result_fk
    FOREIGN KEY (result_id) REFERENCES secret_operation_results (result_id)
    DEFERRABLE INITIALLY DEFERRED;

CREATE INDEX secret_operation_results_secret_cleanup_idx
    ON secret_operation_results (secret_expires_at)
    WHERE status = 'available';
CREATE INDEX secret_operation_results_tombstone_cleanup_idx
    ON secret_operation_results (tombstone_expires_at)
    WHERE status <> 'available';

CREATE TABLE user_recovery_attempts (
    recovery_attempt_id uuid PRIMARY KEY,
    grant_selector text NOT NULL UNIQUE,
    grant_secret_hash bytea NOT NULL CHECK (octet_length(grant_secret_hash) = 32),
    grant_key_version integer NOT NULL CHECK (grant_key_version > 0),
    user_id uuid NOT NULL REFERENCES users (user_id),
    recovery_credential_id uuid REFERENCES user_recovery_credentials (recovery_credential_id),
    recovery_credential_version bigint,
    assisted_grant_id uuid,
    challenge_id uuid NOT NULL REFERENCES anonymous_challenges (challenge_id),
    origin_hash bytea NOT NULL CHECK (octet_length(origin_hash) = 32),
    purpose text NOT NULL,
    request_digest bytea CHECK (request_digest IS NULL OR octet_length(request_digest) = 32),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    max_attempts integer NOT NULL CHECK (max_attempts > 0),
    status text NOT NULL CHECK (status IN ('active', 'consumed', 'expired', 'revoked')),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    revoked_at timestamptz,
    result_id uuid REFERENCES secret_operation_results (result_id),
    CHECK (expires_at > created_at),
    CHECK (
        (recovery_credential_id IS NOT NULL AND recovery_credential_version IS NOT NULL AND assisted_grant_id IS NULL)
        OR (recovery_credential_id IS NULL AND recovery_credential_version IS NULL AND assisted_grant_id IS NOT NULL)
    ),
    CHECK (
        (status = 'active' AND consumed_at IS NULL AND revoked_at IS NULL AND result_id IS NULL)
        OR (status = 'consumed' AND consumed_at IS NOT NULL AND revoked_at IS NULL AND result_id IS NOT NULL)
        OR (status = 'expired' AND consumed_at IS NULL AND revoked_at IS NULL AND result_id IS NULL)
        OR (status = 'revoked' AND consumed_at IS NULL AND revoked_at IS NOT NULL AND result_id IS NULL)
    )
);

CREATE INDEX user_recovery_attempts_expiry_idx
    ON user_recovery_attempts (expires_at)
    WHERE status = 'active';
CREATE INDEX user_recovery_attempts_user_idx ON user_recovery_attempts (user_id, created_at);

-- +goose Down

DROP TABLE IF EXISTS user_recovery_attempts;
ALTER TABLE IF EXISTS anonymous_challenges DROP CONSTRAINT IF EXISTS anonymous_challenges_result_fk;
DROP TABLE IF EXISTS secret_operation_results;
DROP TABLE IF EXISTS anonymous_challenges;
DROP TABLE IF EXISTS user_recovery_credentials;
DROP TABLE IF EXISTS device_credentials;
DROP TRIGGER IF EXISTS username_claims_user_invariants ON username_claims;
DROP TRIGGER IF EXISTS users_username_claim_invariants ON users;
DROP FUNCTION IF EXISTS enforce_username_claim_invariants();
ALTER TABLE IF EXISTS users DROP CONSTRAINT IF EXISTS users_current_username_claim_fk;
DROP TABLE IF EXISTS username_claims;
DROP TABLE IF EXISTS users;
