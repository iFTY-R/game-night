-- +goose Up

ALTER TABLE game_sessions
    ADD COLUMN start_config_message_type text,
    ADD COLUMN start_config_schema_version integer,
    ADD COLUMN start_config_payload bytea,
    ADD COLUMN start_config_digest bytea,
    ADD COLUMN start_config_revision bigint,
    ADD COLUMN start_room_version bigint,
    ADD COLUMN start_membership_version bigint,
    ADD COLUMN start_ownership_epoch bigint,
    ADD CONSTRAINT game_sessions_start_snapshot_shape CHECK (
        (
            start_config_message_type IS NULL
            AND start_config_schema_version IS NULL
            AND start_config_payload IS NULL
            AND start_config_digest IS NULL
            AND start_config_revision IS NULL
            AND start_room_version IS NULL
            AND start_membership_version IS NULL
            AND start_ownership_epoch IS NULL
        )
        OR (
            start_config_message_type IS NOT NULL
            AND start_config_schema_version IS NOT NULL
            AND start_config_payload IS NOT NULL
            AND start_config_digest IS NOT NULL
            AND start_config_revision IS NOT NULL
            AND start_room_version IS NOT NULL
            AND start_membership_version IS NOT NULL
            AND start_ownership_epoch IS NOT NULL
            AND length(start_config_message_type) BETWEEN 1 AND 64
            AND start_config_message_type ~ '^[a-z0-9]+([._-][a-z0-9]+)*$'
            AND start_config_schema_version > 0
            AND octet_length(start_config_payload) <= 1048576
            AND octet_length(start_config_digest) = 32
            AND start_config_revision >= 0
            AND start_room_version > 0
            AND start_membership_version > 0
            AND start_ownership_epoch > 0
        )
    );

-- +goose Down

ALTER TABLE game_sessions
    DROP CONSTRAINT IF EXISTS game_sessions_start_snapshot_shape,
    DROP COLUMN IF EXISTS start_ownership_epoch,
    DROP COLUMN IF EXISTS start_membership_version,
    DROP COLUMN IF EXISTS start_room_version,
    DROP COLUMN IF EXISTS start_config_revision,
    DROP COLUMN IF EXISTS start_config_digest,
    DROP COLUMN IF EXISTS start_config_payload,
    DROP COLUMN IF EXISTS start_config_schema_version,
    DROP COLUMN IF EXISTS start_config_message_type;
