-- name: ReadAuditHead :one
WITH result AS (
    SELECT pg_catalog.to_jsonb(function_row) AS payload
    FROM read_audit_head(sqlc.arg(chain_id)) AS function_row
)
SELECT (result.payload ->> 'sequence')::bigint AS sequence,
       decode(pg_catalog.substring(result.payload ->> 'head_hash', 3), 'hex') AS head_hash,
       (result.payload ->> 'updated_at')::timestamptz AS updated_at
FROM result;

-- name: ReadAuditAnchor :one
WITH result AS (
    SELECT pg_catalog.to_jsonb(function_row) AS payload
    FROM read_audit_anchor(sqlc.arg(chain_id)::text, sqlc.arg(sequence)::bigint) AS function_row
)
SELECT decode(pg_catalog.substring(result.payload ->> 'event_hash', 3), 'hex') AS event_hash,
       (result.payload ->> 'created_at')::timestamptz AS created_at
FROM result;

-- name: AppendAuditEvent :one
WITH result AS (
    SELECT pg_catalog.to_jsonb(function_row) AS payload
    FROM append_audit_event(
        sqlc.arg(chain_id),
        sqlc.arg(expected_previous_hash),
        sqlc.arg(event_id),
        sqlc.arg(canonical_event),
        sqlc.arg(signature),
        sqlc.arg(signing_key_version),
        sqlc.arg(created_at)
    ) AS function_row
)
SELECT (result.payload ->> 'appended_sequence')::bigint AS appended_sequence,
       decode(pg_catalog.substring(result.payload ->> 'appended_hash', 3), 'hex') AS appended_hash
FROM result;

-- name: ResetAdminAccount :one
WITH result AS (
    SELECT pg_catalog.to_jsonb(function_row) AS payload
    FROM reset_admin_account(
        sqlc.arg(expected_previous_hash),
        sqlc.arg(event_id),
        sqlc.arg(canonical_event),
        sqlc.arg(signature),
        sqlc.arg(signing_key_version),
        sqlc.arg(created_at),
        sqlc.arg(password_hash),
        sqlc.arg(password_algorithm),
        sqlc.arg(password_parameters),
        sqlc.arg(checkpoint_event_id),
        sqlc.arg(checkpoint_payload)
    ) AS function_row
)
SELECT (result.payload ->> 'appended_sequence')::bigint AS appended_sequence,
       decode(pg_catalog.substring(result.payload ->> 'appended_hash', 3), 'hex') AS appended_hash
FROM result;

-- name: ListAuditEvents :many
SELECT chain_id, sequence, event_id, previous_hash, canonical_event, event_hash,
       signature, signing_key_version, created_at
FROM audit_events_redacted
WHERE chain_id = sqlc.arg(chain_id)
  AND sequence > sqlc.arg(after_sequence)
ORDER BY sequence
LIMIT sqlc.arg(page_size);
