package module

import (
	"bytes"
	"testing"

	meetv1 "github.com/iFTY-R/game-night/games/meet-by-chance/gen/go/game/meet_by_chance/v1"
)

// FuzzStrictProtoPayloads ensures accepted command payloads remain deterministic and unknown fields never escape strict decoding.
func FuzzStrictProtoPayloads(f *testing.F) {
	canonical, err := marshalDeterministic(&meetv1.Command{Command: &meetv1.Command_Stand{Stand: &meetv1.Stand{}}})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(canonical)
	f.Add([]byte{0x78, 0x01})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, payload []byte) {
		var command meetv1.Command
		if err := unmarshalStrict(payload, &command); err != nil {
			return
		}
		reencoded, err := marshalDeterministic(&command)
		if err != nil || !bytes.Equal(payload, reencoded) {
			t.Fatalf("accepted payload was not canonical: error=%v", err)
		}
	})
}
