package module

import (
	"strings"

	"github.com/iFTY-R/game-night/games/meet-by-chance/engine"
	meetv1 "github.com/iFTY-R/game-night/games/meet-by-chance/gen/go/game/meet_by_chance/v1"
	"github.com/iFTY-R/game-night/games/meet-by-chance/projection"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"google.golang.org/protobuf/proto"
)

// encodeEvents expands engine facts into deterministic public leaf envelopes.
// A reveal may add one adjacent 235 evaluation, so output cardinality can be
// larger than the engine fact batch while preserving its causal order.
func encodeEvents(facts []engine.Event, state engine.State) ([]game.Event, error) {
	if len(facts) == 0 {
		return nil, malformed("transition has no engine events")
	}
	events := make([]game.Event, 0, len(facts)+2)
	for _, fact := range facts {
		message, err := encodeEvent(fact, facts, state)
		if err != nil {
			return nil, err
		}
		events = append(events, game.Event{Message: message})
		if fact.Kind != engine.EventDiceRevealed {
			continue
		}
		special, ok, err := encodeSpecial235(fact, facts)
		if err != nil {
			return nil, err
		}
		if ok {
			events = append(events, game.Event{Message: special})
		}
	}
	return events, nil
}

func encodeEvent(fact engine.Event, batch []engine.Event, state engine.State) (game.Message, error) {
	var messageType game.Identifier
	var message proto.Message
	switch fact.Kind {
	case engine.EventRoundStarted:
		value, err := roundStartedToProto(fact, batch, state)
		if err != nil {
			return game.Message{}, err
		}
		messageType, message = EventRoundStartedMessage, value
	case engine.EventDiceRevealed:
		value, err := diceRevealedToProto(fact)
		if err != nil {
			return game.Message{}, err
		}
		messageType, message = EventDiceRevealedMessage, value
	case engine.EventHandClassified:
		if fact.Round == 0 || fact.UserID == "" || fact.Hand.FullKey.Length == 0 {
			return game.Message{}, malformed("hand classification event is incomplete")
		}
		messageType, message = EventHandClassifiedMessage, &meetv1.HandClassified{
			UserId: fact.UserID, HandClass: handClassToProto(fact.Hand.Class), Round: fact.Round, BatchIndex: fact.Batch,
			Dice: facesToProto(fact.Hand.Raw[:]), NormalizedDice: facesToProto(fact.Hand.Normalized[:]),
			Special_235: fact.Hand.Special235, Special_235Outcome: specialOutcomeToProto(fact.Hand.SpecialContext),
		}
	case engine.EventMatchResolved:
		value, err := matchResolvedToProto(fact, state.Config, state.Players)
		if err != nil {
			return game.Message{}, err
		}
		messageType, message = EventMatchResolvedMessage, value
	case engine.EventTargetSelected:
		cause := eventCause(fact, batch)
		if fact.Round == 0 || fact.UserID == "" || cause == meetv1.ResolutionCause_RESOLUTION_CAUSE_UNSPECIFIED {
			return game.Message{}, malformed("target selection event is incomplete")
		}
		messageType, message = EventTargetSelectedMessage, &meetv1.TargetSelected{
			UserId: fact.UserID, PenaltyTicks: uint32(fact.PenaltyTicks), Round: fact.Round,
			PreviousUserId: fact.PreviousUserID, FirstSelectionThisRound: fact.FirstSelectionThisRound,
			TargetRerollCount: fact.TargetRerollCount, TargetStreak: fact.TargetStreak,
			MatchResolutionCount: fact.MatchResolutionCount, Cause: cause,
		}
	case engine.EventTargetRerolled:
		if fact.Round == 0 || fact.UserID == "" || fact.TargetRerollCount == 0 || fact.Reason != "target_reroll" {
			return game.Message{}, malformed("target reroll event is incomplete")
		}
		messageType, message = EventTargetRerolledMessage, &meetv1.TargetRerolled{
			UserId: fact.UserID, Count: fact.TargetRerollCount, Round: fact.Round,
			TargetStreak: fact.TargetStreak, PenaltyTicks: uint32(fact.PenaltyTicks),
			MatchResolutionCount: fact.MatchResolutionCount,
			Cause:                meetv1.ResolutionCause_RESOLUTION_CAUSE_PLAYER_REROLL,
		}
	case engine.EventPenaltyRecorded:
		cause := eventCause(fact, batch)
		if fact.Round == 0 || fact.UserID == "" || fact.Reason == "" || cause == meetv1.ResolutionCause_RESOLUTION_CAUSE_UNSPECIFIED ||
			fact.PenaltyAfterTicks < fact.PenaltyBeforeTicks || fact.PenaltyAfterTicks-fact.PenaltyBeforeTicks != fact.PenaltyTicks {
			return game.Message{}, malformed("penalty event is incomplete")
		}
		messageType, message = EventPenaltyRecordedMessage, &meetv1.PenaltyRecorded{
			UserId: fact.UserID, Ticks: uint32(fact.PenaltyTicks), Reason: fact.Reason,
			BeforeTotalTicks: uint32(fact.PenaltyBeforeTicks), AfterTotalTicks: uint32(fact.PenaltyAfterTicks),
			Round: fact.Round, BatchIndex: fact.Batch, Cause: cause,
		}
	case engine.EventParticipantRevoked:
		if fact.Round == 0 || fact.UserID == "" || fact.ActivePlayerCount >= uint32(len(state.Players)) ||
			!fact.RoundCancelled && fact.NextRound != 0 || fact.NextRound != 0 && fact.NextRound != fact.Round+1 {
			return game.Message{}, malformed("participant revocation event is incomplete")
		}
		messageType, message = EventParticipantRevokedMessage, &meetv1.ParticipantRevoked{
			UserId: fact.UserID, Round: fact.Round, PhaseBefore: meetv1.Phase_PHASE_TARGET_DECISION,
			WasTarget: fact.RoundCancelled, RoundCancelled: fact.RoundCancelled,
			NextRound: fact.NextRound, ActivePlayerCount: fact.ActivePlayerCount,
		}
	case engine.EventSessionFinished:
		cause := causeToProto(fact.Reason)
		if fact.Round == 0 || cause == meetv1.ResolutionCause_RESOLUTION_CAUSE_UNSPECIFIED ||
			cause == meetv1.ResolutionCause_RESOLUTION_CAUSE_HOST_FINISHED && fact.OperatorUserID == "" ||
			cause != meetv1.ResolutionCause_RESOLUTION_CAUSE_HOST_FINISHED && fact.OperatorUserID != "" {
			return game.Message{}, malformed("session finish event is incomplete")
		}
		messageType, message = EventSessionFinishedMessage, &meetv1.SessionFinished{
			Reason: fact.Reason, Round: fact.Round, OperatorUserId: fact.OperatorUserID, Cause: cause,
		}
	case engine.EventRoundSettled:
		if fact.Round == 0 || fact.Settlement.Round != fact.Round || fact.Settlement.TargetUserID != fact.UserID || fact.Settlement.Reason != fact.Reason {
			return game.Message{}, malformed("round settlement event is incomplete")
		}
		messageType, message = EventRoundSettledMessage, &meetv1.RoundSettled{Summary: settlementToProto(fact.Settlement)}
	default:
		return game.Message{}, malformed("unknown engine event")
	}
	payload, err := marshalDeterministic(message)
	if err != nil {
		return game.Message{}, malformed("event encoding failed")
	}
	return game.Message{MessageType: messageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload}, nil
}

func roundStartedToProto(fact engine.Event, batch []engine.Event, state engine.State) (*meetv1.RoundStarted, error) {
	if fact.Round == 0 {
		return nil, malformed("round start event is incomplete")
	}
	cause, previousOutcome := roundStartContext(fact, batch)
	value := &meetv1.RoundStarted{Round: fact.Round, Cause: cause, PreviousOutcome: previousOutcome}
	if fact.Round == 1 {
		if fact.InitialConfig == nil || len(fact.InitialParticipants) < engine.MinimumPlayers || state.HostUserID == "" {
			return nil, malformed("round one lacks initialization facts")
		}
		value.Config = configToProto(*fact.InitialConfig)
		value.HostUserId = state.HostUserID
		value.Players = make([]*meetv1.ReplayPlayer, len(fact.InitialParticipants))
		hostFound := false
		for index, participant := range fact.InitialParticipants {
			value.Players[index] = &meetv1.ReplayPlayer{UserId: participant.UserID, SeatIndex: participant.SeatIndex}
			hostFound = hostFound || participant.UserID == state.HostUserID
		}
		if !hostFound || engine.ValidateParticipants(fact.InitialParticipants) != nil || fact.InitialConfig.Validate(len(fact.InitialParticipants)) != nil {
			return nil, malformed("round one initialization facts are invalid")
		}
	} else if fact.InitialConfig != nil || len(fact.InitialParticipants) != 0 {
		return nil, malformed("later round repeats initialization facts")
	}
	if cause == meetv1.ResolutionCause_RESOLUTION_CAUSE_UNSPECIFIED ||
		(fact.Round == 1) != (previousOutcome == meetv1.RoundOutcome_ROUND_OUTCOME_UNSPECIFIED) {
		return nil, malformed("round start cause is incomplete")
	}
	return value, nil
}

func roundStartContext(fact engine.Event, batch []engine.Event) (meetv1.ResolutionCause, meetv1.RoundOutcome) {
	if fact.Round == 1 {
		return meetv1.ResolutionCause_RESOLUTION_CAUSE_INITIAL_ROLL, meetv1.RoundOutcome_ROUND_OUTCOME_UNSPECIFIED
	}
	for _, item := range batch {
		if item.Kind == engine.EventRoundSettled && item.Round+1 == fact.Round {
			return causeToProto(item.Reason), outcomeToProto(item.Reason)
		}
		if item.Kind == engine.EventParticipantRevoked && item.RoundCancelled && item.NextRound == fact.Round {
			return meetv1.ResolutionCause_RESOLUTION_CAUSE_TARGET_REVOKED, meetv1.RoundOutcome_ROUND_OUTCOME_TARGET_REVOKED
		}
	}
	return meetv1.ResolutionCause_RESOLUTION_CAUSE_UNSPECIFIED, meetv1.RoundOutcome_ROUND_OUTCOME_UNSPECIFIED
}

func diceRevealedToProto(fact engine.Event) (*meetv1.DiceRevealed, error) {
	cause := causeToProto(fact.Reason)
	if fact.Round == 0 || len(fact.Players) == 0 || cause == meetv1.ResolutionCause_RESOLUTION_CAUSE_UNSPECIFIED {
		return nil, malformed("dice reveal event is incomplete")
	}
	value := &meetv1.DiceRevealed{Round: fact.Round, BatchIndex: fact.Batch, Cause: cause}
	for _, player := range fact.Players {
		if player.UserID == "" {
			return nil, malformed("dice reveal contains an invalid player")
		}
		value.PublicDice = append(value.PublicDice, &meetv1.PublicDice{
			UserId: player.UserID, SeatIndex: player.SeatIndex, Dice: facesToProto(player.Hand.Raw[:]),
		})
		value.RolledUserIds = append(value.RolledUserIds, player.UserID)
	}
	return value, nil
}

func matchResolvedToProto(fact engine.Event, config engine.Config, players []engine.PlayerState) (*meetv1.MatchResolved, error) {
	cause := eventCause(fact, nil)
	if fact.Round == 0 || len(fact.MatchGroups) == 0 || fact.Batch != fact.MatchResolutionCount || cause == meetv1.ResolutionCause_RESOLUTION_CAUSE_UNSPECIFIED {
		return nil, malformed("match resolution event is incomplete")
	}
	resolution := engine.MatchResolution{Batch: fact.Batch, Groups: fact.MatchGroups, Capped: fact.Capped}
	batch := matchBatchToProto(fact.Round, resolution, config, players)
	if !fact.Capped && fact.PenaltyTicks != config.MatchPenaltyTicks {
		return nil, malformed("match resolution penalty differs from frozen config")
	}
	return &meetv1.MatchResolved{Round: fact.Round, Batch: batch, Cause: cause}, nil
}

func encodeSpecial235(reveal engine.Event, batch []engine.Event) (game.Message, bool, error) {
	classified := make([]engine.Event, 0)
	for _, fact := range batch {
		if fact.Kind == engine.EventHandClassified && fact.Round == reveal.Round && fact.Batch == reveal.Batch {
			classified = append(classified, fact)
		}
	}
	specialUsers := make([]string, 0)
	allOthersLeopards := true
	outcome := meetv1.Special235Outcome_SPECIAL235_OUTCOME_UNSPECIFIED
	for _, fact := range classified {
		if fact.Hand.Special235 {
			specialUsers = append(specialUsers, fact.UserID)
			current := specialOutcomeToProto(fact.Hand.SpecialContext)
			if outcome != meetv1.Special235Outcome_SPECIAL235_OUTCOME_UNSPECIFIED && outcome != current {
				return game.Message{}, false, malformed("235 evaluation outcomes disagree")
			}
			outcome = current
			continue
		}
		allOthersLeopards = allOthersLeopards && fact.Hand.Class == engine.HandLeopard
	}
	if len(specialUsers) == 0 {
		return game.Message{}, false, nil
	}
	allOthersLeopards = allOthersLeopards && len(specialUsers) == 1
	if (outcome == meetv1.Special235Outcome_SPECIAL235_OUTCOME_BEATS_LEOPARDS) != allOthersLeopards {
		return game.Message{}, false, malformed("235 evaluation context is inconsistent")
	}
	payload, err := marshalDeterministic(&meetv1.Special235Evaluated{
		Round: reveal.Round, BatchIndex: reveal.Batch, SpecialUserIds: specialUsers,
		AllOtherPlayersAreLeopards: allOthersLeopards, Outcome: outcome,
	})
	if err != nil {
		return game.Message{}, false, malformed("235 evaluation encoding failed")
	}
	return game.Message{MessageType: EventSpecial235Message, SchemaVersion: ProtocolSchemaVersion, Payload: payload}, true, nil
}

// eventCause ties derivative penalty and match facts back to the trusted input
// that initiated their atomic transition.
func eventCause(fact engine.Event, batch []engine.Event) meetv1.ResolutionCause {
	if cause := causeToProto(fact.Reason); cause != meetv1.ResolutionCause_RESOLUTION_CAUSE_UNSPECIFIED {
		return cause
	}
	if strings.HasPrefix(fact.Reason, "match_") {
		return meetv1.ResolutionCause_RESOLUTION_CAUSE_MATCH_REROLL
	}
	if fact.Reason == "target_selected" {
		for _, item := range batch {
			if item.Kind == engine.EventTargetSelected && item.Round == fact.Round && item.UserID == fact.UserID {
				return eventCause(item, nil)
			}
		}
	}
	return meetv1.ResolutionCause_RESOLUTION_CAUSE_UNSPECIFIED
}

// validateVersionedEvents fails closed on malformed leaf payloads before a
// current view is wrapped as a subscription replacement.
func validateVersionedEvents(events []game.VersionedEvent) error {
	var lastVersion uint64
	for _, event := range events {
		if !event.Valid() || lastVersion > event.StateVersion || event.Event.Message.SchemaVersion != ProtocolSchemaVersion {
			return malformed("versioned event ordering is invalid")
		}
		lastVersion = event.StateVersion
		if err := projection.ValidateEvent(event.Event.Message); err != nil {
			return err
		}
	}
	return nil
}
