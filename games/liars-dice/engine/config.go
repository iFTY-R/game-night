package engine

import (
	"sort"

	game "github.com/iFTY-R/game-night/sdk/go/game"
	"github.com/iFTY-R/game-night/sdk/go/game/dice"
)

// DefaultConfig returns the approved complete ruleset for a frozen participant count.
func DefaultConfig(playerCount uint32) Config {
	return Config{
		DicePerPlayer: 5, OnesWild: true, StrictEnabled: true, FlyingEnabled: true,
		FirstBidMinimum: playerCount, PenaltyTicks: dice.TicksPerUnit, ActionTimeoutSeconds: 30,
	}
}

// Validate enforces creation-time limits; action_timeout_seconds=0 explicitly disables timers.
func (config Config) Validate(playerCount int) error {
	if playerCount < MinimumPlayers || playerCount > MaximumPlayers ||
		config.DicePerPlayer < MinimumDicePerPlayer || config.DicePerPlayer > MaximumDicePerPlayer ||
		config.FirstBidMinimum == 0 || config.FirstBidMinimum > uint32(playerCount) ||
		config.PenaltyTicks < dice.TicksPerUnit/2 || config.PenaltyTicks > dice.TicksPerUnit*2 ||
		config.FlyingEnabled && !config.StrictEnabled ||
		config.ActionTimeoutSeconds != 0 && (config.ActionTimeoutSeconds < 10 || config.ActionTimeoutSeconds > 120) {
		return ruleError(CodeInvalidConfig, "configuration is outside frozen rules")
	}
	return nil
}

// ValidateParticipants canonicalizes identity constraints independently from room membership authorization.
func ValidateParticipants(participants []Participant) error {
	if len(participants) < MinimumPlayers || len(participants) > MaximumPlayers {
		return ruleError(CodeInvalidParticipants, "participant count is unsupported")
	}
	users := make(map[string]struct{}, len(participants))
	seats := make(map[uint32]struct{}, len(participants))
	for _, participant := range participants {
		if _, err := game.ParseIdentifier(participant.UserID); err != nil {
			return ruleError(CodeInvalidParticipants, "participant identity is invalid")
		}
		if _, duplicate := users[participant.UserID]; duplicate {
			return ruleError(CodeInvalidParticipants, "participant identity is duplicated")
		}
		if _, duplicate := seats[participant.SeatIndex]; duplicate {
			return ruleError(CodeInvalidParticipants, "participant seat is duplicated")
		}
		users[participant.UserID] = struct{}{}
		seats[participant.SeatIndex] = struct{}{}
	}
	return nil
}

func canonicalParticipants(participants []Participant) ([]Participant, error) {
	if err := ValidateParticipants(participants); err != nil {
		return nil, err
	}
	canonical := append([]Participant(nil), participants...)
	sort.Slice(canonical, func(left, right int) bool { return canonical[left].SeatIndex < canonical[right].SeatIndex })
	return canonical, nil
}
