package migrator

import (
	"context"
	"database/sql"
	"fmt"
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

func TestIdentityInvariantMigrationAcceptsValidVersionEightRows(t *testing.T) {
	ctx, fixture, database, migrationsDir := openMigrationEightTest(t)
	_, err := fixture.Pool.Exec(ctx, `
		INSERT INTO users (
			user_id, status, username, current_username_key, username_changed_at, created_at, updated_at
		) VALUES (
			'91000000-0000-4000-8000-000000000001', 'active', 'Valid9', 'valid9',
			transaction_timestamp(), transaction_timestamp(), transaction_timestamp()
		);
		INSERT INTO username_claims (
			username_key, display_username, status, owner_user_id, created_at, updated_at
		) VALUES (
			'valid9', 'Valid9', 'active', '91000000-0000-4000-8000-000000000001',
			transaction_timestamp(), transaction_timestamp()
		);
		INSERT INTO device_credentials (
			credential_id, user_id, secret_hash, secret_key_version, csrf_hash, generation, label,
			created_at, last_seen_at, rotated_at, idle_expires_at, absolute_expires_at
		) VALUES (
			'92000000-0000-4000-8000-000000000001', '91000000-0000-4000-8000-000000000001',
			decode(repeat('11', 32), 'hex'), 1, decode(repeat('22', 32), 'hex'), 1, 'Phone',
			transaction_timestamp(), transaction_timestamp(), transaction_timestamp(),
			transaction_timestamp() + interval '15552000 seconds',
			transaction_timestamp() + interval '31536000 seconds'
		)
	`)
	if err != nil {
		t.Fatalf("insert valid migration-8 identity state: %v", err)
	}
	if err := goose.UpToContext(ctx, database, migrationsDir, 9); err != nil {
		t.Fatalf("upgrade valid identity state to migration 9: %v", err)
	}
}

func TestIdentityInvariantMigrationRejectsInvalidVersionEightRows(t *testing.T) {
	for _, test := range []struct {
		name       string
		insertSQL  string
		constraint string
	}{
		{
			name: "active user missing username timestamp",
			insertSQL: `
				INSERT INTO users (
					user_id, status, username, current_username_key, username_changed_at, created_at, updated_at
				) VALUES (
					'93000000-0000-4000-8000-000000000001', 'active', 'Broken9', 'broken9', NULL,
					transaction_timestamp(), transaction_timestamp()
				);
				INSERT INTO username_claims (
					username_key, display_username, status, owner_user_id, created_at, updated_at
				) VALUES (
					'broken9', 'Broken9', 'active', '93000000-0000-4000-8000-000000000001',
					transaction_timestamp(), transaction_timestamp()
				)
			`,
			constraint: "users_username_timestamp_invariant",
		},
		{
			name: "username timestamp follows update",
			insertSQL: `
				INSERT INTO users (
					user_id, status, username, current_username_key, username_changed_at, created_at, updated_at
				) VALUES (
					'93000000-0000-4000-8000-000000000007', 'active', 'Future9', 'future9',
					transaction_timestamp() + interval '1 second', transaction_timestamp(), transaction_timestamp()
				);
				INSERT INTO username_claims (
					username_key, display_username, status, owner_user_id, created_at, updated_at
				) VALUES (
					'future9', 'Future9', 'active', '93000000-0000-4000-8000-000000000007',
					transaction_timestamp(), transaction_timestamp()
				)
			`,
			constraint: "users_username_timestamp_invariant",
		},
		{
			name: "expired reservation has invalid chronology",
			insertSQL: `
				INSERT INTO users (user_id, status, created_at, updated_at)
				VALUES (
					'93000000-0000-4000-8000-000000000002', 'onboarding',
					transaction_timestamp(), transaction_timestamp()
				);
				INSERT INTO username_claims (
					username_key, display_username, status, owner_user_id, reserved_until, created_at, updated_at
				) VALUES (
					'expired9', 'Expired9', 'reserved', '93000000-0000-4000-8000-000000000002',
					transaction_timestamp(), transaction_timestamp(), transaction_timestamp()
				)
			`,
			constraint: "username_claims_reservation_time_invariant",
		},
		{
			name: "device ttl",
			insertSQL: invalidMigrationEightDeviceSQL(
				"93000000-0000-4000-8000-000000000003", "94000000-0000-4000-8000-000000000003",
				"1, 'Phone', transaction_timestamp(), transaction_timestamp(), transaction_timestamp(), "+
					"transaction_timestamp() + interval '15552000 seconds', transaction_timestamp() + interval '31449600 seconds', NULL, NULL",
				false,
			),
			constraint: "device_credentials_time_invariant",
		},
		{
			name: "previous secret grace",
			insertSQL: invalidMigrationEightDeviceSQL(
				"93000000-0000-4000-8000-000000000004", "94000000-0000-4000-8000-000000000004",
				"2, 'Phone', transaction_timestamp(), transaction_timestamp(), transaction_timestamp(), "+
					"transaction_timestamp() + interval '15552000 seconds', transaction_timestamp() + interval '31536000 seconds', NULL, NULL",
				true,
			),
			constraint: "device_credentials_previous_time_invariant",
		},
		{
			name: "empty device label",
			insertSQL: invalidMigrationEightDeviceSQL(
				"93000000-0000-4000-8000-000000000005", "94000000-0000-4000-8000-000000000005",
				"1, '', transaction_timestamp(), transaction_timestamp(), transaction_timestamp(), "+
					"transaction_timestamp() + interval '15552000 seconds', transaction_timestamp() + interval '31536000 seconds', NULL, NULL",
				false,
			),
			constraint: "device_credentials_label_invariant",
		},
		{
			name: "revocation predates last activity",
			insertSQL: invalidMigrationEightDeviceSQL(
				"93000000-0000-4000-8000-000000000006", "94000000-0000-4000-8000-000000000006",
				"1, 'Phone', transaction_timestamp(), transaction_timestamp() + interval '60 seconds', transaction_timestamp(), "+
					"transaction_timestamp() + interval '15552060 seconds', transaction_timestamp() + interval '31536000 seconds', "+
					"transaction_timestamp() + interval '30 seconds', 'account_deleted'",
				false,
			),
			constraint: "device_credentials_time_invariant",
		},
		{
			name: "unknown revocation reason",
			insertSQL: invalidMigrationEightDeviceSQL(
				"93000000-0000-4000-8000-000000000008", "94000000-0000-4000-8000-000000000008",
				"1, 'Phone', transaction_timestamp(), transaction_timestamp(), transaction_timestamp(), "+
					"transaction_timestamp() + interval '15552000 seconds', transaction_timestamp() + interval '31536000 seconds', "+
					"transaction_timestamp(), 'unknown_reason'",
				false,
			),
			constraint: "device_credentials_revoke_reason_invariant",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, fixture, database, migrationsDir := openMigrationEightTest(t)
			if _, err := fixture.Pool.Exec(ctx, test.insertSQL); err != nil {
				t.Fatalf("insert invalid migration-8 state: %v", err)
			}
			err := goose.UpToContext(ctx, database, migrationsDir, 9)
			if err == nil || !strings.Contains(err.Error(), test.constraint) {
				t.Fatalf("migration error = %v, want constraint %q", err, test.constraint)
			}
		})
	}
}

func openMigrationEightTest(t testing.TB) (
	context.Context,
	*integrationtest.PostgresSchema,
	*sql.DB,
	string,
) {
	t.Helper()
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), migrationTestTimeout)
	t.Cleanup(cancel)
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
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(ctx, database, migrationsDir, 8); err != nil {
		t.Fatalf("apply migrations through version 8: %v", err)
	}
	return ctx, fixture, database, migrationsDir
}

func invalidMigrationEightDeviceSQL(userID, credentialID, stateValues string, withPrevious bool) string {
	previousColumns := "NULL, NULL, NULL"
	if withPrevious {
		previousColumns = "decode(repeat('33', 32), 'hex'), 1, transaction_timestamp() + interval '121 seconds'"
	}
	return fmt.Sprintf(`
		INSERT INTO users (user_id, status, created_at, updated_at)
		VALUES ('%s', 'onboarding', transaction_timestamp(), transaction_timestamp());
		INSERT INTO device_credentials (
			credential_id, user_id, secret_hash, secret_key_version,
			previous_secret_hash, previous_secret_key_version, previous_valid_until,
			csrf_hash, generation, label, created_at, last_seen_at, rotated_at,
			idle_expires_at, absolute_expires_at, revoked_at, revoke_reason
		) VALUES (
			'%s', '%s', decode(repeat('11', 32), 'hex'), 1,
			%s, decode(repeat('22', 32), 'hex'), %s
		)
	`, userID, credentialID, userID, previousColumns, stateValues)
}

func TestSecretResultWorkflowDownRemovesConsumedChallengesWithoutReplay(t *testing.T) {
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

	_, err := fixture.Pool.Exec(ctx, `
		INSERT INTO users (user_id, status, created_at, updated_at)
		VALUES ('10000000-0000-4000-8000-000000000001', 'onboarding', transaction_timestamp(), transaction_timestamp());

		INSERT INTO user_recovery_credentials (
			recovery_credential_id, user_id, selector, secret_hash, version, status, created_at
		) VALUES (
			'20000000-0000-4000-8000-000000000001',
			'10000000-0000-4000-8000-000000000001',
			'downgrade-recovery-credential', 'argon2id hash', 1, 'active', transaction_timestamp()
		);

		INSERT INTO anonymous_challenges (
			challenge_id, selector, secret_hash, secret_key_version, purpose, audience,
			origin_hash, request_flow_id, max_attempts, created_at, expires_at, consumed_at
		) VALUES (
			'30000000-0000-4000-8000-000000000001',
			'downgrade-consumed-challenge', decode(repeat('11', 32), 'hex'), 1,
			'identity.recovery', 'identity.v1.IdentityService', decode(repeat('22', 32), 'hex'),
			'downgrade-flow', 5, transaction_timestamp() - interval '1 minute',
			transaction_timestamp() + interval '4 minutes', transaction_timestamp()
		);

		INSERT INTO user_recovery_attempts (
			recovery_attempt_id, grant_selector, grant_secret_hash, grant_key_version, user_id,
			recovery_credential_id, recovery_credential_version, challenge_id, origin_hash,
			purpose, attempt_count, max_attempts, status, created_at, expires_at
		) VALUES (
			'40000000-0000-4000-8000-000000000001', 'downgrade-recovery-grant',
			decode(repeat('33', 32), 'hex'), 1, '10000000-0000-4000-8000-000000000001',
			'20000000-0000-4000-8000-000000000001', 1,
			'30000000-0000-4000-8000-000000000001', decode(repeat('22', 32), 'hex'),
			'identity.recovery', 0, 5, 'active', transaction_timestamp(),
			transaction_timestamp() + interval '4 minutes'
		)
	`)
	if err != nil {
		t.Fatalf("insert migration-6-only recovery state: %v", err)
	}

	if err := goose.DownToContext(ctx, database, migrationsDir, 5); err != nil {
		t.Fatalf("downgrade migration 6: %v", err)
	}
	for _, table := range []string{"user_recovery_attempts", "anonymous_challenges"} {
		var count int
		if err := fixture.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s retained %d migration-6-only rows", table, count)
		}
	}

	assertQueryFails(t, ctx, fixture.Pool, `
		INSERT INTO anonymous_challenges (
			challenge_id, selector, secret_hash, secret_key_version, purpose, audience,
			origin_hash, request_flow_id, max_attempts, created_at, expires_at, consumed_at
		) VALUES (
			'30000000-0000-4000-8000-000000000002',
			'downgrade-rejected-challenge', decode(repeat('44', 32), 'hex'), 1,
			'identity.recovery', 'identity.v1.IdentityService', decode(repeat('55', 32), 'hex'),
			'downgrade-rejected-flow', 5, transaction_timestamp() - interval '1 minute',
			transaction_timestamp() + interval '4 minutes', transaction_timestamp()
		)
	`, "anonymous_challenges_consumption_shape_check")
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
	assertQuerySucceeds(t, ctx, runtimePool, "SELECT event_hash FROM read_audit_anchor('admin', 1)")
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
	assertQuerySucceeds(t, ctx, workerPool, "SELECT event_hash FROM read_audit_anchor('admin', 1)")
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
		  AND procedure.proname IN ('read_audit_head', 'read_audit_anchor', 'append_audit_event', 'reset_admin_account')
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
	if functionCount != 4 {
		t.Fatalf("expected 4 SECURITY DEFINER functions, got %d", functionCount)
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
