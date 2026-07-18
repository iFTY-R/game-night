-- name: CreateOutboxEvent :one
INSERT INTO outbox_events (
    event_id,
    event_type,
    aggregate_type,
    aggregate_id,
    payload,
    created_at,
    available_at
) VALUES (
    sqlc.arg(event_id),
    sqlc.arg(event_type),
    sqlc.arg(aggregate_type),
    sqlc.arg(aggregate_id),
    sqlc.arg(payload),
    sqlc.arg(created_at),
    sqlc.arg(available_at)
)
ON CONFLICT (event_id) DO NOTHING
RETURNING event_sequence, event_id, event_type, aggregate_type, aggregate_id,
          payload, created_at, available_at;

-- name: GetOutboxConsumer :one
SELECT consumer_id, subscriptions, last_acked_sequence, lease_owner, lease_until,
       retry_count, next_attempt_at, last_error_code, created_at, updated_at
FROM outbox_consumers
WHERE consumer_id = sqlc.arg(consumer_id);

-- name: AcquireOutboxConsumerLeaseCAS :one
UPDATE outbox_consumers
SET lease_owner = sqlc.arg(lease_owner),
    lease_until = sqlc.arg(lease_until),
    updated_at = sqlc.arg(acquired_at)
WHERE consumer_id = sqlc.arg(consumer_id)
  AND (
      lease_owner IS NULL
      OR lease_until <= sqlc.arg(acquired_at)
      OR lease_owner = sqlc.arg(lease_owner)
  )
  AND (next_attempt_at IS NULL OR next_attempt_at <= sqlc.arg(acquired_at))
RETURNING consumer_id, subscriptions, last_acked_sequence, lease_owner, lease_until,
          retry_count, next_attempt_at, last_error_code, created_at, updated_at;

-- name: RenewOutboxConsumerLeaseCAS :one
UPDATE outbox_consumers
SET lease_until = sqlc.arg(lease_until),
    updated_at = sqlc.arg(renewed_at)
WHERE consumer_id = sqlc.arg(consumer_id)
  AND lease_owner = sqlc.arg(lease_owner)
  AND lease_until > sqlc.arg(renewed_at)
RETURNING consumer_id, last_acked_sequence, lease_owner, lease_until, updated_at;

-- name: ListOutboxEventsForConsumer :many
SELECT event.event_sequence, event.event_id, event.event_type, event.aggregate_type,
       event.aggregate_id, event.payload, event.created_at, event.available_at
FROM outbox_events AS event
JOIN outbox_consumers AS consumer
  ON consumer.consumer_id = sqlc.arg(consumer_id)
WHERE consumer.lease_owner = sqlc.arg(lease_owner)
  AND consumer.lease_until > sqlc.arg(read_at)
  AND event.event_sequence > consumer.last_acked_sequence
  AND event.available_at <= sqlc.arg(read_at)
  AND event.event_type = ANY(consumer.subscriptions)
ORDER BY event.event_sequence
LIMIT sqlc.arg(batch_size);

-- name: AckOutboxConsumerOffsetCAS :one
UPDATE outbox_consumers
SET last_acked_sequence = sqlc.arg(acked_sequence),
    retry_count = 0,
    next_attempt_at = NULL,
    last_error_code = NULL,
    updated_at = sqlc.arg(acked_at)
WHERE consumer_id = sqlc.arg(consumer_id)
  AND lease_owner = sqlc.arg(lease_owner)
  AND lease_until > sqlc.arg(acked_at)
  AND last_acked_sequence = sqlc.arg(expected_sequence)
  AND sqlc.arg(acked_sequence)::bigint > sqlc.arg(expected_sequence)::bigint
RETURNING consumer_id, last_acked_sequence, lease_owner, lease_until, retry_count, updated_at;

-- name: RecordOutboxConsumerRetryCAS :one
UPDATE outbox_consumers
SET retry_count = retry_count + 1,
    next_attempt_at = sqlc.arg(next_attempt_at),
    last_error_code = sqlc.arg(error_code),
    updated_at = sqlc.arg(failed_at)
WHERE consumer_id = sqlc.arg(consumer_id)
  AND lease_owner = sqlc.arg(lease_owner)
  AND lease_until > sqlc.arg(failed_at)
RETURNING consumer_id, last_acked_sequence, lease_owner, lease_until, retry_count,
          next_attempt_at, last_error_code, updated_at;

-- name: ReleaseOutboxConsumerLeaseCAS :one
UPDATE outbox_consumers
SET lease_owner = NULL,
    lease_until = NULL,
    updated_at = sqlc.arg(released_at)
WHERE consumer_id = sqlc.arg(consumer_id)
  AND lease_owner = sqlc.arg(lease_owner)
RETURNING consumer_id, last_acked_sequence, lease_owner, lease_until, updated_at;
