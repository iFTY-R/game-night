// Package ratelimit contains API transport decorators around the frozen domain limiter port.
package ratelimit

import (
	"context"

	domain "github.com/iFTY-R/game-night/platform/ratelimit"
)

// Observer receives bounded decision labels and never receives raw bucket values.
type Observer interface {
	ObserveRateLimit(domain.Operation, domain.Dimension, string)
}

// Limiter records one delegate result for metrics while preserving fail-closed behavior and error identity.
type Limiter struct {
	delegate domain.RateLimiter
	observer Observer
}

// New validates the transport wiring before a protected handler can start serving requests.
func New(delegate domain.RateLimiter, observer Observer) (*Limiter, error) {
	if delegate == nil {
		return nil, domain.ErrUnavailable
	}
	return &Limiter{delegate: delegate, observer: observer}, nil
}

// Consume delegates one typed request and emits only allowed, rejected, or unavailable.
func (limiter *Limiter) Consume(ctx context.Context, request domain.ConsumptionRequest) (domain.Decision, error) {
	if limiter == nil || limiter.delegate == nil {
		return domain.Decision{}, domain.ErrUnavailable
	}
	decision, err := limiter.delegate.Consume(ctx, request)
	result := "allowed"
	if err != nil {
		result = "unavailable"
	} else if !decision.Allowed {
		result = "rejected"
	}
	if limiter.observer != nil {
		limiter.observer.ObserveRateLimit(request.Operation(), request.Bucket().Dimension(), result)
	}
	return decision, err
}

var _ domain.RateLimiter = (*Limiter)(nil)
