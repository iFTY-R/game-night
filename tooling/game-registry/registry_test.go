package gameregistry

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	dice789engine "github.com/iFTY-R/game-night/games/dice-789/engine"
	dice789module "github.com/iFTY-R/game-night/games/dice-789/module"
	liarsengine "github.com/iFTY-R/game-night/games/liars-dice/engine"
	liarsmodule "github.com/iFTY-R/game-night/games/liars-dice/module"
	meetengine "github.com/iFTY-R/game-night/games/meet-by-chance/engine"
	meetmodule "github.com/iFTY-R/game-night/games/meet-by-chance/module"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

func TestGeneratedRegistryContainsThreeExactDefaults(t *testing.T) {
	registry, err := New()
	if err != nil {
		t.Fatal(err)
	}
	versions := DefaultVersions()
	if len(registry.Manifests()) != 3 || len(versions) != 3 {
		t.Fatalf("manifests=%d versions=%d", len(registry.Manifests()), len(versions))
	}
	for gameID, key := range versions {
		manifest, err := registry.DefaultManifest(t.Context(), gameID)
		if err != nil || manifest.Key() != key {
			t.Fatalf("game=%s manifest=%+v key=%+v err=%v", gameID, manifest, key, err)
		}
		module, err := registry.Resolve(key)
		if err != nil || module.Manifest().Key() != key {
			t.Fatalf("game=%s module=%T err=%v", gameID, module, err)
		}
	}
	missing := game.VersionKey{GameID: "liars-dice", Engine: "9.9.9", Protocol: "1.0.0", Client: "1.0.0"}
	if _, err := registry.Resolve(missing); !errors.Is(err, game.ErrVersionNotRegistered) {
		t.Fatalf("missing exact version resolved: %v", err)
	}
	if _, err := registry.DefaultManifest(context.Background(), "unknown-game"); !errors.Is(err, game.ErrGameNotRegistered) {
		t.Fatalf("unknown game resolved: %v", err)
	}
}

func TestRegisteredDefaultsCreateFromFrozenConfigsAndRejectCorruption(t *testing.T) {
	liarsConfig, err := liarsmodule.EncodeConfigForPlayers(liarsengine.DefaultConfig(2), 2)
	if err != nil {
		t.Fatal(err)
	}
	dice789Config, err := dice789module.EncodeConfigForPlayers(dice789engine.DefaultConfig(true), 2)
	if err != nil {
		t.Fatal(err)
	}
	meetConfig, err := meetmodule.EncodeConfigForPlayers(meetengine.DefaultConfig(), 3)
	if err != nil {
		t.Fatal(err)
	}

	registry, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		gameID       game.GameID
		participants uint32
		config       game.Message
	}{
		{gameID: "liars-dice", participants: 2, config: liarsConfig},
		{gameID: "dice-789", participants: 2, config: dice789Config},
		{gameID: "meet-by-chance", participants: 3, config: meetConfig},
	} {
		t.Run(string(test.gameID), func(t *testing.T) {
			module, err := registry.DefaultModule(t.Context(), test.gameID)
			if err != nil {
				t.Fatal(err)
			}
			request := registryCreateRequest(test.participants, test.config)
			transition, err := module.Create(request)
			if err != nil || transition.Snapshot.StateVersion != 1 {
				t.Fatalf("create transition=%+v err=%v", transition, err)
			}
			corrupted := request
			corrupted.Config = request.Config.Clone()
			corrupted.Config.Payload = []byte{0xff}
			if _, err := module.Create(corrupted); err == nil {
				t.Fatal("corrupted frozen config was accepted")
			}
		})
	}
}

func registryCreateRequest(participantCount uint32, config game.Message) game.CreateRequest {
	participants := make([]game.Participant, participantCount)
	for index := range participants {
		participants[index] = game.Participant{UserID: game.Identifier(fmt.Sprintf("player-%d", index+1)), SeatIndex: uint32(index)}
	}
	return game.CreateRequest{
		Context: game.DeterministicContext{
			Now: time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC), RandomSeed: [32]byte{1},
		},
		StartContext: game.SessionStartContext{HostUserID: participants[0].UserID, StartingSeat: 0},
		Participants: participants, Config: config.Clone(),
	}
}
