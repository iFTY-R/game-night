package config

import (
	"io"
	"strings"
	"testing"
)

func TestParseRequiresExplicitConfirmationUnlessDryRun(t *testing.T) {
	lookup := func(name string) (string, bool) {
		values := map[string]string{
			DatabaseURLEnvironment:  "postgres://migration:secret@db.example.test/game_night",
			SchemaEnvironment:       "public",
			AuditKeyringEnvironment: "/run/secrets/audit.json",
		}
		value, ok := values[name]
		return value, ok
	}
	if _, _, err := Parse([]string{"reset", "--secret-file", "secret.txt"}, lookup, io.Discard); err == nil {
		t.Fatal("reset without confirmation was accepted")
	}
	config, command, err := Parse([]string{"reset", "--secret-file", "secret.txt", "--dry-run"}, lookup, io.Discard)
	if err != nil || command != "reset" || !config.DryRun {
		t.Fatalf("dry run parse = %+v, %q, %v", config, command, err)
	}
}

func TestParseNeverEchoesSecretConfiguration(t *testing.T) {
	lookup := func(name string) (string, bool) {
		if name == DatabaseURLEnvironment {
			return "not-a-dsn-with-secret", true
		}
		return "", false
	}
	_, _, err := Parse([]string{"reset", "--secret-file", "password-value"}, lookup, io.Discard)
	if err == nil || strings.Contains(err.Error(), "password-value") || strings.Contains(err.Error(), "not-a-dsn-with-secret") {
		t.Fatalf("unsafe adminctl error = %v", err)
	}
}
