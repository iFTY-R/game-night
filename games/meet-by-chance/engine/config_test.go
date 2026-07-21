package engine

import (
	"testing"

	"github.com/iFTY-R/game-night/sdk/go/game/dice"
)

func TestConfigAndParticipantBoundaries(t *testing.T) {
	if err := DefaultConfig().Validate(3); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"target_penalty_overflow", func(c *Config) { c.TargetPenaltyTicks = 17 }},
		{"reroll_penalty_overflow", func(c *Config) { c.RerollPenaltyTicks = 17 }},
		{"match_penalty_overflow", func(c *Config) { c.MatchPenaltyTicks = 17 }},
		{"weak_penalty_overflow", func(c *Config) { c.WeakExtraPenaltyTicks = 17 }},
		{"target_limit_overflow", func(c *Config) { c.TargetRerollLimit = 4 }},
		{"match_limit_overflow", func(c *Config) { c.MatchResolutionLimit = 9 }},
		{"short_timeout", func(c *Config) { c.ActionTimeoutSeconds = 9 }},
		{"long_timeout", func(c *Config) { c.ActionTimeoutSeconds = 121 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := DefaultConfig()
			test.mutate(&config)
			if err := config.Validate(3); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	config := DefaultConfig()
	config.ActionTimeoutSeconds = 0
	if err := config.Validate(12); err != nil {
		t.Fatal(err)
	}
	if err := config.Validate(2); err == nil {
		t.Fatal("two players accepted")
	}
	if err := config.Validate(13); err == nil {
		t.Fatal("thirteen players accepted")
	}
	_ = dice.Ticks(0)
}

func TestParticipantsRejectDuplicates(t *testing.T) {
	participants := testParticipants(3)
	if err := ValidateParticipants(participants); err != nil {
		t.Fatal(err)
	}
	participants[1].UserID = participants[0].UserID
	if err := ValidateParticipants(participants); err == nil {
		t.Fatal("duplicate user accepted")
	}
	participants = testParticipants(3)
	participants[1].SeatIndex = participants[0].SeatIndex
	if err := ValidateParticipants(participants); err == nil {
		t.Fatal("duplicate seat accepted")
	}
}
