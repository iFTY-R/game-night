-- +goose Up

-- A start receipt is committed with the room pointer and GameSession so retries can never create a second session.
CREATE TABLE game_session_start_receipts (
    actor_user_id uuid NOT NULL REFERENCES users (user_id),
    room_id uuid NOT NULL REFERENCES party_rooms (room_id),
    operation_id text NOT NULL,
    request_digest bytea NOT NULL CHECK (octet_length(request_digest) = 32),
    session_id uuid NOT NULL UNIQUE REFERENCES game_sessions (session_id) ON DELETE RESTRICT,
    committed_at timestamptz NOT NULL,
    PRIMARY KEY (actor_user_id, room_id, operation_id),
    CONSTRAINT game_session_start_receipts_operation_id_shape CHECK (
        length(operation_id) BETWEEN 22 AND 86 AND operation_id ~ '^[A-Za-z0-9_-]+$'
    )
);

-- +goose StatementBegin
DO $permissions$
DECLARE
    trusted_schema text := current_schema();
    owner_role text := current_setting('game_night.owner_role');
    runtime_role text := current_setting('game_night.runtime_role');
BEGIN
    IF trusted_schema IS NULL THEN
        RAISE EXCEPTION 'game start receipt migration requires an explicit current schema';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = owner_role)
       OR NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = runtime_role) THEN
        RAISE EXCEPTION 'game start receipt migration requires configured owner and runtime roles';
    END IF;

    EXECUTE format('ALTER TABLE %I.game_session_start_receipts OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format('REVOKE ALL ON TABLE %I.game_session_start_receipts FROM PUBLIC', trusted_schema);
    EXECUTE format(
        'GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE %I.game_session_start_receipts TO %I',
        trusted_schema,
        runtime_role
    );
END;
$permissions$;
-- +goose StatementEnd

-- +goose Down

DROP TABLE IF EXISTS game_session_start_receipts;
