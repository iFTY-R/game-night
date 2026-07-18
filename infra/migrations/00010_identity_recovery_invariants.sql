-- +goose Up

ALTER TABLE user_recovery_credentials
    ADD CONSTRAINT user_recovery_credentials_id_user_unique
        UNIQUE (recovery_credential_id, user_id),
    ADD CONSTRAINT user_recovery_credentials_terminal_time_invariant CHECK (
        (status <> 'consumed' OR consumed_at >= created_at)
        AND (status <> 'revoked' OR revoked_at >= created_at)
    ),
    ADD CONSTRAINT user_recovery_credentials_revoke_reason_invariant CHECK (
        (status <> 'revoked' AND revoke_reason IS NULL)
        OR (
            status = 'revoked'
            AND revoke_reason IS NOT NULL
            AND revoke_reason IN (
                'user_requested',
                'rotated',
                'account_suspended',
                'account_deleted',
                'assisted_recovery'
            )
        )
    );

ALTER TABLE admin_assisted_recovery_grants
    ADD CONSTRAINT admin_assisted_recovery_grants_id_user_unique
        UNIQUE (assisted_grant_id, user_id),
    ADD CONSTRAINT admin_assisted_recovery_grants_lifecycle_invariant CHECK (
        purpose = 'identity.assisted_recovery'
        AND expires_at = created_at + INTERVAL '900 seconds'
        AND max_attempts BETWEEN 1 AND 5
        AND attempt_count BETWEEN 0 AND max_attempts
        AND (
            (status = 'active' AND attempt_count < max_attempts)
            OR (
                status = 'consumed'
                AND attempt_count < max_attempts
                AND consumed_at >= created_at
                AND consumed_at < expires_at
            )
            OR (status = 'revoked' AND revoked_at >= created_at)
            OR status = 'expired'
        )
    );

ALTER TABLE user_recovery_attempts
    DROP CONSTRAINT user_recovery_attempts_result_id_fkey,
    ADD CONSTRAINT user_recovery_attempts_result_id_fkey
        FOREIGN KEY (result_id)
        REFERENCES secret_operation_results (result_id)
        DEFERRABLE INITIALLY DEFERRED,
    ADD CONSTRAINT user_recovery_attempts_credential_owner_fk
        FOREIGN KEY (recovery_credential_id, user_id)
        REFERENCES user_recovery_credentials (recovery_credential_id, user_id)
        DEFERRABLE INITIALLY DEFERRED,
    ADD CONSTRAINT user_recovery_attempts_assisted_owner_fk
        FOREIGN KEY (assisted_grant_id, user_id)
        REFERENCES admin_assisted_recovery_grants (assisted_grant_id, user_id)
        DEFERRABLE INITIALLY DEFERRED,
    ADD CONSTRAINT user_recovery_attempts_lifecycle_invariant CHECK (
        purpose = 'identity.recovery'
        AND (
            (status = 'consumed' AND request_digest IS NOT NULL AND octet_length(request_digest) = 32)
            OR (status <> 'consumed' AND request_digest IS NULL)
        )
        AND expires_at = created_at + INTERVAL '300 seconds'
        AND (recovery_credential_version IS NULL OR recovery_credential_version > 0)
        AND max_attempts BETWEEN 1 AND 5
        AND attempt_count BETWEEN 0 AND max_attempts
        AND (
            (status = 'active' AND attempt_count < max_attempts)
            OR (
                status = 'consumed'
                AND attempt_count < max_attempts
                AND consumed_at >= created_at
                AND consumed_at < expires_at
            )
            -- TTL cleanup may expire an otherwise unused attempt before its authentication ceiling is reached.
            OR status = 'expired'
            OR (status = 'revoked' AND revoked_at >= created_at)
        )
    );

ALTER TABLE admin_assisted_recovery_grants
    DROP CONSTRAINT admin_assisted_recovery_grants_result_id_fkey,
    ADD CONSTRAINT admin_assisted_recovery_grants_result_id_fkey
        FOREIGN KEY (result_id)
        REFERENCES secret_operation_results (result_id)
        DEFERRABLE INITIALLY DEFERRED;

-- +goose Down

ALTER TABLE admin_assisted_recovery_grants
    DROP CONSTRAINT IF EXISTS admin_assisted_recovery_grants_result_id_fkey,
    ADD CONSTRAINT admin_assisted_recovery_grants_result_id_fkey
        FOREIGN KEY (result_id)
        REFERENCES secret_operation_results (result_id);

ALTER TABLE user_recovery_attempts
    DROP CONSTRAINT IF EXISTS user_recovery_attempts_lifecycle_invariant,
    DROP CONSTRAINT IF EXISTS user_recovery_attempts_assisted_owner_fk,
    DROP CONSTRAINT IF EXISTS user_recovery_attempts_credential_owner_fk,
    DROP CONSTRAINT IF EXISTS user_recovery_attempts_result_id_fkey,
    ADD CONSTRAINT user_recovery_attempts_result_id_fkey
        FOREIGN KEY (result_id)
        REFERENCES secret_operation_results (result_id);

ALTER TABLE admin_assisted_recovery_grants
    DROP CONSTRAINT IF EXISTS admin_assisted_recovery_grants_lifecycle_invariant,
    DROP CONSTRAINT IF EXISTS admin_assisted_recovery_grants_id_user_unique;

ALTER TABLE user_recovery_credentials
    DROP CONSTRAINT IF EXISTS user_recovery_credentials_revoke_reason_invariant,
    DROP CONSTRAINT IF EXISTS user_recovery_credentials_terminal_time_invariant,
    DROP CONSTRAINT IF EXISTS user_recovery_credentials_id_user_unique;
