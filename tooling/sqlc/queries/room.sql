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
    last_finished_session_id,
    last_finished_game_id,
    selected_game_id,
    ownership_epoch,
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
    sqlc.narg(last_finished_session_id),
    sqlc.narg(last_finished_game_id),
    sqlc.arg(selected_game_id),
    sqlc.arg(ownership_epoch),
    sqlc.arg(room_version),
    sqlc.arg(membership_version),
    sqlc.arg(created_at),
    sqlc.arg(updated_at)
)
RETURNING room_id, room_code, visibility, status, host_user_id, participant_capacity,
    participant_admission, spectator_admission, active_session_id, active_game_id,
    room_version, membership_version, created_at, updated_at,
    last_finished_session_id, last_finished_game_id, selected_game_id, ownership_epoch;

-- name: CreateRoomActivityLease :exec
INSERT INTO room_activity_leases (room_id, last_seen_at)
VALUES (sqlc.arg(room_id), sqlc.arg(last_seen_at));

-- name: LockRoomActivityLease :one
SELECT last_seen_at
FROM room_activity_leases
WHERE room_id = sqlc.arg(room_id)
FOR UPDATE;

-- name: TouchRoomActivityLease :one
UPDATE room_activity_leases
SET last_seen_at = GREATEST(last_seen_at, pg_catalog.clock_timestamp())
WHERE room_id = sqlc.arg(room_id)
RETURNING last_seen_at;

-- name: GetPartyRoomForShare :one
SELECT room_id, room_code, visibility, status, host_user_id, participant_capacity,
    participant_admission, spectator_admission, active_session_id, active_game_id,
    room_version, membership_version, created_at, updated_at,
    last_finished_session_id, last_finished_game_id, selected_game_id, ownership_epoch
FROM party_rooms
WHERE room_id = sqlc.arg(room_id)
FOR SHARE;

-- name: GetPartyRoomForUpdate :one
SELECT room_id, room_code, visibility, status, host_user_id, participant_capacity,
    participant_admission, spectator_admission, active_session_id, active_game_id,
    room_version, membership_version, created_at, updated_at,
    last_finished_session_id, last_finished_game_id, selected_game_id, ownership_epoch
FROM party_rooms
WHERE room_id = sqlc.arg(room_id)
FOR UPDATE;

-- name: GetPartyRoomByCodeForShare :one
SELECT room_id, room_code, visibility, status, host_user_id, participant_capacity,
    participant_admission, spectator_admission, active_session_id, active_game_id,
    room_version, membership_version, created_at, updated_at,
    last_finished_session_id, last_finished_game_id, selected_game_id, ownership_epoch
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
    last_finished_session_id = sqlc.narg(last_finished_session_id),
    last_finished_game_id = sqlc.narg(last_finished_game_id),
    selected_game_id = sqlc.arg(selected_game_id),
    ownership_epoch = sqlc.arg(ownership_epoch),
    room_version = sqlc.arg(room_version),
    membership_version = sqlc.arg(membership_version),
    updated_at = sqlc.arg(updated_at)
WHERE room_id = sqlc.arg(room_id)
  AND room_code = sqlc.arg(room_code)
  AND ownership_epoch = sqlc.arg(expected_ownership_epoch)
  AND room_version = sqlc.arg(expected_room_version)
  AND membership_version = sqlc.arg(expected_membership_version)
RETURNING room_id, room_code, visibility, status, host_user_id, participant_capacity,
    participant_admission, spectator_admission, active_session_id, active_game_id,
    room_version, membership_version, created_at, updated_at,
    last_finished_session_id, last_finished_game_id, selected_game_id, ownership_epoch;

-- name: FinishPartyRoomCAS :one
UPDATE party_rooms
SET status = 'post_game',
    participant_admission = 'closed',
    last_finished_session_id = active_session_id,
    last_finished_game_id = active_game_id,
    active_session_id = NULL,
    active_game_id = NULL,
    room_version = sqlc.arg(room_version),
    updated_at = sqlc.arg(updated_at)
WHERE room_id = sqlc.arg(room_id)
  AND status = 'playing'
  AND active_session_id = sqlc.arg(active_session_id)
  AND active_game_id = sqlc.arg(active_game_id)
  AND room_version = sqlc.arg(expected_room_version)
  AND membership_version = sqlc.arg(expected_membership_version)
RETURNING room_id, room_code, visibility, status, host_user_id, participant_capacity,
    participant_admission, spectator_admission, active_session_id, active_game_id,
    room_version, membership_version, created_at, updated_at,
    last_finished_session_id, last_finished_game_id, selected_game_id, ownership_epoch;

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

-- name: ListRoomMemberUsernames :many
SELECT member.user_id, users.username
FROM room_members AS member
JOIN users ON users.user_id = member.user_id
WHERE member.room_id = sqlc.arg(room_id)
ORDER BY member.user_id;

-- name: GetRoomMemberRole :one
SELECT role
FROM room_members
WHERE room_id = sqlc.arg(room_id)
  AND user_id = sqlc.arg(user_id);

-- name: ListPublicRoomCards :many
SELECT room.room_id,
    host.username AS host_username,
    room.status,
    room.participant_capacity,
    counts.participant_count,
    counts.spectator_count,
    counts.waiting_count,
    room.participant_admission,
    room.spectator_admission,
    room.active_game_id,
    viewer.role AS viewer_role,
    viewer.requested_role AS viewer_requested_role,
    room.updated_at
FROM party_rooms AS room
JOIN users AS host
  ON host.user_id = room.host_user_id
 AND host.status = 'active'
JOIN LATERAL (
    SELECT count(*) FILTER (WHERE member.role = 'participant') AS participant_count,
        count(*) FILTER (WHERE member.role = 'spectator') AS spectator_count,
        count(*) FILTER (WHERE member.role = 'waiting') AS waiting_count
    FROM room_members AS member
    WHERE member.room_id = room.room_id
) AS counts ON true
LEFT JOIN room_members AS viewer
  ON viewer.room_id = room.room_id
 AND viewer.user_id = sqlc.arg(actor_user_id)
WHERE room.visibility = 'public'
  AND room.status <> 'closed'
  AND (
      (NOT sqlc.arg(include_lobby)::boolean AND NOT sqlc.arg(include_playing)::boolean AND NOT sqlc.arg(include_post_game)::boolean)
      OR (sqlc.arg(include_lobby)::boolean AND room.status = 'lobby')
      OR (sqlc.arg(include_playing)::boolean AND room.status = 'playing')
      OR (sqlc.arg(include_post_game)::boolean AND room.status = 'post_game')
  )
  AND (sqlc.arg(game_id)::text = '' OR room.active_game_id = sqlc.arg(game_id)::text)
  AND (
      NOT sqlc.arg(participant_joinable_only)::boolean
      OR (
          room.status IN ('lobby', 'post_game')
          AND room.participant_admission IN ('open', 'approval')
          AND counts.participant_count < room.participant_capacity
      )
  )
  AND (
      NOT sqlc.arg(has_after)::boolean
      OR (room.updated_at, room.room_id) < (sqlc.arg(after_updated_at)::timestamptz, sqlc.arg(after_room_id)::uuid)
  )
ORDER BY room.updated_at DESC, room.room_id DESC
LIMIT sqlc.arg(page_limit);

-- name: ListMyRoomCards :many
SELECT room.room_id,
    room.room_code,
    room.visibility,
    host.username AS host_username,
    room.status,
    room.host_user_id = sqlc.arg(actor_user_id) AS is_host,
    room.participant_capacity,
    counts.participant_count,
    counts.spectator_count,
    counts.waiting_count,
    room.participant_admission,
    room.spectator_admission,
    room.active_game_id,
    room.last_finished_game_id,
    viewer.role AS viewer_role,
    viewer.requested_role AS viewer_requested_role,
    room.updated_at
FROM room_members AS viewer
JOIN party_rooms AS room
  ON room.room_id = viewer.room_id
JOIN users AS host
  ON host.user_id = room.host_user_id
JOIN LATERAL (
    SELECT count(*) FILTER (WHERE member.role = 'participant') AS participant_count,
        count(*) FILTER (WHERE member.role = 'spectator') AS spectator_count,
        count(*) FILTER (WHERE member.role = 'waiting') AS waiting_count
    FROM room_members AS member
    WHERE member.room_id = room.room_id
) AS counts ON true
WHERE viewer.user_id = sqlc.arg(actor_user_id)
  AND room.status <> 'closed'
  AND (
      NOT sqlc.arg(has_after)::boolean
      OR (
          room.host_user_id = sqlc.arg(actor_user_id),
          room.updated_at,
          room.room_id
      ) < (
          sqlc.arg(after_is_host)::boolean,
          sqlc.arg(after_updated_at)::timestamptz,
          sqlc.arg(after_room_id)::uuid
      )
  )
ORDER BY (room.host_user_id = sqlc.arg(actor_user_id)) DESC, room.updated_at DESC, room.room_id DESC
LIMIT sqlc.arg(page_limit);

-- name: LockRoomRuleOperationKey :exec
SELECT pg_advisory_xact_lock(pg_catalog.hashtextextended(sqlc.arg(operation_kind)::text || ':' || sqlc.arg(operation_id)::text, 0));

-- name: GetRoomRuleOperationRecord :one
SELECT operation_id, operation_kind, request_digest, room_id, owner_user_id, preset_id,
    pending_start_id, game_id, result_revision, engine_version, protocol_version,
    client_version, config_schema_version, config_message_type, config_payload,
    result_name, result_created_at, result_updated_at, result_last_used_at,
    result_compatible, result_updated_by, cancel_token, deadline_at,
    expected_room_version, expected_membership_version, ownership_epoch,
    config_revision, created_at
FROM room_rule_operation_records
WHERE operation_kind = sqlc.arg(operation_kind)
  AND operation_id = sqlc.arg(operation_id);

-- name: CreateRoomRuleOperationRecord :one
INSERT INTO room_rule_operation_records (
    operation_id,
    operation_kind,
    request_digest,
    room_id,
    owner_user_id,
    preset_id,
    pending_start_id,
    game_id,
    result_revision,
    engine_version,
    protocol_version,
    client_version,
    config_schema_version,
    config_message_type,
    config_payload,
    result_name,
    result_created_at,
    result_updated_at,
    result_last_used_at,
    result_compatible,
    result_updated_by,
    cancel_token,
    deadline_at,
    expected_room_version,
    expected_membership_version,
    ownership_epoch,
    config_revision,
    created_at
) VALUES (
    sqlc.arg(operation_id),
    sqlc.arg(operation_kind),
    sqlc.arg(request_digest),
    sqlc.narg(room_id),
    sqlc.narg(owner_user_id),
    sqlc.narg(preset_id),
    sqlc.narg(pending_start_id),
    sqlc.narg(game_id),
    sqlc.narg(result_revision),
    sqlc.narg(engine_version),
    sqlc.narg(protocol_version),
    sqlc.narg(client_version),
    sqlc.narg(config_schema_version),
    sqlc.narg(config_message_type),
    sqlc.narg(config_payload),
    sqlc.narg(result_name),
    sqlc.narg(result_created_at),
    sqlc.narg(result_updated_at),
    sqlc.narg(result_last_used_at),
    sqlc.narg(result_compatible),
    sqlc.narg(result_updated_by),
    sqlc.narg(cancel_token),
    sqlc.narg(deadline_at),
    sqlc.narg(expected_room_version),
    sqlc.narg(expected_membership_version),
    sqlc.narg(ownership_epoch),
    sqlc.narg(config_revision),
    sqlc.arg(created_at)
)
RETURNING operation_id, operation_kind, request_digest, room_id, owner_user_id, preset_id,
    pending_start_id, game_id, result_revision, engine_version, protocol_version,
    client_version, config_schema_version, config_message_type, config_payload,
    result_name, result_created_at, result_updated_at, result_last_used_at,
    result_compatible, result_updated_by, cancel_token, deadline_at,
    expected_room_version, expected_membership_version, ownership_epoch,
    config_revision, created_at;

-- name: ListRoomGameConfigDrafts :many
SELECT room_id, game_id, engine_version, protocol_version, client_version,
    config_schema_version, config_message_type, config_payload, revision,
    updated_by, updated_at
FROM room_game_config_drafts
WHERE room_id = sqlc.arg(room_id)
ORDER BY game_id;

-- name: GetRoomGameConfigDraft :one
SELECT room_id, game_id, engine_version, protocol_version, client_version,
    config_schema_version, config_message_type, config_payload, revision,
    updated_by, updated_at
FROM room_game_config_drafts
WHERE room_id = sqlc.arg(room_id)
  AND game_id = sqlc.arg(game_id);

-- name: GetRoomGameConfigDraftForUpdate :one
SELECT room_id, game_id, engine_version, protocol_version, client_version,
    config_schema_version, config_message_type, config_payload, revision,
    updated_by, updated_at
FROM room_game_config_drafts
WHERE room_id = sqlc.arg(room_id)
  AND game_id = sqlc.arg(game_id)
FOR UPDATE;

-- name: CreateRoomGameConfigDraft :one
INSERT INTO room_game_config_drafts (
    room_id,
    game_id,
    engine_version,
    protocol_version,
    client_version,
    config_schema_version,
    config_message_type,
    config_payload,
    revision,
    updated_by,
    updated_at
) VALUES (
    sqlc.arg(room_id),
    sqlc.arg(game_id),
    sqlc.arg(engine_version),
    sqlc.arg(protocol_version),
    sqlc.arg(client_version),
    sqlc.arg(config_schema_version),
    sqlc.arg(config_message_type),
    sqlc.arg(config_payload),
    sqlc.arg(revision),
    sqlc.arg(updated_by),
    sqlc.arg(updated_at)
)
RETURNING room_id, game_id, engine_version, protocol_version, client_version,
    config_schema_version, config_message_type, config_payload, revision,
    updated_by, updated_at;

-- name: UpdateRoomGameConfigDraft :one
UPDATE room_game_config_drafts
SET engine_version = sqlc.arg(engine_version),
    protocol_version = sqlc.arg(protocol_version),
    client_version = sqlc.arg(client_version),
    config_schema_version = sqlc.arg(config_schema_version),
    config_message_type = sqlc.arg(config_message_type),
    config_payload = sqlc.arg(config_payload),
    revision = sqlc.arg(revision),
    updated_by = sqlc.arg(updated_by),
    updated_at = sqlc.arg(updated_at)
WHERE room_id = sqlc.arg(room_id)
  AND game_id = sqlc.arg(game_id)
  AND revision = sqlc.arg(expected_revision)
RETURNING room_id, game_id, engine_version, protocol_version, client_version,
    config_schema_version, config_message_type, config_payload, revision,
    updated_by, updated_at;

-- name: ListGameRulePresets :many
SELECT preset_id, owner_user_id, game_id, name, engine_version, protocol_version,
    client_version, config_schema_version, config_message_type, config_payload,
    revision, compatible, created_at, updated_at, last_used_at
FROM game_rule_presets
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND (sqlc.arg(game_id)::text = '' OR game_id = sqlc.arg(game_id)::text)
ORDER BY updated_at DESC, preset_id DESC;

-- name: GetGameRulePresetForUpdate :one
SELECT preset_id, owner_user_id, game_id, name, engine_version, protocol_version,
    client_version, config_schema_version, config_message_type, config_payload,
    revision, compatible, created_at, updated_at, last_used_at
FROM game_rule_presets
WHERE preset_id = sqlc.arg(preset_id)
FOR UPDATE;

-- name: CreateGameRulePreset :one
INSERT INTO game_rule_presets (
    preset_id,
    owner_user_id,
    game_id,
    name,
    engine_version,
    protocol_version,
    client_version,
    config_schema_version,
    config_message_type,
    config_payload,
    revision,
    compatible,
    created_at,
    updated_at,
    last_used_at
) VALUES (
    sqlc.arg(preset_id),
    sqlc.arg(owner_user_id),
    sqlc.arg(game_id),
    sqlc.arg(name),
    sqlc.arg(engine_version),
    sqlc.arg(protocol_version),
    sqlc.arg(client_version),
    sqlc.arg(config_schema_version),
    sqlc.arg(config_message_type),
    sqlc.arg(config_payload),
    sqlc.arg(revision),
    sqlc.arg(compatible),
    sqlc.arg(created_at),
    sqlc.arg(updated_at),
    sqlc.arg(last_used_at)
)
RETURNING preset_id, owner_user_id, game_id, name, engine_version, protocol_version,
    client_version, config_schema_version, config_message_type, config_payload,
    revision, compatible, created_at, updated_at, last_used_at;

-- name: UpdateGameRulePreset :one
UPDATE game_rule_presets
SET name = sqlc.arg(name),
    engine_version = sqlc.arg(engine_version),
    protocol_version = sqlc.arg(protocol_version),
    client_version = sqlc.arg(client_version),
    config_schema_version = sqlc.arg(config_schema_version),
    config_message_type = sqlc.arg(config_message_type),
    config_payload = sqlc.arg(config_payload),
    revision = sqlc.arg(revision),
    compatible = sqlc.arg(compatible),
    updated_at = sqlc.arg(updated_at),
    last_used_at = sqlc.arg(last_used_at)
WHERE preset_id = sqlc.arg(preset_id)
  AND revision = sqlc.arg(expected_revision)
RETURNING preset_id, owner_user_id, game_id, name, engine_version, protocol_version,
    client_version, config_schema_version, config_message_type, config_payload,
    revision, compatible, created_at, updated_at, last_used_at;

-- name: DeleteGameRulePreset :exec
DELETE FROM game_rule_presets
WHERE preset_id = sqlc.arg(preset_id)
  AND owner_user_id = sqlc.arg(owner_user_id)
  AND revision = sqlc.arg(expected_revision);

-- name: ExpireRoomPendingStarts :exec
UPDATE room_pending_starts
SET cancelled_at = sqlc.arg(cancelled_at)
WHERE room_id = sqlc.arg(room_id)
  AND cancelled_at IS NULL
  AND consumed_at IS NULL
  AND deadline_at <= sqlc.arg(cancelled_at);

-- name: CreateRoomPendingStart :one
INSERT INTO room_pending_starts (
    pending_start_id,
    room_id,
    cancel_token,
    game_id,
    config_revision,
    expected_room_version,
    expected_membership_version,
    ownership_epoch,
    operation_id,
    request_digest,
    deadline_at,
    created_at
) VALUES (
    sqlc.arg(pending_start_id),
    sqlc.arg(room_id),
    sqlc.arg(cancel_token),
    sqlc.arg(game_id),
    sqlc.arg(config_revision),
    sqlc.arg(expected_room_version),
    sqlc.arg(expected_membership_version),
    sqlc.arg(ownership_epoch),
    sqlc.arg(operation_id),
    sqlc.arg(request_digest),
    sqlc.arg(deadline_at),
    sqlc.arg(created_at)
)
RETURNING pending_start_id, room_id, cancel_token, game_id, config_revision,
    expected_room_version, expected_membership_version, ownership_epoch,
    operation_id, request_digest, deadline_at, created_at, cancelled_at, consumed_at;

-- name: GetLatestRoomPendingStart :one
SELECT pending_start_id, room_id, cancel_token, game_id, config_revision,
    expected_room_version, expected_membership_version, ownership_epoch,
    operation_id, request_digest, deadline_at, created_at, cancelled_at, consumed_at
FROM room_pending_starts
WHERE room_id = sqlc.arg(room_id)
ORDER BY created_at DESC, pending_start_id DESC
LIMIT 1;

-- name: CancelRoomPendingStart :one
UPDATE room_pending_starts
SET cancelled_at = COALESCE(cancelled_at, sqlc.arg(cancelled_at))
WHERE room_id = sqlc.arg(room_id)
  AND pending_start_id = sqlc.arg(pending_start_id)
  AND cancel_token = sqlc.arg(cancel_token)
  AND ownership_epoch = sqlc.arg(ownership_epoch)
  AND consumed_at IS NULL
  AND deadline_at >= sqlc.arg(cancelled_at)
RETURNING pending_start_id, room_id, cancel_token, game_id, config_revision,
    expected_room_version, expected_membership_version, ownership_epoch,
    operation_id, request_digest, deadline_at, created_at, cancelled_at, consumed_at;

-- name: ConsumeRoomPendingStart :one
UPDATE room_pending_starts
SET consumed_at = COALESCE(consumed_at, sqlc.arg(consumed_at))
WHERE room_id = sqlc.arg(room_id)
  AND pending_start_id = sqlc.arg(pending_start_id)
  AND cancel_token = sqlc.arg(cancel_token)
  AND operation_id = sqlc.arg(operation_id)
  AND request_digest = sqlc.arg(request_digest)
  AND cancelled_at IS NULL
  AND deadline_at <= sqlc.arg(consumed_at)
RETURNING pending_start_id, room_id, cancel_token, game_id, config_revision,
    expected_room_version, expected_membership_version, ownership_epoch,
    operation_id, request_digest, deadline_at, created_at, cancelled_at, consumed_at;
