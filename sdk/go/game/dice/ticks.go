package dice

import "errors"

const (
	// TicksPerUnit is the platform-wide integer scale for one abstract penalty unit.
	TicksPerUnit Ticks = 4
)

var (
	// ErrTicksOverflow rejects arithmetic that cannot be represented by uint32.
	ErrTicksOverflow = errors.New("ticks overflow")
	// ErrTicksUnderflow rejects subtracting more pool ticks than are available.
	ErrTicksUnderflow = errors.New("ticks underflow")
)

// Ticks stores abstract penalty or pool amounts without floating-point rounding.
type Ticks uint32

// Add returns the exact sum or an overflow error.
func (value Ticks) Add(other Ticks) (Ticks, error) {
	if ^uint32(0)-uint32(value) < uint32(other) {
		return 0, ErrTicksOverflow
	}
	return value + other, nil
}

// Sub returns the exact difference or an underflow error.
func (value Ticks) Sub(other Ticks) (Ticks, error) {
	if other > value {
		return 0, ErrTicksUnderflow
	}
	return value - other, nil
}

// HalfFloor and HalfCeil define deterministic halves for odd tick counts.
func HalfFloor(value Ticks) Ticks { return value / 2 }

func HalfCeil(value Ticks) Ticks {
	return value/2 + value%2
}

// Remaining returns the unfilled capacity or an underflow error for invalid pools.
func Remaining(capacity, current Ticks) (Ticks, error) {
	if current > capacity {
		return 0, ErrTicksUnderflow
	}
	return capacity - current, nil
}

// NextAddAmount returns the smallest legal amount for a capacity-limited add action.
// A final remainder smaller than step is still legal so odd half-pool results cannot deadlock.
func NextAddAmount(capacity, current, step Ticks) (Ticks, error) {
	if step == 0 {
		return 0, ErrTicksUnderflow
	}
	remaining, err := Remaining(capacity, current)
	if err != nil {
		return 0, err
	}
	if remaining == 0 {
		return 0, nil
	}
	if remaining < step {
		return remaining, nil
	}
	return step, nil
}
