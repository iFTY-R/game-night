-- +goose Up

CREATE TABLE party_rooms (
    room_id uuid PRIMARY KEY,
    room_code text NOT NULL,
    visibility text NOT NULL CHECK (visibility IN ('private', 'public')),
    status text NOT NULL CHECK (status IN ('lobby', 'playing', 'closed')),
    host_user_id uuid NOT NULL REFERENCES users (user_id),
    participant_capacity integer NOT NULL CHECK (participant_capacity > 0),
    participant_admission text NOT NULL CHECK (participant_admission IN ('open', 'approval', 'closed')),
    spectator_admission text NOT NULL CHECK (spectator_admission IN ('open', 'approval', 'closed')),
    active_session_id uuid,
    active_game_id text,
    room_version bigint NOT NULL CHECK (room_version > 0),
    membership_version bigint NOT NULL CHECK (membership_version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT party_rooms_room_code_unique UNIQUE (room_code),
    CONSTRAINT party_rooms_room_code_shape CHECK (room_code ~ '^[A-Z0-9]{4,16}$'),
    CONSTRAINT party_rooms_time_invariant CHECK (updated_at >= created_at),
    CONSTRAINT party_rooms_session_invariant CHECK (
        (
            status = 'playing'
            AND active_session_id IS NOT NULL
            AND active_game_id IS NOT NULL
            AND active_game_id <> ''
            AND participant_admission = 'closed'
        )
        OR (
            status IN ('lobby', 'closed')
            AND active_session_id IS NULL
            AND active_game_id IS NULL
        )
    ),
    CONSTRAINT party_rooms_closed_invariant CHECK (
        status <> 'closed'
        OR (participant_admission = 'closed' AND spectator_admission = 'closed')
    )
);

CREATE TABLE room_members (
    room_id uuid NOT NULL REFERENCES party_rooms (room_id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users (user_id),
    role text NOT NULL CHECK (role IN ('participant', 'spectator', 'waiting')),
    requested_role text CHECK (requested_role IN ('participant', 'spectator')),
    seat_index integer,
    joined_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    PRIMARY KEY (room_id, user_id),
    CONSTRAINT room_members_role_shape CHECK (
        (role = 'participant' AND requested_role IS NULL AND seat_index IS NOT NULL AND seat_index >= 0)
        OR (role = 'spectator' AND requested_role IS NULL AND seat_index IS NULL)
        OR (role = 'waiting' AND requested_role IS NOT NULL AND seat_index IS NULL)
    ),
    CONSTRAINT room_members_time_invariant CHECK (last_seen_at >= joined_at)
);

CREATE UNIQUE INDEX room_members_participant_seat_unique
    ON room_members (room_id, seat_index)
    WHERE role = 'participant';
CREATE INDEX room_members_user_rooms_idx ON room_members (user_id, joined_at DESC, room_id);
CREATE INDEX room_members_waiting_idx ON room_members (room_id, joined_at, user_id) WHERE role = 'waiting';
CREATE INDEX party_rooms_public_lobby_idx
    ON party_rooms (updated_at DESC, room_id)
    WHERE visibility = 'public' AND status <> 'closed';

-- The deferred cycle permits one atomic room+host insert while rejecting ownerless rooms at commit.
ALTER TABLE party_rooms
    ADD CONSTRAINT party_rooms_host_member_fk
    FOREIGN KEY (room_id, host_user_id)
    REFERENCES room_members (room_id, user_id)
    DEFERRABLE INITIALLY DEFERRED;

-- Cross-table invariants are deferred so full aggregate replacement can delete and reinsert members atomically.
-- +goose StatementBegin
CREATE FUNCTION enforce_party_room_member_invariants()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    target_room_id uuid := COALESCE(NEW.room_id, OLD.room_id);
    invalid_state boolean;
BEGIN
    EXECUTE format(
        $query$
        SELECT EXISTS (
            SELECT 1
            FROM %1$I.party_rooms AS room
            LEFT JOIN %1$I.room_members AS host
              ON host.room_id = room.room_id
             AND host.user_id = room.host_user_id
            WHERE room.room_id = $1
              AND (
                  host.user_id IS NULL
                  OR host.role <> 'participant'
                  OR EXISTS (
                      SELECT 1
                      FROM %1$I.room_members AS member
                      WHERE member.room_id = room.room_id
                        AND (
                            (member.role = 'participant' AND member.seat_index >= room.participant_capacity)
                            OR member.joined_at < room.created_at
                            OR member.last_seen_at > room.updated_at
                        )
                  )
                  OR (
                      SELECT count(*)
                      FROM %1$I.room_members AS participant
                      WHERE participant.room_id = room.room_id
                        AND participant.role = 'participant'
                  ) > room.participant_capacity
              )
        )
        $query$,
        TG_TABLE_SCHEMA
    ) INTO invalid_state USING target_room_id;

    IF invalid_state THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'party room member invariant violated';
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER party_rooms_member_invariants
AFTER INSERT OR UPDATE OR DELETE ON party_rooms
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_party_room_member_invariants();

CREATE CONSTRAINT TRIGGER room_members_party_room_invariants
AFTER INSERT OR UPDATE OR DELETE ON room_members
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_party_room_member_invariants();

-- New objects are created after the original permission migration, so ownership and grants are explicit here.
-- +goose StatementBegin
DO $permissions$
DECLARE
    trusted_schema text := current_schema();
    owner_role text := current_setting('game_night.owner_role');
    runtime_role text := current_setting('game_night.runtime_role');
BEGIN
    IF trusted_schema IS NULL THEN
        RAISE EXCEPTION 'room migration requires an explicit current schema';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = owner_role)
       OR NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = runtime_role) THEN
        RAISE EXCEPTION 'room migration requires configured owner and runtime roles';
    END IF;

    EXECUTE format('ALTER TABLE %I.party_rooms OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format('ALTER TABLE %I.room_members OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format('ALTER FUNCTION %I.enforce_party_room_member_invariants() OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format('REVOKE ALL ON TABLE %I.party_rooms, %I.room_members FROM PUBLIC', trusted_schema, trusted_schema);
    EXECUTE format('REVOKE ALL ON FUNCTION %I.enforce_party_room_member_invariants() FROM PUBLIC', trusted_schema);
    EXECUTE format(
        'GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE %I.party_rooms, %I.room_members TO %I',
        trusted_schema,
        trusted_schema,
        runtime_role
    );
END;
$permissions$;
-- +goose StatementEnd

-- +goose Down

ALTER TABLE IF EXISTS party_rooms DROP CONSTRAINT IF EXISTS party_rooms_host_member_fk;
DROP TABLE IF EXISTS room_members;
DROP TABLE IF EXISTS party_rooms;
DROP FUNCTION IF EXISTS enforce_party_room_member_invariants();
