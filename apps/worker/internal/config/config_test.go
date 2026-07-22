package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
)

func TestLoadComposesBoundedWorkerSettings(t *testing.T) {
	values := validWorkerEnvironment(t)
	values[workerInstanceEnvironment] = "worker-node-1"
	values[workerLeaseEnvironment] = "2m"
	values[workerBatchEnvironment] = "25"
	values[workerPollEnvironment] = "3s"
	values[workerShutdownEnvironment] = "20s"
	values[roomIdleEnvironment] = "12m"

	loaded, err := Load(mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Runtime.InstanceID != "worker-node-1" || loaded.Runtime.LeaseDuration != 2*time.Minute ||
		loaded.Runtime.BatchSize != 25 || loaded.Runtime.PollInterval != 3*time.Second ||
		loaded.Runtime.ShutdownTimeout != 20*time.Second || loaded.Runtime.RoomIdleTimeout != 12*time.Minute {
		t.Fatalf("unexpected worker runtime config: %+v", loaded.Runtime)
	}
	if loaded.CheckpointStorage.LocalDirectory == "" || loaded.Shared.PostgreSQL.Schema != "public" {
		t.Fatal("shared worker dependencies were not composed")
	}
}

func TestLoadDefaultsRoomIdleTimeout(t *testing.T) {
	loaded, err := Load(mapLookup(validWorkerEnvironment(t)))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Runtime.RoomIdleTimeout != 10*time.Minute {
		t.Fatalf("default room idle timeout=%v", loaded.Runtime.RoomIdleTimeout)
	}
}

func TestLoadRejectsUnsafeWorkerValuesWithoutEchoingThem(t *testing.T) {
	for name, value := range map[string]string{
		workerInstanceEnvironment: "secret owner with spaces",
		workerLeaseEnvironment:    "6m-secret",
		workerBatchEnvironment:    "1001-secret",
		workerPollEnvironment:     "61s-secret",
		workerShutdownEnvironment: "61s-secret",
		roomIdleEnvironment:       "30s-secret",
	} {
		t.Run(name, func(t *testing.T) {
			values := validWorkerEnvironment(t)
			values[name] = value
			_, err := Load(mapLookup(values))
			if err == nil || !strings.Contains(err.Error(), name) || strings.Contains(err.Error(), value) {
				t.Fatalf("unsafe worker config error = %v", err)
			}
		})
	}
}

func validWorkerEnvironment(t *testing.T) map[string]string {
	t.Helper()
	directory := t.TempDir()
	return map[string]string{
		"GAME_NIGHT_ENVIRONMENT":                "development",
		"GAME_NIGHT_DATABASE_URL":               "postgres://worker:database-secret@db.example.test/game_night?sslmode=require",
		"GAME_NIGHT_PII_KEYRING_FILE":           filepath.Join(directory, "pii.json"),
		"GAME_NIGHT_TOTP_KEYRING_FILE":          filepath.Join(directory, "totp.json"),
		"GAME_NIGHT_AUDIT_KEYRING_FILE":         filepath.Join(directory, "audit.json"),
		"GAME_NIGHT_CHECKPOINT_SINK":            "local",
		"GAME_NIGHT_CHECKPOINT_LOCAL_DIRECTORY": filepath.Join(directory, "checkpoints"),
		workerInstanceEnvironment:               "worker-test",
	}
}

func mapLookup(values map[string]string) sharedconfig.LookupEnv {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
