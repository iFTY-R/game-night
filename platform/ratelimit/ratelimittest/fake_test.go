package ratelimittest_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/iFTY-R/game-night/platform/ratelimit"
	"github.com/iFTY-R/game-night/platform/ratelimit/ratelimittest"
)

func TestFakeRecordsConsumptionOrderAndInjectedOutcomes(t *testing.T) {
	requests := usernameRequests(t)
	fake := ratelimittest.New()
	if err := fake.Reject(requests[1], 2*time.Second); err != nil {
		t.Fatal(err)
	}

	err := ratelimit.ConsumeInOrder(context.Background(), fake, requests...)
	if !errors.Is(err, ratelimit.ErrRejected) {
		t.Fatalf("expected rejection, got %v", err)
	}
	consumptions := fake.Consumptions()
	if len(consumptions) != 2 || consumptions[0] != requests[0] || consumptions[1] != requests[1] {
		t.Fatalf("unexpected consumption order: %v", consumptions)
	}

	dependencyError := errors.New("dependency failed")
	fake = ratelimittest.New()
	if err := fake.Fail(requests[0], dependencyError); err != nil {
		t.Fatal(err)
	}
	decision, err := fake.Consume(context.Background(), requests[0])
	if decision != (ratelimit.Decision{}) || !errors.Is(err, dependencyError) {
		t.Fatalf("unexpected injected failure: decision=%v error=%v", decision, err)
	}
}

func TestFakeIsConcurrentSafeAndReturnsConsumptionSnapshots(t *testing.T) {
	request := usernameRequests(t)[0]
	fake := ratelimittest.New()
	const goroutines = 64

	var wait sync.WaitGroup
	wait.Add(goroutines)
	for range goroutines {
		go func() {
			defer wait.Done()
			decision, err := fake.Consume(context.Background(), request)
			if err != nil || !decision.Allowed {
				t.Errorf("unexpected fake result: decision=%v error=%v", decision, err)
			}
		}()
	}
	wait.Wait()

	consumptions := fake.Consumptions()
	if len(consumptions) != goroutines {
		t.Fatalf("unexpected consumption count %d", len(consumptions))
	}
	consumptions[0] = ratelimit.ConsumptionRequest{}
	if fake.Consumptions()[0] != request {
		t.Fatal("fake returned mutable internal consumption storage")
	}
}

func TestFakeValidatesInjectedOutcomes(t *testing.T) {
	request := usernameRequests(t)[0]
	fake := ratelimittest.New()
	if err := fake.Reject(request, -time.Second); err == nil {
		t.Fatal("expected negative retry delay to be rejected")
	}
	if err := fake.Fail(request, nil); err == nil {
		t.Fatal("expected nil dependency error to be rejected")
	}
}

func usernameRequests(t *testing.T) []ratelimit.ConsumptionRequest {
	t.Helper()
	policy, err := ratelimit.PolicyFor(ratelimit.OperationUsernameClaim)
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]ratelimit.BucketKey, 0, 3)
	for _, item := range []struct {
		dimension ratelimit.Dimension
		value     string
	}{
		{ratelimit.DimensionIP, "ip"},
		{ratelimit.DimensionDevice, "device"},
		{ratelimit.DimensionUsername, "username"},
	} {
		value, valueErr := ratelimit.NewBucketValue(item.value)
		if valueErr != nil {
			t.Fatal(valueErr)
		}
		key, keyErr := ratelimit.NewBucketKey(item.dimension, value)
		if keyErr != nil {
			t.Fatal(keyErr)
		}
		keys = append(keys, key)
	}
	requests, err := policy.Requests(keys...)
	if err != nil {
		t.Fatal(err)
	}
	return requests
}
