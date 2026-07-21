package engine

import (
	"math/rand"
	"testing"

	"github.com/iFTY-R/game-night/sdk/go/game/dice"
)

func TestAutomaticResolutionNeverExceedsConfiguredBatchLimit(t *testing.T) {
	for seedValue := byte(1); seedValue < 40; seedValue++ {
		config := DefaultConfig()
		config.MatchResolutionLimit = uint32(seedValue % 4)
		state := testStateWithConfigAndSeed(t, 8, config, [32]byte{seedValue})
		if state.MatchResolutionCount > config.MatchResolutionLimit {
			t.Fatalf("seed=%d state=%+v", seedValue, state)
		}
		if err := state.Validate(); err != nil {
			t.Fatalf("seed=%d invalid=%v", seedValue, err)
		}
		if state.Phase != PhaseTargetDecision {
			t.Fatalf("seed=%d phase=%v", seedValue, state.Phase)
		}
	}
}

func TestActiveHandsRemainThreeValidDice(t *testing.T) {
	random := rand.New(rand.NewSource(9))
	for iteration := 0; iteration < 100; iteration++ {
		seed := [32]byte{byte(random.Intn(255) + 1), byte(iteration + 1)}
		state := testStateWithConfigAndSeed(t, 3+random.Intn(10), DefaultConfig(), seed)
		for _, player := range state.Players {
			if player.Active {
				for _, face := range player.Hand.Raw {
					if !face.Valid() {
						t.Fatalf("invalid raw hand=%+v", player.Hand)
					}
				}
				if player.Hand.Normalized[0] > player.Hand.Normalized[1] || player.Hand.Normalized[1] > player.Hand.Normalized[2] {
					t.Fatalf("unsorted hand=%+v", player.Hand)
				}
			}
		}
	}
}

func FuzzClassifyAndResolve(f *testing.F) {
	f.Add(uint8(2), uint8(3), uint8(5), true)
	f.Add(uint8(1), uint8(1), uint8(2), true)
	f.Fuzz(func(t *testing.T, first, second, third uint8, wild bool) {
		config := DefaultConfig()
		config.OnesWild = wild
		hand, err := Classify([3]dice.Face{dice.Face(first%6 + 1), dice.Face(second%6 + 1), dice.Face(third%6 + 1)}, config)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateHandBasic(hand); err != nil {
			t.Fatal(err)
		}
		resolved := Resolve235Context([]Hand{hand, hand, hand})
		if len(resolved) != 3 {
			t.Fatal("context resolution dropped hands")
		}
	})
}
