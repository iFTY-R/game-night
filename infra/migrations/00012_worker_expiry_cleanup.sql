-- +goose Up

-- One database-time transaction keeps TTL transitions, ciphertext erasure, and FK cleanup repeatable.
-- +goose StatementBegin
DO $outer$
DECLARE
    trusted_schema text := current_schema();
    owner_role text := current_setting('game_night.owner_role');
    worker_role text := current_setting('game_night.worker_role');
BEGIN
    IF trusted_schema IS NULL THEN
        RAISE EXCEPTION 'worker cleanup migration requires an explicit current schema';
    END IF;

    EXECUTE format(
        $ddl$
        CREATE FUNCTION %1$I.run_expiry_cleanup()
        RETURNS jsonb
        LANGUAGE plpgsql
        SECURITY DEFINER
        VOLATILE
        SET search_path = pg_catalog, %1$I, pg_temp
        AS $function$
        DECLARE
            boundary timestamptz := pg_catalog.clock_timestamp();
            expired_results bigint := 0;
            deleted_results bigint := 0;
            expired_challenges bigint := 0;
            deleted_challenges bigint := 0;
            deleted_anonymous_challenges bigint := 0;
            expired_sessions bigint := 0;
            deleted_sessions bigint := 0;
            expired_totp bigint := 0;
            deleted_totp bigint := 0;
            expired_exports bigint := 0;
            deleted_exports bigint := 0;
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
              )
              AND NOT EXISTS (
                  SELECT 1 FROM %1$I.admin_assisted_recovery_grants AS grant_row
                  WHERE grant_row.result_id = secret_operation_results.result_id
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

            UPDATE %1$I.admin_assisted_recovery_grants
            SET status = 'expired'
            WHERE status = 'active' AND expires_at <= boundary;
            GET DIAGNOSTICS expired_grants = ROW_COUNT;

            DELETE FROM %1$I.admin_assisted_recovery_grants
            WHERE status <> 'active'
              AND (COALESCE(consumed_at, revoked_at, expires_at) + interval '30 days') <= boundary;
            GET DIAGNOSTICS deleted_grants = ROW_COUNT;

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
            SET revoked_at = boundary, revoke_reason = 'expired'
            WHERE revoked_at IS NULL AND (idle_expires_at <= boundary OR absolute_expires_at <= boundary);
            GET DIAGNOSTICS expired_sessions = ROW_COUNT;

            DELETE FROM %1$I.admin_sessions
            WHERE revoked_at IS NOT NULL AND revoked_at + interval '30 days' <= boundary;
            GET DIAGNOSTICS deleted_sessions = ROW_COUNT;

            UPDATE %1$I.admin_totp_enrollments
            SET status = 'expired', ciphertext = NULL, nonce = NULL
            WHERE status = 'pending' AND expires_at <= boundary;
            GET DIAGNOSTICS expired_totp = ROW_COUNT;

            DELETE FROM %1$I.admin_totp_enrollments
            WHERE status = 'expired' AND expires_at + interval '30 days' <= boundary;
            GET DIAGNOSTICS deleted_totp = ROW_COUNT;

            UPDATE %1$I.profile_export_contexts
            SET status = 'expired', expired_at = boundary
            WHERE status = 'active' AND expires_at <= boundary;
            GET DIAGNOSTICS expired_exports = ROW_COUNT;

            DELETE FROM %1$I.profile_export_contexts
            WHERE status <> 'active'
              AND (COALESCE(completed_at, aborted_at, expired_at) + interval '30 days') <= boundary;
            GET DIAGNOSTICS deleted_exports = ROW_COUNT;

            DELETE FROM %1$I.username_claims
            WHERE status = 'reserved' AND reserved_until <= boundary;
            GET DIAGNOSTICS deleted_claims = ROW_COUNT;

            DELETE FROM %1$I.profile_export_items AS item
            WHERE item.user_id IN (
                SELECT user_id FROM %1$I.users WHERE status = 'onboarding' AND created_at + interval '24 hours' <= boundary
            );
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
              AND NOT EXISTS (SELECT 1 FROM %1$I.admin_assisted_recovery_grants AS grant_row WHERE grant_row.user_id = users.user_id)
              AND NOT EXISTS (SELECT 1 FROM %1$I.user_profiles AS profile WHERE profile.user_id = users.user_id)
              AND NOT EXISTS (SELECT 1 FROM %1$I.profile_export_items AS item WHERE item.user_id = users.user_id);
            GET DIAGNOSTICS deleted_onboarding = ROW_COUNT;

            RETURN pg_catalog.jsonb_build_object(
                'expired_results', expired_results, 'deleted_results', deleted_results,
                'expired_challenges', expired_challenges, 'deleted_challenges', deleted_challenges,
                'expired_sessions', expired_sessions, 'deleted_sessions', deleted_sessions,
                'expired_totp', expired_totp, 'deleted_totp', deleted_totp,
                'expired_exports', expired_exports, 'deleted_exports', deleted_exports,
                'expired_attempts', expired_attempts, 'deleted_attempts', deleted_attempts,
                'expired_grants', expired_grants, 'deleted_grants', deleted_grants,
                'deleted_claims', deleted_claims, 'deleted_onboarding', deleted_onboarding
            );
        END
        $function$
        $ddl$,
        trusted_schema
    );
    EXECUTE format('ALTER FUNCTION %I.run_expiry_cleanup() OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format('REVOKE ALL ON FUNCTION %I.run_expiry_cleanup() FROM PUBLIC', trusted_schema);
    EXECUTE format('GRANT EXECUTE ON FUNCTION %I.run_expiry_cleanup() TO %I', trusted_schema, worker_role);
END;
$outer$;
-- +goose StatementEnd

-- +goose Down

DROP FUNCTION IF EXISTS run_expiry_cleanup();
