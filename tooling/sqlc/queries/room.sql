-- name: CreatePartyRoom :one
INSERT INTO party_rooms (
    room_id,
    room_code,
    visibility,
    status,
    host_user_id,
    participant_capacity,
    participant_admission,
    spectator_admission,
    active_session_id,
    active_game_id,
    room_version,
    membership_version,
    created_at,
    updated_at
) VALUES (
    sqlc.arg(room_id),
    sqlc.arg(room_code),
    sqlc.arg(visibility),
    sqlc.arg(status),
    sqlc.arg(host_user_id),
    sqlc.arg(participant_capacity),
    sqlc.arg(participant_admission),
    sqlc.arg(spectator_admission),
    sqlc.narg(active_session_id),
    sqlc.narg(active_game_id),
    sqlc.arg(room_version),
    sqlc.arg(membership_version),
    sqlc.arg(created_at),
    sqlc.arg(updated_at)
)
RETURNING room_id, room_code, visibility, status, host_user_id, participant_capacity,
    participant_admission, spectator_admission, active_session_id, active_game_id,
    room_version, membership_version, created_at, updated_at;

-- name: GetPartyRoomForShare :one
SELECT room_id, room_code, visibility, status, host_user_id, participant_capacity,
    participant_admission, spectator_admission, active_session_id, active_game_id,
    room_version, membership_version, created_at, updated_at
FROM party_rooms
WHERE room_id = sqlc.arg(room_id)
FOR SHARE;

-- name: GetPartyRoomByCodeForShare :one
SELECT room_id, room_code, visibility, status, host_user_id, participant_capacity,
    participant_admission, spectator_admission, active_session_id, active_game_id,
    room_version, membership_version, created_at, updated_at
FROM party_rooms
WHERE room_code = sqlc.arg(room_code)
FOR SHARE;

-- name: UpdatePartyRoomCAS :one
UPDATE party_rooms
SET visibility = sqlc.arg(visibility),
    status = sqlc.arg(status),
    host_user_id = sqlc.arg(host_user_id),
    participant_capacity = sqlc.arg(participant_capacity),
    participant_admission = sqlc.arg(participant_admission),
    spectator_admission = sqlc.arg(spectator_admission),
    active_session_id = sqlc.narg(active_session_id),
    active_game_id = sqlc.narg(active_game_id),
    room_version = sqlc.arg(room_version),
    membership_version = sqlc.arg(membership_version),
    updated_at = sqlc.arg(updated_at)
WHERE room_id = sqlc.arg(room_id)
  AND room_code = sqlc.arg(room_code)
  AND room_version = sqlc.arg(expected_room_version)
  AND membership_version = sqlc.arg(expected_membership_version)
RETURNING room_id, room_code, visibility, status, host_user_id, participant_capacity,
    participant_admission, spectator_admission, active_session_id, active_game_id,
    room_version, membership_version, created_at, updated_at;

-- name: DeleteRoomMembers :exec
DELETE FROM room_members WHERE room_id = sqlc.arg(room_id);

-- name: CreateRoomMember :exec
INSERT INTO room_members (
    room_id,
    user_id,
    role,
    requested_role,
    seat_index,
    joined_at,
    last_seen_at
) VALUES (
    sqlc.arg(room_id),
    sqlc.arg(user_id),
    sqlc.arg(role),
    sqlc.narg(requested_role),
    sqlc.narg(seat_index),
    sqlc.arg(joined_at),
    sqlc.arg(last_seen_at)
);

-- name: ListRoomMembers :many
SELECT room_id, user_id, role, requested_role, seat_index, joined_at, last_seen_at
FROM room_members
WHERE room_id = sqlc.arg(room_id)
ORDER BY joined_at, user_id;
