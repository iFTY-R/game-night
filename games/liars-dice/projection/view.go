package projection

import (
	"github.com/iFTY-R/game-night/games/liars-dice/engine"
	liarsdicev1 "github.com/iFTY-R/game-night/games/liars-dice/gen/go/game/liars_dice/v1"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"github.com/iFTY-R/game-night/sdk/go/game/dice"
)

const (
	// ActionBid submits the next legal quantity, face, and mode.
	ActionBid game.Identifier = "round.bid"
	// ActionOpen challenges the previous bid and reveals the round.
	ActionOpen game.Identifier = "round.open"
)

// BuildView reconstructs a viewer-safe protobuf view from an authoritative engine state.
func BuildView(state engine.State, viewer game.Viewer) (*liarsdicev1.View, []game.Identifier, error) {
	if err := state.Validate(); err != nil || !viewer.Valid() {
		return nil, nil, projectionError("state or viewer is invalid")
	}
	if viewer.Kind == game.ViewerReplay {
		return nil, nil, projectionError("replay viewers require ProjectReplay")
	}
	if viewer.Kind == game.ViewerPlayer {
		index := playerIndex(state, string(viewer.UserID))
		if index < 0 || state.Players[index].SeatIndex != viewer.SeatIndex {
			return nil, nil, projectionError("player viewer does not own the requested seat")
		}
	}
	view := &liarsdicev1.View{
		Phase:                    phaseToProto(state.Phase),
		Round:                    state.Round,
		Players:                  publicPlayers(state),
		CurrentActorUserId:       state.CurrentActorUserID,
		ActionDeadlineUnixMillis: state.ActionDeadlineUnixMillis,
		FinishReason:             state.FinishReason,
		LastSettlement:           settlementToProto(state.LastSettlement),
	}
	if state.CurrentBid != nil {
		view.CurrentBid = bidToProto(state.CurrentBid)
		view.HasCurrentBid = true
	}
	view.RevealedDice = rollsToProto(state.LastSettlement.RevealedDice)
	if viewer.Kind == game.ViewerPlayer && state.Phase == engine.PhaseBidding {
		for _, roll := range state.PrivateDice {
			if roll.UserID == string(viewer.UserID) {
				view.OwnDice = facesToProto(roll.Faces)
				break
			}
		}
	}
	actions := []game.Identifier(nil)
	if viewer.Kind == game.ViewerPlayer && state.Phase == engine.PhaseBidding && state.CurrentActorUserID == string(viewer.UserID) {
		actions = append(actions, ActionBid)
		if state.CurrentBid != nil {
			actions = append(actions, ActionOpen)
		}
	}
	for _, action := range actions {
		view.AllowedActions = append(view.AllowedActions, string(action))
	}
	return view, actions, nil
}

func publicPlayers(state engine.State) []*liarsdicev1.PublicPlayer {
	players := make([]*liarsdicev1.PublicPlayer, 0, len(state.Players))
	for _, player := range state.Players {
		players = append(players, &liarsdicev1.PublicPlayer{
			UserId: player.UserID, SeatIndex: player.SeatIndex, Active: player.Active,
			PenaltyTicks: uint32(player.PenaltyTicks), HasPrivateDice: player.Active && hasPrivateRoll(state, player.UserID),
		})
	}
	return players
}

func hasPrivateRoll(state engine.State, userID string) bool {
	if state.Phase != engine.PhaseBidding {
		return false
	}
	for _, roll := range state.PrivateDice {
		if roll.UserID == userID {
			return true
		}
	}
	return false
}

func settlementToProto(value engine.Settlement) *liarsdicev1.RoundSettled {
	if value.Round == 0 || value.LoserUserID == "" {
		return nil
	}
	return &liarsdicev1.RoundSettled{
		LoserUserId: value.LoserUserID, PenaltyTicks: uint32(value.PenaltyTicks), NextRound: value.Round + 1,
		ActualQuantity: value.ActualQuantity, Reason: string(value.Reason), OpenerUserId: value.OpenerUserID,
		Bid: bidToProto(value.Bid),
	}
}

func bidToProto(value *engine.Bid) *liarsdicev1.Bid {
	if value == nil {
		return nil
	}
	mode := liarsdicev1.BidMode_BID_MODE_FLYING
	if value.Mode == engine.BidModeStrict {
		mode = liarsdicev1.BidMode_BID_MODE_STRICT
	}
	return &liarsdicev1.Bid{Quantity: value.Quantity, Face: value.Face, Mode: mode}
}

func rollsToProto(values []engine.PrivateRoll) []*liarsdicev1.PrivateDice {
	rolls := make([]*liarsdicev1.PrivateDice, 0, len(values))
	for _, value := range values {
		rolls = append(rolls, &liarsdicev1.PrivateDice{UserId: value.UserID, Faces: facesToProto(value.Faces)})
	}
	return rolls
}

func facesToProto(values []dice.Face) []uint32 {
	faces := make([]uint32, len(values))
	for index, value := range values {
		faces[index] = uint32(value)
	}
	return faces
}

func phaseToProto(value engine.Phase) liarsdicev1.Phase {
	switch value {
	case engine.PhaseBidding:
		return liarsdicev1.Phase_PHASE_BIDDING
	case engine.PhaseFinished:
		return liarsdicev1.Phase_PHASE_FINISHED
	default:
		return liarsdicev1.Phase_PHASE_UNSPECIFIED
	}
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
