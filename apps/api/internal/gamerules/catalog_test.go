package gamerules

import (
	"context"
	"testing"

	roomDomain "github.com/iFTY-R/game-night/platform/room"
)

func TestCatalogSupportsThreeRoundsDefaultAndNormalize(t *testing.T) {
	catalog := NewCatalog()
	envelope, err := catalog.Default(context.Background(), "three-rounds", 2)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.GameID != "three-rounds" {
		t.Fatalf("default envelope=%+v", envelope)
	}
	normalized, err := catalog.Normalize(context.Background(), envelope, 2)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.GameID != envelope.GameID || normalized.EngineVersion != envelope.EngineVersion || normalized.ProtocolVersion != envelope.ProtocolVersion || normalized.ClientVersion != envelope.ClientVersion {
		t.Fatalf("normalized envelope=%+v default=%+v", normalized, envelope)
	}
}

func TestCatalogRejectsUnknownThreeRoundsVersion(t *testing.T) {
	catalog := NewCatalog()
	_, err := catalog.Normalize(context.Background(), roomDomain.ConfigEnvelope{
		GameID:          "three-rounds",
		EngineVersion:   "9.9.9",
		ProtocolVersion: "1.0.0",
		ClientVersion:   "1.0.0",
		SchemaVersion:   1,
		MessageType:     "session.config",
	}, 2)
	if err == nil {
		t.Fatal("unknown version was accepted")
	}
}
