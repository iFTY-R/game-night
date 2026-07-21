package engine

import (
	"testing"

	"github.com/iFTY-R/game-night/sdk/go/game/dice"
)

func TestClassifyFourClassesAndKeys(t *testing.T) {
	config := DefaultConfig()
	tests := []struct {
		name    string
		raw     [3]dice.Face
		class   HandClass
		primary int32
	}{
		{"leopard", [3]dice.Face{6, 6, 6}, HandLeopard, 6},
		{"straight", [3]dice.Face{5, 3, 4}, HandStraight, 5},
		{"pair", [3]dice.Face{2, 5, 2}, HandPair, 2},
		{"single", [3]dice.Face{6, 2, 4}, HandSingle, 6},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hand, err := Classify(test.raw, config)
			if err != nil {
				t.Fatal(err)
			}
			if hand.Class != test.class || hand.TieKey[1] != test.primary {
				t.Fatalf("hand=%+v", hand)
			}
		})
	}
	pairFiveLow, _ := Classify([3]dice.Face{5, 5, 2}, config)
	pairFiveHigh, _ := Classify([3]dice.Face{5, 5, 6}, config)
	if CompareHand(pairFiveHigh, pairFiveLow) <= 0 || pairFiveHigh.TieKey != pairFiveLow.TieKey || ExactMatch(pairFiveHigh, pairFiveLow) {
		t.Fatalf("pair keys low=%+v high=%+v", pairFiveLow, pairFiveHigh)
	}
}

func TestStraightSwitchesAndWildcardSelection(t *testing.T) {
	config := DefaultConfig()
	config.Straight234 = false
	hand, _ := Classify([3]dice.Face{2, 3, 4}, config)
	if hand.Class != HandSingle {
		t.Fatalf("disabled straight=%+v", hand)
	}
	config.OnesWild = true
	hand, _ = Classify([3]dice.Face{1, 1, 2}, config)
	if hand.Class != HandLeopard || hand.Normalized != [3]dice.Face{2, 2, 2} || !hand.WildUsed {
		t.Fatalf("wild hand=%+v", hand)
	}
	hand, _ = Classify([3]dice.Face{1, 1, 1}, config)
	if hand.Class != HandLeopard || hand.Normalized != [3]dice.Face{6, 6, 6} {
		t.Fatalf("all-wild=%+v", hand)
	}
}

func TestSpecial235UsesOneTableContext(t *testing.T) {
	config := DefaultConfig()
	special, _ := Classify([3]dice.Face{2, 3, 5}, config)
	leopardSix, _ := Classify([3]dice.Face{6, 6, 6}, config)
	leopardFive, _ := Classify([3]dice.Face{5, 5, 5}, config)
	resolved := Resolve235Context([]Hand{special, leopardSix, leopardFive})
	if resolved[0].SpecialContext != Special235BeatsLeopards || CompareHand(resolved[0], resolved[1]) <= 0 {
		t.Fatalf("resolved=%+v", resolved)
	}
	pair, _ := Classify([3]dice.Face{4, 4, 2}, config)
	resolved = Resolve235Context([]Hand{special, leopardSix, pair})
	if resolved[0].SpecialContext != Special235Minimum || CompareHand(resolved[0], pair) >= 0 {
		t.Fatalf("resolved=%+v", resolved)
	}
	second235, _ := Classify([3]dice.Face{5, 2, 3}, config)
	resolved = Resolve235Context([]Hand{special, second235, leopardSix})
	if resolved[0].SpecialContext != Special235Minimum || !ExactMatch(resolved[0], resolved[1]) {
		t.Fatalf("multiple235=%+v", resolved)
	}
}

func TestComparatorClassOrder(t *testing.T) {
	config := DefaultConfig()
	raw := [][3]dice.Face{{2, 4, 6}, {2, 2, 6}, {2, 3, 4}, {3, 3, 3}}
	hands := make([]Hand, len(raw))
	for index := range raw {
		hands[index], _ = Classify(raw[index], config)
	}
	for index := 1; index < len(hands); index++ {
		if CompareHand(hands[index-1], hands[index]) >= 0 {
			t.Fatalf("order %d: %+v >= %+v", index, hands[index-1], hands[index])
		}
	}
}
