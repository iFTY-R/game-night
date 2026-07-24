-- +goose Up

ALTER TABLE game_sessions
    ADD COLUMN cancel_reason text;

UPDATE game_sessions
SET cancel_reason = 'legacy_cancelled'
WHERE status = 'cancelled'
  AND cancel_reason IS NULL;

ALTER TABLE game_sessions
    ADD CONSTRAINT game_sessions_cancel_reason_shape CHECK (
        (
            status = 'cancelled'
            AND cancel_reason IS NOT NULL
            AND length(cancel_reason) BETWEEN 1 AND 64
            AND cancel_reason ~ '^[a-z0-9]+([._-][a-z0-9]+)*$'
        )
        OR (
            status <> 'cancelled'
            AND cancel_reason IS NULL
        )
    );

-- +goose Down

ALTER TABLE game_sessions
    DROP CONSTRAINT IF EXISTS game_sessions_cancel_reason_shape,
    DROP COLUMN IF EXISTS cancel_reason;
