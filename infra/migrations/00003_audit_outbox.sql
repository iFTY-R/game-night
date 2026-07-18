-- +goose Up

CREATE TABLE audit_chain_head (
    chain_id text PRIMARY KEY,
    sequence bigint NOT NULL CHECK (sequence >= 0),
    head_hash bytea NOT NULL CHECK (octet_length(head_hash) = 32),
    updated_at timestamptz NOT NULL
);

INSERT INTO audit_chain_head (chain_id, sequence, head_hash, updated_at)
VALUES ('admin', 0, decode(repeat('00', 32), 'hex'), transaction_timestamp());

CREATE TABLE audit_events (
    chain_id text NOT NULL REFERENCES audit_chain_head (chain_id),
    sequence bigint NOT NULL CHECK (sequence > 0),
    event_id uuid NOT NULL UNIQUE,
    previous_hash bytea NOT NULL CHECK (octet_length(previous_hash) = 32),
    canonical_event bytea NOT NULL,
    event_hash bytea NOT NULL CHECK (octet_length(event_hash) = 32),
    signature bytea NOT NULL CHECK (octet_length(signature) = 64),
    signing_key_version integer NOT NULL CHECK (signing_key_version > 0),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (chain_id, sequence)
);

CREATE INDEX audit_events_created_idx ON audit_events (created_at, chain_id, sequence);

-- SECURITY DEFINER functions are generated with the migration's quoted trusted schema embedded in every object reference.
-- +goose StatementBegin
DO $outer$
DECLARE
    trusted_schema text := current_schema();
BEGIN
    IF trusted_schema IS NULL THEN
        RAISE EXCEPTION 'audit migration requires an explicit current schema';
    END IF;

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

    EXECUTE format(
        $ddl$
        CREATE FUNCTION %1$I.append_audit_event(
            requested_chain_id text,
            expected_previous_hash bytea,
            new_event_id uuid,
            new_canonical_event bytea,
            new_signature bytea,
            new_signing_key_version integer,
            new_created_at timestamptz
        )
        RETURNS TABLE(appended_sequence bigint, appended_hash bytea)
        LANGUAGE plpgsql
        SECURITY DEFINER
        SET search_path = pg_catalog, %1$I, pg_temp
        AS $function$
        DECLARE
            current_sequence bigint;
            current_hash bytea;
            calculated_hash bytea;
        BEGIN
            IF expected_previous_hash IS NULL
               OR new_event_id IS NULL
               OR new_canonical_event IS NULL
               OR new_signature IS NULL
               OR new_created_at IS NULL
               OR octet_length(expected_previous_hash) <> 32
               OR octet_length(new_signature) <> 64
               OR new_signing_key_version <= 0 THEN
                RAISE EXCEPTION USING ERRCODE = '22023', MESSAGE = 'invalid audit append input';
            END IF;

            SELECT head.sequence, head.head_hash
            INTO current_sequence, current_hash
            FROM %1$I.audit_chain_head AS head
            WHERE head.chain_id = requested_chain_id
            FOR UPDATE;

            IF NOT FOUND THEN
                RAISE EXCEPTION USING ERRCODE = '23503', MESSAGE = 'audit chain does not exist';
            END IF;
            IF current_hash IS DISTINCT FROM expected_previous_hash THEN
                RAISE EXCEPTION USING ERRCODE = '40001', MESSAGE = 'audit head changed';
            END IF;

            calculated_hash := sha256(new_canonical_event);
            current_sequence := current_sequence + 1;

            INSERT INTO %1$I.audit_events (
                chain_id,
                sequence,
                event_id,
                previous_hash,
                canonical_event,
                event_hash,
                signature,
                signing_key_version,
                created_at
            ) VALUES (
                requested_chain_id,
                current_sequence,
                new_event_id,
                expected_previous_hash,
                new_canonical_event,
                calculated_hash,
                new_signature,
                new_signing_key_version,
                new_created_at
            );

            UPDATE %1$I.audit_chain_head
            SET sequence = current_sequence,
                head_hash = calculated_hash,
                updated_at = new_created_at
            WHERE chain_id = requested_chain_id;

            RETURN QUERY SELECT current_sequence, calculated_hash;
        END
        $function$
        $ddl$,
        trusted_schema
    );
END;
$outer$;
-- +goose StatementEnd

CREATE TABLE outbox_events (
    event_sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_id uuid NOT NULL UNIQUE,
    event_type text NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id uuid NOT NULL,
    payload bytea NOT NULL,
    created_at timestamptz NOT NULL,
    available_at timestamptz NOT NULL
);

CREATE INDEX outbox_events_dispatch_idx ON outbox_events (available_at, event_sequence);
CREATE INDEX outbox_events_type_sequence_idx ON outbox_events (event_type, event_sequence);

CREATE TABLE outbox_consumers (
    consumer_id text PRIMARY KEY,
    subscriptions text[] NOT NULL CHECK (cardinality(subscriptions) > 0),
    last_acked_sequence bigint NOT NULL DEFAULT 0 CHECK (last_acked_sequence >= 0),
    lease_owner text,
    lease_until timestamptz,
    retry_count integer NOT NULL DEFAULT 0 CHECK (retry_count >= 0),
    next_attempt_at timestamptz,
    last_error_code text,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (
        (lease_owner IS NULL AND lease_until IS NULL)
        OR (lease_owner IS NOT NULL AND lease_until IS NOT NULL)
    )
);

INSERT INTO outbox_consumers (
    consumer_id,
    subscriptions,
    created_at,
    updated_at
) VALUES (
    'audit.checkpoint',
    ARRAY['audit.checkpoint.pending'],
    transaction_timestamp(),
    transaction_timestamp()
);

-- +goose Down

DROP TABLE IF EXISTS outbox_consumers;
DROP TABLE IF EXISTS outbox_events;
DROP FUNCTION IF EXISTS append_audit_event(text, bytea, uuid, bytea, bytea, integer, timestamptz);
DROP FUNCTION IF EXISTS read_audit_head(text);
DROP TABLE IF EXISTS audit_events;
DROP TABLE IF EXISTS audit_chain_head;
