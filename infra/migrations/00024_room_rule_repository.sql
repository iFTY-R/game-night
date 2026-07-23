-- +goose Up

ALTER TABLE party_rooms
    ADD COLUMN selected_game_id text NOT NULL DEFAULT 'liars-dice',
    ADD COLUMN ownership_epoch bigint NOT NULL DEFAULT 1,
    ADD CONSTRAINT party_rooms_selected_game_shape CHECK (
        length(selected_game_id) BETWEEN 1 AND 64
        AND selected_game_id ~ '^[a-z0-9]+([._-][a-z0-9]+)*$'
    ),
    ADD CONSTRAINT party_rooms_ownership_epoch_positive CHECK (ownership_epoch > 0);

CREATE TABLE room_game_config_drafts (
    room_id uuid NOT NULL REFERENCES party_rooms (room_id) ON DELETE CASCADE,
    game_id text NOT NULL,
    engine_version text NOT NULL,
    protocol_version text NOT NULL,
    client_version text NOT NULL,
    config_schema_version integer NOT NULL CHECK (config_schema_version > 0),
    config_message_type text NOT NULL,
    config_payload bytea NOT NULL CHECK (octet_length(config_payload) <= 1048576),
    revision bigint NOT NULL CHECK (revision > 0),
    updated_by uuid NOT NULL REFERENCES users (user_id),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (room_id, game_id),
    CONSTRAINT room_game_config_drafts_game_shape CHECK (
        length(game_id) BETWEEN 1 AND 64
        AND game_id ~ '^[a-z0-9]+([._-][a-z0-9]+)*$'
    ),
    CONSTRAINT room_game_config_drafts_engine_shape CHECK (length(engine_version) BETWEEN 1 AND 64 AND btrim(engine_version) = engine_version),
    CONSTRAINT room_game_config_drafts_protocol_shape CHECK (length(protocol_version) BETWEEN 1 AND 64 AND btrim(protocol_version) = protocol_version),
    CONSTRAINT room_game_config_drafts_client_shape CHECK (length(client_version) BETWEEN 1 AND 64 AND btrim(client_version) = client_version),
    CONSTRAINT room_game_config_drafts_message_shape CHECK (
        length(config_message_type) BETWEEN 1 AND 128
        AND btrim(config_message_type) = config_message_type
    )
);

CREATE TABLE game_rule_presets (
    preset_id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
    game_id text NOT NULL,
    name text NOT NULL,
    engine_version text NOT NULL,
    protocol_version text NOT NULL,
    client_version text NOT NULL,
    config_schema_version integer NOT NULL CHECK (config_schema_version > 0),
    config_message_type text NOT NULL,
    config_payload bytea NOT NULL CHECK (octet_length(config_payload) <= 1048576),
    revision bigint NOT NULL CHECK (revision > 0),
    compatible boolean NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    last_used_at timestamptz NOT NULL,
    CONSTRAINT game_rule_presets_game_shape CHECK (
        length(game_id) BETWEEN 1 AND 64
        AND game_id ~ '^[a-z0-9]+([._-][a-z0-9]+)*$'
    ),
    CONSTRAINT game_rule_presets_name_shape CHECK (length(name) BETWEEN 1 AND 120 AND btrim(name) = name),
    CONSTRAINT game_rule_presets_engine_shape CHECK (length(engine_version) BETWEEN 1 AND 64 AND btrim(engine_version) = engine_version),
    CONSTRAINT game_rule_presets_protocol_shape CHECK (length(protocol_version) BETWEEN 1 AND 64 AND btrim(protocol_version) = protocol_version),
    CONSTRAINT game_rule_presets_client_shape CHECK (length(client_version) BETWEEN 1 AND 64 AND btrim(client_version) = client_version),
    CONSTRAINT game_rule_presets_message_shape CHECK (
        length(config_message_type) BETWEEN 1 AND 128
        AND btrim(config_message_type) = config_message_type
    ),
    CONSTRAINT game_rule_presets_time_invariant CHECK (updated_at >= created_at AND last_used_at >= created_at)
);

CREATE INDEX game_rule_presets_owner_game_updated_idx
    ON game_rule_presets (owner_user_id, game_id, updated_at DESC, preset_id);
CREATE INDEX game_rule_presets_owner_updated_idx
    ON game_rule_presets (owner_user_id, updated_at DESC, preset_id);

CREATE TABLE room_pending_starts (
    pending_start_id uuid PRIMARY KEY,
    room_id uuid NOT NULL REFERENCES party_rooms (room_id) ON DELETE CASCADE,
    cancel_token text NOT NULL,
    game_id text NOT NULL,
    config_revision bigint NOT NULL CHECK (config_revision > 0),
    expected_room_version bigint NOT NULL CHECK (expected_room_version > 0),
    expected_membership_version bigint NOT NULL CHECK (expected_membership_version > 0),
    ownership_epoch bigint NOT NULL CHECK (ownership_epoch > 0),
    operation_id text NOT NULL,
    request_digest bytea NOT NULL CHECK (octet_length(request_digest) = 32),
    deadline_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    cancelled_at timestamptz,
    consumed_at timestamptz,
    CONSTRAINT room_pending_starts_cancel_token_shape CHECK (length(cancel_token) BETWEEN 22 AND 86),
    CONSTRAINT room_pending_starts_game_shape CHECK (
        length(game_id) BETWEEN 1 AND 64
        AND game_id ~ '^[a-z0-9]+([._-][a-z0-9]+)*$'
    ),
    CONSTRAINT room_pending_starts_operation_shape CHECK (length(operation_id) BETWEEN 1 AND 128 AND btrim(operation_id) = operation_id),
    CONSTRAINT room_pending_starts_time_invariant CHECK (
        deadline_at >= created_at
        AND (cancelled_at IS NULL OR cancelled_at >= created_at)
        AND (consumed_at IS NULL OR consumed_at >= created_at)
        AND NOT (cancelled_at IS NOT NULL AND consumed_at IS NOT NULL)
    )
);

CREATE UNIQUE INDEX room_pending_starts_active_unique_idx
    ON room_pending_starts (room_id)
    WHERE cancelled_at IS NULL AND consumed_at IS NULL;
CREATE UNIQUE INDEX room_pending_starts_operation_unique_idx
    ON room_pending_starts (operation_id);
CREATE INDEX room_pending_starts_room_created_idx
    ON room_pending_starts (room_id, created_at DESC, pending_start_id DESC);

CREATE TABLE room_rule_operation_records (
    operation_id text NOT NULL,
    operation_kind text NOT NULL CHECK (operation_kind IN ('draft_update', 'preset_save', 'preset_delete', 'pending_start_begin')),
    request_digest bytea NOT NULL CHECK (octet_length(request_digest) = 32),
    room_id uuid REFERENCES party_rooms (room_id) ON DELETE CASCADE,
    owner_user_id uuid REFERENCES users (user_id) ON DELETE CASCADE,
    preset_id uuid,
    pending_start_id uuid,
    game_id text,
    result_revision bigint CHECK (result_revision IS NULL OR result_revision > 0),
    engine_version text,
    protocol_version text,
    client_version text,
    config_schema_version integer CHECK (config_schema_version IS NULL OR config_schema_version > 0),
    config_message_type text,
    config_payload bytea CHECK (config_payload IS NULL OR octet_length(config_payload) <= 1048576),
    result_name text,
    result_created_at timestamptz,
    result_updated_at timestamptz,
    result_last_used_at timestamptz,
    result_compatible boolean,
    result_updated_by uuid REFERENCES users (user_id),
    cancel_token text,
    deadline_at timestamptz,
    expected_room_version bigint CHECK (expected_room_version IS NULL OR expected_room_version > 0),
    expected_membership_version bigint CHECK (expected_membership_version IS NULL OR expected_membership_version > 0),
    ownership_epoch bigint CHECK (ownership_epoch IS NULL OR ownership_epoch > 0),
    config_revision bigint CHECK (config_revision IS NULL OR config_revision > 0),
    created_at timestamptz NOT NULL,
    CONSTRAINT room_rule_operation_records_kind_operation_unique UNIQUE (operation_kind, operation_id),
    CONSTRAINT room_rule_operation_records_operation_shape CHECK (length(operation_id) BETWEEN 1 AND 128 AND btrim(operation_id) = operation_id),
    CONSTRAINT room_rule_operation_records_game_shape CHECK (
        game_id IS NULL
        OR (
            length(game_id) BETWEEN 1 AND 64
            AND game_id ~ '^[a-z0-9]+([._-][a-z0-9]+)*$'
        )
    )
);

-- These objects postdate the base permission migrations, so owner and runtime grants stay explicit.
-- +goose StatementBegin
DO $permissions$
DECLARE
    trusted_schema text := current_schema();
    owner_role text := current_setting('game_night.owner_role');
    runtime_role text := current_setting('game_night.runtime_role');
    table_name text;
BEGIN
    IF trusted_schema IS NULL THEN
        RAISE EXCEPTION 'room rule repository migration requires an explicit current schema';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = owner_role)
       OR NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = runtime_role) THEN
        RAISE EXCEPTION 'room rule repository migration requires configured owner and runtime roles';
    END IF;

    FOREACH table_name IN ARRAY ARRAY[
        'room_game_config_drafts',
        'game_rule_presets',
        'room_pending_starts',
        'room_rule_operation_records'
    ] LOOP
        EXECUTE format('ALTER TABLE %I.%I OWNER TO %I', trusted_schema, table_name, owner_role);
        EXECUTE format('REVOKE ALL ON TABLE %I.%I FROM PUBLIC', trusted_schema, table_name);
        EXECUTE format(
            'GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE %I.%I TO %I',
            trusted_schema,
            table_name,
            runtime_role
        );
    END LOOP;
END;
$permissions$;
-- +goose StatementEnd

-- +goose Down

DROP TABLE IF EXISTS room_rule_operation_records;
DROP TABLE IF EXISTS room_pending_starts;
DROP TABLE IF EXISTS game_rule_presets;
DROP TABLE IF EXISTS room_game_config_drafts;

ALTER TABLE party_rooms
    DROP CONSTRAINT IF EXISTS party_rooms_ownership_epoch_positive,
    DROP CONSTRAINT IF EXISTS party_rooms_selected_game_shape,
    DROP COLUMN IF EXISTS ownership_epoch,
    DROP COLUMN IF EXISTS selected_game_id;
