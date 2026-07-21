package engine

import (
	"sort"

	game "github.com/iFTY-R/game-night/sdk/go/game"
	"github.com/iFTY-R/game-night/sdk/go/game/dice"
)

const (
	CurrentSchemaVersion uint32     = 1
	MinimumPlayers                  = 3
	MaximumPlayers                  = 12
	DicePerPlayer        uint32     = 3
	MaximumPenaltyTicks  dice.Ticks = 16
	RoundHistoryLimit               = 32
)

// Config is frozen at creation. Penalty fields are bounded abstract ticks, never real beverage amounts.
type Config struct {
	Straight123           bool
	Straight234           bool
	Straight345           bool
	Straight456           bool
	Special235Enabled     bool
	OnesWild              bool
	TargetPenaltyTicks    dice.Ticks
	RerollPenaltyTicks    dice.Ticks
	MatchPenaltyTicks     dice.Ticks
	WeakExtraPenaltyTicks dice.Ticks
	TargetRerollLimit     uint32
	MatchResolutionLimit  uint32
	ActionTimeoutSeconds  uint32
}

// DefaultConfig returns the complete approved ruleset.
func DefaultConfig() Config {
	return Config{
		Straight123: true, Straight234: true, Straight345: true, Straight456: true,
		Special235Enabled: true, TargetPenaltyTicks: 2, RerollPenaltyTicks: 2,
		MatchPenaltyTicks: 4, WeakExtraPenaltyTicks: 2, TargetRerollLimit: 2,
		MatchResolutionLimit: 3, ActionTimeoutSeconds: 30,
	}
}

// Validate enforces creation-time hard bounds; action_timeout_seconds=0 disables only target timers.
func (config Config) Validate(playerCount int) error {
	if playerCount < MinimumPlayers || playerCount > MaximumPlayers ||
		!config.Straight123 || !config.Straight456 ||
		config.TargetPenaltyTicks > MaximumPenaltyTicks || config.RerollPenaltyTicks > MaximumPenaltyTicks ||
		config.MatchPenaltyTicks > MaximumPenaltyTicks || config.WeakExtraPenaltyTicks > MaximumPenaltyTicks ||
		config.TargetRerollLimit > 3 || config.MatchResolutionLimit > 8 ||
		(config.ActionTimeoutSeconds != 0 && (config.ActionTimeoutSeconds < 10 || config.ActionTimeoutSeconds > 120)) {
		return ruleError(CodeInvalidConfig, "configuration is outside frozen rules")
	}
	return nil
}

// Participant freezes one canonical user identity to a stable room seat.
type Participant struct {
	UserID    string
	SeatIndex uint32
}

// ValidateParticipants checks user/seat uniqueness without trusting caller order.
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
