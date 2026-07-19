-- +goose Up

CREATE TABLE game_sessions (
    session_id uuid PRIMARY KEY,
    room_id uuid NOT NULL REFERENCES party_rooms (room_id),
    game_id text NOT NULL,
    engine_version text NOT NULL,
    protocol_version text NOT NULL,
    client_version text NOT NULL,
    state_version bigint NOT NULL CHECK (state_version > 0),
    ownership_epoch bigint NOT NULL CHECK (ownership_epoch >= 0),
    snapshot_version integer NOT NULL CHECK (snapshot_version > 0),
    state_message_type text NOT NULL,
    state_schema_version integer NOT NULL CHECK (state_schema_version > 0),
    state_payload bytea NOT NULL CHECK (octet_length(state_payload) <= 1048576),
    next_deadline_at timestamptz,
    status text NOT NULL CHECK (status IN ('active', 'suspended', 'finished', 'cancelled')),
    started_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    ended_at timestamptz,
    CONSTRAINT game_sessions_game_id_shape CHECK (
        length(game_id) <= 64 AND game_id ~ '^[a-z0-9]+([._-][a-z0-9]+)*$'
    ),
    CONSTRAINT game_sessions_engine_version_shape CHECK (
        length(engine_version) <= 64
        AND engine_version ~ '^(0|[1-9][0-9]*)[.](0|[1-9][0-9]*)[.](0|[1-9][0-9]*)(-[0-9A-Za-z-]+([.][0-9A-Za-z-]+)*)?([+][0-9A-Za-z-]+([.][0-9A-Za-z-]+)*)?$'
    ),
    CONSTRAINT game_sessions_protocol_version_shape CHECK (
        length(protocol_version) <= 64
        AND protocol_version ~ '^(0|[1-9][0-9]*)[.](0|[1-9][0-9]*)[.](0|[1-9][0-9]*)(-[0-9A-Za-z-]+([.][0-9A-Za-z-]+)*)?([+][0-9A-Za-z-]+([.][0-9A-Za-z-]+)*)?$'
    ),
    CONSTRAINT game_sessions_client_version_shape CHECK (
        length(client_version) <= 64
        AND client_version ~ '^(0|[1-9][0-9]*)[.](0|[1-9][0-9]*)[.](0|[1-9][0-9]*)(-[0-9A-Za-z-]+([.][0-9A-Za-z-]+)*)?([+][0-9A-Za-z-]+([.][0-9A-Za-z-]+)*)?$'
    ),
    CONSTRAINT game_sessions_state_message_shape CHECK (
        length(state_message_type) <= 64 AND state_message_type ~ '^[a-z0-9]+([._-][a-z0-9]+)*$'
    ),
    CONSTRAINT game_sessions_time_invariant CHECK (
        updated_at >= started_at
        AND (ended_at IS NULL OR ended_at = updated_at)
    ),
    CONSTRAINT game_sessions_status_invariant CHECK (
        (status IN ('active', 'suspended') AND ended_at IS NULL)
        OR (status IN ('finished', 'cancelled') AND ended_at IS NOT NULL AND next_deadline_at IS NULL)
    )
);

CREATE UNIQUE INDEX game_sessions_one_live_per_room_idx
    ON game_sessions (room_id)
    WHERE status IN ('active', 'suspended');

CREATE TABLE game_session_participants (
    session_id uuid NOT NULL REFERENCES game_sessions (session_id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users (user_id),
    seat_index integer NOT NULL CHECK (seat_index >= 0),
    PRIMARY KEY (session_id, user_id),
    CONSTRAINT game_session_participants_seat_unique UNIQUE (session_id, seat_index)
);

CREATE TABLE game_session_timers (
    session_id uuid NOT NULL REFERENCES game_sessions (session_id) ON DELETE CASCADE,
    timer_id text NOT NULL,
    expected_state_version bigint NOT NULL CHECK (expected_state_version > 0),
    due_at timestamptz NOT NULL,
    message_type text NOT NULL,
    schema_version integer NOT NULL CHECK (schema_version > 0),
    payload bytea NOT NULL CHECK (octet_length(payload) <= 1048576),
    PRIMARY KEY (session_id, timer_id),
    CONSTRAINT game_session_timers_id_shape CHECK (
        length(timer_id) <= 64 AND timer_id ~ '^[a-z0-9]+([._-][a-z0-9]+)*$'
    ),
    CONSTRAINT game_session_timers_message_shape CHECK (
        length(message_type) <= 64 AND message_type ~ '^[a-z0-9]+([._-][a-z0-9]+)*$'
    )
);

CREATE INDEX game_session_timers_due_idx ON game_session_timers (due_at, session_id, timer_id);

CREATE TABLE game_session_event_batches (
    batch_id uuid PRIMARY KEY,
    session_id uuid NOT NULL REFERENCES game_sessions (session_id) ON DELETE CASCADE,
    state_version bigint NOT NULL CHECK (state_version > 0),
    ownership_epoch bigint NOT NULL CHECK (ownership_epoch >= 0),
    cause text NOT NULL CHECK (cause IN ('created', 'action', 'timer', 'system')),
    actor_user_id uuid REFERENCES users (user_id),
    action_id text,
    executed_at timestamptz NOT NULL,
    random_seed bytea NOT NULL CHECK (octet_length(random_seed) = 32),
    allocated_ids text[] NOT NULL CHECK (cardinality(allocated_ids) <= 256),
    input_message_type text NOT NULL,
    input_schema_version integer NOT NULL CHECK (input_schema_version > 0),
    input_payload bytea NOT NULL CHECK (octet_length(input_payload) <= 1048576),
    event_count integer NOT NULL CHECK (event_count > 0 AND event_count <= 1024),
    committed_at timestamptz NOT NULL,
    CONSTRAINT game_session_event_batches_version_unique UNIQUE (session_id, state_version),
    CONSTRAINT game_session_event_batches_action_unique UNIQUE (session_id, actor_user_id, action_id),
    CONSTRAINT game_session_event_batches_receipt_unique UNIQUE (
        session_id, actor_user_id, action_id, state_version, committed_at
    ),
    CONSTRAINT game_session_event_batches_actor_participant_fk
        FOREIGN KEY (session_id, actor_user_id)
        REFERENCES game_session_participants (session_id, user_id)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT game_session_event_batches_actor_shape CHECK (
        (cause = 'created' AND ownership_epoch = 0 AND actor_user_id IS NULL AND action_id IS NULL)
        OR (cause = 'action' AND ownership_epoch > 0 AND actor_user_id IS NOT NULL AND action_id IS NOT NULL)
        OR (cause IN ('timer', 'system') AND ownership_epoch > 0 AND actor_user_id IS NULL AND action_id IS NULL)
    ),
    CONSTRAINT game_session_event_batches_action_id_shape CHECK (
        action_id IS NULL
        OR (length(action_id) BETWEEN 22 AND 86 AND action_id ~ '^[A-Za-z0-9_-]+$')
    ),
    CONSTRAINT game_session_event_batches_input_shape CHECK (
        length(input_message_type) <= 64 AND input_message_type ~ '^[a-z0-9]+([._-][a-z0-9]+)*$'
    ),
    CONSTRAINT game_session_event_batches_time_invariant CHECK (executed_at = committed_at)
);

CREATE TABLE game_session_events (
    batch_id uuid NOT NULL REFERENCES game_session_event_batches (batch_id) ON DELETE CASCADE,
    event_ordinal integer NOT NULL CHECK (event_ordinal >= 0),
    message_type text NOT NULL,
    schema_version integer NOT NULL CHECK (schema_version > 0),
    payload bytea NOT NULL CHECK (octet_length(payload) <= 1048576),
    PRIMARY KEY (batch_id, event_ordinal),
    CONSTRAINT game_session_events_message_shape CHECK (
        length(message_type) <= 64 AND message_type ~ '^[a-z0-9]+([._-][a-z0-9]+)*$'
    )
);

CREATE TABLE game_action_receipts (
    session_id uuid NOT NULL,
    actor_user_id uuid NOT NULL,
    action_id text NOT NULL,
    request_digest bytea NOT NULL CHECK (octet_length(request_digest) = 32),
    result_code text NOT NULL,
    result_digest bytea NOT NULL CHECK (octet_length(result_digest) = 32),
    committed_state_version bigint NOT NULL CHECK (committed_state_version > 0),
    committed_at timestamptz NOT NULL,
    PRIMARY KEY (session_id, actor_user_id, action_id),
    CONSTRAINT game_action_receipts_batch_fk
        FOREIGN KEY (session_id, actor_user_id, action_id, committed_state_version, committed_at)
        REFERENCES game_session_event_batches (
            session_id, actor_user_id, action_id, state_version, committed_at
        )
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT game_action_receipts_action_id_shape CHECK (
        length(action_id) BETWEEN 22 AND 86 AND action_id ~ '^[A-Za-z0-9_-]+$'
    ),
    CONSTRAINT game_action_receipts_result_code_shape CHECK (
        length(result_code) <= 64 AND result_code ~ '^[a-z0-9]+([._-][a-z0-9]+)*$'
    )
);

-- Deferred validation permits one transaction to replace the complete timer set while preventing
-- direct database writers from bypassing the runtime's state-version, count, and deadline invariants.
-- +goose StatementBegin
CREATE FUNCTION enforce_game_session_timer_invariants()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    target_session_id uuid := COALESCE(NEW.session_id, OLD.session_id);
    invalid_state boolean;
BEGIN
    EXECUTE format(
        $query$
        SELECT EXISTS (
            SELECT 1
            FROM %1$I.game_sessions AS session
            WHERE session.session_id = $1
              AND (
                  session.next_deadline_at IS DISTINCT FROM (
                      SELECT min(timer.due_at)
                      FROM %1$I.game_session_timers AS timer
                      WHERE timer.session_id = session.session_id
                  )
                  OR EXISTS (
                      SELECT 1
                      FROM %1$I.game_session_timers AS timer
                      WHERE timer.session_id = session.session_id
                        AND timer.expected_state_version <> session.state_version
                  )
                  OR (
                      SELECT count(*)
                      FROM %1$I.game_session_timers AS timer
                      WHERE timer.session_id = session.session_id
                  ) > 64
              )
        )
        $query$,
        TG_TABLE_SCHEMA
    ) INTO invalid_state USING target_session_id;

    IF invalid_state THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'game session timer invariant violated';
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER game_sessions_timer_invariants
AFTER INSERT OR UPDATE OR DELETE ON game_sessions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_game_session_timer_invariants();

CREATE CONSTRAINT TRIGGER game_session_timers_session_invariants
AFTER INSERT OR UPDATE OR DELETE ON game_session_timers
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_game_session_timer_invariants();

-- Event rows remain normalized for ordered replay, so a deferred trigger checks the declared batch size
-- only after the transaction has inserted the complete batch.
-- +goose StatementBegin
CREATE FUNCTION enforce_game_session_event_batch_invariants()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    target_batch_id uuid := COALESCE(NEW.batch_id, OLD.batch_id);
    invalid_state boolean;
BEGIN
    EXECUTE format(
        $query$
        SELECT EXISTS (
            SELECT 1
            FROM %1$I.game_session_event_batches AS batch
            WHERE batch.batch_id = $1
              AND (
                  batch.state_version > (
                      SELECT session.state_version
                      FROM %1$I.game_sessions AS session
                      WHERE session.session_id = batch.session_id
                  )
                  OR batch.event_count <> (
                      SELECT count(*)
                      FROM %1$I.game_session_events AS event
                      WHERE event.batch_id = batch.batch_id
                  )
                  OR 0 <> (
                      SELECT min(event.event_ordinal)
                      FROM %1$I.game_session_events AS event
                      WHERE event.batch_id = batch.batch_id
                  )
                  OR batch.event_count - 1 <> (
                      SELECT max(event.event_ordinal)
                      FROM %1$I.game_session_events AS event
                      WHERE event.batch_id = batch.batch_id
                  )
              )
        )
        $query$,
        TG_TABLE_SCHEMA
    ) INTO invalid_state USING target_batch_id;

    IF invalid_state THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'game session event batch invariant violated';
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER game_session_event_batches_event_invariants
AFTER INSERT OR UPDATE OR DELETE ON game_session_event_batches
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_game_session_event_batch_invariants();

CREATE CONSTRAINT TRIGGER game_session_events_batch_invariants
AFTER INSERT OR UPDATE OR DELETE ON game_session_events
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_game_session_event_batch_invariants();

-- These tables postdate the base permission migration, so runtime ownership and grants are explicit.
-- +goose StatementBegin
DO $permissions$
DECLARE
    trusted_schema text := current_schema();
    owner_role text := current_setting('game_night.owner_role');
    runtime_role text := current_setting('game_night.runtime_role');
    table_name text;
BEGIN
    IF trusted_schema IS NULL THEN
        RAISE EXCEPTION 'game session migration requires an explicit current schema';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = owner_role)
       OR NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = runtime_role) THEN
        RAISE EXCEPTION 'game session migration requires configured owner and runtime roles';
    END IF;

    FOREACH table_name IN ARRAY ARRAY[
        'game_sessions',
        'game_session_participants',
        'game_session_timers',
        'game_session_event_batches',
        'game_session_events',
        'game_action_receipts'
    ] LOOP
        EXECUTE format('ALTER TABLE %I.%I OWNER TO %I', trusted_schema, table_name, owner_role);
        EXECUTE format('REVOKE ALL ON TABLE %I.%I FROM PUBLIC', trusted_schema, table_name);
        EXECUTE format(
            'GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE %I.%I TO %I',
            trusted_schema,
            table_name,
            runtime_role
        );
    END LOOP;
    EXECUTE format('ALTER FUNCTION %I.enforce_game_session_timer_invariants() OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format('REVOKE ALL ON FUNCTION %I.enforce_game_session_timer_invariants() FROM PUBLIC', trusted_schema);
    EXECUTE format('ALTER FUNCTION %I.enforce_game_session_event_batch_invariants() OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format('REVOKE ALL ON FUNCTION %I.enforce_game_session_event_batch_invariants() FROM PUBLIC', trusted_schema);
END;
$permissions$;
-- +goose StatementEnd

-- +goose Down

DROP TABLE IF EXISTS game_action_receipts;
DROP TABLE IF EXISTS game_session_events;
DROP TABLE IF EXISTS game_session_event_batches;
DROP TABLE IF EXISTS game_session_timers;
DROP TABLE IF EXISTS game_session_participants;
DROP TABLE IF EXISTS game_sessions;
DROP FUNCTION IF EXISTS enforce_game_session_timer_invariants();
DROP FUNCTION IF EXISTS enforce_game_session_event_batch_invariants();
