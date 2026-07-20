-- +goose Up

-- System work remains durable while a concurrent action advances the session. A pending row can
-- be retried with the same logical digest and completed with or without an engine event batch.
ALTER TABLE game_session_event_batches
    ADD COLUMN timer_id text,
    ADD COLUMN system_operation_id text,
    ADD COLUMN system_source_kind text,
    ADD COLUMN system_source_event_id uuid,
    ADD COLUMN system_requested_by_user_id uuid REFERENCES users (user_id),
    ADD COLUMN system_request_digest bytea,
    ADD CONSTRAINT game_session_event_batches_batch_session_unique
        UNIQUE (batch_id, session_id),
    ADD CONSTRAINT game_session_event_batches_system_identity_unique
        UNIQUE (batch_id, session_id, system_operation_id, system_source_event_id, system_request_digest),
    ADD CONSTRAINT game_session_event_batches_runtime_source_shape CHECK (
        (cause IN ('created', 'action')
            AND timer_id IS NULL
            AND system_operation_id IS NULL
            AND system_source_kind IS NULL
            AND system_source_event_id IS NULL
            AND system_requested_by_user_id IS NULL
            AND system_request_digest IS NULL)
        OR (cause = 'timer'
            AND timer_id IS NOT NULL
            AND system_operation_id IS NULL
            AND system_source_kind IS NULL
            AND system_source_event_id IS NULL
            AND system_requested_by_user_id IS NULL
            AND system_request_digest IS NULL)
        OR (cause = 'system'
            AND timer_id IS NULL
            AND system_operation_id IS NOT NULL
            AND system_source_kind IS NOT NULL
            AND system_source_event_id IS NOT NULL
            AND octet_length(system_request_digest) = 32)
    ),
    ADD CONSTRAINT game_session_event_batches_timer_id_shape CHECK (
        timer_id IS NULL
        OR (length(timer_id) <= 64 AND timer_id ~ '^[a-z0-9]+([._-][a-z0-9]+)*$')
    ),
    ADD CONSTRAINT game_session_event_batches_system_operation_shape CHECK (
        system_operation_id IS NULL
        OR (length(system_operation_id) BETWEEN 22 AND 86 AND system_operation_id ~ '^[A-Za-z0-9_-]+$')
    ),
    ADD CONSTRAINT game_session_event_batches_system_source_shape CHECK (
        system_source_kind IS NULL
        OR system_source_kind IN ('host_api', 'room_outbox', 'platform')
    ),
    ADD CONSTRAINT game_session_event_batches_system_requester_shape CHECK (
        (system_source_kind IS NULL AND system_requested_by_user_id IS NULL)
        OR (system_source_kind = 'host_api' AND system_requested_by_user_id IS NOT NULL)
        OR (system_source_kind IN ('room_outbox', 'platform') AND system_requested_by_user_id IS NULL)
    );

CREATE TABLE game_timer_receipts (
    session_id uuid NOT NULL,
    timer_id text NOT NULL,
    expected_state_version bigint NOT NULL CHECK (expected_state_version > 0),
    result_code text NOT NULL,
    result_digest bytea NOT NULL CHECK (octet_length(result_digest) = 32),
    committed_state_version bigint NOT NULL CHECK (committed_state_version > 0),
    batch_id uuid NOT NULL,
    committed_at timestamptz NOT NULL,
    PRIMARY KEY (session_id, timer_id, expected_state_version),
    CONSTRAINT game_timer_receipts_batch_unique UNIQUE (batch_id),
    CONSTRAINT game_timer_receipts_batch_fk
        FOREIGN KEY (batch_id, session_id)
        REFERENCES game_session_event_batches (batch_id, session_id)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT game_timer_receipts_timer_id_shape CHECK (
        length(timer_id) <= 64 AND timer_id ~ '^[a-z0-9]+([._-][a-z0-9]+)*$'
    ),
    CONSTRAINT game_timer_receipts_result_code_shape CHECK (
        length(result_code) <= 64 AND result_code ~ '^[a-z0-9]+([._-][a-z0-9]+)*$'
    )
);

CREATE TABLE game_system_operations (
    session_id uuid NOT NULL REFERENCES game_sessions (session_id) ON DELETE CASCADE,
    operation_id text NOT NULL,
    source_kind text NOT NULL CHECK (source_kind IN ('host_api', 'room_outbox', 'platform')),
    source_event_id uuid NOT NULL,
    requested_by_user_id uuid REFERENCES users (user_id),
    logical_digest bytea NOT NULL CHECK (octet_length(logical_digest) = 32),
    status text NOT NULL CHECK (status IN ('pending', 'completed')),
    result_code text,
    result_digest bytea CHECK (result_digest IS NULL OR octet_length(result_digest) = 32),
    committed_state_version bigint CHECK (committed_state_version > 0),
    batch_id uuid,
    created_at timestamptz NOT NULL,
    completed_at timestamptz,
    PRIMARY KEY (session_id, operation_id),
    CONSTRAINT game_system_operations_batch_unique UNIQUE (batch_id),
    CONSTRAINT game_system_operations_batch_fk
        FOREIGN KEY (batch_id, session_id, operation_id, source_event_id, logical_digest)
        REFERENCES game_session_event_batches (
            batch_id, session_id, system_operation_id, system_source_event_id, system_request_digest
        )
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT game_system_operations_operation_id_shape CHECK (
        length(operation_id) BETWEEN 22 AND 86 AND operation_id ~ '^[A-Za-z0-9_-]+$'
    ),
    CONSTRAINT game_system_operations_result_code_shape CHECK (
        result_code IS NULL
        OR (length(result_code) <= 64 AND result_code ~ '^[a-z0-9]+([._-][a-z0-9]+)*$')
    ),
    CONSTRAINT game_system_operations_source_shape CHECK (
        (source_kind = 'host_api' AND requested_by_user_id IS NOT NULL)
        OR (source_kind IN ('room_outbox', 'platform') AND requested_by_user_id IS NULL)
    ),
    CONSTRAINT game_system_operations_state_check CHECK (
        (
            status = 'pending'
            AND result_code IS NULL
            AND result_digest IS NULL
            AND committed_state_version IS NULL
            AND batch_id IS NULL
            AND completed_at IS NULL
        )
        OR
        (
            status = 'completed'
            AND result_code IS NOT NULL
            AND result_digest IS NOT NULL
            AND committed_state_version IS NOT NULL
            AND completed_at IS NOT NULL
        )
    )
);

CREATE UNIQUE INDEX game_system_operations_source_idx
    ON game_system_operations (session_id, source_event_id);

-- PartyRoom and GameSession form one lifecycle invariant. Deferred checks allow start and finish
-- transactions to update both sides in either statement order while rejecting half-commits.
-- +goose StatementBegin
CREATE FUNCTION enforce_party_room_game_session_lifecycle()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    affected_session_ids uuid[] := ARRAY[]::uuid[];
    affected_room_ids uuid[] := ARRAY[]::uuid[];
    invalid_state boolean;
BEGIN
    IF TG_TABLE_NAME = 'game_sessions' THEN
        IF TG_OP <> 'DELETE' THEN
            affected_session_ids := array_append(affected_session_ids, NEW.session_id);
            affected_room_ids := array_append(affected_room_ids, NEW.room_id);
        END IF;
        IF TG_OP <> 'INSERT' THEN
            affected_session_ids := array_append(affected_session_ids, OLD.session_id);
            affected_room_ids := array_append(affected_room_ids, OLD.room_id);
        END IF;
    ELSIF TG_TABLE_NAME = 'party_rooms' THEN
        IF TG_OP <> 'DELETE' THEN
            affected_room_ids := array_append(affected_room_ids, NEW.room_id);
            IF NEW.active_session_id IS NOT NULL THEN
                affected_session_ids := array_append(affected_session_ids, NEW.active_session_id);
            END IF;
        END IF;
        IF TG_OP <> 'INSERT' THEN
            affected_room_ids := array_append(affected_room_ids, OLD.room_id);
            IF OLD.active_session_id IS NOT NULL THEN
                affected_session_ids := array_append(affected_session_ids, OLD.active_session_id);
            END IF;
        END IF;
    ELSE
        RAISE EXCEPTION 'unsupported lifecycle trigger table %', TG_TABLE_NAME;
    END IF;

    EXECUTE format(
        $query$
        SELECT
            EXISTS (
                SELECT 1
                FROM %1$I.game_sessions AS session
                LEFT JOIN %1$I.party_rooms AS room
                  ON room.active_session_id = session.session_id
                 AND room.room_id = session.room_id
                 AND room.active_game_id = session.game_id
                 AND room.status = 'playing'
                WHERE session.status IN ('active', 'suspended')
                  AND (session.session_id = ANY($1) OR session.room_id = ANY($2))
                  AND room.room_id IS NULL
            )
            OR EXISTS (
                SELECT 1
                FROM %1$I.party_rooms AS room
                LEFT JOIN %1$I.game_sessions AS session
                  ON session.session_id = room.active_session_id
                 AND session.room_id = room.room_id
                 AND session.game_id = room.active_game_id
                 AND session.status IN ('active', 'suspended')
                WHERE room.status = 'playing'
                  AND (room.room_id = ANY($2) OR room.active_session_id = ANY($1))
                  AND session.session_id IS NULL
            )
            OR EXISTS (
                SELECT 1
                FROM %1$I.party_rooms AS room
                JOIN %1$I.game_sessions AS session
                  ON session.session_id = room.active_session_id
                WHERE session.status IN ('finished', 'cancelled')
                  AND (session.session_id = ANY($1) OR room.room_id = ANY($2))
            )
        $query$,
        TG_TABLE_SCHEMA
    ) INTO invalid_state USING affected_session_ids, affected_room_ids;

    IF invalid_state THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'party room and game session lifecycle invariant violated';
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER game_sessions_party_room_lifecycle
AFTER INSERT OR UPDATE OR DELETE ON game_sessions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_party_room_game_session_lifecycle();

CREATE CONSTRAINT TRIGGER party_rooms_game_session_lifecycle
AFTER INSERT OR UPDATE OR DELETE ON party_rooms
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_party_room_game_session_lifecycle();

-- +goose StatementBegin
DO $permissions$
DECLARE
    trusted_schema text := current_schema();
    owner_role text := current_setting('game_night.owner_role');
    runtime_role text := current_setting('game_night.runtime_role');
BEGIN
    IF trusted_schema IS NULL THEN
        RAISE EXCEPTION 'game session runtime migration requires an explicit current schema';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = owner_role)
       OR NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = runtime_role) THEN
        RAISE EXCEPTION 'game session runtime migration requires configured owner and runtime roles';
    END IF;

    EXECUTE format('ALTER TABLE %I.game_timer_receipts OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format('REVOKE ALL ON TABLE %I.game_timer_receipts FROM PUBLIC', trusted_schema);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE %I.game_timer_receipts TO %I', trusted_schema, runtime_role);
    EXECUTE format('ALTER TABLE %I.game_system_operations OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format('REVOKE ALL ON TABLE %I.game_system_operations FROM PUBLIC', trusted_schema);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE %I.game_system_operations TO %I', trusted_schema, runtime_role);
    EXECUTE format(
        'ALTER FUNCTION %I.enforce_party_room_game_session_lifecycle() OWNER TO %I',
        trusted_schema,
        owner_role
    );
    EXECUTE format(
        'REVOKE ALL ON FUNCTION %I.enforce_party_room_game_session_lifecycle() FROM PUBLIC',
        trusted_schema
    );
END;
$permissions$;
-- +goose StatementEnd

-- +goose Down

DROP TRIGGER IF EXISTS party_rooms_game_session_lifecycle ON party_rooms;
DROP TRIGGER IF EXISTS game_sessions_party_room_lifecycle ON game_sessions;
DROP FUNCTION IF EXISTS enforce_party_room_game_session_lifecycle();
DROP TABLE IF EXISTS game_system_operations;
DROP TABLE IF EXISTS game_timer_receipts;
ALTER TABLE game_session_event_batches
    DROP CONSTRAINT IF EXISTS game_session_event_batches_system_identity_unique,
    DROP CONSTRAINT IF EXISTS game_session_event_batches_batch_session_unique,
    DROP CONSTRAINT IF EXISTS game_session_event_batches_runtime_source_shape,
    DROP CONSTRAINT IF EXISTS game_session_event_batches_timer_id_shape,
    DROP CONSTRAINT IF EXISTS game_session_event_batches_system_operation_shape,
    DROP CONSTRAINT IF EXISTS game_session_event_batches_system_source_shape,
    DROP CONSTRAINT IF EXISTS game_session_event_batches_system_requester_shape,
    DROP COLUMN IF EXISTS timer_id,
    DROP COLUMN IF EXISTS system_operation_id,
    DROP COLUMN IF EXISTS system_source_kind,
    DROP COLUMN IF EXISTS system_source_event_id,
    DROP COLUMN IF EXISTS system_requested_by_user_id,
    DROP COLUMN IF EXISTS system_request_digest;
