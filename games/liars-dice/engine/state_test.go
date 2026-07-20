package engine

import (
	"reflect"
	"testing"

	"github.com/iFTY-R/game-night/sdk/go/game/dice"
)

func TestNewStateIsDeterministicAndPrivate(t *testing.T) {
	now := int64(100_000)
	config := DefaultConfig(4)
	participants := testParticipants(4)
	seed := [32]byte{1, 2, 3, 4}
	first, events, err := NewState(config, participants, participants[1].SeatIndex, now, seed)
	if err != nil {
		t.Fatal(err)
	}
	second, secondEvents, err := NewState(config, participants, participants[1].SeatIndex, now, seed)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(events, secondEvents) {
		t.Fatal("same inputs produced different state or events")
	}
	if first.Phase != PhaseBidding || first.Round != 1 || first.CurrentActorUserID != participants[1].UserID || len(first.PrivateDice) != 4 {
		t.Fatalf("state = %+v", first)
	}
	for _, roll := range first.PrivateDice {
		if len(roll.Faces) != int(config.DicePerPlayer) {
			t.Fatalf("roll = %+v", roll)
		}
		for _, face := range roll.Faces {
			if !face.Valid() {
				t.Fatalf("invalid face %d", face)
			}
		}
	}
	if len(events) != 1 || events[0].Kind != EventRoundStarted || len(events[0].Dice) != 0 {
		t.Fatalf("start events leaked dice: %+v", events)
	}
}

func TestBiddingAdvancesInStableSeatOrder(t *testing.T) {
	state := testState(t)
	now := state.ActionDeadlineUnixMillis - 20_000
	next, events, err := PlaceBid(state, state.CurrentActorUserID, Bid{Quantity: 4, Face: 3, Mode: BidModeFlying}, now)
	if err != nil {
		t.Fatal(err)
	}
	if next.LastBidderUserID != state.CurrentActorUserID || next.CurrentActorUserID == state.CurrentActorUserID || len(events) != 1 || events[0].Kind != EventBidPlaced {
		t.Fatalf("next=%+v events=%+v", next, events)
	}
	assertCode(t, func() error {
		_, _, err := PlaceBid(next, state.CurrentActorUserID, Bid{Quantity: 5, Face: 3, Mode: BidModeFlying}, now)
		return err
	}(), CodeNotCurrentActor)
}

func TestOpenDiceSettlesUnderExactAndOver(t *testing.T) {
	tests := []struct {
		name       string
		quantity   uint32
		wantLoser  string
		wantActual uint32
	}{
		{name: "under", quantity: 8, wantLoser: "user-1", wantActual: 7},
		{name: "exact", quantity: 7, wantLoser: "user-2", wantActual: 7},
		{name: "over", quantity: 6, wantLoser: "user-2", wantActual: 7},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := settlementState()
			state.CurrentBid = &Bid{Quantity: test.quantity, Face: 5, Mode: BidModeFlying}
			state.LastBidderUserID = "user-1"
			state.CurrentActorUserID = "user-2"
			beforePenalty := playerByID(t, state, test.wantLoser).PenaltyTicks
			next, events, err := OpenDice(state, "user-2", 100_000, [32]byte{9})
			if err != nil {
				t.Fatal(err)
			}
			if next.LastSettlement.LoserUserID != test.wantLoser || next.LastSettlement.ActualQuantity != test.wantActual || next.FirstActorUserID != test.wantLoser {
				t.Fatalf("settlement = %+v", next.LastSettlement)
			}
			if got := playerByID(t, next, test.wantLoser).PenaltyTicks; got-beforePenalty != dice.Ticks(next.Config.PenaltyTicks) {
				t.Fatalf("penalty delta = %d", got-beforePenalty)
			}
			if countEvent(events, EventRoundSettled) != 1 || countEvent(events, EventDiceRevealed) != 1 || countEvent(events, EventRoundStarted) != 1 {
				t.Fatalf("events = %+v", events)
			}
		})
	}
}

func TestTimeoutAndCurrentParticipantRevocationCancelSafely(t *testing.T) {
	state := testState(t)
	actor := state.CurrentActorUserID
	timer := ActionTimer{Round: state.Round, UserID: actor, DeadlineUnixMillis: state.ActionDeadlineUnixMillis}
	next, events, err := HandleTimeout(state, timer, state.ActionDeadlineUnixMillis, [32]byte{7})
	if err != nil {
		t.Fatal(err)
	}
	if next.LastSettlement.LoserUserID != actor || next.LastSettlement.Reason != SettlementTimeout || countEvent(events, EventDiceRevealed) != 0 {
		t.Fatalf("timeout next=%+v events=%+v", next, events)
	}

	state = testState(t)
	revoked := state.CurrentActorUserID
	next, events, err = RevokeParticipant(state, revoked, 200_000, [32]byte{8})
	if err != nil {
		t.Fatal(err)
	}
	if playerByID(t, next, revoked).Active || next.CurrentActorUserID == revoked || next.LastSettlement.LoserUserID != "" || countEvent(events, EventDiceRevealed) != 0 {
		t.Fatalf("revocation next=%+v events=%+v", next, events)
	}
}

func TestRevocationFinishesBelowTwoActivePlayers(t *testing.T) {
	config := DefaultConfig(2)
	state, _, err := NewState(config, testParticipants(2), 0, 100_000, [32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	next, events, err := RevokeParticipant(state, state.CurrentActorUserID, 101_000, [32]byte{2})
	if err != nil {
		t.Fatal(err)
	}
	if next.Phase != PhaseFinished || next.FinishReason != FinishInsufficientParticipants || countEvent(events, EventSessionFinished) != 1 {
		t.Fatalf("next=%+v events=%+v", next, events)
	}
}

func TestValidateRejectsMalformedFinishedAndSingleActiveBiddingStates(t *testing.T) {
	finished, _, err := Finish(testState(t), FinishHostRequested)
	if err != nil {
		t.Fatal(err)
	}
	finished.FirstActorUserID = "missing-user"
	assertCode(t, finished.Validate(), CodeInvalidState)

	state := testState(t)
	for index := range state.Players {
		if state.Players[index].UserID != state.CurrentActorUserID {
			state.Players[index].Active = false
		}
	}
	state.PrivateDice = state.PrivateDice[:1]
	assertCode(t, state.Validate(), CodeInvalidState)

	state = testState(t)
	state.FinishReason = FinishHostRequested
	assertCode(t, state.Validate(), CodeInvalidState)
}

func testState(t *testing.T) State {
	t.Helper()
	state, _, err := NewState(DefaultConfig(4), testParticipants(4), 0, 100_000, [32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func settlementState() State {
	config := DefaultConfig(2)
	return State{
		SchemaVersion: CurrentSchemaVersion,
		Phase:         PhaseBidding,
		Round:         1,
		Config:        config,
		Players: []PlayerState{
			{UserID: "user-1", SeatIndex: 1, Active: true},
			{UserID: "user-2", SeatIndex: 2, Active: true},
		},
		FirstActorUserID:         "user-1",
		ActionDeadlineUnixMillis: 130_000,
		PrivateDice: []PrivateRoll{
			{UserID: "user-1", Faces: []dice.Face{5, 5, 5, 1, 2}},
			{UserID: "user-2", Faces: []dice.Face{5, 5, 1, 3, 4}},
		},
	}
}

func testParticipants(count int) []Participant {
	participants := make([]Participant, count)
	for index := range participants {
		participants[index] = Participant{UserID: "user-" + string(rune('1'+index)), SeatIndex: uint32(index)}
	}
	return participants
}

func playerByID(t *testing.T, state State, userID string) PlayerState {
	t.Helper()
	for _, player := range state.Players {
		if player.UserID == userID {
			return player
		}
	}
	t.Fatalf("player %s missing", userID)
	return PlayerState{}
}

func countEvent(events []Event, kind EventKind) int {
	count := 0
	for _, event := range events {
		if event.Kind == kind {
			count++
		}
	}
	return count
}

func assertCode(t *testing.T, err error, want ErrorCode) {
	t.Helper()
	if got := ErrorCodeOf(err); got != want {
		t.Fatalf("error code = %q, want %q (error=%v)", got, want, err)
	}
}
