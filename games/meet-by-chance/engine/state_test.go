package engine

import (
	"reflect"
	"testing"

	"github.com/iFTY-R/game-night/sdk/go/game/dice"
)

const testNow int64 = 100_000

func TestNewStateDeterminismAndInitialTarget(t *testing.T) {
	config := DefaultConfig()
	participants := testParticipants(4)
	first, firstEvents, err := NewState(config, participants, participants[2].UserID, testNow, [32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	second, secondEvents, err := NewState(config, participants, participants[2].UserID, testNow, [32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(firstEvents, secondEvents) {
		t.Fatal("creation is not deterministic")
	}
	if first.Phase != PhaseTargetDecision || first.Round != 1 || first.HostUserID != participants[2].UserID || first.TargetUserID == "" || first.ActionDeadlineUnixMillis == 0 {
		t.Fatalf("state=%+v", first)
	}
	if first.LastSettlement.Round != 0 || len(firstEvents) == 0 || firstEvents[0].InitialConfig == nil || len(firstEvents[0].InitialParticipants) != 4 {
		t.Fatalf("events=%+v", firstEvents)
	}
	if err := first.Validate(); err != nil {
		t.Fatal(err)
	}
	if playerByID(first, first.TargetUserID).PenaltyTicks != config.TargetPenaltyTicks {
		t.Fatalf("target penalty=%+v", first)
	}
}

func TestTargetActionsStandTimeoutAndStaleTimer(t *testing.T) {
	state := testState(t)
	timer := CurrentTimer(state)
	if timer == nil || timer.TargetUserID != state.TargetUserID || timer.Round != state.Round {
		t.Fatalf("timer=%+v", timer)
	}
	stale := *timer
	stale.TargetStreak++
	if _, _, err := HandleTimeout(state, stale, timer.DeadlineUnixMillis, [32]byte{2}); ErrorCodeOf(err) != CodeTimerMismatch {
		t.Fatalf("stale timer=%v", err)
	}
	stale = *timer
	stale.MatchResolutionCount++
	if _, _, err := HandleTimeout(state, stale, timer.DeadlineUnixMillis, [32]byte{2}); ErrorCodeOf(err) != CodeTimerMismatch {
		t.Fatalf("stale match-resolution timer=%v", err)
	}
	next, events, err := Stand(state, state.TargetUserID, testNow+1, [32]byte{2})
	if err != nil {
		t.Fatal(err)
	}
	if next.Round != state.Round+1 || next.Phase != PhaseTargetDecision || next.LastSettlement.Round != state.Round ||
		len(next.RoundHistory) != 1 || countEvent(events, EventRoundSettled) != 1 {
		t.Fatalf("next=%+v events=%+v", next, events)
	}
	if err := next.Validate(); err != nil {
		t.Fatalf("settled state is invalid: %v", err)
	}
	state = testState(t)
	timer = CurrentTimer(state)
	next, _, err = HandleTimeout(state, *timer, timer.DeadlineUnixMillis, [32]byte{3})
	if err != nil || next.LastSettlement.Reason != "timeout_stand" {
		t.Fatalf("timeout next=%+v err=%v", next, err)
	}
}

func TestRerollLimitAndPenaltyMonotonicity(t *testing.T) {
	config := DefaultConfig()
	config.TargetRerollLimit = 0
	state := testStateWithConfig(t, config)
	if _, _, err := Reroll(state, state.TargetUserID, testNow+1, [32]byte{4}); ErrorCodeOf(err) != CodeRerollLimitReached {
		t.Fatalf("reroll limit error=%v", err)
	}
	config = DefaultConfig()
	config.TargetRerollLimit = 1
	state = testStateWithConfig(t, config)
	target := state.TargetUserID
	before := playerByID(state, target).PenaltyTicks
	next, events, err := Reroll(state, target, testNow+1, [32]byte{5})
	if err != nil {
		t.Fatal(err)
	}
	if next.LastSettlement.Round == 0 && playerByID(next, target).PenaltyTicks < before+config.RerollPenaltyTicks {
		t.Fatalf("reroll penalty lost: before=%d next=%+v", before, next)
	}
	if len(events) == 0 || events[0].Kind != EventTargetRerolled || events[0].MatchResolutionCount != state.MatchResolutionCount {
		t.Fatalf("reroll event lacks stale-resistant counters: %+v", events)
	}
}

func TestTargetRevocationCancelsRoundAndInsufficientPlayersFinish(t *testing.T) {
	state := testState(t)
	oldRound := state.Round
	revokedTarget := state.TargetUserID
	revokedPenalty := playerByID(state, revokedTarget).PenaltyTicks
	next, events, err := RevokeParticipant(state, revokedTarget, testNow+1, [32]byte{6})
	if err != nil {
		t.Fatal(err)
	}
	if next.Round != oldRound+1 || next.Phase != PhaseTargetDecision || next.LastSettlement.Round != 0 ||
		playerByID(next, revokedTarget).PenaltyTicks != revokedPenalty || countEvent(events, EventParticipantRevoked) != 1 ||
		!events[0].RoundCancelled || events[0].NextRound != oldRound+1 || events[0].ActivePlayerCount != 3 {
		t.Fatalf("cancel next=%+v events=%+v", next, events)
	}
	state = testStateWithCount(t, 3)
	next, events, err = RevokeParticipant(state, state.Players[0].UserID, testNow+1, [32]byte{7})
	if err != nil {
		t.Fatal(err)
	}
	if next.Phase != PhaseFinished || next.FinishReason != FinishInsufficientParticipants || countEvent(events, EventSessionFinished) != 1 || CurrentTimer(next) != nil {
		t.Fatalf("finished next=%+v events=%+v", next, events)
	}
}

func TestNonTargetRevocationPreservesPublished235Context(t *testing.T) {
	state := testState(t)
	raw := [][3]dice.Face{{2, 3, 5}, {6, 6, 6}, {5, 5, 5}, {2, 2, 4}}
	hands := make([]Hand, len(raw))
	for index := range raw {
		var err error
		hands[index], err = Classify(raw[index], state.Config)
		if err != nil {
			t.Fatal(err)
		}
	}
	hands = Resolve235Context(hands)
	for index := range state.Players {
		state.Players[index].Active = true
		state.Players[index].IncludedInCurrentResolution = true
		state.Players[index].TargetedThisRound = false
		state.Players[index].Hand = hands[index]
	}
	state.TargetUserID = state.Players[0].UserID
	state.Players[0].TargetedThisRound = true
	state.TargetRerollCount, state.TargetStreak, state.MatchResolutionCount = 0, 0, 0
	state.MatchHistory = nil
	if state.Players[0].Hand.SpecialContext != Special235Minimum {
		t.Fatalf("precondition context=%v", state.Players[0].Hand.SpecialContext)
	}
	if err := state.Validate(); err != nil {
		t.Fatal(err)
	}

	next, _, err := RevokeParticipant(state, state.Players[3].UserID, testNow+1, [32]byte{8})
	if err != nil {
		t.Fatal(err)
	}
	if next.Round != state.Round || next.TargetUserID != state.TargetUserID ||
		next.Players[0].Hand != state.Players[0].Hand || !next.Players[3].IncludedInCurrentResolution {
		t.Fatalf("non-target revoke changed the published resolution: %+v", next)
	}
	if err := next.Validate(); err != nil {
		t.Fatalf("preserved resolution is invalid: %v", err)
	}
}

func TestRoundHistoryRetainsLatestThirtyTwoSettlements(t *testing.T) {
	state := testState(t)
	for iteration := 0; iteration < RoundHistoryLimit+3; iteration++ {
		var err error
		state, _, err = Stand(state, state.TargetUserID, testNow+int64(iteration+1), [32]byte{byte(iteration + 1)})
		if err != nil {
			t.Fatalf("iteration %d: %v", iteration, err)
		}
	}
	if len(state.RoundHistory) != RoundHistoryLimit || state.RoundHistory[0].Round != 4 ||
		state.RoundHistory[len(state.RoundHistory)-1].Round != state.LastSettlement.Round {
		t.Fatalf("history=%+v last=%+v", state.RoundHistory, state.LastSettlement)
	}
	if err := state.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestReturningTargetIsNotChargedFirstSelectionTwice(t *testing.T) {
	state := testState(t)
	returning := state.TargetUserID
	previous := ""
	for index := range state.Players {
		if state.Players[index].UserID != returning && state.Players[index].Active {
			previous = state.Players[index].UserID
			state.Players[index].TargetedThisRound = true
			break
		}
	}
	state.TargetUserID = previous
	state.TargetStreak = 1
	before := playerByID(state, returning).PenaltyTicks
	next, events, err := selectTarget(state, previous, testNow+1, "post_reroll_target")
	if err != nil {
		t.Fatal(err)
	}
	if next.TargetUserID != returning || next.TargetStreak != 0 || playerByID(next, returning).PenaltyTicks != before ||
		len(events) != 1 || events[0].FirstSelectionThisRound || events[0].PenaltyTicks != 0 {
		t.Fatalf("returning target was charged again: next=%+v events=%+v", next, events)
	}
}

func TestFinishKeepsPublicHandsAndClearsTimer(t *testing.T) {
	state := testState(t)
	hands := clonePlayers(state.Players)
	next, events, err := Finish(state, FinishHostRequested, state.HostUserID)
	if err != nil {
		t.Fatal(err)
	}
	if next.Phase != PhaseFinished || next.FinishReason != FinishHostRequested || CurrentTimer(next) != nil || !reflect.DeepEqual(next.Players, hands) || countEvent(events, EventSessionFinished) != 1 {
		t.Fatalf("next=%+v events=%+v", next, events)
	}
	if err := next.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestHostIsFrozenAndRemainsValidAfterRevocation(t *testing.T) {
	participants := testParticipants(4)
	state, _, err := NewState(DefaultConfig(), participants, "missing-host", testNow, [32]byte{1})
	if ErrorCodeOf(err) != CodeInvalidParticipants || state.SchemaVersion != 0 || len(state.Players) != 0 {
		t.Fatalf("invalid host state=%+v err=%v", state, err)
	}
	state, _, err = NewState(DefaultConfig(), participants, participants[1].UserID, testNow, [32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	if state.HostUserID != participants[1].UserID {
		t.Fatalf("host=%q", state.HostUserID)
	}
	state, _, err = RevokeParticipant(state, state.HostUserID, testNow+1, [32]byte{2})
	if err != nil {
		t.Fatal(err)
	}
	if activePlayer(state, state.HostUserID) {
		t.Fatalf("revoked host remains active: %+v", state)
	}
	if err := state.Validate(); err != nil {
		t.Fatalf("inactive frozen host should remain valid: %v", err)
	}
	clone := state.Clone()
	clone.Players[0].Active = !clone.Players[0].Active
	if clone.HostUserID != state.HostUserID || reflect.DeepEqual(clone.Players, state.Players) {
		t.Fatalf("clone=%+v state=%+v", clone, state)
	}
	corrupted := state.Clone()
	corrupted.HostUserID = "missing-host"
	if ErrorCodeOf(corrupted.Validate()) != CodeInvalidState {
		t.Fatalf("unknown host accepted: %+v", corrupted)
	}
}

func testState(t *testing.T) State { return testStateWithCount(t, 4) }

func testStateWithCount(t *testing.T, count int) State {
	return testStateWithConfigAndSeed(t, count, DefaultConfig(), [32]byte{1})
}

func testStateWithConfig(t *testing.T, config Config) State {
	return testStateWithConfigAndSeed(t, 4, config, [32]byte{1})
}

func testStateWithConfigAndSeed(t *testing.T, count int, config Config, seed [32]byte) State {
	participants := testParticipants(count)
	state, _, err := NewState(config, participants, participants[0].UserID, testNow, seed)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func testParticipants(count int) []Participant {
	participants := make([]Participant, count)
	for index := range participants {
		participants[index] = Participant{UserID: "user-" + string(rune('a'+index)), SeatIndex: uint32(index * 2)}
	}
	return participants
}

func playerByID(state State, userID string) PlayerState {
	for _, player := range state.Players {
		if player.UserID == userID {
			return player
		}
	}
	return PlayerState{}
}

func totalPenalty(state State) (total uint32) {
	for _, player := range state.Players {
		total += uint32(player.PenaltyTicks)
	}
	return total
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
