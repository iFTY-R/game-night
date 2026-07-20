package projection

import (
	"github.com/iFTY-R/game-night/games/dice-789/engine"
	dice789v1 "github.com/iFTY-R/game-night/games/dice-789/gen/go/game/dice789/v1"
	"google.golang.org/protobuf/proto"
)

// BuildReplay keeps the projection helper compatible with other game modules.
// The module entry point uses BuildReplayWithInitialization for strict config/seat recovery.
func BuildReplay(events []engine.Event) (*dice789v1.Replay, error) {
	items := make([]ReplayEvent, len(events))
	for index, event := range events {
		items[index] = ReplayEvent{Event: event, Initialization: initializationFromEvent(event)}
	}
	return buildReplay(items, false)
}

// BuildReplayWithInitialization reduces public facts into a strict replay artifact.
// A rolled but unsettled turn is retained when the platform finishes or cancels a session.
func BuildReplayWithInitialization(events []ReplayEvent) (*dice789v1.Replay, error) {
	return buildReplay(events, true)
}

func buildReplay(events []ReplayEvent, requireInitialization bool) (*dice789v1.Replay, error) {
	if len(events) == 0 {
		return nil, projectionError("replay event stream is empty")
	}
	replay := &dice789v1.Replay{SchemaVersion: 1}
	entries := make([]*dice789v1.ReplayEntry, 0, len(events))
	var current *dice789v1.ReplayTurn
	var lastTurn uint32
	var trackedPool []engine.PoolLayer
	var trackedPoolTicks uint32
	var trackedDirection engine.Direction
	var pendingPoolRemoval uint32
	var frozenConfig engine.Config
	var expectedResult engine.ResultKind
	var expectedNext string
	var revocationExpectedNext string
	currentPhase := engine.Phase(0)
	var frozenPlayers []engine.Participant
	effectCancelled := false
	sourceRevoked := false
	poolChanged := false
	penaltyRecorded := false
	directionChanged := false
	effectSelected := false
	selectedPhaseAfter := engine.Phase(0)
	activePlayers := make(map[string]bool)
	penaltyTotals := make(map[string]uint32)
	haveInitialization := false
	started := false
	finished := false
	for index, item := range events {
		fact := item.Event
		if finished {
			return nil, projectionError("event appears after session finish")
		}
		if !started && (fact.Kind != engine.EventTurnStarted || fact.Turn != 1 || requireInitialization && item.Initialization == nil) {
			return nil, projectionError("replay must begin with initialized turn one")
		}
		publicEvent, err := eventToProto(item)
		if err != nil {
			return nil, err
		}
		entries = append(entries, &dice789v1.ReplayEntry{Sequence: uint64(index + 1), Event: publicEvent})
		switch fact.Kind {
		case engine.EventTurnStarted:
			if fact.Turn == 0 || started && fact.Turn != lastTurn+1 || fact.UserID == "" || !fact.Direction.Valid() || started && item.Initialization != nil ||
				started && expectedNext != "" && fact.UserID != expectedNext {
				return nil, projectionError("turn.started does not advance replay lifecycle")
			}
			if current != nil {
				if !current.Settled {
					return nil, projectionError("new turn starts while previous turn is pending")
				}
				current = nil
			}
			if !started {
				init := item.Initialization
				if init != nil && !validInitialization(init) {
					return nil, projectionError("replay initialization is invalid")
				}
				if init != nil {
					haveInitialization = true
					frozenConfig = init.Config
					frozenPlayers = append([]engine.Participant(nil), init.Players...)
					trackedPool = append([]engine.PoolLayer(nil), init.Pool...)
					trackedPoolTicks = uint32(init.TotalPoolTicks)
					replay.Config = configToProto(init.Config)
					replay.Players = make([]*dice789v1.ReplayPlayer, len(init.Players))
					for playerIndex, player := range init.Players {
						replay.Players[playerIndex] = &dice789v1.ReplayPlayer{UserId: player.UserID, SeatIndex: player.SeatIndex}
						activePlayers[player.UserID] = true
						penaltyTotals[player.UserID] = 0
					}
				}
				started = true
			}
			if trackedDirection != 0 && trackedDirection != fact.Direction || haveInitialization && !activePlayers[fact.UserID] {
				return nil, projectionError("turn.started actor or direction is inconsistent")
			}
			trackedDirection = fact.Direction
			expectedResult = engine.ResultNone
			expectedNext = ""
			revocationExpectedNext = ""
			currentPhase = engine.PhaseAwaitingRoll
			effectCancelled = false
			sourceRevoked = false
			poolChanged = false
			penaltyRecorded = false
			directionChanged = false
			effectSelected = false
			selectedPhaseAfter = 0
			current = &dice789v1.ReplayTurn{Summary: &dice789v1.TurnSummary{
				Turn: fact.Turn, SourceUserId: fact.UserID, DirectionBefore: uint32(fact.Direction), DirectionAfter: uint32(fact.Direction), NextUserId: fact.UserID,
				PoolBeforeTicks: trackedPoolTicks, PoolAfterTicks: trackedPoolTicks,
				PoolBeforeLayers: poolToProto(trackedPool), PoolAfterLayers: poolToProto(trackedPool),
			}}
			lastTurn = fact.Turn
		case engine.EventDiceRolled:
			if err := requireCurrent(current, fact.Turn); err != nil || !validDice(fact.DieOne, fact.DieTwo, fact.Sum) || current.Summary.DieOne != 0 || fact.SourceUserID != current.Summary.SourceUserId {
				return nil, projectionError("dice.rolled is outside the active turn")
			}
			if haveInitialization {
				expectedResult, err = engine.ClassifyRoll(frozenConfig, fact.DieOne, fact.DieTwo)
				if err != nil {
					return nil, projectionError("dice.rolled cannot be classified by frozen config")
				}
			}
			current.Summary.DieOne, current.Summary.DieTwo, current.Summary.Sum = fact.DieOne, fact.DieTwo, fact.Sum
			currentPhase = engine.PhaseResultPending
		case engine.EventEffectSelected:
			if err := requireCurrent(current, fact.Turn); err != nil || fact.Result == engine.ResultNone || current.Summary.Effect != dice789v1.Effect_EFFECT_UNSPECIFIED || current.Summary.DieOne == 0 ||
				fact.SourceUserID != current.Summary.SourceUserId || haveInitialization && (fact.Result != expectedResult ||
				fact.PhaseAfter != expectedEffectPhase(fact.Result, trackedPoolTicks, frozenConfig)) ||
				fact.DieOne != 0 && (fact.DieOne != current.Summary.DieOne || fact.DieTwo != current.Summary.DieTwo || fact.Sum != current.Summary.Sum) {
				return nil, projectionError("effect.selected is outside the active turn")
			}
			if !current.Summary.DroppedReported {
				current.Summary.Effect = replayEffect(item)
			}
			effectSelected = true
			selectedPhaseAfter = fact.PhaseAfter
			currentPhase = fact.PhaseAfter
			current.Summary.Cause = replayCause(item, causeToProto(fact.Reason))
			if fact.DieOne != 0 {
				current.Summary.DieOne, current.Summary.DieTwo, current.Summary.Sum = fact.DieOne, fact.DieTwo, fact.Sum
			}
		case engine.EventTargetSelected:
			if err := requireCurrent(current, fact.Turn); err != nil || fact.TargetUserID == "" || fact.TargetUserID == current.Summary.SourceUserId ||
				fact.SourceUserID != current.Summary.SourceUserId || haveInitialization && fact.Result != expectedResult ||
				haveInitialization && !activePlayers[fact.TargetUserID] || replayEffect(item) != current.Summary.Effect ||
				current.Summary.TargetUserId != "" ||
				current.Summary.Effect != dice789v1.Effect_EFFECT_DOUBLE_ONE_TARGET_DRAIN && current.Summary.Effect != dice789v1.Effect_EFFECT_DOUBLE_SIX_TARGET_ADD {
				return nil, projectionError("target.selected is outside the active turn")
			}
			current.Summary.TargetUserId = fact.TargetUserID
			if fact.Result == engine.ResultDoubleSix {
				currentPhase = engine.PhaseAwaitingAdd
			} else {
				currentPhase = engine.PhaseTurnSettled
			}
		case engine.EventPoolChanged:
			effect := replayEffect(item)
			actor, actorValid := poolActor(fact.Result, current.Summary.SourceUserId, current.Summary.TargetUserId)
			if err := requireCurrent(current, fact.Turn); err != nil || len(fact.PoolBefore) == 0 || len(fact.PoolAfter) == 0 ||
				fact.SourceUserID != current.Summary.SourceUserId || !actorValid || fact.UserID != actor || effect != current.Summary.Effect ||
				fact.Result != engine.ResultDropped && haveInitialization && fact.Result != expectedResult ||
				(fact.Result == engine.ResultDoubleOne || fact.Result == engine.ResultDoubleSix) && fact.TargetUserID != current.Summary.TargetUserId ||
				fact.Result != engine.ResultDoubleOne && fact.Result != engine.ResultDoubleSix && fact.TargetUserID != "" || poolChanged {
				return nil, projectionError("pot.changed is outside the active turn")
			}
			if haveInitialization && (uint32(fact.PoolBeforeTicks) != trackedPoolTicks || !poolEqual(fact.PoolBefore, trackedPool) ||
				!poolShapeValid(fact.PoolBefore, frozenConfig) || !poolShapeValid(fact.PoolAfter, frozenConfig)) {
				return nil, projectionError("pot.changed does not continue the public pool")
			}
			adding := fact.Result == engine.ResultSeven || fact.Result == engine.ResultDoubleSix
			if adding && fact.PoolAfterTicks < fact.PoolBeforeTicks || !adding && fact.PoolAfterTicks > fact.PoolBeforeTicks {
				return nil, projectionError("pot.changed moves in the wrong rule direction")
			}
			if haveInitialization && !poolDeltaValid(fact, frozenConfig, replayCause(item, causeToProto(fact.Reason))) {
				return nil, projectionError("pot.changed amount violates the selected rule")
			}
			poolChanged = true
			switch fact.Result {
			case engine.ResultSeven:
				currentPhase = engine.PhaseAwaitingContinue
			default:
				currentPhase = engine.PhaseTurnSettled
			}
			pendingPoolRemoval = 0
			if fact.PoolBeforeTicks > fact.PoolAfterTicks {
				pendingPoolRemoval = uint32(fact.PoolBeforeTicks - fact.PoolAfterTicks)
			}
			trackedPool = append([]engine.PoolLayer(nil), fact.PoolAfter...)
			trackedPoolTicks = uint32(fact.PoolAfterTicks)
			current.Summary.PoolBeforeTicks, current.Summary.PoolAfterTicks = uint32(fact.PoolBeforeTicks), uint32(fact.PoolAfterTicks)
			current.Summary.PoolBeforeLayers, current.Summary.PoolAfterLayers = poolToProto(fact.PoolBefore), poolToProto(fact.PoolAfter)
			current.Summary.Effect = effect
		case engine.EventPenaltyRecorded:
			effect := replayEffect(item)
			actor, actorValid := penaltyActor(fact.Result, current.Summary.SourceUserId, current.Summary.TargetUserId)
			if err := requireCurrent(current, fact.Turn); err != nil || fact.UserID == "" || fact.SourceUserID != current.Summary.SourceUserId ||
				!actorValid || fact.UserID != actor || effect != current.Summary.Effect ||
				fact.Result != engine.ResultDropped && haveInitialization && fact.Result != expectedResult || penaltyRecorded {
				return nil, projectionError("penalty.recorded is outside the active turn")
			}
			if pendingPoolRemoval != uint32(fact.PenaltyTicks) || haveInitialization && (!activePlayers[fact.UserID] ||
				uint32(fact.PenaltyBeforeTicks) != penaltyTotals[fact.UserID] || uint64(fact.PenaltyAfterTicks) != uint64(penaltyTotals[fact.UserID])+uint64(fact.PenaltyTicks)) {
				return nil, projectionError("penalty.recorded violates pool conservation or target activity")
			}
			pendingPoolRemoval = 0
			penaltyRecorded = true
			if fact.Result == engine.ResultEight || fact.Result == engine.ResultNine {
				currentPhase = engine.PhaseAwaitingContinue
			} else {
				currentPhase = engine.PhaseTurnSettled
			}
			penaltyTotals[fact.UserID] = uint32(fact.PenaltyAfterTicks)
			current.Summary.PenaltyUserId, current.Summary.PenaltyTicks = fact.UserID, uint32(fact.PenaltyTicks)
		case engine.EventDirectionChanged:
			if err := requireCurrent(current, fact.Turn); err != nil || !fact.DirectionBefore.Valid() || !fact.Direction.Valid() {
				return nil, projectionError("direction.changed is outside the active turn")
			}
			if trackedDirection != fact.DirectionBefore || fact.Result != engine.ResultOrdinaryPair || haveInitialization && expectedResult != engine.ResultOrdinaryPair ||
				fact.SourceUserID != current.Summary.SourceUserId || replayEffect(item) != current.Summary.Effect || fact.DirectionBefore == fact.Direction || directionChanged {
				return nil, projectionError("direction.changed is inconsistent with ordinary pair rules")
			}
			trackedDirection = fact.Direction
			directionChanged = true
			currentPhase = engine.PhaseTurnSettled
			current.Summary.DirectionBefore, current.Summary.DirectionAfter = uint32(fact.DirectionBefore), uint32(fact.Direction)
		case engine.EventTurnDropped:
			if err := requireCurrent(current, fact.Turn); err != nil || fact.UserID == "" || fact.SourceUserID != current.Summary.SourceUserId || fact.AuditRef == "" || current.Summary.DieOne == 0 || current.Summary.Effect != dice789v1.Effect_EFFECT_UNSPECIFIED {
				return nil, projectionError("turn.dropped_reported is outside the active turn")
			}
			current.Summary.Effect = dice789v1.Effect_EFFECT_DROPPED_DRAIN_POOL
			current.Summary.TargetUserId = fact.SourceUserID
			current.Summary.DroppedReported = true
			currentPhase = engine.PhaseTurnSettled
			current.Summary.DropOperatorUserId, current.Summary.DropReason, current.Summary.AuditRef = fact.UserID, fact.Reason, fact.AuditRef
		case engine.EventTurnSettled:
			settledEffect := replayEffect(item)
			settledOutcome := replayOutcome(item, outcomeToProto(settlementFromEvent(fact)))
			if err := requireCurrent(current, fact.Turn); err != nil || fact.SourceUserID == "" || fact.SourceUserID != current.Summary.SourceUserId || fact.NextUserID == "" || fact.Reason == "" ||
				fact.Direction != trackedDirection || haveInitialization && (!activePlayers[fact.NextUserID] ||
				!settledResultValid(fact.Result, expectedResult, current.Summary.DroppedReported, current.Summary.DieOne != 0, sourceRevoked, effectSelected) ||
				!settledNextValid(settledOutcome, fact.SourceUserID, fact.TargetUserID, fact.NextUserID, trackedDirection, frozenPlayers, activePlayers) ||
				!settledFactsComplete(fact.Result, frozenConfig, activePlayers, poolChanged, penaltyRecorded, directionChanged)) {
				return nil, projectionError("turn.settled is outside the active turn")
			}
			if sourceRevoked && revocationExpectedNext != "" && fact.NextUserID != revocationExpectedNext {
				return nil, projectionError("turn.settled disagrees with revocation audit")
			}
			if pendingPoolRemoval != 0 || current.Summary.Effect != dice789v1.Effect_EFFECT_UNSPECIFIED && current.Summary.Effect != settledEffect && !(effectCancelled && fact.Result == engine.ResultCancelled) ||
				fact.TargetUserID != current.Summary.TargetUserId {
				return nil, projectionError("turn.settled disagrees with applied public effects")
			}
			current.Settled = true
			current.TerminalPhase = dice789v1.Phase_PHASE_AWAITING_ROLL
			current.Summary.SourceUserId = fact.SourceUserID
			current.Summary.TargetUserId = fact.TargetUserID
			current.Summary.NextUserId = fact.NextUserID
			current.Summary.DirectionAfter = uint32(fact.Direction)
			current.Summary.Effect = settledEffect
			current.Summary.Outcome = settledOutcome
			current.Summary.Cause = replayCause(item, causeToProto(fact.Reason))
			current.Summary.ResolutionReason = fact.Reason
			expectedNext = fact.NextUserID
			if fact.AuditRef != "" {
				current.Summary.AuditRef = fact.AuditRef
			}
			replay.Turns = append(replay.Turns, current)
			current = nil
		case engine.EventParticipantRevoked:
			if fact.UserID == "" || current == nil || fact.Turn != current.Summary.Turn {
				return nil, projectionError("revocation user is missing")
			}
			if haveInitialization && (!activePlayers[fact.UserID] || !revocationMatchesState(fact, replayEffect(item), currentPhase, current.Summary, expectedResult, frozenConfig, activePlayers)) {
				return nil, projectionError("participant is revoked more than once")
			}
			activePlayers[fact.UserID] = false
			effectCancelled = fact.PendingEffectCancelled
			sourceRevoked = fact.UserID == current.Summary.SourceUserId
			if sourceRevoked {
				revocationExpectedNext = fact.NextUserID
			}
			if fact.TargetSelectionReopened || fact.PendingEffectCancelled {
				current.Summary.TargetUserId = ""
			}
			if fact.TargetSelectionReopened {
				currentPhase = engine.PhaseAwaitingTarget
			}
		case engine.EventSessionFinished:
			if finishCause(fact.Reason) == dice789v1.ResolutionCause_RESOLUTION_CAUSE_UNSPECIFIED || current == nil || current.Summary == nil || fact.Turn != current.Summary.Turn || pendingPoolRemoval != 0 || currentPhase == engine.PhaseTurnSettled ||
				!finishFactsValid(expectedResult, selectedPhaseAfter, current.Summary, effectSelected, poolChanged, penaltyRecorded, directionChanged) {
				return nil, projectionError("session.finished is inconsistent with the active turn")
			}
			replay.FinishReason = fact.Reason
			if current != nil {
				if current.Summary.DieOne != 0 {
					current.TerminalPhase = dice789v1.Phase_PHASE_FINISHED
					current.Summary.Outcome = dice789v1.TurnOutcome_TURN_OUTCOME_SESSION_FINISHED
					current.Summary.Cause = finishCause(fact.Reason)
					current.Summary.ResolutionReason = fact.Reason
					replay.Turns = append(replay.Turns, current)
				}
				current = nil
			}
			finished = true
		default:
			return nil, projectionError("unknown event cannot enter replay")
		}
	}
	if current != nil {
		// A stream ending at a public roll is valid for a live replay request; it is
		// marked pending rather than inventing a settlement that never occurred.
		if current.Summary.DieOne != 0 {
			replay.Turns = append(replay.Turns, current)
		}
	}
	replay.Entries = entries
	return replay, nil
}

func eventToProto(item ReplayEvent) (*dice789v1.Event, error) {
	fact := item.Event
	if item.ProtocolEvent != nil {
		if !protocolEventMatchesKind(item.ProtocolEvent, fact.Kind) {
			return nil, projectionError("protocol replay event does not match engine fact")
		}
		return proto.Clone(item.ProtocolEvent).(*dice789v1.Event), nil
	}
	switch fact.Kind {
	case engine.EventTurnStarted:
		value := &dice789v1.TurnStarted{Turn: fact.Turn, UserId: fact.UserID, Direction: uint32(fact.Direction)}
		value.Cause = replayCause(item, dice789v1.ResolutionCause_RESOLUTION_CAUSE_UNSPECIFIED)
		value.PreviousOutcome = replayOutcome(item, dice789v1.TurnOutcome_TURN_OUTCOME_UNSPECIFIED)
		initialization := item.Initialization
		if initialization == nil {
			initialization = initializationFromEvent(fact)
		}
		if initialization != nil {
			value.Config = configToProto(initialization.Config)
			value.InitialPool = poolToProto(initialization.Pool)
			value.InitialPoolTicks = uint32(initialization.TotalPoolTicks)
			value.Players = make([]*dice789v1.ReplayPlayer, len(initialization.Players))
			for index, player := range initialization.Players {
				value.Players[index] = &dice789v1.ReplayPlayer{UserId: player.UserID, SeatIndex: player.SeatIndex}
			}
		}
		return &dice789v1.Event{Event: &dice789v1.Event_TurnStarted{TurnStarted: value}}, nil
	case engine.EventDiceRolled:
		return &dice789v1.Event{Event: &dice789v1.Event_DiceRolled{DiceRolled: &dice789v1.DiceRolled{Turn: fact.Turn, SourceUserId: fact.SourceUserID, DieOne: fact.DieOne, DieTwo: fact.DieTwo, Sum: fact.Sum, Cause: replayCause(item, dice789v1.ResolutionCause_RESOLUTION_CAUSE_PLAYER_ACTION)}}}, nil
	case engine.EventEffectSelected:
		return &dice789v1.Event{Event: &dice789v1.Event_EffectSelected{EffectSelected: &dice789v1.EffectSelected{Turn: fact.Turn, Effect: effectToProto(fact), SourceUserId: fact.SourceUserID, TargetUserId: fact.TargetUserID, DieOne: fact.DieOne, DieTwo: fact.DieTwo, Sum: fact.Sum, RulePriority: rulePriority(fact.Result), NextPhase: phaseToProto(fact.PhaseAfter), Cause: replayCause(item, causeToProto(fact.Reason))}}}, nil
	case engine.EventTargetSelected:
		return &dice789v1.Event{Event: &dice789v1.Event_TargetSelected{TargetSelected: &dice789v1.TargetSelected{Turn: fact.Turn, SourceUserId: fact.SourceUserID, TargetUserId: fact.TargetUserID, Effect: effectToProto(fact), Cause: replayCause(item, causeToProto(fact.Reason))}}}, nil
	case engine.EventPoolChanged:
		return &dice789v1.Event{Event: &dice789v1.Event_PoolChanged{PoolChanged: &dice789v1.PoolChanged{BeforeTicks: uint32(fact.PoolBeforeTicks), AfterTicks: uint32(fact.PoolAfterTicks), ActorTicks: uint32(fact.EffectTicks), BeforeLayers: poolToProto(fact.PoolBefore), AfterLayers: poolToProto(fact.PoolAfter), Turn: fact.Turn, SourceUserId: fact.SourceUserID, TargetUserId: fact.TargetUserID, ActorUserId: fact.UserID, Effect: effectToProto(fact), Cause: replayCause(item, causeToProto(fact.Reason))}}}, nil
	case engine.EventPenaltyRecorded:
		return &dice789v1.Event{Event: &dice789v1.Event_PenaltyRecorded{PenaltyRecorded: &dice789v1.PenaltyRecorded{UserId: fact.UserID, Ticks: uint32(fact.PenaltyTicks), Reason: fact.Reason, BeforeTotalTicks: uint32(fact.PenaltyBeforeTicks), AfterTotalTicks: uint32(fact.PenaltyAfterTicks), Turn: fact.Turn, SourceUserId: fact.SourceUserID, Effect: effectToProto(fact), Cause: replayCause(item, causeToProto(fact.Reason))}}}, nil
	case engine.EventDirectionChanged:
		return &dice789v1.Event{Event: &dice789v1.Event_DirectionChanged{DirectionChanged: &dice789v1.DirectionChanged{Direction: uint32(fact.Direction), BeforeDirection: uint32(fact.DirectionBefore), Turn: fact.Turn, SourceUserId: fact.SourceUserID, Effect: effectToProto(fact), Cause: replayCause(item, causeToProto(fact.Reason))}}}, nil
	case engine.EventTurnSettled:
		return &dice789v1.Event{Event: &dice789v1.Event_TurnSettled{TurnSettled: &dice789v1.TurnSettled{Turn: fact.Turn, SourceUserId: fact.SourceUserID, TargetUserId: fact.TargetUserID, NextUserId: fact.NextUserID, Direction: uint32(fact.Direction), Effect: effectToProto(fact), Outcome: replayOutcome(item, outcomeToProto(settlementFromEvent(fact))), Cause: replayCause(item, causeToProto(fact.Reason)), Reason: fact.Reason}}}, nil
	case engine.EventTurnDropped:
		return &dice789v1.Event{Event: &dice789v1.Event_TurnDropped{TurnDropped: &dice789v1.TurnDropped{UserId: fact.SourceUserID, OperatorUserId: fact.UserID, SourceUserId: fact.SourceUserID, Turn: fact.Turn, Reason: fact.Reason, Cause: replayCause(item, dice789v1.ResolutionCause_RESOLUTION_CAUSE_DROP_REPORTED), AuditRef: fact.AuditRef}}}, nil
	case engine.EventParticipantRevoked:
		return &dice789v1.Event{Event: &dice789v1.Event_ParticipantRevoked{ParticipantRevoked: &dice789v1.ParticipantRevoked{UserId: fact.UserID, Turn: fact.Turn, PhaseBefore: phaseToProto(fact.PhaseBefore), Effect: effectToProto(fact), SourceUserId: fact.SourceUserID, TargetUserId: fact.TargetUserID, PendingEffectCancelled: fact.PendingEffectCancelled, TargetSelectionReopened: fact.TargetSelectionReopened, NextUserId: fact.NextUserID}}}, nil
	case engine.EventSessionFinished:
		return &dice789v1.Event{Event: &dice789v1.Event_SessionFinished{SessionFinished: &dice789v1.SessionFinished{Reason: fact.Reason, Turn: fact.Turn, OperatorUserId: fact.OperatorUserID, Cause: replayCause(item, finishCause(fact.Reason))}}}, nil
	default:
		return nil, projectionError("unknown event cannot enter replay")
	}
}

func protocolEventMatchesKind(value *dice789v1.Event, kind engine.EventKind) bool {
	if value == nil {
		return false
	}
	switch kind {
	case engine.EventTurnStarted:
		return value.GetTurnStarted() != nil
	case engine.EventDiceRolled:
		return value.GetDiceRolled() != nil
	case engine.EventEffectSelected:
		return value.GetEffectSelected() != nil
	case engine.EventTargetSelected:
		return value.GetTargetSelected() != nil
	case engine.EventPoolChanged:
		return value.GetPoolChanged() != nil
	case engine.EventPenaltyRecorded:
		return value.GetPenaltyRecorded() != nil
	case engine.EventDirectionChanged:
		return value.GetDirectionChanged() != nil
	case engine.EventTurnSettled:
		return value.GetTurnSettled() != nil
	case engine.EventTurnDropped:
		return value.GetTurnDropped() != nil
	case engine.EventParticipantRevoked:
		return value.GetParticipantRevoked() != nil
	case engine.EventSessionFinished:
		return value.GetSessionFinished() != nil
	default:
		return false
	}
}

// replayCause prefers the durable protocol audit value and falls back only for
// callers that build a replay directly from pure engine events.
func replayCause(item ReplayEvent, fallback dice789v1.ResolutionCause) dice789v1.ResolutionCause {
	if item.Cause != dice789v1.ResolutionCause_RESOLUTION_CAUSE_UNSPECIFIED {
		return item.Cause
	}
	return fallback
}

// replayOutcome preserves an explicitly validated protocol outcome across the
// decode/project cycle while keeping the engine-only helper backward compatible.
func replayOutcome(item ReplayEvent, fallback dice789v1.TurnOutcome) dice789v1.TurnOutcome {
	if item.Outcome != dice789v1.TurnOutcome_TURN_OUTCOME_UNSPECIFIED {
		return item.Outcome
	}
	return fallback
}

func replayEffect(item ReplayEvent) dice789v1.Effect {
	if value := item.ProtocolEvent; value != nil {
		switch item.Event.Kind {
		case engine.EventEffectSelected:
			return value.GetEffectSelected().GetEffect()
		case engine.EventTargetSelected:
			return value.GetTargetSelected().GetEffect()
		case engine.EventPoolChanged:
			return value.GetPoolChanged().GetEffect()
		case engine.EventPenaltyRecorded:
			return value.GetPenaltyRecorded().GetEffect()
		case engine.EventDirectionChanged:
			return value.GetDirectionChanged().GetEffect()
		case engine.EventTurnSettled:
			return value.GetTurnSettled().GetEffect()
		case engine.EventParticipantRevoked:
			return value.GetParticipantRevoked().GetEffect()
		}
	}
	return effectToProto(item.Event)
}

func revocationMatchesState(
	fact engine.Event,
	effect dice789v1.Effect,
	phase engine.Phase,
	summary *dice789v1.TurnSummary,
	expectedResult engine.ResultKind,
	config engine.Config,
	active map[string]bool,
) bool {
	if summary == nil || fact.PhaseBefore != phase || fact.SourceUserID != summary.SourceUserId || fact.TargetUserID != summary.TargetUserId {
		return false
	}
	expectedEffect := summary.Effect
	if phase == engine.PhaseAwaitingRoll {
		expectedEffect = dice789v1.Effect_EFFECT_UNSPECIFIED
	} else if phase == engine.PhaseResultPending {
		expectedEffect = resultEffect(expectedResult, config, active)
	}
	if effect != expectedEffect {
		return false
	}
	activeCount := 0
	for _, isActive := range active {
		if isActive {
			activeCount++
		}
	}
	willFinish := activeCount-1 < engine.MinimumPlayers
	preEffect := phase == engine.PhaseResultPending || phase == engine.PhaseAwaitingAdd || phase == engine.PhaseAwaitingTarget
	if willFinish {
		return fact.NextUserID == "" && !fact.TargetSelectionReopened && fact.PendingEffectCancelled == preEffect
	}
	if fact.UserID == summary.SourceUserId {
		return fact.NextUserID != "" && !fact.TargetSelectionReopened && fact.PendingEffectCancelled == preEffect
	}
	if phase == engine.PhaseAwaitingAdd && expectedResult == engine.ResultDoubleSix && fact.UserID == summary.TargetUserId {
		return fact.NextUserID == "" && fact.TargetSelectionReopened && !fact.PendingEffectCancelled
	}
	return fact.NextUserID == "" && !fact.TargetSelectionReopened && !fact.PendingEffectCancelled
}

func resultEffect(result engine.ResultKind, config engine.Config, active map[string]bool) dice789v1.Effect {
	if result == engine.ResultOrdinaryPair {
		activeCount := 0
		for _, isActive := range active {
			if isActive {
				activeCount++
			}
		}
		if config.OrdinaryPairsReverse && activeCount >= 3 {
			return dice789v1.Effect_EFFECT_ORDINARY_PAIR_REVERSE
		}
		return dice789v1.Effect_EFFECT_ORDINARY_PAIR_REROLL
	}
	return effectToProto(engine.Event{Result: result})
}

func poolActor(result engine.ResultKind, source, target string) (string, bool) {
	switch result {
	case engine.ResultSeven, engine.ResultEight, engine.ResultNine, engine.ResultDoubleFour, engine.ResultDropped:
		return source, source != ""
	case engine.ResultDoubleOne, engine.ResultDoubleSix:
		return target, target != ""
	default:
		return "", false
	}
}

func expectedEffectPhase(result engine.ResultKind, poolTicks uint32, config engine.Config) engine.Phase {
	switch result {
	case engine.ResultSeven:
		capacity := uint64(config.LayerCapacityTicks) * uint64(config.MaxLayers)
		if uint64(poolTicks) == capacity {
			return engine.PhaseAwaitingContinue
		}
		return engine.PhaseAwaitingAdd
	case engine.ResultEight, engine.ResultNine:
		return engine.PhaseAwaitingContinue
	case engine.ResultDoubleOne, engine.ResultDoubleSix:
		return engine.PhaseAwaitingTarget
	default:
		return engine.PhaseAwaitingRoll
	}
}

func penaltyActor(result engine.ResultKind, source, target string) (string, bool) {
	switch result {
	case engine.ResultEight, engine.ResultNine, engine.ResultDoubleFour, engine.ResultDropped:
		return source, source != ""
	case engine.ResultDoubleOne:
		return target, target != ""
	default:
		return "", false
	}
}

func poolShapeValid(layers []engine.PoolLayer, config engine.Config) bool {
	if len(layers) == 0 || len(layers) > int(config.MaxLayers) {
		return false
	}
	for index, layer := range layers {
		if layer.Ticks > config.LayerCapacityTicks || index < len(layers)-1 && layer.Ticks != config.LayerCapacityTicks ||
			len(layers) > 1 && index == len(layers)-1 && layer.Ticks == 0 {
			return false
		}
	}
	return true
}

func poolDeltaValid(fact engine.Event, config engine.Config, cause dice789v1.ResolutionCause) bool {
	before, after := uint64(fact.PoolBeforeTicks), uint64(fact.PoolAfterTicks)
	switch fact.Result {
	case engine.ResultEight, engine.ResultDoubleFour:
		return before >= after && before-after == (before+1)/2
	case engine.ResultNine, engine.ResultDoubleOne, engine.ResultDropped:
		return after == 0 && uint64(fact.EffectTicks) == before
	case engine.ResultSeven, engine.ResultDoubleSix:
		if after < before {
			return false
		}
		amount := after - before
		capacity := uint64(config.LayerCapacityTicks) * uint64(config.MaxLayers)
		if before > capacity {
			return false
		}
		remaining := capacity - before
		minimum := uint64(config.AddStepTicks)
		if remaining < minimum {
			minimum = remaining
		}
		legal := remaining == 0 && amount == 0 || remaining > 0 && (amount == remaining || amount > 0 && amount%uint64(config.AddStepTicks) == 0)
		if cause == dice789v1.ResolutionCause_RESOLUTION_CAUSE_TIMEOUT {
			return legal && amount == minimum
		}
		return legal
	default:
		return false
	}
}

func settledFactsComplete(result engine.ResultKind, config engine.Config, active map[string]bool, pool, penalty, direction bool) bool {
	switch result {
	case engine.ResultSeven, engine.ResultDoubleSix:
		return pool && !penalty && !direction
	case engine.ResultEight, engine.ResultNine, engine.ResultDoubleOne, engine.ResultDoubleFour, engine.ResultDropped:
		return pool && penalty && !direction
	case engine.ResultOrdinaryPair:
		activeCount := 0
		for _, isActive := range active {
			if isActive {
				activeCount++
			}
		}
		return !pool && !penalty && direction == (config.OrdinaryPairsReverse && activeCount >= 3)
	case engine.ResultOther, engine.ResultRollTimeout, engine.ResultCancelled:
		return !pool && !penalty && !direction
	default:
		return false
	}
}

func settledResultValid(result, expected engine.ResultKind, dropped, rolled, sourceRevoked, effectSelected bool) bool {
	switch result {
	case engine.ResultDropped:
		return dropped
	case engine.ResultRollTimeout:
		return !rolled && expected == engine.ResultNone
	case engine.ResultCancelled:
		return sourceRevoked
	default:
		return rolled && effectSelected && result == expected
	}
}

func finishFactsValid(
	result engine.ResultKind,
	phaseAfter engine.Phase,
	summary *dice789v1.TurnSummary,
	effectSelected, pool, penalty, direction bool,
) bool {
	if summary == nil || summary.DieOne == 0 {
		return !effectSelected && !pool && !penalty && !direction
	}
	if !effectSelected {
		return !pool && !penalty && !direction
	}
	switch result {
	case engine.ResultSeven:
		if phaseAfter == engine.PhaseAwaitingAdd {
			// A single legal pot event advances awaiting_add to awaiting_continue.
			return !penalty && !direction
		}
		return phaseAfter == engine.PhaseAwaitingContinue && pool && !penalty && !direction
	case engine.ResultEight, engine.ResultNine:
		return phaseAfter == engine.PhaseAwaitingContinue && pool && penalty && !direction
	case engine.ResultDoubleOne:
		return phaseAfter == engine.PhaseAwaitingTarget && summary.TargetUserId == "" && !pool && !penalty && !direction
	case engine.ResultDoubleSix:
		return phaseAfter == engine.PhaseAwaitingTarget && !pool && !penalty && !direction
	default:
		return false
	}
}

func settledNextValid(
	outcome dice789v1.TurnOutcome,
	source, target, next string,
	direction engine.Direction,
	players []engine.Participant,
	active map[string]bool,
) bool {
	switch outcome {
	case dice789v1.TurnOutcome_TURN_OUTCOME_REROLL:
		return next == source && active[source]
	case dice789v1.TurnOutcome_TURN_OUTCOME_TARGET_TAKES_TURN:
		return target != "" && next == target && active[target]
	case dice789v1.TurnOutcome_TURN_OUTCOME_PASS, dice789v1.TurnOutcome_TURN_OUTCOME_SOURCE_REVOKED:
		expected, ok := adjacentActivePlayer(players, active, source, direction)
		return ok && next == expected
	default:
		return false
	}
}

func adjacentActivePlayer(players []engine.Participant, active map[string]bool, source string, direction engine.Direction) (string, bool) {
	start := -1
	for index, player := range players {
		if player.UserID == source {
			start = index
			break
		}
	}
	if start < 0 || !direction.Valid() {
		return "", false
	}
	step := 1
	if direction == engine.DirectionCounterClockwise {
		step = -1
	}
	for offset := 1; offset <= len(players); offset++ {
		index := (start + step*offset) % len(players)
		if index < 0 {
			index += len(players)
		}
		if active[players[index].UserID] {
			return players[index].UserID, true
		}
	}
	return "", false
}

func requireCurrent(current *dice789v1.ReplayTurn, turn uint32) error {
	if current == nil || current.Summary == nil || current.Summary.Turn != turn {
		return projectionError("event turn does not match active replay turn")
	}
	return nil
}

func settlementFromEvent(value engine.Event) engine.TurnSettlement {
	return engine.TurnSettlement{Turn: value.Turn, SourceUserID: value.SourceUserID, TargetUserID: value.TargetUserID, Result: value.Result, NextUserID: value.NextUserID, DirectionAfter: value.Direction, DirectionBefore: value.DirectionBefore, Reason: value.Reason, PoolBeforeTicks: value.PoolBeforeTicks, PoolAfterTicks: value.PoolAfterTicks, PoolBefore: value.PoolBefore, PoolAfter: value.PoolAfter, PenaltyTicks: value.PenaltyTicks, AuditRef: value.AuditRef}
}

func effectToProto(value engine.Event) dice789v1.Effect {
	result := value.Result
	if result == "" || result == engine.ResultNone {
		switch value.Reason {
		case "eight":
			result = engine.ResultEight
		case "nine":
			result = engine.ResultNine
		case "double_one":
			result = engine.ResultDoubleOne
		case "double_four":
			result = engine.ResultDoubleFour
		case "dropped":
			result = engine.ResultDropped
		}
	}
	if result == engine.ResultOrdinaryPair {
		if value.DirectionBefore.Valid() && value.Direction.Valid() && value.DirectionBefore != value.Direction {
			return dice789v1.Effect_EFFECT_ORDINARY_PAIR_REVERSE
		}
		return dice789v1.Effect_EFFECT_ORDINARY_PAIR_REROLL
	}
	switch result {
	case engine.ResultSeven:
		return dice789v1.Effect_EFFECT_SUM_SEVEN_ADD
	case engine.ResultEight:
		return dice789v1.Effect_EFFECT_SUM_EIGHT_HALF_POOL
	case engine.ResultNine:
		return dice789v1.Effect_EFFECT_SUM_NINE_DRAIN_POOL
	case engine.ResultDoubleOne:
		return dice789v1.Effect_EFFECT_DOUBLE_ONE_TARGET_DRAIN
	case engine.ResultDoubleFour:
		return dice789v1.Effect_EFFECT_DOUBLE_FOUR_HALF_POOL_REROLL
	case engine.ResultDoubleSix:
		return dice789v1.Effect_EFFECT_DOUBLE_SIX_TARGET_ADD
	case engine.ResultDropped:
		return dice789v1.Effect_EFFECT_DROPPED_DRAIN_POOL
	case engine.ResultOther, engine.ResultRollTimeout, engine.ResultCancelled:
		return dice789v1.Effect_EFFECT_PASS
	default:
		return dice789v1.Effect_EFFECT_UNSPECIFIED
	}
}

func rulePriority(value engine.ResultKind) uint32 {
	switch value {
	case engine.ResultDoubleOne, engine.ResultDoubleFour, engine.ResultDoubleSix:
		return 2
	case engine.ResultOrdinaryPair:
		return 3
	case engine.ResultSeven, engine.ResultEight, engine.ResultNine:
		return 4
	default:
		return 5
	}
}

func finishCause(reason string) dice789v1.ResolutionCause {
	switch reason {
	case engine.FinishPlatformCancelled:
		return dice789v1.ResolutionCause_RESOLUTION_CAUSE_PLATFORM_CANCELLED
	case engine.FinishInsufficientParticipants:
		return dice789v1.ResolutionCause_RESOLUTION_CAUSE_INSUFFICIENT_PLAYERS
	case engine.FinishHostRequested:
		return dice789v1.ResolutionCause_RESOLUTION_CAUSE_HOST_FINISHED
	default:
		return dice789v1.ResolutionCause_RESOLUTION_CAUSE_UNSPECIFIED
	}
}

func validDice(one, two, sum uint32) bool {
	return one >= 1 && one <= 6 && two >= 1 && two <= 6 && sum == one+two
}

func validInitialization(value *ReplayInitialization) bool {
	if value == nil || engine.ValidateParticipants(value.Players) != nil || value.Config.Validate(len(value.Players)) != nil || len(value.Pool) == 0 || len(value.Pool) > int(value.Config.MaxLayers) {
		return false
	}
	for index := 1; index < len(value.Players); index++ {
		if value.Players[index-1].SeatIndex >= value.Players[index].SeatIndex {
			return false
		}
	}
	var total uint32
	for index, layer := range value.Pool {
		if layer.Ticks > value.Config.LayerCapacityTicks || index < len(value.Pool)-1 && layer.Ticks != value.Config.LayerCapacityTicks || len(value.Pool) > 1 && index == len(value.Pool)-1 && layer.Ticks == 0 {
			return false
		}
		total += uint32(layer.Ticks)
	}
	return total == uint32(value.TotalPoolTicks) && value.TotalPoolTicks == value.Config.InitialPoolTicks
}

func poolEqual(left, right []engine.PoolLayer) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Ticks != right[index].Ticks {
			return false
		}
	}
	return true
}

func initializationFromEvent(event engine.Event) *ReplayInitialization {
	if event.Turn != 1 || event.InitialConfig == nil || len(event.InitialParticipants) == 0 || len(event.InitialPool) == 0 {
		return nil
	}
	config := *event.InitialConfig
	return &ReplayInitialization{
		Config: config, Players: append([]engine.Participant(nil), event.InitialParticipants...),
		Pool: append([]engine.PoolLayer(nil), event.InitialPool...), TotalPoolTicks: event.InitialTotalPoolTicks,
	}
}
