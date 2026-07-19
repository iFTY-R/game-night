-- +goose Up

-- Expose only the checkpoint consumer state needed by runtime readiness; worker keeps table-level lease authority.
-- +goose StatementBegin
DO $outer$
DECLARE
    trusted_schema text := current_schema();
    owner_role text := current_setting('game_night.owner_role');
    runtime_role text := current_setting('game_night.runtime_role');
BEGIN
    IF trusted_schema IS NULL THEN
        RAISE EXCEPTION 'checkpoint health migration requires an explicit current schema';
    END IF;

    EXECUTE format(
        $ddl$
        CREATE FUNCTION %1$I.read_checkpoint_consumer_sequence()
        RETURNS bigint
        LANGUAGE sql
        SECURITY DEFINER
        STABLE
        SET search_path = pg_catalog, %1$I, pg_temp
        AS $function$
            SELECT COALESCE((
                SELECT consumer.last_acked_sequence
                FROM %1$I.outbox_consumers AS consumer
                WHERE consumer.consumer_id = 'audit.checkpoint'
            ), -1::bigint)
        $function$
        $ddl$,
        trusted_schema
    );
    EXECUTE format('ALTER FUNCTION %I.read_checkpoint_consumer_sequence() OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format('REVOKE ALL ON FUNCTION %I.read_checkpoint_consumer_sequence() FROM PUBLIC', trusted_schema);
    EXECUTE format('GRANT EXECUTE ON FUNCTION %I.read_checkpoint_consumer_sequence() TO %I', trusted_schema, runtime_role);
END;
$outer$;
-- +goose StatementEnd

-- +goose Down

DROP FUNCTION IF EXISTS read_checkpoint_consumer_sequence();
