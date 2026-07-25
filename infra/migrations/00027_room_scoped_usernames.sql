-- +goose Up

-- A display name identifies a player inside a room, not across the whole platform.
-- Username claims remain per-user history so cooldown and administrative rename flows keep their audit semantics.
ALTER TABLE users DROP CONSTRAINT users_current_username_claim_fk;
DROP TRIGGER users_username_claim_invariants ON users;
DROP TRIGGER username_claims_user_invariants ON username_claims;
DROP FUNCTION enforce_username_claim_invariants();

ALTER TABLE username_claims DROP CONSTRAINT username_claims_pkey;
ALTER TABLE username_claims DROP CONSTRAINT username_claims_username_key_owner_user_id_key;
ALTER TABLE username_claims
    ADD CONSTRAINT username_claims_pkey PRIMARY KEY (username_key, owner_user_id);

ALTER TABLE users
    ADD CONSTRAINT users_current_username_claim_fk
    FOREIGN KEY (current_username_key, user_id)
    REFERENCES username_claims (username_key, owner_user_id)
    DEFERRABLE INITIALLY DEFERRED;

-- +goose StatementBegin
CREATE FUNCTION enforce_username_claim_invariants()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    invalid_state boolean;
BEGIN
    EXECUTE format(
        $query$
        SELECT EXISTS (
            SELECT 1
            FROM %1$I.users AS u
            LEFT JOIN %1$I.username_claims AS c
              ON c.username_key = u.current_username_key
             AND c.owner_user_id = u.user_id
            WHERE (
                u.status IN ('active', 'suspended')
                AND (
                    c.username_key IS NULL
                    OR c.status <> 'active'
                    OR c.display_username <> u.username
                )
            ) OR (
                u.status IN ('onboarding', 'deleted')
                AND u.current_username_key IS NOT NULL
            )
            UNION ALL
            SELECT 1
            FROM %1$I.username_claims AS c
            LEFT JOIN %1$I.users AS u
              ON u.user_id = c.owner_user_id
             AND u.current_username_key = c.username_key
            WHERE c.status = 'active'
              AND (u.user_id IS NULL OR u.status NOT IN ('active', 'suspended'))
        )
        $query$,
        TG_TABLE_SCHEMA
    ) INTO invalid_state;

    IF invalid_state THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'username claim invariant violated';
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER users_username_claim_invariants
AFTER INSERT OR UPDATE OR DELETE ON users
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_username_claim_invariants();

CREATE CONSTRAINT TRIGGER username_claims_user_invariants
AFTER INSERT OR UPDATE OR DELETE ON username_claims
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_username_claim_invariants();

-- This pre-production migration assumes room_members is empty; development data is reset instead of backfilled.
-- Room membership owns the stable, viewer-independent name shown at that table.
ALTER TABLE room_members
    ADD COLUMN display_username text NOT NULL,
    ADD COLUMN username_key text NOT NULL,
    ADD CONSTRAINT room_members_display_username_nonempty CHECK (display_username <> ''),
    ADD CONSTRAINT room_members_username_key_nonempty CHECK (username_key <> ''),
    ADD CONSTRAINT room_members_username_key_unique UNIQUE (room_id, username_key);

-- The row lock serializes joining with a concurrent rename. Existing aliases are accepted only for deleted users,
-- allowing aggregate rewrites to preserve the last room-visible name after account deletion.
-- +goose StatementBegin
CREATE FUNCTION fill_room_member_username()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    current_display text;
    current_key text;
BEGIN
    EXECUTE format(
        'SELECT username, current_username_key FROM %I.users WHERE user_id = $1 AND status IN (''active'', ''suspended'') FOR KEY SHARE',
        TG_TABLE_SCHEMA
    ) INTO current_display, current_key USING NEW.user_id;

    IF current_key IS NOT NULL THEN
        NEW.display_username := current_display;
        NEW.username_key := current_key;
    ELSIF NEW.display_username IS NULL OR NEW.username_key IS NULL THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'room member requires a current or preserved username';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER room_members_fill_username
BEFORE INSERT ON room_members
FOR EACH ROW EXECUTE FUNCTION fill_room_member_username();

-- A global rename updates every current room alias in the same transaction. The room unique constraint rejects
-- a rename that would make two current members indistinguishable in any shared room.
-- +goose StatementBegin
CREATE FUNCTION propagate_user_room_username()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NEW.current_username_key IS NOT NULL
       AND (
           NEW.current_username_key IS DISTINCT FROM OLD.current_username_key
           OR NEW.username IS DISTINCT FROM OLD.username
       ) THEN
        EXECUTE format(
            $query$
            UPDATE %1$I.room_members AS member
            SET display_username = $1,
                username_key = $2
            FROM %1$I.party_rooms AS room
            WHERE member.user_id = $3
              AND room.room_id = member.room_id
              AND room.status <> 'closed'
            $query$,
            TG_TABLE_SCHEMA
        ) USING NEW.username, NEW.current_username_key, NEW.user_id;
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER users_propagate_room_username
AFTER UPDATE OF username, current_username_key ON users
FOR EACH ROW EXECUTE FUNCTION propagate_user_room_username();

-- +goose Down

-- The previous schema can represent only one owner for each username key. Fail before changing any schema objects
-- when room-scoped naming has produced data that cannot be represented by that global-uniqueness model.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM username_claims
        GROUP BY username_key
        HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23505',
            MESSAGE = 'cannot roll back room-scoped usernames while duplicate username claims exist';
    END IF;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER users_propagate_room_username ON users;
DROP FUNCTION propagate_user_room_username();
DROP TRIGGER room_members_fill_username ON room_members;
DROP FUNCTION fill_room_member_username();

ALTER TABLE room_members
    DROP CONSTRAINT room_members_username_key_unique,
    DROP CONSTRAINT room_members_username_key_nonempty,
    DROP CONSTRAINT room_members_display_username_nonempty,
    DROP COLUMN username_key,
    DROP COLUMN display_username;

ALTER TABLE users DROP CONSTRAINT users_current_username_claim_fk;
DROP TRIGGER users_username_claim_invariants ON users;
DROP TRIGGER username_claims_user_invariants ON username_claims;
DROP FUNCTION enforce_username_claim_invariants();

ALTER TABLE username_claims DROP CONSTRAINT username_claims_pkey;
ALTER TABLE username_claims
    ADD CONSTRAINT username_claims_pkey PRIMARY KEY (username_key),
    ADD CONSTRAINT username_claims_username_key_owner_user_id_key UNIQUE (username_key, owner_user_id);

ALTER TABLE users
    ADD CONSTRAINT users_current_username_claim_fk
    FOREIGN KEY (current_username_key)
    REFERENCES username_claims (username_key)
    DEFERRABLE INITIALLY DEFERRED;

-- +goose StatementBegin
CREATE FUNCTION enforce_username_claim_invariants()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    invalid_state boolean;
BEGIN
    EXECUTE format(
        $query$
        SELECT EXISTS (
            SELECT 1
            FROM %1$I.users AS u
            LEFT JOIN %1$I.username_claims AS c
              ON c.username_key = u.current_username_key
            WHERE (
                u.status IN ('active', 'suspended')
                AND (
                    c.username_key IS NULL
                    OR c.owner_user_id <> u.user_id
                    OR c.status <> 'active'
                    OR c.display_username <> u.username
                )
            ) OR (
                u.status IN ('onboarding', 'deleted')
                AND u.current_username_key IS NOT NULL
            )
            UNION ALL
            SELECT 1
            FROM %1$I.username_claims AS c
            LEFT JOIN %1$I.users AS u
              ON u.user_id = c.owner_user_id
             AND u.current_username_key = c.username_key
            WHERE c.status = 'active'
              AND (u.user_id IS NULL OR u.status NOT IN ('active', 'suspended'))
        )
        $query$,
        TG_TABLE_SCHEMA
    ) INTO invalid_state;

    IF invalid_state THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'username claim invariant violated';
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER users_username_claim_invariants
AFTER INSERT OR UPDATE OR DELETE ON users
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_username_claim_invariants();

CREATE CONSTRAINT TRIGGER username_claims_user_invariants
AFTER INSERT OR UPDATE OR DELETE ON username_claims
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_username_claim_invariants();
