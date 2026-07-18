package integrationtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

const (
	// testDatabaseURLEnvironment points at the ordinary shared PostgreSQL test service.
	testDatabaseURLEnvironment = "GAME_NIGHT_TEST_DATABASE_URL"
	// testAdminDatabaseURLEnvironment points at a cluster administrator used only by privilege tests.
	testAdminDatabaseURLEnvironment = "GAME_NIGHT_TEST_ADMIN_DATABASE_URL"
	// postgresCleanupTimeout bounds fixture setup and cleanup so failed services cannot hang the test process.
	postgresCleanupTimeout = 15 * time.Second
)

// PostgresSchema owns one random schema and pools whose search_path starts at that schema.
type PostgresSchema struct {
	Name string
	Pool *pgxpool.Pool

	// databaseURL is retained only to open goose's database/sql handle with the same schema binding.
	databaseURL string
}

// OpenPostgresSchema creates an isolated schema or explicitly skips when local PostgreSQL is not configured.
func OpenPostgresSchema(t testing.TB) *PostgresSchema {
	t.Helper()

	databaseURL := RequireEnvironment(t, DependencyPostgres, testDatabaseURLEnvironment)[0]
	ctx, cancel := context.WithTimeout(context.Background(), postgresCleanupTimeout)
	defer cancel()

	adminPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("parse PostgreSQL test URL: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("connect to PostgreSQL test service: %v", err)
	}

	schemaName := randomIdentifier(t, "gn_schema_")
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schemaName}.Sanitize()); err != nil {
		adminPool.Close()
		t.Fatalf("create PostgreSQL test schema: %v", err)
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		adminPool.Close()
		t.Fatalf("parse PostgreSQL schema pool URL: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schemaName + ",pg_catalog"
	schemaPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		adminPool.Close()
		t.Fatalf("create PostgreSQL schema pool: %v", err)
	}
	if err := schemaPool.Ping(ctx); err != nil {
		schemaPool.Close()
		adminPool.Close()
		t.Fatalf("connect to PostgreSQL schema pool: %v", err)
	}

	fixture := &PostgresSchema{Name: schemaName, Pool: schemaPool, databaseURL: databaseURL}
	t.Cleanup(func() {
		schemaPool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), postgresCleanupTimeout)
		defer cleanupCancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+pgx.Identifier{schemaName}.Sanitize()+" CASCADE"); err != nil {
			t.Errorf("drop PostgreSQL test schema: %v", err)
		}
		adminPool.Close()
	})
	return fixture
}

// OpenSQLDB returns a database/sql handle bound to the fixture schema for goose migrations.
func (fixture *PostgresSchema) OpenSQLDB(t testing.TB, runtimeParams map[string]string) *sql.DB {
	t.Helper()

	config, err := pgx.ParseConfig(fixture.databaseURL)
	if err != nil {
		t.Fatalf("parse PostgreSQL SQL DB URL: %v", err)
	}
	config.RuntimeParams["search_path"] = fixture.Name + ",pg_catalog"
	for name, value := range runtimeParams {
		config.RuntimeParams[name] = value
	}
	database := stdlib.OpenDB(*config)
	ctx, cancel := context.WithTimeout(context.Background(), postgresCleanupTimeout)
	defer cancel()
	if err := database.PingContext(ctx); err != nil {
		database.Close()
		t.Fatalf("connect database/sql fixture: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close database/sql fixture: %v", err)
		}
	})
	return database
}

// PrivilegeDatabase contains isolated login URLs and role names for least-privilege tests.
type PrivilegeDatabase struct {
	DatabaseName    string
	Schema          string
	OwnerRole       string
	AuditWriterRole string
	MigrationRole   string
	RuntimeRole     string
	WorkerRole      string
	MigrationURL    string
	RuntimeURL      string
	WorkerURL       string
}

// OpenPrivilegeDatabase creates a random database and cluster roles using the administrator test URL.
func OpenPrivilegeDatabase(t testing.TB) *PrivilegeDatabase {
	t.Helper()

	adminURL := RequireEnvironment(t, DependencyPostgresPrivileges, testAdminDatabaseURLEnvironment)[0]
	ctx, cancel := context.WithTimeout(context.Background(), postgresCleanupTimeout)
	defer cancel()
	adminConfig, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatalf("parse PostgreSQL administrator URL: %v", err)
	}
	adminPool, err := pgxpool.NewWithConfig(ctx, adminConfig)
	if err != nil {
		t.Fatalf("create PostgreSQL administrator pool: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("connect to PostgreSQL administrator service: %v", err)
	}

	suffix := randomIdentifier(t, "")
	fixture := &PrivilegeDatabase{
		DatabaseName:    "gn_db_" + suffix,
		Schema:          "gn_schema_" + suffix,
		OwnerRole:       "gn_owner_" + suffix,
		AuditWriterRole: "gn_audit_" + suffix,
		MigrationRole:   "gn_migrate_" + suffix,
		RuntimeRole:     "gn_runtime_" + suffix,
		WorkerRole:      "gn_worker_" + suffix,
	}
	migrationPassword := randomPassword(t)
	runtimePassword := randomPassword(t)
	workerPassword := randomPassword(t)

	t.Cleanup(func() {
		cleanupPrivilegeDatabase(t, adminPool, fixture)
	})

	createRole(t, ctx, adminPool, fixture.OwnerRole, "")
	createRole(t, ctx, adminPool, fixture.AuditWriterRole, "")
	createRole(t, ctx, adminPool, fixture.MigrationRole, migrationPassword)
	createRole(t, ctx, adminPool, fixture.RuntimeRole, runtimePassword)
	createRole(t, ctx, adminPool, fixture.WorkerRole, workerPassword)
	grantRole(t, ctx, adminPool, fixture.OwnerRole, fixture.MigrationRole)
	grantRole(t, ctx, adminPool, fixture.AuditWriterRole, fixture.RuntimeRole)
	grantRole(t, ctx, adminPool, fixture.AuditWriterRole, fixture.WorkerRole)
	createDatabase(t, ctx, adminPool, fixture.DatabaseName, fixture.MigrationRole)

	fixture.MigrationURL = roleDatabaseURL(t, adminConfig.ConnConfig, fixture.DatabaseName, fixture.Schema, fixture.MigrationRole, migrationPassword)
	fixture.RuntimeURL = roleDatabaseURL(t, adminConfig.ConnConfig, fixture.DatabaseName, fixture.Schema, fixture.RuntimeRole, runtimePassword)
	fixture.WorkerURL = roleDatabaseURL(t, adminConfig.ConnConfig, fixture.DatabaseName, fixture.Schema, fixture.WorkerRole, workerPassword)
	createSchema(t, ctx, fixture.MigrationURL, fixture.Schema)
	return fixture
}

func createRole(t testing.TB, ctx context.Context, pool *pgxpool.Pool, role, password string) {
	t.Helper()

	template := "CREATE ROLE %I NOLOGIN"
	args := []any{role}
	if password != "" {
		template = "CREATE ROLE %I LOGIN PASSWORD %L"
		args = append(args, password)
	}
	execFormattedStatement(t, ctx, pool, template, args...)
}

func grantRole(t testing.TB, ctx context.Context, pool *pgxpool.Pool, grantedRole, memberRole string) {
	t.Helper()
	execFormattedStatement(t, ctx, pool, "GRANT %I TO %I", grantedRole, memberRole)
}

func createDatabase(t testing.TB, ctx context.Context, pool *pgxpool.Pool, databaseName, ownerRole string) {
	t.Helper()
	execFormattedStatement(t, ctx, pool, "CREATE DATABASE %I OWNER %I", databaseName, ownerRole)
}

func createSchema(t testing.TB, ctx context.Context, databaseURL, schemaName string) {
	t.Helper()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("create PostgreSQL schema pool: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schemaName}.Sanitize()); err != nil {
		t.Fatalf("create PostgreSQL privilege schema: %v", err)
	}
}

func execFormattedStatement(t testing.TB, ctx context.Context, pool *pgxpool.Pool, template string, args ...any) {
	t.Helper()

	placeholders := make([]string, len(args)+1)
	placeholders[0] = "$1"
	queryArgs := make([]any, 0, len(args)+1)
	queryArgs = append(queryArgs, template)
	for index, argument := range args {
		placeholders[index+1] = fmt.Sprintf("$%d", index+2)
		queryArgs = append(queryArgs, argument)
	}
	var statement string
	if err := pool.QueryRow(ctx, "SELECT pg_catalog.format("+strings.Join(placeholders, ", ")+")", queryArgs...).Scan(&statement); err != nil {
		t.Fatalf("format PostgreSQL fixture statement: %v", err)
	}
	if _, err := pool.Exec(ctx, statement); err != nil {
		t.Fatalf("execute PostgreSQL fixture statement: %v", err)
	}
}

func roleDatabaseURL(t testing.TB, base *pgx.ConnConfig, databaseName, schemaName, role, password string) string {
	t.Helper()

	config := base.Copy()
	config.Database = databaseName
	config.User = role
	config.Password = password
	config.RuntimeParams = map[string]string{
		"search_path": pgx.Identifier{schemaName}.Sanitize() + ",pg_catalog",
	}
	return config.ConnString()
}

func cleanupPrivilegeDatabase(t testing.TB, adminPool *pgxpool.Pool, fixture *PrivilegeDatabase) {
	t.Helper()
	defer adminPool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), postgresCleanupTimeout)
	defer cancel()
	if _, err := adminPool.Exec(ctx, "SELECT pg_catalog.pg_terminate_backend(pid) FROM pg_catalog.pg_stat_activity WHERE datname = $1 AND pid <> pg_catalog.pg_backend_pid()", fixture.DatabaseName); err != nil {
		t.Errorf("terminate PostgreSQL fixture connections: %v", err)
	}
	for _, statement := range []struct {
		template string
		name     string
	}{
		{template: "DROP DATABASE IF EXISTS %I", name: fixture.DatabaseName},
		{template: "DROP ROLE IF EXISTS %I", name: fixture.WorkerRole},
		{template: "DROP ROLE IF EXISTS %I", name: fixture.RuntimeRole},
		{template: "DROP ROLE IF EXISTS %I", name: fixture.MigrationRole},
		{template: "DROP ROLE IF EXISTS %I", name: fixture.AuditWriterRole},
		{template: "DROP ROLE IF EXISTS %I", name: fixture.OwnerRole},
	} {
		var sqlStatement string
		if err := adminPool.QueryRow(ctx, "SELECT pg_catalog.format($1, $2)", statement.template, statement.name).Scan(&sqlStatement); err != nil {
			t.Errorf("format PostgreSQL cleanup statement: %v", err)
			continue
		}
		if _, err := adminPool.Exec(ctx, sqlStatement); err != nil {
			t.Errorf("execute PostgreSQL cleanup statement: %v", err)
		}
	}
}

func randomIdentifier(t testing.TB, prefix string) string {
	t.Helper()

	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		t.Fatalf("generate PostgreSQL fixture identifier: %v", err)
	}
	return prefix + hex.EncodeToString(buffer)
}

func randomPassword(t testing.TB) string {
	t.Helper()

	buffer := make([]byte, 24)
	if _, err := rand.Read(buffer); err != nil {
		t.Fatalf("generate PostgreSQL fixture password: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer)
}
