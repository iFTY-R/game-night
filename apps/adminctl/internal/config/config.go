package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"
)

const (
	DatabaseURLEnvironment  = "GAME_NIGHT_MIGRATION_DATABASE_URL"
	SchemaEnvironment       = "GAME_NIGHT_DATABASE_SCHEMA"
	AuditKeyringEnvironment = "GAME_NIGHT_AUDIT_KEYRING_FILE"
	confirmationValue       = "RESET-ADMIN"
)

var ErrInvalidConfig = errors.New("invalid adminctl configuration")

// Config contains only non-secret CLI paths and the migration-role DSN loaded from the process environment.
type Config struct {
	DatabaseURL  string
	Schema       string
	AuditKeyring string
	SecretFile   string
	Confirm      string
	DryRun       bool
}

// Parse validates the command, secret-file path, and explicit confirmation before opening any dependency.
func Parse(args []string, lookup func(string) (string, bool), output io.Writer) (Config, string, error) {
	if lookup == nil {
		return Config{}, "", ErrInvalidConfig
	}
	flags := flag.NewFlagSet("game-night-adminctl", flag.ContinueOnError)
	flags.SetOutput(output)
	secretFile := flags.String("secret-file", "", "one-time administrator password file")
	confirm := flags.String("confirm", "", "explicit reset confirmation")
	dryRun := flags.Bool("dry-run", false, "validate inputs without changing the database")
	command, flagArgs, ok := splitCommand(args)
	if !ok || strings.ToLower(command) != "reset" || flags.Parse(flagArgs) != nil || flags.NArg() != 0 {
		return Config{}, "", ErrInvalidConfig
	}
	databaseURL := value(lookup, DatabaseURLEnvironment)
	schema := value(lookup, SchemaEnvironment)
	if schema == "" {
		schema = "public"
	}
	auditKeyring := value(lookup, AuditKeyringEnvironment)
	if databaseURL == "" || !validDatabaseURL(databaseURL) || !validIdentifier(schema) || auditKeyring == "" || *secretFile == "" {
		return Config{}, "", ErrInvalidConfig
	}
	auditKeyring, err := filepath.Abs(auditKeyring)
	if err != nil {
		return Config{}, "", ErrInvalidConfig
	}
	secretPath, err := filepath.Abs(*secretFile)
	if err != nil {
		return Config{}, "", ErrInvalidConfig
	}
	if !*dryRun && *confirm != confirmationValue {
		return Config{}, "", ErrInvalidConfig
	}
	return Config{DatabaseURL: databaseURL, Schema: schema, AuditKeyring: auditKeyring, SecretFile: secretPath, Confirm: *confirm, DryRun: *dryRun}, "reset", nil
}

func splitCommand(args []string) (string, []string, bool) {
	command := ""
	flags := make([]string, 0, len(args))
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") && command == "" {
			command = arg
			continue
		}
		flags = append(flags, arg)
	}
	return command, flags, command != ""
}

func value(lookup func(string) (string, bool), name string) string {
	value, _ := lookup(name)
	return strings.TrimSpace(value)
}

func validDatabaseURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "postgres" || parsed.Scheme == "postgresql") && parsed.Host != "" && parsed.Path != ""
}

func validIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for index, current := range value {
		if !(current == '_' || current >= 'a' && current <= 'z' || current >= 'A' && current <= 'Z' || index > 0 && current >= '0' && current <= '9') {
			return false
		}
	}
	return true
}

func fieldError(name string) error { return fmt.Errorf("%s: invalid adminctl configuration", name) }
