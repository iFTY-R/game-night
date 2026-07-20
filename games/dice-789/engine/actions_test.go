package engine

import (
	"errors"
	"reflect"
	"testing"

	game "github.com/iFTY-R/game-night/sdk/go/game"
	"github.com/iFTY-R/game-night/sdk/go/game/dice"
)

const testNow int64 = 100_000

func TestNewStateFreezesRealHostAndReplayInitialization(t *testing.T) {
	participants := testParticipants(4)
	state, events, err := NewState(DefaultConfig(false), participants, participants[2].UserID, participants[0].SeatIndex, testNow, [32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	if state.HostUserID != participants[2].UserID || state.CurrentUserID != participants[0].UserID || state.Phase != PhaseAwaitingRoll {
		t.Fatalf("state=%+v", state)
	}
	if len(events) != 1 || events[0].InitialConfig == nil || len(events[0].InitialParticipants) != 4 ||
		len(events[0].InitialPool) != 1 || events[0].InitialTotalPoolTicks != 2 {
		t.Fatalf("initial event=%+v", events)
	}
	clone := events[0].Clone()
	clone.InitialParticipants[0].UserID = "changed"
	clone.InitialPool[0].Ticks = 0
	if events[0].InitialParticipants[0].UserID == "changed" || events[0].InitialPool[0].Ticks == 0 {
		t.Fatal("event clone aliases initialization data")
	}
}

func TestResultPriorityMatrix(t *testing.T) {
	tests := []struct {
		name     string
		one, two uint32
		mutate   func(*Config)
		want     ResultKind
	}{
		{"double_one", 1, 1, nil, ResultDoubleOne},
		{"double_one_falls_to_pair", 1, 1, func(c *Config) { c.DoubleOneEnabled = false }, ResultOrdinaryPair},
		{"double_four", 4, 4, nil, ResultDoubleFour},
		{"double_four_falls_to_pair", 4, 4, func(c *Config) { c.DoubleFourEnabled = false }, ResultOrdinaryPair},
		{"double_four_falls_to_eight", 4, 4, func(c *Config) { c.DoubleFourEnabled, c.OrdinaryPairsReverse = false, false }, ResultEight},
		{"double_six", 6, 6, nil, ResultDoubleSix},
		{"ordinary_pair", 3, 3, nil, ResultOrdinaryPair},
		{"seven", 3, 4, nil, ResultSeven},
		{"eight", 3, 5, nil, ResultEight},
		{"nine", 3, 6, nil, ResultNine},
		{"other", 2, 3, nil, ResultOther},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := DefaultConfig(false)
			if test.mutate != nil {
				test.mutate(&config)
			}
			state := newTestState(t, 4, config)
			next, _, err := applyRoll(state, state.CurrentUserID, test.one, test.two, testNow+1)
			if err != nil {
				t.Fatal(err)
			}
			if next.PendingResult != test.want {
				t.Fatalf("result=%s want=%s", next.PendingResult, test.want)
			}
		})
	}
}

func TestSevenAddAndStackedPool(t *testing.T) {
	config := DefaultConfig(true)
	config.AddStepTicks = 2
	state := pendingState(t, 3, 4, 7, config, 4)
	state, _, err := ConfirmLanded(state, state.HostUserID, testNow+2)
	if err != nil || state.Phase != PhaseAwaitingAdd {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if _, _, err := AddToPool(state, state.CurrentUserID, 1, testNow+3); ErrorCodeOf(err) != CodePoolAmountInvalid {
		t.Fatalf("unaligned add error=%v", err)
	}
	next, events, err := AddToPool(state, state.CurrentUserID, 4, testNow+3)
	if err != nil {
		t.Fatal(err)
	}
	if next.TotalPoolTicks != 11 || len(next.Pool) != 2 || next.Pool[0].Ticks != 8 || next.Pool[1].Ticks != 3 || next.Phase != PhaseAwaitingContinue {
		t.Fatalf("next=%+v", next)
	}
	if len(events) != 1 || poolLayersTotal(events[0].PoolBefore) != 7 || poolLayersTotal(events[0].PoolAfter) != 11 {
		t.Fatalf("events=%+v", events)
	}

	state = pendingState(t, 3, 4, 7, config, 4)
	state.Config.LayerCapacityTicks, state.Config.MaxLayers = 8, 1
	state.Config.StackedPool = false
	state.Pool = []PoolLayer{{Ticks: 7}}
	state.TotalPoolTicks, state.PendingPoolBeforeTicks = 7, 7
	if err := state.Validate(); err != nil {
		t.Fatal(err)
	}
	state, _, err = ConfirmLanded(state, state.HostUserID, testNow+2)
	if err != nil {
		t.Fatal(err)
	}
	next, _, err = AddToPool(state, state.CurrentUserID, 1, testNow+3)
	if err != nil || next.TotalPoolTicks != 8 {
		t.Fatalf("remainder next=%+v err=%v", next, err)
	}
}

func TestFullPoolSevenAutomaticallyContinuesWithZeroAdd(t *testing.T) {
	config := DefaultConfig(false)
	state := pendingState(t, 3, 4, 8, config, 4)
	next, events, err := ConfirmLanded(state, state.HostUserID, testNow+2)
	if err != nil || next.Phase != PhaseAwaitingContinue || next.LastSettlement.Result != ResultSeven ||
		next.LastSettlement.EffectTicks != 0 || countEvent(events, EventPoolChanged) != 1 {
		t.Fatalf("next=%+v events=%+v err=%v", next, events, err)
	}
	if err := next.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestEightNineAndDoubleFourConservePoolTicks(t *testing.T) {
	for _, test := range []struct {
		name                 string
		one, two             uint32
		pool, after, penalty dice.Ticks
		sameActor            bool
	}{
		{"eight_odd", 3, 5, 5, 2, 3, false},
		{"nine", 3, 6, 5, 0, 5, false},
		{"double_four", 4, 4, 5, 2, 3, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := pendingState(t, test.one, test.two, test.pool, DefaultConfig(false), 4)
			source := state.SourceUserID
			next, _, err := ConfirmLanded(state, state.HostUserID, testNow+2)
			if err != nil {
				t.Fatal(err)
			}
			if next.TotalPoolTicks != test.after || player(t, next, source).PenaltyTicks != test.penalty {
				t.Fatalf("next=%+v", next)
			}
			if test.sameActor && next.CurrentUserID != source {
				t.Fatalf("double four actor=%s", next.CurrentUserID)
			}
			if next.LastSettlement.EffectTicks != test.penalty || next.LastSettlement.PenaltyTicks != test.penalty {
				t.Fatalf("settlement=%+v", next.LastSettlement)
			}
		})
	}
}

func TestStackedHalfPoolRemovesTopLayersFirst(t *testing.T) {
	state := pendingState(t, 3, 5, 17, DefaultConfig(true), 4)
	next, events, err := ConfirmLanded(state, state.HostUserID, testNow+2)
	if err != nil {
		t.Fatal(err)
	}
	if next.TotalPoolTicks != 8 || len(next.Pool) != 1 || next.Pool[0].Ticks != 8 ||
		player(t, next, state.SourceUserID).PenaltyTicks != 9 {
		t.Fatalf("next=%+v", next)
	}
	for _, event := range events {
		if event.Kind == EventPoolChanged && (len(event.PoolBefore) != 3 || len(event.PoolAfter) != 1) {
			t.Fatalf("pool event=%+v", event)
		}
	}
}

func TestPairAndSpecialTargetFlows(t *testing.T) {
	state := pendingState(t, 3, 3, 2, DefaultConfig(false), 4)
	source := state.SourceUserID
	next, events, err := ConfirmLanded(state, state.HostUserID, testNow+2)
	if err != nil {
		t.Fatal(err)
	}
	if next.Direction != DirectionCounterClockwise || next.CurrentUserID == source || countEvent(events, EventDirectionChanged) != 1 {
		t.Fatalf("next=%+v events=%+v", next, events)
	}

	state = pendingState(t, 3, 3, 2, DefaultConfig(false), 2)
	source = state.SourceUserID
	next, _, err = ConfirmLanded(state, state.HostUserID, testNow+2)
	if err != nil || next.Direction != DirectionClockwise || next.CurrentUserID != source {
		t.Fatalf("two player pair=%+v err=%v", next, err)
	}

	state = pendingState(t, 1, 1, 5, DefaultConfig(false), 4)
	target := state.Players[2].UserID
	state, _, err = ConfirmLanded(state, state.HostUserID, testNow+2)
	if err != nil {
		t.Fatal(err)
	}
	next, _, err = ChooseTarget(state, state.SourceUserID, target, testNow+3)
	if err != nil || next.CurrentUserID != target || next.TotalPoolTicks != 0 || player(t, next, target).PenaltyTicks != 5 {
		t.Fatalf("double one=%+v err=%v", next, err)
	}

	state = pendingState(t, 6, 6, 2, DefaultConfig(false), 4)
	source = state.SourceUserID
	target = state.Players[2].UserID
	state, _, _ = ConfirmLanded(state, state.HostUserID, testNow+2)
	state, _, err = ChooseTarget(state, source, target, testNow+3)
	if err != nil || state.CurrentUserID != target || state.Phase != PhaseAwaitingAdd {
		t.Fatalf("double six target=%+v err=%v", state, err)
	}
	next, _, err = AddToPool(state, target, 1, testNow+4)
	if err != nil || next.CurrentUserID == source || next.CurrentUserID == target || next.LastSettlement.TargetUserID != target {
		t.Fatalf("double six=%+v err=%v", next, err)
	}
}

func TestHostDropAuthorizationAndAudit(t *testing.T) {
	state := pendingState(t, 2, 3, 5, DefaultConfig(false), 4)
	if state.HostUserID == state.SourceUserID {
		t.Fatal("fixture must separate host and source")
	}
	if _, _, err := ConfirmLanded(state, state.SourceUserID, testNow+2); ErrorCodeOf(err) != CodeNotHost {
		t.Fatalf("error=%v", err)
	}
	next, events, err := ReportDropped(state, state.HostUserID, "action-123", "manual table report", testNow+2)
	if err != nil {
		t.Fatal(err)
	}
	if next.TotalPoolTicks != 0 || next.CurrentUserID == state.SourceUserID || next.LastSettlement.AuditRef != "action-123" || countEvent(events, EventTurnDropped) != 1 {
		t.Fatalf("next=%+v events=%+v", next, events)
	}
	if next.LastSettlement.DropReason != "manual table report" {
		t.Fatalf("drop reason = %q", next.LastSettlement.DropReason)
	}
	if _, _, err := ReportDropped(next, state.HostUserID, "late", "late report", testNow+3); ErrorCodeOf(err) != CodeDropReportClosed {
		t.Fatalf("late error=%v", err)
	}
}

func TestTimersContinueModesAndStaleToken(t *testing.T) {
	for _, mode := range []ContinueMode{ContinueOptional, ContinueForcedPass, ContinueForcedReroll} {
		config := DefaultConfig(false)
		config.ContinueMode = mode
		state := pendingState(t, 3, 5, 4, config, 4)
		state, _, _ = ConfirmLanded(state, state.HostUserID, testNow+2)
		timer := *CurrentTimer(state)
		next, _, err := HandleTimeout(state, timer, timer.DeadlineUnixMillis)
		if err != nil {
			t.Fatalf("mode %d: %v", mode, err)
		}
		if mode == ContinueForcedReroll && next.CurrentUserID != state.SourceUserID {
			t.Fatalf("forced reroll next=%s", next.CurrentUserID)
		}
		if mode != ContinueForcedReroll && next.CurrentUserID == state.SourceUserID {
			t.Fatalf("pass mode next=%s", next.CurrentUserID)
		}
		timer.PendingResult = ResultNine
		if _, _, err := HandleTimeout(state, timer, timer.DeadlineUnixMillis); ErrorCodeOf(err) != CodeTimerMismatch {
			t.Fatalf("stale error=%v", err)
		}
	}
}

func TestResultWindowTimerRemainsWhenActionTimersDisabled(t *testing.T) {
	config := DefaultConfig(false)
	config.ActionTimeoutSeconds = 0
	state := newTestState(t, 4, config)
	if CurrentTimer(state) != nil {
		t.Fatal("awaiting roll retained disabled action timer")
	}
	state, _, err := applyRoll(state, state.CurrentUserID, 3, 4, testNow+1)
	if err != nil {
		t.Fatal(err)
	}
	timer := CurrentTimer(state)
	if timer == nil || timer.Phase != PhaseResultPending || timer.SourceUserID != state.SourceUserID || timer.PendingResult != ResultSeven {
		t.Fatalf("result timer=%+v", timer)
	}
	next, _, err := HandleTimeout(state, *timer, timer.DeadlineUnixMillis)
	if err != nil || next.Phase != PhaseAwaitingAdd || CurrentTimer(next) != nil {
		t.Fatalf("next=%+v err=%v", next, err)
	}
}

func TestEachPhaseTimeoutFallback(t *testing.T) {
	t.Run("awaiting_roll_passes", func(t *testing.T) {
		state := newTestState(t, 4, DefaultConfig(false))
		actor := state.CurrentUserID
		timer := *CurrentTimer(state)
		next, _, err := HandleTimeout(state, timer, timer.DeadlineUnixMillis)
		if err != nil || next.CurrentUserID == actor || next.LastSettlement.Result != ResultRollTimeout {
			t.Fatalf("next=%+v err=%v", next, err)
		}
	})
	t.Run("awaiting_add_uses_minimum", func(t *testing.T) {
		config := DefaultConfig(false)
		config.AddStepTicks = 2
		state := pendingState(t, 3, 4, 2, config, 4)
		state, _, _ = ConfirmLanded(state, state.HostUserID, testNow+2)
		timer := *CurrentTimer(state)
		next, events, err := HandleTimeout(state, timer, timer.DeadlineUnixMillis)
		if err != nil || next.TotalPoolTicks != 4 || events[0].Reason != "timeout_add" {
			t.Fatalf("next=%+v events=%+v err=%v", next, events, err)
		}
	})
	t.Run("awaiting_target_uses_direction", func(t *testing.T) {
		state := pendingState(t, 1, 1, 2, DefaultConfig(false), 4)
		source := state.SourceUserID
		state, _, _ = ConfirmLanded(state, state.HostUserID, testNow+2)
		want, _ := nextActiveUser(state, source)
		timer := *CurrentTimer(state)
		next, events, err := HandleTimeout(state, timer, timer.DeadlineUnixMillis)
		if err != nil || next.CurrentUserID != want || events[0].Reason != "timeout_target" {
			t.Fatalf("next=%+v events=%+v err=%v", next, events, err)
		}
	})
}

func TestPenaltyOverflowRejectsWholeEffect(t *testing.T) {
	state := pendingState(t, 3, 6, 5, DefaultConfig(false), 4)
	index := playerIndex(state.Players, state.SourceUserID)
	state.Players[index].PenaltyTicks = ^dice.Ticks(0) - 2
	before := state.Clone()
	_, _, err := ConfirmLanded(state, state.HostUserID, testNow+2)
	if ErrorCodeOf(err) != CodePenaltyOverflow || !reflect.DeepEqual(state, before) {
		t.Fatalf("error=%v state mutated=%v", err, !reflect.DeepEqual(state, before))
	}
}

func TestRevocationBeforeAndAfterEffect(t *testing.T) {
	state := pendingState(t, 3, 5, 5, DefaultConfig(false), 4)
	source := state.SourceUserID
	next, events, err := RevokeParticipant(state, source, testNow+2)
	if err != nil || next.LastSettlement.Result != ResultCancelled || next.TotalPoolTicks != 5 || player(t, next, source).PenaltyTicks != 0 {
		t.Fatalf("before=%+v err=%v", next, err)
	}
	if events[0].PhaseBefore != PhaseResultPending || events[0].Result != ResultEight || !events[0].PendingEffectCancelled || events[0].NextUserID == "" {
		t.Fatalf("before-effect revocation audit=%+v", events[0])
	}

	state = pendingState(t, 3, 5, 5, DefaultConfig(false), 4)
	source = state.SourceUserID
	state, _, _ = ConfirmLanded(state, state.HostUserID, testNow+2)
	next, events, err = RevokeParticipant(state, source, testNow+3)
	if err != nil || next.TotalPoolTicks != 2 || player(t, next, source).PenaltyTicks != 3 || next.LastSettlement.Result != ResultEight {
		t.Fatalf("after=%+v err=%v", next, err)
	}
	if events[0].PhaseBefore != PhaseAwaitingContinue || events[0].PendingEffectCancelled || events[0].NextUserID == "" {
		t.Fatalf("after-effect revocation audit=%+v", events[0])
	}

	state = pendingState(t, 6, 6, 2, DefaultConfig(false), 4)
	source = state.SourceUserID
	target := state.Players[2].UserID
	state, _, _ = ConfirmLanded(state, state.HostUserID, testNow+2)
	state, _, _ = ChooseTarget(state, source, target, testNow+3)
	next, events, err = RevokeParticipant(state, target, testNow+4)
	if err != nil || next.Phase != PhaseAwaitingTarget || next.CurrentUserID != source || next.TargetUserID != "" {
		t.Fatalf("target revoke=%+v err=%v", next, err)
	}
	if events[0].PhaseBefore != PhaseAwaitingAdd || !events[0].TargetSelectionReopened || events[0].TargetUserID != target {
		t.Fatalf("target revocation audit=%+v", events[0])
	}
}

func TestRevocationBelowTwoAndHostFinishAreTerminal(t *testing.T) {
	state := newTestState(t, 2, DefaultConfig(false))
	next, events, err := RevokeParticipant(state, state.CurrentUserID, testNow+1)
	if err != nil || next.Phase != PhaseFinished || next.FinishReason != FinishInsufficientParticipants ||
		CurrentTimer(next) != nil || countEvent(events, EventSessionFinished) != 1 {
		t.Fatalf("next=%+v events=%+v err=%v", next, events, err)
	}
	state = newTestState(t, 12, DefaultConfig(true))
	next, events, err = Finish(state, FinishHostRequested)
	if err != nil || next.Phase != PhaseFinished || next.FinishReason != FinishHostRequested || CurrentTimer(next) != nil ||
		countEvent(events, EventSessionFinished) != 1 {
		t.Fatalf("next=%+v events=%+v err=%v", next, events, err)
	}
}

func TestAppliedEffectCanFinishWithoutInventingSettlement(t *testing.T) {
	state := pendingState(t, 3, 5, 5, DefaultConfig(false), 2)
	applied, _, err := ConfirmLanded(state, state.HostUserID, testNow+2)
	if err != nil || applied.Phase != PhaseAwaitingContinue || applied.LastSettlement.Turn != applied.Turn {
		t.Fatalf("applied=%+v err=%v", applied, err)
	}

	finished, _, err := Finish(applied, FinishHostRequested)
	if err != nil || finished.Validate() != nil || finished.LastSettlement.NextUserID != "" {
		t.Fatalf("host finish=%+v err=%v validate=%v", finished, err, finished.Validate())
	}

	revoked, events, err := RevokeParticipant(applied, applied.SourceUserID, testNow+3)
	if err != nil || revoked.FinishReason != FinishInsufficientParticipants || revoked.Validate() != nil ||
		revoked.LastSettlement.NextUserID != "" || len(events) != 2 {
		t.Fatalf("insufficient finish=%+v events=%+v err=%v validate=%v", revoked, events, err, revoked.Validate())
	}
}

func TestDeterminismAndSDKErrorMapping(t *testing.T) {
	state := newTestState(t, 4, DefaultConfig(false))
	first, firstEvents, err := Roll(state, state.CurrentUserID, testNow+1, [32]byte{9})
	if err != nil {
		t.Fatal(err)
	}
	second, secondEvents, err := Roll(state, state.CurrentUserID, testNow+1, [32]byte{9})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(firstEvents, secondEvents) {
		t.Fatal("same inputs are not deterministic")
	}
	_, _, err = Roll(state, "not-current", testNow+1, [32]byte{1})
	if !errors.Is(err, game.ErrInvalidContract) {
		t.Fatalf("rule error does not unwrap SDK contract: %v", err)
	}
}

func newTestState(t *testing.T, count int, config Config) State {
	t.Helper()
	participants := testParticipants(count)
	host := participants[count-1].UserID
	state, _, err := NewState(config, participants, host, participants[0].SeatIndex, testNow, [32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func pendingState(t *testing.T, one, two uint32, pool dice.Ticks, config Config, count int) State {
	t.Helper()
	state := newTestState(t, count, config)
	if err := setPoolTotal(&state, pool); err != nil {
		t.Fatal(err)
	}
	next, _, err := applyRoll(state, state.CurrentUserID, one, two, testNow+1)
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Validate(); err != nil {
		t.Fatal(err)
	}
	return next
}

func testParticipants(count int) []Participant {
	participants := make([]Participant, count)
	for index := range participants {
		participants[index] = Participant{UserID: "user-" + string(rune('a'+index)), SeatIndex: uint32(index * 2)}
	}
	return participants
}

func player(t *testing.T, state State, userID string) PlayerState {
	t.Helper()
	for _, value := range state.Players {
		if value.UserID == userID {
			return value
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
