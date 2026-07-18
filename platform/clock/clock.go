// Package clock provides UTC-only time sources for production code and deterministic tests.
package clock

import (
	"errors"
	"sync"
	"time"
)

// ErrNegativeAdvance prevents tests from hiding expiry and ordering bugs by moving time backwards.
var ErrNegativeAdvance = errors.New("clock advance cannot be negative")

// Clock supplies application-owned timestamps. Implementations must return UTC values so domains do
// not inherit process-local timezone configuration before persisting or comparing time boundaries.
type Clock interface {
	Now() time.Time
}

// System is the production wall clock. It has no configuration or mutable process state.
type System struct{}

// Now returns the current wall time in UTC without a process-local monotonic component.
func (System) Now() time.Time {
	return normalizeUTC(time.Now())
}

// Fake is a concurrency-safe deterministic clock for domain and adapter tests.
// Its zero value is usable and represents the zero instant in UTC.
type Fake struct {
	mu  sync.RWMutex
	now time.Time
}

// NewFake creates a fake clock at start after normalizing its location to UTC.
func NewFake(start time.Time) *Fake {
	return &Fake{now: normalizeUTC(start)}
}

// Now returns a snapshot of the fake clock's current UTC instant.
func (clock *Fake) Now() time.Time {
	clock.mu.RLock()
	defer clock.mu.RUnlock()
	return normalizeUTC(clock.now)
}

// Advance atomically moves the clock forward by delta and returns the new UTC instant.
// A negative delta is rejected without mutating the clock because security expiry tests assume time is monotonic.
func (clock *Fake) Advance(delta time.Duration) (time.Time, error) {
	if delta < 0 {
		return time.Time{}, ErrNegativeAdvance
	}

	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = normalizeUTC(clock.now.Add(delta))
	return clock.now, nil
}

// normalizeUTC also strips monotonic metadata so values remain stable across persistence round trips.
func normalizeUTC(value time.Time) time.Time {
	return value.Round(0).UTC()
}

var _ Clock = System{}
var _ Clock = (*Fake)(nil)
