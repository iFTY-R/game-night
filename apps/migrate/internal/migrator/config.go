// Package migrator owns the explicit goose process used before API and worker deployments.
package migrator

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const (
	databaseURLEnvironment    = "GAME_NIGHT_DATABASE_URL"
	databaseSchemaEnvironment = "GAME_NIGHT_DATABASE_SCHEMA"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)

// LookupEnv matches os.LookupEnv while allowing deterministic CLI tests.
type LookupEnv func(string) (string, bool)

// Config contains the shared DSN and schema. Role fields remain available for integration fixtures;
// production parsing leaves them empty so OpenDatabase derives every role from current_user.
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

// ParseConfig validates the command and shared database settings before opening PostgreSQL.
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
		DatabaseURL:   requiredEnvironment(lookupEnv, databaseURLEnvironment),
		Schema:        environmentOrDefault(lookupEnv, databaseSchemaEnvironment, "public"),
		MigrationsDir: strings.TrimSpace(*migrationsDir),
		AllowDown:     *allowDown,
	}
	if config.DatabaseURL == "" {
		return Config{}, "", fmt.Errorf("missing required migration environment: %s", databaseURLEnvironment)
	}
	if config.MigrationsDir == "" {
		return Config{}, "", errors.New("migration directory cannot be empty")
	}
	if !identifierPattern.MatchString(config.Schema) {
		return Config{}, "", errors.New("schema must be an unquoted PostgreSQL identifier")
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
