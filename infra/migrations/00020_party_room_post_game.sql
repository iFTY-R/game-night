-- +goose Up

ALTER TABLE party_rooms DROP CONSTRAINT party_rooms_status_check;
ALTER TABLE party_rooms DROP CONSTRAINT party_rooms_session_invariant;

ALTER TABLE party_rooms
    ADD COLUMN last_finished_session_id uuid,
    ADD COLUMN last_finished_game_id text,
    ADD CONSTRAINT party_rooms_status_check CHECK (status IN ('lobby', 'playing', 'post_game', 'closed')),
    ADD CONSTRAINT party_rooms_session_invariant CHECK (
        (
            status = 'playing'
            AND active_session_id IS NOT NULL
            AND active_game_id IS NOT NULL
            AND active_game_id <> ''
            AND participant_admission = 'closed'
            AND last_finished_session_id IS NULL
            AND last_finished_game_id IS NULL
        )
        OR (
            status = 'post_game'
            AND active_session_id IS NULL
            AND active_game_id IS NULL
            AND last_finished_session_id IS NOT NULL
            AND last_finished_game_id IS NOT NULL
            AND last_finished_game_id <> ''
        )
        OR (
            status = 'lobby'
            AND active_session_id IS NULL
            AND active_game_id IS NULL
            AND last_finished_session_id IS NULL
            AND last_finished_game_id IS NULL
        )
        OR (
            status = 'closed'
            AND active_session_id IS NULL
            AND active_game_id IS NULL
            AND (last_finished_session_id IS NULL) = (last_finished_game_id IS NULL)
        )
    ),
    ADD CONSTRAINT party_rooms_last_finished_session_fk
        FOREIGN KEY (last_finished_session_id, room_id, last_finished_game_id)
        REFERENCES game_sessions (session_id, room_id, game_id)
        ON DELETE RESTRICT;

-- +goose Down

ALTER TABLE party_rooms DROP CONSTRAINT party_rooms_last_finished_session_fk;
ALTER TABLE party_rooms DROP CONSTRAINT party_rooms_session_invariant;
ALTER TABLE party_rooms DROP CONSTRAINT party_rooms_status_check;

UPDATE party_rooms
SET status = 'lobby'
WHERE status = 'post_game';

ALTER TABLE party_rooms
    DROP COLUMN last_finished_session_id,
    DROP COLUMN last_finished_game_id,
    ADD CONSTRAINT party_rooms_status_check CHECK (status IN ('lobby', 'playing', 'closed')),
    ADD CONSTRAINT party_rooms_session_invariant CHECK (
        (
            status = 'playing'
            AND active_session_id IS NOT NULL
            AND active_game_id IS NOT NULL
            AND active_game_id <> ''
            AND participant_admission = 'closed'
        )
        OR (
            status IN ('lobby', 'closed')
            AND active_session_id IS NULL
            AND active_game_id IS NULL
        )
    );
