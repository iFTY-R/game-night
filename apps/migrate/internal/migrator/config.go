// Package migrator owns the explicit goose process used before API and worker deployments.
package migrator

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
)

const (
	databaseURLEnvironment     = "GAME_NIGHT_MIGRATION_DATABASE_URL"
	databaseSchemaEnvironment  = "GAME_NIGHT_DATABASE_SCHEMA"
	ownerRoleEnvironment       = "GAME_NIGHT_DATABASE_OWNER_ROLE"
	auditWriterRoleEnvironment = "GAME_NIGHT_DATABASE_AUDIT_WRITER_ROLE"
	migrationRoleEnvironment   = "GAME_NIGHT_DATABASE_MIGRATION_ROLE"
	runtimeRoleEnvironment     = "GAME_NIGHT_DATABASE_RUNTIME_ROLE"
	workerRoleEnvironment      = "GAME_NIGHT_DATABASE_WORKER_ROLE"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)

// LookupEnv matches os.LookupEnv while allowing deterministic CLI tests.
type LookupEnv func(string) (string, bool)

// Config contains non-secret identifiers plus the migration DSN loaded from the process environment.
type Config struct {
	DatabaseURL     string
	Schema          string
	OwnerRole       string
	AuditWriterRole string
	MigrationRole   string
	RuntimeRole     string
	WorkerRole      string
	MigrationsDir   string
	AllowDown       bool
}

// ParseConfig validates the command and all role bindings before opening PostgreSQL.
func ParseConfig(args []string, lookupEnv LookupEnv, output io.Writer) (Config, string, error) {
	flags := flag.NewFlagSet("game-night-migrate", flag.ContinueOnError)
	flags.SetOutput(output)
	migrationsDir := flags.String("dir", "infra/migrations", "directory containing goose SQL migrations")
	allowDown := flags.Bool("allow-destructive-down", false, "allow a destructive rollback in a non-production environment")
	if err := flags.Parse(args); err != nil {
		return Config{}, "", err
	}
	if flags.NArg() != 1 {
		return Config{}, "", errors.New("usage: game-night-migrate [flags] <up|down|status>")
	}
	command := strings.ToLower(flags.Arg(0))
	if command != "up" && command != "down" && command != "status" {
		return Config{}, "", fmt.Errorf("unsupported migration command %q", command)
	}
	if command == "down" && !*allowDown {
		return Config{}, "", errors.New("down requires -allow-destructive-down and is forbidden for production data")
	}

	config := Config{
		DatabaseURL:     requiredEnvironment(lookupEnv, databaseURLEnvironment),
		Schema:          environmentOrDefault(lookupEnv, databaseSchemaEnvironment, "public"),
		OwnerRole:       requiredEnvironment(lookupEnv, ownerRoleEnvironment),
		AuditWriterRole: requiredEnvironment(lookupEnv, auditWriterRoleEnvironment),
		MigrationRole:   requiredEnvironment(lookupEnv, migrationRoleEnvironment),
		RuntimeRole:     requiredEnvironment(lookupEnv, runtimeRoleEnvironment),
		WorkerRole:      requiredEnvironment(lookupEnv, workerRoleEnvironment),
		MigrationsDir:   strings.TrimSpace(*migrationsDir),
		AllowDown:       *allowDown,
	}
	missing := make([]string, 0)
	for name, value := range map[string]string{
		databaseURLEnvironment:     config.DatabaseURL,
		ownerRoleEnvironment:       config.OwnerRole,
		auditWriterRoleEnvironment: config.AuditWriterRole,
		migrationRoleEnvironment:   config.MigrationRole,
		runtimeRoleEnvironment:     config.RuntimeRole,
		workerRoleEnvironment:      config.WorkerRole,
	} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return Config{}, "", fmt.Errorf("missing required migration environment: %s", strings.Join(missing, ", "))
	}
	if config.MigrationsDir == "" {
		return Config{}, "", errors.New("migration directory cannot be empty")
	}
	for name, value := range map[string]string{
		"schema":            config.Schema,
		"owner role":        config.OwnerRole,
		"audit writer role": config.AuditWriterRole,
		"migration role":    config.MigrationRole,
		"runtime role":      config.RuntimeRole,
		"worker role":       config.WorkerRole,
	} {
		if !identifierPattern.MatchString(value) {
			return Config{}, "", fmt.Errorf("%s must be an unquoted PostgreSQL identifier", name)
		}
	}
	return config, command, nil
}

func requiredEnvironment(lookupEnv LookupEnv, name string) string {
	value, _ := lookupEnv(name)
	return strings.TrimSpace(value)
}

func environmentOrDefault(lookupEnv LookupEnv, name, fallback string) string {
	if value := requiredEnvironment(lookupEnv, name); value != "" {
		return value
	}
	return fallback
}
