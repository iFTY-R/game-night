-- +goose Up

-- The producer writes this neutral inbox row with the room mutation, so terminal commits can fence pending revocations.
CREATE TABLE game_system_inbox (
    session_id uuid NOT NULL REFERENCES game_sessions (session_id) ON DELETE RESTRICT,
    source_event_id uuid NOT NULL REFERENCES outbox_events (event_id) ON DELETE RESTRICT,
    event_type text NOT NULL,
    payload_digest bytea NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    committed_state_version bigint,
    created_at timestamptz NOT NULL,
    completed_at timestamptz,
    PRIMARY KEY (session_id, source_event_id),
    CONSTRAINT game_system_inbox_source_unique UNIQUE (source_event_id),
    CONSTRAINT game_system_inbox_event_type_check CHECK (event_type = 'room.participant.revoked.v1'),
    CONSTRAINT game_system_inbox_digest_shape CHECK (octet_length(payload_digest) = 32),
    CONSTRAINT game_system_inbox_status_check CHECK (status IN ('pending', 'completed')),
    CONSTRAINT game_system_inbox_completion_shape CHECK (
        (status = 'pending' AND committed_state_version IS NULL AND completed_at IS NULL)
        OR (status = 'completed' AND committed_state_version > 0 AND completed_at IS NOT NULL)
    ),
    CONSTRAINT game_system_inbox_time_invariant CHECK (completed_at IS NULL OR completed_at >= created_at)
);

CREATE INDEX game_system_inbox_pending_idx
    ON game_system_inbox (session_id, created_at, source_event_id)
    WHERE status = 'pending';

-- +goose StatementBegin
DO $permissions$
DECLARE
    trusted_schema text := current_schema();
    owner_role text := current_setting('game_night.owner_role');
    runtime_role text := current_setting('game_night.runtime_role');
BEGIN
    IF trusted_schema IS NULL THEN
        RAISE EXCEPTION 'game system inbox migration requires an explicit current schema';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = owner_role)
       OR NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = runtime_role) THEN
        RAISE EXCEPTION 'game system inbox migration requires configured owner and runtime roles';
    END IF;

    EXECUTE format('ALTER TABLE %I.game_system_inbox OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format('REVOKE ALL ON TABLE %I.game_system_inbox FROM PUBLIC', trusted_schema);
    EXECUTE format(
        'GRANT SELECT, INSERT, UPDATE ON TABLE %I.game_system_inbox TO %I',
        trusted_schema,
        runtime_role
    );
END;
$permissions$;
-- +goose StatementEnd

-- +goose Down

DROP TABLE IF EXISTS game_system_inbox;
