-- +goose Up

ALTER TABLE users
    ADD CONSTRAINT users_username_timestamp_invariant CHECK (
        created_at <= updated_at
        AND (
            (status = 'onboarding' AND username_changed_at IS NULL)
            OR (
                status IN ('active', 'suspended')
                AND username_changed_at IS NOT NULL
                AND username_changed_at BETWEEN created_at AND updated_at
            )
            OR (
                status = 'deleted'
                AND username IS NULL
                AND current_username_key IS NULL
                AND (username_changed_at IS NULL OR username_changed_at BETWEEN created_at AND updated_at)
            )
        )
    );

ALTER TABLE username_claims
    ADD CONSTRAINT username_claims_reservation_time_invariant CHECK (
        created_at <= updated_at
        AND (status = 'active' OR reserved_until > updated_at)
    );

ALTER TABLE device_credentials
    ADD CONSTRAINT device_credentials_time_invariant CHECK (
        created_at <= rotated_at
        AND rotated_at <= last_seen_at
        AND last_seen_at < idle_expires_at
        AND (revoked_at IS NULL OR revoked_at >= last_seen_at)
        AND absolute_expires_at = created_at + INTERVAL '31536000 seconds'
        AND idle_expires_at = LEAST(last_seen_at + INTERVAL '15552000 seconds', absolute_expires_at)
    ),
    ADD CONSTRAINT device_credentials_previous_time_invariant CHECK (
        previous_secret_hash IS NULL
        OR (
            generation >= 2
            AND previous_valid_until = LEAST(rotated_at + INTERVAL '120 seconds', absolute_expires_at)
        )
    ),
    ADD CONSTRAINT device_credentials_label_invariant CHECK (
        char_length(label) BETWEEN 1 AND 64
        AND octet_length(label) <= 1024
    ),
    ADD CONSTRAINT device_credentials_revoke_reason_invariant CHECK (
        revoke_reason IS NULL
        OR revoke_reason IN (
            'user_requested',
            'admin_requested',
            'recovery',
            'onboarding_expired',
            'account_suspended',
            'account_deleted'
        )
    );

-- +goose Down

ALTER TABLE device_credentials
    DROP CONSTRAINT IF EXISTS device_credentials_revoke_reason_invariant,
    DROP CONSTRAINT IF EXISTS device_credentials_label_invariant,
    DROP CONSTRAINT IF EXISTS device_credentials_previous_time_invariant,
    DROP CONSTRAINT IF EXISTS device_credentials_time_invariant;

ALTER TABLE username_claims
    DROP CONSTRAINT IF EXISTS username_claims_reservation_time_invariant;

ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_username_timestamp_invariant;
