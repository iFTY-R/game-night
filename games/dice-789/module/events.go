package module

import (
	"github.com/iFTY-R/game-night/games/dice-789/engine"
	dice789v1 "github.com/iFTY-R/game-night/games/dice-789/gen/go/game/dice789/v1"
	"github.com/iFTY-R/game-night/games/dice-789/projection"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"github.com/iFTY-R/game-night/sdk/go/game/dice"
	"google.golang.org/protobuf/proto"
)

func encodeEvents(facts []engine.Event, state engine.State) ([]game.Event, error) {
	if len(facts) == 0 {
		return nil, malformed("transition has no engine events")
	}
	events := make([]game.Event, len(facts))
	for index, fact := range facts {
		message, err := encodeEvent(fact, facts, state)
		if err != nil {
			return nil, err
		}
		events[index] = game.Event{Message: message}
	}
	return events, nil
}

func encodeEvent(fact engine.Event, batch []engine.Event, state engine.State) (game.Message, error) {
	var messageType game.Identifier
	var message proto.Message
	switch fact.Kind {
	case engine.EventTurnStarted:
		if fact.Turn == 0 || fact.UserID == "" || !fact.Direction.Valid() {
			return game.Message{}, malformed("turn start event is incomplete")
		}
		value := &dice789v1.TurnStarted{Turn: fact.Turn, UserId: fact.UserID, Direction: uint32(fact.Direction)}
		if state.LastSettlement.Turn+1 == fact.Turn {
			value.PreviousOutcome = outcomeToProto(state.LastSettlement)
			value.Cause = eventCause(fact, batch)
		}
		if fact.Turn == 1 {
			if fact.InitialConfig == nil || len(fact.InitialParticipants) < engine.MinimumPlayers || len(fact.InitialPool) == 0 {
				return game.Message{}, malformed("first turn lacks initialization facts")
			}
			value.Config = configToProto(*fact.InitialConfig)
			value.InitialPool = poolToProto(fact.InitialPool)
			value.InitialPoolTicks = uint32(fact.InitialTotalPoolTicks)
			value.Players = make([]*dice789v1.ReplayPlayer, len(fact.InitialParticipants))
			for index, player := range fact.InitialParticipants {
				value.Players[index] = &dice789v1.ReplayPlayer{UserId: player.UserID, SeatIndex: player.SeatIndex}
			}
		}
		messageType, message = EventTurnStartedMessage, value
	case engine.EventDiceRolled:
		if fact.Turn == 0 || fact.SourceUserID == "" || !validDice(fact.DieOne, fact.DieTwo, fact.Sum) {
			return game.Message{}, malformed("dice roll event is incomplete")
		}
		messageType, message = EventDiceRolledMessage, &dice789v1.DiceRolled{
			Turn: fact.Turn, SourceUserId: fact.SourceUserID, DieOne: fact.DieOne, DieTwo: fact.DieTwo, Sum: fact.Sum,
			Cause: dice789v1.ResolutionCause_RESOLUTION_CAUSE_PLAYER_ACTION,
		}
	case engine.EventEffectSelected:
		if fact.Turn == 0 || fact.SourceUserID == "" || fact.Result == engine.ResultNone {
			return game.Message{}, malformed("effect selection event is incomplete")
		}
		dieOne, dieTwo, sum := turnDice(fact.Turn, batch, state)
		effect := eventEffect(fact)
		if fact.Result == engine.ResultOrdinaryPair && activePlayers(state) >= 3 && state.Config.OrdinaryPairsReverse {
			effect = dice789v1.Effect_EFFECT_ORDINARY_PAIR_REVERSE
		}
		nextPhase := nextPhaseForEffect(fact, state)
		if fact.PhaseAfter >= engine.PhaseAwaitingRoll && fact.PhaseAfter <= engine.PhaseFinished && fact.PhaseAfter != engine.PhaseTurnSettled {
			nextPhase = phaseToProto(fact.PhaseAfter)
		}
		messageType, message = EventEffectSelectedMessage, &dice789v1.EffectSelected{
			Turn: fact.Turn, Effect: eventEffect(fact), SourceUserId: fact.SourceUserID, TargetUserId: fact.TargetUserID,
			DieOne: dieOne, DieTwo: dieTwo, Sum: sum, RulePriority: rulePriority(fact.Result),
			NextPhase: nextPhase, Cause: eventCause(fact, batch),
		}
		message.(*dice789v1.EffectSelected).Effect = effect
	case engine.EventTargetSelected:
		if fact.Turn == 0 || fact.SourceUserID == "" || fact.TargetUserID == "" {
			return game.Message{}, malformed("target selection event is incomplete")
		}
		messageType, message = EventTargetSelectedMessage, &dice789v1.TargetSelected{
			Turn: fact.Turn, SourceUserId: fact.SourceUserID, TargetUserId: fact.TargetUserID,
			Effect: eventEffect(fact), Cause: eventCause(fact, batch),
		}
	case engine.EventPoolChanged:
		if fact.Turn == 0 || fact.UserID == "" || len(fact.PoolBefore) == 0 || len(fact.PoolAfter) == 0 {
			return game.Message{}, malformed("pool change event is incomplete")
		}
		effect := eventEffect(fact)
		cause := eventCause(fact, batch)
		if batchContainsDropped(batch, fact.Turn) {
			effect = dice789v1.Effect_EFFECT_DROPPED_DRAIN_POOL
			cause = dice789v1.ResolutionCause_RESOLUTION_CAUSE_DROP_REPORTED
		}
		messageType, message = EventPoolChangedMessage, &dice789v1.PoolChanged{
			BeforeTicks: uint32(fact.PoolBeforeTicks), AfterTicks: uint32(fact.PoolAfterTicks), ActorTicks: uint32(fact.EffectTicks),
			BeforeLayers: poolToProto(fact.PoolBefore), AfterLayers: poolToProto(fact.PoolAfter), Turn: fact.Turn,
			SourceUserId: fact.SourceUserID, TargetUserId: fact.TargetUserID, ActorUserId: fact.UserID,
			Effect: effect, Cause: cause,
		}
	case engine.EventPenaltyRecorded:
		if fact.Turn == 0 || fact.UserID == "" || fact.SourceUserID == "" {
			return game.Message{}, malformed("penalty event is incomplete")
		}
		before, after := uint32(fact.PenaltyBeforeTicks), uint32(fact.PenaltyAfterTicks)
		if before == 0 && after == 0 {
			after = playerPenalty(state, fact.UserID)
			if after >= uint32(fact.PenaltyTicks) {
				before = after - uint32(fact.PenaltyTicks)
			}
		}
		messageType, message = EventPenaltyRecordedMessage, &dice789v1.PenaltyRecorded{
			UserId: fact.UserID, Ticks: uint32(fact.PenaltyTicks), Reason: fact.Reason,
			BeforeTotalTicks: before, AfterTotalTicks: after, Turn: fact.Turn, SourceUserId: fact.SourceUserID,
			Effect: eventEffect(fact), Cause: eventCause(fact, batch),
		}
	case engine.EventDirectionChanged:
		if fact.Turn == 0 || fact.SourceUserID == "" || !fact.DirectionBefore.Valid() || !fact.Direction.Valid() {
			return game.Message{}, malformed("direction event is incomplete")
		}
		messageType, message = EventDirectionChangedMessage, &dice789v1.DirectionChanged{
			Turn: fact.Turn, SourceUserId: fact.SourceUserID, BeforeDirection: uint32(fact.DirectionBefore),
			Direction: uint32(fact.Direction), Effect: eventEffect(fact), Cause: eventCause(fact, batch),
		}
	case engine.EventTurnSettled:
		if fact.Turn == 0 || fact.SourceUserID == "" || fact.NextUserID == "" || !fact.Direction.Valid() || fact.Reason == "" {
			return game.Message{}, malformed("turn settlement event is incomplete")
		}
		settlement := settlementFromEvent(fact)
		messageType, message = EventTurnSettledMessage, &dice789v1.TurnSettled{
			Turn: fact.Turn, SourceUserId: fact.SourceUserID, TargetUserId: fact.TargetUserID, NextUserId: fact.NextUserID,
			Direction: uint32(fact.Direction), Effect: eventEffect(fact), Outcome: outcomeToProto(settlement),
			Cause: eventCause(fact, batch), Reason: fact.Reason,
		}
	case engine.EventTurnDropped:
		if fact.Turn == 0 || fact.UserID == "" || fact.SourceUserID == "" || fact.Reason == "" || fact.AuditRef == "" {
			return game.Message{}, malformed("dropped-roll audit event is incomplete")
		}
		messageType, message = EventTurnDroppedMessage, &dice789v1.TurnDropped{
			UserId: fact.SourceUserID, OperatorUserId: fact.UserID, SourceUserId: fact.SourceUserID,
			Turn: fact.Turn, Reason: fact.Reason, AuditRef: fact.AuditRef,
			Cause: dice789v1.ResolutionCause_RESOLUTION_CAUSE_DROP_REPORTED,
		}
	case engine.EventParticipantRevoked:
		if fact.Turn == 0 || fact.UserID == "" {
			return game.Message{}, malformed("participant revocation event is incomplete")
		}
		messageType, message = EventParticipantRevokedMessage, revocationToProto(fact, batch, state)
	case engine.EventSessionFinished:
		cause := finishCause(fact.Reason)
		if fact.Turn == 0 || cause == dice789v1.ResolutionCause_RESOLUTION_CAUSE_UNSPECIFIED ||
			cause == dice789v1.ResolutionCause_RESOLUTION_CAUSE_HOST_FINISHED && fact.OperatorUserID == "" ||
			cause != dice789v1.ResolutionCause_RESOLUTION_CAUSE_HOST_FINISHED && fact.OperatorUserID != "" {
			return game.Message{}, malformed("session finish event is incomplete")
		}
		messageType, message = EventSessionFinishedMessage, &dice789v1.SessionFinished{
			Reason: fact.Reason, Turn: fact.Turn, OperatorUserId: fact.OperatorUserID, Cause: cause,
		}
	default:
		return game.Message{}, malformed("unknown engine event")
	}
	payload, err := marshalDeterministic(message)
	if err != nil {
		return game.Message{}, malformed("event encoding failed")
	}
	return game.Message{MessageType: messageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload}, nil
}

func decodeEvents(events []game.Event) ([]projection.ReplayEvent, error) {
	if len(events) == 0 {
		return nil, malformed("replay event stream is empty")
	}
	decoded := make([]projection.ReplayEvent, len(events))
	for index, event := range events {
		if !event.Valid() || event.Message.SchemaVersion != ProtocolSchemaVersion {
			return nil, malformed("event envelope is invalid")
		}
		value, err := decodeEvent(event.Message)
		if err != nil {
			return nil, err
		}
		decoded[index] = value
	}
	return decoded, nil
}

func validateVersionedEvents(events []game.VersionedEvent) error {
	var lastVersion uint64
	for _, event := range events {
		if !event.Valid() || lastVersion > event.StateVersion {
			return malformed("versioned event ordering is invalid")
		}
		lastVersion = event.StateVersion
		if _, err := decodeEvent(event.Event.Message); err != nil {
			return err
		}
	}
	return nil
}

func decodeEvent(message game.Message) (projection.ReplayEvent, error) {
	if message.SchemaVersion != ProtocolSchemaVersion {
		return projection.ReplayEvent{}, malformed("event schema version is unsupported")
	}
	var fact engine.Event
	var initialization *projection.ReplayInitialization
	var cause dice789v1.ResolutionCause
	var outcome dice789v1.TurnOutcome
	var protocolEvent *dice789v1.Event
	switch message.MessageType {
	case EventTurnStartedMessage:
		var value dice789v1.TurnStarted
		if err := unmarshalStrict(message.Payload, &value); err != nil || value.GetTurn() == 0 || value.GetUserId() == "" || !validCause(value.GetCause(), true) || !validOutcome(value.GetPreviousOutcome(), true) {
			return projection.ReplayEvent{}, malformed("turn.started event is invalid")
		}
		direction := engine.Direction(value.GetDirection())
		if !direction.Valid() {
			return projection.ReplayEvent{}, malformed("turn.started direction is invalid")
		}
		fact = engine.Event{Kind: engine.EventTurnStarted, Turn: value.GetTurn(), UserID: value.GetUserId(), NextUserID: value.GetUserId(), Direction: direction}
		if value.GetTurn() == 1 {
			if value.GetConfig() == nil || len(value.GetPlayers()) < engine.MinimumPlayers || len(value.GetInitialPool()) == 0 {
				return projection.ReplayEvent{}, malformed("first turn lacks replay initialization")
			}
			config, err := configFromProto(value.GetConfig())
			if err != nil {
				return projection.ReplayEvent{}, err
			}
			players := make([]engine.Participant, len(value.GetPlayers()))
			for index, player := range value.GetPlayers() {
				if player == nil || index > 0 && value.GetPlayers()[index-1].GetSeatIndex() >= player.GetSeatIndex() {
					return projection.ReplayEvent{}, malformed("replay player is nil")
				}
				players[index] = engine.Participant{UserID: player.GetUserId(), SeatIndex: player.GetSeatIndex()}
			}
			if err := engine.ValidateParticipants(players); err != nil || config.Validate(len(players)) != nil {
				return projection.ReplayEvent{}, malformed("replay initialization is invalid")
			}
			pool, err := poolFromProto(value.GetInitialPool())
			if err != nil || poolTotal(pool) != dice.Ticks(value.GetInitialPoolTicks()) || dice.Ticks(value.GetInitialPoolTicks()) != config.InitialPoolTicks {
				return projection.ReplayEvent{}, malformed("replay initial pool is invalid")
			}
			initialization = &projection.ReplayInitialization{Config: config, Players: players, Pool: pool, TotalPoolTicks: dice.Ticks(value.GetInitialPoolTicks())}
			initialConfig := config
			fact.InitialConfig = &initialConfig
			fact.InitialParticipants = append([]engine.Participant(nil), players...)
			fact.InitialPool = append([]engine.PoolLayer(nil), pool...)
			fact.InitialTotalPoolTicks = dice.Ticks(value.GetInitialPoolTicks())
		} else if value.GetConfig() != nil || len(value.GetPlayers()) != 0 || len(value.GetInitialPool()) != 0 || value.GetInitialPoolTicks() != 0 {
			return projection.ReplayEvent{}, malformed("later turn repeats replay initialization")
		}
		cause, outcome = value.GetCause(), value.GetPreviousOutcome()
		protocolEvent = &dice789v1.Event{Event: &dice789v1.Event_TurnStarted{TurnStarted: &value}}
	case EventDiceRolledMessage:
		var value dice789v1.DiceRolled
		if err := unmarshalStrict(message.Payload, &value); err != nil || value.GetTurn() == 0 || value.GetSourceUserId() == "" || !validDice(value.GetDieOne(), value.GetDieTwo(), value.GetSum()) || value.GetCause() != dice789v1.ResolutionCause_RESOLUTION_CAUSE_PLAYER_ACTION {
			return projection.ReplayEvent{}, malformed("dice.rolled event is invalid")
		}
		cause = value.GetCause()
		fact = engine.Event{Kind: engine.EventDiceRolled, Turn: value.GetTurn(), UserID: value.GetSourceUserId(), SourceUserID: value.GetSourceUserId(), DieOne: value.GetDieOne(), DieTwo: value.GetDieTwo(), Sum: value.GetSum()}
		protocolEvent = &dice789v1.Event{Event: &dice789v1.Event_DiceRolled{DiceRolled: &value}}
	case EventEffectSelectedMessage:
		var value dice789v1.EffectSelected
		if err := unmarshalStrict(message.Payload, &value); err != nil {
			return projection.ReplayEvent{}, malformed("effect.selected event is invalid")
		}
		_, phaseValid := phaseFromProto(value.GetNextPhase())
		if value.GetTurn() == 0 || value.GetSourceUserId() == "" || !phaseValid || !validCause(value.GetCause(), false) {
			return projection.ReplayEvent{}, malformed("effect.selected event is invalid")
		}
		result, ok := resultFromProto(value.GetEffect(), engine.PhaseResultPending)
		if !ok || value.GetCause() != dice789v1.ResolutionCause_RESOLUTION_CAUSE_HOST_CONFIRMED && value.GetCause() != dice789v1.ResolutionCause_RESOLUTION_CAUSE_TIMEOUT ||
			!effectNextPhaseValid(value.GetEffect(), value.GetNextPhase()) || value.GetRulePriority() != rulePriority(result) {
			return projection.ReplayEvent{}, malformed("effect.selected effect is invalid")
		}
		cause = value.GetCause()
		phaseAfter, _ := phaseFromProto(value.GetNextPhase())
		fact = engine.Event{Kind: engine.EventEffectSelected, Turn: value.GetTurn(), UserID: value.GetSourceUserId(), SourceUserID: value.GetSourceUserId(), TargetUserID: value.GetTargetUserId(), DieOne: value.GetDieOne(), DieTwo: value.GetDieTwo(), Sum: value.GetSum(), Result: result, PhaseAfter: phaseAfter, Reason: causeReason(value.GetCause())}
		protocolEvent = &dice789v1.Event{Event: &dice789v1.Event_EffectSelected{EffectSelected: &value}}
	case EventTargetSelectedMessage:
		var value dice789v1.TargetSelected
		if err := unmarshalStrict(message.Payload, &value); err != nil || value.GetTurn() == 0 || value.GetSourceUserId() == "" || value.GetTargetUserId() == "" || !validCause(value.GetCause(), false) {
			return projection.ReplayEvent{}, malformed("target.selected event is invalid")
		}
		result, ok := resultFromProto(value.GetEffect(), engine.PhaseAwaitingTarget)
		if !ok || result != engine.ResultDoubleOne && result != engine.ResultDoubleSix {
			return projection.ReplayEvent{}, malformed("target.selected effect is invalid")
		}
		cause = value.GetCause()
		fact = engine.Event{Kind: engine.EventTargetSelected, Turn: value.GetTurn(), UserID: value.GetSourceUserId(), SourceUserID: value.GetSourceUserId(), TargetUserID: value.GetTargetUserId(), Result: result, Reason: causeReason(value.GetCause())}
		protocolEvent = &dice789v1.Event{Event: &dice789v1.Event_TargetSelected{TargetSelected: &value}}
	case EventPoolChangedMessage:
		var value dice789v1.PoolChanged
		if err := unmarshalStrict(message.Payload, &value); err != nil || value.GetTurn() == 0 || value.GetActorUserId() == "" || !validCause(value.GetCause(), false) {
			return projection.ReplayEvent{}, malformed("pot.changed event is invalid")
		}
		before, err := poolFromProto(value.GetBeforeLayers())
		if err != nil {
			return projection.ReplayEvent{}, err
		}
		after, err := poolFromProto(value.GetAfterLayers())
		if err != nil || poolTotal(before) != dice.Ticks(value.GetBeforeTicks()) || poolTotal(after) != dice.Ticks(value.GetAfterTicks()) {
			return projection.ReplayEvent{}, malformed("pot.changed layers disagree with totals")
		}
		delta := value.GetAfterTicks() - value.GetBeforeTicks()
		if value.GetBeforeTicks() > value.GetAfterTicks() {
			delta = value.GetBeforeTicks() - value.GetAfterTicks()
		}
		if value.GetActorTicks() != delta {
			return projection.ReplayEvent{}, malformed("pot.changed actor ticks disagree with delta")
		}
		result, ok := resultFromProto(value.GetEffect(), engine.PhaseAwaitingAdd)
		if !ok || result == engine.ResultOther || result == engine.ResultOrdinaryPair {
			return projection.ReplayEvent{}, malformed("pot.changed effect is invalid")
		}
		cause = value.GetCause()
		fact = engine.Event{Kind: engine.EventPoolChanged, Turn: value.GetTurn(), UserID: value.GetActorUserId(), SourceUserID: value.GetSourceUserId(), TargetUserID: value.GetTargetUserId(), Result: result, PoolBeforeTicks: dice.Ticks(value.GetBeforeTicks()), PoolAfterTicks: dice.Ticks(value.GetAfterTicks()), PoolBefore: before, PoolAfter: after, EffectTicks: dice.Ticks(value.GetActorTicks()), Reason: causeReason(value.GetCause())}
		protocolEvent = &dice789v1.Event{Event: &dice789v1.Event_PoolChanged{PoolChanged: &value}}
	case EventPenaltyRecordedMessage:
		var value dice789v1.PenaltyRecorded
		if err := unmarshalStrict(message.Payload, &value); err != nil || value.GetTurn() == 0 || value.GetUserId() == "" || value.GetSourceUserId() == "" || !validCause(value.GetCause(), false) {
			return projection.ReplayEvent{}, malformed("penalty.recorded event is invalid")
		}
		if value.GetAfterTotalTicks() < value.GetBeforeTotalTicks() || value.GetAfterTotalTicks()-value.GetBeforeTotalTicks() != value.GetTicks() {
			return projection.ReplayEvent{}, malformed("penalty.recorded cumulative ticks are inconsistent")
		}
		result, ok := resultFromProto(value.GetEffect(), engine.PhaseAwaitingContinue)
		if !ok || result != engine.ResultEight && result != engine.ResultNine && result != engine.ResultDoubleOne && result != engine.ResultDoubleFour && result != engine.ResultDropped {
			return projection.ReplayEvent{}, malformed("penalty.recorded effect is invalid")
		}
		cause = value.GetCause()
		fact = engine.Event{Kind: engine.EventPenaltyRecorded, Turn: value.GetTurn(), UserID: value.GetUserId(), SourceUserID: value.GetSourceUserId(), Result: result, PenaltyTicks: dice.Ticks(value.GetTicks()), PenaltyBeforeTicks: dice.Ticks(value.GetBeforeTotalTicks()), PenaltyAfterTicks: dice.Ticks(value.GetAfterTotalTicks()), Reason: value.GetReason()}
		protocolEvent = &dice789v1.Event{Event: &dice789v1.Event_PenaltyRecorded{PenaltyRecorded: &value}}
	case EventDirectionChangedMessage:
		var value dice789v1.DirectionChanged
		if err := unmarshalStrict(message.Payload, &value); err != nil || value.GetTurn() == 0 || value.GetSourceUserId() == "" || !validCause(value.GetCause(), false) {
			return projection.ReplayEvent{}, malformed("direction.changed event is invalid")
		}
		before, after := engine.Direction(value.GetBeforeDirection()), engine.Direction(value.GetDirection())
		if !before.Valid() || !after.Valid() || value.GetEffect() != dice789v1.Effect_EFFECT_ORDINARY_PAIR_REVERSE {
			return projection.ReplayEvent{}, malformed("direction.changed direction is invalid")
		}
		cause = value.GetCause()
		fact = engine.Event{Kind: engine.EventDirectionChanged, Turn: value.GetTurn(), UserID: value.GetSourceUserId(), SourceUserID: value.GetSourceUserId(), Result: engine.ResultOrdinaryPair, DirectionBefore: before, Direction: after}
		protocolEvent = &dice789v1.Event{Event: &dice789v1.Event_DirectionChanged{DirectionChanged: &value}}
	case EventTurnSettledMessage:
		var value dice789v1.TurnSettled
		if err := unmarshalStrict(message.Payload, &value); err != nil || value.GetTurn() == 0 || value.GetSourceUserId() == "" || value.GetNextUserId() == "" || value.GetReason() == "" || !validCause(value.GetCause(), false) || !validOutcome(value.GetOutcome(), false) {
			return projection.ReplayEvent{}, malformed("turn.settled event is invalid")
		}
		result, ok := resultFromProto(value.GetEffect(), engine.PhaseAwaitingRoll)
		if !ok {
			return projection.ReplayEvent{}, malformed("turn.settled effect is invalid")
		}
		if value.GetReason() == "roll_timeout" {
			result = engine.ResultRollTimeout
		} else if value.GetReason() == "cancelled" {
			result = engine.ResultCancelled
		}
		direction := engine.Direction(value.GetDirection())
		if !direction.Valid() {
			return projection.ReplayEvent{}, malformed("turn.settled direction is invalid")
		}
		cause, outcome = value.GetCause(), value.GetOutcome()
		fact = engine.Event{Kind: engine.EventTurnSettled, Turn: value.GetTurn(), UserID: value.GetSourceUserId(), SourceUserID: value.GetSourceUserId(), TargetUserID: value.GetTargetUserId(), NextUserID: value.GetNextUserId(), Direction: direction, Result: result, Reason: value.GetReason()}
		if value.GetOutcome() != outcomeToProto(settlementFromEvent(fact)) {
			return projection.ReplayEvent{}, malformed("turn.settled outcome is inconsistent")
		}
		protocolEvent = &dice789v1.Event{Event: &dice789v1.Event_TurnSettled{TurnSettled: &value}}
	case EventTurnDroppedMessage:
		var value dice789v1.TurnDropped
		if err := unmarshalStrict(message.Payload, &value); err != nil || value.GetTurn() == 0 || value.GetOperatorUserId() == "" || value.GetSourceUserId() == "" || value.GetUserId() != value.GetSourceUserId() || value.GetReason() == "" || value.GetAuditRef() == "" || value.GetCause() != dice789v1.ResolutionCause_RESOLUTION_CAUSE_DROP_REPORTED {
			return projection.ReplayEvent{}, malformed("turn.dropped_reported event is invalid")
		}
		cause = value.GetCause()
		fact = engine.Event{Kind: engine.EventTurnDropped, Turn: value.GetTurn(), UserID: value.GetOperatorUserId(), SourceUserID: value.GetSourceUserId(), Result: engine.ResultDropped, Reason: value.GetReason(), AuditRef: value.GetAuditRef()}
		protocolEvent = &dice789v1.Event{Event: &dice789v1.Event_TurnDropped{TurnDropped: &value}}
	case EventParticipantRevokedMessage:
		var value dice789v1.ParticipantRevoked
		if err := unmarshalStrict(message.Payload, &value); err != nil {
			return projection.ReplayEvent{}, malformed("participant.revoked event is invalid")
		}
		_, phaseValid := phaseFromProto(value.GetPhaseBefore())
		effectValid := value.GetEffect() == dice789v1.Effect_EFFECT_UNSPECIFIED
		if !effectValid {
			_, effectValid = resultFromProto(value.GetEffect(), engine.PhaseAwaitingRoll)
		}
		if value.GetTurn() == 0 || value.GetUserId() == "" || !phaseValid || !effectValid || !revocationEventValid(&value) {
			return projection.ReplayEvent{}, malformed("participant.revoked event is invalid")
		}
		phaseBefore := engine.Phase(0)
		if value.GetPhaseBefore() != dice789v1.Phase_PHASE_UNSPECIFIED {
			phaseBefore, _ = phaseFromProto(value.GetPhaseBefore())
		}
		result := engine.ResultNone
		if value.GetEffect() != dice789v1.Effect_EFFECT_UNSPECIFIED {
			result, _ = resultFromProto(value.GetEffect(), engine.PhaseAwaitingRoll)
		}
		fact = engine.Event{Kind: engine.EventParticipantRevoked, Turn: value.GetTurn(), UserID: value.GetUserId(), PhaseBefore: phaseBefore,
			Result: result, SourceUserID: value.GetSourceUserId(), TargetUserID: value.GetTargetUserId(), NextUserID: value.GetNextUserId(),
			PendingEffectCancelled: value.GetPendingEffectCancelled(), TargetSelectionReopened: value.GetTargetSelectionReopened()}
		protocolEvent = &dice789v1.Event{Event: &dice789v1.Event_ParticipantRevoked{ParticipantRevoked: &value}}
	case EventSessionFinishedMessage:
		var value dice789v1.SessionFinished
		if err := unmarshalStrict(message.Payload, &value); err != nil || value.GetTurn() == 0 || finishCause(value.GetReason()) == dice789v1.ResolutionCause_RESOLUTION_CAUSE_UNSPECIFIED || value.GetCause() != finishCause(value.GetReason()) || value.GetCause() == dice789v1.ResolutionCause_RESOLUTION_CAUSE_HOST_FINISHED && value.GetOperatorUserId() == "" || value.GetCause() != dice789v1.ResolutionCause_RESOLUTION_CAUSE_HOST_FINISHED && value.GetOperatorUserId() != "" {
			return projection.ReplayEvent{}, malformed("session.finished event is invalid")
		}
		cause = value.GetCause()
		fact = engine.Event{Kind: engine.EventSessionFinished, Turn: value.GetTurn(), Reason: value.GetReason(), OperatorUserID: value.GetOperatorUserId()}
		protocolEvent = &dice789v1.Event{Event: &dice789v1.Event_SessionFinished{SessionFinished: &value}}
	default:
		return projection.ReplayEvent{}, malformed("unknown event message type")
	}
	return projection.ReplayEvent{Event: fact, Initialization: initialization, Cause: cause, Outcome: outcome, ProtocolEvent: protocolEvent}, nil
}

func revocationEventValid(value *dice789v1.ParticipantRevoked) bool {
	phase := value.GetPhaseBefore()
	effect := value.GetEffect()
	source, target := value.GetSourceUserId(), value.GetTargetUserId()
	if source == "" || value.GetUserId() == "" || phase == dice789v1.Phase_PHASE_FINISHED {
		return false
	}
	switch phase {
	case dice789v1.Phase_PHASE_AWAITING_ROLL:
		if effect != dice789v1.Effect_EFFECT_UNSPECIFIED || target != "" || value.GetPendingEffectCancelled() || value.GetTargetSelectionReopened() {
			return false
		}
	case dice789v1.Phase_PHASE_RESULT_PENDING:
		if effect == dice789v1.Effect_EFFECT_UNSPECIFIED || target != "" || value.GetTargetSelectionReopened() {
			return false
		}
	case dice789v1.Phase_PHASE_AWAITING_ADD:
		if effect != dice789v1.Effect_EFFECT_SUM_SEVEN_ADD && effect != dice789v1.Effect_EFFECT_DOUBLE_SIX_TARGET_ADD ||
			effect == dice789v1.Effect_EFFECT_SUM_SEVEN_ADD && target != "" {
			return false
		}
	case dice789v1.Phase_PHASE_AWAITING_TARGET:
		if effect != dice789v1.Effect_EFFECT_DOUBLE_ONE_TARGET_DRAIN && effect != dice789v1.Effect_EFFECT_DOUBLE_SIX_TARGET_ADD || target != "" || value.GetTargetSelectionReopened() {
			return false
		}
	case dice789v1.Phase_PHASE_AWAITING_CONTINUE:
		if effect != dice789v1.Effect_EFFECT_SUM_SEVEN_ADD && effect != dice789v1.Effect_EFFECT_SUM_EIGHT_HALF_POOL && effect != dice789v1.Effect_EFFECT_SUM_NINE_DRAIN_POOL ||
			target != "" || value.GetPendingEffectCancelled() || value.GetTargetSelectionReopened() {
			return false
		}
	default:
		return false
	}
	if value.GetTargetSelectionReopened() {
		return phase == dice789v1.Phase_PHASE_AWAITING_ADD && effect == dice789v1.Effect_EFFECT_DOUBLE_SIX_TARGET_ADD && target == value.GetUserId() && !value.GetPendingEffectCancelled() && value.GetNextUserId() == ""
	}
	if value.GetPendingEffectCancelled() && phase != dice789v1.Phase_PHASE_RESULT_PENDING && phase != dice789v1.Phase_PHASE_AWAITING_ADD && phase != dice789v1.Phase_PHASE_AWAITING_TARGET {
		return false
	}
	return value.GetNextUserId() == "" || value.GetPendingEffectCancelled() || value.GetUserId() == source
}

func settlementFromEvent(value engine.Event) engine.TurnSettlement {
	return engine.TurnSettlement{
		Turn: value.Turn, SourceUserID: value.SourceUserID, DieOne: value.DieOne, DieTwo: value.DieTwo, Sum: value.Sum,
		Result: value.Result, TargetUserID: value.TargetUserID, PoolBeforeTicks: value.PoolBeforeTicks,
		PoolAfterTicks: value.PoolAfterTicks, PoolBefore: value.PoolBefore, PoolAfter: value.PoolAfter,
		EffectTicks: value.EffectTicks, PenaltyTicks: value.PenaltyTicks, DirectionBefore: value.DirectionBefore,
		DirectionAfter: value.Direction, NextUserID: value.NextUserID, Reason: value.Reason, AuditRef: value.AuditRef,
	}
}

func eventEffect(value engine.Event) dice789v1.Effect {
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
	return resultToProto(result, engine.State{})
}

func batchContainsDropped(values []engine.Event, turn uint32) bool {
	for _, value := range values {
		if value.Turn == turn && value.Kind == engine.EventTurnDropped {
			return true
		}
	}
	return false
}

// eventCause ties every fact in one atomic transition to the action or timer
// that resolved it, including target and follow-up timeout batches.
func eventCause(fact engine.Event, batch []engine.Event) dice789v1.ResolutionCause {
	resolutionTurn := fact.Turn
	if fact.Kind == engine.EventTurnStarted && resolutionTurn > 1 {
		resolutionTurn--
	}
	if batchContainsDropped(batch, resolutionTurn) {
		return dice789v1.ResolutionCause_RESOLUTION_CAUSE_DROP_REPORTED
	}
	for _, value := range batch {
		if value.Turn == resolutionTurn && value.Kind == engine.EventEffectSelected {
			return causeToProto(value.Reason)
		}
	}
	for _, value := range batch {
		if value.Turn == resolutionTurn && value.Kind == engine.EventTargetSelected {
			return causeToProto(value.Reason)
		}
	}
	for _, value := range batch {
		if value.Turn == resolutionTurn && value.Kind == engine.EventTurnSettled {
			return causeToProto(value.Reason)
		}
	}
	for _, value := range batch {
		if value.Turn == resolutionTurn && value.Kind == engine.EventParticipantRevoked {
			return dice789v1.ResolutionCause_RESOLUTION_CAUSE_PARTICIPANT_REVOKED
		}
	}
	return causeToProto(fact.Reason)
}

func revocationToProto(fact engine.Event, _ []engine.Event, state engine.State) *dice789v1.ParticipantRevoked {
	effect := eventEffect(fact)
	if fact.Result == engine.ResultOrdinaryPair && state.Config.OrdinaryPairsReverse && activePlayers(state)+1 >= 3 {
		effect = dice789v1.Effect_EFFECT_ORDINARY_PAIR_REVERSE
	}
	return &dice789v1.ParticipantRevoked{
		UserId: fact.UserID, Turn: fact.Turn, PhaseBefore: phaseToProto(fact.PhaseBefore), Effect: effect,
		SourceUserId: fact.SourceUserID, TargetUserId: fact.TargetUserID, NextUserId: fact.NextUserID,
		PendingEffectCancelled: fact.PendingEffectCancelled, TargetSelectionReopened: fact.TargetSelectionReopened,
	}
}

func activePlayers(state engine.State) int {
	count := 0
	for _, player := range state.Players {
		if player.Active {
			count++
		}
	}
	return count
}

func turnDice(turn uint32, facts []engine.Event, state engine.State) (uint32, uint32, uint32) {
	for _, fact := range facts {
		if fact.Turn == turn && validDice(fact.DieOne, fact.DieTwo, fact.Sum) {
			return fact.DieOne, fact.DieTwo, fact.Sum
		}
	}
	if state.Turn == turn && validDice(state.DieOne, state.DieTwo, state.Sum) {
		return state.DieOne, state.DieTwo, state.Sum
	}
	if state.LastSettlement.Turn == turn && validDice(state.LastSettlement.DieOne, state.LastSettlement.DieTwo, state.LastSettlement.Sum) {
		return state.LastSettlement.DieOne, state.LastSettlement.DieTwo, state.LastSettlement.Sum
	}
	return 0, 0, 0
}

func nextPhaseForEffect(value engine.Event, state engine.State) dice789v1.Phase {
	if state.Turn == value.Turn {
		return phaseToProto(state.Phase)
	}
	switch value.Result {
	case engine.ResultSeven:
		return dice789v1.Phase_PHASE_AWAITING_ADD
	case engine.ResultEight, engine.ResultNine:
		return dice789v1.Phase_PHASE_AWAITING_CONTINUE
	case engine.ResultDoubleOne, engine.ResultDoubleSix:
		return dice789v1.Phase_PHASE_AWAITING_TARGET
	default:
		return dice789v1.Phase_PHASE_AWAITING_ROLL
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

func playerPenalty(state engine.State, userID string) uint32 {
	for _, player := range state.Players {
		if player.UserID == userID {
			return uint32(player.PenaltyTicks)
		}
	}
	return 0
}

func poolTotal(values []engine.PoolLayer) dice.Ticks {
	var total dice.Ticks
	for _, value := range values {
		total += value.Ticks
	}
	return total
}

func validDice(one, two, sum uint32) bool {
	return one >= 1 && one <= 6 && two >= 1 && two <= 6 && sum == one+two
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

func causeReason(value dice789v1.ResolutionCause) string {
	switch value {
	case dice789v1.ResolutionCause_RESOLUTION_CAUSE_TIMEOUT:
		return "timeout"
	case dice789v1.ResolutionCause_RESOLUTION_CAUSE_DROP_REPORTED:
		return "dropped"
	case dice789v1.ResolutionCause_RESOLUTION_CAUSE_PARTICIPANT_REVOKED:
		return "participant_revoked"
	case dice789v1.ResolutionCause_RESOLUTION_CAUSE_HOST_CONFIRMED:
		return "confirmed"
	default:
		return "action"
	}
}

func validCause(value dice789v1.ResolutionCause, allowUnspecified bool) bool {
	switch value {
	case dice789v1.ResolutionCause_RESOLUTION_CAUSE_UNSPECIFIED:
		return allowUnspecified
	case dice789v1.ResolutionCause_RESOLUTION_CAUSE_PLAYER_ACTION,
		dice789v1.ResolutionCause_RESOLUTION_CAUSE_HOST_CONFIRMED,
		dice789v1.ResolutionCause_RESOLUTION_CAUSE_DROP_REPORTED,
		dice789v1.ResolutionCause_RESOLUTION_CAUSE_TIMEOUT,
		dice789v1.ResolutionCause_RESOLUTION_CAUSE_PARTICIPANT_REVOKED,
		dice789v1.ResolutionCause_RESOLUTION_CAUSE_HOST_FINISHED,
		dice789v1.ResolutionCause_RESOLUTION_CAUSE_INSUFFICIENT_PLAYERS,
		dice789v1.ResolutionCause_RESOLUTION_CAUSE_PLATFORM_CANCELLED:
		return true
	default:
		return false
	}
}

func validOutcome(value dice789v1.TurnOutcome, allowUnspecified bool) bool {
	switch value {
	case dice789v1.TurnOutcome_TURN_OUTCOME_UNSPECIFIED:
		return allowUnspecified
	case dice789v1.TurnOutcome_TURN_OUTCOME_PASS,
		dice789v1.TurnOutcome_TURN_OUTCOME_REROLL,
		dice789v1.TurnOutcome_TURN_OUTCOME_TARGET_TAKES_TURN,
		dice789v1.TurnOutcome_TURN_OUTCOME_SOURCE_REVOKED,
		dice789v1.TurnOutcome_TURN_OUTCOME_SESSION_FINISHED:
		return true
	default:
		return false
	}
}

func effectNextPhaseValid(effect dice789v1.Effect, phase dice789v1.Phase) bool {
	switch effect {
	case dice789v1.Effect_EFFECT_SUM_SEVEN_ADD:
		return phase == dice789v1.Phase_PHASE_AWAITING_ADD || phase == dice789v1.Phase_PHASE_AWAITING_CONTINUE
	case dice789v1.Effect_EFFECT_SUM_EIGHT_HALF_POOL, dice789v1.Effect_EFFECT_SUM_NINE_DRAIN_POOL:
		return phase == dice789v1.Phase_PHASE_AWAITING_CONTINUE
	case dice789v1.Effect_EFFECT_DOUBLE_ONE_TARGET_DRAIN, dice789v1.Effect_EFFECT_DOUBLE_SIX_TARGET_ADD:
		return phase == dice789v1.Phase_PHASE_AWAITING_TARGET
	case dice789v1.Effect_EFFECT_DOUBLE_FOUR_HALF_POOL_REROLL,
		dice789v1.Effect_EFFECT_ORDINARY_PAIR_REVERSE,
		dice789v1.Effect_EFFECT_ORDINARY_PAIR_REROLL,
		dice789v1.Effect_EFFECT_PASS:
		return phase == dice789v1.Phase_PHASE_AWAITING_ROLL
	default:
		return false
	}
}
