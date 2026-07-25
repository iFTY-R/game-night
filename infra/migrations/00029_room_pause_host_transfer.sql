-- +goose Up

ALTER TABLE game_sessions
    ADD COLUMN suspended_at timestamptz;

-- Existing suspended sessions predate an explicit freeze boundary. Their last
-- durable lifecycle update is the only safe timestamp from which to resume.
UPDATE game_sessions
SET suspended_at = updated_at
WHERE status = 'suspended';

ALTER TABLE game_sessions
    ADD CONSTRAINT game_sessions_suspension_invariant CHECK (
        (
            status = 'suspended'
            AND suspended_at IS NOT NULL
            AND suspended_at BETWEEN started_at AND updated_at
        )
        OR (status <> 'suspended' AND suspended_at IS NULL)
    );

ALTER TABLE party_rooms
    ADD COLUMN pause_request_id uuid,
    ADD COLUMN pause_request_session_id uuid,
    ADD COLUMN pause_requested_by_user_id uuid,
    ADD COLUMN pause_requested_at timestamptz,
    ADD COLUMN active_pause_id uuid,
    ADD COLUMN active_pause_session_id uuid,
    ADD COLUMN active_pause_source text,
    ADD COLUMN active_pause_requested_by_user_id uuid,
    ADD COLUMN active_pause_paused_by_user_id uuid,
    ADD COLUMN active_pause_paused_at timestamptz,
    ADD CONSTRAINT party_rooms_pause_governance_invariant CHECK (
        NOT (
            pause_request_id IS NOT NULL
            AND active_pause_id IS NOT NULL
        )
        AND (
            (
                pause_request_id IS NULL
                AND pause_request_session_id IS NULL
                AND pause_requested_by_user_id IS NULL
                AND pause_requested_at IS NULL
            )
            OR (
                pause_request_id IS NOT NULL
                AND pause_request_session_id = active_session_id
                AND pause_requested_by_user_id IS NOT NULL
                AND pause_requested_by_user_id <> host_user_id
                AND pause_requested_at BETWEEN created_at AND updated_at
                AND status = 'playing'
            )
        )
        AND (
            (
                active_pause_id IS NULL
                AND active_pause_session_id IS NULL
                AND active_pause_source IS NULL
                AND active_pause_requested_by_user_id IS NULL
                AND active_pause_paused_by_user_id IS NULL
                AND active_pause_paused_at IS NULL
            )
            OR (
                active_pause_id IS NOT NULL
                AND active_pause_session_id = active_session_id
                AND active_pause_source IN ('host', 'approved_request')
                AND (
                    (active_pause_source = 'host' AND active_pause_requested_by_user_id IS NULL)
                    OR (active_pause_source = 'approved_request' AND active_pause_requested_by_user_id IS NOT NULL)
                )
                AND active_pause_paused_by_user_id IS NOT NULL
                AND active_pause_paused_at BETWEEN created_at AND updated_at
                AND status = 'playing'
            )
        )
    ),
    ADD CONSTRAINT party_rooms_pause_request_member_fk
        FOREIGN KEY (room_id, pause_requested_by_user_id)
        REFERENCES room_members (room_id, user_id)
        DEFERRABLE INITIALLY DEFERRED,
    ADD CONSTRAINT party_rooms_active_pause_requester_member_fk
        FOREIGN KEY (active_pause_requested_by_user_id)
        REFERENCES users (user_id),
    ADD CONSTRAINT party_rooms_active_pause_host_member_fk
        FOREIGN KEY (active_pause_paused_by_user_id)
        REFERENCES users (user_id);

-- +goose Down

ALTER TABLE party_rooms
    DROP CONSTRAINT IF EXISTS party_rooms_active_pause_host_member_fk,
    DROP CONSTRAINT IF EXISTS party_rooms_active_pause_requester_member_fk,
    DROP CONSTRAINT IF EXISTS party_rooms_pause_request_member_fk,
    DROP CONSTRAINT IF EXISTS party_rooms_pause_governance_invariant,
    DROP COLUMN IF EXISTS active_pause_paused_at,
    DROP COLUMN IF EXISTS active_pause_paused_by_user_id,
    DROP COLUMN IF EXISTS active_pause_requested_by_user_id,
    DROP COLUMN IF EXISTS active_pause_source,
    DROP COLUMN IF EXISTS active_pause_session_id,
    DROP COLUMN IF EXISTS active_pause_id,
    DROP COLUMN IF EXISTS pause_requested_at,
    DROP COLUMN IF EXISTS pause_requested_by_user_id,
    DROP COLUMN IF EXISTS pause_request_session_id,
    DROP COLUMN IF EXISTS pause_request_id;

ALTER TABLE game_sessions
    DROP CONSTRAINT IF EXISTS game_sessions_suspension_invariant,
    DROP COLUMN IF EXISTS suspended_at;
