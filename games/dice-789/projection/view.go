package projection

import (
	"strings"

	"github.com/iFTY-R/game-night/games/dice-789/engine"
	dice789v1 "github.com/iFTY-R/game-night/games/dice-789/gen/go/game/dice789/v1"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

const (
	ActionRoll          game.Identifier = "turn.roll"
	ActionConfirmLanded game.Identifier = "turn.confirm_landed"
	ActionAdd           game.Identifier = "pot.add"
	ActionChooseTarget  game.Identifier = "turn.choose_target"
	ActionReroll        game.Identifier = "turn.reroll"
	ActionPass          game.Identifier = "turn.pass"
	ActionReportDropped game.Identifier = "turn.report_dropped"
)

// BuildView reconstructs a complete public view and authorizes actions for exactly one viewer.
func BuildView(state engine.State, viewer game.Viewer) (*dice789v1.View, []game.Identifier, error) {
	if err := state.Validate(); err != nil || !viewer.Valid() || viewer.Kind == game.ViewerReplay {
		return nil, nil, projectionError("state or viewer is invalid")
	}
	viewerID := string(viewer.UserID)
	viewerPlayer := -1
	if viewer.Kind == game.ViewerPlayer {
		viewerPlayer = playerIndex(state, viewerID)
		if viewerPlayer < 0 || state.Players[viewerPlayer].SeatIndex != viewer.SeatIndex {
			return nil, nil, projectionError("player viewer does not own the requested seat")
		}
	}
	actions := allowedActions(state, viewerID, viewer.Kind == game.ViewerPlayer && viewerPlayer >= 0 && state.Players[viewerPlayer].Active)
	view := &dice789v1.View{
		Phase:                    phaseToProto(state.Phase),
		Turn:                     state.Turn,
		Players:                  playersToProto(state.Players),
		CurrentUserId:            state.CurrentUserID,
		TargetUserId:             state.TargetUserID,
		Direction:                uint32(state.Direction),
		Pool:                     poolToProto(state.Pool),
		TotalPoolTicks:           uint32(state.TotalPoolTicks),
		DieOne:                   state.DieOne,
		DieTwo:                   state.DieTwo,
		Sum:                      state.Sum,
		ActionDeadlineUnixMillis: state.ActionDeadlineUnixMillis,
		FinishReason:             state.FinishReason,
		SourceUserId:             state.SourceUserID,
		Effect:                   resultToProto(state.PendingResult, state),
		Config:                   configToProto(state.Config),
		ViewerIsHost:             viewer.Kind == game.ViewerPlayer && viewerID == state.HostUserID,
	}
	for _, action := range actions {
		view.AllowedActions = append(view.AllowedActions, string(action))
	}
	if state.LastSettlement.Turn != 0 {
		view.LastSettlement = settlementToProto(state.LastSettlement, state)
		view.RecentTurns = []*dice789v1.TurnSummary{settlementToProto(state.LastSettlement, state)}
	}
	if containsAction(actions, ActionAdd) || containsAction(actions, ActionChooseTarget) {
		view.ActionConstraints = constraintsToProto(state)
	}
	return view, actions, nil
}

func allowedActions(state engine.State, viewerID string, active bool) []game.Identifier {
	if !active || state.Phase == engine.PhaseFinished {
		return nil
	}
	switch state.Phase {
	case engine.PhaseAwaitingRoll:
		if state.CurrentUserID == viewerID {
			return []game.Identifier{ActionRoll}
		}
	case engine.PhaseResultPending:
		if state.HostUserID == viewerID {
			return []game.Identifier{ActionConfirmLanded, ActionReportDropped}
		}
	case engine.PhaseAwaitingAdd:
		if state.CurrentUserID == viewerID {
			return []game.Identifier{ActionAdd}
		}
	case engine.PhaseAwaitingTarget:
		if state.CurrentUserID == viewerID {
			return []game.Identifier{ActionChooseTarget}
		}
	case engine.PhaseAwaitingContinue:
		if state.CurrentUserID != viewerID {
			return nil
		}
		switch state.Config.ContinueMode {
		case engine.ContinueForcedReroll:
			return []game.Identifier{ActionReroll}
		case engine.ContinueForcedPass:
			return []game.Identifier{ActionPass}
		default:
			return []game.Identifier{ActionReroll, ActionPass}
		}
	}
	return nil
}

func constraintsToProto(state engine.State) *dice789v1.ActionConstraints {
	switch state.Phase {
	case engine.PhaseAwaitingAdd:
		capacity := uint32(uint64(state.Config.LayerCapacityTicks) * uint64(state.Config.MaxLayers))
		remaining := capacity - uint32(state.TotalPoolTicks)
		minimum := uint32(0)
		if remaining > 0 {
			minimum = uint32(state.Config.AddStepTicks)
			if remaining < minimum {
				minimum = remaining
			}
		}
		return &dice789v1.ActionConstraints{
			MinimumAddTicks: minimum, MaximumAddTicks: remaining, AddStepTicks: uint32(state.Config.AddStepTicks),
			AllowCapacityRemainder: remaining > 0 && remaining%uint32(state.Config.AddStepTicks) != 0,
		}
	case engine.PhaseAwaitingTarget:
		ids := make([]string, 0, len(state.Players))
		for _, player := range state.Players {
			if player.Active && player.UserID != state.SourceUserID {
				ids = append(ids, player.UserID)
			}
		}
		return &dice789v1.ActionConstraints{TargetUserIds: ids}
	default:
		return nil
	}
}

func playersToProto(values []engine.PlayerState) []*dice789v1.PlayerState {
	players := make([]*dice789v1.PlayerState, len(values))
	for index, value := range values {
		players[index] = &dice789v1.PlayerState{UserId: value.UserID, SeatIndex: value.SeatIndex, Active: value.Active, PenaltyTicks: uint32(value.PenaltyTicks)}
	}
	return players
}

func poolToProto(values []engine.PoolLayer) []*dice789v1.PoolLayer {
	layers := make([]*dice789v1.PoolLayer, len(values))
	for index, value := range values {
		layers[index] = &dice789v1.PoolLayer{Ticks: uint32(value.Ticks), Index: uint32(index)}
	}
	return layers
}

func configToProto(value engine.Config) *dice789v1.Config {
	mode := dice789v1.ContinueMode_CONTINUE_MODE_OPTIONAL
	if value.ContinueMode == engine.ContinueForcedReroll {
		mode = dice789v1.ContinueMode_CONTINUE_MODE_FORCED_REROLL
	} else if value.ContinueMode == engine.ContinueForcedPass {
		mode = dice789v1.ContinueMode_CONTINUE_MODE_FORCED_PASS
	}
	return &dice789v1.Config{
		InitialPoolTicks: uint32(value.InitialPoolTicks), LayerCapacityTicks: uint32(value.LayerCapacityTicks), AddStepTicks: uint32(value.AddStepTicks),
		MaxLayers: value.MaxLayers, StackedPool: value.StackedPool, OrdinaryPairsReverse: value.OrdinaryPairsReverse,
		DoubleOneEnabled: value.DoubleOneEnabled, DoubleFourEnabled: value.DoubleFourEnabled, DoubleSixEnabled: value.DoubleSixEnabled,
		ContinueMode: mode, LastDigitMatch: value.LastDigitMatch, ActionTimeoutSeconds: value.ActionTimeoutSeconds,
		DropReportWindowSeconds: value.DropReportWindowSeconds,
	}
}

func settlementToProto(value engine.TurnSettlement, state engine.State) *dice789v1.TurnSummary {
	effect := resultToProto(value.Result, engine.State{})
	if value.Result == engine.ResultOrdinaryPair {
		if value.DirectionBefore != value.DirectionAfter {
			effect = dice789v1.Effect_EFFECT_ORDINARY_PAIR_REVERSE
		} else {
			effect = dice789v1.Effect_EFFECT_ORDINARY_PAIR_REROLL
		}
	}
	operator := ""
	dropReason := ""
	if value.Result == engine.ResultDropped {
		operator = state.HostUserID
		dropReason = value.DropReason
	}
	summary := &dice789v1.TurnSummary{
		Turn: value.Turn, SourceUserId: value.SourceUserID, DieOne: value.DieOne, DieTwo: value.DieTwo, Sum: value.Sum,
		Effect: effect, TargetUserId: value.TargetUserID, PoolBeforeTicks: uint32(value.PoolBeforeTicks), PoolAfterTicks: uint32(value.PoolAfterTicks),
		PenaltyUserId: value.PenaltyUserID, PenaltyTicks: uint32(value.PenaltyTicks), DirectionBefore: uint32(value.DirectionBefore),
		DirectionAfter: uint32(value.DirectionAfter), NextUserId: value.NextUserID, Outcome: outcomeToProto(value), Cause: causeToProto(value.Reason),
		DroppedReported: value.Result == engine.ResultDropped, DropOperatorUserId: operator, DropReason: dropReason,
		ResolutionReason: value.Reason, PoolBeforeLayers: poolToProto(value.PoolBefore), PoolAfterLayers: poolToProto(value.PoolAfter), AuditRef: value.AuditRef,
	}
	if state.Phase == engine.PhaseFinished && value.Turn == state.Turn && value.NextUserID == "" {
		summary.Outcome = dice789v1.TurnOutcome_TURN_OUTCOME_SESSION_FINISHED
		summary.Cause = finishCause(state.FinishReason)
	}
	return summary
}

func resultToProto(value engine.ResultKind, state engine.State) dice789v1.Effect {
	switch value {
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
	case engine.ResultOrdinaryPair:
		active := 0
		for _, player := range state.Players {
			if player.Active {
				active++
			}
		}
		if active >= 3 && state.Config.OrdinaryPairsReverse {
			return dice789v1.Effect_EFFECT_ORDINARY_PAIR_REVERSE
		}
		return dice789v1.Effect_EFFECT_ORDINARY_PAIR_REROLL
	case engine.ResultDropped:
		return dice789v1.Effect_EFFECT_DROPPED_DRAIN_POOL
	case engine.ResultOther, engine.ResultRollTimeout, engine.ResultCancelled:
		return dice789v1.Effect_EFFECT_PASS
	default:
		return dice789v1.Effect_EFFECT_UNSPECIFIED
	}
}

func phaseToProto(value engine.Phase) dice789v1.Phase {
	switch value {
	case engine.PhaseAwaitingRoll:
		return dice789v1.Phase_PHASE_AWAITING_ROLL
	case engine.PhaseResultPending:
		return dice789v1.Phase_PHASE_RESULT_PENDING
	case engine.PhaseAwaitingAdd:
		return dice789v1.Phase_PHASE_AWAITING_ADD
	case engine.PhaseAwaitingTarget:
		return dice789v1.Phase_PHASE_AWAITING_TARGET
	case engine.PhaseAwaitingContinue:
		return dice789v1.Phase_PHASE_AWAITING_CONTINUE
	case engine.PhaseFinished:
		return dice789v1.Phase_PHASE_FINISHED
	default:
		return dice789v1.Phase_PHASE_UNSPECIFIED
	}
}

func outcomeToProto(value engine.TurnSettlement) dice789v1.TurnOutcome {
	if strings.Contains(value.Reason, "reroll") || value.Result == engine.ResultDoubleFour ||
		value.Result == engine.ResultOrdinaryPair && value.NextUserID == value.SourceUserID {
		return dice789v1.TurnOutcome_TURN_OUTCOME_REROLL
	}
	if strings.Contains(value.Reason, "revoked") || value.Result == engine.ResultCancelled {
		return dice789v1.TurnOutcome_TURN_OUTCOME_SOURCE_REVOKED
	}
	if value.Result == engine.ResultDoubleOne {
		return dice789v1.TurnOutcome_TURN_OUTCOME_TARGET_TAKES_TURN
	}
	return dice789v1.TurnOutcome_TURN_OUTCOME_PASS
}

func causeToProto(reason string) dice789v1.ResolutionCause {
	switch {
	case strings.Contains(reason, "timeout"):
		return dice789v1.ResolutionCause_RESOLUTION_CAUSE_TIMEOUT
	case reason == "confirmed":
		return dice789v1.ResolutionCause_RESOLUTION_CAUSE_HOST_CONFIRMED
	case reason == "dropped":
		return dice789v1.ResolutionCause_RESOLUTION_CAUSE_DROP_REPORTED
	case strings.Contains(reason, "revoked") || reason == "cancelled":
		return dice789v1.ResolutionCause_RESOLUTION_CAUSE_PARTICIPANT_REVOKED
	default:
		return dice789v1.ResolutionCause_RESOLUTION_CAUSE_PLAYER_ACTION
	}
}

func containsAction(values []game.Identifier, target game.Identifier) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
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
