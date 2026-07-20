package engine

import "math"

// ValidateBid applies first-bid, same-mode, and strict/flying conversion rules without a total-dice ceiling.
func ValidateBid(config Config, previous *Bid, next Bid) error {
	if next.Quantity == 0 {
		return ruleError(CodeBidTooLow, "quantity must be positive")
	}
	if next.Face < 1 || next.Face > 6 {
		return ruleError(CodeInvalidFace, "face must be between one and six")
	}
	if next.Mode != BidModeFlying && next.Mode != BidModeStrict {
		return ruleError(CodeInvalidBidMode, "bid mode is unknown")
	}
	if next.Mode == BidModeStrict && !config.StrictEnabled {
		return ruleError(CodeStrictDisabled, "strict bids are disabled")
	}
	if next.Face == 1 && next.Mode != BidModeStrict {
		return ruleError(CodeFaceOneMustBeStrict, "face one cannot be flying")
	}
	if previous == nil {
		if next.Quantity < config.FirstBidMinimum {
			return ruleError(CodeBidTooLow, "first bid is below the frozen minimum")
		}
		return nil
	}
	if previous.Quantity == 0 || previous.Face < 1 || previous.Face > 6 ||
		(previous.Mode != BidModeFlying && previous.Mode != BidModeStrict) ||
		previous.Mode == BidModeStrict && !config.StrictEnabled ||
		previous.Face == 1 && previous.Mode != BidModeStrict {
		return ruleError(CodeInvalidState, "previous bid is invalid")
	}
	if previous.Mode == next.Mode {
		if bidAtLeast(next, *previous, false) {
			return nil
		}
		return ruleError(CodeBidNotHigher, "same-mode bid did not increase")
	}

	anchor := Bid{Face: previous.Face, Mode: next.Mode}
	if previous.Mode == BidModeStrict {
		// FlyingEnabled controls the optional strict-to-flying move; ordinary flying bids remain the base mode.
		if !config.FlyingEnabled {
			return ruleError(CodeFlyingDisabled, "strict-to-flying conversion is disabled")
		}
		if previous.Face == 1 {
			return ruleError(CodeBidNotHigher, "face one strict cannot switch directly to flying")
		}
		if previous.Quantity > math.MaxUint32/2 {
			return ruleError(CodeBidQuantityOverflow, "strict-to-flying anchor overflows uint32")
		}
		anchor.Quantity = previous.Quantity * 2
	} else {
		anchor.Quantity = previous.Quantity/2 + 1
	}
	if bidAtLeast(next, anchor, true) {
		return nil
	}
	return ruleError(CodeBidNotHigher, "mode-switch bid is below its conversion anchor")
}

func bidAtLeast(next, anchor Bid, equalityAllowed bool) bool {
	if next.Quantity > anchor.Quantity {
		return true
	}
	if next.Quantity < anchor.Quantity {
		return false
	}
	if equalityAllowed {
		return next.Face >= anchor.Face
	}
	return next.Face > anchor.Face
}

// CountBid counts only active current-round dice and applies ones-wild exclusively to non-one flying bids.
func CountBid(state State, bid Bid) uint32 {
	active := make(map[string]struct{}, len(state.Players))
	for _, player := range state.Players {
		if player.Active {
			active[player.UserID] = struct{}{}
		}
	}
	var count uint32
	for _, roll := range state.PrivateDice {
		if _, present := active[roll.UserID]; !present {
			continue
		}
		for _, face := range roll.Faces {
			if uint32(face) == bid.Face || bid.Mode == BidModeFlying && state.Config.OnesWild && bid.Face != 1 && face == 1 {
				count++
			}
		}
	}
	return count
}
