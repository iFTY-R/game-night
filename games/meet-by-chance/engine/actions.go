package engine

import "math"

// Reroll charges the current target, resolves the new public hand and all automatic match batches.
func Reroll(state State, actor string, nowUnixMillis int64, seed [32]byte) (State, []Event, error) {
	if err := validateTargetAction(state, actor, nowUnixMillis, false); err != nil {
		return State{}, nil, err
	}
	if state.TargetRerollCount >= state.Config.TargetRerollLimit {
		return State{}, nil, ruleError(CodeRerollLimitReached, "target reroll limit is exhausted")
	}
	next := state.Clone()
	var penalty Event
	var err error
	next, penalty, err = addPenalty(next, actor, next.Config.RerollPenaltyTicks, "target_reroll", 0)
	if err != nil {
		return State{}, nil, err
	}
	next.TargetRerollCount++
	next.TargetStreak++
	events := []Event{{Kind: EventTargetRerolled, Round: next.Round, UserID: actor,
		TargetRerollCount: next.TargetRerollCount, TargetStreak: next.TargetStreak,
		MatchResolutionCount: next.MatchResolutionCount,
		PenaltyTicks:         next.Config.RerollPenaltyTicks, Reason: "target_reroll"}, penalty}
	roller := deterministicRoller{seed: seed}
	rolled, revealEvents, err := rollUsers(next, &roller, []string{actor}, 0, "target_reroll")
	if err != nil {
		return State{}, nil, err
	}
	events = append(events, revealEvents...)
	resolved, matchEvents, err := resolveMatches(rolled, &roller)
	if err != nil {
		return State{}, nil, err
	}
	events = append(events, matchEvents...)
	if targetStrictlyStrongest(resolved, actor) {
		settled, settleEvents, settleErr := settleAndStart(resolved, actor, "target_surpassed", nowUnixMillis, &roller)
		return settled, append(events, settleEvents...), settleErr
	}
	if resolved.TargetRerollCount >= resolved.Config.TargetRerollLimit {
		settled, settleEvents, settleErr := settleAndStart(resolved, actor, "reroll_limit", nowUnixMillis, &roller)
		return settled, append(events, settleEvents...), settleErr
	}
	selected, targetEvents, err := selectTarget(resolved, actor, nowUnixMillis, "post_reroll_target")
	if err != nil {
		return State{}, nil, err
	}
	return selected, append(events, targetEvents...), nil
}

// Stand settles the current target and immediately starts the next simultaneous round.
func Stand(state State, actor string, nowUnixMillis int64, seed [32]byte) (State, []Event, error) {
	if err := validateTargetAction(state, actor, nowUnixMillis, false); err != nil {
		return State{}, nil, err
	}
	roller := deterministicRoller{seed: seed}
	return settleAndStart(state.Clone(), actor, "stand", nowUnixMillis, &roller)
}

// HandleTimeout is exactly stand for the complete persisted target decision token.
func HandleTimeout(state State, timer ActionTimer, nowUnixMillis int64, seed [32]byte) (State, []Event, error) {
	if err := state.Validate(); err != nil {
		return State{}, nil, err
	}
	if state.Phase == PhaseFinished {
		return State{}, nil, ruleError(CodeSessionFinished, "session no longer accepts timers")
	}
	if timer.Round != state.Round || timer.TargetUserID != state.TargetUserID ||
		timer.TargetRerollCount != state.TargetRerollCount || timer.TargetStreak != state.TargetStreak ||
		timer.MatchResolutionCount != state.MatchResolutionCount || timer.DeadlineUnixMillis == 0 ||
		timer.DeadlineUnixMillis != state.ActionDeadlineUnixMillis {
		return State{}, nil, ruleError(CodeTimerMismatch, "timer does not match the current target decision")
	}
	if !validUnixMillis(nowUnixMillis) || nowUnixMillis < state.ActionDeadlineUnixMillis {
		return State{}, nil, ruleError(CodeTimerNotDue, "timer fired before its deadline")
	}
	roller := deterministicRoller{seed: seed}
	return settleAndStart(state.Clone(), state.TargetUserID, "timeout_stand", nowUnixMillis, &roller)
}

// RevokeParticipant removes a frozen seat; revoking the current target cancels only the unsettled round.
func RevokeParticipant(state State, userID string, nowUnixMillis int64, seed [32]byte) (State, []Event, error) {
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
	wasTarget := userID == state.TargetUserID
	events := []Event{{Kind: EventParticipantRevoked, Round: state.Round, UserID: userID,
		Reason: "participant_revoked", TargetRerollCount: state.TargetRerollCount,
		TargetStreak: state.TargetStreak, MatchResolutionCount: state.MatchResolutionCount,
		RoundCancelled: wasTarget, ActivePlayerCount: uint32(activeCount(next.Players))}}
	if activeCount(next.Players) < MinimumPlayers {
		finished, finishEvent := finishState(next, FinishInsufficientParticipants, "")
		return finished, append(events, finishEvent), nil
	}
	if !wasTarget {
		return next, events, nil
	}
	if state.Round == math.MaxUint32 {
		return State{}, nil, ruleError(CodeRoundOverflow, "round counter cannot advance")
	}
	events[0].NextRound = state.Round + 1
	next.Round++
	roller := deterministicRoller{seed: seed}
	started, startEvents, err := startRound(next, &roller, nowUnixMillis, false, nil)
	if err != nil {
		return State{}, nil, err
	}
	return started, append(events, startEvents...), nil
}

// Finish terminates the session without altering already-public hands or cumulative penalties.
func Finish(state State, reason, operatorUserID string) (State, []Event, error) {
	if err := state.Validate(); err != nil {
		return State{}, nil, err
	}
	if state.Phase == PhaseFinished {
		return State{}, nil, ruleError(CodeSessionFinished, "session is already finished")
	}
	if reason == "" {
		reason = FinishHostRequested
	}
	if !validFinishReason(reason) {
		return State{}, nil, ruleError(CodeInvalidAction, "finish reason is unsupported")
	}
	if reason == FinishHostRequested && operatorUserID == "" || reason != FinishHostRequested && operatorUserID != "" {
		return State{}, nil, ruleError(CodeInvalidAction, "finish operator does not match the resolution cause")
	}
	finished, event := finishState(state.Clone(), reason, operatorUserID)
	return finished, []Event{event}, nil
}

func settleAndStart(state State, target, reason string, now int64, roller *deterministicRoller) (State, []Event, error) {
	if state.Round == math.MaxUint32 {
		return State{}, nil, ruleError(CodeRoundOverflow, "round counter cannot advance")
	}
	next := state.Clone()
	settlement := RoundSettlement{
		Round: next.Round, TargetUserID: target, Reason: reason,
		TargetRerollCount: next.TargetRerollCount, TargetStreak: next.TargetStreak,
		MatchResolutionCount: next.MatchResolutionCount, Players: clonePlayers(next.Players),
	}
	next.LastSettlement = settlement
	next.RoundHistory = append(next.RoundHistory, cloneSettlement(settlement))
	if len(next.RoundHistory) > RoundHistoryLimit {
		next.RoundHistory = append([]RoundSettlement(nil), next.RoundHistory[len(next.RoundHistory)-RoundHistoryLimit:]...)
	}
	events := []Event{{Kind: EventRoundSettled, Round: next.Round, UserID: target, Reason: reason, Settlement: cloneSettlement(settlement)}}
	next.Round++
	started, startEvents, err := startRound(next, roller, now, false, nil)
	if err != nil {
		return State{}, nil, err
	}
	return started, append(events, startEvents...), nil
}

func validateTargetAction(state State, actor string, now int64, timer bool) error {
	if err := state.Validate(); err != nil {
		return err
	}
	if state.Phase == PhaseFinished {
		return ruleError(CodeSessionFinished, "session is finished")
	}
	if actor != state.TargetUserID || !activePlayer(state, actor) {
		return ruleError(CodeNotCurrentTarget, "actor is not the current target")
	}
	if !validUnixMillis(now) {
		return ruleError(CodeInvalidAction, "deterministic action time is invalid")
	}
	if !timer && state.ActionDeadlineUnixMillis != 0 && now >= state.ActionDeadlineUnixMillis {
		return ruleError(CodeActionExpired, "target action deadline has passed")
	}
	return nil
}

func finishState(state State, reason, operator string) (State, Event) {
	state.Phase = PhaseFinished
	state.FinishReason = reason
	state.TargetUserID = ""
	state.ActionDeadlineUnixMillis = 0
	return state, Event{Kind: EventSessionFinished, Round: state.Round, Reason: reason, OperatorUserID: operator}
}
