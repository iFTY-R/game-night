package engine

import (
	"cmp"
	"math"
	"slices"
)

// NewState shuffles, deals, and enters the first simultaneous selection phase.
func NewState(config Config, participants []Participant, hostUserID string, nowUnixMillis int64, seed [32]byte) (State, []Event, error) {
	canonical, err := canonicalParticipants(participants)
	if err != nil {
		return State{}, nil, err
	}
	if err := config.Validate(len(canonical)); err != nil {
		return State{}, nil, err
	}
	if !validUnixMillis(nowUnixMillis) {
		return State{}, nil, ruleError(CodeInvalidState, "deterministic time is required")
	}
	hostFound := false
	for _, participant := range canonical {
		hostFound = hostFound || participant.UserID == hostUserID
	}
	if !hostFound {
		return State{}, nil, ruleError(CodeInvalidParticipants, "host must exist in the frozen participant set")
	}
	deck, err := ShuffleDeck(seed)
	if err != nil {
		return State{}, nil, err
	}
	hands, _, err := DealHands(canonical, deck)
	if err != nil {
		return State{}, nil, err
	}
	players := make([]PlayerState, len(canonical))
	deals := make([]PlayerDeal, len(canonical))
	for index, participant := range canonical {
		initial := append([]CardID(nil), hands[participant.UserID]...)
		players[index] = PlayerState{
			UserID:        participant.UserID,
			SeatIndex:     participant.SeatIndex,
			Active:        true,
			InitialHand:   append([]CardID(nil), initial...),
			RemainingHand: append([]CardID(nil), initial...),
		}
		deals[index] = PlayerDeal{UserID: participant.UserID, InitialHand: append([]CardID(nil), initial...)}
	}
	state := State{
		SchemaVersion: CurrentSchemaVersion,
		Phase:         PhaseRoundOneSelecting,
		CurrentRound:  1,
		Config:        config,
		HostUserID:    hostUserID,
		Players:       players,
	}
	state.PhaseGeneration = 1
	state.PhaseDeadlineUnixMillis, err = deadlineForPhase(nowUnixMillis, state.Phase, config)
	if err != nil {
		return State{}, nil, err
	}
	events := []Event{
		{Kind: EventSessionStarted, Config: &config, Participants: append([]Participant(nil), canonical...), HostUserID: hostUserID},
		{Kind: EventCardsDealt, Deals: deals},
		{Kind: EventPhaseStarted, Phase: PhaseDealing, Round: 0},
		{Kind: EventPhaseStarted, Phase: state.Phase, Round: state.CurrentRound, DeadlineUnixMillis: state.PhaseDeadlineUnixMillis, PhaseGeneration: state.PhaseGeneration},
	}
	return state, events, state.Validate()
}

// Validate rejects corrupted restored state before it reaches commands, timers, or projections.
func (state State) Validate() error {
	if state.SchemaVersion != CurrentSchemaVersion || state.CurrentRound == 0 || len(state.Players) < MinimumPlayers || len(state.Players) > MaximumPlayers {
		return ruleError(CodeInvalidState, "state header is invalid")
	}
	if err := state.Config.Validate(len(state.Players)); err != nil {
		return err
	}
	if state.Phase < PhaseDealing || state.Phase > PhaseFinished {
		return ruleError(CodeInvalidState, "state phase is unknown")
	}
	hostFound := false
	users := make(map[string]struct{}, len(state.Players))
	seats := make(map[uint32]struct{}, len(state.Players))
	cardSeen := make(map[CardID]struct{}, len(state.Players)*6)
	for index, player := range state.Players {
		if player.UserID == "" {
			return ruleError(CodeInvalidState, "player identity is empty")
		}
		hostFound = hostFound || player.UserID == state.HostUserID
		if index > 0 && state.Players[index-1].SeatIndex >= player.SeatIndex {
			return ruleError(CodeInvalidState, "players are not in stable seat order")
		}
		if _, duplicate := users[player.UserID]; duplicate {
			return ruleError(CodeInvalidState, "player identity repeats")
		}
		if _, duplicate := seats[player.SeatIndex]; duplicate {
			return ruleError(CodeInvalidState, "seat index repeats")
		}
		users[player.UserID] = struct{}{}
		seats[player.SeatIndex] = struct{}{}
		if err := validatePlayerCards(player, cardSeen); err != nil {
			return err
		}
		if player.TotalPoints != player.RoundOnePoints+player.RoundTwoPoints+player.RoundThreePoints {
			return ruleError(CodeInvalidState, "player total points are stale")
		}
		if player.PendingSelection.Round != 0 {
			if state.Phase != PhaseRoundOneSelecting && state.Phase != PhaseRoundTwoSelecting {
				return ruleError(CodeInvalidState, "pending selection exists outside a selecting phase")
			}
			if player.PendingSelection.Round != state.CurrentRound {
				return ruleError(CodeInvalidState, "pending selection targets another round")
			}
		}
	}
	if !hostFound {
		return ruleError(CodeInvalidState, "host identity is absent")
	}
	if !validFinishReason(state.FinishReason) && state.FinishReason != "" {
		return ruleError(CodeInvalidState, "finish reason is unknown")
	}
	if state.Phase == PhaseFinished {
		if state.PhaseDeadlineUnixMillis != 0 {
			return ruleError(CodeInvalidState, "finished state still owns a deadline")
		}
	} else {
		if state.PhaseGeneration == 0 {
			return ruleError(CodeInvalidState, "live state phase generation is missing")
		}
		if state.Phase != PhaseDealing && phaseRequiresDeadline(state.Phase) && state.PhaseDeadlineUnixMillis <= 0 && selectionDeadlineEnabled(state.Phase, state.Config) {
			return ruleError(CodeInvalidState, "phase deadline is missing")
		}
	}
	if len(state.RoundHistory) > RoundHistoryLimit {
		return ruleError(CodeInvalidState, "round history exceeds retention")
	}
	previousRound := uint32(0)
	for _, round := range state.RoundHistory {
		if round.Round <= previousRound || round.Round > 3 {
			return ruleError(CodeInvalidState, "round history ordering is invalid")
		}
		previousRound = round.Round
	}
	return nil
}

func validatePlayerCards(player PlayerState, seen map[CardID]struct{}) error {
	if len(player.InitialHand) != 6 {
		return ruleError(CodeInvalidState, "initial hand must contain exactly six cards")
	}
	currentTotal := 0
	currentSeen := make(map[CardID]struct{}, 6)
	for _, cardID := range player.InitialHand {
		if _, ok := seen[cardID]; ok {
			return ruleError(CodeInvalidState, "initial hands contain duplicate cards")
		}
		seen[cardID] = struct{}{}
		if _, ok := ParseCardID(string(cardID)); !ok {
			return ruleError(CodeInvalidState, "state contains an unknown initial card id")
		}
	}
	for _, set := range [][]CardID{player.RemainingHand, player.RoundOneCards, player.RoundTwoCards, player.RoundThreeCards} {
		for _, cardID := range set {
			if _, ok := ParseCardID(string(cardID)); !ok {
				return ruleError(CodeInvalidState, "state contains an unknown card id")
			}
			if _, duplicate := currentSeen[cardID]; duplicate {
				return ruleError(CodeInvalidState, "current card allocation contains duplicates")
			}
			currentSeen[cardID] = struct{}{}
		}
		currentTotal += len(set)
	}
	if player.PendingSelection.Round != 0 {
		remaining := make(map[CardID]struct{}, len(player.RemainingHand))
		for _, cardID := range player.RemainingHand {
			remaining[cardID] = struct{}{}
		}
		for _, cardID := range player.PendingSelection.CardIDs {
			if _, ok := ParseCardID(string(cardID)); !ok {
				return ruleError(CodeInvalidState, "pending selection contains an unknown card id")
			}
			if _, ok := remaining[cardID]; !ok {
				return ruleError(CodeInvalidState, "pending selection must remain inside the current hand")
			}
		}
	}
	if currentTotal != 6 {
		return ruleError(CodeInvalidState, "player current card allocation is broken")
	}
	return nil
}

// CurrentTimer returns the exact persisted phase timeout payload for the current state.
func CurrentTimer(state State) *Timer {
	if state.Phase == PhaseFinished || state.Phase == PhaseDealing {
		return nil
	}
	if state.PhaseDeadlineUnixMillis == 0 {
		return nil
	}
	return &Timer{
		Phase:              state.Phase,
		Round:              state.CurrentRound,
		DeadlineUnixMillis: state.PhaseDeadlineUnixMillis,
		PhaseGeneration:    state.PhaseGeneration,
	}
}

// SubmitSelection records one simultaneous round-one or round-two choice.
func SubmitSelection(state State, actor string, round uint32, cardIDs []CardID, auto bool, nowUnixMillis int64) (State, []Event, error) {
	if err := validateSelectableAction(state, actor, round, cardIDs, nowUnixMillis); err != nil {
		return State{}, nil, err
	}
	next := state.Clone()
	index := playerIndex(next.Players, actor)
	next.Players[index].PendingSelection = PendingSelection{Round: round, CardIDs: canonicalSelection(cardIDs), AutoSubmitted: auto}
	eventKind := EventSelectionSubmitted
	if auto {
		eventKind = EventSelectionAutoSubmitted
	}
	events := []Event{{Kind: eventKind, UserID: actor, Round: round, AutoSubmitted: auto}}
	if allActivePlayersSubmitted(next) {
		settled, settleEvents, err := settleCurrentSelection(next, nowUnixMillis)
		if err != nil {
			return State{}, nil, err
		}
		return settled, append(events, settleEvents...), nil
	}
	return next, events, next.Validate()
}

// HandleTimeout auto-submits or advances one exact phase generation.
func HandleTimeout(state State, timer Timer, nowUnixMillis int64) (State, []Event, error) {
	if err := state.Validate(); err != nil {
		return State{}, nil, err
	}
	if state.Phase == PhaseFinished {
		return State{}, nil, ruleError(CodeSessionFinished, "session no longer accepts timers")
	}
	if timer.Phase != state.Phase || timer.Round != state.CurrentRound || timer.PhaseGeneration != state.PhaseGeneration || timer.DeadlineUnixMillis != state.PhaseDeadlineUnixMillis {
		return State{}, nil, ruleError(CodeTimerMismatch, "timer does not match the current phase generation")
	}
	if !validUnixMillis(nowUnixMillis) || timer.DeadlineUnixMillis == 0 || nowUnixMillis < timer.DeadlineUnixMillis {
		return State{}, nil, ruleError(CodeTimerNotDue, "timer fired before its deadline")
	}
	switch state.Phase {
	case PhaseRoundOneSelecting, PhaseRoundTwoSelecting:
		next := state.Clone()
		events := make([]Event, 0)
		for index, player := range next.Players {
			if !player.Active || player.PendingSelection.Round == state.CurrentRound {
				continue
			}
			cardIDs, err := autoSelect(next, player, state.CurrentRound)
			if err != nil {
				return State{}, nil, err
			}
			next.Players[index].PendingSelection = PendingSelection{Round: state.CurrentRound, CardIDs: cardIDs, AutoSubmitted: true}
			events = append(events, Event{Kind: EventSelectionAutoSubmitted, UserID: player.UserID, Round: state.CurrentRound, AutoSubmitted: true})
		}
		settled, settleEvents, err := settleCurrentSelection(next, nowUnixMillis)
		if err != nil {
			return State{}, nil, err
		}
		return settled, append(events, settleEvents...), nil
	case PhaseRoundOneResult:
		return advancePhaseAfterRoundOneResult(state, nowUnixMillis)
	case PhaseRoundTwoResult:
		return resolveRoundThree(state, nowUnixMillis)
	case PhaseRoundThreeResult:
		return enterFinalResult(state, nowUnixMillis)
	case PhaseFinalResult:
		return finishNormally(state)
	default:
		return State{}, nil, ruleError(CodeWrongPhase, "phase does not own a runtime timer")
	}
}

// RevokeParticipant removes one player from the active standings and unresolved rounds.
func RevokeParticipant(state State, userID string, nowUnixMillis int64) (State, []Event, error) {
	if err := state.Validate(); err != nil {
		return State{}, nil, err
	}
	if state.Phase == PhaseFinished {
		return State{}, nil, ruleError(CodeSessionFinished, "session is already finished")
	}
	index := playerIndex(state.Players, userID)
	if index < 0 || !state.Players[index].Active {
		return State{}, nil, ruleError(CodeParticipantInactive, "participant is absent or already inactive")
	}
	next := state.Clone()
	removedPending := next.Players[index].PendingSelection.Round == next.CurrentRound
	next.Players[index].Active = false
	next.Players[index].PendingSelection = PendingSelection{}
	recomputeFinalDecorations(&next)
	activePlayers := uint32(activeCount(next.Players))
	events := []Event{{
		Kind:                    EventParticipantRevoked,
		UserID:                  userID,
		Round:                   next.CurrentRound,
		Phase:                   next.Phase,
		RemovedPendingSelection: removedPending,
		ActivePlayerCount:       activePlayers,
	}}
	if activePlayers < MinimumPlayers {
		finished, finishEvents := finishWithReason(next, FinishInsufficientParticipants, "")
		return finished, append(events, finishEvents...), nil
	}
	if (next.Phase == PhaseRoundOneSelecting || next.Phase == PhaseRoundTwoSelecting) && allActivePlayersSubmitted(next) {
		settled, settleEvents, err := settleCurrentSelection(next, nowUnixMillis)
		if err != nil {
			return State{}, nil, err
		}
		return settled, append(events, settleEvents...), nil
	}
	if next.Phase == PhaseFinalResult || next.Phase == PhaseRoundThreeResult {
		recomputeFinalDecorations(&next)
	}
	return next, events, next.Validate()
}

// Finish applies the host-governed session.finish system command.
func Finish(state State, reason, operatorUserID string) (State, []Event, error) {
	if err := state.Validate(); err != nil {
		return State{}, nil, err
	}
	if state.Phase == PhaseFinished {
		return State{}, nil, ruleError(CodeSessionFinished, "session is already finished")
	}
	if state.Phase == PhaseFinalResult {
		return State{}, nil, ruleError(CodeWrongPhase, "host finish is closed during final result")
	}
	if reason == "" {
		reason = FinishHostRequested
	}
	if reason != FinishHostRequested || operatorUserID == "" {
		return State{}, nil, ruleError(CodeInvalidAction, "session.finish must carry the verified host operator")
	}
	finished, events := finishWithReason(state.Clone(), reason, operatorUserID)
	return finished, events, nil
}

func finishNormally(state State) (State, []Event, error) {
	next, events := finishWithReason(state.Clone(), FinishNormalCompleted, "")
	return next, events, nil
}

func finishWithReason(state State, reason, operatorUserID string) (State, []Event) {
	finalSummaryWasPublic := state.Phase == PhaseFinalResult || state.Phase == PhaseFinished
	state.Phase = PhaseFinished
	state.PhaseDeadlineUnixMillis = 0
	state.PhaseGeneration++
	state.FinishReason = reason
	if reason != FinishNormalCompleted {
		if finalSummaryWasPublic && len(state.FinalSummary.Standings) != 0 {
			state.FinalSummary.WinnerUserIDs = nil
			for index := range state.FinalSummary.Standings {
				state.FinalSummary.Standings[index].Winner = false
			}
			for index := range state.Players {
				state.Players[index].FinalWinner = false
			}
		} else {
			state.FinalSummary = FinalSummary{}
			clearFinalDecorations(&state)
		}
	}
	return state, []Event{{Kind: EventSessionFinished, FinishReason: reason, OperatorUserID: operatorUserID, Round: state.CurrentRound}}
}

func advancePhaseAfterRoundOneResult(state State, nowUnixMillis int64) (State, []Event, error) {
	next := state.Clone()
	next.Phase = PhaseRoundTwoSelecting
	next.CurrentRound = 2
	next.PhaseGeneration++
	deadline, err := deadlineForPhase(nowUnixMillis, next.Phase, next.Config)
	if err != nil {
		return State{}, nil, err
	}
	next.PhaseDeadlineUnixMillis = deadline
	return next, []Event{{Kind: EventPhaseStarted, Phase: next.Phase, Round: next.CurrentRound, DeadlineUnixMillis: deadline, PhaseGeneration: next.PhaseGeneration}}, next.Validate()
}

func resolveRoundThree(state State, nowUnixMillis int64) (State, []Event, error) {
	if state.Phase != PhaseRoundTwoResult {
		return State{}, nil, ruleError(CodeWrongPhase, "round three can only start after round two result")
	}
	next := state.Clone()
	summary := RoundSummary{Round: 3, Reveals: make([]PlayerReveal, 0, len(next.Players))}
	events := make([]Event, 0)
	best := thirdComparison{}
	hasBest := false
	for index, player := range next.Players {
		if !player.Active {
			continue
		}
		cards := cardsFromIDs(player.RemainingHand)
		resolved, err := resolveThirdHand(cards)
		if err != nil {
			return State{}, nil, err
		}
		next.Players[index].RoundThreeCards = append([]CardID(nil), player.RemainingHand...)
		next.Players[index].RemainingHand = nil
		next.Players[index].RoundThreeWildcardResolution = cloneWildcardResolution(resolved.WildcardResolution)
		reveal := PlayerReveal{
			UserID:        player.UserID,
			SeatIndex:     player.SeatIndex,
			Active:        true,
			CardIDs:       append([]CardID(nil), next.Players[index].RoundThreeCards...),
			AwardedPoints: 0,
			RoundThreeEvaluation: RoundThreeEvaluation{
				HandClass:     resolved.HandClass,
				CompareValues: append([]uint8(nil), resolved.CompareValues...),
				ResolvedCards: append([]CardID(nil), resolved.ResolvedCards...),
			},
			WildcardResolution: cloneWildcardResolution(resolved.WildcardResolution),
		}
		summary.Reveals = append(summary.Reveals, reveal)
		if len(resolved.WildcardResolution.Substitutions) != 0 {
			events = append(events, Event{Kind: EventWildcardResolved, UserID: player.UserID, Round: 3, WildcardResolution: cloneWildcardResolution(resolved.WildcardResolution)})
		}
		if !hasBest || compareThirdResult(resolved.thirdComparison, best) > 0 {
			best = resolved.thirdComparison
			hasBest = true
			summary.WinnerUserIDs = []string{player.UserID}
		} else if compareThirdResult(resolved.thirdComparison, best) == 0 {
			summary.WinnerUserIDs = append(summary.WinnerUserIDs, player.UserID)
		}
	}
	for index := range summary.Reveals {
		if slices.Contains(summary.WinnerUserIDs, summary.Reveals[index].UserID) {
			summary.Reveals[index].AwardedPoints = 1
			playerIndex := playerIndex(next.Players, summary.Reveals[index].UserID)
			next.Players[playerIndex].RoundThreePoints++
			next.Players[playerIndex].TotalPoints++
			next.Players[playerIndex].WonRoundThree = true
		}
	}
	reapplyFinalTotals(&next)
	summary = canonicalRoundSummary(summary)
	next.RoundHistory = append(next.RoundHistory, summary)
	next.FinalSummary = FinalSummary{}
	clearFinalDecorations(&next)
	next.Phase = PhaseRoundThreeResult
	next.CurrentRound = 3
	next.PhaseGeneration++
	deadline, err := deadlineForPhase(nowUnixMillis, next.Phase, next.Config)
	if err != nil {
		return State{}, nil, err
	}
	next.PhaseDeadlineUnixMillis = deadline
	events = append(events,
		Event{Kind: EventRoundRevealed, Summary: summary, Round: 3},
		Event{Kind: EventRoundSettled, Summary: summary, Round: 3},
		Event{Kind: EventPhaseStarted, Phase: next.Phase, Round: next.CurrentRound, DeadlineUnixMillis: deadline, PhaseGeneration: next.PhaseGeneration},
	)
	return next, events, next.Validate()
}

func enterFinalResult(state State, nowUnixMillis int64) (State, []Event, error) {
	if state.Phase != PhaseRoundThreeResult {
		return State{}, nil, ruleError(CodeWrongPhase, "final result can only start after round three result")
	}
	next := state.Clone()
	next.Phase = PhaseFinalResult
	next.PhaseGeneration++
	next.FinalSummary = computeFinalSummary(next.Players)
	deadline, err := deadlineForPhase(nowUnixMillis, next.Phase, next.Config)
	if err != nil {
		return State{}, nil, err
	}
	next.PhaseDeadlineUnixMillis = deadline
	recomputeFinalDecorations(&next)
	return next, []Event{{Kind: EventPhaseStarted, Phase: next.Phase, Round: next.CurrentRound, DeadlineUnixMillis: deadline, PhaseGeneration: next.PhaseGeneration}}, next.Validate()
}

func settleCurrentSelection(state State, nowUnixMillis int64) (State, []Event, error) {
	switch state.Phase {
	case PhaseRoundOneSelecting:
		return settleRoundOne(state, nowUnixMillis)
	case PhaseRoundTwoSelecting:
		return settleRoundTwo(state, nowUnixMillis)
	default:
		return State{}, nil, ruleError(CodeWrongPhase, "phase is not waiting for selections")
	}
}

func settleRoundOne(state State, nowUnixMillis int64) (State, []Event, error) {
	next := state.Clone()
	summary := RoundSummary{Round: 1, Reveals: make([]PlayerReveal, 0, len(next.Players))}
	best := roundOneComparison{}
	hasBest := false
	for index, player := range next.Players {
		if !player.Active || player.PendingSelection.Round != 1 {
			next.Players[index].PendingSelection = PendingSelection{}
			continue
		}
		selected := append([]CardID(nil), player.PendingSelection.CardIDs...)
		card := cardByID[selected[0]]
		next.Players[index].RoundOneCards = selected
		next.Players[index].RemainingHand = removeCards(next.Players[index].RemainingHand, selected)
		next.Players[index].PendingSelection = PendingSelection{}
		comparison := roundOneComparison{RankStrength: rankStrength(card), SuitStrength: suitStrength(card)}
		reveal := PlayerReveal{
			UserID:        player.UserID,
			SeatIndex:     player.SeatIndex,
			Active:        true,
			CardIDs:       selected,
			AutoSubmitted: player.PendingSelection.AutoSubmitted,
			RoundOneEvaluation: RoundOneEvaluation{
				CardID:       selected[0],
				RankStrength: comparison.RankStrength,
				SuitStrength: comparison.SuitStrength,
			},
		}
		summary.Reveals = append(summary.Reveals, reveal)
		if !hasBest || compareRoundOne(comparison, best) > 0 {
			best = comparison
			hasBest = true
			summary.WinnerUserIDs = []string{player.UserID}
		} else if compareRoundOne(comparison, best) == 0 {
			summary.WinnerUserIDs = append(summary.WinnerUserIDs, player.UserID)
		}
	}
	for index := range summary.Reveals {
		if slices.Contains(summary.WinnerUserIDs, summary.Reveals[index].UserID) {
			summary.Reveals[index].AwardedPoints = 1
			playerIndex := playerIndex(next.Players, summary.Reveals[index].UserID)
			next.Players[playerIndex].RoundOnePoints++
			next.Players[playerIndex].TotalPoints++
			next.Players[playerIndex].WonRoundOne = true
		}
	}
	reapplyFinalTotals(&next)
	summary = canonicalRoundSummary(summary)
	next.RoundHistory = append(next.RoundHistory, summary)
	next.Phase = PhaseRoundOneResult
	next.PhaseGeneration++
	deadline, err := deadlineForPhase(nowUnixMillis, next.Phase, next.Config)
	if err != nil {
		return State{}, nil, err
	}
	next.PhaseDeadlineUnixMillis = deadline
	return next, []Event{
		{Kind: EventRoundRevealed, Summary: summary, Round: 1},
		{Kind: EventRoundSettled, Summary: summary, Round: 1},
		{Kind: EventPhaseStarted, Phase: next.Phase, Round: next.CurrentRound, DeadlineUnixMillis: deadline, PhaseGeneration: next.PhaseGeneration},
	}, next.Validate()
}

func settleRoundTwo(state State, nowUnixMillis int64) (State, []Event, error) {
	next := state.Clone()
	summary := RoundSummary{Round: 2, Reveals: make([]PlayerReveal, 0, len(next.Players))}
	best := roundTwoComparison{}
	hasBest := false
	allBusted := true
	for index, player := range next.Players {
		if !player.Active || player.PendingSelection.Round != 2 {
			next.Players[index].PendingSelection = PendingSelection{}
			continue
		}
		selected := append([]CardID(nil), player.PendingSelection.CardIDs...)
		cards := cardsFromIDs(selected)
		evaluation := evaluateRoundTwo(cards)
		next.Players[index].RoundTwoCards = selected
		next.Players[index].RemainingHand = removeCards(next.Players[index].RemainingHand, selected)
		next.Players[index].PendingSelection = PendingSelection{}
		reveal := PlayerReveal{
			UserID:        player.UserID,
			SeatIndex:     player.SeatIndex,
			Active:        true,
			CardIDs:       selected,
			AutoSubmitted: player.PendingSelection.AutoSubmitted,
			RoundTwoEvaluation: RoundTwoEvaluation{
				TotalHalfPoints:   evaluation.TotalHalfPoints,
				Busted:            evaluation.Busted,
				RankStrengthsDesc: evaluation.RankStrengthsDesc,
			},
		}
		summary.Reveals = append(summary.Reveals, reveal)
		if evaluation.Busted {
			continue
		}
		allBusted = false
		if !hasBest || compareRoundTwo(evaluation.roundTwoComparison, best) > 0 {
			best = evaluation.roundTwoComparison
			hasBest = true
			summary.WinnerUserIDs = []string{player.UserID}
		} else if compareRoundTwo(evaluation.roundTwoComparison, best) == 0 {
			summary.WinnerUserIDs = append(summary.WinnerUserIDs, player.UserID)
		}
	}
	summary.AllBusted = allBusted
	for index := range summary.Reveals {
		if slices.Contains(summary.WinnerUserIDs, summary.Reveals[index].UserID) {
			summary.Reveals[index].AwardedPoints = 1
			playerIndex := playerIndex(next.Players, summary.Reveals[index].UserID)
			next.Players[playerIndex].RoundTwoPoints++
			next.Players[playerIndex].TotalPoints++
			next.Players[playerIndex].WonRoundTwo = true
		}
	}
	reapplyFinalTotals(&next)
	summary = canonicalRoundSummary(summary)
	next.RoundHistory = append(next.RoundHistory, summary)
	next.Phase = PhaseRoundTwoResult
	next.PhaseGeneration++
	deadline, err := deadlineForPhase(nowUnixMillis, next.Phase, next.Config)
	if err != nil {
		return State{}, nil, err
	}
	next.PhaseDeadlineUnixMillis = deadline
	return next, []Event{
		{Kind: EventRoundRevealed, Summary: summary, Round: 2},
		{Kind: EventRoundSettled, Summary: summary, Round: 2},
		{Kind: EventPhaseStarted, Phase: next.Phase, Round: next.CurrentRound, DeadlineUnixMillis: deadline, PhaseGeneration: next.PhaseGeneration},
	}, next.Validate()
}

func canonicalParticipants(participants []Participant) ([]Participant, error) {
	if len(participants) < MinimumPlayers || len(participants) > MaximumPlayers {
		return nil, ruleError(CodeInvalidParticipants, "participant count is outside the supported range")
	}
	canonical := append([]Participant(nil), participants...)
	sortParticipants(canonical)
	users := make(map[string]struct{}, len(canonical))
	seats := make(map[uint32]struct{}, len(canonical))
	for _, participant := range canonical {
		if participant.UserID == "" {
			return nil, ruleError(CodeInvalidParticipants, "participant user id is empty")
		}
		if _, duplicate := users[participant.UserID]; duplicate {
			return nil, ruleError(CodeInvalidParticipants, "participant user id repeats")
		}
		if _, duplicate := seats[participant.SeatIndex]; duplicate {
			return nil, ruleError(CodeInvalidParticipants, "participant seat repeats")
		}
		users[participant.UserID] = struct{}{}
		seats[participant.SeatIndex] = struct{}{}
	}
	return canonical, nil
}

func validateSelectableAction(state State, actor string, round uint32, cardIDs []CardID, nowUnixMillis int64) error {
	if err := state.Validate(); err != nil {
		return err
	}
	if state.Phase == PhaseFinished {
		return ruleError(CodeSessionFinished, "session is already finished")
	}
	if state.Phase != PhaseRoundOneSelecting && state.Phase != PhaseRoundTwoSelecting {
		return ruleError(CodeWrongPhase, "phase does not accept round.submit_selection")
	}
	if !validUnixMillis(nowUnixMillis) {
		return ruleError(CodeInvalidAction, "deterministic action time is invalid")
	}
	if state.PhaseDeadlineUnixMillis != 0 && nowUnixMillis >= state.PhaseDeadlineUnixMillis {
		return ruleError(CodeActionExpired, "selection deadline has passed")
	}
	if round != state.CurrentRound {
		return ruleError(CodeRoundMismatch, "selection targets another round")
	}
	index := playerIndex(state.Players, actor)
	if index < 0 || !state.Players[index].Active {
		return ruleError(CodeParticipantInactive, "actor is not an active participant")
	}
	if state.Players[index].PendingSelection.Round == round {
		return ruleError(CodeSelectionExists, "selection is already recorded")
	}
	expectedCount := 1
	if round == 2 {
		expectedCount = 2
	}
	if len(cardIDs) != expectedCount {
		return ruleError(CodeCardCountInvalid, "selection card count does not match the current round")
	}
	seen := make(map[CardID]struct{}, len(cardIDs))
	remaining := make(map[CardID]struct{}, len(state.Players[index].RemainingHand))
	for _, cardID := range state.Players[index].RemainingHand {
		remaining[cardID] = struct{}{}
	}
	for _, cardID := range cardIDs {
		if _, ok := ParseCardID(string(cardID)); !ok {
			return ruleError(CodeCardInvalid, "selection contains an unknown card id")
		}
		if _, duplicate := seen[cardID]; duplicate {
			return ruleError(CodeCardInvalid, "selection contains duplicate card ids")
		}
		if _, ok := remaining[cardID]; !ok {
			return ruleError(CodeCardInvalid, "selection contains a card outside the actor hand")
		}
		seen[cardID] = struct{}{}
	}
	return nil
}

func allActivePlayersSubmitted(state State) bool {
	for _, player := range state.Players {
		if player.Active && player.PendingSelection.Round != state.CurrentRound {
			return false
		}
	}
	return true
}

func canonicalSelection(cardIDs []CardID) []CardID {
	canonical := append([]CardID(nil), cardIDs...)
	sortCardIDsByOrdinal(canonical)
	return canonical
}

func cardsFromIDs(cardIDs []CardID) []Card {
	result := make([]Card, len(cardIDs))
	for index, cardID := range cardIDs {
		result[index] = cardByID[cardID]
	}
	return result
}

func removeCards(values []CardID, selected []CardID) []CardID {
	remaining := append([]CardID(nil), values...)
	for _, target := range selected {
		for index, cardID := range remaining {
			if cardID == target {
				remaining = append(remaining[:index], remaining[index+1:]...)
				break
			}
		}
	}
	return remaining
}

type roundOneComparison struct {
	RankStrength uint8
	SuitStrength uint8
}

func compareRoundOne(left, right roundOneComparison) int {
	if diff := cmp.Compare(left.RankStrength, right.RankStrength); diff != 0 {
		return diff
	}
	return cmp.Compare(left.SuitStrength, right.SuitStrength)
}

type roundTwoComparison struct {
	Busted            bool
	TotalHalfPoints   uint8
	RankStrengthsDesc [2]uint8
}

type roundTwoEvaluation struct {
	roundTwoComparison
}

func evaluateRoundTwo(cards []Card) roundTwoEvaluation {
	ranks := [2]uint8{rankStrength(cards[0]), rankStrength(cards[1])}
	if ranks[0] < ranks[1] {
		ranks[0], ranks[1] = ranks[1], ranks[0]
	}
	total := halfPoints(cards[0]) + halfPoints(cards[1])
	return roundTwoEvaluation{
		roundTwoComparison: roundTwoComparison{
			Busted:            total > 21,
			TotalHalfPoints:   total,
			RankStrengthsDesc: ranks,
		},
	}
}

func compareRoundTwo(left, right roundTwoComparison) int {
	if left.Busted != right.Busted {
		if left.Busted {
			return -1
		}
		return 1
	}
	if diff := cmp.Compare(left.TotalHalfPoints, right.TotalHalfPoints); diff != 0 {
		return diff
	}
	if diff := cmp.Compare(left.RankStrengthsDesc[0], right.RankStrengthsDesc[0]); diff != 0 {
		return diff
	}
	return cmp.Compare(left.RankStrengthsDesc[1], right.RankStrengthsDesc[1])
}

func autoSelect(state State, player PlayerState, round uint32) ([]CardID, error) {
	if round == 1 {
		cards := cardsFromIDs(player.RemainingHand)
		bestIndex := 0
		best := roundOneComparison{RankStrength: rankStrength(cards[0]), SuitStrength: suitAscending(cards[0])}
		for index := 1; index < len(cards); index++ {
			current := roundOneComparison{RankStrength: rankStrength(cards[index]), SuitStrength: suitAscending(cards[index])}
			if diff := compareAutoRoundOne(current, best); diff < 0 {
				best = current
				bestIndex = index
			}
		}
		return []CardID{cards[bestIndex].ID}, nil
	}
	cards := cardsFromIDs(player.RemainingHand)
	type combo struct {
		cardIDs []CardID
		key     roundTwoAutoKey
	}
	best := combo{}
	found := false
	for left := 0; left < len(cards); left++ {
		for right := left + 1; right < len(cards); right++ {
			selected := []Card{cards[left], cards[right]}
			key := buildRoundTwoAutoKey(selected)
			current := combo{cardIDs: canonicalSelection([]CardID{cards[left].ID, cards[right].ID}), key: key}
			if !found || compareRoundTwoAuto(current.key, best.key) < 0 {
				best = current
				found = true
			}
		}
	}
	if !found {
		return nil, ruleError(CodeCardInvalid, "round two auto-selection could not find a legal combination")
	}
	return best.cardIDs, nil
}

func compareAutoRoundOne(left, right roundOneComparison) int {
	if diff := cmp.Compare(left.RankStrength, right.RankStrength); diff != 0 {
		return diff
	}
	return cmp.Compare(left.SuitStrength, right.SuitStrength)
}

type roundTwoAutoKey struct {
	Busted          bool
	TotalHalfPoints uint8
	RankAsc         [2]uint8
	Ordinals        [2]uint8
}

func buildRoundTwoAutoKey(cards []Card) roundTwoAutoKey {
	evaluation := evaluateRoundTwo(cards)
	rankAsc := [2]uint8{rankStrength(cards[0]), rankStrength(cards[1])}
	if rankAsc[0] > rankAsc[1] {
		rankAsc[0], rankAsc[1] = rankAsc[1], rankAsc[0]
	}
	ordinals := [2]uint8{cards[0].Ordinal, cards[1].Ordinal}
	if ordinals[0] > ordinals[1] {
		ordinals[0], ordinals[1] = ordinals[1], ordinals[0]
	}
	return roundTwoAutoKey{
		Busted:          evaluation.Busted,
		TotalHalfPoints: evaluation.TotalHalfPoints,
		RankAsc:         rankAsc,
		Ordinals:        ordinals,
	}
}

func compareRoundTwoAuto(left, right roundTwoAutoKey) int {
	if left.Busted != right.Busted {
		if left.Busted {
			return 1
		}
		return -1
	}
	if diff := cmp.Compare(left.TotalHalfPoints, right.TotalHalfPoints); diff != 0 {
		return diff
	}
	if diff := cmp.Compare(left.RankAsc[0], right.RankAsc[0]); diff != 0 {
		return diff
	}
	if diff := cmp.Compare(left.RankAsc[1], right.RankAsc[1]); diff != 0 {
		return diff
	}
	if diff := cmp.Compare(left.Ordinals[0], right.Ordinals[0]); diff != 0 {
		return diff
	}
	return cmp.Compare(left.Ordinals[1], right.Ordinals[1])
}

type thirdComparison struct {
	HandClass     HandClass
	CompareValues []uint8
}

type thirdResolution struct {
	thirdComparison
	ResolvedCards      []CardID
	WildcardResolution WildcardResolution
}

func resolveThirdHand(cards []Card) (thirdResolution, error) {
	jokers := make([]Card, 0, 2)
	real := make([]Card, 0, 3)
	used := make(map[uint8]struct{}, 3)
	for _, card := range cards {
		if card.Kind == cardNormal {
			used[card.Ordinal] = struct{}{}
			real = append(real, card)
			continue
		}
		jokers = append(jokers, card)
	}
	if len(jokers) == 0 {
		result := classifyThird(real)
		resolved := toResolvedCardIDs(real)
		return thirdResolution{
			thirdComparison: thirdComparison{HandClass: result.HandClass, CompareValues: result.CompareValues},
			ResolvedCards:   resolved,
		}, nil
	}
	if len(jokers) > 2 {
		return thirdResolution{}, ruleError(CodeInvalidState, "third round cannot contain more than two jokers")
	}
	jokerOrder := make([]Card, 0, len(jokers))
	for _, wanted := range []CardID{BigJokerID, SmallJokerID} {
		for _, joker := range jokers {
			if joker.ID == wanted {
				jokerOrder = append(jokerOrder, joker)
			}
		}
	}
	best := thirdResolution{}
	hasBest := false
	for first := 0; first < len(normalDeck); first++ {
		if _, clash := used[normalDeck[first].Ordinal]; clash {
			continue
		}
		assignments := []Card{normalDeck[first]}
		if len(jokerOrder) == 1 {
			candidate := append(append([]Card(nil), real...), assignments...)
			if improved, resolved := maybeBestThird(best, hasBest, jokerOrder, candidate, assignments); improved {
				best = resolved
				hasBest = true
			}
			continue
		}
		for second := 0; second < len(normalDeck); second++ {
			if second == first {
				continue
			}
			if _, clash := used[normalDeck[second].Ordinal]; clash {
				continue
			}
			assignments = []Card{normalDeck[first], normalDeck[second]}
			candidate := append(append([]Card(nil), real...), assignments...)
			if improved, resolved := maybeBestThird(best, hasBest, jokerOrder, candidate, assignments); improved {
				best = resolved
				hasBest = true
			}
		}
	}
	if !hasBest {
		return thirdResolution{}, ruleError(CodeInvalidState, "third round wildcard resolution found no candidate")
	}
	return best, nil
}

func maybeBestThird(current thirdResolution, hasCurrent bool, jokerOrder []Card, candidate []Card, assignments []Card) (bool, thirdResolution) {
	classified := classifyThird(candidate)
	sortCardsByOrdinal(candidate)
	resolvedCards := toResolvedCardIDs(candidate)
	resolution := WildcardResolution{ResolvedCards: append([]CardID(nil), resolvedCards...)}
	substitutionKey := make([]uint8, len(assignments))
	for index, assigned := range assignments {
		substitutionKey[index] = assigned.Ordinal
		resolution.Substitutions = append(resolution.Substitutions, WildcardSubstitution{
			WildcardCardID:    jokerOrder[index].ID,
			SubstitutedCardID: assigned.ID,
			CardOrdinal:       assigned.Ordinal,
		})
	}
	next := thirdResolution{
		thirdComparison:    thirdComparison{HandClass: classified.HandClass, CompareValues: append([]uint8(nil), classified.CompareValues...)},
		ResolvedCards:      resolvedCards,
		WildcardResolution: resolution,
	}
	if !hasCurrent {
		return true, next
	}
	if diff := compareThirdResult(next.thirdComparison, current.thirdComparison); diff != 0 {
		return diff > 0, next
	}
	return compareOrdinalKey(substitutionKey, substitutionOrdinals(current.WildcardResolution)) < 0, next
}

func substitutionOrdinals(value WildcardResolution) []uint8 {
	result := make([]uint8, len(value.Substitutions))
	for index, substitution := range value.Substitutions {
		result[index] = substitution.CardOrdinal
	}
	return result
}

func compareOrdinalKey(left, right []uint8) int {
	for index := 0; index < len(left) && index < len(right); index++ {
		if diff := cmp.Compare(left[index], right[index]); diff != 0 {
			return diff
		}
	}
	return cmp.Compare(len(left), len(right))
}

type classifiedThird struct {
	HandClass     HandClass
	CompareValues []uint8
}

func classifyThird(cards []Card) classifiedThird {
	sortCardsByOrdinal(cards)
	ranks := []uint8{cards[0].Rank, cards[1].Rank, cards[2].Rank}
	sortedDesc := append([]uint8(nil), ranks...)
	slices.SortFunc(sortedDesc, func(left, right uint8) int { return cmp.Compare(right, left) })
	flush := cards[0].Suit != SuitNone && cards[0].Suit == cards[1].Suit && cards[1].Suit == cards[2].Suit
	if cards[0].Rank == cards[1].Rank && cards[1].Rank == cards[2].Rank {
		return classifiedThird{HandClass: HandTrips, CompareValues: []uint8{cards[0].Rank}}
	}
	if straight, high := straightHigh(ranks); straight {
		if flush {
			return classifiedThird{HandClass: HandStraightFlush, CompareValues: []uint8{high}}
		}
		return classifiedThird{HandClass: HandStraight, CompareValues: []uint8{high}}
	}
	if flush {
		return classifiedThird{HandClass: HandFlush, CompareValues: sortedDesc}
	}
	if cards[0].Rank == cards[1].Rank || cards[0].Rank == cards[2].Rank || cards[1].Rank == cards[2].Rank {
		pairRank := cards[0].Rank
		kicker := cards[2].Rank
		switch {
		case cards[0].Rank == cards[1].Rank:
			pairRank, kicker = cards[0].Rank, cards[2].Rank
		case cards[0].Rank == cards[2].Rank:
			pairRank, kicker = cards[0].Rank, cards[1].Rank
		default:
			pairRank, kicker = cards[1].Rank, cards[0].Rank
		}
		return classifiedThird{HandClass: HandPair, CompareValues: []uint8{pairRank, kicker}}
	}
	return classifiedThird{HandClass: HandSingle, CompareValues: sortedDesc}
}

func straightHigh(ranks []uint8) (bool, uint8) {
	sorted := append([]uint8(nil), ranks...)
	slices.Sort(sorted)
	if sorted[0] == 2 && sorted[1] == 3 && sorted[2] == 14 {
		return true, 3
	}
	if sorted[0]+1 == sorted[1] && sorted[1]+1 == sorted[2] {
		return true, sorted[2]
	}
	return false, 0
}

func compareThirdResult(left, right thirdComparison) int {
	if diff := cmp.Compare(left.HandClass, right.HandClass); diff != 0 {
		return diff
	}
	for index := 0; index < len(left.CompareValues) && index < len(right.CompareValues); index++ {
		if diff := cmp.Compare(left.CompareValues[index], right.CompareValues[index]); diff != 0 {
			return diff
		}
	}
	return cmp.Compare(len(left.CompareValues), len(right.CompareValues))
}

func toResolvedCardIDs(cards []Card) []CardID {
	clones := append([]Card(nil), cards...)
	sortCardsByOrdinal(clones)
	result := make([]CardID, len(clones))
	for index, card := range clones {
		result[index] = card.ID
	}
	return result
}

func canonicalRoundSummary(summary RoundSummary) RoundSummary {
	summary.WinnerUserIDs = append([]string(nil), summary.WinnerUserIDs...)
	slices.Sort(summary.WinnerUserIDs)
	slices.SortFunc(summary.Reveals, func(left, right PlayerReveal) int {
		return cmp.Compare(left.SeatIndex, right.SeatIndex)
	})
	return summary
}

func computeFinalSummary(players []PlayerState) FinalSummary {
	standings := make([]FinalStanding, 0, len(players))
	for _, player := range players {
		if !player.Active {
			continue
		}
		standings = append(standings, FinalStanding{
			UserID:        player.UserID,
			SeatIndex:     player.SeatIndex,
			Active:        player.Active,
			TotalPoints:   player.TotalPoints,
			WonRoundOne:   player.WonRoundOne,
			WonRoundTwo:   player.WonRoundTwo,
			WonRoundThree: player.WonRoundThree,
		})
	}
	slices.SortFunc(standings, func(left, right FinalStanding) int {
		if diff := cmp.Compare(right.TotalPoints, left.TotalPoints); diff != 0 {
			return diff
		}
		if diff := cmp.Compare(boolToUint8(right.WonRoundThree), boolToUint8(left.WonRoundThree)); diff != 0 {
			return diff
		}
		if diff := cmp.Compare(boolToUint8(right.WonRoundTwo), boolToUint8(left.WonRoundTwo)); diff != 0 {
			return diff
		}
		if diff := cmp.Compare(boolToUint8(right.WonRoundOne), boolToUint8(left.WonRoundOne)); diff != 0 {
			return diff
		}
		return cmp.Compare(left.SeatIndex, right.SeatIndex)
	})
	if len(standings) == 0 {
		return FinalSummary{}
	}
	winningKey := standings[0]
	position := uint32(1)
	lastDistinct := uint32(1)
	for index := range standings {
		if index == 0 {
			standings[index].Rank = 1
			continue
		}
		position++
		if compareStandingKey(standings[index], standings[index-1]) != 0 {
			lastDistinct = position
		}
		standings[index].Rank = lastDistinct
	}
	winners := make([]string, 0, len(standings))
	for index := range standings {
		if compareStandingKey(standings[index], winningKey) == 0 {
			standings[index].Winner = true
			winners = append(winners, standings[index].UserID)
		}
	}
	slices.Sort(winners)
	return FinalSummary{Standings: standings, WinnerUserIDs: winners}
}

func compareStandingKey(left, right FinalStanding) int {
	if diff := cmp.Compare(left.TotalPoints, right.TotalPoints); diff != 0 {
		return diff
	}
	if diff := cmp.Compare(boolToUint8(left.WonRoundThree), boolToUint8(right.WonRoundThree)); diff != 0 {
		return diff
	}
	if diff := cmp.Compare(boolToUint8(left.WonRoundTwo), boolToUint8(right.WonRoundTwo)); diff != 0 {
		return diff
	}
	return cmp.Compare(boolToUint8(left.WonRoundOne), boolToUint8(right.WonRoundOne))
}

func reapplyFinalTotals(state *State) {
	if state == nil {
		return
	}
	for index := range state.Players {
		state.Players[index].TotalPoints = state.Players[index].RoundOnePoints + state.Players[index].RoundTwoPoints + state.Players[index].RoundThreePoints
	}
}

func clearFinalDecorations(state *State) {
	for index := range state.Players {
		state.Players[index].FinalWinner = false
		state.Players[index].FinalRank = 0
	}
}

func recomputeFinalDecorations(state *State) {
	clearFinalDecorations(state)
	for _, standing := range state.FinalSummary.Standings {
		index := playerIndex(state.Players, standing.UserID)
		if index < 0 {
			continue
		}
		state.Players[index].FinalWinner = standing.Winner
		state.Players[index].FinalRank = standing.Rank
	}
}

func deadlineForPhase(nowUnixMillis int64, phase Phase, config Config) (int64, error) {
	switch phase {
	case PhaseRoundOneSelecting:
		return addDeadline(nowUnixMillis, config.RoundOneTimeoutSeconds)
	case PhaseRoundTwoSelecting:
		return addDeadline(nowUnixMillis, config.RoundTwoTimeoutSeconds)
	case PhaseRoundOneResult, PhaseRoundTwoResult, PhaseRoundThreeResult:
		return addDeadline(nowUnixMillis, config.RoundResultSeconds)
	case PhaseFinalResult:
		return addDeadline(nowUnixMillis, config.FinalResultSeconds)
	default:
		return 0, nil
	}
}

func addDeadline(nowUnixMillis int64, seconds uint32) (int64, error) {
	if !validUnixMillis(nowUnixMillis) {
		return 0, ruleError(CodeInvalidState, "deterministic time is required")
	}
	if seconds == 0 {
		return 0, nil
	}
	delta := int64(seconds) * 1000
	if nowUnixMillis > math.MaxInt64-delta {
		return 0, ruleError(CodeInvalidState, "deadline overflows unix milliseconds")
	}
	return nowUnixMillis + delta, nil
}

func validUnixMillis(value int64) bool { return value > 0 }

func activeCount(players []PlayerState) uint32 {
	count := uint32(0)
	for _, player := range players {
		if player.Active {
			count++
		}
	}
	return count
}

func playerIndex(players []PlayerState, userID string) int {
	for index := range players {
		if players[index].UserID == userID {
			return index
		}
	}
	return -1
}

func phaseRequiresDeadline(phase Phase) bool {
	return phase == PhaseRoundOneSelecting || phase == PhaseRoundTwoSelecting || phase == PhaseRoundOneResult ||
		phase == PhaseRoundTwoResult || phase == PhaseRoundThreeResult || phase == PhaseFinalResult
}

func selectionDeadlineEnabled(phase Phase, config Config) bool {
	switch phase {
	case PhaseRoundOneSelecting:
		return config.RoundOneTimeoutSeconds != 0
	case PhaseRoundTwoSelecting:
		return config.RoundTwoTimeoutSeconds != 0
	default:
		return true
	}
}

func validFinishReason(value string) bool {
	return value == FinishNormalCompleted || value == FinishHostRequested || value == FinishInsufficientParticipants
}

func boolToUint8(value bool) uint8 {
	if value {
		return 1
	}
	return 0
}
