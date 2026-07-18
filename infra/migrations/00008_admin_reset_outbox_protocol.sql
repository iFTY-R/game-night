-- +goose Up

-- Keep the offline reset checkpoint compatible with the canonical dotted outbox aggregate protocol.
-- +goose StatementBegin
DO $outer$
DECLARE
    trusted_schema text := current_schema();
BEGIN
    IF trusted_schema IS NULL THEN
        RAISE EXCEPTION 'admin reset outbox migration requires an explicit current schema';
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

            -- Use the same producer lock as CreateOutboxEvent so sequence order remains a committed prefix.
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
END;
$outer$;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DO $outer$
DECLARE
    trusted_schema text := current_schema();
BEGIN
    IF trusted_schema IS NULL THEN
        RAISE EXCEPTION 'admin reset outbox migration requires an explicit current schema';
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
