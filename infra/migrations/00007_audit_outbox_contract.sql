-- +goose Up

-- read_audit_head must return the authoritative update time so canonical events cannot move backwards in time.
-- +goose StatementBegin
DO $outer$
DECLARE
    trusted_schema text := current_schema();
    owner_role text := current_setting('game_night.owner_role');
    audit_writer_role text := current_setting('game_night.audit_writer_role');
BEGIN
    IF trusted_schema IS NULL THEN
        RAISE EXCEPTION 'audit contract migration requires an explicit current schema';
    END IF;

    EXECUTE format('DROP FUNCTION %I.read_audit_head(text)', trusted_schema);
    EXECUTE format(
        $ddl$
        CREATE FUNCTION %1$I.read_audit_head(requested_chain_id text)
        RETURNS TABLE(sequence bigint, head_hash bytea, updated_at timestamptz)
        LANGUAGE sql
        SECURITY DEFINER
        STABLE
        SET search_path = pg_catalog, %1$I, pg_temp
        AS $function$
            SELECT head.sequence, head.head_hash, head.updated_at
            FROM %1$I.audit_chain_head AS head
            WHERE head.chain_id = requested_chain_id
        $function$
        $ddl$,
        trusted_schema
    );
    EXECUTE format('ALTER FUNCTION %I.read_audit_head(text) OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format('REVOKE ALL ON FUNCTION %I.read_audit_head(text) FROM PUBLIC', trusted_schema);
    EXECUTE format('GRANT EXECUTE ON FUNCTION %I.read_audit_head(text) TO %I', trusted_schema, audit_writer_role);

    EXECUTE format(
        $ddl$
        CREATE FUNCTION %1$I.read_audit_anchor(requested_chain_id text, requested_sequence bigint)
        RETURNS TABLE(event_hash bytea, created_at timestamptz)
        LANGUAGE sql
        SECURITY DEFINER
        STABLE
        SET search_path = pg_catalog, %1$I, pg_temp
        AS $function$
            SELECT event.event_hash, event.created_at
            FROM %1$I.audit_events AS event
            WHERE event.chain_id = requested_chain_id
              AND event.sequence = requested_sequence
        $function$
        $ddl$,
        trusted_schema
    );
    EXECUTE format('ALTER FUNCTION %I.read_audit_anchor(text, bigint) OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format('REVOKE ALL ON FUNCTION %I.read_audit_anchor(text, bigint) FROM PUBLIC', trusted_schema);
    EXECUTE format('GRANT EXECUTE ON FUNCTION %I.read_audit_anchor(text, bigint) TO %I', trusted_schema, audit_writer_role);
END;
$outer$;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DO $outer$
DECLARE
    trusted_schema text := current_schema();
    owner_role text := current_setting('game_night.owner_role');
    audit_writer_role text := current_setting('game_night.audit_writer_role');
BEGIN
    IF trusted_schema IS NULL THEN
        RAISE EXCEPTION 'audit contract migration requires an explicit current schema';
    END IF;

    EXECUTE format('DROP FUNCTION %I.read_audit_anchor(text, bigint)', trusted_schema);
    EXECUTE format('DROP FUNCTION %I.read_audit_head(text)', trusted_schema);
    EXECUTE format(
        $ddl$
        CREATE FUNCTION %1$I.read_audit_head(requested_chain_id text)
        RETURNS TABLE(sequence bigint, head_hash bytea)
        LANGUAGE sql
        SECURITY DEFINER
        STABLE
        SET search_path = pg_catalog, %1$I, pg_temp
        AS $function$
            SELECT head.sequence, head.head_hash
            FROM %1$I.audit_chain_head AS head
            WHERE head.chain_id = requested_chain_id
        $function$
        $ddl$,
        trusted_schema
    );
    EXECUTE format('ALTER FUNCTION %I.read_audit_head(text) OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format('REVOKE ALL ON FUNCTION %I.read_audit_head(text) FROM PUBLIC', trusted_schema);
    EXECUTE format('GRANT EXECUTE ON FUNCTION %I.read_audit_head(text) TO %I', trusted_schema, audit_writer_role);
END;
$outer$;
-- +goose StatementEnd
