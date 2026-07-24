package engine

import (
	"slices"
	"testing"
)

func TestShuffleDeckZeroSeedVector(t *testing.T) {
	deck, err := ShuffleDeck([32]byte{})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]CardID, len(deck))
	for index, card := range deck {
		ids[index] = card.ID
	}
	expected := []CardID{
		"2C", "KD", "KS", "3D", "KC", "AH", "10C", "10H", "AD", "JS", "SJ", "7S", "BJ", "2D",
		"9H", "AS", "AC", "2H", "3C", "9C", "10S", "8S", "7D", "9D", "8C", "6C", "2S", "QH",
		"QS", "4C", "4D", "JC", "6S", "5C", "10D", "QD", "8D", "6H", "3S", "7H", "4S", "8H",
		"7C", "QC", "5D", "6D", "3H", "5S", "9S", "JH", "KH", "4H", "5H", "JD",
	}
	if !slices.Equal(ids, expected) {
		t.Fatalf("zero-seed shuffle mismatch:\n got=%v\nwant=%v", ids, expected)
	}
	if digest := deckDigest(deck); digest != zeroSeedHash {
		t.Fatalf("zero-seed digest=%s want=%s", digest, zeroSeedHash)
	}
}

func TestDealHandsPreservesRoundRobinAndNinePlayerExhaustion(t *testing.T) {
	participants := make([]Participant, 9)
	for index := range participants {
		participants[index] = Participant{UserID: "user-" + itoa(uint32(index+1)), SeatIndex: uint32(index)}
	}
	hands, remaining, err := DealHands(participants, OrderedDeck())
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("remaining deck=%d want=0", len(remaining))
	}
	if want := []CardID{"2D", "4C", "6H", "8S", "JD", "KC"}; !slices.Equal(hands["user-1"], want) {
		t.Fatalf("seat-0 hand=%v want=%v", hands["user-1"], want)
	}
	seen := map[CardID]struct{}{}
	for _, hand := range hands {
		if len(hand) != 6 {
			t.Fatalf("hand len=%d want=6", len(hand))
		}
		for _, cardID := range hand {
			if _, duplicate := seen[cardID]; duplicate {
				t.Fatalf("duplicate dealt card=%s", cardID)
			}
			seen[cardID] = struct{}{}
		}
	}
	if len(seen) != 54 {
		t.Fatalf("dealt unique cards=%d want=54", len(seen))
	}
}
