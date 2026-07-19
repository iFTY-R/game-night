// Package redis implements non-authoritative rate-limit persistence with Redis.
package redis

import (
	"errors"
	"math"
	"time"

	"github.com/iFTY-R/game-night/platform/ratelimit"
)

// Rule defines one token bucket's burst capacity, single-token refill period, and idle retention.
type Rule struct {
	Capacity    uint32
	RefillEvery time.Duration
	TTL         time.Duration
}

// Rules maps the reviewed domain operation and dimension pair to one storage-level token policy.
type Rules map[ratelimit.Operation]map[ratelimit.Dimension]Rule

// supportedOperations freezes complete rule coverage to the domain policy set reviewed for Redis persistence.
var supportedOperations = [...]ratelimit.Operation{
	ratelimit.OperationIdentityBootstrap,
	ratelimit.OperationUsernameCheck,
	ratelimit.OperationUsernameClaim,
	ratelimit.OperationIdentityRecoveryLookup,
	ratelimit.OperationIdentityRecoveryResolved,
	ratelimit.OperationAdminPasswordLogin,
	ratelimit.OperationAdminSecondFactor,
	ratelimit.OperationRealNameRead,
	ratelimit.OperationRealNameUpdate,
	ratelimit.OperationProfileExport,
}

// StandardRules returns conservative security defaults; callers receive an independent map for tests or reviewed tuning.
func StandardRules() Rules {
	return Rules{
		ratelimit.OperationIdentityBootstrap: {
			ratelimit.DimensionIP: standardRule(12, 10*time.Second),
		},
		ratelimit.OperationUsernameCheck: {
			ratelimit.DimensionIP:       standardRule(60, time.Second),
			ratelimit.DimensionDevice:   standardRule(30, 2*time.Second),
			ratelimit.DimensionUsername: standardRule(10, 6*time.Second),
		},
		ratelimit.OperationUsernameClaim: {
			ratelimit.DimensionIP:       standardRule(20, 3*time.Second),
			ratelimit.DimensionDevice:   standardRule(10, 6*time.Second),
			ratelimit.DimensionUsername: standardRule(5, 12*time.Second),
		},
		ratelimit.OperationIdentityRecoveryLookup: {
			ratelimit.DimensionIP:               standardRule(12, 30*time.Second),
			ratelimit.DimensionRecoverySelector: standardRule(5, time.Minute),
		},
		ratelimit.OperationIdentityRecoveryResolved: {
			ratelimit.DimensionUser: standardRule(5, 5*time.Minute),
		},
		ratelimit.OperationAdminPasswordLogin: {
			ratelimit.DimensionIP:           standardRule(10, time.Minute),
			ratelimit.DimensionAdminAccount: standardRule(5, 3*time.Minute),
		},
		ratelimit.OperationAdminSecondFactor: {
			ratelimit.DimensionIP:           standardRule(20, 30*time.Second),
			ratelimit.DimensionAdminAccount: standardRule(10, time.Minute),
			ratelimit.DimensionFlowPurpose:  standardRule(5, time.Minute),
		},
		ratelimit.OperationRealNameRead: {
			ratelimit.DimensionAdminSession: standardRule(60, time.Second),
			ratelimit.DimensionTargetUser:   standardRule(30, 2*time.Second),
		},
		ratelimit.OperationRealNameUpdate: {
			ratelimit.DimensionAdminSession: standardRule(20, 3*time.Second),
			ratelimit.DimensionTargetUser:   standardRule(10, 6*time.Second),
		},
		ratelimit.OperationProfileExport: {
			ratelimit.DimensionAdminSession: standardRule(10, time.Minute),
			ratelimit.DimensionTargetUser:   standardRule(5, 2*time.Minute),
		},
	}
}

func standardRule(capacity uint32, refillEvery time.Duration) Rule {
	// Two complete refill windows retain exhausted buckets without allowing expiry to replenish them early.
	return Rule{Capacity: capacity, RefillEvery: refillEvery, TTL: 2 * time.Duration(capacity) * refillEvery}
}

func validateAndCloneRules(rules Rules) (Rules, error) {
	if len(rules) != len(supportedOperations) {
		return nil, errors.New("invalid Redis rate-limit rules")
	}
	result := make(Rules, len(rules))
	for _, operation := range supportedOperations {
		policy, err := ratelimit.PolicyFor(operation)
		if err != nil {
			return nil, errors.New("invalid Redis rate-limit rules")
		}
		dimensions, exists := rules[operation]
		if !exists || len(dimensions) != len(policy.Dimensions()) {
			return nil, errors.New("invalid Redis rate-limit rules")
		}
		clonedDimensions := make(map[ratelimit.Dimension]Rule, len(dimensions))
		for _, dimension := range policy.Dimensions() {
			rule, exists := dimensions[dimension]
			if !exists || !validRule(rule) {
				return nil, errors.New("invalid Redis rate-limit rules")
			}
			clonedDimensions[dimension] = rule
		}
		result[operation] = clonedDimensions
	}
	for operation := range rules {
		if !containsOperation(operation) {
			return nil, errors.New("invalid Redis rate-limit rules")
		}
	}
	return result, nil
}

func validRule(rule Rule) bool {
	if rule.Capacity == 0 || rule.RefillEvery < time.Millisecond || rule.TTL < time.Millisecond ||
		rule.RefillEvery > time.Duration(math.MaxInt64/int64(rule.Capacity)) {
		return false
	}
	return rule.TTL >= time.Duration(rule.Capacity)*rule.RefillEvery
}

func containsOperation(candidate ratelimit.Operation) bool {
	for _, operation := range supportedOperations {
		if candidate == operation {
			return true
		}
	}
	return false
}
