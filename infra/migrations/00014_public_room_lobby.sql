-- +goose Up

-- Match the mixed-direction keyset exactly so public lobby pages avoid a sort as the room table grows.
DROP INDEX party_rooms_public_lobby_idx;
CREATE INDEX party_rooms_public_lobby_idx
    ON party_rooms (updated_at DESC, room_id DESC)
    WHERE visibility = 'public' AND status <> 'closed';

-- +goose Down

DROP INDEX party_rooms_public_lobby_idx;
CREATE INDEX party_rooms_public_lobby_idx
    ON party_rooms (updated_at DESC, room_id)
    WHERE visibility = 'public' AND status <> 'closed';
