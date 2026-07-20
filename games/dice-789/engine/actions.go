package engine

import (
	"math"

	"github.com/iFTY-R/game-night/sdk/go/game/dice"
)

// Roll deterministically exposes two dice and opens the short host confirmation window.
func Roll(state State, actor string, nowUnixMillis int64, seed [32]byte) (State, []Event, error) {
	if err := validateActor(state, actor, PhaseAwaitingRoll, nowUnixMillis, false); err != nil {
		return State{}, nil, err
	}
	faces, err := dice.Roll(seed, uint64(state.Turn), 2)
	if err != nil {
		return State{}, nil, ruleError(CodeSeedInvalid, "deterministic seed is invalid")
	}
	return applyRoll(state, actor, uint32(faces[0]), uint32(faces[1]), nowUnixMillis)
}

// applyRoll is the test seam for the 36-face priority matrix; production enters through Roll.
func applyRoll(state State, actor string, dieOne, dieTwo uint32, nowUnixMillis int64) (State, []Event, error) {
	if err := validateActor(state, actor, PhaseAwaitingRoll, nowUnixMillis, false); err != nil {
		return State{}, nil, err
	}
	if dieOne < 1 || dieOne > 6 || dieTwo < 1 || dieTwo > 6 {
		return State{}, nil, ruleError(CodeInvalidAction, "dice faces must be in 1..6")
	}
	next := state.Clone()
	next.Phase = PhaseResultPending
	next.SourceUserID = actor
	next.CurrentUserID = actor
	next.TargetUserID = ""
	next.DieOne, next.DieTwo = dieOne, dieTwo
	next.Sum = next.DieOne + next.DieTwo
	result, err := ClassifyRoll(next.Config, next.DieOne, next.DieTwo)
	if err != nil {
		return State{}, nil, err
	}
	next.PendingResult = result
	next.PendingPoolBeforeTicks = next.TotalPoolTicks
	next.PendingDirectionBefore = next.Direction
	deadline, err := deadlineUnixMillis(nowUnixMillis, next.Config.DropReportWindowSeconds)
	if err != nil {
		return State{}, nil, err
	}
	next.ActionDeadlineUnixMillis = deadline
	return next, []Event{{
		Kind: EventDiceRolled, Turn: state.Turn, UserID: actor, SourceUserID: actor,
		DieOne: next.DieOne, DieTwo: next.DieTwo, Sum: next.Sum, Result: next.PendingResult,
	}}, nil
}

// ConfirmLanded applies the selected result after the frozen host confirms no die fell.
func ConfirmLanded(state State, actor string, nowUnixMillis int64) (State, []Event, error) {
	if err := validateHost(state, actor, nowUnixMillis); err != nil {
		return State{}, nil, err
	}
	return applyLanded(state, nowUnixMillis, "confirmed")
}

// ReportDropped records the host audit reference and resolves a full-pool penalty.
func ReportDropped(state State, actor string, auditRef, reason string, nowUnixMillis int64) (State, []Event, error) {
	if err := validateHost(state, actor, nowUnixMillis); err != nil {
		return State{}, nil, err
	}
	if auditRef == "" || reason == "" {
		return State{}, nil, ruleError(CodeInvalidAction, "drop report requires an audit reference and reason")
	}
	next := state.Clone()
	penaltyBefore := next.Players[playerIndex(next.Players, next.SourceUserID)].PenaltyTicks
	beforeLayers := append([]PoolLayer(nil), next.Pool...)
	before := next.TotalPoolTicks
	if err := addPenalty(&next, next.SourceUserID, before); err != nil {
		return State{}, nil, err
	}
	if err := setPoolTotal(&next, 0); err != nil {
		return State{}, nil, err
	}
	next.LastSettlement = settlementFromState(next, ResultDropped, next.SourceUserID, "dropped")
	next.LastSettlement.AuditRef = auditRef
	next.LastSettlement.DropReason = reason
	settled, settledEvents, err := finishTurn(next, next.SourceUserID, "", nowUnixMillis)
	if err != nil {
		return State{}, nil, err
	}
	events := []Event{
		{Kind: EventTurnDropped, Turn: state.Turn, UserID: actor, SourceUserID: state.SourceUserID, Reason: reason, AuditRef: auditRef},
		poolChangedEvent(state, state.SourceUserID, beforeLayers, settled.LastSettlement.PoolAfter, before, 0, before),
		{Kind: EventPenaltyRecorded, Turn: state.Turn, UserID: state.SourceUserID, SourceUserID: state.SourceUserID,
			Result: ResultDropped, PenaltyTicks: before, PenaltyBeforeTicks: penaltyBefore,
			PenaltyAfterTicks: settled.Players[playerIndex(settled.Players, state.SourceUserID)].PenaltyTicks, Reason: "dropped"},
	}
	return settled, append(events, settledEvents...), nil
}

// AddToPool applies a legal 7 or double-six add.
func AddToPool(state State, actor string, amount dice.Ticks, nowUnixMillis int64) (State, []Event, error) {
	return addToPool(state, actor, amount, nowUnixMillis, false)
}

func addToPool(state State, actor string, amount dice.Ticks, nowUnixMillis int64, timer bool) (State, []Event, error) {
	if err := validateActor(state, actor, PhaseAwaitingAdd, nowUnixMillis, timer); err != nil {
		return State{}, nil, err
	}
	if err := validateAddAmount(state, amount); err != nil {
		return State{}, nil, err
	}
	next := state.Clone()
	beforeLayers := append([]PoolLayer(nil), next.Pool...)
	before := next.TotalPoolTicks
	if err := setPoolTotal(&next, before+amount); err != nil {
		return State{}, nil, err
	}
	events := []Event{poolChangedEvent(state, actor, beforeLayers, next.Pool, before, next.TotalPoolTicks, amount)}
	if timer {
		events[0].Reason = "timeout_add"
	}
	if state.PendingResult == ResultDoubleSix {
		target := state.TargetUserID
		reason := "double_six_add"
		if timer {
			reason = "timeout_add"
		}
		next.LastSettlement = settlementFromState(next, ResultNone, target, reason)
		settled, settledEvents, err := finishTurn(next, state.SourceUserID, "", nowUnixMillis)
		if err != nil {
			return State{}, nil, err
		}
		return settled, append(events, settledEvents...), nil
	}
	next.LastSettlement = settlementFromState(next, ResultNone, "", "seven_add")
	next.Phase = PhaseAwaitingContinue
	next.CurrentUserID = next.SourceUserID
	next.TargetUserID = ""
	var err error
	next.ActionDeadlineUnixMillis, err = deadlineUnixMillis(nowUnixMillis, next.Config.ActionTimeoutSeconds)
	if err != nil {
		return State{}, nil, err
	}
	return next, events, nil
}

// ChooseTarget selects a different active player for double-one or double-six.
func ChooseTarget(state State, actor string, target string, nowUnixMillis int64) (State, []Event, error) {
	return chooseTarget(state, actor, target, nowUnixMillis, false)
}

func chooseTarget(state State, actor string, target string, nowUnixMillis int64, timer bool) (State, []Event, error) {
	if err := validateActor(state, actor, PhaseAwaitingTarget, nowUnixMillis, timer); err != nil {
		return State{}, nil, err
	}
	if target == "" || target == actor || !activePlayer(state, target) {
		return State{}, nil, ruleError(CodeTargetInvalid, "target must be a different active participant")
	}
	next := state.Clone()
	next.TargetUserID = target
	events := []Event{{Kind: EventTargetSelected, Turn: state.Turn, UserID: actor, SourceUserID: actor, TargetUserID: target, Result: state.PendingResult}}
	if timer {
		events[0].Reason = "timeout_target"
	}
	if state.PendingResult == ResultDoubleOne {
		beforeLayers := append([]PoolLayer(nil), next.Pool...)
		before := next.TotalPoolTicks
		penaltyBefore := next.Players[playerIndex(next.Players, target)].PenaltyTicks
		if err := addPenalty(&next, target, before); err != nil {
			return State{}, nil, err
		}
		if err := setPoolTotal(&next, 0); err != nil {
			return State{}, nil, err
		}
		reason := "double_one"
		if timer {
			reason = "timeout_target"
		}
		next.LastSettlement = settlementFromState(next, ResultNone, target, reason)
		settled, settledEvents, err := finishTurn(next, actor, target, nowUnixMillis)
		if err != nil {
			return State{}, nil, err
		}
		poolEvent := poolChangedEvent(state, target, beforeLayers, settled.LastSettlement.PoolAfter, before, 0, before)
		poolEvent.TargetUserID = target
		events = append(events,
			poolEvent,
			Event{Kind: EventPenaltyRecorded, Turn: state.Turn, UserID: target, SourceUserID: actor, TargetUserID: target,
				Result: ResultDoubleOne, PenaltyTicks: before, PenaltyBeforeTicks: penaltyBefore,
				PenaltyAfterTicks: settled.Players[playerIndex(settled.Players, target)].PenaltyTicks, Reason: "double_one"},
		)
		return settled, append(events, settledEvents...), nil
	}
	next.CurrentUserID = target
	next.Phase = PhaseAwaitingAdd
	var err error
	next.ActionDeadlineUnixMillis, err = deadlineUnixMillis(nowUnixMillis, next.Config.ActionTimeoutSeconds)
	if err != nil {
		return State{}, nil, err
	}
	return next, events, nil
}

// Reroll starts a new awaiting-roll turn for the same source.
func Reroll(state State, actor string, nowUnixMillis int64) (State, []Event, error) {
	return continueTurn(state, actor, true, nowUnixMillis, false)
}

// Pass transfers the next awaiting-roll turn according to current direction.
func Pass(state State, actor string, nowUnixMillis int64) (State, []Event, error) {
	return continueTurn(state, actor, false, nowUnixMillis, false)
}

func continueTurn(state State, actor string, reroll bool, nowUnixMillis int64, timer bool) (State, []Event, error) {
	if err := validateActor(state, actor, PhaseAwaitingContinue, nowUnixMillis, timer); err != nil {
		return State{}, nil, err
	}
	if reroll && state.Config.ContinueMode == ContinueForcedPass || !reroll && state.Config.ContinueMode == ContinueForcedReroll {
		return State{}, nil, ruleError(CodeContinueModeInvalid, "continue action is disabled by frozen mode")
	}
	next := state.Clone()
	reason := "pass"
	nextActor := ""
	if reroll {
		reason = "reroll"
		nextActor = actor
	}
	if timer {
		if reroll {
			reason = "timeout_reroll"
		} else {
			reason = "timeout_pass"
		}
	}
	next.LastSettlement = settlementFromState(next, ResultNone, "", reason)
	return finishTurn(next, actor, nextActor, nowUnixMillis)
}

// HandleTimeout applies the deterministic fallback for one exact persisted timer.
func HandleTimeout(state State, timer ActionTimer, nowUnixMillis int64) (State, []Event, error) {
	if err := state.Validate(); err != nil {
		return State{}, nil, err
	}
	if state.Phase == PhaseFinished {
		return State{}, nil, ruleError(CodeSessionFinished, "session no longer accepts timers")
	}
	if timer.Turn != state.Turn || timer.Phase != state.Phase || timer.CurrentUserID != state.CurrentUserID ||
		timer.SourceUserID != state.SourceUserID || timer.TargetUserID != state.TargetUserID || timer.PendingResult != state.PendingResult ||
		timer.DeadlineUnixMillis == 0 || timer.DeadlineUnixMillis != state.ActionDeadlineUnixMillis {
		return State{}, nil, ruleError(CodeTimerMismatch, "timer does not match current pending effect")
	}
	if !validUnixMillis(nowUnixMillis) || nowUnixMillis < state.ActionDeadlineUnixMillis {
		return State{}, nil, ruleError(CodeTimerNotDue, "timer fired before its deadline")
	}
	switch state.Phase {
	case PhaseAwaitingRoll:
		next := state.Clone()
		next.SourceUserID = state.CurrentUserID
		next.PendingResult = ResultRollTimeout
		next.PendingPoolBeforeTicks = state.TotalPoolTicks
		next.PendingDirectionBefore = state.Direction
		next.LastSettlement = settlementFromState(next, ResultRollTimeout, "", "roll_timeout")
		return finishTurn(next, state.CurrentUserID, "", nowUnixMillis)
	case PhaseResultPending:
		return applyLanded(state, nowUnixMillis, "result_timeout")
	case PhaseAwaitingAdd:
		amount, err := minimumAddAmount(state)
		if err != nil {
			return State{}, nil, err
		}
		return addToPool(state, state.CurrentUserID, amount, nowUnixMillis, true)
	case PhaseAwaitingTarget:
		target, err := nextActiveUser(state, state.SourceUserID)
		if err != nil {
			return State{}, nil, err
		}
		return chooseTarget(state, state.SourceUserID, target, nowUnixMillis, true)
	case PhaseAwaitingContinue:
		reroll := state.Config.ContinueMode == ContinueForcedReroll
		return continueTurn(state, state.SourceUserID, reroll, nowUnixMillis, true)
	default:
		return State{}, nil, ruleError(CodeWrongPhase, "phase has no timeout fallback")
	}
}

// RevokeParticipant applies platform removal without reusing the frozen seat.
func RevokeParticipant(state State, userID string, nowUnixMillis int64) (State, []Event, error) {
	if err := state.Validate(); err != nil {
		return State{}, nil, err
	}
	if state.Phase == PhaseFinished {
		return State{}, nil, ruleError(CodeSessionFinished, "session no longer accepts participant changes")
	}
	if !validUnixMillis(nowUnixMillis) {
		return State{}, nil, ruleError(CodeInvalidState, "deterministic time is required")
	}
	index := playerIndex(state.Players, userID)
	if index < 0 || !state.Players[index].Active {
		return State{}, nil, ruleError(CodeParticipantInactive, "participant is absent or already inactive")
	}
	next := state.Clone()
	next.Players[index].Active = false
	revoked := Event{
		Kind: EventParticipantRevoked, Turn: state.Turn, UserID: userID, PhaseBefore: state.Phase,
		SourceUserID: state.SourceUserID, TargetUserID: state.TargetUserID, Result: state.PendingResult,
	}
	if state.Phase == PhaseAwaitingRoll {
		revoked.SourceUserID = state.CurrentUserID
	}
	events := []Event{revoked}
	if activeCount(next.Players) < MinimumPlayers {
		events[0].PendingEffectCancelled = state.Phase == PhaseResultPending || state.Phase == PhaseAwaitingAdd || state.Phase == PhaseAwaitingTarget
		finished, finishEvent := finishState(next, FinishInsufficientParticipants)
		return finished, append(events, finishEvent), nil
	}
	// A selected double-six target has not changed the pool; return target choice to the source.
	if state.Phase == PhaseAwaitingAdd && state.PendingResult == ResultDoubleSix && userID == state.TargetUserID {
		events[0].TargetSelectionReopened = true
		next.TargetUserID = ""
		next.CurrentUserID = next.SourceUserID
		next.Phase = PhaseAwaitingTarget
		var err error
		next.ActionDeadlineUnixMillis, err = deadlineUnixMillis(nowUnixMillis, next.Config.ActionTimeoutSeconds)
		if err != nil {
			return State{}, nil, err
		}
		return next, events, nil
	}
	if userID == state.SourceUserID || state.Phase == PhaseAwaitingRoll && userID == state.CurrentUserID {
		if state.Phase == PhaseAwaitingContinue {
			// The 7/8/9 effect is already committed; preserve it and cancel only follow-up choice.
			next.LastSettlement = settlementFromState(next, ResultNone, "", "source_revoked_after_effect")
		} else {
			if state.Phase == PhaseAwaitingRoll {
				next.SourceUserID = userID
				next.PendingPoolBeforeTicks = next.TotalPoolTicks
				next.PendingDirectionBefore = next.Direction
			}
			next.LastSettlement = settlementFromState(next, ResultCancelled, "", "cancelled")
		}
		settled, settledEvents, err := finishTurn(next, userID, "", nowUnixMillis)
		if err != nil {
			return State{}, nil, err
		}
		events[0].PendingEffectCancelled = state.Phase != PhaseAwaitingRoll && next.LastSettlement.Result == ResultCancelled
		events[0].NextUserID = settledEvents[0].NextUserID
		return settled, append(events, settledEvents...), nil
	}
	return next, events, nil
}

// Finish terminates the state. Platform host authorization must occur before this call.
func Finish(state State, reason string) (State, []Event, error) {
	if err := state.Validate(); err != nil {
		return State{}, nil, err
	}
	if state.Phase == PhaseFinished {
		return State{}, nil, ruleError(CodeSessionFinished, "session is already finished")
	}
	if reason == "" {
		reason = FinishHostRequested
	}
	if reason != FinishHostRequested && reason != FinishPlatformCancelled {
		return State{}, nil, ruleError(CodeInvalidAction, "finish reason is unsupported")
	}
	finished, event := finishState(state.Clone(), reason)
	return finished, []Event{event}, nil
}

func applyLanded(state State, nowUnixMillis int64, cause string) (State, []Event, error) {
	if state.Phase != PhaseResultPending || !state.PendingResult.validRollResult() {
		return State{}, nil, ruleError(CodeWrongPhase, "landed confirmation requires result_pending")
	}
	next := state.Clone()
	events := []Event{{Kind: EventEffectSelected, Turn: state.Turn, UserID: state.SourceUserID, SourceUserID: state.SourceUserID, Result: state.PendingResult, Reason: cause}}
	switch state.PendingResult {
	case ResultSeven:
		next.CurrentUserID = state.SourceUserID
		if next.TotalPoolTicks == mustCapacity(next.Config) {
			next.LastSettlement = settlementFromState(next, ResultNone, "", "seven_add")
			next.Phase = PhaseAwaitingContinue
			events = append(events, poolChangedEvent(state, state.SourceUserID, state.Pool, state.Pool, state.TotalPoolTicks, state.TotalPoolTicks, 0))
		} else {
			next.Phase = PhaseAwaitingAdd
		}
		events[0].PhaseAfter = next.Phase
		next.ActionDeadlineUnixMillis, _ = deadlineUnixMillis(nowUnixMillis, next.Config.ActionTimeoutSeconds)
		return next, events, nil
	case ResultEight:
		return applyDrinkAndContinue(next, state.SourceUserID, dice.HalfCeil(state.TotalPoolTicks), "eight", events, nowUnixMillis)
	case ResultNine:
		return applyDrinkAndContinue(next, state.SourceUserID, state.TotalPoolTicks, "nine", events, nowUnixMillis)
	case ResultDoubleOne, ResultDoubleSix:
		next.Phase = PhaseAwaitingTarget
		next.CurrentUserID = state.SourceUserID
		next.ActionDeadlineUnixMillis, _ = deadlineUnixMillis(nowUnixMillis, next.Config.ActionTimeoutSeconds)
		events[0].PhaseAfter = next.Phase
		return next, events, nil
	case ResultDoubleFour:
		var err error
		next, events, err = applyDrink(next, state.SourceUserID, dice.HalfCeil(state.TotalPoolTicks), "double_four", events)
		if err != nil {
			return State{}, nil, err
		}
		next.LastSettlement = settlementFromState(next, ResultNone, "", "double_four")
		settled, settledEvents, err := finishTurn(next, state.SourceUserID, state.SourceUserID, nowUnixMillis)
		events[0].PhaseAfter = settled.Phase
		return settled, append(events, settledEvents...), err
	case ResultOrdinaryPair:
		before := next.Direction
		if activeCount(next.Players) >= 3 {
			next.Direction = reverseDirection(next.Direction)
			events = append(events, Event{Kind: EventDirectionChanged, Turn: state.Turn, UserID: state.SourceUserID,
				DirectionBefore: before, Direction: next.Direction, Result: ResultOrdinaryPair})
		}
		next.LastSettlement = settlementFromState(next, ResultNone, "", "ordinary_pair")
		nextActor := ""
		if activeCount(next.Players) == 2 {
			nextActor = state.SourceUserID
		}
		settled, settledEvents, err := finishTurn(next, state.SourceUserID, nextActor, nowUnixMillis)
		events[0].PhaseAfter = settled.Phase
		return settled, append(events, settledEvents...), err
	default:
		next.LastSettlement = settlementFromState(next, ResultNone, "", "pass")
		settled, settledEvents, err := finishTurn(next, state.SourceUserID, "", nowUnixMillis)
		events[0].PhaseAfter = settled.Phase
		return settled, append(events, settledEvents...), err
	}
}

func applyDrinkAndContinue(state State, target string, amount dice.Ticks, reason string, events []Event, now int64) (State, []Event, error) {
	next, events, err := applyDrink(state, target, amount, reason, events)
	if err != nil {
		return State{}, nil, err
	}
	next.LastSettlement = settlementFromState(next, ResultNone, "", reason)
	next.Phase = PhaseAwaitingContinue
	next.CurrentUserID = next.SourceUserID
	next.TargetUserID = ""
	next.ActionDeadlineUnixMillis, err = deadlineUnixMillis(now, next.Config.ActionTimeoutSeconds)
	if err != nil {
		return State{}, nil, err
	}
	events[0].PhaseAfter = next.Phase
	return next, events, nil
}

func applyDrink(state State, target string, amount dice.Ticks, reason string, events []Event) (State, []Event, error) {
	next := state.Clone()
	beforeLayers := append([]PoolLayer(nil), next.Pool...)
	before := next.TotalPoolTicks
	penaltyBefore := next.Players[playerIndex(next.Players, target)].PenaltyTicks
	if amount > before {
		amount = before
	}
	if err := addPenalty(&next, target, amount); err != nil {
		return State{}, nil, err
	}
	if err := setPoolTotal(&next, before-amount); err != nil {
		return State{}, nil, err
	}
	events = append(events,
		poolChangedEvent(state, target, beforeLayers, next.Pool, before, next.TotalPoolTicks, amount),
		Event{Kind: EventPenaltyRecorded, Turn: state.Turn, UserID: target, SourceUserID: state.SourceUserID,
			TargetUserID: target, Result: state.PendingResult, PenaltyTicks: amount, PenaltyBeforeTicks: penaltyBefore,
			PenaltyAfterTicks: next.Players[playerIndex(next.Players, target)].PenaltyTicks, Reason: reason},
	)
	return next, events, nil
}

func finishTurn(state State, sourceUserID string, nextActor string, now int64) (State, []Event, error) {
	if nextActor == "" {
		var err error
		nextActor, err = nextActiveUser(state, sourceUserID)
		if err != nil {
			return State{}, nil, err
		}
	}
	if state.Turn == math.MaxUint32 {
		return State{}, nil, ruleError(CodeRoundOverflow, "turn counter cannot advance")
	}
	next := state.Clone()
	next.LastSettlement.NextUserID = nextActor
	next.LastSettlement.DirectionAfter = next.Direction
	settlement := cloneSettlement(next.LastSettlement)
	next.Turn++
	next, started, err := startTurn(next, nextActor, now)
	if err != nil {
		return State{}, nil, err
	}
	settled := Event{
		Kind: EventTurnSettled, Turn: settlement.Turn, UserID: settlement.SourceUserID, SourceUserID: settlement.SourceUserID,
		TargetUserID: settlement.TargetUserID, DieOne: settlement.DieOne, DieTwo: settlement.DieTwo, Sum: settlement.Sum,
		Result: settlement.Result, DirectionBefore: settlement.DirectionBefore, Direction: settlement.DirectionAfter,
		PoolBeforeTicks: settlement.PoolBeforeTicks, PoolAfterTicks: settlement.PoolAfterTicks,
		PoolBefore: append([]PoolLayer(nil), settlement.PoolBefore...), PoolAfter: append([]PoolLayer(nil), settlement.PoolAfter...),
		EffectTicks: settlement.EffectTicks, PenaltyTicks: settlement.PenaltyTicks, NextUserID: nextActor,
		Reason: settlement.Reason, AuditRef: settlement.AuditRef,
	}
	return next, []Event{settled, started}, nil
}

func settlementFromState(state State, override ResultKind, target, reason string) TurnSettlement {
	result := state.PendingResult
	if override != ResultNone {
		result = override
	}
	source := state.SourceUserID
	if source == "" {
		source = state.CurrentUserID
	}
	beforePool, _ := poolFromTotal(state.Config, state.PendingPoolBeforeTicks)
	settlement := TurnSettlement{
		Turn: state.Turn, SourceUserID: source, DieOne: state.DieOne, DieTwo: state.DieTwo, Sum: state.Sum,
		Result: result, TargetUserID: target, PoolBeforeTicks: state.PendingPoolBeforeTicks,
		PoolAfterTicks: state.TotalPoolTicks, PoolBefore: beforePool, PoolAfter: append([]PoolLayer(nil), state.Pool...),
		DirectionBefore: state.PendingDirectionBefore, DirectionAfter: state.Direction, Reason: reason,
	}
	if settlement.DirectionBefore == 0 {
		settlement.DirectionBefore = state.Direction
	}
	switch result {
	case ResultSeven, ResultDoubleSix:
		settlement.EffectTicks = settlement.PoolAfterTicks - settlement.PoolBeforeTicks
	case ResultEight, ResultNine, ResultDoubleFour:
		settlement.EffectTicks = settlement.PoolBeforeTicks - settlement.PoolAfterTicks
		settlement.PenaltyUserID = source
		settlement.PenaltyTicks = settlement.EffectTicks
	case ResultDoubleOne:
		settlement.EffectTicks = settlement.PoolBeforeTicks - settlement.PoolAfterTicks
		settlement.PenaltyUserID = target
		settlement.PenaltyTicks = settlement.EffectTicks
	case ResultDropped:
		settlement.EffectTicks = settlement.PoolBeforeTicks - settlement.PoolAfterTicks
		settlement.PenaltyUserID = source
		settlement.PenaltyTicks = settlement.EffectTicks
	}
	return settlement
}

func validateActor(state State, actor string, phase Phase, now int64, timer bool) error {
	if err := state.Validate(); err != nil {
		return err
	}
	if state.Phase == PhaseFinished {
		return ruleError(CodeSessionFinished, "session is finished")
	}
	if state.Phase != phase {
		return ruleError(CodeWrongPhase, "action is not valid in this phase")
	}
	if actor != state.CurrentUserID || !activePlayer(state, actor) {
		return ruleError(CodeNotCurrentActor, "actor does not own current action")
	}
	if !validUnixMillis(now) {
		return ruleError(CodeInvalidAction, "deterministic action time is invalid")
	}
	if !timer && state.ActionDeadlineUnixMillis != 0 && now >= state.ActionDeadlineUnixMillis {
		return ruleError(CodeActionExpired, "action deadline has passed")
	}
	return nil
}

func validateHost(state State, actor string, now int64) error {
	if err := state.Validate(); err != nil {
		return err
	}
	if state.Phase != PhaseResultPending {
		return ruleError(CodeDropReportClosed, "host confirmation window is closed")
	}
	if actor != state.HostUserID || !activePlayer(state, actor) {
		return ruleError(CodeNotHost, "only the active frozen host may confirm or report")
	}
	if !validUnixMillis(now) || now >= state.ActionDeadlineUnixMillis {
		return ruleError(CodeActionExpired, "host confirmation window has expired")
	}
	return nil
}

func selectResult(state State) ResultKind {
	if state.DieOne == state.DieTwo {
		switch state.DieOne {
		case 1:
			if state.Config.DoubleOneEnabled {
				return ResultDoubleOne
			}
		case 4:
			if state.Config.DoubleFourEnabled {
				return ResultDoubleFour
			}
		case 6:
			if state.Config.DoubleSixEnabled {
				return ResultDoubleSix
			}
		}
		if state.Config.OrdinaryPairsReverse {
			return ResultOrdinaryPair
		}
	}
	sum := state.Sum
	if state.Config.LastDigitMatch {
		sum %= 10
	}
	switch sum {
	case 7:
		return ResultSeven
	case 8:
		return ResultEight
	case 9:
		return ResultNine
	default:
		return ResultOther
	}
}

func validateAddAmount(state State, amount dice.Ticks) error {
	remaining := mustCapacity(state.Config) - state.TotalPoolTicks
	if amount > remaining {
		return ruleError(CodePoolAmountInvalid, "pool add exceeds remaining capacity")
	}
	if remaining == 0 {
		if amount != 0 {
			return ruleError(CodePoolAmountInvalid, "full pool accepts only zero add")
		}
		return nil
	}
	if remaining < state.Config.AddStepTicks {
		if amount != remaining {
			return ruleError(CodePoolAmountInvalid, "final pool remainder must be filled")
		}
		return nil
	}
	if amount == remaining || amount > 0 && amount%state.Config.AddStepTicks == 0 {
		return nil
	}
	return ruleError(CodePoolAmountInvalid, "pool add is not aligned to configured step")
}

func minimumAddAmount(state State) (dice.Ticks, error) {
	remaining := mustCapacity(state.Config) - state.TotalPoolTicks
	if remaining == 0 {
		return 0, nil
	}
	if remaining < state.Config.AddStepTicks {
		return remaining, nil
	}
	return state.Config.AddStepTicks, nil
}

func poolChangedEvent(state State, actor string, beforeLayers, afterLayers []PoolLayer, before, after, effect dice.Ticks) Event {
	return Event{
		Kind: EventPoolChanged, Turn: state.Turn, UserID: actor, SourceUserID: state.SourceUserID,
		TargetUserID: state.TargetUserID, Result: state.PendingResult, PoolBeforeTicks: before, PoolAfterTicks: after,
		PoolBefore: append([]PoolLayer(nil), beforeLayers...), PoolAfter: append([]PoolLayer(nil), afterLayers...), EffectTicks: effect,
	}
}

func setPoolTotal(state *State, total dice.Ticks) error {
	pool, err := poolFromTotal(state.Config, total)
	if err != nil {
		return err
	}
	state.Pool, state.TotalPoolTicks = pool, total
	return nil
}

func addPenalty(state *State, userID string, amount dice.Ticks) error {
	index := playerIndex(state.Players, userID)
	if index < 0 || !state.Players[index].Active {
		return ruleError(CodeParticipantInactive, "penalty target is inactive")
	}
	value, err := state.Players[index].PenaltyTicks.Add(amount)
	if err != nil {
		return ruleError(CodePenaltyOverflow, "player penalty exceeds uint32")
	}
	state.Players[index].PenaltyTicks = value
	return nil
}

func finishState(state State, reason string) (State, Event) {
	state.Phase = PhaseFinished
	state.FinishReason = reason
	state.CurrentUserID, state.SourceUserID, state.TargetUserID = "", "", ""
	state.DieOne, state.DieTwo, state.Sum = 0, 0, 0
	state.ActionDeadlineUnixMillis = 0
	state.PendingResult = ResultNone
	state.PendingPoolBeforeTicks = 0
	state.PendingDirectionBefore = 0
	return state, Event{Kind: EventSessionFinished, Turn: state.Turn, Reason: reason}
}

func mustCapacity(config Config) dice.Ticks {
	value, _ := config.totalCapacity()
	return value
}
