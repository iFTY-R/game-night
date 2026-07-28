-- +goose Up

ALTER TABLE admin_accounts
    DROP CONSTRAINT IF EXISTS admin_accounts_status_check;

ALTER TABLE admin_accounts
    ADD CONSTRAINT admin_accounts_status_check
    CHECK (status IN ('bootstrap_pending', 'setup_required', 'active')) NOT VALID;

ALTER TABLE admin_totp_enrollments
    ADD COLUMN enrollment_version bigint,
    ADD COLUMN replay_floor bigint;

UPDATE admin_totp_enrollments AS enrollment
SET enrollment_version = 1,
    replay_floor = CASE
        WHEN enrollment.status = 'active' THEN GREATEST(COALESCE(account.last_accepted_totp_step, 0), 0)
        ELSE NULL
    END
FROM admin_accounts AS account
WHERE enrollment.admin_id = account.admin_id
  AND enrollment_version IS NULL;

ALTER TABLE admin_totp_enrollments
    ALTER COLUMN enrollment_version SET NOT NULL;

ALTER TABLE admin_totp_enrollments
    DROP CONSTRAINT IF EXISTS admin_totp_enrollments_status_check,
    DROP CONSTRAINT IF EXISTS admin_totp_enrollments_check,
    DROP CONSTRAINT IF EXISTS admin_totp_enrollments_check1;

ALTER TABLE admin_totp_enrollments
    ADD CONSTRAINT admin_totp_enrollments_status_check
    CHECK (status IN ('pending', 'active', 'disabled')) NOT VALID,
    ADD CONSTRAINT admin_totp_enrollments_version_check
    CHECK (enrollment_version > 0) NOT VALID,
    ADD CONSTRAINT admin_totp_enrollments_replay_floor_check
    CHECK (replay_floor IS NULL OR replay_floor >= 0) NOT VALID,
    ADD CONSTRAINT admin_totp_enrollments_security_state_check
    CHECK (
        (status = 'pending'
            AND ciphertext IS NOT NULL
            AND nonce IS NOT NULL
            AND expires_at IS NOT NULL
            AND activated_at IS NULL
            AND disabled_at IS NULL
            AND replay_floor IS NULL)
        OR (status = 'active'
            AND ciphertext IS NOT NULL
            AND nonce IS NOT NULL
            AND expires_at IS NULL
            AND activated_at IS NOT NULL
            AND disabled_at IS NULL
            AND replay_floor IS NOT NULL)
        OR (status = 'disabled'
            AND ciphertext IS NULL
            AND nonce IS NULL
            AND disabled_at IS NOT NULL)
    ) NOT VALID;

ALTER TABLE admin_sessions
    ADD COLUMN session_version bigint,
    ADD COLUMN client_ip text,
    ADD COLUMN user_agent text;

UPDATE admin_sessions
SET kind = CASE kind
    WHEN 'full' THEN 'full'
    WHEN 'setup_password_pending' THEN 'setup_password_pending'
    ELSE 'mfa_pending'
END,
    session_version = COALESCE(session_version, 1),
    client_ip = COALESCE(client_ip, ''),
    user_agent = COALESCE(user_agent, '');

ALTER TABLE admin_sessions
    ALTER COLUMN session_version SET NOT NULL,
    ALTER COLUMN client_ip SET NOT NULL,
    ALTER COLUMN user_agent SET NOT NULL;

ALTER TABLE admin_sessions
    DROP CONSTRAINT IF EXISTS admin_sessions_kind_check;

ALTER TABLE admin_sessions
    ADD CONSTRAINT admin_sessions_kind_check
    CHECK (kind IN ('setup_password_pending', 'mfa_pending', 'full')),
    ADD CONSTRAINT admin_sessions_session_version_check
    CHECK (session_version > 0);

CREATE TABLE admin_elevation_grants (
    admin_id uuid NOT NULL REFERENCES admin_accounts (admin_id),
    session_id uuid NOT NULL REFERENCES admin_sessions (session_id) ON DELETE CASCADE,
    scope text NOT NULL,
    admin_version bigint NOT NULL CHECK (admin_version > 0),
    password_version bigint NOT NULL CHECK (password_version >= 0),
    session_version bigint NOT NULL CHECK (session_version > 0),
    enrollment_version bigint NOT NULL CHECK (enrollment_version >= 0),
    granted_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    PRIMARY KEY (session_id, scope),
    CHECK (expires_at > granted_at),
    CHECK (revoked_at IS NULL OR revoked_at >= granted_at)
);

CREATE INDEX admin_elevation_grants_expiry_idx
    ON admin_elevation_grants (expires_at)
    WHERE revoked_at IS NULL;

CREATE TABLE admin_command_receipts (
    admin_id uuid NOT NULL REFERENCES admin_accounts (admin_id),
    operation_id text NOT NULL,
    request_digest bytea NOT NULL CHECK (octet_length(request_digest) = 32),
    command text NOT NULL,
    target_type text NOT NULL,
    target_id text NOT NULL,
    result_admin_version bigint NOT NULL CHECK (result_admin_version > 0),
    result_password_version bigint NOT NULL CHECK (result_password_version >= 0),
    result_session_version bigint NOT NULL CHECK (result_session_version >= 0),
    result_enrollment_version bigint NOT NULL CHECK (result_enrollment_version >= 0),
    audit_event_id uuid NOT NULL REFERENCES audit_events (event_id),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (admin_id, operation_id)
);

CREATE INDEX admin_command_receipts_created_idx
    ON admin_command_receipts (created_at, admin_id);

DROP TABLE IF EXISTS profile_export_items;
DROP TABLE IF EXISTS profile_export_contexts;

-- One boundary timestamp keeps the forced reset coherent across every administrator security artifact.
-- +goose StatementBegin
DO $reset$
DECLARE
    boundary timestamptz := pg_catalog.clock_timestamp();
    singleton_admin_id uuid;
BEGIN
    SELECT admin_id
    INTO singleton_admin_id
    FROM admin_accounts
    WHERE singleton_id = 1
    FOR UPDATE;

    IF singleton_admin_id IS NULL THEN
        RAISE EXCEPTION 'administrator singleton row is missing during security migration';
    END IF;

    UPDATE admin_accounts
    SET status = CASE
            WHEN password_hash IS NULL THEN 'bootstrap_pending'
            ELSE 'setup_required'
        END,
        admin_version = admin_version + 1,
        updated_at = boundary
    WHERE singleton_id = 1;

    UPDATE admin_sessions
    SET revoked_at = COALESCE(revoked_at, boundary),
        revoke_reason = COALESCE(revoke_reason, 'security_migration_reset'),
        session_version = session_version + CASE WHEN revoked_at IS NULL THEN 1 ELSE 0 END
    WHERE admin_id = singleton_admin_id;

	UPDATE admin_challenges
	SET status = 'revoked',
		revoked_at = COALESCE(revoked_at, boundary)
	WHERE admin_id = singleton_admin_id
	  AND status = 'active';

	UPDATE user_recovery_attempts
	SET status = 'revoked',
		revoked_at = COALESCE(revoked_at, boundary)
	WHERE assisted_grant_id IS NOT NULL
	  AND status = 'active';

	UPDATE admin_assisted_recovery_grants
	SET status = 'revoked',
		revoked_at = COALESCE(revoked_at, boundary)
	WHERE status = 'active';

	UPDATE admin_recovery_codes
    SET status = 'revoked',
        revoked_at = COALESCE(revoked_at, boundary)
    WHERE admin_id = singleton_admin_id
      AND status = 'active';

    -- Legacy expired enrollments no longer satisfy the new state machine, so the reset folds every
    -- non-disabled row into the disabled terminal state before the stricter constraint lands.
    UPDATE admin_totp_enrollments
    SET status = 'disabled',
        enrollment_version = enrollment_version + 1,
        ciphertext = NULL,
        nonce = NULL,
        expires_at = NULL,
        disabled_at = COALESCE(disabled_at, boundary)
    WHERE admin_id = singleton_admin_id
      AND status <> 'disabled';

    DELETE FROM admin_elevation_grants
    WHERE admin_id = singleton_admin_id;

    DELETE FROM admin_command_receipts
    WHERE admin_id = singleton_admin_id;
END;
$reset$;
-- +goose StatementEnd

-- The reset above rewrites every legacy row into the new state machine before the deferred scans run.
ALTER TABLE admin_accounts
    VALIDATE CONSTRAINT admin_accounts_status_check;

ALTER TABLE admin_totp_enrollments
    VALIDATE CONSTRAINT admin_totp_enrollments_status_check,
    VALIDATE CONSTRAINT admin_totp_enrollments_version_check,
    VALIDATE CONSTRAINT admin_totp_enrollments_replay_floor_check,
    VALIDATE CONSTRAINT admin_totp_enrollments_security_state_check;

ALTER TABLE admin_accounts
    DROP COLUMN last_accepted_totp_step;

-- +goose StatementBegin
DO $functions$
DECLARE
    trusted_schema text := current_schema();
    owner_role text := current_setting('game_night.owner_role');
    migration_role text := current_setting('game_night.migration_role');
    runtime_role text := current_setting('game_night.runtime_role');
    worker_role text := current_setting('game_night.worker_role');
BEGIN
    IF trusted_schema IS NULL THEN
        RAISE EXCEPTION 'security migration requires an explicit current schema';
    END IF;

    EXECUTE format(
        $ddl$
        CREATE OR REPLACE FUNCTION %1$I.reset_admin_account(
            expected_previous_hash bytea,
            new_event_id uuid,
            new_canonical_event bytea,
            new_signature bytea,
            new_signing_key_version integer,
            new_created_at timestamptz,
            new_password_hash text,
            new_password_algorithm text,
            new_password_parameters text,
            checkpoint_event_id uuid,
            checkpoint_payload bytea
        )
        RETURNS TABLE(appended_sequence bigint, appended_hash bytea)
        LANGUAGE plpgsql
        SECURITY DEFINER
        SET search_path = pg_catalog, %1$I, pg_temp
        AS $function$
        DECLARE
            singleton_admin_id uuid;
        BEGIN
            IF new_password_hash IS NULL
               OR new_password_algorithm IS NULL
               OR new_password_parameters IS NULL
               OR checkpoint_event_id IS NULL
               OR checkpoint_payload IS NULL THEN
                RAISE EXCEPTION USING ERRCODE = '22023', MESSAGE = 'invalid admin reset input';
            END IF;

            SELECT account.admin_id
            INTO singleton_admin_id
            FROM %1$I.admin_accounts AS account
            WHERE account.singleton_id = 1
            FOR UPDATE;

            IF NOT FOUND THEN
                RAISE EXCEPTION USING ERRCODE = 'P0002', MESSAGE = 'singleton admin account is missing';
            END IF;

            UPDATE %1$I.admin_accounts
            SET status = 'setup_required',
                password_hash = new_password_hash,
                password_algorithm = new_password_algorithm,
                password_parameters = new_password_parameters,
                password_version = password_version + 1,
                admin_version = admin_version + 1,
                updated_at = new_created_at
            WHERE singleton_id = 1;

            UPDATE %1$I.admin_sessions
            SET revoked_at = COALESCE(revoked_at, new_created_at),
                revoke_reason = COALESCE(revoke_reason, 'offline_reset'),
                session_version = session_version + CASE WHEN revoked_at IS NULL THEN 1 ELSE 0 END
            WHERE admin_id = singleton_admin_id;

            UPDATE %1$I.admin_challenges
            SET status = 'revoked',
                revoked_at = COALESCE(revoked_at, new_created_at)
            WHERE admin_id = singleton_admin_id
              AND status = 'active';

            UPDATE %1$I.admin_totp_enrollments
            SET status = 'disabled',
                admin_version = admin_version + 1,
                enrollment_version = enrollment_version + 1,
                ciphertext = NULL,
                nonce = NULL,
                expires_at = NULL,
                disabled_at = COALESCE(disabled_at, new_created_at)
            WHERE admin_id = singleton_admin_id
              AND status IN ('pending', 'active');

            UPDATE %1$I.admin_recovery_codes
            SET status = 'revoked',
                revoked_at = COALESCE(revoked_at, new_created_at)
            WHERE admin_id = singleton_admin_id
              AND status = 'active';

            UPDATE %1$I.admin_elevation_grants
            SET revoked_at = COALESCE(revoked_at, new_created_at)
            WHERE admin_id = singleton_admin_id
              AND revoked_at IS NULL;

            DELETE FROM %1$I.admin_command_receipts
            WHERE admin_id = singleton_admin_id;

            RETURN QUERY
            SELECT result.appended_sequence, result.appended_hash
            FROM %1$I.append_audit_event(
                'admin',
                expected_previous_hash,
                new_event_id,
                new_canonical_event,
                new_signature,
                new_signing_key_version,
                new_created_at
            ) AS result;

            PERFORM pg_catalog.pg_advisory_xact_lock(1196314434, 1);

            INSERT INTO %1$I.outbox_events (
                event_id,
                event_type,
                aggregate_type,
                aggregate_id,
                payload,
                created_at,
                available_at
            ) VALUES (
                checkpoint_event_id,
                'audit.checkpoint.pending',
                'audit.chain',
                '9c26d493-92b3-59a5-a787-3a1a3df235aa'::uuid,
                checkpoint_payload,
                new_created_at,
                new_created_at
            );
        END
        $function$
        $ddl$,
        trusted_schema
    );

    EXECUTE format(
        $ddl$
        CREATE OR REPLACE FUNCTION %1$I.cleanup_expired_security_state(boundary timestamptz DEFAULT pg_catalog.clock_timestamp())
        RETURNS jsonb
        LANGUAGE plpgsql
        SECURITY DEFINER
        VOLATILE
        SET search_path = pg_catalog, %1$I, pg_temp
        AS $function$
        DECLARE
            expired_results bigint := 0;
            deleted_results bigint := 0;
            expired_challenges bigint := 0;
            deleted_challenges bigint := 0;
            deleted_anonymous_challenges bigint := 0;
            expired_sessions bigint := 0;
            deleted_sessions bigint := 0;
            expired_totp bigint := 0;
            deleted_totp bigint := 0;
            expired_attempts bigint := 0;
            deleted_attempts bigint := 0;
            expired_grants bigint := 0;
            deleted_grants bigint := 0;
            deleted_claims bigint := 0;
            deleted_onboarding bigint := 0;
        BEGIN
            UPDATE %1$I.secret_operation_results
            SET status = 'expired', ciphertext = NULL, nonce = NULL, wrapped_data_key = NULL
            WHERE status = 'available' AND secret_expires_at <= boundary;
            GET DIAGNOSTICS expired_results = ROW_COUNT;

            DELETE FROM %1$I.secret_operation_results
            WHERE status <> 'available' AND tombstone_expires_at <= boundary
              AND NOT EXISTS (
                  SELECT 1 FROM %1$I.anonymous_challenges AS challenge
                  WHERE challenge.result_id = secret_operation_results.result_id
              )
              AND NOT EXISTS (
                  SELECT 1 FROM %1$I.user_recovery_attempts AS attempt
                  WHERE attempt.result_id = secret_operation_results.result_id
              )
              AND NOT EXISTS (
                  SELECT 1 FROM %1$I.admin_challenges AS challenge
                  WHERE challenge.result_id = secret_operation_results.result_id
              );
            GET DIAGNOSTICS deleted_results = ROW_COUNT;

            UPDATE %1$I.user_recovery_attempts
            SET status = 'expired'
            WHERE status = 'active' AND expires_at <= boundary;
            GET DIAGNOSTICS expired_attempts = ROW_COUNT;

            DELETE FROM %1$I.user_recovery_attempts
            WHERE status <> 'active'
              AND (COALESCE(consumed_at, revoked_at, expires_at) + interval '30 days') <= boundary;
            GET DIAGNOSTICS deleted_attempts = ROW_COUNT;

            UPDATE %1$I.admin_challenges
            SET status = 'expired'
            WHERE status = 'active' AND expires_at <= boundary;
            GET DIAGNOSTICS expired_challenges = ROW_COUNT;

            DELETE FROM %1$I.admin_challenges
            WHERE status <> 'active'
              AND (COALESCE(consumed_at, revoked_at, expires_at) + interval '24 hours') <= boundary;
            GET DIAGNOSTICS deleted_challenges = ROW_COUNT;

            DELETE FROM %1$I.anonymous_challenges AS challenge
            WHERE (
                (challenge.consumed_at IS NULL AND challenge.expires_at + interval '5 minutes' + interval '24 hours' <= boundary)
                OR (challenge.consumed_at IS NOT NULL AND challenge.replay_until + interval '24 hours' <= boundary)
            )
            AND NOT EXISTS (
                SELECT 1 FROM %1$I.user_recovery_attempts AS attempt
                WHERE attempt.challenge_id = challenge.challenge_id
            );
            GET DIAGNOSTICS deleted_anonymous_challenges = ROW_COUNT;
            deleted_challenges := deleted_challenges + deleted_anonymous_challenges;

            UPDATE %1$I.admin_sessions
            SET revoked_at = boundary,
                revoke_reason = 'expired',
                session_version = session_version + 1
            WHERE revoked_at IS NULL
              AND (idle_expires_at <= boundary OR absolute_expires_at <= boundary);
            GET DIAGNOSTICS expired_sessions = ROW_COUNT;

            DELETE FROM %1$I.admin_sessions
            WHERE revoked_at IS NOT NULL AND revoked_at + interval '30 days' <= boundary;
            GET DIAGNOSTICS deleted_sessions = ROW_COUNT;

            UPDATE %1$I.admin_totp_enrollments
            SET status = 'disabled',
                enrollment_version = enrollment_version + 1,
                ciphertext = NULL,
                nonce = NULL,
                expires_at = NULL,
                disabled_at = boundary
            WHERE status = 'pending'
              AND expires_at <= boundary;
            GET DIAGNOSTICS expired_totp = ROW_COUNT;

            DELETE FROM %1$I.admin_totp_enrollments
            WHERE status = 'disabled'
              AND disabled_at <= boundary - interval '30 days'
              AND activated_at IS NULL;
            GET DIAGNOSTICS deleted_totp = ROW_COUNT;

            UPDATE %1$I.admin_elevation_grants
            SET revoked_at = boundary
            WHERE revoked_at IS NULL
              AND expires_at <= boundary;
            GET DIAGNOSTICS expired_grants = ROW_COUNT;

            DELETE FROM %1$I.admin_elevation_grants
            WHERE revoked_at IS NOT NULL
              AND revoked_at + interval '30 days' <= boundary;
            GET DIAGNOSTICS deleted_grants = ROW_COUNT;

            DELETE FROM %1$I.username_claims
            WHERE status = 'reserved' AND reserved_until <= boundary;
            GET DIAGNOSTICS deleted_claims = ROW_COUNT;

            DELETE FROM %1$I.user_profiles AS profile
            WHERE profile.user_id IN (
                SELECT user_id FROM %1$I.users WHERE status = 'onboarding' AND created_at + interval '24 hours' <= boundary
            );
            DELETE FROM %1$I.device_credentials AS device
            WHERE device.user_id IN (
                SELECT user_id FROM %1$I.users WHERE status = 'onboarding' AND created_at + interval '24 hours' <= boundary
            );
            DELETE FROM %1$I.user_recovery_credentials AS credential
            WHERE credential.user_id IN (
                SELECT user_id FROM %1$I.users WHERE status = 'onboarding' AND created_at + interval '24 hours' <= boundary
            );
            DELETE FROM %1$I.username_claims AS claim
            WHERE claim.owner_user_id IN (
                SELECT user_id FROM %1$I.users WHERE status = 'onboarding' AND created_at + interval '24 hours' <= boundary
            );
            DELETE FROM %1$I.users
            WHERE status = 'onboarding' AND created_at + interval '24 hours' <= boundary
              AND NOT EXISTS (SELECT 1 FROM %1$I.device_credentials AS device WHERE device.user_id = users.user_id)
              AND NOT EXISTS (SELECT 1 FROM %1$I.user_recovery_credentials AS credential WHERE credential.user_id = users.user_id)
              AND NOT EXISTS (SELECT 1 FROM %1$I.user_recovery_attempts AS attempt WHERE attempt.user_id = users.user_id)
              AND NOT EXISTS (SELECT 1 FROM %1$I.user_profiles AS profile WHERE profile.user_id = users.user_id);
            GET DIAGNOSTICS deleted_onboarding = ROW_COUNT;

            RETURN pg_catalog.jsonb_build_object(
                'expired_results', expired_results,
                'deleted_results', deleted_results,
                'expired_challenges', expired_challenges,
                'deleted_challenges', deleted_challenges,
                'expired_sessions', expired_sessions,
                'deleted_sessions', deleted_sessions,
                'expired_totp', expired_totp,
                'deleted_totp', deleted_totp,
                'expired_exports', 0,
                'deleted_exports', 0,
                'expired_attempts', expired_attempts,
                'deleted_attempts', deleted_attempts,
                'expired_grants', expired_grants,
                'deleted_grants', deleted_grants,
                'deleted_claims', deleted_claims,
                'deleted_onboarding', deleted_onboarding
            );
        END
        $function$
        $ddl$,
        trusted_schema
    );

    EXECUTE format(
        $ddl$
        CREATE OR REPLACE FUNCTION %1$I.run_expiry_cleanup()
        RETURNS jsonb
        LANGUAGE sql
        SECURITY DEFINER
        VOLATILE
        SET search_path = pg_catalog, %1$I, pg_temp
        AS $function$
            SELECT %1$I.cleanup_expired_security_state(pg_catalog.clock_timestamp())
        $function$
        $ddl$,
        trusted_schema
    );

    EXECUTE format('ALTER TABLE %I.admin_elevation_grants OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format('ALTER TABLE %I.admin_command_receipts OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format('ALTER FUNCTION %I.reset_admin_account(bytea, uuid, bytea, bytea, integer, timestamptz, text, text, text, uuid, bytea) OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format('ALTER FUNCTION %I.cleanup_expired_security_state(timestamptz) OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format('ALTER FUNCTION %I.run_expiry_cleanup() OWNER TO %I', trusted_schema, owner_role);

    EXECUTE format('REVOKE ALL ON TABLE %I.admin_elevation_grants, %I.admin_command_receipts FROM PUBLIC', trusted_schema, trusted_schema);
    EXECUTE format('REVOKE ALL ON FUNCTION %I.reset_admin_account(bytea, uuid, bytea, bytea, integer, timestamptz, text, text, text, uuid, bytea) FROM PUBLIC', trusted_schema);
    EXECUTE format('REVOKE ALL ON FUNCTION %I.cleanup_expired_security_state(timestamptz) FROM PUBLIC', trusted_schema);
    EXECUTE format('REVOKE ALL ON FUNCTION %I.run_expiry_cleanup() FROM PUBLIC', trusted_schema);

    EXECUTE format(
        'GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE %I.admin_elevation_grants, %I.admin_command_receipts TO %I',
        trusted_schema, trusted_schema, runtime_role
    );
    EXECUTE format('GRANT EXECUTE ON FUNCTION %I.reset_admin_account(bytea, uuid, bytea, bytea, integer, timestamptz, text, text, text, uuid, bytea) TO %I', trusted_schema, migration_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION %I.run_expiry_cleanup() TO %I', trusted_schema, worker_role);
END;
$functions$;
-- +goose StatementEnd

-- +goose Down

DROP FUNCTION IF EXISTS cleanup_expired_security_state(timestamptz);
DROP TABLE IF EXISTS admin_command_receipts;
DROP TABLE IF EXISTS admin_elevation_grants;
