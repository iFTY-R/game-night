-- +goose Up

-- The singleton PostgreSQL row is the authority consulted by every user-mutation gate.
CREATE TABLE admin_maintenance_state (
    singleton_id smallint PRIMARY KEY DEFAULT 1 CHECK (singleton_id = 1),
    enabled boolean NOT NULL,
    scope text NOT NULL CHECK (scope = 'user_mutations'),
    reason text NOT NULL CHECK (length(reason) <= 512),
    planned_end_at timestamptz,
    version bigint NOT NULL CHECK (version > 0),
    changed_by_admin_id uuid REFERENCES admin_accounts (admin_id),
    changed_at timestamptz NOT NULL,
    CHECK (NOT enabled OR length(reason) BETWEEN 1 AND 512),
    CHECK (planned_end_at IS NULL OR planned_end_at > changed_at)
);

INSERT INTO admin_maintenance_state (
    singleton_id, enabled, scope, reason, version, changed_at
) VALUES (
    1, false, 'user_mutations', '', 1, transaction_timestamp()
);

-- Heartbeats carry only bounded component codes and states; raw errors never enter this table.
CREATE TABLE admin_service_instances (
    service_kind text NOT NULL CHECK (service_kind IN ('api', 'edge', 'realtime', 'worker')),
    instance_id text NOT NULL CHECK (length(instance_id) BETWEEN 1 AND 128 AND instance_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]*$'),
    build_version text NOT NULL CHECK (length(build_version) BETWEEN 1 AND 128 AND build_version ~ '^[A-Za-z0-9][A-Za-z0-9._+-]*$'),
    started_at timestamptz NOT NULL,
    last_heartbeat_at timestamptz NOT NULL,
    status text NOT NULL CHECK (status IN ('healthy', 'degraded', 'unavailable')),
    components jsonb NOT NULL CHECK (jsonb_typeof(components) = 'object' AND octet_length(components::text) <= 8192),
    maintenance_version bigint NOT NULL CHECK (maintenance_version > 0),
    PRIMARY KEY (service_kind, instance_id),
    CHECK (last_heartbeat_at >= started_at)
);

CREATE INDEX admin_service_instances_heartbeat_idx
    ON admin_service_instances (last_heartbeat_at DESC, service_kind, instance_id);

-- Buckets are UTC-aligned and idempotently recomputed by the worker over a fixed lookback window.
CREATE TABLE admin_metric_buckets (
    metric_name text NOT NULL CHECK (metric_name IN (
        'online_users', 'active_rooms', 'running_games', 'new_users',
        'suspended_users', 'unsuspended_users', 'abnormal_terminations', 'emergency_repairs'
    )),
    bucket_width text NOT NULL CHECK (bucket_width IN ('hour', 'day')),
    bucket_start timestamptz NOT NULL,
    value bigint NOT NULL CHECK (value >= 0),
    sampled_at timestamptz NOT NULL,
    source_watermark bigint NOT NULL CHECK (source_watermark >= 0),
    PRIMARY KEY (metric_name, bucket_width, bucket_start),
    CHECK (sampled_at >= bucket_start),
    CHECK (
        (bucket_width = 'hour' AND mod(extract(epoch FROM bucket_start)::bigint, 3600) = 0)
        OR (bucket_width = 'day' AND mod(extract(epoch FROM bucket_start)::bigint, 86400) = 0)
    )
);

CREATE INDEX admin_metric_buckets_window_idx
    ON admin_metric_buckets (bucket_width, bucket_start DESC, metric_name);

-- Generation counters replace arbitrary Redis key deletion with three reviewed projections.
CREATE TABLE admin_cache_generations (
    namespace text PRIMARY KEY CHECK (namespace IN (
        'admin_overview_projection', 'admin_operations_probes', 'realtime_presence_projection'
    )),
    generation bigint NOT NULL CHECK (generation > 0),
    updated_by_admin_id uuid REFERENCES admin_accounts (admin_id),
    updated_at timestamptz NOT NULL
);

INSERT INTO admin_cache_generations (namespace, generation, updated_at) VALUES
    ('admin_overview_projection', 1, transaction_timestamp()),
    ('admin_operations_probes', 1, transaction_timestamp()),
    ('realtime_presence_projection', 1, transaction_timestamp());

-- A persisted preview binds a short-lived reviewed command to one administrator and exact state version.
CREATE TABLE admin_operations_previews (
    preview_digest bytea PRIMARY KEY CHECK (octet_length(preview_digest) = 32),
    actor_admin_id uuid NOT NULL REFERENCES admin_accounts (admin_id),
    command_kind text NOT NULL CHECK (command_kind IN ('maintenance_change', 'cache_refresh', 'task_retry')),
    reason_digest bytea NOT NULL CHECK (octet_length(reason_digest) = 32),
    expected_version bigint NOT NULL CHECK (expected_version > 0),
    maintenance_enabled boolean,
    maintenance_planned_end_at timestamptz,
    cache_namespace text CHECK (cache_namespace IN (
        'admin_overview_projection', 'admin_operations_probes', 'realtime_presence_projection'
    )),
    task_kind text CHECK (task_kind IN ('user_batch', 'user_erasure')),
    task_id uuid,
    sampled_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    CHECK (expires_at > sampled_at),
    CHECK (consumed_at IS NULL OR consumed_at >= sampled_at),
    CHECK (
        (command_kind = 'maintenance_change' AND maintenance_enabled IS NOT NULL AND cache_namespace IS NULL AND task_kind IS NULL AND task_id IS NULL)
        OR (command_kind = 'cache_refresh' AND maintenance_enabled IS NULL AND maintenance_planned_end_at IS NULL AND cache_namespace IS NOT NULL AND task_kind IS NULL AND task_id IS NULL)
        OR (command_kind = 'task_retry' AND maintenance_enabled IS NULL AND maintenance_planned_end_at IS NULL AND cache_namespace IS NULL AND task_kind IS NOT NULL AND task_id IS NOT NULL)
    )
);

CREATE INDEX admin_operations_previews_expiry_idx
    ON admin_operations_previews (expires_at, actor_admin_id);

-- Maintenance and cache receipts preserve the first committed response for operation-id replay.
CREATE TABLE admin_operations_command_receipts (
    actor_admin_id uuid NOT NULL REFERENCES admin_accounts (admin_id),
    operation_id text NOT NULL CHECK (length(operation_id) BETWEEN 1 AND 128),
    request_digest bytea NOT NULL CHECK (octet_length(request_digest) = 32),
    command_kind text NOT NULL CHECK (command_kind IN ('maintenance_change', 'cache_refresh')),
    target text NOT NULL CHECK (target IN (
        'user_mutations', 'admin_overview_projection', 'admin_operations_probes', 'realtime_presence_projection'
    )),
    outcome text NOT NULL CHECK (outcome IN ('applied', 'no_change', 'rejected')),
    previous_version bigint NOT NULL CHECK (previous_version > 0),
    current_version bigint NOT NULL CHECK (current_version > 0),
    maintenance_enabled boolean,
    maintenance_reason text NOT NULL DEFAULT '' CHECK (length(maintenance_reason) <= 512),
    maintenance_planned_end_at timestamptz,
    maintenance_changed_by_admin_id uuid REFERENCES admin_accounts (admin_id),
    maintenance_changed_at timestamptz,
    audit_event_id uuid NOT NULL REFERENCES audit_events (event_id),
    completed_at timestamptz NOT NULL,
    PRIMARY KEY (actor_admin_id, operation_id),
    CHECK (
        (command_kind = 'maintenance_change' AND target = 'user_mutations' AND maintenance_enabled IS NOT NULL AND maintenance_changed_at IS NOT NULL)
        OR (command_kind = 'cache_refresh' AND target <> 'user_mutations' AND maintenance_enabled IS NULL AND maintenance_reason = '' AND maintenance_planned_end_at IS NULL AND maintenance_changed_by_admin_id IS NULL AND maintenance_changed_at IS NULL)
    )
);

-- Manual retry receipts bind one operation to one task and preserve the first committed result.
CREATE TABLE admin_operations_retry_receipts (
    actor_admin_id uuid NOT NULL REFERENCES admin_accounts (admin_id),
    operation_id text NOT NULL CHECK (length(operation_id) BETWEEN 1 AND 128),
    request_digest bytea NOT NULL CHECK (octet_length(request_digest) = 32),
    task_kind text NOT NULL CHECK (task_kind IN ('user_batch', 'user_erasure')),
    task_id uuid NOT NULL,
    expected_task_version bigint NOT NULL CHECK (expected_task_version > 0),
    outcome text NOT NULL CHECK (outcome IN ('applied', 'no_change', 'rejected')),
    task_version bigint NOT NULL CHECK (task_version > 0),
    manual_retry_count integer NOT NULL CHECK (manual_retry_count > 0),
    task_state text NOT NULL CHECK (task_state = 'queued'),
    original_error_code text NOT NULL CHECK (length(original_error_code) BETWEEN 1 AND 128 AND original_error_code ~ '^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$'),
    audit_event_id uuid NOT NULL REFERENCES audit_events (event_id),
    completed_at timestamptz NOT NULL,
    PRIMARY KEY (actor_admin_id, operation_id)
);

CREATE INDEX admin_operations_retry_receipts_task_idx
    ON admin_operations_retry_receipts (task_kind, task_id, completed_at DESC);

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
        RAISE EXCEPTION 'admin operations migration requires an explicit current schema';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = owner_role)
       OR NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = runtime_role)
       OR NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = worker_role) THEN
        RAISE EXCEPTION 'admin operations migration requires configured owner, runtime, and worker roles';
    END IF;

    FOREACH table_name IN ARRAY ARRAY[
        'admin_maintenance_state',
        'admin_service_instances',
        'admin_metric_buckets',
        'admin_cache_generations',
        'admin_operations_previews',
        'admin_operations_command_receipts',
        'admin_operations_retry_receipts'
    ] LOOP
        EXECUTE format('ALTER TABLE %I.%I OWNER TO %I', trusted_schema, table_name, owner_role);
        EXECUTE format('REVOKE ALL ON TABLE %I.%I FROM PUBLIC', trusted_schema, table_name);
    END LOOP;

    EXECUTE format('GRANT SELECT, UPDATE ON TABLE %I.admin_maintenance_state TO %I', trusted_schema, runtime_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON TABLE %I.admin_service_instances TO %I', trusted_schema, runtime_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON TABLE %I.admin_metric_buckets TO %I', trusted_schema, runtime_role);
    EXECUTE format('GRANT SELECT, UPDATE ON TABLE %I.admin_cache_generations TO %I', trusted_schema, runtime_role);
    EXECUTE format('GRANT SELECT, INSERT ON TABLE %I.admin_operations_previews TO %I', trusted_schema, runtime_role);
    EXECUTE format('GRANT SELECT, INSERT ON TABLE %I.admin_operations_command_receipts TO %I', trusted_schema, runtime_role);
    EXECUTE format('GRANT SELECT, INSERT ON TABLE %I.admin_operations_retry_receipts TO %I', trusted_schema, runtime_role);

    EXECUTE format('GRANT SELECT ON TABLE %I.admin_maintenance_state, %I.admin_service_instances, %I.admin_cache_generations, %I.admin_operations_previews, %I.admin_operations_command_receipts, %I.admin_operations_retry_receipts TO %I', trusted_schema, trusted_schema, trusted_schema, trusted_schema, trusted_schema, trusted_schema, worker_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON TABLE %I.admin_metric_buckets TO %I', trusted_schema, worker_role);
END;
$permissions$;
-- +goose StatementEnd

-- +goose Down

DROP TABLE IF EXISTS admin_operations_retry_receipts;
DROP TABLE IF EXISTS admin_operations_command_receipts;
DROP TABLE IF EXISTS admin_operations_previews;
DROP TABLE IF EXISTS admin_cache_generations;
DROP TABLE IF EXISTS admin_metric_buckets;
DROP TABLE IF EXISTS admin_service_instances;
DROP TABLE IF EXISTS admin_maintenance_state;
