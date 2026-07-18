package ratelimit

import (
	"context"
	"fmt"
)

// Operation is a stable method-level namespace for bucket configuration, metrics, and HMAC storage keys.
// User, admin, and privileged profile operations remain distinct even when they consume the same dimension type.
type Operation string

const (
	// OperationIdentityBootstrap protects anonymous creation of a pending device identity.
	OperationIdentityBootstrap Operation = "identity.bootstrap"
	// OperationUsernameCheck protects availability probes before any username claim is attempted.
	OperationUsernameCheck Operation = "identity.username_check"
	// OperationUsernameClaim protects onboarding and rename claims with the same ordered dimensions.
	OperationUsernameClaim Operation = "identity.username_claim"
	// OperationIdentityRecoveryLookup covers the anonymous IP and selector phase before identity resolution.
	OperationIdentityRecoveryLookup Operation = "identity.recovery_lookup"
	// OperationIdentityRecoveryResolved is consumed only after a selector resolves to an authoritative user.
	OperationIdentityRecoveryResolved Operation = "identity.recovery_resolved"
	// OperationAdminPasswordLogin protects password verification independently from user authentication capacity.
	OperationAdminPasswordLogin Operation = "admin.password_login"
	// OperationAdminSecondFactor protects TOTP and recovery-code verification using a fixed flow-purpose bucket.
	OperationAdminSecondFactor Operation = "admin.second_factor"
	// OperationRealNameRead protects privileged PII reads before decryption or disclosure audit work begins.
	OperationRealNameRead Operation = "admin.real_name_read"
	// OperationRealNameUpdate protects privileged PII writes before encryption or disclosure audit work begins.
	OperationRealNameUpdate Operation = "admin.real_name_update"
	// OperationProfileExport protects export creation and paging before materialization or decryption.
	OperationProfileExport Operation = "admin.profile_export"
)

// String returns the stable operation label suitable for bounded metrics and adapter namespaces.
func (operation Operation) String() string {
	return string(operation)
}

// Valid reports whether the operation has a reviewed method policy.
func (operation Operation) Valid() bool {
	_, ok := policies[operation]
	return ok
}

// Policy is an immutable method contract containing the exact bucket dimension order.
// Fixed storage avoids exposing mutable package-level slices through copied policy values.
type Policy struct {
	operation  Operation
	dimensions [3]Dimension
	count      uint8
}

// policies is the reviewed cross-domain limiter matrix; changes alter security capacity and require explicit review.
var policies = map[Operation]Policy{
	OperationIdentityBootstrap:        newPolicy(OperationIdentityBootstrap, DimensionIP),
	OperationUsernameCheck:            newPolicy(OperationUsernameCheck, DimensionIP, DimensionDevice, DimensionUsername),
	OperationUsernameClaim:            newPolicy(OperationUsernameClaim, DimensionIP, DimensionDevice, DimensionUsername),
	OperationIdentityRecoveryLookup:   newPolicy(OperationIdentityRecoveryLookup, DimensionIP, DimensionRecoverySelector),
	OperationIdentityRecoveryResolved: newPolicy(OperationIdentityRecoveryResolved, DimensionUser),
	OperationAdminPasswordLogin:       newPolicy(OperationAdminPasswordLogin, DimensionIP, DimensionAdminAccount),
	OperationAdminSecondFactor:        newPolicy(OperationAdminSecondFactor, DimensionIP, DimensionAdminAccount, DimensionFlowPurpose),
	OperationRealNameRead:             newPolicy(OperationRealNameRead, DimensionAdminSession, DimensionTargetUser),
	OperationRealNameUpdate:           newPolicy(OperationRealNameUpdate, DimensionAdminSession, DimensionTargetUser),
	OperationProfileExport:            newPolicy(OperationProfileExport, DimensionAdminSession, DimensionTargetUser),
}

// PolicyFor returns the immutable ordering contract for one protected operation.
func PolicyFor(operation Operation) (Policy, error) {
	policy, ok := policies[operation]
	if !ok {
		return Policy{}, fmt.Errorf("rate limit operation %q is invalid", operation)
	}
	return policy, nil
}

// Operation returns the stable method namespace carried by every request created from the policy.
func (policy Policy) Operation() Operation {
	return policy.operation
}

// Dimensions returns a caller-owned copy of the required consumption order.
func (policy Policy) Dimensions() []Dimension {
	dimensions := make([]Dimension, int(policy.count))
	copy(dimensions, policy.dimensions[:policy.count])
	return dimensions
}

// Requests binds already typed keys to this method and rejects missing, extra, or misordered dimensions.
// The complete list is checked before any request is returned, preventing partial consumption on caller mistakes.
func (policy Policy) Requests(keys ...BucketKey) ([]ConsumptionRequest, error) {
	if _, err := PolicyFor(policy.operation); err != nil {
		return nil, err
	}
	if len(keys) != int(policy.count) {
		return nil, fmt.Errorf("rate limit operation %q requires %d buckets, got %d", policy.operation, policy.count, len(keys))
	}

	requests := make([]ConsumptionRequest, len(keys))
	for index, key := range keys {
		if err := key.validate(); err != nil {
			return nil, fmt.Errorf("rate limit operation %q bucket %d: %w", policy.operation, index, err)
		}
		expected := policy.dimensions[index]
		if key.dimension != expected {
			return nil, fmt.Errorf("rate limit operation %q bucket %d requires dimension %q, got %q", policy.operation, index, expected, key.dimension)
		}
		requests[index] = ConsumptionRequest{operation: policy.operation, bucket: key}
	}
	return requests, nil
}

// Consume binds the complete method policy and consumes its buckets sequentially with fail-closed semantics.
func (policy Policy) Consume(ctx context.Context, limiter RateLimiter, keys ...BucketKey) error {
	requests, err := policy.Requests(keys...)
	if err != nil {
		return unavailableFailure(policy.operation, Dimension(""), err)
	}
	return ConsumeInOrder(ctx, limiter, requests...)
}

func (policy Policy) contains(dimension Dimension) bool {
	for index := range int(policy.count) {
		if policy.dimensions[index] == dimension {
			return true
		}
	}
	return false
}

func newPolicy(operation Operation, dimensions ...Dimension) Policy {
	if len(dimensions) == 0 || len(dimensions) > 3 {
		panic("rate limit policy must contain between one and three dimensions")
	}
	policy := Policy{operation: operation, count: uint8(len(dimensions))}
	copy(policy.dimensions[:], dimensions)
	return policy
}
