-- name: CreateGameSession :one
INSERT INTO game_sessions (
    session_id,
    room_id,
    game_id,
    engine_version,
    protocol_version,
    client_version,
    state_version,
    ownership_epoch,
    snapshot_version,
    state_message_type,
    state_schema_version,
    state_payload,
    next_deadline_at,
    status,
    started_at,
    updated_at,
    ended_at
) VALUES (
    sqlc.arg(session_id),
    sqlc.arg(room_id),
    sqlc.arg(game_id),
    sqlc.arg(engine_version),
    sqlc.arg(protocol_version),
    sqlc.arg(client_version),
    sqlc.arg(state_version),
    sqlc.arg(ownership_epoch),
    sqlc.arg(snapshot_version),
    sqlc.arg(state_message_type),
    sqlc.arg(state_schema_version),
    sqlc.arg(state_payload),
    sqlc.narg(next_deadline_at),
    sqlc.arg(status),
    sqlc.arg(started_at),
    sqlc.arg(updated_at),
    sqlc.narg(ended_at)
)
RETURNING session_id, room_id, game_id, engine_version, protocol_version, client_version,
    state_version, ownership_epoch, snapshot_version, state_message_type, state_schema_version,
    state_payload, next_deadline_at, status, started_at, updated_at, ended_at;

-- name: GetGameSessionForShare :one
SELECT session_id, room_id, game_id, engine_version, protocol_version, client_version,
    state_version, ownership_epoch, snapshot_version, state_message_type, state_schema_version,
    state_payload, next_deadline_at, status, started_at, updated_at, ended_at
FROM game_sessions
WHERE session_id = sqlc.arg(session_id)
FOR SHARE;

-- name: GetGameSessionForUpdate :one
SELECT session_id, room_id, game_id, engine_version, protocol_version, client_version,
    state_version, ownership_epoch, snapshot_version, state_message_type, state_schema_version,
    state_payload, next_deadline_at, status, started_at, updated_at, ended_at
FROM game_sessions
WHERE session_id = sqlc.arg(session_id)
FOR UPDATE;

-- name: AcquireGameSessionOwnershipCAS :one
UPDATE game_sessions
SET ownership_epoch = ownership_epoch + 1,
    updated_at = sqlc.arg(updated_at)
WHERE session_id = sqlc.arg(session_id)
  AND state_version = sqlc.arg(expected_state_version)
  AND ownership_epoch = sqlc.arg(expected_ownership_epoch)
  AND status IN ('active', 'suspended')
RETURNING session_id, room_id, game_id, engine_version, protocol_version, client_version,
    state_version, ownership_epoch, snapshot_version, state_message_type, state_schema_version,
    state_payload, next_deadline_at, status, started_at, updated_at, ended_at;

-- name: UpdateGameSessionStateCAS :one
UPDATE game_sessions
SET state_version = sqlc.arg(state_version),
    snapshot_version = sqlc.arg(snapshot_version),
    state_message_type = sqlc.arg(state_message_type),
    state_schema_version = sqlc.arg(state_schema_version),
    state_payload = sqlc.arg(state_payload),
    next_deadline_at = sqlc.narg(next_deadline_at),
    status = sqlc.arg(status),
    updated_at = sqlc.arg(updated_at),
    ended_at = sqlc.narg(ended_at)
WHERE session_id = sqlc.arg(session_id)
  AND state_version = sqlc.arg(expected_state_version)
  AND ownership_epoch = sqlc.arg(expected_ownership_epoch)
  AND status = 'active'
RETURNING session_id, room_id, game_id, engine_version, protocol_version, client_version,
    state_version, ownership_epoch, snapshot_version, state_message_type, state_schema_version,
    state_payload, next_deadline_at, status, started_at, updated_at, ended_at;

-- name: CreateGameSessionParticipant :exec
INSERT INTO game_session_participants (session_id, user_id, seat_index)
VALUES (sqlc.arg(session_id), sqlc.arg(user_id), sqlc.arg(seat_index));

-- name: ListGameSessionParticipants :many
SELECT session_id, user_id, seat_index
FROM game_session_participants
WHERE session_id = sqlc.arg(session_id)
ORDER BY seat_index, user_id;

-- name: DeleteGameSessionTimers :exec
DELETE FROM game_session_timers WHERE session_id = sqlc.arg(session_id);

-- name: CreateGameSessionTimer :exec
INSERT INTO game_session_timers (
    session_id, timer_id, expected_state_version, due_at, message_type, schema_version, payload
) VALUES (
    sqlc.arg(session_id), sqlc.arg(timer_id), sqlc.arg(expected_state_version), sqlc.arg(due_at),
    sqlc.arg(message_type), sqlc.arg(schema_version), sqlc.arg(payload)
);

-- name: ListGameSessionTimers :many
SELECT session_id, timer_id, expected_state_version, due_at, message_type, schema_version, payload
FROM game_session_timers
WHERE session_id = sqlc.arg(session_id)
ORDER BY timer_id;

-- name: CreateGameSessionEventBatch :one
INSERT INTO game_session_event_batches (
    batch_id,
    session_id,
    state_version,
    ownership_epoch,
    cause,
    actor_user_id,
    action_id,
    executed_at,
    random_seed,
    allocated_ids,
    input_message_type,
    input_schema_version,
    input_payload,
    event_count,
    committed_at
) VALUES (
    sqlc.arg(batch_id),
    sqlc.arg(session_id),
    sqlc.arg(state_version),
    sqlc.arg(ownership_epoch),
    sqlc.arg(cause),
    sqlc.narg(actor_user_id),
    sqlc.narg(action_id),
    sqlc.arg(executed_at),
    sqlc.arg(random_seed),
    sqlc.arg(allocated_ids),
    sqlc.arg(input_message_type),
    sqlc.arg(input_schema_version),
    sqlc.arg(input_payload),
    sqlc.arg(event_count),
    sqlc.arg(committed_at)
)
RETURNING batch_id, session_id, state_version, ownership_epoch, cause, actor_user_id,
    action_id, executed_at, random_seed, allocated_ids, input_message_type,
    input_schema_version, input_payload, event_count, committed_at;

-- name: CreateGameSessionEvent :exec
INSERT INTO game_session_events (batch_id, event_ordinal, message_type, schema_version, payload)
VALUES (
    sqlc.arg(batch_id), sqlc.arg(event_ordinal), sqlc.arg(message_type),
    sqlc.arg(schema_version), sqlc.arg(payload)
);

-- name: ListGameSessionEvents :many
SELECT batch_id, event_ordinal, message_type, schema_version, payload
FROM game_session_events
WHERE batch_id = sqlc.arg(batch_id)
ORDER BY event_ordinal;

-- name: CreateGameActionReceipt :one
INSERT INTO game_action_receipts (
    session_id,
    actor_user_id,
    action_id,
    request_digest,
    result_code,
    result_digest,
    committed_state_version,
    committed_at
) VALUES (
    sqlc.arg(session_id),
    sqlc.arg(actor_user_id),
    sqlc.arg(action_id),
    sqlc.arg(request_digest),
    sqlc.arg(result_code),
    sqlc.arg(result_digest),
    sqlc.arg(committed_state_version),
    sqlc.arg(committed_at)
)
ON CONFLICT (session_id, actor_user_id, action_id) DO NOTHING
RETURNING session_id, actor_user_id, action_id, request_digest, result_code,
    result_digest, committed_state_version, committed_at;

-- name: GetGameActionReceipt :one
SELECT session_id, actor_user_id, action_id, request_digest, result_code,
    result_digest, committed_state_version, committed_at
FROM game_action_receipts
WHERE session_id = sqlc.arg(session_id)
  AND actor_user_id = sqlc.arg(actor_user_id)
  AND action_id = sqlc.arg(action_id);
