// Package gameregistry owns the build-time list of retained concrete game releases.
package gameregistry

import (
	"context"
	"errors"
	"slices"

	game "github.com/iFTY-R/game-night/sdk/go/game"
)

var errGeneratedRegistryDrift = errors.New("generated game registry differs from module manifests")

// New constructs the immutable production registry and fails startup when any
// generated client metadata has drifted from the concrete server manifests.
func New() (*game.Registry, error) {
	registry, err := game.NewRegistry(generatedRegistrations()...)
	if err != nil {
		return nil, err
	}
	if err := validateGeneratedRegistry(registry); err != nil {
		return nil, err
	}
	return registry, nil
}

// DefaultVersions returns a defensive exact version map for new-session clients.
func DefaultVersions() map[game.GameID]game.VersionKey {
	versions := make(map[game.GameID]game.VersionKey, len(generatedExpectedManifests))
	for _, manifest := range generatedExpectedManifests {
		versions[manifest.GameID] = manifest.Key()
	}
	return versions
}

func validateGeneratedRegistry(registry *game.Registry) error {
	if registry == nil || len(registry.Manifests()) != len(generatedExpectedManifests) {
		return errGeneratedRegistryDrift
	}
	for _, expected := range generatedExpectedManifests {
		actual, err := registry.DefaultManifest(context.Background(), expected.GameID)
		if err != nil || !manifestsEqual(actual, expected) {
			return errGeneratedRegistryDrift
		}
		if !slices.Contains(actual.Themes.Variants, actual.Themes.Default) || !slices.Contains(actual.Themes.Variants, actual.Themes.Fallback) {
			return errGeneratedRegistryDrift
		}
	}
	return nil
}

func manifestsEqual(left, right game.Manifest) bool {
	return left.GameID == right.GameID && left.Versions == right.Versions && left.Participants == right.Participants &&
		left.Capabilities == right.Capabilities && left.Presentation == right.Presentation &&
		left.Themes.Default == right.Themes.Default && left.Themes.Fallback == right.Themes.Fallback &&
		slices.Equal(left.Themes.Variants, right.Themes.Variants)
}
