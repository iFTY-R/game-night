-- +goose Up

CREATE TABLE user_profiles (
    user_id uuid PRIMARY KEY REFERENCES users (user_id),
    real_name_ciphertext bytea NOT NULL,
    real_name_nonce bytea NOT NULL,
    real_name_key_version integer NOT NULL CHECK (real_name_key_version > 0),
    profile_version bigint NOT NULL CHECK (profile_version > 0),
    real_name_updated_at timestamptz NOT NULL,
    real_name_updated_by uuid NOT NULL REFERENCES admin_accounts (admin_id)
);

CREATE INDEX user_profiles_key_version_idx ON user_profiles (real_name_key_version, user_id);

CREATE TABLE profile_export_contexts (
    export_id uuid PRIMARY KEY,
    created_by_admin_id uuid NOT NULL REFERENCES admin_accounts (admin_id),
    filter_digest bytea NOT NULL CHECK (octet_length(filter_digest) = 32),
    requested_fields text[] NOT NULL CHECK (cardinality(requested_fields) > 0),
    schema_version integer NOT NULL CHECK (schema_version > 0),
    item_count bigint NOT NULL CHECK (item_count >= 0),
    status text NOT NULL CHECK (status IN ('active', 'completed', 'aborted', 'expired')),
    reason text NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    completed_at timestamptz,
    aborted_at timestamptz,
    expired_at timestamptz,
    CHECK (expires_at > created_at),
    CHECK (
        (status = 'active' AND completed_at IS NULL AND aborted_at IS NULL AND expired_at IS NULL)
        OR (status = 'completed' AND completed_at IS NOT NULL AND aborted_at IS NULL AND expired_at IS NULL)
        OR (status = 'aborted' AND completed_at IS NULL AND aborted_at IS NOT NULL AND expired_at IS NULL)
        OR (status = 'expired' AND completed_at IS NULL AND aborted_at IS NULL AND expired_at IS NOT NULL)
    )
);

CREATE INDEX profile_export_contexts_expiry_idx
    ON profile_export_contexts (expires_at)
    WHERE status = 'active';

CREATE TABLE profile_export_items (
    export_id uuid NOT NULL REFERENCES profile_export_contexts (export_id) ON DELETE CASCADE,
    ordinal bigint NOT NULL CHECK (ordinal > 0),
    user_id uuid NOT NULL REFERENCES users (user_id),
    username text NOT NULL,
    profile_version bigint,
    real_name_ciphertext bytea,
    real_name_nonce bytea,
    real_name_key_version integer,
    PRIMARY KEY (export_id, ordinal),
    UNIQUE (export_id, user_id),
    CHECK (
        (profile_version IS NULL AND real_name_ciphertext IS NULL AND real_name_nonce IS NULL AND real_name_key_version IS NULL)
        OR (
            profile_version > 0
            AND real_name_ciphertext IS NOT NULL
            AND real_name_nonce IS NOT NULL
            AND real_name_key_version > 0
        )
    )
);

CREATE INDEX profile_export_items_key_version_idx
    ON profile_export_items (real_name_key_version, export_id, ordinal)
    WHERE real_name_key_version IS NOT NULL;

-- +goose Down

DROP TABLE IF EXISTS profile_export_items;
DROP TABLE IF EXISTS profile_export_contexts;
DROP TABLE IF EXISTS user_profiles;
