package engine

import (
	"math"

	game "github.com/iFTY-R/game-night/sdk/go/game"
	"github.com/iFTY-R/game-night/sdk/go/game/dice"
)

const (
	CurrentSchemaVersion uint32     = 1
	MinimumPlayers                  = 2
	MaximumPlayers                  = 12
	MinimumLayers                   = 1
	MaximumLayers                   = 6
	MaximumLayerCapacity dice.Ticks = 64
)

// ContinueMode freezes the action offered after a public effect is applied.
type ContinueMode uint8

const (
	ContinueOptional ContinueMode = iota + 1
	ContinueForcedReroll
	ContinueForcedPass
)

// Config is immutable after session creation. All pool and penalty values are integer ticks.
type Config struct {
	InitialPoolTicks        dice.Ticks
	LayerCapacityTicks      dice.Ticks
	AddStepTicks            dice.Ticks
	MaxLayers               uint32
	StackedPool             bool
	OrdinaryPairsReverse    bool
	DoubleOneEnabled        bool
	DoubleFourEnabled       bool
	DoubleSixEnabled        bool
	ContinueMode            ContinueMode
	LastDigitMatch          bool
	ActionTimeoutSeconds    uint32
	DropReportWindowSeconds uint32
}

// DefaultConfig returns the approved ruleset for the selected pool presentation.
func DefaultConfig(stacked bool) Config {
	config := Config{
		InitialPoolTicks: 2, LayerCapacityTicks: 8, AddStepTicks: 1,
		MaxLayers: 1, StackedPool: stacked, OrdinaryPairsReverse: true,
		DoubleOneEnabled: true, DoubleFourEnabled: true, DoubleSixEnabled: true,
		ContinueMode: ContinueOptional, ActionTimeoutSeconds: 30, DropReportWindowSeconds: 5,
	}
	if stacked {
		config.MaxLayers = 3
	}
	return config
}

// Validate enforces all frozen configuration hard bounds and checked products.
func (config Config) Validate(playerCount int) error {
	if playerCount < MinimumPlayers || playerCount > MaximumPlayers ||
		config.InitialPoolTicks > 64 || config.LayerCapacityTicks == 0 || config.LayerCapacityTicks > MaximumLayerCapacity ||
		config.AddStepTicks == 0 || config.AddStepTicks > 16 || config.LayerCapacityTicks%config.AddStepTicks != 0 ||
		config.MaxLayers < MinimumLayers || config.MaxLayers > MaximumLayers ||
		(!config.StackedPool && config.MaxLayers != 1) || (config.StackedPool && config.MaxLayers < 2) ||
		config.ContinueMode < ContinueOptional || config.ContinueMode > ContinueForcedPass ||
		(config.ActionTimeoutSeconds != 0 && (config.ActionTimeoutSeconds < 10 || config.ActionTimeoutSeconds > 120)) ||
		config.DropReportWindowSeconds < 3 || config.DropReportWindowSeconds > 15 {
		return ruleError(CodeInvalidConfig, "configuration is outside frozen rules")
	}
	if uint64(config.LayerCapacityTicks)*uint64(config.MaxLayers) > math.MaxUint32 ||
		uint64(config.InitialPoolTicks) > uint64(config.LayerCapacityTicks)*uint64(config.MaxLayers) {
		return ruleError(CodePoolOverflow, "pool capacity is invalid")
	}
	return nil
}

// ValidateParticipants validates stable user/seat uniqueness without changing order.
func ValidateParticipants(participants []Participant) error {
	if len(participants) < MinimumPlayers || len(participants) > MaximumPlayers {
		return ruleError(CodeInvalidParticipants, "participant count is unsupported")
	}
	seenUsers := make(map[string]struct{}, len(participants))
	seenSeats := make(map[uint32]struct{}, len(participants))
	for _, participant := range participants {
		if _, err := game.ParseIdentifier(participant.UserID); err != nil {
			return ruleError(CodeInvalidParticipants, "participant identity is empty")
		}
		if _, ok := seenUsers[participant.UserID]; ok {
			return ruleError(CodeInvalidParticipants, "participant identity is duplicated")
		}
		if _, ok := seenSeats[participant.SeatIndex]; ok {
			return ruleError(CodeInvalidParticipants, "participant seat is duplicated")
		}
		seenUsers[participant.UserID] = struct{}{}
		seenSeats[participant.SeatIndex] = struct{}{}
	}
	return nil
}
