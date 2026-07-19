package room

import (
	"context"
	"errors"
	"testing"

	gameSDK "github.com/iFTY-R/game-night/sdk/go/game"
)

func TestRegisteredGameCatalogReturnsValidatedDefaultParticipantLimits(t *testing.T) {
	registry := &stubManifestRegistry{manifest: roomCatalogManifest("dice")}
	catalog, err := NewRegisteredGameCatalog(registry)
	if err != nil {
		t.Fatal(err)
	}
	limits, err := catalog.ParticipantLimits(t.Context(), " dice ")
	if err != nil || limits.Minimum != 2 || limits.Maximum != 9 || registry.requested != "dice" {
		t.Fatalf("limits = %+v, requested = %q, err = %v", limits, registry.requested, err)
	}
}

func TestRegisteredGameCatalogHidesMissingAndInvalidRegistryMetadata(t *testing.T) {
	missing, err := NewRegisteredGameCatalog(&stubManifestRegistry{err: gameSDK.ErrGameNotRegistered})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := missing.ParticipantLimits(t.Context(), "dice"); !errors.Is(err, ErrGameUnavailable) {
		t.Fatalf("missing game error = %v", err)
	}

	invalidManifest := roomCatalogManifest("other-game")
	invalid, err := NewRegisteredGameCatalog(&stubManifestRegistry{manifest: invalidManifest})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := invalid.ParticipantLimits(t.Context(), "dice"); !errors.Is(err, ErrGameUnavailable) {
		t.Fatalf("mismatched manifest error = %v", err)
	}
}

func TestRegisteredGameCatalogPreservesContextCancellation(t *testing.T) {
	catalog, err := NewRegisteredGameCatalog(&stubManifestRegistry{err: context.Canceled})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.ParticipantLimits(t.Context(), "dice"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled catalog error = %v", err)
	}
}

func TestDisabledGameCatalogKeepsSessionStartUnavailable(t *testing.T) {
	if _, err := NewDisabledGameCatalog().ParticipantLimits(t.Context(), "dice"); !errors.Is(err, ErrGameUnavailable) {
		t.Fatalf("disabled catalog error = %v", err)
	}
}

type stubManifestRegistry struct {
	manifest  gameSDK.Manifest
	err       error
	requested gameSDK.GameID
}

func (registry *stubManifestRegistry) DefaultManifest(_ context.Context, gameID gameSDK.GameID) (gameSDK.Manifest, error) {
	registry.requested = gameID
	return registry.manifest, registry.err
}

func roomCatalogManifest(gameID gameSDK.GameID) gameSDK.Manifest {
	return gameSDK.Manifest{
		GameID:       gameID,
		Versions:     gameSDK.VersionSet{Engine: "1.0.0", Protocol: "1.0.0", Client: "1.0.0"},
		Participants: gameSDK.ParticipantLimits{Minimum: 2, Maximum: 9},
		Capabilities: gameSDK.Capabilities{
			Submission: gameSDK.SubmissionModeTurnBased, Timers: true, Spectating: true,
			Replay: true, Reveal: gameSDK.RevealPolicyRuleControlled,
		},
		Presentation: gameSDK.PresentationPreferences{
			TableShape: gameSDK.TableShapeAdaptive, Orientation: gameSDK.OrientationResponsive,
			ActionDock: gameSDK.ActionDockSeatAnchored,
		},
		Themes: gameSDK.ThemePreferences{
			Default: "classic", Fallback: "safe", Variants: []gameSDK.Identifier{"classic", "safe"},
		},
	}
}

var _ ManifestRegistry = (*stubManifestRegistry)(nil)
