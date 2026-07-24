package module

import (
	"cmp"
	"slices"

	"github.com/iFTY-R/game-night/games/three-rounds/engine"
	threeroundsv1 "github.com/iFTY-R/game-night/games/three-rounds/gen/go/game/three_rounds/v1"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"google.golang.org/protobuf/proto"
)

// EncodeConfig serializes a canonical frozen configuration.
func EncodeConfig(config engine.Config) (game.Message, error) {
	return EncodeConfigForPlayers(config, engine.MaximumPlayers)
}

// EncodeConfigForPlayers validates room-size-dependent rules before encoding.
func EncodeConfigForPlayers(config engine.Config, playerCount int) (game.Message, error) {
	if err := config.Validate(playerCount); err != nil {
		return game.Message{}, err
	}
	payload, err := marshalDeterministic(configToProto(config))
	if err != nil {
		return game.Message{}, malformed("config encoding failed")
	}
	return game.Message{MessageType: ConfigMessageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload}, nil
}

// DecodeConfig treats only an empty payload as the default ruleset.
func DecodeConfig(message game.Message, playerCount int) (engine.Config, error) {
	if message.MessageType != ConfigMessageType || message.SchemaVersion != ProtocolSchemaVersion {
		return engine.Config{}, malformed("config envelope is not owned by three-rounds")
	}
	if len(message.Payload) == 0 {
		config := engine.DefaultConfig()
		if err := config.Validate(playerCount); err != nil {
			return engine.Config{}, err
		}
		return config, nil
	}
	var value threeroundsv1.Config
	if err := unmarshalStrict(message.Payload, &value); err != nil {
		return engine.Config{}, err
	}
	config, err := configFromProto(&value)
	if err != nil {
		return engine.Config{}, err
	}
	if err := config.Validate(playerCount); err != nil {
		return engine.Config{}, err
	}
	if !proto.Equal(&value, configToProto(config)) {
		return engine.Config{}, malformed("config payload is not canonical")
	}
	return config, nil
}

// EncodeState validates and emits a canonical authoritative snapshot.
func EncodeState(state engine.State) (game.Message, error) {
	if err := state.Validate(); err != nil {
		return game.Message{}, err
	}
	payload, err := marshalDeterministic(stateToProto(state))
	if err != nil {
		return game.Message{}, malformed("state encoding failed")
	}
	return game.Message{MessageType: StateMessageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload}, nil
}

// DecodeState validates the canonical snapshot before commands or projections consume it.
func DecodeState(message game.Message) (engine.State, error) {
	if message.MessageType != StateMessageType || message.SchemaVersion != ProtocolSchemaVersion {
		return engine.State{}, malformed("state envelope is not owned by three-rounds")
	}
	var value threeroundsv1.State
	if err := unmarshalStrict(message.Payload, &value); err != nil {
		return engine.State{}, err
	}
	state, err := stateFromProto(&value)
	if err != nil {
		return engine.State{}, err
	}
	if err := state.Validate(); err != nil {
		return engine.State{}, err
	}
	if !proto.Equal(&value, stateToProto(state)) {
		return engine.State{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "state contains non-canonical derived fields"}
	}
	return state, nil
}

func configToProto(config engine.Config) *threeroundsv1.Config {
	return &threeroundsv1.Config{
		RoundOneTimeoutSeconds: config.RoundOneTimeoutSeconds,
		RoundTwoTimeoutSeconds: config.RoundTwoTimeoutSeconds,
		RoundResultSeconds:     config.RoundResultSeconds,
		FinalResultSeconds:     config.FinalResultSeconds,
	}
}

func configFromProto(value *threeroundsv1.Config) (engine.Config, error) {
	if value == nil {
		return engine.Config{}, malformed("config is missing")
	}
	return engine.Config{
		RoundOneTimeoutSeconds: value.GetRoundOneTimeoutSeconds(),
		RoundTwoTimeoutSeconds: value.GetRoundTwoTimeoutSeconds(),
		RoundResultSeconds:     value.GetRoundResultSeconds(),
		FinalResultSeconds:     value.GetFinalResultSeconds(),
	}, nil
}

func stateToProto(state engine.State) *threeroundsv1.State {
	return &threeroundsv1.State{
		SchemaVersion:           state.SchemaVersion,
		Phase:                   phaseToProto(state.Phase),
		CurrentRound:            state.CurrentRound,
		PhaseDeadlineUnixMillis: state.PhaseDeadlineUnixMillis,
		PhaseGeneration:         state.PhaseGeneration,
		Config:                  configToProto(state.Config),
		HostUserId:              state.HostUserID,
		Players:                 playersToProto(state.Players),
		RoundHistory:            roundHistoryToProto(state.RoundHistory),
		FinalSummary:            finalSummaryToProto(state.FinalSummary),
		FinishReason:            finishReasonToProto(state.FinishReason),
	}
}

func stateFromProto(value *threeroundsv1.State) (engine.State, error) {
	if value == nil || value.GetSchemaVersion() != engine.CurrentSchemaVersion || value.GetCurrentRound() == 0 || value.GetConfig() == nil {
		return engine.State{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "state header is malformed"}
	}
	config, err := configFromProto(value.GetConfig())
	if err != nil {
		return engine.State{}, err
	}
	players, err := playersFromProto(value.GetPlayers())
	if err != nil {
		return engine.State{}, err
	}
	phase, ok := phaseFromProto(value.GetPhase())
	if !ok {
		return engine.State{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "state phase is unknown"}
	}
	history, err := roundHistoryFromProto(value.GetRoundHistory())
	if err != nil {
		return engine.State{}, err
	}
	finalSummary, err := finalSummaryFromProto(value.GetFinalSummary())
	if err != nil {
		return engine.State{}, err
	}
	return engine.State{
		SchemaVersion:           value.GetSchemaVersion(),
		Phase:                   phase,
		CurrentRound:            value.GetCurrentRound(),
		PhaseDeadlineUnixMillis: value.GetPhaseDeadlineUnixMillis(),
		PhaseGeneration:         value.GetPhaseGeneration(),
		Config:                  config,
		HostUserID:              value.GetHostUserId(),
		Players:                 players,
		RoundHistory:            history,
		FinalSummary:            finalSummary,
		FinishReason:            finishReasonFromProto(value.GetFinishReason()),
	}, nil
}

func playersToProto(players []engine.PlayerState) []*threeroundsv1.PlayerState {
	result := make([]*threeroundsv1.PlayerState, len(players))
	for index, player := range players {
		result[index] = &threeroundsv1.PlayerState{
			UserId:                       player.UserID,
			SeatIndex:                    player.SeatIndex,
			Active:                       player.Active,
			InitialHand:                  cardIDsToStrings(player.InitialHand),
			RemainingHand:                cardIDsToStrings(player.RemainingHand),
			PendingSelection:             pendingSelectionToProto(player.PendingSelection),
			RoundOneCards:                roundCardsToProto(player.RoundOneCards),
			RoundTwoCards:                roundCardsToProto(player.RoundTwoCards),
			RoundThreeCards:              roundCardsToProto(player.RoundThreeCards),
			RoundThreeWildcardResolution: wildcardResolutionToProto(player.RoundThreeWildcardResolution),
			RoundOnePoints:               uint32(player.RoundOnePoints),
			RoundTwoPoints:               uint32(player.RoundTwoPoints),
			RoundThreePoints:             uint32(player.RoundThreePoints),
			TotalPoints:                  uint32(player.TotalPoints),
			WonRoundOne:                  player.WonRoundOne,
			WonRoundTwo:                  player.WonRoundTwo,
			WonRoundThree:                player.WonRoundThree,
			FinalWinner:                  player.FinalWinner,
			FinalRank:                    player.FinalRank,
		}
	}
	return result
}

func playersFromProto(values []*threeroundsv1.PlayerState) ([]engine.PlayerState, error) {
	players := make([]engine.PlayerState, len(values))
	for index, value := range values {
		if value == nil || value.GetUserId() == "" {
			return nil, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "state contains a malformed player"}
		}
		pending, err := pendingSelectionFromProto(value.GetPendingSelection())
		if err != nil {
			return nil, err
		}
		resolution, err := wildcardResolutionFromProto(value.GetRoundThreeWildcardResolution())
		if err != nil {
			return nil, err
		}
		players[index] = engine.PlayerState{
			UserID:                       value.GetUserId(),
			SeatIndex:                    value.GetSeatIndex(),
			Active:                       value.GetActive(),
			InitialHand:                  stringsToCardIDs(value.GetInitialHand()),
			RemainingHand:                stringsToCardIDs(value.GetRemainingHand()),
			PendingSelection:             pending,
			RoundOneCards:                stringsToCardIDs(value.GetRoundOneCards().GetCardIds()),
			RoundTwoCards:                stringsToCardIDs(value.GetRoundTwoCards().GetCardIds()),
			RoundThreeCards:              stringsToCardIDs(value.GetRoundThreeCards().GetCardIds()),
			RoundThreeWildcardResolution: resolution,
			RoundOnePoints:               uint8(value.GetRoundOnePoints()),
			RoundTwoPoints:               uint8(value.GetRoundTwoPoints()),
			RoundThreePoints:             uint8(value.GetRoundThreePoints()),
			TotalPoints:                  uint8(value.GetTotalPoints()),
			WonRoundOne:                  value.GetWonRoundOne(),
			WonRoundTwo:                  value.GetWonRoundTwo(),
			WonRoundThree:                value.GetWonRoundThree(),
			FinalWinner:                  value.GetFinalWinner(),
			FinalRank:                    value.GetFinalRank(),
		}
	}
	return players, nil
}

func pendingSelectionToProto(value engine.PendingSelection) *threeroundsv1.PendingSelection {
	if value.Round == 0 {
		return nil
	}
	return &threeroundsv1.PendingSelection{
		Round:         value.Round,
		CardIds:       cardIDsToStrings(value.CardIDs),
		AutoSubmitted: value.AutoSubmitted,
	}
}

func pendingSelectionFromProto(value *threeroundsv1.PendingSelection) (engine.PendingSelection, error) {
	if value == nil {
		return engine.PendingSelection{}, nil
	}
	return engine.PendingSelection{
		Round:         value.GetRound(),
		CardIDs:       stringsToCardIDs(value.GetCardIds()),
		AutoSubmitted: value.GetAutoSubmitted(),
	}, nil
}

func roundCardsToProto(values []engine.CardID) *threeroundsv1.RoundCards {
	if len(values) == 0 {
		return nil
	}
	return &threeroundsv1.RoundCards{CardIds: cardIDsToStrings(values)}
}

func wildcardResolutionToProto(value engine.WildcardResolution) *threeroundsv1.WildcardResolution {
	if len(value.Substitutions) == 0 && len(value.ResolvedCards) == 0 {
		return nil
	}
	result := &threeroundsv1.WildcardResolution{ResolvedCards: cardIDsToStrings(value.ResolvedCards)}
	for _, substitution := range value.Substitutions {
		result.Substitutions = append(result.Substitutions, &threeroundsv1.WildcardSubstitution{
			WildcardCardId:    string(substitution.WildcardCardID),
			SubstitutedCardId: string(substitution.SubstitutedCardID),
			CardOrdinal:       uint32(substitution.CardOrdinal),
		})
	}
	return result
}

func wildcardResolutionFromProto(value *threeroundsv1.WildcardResolution) (engine.WildcardResolution, error) {
	if value == nil {
		return engine.WildcardResolution{}, nil
	}
	result := engine.WildcardResolution{ResolvedCards: stringsToCardIDs(value.GetResolvedCards())}
	for _, substitution := range value.GetSubstitutions() {
		if substitution == nil {
			return engine.WildcardResolution{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "wildcard substitution is nil"}
		}
		result.Substitutions = append(result.Substitutions, engine.WildcardSubstitution{
			WildcardCardID:    engine.CardID(substitution.GetWildcardCardId()),
			SubstitutedCardID: engine.CardID(substitution.GetSubstitutedCardId()),
			CardOrdinal:       uint8(substitution.GetCardOrdinal()),
		})
	}
	return result, nil
}

func roundHistoryToProto(values []engine.RoundSummary) []*threeroundsv1.RoundSummary {
	result := make([]*threeroundsv1.RoundSummary, len(values))
	for index, value := range values {
		result[index] = roundSummaryToProto(value)
	}
	return result
}

func roundHistoryFromProto(values []*threeroundsv1.RoundSummary) ([]engine.RoundSummary, error) {
	result := make([]engine.RoundSummary, len(values))
	for index, value := range values {
		summary, err := roundSummaryFromProto(value)
		if err != nil {
			return nil, err
		}
		result[index] = summary
	}
	return result, nil
}

func roundSummaryToProto(value engine.RoundSummary) *threeroundsv1.RoundSummary {
	result := &threeroundsv1.RoundSummary{
		Round:         value.Round,
		WinnerUserIds: append([]string(nil), value.WinnerUserIDs...),
		AllBusted:     value.AllBusted,
	}
	for _, reveal := range value.Reveals {
		result.Reveals = append(result.Reveals, playerRevealToProto(reveal))
	}
	return result
}

func roundSummaryFromProto(value *threeroundsv1.RoundSummary) (engine.RoundSummary, error) {
	if value == nil {
		return engine.RoundSummary{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "round summary is nil"}
	}
	result := engine.RoundSummary{
		Round:         value.GetRound(),
		WinnerUserIDs: append([]string(nil), value.GetWinnerUserIds()...),
		AllBusted:     value.GetAllBusted(),
	}
	for _, reveal := range value.GetReveals() {
		item, err := playerRevealFromProto(reveal)
		if err != nil {
			return engine.RoundSummary{}, err
		}
		result.Reveals = append(result.Reveals, item)
	}
	return result, nil
}

func playerRevealToProto(value engine.PlayerReveal) *threeroundsv1.PlayerReveal {
	result := &threeroundsv1.PlayerReveal{
		UserId:             value.UserID,
		SeatIndex:          value.SeatIndex,
		Active:             value.Active,
		CardIds:            cardIDsToStrings(value.CardIDs),
		AutoSubmitted:      value.AutoSubmitted,
		AwardedPoints:      uint32(value.AwardedPoints),
		WildcardResolution: wildcardResolutionToProto(value.WildcardResolution),
	}
	if value.RoundOneEvaluation.CardID != "" {
		result.RoundOne = &threeroundsv1.RoundOneEvaluation{
			CardId:       string(value.RoundOneEvaluation.CardID),
			RankStrength: uint32(value.RoundOneEvaluation.RankStrength),
			SuitStrength: uint32(value.RoundOneEvaluation.SuitStrength),
		}
	}
	if value.RoundTwoEvaluation.TotalHalfPoints != 0 || value.RoundTwoEvaluation.Busted {
		result.RoundTwo = &threeroundsv1.RoundTwoEvaluation{
			TotalHalfPoints:   uint32(value.RoundTwoEvaluation.TotalHalfPoints),
			Busted:            value.RoundTwoEvaluation.Busted,
			RankStrengthsDesc: []uint32{uint32(value.RoundTwoEvaluation.RankStrengthsDesc[0]), uint32(value.RoundTwoEvaluation.RankStrengthsDesc[1])},
		}
	}
	if value.RoundThreeEvaluation.HandClass != 0 {
		result.RoundThree = &threeroundsv1.RoundThreeEvaluation{
			HandClass:     handClassToProto(value.RoundThreeEvaluation.HandClass),
			CompareValues: bytesToUint32s(value.RoundThreeEvaluation.CompareValues),
			ResolvedCards: cardIDsToStrings(value.RoundThreeEvaluation.ResolvedCards),
		}
	}
	return result
}

func playerRevealFromProto(value *threeroundsv1.PlayerReveal) (engine.PlayerReveal, error) {
	if value == nil || value.GetUserId() == "" {
		return engine.PlayerReveal{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "player reveal is malformed"}
	}
	resolution, err := wildcardResolutionFromProto(value.GetWildcardResolution())
	if err != nil {
		return engine.PlayerReveal{}, err
	}
	reveal := engine.PlayerReveal{
		UserID:             value.GetUserId(),
		SeatIndex:          value.GetSeatIndex(),
		Active:             value.GetActive(),
		CardIDs:            stringsToCardIDs(value.GetCardIds()),
		AutoSubmitted:      value.GetAutoSubmitted(),
		AwardedPoints:      uint8(value.GetAwardedPoints()),
		WildcardResolution: resolution,
	}
	if current := value.GetRoundOne(); current != nil {
		reveal.RoundOneEvaluation = engine.RoundOneEvaluation{
			CardID:       engine.CardID(current.GetCardId()),
			RankStrength: uint8(current.GetRankStrength()),
			SuitStrength: uint8(current.GetSuitStrength()),
		}
	}
	if current := value.GetRoundTwo(); current != nil {
		if len(current.GetRankStrengthsDesc()) != 2 {
			return engine.PlayerReveal{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "round two rank strengths are malformed"}
		}
		reveal.RoundTwoEvaluation = engine.RoundTwoEvaluation{
			TotalHalfPoints:   uint8(current.GetTotalHalfPoints()),
			Busted:            current.GetBusted(),
			RankStrengthsDesc: [2]uint8{uint8(current.GetRankStrengthsDesc()[0]), uint8(current.GetRankStrengthsDesc()[1])},
		}
	}
	if current := value.GetRoundThree(); current != nil {
		handClass, ok := handClassFromProto(current.GetHandClass())
		if !ok {
			return engine.PlayerReveal{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "round three hand class is unknown"}
		}
		reveal.RoundThreeEvaluation = engine.RoundThreeEvaluation{
			HandClass:     handClass,
			CompareValues: uint32sToBytes(current.GetCompareValues()),
			ResolvedCards: stringsToCardIDs(current.GetResolvedCards()),
		}
	}
	return reveal, nil
}

func finalSummaryToProto(value engine.FinalSummary) *threeroundsv1.FinalSummary {
	if len(value.Standings) == 0 && len(value.WinnerUserIDs) == 0 {
		return nil
	}
	result := &threeroundsv1.FinalSummary{WinnerUserIds: append([]string(nil), value.WinnerUserIDs...)}
	for _, standing := range value.Standings {
		result.Standings = append(result.Standings, &threeroundsv1.FinalStanding{
			UserId:        standing.UserID,
			SeatIndex:     standing.SeatIndex,
			Active:        standing.Active,
			TotalPoints:   uint32(standing.TotalPoints),
			WonRoundOne:   standing.WonRoundOne,
			WonRoundTwo:   standing.WonRoundTwo,
			WonRoundThree: standing.WonRoundThree,
			Winner:        standing.Winner,
			Rank:          standing.Rank,
		})
	}
	return result
}

func finalSummaryFromProto(value *threeroundsv1.FinalSummary) (engine.FinalSummary, error) {
	if value == nil {
		return engine.FinalSummary{}, nil
	}
	result := engine.FinalSummary{WinnerUserIDs: append([]string(nil), value.GetWinnerUserIds()...)}
	for _, standing := range value.GetStandings() {
		if standing == nil || standing.GetUserId() == "" {
			return engine.FinalSummary{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "final standing is malformed"}
		}
		result.Standings = append(result.Standings, engine.FinalStanding{
			UserID:        standing.GetUserId(),
			SeatIndex:     standing.GetSeatIndex(),
			Active:        standing.GetActive(),
			TotalPoints:   uint8(standing.GetTotalPoints()),
			WonRoundOne:   standing.GetWonRoundOne(),
			WonRoundTwo:   standing.GetWonRoundTwo(),
			WonRoundThree: standing.GetWonRoundThree(),
			Winner:        standing.GetWinner(),
			Rank:          standing.GetRank(),
		})
	}
	return result, nil
}

func phaseToProto(value engine.Phase) threeroundsv1.Phase {
	switch value {
	case engine.PhaseDealing:
		return threeroundsv1.Phase_PHASE_DEALING
	case engine.PhaseRoundOneSelecting:
		return threeroundsv1.Phase_PHASE_ROUND_ONE_SELECTING
	case engine.PhaseRoundOneResult:
		return threeroundsv1.Phase_PHASE_ROUND_ONE_RESULT
	case engine.PhaseRoundTwoSelecting:
		return threeroundsv1.Phase_PHASE_ROUND_TWO_SELECTING
	case engine.PhaseRoundTwoResult:
		return threeroundsv1.Phase_PHASE_ROUND_TWO_RESULT
	case engine.PhaseRoundThreeResult:
		return threeroundsv1.Phase_PHASE_ROUND_THREE_RESULT
	case engine.PhaseFinalResult:
		return threeroundsv1.Phase_PHASE_FINAL_RESULT
	case engine.PhaseFinished:
		return threeroundsv1.Phase_PHASE_FINISHED
	default:
		return threeroundsv1.Phase_PHASE_UNSPECIFIED
	}
}

func phaseFromProto(value threeroundsv1.Phase) (engine.Phase, bool) {
	switch value {
	case threeroundsv1.Phase_PHASE_DEALING:
		return engine.PhaseDealing, true
	case threeroundsv1.Phase_PHASE_ROUND_ONE_SELECTING:
		return engine.PhaseRoundOneSelecting, true
	case threeroundsv1.Phase_PHASE_ROUND_ONE_RESULT:
		return engine.PhaseRoundOneResult, true
	case threeroundsv1.Phase_PHASE_ROUND_TWO_SELECTING:
		return engine.PhaseRoundTwoSelecting, true
	case threeroundsv1.Phase_PHASE_ROUND_TWO_RESULT:
		return engine.PhaseRoundTwoResult, true
	case threeroundsv1.Phase_PHASE_ROUND_THREE_RESULT:
		return engine.PhaseRoundThreeResult, true
	case threeroundsv1.Phase_PHASE_FINAL_RESULT:
		return engine.PhaseFinalResult, true
	case threeroundsv1.Phase_PHASE_FINISHED:
		return engine.PhaseFinished, true
	default:
		return 0, false
	}
}

func finishReasonToProto(value string) threeroundsv1.FinishReason {
	switch value {
	case engine.FinishNormalCompleted:
		return threeroundsv1.FinishReason_FINISH_REASON_NORMAL_COMPLETED
	case engine.FinishHostRequested:
		return threeroundsv1.FinishReason_FINISH_REASON_HOST_REQUESTED
	case engine.FinishInsufficientParticipants:
		return threeroundsv1.FinishReason_FINISH_REASON_INSUFFICIENT_PARTICIPANTS
	default:
		return threeroundsv1.FinishReason_FINISH_REASON_UNSPECIFIED
	}
}

func finishReasonFromProto(value threeroundsv1.FinishReason) string {
	switch value {
	case threeroundsv1.FinishReason_FINISH_REASON_NORMAL_COMPLETED:
		return engine.FinishNormalCompleted
	case threeroundsv1.FinishReason_FINISH_REASON_HOST_REQUESTED:
		return engine.FinishHostRequested
	case threeroundsv1.FinishReason_FINISH_REASON_INSUFFICIENT_PARTICIPANTS:
		return engine.FinishInsufficientParticipants
	default:
		return ""
	}
}

func handClassToProto(value engine.HandClass) threeroundsv1.HandClass {
	switch value {
	case engine.HandSingle:
		return threeroundsv1.HandClass_HAND_CLASS_SINGLE
	case engine.HandPair:
		return threeroundsv1.HandClass_HAND_CLASS_PAIR
	case engine.HandStraight:
		return threeroundsv1.HandClass_HAND_CLASS_STRAIGHT
	case engine.HandFlush:
		return threeroundsv1.HandClass_HAND_CLASS_FLUSH
	case engine.HandStraightFlush:
		return threeroundsv1.HandClass_HAND_CLASS_STRAIGHT_FLUSH
	case engine.HandTrips:
		return threeroundsv1.HandClass_HAND_CLASS_TRIPS
	default:
		return threeroundsv1.HandClass_HAND_CLASS_UNSPECIFIED
	}
}

func handClassFromProto(value threeroundsv1.HandClass) (engine.HandClass, bool) {
	switch value {
	case threeroundsv1.HandClass_HAND_CLASS_SINGLE:
		return engine.HandSingle, true
	case threeroundsv1.HandClass_HAND_CLASS_PAIR:
		return engine.HandPair, true
	case threeroundsv1.HandClass_HAND_CLASS_STRAIGHT:
		return engine.HandStraight, true
	case threeroundsv1.HandClass_HAND_CLASS_FLUSH:
		return engine.HandFlush, true
	case threeroundsv1.HandClass_HAND_CLASS_STRAIGHT_FLUSH:
		return engine.HandStraightFlush, true
	case threeroundsv1.HandClass_HAND_CLASS_TRIPS:
		return engine.HandTrips, true
	default:
		return 0, false
	}
}

func cardIDsToStrings(values []engine.CardID) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}

func stringsToCardIDs(values []string) []engine.CardID {
	result := make([]engine.CardID, len(values))
	for index, value := range values {
		result[index] = engine.CardID(value)
	}
	return result
}

func bytesToUint32s(values []uint8) []uint32 {
	result := make([]uint32, len(values))
	for index, value := range values {
		result[index] = uint32(value)
	}
	return result
}

func uint32sToBytes(values []uint32) []uint8 {
	result := make([]uint8, len(values))
	for index, value := range values {
		result[index] = uint8(value)
	}
	return result
}

func canonicalParticipants(participants []engine.Participant) []engine.Participant {
	result := append([]engine.Participant(nil), participants...)
	slices.SortFunc(result, func(left, right engine.Participant) int {
		if diff := cmp.Compare(left.SeatIndex, right.SeatIndex); diff != 0 {
			return diff
		}
		return cmp.Compare(left.UserID, right.UserID)
	})
	return result
}
