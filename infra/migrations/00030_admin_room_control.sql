-- +goose Up

CREATE TABLE admin_repair_operations (
    repair_id uuid PRIMARY KEY,
    repair_type text NOT NULL CHECK (repair_type IN (
        'clear_stale_owner_lease',
        'terminate_unrecoverable_game',
        'repair_room_game_link'
    )),
    state text NOT NULL CHECK (state IN ('previewed', 'executed', 'rejected', 'expired')),
    target_id uuid NOT NULL,
    target_kind text NOT NULL CHECK (target_kind IN ('room', 'game_session')),
    target_digest bytea NOT NULL CHECK (octet_length(target_digest) = 32),
    preview_digest bytea NOT NULL CHECK (octet_length(preview_digest) = 32),
    command_version bigint NOT NULL CHECK (command_version > 0),
    expected_room_version bigint CHECK (expected_room_version IS NULL OR expected_room_version > 0),
    expected_membership_version bigint CHECK (expected_membership_version IS NULL OR expected_membership_version > 0),
    expected_state_version bigint CHECK (expected_state_version IS NULL OR expected_state_version > 0),
    expected_ownership_epoch bigint CHECK (expected_ownership_epoch IS NULL OR expected_ownership_epoch >= 0),
    summary text NOT NULL CHECK (char_length(summary) BETWEEN 1 AND 2000),
    irreversible_effects text[] NOT NULL DEFAULT ARRAY[]::text[] CHECK (cardinality(irreversible_effects) <= 16),
    before_snapshot_digest bytea NOT NULL CHECK (octet_length(before_snapshot_digest) = 32),
    after_snapshot_digest bytea CHECK (after_snapshot_digest IS NULL OR octet_length(after_snapshot_digest) = 32),
    requested_by_admin_id uuid NOT NULL REFERENCES admin_accounts (admin_id),
    operation_id text,
    request_digest bytea CHECK (request_digest IS NULL OR octet_length(request_digest) = 32),
    audit_event_id uuid,
    reason text NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 512),
    version bigint NOT NULL CHECK (version > 0),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    executed_at timestamptz,
    CONSTRAINT admin_repair_operations_time_invariant CHECK (
        expires_at > created_at
        AND (executed_at IS NULL OR executed_at >= created_at)
    ),
    CONSTRAINT admin_repair_operations_state_invariant CHECK (
        (
            state = 'previewed'
            AND operation_id IS NULL
            AND request_digest IS NULL
            AND audit_event_id IS NULL
            AND executed_at IS NULL
        )
        OR (
            state IN ('executed', 'rejected')
            AND operation_id IS NOT NULL
            AND request_digest IS NOT NULL
            AND audit_event_id IS NOT NULL
            AND executed_at IS NOT NULL
        )
        OR (
            state = 'expired'
            AND operation_id IS NULL
            AND request_digest IS NULL
            AND audit_event_id IS NULL
            AND executed_at IS NULL
        )
    )
);

CREATE UNIQUE INDEX admin_repair_operations_actor_operation_idx
    ON admin_repair_operations (requested_by_admin_id, operation_id)
    WHERE operation_id IS NOT NULL;
CREATE INDEX admin_repair_operations_target_idx
    ON admin_repair_operations (target_kind, target_id, created_at DESC, repair_id DESC);
CREATE INDEX admin_repair_operations_state_expiry_idx
    ON admin_repair_operations (state, expires_at, repair_id)
    WHERE state = 'previewed';

CREATE INDEX party_rooms_admin_status_updated_idx
    ON party_rooms (status, updated_at DESC, room_id DESC);
CREATE INDEX party_rooms_admin_created_idx
    ON party_rooms (created_at DESC, room_id DESC);
CREATE INDEX party_rooms_admin_game_idx
    ON party_rooms (active_game_id, updated_at DESC, room_id DESC)
    WHERE active_game_id IS NOT NULL;
CREATE INDEX party_rooms_admin_host_idx
    ON party_rooms (host_user_id, updated_at DESC, room_id DESC);
CREATE INDEX room_members_admin_member_idx
    ON room_members (user_id, joined_at DESC, room_id);
CREATE INDEX game_sessions_admin_status_updated_idx
    ON game_sessions (status, updated_at DESC, session_id DESC);
CREATE INDEX game_sessions_admin_game_updated_idx
    ON game_sessions (game_id, updated_at DESC, session_id DESC);
CREATE INDEX game_sessions_admin_room_updated_idx
    ON game_sessions (room_id, updated_at DESC, session_id DESC);
CREATE INDEX game_session_event_batches_admin_recent_idx
    ON game_session_event_batches (session_id, committed_at DESC, state_version DESC);

-- +goose StatementBegin
DO $permissions$
DECLARE
    trusted_schema text := current_schema();
    owner_role text := current_setting('game_night.owner_role');
    runtime_role text := current_setting('game_night.runtime_role');
BEGIN
    IF trusted_schema IS NULL THEN
        RAISE EXCEPTION 'admin room control migration requires an explicit current schema';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = owner_role)
       OR NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = runtime_role) THEN
        RAISE EXCEPTION 'admin room control migration requires configured owner and runtime roles';
    END IF;

    EXECUTE format('ALTER TABLE %I.admin_repair_operations OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format('REVOKE ALL ON TABLE %I.admin_repair_operations FROM PUBLIC', trusted_schema);
    EXECUTE format(
        'GRANT SELECT, INSERT, UPDATE ON TABLE %I.admin_repair_operations TO %I',
        trusted_schema,
        runtime_role
    );
END;
$permissions$;
-- +goose StatementEnd

-- +goose Down

DROP TABLE IF EXISTS admin_repair_operations;
DROP INDEX IF EXISTS game_session_event_batches_admin_recent_idx;
DROP INDEX IF EXISTS game_sessions_admin_room_updated_idx;
DROP INDEX IF EXISTS game_sessions_admin_game_updated_idx;
DROP INDEX IF EXISTS game_sessions_admin_status_updated_idx;
DROP INDEX IF EXISTS room_members_admin_member_idx;
DROP INDEX IF EXISTS party_rooms_admin_host_idx;
DROP INDEX IF EXISTS party_rooms_admin_game_idx;
DROP INDEX IF EXISTS party_rooms_admin_created_idx;
DROP INDEX IF EXISTS party_rooms_admin_status_updated_idx;
