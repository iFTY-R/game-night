package projection

import (
	"slices"

	"github.com/iFTY-R/game-night/games/meet-by-chance/engine"
	meetv1 "github.com/iFTY-R/game-night/games/meet-by-chance/gen/go/game/meet_by_chance/v1"
	"github.com/iFTY-R/game-night/sdk/go/game/dice"
	"google.golang.org/protobuf/proto"
)

type replayStage uint8

const (
	replayNeedReveal replayStage = iota + 1
	replayClassifying
	replayResolving
	replayDecision
	replayNeedNextRound
	replayNeedFinish
	replayFinished
)

type expectedReveal struct {
	users []string
	batch uint32
	cause meetv1.ResolutionCause
}

type expectedPenalty struct {
	userID string
	ticks  uint32
	reason string
	batch  uint32
	cause  meetv1.ResolutionCause
}

type classificationBatch struct {
	users   []string
	round   uint32
	batch   uint32
	events  map[string]*meetv1.HandClassified
	special *meetv1.Special235Evaluated
}

// replaySemanticReducer reconstructs the public authoritative state needed to
// prove that an ordered event stream could have been emitted by the engine.
type replaySemanticReducer struct {
	initialized bool
	config      engine.Config
	players     []engine.Participant
	active      map[string]bool
	included    map[string]bool
	targeted    map[string]bool
	penalties   map[string]uint32
	raw         map[string][3]dice.Face
	hands       map[string]engine.Hand

	round            uint32
	targetUserID     string
	targetRerolls    uint32
	targetStreak     uint32
	matchResolutions uint32
	matchesCapped    bool
	stage            replayStage

	reveal       *expectedReveal
	classify     *classificationBatch
	penaltiesDue []expectedPenalty
}

func (reducer *replaySemanticReducer) apply(value *meetv1.Event) error {
	if value == nil || value.GetEvent() == nil || reducer.stage == replayFinished {
		return projectionError("replay semantic event is invalid")
	}
	if len(reducer.penaltiesDue) != 0 {
		if value.GetPenaltyRecorded() == nil {
			return projectionError("replay omitted an ordered penalty fact")
		}
	} else {
		if reducer.stage == replayNeedReveal && value.GetDiceRevealed() == nil && value.GetRoundStarted() == nil {
			return projectionError("replay omitted an expected dice reveal")
		}
		if reducer.stage == replayClassifying && value.GetHandClassified() == nil && value.GetSpecial_235Evaluated() == nil {
			return projectionError("replay interrupted a classification batch")
		}
		if reducer.stage == replayNeedNextRound && value.GetRoundStarted() == nil {
			return projectionError("replay omitted the next round start")
		}
		if reducer.stage == replayNeedFinish && value.GetSessionFinished() == nil {
			return projectionError("replay omitted the required session finish")
		}
	}

	switch event := value.GetEvent().(type) {
	case *meetv1.Event_RoundStarted:
		return reducer.applyRoundStarted(event.RoundStarted)
	case *meetv1.Event_DiceRevealed:
		return reducer.applyDiceRevealed(event.DiceRevealed)
	case *meetv1.Event_Special_235Evaluated:
		return reducer.applySpecial235(event.Special_235Evaluated)
	case *meetv1.Event_HandClassified:
		return reducer.applyHandClassified(event.HandClassified)
	case *meetv1.Event_MatchResolved:
		return reducer.applyMatchResolved(event.MatchResolved)
	case *meetv1.Event_TargetSelected:
		return reducer.applyTargetSelected(event.TargetSelected)
	case *meetv1.Event_TargetRerolled:
		return reducer.applyTargetRerolled(event.TargetRerolled)
	case *meetv1.Event_PenaltyRecorded:
		return reducer.applyPenalty(event.PenaltyRecorded)
	case *meetv1.Event_ParticipantRevoked:
		return reducer.applyRevocation(event.ParticipantRevoked)
	case *meetv1.Event_RoundSettled:
		return reducer.applyRoundSettled(event.RoundSettled)
	case *meetv1.Event_SessionFinished:
		return reducer.applySessionFinished(event.SessionFinished)
	default:
		return projectionError("replay contains an unsupported semantic event")
	}
}

func (reducer *replaySemanticReducer) applyRoundStarted(value *meetv1.RoundStarted) error {
	if value == nil || reducer.initialized && reducer.stage != replayNeedNextRound {
		return projectionError("round start violates replay phase")
	}
	if !reducer.initialized {
		config, players, _, err := validateReplayInitialization(value)
		if err != nil {
			return err
		}
		reducer.config = config
		reducer.players = make([]engine.Participant, len(players))
		reducer.active = make(map[string]bool, len(players))
		reducer.included = make(map[string]bool, len(players))
		reducer.targeted = make(map[string]bool, len(players))
		reducer.penalties = make(map[string]uint32, len(players))
		reducer.raw = make(map[string][3]dice.Face, len(players))
		reducer.hands = make(map[string]engine.Hand, len(players))
		for index, player := range players {
			reducer.players[index] = engine.Participant{UserID: player.GetUserId(), SeatIndex: player.GetSeatIndex()}
			reducer.active[player.GetUserId()] = true
		}
		reducer.initialized = true
	}
	reducer.round = value.GetRound()
	reducer.targetUserID = ""
	reducer.targetRerolls = 0
	reducer.targetStreak = 0
	reducer.matchResolutions = 0
	reducer.matchesCapped = false
	for _, player := range reducer.players {
		reducer.included[player.UserID] = reducer.active[player.UserID]
		reducer.targeted[player.UserID] = false
	}
	reducer.reveal = &expectedReveal{users: reducer.activeUsers(), cause: meetv1.ResolutionCause_RESOLUTION_CAUSE_INITIAL_ROLL}
	reducer.stage = replayNeedReveal
	return nil
}

func (reducer *replaySemanticReducer) applyDiceRevealed(value *meetv1.DiceRevealed) error {
	if value == nil || reducer.stage != replayNeedReveal || reducer.reveal == nil || value.GetRound() != reducer.round ||
		value.GetBatchIndex() != reducer.reveal.batch || value.GetCause() != reducer.reveal.cause || !slices.Equal(value.GetRolledUserIds(), reducer.reveal.users) {
		return projectionError("dice reveal disagrees with replay state")
	}
	for index, item := range value.GetPublicDice() {
		if item.GetUserId() != reducer.reveal.users[index] || !reducer.active[item.GetUserId()] {
			return projectionError("dice reveal targets an inactive or unexpected player")
		}
		raw, ok := replayFaces(item.GetDice())
		if !ok {
			return projectionError("dice reveal contains invalid faces")
		}
		reducer.raw[item.GetUserId()] = raw
	}
	// rollUsers establishes a new full-table classification boundary. A seat
	// revoked since the previous roll remains auditable in the snapshot but no
	// longer participates in this or any later resolution pass.
	for _, player := range reducer.players {
		reducer.included[player.UserID] = reducer.active[player.UserID]
	}
	reducer.classify = &classificationBatch{
		users: reducer.includedUsers(), round: reducer.round, batch: value.GetBatchIndex(),
		events: make(map[string]*meetv1.HandClassified),
	}
	reducer.reveal = nil
	reducer.stage = replayClassifying
	return nil
}

func (reducer *replaySemanticReducer) applySpecial235(value *meetv1.Special235Evaluated) error {
	if value == nil || reducer.stage != replayClassifying || reducer.classify == nil || len(reducer.classify.events) != 0 || reducer.classify.special != nil ||
		value.GetRound() != reducer.round || value.GetBatchIndex() != reducer.classify.batch {
		return projectionError("235 evaluation is outside its reveal boundary")
	}
	reducer.classify.special = proto.Clone(value).(*meetv1.Special235Evaluated)
	return nil
}

func (reducer *replaySemanticReducer) applyHandClassified(value *meetv1.HandClassified) error {
	batch := reducer.classify
	if value == nil || reducer.stage != replayClassifying || batch == nil || value.GetRound() != batch.round || value.GetBatchIndex() != batch.batch {
		return projectionError("hand classification is outside its reveal boundary")
	}
	index := len(batch.events)
	if index >= len(batch.users) || batch.users[index] != value.GetUserId() {
		return projectionError("hand classifications are not in stable resolution order")
	}
	batch.events[value.GetUserId()] = proto.Clone(value).(*meetv1.HandClassified)
	if len(batch.events) != len(batch.users) {
		return nil
	}
	if err := reducer.finishClassification(); err != nil {
		return err
	}
	reducer.classify = nil
	reducer.matchesCapped = false
	reducer.stage = replayResolving
	return nil
}

func (reducer *replaySemanticReducer) finishClassification() error {
	batch := reducer.classify
	classified := make([]engine.Hand, len(batch.users))
	for index, userID := range batch.users {
		raw, exists := reducer.raw[userID]
		if !exists {
			return projectionError("classification has no revealed raw dice")
		}
		hand, err := engine.Classify(raw, reducer.config)
		if err != nil {
			return projectionError("classification cannot be reproduced")
		}
		classified[index] = hand
	}
	resolved := engine.Resolve235Context(classified)
	specialUsers := make([]string, 0)
	allOthersLeopards := true
	for index, userID := range batch.users {
		value := batch.events[userID]
		if !classifiedEventMatchesHand(value, resolved[index]) {
			return projectionError("classified hand differs from deterministic rules")
		}
		reducer.hands[userID] = resolved[index]
		if resolved[index].Special235 {
			specialUsers = append(specialUsers, userID)
		} else {
			allOthersLeopards = allOthersLeopards && resolved[index].Class == engine.HandLeopard
		}
	}
	if len(specialUsers) == 0 {
		if batch.special != nil {
			return projectionError("235 evaluation has no special hand")
		}
		return nil
	}
	allOthersLeopards = allOthersLeopards && len(specialUsers) == 1
	expectedOutcome := meetv1.Special235Outcome_SPECIAL235_OUTCOME_MINIMUM_SINGLE
	if allOthersLeopards {
		expectedOutcome = meetv1.Special235Outcome_SPECIAL235_OUTCOME_BEATS_LEOPARDS
	}
	if batch.special == nil || !slices.Equal(batch.special.GetSpecialUserIds(), specialUsers) ||
		batch.special.GetAllOtherPlayersAreLeopards() != allOthersLeopards || batch.special.GetOutcome() != expectedOutcome {
		return projectionError("235 evaluation differs from classified table")
	}
	return nil
}

func (reducer *replaySemanticReducer) applyMatchResolved(value *meetv1.MatchResolved) error {
	if value == nil || reducer.stage != replayResolving || value.GetRound() != reducer.round || reducer.matchesCapped {
		return projectionError("match resolution violates replay phase")
	}
	expectedGroups := engine.FindMatchGroups(reducer.enginePlayers())
	batch := value.GetBatch()
	shouldCap := reducer.matchResolutions >= reducer.config.MatchResolutionLimit
	expectedBatch := reducer.matchResolutions
	if !shouldCap {
		expectedBatch++
	}
	if len(expectedGroups) == 0 || batch == nil || batch.GetBatchIndex() != expectedBatch || batch.GetResolutionCount() != expectedBatch || batch.GetCapped() != shouldCap ||
		!matchGroupsEqual(batch.GetGroups(), expectedGroups, reducer.config, shouldCap) ||
		!slices.Equal(batch.GetRerolledUserIds(), reducer.rerolledUsers(expectedGroups, shouldCap)) {
		return projectionError("match batch differs from deterministic groups")
	}
	if shouldCap {
		reducer.matchesCapped = true
		return nil
	}
	reducer.matchResolutions = expectedBatch
	reducer.penaltiesDue = matchPenalties(batch, expectedGroups)
	reducer.reveal = &expectedReveal{
		users: append([]string(nil), batch.GetRerolledUserIds()...), batch: expectedBatch,
		cause: meetv1.ResolutionCause_RESOLUTION_CAUSE_MATCH_REROLL,
	}
	reducer.stage = replayNeedReveal
	return nil
}

func (reducer *replaySemanticReducer) rerolledUsers(groups []engine.MatchGroup, capped bool) []string {
	if capped {
		return nil
	}
	selected := make(map[string]struct{})
	for _, group := range groups {
		for _, userID := range group.UserIDs {
			selected[userID] = struct{}{}
		}
	}
	users := make([]string, 0, len(selected))
	for _, player := range reducer.players {
		if _, ok := selected[player.UserID]; ok {
			users = append(users, player.UserID)
		}
	}
	return users
}

func (reducer *replaySemanticReducer) applyTargetSelected(value *meetv1.TargetSelected) error {
	if value == nil || reducer.stage != replayResolving || value.GetRound() != reducer.round {
		return projectionError("target selection violates replay phase")
	}
	groups := engine.FindMatchGroups(reducer.enginePlayers())
	if len(groups) != 0 && !reducer.matchesCapped {
		return projectionError("target selected before automatic matches resolved")
	}
	target := reducer.lowestActiveUser()
	first := !reducer.targeted[target]
	expectedTicks := uint32(0)
	if first {
		expectedTicks = uint32(reducer.config.TargetPenaltyTicks)
	}
	expectedStreak := reducer.targetStreak
	if reducer.targetUserID != target {
		expectedStreak = 0
	}
	expectedCause := meetv1.ResolutionCause_RESOLUTION_CAUSE_INITIAL_ROLL
	if reducer.targetUserID != "" {
		expectedCause = meetv1.ResolutionCause_RESOLUTION_CAUSE_PLAYER_REROLL
	}
	if value.GetUserId() != target || value.GetPreviousUserId() != reducer.targetUserID || value.GetFirstSelectionThisRound() != first ||
		value.GetPenaltyTicks() != expectedTicks || value.GetTargetRerollCount() != reducer.targetRerolls || value.GetTargetStreak() != expectedStreak ||
		value.GetMatchResolutionCount() != reducer.matchResolutions || value.GetCause() != expectedCause {
		return projectionError("target selection differs from deterministic table")
	}
	reducer.targetUserID = target
	reducer.targetStreak = expectedStreak
	reducer.targeted[target] = true
	reducer.stage = replayDecision
	if first {
		reducer.penaltiesDue = []expectedPenalty{{
			userID: target, ticks: expectedTicks, reason: "target_selected",
			cause: expectedCause,
		}}
	}
	return nil
}

func (reducer *replaySemanticReducer) applyTargetRerolled(value *meetv1.TargetRerolled) error {
	if value == nil || reducer.stage != replayDecision || value.GetRound() != reducer.round || value.GetUserId() != reducer.targetUserID ||
		value.GetCount() != reducer.targetRerolls+1 || value.GetTargetStreak() != reducer.targetStreak+1 ||
		value.GetMatchResolutionCount() != reducer.matchResolutions || value.GetPenaltyTicks() != uint32(reducer.config.RerollPenaltyTicks) ||
		reducer.targetRerolls >= reducer.config.TargetRerollLimit {
		return projectionError("target reroll differs from current decision")
	}
	reducer.targetRerolls++
	reducer.targetStreak++
	reducer.penaltiesDue = []expectedPenalty{{
		userID: reducer.targetUserID, ticks: uint32(reducer.config.RerollPenaltyTicks), reason: "target_reroll",
		cause: meetv1.ResolutionCause_RESOLUTION_CAUSE_PLAYER_REROLL,
	}}
	reducer.reveal = &expectedReveal{
		users: []string{reducer.targetUserID}, cause: meetv1.ResolutionCause_RESOLUTION_CAUSE_PLAYER_REROLL,
	}
	reducer.stage = replayNeedReveal
	return nil
}

func (reducer *replaySemanticReducer) applyPenalty(value *meetv1.PenaltyRecorded) error {
	if value == nil || len(reducer.penaltiesDue) == 0 {
		return projectionError("penalty has no causal rule fact")
	}
	expected := reducer.penaltiesDue[0]
	before := reducer.penalties[expected.userID]
	if value.GetRound() != reducer.round || value.GetUserId() != expected.userID || value.GetTicks() != expected.ticks || value.GetReason() != expected.reason ||
		value.GetBatchIndex() != expected.batch || value.GetCause() != expected.cause || value.GetBeforeTotalTicks() != before || value.GetAfterTotalTicks() != before+expected.ticks {
		return projectionError("penalty total or cause differs from replay state")
	}
	reducer.penalties[expected.userID] = value.GetAfterTotalTicks()
	reducer.penaltiesDue = reducer.penaltiesDue[1:]
	return nil
}

func (reducer *replaySemanticReducer) applyRevocation(value *meetv1.ParticipantRevoked) error {
	if value == nil || reducer.stage != replayDecision || value.GetRound() != reducer.round || !reducer.active[value.GetUserId()] {
		return projectionError("participant revocation violates replay phase")
	}
	wasTarget := value.GetUserId() == reducer.targetUserID
	reducer.active[value.GetUserId()] = false
	activeCount := len(reducer.activeUsers())
	if value.GetWasTarget() != wasTarget || value.GetRoundCancelled() != wasTarget || value.GetActivePlayerCount() != uint32(activeCount) {
		return projectionError("participant revocation summary is inconsistent")
	}
	if activeCount < engine.MinimumPlayers {
		if value.GetNextRound() != 0 {
			return projectionError("insufficient-player revocation starts another round")
		}
		reducer.stage = replayNeedFinish
		return nil
	}
	if wasTarget {
		if value.GetNextRound() != reducer.round+1 {
			return projectionError("target revocation does not advance exactly one round")
		}
		reducer.stage = replayNeedNextRound
	} else if value.GetNextRound() != 0 {
		return projectionError("non-target revocation advances the round")
	}
	return nil
}

func (reducer *replaySemanticReducer) applyRoundSettled(value *meetv1.RoundSettled) error {
	if value == nil || value.GetSummary() == nil || (reducer.stage != replayDecision && reducer.stage != replayResolving) {
		return projectionError("round settlement violates replay phase")
	}
	summary := value.GetSummary()
	if summary.GetRound() != reducer.round || summary.GetTargetUserId() != reducer.targetUserID ||
		summary.GetTargetRerollCount() != reducer.targetRerolls || summary.GetTargetStreak() != reducer.targetStreak ||
		summary.GetMatchResolutionCount() != reducer.matchResolutions {
		return projectionError("round settlement counters differ from replay state")
	}
	if reducer.stage == replayDecision && summary.GetReason() != "stand" && summary.GetReason() != "timeout_stand" {
		return projectionError("decision phase has an invalid settlement reason")
	}
	if reducer.stage == replayResolving {
		groups := engine.FindMatchGroups(reducer.enginePlayers())
		if len(groups) != 0 && !reducer.matchesCapped {
			return projectionError("round settled before matches resolved")
		}
		switch summary.GetReason() {
		case "target_surpassed":
			if !reducer.targetStrictlyStrongest() {
				return projectionError("target-surpassed outcome is false")
			}
		case "reroll_limit":
			if reducer.targetRerolls < reducer.config.TargetRerollLimit {
				return projectionError("reroll-limit outcome is premature")
			}
		default:
			return projectionError("resolution phase has an invalid settlement reason")
		}
	}
	expectedPlayers := publicPlayers(reducer.enginePlayers())
	if !proto.Equal(&meetv1.RoundSummary{FinalPlayers: summary.GetFinalPlayers()}, &meetv1.RoundSummary{FinalPlayers: expectedPlayers}) ||
		summary.GetCause() != causeToProto(summary.GetReason()) || summary.GetOutcome() != outcomeToProto(summary.GetReason()) {
		return projectionError("round settlement public state is inconsistent")
	}
	reducer.stage = replayNeedNextRound
	return nil
}

func (reducer *replaySemanticReducer) applySessionFinished(value *meetv1.SessionFinished) error {
	if value == nil || value.GetRound() != reducer.round || (reducer.stage != replayDecision && reducer.stage != replayNeedFinish) {
		return projectionError("session finish violates replay phase")
	}
	if reducer.stage == replayNeedFinish && value.GetReason() != engine.FinishInsufficientParticipants {
		return projectionError("insufficient-player session has another finish cause")
	}
	if reducer.stage == replayDecision && value.GetReason() == engine.FinishInsufficientParticipants {
		return projectionError("session reports insufficient players while decision remains valid")
	}
	reducer.stage = replayFinished
	return nil
}

func (reducer *replaySemanticReducer) complete() bool {
	return reducer.initialized && len(reducer.penaltiesDue) == 0 && reducer.classify == nil && reducer.reveal == nil &&
		(reducer.stage == replayDecision || reducer.stage == replayFinished)
}

func (reducer *replaySemanticReducer) activeUsers() []string {
	users := make([]string, 0, len(reducer.players))
	for _, player := range reducer.players {
		if reducer.active[player.UserID] {
			users = append(users, player.UserID)
		}
	}
	return users
}

func (reducer *replaySemanticReducer) includedUsers() []string {
	users := make([]string, 0, len(reducer.players))
	for _, player := range reducer.players {
		if reducer.included[player.UserID] {
			users = append(users, player.UserID)
		}
	}
	return users
}

func (reducer *replaySemanticReducer) enginePlayers() []engine.PlayerState {
	players := make([]engine.PlayerState, len(reducer.players))
	for index, participant := range reducer.players {
		players[index] = engine.PlayerState{
			UserID: participant.UserID, SeatIndex: participant.SeatIndex,
			Active: reducer.active[participant.UserID], IncludedInCurrentResolution: reducer.included[participant.UserID],
			TargetedThisRound: reducer.targeted[participant.UserID], PenaltyTicks: dice.Ticks(reducer.penalties[participant.UserID]),
			Hand: reducer.hands[participant.UserID],
		}
	}
	return players
}

func (reducer *replaySemanticReducer) lowestActiveUser() string {
	players := reducer.enginePlayers()
	lowest := -1
	for index, player := range players {
		if !player.Active {
			continue
		}
		if lowest < 0 || engine.CompareHand(player.Hand, players[lowest].Hand) < 0 {
			lowest = index
		}
	}
	if lowest < 0 {
		return ""
	}
	return players[lowest].UserID
}

func (reducer *replaySemanticReducer) targetStrictlyStrongest() bool {
	players := reducer.enginePlayers()
	target := -1
	for index, player := range players {
		if player.UserID == reducer.targetUserID && player.Active {
			target = index
			break
		}
	}
	if target < 0 {
		return false
	}
	for index, player := range players {
		if index != target && player.Active && engine.CompareHand(players[target].Hand, player.Hand) <= 0 {
			return false
		}
	}
	return true
}

func replayFaces(values []uint32) ([3]dice.Face, bool) {
	if len(values) != int(engine.DicePerPlayer) {
		return [3]dice.Face{}, false
	}
	result := [3]dice.Face{}
	for index, value := range values {
		result[index] = dice.Face(value)
		if !result[index].Valid() {
			return [3]dice.Face{}, false
		}
	}
	return result, true
}

func classifiedEventMatchesHand(value *meetv1.HandClassified, hand engine.Hand) bool {
	return value != nil && slices.Equal(value.GetDice(), faces(hand.Raw[:])) && slices.Equal(value.GetNormalizedDice(), faces(hand.Normalized[:])) &&
		value.GetHandClass() == handClassToProto(hand.Class) && value.GetSpecial_235() == hand.Special235 &&
		value.GetSpecial_235Outcome() == specialOutcomeToProto(hand.SpecialContext)
}

func matchGroupsEqual(values []*meetv1.MatchGroup, groups []engine.MatchGroup, config engine.Config, capped bool) bool {
	if len(values) != len(groups) {
		return false
	}
	for index, group := range groups {
		value := values[index]
		expectedPenalty := uint32(config.MatchPenaltyTicks)
		expectedWeak := uint32(0)
		if group.Kind == engine.MatchLow {
			expectedWeak = uint32(config.WeakExtraPenaltyTicks)
		}
		if capped {
			expectedPenalty, expectedWeak = 0, 0
		}
		if value == nil || value.GetKind() != matchKindToProto(group.Kind) || !slices.Equal(value.GetUserIds(), group.UserIDs) ||
			value.GetWeakestUserId() != group.WeakestUserID || value.GetPenaltyTicks() != expectedPenalty || value.GetWeakExtraPenaltyTicks() != expectedWeak {
			return false
		}
	}
	return true
}

func matchPenalties(batch *meetv1.MatchBatch, groups []engine.MatchGroup) []expectedPenalty {
	penalties := make([]expectedPenalty, 0)
	for index, group := range groups {
		encoded := batch.GetGroups()[index]
		for _, userID := range group.UserIDs {
			penalties = append(penalties, expectedPenalty{
				userID: userID, ticks: encoded.GetPenaltyTicks(), reason: "match_" + string(group.Kind),
				batch: batch.GetBatchIndex(), cause: meetv1.ResolutionCause_RESOLUTION_CAUSE_MATCH_REROLL,
			})
		}
		if group.Kind == engine.MatchLow {
			penalties = append(penalties, expectedPenalty{
				userID: group.WeakestUserID, ticks: encoded.GetWeakExtraPenaltyTicks(), reason: "match_low_extra",
				batch: batch.GetBatchIndex(), cause: meetv1.ResolutionCause_RESOLUTION_CAUSE_MATCH_REROLL,
			})
		}
	}
	return penalties
}
