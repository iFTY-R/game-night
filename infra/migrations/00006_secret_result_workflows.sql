-- +goose Up

-- +goose StatementBegin
DO $migration$
DECLARE
    consumption_constraint name;
BEGIN
    SELECT constraint_record.conname
    INTO consumption_constraint
    FROM pg_catalog.pg_constraint AS constraint_record
    WHERE constraint_record.conrelid = 'anonymous_challenges'::regclass
      AND constraint_record.contype = 'c'
      AND pg_catalog.pg_get_constraintdef(constraint_record.oid) LIKE '%consumed_at IS NULL%replay_until IS NULL%result_id IS NULL%';

    IF consumption_constraint IS NULL THEN
        RAISE EXCEPTION 'anonymous challenge consumption constraint not found';
    END IF;

    EXECUTE pg_catalog.format('ALTER TABLE anonymous_challenges DROP CONSTRAINT %I', consumption_constraint);
END
$migration$;
-- +goose StatementEnd

ALTER TABLE anonymous_challenges
    ADD CONSTRAINT anonymous_challenges_consumption_shape_check
    CHECK (
        (
            consumed_at IS NULL
            AND replay_until IS NULL
            AND operation_id IS NULL
            AND request_digest IS NULL
            AND result_id IS NULL
        )
        OR (
            consumed_at IS NOT NULL
            AND (
                (
                    replay_until IS NULL
                    AND operation_id IS NULL
                    AND request_digest IS NULL
                    AND result_id IS NULL
                )
                OR (
                    replay_until IS NOT NULL
                    AND operation_id IS NOT NULL
                    AND octet_length(request_digest) = 32
                    AND result_id IS NOT NULL
                )
            )
        )
    );

-- +goose Down

ALTER TABLE anonymous_challenges
    DROP CONSTRAINT IF EXISTS anonymous_challenges_consumption_shape_check;

-- Downgrade cannot represent consumed challenges without replay metadata. Remove dependent
-- short-lived recovery grants first because their challenge foreign key is intentionally restrictive.
DELETE FROM user_recovery_attempts
WHERE challenge_id IN (
    SELECT challenge_id
    FROM anonymous_challenges
    WHERE consumed_at IS NOT NULL
      AND replay_until IS NULL
      AND result_id IS NULL
);

-- This is an intentionally lossy downgrade for rows created only by the newer recovery protocol.
DELETE FROM anonymous_challenges
WHERE consumed_at IS NOT NULL
  AND replay_until IS NULL
  AND result_id IS NULL;

ALTER TABLE anonymous_challenges
    ADD CONSTRAINT anonymous_challenges_consumption_shape_check
    CHECK (
        (consumed_at IS NULL AND replay_until IS NULL AND operation_id IS NULL AND request_digest IS NULL AND result_id IS NULL)
        OR (
            consumed_at IS NOT NULL
            AND replay_until IS NOT NULL
            AND operation_id IS NOT NULL
            AND octet_length(request_digest) = 32
            AND result_id IS NOT NULL
        )
    );
