package migrator

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iFTY-R/game-night/internal/integrationtest"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
)

// migrationTestTimeout covers database creation plus a complete up/down/up cycle on shared CI runners.
const migrationTestTimeout = 90 * time.Second

func TestMigrationsUpDownUp(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), migrationTestTimeout)
	defer cancel()

	var currentUser string
	if err := fixture.Pool.QueryRow(ctx, "SELECT current_user").Scan(&currentUser); err != nil {
		t.Fatal(err)
	}
	database := fixture.OpenSQLDB(t, map[string]string{
		ownerRoleSetting:       currentUser,
		auditWriterRoleSetting: currentUser,
		migrationRoleSetting:   currentUser,
		runtimeRoleSetting:     currentUser,
		workerRoleSetting:      currentUser,
	})
	migrationsDir := migrationDirectory(t)

	if err := Run(ctx, database, migrationsDir, "up"); err != nil {
		t.Fatal(err)
	}
	assertExpectedTables(t, ctx, fixture.Pool)
	assertBootstrapAdminHasNoDefaultSecret(t, ctx, fixture.Pool)

	if err := goose.DownToContext(ctx, database, migrationsDir, 0); err != nil {
		t.Fatalf("roll all migrations down: %v", err)
	}
	if err := Run(ctx, database, migrationsDir, "up"); err != nil {
		t.Fatalf("reapply migrations: %v", err)
	}
	assertExpectedTables(t, ctx, fixture.Pool)
}

func TestMigrationPrivileges(t *testing.T) {
	fixture := integrationtest.OpenPrivilegeDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), migrationTestTimeout)
	defer cancel()

	config := Config{
		DatabaseURL:     fixture.MigrationURL,
		Schema:          fixture.Schema,
		OwnerRole:       fixture.OwnerRole,
		AuditWriterRole: fixture.AuditWriterRole,
		MigrationRole:   fixture.MigrationRole,
		RuntimeRole:     fixture.RuntimeRole,
		WorkerRole:      fixture.WorkerRole,
		MigrationsDir:   migrationDirectory(t),
	}
	database, err := OpenDatabase(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := Run(ctx, database, config.MigrationsDir, "up"); err != nil {
		t.Fatal(err)
	}

	migrationPool := openRolePool(t, ctx, fixture.MigrationURL)
	runtimePool := openRolePool(t, ctx, fixture.RuntimeURL)
	workerPool := openRolePool(t, ctx, fixture.WorkerURL)
	assertSecurityDefinerConfiguration(t, ctx, migrationPool, fixture)
	assertPublicAndTemporaryShadowingCannotRedirect(t, ctx, migrationPool, runtimePool)

	assertQuerySucceeds(t, ctx, runtimePool, "SELECT sequence FROM read_audit_head('admin')")
	assertQuerySucceeds(t, ctx, runtimePool, "SELECT status FROM admin_accounts WHERE singleton_id = 1")
	assertQueryFails(t, ctx, runtimePool, "SELECT count(*) FROM audit_events", "permission denied")
	assertQueryFails(t, ctx, runtimePool, "UPDATE audit_chain_head SET sequence = sequence", "permission denied")
	assertQueryFails(t, ctx, runtimePool, "INSERT INTO admin_accounts (singleton_id) VALUES (1)", "permission denied")
	assertQueryFails(t, ctx, runtimePool, "DELETE FROM admin_accounts", "permission denied")
	assertQueryFails(t, ctx, runtimePool, "SELECT count(*) FROM outbox_consumers", "permission denied")
	assertQueryFails(t, ctx, runtimePool, "UPDATE outbox_consumers SET last_acked_sequence = last_acked_sequence", "permission denied")
	assertQueryFails(t, ctx, runtimePool, "CREATE TABLE runtime_escape (id integer)", "permission denied")
	assertResetDenied(t, ctx, runtimePool)

	assertQuerySucceeds(t, ctx, workerPool, "SELECT count(*) FROM outbox_consumers")
	assertQuerySucceeds(t, ctx, workerPool, "UPDATE outbox_consumers SET updated_at = updated_at WHERE consumer_id = 'audit.checkpoint'")
	assertQuerySucceeds(t, ctx, workerPool, "UPDATE user_profiles SET real_name_ciphertext = real_name_ciphertext, real_name_nonce = real_name_nonce, real_name_key_version = real_name_key_version WHERE false")
	assertQuerySucceeds(t, ctx, workerPool, "UPDATE profile_export_items SET real_name_ciphertext = real_name_ciphertext, real_name_nonce = real_name_nonce, real_name_key_version = real_name_key_version WHERE false")
	assertQuerySucceeds(t, ctx, workerPool, "UPDATE admin_totp_enrollments SET ciphertext = ciphertext, nonce = nonce, key_version = key_version WHERE false")
	assertQueryFails(t, ctx, workerPool, "UPDATE outbox_events SET payload = payload", "permission denied")
	assertQueryFails(t, ctx, workerPool, "SELECT count(*) FROM admin_accounts", "permission denied")
	assertQueryFails(t, ctx, workerPool, "SELECT count(*) FROM users", "permission denied")
	assertQueryFails(t, ctx, workerPool, "DELETE FROM anonymous_challenges", "permission denied")
	assertQueryFails(t, ctx, workerPool, "UPDATE admin_totp_enrollments SET status = status", "permission denied")
	assertQueryFails(t, ctx, workerPool, "UPDATE profile_export_items SET username = username", "permission denied")
	assertQueryFails(t, ctx, workerPool, "SELECT count(*) FROM audit_events", "permission denied")
	assertResetDenied(t, ctx, workerPool)
}

func TestResetAdminAccountRejectsMissingSingleton(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), migrationTestTimeout)
	defer cancel()

	var currentUser string
	if err := fixture.Pool.QueryRow(ctx, "SELECT current_user").Scan(&currentUser); err != nil {
		t.Fatal(err)
	}
	database := fixture.OpenSQLDB(t, map[string]string{
		ownerRoleSetting:       currentUser,
		auditWriterRoleSetting: currentUser,
		migrationRoleSetting:   currentUser,
		runtimeRoleSetting:     currentUser,
		workerRoleSetting:      currentUser,
	})
	if err := Run(ctx, database, migrationDirectory(t), "up"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Pool.Exec(ctx, "DELETE FROM admin_accounts WHERE singleton_id = 1"); err != nil {
		t.Fatal(err)
	}

	assertQueryFails(t, ctx, fixture.Pool, `
		SELECT * FROM reset_admin_account(
			decode(repeat('00', 32), 'hex'),
			gen_random_uuid(),
			convert_to('admin reset', 'UTF8'),
			decode(repeat('00', 64), 'hex'),
			1,
			transaction_timestamp(),
			'argon2id hash',
			'argon2id',
			'm=65536,t=3,p=1',
			gen_random_uuid(),
			convert_to('checkpoint', 'UTF8')
		)
	`, "singleton admin account is missing")
}

func assertSecurityDefinerConfiguration(t testing.TB, ctx context.Context, pool *pgxpool.Pool, fixture *integrationtest.PrivilegeDatabase) {
	t.Helper()

	rows, err := pool.Query(ctx, `
		SELECT
			procedure.proname,
			pg_catalog.pg_get_userbyid(procedure.proowner),
			procedure.prosecdef,
			pg_catalog.array_to_string(procedure.proconfig, ','),
			EXISTS (
				SELECT 1
				FROM pg_catalog.aclexplode(
					COALESCE(procedure.proacl, pg_catalog.acldefault('f'::"char", procedure.proowner))
				) AS privilege
				WHERE privilege.grantee = 0
				  AND privilege.privilege_type = 'EXECUTE'
			),
			owner.rolcanlogin
		FROM pg_catalog.pg_proc AS procedure
		JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = procedure.pronamespace
		JOIN pg_catalog.pg_roles AS owner ON owner.oid = procedure.proowner
		WHERE namespace.nspname = current_schema()
		  AND procedure.proname IN ('read_audit_head', 'append_audit_event', 'reset_admin_account')
		ORDER BY procedure.proname
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	functionCount := 0
	wantSearchPath := "search_path=pg_catalog," + fixture.Schema + ",pg_temp"
	for rows.Next() {
		var name, ownerRole, searchPath string
		var securityDefiner, publicExecute, ownerCanLogin bool
		if err := rows.Scan(&name, &ownerRole, &securityDefiner, &searchPath, &publicExecute, &ownerCanLogin); err != nil {
			t.Fatal(err)
		}
		functionCount++
		searchPath = strings.ReplaceAll(searchPath, " ", "")
		if ownerRole != fixture.OwnerRole || ownerCanLogin {
			t.Errorf("function %s has unsafe owner %s (can_login=%t)", name, ownerRole, ownerCanLogin)
		}
		if !securityDefiner || publicExecute {
			t.Errorf("function %s has unsafe execution flags (security_definer=%t public_execute=%t)", name, securityDefiner, publicExecute)
		}
		if searchPath != wantSearchPath || strings.Contains(searchPath, "$user") || strings.Contains(searchPath, ",public") {
			t.Errorf("function %s has unsafe search_path %q, want %q", name, searchPath, wantSearchPath)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if functionCount != 3 {
		t.Fatalf("expected 3 SECURITY DEFINER functions, got %d", functionCount)
	}
}

func assertPublicAndTemporaryShadowingCannotRedirect(t testing.TB, ctx context.Context, migrationPool, runtimePool *pgxpool.Pool) {
	t.Helper()

	if _, err := migrationPool.Exec(ctx, `
		CREATE TABLE public.audit_chain_head (
			chain_id text PRIMARY KEY,
			sequence bigint NOT NULL,
			head_hash bytea NOT NULL
		);
		INSERT INTO public.audit_chain_head (chain_id, sequence, head_hash)
		VALUES ('admin', 777, decode(repeat('ee', 32), 'hex'))
	`); err != nil {
		t.Fatalf("create public shadow table: %v", err)
	}
	assertAuditHeadSequence(t, ctx, runtimePool, 0, "public shadow")

	connection, err := runtimePool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, `
		CREATE TEMP TABLE audit_chain_head (
			chain_id text PRIMARY KEY,
			sequence bigint NOT NULL,
			head_hash bytea NOT NULL
		);
		INSERT INTO audit_chain_head (chain_id, sequence, head_hash)
		VALUES ('admin', 999, decode(repeat('ff', 32), 'hex'))
	`); err != nil {
		t.Fatalf("create temporary shadow table: %v", err)
	}
	var sequence int64
	if err := connection.QueryRow(ctx, "SELECT sequence FROM read_audit_head('admin')").Scan(&sequence); err != nil {
		t.Fatalf("read audit head with temporary shadow present: %v", err)
	}
	if sequence != 0 {
		t.Fatalf("temporary shadow redirected read_audit_head: got sequence %d", sequence)
	}
}

func assertAuditHeadSequence(t testing.TB, ctx context.Context, pool *pgxpool.Pool, want int64, scenario string) {
	t.Helper()

	var sequence int64
	if err := pool.QueryRow(ctx, "SELECT sequence FROM read_audit_head('admin')").Scan(&sequence); err != nil {
		t.Fatalf("read audit head with %s present: %v", scenario, err)
	}
	if sequence != want {
		t.Fatalf("%s redirected read_audit_head: got sequence %d, want %d", scenario, sequence, want)
	}
}

func migrationDirectory(t testing.TB) string {
	t.Helper()
	directory, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "infra", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

func assertExpectedTables(t testing.TB, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	want := []string{
		"admin_accounts",
		"admin_assisted_recovery_grants",
		"admin_challenges",
		"admin_recovery_codes",
		"admin_sessions",
		"admin_totp_enrollments",
		"anonymous_challenges",
		"audit_chain_head",
		"audit_events",
		"device_credentials",
		"key_rotation_jobs",
		"outbox_consumers",
		"outbox_events",
		"profile_export_contexts",
		"profile_export_items",
		"secret_operation_results",
		"user_profiles",
		"user_recovery_attempts",
		"user_recovery_credentials",
		"username_claims",
		"users",
	}
	rows, err := pool.Query(ctx, `
        SELECT table_name
        FROM information_schema.tables
        WHERE table_schema = current_schema()
          AND table_type = 'BASE TABLE'
          AND table_name <> 'goose_db_version'
        ORDER BY table_name
    `)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := make([]string, 0, len(want))
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected migrated tables:\nwant: %v\n got: %v", want, got)
	}
}

func assertBootstrapAdminHasNoDefaultSecret(t testing.TB, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var status string
	var passwordMissing bool
	if err := pool.QueryRow(ctx, `
        SELECT status, password_hash IS NULL
        FROM admin_accounts
        WHERE singleton_id = 1
    `).Scan(&status, &passwordMissing); err != nil {
		t.Fatal(err)
	}
	if status != "bootstrap_pending" || !passwordMissing {
		t.Fatalf("unexpected bootstrap administrator state: status=%s password_missing=%t", status, passwordMissing)
	}
}

func openRolePool(t testing.TB, ctx context.Context, databaseURL string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func assertQuerySucceeds(t testing.TB, ctx context.Context, pool *pgxpool.Pool, query string) {
	t.Helper()
	rows, err := pool.Query(ctx, query)
	if err != nil {
		t.Fatalf("expected query to succeed: %v", err)
	}
	rows.Close()
}

func assertQueryFails(t testing.TB, ctx context.Context, pool *pgxpool.Pool, query, diagnostic string) {
	t.Helper()
	_, err := pool.Exec(ctx, query)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), diagnostic) {
		t.Fatalf("expected query failure containing %q, got %v", diagnostic, err)
	}
}

func assertResetDenied(t testing.TB, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	assertQueryFails(t, ctx, pool, `
        SELECT * FROM reset_admin_account(
            NULL::bytea,
            NULL::uuid,
            NULL::bytea,
            NULL::bytea,
            NULL::integer,
            NULL::timestamptz,
            NULL::text,
            NULL::text,
            NULL::text,
            NULL::uuid,
            NULL::bytea
        )
    `, "permission denied")
}
