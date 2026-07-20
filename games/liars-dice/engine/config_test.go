package engine

import (
	"errors"
	"testing"

	game "github.com/iFTY-R/game-night/sdk/go/game"
)

func TestConfigValidationBoundaries(t *testing.T) {
	valid := DefaultConfig(4)
	if err := valid.Validate(4); err != nil {
		t.Fatalf("default config: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Config)
		code   ErrorCode
	}{
		{name: "too few dice", mutate: func(config *Config) { config.DicePerPlayer = 2 }, code: CodeInvalidConfig},
		{name: "too many dice", mutate: func(config *Config) { config.DicePerPlayer = 7 }, code: CodeInvalidConfig},
		{name: "zero first bid", mutate: func(config *Config) { config.FirstBidMinimum = 0 }, code: CodeInvalidConfig},
		{name: "first bid exceeds players", mutate: func(config *Config) { config.FirstBidMinimum = 5 }, code: CodeInvalidConfig},
		{name: "penalty below half unit", mutate: func(config *Config) { config.PenaltyTicks = 1 }, code: CodeInvalidConfig},
		{name: "penalty above two units", mutate: func(config *Config) { config.PenaltyTicks = 9 }, code: CodeInvalidConfig},
		{name: "timeout below minimum", mutate: func(config *Config) { config.ActionTimeoutSeconds = 9 }, code: CodeInvalidConfig},
		{name: "timeout above maximum", mutate: func(config *Config) { config.ActionTimeoutSeconds = 121 }, code: CodeInvalidConfig},
		{name: "flying without strict", mutate: func(config *Config) { config.StrictEnabled = false }, code: CodeInvalidConfig},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.mutate(&config)
			assertCode(t, config.Validate(4), test.code)
		})
	}

	valid.ActionTimeoutSeconds = 0
	if err := valid.Validate(4); err != nil {
		t.Fatalf("disabled timeout: %v", err)
	}
	if err := valid.Validate(1); !errors.Is(err, game.ErrInvalidContract) || ErrorCodeOf(err) != CodeInvalidConfig {
		t.Fatalf("rule error classification = %v", err)
	}
}

func TestParticipantValidationUsesStableUniqueSeats(t *testing.T) {
	valid := testParticipants(4)
	if err := ValidateParticipants(valid); err != nil {
		t.Fatalf("participants: %v", err)
	}
	assertCode(t, ValidateParticipants(valid[:1]), CodeInvalidParticipants)
	duplicate := append([]Participant(nil), valid...)
	duplicate[1].SeatIndex = duplicate[0].SeatIndex
	assertCode(t, ValidateParticipants(duplicate), CodeInvalidParticipants)
}
