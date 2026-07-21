package projection

import (
	"slices"

	"github.com/iFTY-R/game-night/games/meet-by-chance/engine"
	meetv1 "github.com/iFTY-R/game-night/games/meet-by-chance/gen/go/game/meet_by_chance/v1"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"github.com/iFTY-R/game-night/sdk/go/game/dice"
	"google.golang.org/protobuf/proto"
)

const (
	EventRoundStarted       game.Identifier = "round.started"
	EventDiceRevealed       game.Identifier = "dice.revealed"
	EventHandClassified     game.Identifier = "hand.classified"
	EventMatchResolved      game.Identifier = "match.resolved"
	EventTargetSelected     game.Identifier = "target.selected"
	EventTargetRerolled     game.Identifier = "target.rerolled"
	EventPenaltyRecorded    game.Identifier = "penalty.recorded"
	EventParticipantRevoked game.Identifier = "participant.revoked"
	EventSessionFinished    game.Identifier = "session.finished"
	EventSpecial235         game.Identifier = "special235.evaluated"
	EventRoundSettled       game.Identifier = "round.settled"
)

// ValidateEvent strictly validates one public leaf envelope without applying
// replay lifecycle state. Live event projection uses this to reject legacy or
// non-canonical payloads before wrapping the current safe view.
func ValidateEvent(message game.Message) error {
	if !message.Valid() || message.SchemaVersion != engine.CurrentSchemaVersion {
		return projectionError("event envelope is invalid")
	}
	_, _, _, _, _, err := decodeReplayEvent(message)
	return err
}

// BuildReplay validates public event envelopes and appends only fully settled
// rounds. Events from a cancelled or still-pending round are intentionally
// discarded from the replay artifact.
func BuildReplay(events []game.Event, viewer game.Viewer, policy game.ReplayAccessPolicy) (*meetv1.Replay, error) {
	if len(events) == 0 || !viewer.Valid() || viewer.Kind != game.ViewerReplay || !policy.Valid() {
		return nil, projectionError("replay request is invalid")
	}
	replay := &meetv1.Replay{SchemaVersion: engine.CurrentSchemaVersion}
	var current []*meetv1.ReplayEntry
	var currentRound uint32
	var lastStarted uint32
	var hostUserID string
	var expectedCause meetv1.ResolutionCause
	var expectedOutcome meetv1.RoundOutcome
	sequence := uint64(0)
	started, finished := false, false
	semantic := &replaySemanticReducer{}
	for _, raw := range events {
		if finished || !raw.Valid() || raw.Message.SchemaVersion != engine.CurrentSchemaVersion {
			return nil, projectionError("replay event envelope is invalid")
		}
		decoded, round, summary, start, finishReason, err := decodeReplayEvent(raw.Message)
		if err != nil {
			return nil, err
		}
		if err := semantic.apply(decoded); err != nil {
			return nil, err
		}
		sequence++
		entry := &meetv1.ReplayEntry{Sequence: sequence, Event: decoded}
		if start != nil {
			if round == 0 || started && round != lastStarted+1 {
				return nil, projectionError("round.started does not advance replay lifecycle")
			}
			if !started {
				if round != 1 || start.GetCause() != meetv1.ResolutionCause_RESOLUTION_CAUSE_INITIAL_ROLL ||
					start.GetPreviousOutcome() != meetv1.RoundOutcome_ROUND_OUTCOME_UNSPECIFIED || start.GetConfig() == nil || len(start.GetPlayers()) < engine.MinimumPlayers {
					return nil, projectionError("round one lacks replay initialization")
				}
				config, participants, host, initErr := validateReplayInitialization(start)
				if initErr != nil {
					return nil, initErr
				}
				replay.Config = configToProto(config)
				replay.Players = participants
				hostUserID = host
				started = true
			} else if start.GetConfig() != nil || len(start.GetPlayers()) != 0 || start.GetHostUserId() != "" || hostUserID == "" ||
				start.GetCause() != expectedCause || start.GetPreviousOutcome() != expectedOutcome {
				return nil, projectionError("later round repeats replay initialization")
			}
			// Starting a new round after target revocation discards the cancelled accumulator.
			current = []*meetv1.ReplayEntry{entry}
			currentRound, lastStarted = round, round
			continue
		}
		if !started || currentRound == 0 || round != 0 && round != currentRound {
			return nil, projectionError("event is outside the active replay round")
		}
		current = append(current, entry)
		if revoked := decoded.GetParticipantRevoked(); revoked != nil && revoked.GetRoundCancelled() {
			expectedCause = meetv1.ResolutionCause_RESOLUTION_CAUSE_TARGET_REVOKED
			expectedOutcome = meetv1.RoundOutcome_ROUND_OUTCOME_TARGET_REVOKED
		}
		if summary != nil {
			if summary.GetRound() != currentRound {
				return nil, projectionError("round settlement targets another round")
			}
			if summary.GetSettled() {
				entries := cloneReplayEntries(current)
				replay.Entries = append(replay.Entries, entries...)
				replay.Rounds = append(replay.Rounds, &meetv1.ReplayRound{Summary: proto.Clone(summary).(*meetv1.RoundSummary)})
			}
			expectedCause, expectedOutcome = summary.GetCause(), summary.GetOutcome()
			current, currentRound = nil, 0
		}
		if finishReason != "" {
			replay.FinishReason = finishReason
			current, currentRound = nil, 0
			finished = true
		}
	}
	if !semantic.complete() {
		return nil, projectionError("replay stream ends outside a committed decision boundary")
	}
	return replay, nil
}

func decodeReplayEvent(message game.Message) (*meetv1.Event, uint32, *meetv1.RoundSummary, *meetv1.RoundStarted, string, error) {
	switch message.MessageType {
	case EventRoundStarted:
		var value meetv1.RoundStarted
		if err := unmarshalStrict(message.Payload, &value); err != nil || value.GetRound() == 0 || value.GetCause() == meetv1.ResolutionCause_RESOLUTION_CAUSE_UNSPECIFIED {
			return nil, 0, nil, nil, "", projectionError("round.started event is invalid")
		}
		return &meetv1.Event{Event: &meetv1.Event_RoundStarted{RoundStarted: &value}}, value.GetRound(), nil, &value, "", nil
	case EventDiceRevealed:
		var value meetv1.DiceRevealed
		if err := unmarshalStrict(message.Payload, &value); err != nil || len(value.GetPlayers()) != 0 || value.GetRound() == 0 || !publicDiceValid(value.GetPublicDice(), value.GetRolledUserIds()) || value.GetCause() == meetv1.ResolutionCause_RESOLUTION_CAUSE_UNSPECIFIED {
			return nil, 0, nil, nil, "", projectionError("dice.revealed event is invalid")
		}
		return &meetv1.Event{Event: &meetv1.Event_DiceRevealed{DiceRevealed: &value}}, value.GetRound(), nil, nil, "", nil
	case EventHandClassified:
		var value meetv1.HandClassified
		if err := unmarshalStrict(message.Payload, &value); err != nil || !classifiedHandValid(&value) {
			return nil, 0, nil, nil, "", projectionError("hand.classified event is invalid")
		}
		return &meetv1.Event{Event: &meetv1.Event_HandClassified{HandClassified: &value}}, value.GetRound(), nil, nil, "", nil
	case EventMatchResolved:
		var value meetv1.MatchResolved
		if err := unmarshalStrict(message.Payload, &value); err != nil || len(value.GetUserIds()) != 0 || value.GetKind() != "" || value.GetPenaltyTicks() != 0 || !matchEventValid(&value) {
			return nil, 0, nil, nil, "", projectionError("match.resolved event is invalid")
		}
		return &meetv1.Event{Event: &meetv1.Event_MatchResolved{MatchResolved: &value}}, value.GetRound(), nil, nil, "", nil
	case EventTargetSelected:
		var value meetv1.TargetSelected
		if err := unmarshalStrict(message.Payload, &value); err != nil || value.GetUserId() == "" || value.GetRound() == 0 || value.GetTargetStreak() > value.GetTargetRerollCount() ||
			(value.GetCause() != meetv1.ResolutionCause_RESOLUTION_CAUSE_INITIAL_ROLL && value.GetCause() != meetv1.ResolutionCause_RESOLUTION_CAUSE_PLAYER_REROLL) {
			return nil, 0, nil, nil, "", projectionError("target.selected event is invalid")
		}
		return &meetv1.Event{Event: &meetv1.Event_TargetSelected{TargetSelected: &value}}, value.GetRound(), nil, nil, "", nil
	case EventTargetRerolled:
		var value meetv1.TargetRerolled
		if err := unmarshalStrict(message.Payload, &value); err != nil || value.GetUserId() == "" || value.GetRound() == 0 || value.GetCount() == 0 || value.GetTargetStreak() == 0 || value.GetTargetStreak() > value.GetCount() || value.GetCause() != meetv1.ResolutionCause_RESOLUTION_CAUSE_PLAYER_REROLL {
			return nil, 0, nil, nil, "", projectionError("target.rerolled event is invalid")
		}
		return &meetv1.Event{Event: &meetv1.Event_TargetRerolled{TargetRerolled: &value}}, value.GetRound(), nil, nil, "", nil
	case EventPenaltyRecorded:
		var value meetv1.PenaltyRecorded
		if err := unmarshalStrict(message.Payload, &value); err != nil || value.GetUserId() == "" || value.GetRound() == 0 || value.GetReason() == "" || value.GetCause() == meetv1.ResolutionCause_RESOLUTION_CAUSE_UNSPECIFIED || value.GetAfterTotalTicks() < value.GetBeforeTotalTicks() || value.GetAfterTotalTicks()-value.GetBeforeTotalTicks() != value.GetTicks() {
			return nil, 0, nil, nil, "", projectionError("penalty.recorded event is invalid")
		}
		return &meetv1.Event{Event: &meetv1.Event_PenaltyRecorded{PenaltyRecorded: &value}}, value.GetRound(), nil, nil, "", nil
	case EventParticipantRevoked:
		var value meetv1.ParticipantRevoked
		if err := unmarshalStrict(message.Payload, &value); err != nil || !revocationEventValid(&value) {
			return nil, 0, nil, nil, "", projectionError("participant.revoked event is invalid")
		}
		return &meetv1.Event{Event: &meetv1.Event_ParticipantRevoked{ParticipantRevoked: &value}}, value.GetRound(), nil, nil, "", nil
	case EventSessionFinished:
		var value meetv1.SessionFinished
		if err := unmarshalStrict(message.Payload, &value); err != nil || !finishEventValid(&value) {
			return nil, 0, nil, nil, "", projectionError("session.finished event is invalid")
		}
		return &meetv1.Event{Event: &meetv1.Event_SessionFinished{SessionFinished: &value}}, value.GetRound(), nil, nil, value.GetReason(), nil
	case EventSpecial235:
		var value meetv1.Special235Evaluated
		if err := unmarshalStrict(message.Payload, &value); err != nil || !special235EventValid(&value) {
			return nil, 0, nil, nil, "", projectionError("special235.evaluated event is invalid")
		}
		return &meetv1.Event{Event: &meetv1.Event_Special_235Evaluated{Special_235Evaluated: &value}}, value.GetRound(), nil, nil, "", nil
	case EventRoundSettled:
		var value meetv1.RoundSettled
		if err := unmarshalStrict(message.Payload, &value); err != nil || !roundSummaryValid(value.GetSummary()) {
			return nil, 0, nil, nil, "", projectionError("round.settled event is invalid")
		}
		return &meetv1.Event{Event: &meetv1.Event_RoundSettled{RoundSettled: &value}}, value.GetSummary().GetRound(), value.GetSummary(), nil, "", nil
	default:
		return nil, 0, nil, nil, "", projectionError("unknown event message type")
	}
}

func validateReplayInitialization(value *meetv1.RoundStarted) (engine.Config, []*meetv1.ReplayPlayer, string, error) {
	config := engine.Config{
		Straight123: value.GetConfig().GetStraight_123(), Straight234: value.GetConfig().GetStraight_234(),
		Straight345: value.GetConfig().GetStraight_345(), Straight456: value.GetConfig().GetStraight_456(),
		Special235Enabled: value.GetConfig().GetSpecial_235Enabled(), OnesWild: value.GetConfig().GetOnesWild(),
		TargetPenaltyTicks: dice.Ticks(value.GetConfig().GetTargetPenaltyTicks()), RerollPenaltyTicks: dice.Ticks(value.GetConfig().GetRerollPenaltyTicks()),
		MatchPenaltyTicks: dice.Ticks(value.GetConfig().GetMatchPenaltyTicks()), WeakExtraPenaltyTicks: dice.Ticks(value.GetConfig().GetWeakExtraPenaltyTicks()),
		TargetRerollLimit: value.GetConfig().GetTargetRerollLimit(), MatchResolutionLimit: value.GetConfig().GetMatchResolutionLimit(),
		ActionTimeoutSeconds: value.GetConfig().GetActionTimeoutSeconds(),
	}
	participants := make([]engine.Participant, len(value.GetPlayers()))
	players := make([]*meetv1.ReplayPlayer, len(value.GetPlayers()))
	for index, player := range value.GetPlayers() {
		if player == nil || index > 0 && value.GetPlayers()[index-1].GetSeatIndex() >= player.GetSeatIndex() {
			return engine.Config{}, nil, "", projectionError("replay initialization contains a nil player")
		}
		participants[index] = engine.Participant{UserID: player.GetUserId(), SeatIndex: player.GetSeatIndex()}
		players[index] = proto.Clone(player).(*meetv1.ReplayPlayer)
	}
	hostUserID := value.GetHostUserId()
	hostFound := false
	for _, participant := range participants {
		hostFound = hostFound || participant.UserID == hostUserID
	}
	if err := engine.ValidateParticipants(participants); err != nil || config.Validate(len(participants)) != nil || !hostFound {
		return engine.Config{}, nil, "", projectionError("replay initialization is invalid")
	}
	return config, players, hostUserID, nil
}

// publicDiceValid keeps reveal events limited to raw public facts and binds the
// explicit roll list to the same deterministic seat-ordered payload.
func publicDiceValid(values []*meetv1.PublicDice, rolledUserIDs []string) bool {
	if len(values) == 0 || len(values) != len(rolledUserIDs) {
		return false
	}
	users := make(map[string]struct{}, len(values))
	seats := make(map[uint32]struct{}, len(values))
	for index, value := range values {
		if value == nil || value.GetUserId() == "" || value.GetUserId() != rolledUserIDs[index] || len(value.GetDice()) != int(engine.DicePerPlayer) {
			return false
		}
		if _, duplicate := users[value.GetUserId()]; duplicate {
			return false
		}
		if _, duplicate := seats[value.GetSeatIndex()]; duplicate {
			return false
		}
		if index > 0 && values[index-1].GetSeatIndex() >= value.GetSeatIndex() {
			return false
		}
		for _, face := range value.GetDice() {
			if !dice.Face(face).Valid() {
				return false
			}
		}
		users[value.GetUserId()] = struct{}{}
		seats[value.GetSeatIndex()] = struct{}{}
	}
	return true
}

func classifiedHandValid(value *meetv1.HandClassified) bool {
	if value == nil || value.GetUserId() == "" || value.GetRound() == 0 || len(value.GetFullKey()) != 0 || len(value.GetTieKey()) != 0 ||
		!facesValid(value.GetDice(), false) || !facesValid(value.GetNormalizedDice(), true) {
		return false
	}
	class := value.GetHandClass()
	special := value.GetSpecial_235()
	outcome := value.GetSpecial_235Outcome()
	classValid := class >= meetv1.HandClass_HAND_CLASS_SINGLE && class <= meetv1.HandClass_HAND_CLASS_SPECIAL_235
	if !classValid || special != (class == meetv1.HandClass_HAND_CLASS_SPECIAL_235) {
		return false
	}
	if special {
		return outcome == meetv1.Special235Outcome_SPECIAL235_OUTCOME_BEATS_LEOPARDS || outcome == meetv1.Special235Outcome_SPECIAL235_OUTCOME_MINIMUM_SINGLE
	}
	return outcome == meetv1.Special235Outcome_SPECIAL235_OUTCOME_NOT_APPLICABLE
}

func matchEventValid(value *meetv1.MatchResolved) bool {
	if value == nil || value.GetRound() == 0 || value.GetCause() != meetv1.ResolutionCause_RESOLUTION_CAUSE_MATCH_REROLL || value.GetBatch() == nil {
		return false
	}
	batch := value.GetBatch()
	if batch.GetRound() != value.GetRound() || batch.GetBatchIndex() != batch.GetResolutionCount() || len(batch.GetGroups()) == 0 {
		return false
	}
	seen := make(map[string]struct{})
	rerollSet := make(map[string]struct{})
	for _, group := range batch.GetGroups() {
		if group == nil || len(group.GetUserIds()) < 2 || group.GetKind() < meetv1.MatchKind_MATCH_KIND_EXACT || group.GetKind() > meetv1.MatchKind_MATCH_KIND_LOWEST {
			return false
		}
		for _, userID := range group.GetUserIds() {
			if userID == "" {
				return false
			}
			if _, duplicate := seen[userID]; duplicate {
				return false
			}
			seen[userID] = struct{}{}
		}
		if group.GetKind() == meetv1.MatchKind_MATCH_KIND_LOWEST {
			if !slices.Contains(group.GetUserIds(), group.GetWeakestUserId()) {
				return false
			}
		} else if group.GetWeakestUserId() != "" || group.GetWeakExtraPenaltyTicks() != 0 {
			return false
		}
		if batch.GetCapped() {
			if group.GetPenaltyTicks() != 0 || group.GetWeakExtraPenaltyTicks() != 0 {
				return false
			}
		} else {
			for _, userID := range group.GetUserIds() {
				rerollSet[userID] = struct{}{}
			}
		}
	}
	if batch.GetCapped() {
		return len(batch.GetRerolledUserIds()) == 0
	}
	if len(batch.GetRerolledUserIds()) != len(rerollSet) {
		return false
	}
	for _, userID := range batch.GetRerolledUserIds() {
		if _, ok := rerollSet[userID]; !ok {
			return false
		}
		delete(rerollSet, userID)
	}
	return len(rerollSet) == 0
}

func revocationEventValid(value *meetv1.ParticipantRevoked) bool {
	if value == nil || value.GetUserId() == "" || value.GetRound() == 0 || value.GetPhaseBefore() != meetv1.Phase_PHASE_TARGET_DECISION ||
		value.GetWasTarget() != value.GetRoundCancelled() || !value.GetRoundCancelled() && value.GetNextRound() != 0 {
		return false
	}
	return value.GetNextRound() == 0 || value.GetNextRound() == value.GetRound()+1
}

func finishEventValid(value *meetv1.SessionFinished) bool {
	if value == nil || value.GetRound() == 0 || value.GetReason() == "" {
		return false
	}
	switch value.GetCause() {
	case meetv1.ResolutionCause_RESOLUTION_CAUSE_HOST_FINISHED:
		_, err := game.ParseIdentifier(value.GetOperatorUserId())
		return value.GetReason() == engine.FinishHostRequested && err == nil
	case meetv1.ResolutionCause_RESOLUTION_CAUSE_INSUFFICIENT_PLAYERS:
		return value.GetReason() == engine.FinishInsufficientParticipants && value.GetOperatorUserId() == ""
	case meetv1.ResolutionCause_RESOLUTION_CAUSE_PLATFORM_CANCELLED:
		return value.GetReason() == engine.FinishPlatformCancelled && value.GetOperatorUserId() == ""
	default:
		return false
	}
}

func special235EventValid(value *meetv1.Special235Evaluated) bool {
	if value == nil || value.GetRound() == 0 || len(value.GetSpecialUserIds()) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(value.GetSpecialUserIds()))
	for _, userID := range value.GetSpecialUserIds() {
		if userID == "" {
			return false
		}
		if _, duplicate := seen[userID]; duplicate {
			return false
		}
		seen[userID] = struct{}{}
	}
	if value.GetAllOtherPlayersAreLeopards() {
		return len(value.GetSpecialUserIds()) == 1 && value.GetOutcome() == meetv1.Special235Outcome_SPECIAL235_OUTCOME_BEATS_LEOPARDS
	}
	return value.GetOutcome() == meetv1.Special235Outcome_SPECIAL235_OUTCOME_MINIMUM_SINGLE
}

func roundSummaryValid(value *meetv1.RoundSummary) bool {
	if value == nil || value.GetRound() == 0 || value.GetTargetUserId() == "" || value.GetReason() == "" || !value.GetSettled() || len(value.GetFinalPlayers()) < engine.MinimumPlayers {
		return false
	}
	targetHistory := make([]string, 0)
	users := make(map[string]struct{}, len(value.GetFinalPlayers()))
	for index, player := range value.GetFinalPlayers() {
		if player == nil || player.GetUserId() == "" || index > 0 && value.GetFinalPlayers()[index-1].GetSeatIndex() >= player.GetSeatIndex() ||
			!classifiedHandValid(&meetv1.HandClassified{
				UserId: player.GetUserId(), Round: value.GetRound(), Dice: player.GetDice(), NormalizedDice: player.GetNormalizedDice(),
				HandClass: player.GetHandClass(), Special_235: player.GetSpecial_235(), Special_235Outcome: player.GetSpecial_235Outcome(),
			}) {
			return false
		}
		if _, duplicate := users[player.GetUserId()]; duplicate {
			return false
		}
		users[player.GetUserId()] = struct{}{}
		if player.GetTargetedThisRound() {
			targetHistory = append(targetHistory, player.GetUserId())
		}
	}
	return slices.Equal(value.GetTargetHistoryUserIds(), targetHistory) && slices.Contains(targetHistory, value.GetTargetUserId()) &&
		value.GetTargetStreak() <= value.GetTargetRerollCount() && value.GetOutcome() == outcomeToProto(value.GetReason()) && value.GetCause() == causeToProto(value.GetReason())
}

func facesValid(values []uint32, sorted bool) bool {
	if len(values) != int(engine.DicePerPlayer) {
		return false
	}
	for index, value := range values {
		if !dice.Face(value).Valid() || sorted && index > 0 && values[index-1] > value {
			return false
		}
	}
	return true
}

func cloneReplayEntries(values []*meetv1.ReplayEntry) []*meetv1.ReplayEntry {
	result := make([]*meetv1.ReplayEntry, len(values))
	for index, value := range values {
		result[index] = proto.Clone(value).(*meetv1.ReplayEntry)
	}
	return result
}
