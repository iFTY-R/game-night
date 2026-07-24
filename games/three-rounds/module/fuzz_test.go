package module

import (
	"bytes"
	"testing"

	threeroundsv1 "github.com/iFTY-R/game-night/games/three-rounds/gen/go/game/three_rounds/v1"
)

func FuzzStrictProtoPayloads(f *testing.F) {
	canonical, err := marshalDeterministic(&threeroundsv1.Command{
		Command: &threeroundsv1.Command_SubmitSelection{
			SubmitSelection: &threeroundsv1.SubmitSelection{Round: 1, CardIds: []string{"2D"}},
		},
	})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(canonical)
	f.Add([]byte{0x28, 0x01})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, payload []byte) {
		var command threeroundsv1.Command
		if err := unmarshalStrict(payload, &command); err != nil {
			return
		}
		reencoded, err := marshalDeterministic(&command)
		if err != nil || !bytes.Equal(payload, reencoded) {
			t.Fatalf("accepted payload was not canonical: error=%v", err)
		}
	})
}
