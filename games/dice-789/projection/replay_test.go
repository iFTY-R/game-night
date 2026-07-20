package projection

import (
	"testing"

	"github.com/iFTY-R/game-night/games/dice-789/engine"
)

func TestBuildReplayAcceptsSettledPublicTurn(t *testing.T) {
	replay, err := BuildReplay(validReplayEvents())
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.GetTurns()) != 1 || !replay.GetTurns()[0].GetSettled() || replay.GetTurns()[0].GetSummary().GetPoolAfterTicks() != 3 {
		t.Fatalf("unexpected replay: %+v", replay)
	}
}

func TestBuildReplayRejectsCorruptedLifecycle(t *testing.T) {
	base := validReplayEvents()
	tests := map[string][]engine.Event{
		"duplicate roll":     append(insertAfter(base, 1, base[1]), base[2:]...),
		"effect before roll": {base[0], base[2], base[1]},
		"wrong roll source":  {base[0], withSource(base[1], "user-2")},
		"wrong roll result":  {base[0], base[1], withResult(base[2], engine.ResultEight)},
		"wrong target source": {
			base[0],
			{Kind: engine.EventDiceRolled, Turn: 1, UserID: "user-1", SourceUserID: "user-1", DieOne: 1, DieTwo: 1, Sum: 2},
			{Kind: engine.EventEffectSelected, Turn: 1, UserID: "user-1", SourceUserID: "user-1", DieOne: 1, DieTwo: 1, Sum: 2, Result: engine.ResultDoubleOne, PhaseAfter: engine.PhaseAwaitingTarget},
			{Kind: engine.EventTargetSelected, Turn: 1, UserID: "user-2", SourceUserID: "user-2", TargetUserID: "user-3", Result: engine.ResultDoubleOne},
		},
		"drop after effect":  {base[0], base[1], base[2], {Kind: engine.EventTurnDropped, Turn: 1, UserID: "user-1", SourceUserID: "user-1", Reason: "dropped", AuditRef: "audit-1"}},
		"pool discontinuity": {base[0], base[1], base[2], {Kind: engine.EventPoolChanged, Turn: 1, UserID: "user-1", SourceUserID: "user-1", Result: engine.ResultSeven, PoolBeforeTicks: 1, PoolAfterTicks: 2, PoolBefore: []engine.PoolLayer{{Ticks: 1}}, PoolAfter: []engine.PoolLayer{{Ticks: 2}}, EffectTicks: 1}},
		"finish wrong turn":  {base[0], {Kind: engine.EventSessionFinished, Turn: 2, Reason: engine.FinishHostRequested}},
		"finish before penalty": {
			base[0],
			{Kind: engine.EventDiceRolled, Turn: 1, UserID: "user-1", SourceUserID: "user-1", DieOne: 3, DieTwo: 5, Sum: 8},
			{Kind: engine.EventEffectSelected, Turn: 1, UserID: "user-1", SourceUserID: "user-1", DieOne: 3, DieTwo: 5, Sum: 8, Result: engine.ResultEight, PhaseAfter: engine.PhaseAwaitingContinue},
			{Kind: engine.EventPoolChanged, Turn: 1, UserID: "user-1", SourceUserID: "user-1", Result: engine.ResultEight, PoolBeforeTicks: 2, PoolAfterTicks: 1, PoolBefore: []engine.PoolLayer{{Ticks: 2}}, PoolAfter: []engine.PoolLayer{{Ticks: 1}}, EffectTicks: 1},
			{Kind: engine.EventSessionFinished, Turn: 1, Reason: engine.FinishHostRequested},
		},
		"event after finish": {base[0], {Kind: engine.EventSessionFinished, Turn: 1, Reason: engine.FinishHostRequested}, base[1]},
	}
	for name, events := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := BuildReplay(events); err == nil {
				t.Fatal("corrupted replay was accepted")
			}
		})
	}
}

func TestBuildReplayRejectsRuleSemanticTampering(t *testing.T) {
	base := validDrinkReplayEvents()
	wrongPenalty := append([]engine.Event(nil), base...)
	wrongPenalty[4].UserID = "user-2"
	brokenCumulative := append([]engine.Event(nil), base...)
	brokenCumulative[4].PenaltyBeforeTicks, brokenCumulative[4].PenaltyAfterTicks = 1, 2
	changedEffect := append([]engine.Event(nil), base...)
	changedEffect[3].Result = engine.ResultDoubleOne
	wrongNine := append([]engine.Event(nil), base...)
	wrongNine[1].DieOne, wrongNine[1].DieTwo, wrongNine[1].Sum, wrongNine[1].Result = 3, 6, 9, engine.ResultNine
	wrongNine[2].Result, wrongNine[3].Result, wrongNine[4].Result, wrongNine[5].Result = engine.ResultNine, engine.ResultNine, engine.ResultNine, engine.ResultNine
	duplicatePool := append([]engine.Event(nil), base[:4]...)
	duplicatePool = append(duplicatePool, engine.Event{Kind: engine.EventPoolChanged, Turn: 1, UserID: "user-1", SourceUserID: "user-1", Result: engine.ResultEight, PoolBeforeTicks: 1, PoolAfterTicks: 0, PoolBefore: []engine.PoolLayer{{Ticks: 1}}, PoolAfter: []engine.PoolLayer{{Ticks: 0}}, EffectTicks: 1})
	duplicatePool = append(duplicatePool, base[4:]...)
	duplicateTarget := []engine.Event{
		base[0],
		{Kind: engine.EventDiceRolled, Turn: 1, UserID: "user-1", SourceUserID: "user-1", DieOne: 6, DieTwo: 6, Sum: 12},
		{Kind: engine.EventEffectSelected, Turn: 1, UserID: "user-1", SourceUserID: "user-1", DieOne: 6, DieTwo: 6, Sum: 12, Result: engine.ResultDoubleSix, PhaseAfter: engine.PhaseAwaitingTarget},
		{Kind: engine.EventTargetSelected, Turn: 1, UserID: "user-1", SourceUserID: "user-1", TargetUserID: "user-2", Result: engine.ResultDoubleSix},
		{Kind: engine.EventTargetSelected, Turn: 1, UserID: "user-1", SourceUserID: "user-1", TargetUserID: "user-3", Result: engine.ResultDoubleSix},
	}
	nonAdjacent := append([]engine.Event(nil), base...)
	nonAdjacent[5].NextUserID = "user-3"
	startMismatch := append([]engine.Event(nil), base...)
	startMismatch[6].UserID, startMismatch[6].NextUserID = "user-3", "user-3"

	configConflict := append([]engine.Event(nil), base...)
	config := *configConflict[0].InitialConfig
	config.InitialPoolTicks = 3
	configConflict[0].InitialConfig = &config
	invalidStep := validReplayEvents()
	stepConfig := *invalidStep[0].InitialConfig
	stepConfig.AddStepTicks = 2
	invalidStep[0].InitialConfig = &stepConfig
	missingSelection := []engine.Event{
		base[0],
		{Kind: engine.EventDiceRolled, Turn: 1, UserID: "user-1", SourceUserID: "user-1", DieOne: 2, DieTwo: 3, Sum: 5, Result: engine.ResultOther},
		{Kind: engine.EventTurnSettled, Turn: 1, UserID: "user-1", SourceUserID: "user-1", Result: engine.ResultOther, DirectionBefore: engine.DirectionClockwise, Direction: engine.DirectionClockwise, NextUserID: "user-2", Reason: "pass"},
	}
	finishBeforeEightFacts := []engine.Event{
		base[0],
		{Kind: engine.EventDiceRolled, Turn: 1, UserID: "user-1", SourceUserID: "user-1", DieOne: 3, DieTwo: 5, Sum: 8, Result: engine.ResultEight},
		{Kind: engine.EventEffectSelected, Turn: 1, UserID: "user-1", SourceUserID: "user-1", DieOne: 3, DieTwo: 5, Sum: 8, Result: engine.ResultEight, PhaseAfter: engine.PhaseAwaitingContinue},
		{Kind: engine.EventSessionFinished, Turn: 1, Reason: engine.FinishHostRequested},
	}
	fullConfig := engine.DefaultConfig(false)
	fullConfig.InitialPoolTicks = fullConfig.LayerCapacityTicks
	fullPool := []engine.PoolLayer{{Ticks: fullConfig.InitialPoolTicks}}
	finishBeforeFullSevenFact := []engine.Event{
		{Kind: engine.EventTurnStarted, Turn: 1, UserID: "user-1", NextUserID: "user-1", Direction: engine.DirectionClockwise, InitialConfig: &fullConfig, InitialParticipants: base[0].InitialParticipants, InitialPool: fullPool, InitialTotalPoolTicks: fullConfig.InitialPoolTicks},
		{Kind: engine.EventDiceRolled, Turn: 1, UserID: "user-1", SourceUserID: "user-1", DieOne: 2, DieTwo: 5, Sum: 7, Result: engine.ResultSeven},
		{Kind: engine.EventEffectSelected, Turn: 1, UserID: "user-1", SourceUserID: "user-1", DieOne: 2, DieTwo: 5, Sum: 7, Result: engine.ResultSeven, PhaseAfter: engine.PhaseAwaitingContinue},
		{Kind: engine.EventSessionFinished, Turn: 1, Reason: engine.FinishHostRequested},
	}

	tests := map[string][]engine.Event{
		"wrong penalty target":      wrongPenalty,
		"penalty cumulative break":  brokenCumulative,
		"post-selected effect swap": changedEffect,
		"wrong nine amount":         wrongNine,
		"duplicate pool event":      duplicatePool,
		"duplicate target event":    duplicateTarget,
		"non-adjacent next player":  nonAdjacent,
		"next turn actor mismatch":  startMismatch,
		"initial pool config drift": configConflict,
		"invalid add step":          invalidStep,
		"settlement without effect": missingSelection,
		"finish before eight facts": finishBeforeEightFacts,
		"finish before full seven":  finishBeforeFullSevenFact,
	}
	for name, events := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := BuildReplay(events); err == nil {
				t.Fatal("rule-semantic corruption was accepted")
			}
		})
	}
}

func TestBuildReplayAcceptsStreamEndingAtSettlement(t *testing.T) {
	events := validDrinkReplayEvents()
	replay, err := BuildReplay(events[:6])
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.GetTurns()) != 1 || !replay.GetTurns()[0].GetSettled() {
		t.Fatalf("settlement-ending replay=%+v", replay.GetTurns())
	}
}

func withSource(value engine.Event, source string) engine.Event {
	value.SourceUserID = source
	return value
}

func withResult(value engine.Event, result engine.ResultKind) engine.Event {
	value.Result = result
	return value
}

func validReplayEvents() []engine.Event {
	config := engine.DefaultConfig(false)
	participants := []engine.Participant{{UserID: "user-1", SeatIndex: 0}, {UserID: "user-2", SeatIndex: 1}, {UserID: "user-3", SeatIndex: 2}}
	poolBefore := []engine.PoolLayer{{Ticks: 2}}
	poolAfter := []engine.PoolLayer{{Ticks: 3}}
	return []engine.Event{
		{Kind: engine.EventTurnStarted, Turn: 1, UserID: "user-1", NextUserID: "user-1", Direction: engine.DirectionClockwise, InitialConfig: &config, InitialParticipants: participants, InitialPool: poolBefore, InitialTotalPoolTicks: 2},
		{Kind: engine.EventDiceRolled, Turn: 1, UserID: "user-1", SourceUserID: "user-1", DieOne: 2, DieTwo: 5, Sum: 7, Result: engine.ResultSeven},
		{Kind: engine.EventEffectSelected, Turn: 1, UserID: "user-1", SourceUserID: "user-1", DieOne: 2, DieTwo: 5, Sum: 7, Result: engine.ResultSeven, PhaseAfter: engine.PhaseAwaitingAdd, Reason: "confirmed"},
		{Kind: engine.EventPoolChanged, Turn: 1, UserID: "user-1", SourceUserID: "user-1", Result: engine.ResultSeven, PoolBeforeTicks: 2, PoolAfterTicks: 3, PoolBefore: poolBefore, PoolAfter: poolAfter, EffectTicks: 1},
		{Kind: engine.EventTurnSettled, Turn: 1, UserID: "user-1", SourceUserID: "user-1", Result: engine.ResultSeven, DirectionBefore: engine.DirectionClockwise, Direction: engine.DirectionClockwise, PoolBeforeTicks: 2, PoolAfterTicks: 3, PoolBefore: poolBefore, PoolAfter: poolAfter, EffectTicks: 1, NextUserID: "user-2", Reason: "pass"},
		{Kind: engine.EventTurnStarted, Turn: 2, UserID: "user-2", NextUserID: "user-2", Direction: engine.DirectionClockwise},
	}
}

func validDrinkReplayEvents() []engine.Event {
	config := engine.DefaultConfig(false)
	participants := []engine.Participant{{UserID: "user-1", SeatIndex: 0}, {UserID: "user-2", SeatIndex: 1}, {UserID: "user-3", SeatIndex: 2}}
	poolBefore := []engine.PoolLayer{{Ticks: 2}}
	poolAfter := []engine.PoolLayer{{Ticks: 1}}
	return []engine.Event{
		{Kind: engine.EventTurnStarted, Turn: 1, UserID: "user-1", NextUserID: "user-1", Direction: engine.DirectionClockwise, InitialConfig: &config, InitialParticipants: participants, InitialPool: poolBefore, InitialTotalPoolTicks: 2},
		{Kind: engine.EventDiceRolled, Turn: 1, UserID: "user-1", SourceUserID: "user-1", DieOne: 3, DieTwo: 5, Sum: 8, Result: engine.ResultEight},
		{Kind: engine.EventEffectSelected, Turn: 1, UserID: "user-1", SourceUserID: "user-1", DieOne: 3, DieTwo: 5, Sum: 8, Result: engine.ResultEight, PhaseAfter: engine.PhaseAwaitingContinue, Reason: "confirmed"},
		{Kind: engine.EventPoolChanged, Turn: 1, UserID: "user-1", SourceUserID: "user-1", Result: engine.ResultEight, PoolBeforeTicks: 2, PoolAfterTicks: 1, PoolBefore: poolBefore, PoolAfter: poolAfter, EffectTicks: 1},
		{Kind: engine.EventPenaltyRecorded, Turn: 1, UserID: "user-1", SourceUserID: "user-1", Result: engine.ResultEight, PenaltyTicks: 1, PenaltyBeforeTicks: 0, PenaltyAfterTicks: 1, Reason: "eight"},
		{Kind: engine.EventTurnSettled, Turn: 1, UserID: "user-1", SourceUserID: "user-1", Result: engine.ResultEight, DirectionBefore: engine.DirectionClockwise, Direction: engine.DirectionClockwise, PoolBeforeTicks: 2, PoolAfterTicks: 1, PoolBefore: poolBefore, PoolAfter: poolAfter, EffectTicks: 1, PenaltyTicks: 1, NextUserID: "user-2", Reason: "pass"},
		{Kind: engine.EventTurnStarted, Turn: 2, UserID: "user-2", NextUserID: "user-2", Direction: engine.DirectionClockwise},
	}
}

func insertAfter(values []engine.Event, index int, value engine.Event) []engine.Event {
	result := append([]engine.Event(nil), values[:index+1]...)
	return append(result, value)
}
