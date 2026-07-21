package module

import (
	"slices"

	"github.com/iFTY-R/game-night/games/meet-by-chance/engine"
	meetv1 "github.com/iFTY-R/game-night/games/meet-by-chance/gen/go/game/meet_by_chance/v1"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"github.com/iFTY-R/game-night/sdk/go/game/dice"
	"google.golang.org/protobuf/proto"
)

// EncodeConfig serializes a complete frozen rules configuration.
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

// DecodeConfig treats only an empty payload as the default ruleset. Every
// explicit payload must be canonical and valid for the frozen room size.
func DecodeConfig(message game.Message, playerCount int) (engine.Config, error) {
	if message.MessageType != ConfigMessageType || message.SchemaVersion != ProtocolSchemaVersion {
		return engine.Config{}, malformed("config envelope is not owned by meet-by-chance")
	}
	if len(message.Payload) == 0 {
		config := engine.DefaultConfig()
		if err := config.Validate(playerCount); err != nil {
			return engine.Config{}, err
		}
		return config, nil
	}
	var value meetv1.Config
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
		return engine.Config{}, malformed("config payload is not complete and canonical")
	}
	return config, nil
}

func configToProto(config engine.Config) *meetv1.Config {
	return &meetv1.Config{
		Straight_123: config.Straight123, Straight_234: config.Straight234,
		Straight_345: config.Straight345, Straight_456: config.Straight456,
		Special_235Enabled: config.Special235Enabled, OnesWild: config.OnesWild,
		TargetPenaltyTicks: uint32(config.TargetPenaltyTicks), RerollPenaltyTicks: uint32(config.RerollPenaltyTicks),
		MatchPenaltyTicks: uint32(config.MatchPenaltyTicks), WeakExtraPenaltyTicks: uint32(config.WeakExtraPenaltyTicks),
		TargetRerollLimit: config.TargetRerollLimit, MatchResolutionLimit: config.MatchResolutionLimit,
		ActionTimeoutSeconds: config.ActionTimeoutSeconds,
	}
}

func configFromProto(value *meetv1.Config) (engine.Config, error) {
	if value == nil {
		return engine.Config{}, malformed("config message is missing")
	}
	return engine.Config{
		Straight123: value.GetStraight_123(), Straight234: value.GetStraight_234(),
		Straight345: value.GetStraight_345(), Straight456: value.GetStraight_456(),
		Special235Enabled: value.GetSpecial_235Enabled(), OnesWild: value.GetOnesWild(),
		TargetPenaltyTicks: dice.Ticks(value.GetTargetPenaltyTicks()), RerollPenaltyTicks: dice.Ticks(value.GetRerollPenaltyTicks()),
		MatchPenaltyTicks: dice.Ticks(value.GetMatchPenaltyTicks()), WeakExtraPenaltyTicks: dice.Ticks(value.GetWeakExtraPenaltyTicks()),
		TargetRerollLimit: value.GetTargetRerollLimit(), MatchResolutionLimit: value.GetMatchResolutionLimit(),
		ActionTimeoutSeconds: value.GetActionTimeoutSeconds(),
	}, nil
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

// DecodeState re-derives every active hand before authoritative rules or public
// projections may consume restored state.
func DecodeState(message game.Message) (engine.State, error) {
	if message.MessageType != StateMessageType || message.SchemaVersion != ProtocolSchemaVersion {
		return engine.State{}, malformed("state envelope is not owned by meet-by-chance")
	}
	var value meetv1.State
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

func stateToProto(state engine.State) *meetv1.State {
	value := &meetv1.State{
		SchemaVersion: state.SchemaVersion, Phase: phaseToProto(state.Phase), Round: state.Round,
		Players: playersToProto(state.Players), TargetUserId: state.TargetUserID,
		TargetRerollCount: state.TargetRerollCount, TargetStreak: state.TargetStreak,
		MatchResolutionCount: state.MatchResolutionCount, ActionDeadlineUnixMillis: state.ActionDeadlineUnixMillis,
		Config: configToProto(state.Config), HostUserId: state.HostUserID, FinishReason: state.FinishReason,
	}
	value.RoundHistory = make([]*meetv1.RoundSummary, len(state.RoundHistory))
	for index, settlement := range state.RoundHistory {
		value.RoundHistory[index] = settlementToProto(settlement)
	}
	if state.LastSettlement.Round != 0 {
		value.LastSettlement = settlementToProto(state.LastSettlement)
	}
	value.MatchHistory = make([]*meetv1.MatchBatch, len(state.MatchHistory))
	for index, resolution := range state.MatchHistory {
		value.MatchHistory[index] = matchBatchToProto(state.Round, resolution, state.Config, state.Players)
	}
	if len(state.MatchHistory) != 0 {
		value.LastMatchBatch = proto.Clone(value.MatchHistory[len(value.MatchHistory)-1]).(*meetv1.MatchBatch)
	}
	return value
}

func stateFromProto(value *meetv1.State) (engine.State, error) {
	if value == nil || value.GetSchemaVersion() != engine.CurrentSchemaVersion || value.GetRound() == 0 || value.GetConfig() == nil || len(value.GetAllowedActions()) != 0 {
		return engine.State{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "state header or derived actions are malformed"}
	}
	config, err := configFromProto(value.GetConfig())
	if err != nil {
		return engine.State{}, err
	}
	players, err := playersFromProto(value.GetPlayers(), config)
	if err != nil {
		return engine.State{}, err
	}
	phase, ok := phaseFromProto(value.GetPhase())
	if !ok {
		return engine.State{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "state phase is unknown"}
	}
	state := engine.State{
		SchemaVersion: value.GetSchemaVersion(), Phase: phase, Round: value.GetRound(),
		HostUserID: value.GetHostUserId(), Config: config, Players: players,
		TargetUserID: value.GetTargetUserId(), TargetRerollCount: value.GetTargetRerollCount(),
		TargetStreak: value.GetTargetStreak(), MatchResolutionCount: value.GetMatchResolutionCount(),
		ActionDeadlineUnixMillis: value.GetActionDeadlineUnixMillis(), FinishReason: value.GetFinishReason(),
	}
	if value.GetLastSettlement() != nil {
		settlement, settlementErr := settlementFromProto(value.GetLastSettlement(), config)
		if settlementErr != nil {
			return engine.State{}, settlementErr
		}
		state.LastSettlement = settlement
	}
	if len(value.GetRoundHistory()) > 32 {
		return engine.State{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "round history exceeds the engine buffer"}
	}
	state.RoundHistory = make([]engine.RoundSettlement, len(value.GetRoundHistory()))
	for index, summary := range value.GetRoundHistory() {
		settlement, settlementErr := settlementFromProto(summary, config)
		if settlementErr != nil {
			return engine.State{}, settlementErr
		}
		state.RoundHistory[index] = settlement
	}
	if len(value.GetRoundHistory()) != 0 && (value.GetLastSettlement() == nil || !proto.Equal(value.GetRoundHistory()[len(value.GetRoundHistory())-1], value.GetLastSettlement())) {
		return engine.State{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "round history disagrees with the latest settlement"}
	}
	state.MatchHistory = make([]engine.MatchResolution, len(value.GetMatchHistory()))
	for index, batch := range value.GetMatchHistory() {
		resolution, resolutionErr := matchBatchFromProto(batch, value.GetRound(), config, players)
		if resolutionErr != nil {
			return engine.State{}, resolutionErr
		}
		state.MatchHistory[index] = resolution
	}
	if len(value.GetMatchHistory()) == 0 {
		if value.GetLastMatchBatch() != nil {
			return engine.State{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "last match batch has no history"}
		}
	} else if value.GetLastMatchBatch() == nil || !proto.Equal(value.GetLastMatchBatch(), value.GetMatchHistory()[len(value.GetMatchHistory())-1]) {
		return engine.State{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "last match batch disagrees with history"}
	}
	return state, nil
}

func playersToProto(players []engine.PlayerState) []*meetv1.PlayerState {
	result := make([]*meetv1.PlayerState, len(players))
	for index, player := range players {
		result[index] = playerToProto(player)
	}
	return result
}

func playerToProto(player engine.PlayerState) *meetv1.PlayerState {
	return &meetv1.PlayerState{
		UserId: player.UserID, SeatIndex: player.SeatIndex, Active: player.Active,
		PenaltyTicks: uint32(player.PenaltyTicks), Dice: facesToProto(player.Hand.Raw[:]),
		NormalizedDice: facesToProto(player.Hand.Normalized[:]), HandClass: handClassToProto(player.Hand.Class),
		FullKey: rankKeyToProto(player.Hand.FullKey), TieKey: append([]int32(nil), player.Hand.TieKey[:]...),
		Special_235: player.Hand.Special235, Special_235Outcome: specialOutcomeToProto(player.Hand.SpecialContext),
		TargetedThisRound: player.TargetedThisRound, IncludedInCurrentResolution: player.IncludedInCurrentResolution,
	}
}

func playersFromProto(values []*meetv1.PlayerState, config engine.Config) ([]engine.PlayerState, error) {
	players := make([]engine.PlayerState, len(values))
	resolutionIndexes := make([]int, 0, len(values))
	classified := make([]engine.Hand, 0, len(values))
	for index, value := range values {
		if value == nil || value.GetUserId() == "" {
			return nil, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "state contains a malformed player"}
		}
		hand, base, err := handFromProto(value, config)
		if err != nil {
			return nil, err
		}
		players[index] = engine.PlayerState{
			UserID: value.GetUserId(), SeatIndex: value.GetSeatIndex(), Active: value.GetActive(),
			TargetedThisRound: value.GetTargetedThisRound(), IncludedInCurrentResolution: value.GetIncludedInCurrentResolution(),
			PenaltyTicks: dice.Ticks(value.GetPenaltyTicks()), Hand: hand,
		}
		if value.GetIncludedInCurrentResolution() {
			resolutionIndexes = append(resolutionIndexes, index)
			classified = append(classified, base)
		}
	}
	resolved := engine.Resolve235Context(classified)
	for index, playerIndex := range resolutionIndexes {
		if players[playerIndex].Hand != resolved[index] {
			return nil, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "active hand derived fields are stale"}
		}
	}
	return players, nil
}

func handFromProto(value *meetv1.PlayerState, config engine.Config) (engine.Hand, engine.Hand, error) {
	raw, err := facesFromProto(value.GetDice())
	if err != nil {
		return engine.Hand{}, engine.Hand{}, err
	}
	normalized, err := facesFromProto(value.GetNormalizedDice())
	if err != nil {
		return engine.Hand{}, engine.Hand{}, err
	}
	class, ok := handClassFromProto(value.GetHandClass())
	if !ok || len(value.GetFullKey()) == 0 || len(value.GetFullKey()) > 5 || len(value.GetTieKey()) != 2 {
		return engine.Hand{}, engine.Hand{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "player hand rank is malformed"}
	}
	context, ok := specialOutcomeFromProto(value.GetSpecial_235Outcome())
	if !ok {
		return engine.Hand{}, engine.Hand{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "player 235 outcome is unknown"}
	}
	base, err := engine.Classify(raw, config)
	if err != nil {
		return engine.Hand{}, engine.Hand{}, err
	}
	hand := engine.Hand{
		Raw: raw, Normalized: normalized, Class: class, FullKey: rankKeyFromProto(value.GetFullKey()),
		TieKey: [2]int32{value.GetTieKey()[0], value.GetTieKey()[1]}, WildUsed: base.WildUsed,
		Special235: value.GetSpecial_235(), SpecialContext: context,
	}
	return hand, base, nil
}

func rankKeyToProto(value engine.RankKey) []int32 {
	return append([]int32(nil), value.Values[:value.Length]...)
}

func rankKeyFromProto(values []int32) engine.RankKey {
	result := engine.RankKey{Length: uint8(len(values))}
	copy(result.Values[:], values)
	return result
}

func settlementToProto(value engine.RoundSettlement) *meetv1.RoundSummary {
	if value.Round == 0 {
		return nil
	}
	finalPlayers := publicPlayersToProto(value.Players)
	return &meetv1.RoundSummary{
		Round: value.Round, TargetUserId: value.TargetUserID, Outcome: outcomeToProto(value.Reason), Cause: causeToProto(value.Reason),
		TargetRerollCount: value.TargetRerollCount, TargetStreak: value.TargetStreak, MatchResolutionCount: value.MatchResolutionCount,
		FinalPlayers: finalPlayers, TargetHistoryUserIds: targetHistory(value.Players), Reason: value.Reason, Settled: true,
	}
}

func settlementFromProto(value *meetv1.RoundSummary, config engine.Config) (engine.RoundSettlement, error) {
	if value == nil || value.GetRound() == 0 || value.GetTargetUserId() == "" || value.GetReason() == "" || !value.GetSettled() ||
		value.GetOutcome() != outcomeToProto(value.GetReason()) || value.GetCause() != causeToProto(value.GetReason()) {
		return engine.RoundSettlement{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "round settlement is malformed"}
	}
	players, err := publicPlayersFromProto(value.GetFinalPlayers(), config)
	if err != nil {
		return engine.RoundSettlement{}, err
	}
	if !slices.Equal(value.GetTargetHistoryUserIds(), targetHistory(players)) || !slices.Contains(value.GetTargetHistoryUserIds(), value.GetTargetUserId()) {
		return engine.RoundSettlement{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "round target history is malformed"}
	}
	return engine.RoundSettlement{
		Round: value.GetRound(), TargetUserID: value.GetTargetUserId(), Reason: value.GetReason(),
		TargetRerollCount: value.GetTargetRerollCount(), TargetStreak: value.GetTargetStreak(),
		MatchResolutionCount: value.GetMatchResolutionCount(), Players: players,
	}, nil
}

func publicPlayersToProto(players []engine.PlayerState) []*meetv1.PublicPlayer {
	result := make([]*meetv1.PublicPlayer, len(players))
	for index, player := range players {
		result[index] = &meetv1.PublicPlayer{
			UserId: player.UserID, SeatIndex: player.SeatIndex, Active: player.Active, PenaltyTicks: uint32(player.PenaltyTicks),
			Dice: facesToProto(player.Hand.Raw[:]), NormalizedDice: facesToProto(player.Hand.Normalized[:]),
			HandClass: handClassToProto(player.Hand.Class), Special_235: player.Hand.Special235,
			Special_235Outcome: specialOutcomeToProto(player.Hand.SpecialContext), TargetedThisRound: player.TargetedThisRound,
		}
	}
	return result
}

func publicPlayersFromProto(values []*meetv1.PublicPlayer, config engine.Config) ([]engine.PlayerState, error) {
	players := make([]engine.PlayerState, len(values))
	baseHands := make([]engine.Hand, len(values))
	inactiveIndexes := make([]int, 0, len(values))
	for index, value := range values {
		if value == nil || value.GetUserId() == "" {
			return nil, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "settlement contains a malformed player"}
		}
		raw, err := facesFromProto(value.GetDice())
		if err != nil {
			return nil, err
		}
		base, err := engine.Classify(raw, config)
		if err != nil {
			return nil, err
		}
		expected, err := publicExpectedHand(value, base, config)
		if err != nil {
			return nil, err
		}
		baseHands[index] = base
		players[index] = engine.PlayerState{
			UserID: value.GetUserId(), SeatIndex: value.GetSeatIndex(), Active: value.GetActive(),
			TargetedThisRound: value.GetTargetedThisRound(), IncludedInCurrentResolution: value.GetActive(),
			PenaltyTicks: dice.Ticks(value.GetPenaltyTicks()), Hand: expected,
		}
		if !value.GetActive() {
			inactiveIndexes = append(inactiveIndexes, index)
		}
	}
	if !restoreResolutionMembership(players, baseHands, inactiveIndexes) {
		return nil, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "settlement resolution membership cannot reproduce public hands"}
	}
	if !proto.Equal(&meetv1.RoundSummary{FinalPlayers: values}, &meetv1.RoundSummary{FinalPlayers: publicPlayersToProto(players)}) {
		return nil, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "settlement public hand fields are stale"}
	}
	return players, nil
}

// publicExpectedHand reconstructs internal keys from the safe classification
// fields retained by a settlement summary.
func publicExpectedHand(value *meetv1.PublicPlayer, base engine.Hand, config engine.Config) (engine.Hand, error) {
	context, ok := specialOutcomeFromProto(value.GetSpecial_235Outcome())
	if !ok {
		return engine.Hand{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "settlement 235 outcome is unknown"}
	}
	if base.Special235 && context == engine.Special235BeatsLeopards {
		leopard, err := engine.Classify([3]dice.Face{6, 6, 6}, config)
		if err != nil {
			return engine.Hand{}, err
		}
		base = engine.Resolve235Context([]engine.Hand{base, leopard})[0]
	}
	normalized, err := facesFromProto(value.GetNormalizedDice())
	if err != nil {
		return engine.Hand{}, err
	}
	class, classOK := handClassFromProto(value.GetHandClass())
	if !classOK || base.Normalized != normalized || base.Class != class || base.Special235 != value.GetSpecial_235() || base.SpecialContext != context {
		return engine.Hand{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "settlement public hand is stale"}
	}
	return base, nil
}

// restoreResolutionMembership deterministically selects the smallest inactive
// subset needed to reproduce all contextual classifications. Active seats are
// always included; inactive seats are considered in stable seat order.
func restoreResolutionMembership(players []engine.PlayerState, baseHands []engine.Hand, inactiveIndexes []int) bool {
	for mask := 0; mask < 1<<len(inactiveIndexes); mask++ {
		indexes := make([]int, 0, len(players))
		hands := make([]engine.Hand, 0, len(players))
		for index := range players {
			included := players[index].Active
			if !included {
				for bit, inactiveIndex := range inactiveIndexes {
					if inactiveIndex == index && mask&(1<<bit) != 0 {
						included = true
						break
					}
				}
			}
			players[index].IncludedInCurrentResolution = included
			if included {
				indexes = append(indexes, index)
				hands = append(hands, baseHands[index])
			}
		}
		resolved := engine.Resolve235Context(hands)
		matches := true
		for index, playerIndex := range indexes {
			matches = matches && resolved[index] == players[playerIndex].Hand
		}
		if matches {
			return true
		}
	}
	return false
}

func targetHistory(players []engine.PlayerState) []string {
	result := make([]string, 0, len(players))
	for _, player := range players {
		if player.TargetedThisRound {
			result = append(result, player.UserID)
		}
	}
	return result
}

func matchBatchToProto(round uint32, value engine.MatchResolution, config engine.Config, players []engine.PlayerState) *meetv1.MatchBatch {
	batch := &meetv1.MatchBatch{Round: round, BatchIndex: value.Batch, ResolutionCount: value.Batch, Capped: value.Capped}
	rerollSet := make(map[string]struct{})
	for _, group := range value.Groups {
		encoded := &meetv1.MatchGroup{
			Kind: matchKindToProto(group.Kind), UserIds: append([]string(nil), group.UserIDs...),
			PenaltyTicks: uint32(config.MatchPenaltyTicks), WeakestUserId: group.WeakestUserID,
		}
		if group.Kind == engine.MatchLow {
			encoded.WeakExtraPenaltyTicks = uint32(config.WeakExtraPenaltyTicks)
		}
		if value.Capped {
			encoded.PenaltyTicks = 0
			encoded.WeakExtraPenaltyTicks = 0
		}
		batch.Groups = append(batch.Groups, encoded)
		if !value.Capped {
			for _, userID := range group.UserIDs {
				rerollSet[userID] = struct{}{}
			}
		}
	}
	for _, player := range players {
		if _, rerolled := rerollSet[player.UserID]; rerolled {
			batch.RerolledUserIds = append(batch.RerolledUserIds, player.UserID)
		}
	}
	return batch
}

func matchBatchFromProto(value *meetv1.MatchBatch, round uint32, config engine.Config, players []engine.PlayerState) (engine.MatchResolution, error) {
	if value == nil || value.GetRound() != round || value.GetBatchIndex() != value.GetResolutionCount() || len(value.GetGroups()) == 0 {
		return engine.MatchResolution{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "match batch is malformed"}
	}
	resolution := engine.MatchResolution{Batch: value.GetBatchIndex(), Capped: value.GetCapped()}
	for _, group := range value.GetGroups() {
		if group == nil || len(group.GetUserIds()) < 2 {
			return engine.MatchResolution{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "match group is malformed"}
		}
		kind, ok := matchKindFromProto(group.GetKind())
		if !ok {
			return engine.MatchResolution{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "match kind is unknown"}
		}
		expectedPenalty := uint32(config.MatchPenaltyTicks)
		expectedWeakExtra := uint32(0)
		if value.GetCapped() {
			expectedPenalty = 0
		} else if kind == engine.MatchLow {
			expectedWeakExtra = uint32(config.WeakExtraPenaltyTicks)
		}
		if group.GetPenaltyTicks() != expectedPenalty || group.GetWeakExtraPenaltyTicks() != expectedWeakExtra {
			return engine.MatchResolution{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "match weak penalty is malformed"}
		}
		resolution.Groups = append(resolution.Groups, engine.MatchGroup{Kind: kind, UserIDs: append([]string(nil), group.GetUserIds()...), WeakestUserID: group.GetWeakestUserId()})
	}
	if !proto.Equal(value, matchBatchToProto(round, resolution, config, players)) {
		return engine.MatchResolution{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "match batch derived fields are stale"}
	}
	return resolution, nil
}

func actionTimerToProto(value *engine.ActionTimer) *meetv1.ActionTimer {
	if value == nil {
		return nil
	}
	return &meetv1.ActionTimer{
		Round: value.Round, Phase: meetv1.Phase_PHASE_TARGET_DECISION, TargetUserId: value.TargetUserID,
		TargetRerollCount: value.TargetRerollCount, TargetStreak: value.TargetStreak,
		MatchResolutionCount: value.MatchResolutionCount, DeadlineUnixMillis: value.DeadlineUnixMillis,
	}
}

func actionTimerFromProto(value *meetv1.ActionTimer) (engine.ActionTimer, error) {
	if value == nil || value.GetRound() == 0 || value.GetPhase() != meetv1.Phase_PHASE_TARGET_DECISION || value.GetTargetUserId() == "" || value.GetDeadlineUnixMillis() <= 0 {
		return engine.ActionTimer{}, &engine.RuleError{Code: engine.CodeTimerMismatch, Detail: "timer token is malformed"}
	}
	return engine.ActionTimer{
		Round: value.GetRound(), TargetUserID: value.GetTargetUserId(), TargetRerollCount: value.GetTargetRerollCount(),
		TargetStreak: value.GetTargetStreak(), MatchResolutionCount: value.GetMatchResolutionCount(), DeadlineUnixMillis: value.GetDeadlineUnixMillis(),
	}, nil
}

func facesToProto(values []dice.Face) []uint32 {
	result := make([]uint32, len(values))
	for index, value := range values {
		result[index] = uint32(value)
	}
	return result
}

func facesFromProto(values []uint32) ([3]dice.Face, error) {
	if len(values) != int(engine.DicePerPlayer) {
		return [3]dice.Face{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "hand must contain exactly three dice"}
	}
	result := [3]dice.Face{}
	for index, value := range values {
		face := dice.Face(value)
		if !face.Valid() {
			return [3]dice.Face{}, &engine.RuleError{Code: engine.CodeInvalidState, Detail: "hand contains an invalid die face"}
		}
		result[index] = face
	}
	return result, nil
}

func phaseToProto(value engine.Phase) meetv1.Phase {
	switch value {
	case engine.PhaseTargetDecision:
		return meetv1.Phase_PHASE_TARGET_DECISION
	case engine.PhaseFinished:
		return meetv1.Phase_PHASE_FINISHED
	default:
		return meetv1.Phase_PHASE_UNSPECIFIED
	}
}

func phaseFromProto(value meetv1.Phase) (engine.Phase, bool) {
	switch value {
	case meetv1.Phase_PHASE_TARGET_DECISION:
		return engine.PhaseTargetDecision, true
	case meetv1.Phase_PHASE_FINISHED:
		return engine.PhaseFinished, true
	default:
		return 0, false
	}
}

func handClassToProto(value engine.HandClass) meetv1.HandClass {
	switch value {
	case engine.HandSingle:
		return meetv1.HandClass_HAND_CLASS_SINGLE
	case engine.HandPair:
		return meetv1.HandClass_HAND_CLASS_PAIR
	case engine.HandStraight:
		return meetv1.HandClass_HAND_CLASS_STRAIGHT
	case engine.HandLeopard:
		return meetv1.HandClass_HAND_CLASS_LEOPARD
	case engine.HandSpecial235:
		return meetv1.HandClass_HAND_CLASS_SPECIAL_235
	default:
		return meetv1.HandClass_HAND_CLASS_UNSPECIFIED
	}
}

func handClassFromProto(value meetv1.HandClass) (engine.HandClass, bool) {
	switch value {
	case meetv1.HandClass_HAND_CLASS_SINGLE:
		return engine.HandSingle, true
	case meetv1.HandClass_HAND_CLASS_PAIR:
		return engine.HandPair, true
	case meetv1.HandClass_HAND_CLASS_STRAIGHT:
		return engine.HandStraight, true
	case meetv1.HandClass_HAND_CLASS_LEOPARD:
		return engine.HandLeopard, true
	case meetv1.HandClass_HAND_CLASS_SPECIAL_235:
		return engine.HandSpecial235, true
	default:
		return 0, false
	}
}

func specialOutcomeToProto(value engine.Special235Context) meetv1.Special235Outcome {
	switch value {
	case engine.Special235None:
		return meetv1.Special235Outcome_SPECIAL235_OUTCOME_NOT_APPLICABLE
	case engine.Special235Minimum:
		return meetv1.Special235Outcome_SPECIAL235_OUTCOME_MINIMUM_SINGLE
	case engine.Special235BeatsLeopards:
		return meetv1.Special235Outcome_SPECIAL235_OUTCOME_BEATS_LEOPARDS
	default:
		return meetv1.Special235Outcome_SPECIAL235_OUTCOME_UNSPECIFIED
	}
}

func specialOutcomeFromProto(value meetv1.Special235Outcome) (engine.Special235Context, bool) {
	switch value {
	case meetv1.Special235Outcome_SPECIAL235_OUTCOME_NOT_APPLICABLE:
		return engine.Special235None, true
	case meetv1.Special235Outcome_SPECIAL235_OUTCOME_MINIMUM_SINGLE:
		return engine.Special235Minimum, true
	case meetv1.Special235Outcome_SPECIAL235_OUTCOME_BEATS_LEOPARDS:
		return engine.Special235BeatsLeopards, true
	default:
		return 0, false
	}
}

func matchKindToProto(value engine.MatchKind) meetv1.MatchKind {
	switch value {
	case engine.MatchExact:
		return meetv1.MatchKind_MATCH_KIND_EXACT
	case engine.MatchHigh:
		return meetv1.MatchKind_MATCH_KIND_HIGHEST
	case engine.MatchLow:
		return meetv1.MatchKind_MATCH_KIND_LOWEST
	default:
		return meetv1.MatchKind_MATCH_KIND_UNSPECIFIED
	}
}

func matchKindFromProto(value meetv1.MatchKind) (engine.MatchKind, bool) {
	switch value {
	case meetv1.MatchKind_MATCH_KIND_EXACT:
		return engine.MatchExact, true
	case meetv1.MatchKind_MATCH_KIND_HIGHEST:
		return engine.MatchHigh, true
	case meetv1.MatchKind_MATCH_KIND_LOWEST:
		return engine.MatchLow, true
	default:
		return "", false
	}
}

func outcomeToProto(reason string) meetv1.RoundOutcome {
	switch reason {
	case "stand", "timeout_stand":
		return meetv1.RoundOutcome_ROUND_OUTCOME_STOOD
	case "target_surpassed":
		return meetv1.RoundOutcome_ROUND_OUTCOME_TARGET_EXCEEDED_ALL
	case "reroll_limit":
		return meetv1.RoundOutcome_ROUND_OUTCOME_REROLL_LIMIT_REACHED
	case "target_revoked":
		return meetv1.RoundOutcome_ROUND_OUTCOME_TARGET_REVOKED
	case engine.FinishHostRequested, engine.FinishInsufficientParticipants, engine.FinishPlatformCancelled:
		return meetv1.RoundOutcome_ROUND_OUTCOME_SESSION_FINISHED
	default:
		return meetv1.RoundOutcome_ROUND_OUTCOME_UNSPECIFIED
	}
}

func causeToProto(reason string) meetv1.ResolutionCause {
	switch reason {
	case "round_started", "initial_target":
		return meetv1.ResolutionCause_RESOLUTION_CAUSE_INITIAL_ROLL
	case "match_reroll", "match_resolution_capped", string(engine.MatchExact), string(engine.MatchHigh), string(engine.MatchLow):
		return meetv1.ResolutionCause_RESOLUTION_CAUSE_MATCH_REROLL
	case "target_reroll", "post_reroll_target", "target_surpassed", "reroll_limit":
		return meetv1.ResolutionCause_RESOLUTION_CAUSE_PLAYER_REROLL
	case "stand":
		return meetv1.ResolutionCause_RESOLUTION_CAUSE_PLAYER_STAND
	case "timeout_stand":
		return meetv1.ResolutionCause_RESOLUTION_CAUSE_TIMEOUT_STAND
	case "participant_revoked":
		return meetv1.ResolutionCause_RESOLUTION_CAUSE_PARTICIPANT_REVOKED
	case "target_revoked":
		return meetv1.ResolutionCause_RESOLUTION_CAUSE_TARGET_REVOKED
	case engine.FinishHostRequested:
		return meetv1.ResolutionCause_RESOLUTION_CAUSE_HOST_FINISHED
	case engine.FinishInsufficientParticipants:
		return meetv1.ResolutionCause_RESOLUTION_CAUSE_INSUFFICIENT_PLAYERS
	case engine.FinishPlatformCancelled:
		return meetv1.ResolutionCause_RESOLUTION_CAUSE_PLATFORM_CANCELLED
	default:
		return meetv1.ResolutionCause_RESOLUTION_CAUSE_UNSPECIFIED
	}
}

func malformed(detail string) error {
	return &engine.RuleError{Code: engine.CodeMalformedPayload, Detail: detail}
}
