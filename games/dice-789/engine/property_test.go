package engine

import (
	"math/rand"
	"testing"

	"github.com/iFTY-R/game-night/sdk/go/game/dice"
)

func TestAllDiceOutcomesSelectExactlyOnePriority(t *testing.T) {
	for one := uint32(1); one <= 6; one++ {
		for two := uint32(1); two <= 6; two++ {
			state := newTestState(t, 4, DefaultConfig(false))
			next, _, err := applyRoll(state, state.CurrentUserID, one, two, testNow+1)
			if err != nil || !next.PendingResult.validRollResult() {
				t.Fatalf("faces %d,%d result=%s err=%v", one, two, next.PendingResult, err)
			}
		}
	}
}

func TestPoolAndPenaltyConservationProperty(t *testing.T) {
	random := rand.New(rand.NewSource(7))
	for iteration := 0; iteration < 500; iteration++ {
		config := DefaultConfig(true)
		pool := dice.Ticks(random.Intn(25))
		one, two := uint32(random.Intn(6)+1), uint32(random.Intn(6)+1)
		state := pendingState(t, one, two, pool, config, 4)
		beforePenalty := totalPenalty(state)
		next, _, err := ConfirmLanded(state, state.HostUserID, testNow+2)
		if err != nil {
			t.Fatalf("iteration %d: %v", iteration, err)
		}
		if err := next.Validate(); err != nil {
			t.Fatalf("iteration %d invalid: %v", iteration, err)
		}
		capacity := mustCapacity(config)
		if next.TotalPoolTicks > capacity || poolLayersTotal(next.Pool) != next.TotalPoolTicks {
			t.Fatalf("iteration %d pool=%+v", iteration, next)
		}
		penaltyDelta := totalPenalty(next) - beforePenalty
		if next.TotalPoolTicks < pool && penaltyDelta != pool-next.TotalPoolTicks {
			t.Fatalf("iteration %d delta=%d removed=%d", iteration, penaltyDelta, pool-next.TotalPoolTicks)
		}
		if next.Phase != PhaseFinished && !activePlayer(next, next.CurrentUserID) {
			t.Fatalf("iteration %d inactive current", iteration)
		}
	}
}

func totalPenalty(state State) dice.Ticks {
	var total dice.Ticks
	for _, player := range state.Players {
		total += player.PenaltyTicks
	}
	return total
}

func FuzzRollPriorityAndState(f *testing.F) {
	f.Add(uint8(1), uint8(1), uint8(0))
	f.Add(uint8(4), uint8(4), uint8(5))
	f.Add(uint8(3), uint8(6), uint8(24))
	f.Fuzz(func(t *testing.T, first, second, poolByte uint8) {
		one, two := uint32(first%6+1), uint32(second%6+1)
		pool := dice.Ticks(poolByte % 25)
		state := pendingState(t, one, two, pool, DefaultConfig(true), 4)
		next, _, err := ConfirmLanded(state, state.HostUserID, testNow+2)
		if err != nil {
			t.Fatal(err)
		}
		if err := next.Validate(); err != nil {
			t.Fatal(err)
		}
		if next.TotalPoolTicks > mustCapacity(next.Config) || poolLayersTotal(next.Pool) != next.TotalPoolTicks {
			t.Fatalf("invalid pool %+v", next.Pool)
		}
	})
}
