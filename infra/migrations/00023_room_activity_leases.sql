-- +goose Up

-- Room activity is intentionally separate from the optimistic PartyRoom aggregate so heartbeats never invalidate host commands.
CREATE TABLE room_activity_leases (
    room_id uuid PRIMARY KEY REFERENCES party_rooms (room_id) ON DELETE CASCADE,
    last_seen_at timestamptz NOT NULL
);

-- Existing rooms start from their latest aggregate activity so upgrades neither strand nor immediately expire them.
INSERT INTO room_activity_leases (room_id, last_seen_at)
SELECT room_id, updated_at
FROM party_rooms;

CREATE INDEX room_activity_leases_expiry_idx
    ON room_activity_leases (last_seen_at, room_id);

-- The worker locks each lease before closing its room; concurrent heartbeats therefore either renew first or observe CLOSED.
-- +goose StatementBegin
DO $permissions$
DECLARE
    trusted_schema text := current_schema();
    owner_role text := current_setting('game_night.owner_role');
    runtime_role text := current_setting('game_night.runtime_role');
    worker_role text := current_setting('game_night.worker_role');
BEGIN
    IF trusted_schema IS NULL THEN
        RAISE EXCEPTION 'room activity lease migration requires an explicit current schema';
    END IF;

    EXECUTE format(
        $ddl$
        CREATE FUNCTION %1$I.close_expired_party_rooms(room_idle_seconds bigint)
        RETURNS bigint
        LANGUAGE plpgsql
        SECURITY DEFINER
        VOLATILE
        SET search_path = pg_catalog, %1$I, pg_temp
        AS $function$
        DECLARE
            boundary timestamptz := pg_catalog.clock_timestamp();
            cutoff timestamptz;
            target record;
            affected bigint := 0;
            closed_rooms bigint := 0;
        BEGIN
            IF room_idle_seconds < 60 OR room_idle_seconds > 86400 THEN
                RAISE EXCEPTION 'room idle timeout is outside the supported range';
            END IF;
            cutoff := boundary - pg_catalog.make_interval(secs => room_idle_seconds);

            FOR target IN
                SELECT lease.room_id
                FROM %1$I.room_activity_leases AS lease
                JOIN %1$I.party_rooms AS room ON room.room_id = lease.room_id
                WHERE lease.last_seen_at < cutoff
                  AND room.updated_at < cutoff
                  AND room.status IN ('lobby', 'post_game')
                ORDER BY lease.last_seen_at, lease.room_id
                LIMIT 100
                FOR UPDATE OF lease SKIP LOCKED
            LOOP
                UPDATE %1$I.party_rooms AS room
                SET status = 'closed',
                    participant_admission = 'closed',
                    spectator_admission = 'closed',
                    room_version = room.room_version + 1,
                    updated_at = boundary
                WHERE room.room_id = target.room_id
                  AND room.updated_at < cutoff
                  AND room.status IN ('lobby', 'post_game')
                  AND EXISTS (
                      SELECT 1
                      FROM %1$I.room_activity_leases AS lease
                      WHERE lease.room_id = room.room_id
                        AND lease.last_seen_at < cutoff
                  );
                GET DIAGNOSTICS affected = ROW_COUNT;
                closed_rooms := closed_rooms + affected;
            END LOOP;

            RETURN closed_rooms;
        END
        $function$
        $ddl$,
        trusted_schema
    );

    EXECUTE format('ALTER TABLE %I.room_activity_leases OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format('REVOKE ALL ON TABLE %I.room_activity_leases FROM PUBLIC', trusted_schema);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE %I.room_activity_leases TO %I', trusted_schema, runtime_role);
    EXECUTE format('ALTER FUNCTION %I.close_expired_party_rooms(bigint) OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format('REVOKE ALL ON FUNCTION %I.close_expired_party_rooms(bigint) FROM PUBLIC', trusted_schema);
    EXECUTE format('GRANT EXECUTE ON FUNCTION %I.close_expired_party_rooms(bigint) TO %I', trusted_schema, worker_role);
END;
$permissions$;
-- +goose StatementEnd

-- +goose Down

DROP FUNCTION IF EXISTS close_expired_party_rooms(bigint);
DROP TABLE IF EXISTS room_activity_leases;
