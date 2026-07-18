-- sqlc reads declarations but does not execute the dynamic, schema-qualified function DDL in migrations.
CREATE FUNCTION read_audit_head(requested_chain_id text)
RETURNS TABLE(sequence bigint, head_hash bytea)
LANGUAGE sql
AS $$
    SELECT 0::bigint, decode(repeat('00', 32), 'hex')
$$;

CREATE FUNCTION append_audit_event(
    requested_chain_id text,
    expected_previous_hash bytea,
    new_event_id uuid,
    new_canonical_event bytea,
    new_signature bytea,
    new_signing_key_version integer,
    new_created_at timestamptz
)
RETURNS TABLE(appended_sequence bigint, appended_hash bytea)
LANGUAGE sql
AS $$
    SELECT 0::bigint, decode(repeat('00', 32), 'hex')
$$;

CREATE FUNCTION reset_admin_account(
    expected_previous_hash bytea,
    new_event_id uuid,
    new_canonical_event bytea,
    new_signature bytea,
    new_signing_key_version integer,
    new_created_at timestamptz,
    new_password_hash text,
    new_password_algorithm text,
    new_password_parameters text,
    checkpoint_event_id uuid,
    checkpoint_payload bytea
)
RETURNS TABLE(appended_sequence bigint, appended_hash bytea)
LANGUAGE sql
AS $$
    SELECT 0::bigint, decode(repeat('00', 32), 'hex')
$$;
