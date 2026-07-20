package engine

import (
	"math"
	"testing"

	"github.com/iFTY-R/game-night/sdk/go/game/dice"
)

// FuzzAcceptedBidsAreStrictlyStronger exercises the uint32 and mode-switch
// boundaries without requiring a finite total-dice ceiling.
func FuzzAcceptedBidsAreStrictlyStronger(f *testing.F) {
	f.Add(uint32(4), uint32(3), uint8(BidModeFlying), uint32(5), uint32(2), uint8(BidModeFlying))
	f.Add(uint32(3), uint32(4), uint8(BidModeStrict), uint32(6), uint32(4), uint8(BidModeFlying))
	f.Fuzz(func(t *testing.T, previousQuantity, previousFace uint32, previousModeRaw uint8, nextQuantity, nextFace uint32, nextModeRaw uint8) {
		config := DefaultConfig(4)
		previous := Bid{Quantity: previousQuantity, Face: previousFace, Mode: BidMode(previousModeRaw)}
		next := Bid{Quantity: nextQuantity, Face: nextFace, Mode: BidMode(nextModeRaw)}
		if err := ValidateBid(config, &previous, next); err != nil {
			return
		}
		if previous.Mode == next.Mode && (next.Quantity < previous.Quantity || next.Quantity == previous.Quantity && next.Face <= previous.Face) {
			t.Fatalf("accepted same-mode regression: previous=%+v next=%+v", previous, next)
		}
		if previous.Mode == BidModeStrict && next.Mode == BidModeFlying {
			if previous.Quantity > math.MaxUint32/2 {
				t.Fatalf("accepted overflowing conversion: previous=%+v next=%+v", previous, next)
			}
			anchor := previous.Quantity * 2
			if next.Quantity < anchor || next.Quantity == anchor && next.Face < previous.Face {
				t.Fatalf("accepted strict-to-flying regression: previous=%+v next=%+v", previous, next)
			}
		}
		if previous.Mode == BidModeFlying && next.Mode == BidModeStrict {
			anchor := previous.Quantity/2 + 1
			if next.Quantity < anchor || next.Quantity == anchor && next.Face < previous.Face {
				t.Fatalf("accepted flying-to-strict regression: previous=%+v next=%+v", previous, next)
			}
		}
	})
}

func TestAcceptedBidGraphHasNoBackwardEdge(t *testing.T) {
	config := DefaultConfig(4)
	for previousQuantity := uint32(1); previousQuantity <= 32; previousQuantity++ {
		for previousFace := uint32(1); previousFace <= 6; previousFace++ {
			for _, previousMode := range []BidMode{BidModeFlying, BidModeStrict} {
				previous := Bid{Quantity: previousQuantity, Face: previousFace, Mode: previousMode}
				if previous.Face == 1 && previous.Mode == BidModeFlying {
					continue
				}
				for nextQuantity := uint32(1); nextQuantity <= 66; nextQuantity++ {
					for nextFace := uint32(1); nextFace <= 6; nextFace++ {
						for _, nextMode := range []BidMode{BidModeFlying, BidModeStrict} {
							next := Bid{Quantity: nextQuantity, Face: nextFace, Mode: nextMode}
							if ValidateBid(config, &previous, next) == nil && normalizedBidStrength(next) <= normalizedBidStrength(previous) {
								t.Fatalf("accepted backward edge: previous=%+v next=%+v", previous, next)
							}
						}
					}
				}
			}
		}
	}
}

func TestSettlementHasOneLoserAndConservesPenalty(t *testing.T) {
	for quantity := uint32(1); quantity <= 20; quantity++ {
		for face := uint32(1); face <= 6; face++ {
			for _, mode := range []BidMode{BidModeFlying, BidModeStrict} {
				if face == 1 && mode == BidModeFlying {
					continue
				}
				state := settlementState()
				state.CurrentBid = &Bid{Quantity: quantity, Face: face, Mode: mode}
				state.LastBidderUserID = "user-1"
				state.CurrentActorUserID = "user-2"
				before := totalPenalty(state)
				next, events, err := OpenDice(state, "user-2", 100_000, [32]byte{9})
				if err != nil {
					t.Fatal(err)
				}
				changed := 0
				for index := range state.Players {
					if next.Players[index].PenaltyTicks != state.Players[index].PenaltyTicks {
						changed++
					}
				}
				if changed != 1 || totalPenalty(next)-before != state.Config.PenaltyTicks || countEvent(events, EventRoundSettled) != 1 {
					t.Fatalf("penalty invariant failed: quantity=%d face=%d mode=%d next=%+v events=%+v", quantity, face, mode, next, events)
				}
			}
		}
	}
}

func normalizedBidStrength(bid Bid) uint64 {
	var primary uint64
	if bid.Mode == BidModeStrict {
		primary = uint64(bid.Quantity)*4 - 1
	} else {
		primary = uint64(bid.Quantity) * 2
	}
	return primary*7 + uint64(bid.Face)
}

func totalPenalty(state State) dice.Ticks {
	var total dice.Ticks
	for _, player := range state.Players {
		total += player.PenaltyTicks
	}
	return total
}
