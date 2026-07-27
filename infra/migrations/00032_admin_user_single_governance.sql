-- +goose Up

-- Single-user governance previews freeze one reviewed command so retries never rebind to mutable live state.
CREATE TABLE admin_user_command_previews (
    preview_id uuid PRIMARY KEY,
    actor_admin_id uuid NOT NULL REFERENCES admin_accounts (admin_id),
    user_id uuid NOT NULL REFERENCES users (user_id),
    command text NOT NULL CHECK (command IN ('suspend', 'unsuspend', 'revoke_all_devices', 'remove_from_current_room', 'delete')),
    snapshot_schema_version integer NOT NULL CHECK (snapshot_schema_version > 0),
    snapshot jsonb NOT NULL CHECK (jsonb_typeof(snapshot) = 'object'),
    preview_digest bytea NOT NULL CHECK (octet_length(preview_digest) = 32),
    affected_devices integer NOT NULL CHECK (affected_devices >= 0),
    affected_rooms integer NOT NULL CHECK (affected_rooms >= 0),
    blockers jsonb NOT NULL CHECK (jsonb_typeof(blockers) = 'array'),
    required_elevation text CHECK (
        required_elevation IS NULL
        OR required_elevation IN ('users.revoke_devices', 'users.delete')
    ),
    sampled_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    version bigint NOT NULL CHECK (version > 0),
    CHECK (expires_at > sampled_at),
    CHECK (consumed_at IS NULL OR consumed_at BETWEEN sampled_at AND expires_at)
);

CREATE INDEX admin_user_command_previews_expiry_idx
    ON admin_user_command_previews (expires_at, preview_id)
    WHERE consumed_at IS NULL;

-- Receipts persist the exact business outcome so operation-id retries never need to rerun destructive work.
CREATE TABLE admin_user_command_receipts (
    actor_admin_id uuid NOT NULL REFERENCES admin_accounts (admin_id),
    operation_id text NOT NULL CHECK (length(operation_id) BETWEEN 1 AND 128),
    request_digest bytea NOT NULL CHECK (octet_length(request_digest) = 32),
    preview_id uuid NOT NULL REFERENCES admin_user_command_previews (preview_id),
    user_id uuid NOT NULL REFERENCES users (user_id),
    command text NOT NULL CHECK (command IN ('suspend', 'unsuspend', 'revoke_all_devices', 'remove_from_current_room', 'delete')),
    outcome text NOT NULL CHECK (outcome IN ('executed', 'no_change', 'rejected')),
    user_version bigint NOT NULL CHECK (user_version > 0),
    revoked_devices integer NOT NULL CHECK (revoked_devices >= 0),
    removed_rooms integer NOT NULL CHECK (removed_rooms >= 0),
    erasure_job_id uuid REFERENCES admin_user_erasure_jobs (erasure_job_id),
    audit_event_id uuid NOT NULL REFERENCES audit_events (event_id),
    completed_at timestamptz NOT NULL,
    PRIMARY KEY (actor_admin_id, operation_id)
);

CREATE INDEX admin_user_command_receipts_user_completed_idx
    ON admin_user_command_receipts (user_id, completed_at DESC, actor_admin_id);

-- These objects postdate the original user-center migration, so ownership and grants are applied explicitly.
-- +goose StatementBegin
DO $permissions$
DECLARE
    trusted_schema text := current_schema();
    owner_role text := current_setting('game_night.owner_role');
    runtime_role text := current_setting('game_night.runtime_role');
BEGIN
    IF trusted_schema IS NULL THEN
        RAISE EXCEPTION 'admin single-user governance migration requires an explicit current schema';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = owner_role)
       OR NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = runtime_role) THEN
        RAISE EXCEPTION 'admin single-user governance migration requires configured owner and runtime roles';
    END IF;

    EXECUTE format('ALTER TABLE %I.admin_user_command_previews OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format('ALTER TABLE %I.admin_user_command_receipts OWNER TO %I', trusted_schema, owner_role);
    EXECUTE format('REVOKE ALL ON TABLE %I.admin_user_command_previews, %I.admin_user_command_receipts FROM PUBLIC', trusted_schema, trusted_schema);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON TABLE %I.admin_user_command_previews TO %I', trusted_schema, runtime_role);
    EXECUTE format('GRANT SELECT, INSERT ON TABLE %I.admin_user_command_receipts TO %I', trusted_schema, runtime_role);
END;
$permissions$;
-- +goose StatementEnd

-- +goose Down

DROP TABLE IF EXISTS admin_user_command_receipts;
DROP TABLE IF EXISTS admin_user_command_previews;
