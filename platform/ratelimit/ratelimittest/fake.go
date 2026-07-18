// Package ratelimittest provides a concurrent-safe RateLimiter fake for domain service tests.
package ratelimittest

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/iFTY-R/game-night/platform/ratelimit"
)

// Fake records the serialized order observed by Consume and supports deterministic per-request outcomes.
// A newly constructed fake allows valid requests so tests only configure the security branch they exercise.
type Fake struct {
	mu           sync.Mutex
	consumptions []ratelimit.ConsumptionRequest
	outcomes     map[ratelimit.ConsumptionRequest]outcome
}

type outcome struct {
	decision ratelimit.Decision
	err      error
}

// New returns an allow-by-default fake with isolated outcome and call storage.
func New() *Fake {
	return &Fake{outcomes: make(map[ratelimit.ConsumptionRequest]outcome)}
}

// Consume implements ratelimit.RateLimiter and records each call before returning its injected outcome.
func (fake *Fake) Consume(ctx context.Context, request ratelimit.ConsumptionRequest) (ratelimit.Decision, error) {
	if ctx == nil {
		return ratelimit.Decision{}, errors.New("rate limit fake context is required")
	}
	if err := ctx.Err(); err != nil {
		return ratelimit.Decision{}, err
	}
	if err := request.Validate(); err != nil {
		return ratelimit.Decision{}, err
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.consumptions = append(fake.consumptions, request)
	configured, ok := fake.outcomes[request]
	if !ok {
		return ratelimit.Allow(), nil
	}
	return configured.decision, configured.err
}

// Reject configures every matching request to return an authoritative denial with the supplied retry delay.
func (fake *Fake) Reject(request ratelimit.ConsumptionRequest, retryAfter time.Duration) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if retryAfter < 0 {
		return errors.New("rate limit fake retry delay cannot be negative")
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.outcomes[request] = outcome{decision: ratelimit.Reject(retryAfter)}
	return nil
}

// Fail configures every matching request to return a dependency error, allowing fail-closed branches to be tested.
func (fake *Fake) Fail(request ratelimit.ConsumptionRequest, dependencyError error) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if dependencyError == nil {
		return errors.New("rate limit fake dependency error is required")
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.outcomes[request] = outcome{err: dependencyError}
	return nil
}

// Consumptions returns a caller-owned snapshot in the exact serialized order observed by the fake.
func (fake *Fake) Consumptions() []ratelimit.ConsumptionRequest {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]ratelimit.ConsumptionRequest(nil), fake.consumptions...)
}
