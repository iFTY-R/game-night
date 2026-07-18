package ratelimit_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iFTY-R/game-night/platform/ratelimit"
)

func TestPoliciesFreezeMethodBucketOrder(t *testing.T) {
	tests := []struct {
		operation  ratelimit.Operation
		dimensions []ratelimit.Dimension
	}{
		{ratelimit.OperationIdentityBootstrap, []ratelimit.Dimension{ratelimit.DimensionIP}},
		{ratelimit.OperationUsernameCheck, []ratelimit.Dimension{ratelimit.DimensionIP, ratelimit.DimensionDevice, ratelimit.DimensionUsername}},
		{ratelimit.OperationUsernameClaim, []ratelimit.Dimension{ratelimit.DimensionIP, ratelimit.DimensionDevice, ratelimit.DimensionUsername}},
		{ratelimit.OperationIdentityRecoveryLookup, []ratelimit.Dimension{ratelimit.DimensionIP, ratelimit.DimensionRecoverySelector}},
		{ratelimit.OperationIdentityRecoveryResolved, []ratelimit.Dimension{ratelimit.DimensionUser}},
		{ratelimit.OperationAdminPasswordLogin, []ratelimit.Dimension{ratelimit.DimensionIP, ratelimit.DimensionAdminAccount}},
		{ratelimit.OperationAdminSecondFactor, []ratelimit.Dimension{ratelimit.DimensionIP, ratelimit.DimensionAdminAccount, ratelimit.DimensionFlowPurpose}},
		{ratelimit.OperationRealNameRead, []ratelimit.Dimension{ratelimit.DimensionAdminSession, ratelimit.DimensionTargetUser}},
		{ratelimit.OperationRealNameUpdate, []ratelimit.Dimension{ratelimit.DimensionAdminSession, ratelimit.DimensionTargetUser}},
		{ratelimit.OperationProfileExport, []ratelimit.Dimension{ratelimit.DimensionAdminSession, ratelimit.DimensionTargetUser}},
	}

	for _, test := range tests {
		t.Run(test.operation.String(), func(t *testing.T) {
			policy, err := ratelimit.PolicyFor(test.operation)
			if err != nil {
				t.Fatal(err)
			}
			if got := policy.Dimensions(); !reflect.DeepEqual(got, test.dimensions) {
				t.Fatalf("unexpected dimensions: got %v want %v", got, test.dimensions)
			}

			// Mutating a caller-owned snapshot must not change the package-level policy contract.
			got := policy.Dimensions()
			got[0] = ratelimit.DimensionIP
			if test.dimensions[0] == ratelimit.DimensionIP {
				got[0] = ratelimit.DimensionUser
			}
			if reflect.DeepEqual(policy.Dimensions(), got) {
				t.Fatal("policy dimensions leaked mutable internal storage")
			}
		})
	}

	if _, err := ratelimit.PolicyFor(ratelimit.Operation("unknown")); err == nil {
		t.Fatal("expected unknown operation to be rejected")
	}
	if !ratelimit.OperationUsernameClaim.Valid() || ratelimit.Operation("unknown").Valid() {
		t.Fatal("operation validity did not match the reviewed policy set")
	}
}

func TestPolicyRequestsRejectMissingOrMisorderedBucketsBeforeConsumption(t *testing.T) {
	policy := mustPolicy(t, ratelimit.OperationUsernameClaim)
	ip := mustBucket(t, ratelimit.DimensionIP, "ip")
	device := mustBucket(t, ratelimit.DimensionDevice, "device")
	username := mustBucket(t, ratelimit.DimensionUsername, "username")

	requests, err := policy.Requests(ip, device, username)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 3 || requests[0].Bucket() != ip || requests[2].Bucket() != username {
		t.Fatalf("unexpected requests: %v", requests)
	}

	if _, err := policy.Requests(ip, username, device); err == nil {
		t.Fatal("expected misordered dimensions to be rejected")
	}
	if _, err := policy.Requests(ip, device); err == nil {
		t.Fatal("expected missing dimension to be rejected")
	}
}

func TestConsumptionRequestFormattingNeverExposesBucketValue(t *testing.T) {
	policy := mustPolicy(t, ratelimit.OperationIdentityBootstrap)
	request := mustRequests(t, policy, mustBucket(t, ratelimit.DimensionIP, "sensitive-address"))[0]
	for _, formatted := range []string{fmt.Sprint(request), fmt.Sprintf("%#v", request)} {
		if strings.Contains(formatted, "sensitive-address") {
			t.Fatalf("consumption request formatting exposed bucket value: %q", formatted)
		}
	}
}

func TestConsumeInOrderStopsAndClassifiesRejection(t *testing.T) {
	policy := mustPolicy(t, ratelimit.OperationUsernameClaim)
	requests := mustRequests(t, policy,
		mustBucket(t, ratelimit.DimensionIP, "ip"),
		mustBucket(t, ratelimit.DimensionDevice, "device"),
		mustBucket(t, ratelimit.DimensionUsername, "username"),
	)
	limiter := &scriptedLimiter{
		decisions: []ratelimit.Decision{
			ratelimit.Allow(),
			ratelimit.Reject(3 * time.Second),
			ratelimit.Allow(),
		},
	}

	err := ratelimit.ConsumeInOrder(context.Background(), limiter, requests...)
	if !errors.Is(err, ratelimit.ErrRejected) || errors.Is(err, ratelimit.ErrUnavailable) {
		t.Fatalf("expected rejected classification, got %v", err)
	}
	failure := requireFailure(t, err)
	if failure.Operation() != ratelimit.OperationUsernameClaim || failure.Dimension() != ratelimit.DimensionDevice {
		t.Fatalf("unexpected failure location: operation=%s dimension=%s", failure.Operation(), failure.Dimension())
	}
	if failure.RetryAfter() != 3*time.Second {
		t.Fatalf("unexpected retry delay %s", failure.RetryAfter())
	}
	if !reflect.DeepEqual(limiter.requests, requests[:2]) {
		t.Fatalf("limiter continued after rejection: %v", limiter.requests)
	}
}

func TestConsumeInOrderFailsClosedOnDependencyError(t *testing.T) {
	policy := mustPolicy(t, ratelimit.OperationAdminPasswordLogin)
	requests := mustRequests(t, policy,
		mustBucket(t, ratelimit.DimensionIP, "ip"),
		mustBucket(t, ratelimit.DimensionAdminAccount, "admin"),
	)
	dependencyError := errors.New("redis unavailable")
	limiter := &scriptedLimiter{decisions: []ratelimit.Decision{ratelimit.Allow()}, errors: map[int]error{1: dependencyError}}

	err := ratelimit.ConsumeInOrder(context.Background(), limiter, requests...)
	if !errors.Is(err, ratelimit.ErrUnavailable) || !errors.Is(err, dependencyError) {
		t.Fatalf("expected fail-closed dependency error, got %v", err)
	}
	failure := requireFailure(t, err)
	if failure.Kind() != ratelimit.FailureUnavailable || failure.Dimension() != ratelimit.DimensionAdminAccount {
		t.Fatalf("unexpected failure: kind=%s dimension=%s", failure.Kind(), failure.Dimension())
	}
	if len(limiter.requests) != 2 {
		t.Fatalf("unexpected request count %d", len(limiter.requests))
	}
	if got := err.Error(); got == "" || strings.Contains(got, dependencyError.Error()) {
		t.Fatalf("failure string leaked dependency details: %q", got)
	}
}

func TestConsumeInOrderFailsClosedOnInvalidDecisionAndNilLimiter(t *testing.T) {
	request := mustRequests(t, mustPolicy(t, ratelimit.OperationIdentityBootstrap), mustBucket(t, ratelimit.DimensionIP, "ip"))[0]

	for name, limiter := range map[string]ratelimit.RateLimiter{
		"negative retry": &scriptedLimiter{decisions: []ratelimit.Decision{{Allowed: false, RetryAfter: -time.Second}}},
		"allowed retry":  &scriptedLimiter{decisions: []ratelimit.Decision{{Allowed: true, RetryAfter: time.Second}}},
		"nil limiter":    nil,
	} {
		t.Run(name, func(t *testing.T) {
			err := ratelimit.ConsumeInOrder(context.Background(), limiter, request)
			if !errors.Is(err, ratelimit.ErrUnavailable) {
				t.Fatalf("expected unavailable classification, got %v", err)
			}
		})
	}
}

func TestPolicyConsumeUsesTheFrozenOrder(t *testing.T) {
	policy := mustPolicy(t, ratelimit.OperationIdentityRecoveryLookup)
	ip := mustBucket(t, ratelimit.DimensionIP, "ip")
	selector := mustBucket(t, ratelimit.DimensionRecoverySelector, "selector")
	limiter := &scriptedLimiter{}

	if err := policy.Consume(context.Background(), limiter, ip, selector); err != nil {
		t.Fatal(err)
	}
	want := mustRequests(t, policy, ip, selector)
	if !reflect.DeepEqual(limiter.requests, want) {
		t.Fatalf("unexpected consumption order: got %v want %v", limiter.requests, want)
	}
}

func TestConsumeInOrderFailsClosedOnCanceledContextWithoutCallingLimiter(t *testing.T) {
	request := mustRequests(t, mustPolicy(t, ratelimit.OperationIdentityBootstrap), mustBucket(t, ratelimit.DimensionIP, "ip"))[0]
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	limiter := &scriptedLimiter{}

	err := ratelimit.ConsumeInOrder(ctx, limiter, request)
	if !errors.Is(err, ratelimit.ErrUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled fail-closed result, got %v", err)
	}
	if len(limiter.requests) != 0 {
		t.Fatalf("canceled request reached limiter: %v", limiter.requests)
	}
}

func TestConsumeInOrderRejectsPolicyBypassBeforeCallingLimiter(t *testing.T) {
	ip := mustBucket(t, ratelimit.DimensionIP, "ip")
	device := mustBucket(t, ratelimit.DimensionDevice, "device")
	username := mustBucket(t, ratelimit.DimensionUsername, "username")
	ordered := make([]ratelimit.ConsumptionRequest, 0, 3)
	for _, key := range []ratelimit.BucketKey{ip, device, username} {
		request, err := ratelimit.NewConsumptionRequest(ratelimit.OperationUsernameClaim, key)
		if err != nil {
			t.Fatal(err)
		}
		ordered = append(ordered, request)
	}
	limiter := &scriptedLimiter{}

	err := ratelimit.ConsumeInOrder(context.Background(), limiter, ordered[0], ordered[2], ordered[1])
	if !errors.Is(err, ratelimit.ErrUnavailable) {
		t.Fatalf("expected misordered direct requests to fail closed, got %v", err)
	}
	if len(limiter.requests) != 0 {
		t.Fatalf("misordered requests reached limiter: %v", limiter.requests)
	}

	user := mustBucket(t, ratelimit.DimensionUser, "user")
	if _, err := ratelimit.NewConsumptionRequest(ratelimit.OperationUsernameClaim, user); err == nil {
		t.Fatal("expected a dimension outside the operation policy to be rejected")
	}
}

type scriptedLimiter struct {
	decisions []ratelimit.Decision
	errors    map[int]error
	requests  []ratelimit.ConsumptionRequest
}

func (limiter *scriptedLimiter) Consume(_ context.Context, request ratelimit.ConsumptionRequest) (ratelimit.Decision, error) {
	index := len(limiter.requests)
	limiter.requests = append(limiter.requests, request)
	if err := limiter.errors[index]; err != nil {
		return ratelimit.Decision{}, err
	}
	if index >= len(limiter.decisions) {
		return ratelimit.Allow(), nil
	}
	return limiter.decisions[index], nil
}

func mustBucket(t *testing.T, dimension ratelimit.Dimension, value string) ratelimit.BucketKey {
	t.Helper()
	bucketValue, err := ratelimit.NewBucketValue(value)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ratelimit.NewBucketKey(dimension, bucketValue)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func mustPolicy(t *testing.T, operation ratelimit.Operation) ratelimit.Policy {
	t.Helper()
	policy, err := ratelimit.PolicyFor(operation)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func mustRequests(t *testing.T, policy ratelimit.Policy, keys ...ratelimit.BucketKey) []ratelimit.ConsumptionRequest {
	t.Helper()
	requests, err := policy.Requests(keys...)
	if err != nil {
		t.Fatal(err)
	}
	return requests
}

func requireFailure(t *testing.T, err error) *ratelimit.Failure {
	t.Helper()
	var failure *ratelimit.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("expected rate limit failure, got %T: %v", err, err)
	}
	return failure
}
