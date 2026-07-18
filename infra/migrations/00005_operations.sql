-- +goose Up

CREATE TABLE key_rotation_jobs (
    job_id uuid PRIMARY KEY,
    purpose text NOT NULL CHECK (purpose IN ('pii', 'totp')),
    source_key_version integer NOT NULL CHECK (source_key_version > 0),
    target_key_version integer NOT NULL CHECK (target_key_version > 0),
    status text NOT NULL CHECK (status IN ('pending', 'running', 'completed', 'failed')),
    cursor_id uuid,
    processed_count bigint NOT NULL DEFAULT 0 CHECK (processed_count >= 0),
    conflict_count bigint NOT NULL DEFAULT 0 CHECK (conflict_count >= 0),
    lease_owner text,
    lease_until timestamptz,
    last_error_code text,
    created_at timestamptz NOT NULL,
    started_at timestamptz,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz,
    CHECK (source_key_version <> target_key_version),
    CHECK (
        (lease_owner IS NULL AND lease_until IS NULL)
        OR (lease_owner IS NOT NULL AND lease_until IS NOT NULL)
    ),
    CHECK (
        (status = 'pending' AND started_at IS NULL AND completed_at IS NULL)
        OR (status = 'running' AND started_at IS NOT NULL AND completed_at IS NULL)
        OR (status = 'completed' AND started_at IS NOT NULL AND completed_at IS NOT NULL)
        OR (status = 'failed' AND started_at IS NOT NULL AND completed_at IS NULL AND last_error_code IS NOT NULL)
    )
);

CREATE UNIQUE INDEX key_rotation_jobs_one_active_idx
    ON key_rotation_jobs (purpose)
    WHERE status IN ('pending', 'running');
CREATE INDEX key_rotation_jobs_lease_idx
    ON key_rotation_jobs (lease_until, created_at)
    WHERE status IN ('pending', 'running');

CREATE VIEW audit_events_redacted
WITH (security_barrier = true)
AS
SELECT
    chain_id,
    sequence,
    event_id,
    previous_hash,
    canonical_event,
    event_hash,
    signature,
    signing_key_version,
    created_at
FROM audit_events;

-- The reset function accepts an already canonicalized and signed event; the database keeps reset and chain append atomic.
-- +goose StatementBegin
DO $outer$
DECLARE
    trusted_schema text := current_schema();
BEGIN
    IF trusted_schema IS NULL THEN
        RAISE EXCEPTION 'operations migration requires an explicit current schema';
    END IF;

    EXECUTE format(
        $ddl$
        CREATE FUNCTION %1$I.reset_admin_account(
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
                last_accepted_totp_step = NULL,
                updated_at = new_created_at
            WHERE singleton_id = 1;

            UPDATE %1$I.admin_sessions
            SET revoked_at = COALESCE(revoked_at, new_created_at),
                revoke_reason = COALESCE(revoke_reason, 'offline_reset')
            WHERE admin_id = singleton_admin_id
              AND revoked_at IS NULL;

            UPDATE %1$I.admin_challenges
            SET status = 'revoked',
                revoked_at = COALESCE(revoked_at, new_created_at)
            WHERE admin_id = singleton_admin_id
              AND status = 'active';

            UPDATE %1$I.admin_totp_enrollments
            SET status = 'disabled',
                ciphertext = NULL,
                nonce = NULL,
                disabled_at = COALESCE(disabled_at, new_created_at)
            WHERE admin_id = singleton_admin_id
              AND status IN ('pending', 'active');

            UPDATE %1$I.admin_recovery_codes
            SET status = 'revoked',
                revoked_at = COALESCE(revoked_at, new_created_at)
            WHERE admin_id = singleton_admin_id
              AND status = 'active';

            UPDATE %1$I.admin_assisted_recovery_grants
            SET status = 'revoked',
                revoked_at = COALESCE(revoked_at, new_created_at)
            WHERE created_by_admin_id = singleton_admin_id
              AND status = 'active';

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
                'audit_chain',
                new_event_id,
                checkpoint_payload,
                new_created_at,
                new_created_at
            );
        END
        $function$
        $ddl$,
        trusted_schema
    );
END;
$outer$;
-- +goose StatementEnd

-- Ownership and grants use connection-scoped custom settings supplied by apps/migrate.
-- +goose StatementBegin
DO $permissions$
DECLARE
    trusted_schema text := current_schema();
    owner_role text := current_setting('game_night.owner_role');
    audit_writer_role text := current_setting('game_night.audit_writer_role');
    migration_role text := current_setting('game_night.migration_role');
    runtime_role text := current_setting('game_night.runtime_role');
    worker_role text := current_setting('game_night.worker_role');
    object_record record;
    function_record record;
    role_name text;
BEGIN
    IF trusted_schema IS NULL THEN
        RAISE EXCEPTION 'permissions migration requires an explicit current schema';
    END IF;

    FOREACH role_name IN ARRAY ARRAY[owner_role, audit_writer_role, migration_role, runtime_role, worker_role]
    LOOP
        IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = role_name) THEN
            RAISE EXCEPTION 'required database role does not exist: %', role_name;
        END IF;
    END LOOP;

    FOR object_record IN
        SELECT class.relname, class.relkind
        FROM pg_catalog.pg_class AS class
        JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = class.relnamespace
        WHERE namespace.nspname = trusted_schema
          AND class.relkind IN ('r', 'p', 'S', 'v')
    LOOP
        EXECUTE format(
            CASE object_record.relkind
                WHEN 'S' THEN 'ALTER SEQUENCE %I.%I OWNER TO %I'
                WHEN 'v' THEN 'ALTER VIEW %I.%I OWNER TO %I'
                ELSE 'ALTER TABLE %I.%I OWNER TO %I'
            END,
            trusted_schema,
            object_record.relname,
            owner_role
        );
    END LOOP;

    FOR function_record IN
        SELECT procedure.proname, pg_catalog.pg_get_function_identity_arguments(procedure.oid) AS identity_arguments
        FROM pg_catalog.pg_proc AS procedure
        JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = procedure.pronamespace
        WHERE namespace.nspname = trusted_schema
    LOOP
        EXECUTE format(
            'ALTER FUNCTION %I.%I(%s) OWNER TO %I',
            trusted_schema,
            function_record.proname,
            function_record.identity_arguments,
            owner_role
        );
    END LOOP;

    EXECUTE format('ALTER SCHEMA %I OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format('REVOKE ALL ON SCHEMA %I FROM PUBLIC', trusted_schema);
    EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA %I FROM PUBLIC', trusted_schema);
    EXECUTE format('REVOKE ALL ON ALL SEQUENCES IN SCHEMA %I FROM PUBLIC', trusted_schema);
    EXECUTE format('REVOKE ALL ON ALL FUNCTIONS IN SCHEMA %I FROM PUBLIC', trusted_schema);

    -- Login roles receive no direct public-schema privilege; SECURITY DEFINER paths also exclude this fallback.
    IF trusted_schema <> 'public'
       AND EXISTS (SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname = 'public') THEN
        EXECUTE format('REVOKE ALL ON SCHEMA public FROM %I, %I', runtime_role, worker_role);
    END IF;

    EXECUTE format('GRANT USAGE ON SCHEMA %I TO %I, %I, %I', trusted_schema, migration_role, runtime_role, worker_role);

    EXECUTE format(
        'GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE %I.users, %I.username_claims, %I.device_credentials, %I.user_recovery_credentials, %I.user_recovery_attempts, %I.anonymous_challenges, %I.secret_operation_results, %I.admin_accounts, %I.admin_challenges, %I.admin_totp_enrollments, %I.admin_sessions, %I.admin_recovery_codes, %I.admin_assisted_recovery_grants, %I.user_profiles, %I.profile_export_contexts, %I.profile_export_items TO %I',
        trusted_schema, trusted_schema, trusted_schema, trusted_schema, trusted_schema, trusted_schema, trusted_schema,
        trusted_schema, trusted_schema, trusted_schema, trusted_schema, trusted_schema, trusted_schema,
        trusted_schema, trusted_schema, trusted_schema, runtime_role
    );
    EXECUTE format('REVOKE INSERT, DELETE ON TABLE %I.admin_accounts FROM %I', trusted_schema, runtime_role);
    EXECUTE format('GRANT SELECT, INSERT ON TABLE %I.outbox_events TO %I', trusted_schema, runtime_role);
    EXECUTE format('GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA %I TO %I', trusted_schema, runtime_role);
    EXECUTE format('GRANT SELECT ON TABLE %I.audit_events_redacted TO %I', trusted_schema, runtime_role);

    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON TABLE %I.key_rotation_jobs TO %I', trusted_schema, worker_role);
    EXECUTE format('GRANT SELECT ON TABLE %I.user_profiles, %I.admin_totp_enrollments TO %I', trusted_schema, trusted_schema, worker_role);
    EXECUTE format('GRANT UPDATE (real_name_ciphertext, real_name_nonce, real_name_key_version) ON TABLE %I.user_profiles TO %I', trusted_schema, worker_role);
    EXECUTE format('GRANT UPDATE (ciphertext, nonce, key_version) ON TABLE %I.admin_totp_enrollments TO %I', trusted_schema, worker_role);
    EXECUTE format('GRANT SELECT ON TABLE %I.outbox_events TO %I', trusted_schema, worker_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON TABLE %I.outbox_consumers TO %I', trusted_schema, worker_role);
    EXECUTE format('GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA %I TO %I', trusted_schema, worker_role);

    EXECUTE format('GRANT EXECUTE ON FUNCTION %I.read_audit_head(text) TO %I', trusted_schema, audit_writer_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION %I.append_audit_event(text, bytea, uuid, bytea, bytea, integer, timestamptz) TO %I', trusted_schema, audit_writer_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION %I.reset_admin_account(bytea, uuid, bytea, bytea, integer, timestamptz, text, text, text, uuid, bytea) TO %I', trusted_schema, migration_role);
END;
$permissions$;
-- +goose StatementEnd

-- +goose Down

DROP FUNCTION IF EXISTS reset_admin_account(bytea, uuid, bytea, bytea, integer, timestamptz, text, text, text, uuid, bytea);
DROP VIEW IF EXISTS audit_events_redacted;
DROP TABLE IF EXISTS key_rotation_jobs;
