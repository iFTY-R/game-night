-- name: CreateOutboxEvent :one
WITH serialized AS MATERIALIZED (
    -- Transaction-level serialization makes identity sequence order match commit order for offset safety.
    SELECT pg_catalog.pg_advisory_xact_lock(1196314434, 1)
)
INSERT INTO outbox_events (
    event_id,
    event_type,
    aggregate_type,
    aggregate_id,
    payload,
    created_at,
    available_at
) SELECT
    sqlc.arg(event_id),
    sqlc.arg(event_type),
    sqlc.arg(aggregate_type),
    sqlc.arg(aggregate_id),
    sqlc.arg(payload),
    sqlc.arg(created_at),
    sqlc.arg(available_at)
FROM serialized
ON CONFLICT (event_id) DO NOTHING
RETURNING event_sequence, event_id, event_type, aggregate_type, aggregate_id,
          payload, created_at, available_at;

-- name: GetOutboxEventByID :one
SELECT event_sequence, event_id, event_type, aggregate_type, aggregate_id,
       payload, created_at, available_at
FROM outbox_events
WHERE event_id = sqlc.arg(event_id);

-- name: CreateOutboxConsumer :one
INSERT INTO outbox_consumers (
    consumer_id,
    subscriptions,
    last_acked_sequence,
    created_at,
    updated_at
) VALUES (
    sqlc.arg(consumer_id),
    sqlc.arg(subscriptions),
    sqlc.arg(last_acked_sequence),
    sqlc.arg(created_at),
    sqlc.arg(created_at)
)
ON CONFLICT (consumer_id) DO NOTHING
RETURNING consumer_id, subscriptions, last_acked_sequence, lease_owner, lease_until,
          retry_count, next_attempt_at, last_error_code, created_at, updated_at;

-- name: GetOutboxConsumer :one
SELECT consumer_id, subscriptions, last_acked_sequence, lease_owner, lease_until,
       retry_count, next_attempt_at, last_error_code, created_at, updated_at
FROM outbox_consumers
WHERE consumer_id = sqlc.arg(consumer_id);

-- name: AcquireOutboxConsumerLeaseCAS :one
UPDATE outbox_consumers
SET lease_owner = sqlc.arg(next_lease_owner),
    lease_until = sqlc.arg(next_lease_until),
    updated_at = sqlc.arg(next_updated_at)
WHERE consumer_id = sqlc.arg(consumer_id)
  AND lease_owner IS NOT DISTINCT FROM sqlc.narg(expected_lease_owner)
  AND lease_until IS NOT DISTINCT FROM sqlc.narg(expected_lease_until)
  AND last_acked_sequence = sqlc.arg(expected_sequence)
  AND retry_count = sqlc.arg(expected_retry_count)
  AND next_attempt_at IS NOT DISTINCT FROM sqlc.narg(expected_next_attempt_at)
  AND last_error_code IS NOT DISTINCT FROM sqlc.narg(expected_error_code)
  AND updated_at = sqlc.arg(expected_updated_at)
  AND (lease_until IS NULL OR lease_until <= pg_catalog.clock_timestamp())
  AND (next_attempt_at IS NULL OR next_attempt_at <= pg_catalog.clock_timestamp())
  AND sqlc.arg(next_lease_until) > pg_catalog.clock_timestamp()
RETURNING consumer_id, subscriptions, last_acked_sequence, lease_owner, lease_until,
          retry_count, next_attempt_at, last_error_code, created_at, updated_at;

-- name: RenewOutboxConsumerLeaseCAS :one
UPDATE outbox_consumers
SET lease_until = sqlc.arg(next_lease_until),
    updated_at = sqlc.arg(next_updated_at)
WHERE consumer_id = sqlc.arg(consumer_id)
  AND lease_owner = sqlc.arg(expected_lease_owner)
  AND lease_until = sqlc.arg(expected_lease_until)
  AND last_acked_sequence = sqlc.arg(expected_sequence)
  AND retry_count = sqlc.arg(expected_retry_count)
  AND next_attempt_at IS NOT DISTINCT FROM sqlc.narg(expected_next_attempt_at)
  AND last_error_code IS NOT DISTINCT FROM sqlc.narg(expected_error_code)
  AND updated_at = sqlc.arg(expected_updated_at)
  AND lease_until > pg_catalog.clock_timestamp()
  AND sqlc.arg(next_lease_until) > pg_catalog.clock_timestamp()
RETURNING consumer_id, subscriptions, last_acked_sequence, lease_owner, lease_until,
          retry_count, next_attempt_at, last_error_code, created_at, updated_at;

-- name: ListOutboxEventsForConsumer :many
WITH consumer_state AS MATERIALIZED (
    SELECT subscriptions, last_acked_sequence
    FROM outbox_consumers
    WHERE consumer_id = sqlc.arg(consumer_id)
      AND lease_owner = sqlc.arg(lease_owner)
      AND lease_until > pg_catalog.clock_timestamp()
      AND (next_attempt_at IS NULL OR next_attempt_at <= pg_catalog.clock_timestamp())
      AND sqlc.arg(read_at)::timestamptz <= pg_catalog.clock_timestamp()
), candidates AS MATERIALIZED (
    SELECT event.event_sequence, event.event_id, event.event_type, event.aggregate_type,
           event.aggregate_id, event.payload, event.created_at, event.available_at
    FROM outbox_events AS event
    CROSS JOIN consumer_state AS consumer
    WHERE event.event_sequence > consumer.last_acked_sequence
      AND event.event_type = ANY(consumer.subscriptions)
    ORDER BY event.event_sequence
    LIMIT sqlc.arg(batch_size)
)
SELECT event_sequence, event_id, event_type, aggregate_type, aggregate_id,
       payload, created_at, available_at
FROM candidates AS event
WHERE event.available_at <= pg_catalog.clock_timestamp()
  AND NOT EXISTS (
      SELECT 1
      FROM candidates AS earlier
      WHERE earlier.event_sequence < event.event_sequence
        AND earlier.available_at > pg_catalog.clock_timestamp()
  )
ORDER BY event.event_sequence;

-- name: AckOutboxConsumerOffsetCAS :one
UPDATE outbox_consumers
SET last_acked_sequence = sqlc.arg(acked_sequence),
    retry_count = 0,
    next_attempt_at = NULL,
    last_error_code = NULL,
    updated_at = sqlc.arg(acked_at)
WHERE consumer_id = sqlc.arg(consumer_id)
  AND lease_owner = sqlc.arg(expected_lease_owner)
  AND lease_until = sqlc.arg(expected_lease_until)
  AND last_acked_sequence = sqlc.arg(expected_sequence)
  AND retry_count = sqlc.arg(expected_retry_count)
  AND next_attempt_at IS NOT DISTINCT FROM sqlc.narg(expected_next_attempt_at)
  AND last_error_code IS NOT DISTINCT FROM sqlc.narg(expected_error_code)
  AND updated_at = sqlc.arg(expected_updated_at)
  AND lease_until > pg_catalog.clock_timestamp()
  AND sqlc.arg(acked_sequence)::bigint > sqlc.arg(expected_sequence)::bigint
  AND EXISTS (
      SELECT 1
      FROM outbox_events AS event
      WHERE event.event_sequence = sqlc.arg(acked_sequence)
        AND event.event_type = ANY(outbox_consumers.subscriptions)
        AND event.available_at <= pg_catalog.clock_timestamp()
  )
  AND NOT EXISTS (
      SELECT 1
      FROM outbox_events AS skipped
      WHERE skipped.event_sequence > sqlc.arg(expected_sequence)
        AND skipped.event_sequence < sqlc.arg(acked_sequence)
        AND skipped.event_type = ANY(outbox_consumers.subscriptions)
  )
RETURNING consumer_id, subscriptions, last_acked_sequence, lease_owner, lease_until,
          retry_count, next_attempt_at, last_error_code, created_at, updated_at;

-- name: RecordOutboxConsumerRetryCAS :one
UPDATE outbox_consumers
SET retry_count = sqlc.arg(next_retry_count),
    next_attempt_at = sqlc.arg(next_attempt_at),
    last_error_code = sqlc.arg(error_code),
    updated_at = sqlc.arg(failed_at)
WHERE consumer_id = sqlc.arg(consumer_id)
  AND lease_owner = sqlc.arg(expected_lease_owner)
  AND lease_until = sqlc.arg(expected_lease_until)
  AND last_acked_sequence = sqlc.arg(expected_sequence)
  AND retry_count = sqlc.arg(expected_retry_count)
  AND next_attempt_at IS NOT DISTINCT FROM sqlc.narg(expected_next_attempt_at)
  AND last_error_code IS NOT DISTINCT FROM sqlc.narg(expected_error_code)
  AND updated_at = sqlc.arg(expected_updated_at)
  AND lease_until > pg_catalog.clock_timestamp()
RETURNING consumer_id, subscriptions, last_acked_sequence, lease_owner, lease_until,
          retry_count, next_attempt_at, last_error_code, created_at, updated_at;

-- name: ReleaseOutboxConsumerLeaseCAS :one
UPDATE outbox_consumers
SET lease_owner = NULL,
    lease_until = NULL,
    updated_at = sqlc.arg(released_at)
WHERE consumer_id = sqlc.arg(consumer_id)
  AND lease_owner = sqlc.arg(expected_lease_owner)
  AND lease_until = sqlc.arg(expected_lease_until)
  AND last_acked_sequence = sqlc.arg(expected_sequence)
  AND retry_count = sqlc.arg(expected_retry_count)
  AND next_attempt_at IS NOT DISTINCT FROM sqlc.narg(expected_next_attempt_at)
  AND last_error_code IS NOT DISTINCT FROM sqlc.narg(expected_error_code)
  AND updated_at = sqlc.arg(expected_updated_at)
  AND lease_until > pg_catalog.clock_timestamp()
RETURNING consumer_id, subscriptions, last_acked_sequence, lease_owner, lease_until,
          retry_count, next_attempt_at, last_error_code, created_at, updated_at;

-- name: GetLatestAckedOutboxEventByType :one
SELECT event.event_sequence, event.event_id, event.event_type, event.aggregate_type,
       event.aggregate_id, event.payload, event.created_at, event.available_at
FROM outbox_events AS event
JOIN outbox_consumers AS consumer
  ON consumer.consumer_id = sqlc.arg(consumer_id)
WHERE event.event_sequence <= consumer.last_acked_sequence
  AND event.event_type = sqlc.arg(event_type)
  AND event.event_type = ANY(consumer.subscriptions)
ORDER BY event.event_sequence DESC
LIMIT 1;

-- name: ReadCheckpointConsumerSequence :one
SELECT read_checkpoint_consumer_sequence();
