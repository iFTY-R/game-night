-- +goose Up

-- Resource authorization is stored separately from game-owned replay projections and raw event history.
CREATE TABLE game_session_replay_access (
    session_id uuid PRIMARY KEY,
    room_id uuid NOT NULL,
    policy text NOT NULL DEFAULT 'participant',
    policy_version bigint NOT NULL DEFAULT 1 CHECK (policy_version > 0),
    member_snapshot_completed_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT game_session_replay_access_session_fk
        FOREIGN KEY (session_id)
        REFERENCES game_sessions (session_id)
        ON DELETE RESTRICT,
    CONSTRAINT game_session_replay_access_policy_check
        CHECK (policy IN ('participant', 'room_member', 'public')),
    CONSTRAINT game_session_replay_access_time_invariant CHECK (
        updated_at >= created_at
        AND (member_snapshot_completed_at IS NULL OR member_snapshot_completed_at >= created_at)
    )
);

-- Existing sessions retain participant-only access. Their historical room-member set cannot be reconstructed safely.
INSERT INTO game_session_replay_access (
    session_id, room_id, policy, policy_version, created_at, updated_at
)
SELECT session_id, room_id, 'participant', 1, started_at, COALESCE(ended_at, started_at)
FROM game_sessions;

-- This immutable set is captured in the same transaction that makes a normal session terminal.
CREATE TABLE game_session_replay_members (
    session_id uuid NOT NULL REFERENCES game_session_replay_access (session_id) ON DELETE RESTRICT,
    user_id uuid NOT NULL REFERENCES users (user_id) ON DELETE RESTRICT,
    role text NOT NULL CHECK (role IN ('participant', 'spectator', 'waiting')),
    PRIMARY KEY (session_id, user_id)
);

CREATE INDEX game_session_replay_members_user_idx
    ON game_session_replay_members (user_id, session_id);

-- +goose StatementBegin
DO $permissions$
DECLARE
    trusted_schema text := current_schema();
    owner_role text := current_setting('game_night.owner_role');
    runtime_role text := current_setting('game_night.runtime_role');
BEGIN
    IF trusted_schema IS NULL THEN
        RAISE EXCEPTION 'game replay access migration requires an explicit current schema';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = owner_role)
       OR NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = runtime_role) THEN
        RAISE EXCEPTION 'game replay access migration requires configured owner and runtime roles';
    END IF;

    EXECUTE format('ALTER TABLE %I.game_session_replay_access OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format('ALTER TABLE %I.game_session_replay_members OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format(
        'REVOKE ALL ON TABLE %I.game_session_replay_access, %I.game_session_replay_members FROM PUBLIC',
        trusted_schema,
        trusted_schema
    );
    EXECUTE format(
        'GRANT SELECT, INSERT, UPDATE ON TABLE %I.game_session_replay_access, %I.game_session_replay_members TO %I',
        trusted_schema,
        trusted_schema,
        runtime_role
    );
END;
$permissions$;
-- +goose StatementEnd

-- +goose Down

DROP TABLE IF EXISTS game_session_replay_members;
DROP TABLE IF EXISTS game_session_replay_access;
