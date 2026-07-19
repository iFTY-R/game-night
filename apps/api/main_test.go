package main

import (
	"testing"

	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
)

func TestDefaultCheckpointSinkFailsClosedInProduction(t *testing.T) {
	if defaultCheckpointSink(sharedconfig.EnvironmentProduction).Ready(t.Context()) {
		t.Fatal("production checkpoint sink was reported ready without a WORM worker probe")
	}
	if !defaultCheckpointSink(sharedconfig.EnvironmentDevelopment).Ready(t.Context()) {
		t.Fatal("development checkpoint sink should use the explicit local policy")
	}
}
