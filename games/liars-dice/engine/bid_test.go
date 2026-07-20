package engine

import (
	"math"
	"testing"
)

func TestBidValidationAndModeConversion(t *testing.T) {
	config := DefaultConfig(4)
	if err := ValidateBid(config, nil, Bid{Quantity: 4, Face: 3, Mode: BidModeFlying}); err != nil {
		t.Fatalf("first bid: %v", err)
	}
	assertCode(t, ValidateBid(config, nil, Bid{Quantity: 3, Face: 6, Mode: BidModeFlying}), CodeBidTooLow)
	assertCode(t, ValidateBid(config, nil, Bid{Quantity: 4, Face: 1, Mode: BidModeFlying}), CodeFaceOneMustBeStrict)

	previous := Bid{Quantity: 4, Face: 3, Mode: BidModeFlying}
	for _, next := range []Bid{
		{Quantity: 4, Face: 4, Mode: BidModeFlying},
		{Quantity: 5, Face: 2, Mode: BidModeFlying},
		{Quantity: 3, Face: 3, Mode: BidModeStrict},
	} {
		if err := ValidateBid(config, &previous, next); err != nil {
			t.Fatalf("valid raise %+v: %v", next, err)
		}
	}
	assertCode(t, ValidateBid(config, &previous, Bid{Quantity: 4, Face: 2, Mode: BidModeFlying}), CodeBidNotHigher)

	strict := Bid{Quantity: 3, Face: 4, Mode: BidModeStrict}
	if err := ValidateBid(config, &strict, Bid{Quantity: 6, Face: 4, Mode: BidModeFlying}); err != nil {
		t.Fatalf("strict to flying anchor: %v", err)
	}
	flying := Bid{Quantity: 6, Face: 4, Mode: BidModeFlying}
	if err := ValidateBid(config, &flying, Bid{Quantity: 4, Face: 4, Mode: BidModeStrict}); err != nil {
		t.Fatalf("flying to strict anchor: %v", err)
	}
}

func TestBidAllowsAboveTotalDiceAndRejectsOverflow(t *testing.T) {
	config := DefaultConfig(4)
	previous := Bid{Quantity: 20, Face: 6, Mode: BidModeFlying}
	if err := ValidateBid(config, &previous, Bid{Quantity: 21, Face: 2, Mode: BidModeFlying}); err != nil {
		t.Fatalf("above total dice: %v", err)
	}
	overflow := Bid{Quantity: math.MaxUint32/2 + 1, Face: 4, Mode: BidModeStrict}
	assertCode(t, ValidateBid(config, &overflow, Bid{Quantity: math.MaxUint32, Face: 6, Mode: BidModeFlying}), CodeBidQuantityOverflow)
	faceOne := Bid{Quantity: 3, Face: 1, Mode: BidModeStrict}
	assertCode(t, ValidateBid(config, &faceOne, Bid{Quantity: 6, Face: 2, Mode: BidModeFlying}), CodeBidNotHigher)
}

func TestDisabledModesAreRejected(t *testing.T) {
	config := DefaultConfig(3)
	config.StrictEnabled = false
	config.FlyingEnabled = false
	assertCode(t, ValidateBid(config, nil, Bid{Quantity: 3, Face: 2, Mode: BidModeStrict}), CodeStrictDisabled)
	if err := ValidateBid(config, nil, Bid{Quantity: 3, Face: 2, Mode: BidModeFlying}); err != nil {
		t.Fatalf("ordinary flying bid must remain available: %v", err)
	}
	config.StrictEnabled = true
	previous := Bid{Quantity: 3, Face: 2, Mode: BidModeStrict}
	assertCode(t, ValidateBid(config, &previous, Bid{Quantity: 6, Face: 2, Mode: BidModeFlying}), CodeFlyingDisabled)
}
