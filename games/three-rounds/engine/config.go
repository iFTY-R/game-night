package engine

const (
	MinimumPlayers      = 2
	MaximumPlayers      = 9
	DefaultRoundOneTime = 30
	DefaultRoundTwoTime = 45
	DefaultResultTime   = 5
	DefaultFinalTime    = 10
)

// Config is the only mutable rules payload frozen into a session.
type Config struct {
	RoundOneTimeoutSeconds uint32
	RoundTwoTimeoutSeconds uint32
	RoundResultSeconds     uint32
	FinalResultSeconds     uint32
}

// DefaultConfig returns the retained production rules preset.
func DefaultConfig() Config {
	return Config{
		RoundOneTimeoutSeconds: DefaultRoundOneTime,
		RoundTwoTimeoutSeconds: DefaultRoundTwoTime,
		RoundResultSeconds:     DefaultResultTime,
		FinalResultSeconds:     DefaultFinalTime,
	}
}

// Validate locks the only room-size-dependent and timer-dependent bounds.
func (config Config) Validate(playerCount int) error {
	if playerCount < MinimumPlayers || playerCount > MaximumPlayers {
		return ruleError(CodeInvalidParticipants, "player count is outside the supported range")
	}
	if !selectionTimeoutValid(config.RoundOneTimeoutSeconds) || !selectionTimeoutValid(config.RoundTwoTimeoutSeconds) {
		return ruleError(CodeInvalidConfig, "selection timeout must be 0 or 10-120 seconds")
	}
	if config.RoundResultSeconds < 2 || config.RoundResultSeconds > 15 {
		return ruleError(CodeInvalidConfig, "result timeout must be 2-15 seconds")
	}
	if config.FinalResultSeconds < 5 || config.FinalResultSeconds > 30 {
		return ruleError(CodeInvalidConfig, "final timeout must be 5-30 seconds")
	}
	return nil
}

func selectionTimeoutValid(value uint32) bool {
	return value == 0 || value >= 10 && value <= 120
}
