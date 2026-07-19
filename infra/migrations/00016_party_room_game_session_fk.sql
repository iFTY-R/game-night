-- +goose Up

-- The reverse reference is composite so a stale room cannot point at a session from another room or game.
ALTER TABLE game_sessions
    ADD CONSTRAINT game_sessions_session_room_game_unique
    UNIQUE (session_id, room_id, game_id);

-- The deferred cycle lets one transaction publish the session and room pointer in either write order.
ALTER TABLE party_rooms
    ADD CONSTRAINT party_rooms_active_session_fk
    FOREIGN KEY (active_session_id, room_id, active_game_id)
    REFERENCES game_sessions (session_id, room_id, game_id)
    DEFERRABLE INITIALLY DEFERRED;

-- +goose Down

ALTER TABLE party_rooms DROP CONSTRAINT IF EXISTS party_rooms_active_session_fk;
ALTER TABLE game_sessions DROP CONSTRAINT IF EXISTS game_sessions_session_room_game_unique;
