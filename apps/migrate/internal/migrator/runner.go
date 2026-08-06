package migrator

import (
	"context"
	"database/sql"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

const (
	ownerRoleSetting       = "game_night.owner_role"
	auditWriterRoleSetting = "game_night.audit_writer_role"
	migrationRoleSetting   = "game_night.migration_role"
	runtimeRoleSetting     = "game_night.runtime_role"
	workerRoleSetting      = "game_night.worker_role"
)

// RunCLI parses configuration and executes exactly one explicit migration command.
func RunCLI(ctx context.Context, args []string, lookupEnv LookupEnv, output io.Writer) error {
	config, command, err := ParseConfig(args, lookupEnv, output)
	if err != nil {
		return err
	}
	database, err := OpenDatabase(ctx, config)
	if err != nil {
		return err
	}
	defer database.Close()
	return Run(ctx, database, config.MigrationsDir, command)
}

// OpenDatabase applies schema and single-account role settings to every goose connection without logging the DSN.
func OpenDatabase(ctx context.Context, config Config) (*sql.DB, error) {
	connectionConfig, err := pgx.ParseConfig(config.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid migration database URL")
	}
	connectionConfig.RuntimeParams["search_path"] = pgx.Identifier{config.Schema}.Sanitize() + ",pg_catalog"
	roles := []string{config.OwnerRole, config.AuditWriterRole, config.MigrationRole, config.RuntimeRole, config.WorkerRole}
	if hasEmptyRole(roles) {
		currentUser, err := readCurrentUser(ctx, *connectionConfig)
		if err != nil {
			return nil, err
		}
		for index := range roles {
			if roles[index] == "" {
				roles[index] = currentUser
			}
		}
	}
	connectionConfig.RuntimeParams[ownerRoleSetting] = roles[0]
	connectionConfig.RuntimeParams[auditWriterRoleSetting] = roles[1]
	connectionConfig.RuntimeParams[migrationRoleSetting] = roles[2]
	connectionConfig.RuntimeParams[runtimeRoleSetting] = roles[3]
	connectionConfig.RuntimeParams[workerRoleSetting] = roles[4]

	database := stdlib.OpenDB(*connectionConfig)
	if err := database.PingContext(ctx); err != nil {
		database.Close()
		return nil, fmt.Errorf("connect migration database: %w", err)
	}
	if err := ensureSchema(ctx, database, config.Schema, roles[0]); err != nil {
		database.Close()
		return nil, err
	}
	var currentSchema string
	if err := database.QueryRowContext(ctx, "SELECT current_schema()").Scan(&currentSchema); err != nil {
		database.Close()
		return nil, fmt.Errorf("read migration schema: %w", err)
	}
	if currentSchema != config.Schema {
		database.Close()
		return nil, fmt.Errorf("migration schema %q does not exist or is not first in search_path", config.Schema)
	}
	return database, nil
}

// ensureSchema creates the isolated application schema for single-account deployments before goose opens it.
func ensureSchema(ctx context.Context, database *sql.DB, schema, owner string) error {
	var exists bool
	if err := database.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname = $1
		)
	`, schema).Scan(&exists); err != nil {
		return fmt.Errorf("inspect migration schema: %w", err)
	}
	if exists {
		return nil
	}
	statement := "CREATE SCHEMA " + pgx.Identifier{schema}.Sanitize() + " AUTHORIZATION " + pgx.Identifier{owner}.Sanitize()
	if _, err := database.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("create migration schema %q: %w", schema, err)
	}
	return nil
}

func hasEmptyRole(roles []string) bool {
	for _, role := range roles {
		if role == "" {
			return true
		}
	}
	return false
}

// readCurrentUser probes the shared DSN before applying startup role settings to goose connections.
func readCurrentUser(ctx context.Context, config pgx.ConnConfig) (string, error) {
	probe := stdlib.OpenDB(config)
	defer probe.Close()
	if err := probe.PingContext(ctx); err != nil {
		return "", fmt.Errorf("connect migration database: %w", err)
	}
	var currentUser string
	if err := probe.QueryRowContext(ctx, "SELECT current_user").Scan(&currentUser); err != nil || currentUser == "" {
		return "", fmt.Errorf("read migration database user")
	}
	return currentUser, nil
}

// Run invokes goose without allowing application startup to mutate schema implicitly.
func Run(ctx context.Context, database *sql.DB, migrationsDir, command string) error {
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("configure goose dialect: %w", err)
	}
	switch command {
	case "up":
		if err := goose.UpContext(ctx, database, migrationsDir); err != nil {
			return fmt.Errorf("apply migrations: %w", err)
		}
	case "down":
		if err := goose.DownContext(ctx, database, migrationsDir); err != nil {
			return fmt.Errorf("roll back migration: %w", err)
		}
	case "status":
		if err := goose.StatusContext(ctx, database, migrationsDir); err != nil {
			return fmt.Errorf("read migration status: %w", err)
		}
	default:
		return fmt.Errorf("unsupported migration command %q", command)
	}
	return nil
}
