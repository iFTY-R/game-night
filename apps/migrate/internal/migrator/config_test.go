package migrator

import (
	"io"
	"strings"
	"testing"
)

func TestParseConfig(t *testing.T) {
	environment := validEnvironment()
	config, command, err := ParseConfig([]string{"up"}, mapLookup(environment), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if command != "up" || config.Schema != "public" || config.DatabaseURL != environment[databaseURLEnvironment] {
		t.Fatalf("unexpected parsed configuration: command=%s config=%+v", command, config)
	}
}

func TestParseConfigRejectsDownWithoutExplicitFlag(t *testing.T) {
	_, _, err := ParseConfig([]string{"down"}, mapLookup(validEnvironment()), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "allow-destructive-down") {
		t.Fatalf("expected destructive down rejection, got %v", err)
	}
}

func TestParseConfigRejectsInvalidSchemaWithoutLeakingDSN(t *testing.T) {
	environment := validEnvironment()
	environment[databaseSchemaEnvironment] = "invalid-schema;drop"
	_, _, err := ParseConfig([]string{"up"}, mapLookup(environment), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "schema") {
		t.Fatalf("expected schema validation error, got %v", err)
	}
	if strings.Contains(err.Error(), environment[databaseURLEnvironment]) {
		t.Fatal("migration validation error leaked database URL")
	}
}

func TestParseConfigReportsAllMissingEnvironmentNames(t *testing.T) {
	_, _, err := ParseConfig([]string{"status"}, mapLookup(map[string]string{}), io.Discard)
	if err == nil {
		t.Fatal("expected missing environment error")
	}
	if !strings.Contains(err.Error(), databaseURLEnvironment) {
		t.Errorf("missing environment error did not include %s: %v", databaseURLEnvironment, err)
	}
}

func validEnvironment() map[string]string {
	return map[string]string{
		databaseURLEnvironment: "postgres://secret@example.invalid/game_night",
	}
}

func mapLookup(values map[string]string) LookupEnv {
	return func(name string) (string, bool) {
		value, exists := values[name]
		return value, exists
	}
}
