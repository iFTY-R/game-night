package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
)

func TestLoadCombinesSharedAndAPIConfiguration(t *testing.T) {
	environment := validAPIEnvironment(t)
	environment[listenAddressEnvironment] = "127.0.0.1:9090"
	environment[readHeaderTimeoutEnvironment] = "3s"
	environment[readTimeoutEnvironment] = "12s"
	environment[writeTimeoutEnvironment] = "25s"
	environment[idleTimeoutEnvironment] = "45s"
	environment[shutdownTimeoutEnvironment] = "20s"
	environment[maxHeaderBytesEnvironment] = "524288"
	environment[argon2WorkersEnvironment] = "4"
	environment[argon2QueueEnvironment] = "128"

	loaded, err := Load(mapLookup(environment))
	if err != nil {
		t.Fatal(err)
	}

	if loaded.Shared.PostgreSQL.Schema != "public" || loaded.Shared.Redis.KeyPrefix != "game-night:api-test:" {
		t.Fatalf("shared configuration was not composed: %+v", loaded.Shared)
	}
	if loaded.Listener.Address != "127.0.0.1:9090" || loaded.Listener.ReadHeaderTimeout != 3*time.Second || loaded.Listener.ReadTimeout != 12*time.Second || loaded.Listener.WriteTimeout != 25*time.Second {
		t.Fatalf("unexpected API listener config: %+v", loaded.Listener)
	}
	if loaded.Listener.IdleTimeout != 45*time.Second || loaded.Listener.ShutdownTimeout != 20*time.Second || loaded.Listener.MaxHeaderBytes != 524288 {
		t.Fatalf("unexpected API resource limits: %+v", loaded.Listener)
	}
	if loaded.Argon2 != (Argon2Config{Workers: 4, QueueCapacity: 128}) {
		t.Fatalf("unexpected Argon2 config: %+v", loaded.Argon2)
	}
	if loaded.Realtime.BootstrapURL != defaultRealtimeBootstrapURL || len(loaded.Realtime.PeerURLs) != 1 ||
		loaded.Realtime.PeerURLs[0] != defaultRealtimeBootstrapURL {
		t.Fatalf("unexpected realtime config: %+v", loaded.Realtime)
	}
	if loaded.CheckpointStorage.LocalDirectory == "" {
		t.Fatal("checkpoint storage configuration was not composed")
	}
}

func TestLoadUsesBoundedAPIListenerDefaults(t *testing.T) {
	loaded, err := Load(mapLookup(validAPIEnvironment(t)))
	if err != nil {
		t.Fatal(err)
	}

	want := ListenerConfig{
		Address:           ":8080",
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       time.Minute,
		ShutdownTimeout:   15 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	if loaded.Listener != want {
		t.Fatalf("unexpected listener defaults: got %+v want %+v", loaded.Listener, want)
	}
	if loaded.Argon2 != (Argon2Config{Workers: 2, QueueCapacity: 64}) {
		t.Fatalf("unexpected Argon2 defaults: %+v", loaded.Argon2)
	}
}

func TestLoadRealtimeRoutingRequiresAllowlistedTLSPeersInProduction(t *testing.T) {
	environment := validAPIEnvironment(t)
	environment[realtimeBootstrapURLEnvironment] = "https://realtime-a.internal:8091"
	environment[realtimePeerURLsEnvironment] = "https://realtime-a.internal:8091,https://realtime-b.internal:8091"
	reader := environmentReader{lookup: mapLookup(environment)}
	loaded, err := loadRealtime(reader, sharedconfig.EnvironmentProduction)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.PeerURLs) != 2 {
		t.Fatalf("realtime peers = %v", loaded.PeerURLs)
	}
	environment[realtimePeerURLsEnvironment] = "https://realtime-b.internal:8091"
	if _, err := loadRealtime(reader, sharedconfig.EnvironmentProduction); err == nil || !strings.Contains(err.Error(), realtimePeerURLsEnvironment) {
		t.Fatalf("missing bootstrap allowlist error = %v", err)
	}
}

func TestLoadRejectsInvalidAPIOptionsWithoutLeakingValues(t *testing.T) {
	tests := []struct {
		name        string
		environment string
		value       string
	}{
		{name: "listen address", environment: listenAddressEnvironment, value: "secret-listener"},
		{name: "read header timeout", environment: readHeaderTimeoutEnvironment, value: "secret-read-header"},
		{name: "read timeout", environment: readTimeoutEnvironment, value: "secret-read"},
		{name: "write timeout", environment: writeTimeoutEnvironment, value: "secret-write"},
		{name: "idle timeout", environment: idleTimeoutEnvironment, value: "secret-idle"},
		{name: "shutdown timeout", environment: shutdownTimeoutEnvironment, value: "secret-shutdown"},
		{name: "max header bytes", environment: maxHeaderBytesEnvironment, value: "secret-header-size"},
		{name: "Argon2 workers", environment: argon2WorkersEnvironment, value: "secret-workers"},
		{name: "Argon2 queue", environment: argon2QueueEnvironment, value: "secret-queue"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := validAPIEnvironment(t)
			environment[test.environment] = test.value
			_, err := Load(mapLookup(environment))
			if err == nil || !strings.Contains(err.Error(), test.environment) {
				t.Fatalf("expected field-only error for %s, got %v", test.environment, err)
			}
			if strings.Contains(err.Error(), test.value) {
				t.Fatalf("API error leaked configured value for %s: %v", test.environment, err)
			}
		})
	}
}

func TestLoadRejectsExcessiveAPIResourceLimits(t *testing.T) {
	tests := []struct {
		environment string
		value       string
	}{
		{environment: readHeaderTimeoutEnvironment, value: "31s"},
		{environment: readTimeoutEnvironment, value: "2m1s"},
		{environment: writeTimeoutEnvironment, value: "2m1s"},
		{environment: idleTimeoutEnvironment, value: "5m1s"},
		{environment: shutdownTimeoutEnvironment, value: "1m1s"},
		{environment: maxHeaderBytesEnvironment, value: "4194305"},
		{environment: argon2WorkersEnvironment, value: "9"},
		{environment: argon2QueueEnvironment, value: "4097"},
	}

	for _, test := range tests {
		t.Run(test.environment, func(t *testing.T) {
			environment := validAPIEnvironment(t)
			environment[test.environment] = test.value
			_, err := Load(mapLookup(environment))
			if err == nil || !strings.Contains(err.Error(), test.environment) || strings.Contains(err.Error(), test.value) {
				t.Fatalf("expected safe bounded-resource error for %s, got %v", test.environment, err)
			}
		})
	}
}

func validAPIEnvironment(t *testing.T) map[string]string {
	t.Helper()
	secretDirectory := t.TempDir()
	return map[string]string{
		"GAME_NIGHT_ENVIRONMENT":                  "development",
		"GAME_NIGHT_DATABASE_URL":                 "postgres://runtime:database-secret@db.example.test/game_night?sslmode=require",
		"GAME_NIGHT_REDIS_URL":                    "rediss://:redis-secret@redis.example.test/0",
		"GAME_NIGHT_REDIS_KEY_PREFIX":             "game-night:api-test:",
		"GAME_NIGHT_USER_ORIGINS":                 "http://localhost:3000",
		"GAME_NIGHT_ADMIN_ORIGINS":                "http://localhost:3001",
		"GAME_NIGHT_TRUSTED_PROXY_CIDRS":          "127.0.0.1/32,::1/128",
		"GAME_NIGHT_PII_KEYRING_FILE":             filepath.Join(secretDirectory, "pii.json"),
		"GAME_NIGHT_TOTP_KEYRING_FILE":            filepath.Join(secretDirectory, "totp.json"),
		"GAME_NIGHT_RESULT_ENVELOPE_KEYRING_FILE": filepath.Join(secretDirectory, "result-envelope.json"),
		"GAME_NIGHT_DEVICE_KEYRING_FILE":          filepath.Join(secretDirectory, "device.json"),
		"GAME_NIGHT_RATE_LIMIT_KEYRING_FILE":      filepath.Join(secretDirectory, "rate-limit.json"),
		"GAME_NIGHT_USER_CHALLENGE_KEYRING_FILE":  filepath.Join(secretDirectory, "user-challenge.json"),
		"GAME_NIGHT_ADMIN_CHALLENGE_KEYRING_FILE": filepath.Join(secretDirectory, "admin-challenge.json"),
		"GAME_NIGHT_ADMIN_SESSION_KEYRING_FILE":   filepath.Join(secretDirectory, "admin-session.json"),
		"GAME_NIGHT_AUDIT_KEYRING_FILE":           filepath.Join(secretDirectory, "audit.json"),
		"GAME_NIGHT_CHECKPOINT_SINK":              "local",
		"GAME_NIGHT_CHECKPOINT_LOCAL_DIRECTORY":   filepath.Join(secretDirectory, "checkpoints"),
		realtimeInternalTokenEnvironment:          strings.Repeat("r", 32),
	}
}

func mapLookup(values map[string]string) sharedconfig.LookupEnv {
	return func(name string) (string, bool) {
		value, exists := values[name]
		return value, exists
	}
}
