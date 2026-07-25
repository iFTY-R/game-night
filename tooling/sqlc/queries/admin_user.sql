-- name: GetAdminUserTagCatalog :one
SELECT *
FROM admin_user_tag_catalog
WHERE singleton_id = 1;

-- name: ListAdminUserTags :many
SELECT *
FROM admin_user_tags
WHERE normalized_name >= sqlc.arg(name_prefix)
  AND (sqlc.arg(name_prefix) = '' OR normalized_name LIKE sqlc.arg(name_prefix) || '%')
  AND (
      sqlc.narg(after_tag_id)::uuid IS NULL
      OR (normalized_name, tag_id) > (sqlc.arg(after_normalized_name), sqlc.narg(after_tag_id)::uuid)
  )
ORDER BY normalized_name, tag_id
LIMIT sqlc.arg(page_size);

-- name: CreateAdminUserTag :one
WITH advanced AS (
    UPDATE admin_user_tag_catalog
    SET catalog_version = catalog_version + 1,
        updated_at = sqlc.arg(created_at)
    WHERE singleton_id = 1
      AND catalog_version = sqlc.arg(expected_catalog_version)
    RETURNING catalog_version
), inserted AS (
    INSERT INTO admin_user_tags (
        tag_id, name, normalized_name, color, version,
        created_by_admin_id, updated_by_admin_id, reason, created_at, updated_at
    )
    SELECT sqlc.arg(tag_id), sqlc.arg(name), sqlc.arg(normalized_name), sqlc.arg(color), 1,
           sqlc.arg(actor_admin_id), sqlc.arg(actor_admin_id), sqlc.arg(reason), sqlc.arg(created_at), sqlc.arg(created_at)
    FROM advanced
    RETURNING *
)
SELECT inserted.*, advanced.catalog_version
FROM inserted
CROSS JOIN advanced;

-- name: UpdateAdminUserTagCAS :one
WITH changed AS (
    UPDATE admin_user_tags
    SET name = sqlc.arg(name),
        normalized_name = sqlc.arg(normalized_name),
        color = sqlc.arg(color),
        updated_by_admin_id = sqlc.arg(actor_admin_id),
        reason = sqlc.arg(reason),
        version = version + 1,
        updated_at = sqlc.arg(updated_at)
    WHERE tag_id = sqlc.arg(tag_id)
      AND version = sqlc.arg(expected_version)
    RETURNING *
), advanced AS (
    UPDATE admin_user_tag_catalog
    SET catalog_version = catalog_version + 1,
        updated_at = sqlc.arg(updated_at)
    WHERE singleton_id = 1
      AND EXISTS (SELECT 1 FROM changed)
)
SELECT * FROM changed;

-- name: DeleteAdminUserTagCAS :one
WITH removed AS (
    DELETE FROM admin_user_tags
    WHERE tag_id = sqlc.arg(tag_id)
      AND version = sqlc.arg(expected_version)
    RETURNING tag_id
), advanced AS (
    UPDATE admin_user_tag_catalog
    SET catalog_version = catalog_version + 1,
        updated_at = sqlc.arg(deleted_at)
    WHERE singleton_id = 1
      AND EXISTS (SELECT 1 FROM removed)
    RETURNING catalog_version
)
SELECT removed.tag_id, advanced.catalog_version
FROM removed
CROSS JOIN advanced;

-- name: LockAdminUserForTagUpdate :one
SELECT user_id, account_version
FROM users
WHERE user_id = sqlc.arg(user_id)
FOR UPDATE;

-- name: DeleteAdminUserTagLinks :execrows
DELETE FROM admin_user_tag_links
WHERE user_id = sqlc.arg(user_id);

-- name: InsertAdminUserTagLinks :execrows
INSERT INTO admin_user_tag_links (
    user_id, tag_id, version, assigned_by_admin_id, reason, created_at, updated_at
)
SELECT sqlc.arg(user_id), selected.tag_id, 1, sqlc.arg(actor_admin_id), sqlc.arg(reason), sqlc.arg(changed_at), sqlc.arg(changed_at)
FROM unnest(sqlc.arg(tag_ids)::uuid[]) AS selected(tag_id);

-- name: IncrementAdminUserVersionCAS :one
UPDATE users
SET account_version = account_version + 1,
    updated_at = sqlc.arg(updated_at)
WHERE user_id = sqlc.arg(user_id)
  AND account_version = sqlc.arg(expected_version)
RETURNING account_version;

-- name: ListAdminUserTagLinks :many
SELECT link.*
FROM admin_user_tag_links AS link
WHERE link.user_id = sqlc.arg(user_id)
ORDER BY link.tag_id;

-- name: AppendAdminUserNote :one
INSERT INTO admin_user_notes (
    note_id, user_id, author_admin_id, body, reason, version, created_at
) VALUES (
    sqlc.arg(note_id), sqlc.arg(user_id), sqlc.arg(author_admin_id),
    sqlc.arg(body), sqlc.arg(reason), 1, sqlc.arg(created_at)
)
RETURNING *;

-- name: ListAdminUserNotes :many
SELECT *
FROM admin_user_notes
WHERE user_id = sqlc.arg(user_id)
  AND (
      sqlc.arg(after_created_at)::timestamptz IS NULL
      OR (created_at, note_id) < (sqlc.arg(after_created_at), sqlc.arg(after_note_id)::uuid)
  )
ORDER BY created_at DESC, note_id DESC
LIMIT sqlc.arg(page_size);

-- name: ListAdminUsers :many
WITH candidates AS (
    SELECT user_row.user_id,
           user_row.status,
           user_row.username,
           user_row.current_username_key,
           user_row.username_changed_at,
           user_row.account_version,
           user_row.created_at,
           user_row.updated_at,
           COALESCE(user_row.current_username_key, '') AS username_sort_key,
           GREATEST(
               CASE WHEN user_row.updated_at <= sqlc.arg(sampled_at) THEN user_row.updated_at ELSE user_row.created_at END,
               COALESCE(device_activity.last_seen_at, user_row.created_at),
               COALESCE(room_activity.last_seen_at, user_row.created_at)
           )::timestamptz AS last_activity_at
    FROM users AS user_row
    LEFT JOIN LATERAL (
        SELECT credential.last_seen_at
        FROM device_credentials AS credential
        WHERE credential.user_id = user_row.user_id
          AND credential.last_seen_at <= sqlc.arg(sampled_at)
        ORDER BY credential.last_seen_at DESC, credential.credential_id DESC
        LIMIT 1
    ) AS device_activity ON true
    LEFT JOIN LATERAL (
        SELECT member.last_seen_at
        FROM room_members AS member
        WHERE member.user_id = user_row.user_id
          AND member.last_seen_at <= sqlc.arg(sampled_at)
        ORDER BY member.last_seen_at DESC, member.room_id DESC
        LIMIT 1
    ) AS room_activity ON true
    WHERE user_row.created_at <= sqlc.arg(sampled_at)
      AND (sqlc.narg(user_id)::uuid IS NULL OR user_row.user_id = sqlc.narg(user_id))
      AND (cardinality(sqlc.arg(statuses)::text[]) = 0 OR user_row.status = ANY(sqlc.arg(statuses)::text[]))
      AND (sqlc.arg(username_prefix)::text = '' OR COALESCE(user_row.current_username_key, '') LIKE sqlc.arg(username_prefix)::text || '%')
      AND (
          cardinality(sqlc.arg(tag_ids)::uuid[]) = 0
          OR NOT EXISTS (
              SELECT 1
              FROM unnest(sqlc.arg(tag_ids)::uuid[]) AS selected_tag(tag_id)
              WHERE NOT EXISTS (
                  SELECT 1
                  FROM admin_user_tag_links AS link
                  WHERE link.user_id = user_row.user_id
                    AND link.tag_id = selected_tag.tag_id
              )
          )
      )
      AND (sqlc.narg(created_from)::timestamptz IS NULL OR user_row.created_at >= sqlc.narg(created_from))
      AND (sqlc.narg(created_to)::timestamptz IS NULL OR user_row.created_at <= sqlc.narg(created_to))
)
SELECT listed_user.user_id,
       listed_user.status,
       listed_user.username,
       listed_user.current_username_key,
       listed_user.username_changed_at,
       listed_user.account_version,
       listed_user.created_at,
       listed_user.updated_at,
       listed_user.last_activity_at,
       listed_user.username_sort_key
FROM candidates AS listed_user
WHERE (sqlc.narg(last_activity_from)::timestamptz IS NULL OR listed_user.last_activity_at >= sqlc.narg(last_activity_from))
  AND (sqlc.narg(last_activity_to)::timestamptz IS NULL OR listed_user.last_activity_at <= sqlc.narg(last_activity_to))
  AND (
      sqlc.narg(after_user_id)::uuid IS NULL
      OR (sqlc.arg(sort_field)::text = 'created_at' AND (
          (sqlc.arg(sort_direction)::text = 'ascending'
              AND (listed_user.created_at > sqlc.narg(after_sort_time)::timestamptz
                  OR (listed_user.created_at = sqlc.narg(after_sort_time)::timestamptz AND listed_user.user_id > sqlc.narg(after_user_id)::uuid)))
          OR (sqlc.arg(sort_direction)::text = 'descending'
              AND (listed_user.created_at < sqlc.narg(after_sort_time)::timestamptz
                  OR (listed_user.created_at = sqlc.narg(after_sort_time)::timestamptz AND listed_user.user_id < sqlc.narg(after_user_id)::uuid)))
      ))
      OR (sqlc.arg(sort_field)::text = 'last_activity_at' AND (
          (sqlc.arg(sort_direction)::text = 'ascending'
              AND (listed_user.last_activity_at > sqlc.narg(after_sort_time)::timestamptz
                  OR (listed_user.last_activity_at = sqlc.narg(after_sort_time)::timestamptz AND listed_user.user_id > sqlc.narg(after_user_id)::uuid)))
          OR (sqlc.arg(sort_direction)::text = 'descending'
              AND (listed_user.last_activity_at < sqlc.narg(after_sort_time)::timestamptz
                  OR (listed_user.last_activity_at = sqlc.narg(after_sort_time)::timestamptz AND listed_user.user_id < sqlc.narg(after_user_id)::uuid)))
      ))
      OR (sqlc.arg(sort_field)::text = 'username' AND (
          (sqlc.arg(sort_direction)::text = 'ascending'
              AND (listed_user.username_sort_key > sqlc.narg(after_sort_text)::text
                  OR (listed_user.username_sort_key = sqlc.narg(after_sort_text)::text AND listed_user.user_id > sqlc.narg(after_user_id)::uuid)))
          OR (sqlc.arg(sort_direction)::text = 'descending'
              AND (listed_user.username_sort_key < sqlc.narg(after_sort_text)::text
                  OR (listed_user.username_sort_key = sqlc.narg(after_sort_text)::text AND listed_user.user_id < sqlc.narg(after_user_id)::uuid)))
      ))
      OR (sqlc.arg(sort_field)::text = 'user_id' AND (
          (sqlc.arg(sort_direction)::text = 'ascending' AND listed_user.user_id > sqlc.narg(after_user_id)::uuid)
          OR (sqlc.arg(sort_direction)::text = 'descending' AND listed_user.user_id < sqlc.narg(after_user_id)::uuid)
      ))
  )
ORDER BY
    CASE WHEN sqlc.arg(sort_field)::text = 'created_at' AND sqlc.arg(sort_direction)::text = 'ascending' THEN listed_user.created_at END ASC,
    CASE WHEN sqlc.arg(sort_field)::text = 'created_at' AND sqlc.arg(sort_direction)::text = 'descending' THEN listed_user.created_at END DESC,
    CASE WHEN sqlc.arg(sort_field)::text = 'last_activity_at' AND sqlc.arg(sort_direction)::text = 'ascending' THEN listed_user.last_activity_at END ASC,
    CASE WHEN sqlc.arg(sort_field)::text = 'last_activity_at' AND sqlc.arg(sort_direction)::text = 'descending' THEN listed_user.last_activity_at END DESC,
    CASE WHEN sqlc.arg(sort_field)::text = 'username' AND sqlc.arg(sort_direction)::text = 'ascending' THEN listed_user.username_sort_key END ASC,
    CASE WHEN sqlc.arg(sort_field)::text = 'username' AND sqlc.arg(sort_direction)::text = 'descending' THEN listed_user.username_sort_key END DESC,
    CASE WHEN sqlc.arg(sort_direction)::text = 'ascending' THEN listed_user.user_id END ASC,
    CASE WHEN sqlc.arg(sort_direction)::text = 'descending' THEN listed_user.user_id END DESC
LIMIT sqlc.arg(page_size);

-- name: ListAdminUserTagsForUsers :many
SELECT link.user_id,
       tag.tag_id,
       tag.name,
       tag.normalized_name,
       tag.color,
       tag.version,
       tag.created_by_admin_id,
       tag.updated_by_admin_id,
       tag.reason,
       tag.created_at,
       tag.updated_at
FROM admin_user_tag_links AS link
JOIN admin_user_tags AS tag ON tag.tag_id = link.tag_id
WHERE link.user_id = ANY(sqlc.arg(user_ids)::uuid[])
ORDER BY link.user_id, tag.normalized_name, tag.tag_id;
