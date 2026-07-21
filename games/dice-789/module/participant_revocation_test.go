package module

import (
	"bytes"
	"testing"

	dice789v1 "github.com/iFTY-R/game-night/games/dice-789/gen/go/game/dice789/v1"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

func TestEncodeParticipantRevokedFeedsDice789SystemHandler(t *testing.T) {
	module := New()
	fact := game.ParticipantRevocationFact{UserID: "user-2"}
	first, err := module.EncodeParticipantRevoked(fact)
	if err != nil {
		t.Fatal(err)
	}
	second, err := module.EncodeParticipantRevoked(fact)
	if err != nil || !bytes.Equal(first.Payload, second.Payload) {
		t.Fatalf("participant revocation encoding is not deterministic: err=%v", err)
	}
	if first.MessageType != EventParticipantRevokedMessage || first.SchemaVersion != ProtocolSchemaVersion {
		t.Fatalf("participant revocation envelope=%+v", first)
	}
	var command dice789v1.ParticipantRevoked
	if err := unmarshalStrict(first.Payload, &command); err != nil || command.GetUserId() != string(fact.UserID) {
		t.Fatalf("participant revocation payload=%+v err=%v", &command, err)
	}
	if _, err := module.EncodeParticipantRevoked(game.ParticipantRevocationFact{UserID: "User-2"}); err == nil {
		t.Fatal("non-canonical participant revocation fact accepted")
	}

	created, err := module.Create(testCreateRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	request := systemRequest(t, created.Snapshot.StateVersion, first.MessageType, &command)
	request.System = first
	revoked, err := module.HandleSystem(created.Snapshot, request)
	if err != nil {
		t.Fatal(err)
	}
	state, err := DecodeState(revoked.Snapshot.State)
	if err != nil {
		t.Fatal(err)
	}
	if state.Players[1].UserID != string(fact.UserID) || state.Players[1].Active {
		t.Fatalf("encoded revocation did not deactivate player: %+v", state.Players[1])
	}
}
