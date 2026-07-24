package projection

import (
	"github.com/iFTY-R/game-night/games/three-rounds/engine"
	threeroundsv1 "github.com/iFTY-R/game-night/games/three-rounds/gen/go/game/three_rounds/v1"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

const ActionSubmitSelection game.Identifier = "round.submit_selection"

// BuildView creates one viewer-safe current state projection.
func BuildView(state engine.State, viewer game.Viewer) (*threeroundsv1.View, []game.Identifier, error) {
	if err := state.Validate(); err != nil || !viewer.Valid() || viewer.Kind == game.ViewerReplay {
		return nil, nil, projectionError("state or viewer is invalid")
	}
	playerIndex := -1
	if viewer.Kind == game.ViewerPlayer {
		playerIndex = viewPlayerIndex(state, string(viewer.UserID), viewer.SeatIndex)
		if playerIndex < 0 {
			return nil, nil, projectionError("player viewer does not own the requested seat")
		}
	}
	actions := allowedActions(state, playerIndex, viewer.Kind)
	view := &threeroundsv1.View{
		Phase:                   phaseToProto(state.Phase),
		CurrentRound:            state.CurrentRound,
		PhaseDeadlineUnixMillis: state.PhaseDeadlineUnixMillis,
		PhaseGeneration:         state.PhaseGeneration,
		Config:                  configToProto(state.Config),
		ViewerIsHost:            viewer.Kind == game.ViewerPlayer && string(viewer.UserID) == state.HostUserID,
		FinishReason:            finishReasonToProto(state.FinishReason),
		RoundHistory:            roundHistoryToProto(state.RoundHistory),
		PublicPlayers:           publicPlayersToProto(state),
	}
	if finalSummaryVisible(state) {
		view.FinalSummary = finalSummaryToProto(state.FinalSummary)
	}
	for _, action := range actions {
		view.AllowedActions = append(view.AllowedActions, string(action))
	}
	if viewer.Kind == game.ViewerPlayer && state.Players[playerIndex].Active {
		view.Self = &threeroundsv1.SelfView{
			RemainingHand:    cardIDsToStrings(state.Players[playerIndex].RemainingHand),
			PendingSelection: pendingSelectionToProto(state.Players[playerIndex].PendingSelection),
		}
	}
	return view, actions, nil
}

func allowedActions(state engine.State, playerIndex int, kind game.ViewerKind) []game.Identifier {
	if kind != game.ViewerPlayer || playerIndex < 0 {
		return nil
	}
	player := state.Players[playerIndex]
	if !player.Active {
		return nil
	}
	if state.Phase != engine.PhaseRoundOneSelecting && state.Phase != engine.PhaseRoundTwoSelecting {
		return nil
	}
	if player.PendingSelection.Round == state.CurrentRound {
		return nil
	}
	return []game.Identifier{ActionSubmitSelection}
}

func publicPlayersToProto(state engine.State) []*threeroundsv1.PublicPlayer {
	result := make([]*threeroundsv1.PublicPlayer, len(state.Players))
	showFinal := finalSummaryVisible(state)
	for index, player := range state.Players {
		result[index] = &threeroundsv1.PublicPlayer{
			UserId:                       player.UserID,
			SeatIndex:                    player.SeatIndex,
			Active:                       player.Active,
			Submitted:                    player.PendingSelection.Round == state.CurrentRound && player.Active && (state.Phase == engine.PhaseRoundOneSelecting || state.Phase == engine.PhaseRoundTwoSelecting),
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
		}
		if showFinal {
			result[index].FinalWinner = player.FinalWinner
			result[index].FinalRank = player.FinalRank
		}
	}
	return result
}

func viewPlayerIndex(state engine.State, userID string, seatIndex uint32) int {
	for index, player := range state.Players {
		if player.UserID == userID && player.SeatIndex == seatIndex {
			return index
		}
	}
	return -1
}

func finalSummaryVisible(state engine.State) bool {
	return (state.Phase == engine.PhaseFinalResult || state.Phase == engine.PhaseFinished) && len(state.FinalSummary.Standings) != 0
}
