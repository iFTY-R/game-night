package gameruntime

import (
	"errors"
	"testing"

	game "github.com/iFTY-R/game-night/sdk/go/game"
)

func TestDisabledRegistryNeverResolvesOrDefaultsAModule(t *testing.T) {
	registry := NewDisabledRegistry()
	if _, err := registry.DefaultManifest(t.Context(), "liars-dice"); !errors.Is(err, ErrModuleUnavailable) {
		t.Fatalf("default manifest error = %v", err)
	}
	if _, err := registry.DefaultModule(t.Context(), "liars-dice"); !errors.Is(err, ErrModuleUnavailable) {
		t.Fatalf("default module error = %v", err)
	}
	if _, err := registry.Resolve(game.VersionKey{}); !errors.Is(err, ErrModuleUnavailable) {
		t.Fatalf("resolve error = %v", err)
	}
}
