package module

import (
	"bytes"
	"testing"

	dice789v1 "github.com/iFTY-R/game-night/games/dice-789/gen/go/game/dice789/v1"
)

// FuzzStrictProtoPayloads proves every accepted command has one canonical wire
// representation, which keeps action digests stable across retries.
func FuzzStrictProtoPayloads(f *testing.F) {
	canonical, err := marshalDeterministic(&dice789v1.Command{
		Command: &dice789v1.Command_Roll{Roll: &dice789v1.Roll{}},
	})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(canonical)
	f.Fuzz(func(t *testing.T, payload []byte) {
		var command dice789v1.Command
		if err := unmarshalStrict(payload, &command); err != nil {
			return
		}
		reencoded, err := marshalDeterministic(&command)
		if err != nil || !bytes.Equal(payload, reencoded) {
			t.Fatalf("accepted payload was not canonical: error=%v", err)
		}
	})
}
