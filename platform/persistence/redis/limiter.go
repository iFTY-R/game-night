package redis

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/iFTY-R/game-night/platform/ratelimit"
	"github.com/iFTY-R/game-night/platform/security"
	goredis "github.com/redis/go-redis/v9"
)

// maximumOperationTimeout prevents a Redis failure from holding a protected request beyond its useful lifetime.
const maximumOperationTimeout = 30 * time.Second

var (
	// redisKeyPrefixPattern keeps operator-defined namespaces bounded and free of Redis glob characters.
	redisKeyPrefixPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_-]{0,62}:$`)
	// ErrInvalidConfig reports rejected construction without echoing URLs, prefixes, or key material.
	ErrInvalidConfig = errors.New("invalid Redis rate-limit configuration")
	// ErrUnavailable hides Redis connection, authentication, script, and response details from domain callers.
	ErrUnavailable = errors.New("Redis rate limiter unavailable")
)

// Config contains the already-validated operational namespace, deadline, and complete bucket rule matrix.
type Config struct {
	KeyPrefix string
	Timeout   time.Duration
	Rules     Rules
}

// Limiter implements the frozen domain port without retaining any raw bucket value after key derivation.
type Limiter struct {
	client    goredis.Scripter
	keyring   *security.HMACKeyring[security.RateLimitHMACKeyPurpose]
	keyPrefix string
	timeout   time.Duration
	rules     Rules
}

// NewLimiter validates complete policy coverage before accepting traffic that must fail closed.
func NewLimiter(
	client goredis.Scripter,
	keyring *security.HMACKeyring[security.RateLimitHMACKeyPurpose],
	config Config,
) (*Limiter, error) {
	if client == nil || keyring == nil || !redisKeyPrefixPattern.MatchString(config.KeyPrefix) ||
		config.Timeout < time.Millisecond || config.Timeout > maximumOperationTimeout {
		return nil, ErrInvalidConfig
	}
	rules, err := validateAndCloneRules(config.Rules)
	if err != nil {
		return nil, ErrInvalidConfig
	}
	return &Limiter{client: client, keyring: keyring, keyPrefix: config.KeyPrefix, timeout: config.Timeout, rules: rules}, nil
}

// Consume atomically refills and spends one token, returning only stable decisions or sanitized errors.
func (limiter *Limiter) Consume(ctx context.Context, request ratelimit.ConsumptionRequest) (ratelimit.Decision, error) {
	if limiter == nil || ctx == nil || request.Validate() != nil {
		return ratelimit.Decision{}, ErrUnavailable
	}
	rule, exists := limiter.rules[request.Operation()][request.Bucket().Dimension()]
	if !exists {
		return ratelimit.Decision{}, ErrUnavailable
	}
	key, err := limiter.storageKey(request)
	if err != nil {
		return ratelimit.Decision{}, ErrUnavailable
	}
	limitedContext, cancel := context.WithTimeout(ctx, limiter.timeout)
	defer cancel()
	values, err := tokenBucketScript.Run(
		limitedContext,
		limiter.client,
		[]string{key},
		int64(rule.Capacity),
		rule.RefillEvery.Microseconds(),
		rule.TTL.Milliseconds(),
	).Slice()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ratelimit.Decision{}, ctxErr
		}
		return ratelimit.Decision{}, ErrUnavailable
	}
	allowed, retryMilliseconds, ok := parseScriptDecision(values)
	if !ok {
		return ratelimit.Decision{}, ErrUnavailable
	}
	if allowed {
		return ratelimit.Allow(), nil
	}
	return ratelimit.Reject(time.Duration(retryMilliseconds) * time.Millisecond), nil
}

func (limiter *Limiter) storageKey(request ratelimit.ConsumptionRequest) (string, error) {
	operation := request.Operation().String()
	dimension := request.Bucket().Dimension().String()
	mac, err := limiter.keyring.Sum([]byte(operation + "\x00" + dimension + "\x00" + request.Bucket().Value().Reveal()))
	if err != nil || mac.KeyVersion == 0 || len(mac.Value) == 0 {
		return "", ErrUnavailable
	}
	digest := base64.RawURLEncoding.EncodeToString(mac.Value)
	return fmt.Sprintf("%sratelimit:v%d:%s:%s:%s", limiter.keyPrefix, mac.KeyVersion, operation, dimension, digest), nil
}

func parseScriptDecision(values []any) (bool, int64, bool) {
	if len(values) != 2 {
		return false, 0, false
	}
	allowed, allowedOK := values[0].(int64)
	retryMilliseconds, retryOK := values[1].(int64)
	if !allowedOK || !retryOK || (allowed != 0 && allowed != 1) || retryMilliseconds < 0 ||
		(allowed == 1 && retryMilliseconds != 0) || (allowed == 0 && retryMilliseconds == 0) {
		return false, 0, false
	}
	return allowed == 1, retryMilliseconds, true
}

var _ ratelimit.RateLimiter = (*Limiter)(nil)
