-- name: GetAdminOverviewCurrentCounts :one
SELECT
    (SELECT count(*)::bigint FROM party_rooms WHERE status IN ('lobby', 'playing')) AS active_rooms,
    (SELECT count(*)::bigint FROM game_sessions WHERE status IN ('active', 'suspended')) AS running_games,
    (SELECT count(*)::bigint FROM users AS account WHERE account.created_at >= sqlc.arg(window_start)::timestamptz AND account.created_at < sqlc.arg(window_end)::timestamptz) AS new_users,
    (SELECT count(*)::bigint FROM users AS account WHERE account.status = 'suspended') AS suspended_users,
    (SELECT count(*)::bigint
       FROM admin_user_command_receipts AS receipt
      WHERE receipt.command = 'unsuspend'
        AND receipt.outcome = 'executed'
        AND receipt.completed_at >= sqlc.arg(window_start)::timestamptz
        AND receipt.completed_at < sqlc.arg(window_end)::timestamptz) AS unsuspended_users,
    (SELECT count(*)::bigint
       FROM game_sessions AS session
      WHERE session.status = 'cancelled'
        AND session.ended_at >= sqlc.arg(window_start)::timestamptz
        AND session.ended_at < sqlc.arg(window_end)::timestamptz) AS abnormal_terminations,
    (SELECT count(*)::bigint
       FROM admin_repair_operations AS repair
      WHERE repair.state = 'executed'
        AND repair.executed_at >= sqlc.arg(window_start)::timestamptz
        AND repair.executed_at < sqlc.arg(window_end)::timestamptz) AS emergency_repairs;

-- name: ListAdminOverviewMetricBuckets :many
SELECT *
FROM admin_metric_buckets
WHERE bucket_width = sqlc.arg(bucket_width)
  AND bucket_start >= sqlc.arg(window_start)
  AND bucket_start < sqlc.arg(window_end)
ORDER BY metric_name, bucket_start
LIMIT sqlc.arg(page_size);

-- name: ListAdminOverviewAttentionItems :many
WITH room_attention AS (
    SELECT 'room'::text AS attention_kind,
           room.room_id AS resource_id,
           room.room_id,
           room.status AS status_code,
           (array_remove(ARRAY[
               CASE WHEN room.active_session_id IS NULL THEN 'room.session_missing' END,
               CASE WHEN session.session_id IS NULL AND room.active_session_id IS NOT NULL THEN 'room.session_not_found' END,
               CASE WHEN session.session_id IS NOT NULL AND session.room_id <> room.room_id THEN 'room.session_room_mismatch' END,
               CASE WHEN session.session_id IS NOT NULL AND session.game_id <> room.active_game_id THEN 'room.game_mismatch' END,
               CASE WHEN session.session_id IS NOT NULL AND session.status NOT IN ('active', 'suspended') THEN 'room.session_inactive' END
           ]::text[], NULL))::text[] AS reason_codes,
           room.updated_at AS observed_at
    FROM party_rooms AS room
    LEFT JOIN game_sessions AS session ON session.session_id = room.active_session_id
    WHERE room.status = 'playing'
      AND (
          room.active_session_id IS NULL
          OR session.session_id IS NULL
          OR session.room_id <> room.room_id
          OR session.game_id <> room.active_game_id
          OR session.status NOT IN ('active', 'suspended')
      )
), game_attention AS (
    SELECT 'game'::text AS attention_kind,
           session.session_id AS resource_id,
           session.room_id,
           session.status AS status_code,
           (array_remove(ARRAY[
               CASE WHEN room.room_id IS NULL THEN 'game.room_not_found' END,
               CASE WHEN room.room_id IS NOT NULL AND room.status <> 'playing' THEN 'game.room_not_playing' END,
               CASE WHEN room.room_id IS NOT NULL AND room.active_session_id IS DISTINCT FROM session.session_id THEN 'game.session_link_mismatch' END,
               CASE WHEN room.room_id IS NOT NULL AND room.active_game_id IS DISTINCT FROM session.game_id THEN 'game.catalog_link_mismatch' END
           ]::text[], NULL))::text[] AS reason_codes,
           session.updated_at AS observed_at
    FROM game_sessions AS session
    LEFT JOIN party_rooms AS room ON room.room_id = session.room_id
    WHERE session.status IN ('active', 'suspended')
      AND (
          room.room_id IS NULL
          OR room.status <> 'playing'
          OR room.active_session_id IS DISTINCT FROM session.session_id
          OR room.active_game_id IS DISTINCT FROM session.game_id
      )
)
SELECT attention_kind, resource_id, room_id, status_code, reason_codes, observed_at
FROM (
    SELECT * FROM room_attention
    UNION ALL
    SELECT * FROM game_attention
) AS attention
ORDER BY observed_at DESC, attention_kind, resource_id
LIMIT sqlc.arg(page_size);

-- name: ListAdminOverviewFailedBatchJobs :many
SELECT batch_job_id AS task_id,
       'user_batch'::text AS task_kind,
       state,
       COALESCE(error_message_key, 'admin.task.failed')::text AS stable_error_code,
       version,
       updated_at
FROM admin_batch_jobs
WHERE state = 'failed'
ORDER BY updated_at DESC, batch_job_id
LIMIT sqlc.arg(page_size);

-- name: ListAdminOverviewFailedErasureJobs :many
SELECT erasure_job_id AS task_id,
       'user_erasure'::text AS task_kind,
       state,
       COALESCE(error_message_key, 'admin.task.failed')::text AS stable_error_code,
       attempt_count,
       version,
       updated_at
FROM admin_user_erasure_jobs
WHERE state = 'failed'
ORDER BY updated_at DESC, erasure_job_id
LIMIT sqlc.arg(page_size);
