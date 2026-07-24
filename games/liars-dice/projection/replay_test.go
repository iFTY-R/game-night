package projection

import (
	"testing"

	"github.com/iFTY-R/game-night/games/liars-dice/engine"
	"github.com/iFTY-R/game-night/sdk/go/game/dice"
)

func TestBuildReplayRejectsCorruptedEventLifecycle(t *testing.T) {
	start := engine.Event{
		Kind: engine.EventRoundStarted, Round: 1, FirstActor: "user-1",
		Players: []engine.Participant{{UserID: "user-1", SeatIndex: 0}, {UserID: "user-2", SeatIndex: 1}},
	}
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
		{name: "roster repeated", events: []engine.Event{start, {Kind: engine.EventRoundStarted, Round: 2, FirstActor: "user-2", Players: start.Players}}},
		{name: "duplicate roster seat", events: []engine.Event{{Kind: engine.EventRoundStarted, Round: 1, FirstActor: "user-1", Players: []engine.Participant{{UserID: "user-1", SeatIndex: 0}, {UserID: "user-2", SeatIndex: 0}}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := BuildReplay(test.events); err == nil {
				t.Fatalf("accepted corrupted replay events: %+v", test.events)
			}
		})
	}
}

func TestBuildReplayPreservesInitialRoster(t *testing.T) {
	events := []engine.Event{
		{
			Kind: engine.EventRoundStarted, Round: 1, FirstActor: "user-2",
			Players: []engine.Participant{{UserID: "user-1", SeatIndex: 3}, {UserID: "user-2", SeatIndex: 7}},
		},
		{Kind: engine.EventSessionFinished, Round: 1, Reason: engine.FinishHostRequested},
	}

	replay, err := BuildReplay(events)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.GetPlayers()) != 2 || replay.GetPlayers()[0].GetUserId() != "user-1" || replay.GetPlayers()[0].GetSeatIndex() != 3 || replay.GetPlayers()[1].GetSeatIndex() != 7 {
		t.Fatalf("replay roster = %+v", replay.GetPlayers())
	}
}

func TestBuildReplayCancelledKeepsPendingPublicRound(t *testing.T) {
	events := []engine.Event{
		{
			Kind: engine.EventRoundStarted, Round: 1, FirstActor: "user-1",
			Players: []engine.Participant{{UserID: "user-1", SeatIndex: 0}, {UserID: "user-2", SeatIndex: 1}},
		},
		{Kind: engine.EventBidPlaced, Round: 1, UserID: "user-1", Bid: &engine.Bid{Quantity: 2, Face: 3, Mode: engine.BidModeFlying}},
	}
	replay, err := BuildReplayCancelled(events, "runtime_cancelled")
	if err != nil {
		t.Fatal(err)
	}
	if replay.GetFinishReason() != "runtime_cancelled" || len(replay.GetRounds()) != 1 {
		t.Fatalf("cancelled replay=%+v", replay)
	}
	pending := replay.GetRounds()[0]
	if pending.GetReason() != "" || pending.GetDiceRevealed() || len(pending.GetDice()) != 0 || len(pending.GetBids()) != 1 {
		t.Fatalf("pending replay round=%+v", pending)
	}
}

func TestBuildReplayCancelledKeepsAlreadyRevealedDice(t *testing.T) {
	events := []engine.Event{
		{
			Kind: engine.EventRoundStarted, Round: 1, FirstActor: "user-1",
			Players: []engine.Participant{{UserID: "user-1", SeatIndex: 0}, {UserID: "user-2", SeatIndex: 1}},
		},
		{
			Kind: engine.EventDiceRevealed, Round: 1,
			Dice: []engine.PrivateRoll{
				{UserID: "user-1", Faces: []dice.Face{1, 2, 3, 4, 5}},
				{UserID: "user-2", Faces: []dice.Face{2, 2, 2, 2, 2}},
			},
		},
	}
	replay, err := BuildReplayCancelled(events, "runtime_cancelled")
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.GetRounds()) != 1 || !replay.GetRounds()[0].GetDiceRevealed() || len(replay.GetRounds()[0].GetDice()) != 2 {
		t.Fatalf("revealed cancelled replay=%+v", replay.GetRounds())
	}
	if replay.GetRounds()[0].GetReason() != "" {
		t.Fatalf("revealed cancelled replay leaked settlement=%+v", replay.GetRounds()[0])
	}
}
