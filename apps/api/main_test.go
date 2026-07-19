package main

import (
	"path/filepath"
	"testing"

	apiConfig "github.com/iFTY-R/game-night/apps/api/internal/config"
	"github.com/iFTY-R/game-night/apps/internal/checkpointstorage"
	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
)

func TestCheckpointReadinessUsesRealSinkPolicy(t *testing.T) {
	storage := checkpointstorage.Config{Kind: checkpointstorage.SinkLocal, LocalDirectory: filepath.Join(t.TempDir(), "checkpoints")}
	development, err := buildCheckpointReadiness(t.Context(), apiConfig.Config{
		Shared: sharedconfig.Config{Environment: sharedconfig.EnvironmentDevelopment}, CheckpointStorage: storage,
	})
	if err != nil || !development.Ready(t.Context()) {
		t.Fatalf("development checkpoint readiness failed: %v", err)
	}
	production, err := buildCheckpointReadiness(t.Context(), apiConfig.Config{
		Shared: sharedconfig.Config{Environment: sharedconfig.EnvironmentProduction}, CheckpointStorage: storage,
	})
	if err != nil {
		t.Fatal(err)
	}
	if production.Ready(t.Context()) {
		t.Fatal("production checkpoint sink was reported ready without a WORM worker probe")
	}
}
