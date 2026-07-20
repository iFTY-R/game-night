package engine

import (
	"testing"

	"github.com/iFTY-R/game-night/sdk/go/game/dice"
)

func TestConfigBoundaries(t *testing.T) {
	for _, stacked := range []bool{false, true} {
		if err := DefaultConfig(stacked).Validate(4); err != nil {
			t.Fatalf("default stacked=%v: %v", stacked, err)
		}
	}
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"initial_over_64", func(c *Config) { c.InitialPoolTicks = 65 }},
		{"layer_zero", func(c *Config) { c.LayerCapacityTicks = 0 }},
		{"layer_over_64", func(c *Config) { c.LayerCapacityTicks = 65 }},
		{"step_zero", func(c *Config) { c.AddStepTicks = 0 }},
		{"step_over_16", func(c *Config) { c.AddStepTicks = 17 }},
		{"step_not_divisor", func(c *Config) { c.LayerCapacityTicks, c.AddStepTicks = 9, 2 }},
		{"single_layer_mismatch", func(c *Config) { c.MaxLayers = 2 }},
		{"stacked_one_layer", func(c *Config) { c.StackedPool, c.MaxLayers = true, 1 }},
		{"layers_over_six", func(c *Config) { c.StackedPool, c.MaxLayers = true, 7 }},
		{"pool_exceeds_capacity", func(c *Config) { c.InitialPoolTicks, c.LayerCapacityTicks = 9, 8 }},
		{"bad_continue", func(c *Config) { c.ContinueMode = 0 }},
		{"short_action_timeout", func(c *Config) { c.ActionTimeoutSeconds = 9 }},
		{"long_action_timeout", func(c *Config) { c.ActionTimeoutSeconds = 121 }},
		{"short_drop_window", func(c *Config) { c.DropReportWindowSeconds = 2 }},
		{"long_drop_window", func(c *Config) { c.DropReportWindowSeconds = 16 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := DefaultConfig(false)
			test.mutate(&config)
			if err := config.Validate(4); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	config := DefaultConfig(false)
	config.ActionTimeoutSeconds = 0
	if err := config.Validate(2); err != nil {
		t.Fatalf("disabled action timer rejected: %v", err)
	}
	for _, count := range []int{1, 13} {
		if err := config.Validate(count); err == nil {
			t.Fatalf("player count %d accepted", count)
		}
	}
}

func TestPoolCanonicalLayersAndRemainders(t *testing.T) {
	config := DefaultConfig(true)
	for _, test := range []struct {
		total dice.Ticks
		want  []dice.Ticks
	}{
		{0, []dice.Ticks{0}},
		{7, []dice.Ticks{7}},
		{8, []dice.Ticks{8}},
		{9, []dice.Ticks{8, 1}},
		{24, []dice.Ticks{8, 8, 8}},
	} {
		layers, err := poolFromTotal(config, test.total)
		if err != nil || len(layers) != len(test.want) {
			t.Fatalf("total %d layers=%v err=%v", test.total, layers, err)
		}
		for index, want := range test.want {
			if layers[index].Ticks != want {
				t.Fatalf("total %d layer %d=%d want=%d", test.total, index, layers[index].Ticks, want)
			}
		}
	}
	if _, err := poolFromTotal(config, 25); err == nil {
		t.Fatal("pool above capacity accepted")
	}
	overflow := Config{LayerCapacityTicks: ^dice.Ticks(0), MaxLayers: 6}
	if _, err := overflow.totalCapacity(); ErrorCodeOf(err) != CodePoolOverflow {
		t.Fatalf("capacity overflow error=%v", err)
	}
}

func TestParticipantValidation(t *testing.T) {
	valid := testParticipants(3)
	if err := ValidateParticipants(valid); err != nil {
		t.Fatal(err)
	}
	duplicateUser := append([]Participant(nil), valid...)
	duplicateUser[1].UserID = duplicateUser[0].UserID
	if err := ValidateParticipants(duplicateUser); err == nil {
		t.Fatal("duplicate user accepted")
	}
	duplicateSeat := append([]Participant(nil), valid...)
	duplicateSeat[1].SeatIndex = duplicateSeat[0].SeatIndex
	if err := ValidateParticipants(duplicateSeat); err == nil {
		t.Fatal("duplicate seat accepted")
	}
}
