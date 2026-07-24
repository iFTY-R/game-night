package engine

import "testing"

func TestResolveThirdHandWildcardsAndStraightBoundaries(t *testing.T) {
	tests := []struct {
		name              string
		cards             []CardID
		wantClass         HandClass
		wantHighOrPair    uint8
		wantResolvedCards []CardID
		wantSubs          []CardID
	}{
		{
			name:              "double joker becomes deterministic trips",
			cards:             []CardID{BigJokerID, SmallJokerID, "5S"},
			wantClass:         HandTrips,
			wantHighOrPair:    5,
			wantResolvedCards: []CardID{"5D", "5C", "5S"},
			wantSubs:          []CardID{"5D", "5C"},
		},
		{
			name:              "single joker completes best straight flush",
			cards:             []CardID{BigJokerID, "8H", "9H"},
			wantClass:         HandStraightFlush,
			wantHighOrPair:    10,
			wantResolvedCards: []CardID{"8H", "9H", "10H"},
			wantSubs:          []CardID{"10H"},
		},
		{
			name:              "qka is highest straight",
			cards:             []CardID{"QH", "KS", "AD"},
			wantClass:         HandStraight,
			wantHighOrPair:    14,
			wantResolvedCards: []CardID{"QH", "KS", "AD"},
		},
		{
			name:              "a23 is lowest straight",
			cards:             []CardID{"AH", "2S", "3D"},
			wantClass:         HandStraight,
			wantHighOrPair:    3,
			wantResolvedCards: []CardID{"2S", "3D", "AH"},
		},
		{
			name:              "ka2 is not a straight",
			cards:             []CardID{"KH", "AS", "2D"},
			wantClass:         HandSingle,
			wantHighOrPair:    14,
			wantResolvedCards: []CardID{"2D", "KH", "AS"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cards := cardsFromIDs(test.cards)
			resolved, err := resolveThirdHand(cards)
			if err != nil {
				t.Fatal(err)
			}
			if resolved.HandClass != test.wantClass {
				t.Fatalf("class=%v want=%v", resolved.HandClass, test.wantClass)
			}
			if len(resolved.CompareValues) == 0 || resolved.CompareValues[0] != test.wantHighOrPair {
				t.Fatalf("compare=%v want-first=%d", resolved.CompareValues, test.wantHighOrPair)
			}
			if got, want := resolved.ResolvedCards, test.wantResolvedCards; len(got) != len(want) {
				t.Fatalf("resolved len=%d want=%d", len(got), len(want))
			} else {
				for index := range got {
					if got[index] != want[index] {
						t.Fatalf("resolved=%v want=%v", got, want)
					}
				}
			}
			if len(test.wantSubs) != 0 {
				if len(resolved.WildcardResolution.Substitutions) != len(test.wantSubs) {
					t.Fatalf("substitutions=%d want=%d", len(resolved.WildcardResolution.Substitutions), len(test.wantSubs))
				}
				for index, want := range test.wantSubs {
					if got := resolved.WildcardResolution.Substitutions[index].SubstitutedCardID; got != want {
						t.Fatalf("substitution[%d]=%s want=%s", index, got, want)
					}
				}
			}
		})
	}
}
