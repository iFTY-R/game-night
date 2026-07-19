package ratelimit

import (
	"context"
	"errors"
	"testing"
	"time"

	domain "github.com/iFTY-R/game-night/platform/ratelimit"
	"github.com/iFTY-R/game-night/platform/ratelimit/ratelimittest"
)

func TestLimiterObservesDelegateOutcomeWithoutChangingIt(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*ratelimittest.Fake, domain.ConsumptionRequest) error
		wantResult string
		wantError  error
		wantAllow  bool
	}{
		{name: "allowed", wantResult: "allowed", wantAllow: true},
		{
			name: "rejected",
			configure: func(fake *ratelimittest.Fake, request domain.ConsumptionRequest) error {
				return fake.Reject(request, time.Second)
			},
			wantResult: "rejected",
		},
		{
			name: "unavailable",
			configure: func(fake *ratelimittest.Fake, request domain.ConsumptionRequest) error {
				return fake.Fail(request, domain.ErrUnavailable)
			},
			wantResult: "unavailable", wantError: domain.ErrUnavailable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := transportConsumptionRequest(t)
			fake := ratelimittest.New()
			if test.configure != nil {
				if err := test.configure(fake, request); err != nil {
					t.Fatal(err)
				}
			}
			observer := &decisionObserver{}
			limiter, err := New(fake, observer)
			if err != nil {
				t.Fatal(err)
			}
			decision, consumeErr := limiter.Consume(context.Background(), request)
			if !errors.Is(consumeErr, test.wantError) || decision.Allowed != test.wantAllow {
				t.Fatalf("decision = %+v, err = %v", decision, consumeErr)
			}
			if observer.operation != request.Operation() || observer.dimension != request.Bucket().Dimension() || observer.result != test.wantResult {
				t.Fatalf("observation = %+v", observer)
			}
		})
	}
}

func transportConsumptionRequest(t testing.TB) domain.ConsumptionRequest {
	t.Helper()
	value, err := domain.NewBucketValue("203.0.113.20")
	if err != nil {
		t.Fatal(err)
	}
	key, err := domain.NewBucketKey(domain.DimensionIP, value)
	if err != nil {
		t.Fatal(err)
	}
	request, err := domain.NewConsumptionRequest(domain.OperationIdentityBootstrap, key)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

type decisionObserver struct {
	operation domain.Operation
	dimension domain.Dimension
	result    string
}

func (observer *decisionObserver) ObserveRateLimit(operation domain.Operation, dimension domain.Dimension, result string) {
	observer.operation = operation
	observer.dimension = dimension
	observer.result = result
}
