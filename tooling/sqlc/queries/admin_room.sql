-- name: AdminListRooms :many
WITH listed AS (
    SELECT room.room_id,
           room.room_code,
           room.status,
           room.active_game_id,
           room.active_session_id,
           room.host_user_id,
           host.display_username AS host_username,
           counts.participant_count,
           counts.spectator_count,
           room.participant_admission,
           room.spectator_admission,
           room.room_version,
           room.membership_version,
           room.ownership_epoch,
           room.created_at,
           room.updated_at,
           GREATEST(room.updated_at, COALESCE(activity.last_seen_at, room.updated_at))::timestamptz AS last_activity_at,
           (
               room.status = 'playing'
               AND (
                   session.session_id IS NULL
                   OR session.room_id <> room.room_id
                   OR session.game_id <> room.active_game_id
                   OR session.status NOT IN ('active', 'suspended')
               )
           ) AS room_game_link_mismatch
    FROM party_rooms AS room
    LEFT JOIN room_members AS host
      ON host.room_id = room.room_id
     AND host.user_id = room.host_user_id
    LEFT JOIN game_sessions AS session
      ON session.session_id = room.active_session_id
    LEFT JOIN LATERAL (
        SELECT count(*) FILTER (WHERE member.role = 'participant')::integer AS participant_count,
               count(*) FILTER (WHERE member.role = 'spectator')::integer AS spectator_count
        FROM room_members AS member
        WHERE member.room_id = room.room_id
    ) AS counts ON true
    LEFT JOIN LATERAL (
        SELECT max(member.last_seen_at)::timestamptz AS last_seen_at
        FROM room_members AS member
        WHERE member.room_id = room.room_id
    ) AS activity ON true
    WHERE (sqlc.narg(room_id)::uuid IS NULL OR room.room_id = sqlc.narg(room_id))
      AND (sqlc.arg(room_code)::text = '' OR room.room_code = sqlc.arg(room_code))
      AND (cardinality(sqlc.arg(statuses)::text[]) = 0 OR room.status = ANY(sqlc.arg(statuses)::text[]))
      AND (cardinality(sqlc.arg(game_ids)::text[]) = 0 OR COALESCE(room.active_game_id, room.last_finished_game_id, room.selected_game_id) = ANY(sqlc.arg(game_ids)::text[]))
      AND (sqlc.narg(host_user_id)::uuid IS NULL OR room.host_user_id = sqlc.narg(host_user_id))
      AND (
          sqlc.narg(member_user_id)::uuid IS NULL
          OR EXISTS (
              SELECT 1
              FROM room_members AS member_filter
              WHERE member_filter.room_id = room.room_id
                AND member_filter.user_id = sqlc.narg(member_user_id)
          )
      )
      AND (sqlc.narg(created_from)::timestamptz IS NULL OR room.created_at >= sqlc.narg(created_from))
      AND (sqlc.narg(created_to)::timestamptz IS NULL OR room.created_at <= sqlc.narg(created_to))
      AND (sqlc.narg(updated_from)::timestamptz IS NULL OR room.updated_at >= sqlc.narg(updated_from))
      AND (sqlc.narg(updated_to)::timestamptz IS NULL OR room.updated_at <= sqlc.narg(updated_to))
)
SELECT *
FROM listed
WHERE (
    NOT sqlc.arg(anomalies_only)::boolean
    OR listed.room_game_link_mismatch
)
AND (
    sqlc.narg(after_room_id)::uuid IS NULL
    OR (sqlc.arg(sort_field)::text = 'updated_at' AND (
        (sqlc.arg(sort_direction)::text = 'ascending'
            AND (listed.updated_at > sqlc.narg(after_sort_time)::timestamptz
                OR (listed.updated_at = sqlc.narg(after_sort_time)::timestamptz AND listed.room_id > sqlc.narg(after_room_id)::uuid)))
        OR (sqlc.arg(sort_direction)::text = 'descending'
            AND (listed.updated_at < sqlc.narg(after_sort_time)::timestamptz
                OR (listed.updated_at = sqlc.narg(after_sort_time)::timestamptz AND listed.room_id < sqlc.narg(after_room_id)::uuid)))
    ))
    OR (sqlc.arg(sort_field)::text = 'created_at' AND (
        (sqlc.arg(sort_direction)::text = 'ascending'
            AND (listed.created_at > sqlc.narg(after_sort_time)::timestamptz
                OR (listed.created_at = sqlc.narg(after_sort_time)::timestamptz AND listed.room_id > sqlc.narg(after_room_id)::uuid)))
        OR (sqlc.arg(sort_direction)::text = 'descending'
            AND (listed.created_at < sqlc.narg(after_sort_time)::timestamptz
                OR (listed.created_at = sqlc.narg(after_sort_time)::timestamptz AND listed.room_id < sqlc.narg(after_room_id)::uuid)))
    ))
    OR (sqlc.arg(sort_field)::text = 'last_activity_at' AND (
        (sqlc.arg(sort_direction)::text = 'ascending'
            AND (listed.last_activity_at > sqlc.narg(after_sort_time)::timestamptz
                OR (listed.last_activity_at = sqlc.narg(after_sort_time)::timestamptz AND listed.room_id > sqlc.narg(after_room_id)::uuid)))
        OR (sqlc.arg(sort_direction)::text = 'descending'
            AND (listed.last_activity_at < sqlc.narg(after_sort_time)::timestamptz
                OR (listed.last_activity_at = sqlc.narg(after_sort_time)::timestamptz AND listed.room_id < sqlc.narg(after_room_id)::uuid)))
    ))
    OR (sqlc.arg(sort_field)::text = 'room_code' AND (
        (sqlc.arg(sort_direction)::text = 'ascending'
            AND (listed.room_code > sqlc.narg(after_sort_text)::text
                OR (listed.room_code = sqlc.narg(after_sort_text)::text AND listed.room_id > sqlc.narg(after_room_id)::uuid)))
        OR (sqlc.arg(sort_direction)::text = 'descending'
            AND (listed.room_code < sqlc.narg(after_sort_text)::text
                OR (listed.room_code = sqlc.narg(after_sort_text)::text AND listed.room_id < sqlc.narg(after_room_id)::uuid)))
    ))
)
ORDER BY
    CASE WHEN sqlc.arg(sort_field)::text = 'updated_at' AND sqlc.arg(sort_direction)::text = 'ascending' THEN listed.updated_at END ASC,
    CASE WHEN sqlc.arg(sort_field)::text = 'updated_at' AND sqlc.arg(sort_direction)::text = 'descending' THEN listed.updated_at END DESC,
    CASE WHEN sqlc.arg(sort_field)::text = 'created_at' AND sqlc.arg(sort_direction)::text = 'ascending' THEN listed.created_at END ASC,
    CASE WHEN sqlc.arg(sort_field)::text = 'created_at' AND sqlc.arg(sort_direction)::text = 'descending' THEN listed.created_at END DESC,
    CASE WHEN sqlc.arg(sort_field)::text = 'last_activity_at' AND sqlc.arg(sort_direction)::text = 'ascending' THEN listed.last_activity_at END ASC,
    CASE WHEN sqlc.arg(sort_field)::text = 'last_activity_at' AND sqlc.arg(sort_direction)::text = 'descending' THEN listed.last_activity_at END DESC,
    CASE WHEN sqlc.arg(sort_field)::text = 'room_code' AND sqlc.arg(sort_direction)::text = 'ascending' THEN listed.room_code END ASC,
    CASE WHEN sqlc.arg(sort_field)::text = 'room_code' AND sqlc.arg(sort_direction)::text = 'descending' THEN listed.room_code END DESC,
    CASE WHEN sqlc.arg(sort_direction)::text = 'ascending' THEN listed.room_id END ASC,
    CASE WHEN sqlc.arg(sort_direction)::text = 'descending' THEN listed.room_id END DESC
LIMIT sqlc.arg(page_size);

-- name: AdminGetRoom :one
SELECT *
FROM (
    SELECT room.room_id,
           room.room_code,
           room.status,
           room.active_game_id,
           room.active_session_id,
           room.host_user_id,
           host.display_username AS host_username,
           counts.participant_count,
           counts.spectator_count,
           room.participant_admission,
           room.spectator_admission,
           room.room_version,
           room.membership_version,
           room.ownership_epoch,
           room.created_at,
           room.updated_at,
           GREATEST(room.updated_at, COALESCE(activity.last_seen_at, room.updated_at))::timestamptz AS last_activity_at,
           (
               room.status = 'playing'
               AND (
                   session.session_id IS NULL
                   OR session.room_id <> room.room_id
                   OR session.game_id <> room.active_game_id
                   OR session.status NOT IN ('active', 'suspended')
               )
           ) AS room_game_link_mismatch
    FROM party_rooms AS room
    LEFT JOIN room_members AS host
      ON host.room_id = room.room_id
     AND host.user_id = room.host_user_id
    LEFT JOIN game_sessions AS session
      ON session.session_id = room.active_session_id
    LEFT JOIN LATERAL (
        SELECT count(*) FILTER (WHERE member.role = 'participant')::integer AS participant_count,
               count(*) FILTER (WHERE member.role = 'spectator')::integer AS spectator_count
        FROM room_members AS member
        WHERE member.room_id = room.room_id
    ) AS counts ON true
    LEFT JOIN LATERAL (
        SELECT max(member.last_seen_at)::timestamptz AS last_seen_at
        FROM room_members AS member
        WHERE member.room_id = room.room_id
    ) AS activity ON true
    WHERE room.room_id = sqlc.arg(room_id)
) AS detail;

-- name: AdminListRoomMembers :many
SELECT member.room_id,
       member.user_id,
       member.display_username AS username,
       member.role,
       member.requested_role,
       member.joined_at,
       member.last_seen_at >= sqlc.arg(online_cutoff)::timestamptz AS online
FROM room_members AS member
WHERE member.room_id = sqlc.arg(room_id)
ORDER BY member.role, member.seat_index NULLS LAST, member.joined_at, member.user_id;

-- name: AdminListRoomActiveGames :many
SELECT session.session_id,
       session.room_id,
       room.room_code,
       session.game_id,
       session.engine_version,
       session.protocol_version,
       session.client_version,
       session.status,
       session.state_version,
       session.ownership_epoch,
       session.started_at,
       session.updated_at,
       COALESCE(progress.last_progress_at, session.updated_at)::timestamptz AS last_progress_at,
       (
           room.room_id IS NULL
           OR room.active_session_id IS DISTINCT FROM session.session_id
           OR room.active_game_id IS DISTINCT FROM session.game_id
       ) AS room_link_mismatch
FROM game_sessions AS session
LEFT JOIN party_rooms AS room ON room.room_id = session.room_id
LEFT JOIN LATERAL (
    SELECT max(batch.committed_at)::timestamptz AS last_progress_at
    FROM game_session_event_batches AS batch
    WHERE batch.session_id = session.session_id
) AS progress ON true
WHERE session.room_id = sqlc.arg(room_id)
  AND session.status IN ('active', 'suspended')
ORDER BY session.updated_at DESC, session.session_id DESC
LIMIT sqlc.arg(page_size);

-- name: AdminListRoomRecentEvents :many
SELECT batch.batch_id,
       batch.session_id,
       batch.cause,
       batch.actor_user_id,
       batch.system_operation_id,
       batch.state_version,
       encode(COALESCE(batch.system_request_digest, repeat('\000', 32)::bytea), 'hex') AS digest,
       batch.committed_at
FROM game_session_event_batches AS batch
JOIN game_sessions AS session ON session.session_id = batch.session_id
WHERE session.room_id = sqlc.arg(room_id)
ORDER BY batch.committed_at DESC, batch.state_version DESC
LIMIT sqlc.arg(page_size);

-- name: AdminListGames :many
WITH listed AS (
    SELECT session.session_id,
           session.room_id,
           room.room_code,
           session.game_id,
           session.engine_version,
           session.protocol_version,
           session.client_version,
           session.status,
           session.state_version,
           session.ownership_epoch,
           session.started_at,
           session.updated_at,
           COALESCE(progress.last_progress_at, session.updated_at)::timestamptz AS last_progress_at,
           (
               room.room_id IS NULL
               OR (session.status IN ('active', 'suspended') AND (
                   room.active_session_id IS DISTINCT FROM session.session_id
                   OR room.active_game_id IS DISTINCT FROM session.game_id
                   OR room.status <> 'playing'
               ))
           ) AS room_link_mismatch
    FROM game_sessions AS session
    LEFT JOIN party_rooms AS room ON room.room_id = session.room_id
    LEFT JOIN LATERAL (
        SELECT max(batch.committed_at)::timestamptz AS last_progress_at
        FROM game_session_event_batches AS batch
        WHERE batch.session_id = session.session_id
    ) AS progress ON true
    WHERE (sqlc.narg(session_id)::uuid IS NULL OR session.session_id = sqlc.narg(session_id))
      AND (sqlc.narg(room_id)::uuid IS NULL OR session.room_id = sqlc.narg(room_id))
      AND (cardinality(sqlc.arg(game_ids)::text[]) = 0 OR session.game_id = ANY(sqlc.arg(game_ids)::text[]))
      AND (cardinality(sqlc.arg(statuses)::text[]) = 0 OR session.status = ANY(sqlc.arg(statuses)::text[]))
      AND (sqlc.narg(started_from)::timestamptz IS NULL OR session.started_at >= sqlc.narg(started_from))
      AND (sqlc.narg(started_to)::timestamptz IS NULL OR session.started_at <= sqlc.narg(started_to))
      AND (sqlc.narg(updated_from)::timestamptz IS NULL OR session.updated_at >= sqlc.narg(updated_from))
      AND (sqlc.narg(updated_to)::timestamptz IS NULL OR session.updated_at <= sqlc.narg(updated_to))
)
SELECT *
FROM listed
WHERE (
    NOT sqlc.arg(anomalies_only)::boolean
    OR listed.room_link_mismatch
)
AND (
    sqlc.narg(after_session_id)::uuid IS NULL
    OR (sqlc.arg(sort_field)::text = 'updated_at' AND (
        (sqlc.arg(sort_direction)::text = 'ascending'
            AND (listed.updated_at > sqlc.narg(after_sort_time)::timestamptz
                OR (listed.updated_at = sqlc.narg(after_sort_time)::timestamptz AND listed.session_id > sqlc.narg(after_session_id)::uuid)))
        OR (sqlc.arg(sort_direction)::text = 'descending'
            AND (listed.updated_at < sqlc.narg(after_sort_time)::timestamptz
                OR (listed.updated_at = sqlc.narg(after_sort_time)::timestamptz AND listed.session_id < sqlc.narg(after_session_id)::uuid)))
    ))
    OR (sqlc.arg(sort_field)::text = 'started_at' AND (
        (sqlc.arg(sort_direction)::text = 'ascending'
            AND (listed.started_at > sqlc.narg(after_sort_time)::timestamptz
                OR (listed.started_at = sqlc.narg(after_sort_time)::timestamptz AND listed.session_id > sqlc.narg(after_session_id)::uuid)))
        OR (sqlc.arg(sort_direction)::text = 'descending'
            AND (listed.started_at < sqlc.narg(after_sort_time)::timestamptz
                OR (listed.started_at = sqlc.narg(after_sort_time)::timestamptz AND listed.session_id < sqlc.narg(after_session_id)::uuid)))
    ))
    OR (sqlc.arg(sort_field)::text = 'last_progress_at' AND (
        (sqlc.arg(sort_direction)::text = 'ascending'
            AND (listed.last_progress_at > sqlc.narg(after_sort_time)::timestamptz
                OR (listed.last_progress_at = sqlc.narg(after_sort_time)::timestamptz AND listed.session_id > sqlc.narg(after_session_id)::uuid)))
        OR (sqlc.arg(sort_direction)::text = 'descending'
            AND (listed.last_progress_at < sqlc.narg(after_sort_time)::timestamptz
                OR (listed.last_progress_at = sqlc.narg(after_sort_time)::timestamptz AND listed.session_id < sqlc.narg(after_session_id)::uuid)))
    ))
    OR (sqlc.arg(sort_field)::text = 'session_id' AND (
        (sqlc.arg(sort_direction)::text = 'ascending' AND listed.session_id > sqlc.narg(after_session_id)::uuid)
        OR (sqlc.arg(sort_direction)::text = 'descending' AND listed.session_id < sqlc.narg(after_session_id)::uuid)
    ))
)
ORDER BY
    CASE WHEN sqlc.arg(sort_field)::text = 'updated_at' AND sqlc.arg(sort_direction)::text = 'ascending' THEN listed.updated_at END ASC,
    CASE WHEN sqlc.arg(sort_field)::text = 'updated_at' AND sqlc.arg(sort_direction)::text = 'descending' THEN listed.updated_at END DESC,
    CASE WHEN sqlc.arg(sort_field)::text = 'started_at' AND sqlc.arg(sort_direction)::text = 'ascending' THEN listed.started_at END ASC,
    CASE WHEN sqlc.arg(sort_field)::text = 'started_at' AND sqlc.arg(sort_direction)::text = 'descending' THEN listed.started_at END DESC,
    CASE WHEN sqlc.arg(sort_field)::text = 'last_progress_at' AND sqlc.arg(sort_direction)::text = 'ascending' THEN listed.last_progress_at END ASC,
    CASE WHEN sqlc.arg(sort_field)::text = 'last_progress_at' AND sqlc.arg(sort_direction)::text = 'descending' THEN listed.last_progress_at END DESC,
    CASE WHEN sqlc.arg(sort_direction)::text = 'ascending' THEN listed.session_id END ASC,
    CASE WHEN sqlc.arg(sort_direction)::text = 'descending' THEN listed.session_id END DESC
LIMIT sqlc.arg(page_size);

-- name: AdminGetGame :one
SELECT *
FROM (
    SELECT session.session_id,
           session.room_id,
           room.room_code,
           session.game_id,
           session.engine_version,
           session.protocol_version,
           session.client_version,
           session.status,
           session.state_version,
           session.ownership_epoch,
           session.started_at,
           session.updated_at,
           COALESCE(progress.last_progress_at, session.updated_at)::timestamptz AS last_progress_at,
           (
               room.room_id IS NULL
               OR (session.status IN ('active', 'suspended') AND (
                   room.active_session_id IS DISTINCT FROM session.session_id
                   OR room.active_game_id IS DISTINCT FROM session.game_id
                   OR room.status <> 'playing'
               ))
           ) AS room_link_mismatch
    FROM game_sessions AS session
    LEFT JOIN party_rooms AS room ON room.room_id = session.room_id
    LEFT JOIN LATERAL (
        SELECT max(batch.committed_at)::timestamptz AS last_progress_at
        FROM game_session_event_batches AS batch
        WHERE batch.session_id = session.session_id
    ) AS progress ON true
    WHERE session.session_id = sqlc.arg(session_id)
) AS detail;

-- name: AdminListGameParticipants :many
SELECT participant.session_id,
       participant.user_id,
       member.display_username AS username,
       member.role AS room_role,
       member.role = 'participant' AS active
FROM game_session_participants AS participant
LEFT JOIN game_sessions AS session ON session.session_id = participant.session_id
LEFT JOIN room_members AS member
  ON member.room_id = session.room_id
 AND member.user_id = participant.user_id
WHERE participant.session_id = sqlc.arg(session_id)
ORDER BY participant.seat_index, participant.user_id;

-- name: AdminListGameRecentEvents :many
SELECT batch.batch_id,
       batch.session_id,
       batch.cause,
       batch.actor_user_id,
       batch.system_operation_id,
       batch.state_version,
       encode(COALESCE(batch.system_request_digest, repeat('\000', 32)::bytea), 'hex') AS digest,
       batch.committed_at
FROM game_session_event_batches AS batch
WHERE batch.session_id = sqlc.arg(session_id)
ORDER BY batch.committed_at DESC, batch.state_version DESC
LIMIT sqlc.arg(page_size);

-- name: AdminCreateRepairOperation :one
INSERT INTO admin_repair_operations (
    repair_id, repair_type, state, target_id, target_kind, target_digest, preview_digest,
    command_version, expected_room_version, expected_membership_version, expected_state_version,
    expected_ownership_epoch, summary, irreversible_effects, before_snapshot_digest,
    after_snapshot_digest, requested_by_admin_id, reason, version, created_at, expires_at
) VALUES (
    sqlc.arg(repair_id), sqlc.arg(repair_type), 'previewed', sqlc.arg(target_id), sqlc.arg(target_kind),
    sqlc.arg(target_digest), sqlc.arg(preview_digest), sqlc.arg(command_version),
    sqlc.narg(expected_room_version), sqlc.narg(expected_membership_version),
    sqlc.narg(expected_state_version), sqlc.narg(expected_ownership_epoch),
    sqlc.arg(summary), sqlc.arg(irreversible_effects), sqlc.arg(before_snapshot_digest),
    sqlc.narg(after_snapshot_digest), sqlc.arg(requested_by_admin_id), sqlc.arg(reason),
    1, sqlc.arg(created_at), sqlc.arg(expires_at)
)
RETURNING *;

-- name: AdminGetRepairOperation :one
SELECT *
FROM admin_repair_operations
WHERE repair_id = sqlc.arg(repair_id);

-- name: AdminGetRepairOperationForUpdate :one
SELECT *
FROM admin_repair_operations
WHERE repair_id = sqlc.arg(repair_id)
FOR UPDATE;

-- name: AdminExpireRepairOperationCAS :one
UPDATE admin_repair_operations
SET state = 'expired',
    version = version + 1
WHERE repair_id = sqlc.arg(repair_id)
  AND state = 'previewed'
  AND version = sqlc.arg(expected_version)
  AND expires_at <= sqlc.arg(expired_at)
RETURNING *;

-- name: AdminExecuteRepairOperationCAS :one
UPDATE admin_repair_operations
SET state = sqlc.arg(state),
    operation_id = sqlc.arg(operation_id),
    request_digest = sqlc.arg(request_digest),
    audit_event_id = sqlc.arg(audit_event_id),
    after_snapshot_digest = sqlc.arg(after_snapshot_digest),
    reason = sqlc.arg(reason),
    version = version + 1,
    executed_at = sqlc.arg(executed_at)
WHERE repair_id = sqlc.arg(repair_id)
  AND state = 'previewed'
  AND version = sqlc.arg(expected_version)
  AND expires_at > sqlc.arg(executed_at)
RETURNING *;
