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

-- name: CreateGameSystemInboxPending :one
INSERT INTO game_system_inbox (
    session_id, source_event_id, event_type, payload_digest, status, created_at
) VALUES (
    sqlc.arg(session_id), sqlc.arg(source_event_id), sqlc.arg(event_type), sqlc.arg(payload_digest), 'pending', sqlc.arg(created_at)
)
RETURNING session_id, source_event_id, event_type, payload_digest, status,
    committed_state_version, created_at, completed_at;

-- name: GetGameSystemInboxForUpdate :one
SELECT session_id, source_event_id, event_type, payload_digest, status,
    committed_state_version, created_at, completed_at
FROM game_system_inbox
WHERE session_id = sqlc.arg(session_id)
  AND source_event_id = sqlc.arg(source_event_id)
FOR UPDATE;

-- name: CompleteGameSystemInboxCAS :one
UPDATE game_system_inbox
SET status = 'completed',
    committed_state_version = sqlc.arg(committed_state_version),
    completed_at = sqlc.arg(completed_at)
WHERE session_id = sqlc.arg(session_id)
  AND source_event_id = sqlc.arg(source_event_id)
  AND payload_digest = sqlc.arg(payload_digest)
  AND status = 'pending'
RETURNING session_id, source_event_id, event_type, payload_digest, status,
    committed_state_version, created_at, completed_at;

-- name: HasPendingGameSystemInbox :one
SELECT EXISTS (
    SELECT 1
    FROM game_system_inbox
    WHERE session_id = sqlc.arg(session_id)
      AND status = 'pending'
      AND (
          sqlc.narg(excluded_source_event_id)::uuid IS NULL
          OR source_event_id <> sqlc.narg(excluded_source_event_id)::uuid
      )
);

-- name: GetGameSessionRoomID :one
SELECT room_id
FROM game_sessions
WHERE session_id = sqlc.arg(session_id);

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

-- name: CreateGameSessionStartReceipt :one
INSERT INTO game_session_start_receipts (
    actor_user_id, room_id, operation_id, request_digest, session_id, committed_at
) VALUES (
    sqlc.arg(actor_user_id), sqlc.arg(room_id), sqlc.arg(operation_id),
    sqlc.arg(request_digest), sqlc.arg(session_id), sqlc.arg(committed_at)
)
ON CONFLICT (actor_user_id, room_id, operation_id) DO NOTHING
RETURNING actor_user_id, room_id, operation_id, request_digest, session_id, committed_at;

-- name: GetGameSessionStartReceipt :one
SELECT actor_user_id, room_id, operation_id, request_digest, session_id, committed_at
FROM game_session_start_receipts
WHERE actor_user_id = sqlc.arg(actor_user_id)
  AND room_id = sqlc.arg(room_id)
  AND operation_id = sqlc.arg(operation_id);

-- name: GetGameSessionStartReceiptForUpdate :one
SELECT actor_user_id, room_id, operation_id, request_digest, session_id, committed_at
FROM game_session_start_receipts
WHERE actor_user_id = sqlc.arg(actor_user_id)
  AND room_id = sqlc.arg(room_id)
  AND operation_id = sqlc.arg(operation_id)
FOR UPDATE;

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

-- name: UpdateGameSessionLifecycleCAS :one
UPDATE game_sessions
SET next_deadline_at = sqlc.narg(next_deadline_at),
    status = sqlc.arg(status),
    updated_at = sqlc.arg(updated_at),
    ended_at = sqlc.narg(ended_at)
WHERE session_id = sqlc.arg(session_id)
  AND state_version = sqlc.arg(expected_state_version)
  AND ownership_epoch = sqlc.arg(expected_ownership_epoch)
  AND status IN ('active', 'suspended')
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

-- name: GetGameSessionTimerForUpdate :one
SELECT session_id, timer_id, expected_state_version, due_at, message_type, schema_version, payload
FROM game_session_timers
WHERE session_id = sqlc.arg(session_id)
  AND timer_id = sqlc.arg(timer_id)
FOR UPDATE;

-- name: ListDueGameSessionTimerCandidates :many
SELECT timer.session_id, timer.timer_id, timer.expected_state_version, timer.due_at,
    timer.message_type, timer.schema_version, timer.payload
FROM game_session_timers AS timer
JOIN game_sessions AS session USING (session_id)
WHERE session.status = 'active'
  AND timer.due_at <= sqlc.arg(due_at)
ORDER BY timer.due_at, timer.session_id, timer.timer_id
LIMIT sqlc.arg(batch_limit);

-- name: CreateGameSessionEventBatch :one
INSERT INTO game_session_event_batches (
    batch_id,
    session_id,
    state_version,
    ownership_epoch,
    cause,
    actor_user_id,
    action_id,
    timer_id,
    system_operation_id,
    system_source_kind,
    system_source_event_id,
    system_requested_by_user_id,
    system_request_digest,
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
    sqlc.narg(timer_id),
    sqlc.narg(system_operation_id),
    sqlc.narg(system_source_kind),
    sqlc.narg(system_source_event_id),
    sqlc.narg(system_requested_by_user_id),
    sqlc.narg(system_request_digest),
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
    action_id, timer_id, system_operation_id, system_source_kind, system_source_event_id,
    system_requested_by_user_id, system_request_digest,
    executed_at, random_seed, allocated_ids, input_message_type,
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

-- name: ListGameSessionEventBatchesAfter :many
SELECT batch_id, session_id, state_version, ownership_epoch, cause, actor_user_id,
    action_id, timer_id, system_operation_id, system_source_kind, system_source_event_id,
    system_requested_by_user_id, system_request_digest,
    executed_at, random_seed, allocated_ids, input_message_type,
    input_schema_version, input_payload, event_count, committed_at
FROM game_session_event_batches
WHERE session_id = sqlc.arg(session_id)
  AND state_version > sqlc.arg(after_state_version)
ORDER BY state_version
LIMIT sqlc.arg(batch_limit);

-- name: CreateGameTimerReceipt :one
INSERT INTO game_timer_receipts (
    session_id, timer_id, expected_state_version, result_code, result_digest,
    committed_state_version, batch_id, committed_at
) VALUES (
    sqlc.arg(session_id), sqlc.arg(timer_id), sqlc.arg(expected_state_version),
    sqlc.arg(result_code), sqlc.arg(result_digest), sqlc.arg(committed_state_version),
    sqlc.arg(batch_id), sqlc.arg(committed_at)
)
ON CONFLICT (session_id, timer_id, expected_state_version) DO NOTHING
RETURNING session_id, timer_id, expected_state_version, result_code, result_digest,
    committed_state_version, batch_id, committed_at;

-- name: GetGameTimerReceipt :one
SELECT session_id, timer_id, expected_state_version, result_code, result_digest,
    committed_state_version, batch_id, committed_at
FROM game_timer_receipts
WHERE session_id = sqlc.arg(session_id)
  AND timer_id = sqlc.arg(timer_id)
  AND expected_state_version = sqlc.arg(expected_state_version);

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

-- name: InsertGameSystemOperationPending :one
INSERT INTO game_system_operations (
    session_id, operation_id, source_kind, source_event_id, requested_by_user_id,
    logical_digest, status, created_at
) VALUES (
    sqlc.arg(session_id), sqlc.arg(operation_id), sqlc.arg(source_kind), sqlc.narg(source_event_id),
    sqlc.narg(requested_by_user_id), sqlc.arg(logical_digest), 'pending', sqlc.arg(created_at)
)
ON CONFLICT DO NOTHING
RETURNING session_id, operation_id, source_kind, source_event_id, requested_by_user_id,
    logical_digest, status, result_code, result_digest, committed_state_version,
    batch_id, created_at, completed_at;

-- name: GetGameSystemOperationForUpdate :one
SELECT session_id, operation_id, source_kind, source_event_id, requested_by_user_id,
    logical_digest, status, result_code, result_digest, committed_state_version,
    batch_id, created_at, completed_at
FROM game_system_operations
WHERE session_id = sqlc.arg(session_id)
  AND operation_id = sqlc.arg(operation_id)
FOR UPDATE;

-- name: GetGameSystemOperationBySourceForUpdate :one
SELECT session_id, operation_id, source_kind, source_event_id, requested_by_user_id,
    logical_digest, status, result_code, result_digest, committed_state_version,
    batch_id, created_at, completed_at
FROM game_system_operations
WHERE session_id = sqlc.arg(session_id)
  AND source_event_id = sqlc.arg(source_event_id)
FOR UPDATE;

-- name: CompleteGameSystemOperationCAS :one
UPDATE game_system_operations
SET status = 'completed',
    result_code = sqlc.arg(result_code),
    result_digest = sqlc.arg(result_digest),
    committed_state_version = sqlc.arg(committed_state_version),
    batch_id = sqlc.narg(batch_id),
    completed_at = sqlc.arg(completed_at)
WHERE session_id = sqlc.arg(session_id)
  AND operation_id = sqlc.arg(operation_id)
  AND status = 'pending'
RETURNING session_id, operation_id, source_kind, source_event_id, requested_by_user_id,
    logical_digest, status, result_code, result_digest, committed_state_version,
    batch_id, created_at, completed_at;
