package postgres

import (
	"strings"
	"testing"
	"time"
)

func TestPoolConfigParse(t *testing.T) {
	config, err := (PoolConfig{
		DatabaseURL:       "postgres://runtime:secret@example.invalid/game_night",
		Schema:            "game_night",
		MinConnections:    2,
		MaxConnections:    8,
		MaxConnectionAge:  30 * time.Minute,
		MaxConnectionIdle: 5 * time.Minute,
		HealthCheckPeriod: 15 * time.Second,
	}).Parse()
	if err != nil {
		t.Fatal(err)
	}
	if config.MinConns != 2 || config.MaxConns != 8 {
		t.Fatalf("unexpected pool sizes: min=%d max=%d", config.MinConns, config.MaxConns)
	}
	if config.MaxConnLifetime != 30*time.Minute || config.MaxConnIdleTime != 5*time.Minute {
		t.Fatalf("unexpected pool lifetimes: max=%s idle=%s", config.MaxConnLifetime, config.MaxConnIdleTime)
	}
	if got := config.ConnConfig.RuntimeParams["search_path"]; got != `"game_night",pg_catalog` {
		t.Fatalf("unexpected search_path %q", got)
	}
	if got := config.ConnConfig.RuntimeParams["timezone"]; got != "UTC" {
		t.Fatalf("unexpected timezone %q", got)
	}
}

func TestPoolConfigRejectsUnsafeValuesWithoutLeakingURL(t *testing.T) {
	databaseURL := "postgres://runtime:do-not-log@example.invalid/game_night"
	tests := []struct {
		name   string
		config PoolConfig
		field  string
	}{
		{
			name:   "missing database URL",
			config: PoolConfig{Schema: "game_night"},
			field:  "database URL",
		},
		{
			name:   "invalid schema",
			config: PoolConfig{DatabaseURL: databaseURL, Schema: "public, attacker"},
			field:  "schema",
		},
		{
			name: "minimum exceeds maximum",
			config: PoolConfig{
				DatabaseURL:    databaseURL,
				Schema:         "game_night",
				MinConnections: 4,
				MaxConnections: 2,
			},
			field: "connections",
		},
		{
			name: "negative duration",
			config: PoolConfig{
				DatabaseURL:      databaseURL,
				Schema:           "game_night",
				MaxConnectionAge: -time.Second,
			},
			field: "duration",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.config.Parse()
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.field)) {
				t.Fatalf("expected %s error, got %v", test.field, err)
			}
			if strings.Contains(err.Error(), databaseURL) || strings.Contains(err.Error(), "do-not-log") {
				t.Fatalf("pool configuration error leaked database URL: %v", err)
			}
		})
	}
}
