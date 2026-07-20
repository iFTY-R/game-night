package module

import (
	"testing"

	liarsdicev1 "github.com/iFTY-R/game-night/games/liars-dice/gen/go/game/liars_dice/v1"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

// FuzzStrictProtoPayloads protects the command/config codecs from malformed
// wire data and verifies arbitrary input never escapes as a panic.
func FuzzStrictProtoPayloads(f *testing.F) {
	f.Add([]byte{0x0a, 0x02, 0x10, 0x01})
	f.Add([]byte{0xff, 0xff, 0xff, 0x7f})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, payload []byte) {
		var bid liarsdicev1.Bid
		_ = unmarshalStrict(payload, &bid)
		var config liarsdicev1.Config
		_ = unmarshalStrict(payload, &config)
		var state liarsdicev1.State
		_ = unmarshalStrict(payload, &state)
		_, _ = DecodeState(game.Message{MessageType: StateMessageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload})
	})
}
