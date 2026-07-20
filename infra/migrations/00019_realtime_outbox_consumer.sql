-- +goose Up

-- Realtime owns only its independent game-fanout consumer offset; durable events remain immutable.
-- +goose StatementBegin
DO $permissions$
DECLARE
    trusted_schema text := current_schema();
    runtime_role text := current_setting('game_night.runtime_role');
BEGIN
    IF trusted_schema IS NULL THEN
        RAISE EXCEPTION 'realtime outbox consumer migration requires an explicit current schema';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = runtime_role) THEN
        RAISE EXCEPTION 'realtime outbox consumer migration requires the configured runtime role';
    END IF;

    EXECUTE format(
        'GRANT SELECT, INSERT, UPDATE ON TABLE %I.outbox_consumers TO %I',
        trusted_schema,
        runtime_role
    );
END;
$permissions$;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DO $permissions$
DECLARE
    trusted_schema text := current_schema();
    runtime_role text := current_setting('game_night.runtime_role');
BEGIN
    IF trusted_schema IS NULL THEN
        RAISE EXCEPTION 'realtime outbox consumer rollback requires an explicit current schema';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = runtime_role) THEN
        RAISE EXCEPTION 'realtime outbox consumer rollback requires the configured runtime role';
    END IF;

    EXECUTE format(
        'REVOKE SELECT, INSERT, UPDATE ON TABLE %I.outbox_consumers FROM %I',
        trusted_schema,
        runtime_role
    );
END;
$permissions$;
-- +goose StatementEnd
