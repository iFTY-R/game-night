package module

import (
	"bytes"
	"testing"
	"time"

	liarsdicev1 "github.com/iFTY-R/game-night/games/liars-dice/gen/go/game/liars_dice/v1"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

func TestEncodeParticipantRevokedFeedsLiarsSystemHandler(t *testing.T) {
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
	var command liarsdicev1.ParticipantRevoked
	if err := unmarshalStrict(first.Payload, &command); err != nil || command.GetUserId() != string(fact.UserID) {
		t.Fatalf("participant revocation payload=%+v err=%v", &command, err)
	}
	if _, err := module.EncodeParticipantRevoked(game.ParticipantRevocationFact{UserID: "User-2"}); err == nil {
		t.Fatal("non-canonical participant revocation fact accepted")
	}

	created, err := module.Create(createRequest(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	request := systemRequest(created.Snapshot.StateVersion, string(first.MessageType), first.Payload, executionAt(time.Unix(101, 0).UTC(), 2))
	request.System = first
	revoked, err := module.HandleSystem(created.Snapshot, request)
	if err != nil {
		t.Fatal(err)
	}
	state, err := DecodeState(revoked.Snapshot.State)
	if err != nil {
		t.Fatal(err)
	}
	for _, player := range state.Players {
		if player.UserID == string(fact.UserID) && player.Active {
			t.Fatalf("encoded revocation did not deactivate player: %+v", player)
		}
	}
}
