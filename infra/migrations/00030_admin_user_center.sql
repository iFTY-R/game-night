-- +goose Up

-- User versions are explicit because administrator commands must not use timestamps as CAS tokens.
ALTER TABLE users
    ADD COLUMN account_version bigint NOT NULL DEFAULT 1 CHECK (account_version > 0);

CREATE INDEX users_admin_status_created_idx
    ON users (status, created_at DESC, user_id DESC);
CREATE INDEX users_admin_username_idx
    ON users ((COALESCE(current_username_key, '')), user_id);
CREATE INDEX users_admin_activity_idx
    ON users (updated_at DESC, user_id DESC);
CREATE INDEX device_credentials_admin_activity_idx
    ON device_credentials (user_id, last_seen_at DESC, credential_id DESC);
CREATE INDEX room_members_admin_user_idx
    ON room_members (user_id, last_seen_at DESC, room_id DESC);
CREATE INDEX game_session_participants_admin_user_idx
    ON game_session_participants (user_id, session_id DESC);

-- A singleton catalog version lets list responses and tag-definition CAS share one monotonic boundary.
CREATE TABLE admin_user_tag_catalog (
    singleton_id smallint PRIMARY KEY DEFAULT 1 CHECK (singleton_id = 1),
    catalog_version bigint NOT NULL CHECK (catalog_version > 0),
    updated_at timestamptz NOT NULL
);

INSERT INTO admin_user_tag_catalog (singleton_id, catalog_version, updated_at)
VALUES (1, 1, pg_catalog.clock_timestamp());

CREATE TABLE admin_user_tags (
    tag_id uuid PRIMARY KEY,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
    normalized_name text NOT NULL CHECK (length(normalized_name) BETWEEN 1 AND 64),
    color text NOT NULL CHECK (color ~ '^#[0-9A-F]{6}$'),
    version bigint NOT NULL CHECK (version > 0),
    created_by_admin_id uuid NOT NULL REFERENCES admin_accounts (admin_id),
    updated_by_admin_id uuid NOT NULL REFERENCES admin_accounts (admin_id),
    reason text NOT NULL CHECK (length(reason) BETWEEN 1 AND 512),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT admin_user_tags_normalized_name_unique UNIQUE (normalized_name),
    CHECK (updated_at >= created_at)
);

CREATE INDEX admin_user_tags_name_idx
    ON admin_user_tags (normalized_name, tag_id);

CREATE TABLE admin_user_tag_links (
    user_id uuid NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
    tag_id uuid NOT NULL REFERENCES admin_user_tags (tag_id),
    version bigint NOT NULL CHECK (version > 0),
    assigned_by_admin_id uuid NOT NULL REFERENCES admin_accounts (admin_id),
    reason text NOT NULL CHECK (length(reason) BETWEEN 1 AND 512),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (user_id, tag_id),
    CHECK (updated_at >= created_at)
);

CREATE INDEX admin_user_tag_links_tag_idx
    ON admin_user_tag_links (tag_id, user_id);

CREATE TABLE admin_user_notes (
    note_id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
    author_admin_id uuid NOT NULL REFERENCES admin_accounts (admin_id),
    body text NOT NULL CHECK (length(body) BETWEEN 1 AND 4000),
    reason text NOT NULL CHECK (length(reason) BETWEEN 1 AND 512),
    version bigint NOT NULL DEFAULT 1 CHECK (version = 1),
    created_at timestamptz NOT NULL
);

CREATE INDEX admin_user_notes_user_created_idx
    ON admin_user_notes (user_id, created_at DESC, note_id DESC);

-- Notes are evidence-bearing history. They may only be appended through this schema.
-- +goose StatementBegin
CREATE FUNCTION reject_admin_user_note_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    RAISE EXCEPTION USING ERRCODE = '55000', MESSAGE = 'admin user notes are append-only';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER admin_user_notes_append_only
BEFORE UPDATE OR DELETE ON admin_user_notes
FOR EACH ROW EXECUTE FUNCTION reject_admin_user_note_mutation();

CREATE TABLE admin_batch_previews (
    preview_id uuid PRIMARY KEY,
    actor_admin_id uuid NOT NULL REFERENCES admin_accounts (admin_id),
    command text NOT NULL CHECK (command IN ('suspend', 'unsuspend', 'remove_from_current_room')),
    selection_schema_version integer NOT NULL CHECK (selection_schema_version > 0),
    selection_snapshot jsonb NOT NULL CHECK (jsonb_typeof(selection_snapshot) = 'object'),
    selection_digest bytea NOT NULL CHECK (octet_length(selection_digest) = 32),
    preview_digest bytea NOT NULL CHECK (octet_length(preview_digest) = 32),
    target_count bigint NOT NULL CHECK (target_count >= 0),
    executable_count bigint NOT NULL CHECK (executable_count >= 0),
    blocked_count bigint NOT NULL CHECK (blocked_count >= 0),
    sampled_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    version bigint NOT NULL CHECK (version > 0),
    CHECK (target_count = executable_count + blocked_count),
    CHECK (expires_at > sampled_at),
    CHECK (consumed_at IS NULL OR consumed_at BETWEEN sampled_at AND expires_at)
);

CREATE INDEX admin_batch_previews_expiry_idx
    ON admin_batch_previews (expires_at)
    WHERE consumed_at IS NULL;

CREATE TABLE admin_batch_jobs (
    batch_job_id uuid PRIMARY KEY,
    actor_admin_id uuid NOT NULL REFERENCES admin_accounts (admin_id),
    operation_id text NOT NULL CHECK (length(operation_id) BETWEEN 1 AND 128),
    request_digest bytea NOT NULL CHECK (octet_length(request_digest) = 32),
    preview_id uuid NOT NULL UNIQUE REFERENCES admin_batch_previews (preview_id),
    command text NOT NULL CHECK (command IN ('suspend', 'unsuspend', 'remove_from_current_room')),
    selection_schema_version integer NOT NULL CHECK (selection_schema_version > 0),
    selection_snapshot jsonb NOT NULL CHECK (jsonb_typeof(selection_snapshot) = 'object'),
    selection_digest bytea NOT NULL CHECK (octet_length(selection_digest) = 32),
    reason text NOT NULL CHECK (length(reason) BETWEEN 1 AND 512),
    state text NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'partially_succeeded', 'failed', 'canceling', 'canceled')),
    target_count bigint NOT NULL CHECK (target_count > 0),
    queued_count bigint NOT NULL CHECK (queued_count >= 0),
    running_count bigint NOT NULL CHECK (running_count >= 0),
    succeeded_count bigint NOT NULL CHECK (succeeded_count >= 0),
    failed_count bigint NOT NULL CHECK (failed_count >= 0),
    skipped_count bigint NOT NULL CHECK (skipped_count >= 0),
    canceled_count bigint NOT NULL CHECK (canceled_count >= 0),
    error_message_key text CHECK (error_message_key IS NULL OR (length(error_message_key) BETWEEN 1 AND 128 AND error_message_key ~ '^[a-z][a-z0-9]*([._-][a-z0-9]+)*$')),
    version bigint NOT NULL CHECK (version > 0),
    created_at timestamptz NOT NULL,
    started_at timestamptz,
    completed_at timestamptz,
    updated_at timestamptz NOT NULL,
    CONSTRAINT admin_batch_jobs_operation_unique UNIQUE (actor_admin_id, operation_id),
    CHECK (updated_at >= created_at),
    CHECK (started_at IS NULL OR started_at >= created_at),
    CHECK (completed_at IS NULL OR (started_at IS NOT NULL AND completed_at >= started_at)),
    CHECK (target_count = queued_count + running_count + succeeded_count + failed_count + skipped_count + canceled_count),
    CHECK ((state = 'queued' AND started_at IS NULL AND completed_at IS NULL)
        OR (state IN ('running', 'canceling') AND started_at IS NOT NULL AND completed_at IS NULL)
        OR (state IN ('succeeded', 'partially_succeeded', 'failed', 'canceled') AND started_at IS NOT NULL AND completed_at IS NOT NULL))
);

CREATE INDEX admin_batch_jobs_state_created_idx
    ON admin_batch_jobs (state, created_at, batch_job_id);

CREATE TABLE admin_batch_job_items (
    item_id uuid PRIMARY KEY,
    batch_job_id uuid NOT NULL REFERENCES admin_batch_jobs (batch_job_id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users (user_id),
    expected_user_version bigint NOT NULL CHECK (expected_user_version > 0),
    request_digest bytea NOT NULL CHECK (octet_length(request_digest) = 32),
    state text NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'skipped', 'canceled')),
    attempt_count integer NOT NULL CHECK (attempt_count >= 0),
    lease_owner text,
    lease_until timestamptz,
    error_message_key text CHECK (error_message_key IS NULL OR (length(error_message_key) BETWEEN 1 AND 128 AND error_message_key ~ '^[a-z][a-z0-9]*([._-][a-z0-9]+)*$')),
    audit_event_id uuid REFERENCES audit_events (event_id),
    started_at timestamptz,
    completed_at timestamptz,
    version bigint NOT NULL CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT admin_batch_job_items_target_unique UNIQUE (batch_job_id, user_id),
    CHECK (updated_at >= created_at),
    CHECK (started_at IS NULL OR started_at >= created_at),
    CHECK (completed_at IS NULL OR (started_at IS NOT NULL AND completed_at >= started_at)),
    CHECK ((state = 'running' AND lease_owner IS NOT NULL AND lease_until IS NOT NULL AND started_at IS NOT NULL AND completed_at IS NULL)
        OR (state <> 'running' AND lease_owner IS NULL AND lease_until IS NULL)),
    CHECK ((state IN ('succeeded', 'failed', 'skipped', 'canceled')) = (completed_at IS NOT NULL)),
    CHECK ((state IN ('failed', 'skipped')) = (error_message_key IS NOT NULL))
);

CREATE INDEX admin_batch_job_items_claim_idx
    ON admin_batch_job_items (state, lease_until, created_at, item_id);
CREATE INDEX admin_batch_job_items_job_state_idx
    ON admin_batch_job_items (batch_job_id, state, item_id);

CREATE TABLE admin_user_erasure_jobs (
    erasure_job_id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users (user_id),
    actor_admin_id uuid NOT NULL REFERENCES admin_accounts (admin_id),
    operation_id text NOT NULL CHECK (length(operation_id) BETWEEN 1 AND 128),
    request_digest bytea NOT NULL CHECK (octet_length(request_digest) = 32),
    state text NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed')),
    step text NOT NULL CHECK (step IN ('queued', 'revoke_credentials', 'erase_profile', 'enqueue_room_cleanup', 'complete')),
    reason text NOT NULL CHECK (length(reason) BETWEEN 1 AND 512),
    attempt_count integer NOT NULL CHECK (attempt_count >= 0),
    lease_owner text,
    lease_until timestamptz,
    error_message_key text CHECK (error_message_key IS NULL OR (length(error_message_key) BETWEEN 1 AND 128 AND error_message_key ~ '^[a-z][a-z0-9]*([._-][a-z0-9]+)*$')),
    version bigint NOT NULL CHECK (version > 0),
    created_at timestamptz NOT NULL,
    started_at timestamptz,
    completed_at timestamptz,
    updated_at timestamptz NOT NULL,
    CONSTRAINT admin_user_erasure_jobs_operation_unique UNIQUE (actor_admin_id, operation_id),
    CHECK (updated_at >= created_at),
    CHECK (started_at IS NULL OR started_at >= created_at),
    CHECK (completed_at IS NULL OR (started_at IS NOT NULL AND completed_at >= started_at)),
    CHECK ((state = 'queued' AND step = 'queued' AND lease_owner IS NULL AND lease_until IS NULL AND started_at IS NULL AND completed_at IS NULL)
        OR (state = 'running' AND step <> 'queued' AND lease_owner IS NOT NULL AND lease_until IS NOT NULL AND started_at IS NOT NULL AND completed_at IS NULL)
        OR (state = 'succeeded' AND step = 'complete' AND lease_owner IS NULL AND lease_until IS NULL AND started_at IS NOT NULL AND completed_at IS NOT NULL)
        OR (state = 'failed' AND step <> 'queued' AND lease_owner IS NULL AND lease_until IS NULL AND started_at IS NOT NULL AND completed_at IS NOT NULL)),
    CHECK ((state = 'failed') = (error_message_key IS NOT NULL))
);

CREATE UNIQUE INDEX admin_user_erasure_jobs_active_user_idx
    ON admin_user_erasure_jobs (user_id)
    WHERE state IN ('queued', 'running');
CREATE INDEX admin_user_erasure_jobs_claim_idx
    ON admin_user_erasure_jobs (state, lease_until, created_at, erasure_job_id);

CREATE TABLE admin_export_jobs (
    export_id uuid PRIMARY KEY,
    actor_admin_id uuid NOT NULL REFERENCES admin_accounts (admin_id),
    operation_id text NOT NULL CHECK (length(operation_id) BETWEEN 1 AND 128),
    request_digest bytea NOT NULL CHECK (octet_length(request_digest) = 32),
    filter_schema_version integer NOT NULL CHECK (filter_schema_version > 0),
    filter_snapshot jsonb NOT NULL CHECK (jsonb_typeof(filter_snapshot) = 'object'),
    filter_digest bytea NOT NULL CHECK (octet_length(filter_digest) = 32),
    field_names text[] NOT NULL CHECK (cardinality(field_names) BETWEEN 1 AND 32),
    masking_policy text NOT NULL CHECK (masking_policy IN ('redact_pii', 'include_authorized_pii')),
    state text NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'partially_succeeded', 'failed', 'expired', 'deleted')),
    matched_users bigint NOT NULL CHECK (matched_users >= 0),
    exported_users bigint NOT NULL CHECK (exported_users >= 0),
    failed_users bigint NOT NULL CHECK (failed_users >= 0),
    result_object_key text,
    result_digest bytea,
    result_key_version integer,
    result_schema_version integer NOT NULL CHECK (result_schema_version > 0),
    result_expires_at timestamptz NOT NULL,
    error_message_key text CHECK (error_message_key IS NULL OR (length(error_message_key) BETWEEN 1 AND 128 AND error_message_key ~ '^[a-z][a-z0-9]*([._-][a-z0-9]+)*$')),
    lease_owner text,
    lease_until timestamptz,
    version bigint NOT NULL CHECK (version > 0),
    created_at timestamptz NOT NULL,
    started_at timestamptz,
    completed_at timestamptz,
    updated_at timestamptz NOT NULL,
    CONSTRAINT admin_export_jobs_operation_unique UNIQUE (actor_admin_id, operation_id),
    CHECK (updated_at >= created_at),
    CHECK (started_at IS NULL OR started_at >= created_at),
    CHECK (completed_at IS NULL OR (started_at IS NOT NULL AND completed_at >= started_at)),
    CHECK (result_expires_at > created_at),
    CHECK ((result_object_key IS NULL AND result_digest IS NULL AND result_key_version IS NULL)
        OR (result_object_key IS NOT NULL AND octet_length(result_digest) = 32 AND result_key_version > 0)),
    CHECK ((state = 'queued' AND lease_owner IS NULL AND lease_until IS NULL AND started_at IS NULL AND completed_at IS NULL)
        OR (state = 'running' AND lease_owner IS NOT NULL AND lease_until IS NOT NULL AND started_at IS NOT NULL AND completed_at IS NULL)
        OR (state IN ('succeeded', 'partially_succeeded', 'failed', 'expired', 'deleted') AND lease_owner IS NULL AND lease_until IS NULL AND started_at IS NOT NULL AND completed_at IS NOT NULL)),
    CHECK (state NOT IN ('succeeded', 'partially_succeeded') OR result_object_key IS NOT NULL),
    CHECK ((state = 'failed') = (error_message_key IS NOT NULL))
);

CREATE INDEX admin_export_jobs_state_created_idx
    ON admin_export_jobs (state, created_at, export_id);
CREATE INDEX admin_export_jobs_expiry_idx
    ON admin_export_jobs (result_expires_at)
    WHERE state IN ('succeeded', 'partially_succeeded');
CREATE INDEX admin_export_jobs_claim_idx
    ON admin_export_jobs (state, lease_until, created_at, export_id);

CREATE TABLE admin_export_download_grants (
    grant_id uuid PRIMARY KEY,
    export_id uuid NOT NULL REFERENCES admin_export_jobs (export_id) ON DELETE CASCADE,
    actor_admin_id uuid NOT NULL REFERENCES admin_accounts (admin_id),
    session_id uuid NOT NULL REFERENCES admin_sessions (session_id) ON DELETE CASCADE,
    operation_id text NOT NULL CHECK (length(operation_id) BETWEEN 1 AND 128),
    request_digest bytea NOT NULL CHECK (octet_length(request_digest) = 32),
    token_digest bytea NOT NULL CHECK (octet_length(token_digest) = 32),
    token_key_version integer NOT NULL CHECK (token_key_version > 0),
    expected_export_version bigint NOT NULL CHECK (expected_export_version > 0),
    masking_policy text NOT NULL CHECK (masking_policy IN ('redact_pii', 'include_authorized_pii')),
    state text NOT NULL CHECK (state IN ('active', 'consumed', 'expired', 'revoked')),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    revoked_at timestamptz,
    version bigint NOT NULL CHECK (version > 0),
    CONSTRAINT admin_export_download_grants_operation_unique UNIQUE (actor_admin_id, operation_id),
    CONSTRAINT admin_export_download_grants_token_unique UNIQUE (token_key_version, token_digest),
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '5 minutes'),
    CHECK ((state = 'active' AND consumed_at IS NULL AND revoked_at IS NULL)
        OR (state = 'consumed' AND consumed_at IS NOT NULL AND revoked_at IS NULL)
        OR (state = 'expired' AND consumed_at IS NULL AND revoked_at IS NULL)
        OR (state = 'revoked' AND consumed_at IS NULL AND revoked_at IS NOT NULL))
);

CREATE INDEX admin_export_download_grants_expiry_idx
    ON admin_export_download_grants (expires_at)
    WHERE state = 'active';

-- New objects postdate the base permission migration, so ownership and role grants are explicit.
-- +goose StatementBegin
DO $permissions$
DECLARE
    trusted_schema text := current_schema();
    owner_role text := current_setting('game_night.owner_role');
    runtime_role text := current_setting('game_night.runtime_role');
    worker_role text := current_setting('game_night.worker_role');
    table_name text;
BEGIN
    IF trusted_schema IS NULL THEN
        RAISE EXCEPTION 'admin user-center migration requires an explicit current schema';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = owner_role)
       OR NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = runtime_role)
       OR NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = worker_role) THEN
        RAISE EXCEPTION 'admin user-center migration requires configured owner, runtime, and worker roles';
    END IF;

    FOREACH table_name IN ARRAY ARRAY[
        'admin_user_tag_catalog',
        'admin_user_tags',
        'admin_user_tag_links',
        'admin_user_notes',
        'admin_batch_previews',
        'admin_batch_jobs',
        'admin_batch_job_items',
        'admin_user_erasure_jobs',
        'admin_export_jobs',
        'admin_export_download_grants'
    ] LOOP
        EXECUTE format('ALTER TABLE %I.%I OWNER TO %I', trusted_schema, table_name, owner_role);
        EXECUTE format('REVOKE ALL ON TABLE %I.%I FROM PUBLIC', trusted_schema, table_name);
    END LOOP;

    -- Runtime handles administrator-facing queries and command creation. Delete stays limited to tag replacement.
    EXECUTE format('GRANT SELECT, UPDATE ON TABLE %I.admin_user_tag_catalog TO %I', trusted_schema, runtime_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE %I.admin_user_tags TO %I', trusted_schema, runtime_role);
    EXECUTE format('GRANT SELECT, INSERT, DELETE ON TABLE %I.admin_user_tag_links TO %I', trusted_schema, runtime_role);
    EXECUTE format('GRANT SELECT, INSERT ON TABLE %I.admin_user_notes TO %I', trusted_schema, runtime_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON TABLE %I.admin_batch_previews, %I.admin_batch_jobs, %I.admin_batch_job_items, %I.admin_export_jobs, %I.admin_export_download_grants TO %I',
        trusted_schema, trusted_schema, trusted_schema, trusted_schema, trusted_schema, runtime_role);
    EXECUTE format('GRANT SELECT, INSERT ON TABLE %I.admin_user_erasure_jobs TO %I', trusted_schema, runtime_role);

    -- Worker can only lease and advance durable work; annotation and download-grant state remains API-owned.
    EXECUTE format('GRANT SELECT, UPDATE ON TABLE %I.admin_batch_jobs, %I.admin_batch_job_items, %I.admin_user_erasure_jobs, %I.admin_export_jobs TO %I',
        trusted_schema, trusted_schema, trusted_schema, trusted_schema, worker_role);

    EXECUTE format('ALTER FUNCTION %I.reject_admin_user_note_mutation() OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format('REVOKE ALL ON FUNCTION %I.reject_admin_user_note_mutation() FROM PUBLIC', trusted_schema);
END;
$permissions$;
-- +goose StatementEnd

-- +goose Down

DROP TABLE IF EXISTS admin_export_download_grants;
DROP TABLE IF EXISTS admin_export_jobs;
DROP TABLE IF EXISTS admin_user_erasure_jobs;
DROP TABLE IF EXISTS admin_batch_job_items;
DROP TABLE IF EXISTS admin_batch_jobs;
DROP TABLE IF EXISTS admin_batch_previews;
DROP TRIGGER IF EXISTS admin_user_notes_append_only ON admin_user_notes;
DROP TABLE IF EXISTS admin_user_notes;
DROP FUNCTION IF EXISTS reject_admin_user_note_mutation();
DROP TABLE IF EXISTS admin_user_tag_links;
DROP TABLE IF EXISTS admin_user_tags;
DROP TABLE IF EXISTS admin_user_tag_catalog;

DROP INDEX IF EXISTS game_session_participants_admin_user_idx;
DROP INDEX IF EXISTS room_members_admin_user_idx;
DROP INDEX IF EXISTS device_credentials_admin_activity_idx;
DROP INDEX IF EXISTS users_admin_activity_idx;
DROP INDEX IF EXISTS users_admin_username_idx;
DROP INDEX IF EXISTS users_admin_status_created_idx;

ALTER TABLE users
    DROP COLUMN IF EXISTS account_version;
