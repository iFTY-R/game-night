package projection

import (
	"github.com/iFTY-R/game-night/games/meet-by-chance/engine"
	meetv1 "github.com/iFTY-R/game-night/games/meet-by-chance/gen/go/game/meet_by_chance/v1"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"github.com/iFTY-R/game-night/sdk/go/game/dice"
)

const (
	ActionReroll game.Identifier = "round.reroll"
	ActionStand  game.Identifier = "round.stand"
)

// BuildView creates a viewer-safe public view. The engine's authoritative
// PlayerState is never assigned to the deprecated wire field because it carries
// comparison keys that are not part of the public game surface.
func BuildView(state engine.State, viewer game.Viewer) (*meetv1.View, []game.Identifier, error) {
	if err := state.Validate(); err != nil || !viewer.Valid() || viewer.Kind == game.ViewerReplay {
		return nil, nil, projectionError("state or viewer is invalid")
	}
	if viewer.Kind == game.ViewerPlayer {
		index := playerIndex(state, string(viewer.UserID))
		if index < 0 || state.Players[index].SeatIndex != viewer.SeatIndex {
			return nil, nil, projectionError("player viewer does not own the requested seat")
		}
	}
	actions := allowedActions(state, viewer)
	view := &meetv1.View{
		Phase:                    phaseToProto(state.Phase),
		Round:                    state.Round,
		TargetUserId:             state.TargetUserID,
		TargetRerollCount:        state.TargetRerollCount,
		TargetRerollLimit:        state.Config.TargetRerollLimit,
		TargetStreak:             state.TargetStreak,
		MatchResolutionCount:     state.MatchResolutionCount,
		MatchResolutionLimit:     state.Config.MatchResolutionLimit,
		ActionDeadlineUnixMillis: state.ActionDeadlineUnixMillis,
		FinishReason:             state.FinishReason,
		PublicPlayers:            publicPlayers(state.Players),
		Config:                   configToProto(state.Config),
		ViewerIsHost: viewer.Kind == game.ViewerPlayer &&
			string(viewer.UserID) == state.HostUserID,
	}
	for _, action := range actions {
		view.AllowedActions = append(view.AllowedActions, string(action))
	}
	if state.LastSettlement.Round != 0 {
		view.LastSettlement = settlementToProto(state.LastSettlement)
	}
	view.RecentRounds = make([]*meetv1.RoundSummary, len(state.RoundHistory))
	for index, settlement := range state.RoundHistory {
		view.RecentRounds[index] = settlementToProto(settlement)
	}
	if len(state.MatchHistory) != 0 {
		view.LastMatchBatch = matchBatchToProto(state, state.MatchHistory[len(state.MatchHistory)-1])
	}
	return view, actions, nil
}

func allowedActions(state engine.State, viewer game.Viewer) []game.Identifier {
	if viewer.Kind != game.ViewerPlayer || state.Phase != engine.PhaseTargetDecision || string(viewer.UserID) != state.TargetUserID {
		return nil
	}
	if state.TargetRerollCount >= state.Config.TargetRerollLimit {
		return []game.Identifier{ActionStand}
	}
	return []game.Identifier{ActionReroll, ActionStand}
}

func publicPlayers(players []engine.PlayerState) []*meetv1.PublicPlayer {
	result := make([]*meetv1.PublicPlayer, len(players))
	for index, player := range players {
		result[index] = publicPlayer(player)
	}
	return result
}

func publicPlayer(player engine.PlayerState) *meetv1.PublicPlayer {
	return &meetv1.PublicPlayer{
		UserId: string(player.UserID), SeatIndex: player.SeatIndex, Active: player.Active,
		PenaltyTicks: uint32(player.PenaltyTicks), Dice: faces(player.Hand.Raw[:]),
		NormalizedDice: faces(player.Hand.Normalized[:]), HandClass: handClassToProto(player.Hand.Class),
		Special_235: player.Hand.Special235, Special_235Outcome: specialOutcomeToProto(player.Hand.SpecialContext),
		TargetedThisRound: player.TargetedThisRound,
	}
}

func configToProto(value engine.Config) *meetv1.Config {
	return &meetv1.Config{
		Straight_123: value.Straight123, Straight_234: value.Straight234, Straight_345: value.Straight345, Straight_456: value.Straight456,
		Special_235Enabled: value.Special235Enabled, OnesWild: value.OnesWild,
		TargetPenaltyTicks: uint32(value.TargetPenaltyTicks), RerollPenaltyTicks: uint32(value.RerollPenaltyTicks),
		MatchPenaltyTicks: uint32(value.MatchPenaltyTicks), WeakExtraPenaltyTicks: uint32(value.WeakExtraPenaltyTicks),
		TargetRerollLimit: value.TargetRerollLimit, MatchResolutionLimit: value.MatchResolutionLimit,
		ActionTimeoutSeconds: value.ActionTimeoutSeconds,
	}
}

func settlementToProto(value engine.RoundSettlement) *meetv1.RoundSummary {
	if value.Round == 0 {
		return nil
	}
	return &meetv1.RoundSummary{
		Round: value.Round, TargetUserId: value.TargetUserID, Outcome: outcomeToProto(value.Reason),
		Cause: causeToProto(value.Reason), TargetRerollCount: value.TargetRerollCount,
		MatchResolutionCount: value.MatchResolutionCount, FinalPlayers: publicPlayers(value.Players),
		TargetHistoryUserIds: targetHistory(value.Players), Reason: value.Reason, Settled: true,
		TargetStreak: value.TargetStreak,
	}
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

func matchBatchToProto(state engine.State, value engine.MatchResolution) *meetv1.MatchBatch {
	batch := &meetv1.MatchBatch{Round: state.Round, BatchIndex: value.Batch, ResolutionCount: value.Batch, Capped: value.Capped}
	rerollSet := make(map[string]struct{})
	for _, group := range value.Groups {
		encoded := &meetv1.MatchGroup{Kind: matchKindToProto(group.Kind), UserIds: append([]string(nil), group.UserIDs...), WeakestUserId: group.WeakestUserID, PenaltyTicks: uint32(state.Config.MatchPenaltyTicks)}
		if group.Kind == engine.MatchLow {
			encoded.WeakExtraPenaltyTicks = uint32(state.Config.WeakExtraPenaltyTicks)
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
	for _, player := range state.Players {
		if _, rerolled := rerollSet[player.UserID]; rerolled {
			batch.RerolledUserIds = append(batch.RerolledUserIds, player.UserID)
		}
	}
	return batch
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

func specialOutcomeToProto(value engine.Special235Context) meetv1.Special235Outcome {
	switch value {
	case engine.Special235Minimum:
		return meetv1.Special235Outcome_SPECIAL235_OUTCOME_MINIMUM_SINGLE
	case engine.Special235BeatsLeopards:
		return meetv1.Special235Outcome_SPECIAL235_OUTCOME_BEATS_LEOPARDS
	default:
		return meetv1.Special235Outcome_SPECIAL235_OUTCOME_NOT_APPLICABLE
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
	default:
		return meetv1.RoundOutcome_ROUND_OUTCOME_UNSPECIFIED
	}
}

func causeToProto(reason string) meetv1.ResolutionCause {
	switch reason {
	case "timeout_stand":
		return meetv1.ResolutionCause_RESOLUTION_CAUSE_TIMEOUT_STAND
	case "target_revoked":
		return meetv1.ResolutionCause_RESOLUTION_CAUSE_TARGET_REVOKED
	case "target_reroll", "target_surpassed", "reroll_limit", "post_reroll_target":
		return meetv1.ResolutionCause_RESOLUTION_CAUSE_PLAYER_REROLL
	case "stand":
		return meetv1.ResolutionCause_RESOLUTION_CAUSE_PLAYER_STAND
	case "initial_target", "round_started":
		return meetv1.ResolutionCause_RESOLUTION_CAUSE_INITIAL_ROLL
	case "insufficient_participants":
		return meetv1.ResolutionCause_RESOLUTION_CAUSE_INSUFFICIENT_PLAYERS
	case "platform_cancelled":
		return meetv1.ResolutionCause_RESOLUTION_CAUSE_PLATFORM_CANCELLED
	default:
		return meetv1.ResolutionCause_RESOLUTION_CAUSE_MATCH_REROLL
	}
}

func faces(values []dice.Face) []uint32 {
	result := make([]uint32, len(values))
	for index, value := range values {
		result[index] = uint32(value)
	}
	return result
}

func playerIndex(state engine.State, userID string) int {
	for index, player := range state.Players {
		if player.UserID == userID {
			return index
		}
	}
	return -1
}

func projectionError(detail string) error {
	return &engine.RuleError{Code: engine.CodeProjectionUnavailable, Detail: detail}
}
