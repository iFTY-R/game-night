package projection

import (
	"testing"

	"github.com/iFTY-R/game-night/games/liars-dice/engine"
	"github.com/iFTY-R/game-night/sdk/go/game/dice"
)

func TestBuildReplayRejectsCorruptedEventLifecycle(t *testing.T) {
	start := engine.Event{Kind: engine.EventRoundStarted, Round: 1, FirstActor: "user-1"}
	reveal := engine.Event{
		Kind: engine.EventDiceRevealed, Round: 1,
		Dice: []engine.PrivateRoll{{UserID: "user-1", Faces: []dice.Face{1, 2, 3}}},
	}
	bid := engine.Event{Kind: engine.EventBidPlaced, Round: 1, UserID: "user-1", Bid: &engine.Bid{Quantity: 2, Face: 3, Mode: engine.BidModeFlying}}
	tests := []struct {
		name   string
		events []engine.Event
	}{
		{name: "empty reveal", events: []engine.Event{start, {Kind: engine.EventDiceRevealed, Round: 1}}},
		{name: "duplicate reveal", events: []engine.Event{start, reveal, reveal}},
		{name: "bid after reveal", events: []engine.Event{start, reveal, bid}},
		{name: "round regression", events: []engine.Event{start, {Kind: engine.EventRoundStarted, Round: 1, FirstActor: "user-2"}}},
		{name: "event after finish", events: []engine.Event{start, {Kind: engine.EventSessionFinished, Round: 1, Reason: engine.FinishHostRequested}, {Kind: engine.EventRoundStarted, Round: 2, FirstActor: "user-2"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := BuildReplay(test.events); err == nil {
				t.Fatalf("accepted corrupted replay events: %+v", test.events)
			}
		})
	}
}
