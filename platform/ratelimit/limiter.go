package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrRejected identifies an authoritative capacity rejection that callers may map to a stable throttled response.
	ErrRejected = errors.New("rate limit rejected")
	// ErrUnavailable identifies a fail-closed dependency, wiring, request, or adapter-contract failure.
	ErrUnavailable = errors.New("rate limit unavailable")
)

// FailureKind is intentionally limited to outcomes a domain service may safely distinguish.
type FailureKind string

const (
	// FailureRejected means the limiter was available and denied the requested bucket consumption.
	FailureRejected FailureKind = "rejected"
	// FailureUnavailable means the operation must stop because capacity could not be authoritatively consumed.
	FailureUnavailable FailureKind = "unavailable"
)

// String returns the stable failure label suitable for bounded metrics.
func (kind FailureKind) String() string {
	return string(kind)
}

// ConsumptionRequest is one unit-cost bucket consumption within a reviewed operation policy.
// Operation and bucket fields are private so zero or cross-policy requests are rejected before use.
type ConsumptionRequest struct {
	operation Operation
	bucket    BucketKey
}

// NewConsumptionRequest builds a request only for a known operation and a valid bucket key.
// Prefer Policy.Requests in domain services because it additionally enforces the complete method order.
func NewConsumptionRequest(operation Operation, bucket BucketKey) (ConsumptionRequest, error) {
	policy, err := PolicyFor(operation)
	if err != nil {
		return ConsumptionRequest{}, err
	}
	if err := bucket.validate(); err != nil {
		return ConsumptionRequest{}, err
	}
	if !policy.contains(bucket.dimension) {
		return ConsumptionRequest{}, fmt.Errorf("rate limit operation %q does not consume dimension %q", operation, bucket.dimension)
	}
	return ConsumptionRequest{operation: operation, bucket: bucket}, nil
}

// Operation returns the stable method policy label and contains no actor identifier.
func (request ConsumptionRequest) Operation() Operation {
	return request.operation
}

// Bucket returns the strongly typed dimension and redacting value object consumed by this request.
func (request ConsumptionRequest) Bucket() BucketKey {
	return request.bucket
}

// Validate allows adapters and test fakes to reject manually constructed zero values fail closed.
func (request ConsumptionRequest) Validate() error {
	policy, err := PolicyFor(request.operation)
	if err != nil {
		return err
	}
	if err := request.bucket.validate(); err != nil {
		return err
	}
	if !policy.contains(request.bucket.dimension) {
		return fmt.Errorf("rate limit operation %q does not consume dimension %q", request.operation, request.bucket.dimension)
	}
	return nil
}

// String omits raw bucket material while retaining enough context for safe diagnostics.
func (request ConsumptionRequest) String() string {
	return request.operation.String() + "/" + request.bucket.String()
}

// Format overrides every fmt verb so Go-syntax formatting cannot traverse into the sensitive bucket value.
func (request ConsumptionRequest) Format(state fmt.State, _ rune) {
	_, _ = state.Write([]byte(request.String()))
}

// Decision is the authoritative result of one atomic bucket consumption.
// A malformed decision is treated as unavailable, never as allowed, by ConsumeInOrder.
type Decision struct {
	Allowed    bool
	RetryAfter time.Duration
}

// Allow constructs a successful decision without a retry delay.
func Allow() Decision {
	return Decision{Allowed: true}
}

// Reject constructs a denied decision with the suggested minimum retry delay.
// Negative values remain representable so an adapter bug is caught and failed closed by ConsumeInOrder.
func Reject(retryAfter time.Duration) Decision {
	return Decision{RetryAfter: retryAfter}
}

func (decision Decision) validate() error {
	if decision.RetryAfter < 0 {
		return errors.New("rate limit retry delay cannot be negative")
	}
	if decision.Allowed && decision.RetryAfter != 0 {
		return errors.New("allowed rate limit decision cannot include a retry delay")
	}
	return nil
}

// RateLimiter is the storage-independent domain port implemented by the Redis adapter and test fakes.
// Any error means capacity was not consumed authoritatively and therefore denies the protected operation.
type RateLimiter interface {
	Consume(context.Context, ConsumptionRequest) (Decision, error)
}

// ConsumeInOrder validates the full request list, then consumes sequentially and stops at the first failure.
// It converts rejected and dependency outcomes into stable failures so callers cannot accidentally fail open.
func ConsumeInOrder(ctx context.Context, limiter RateLimiter, requests ...ConsumptionRequest) error {
	if len(requests) == 0 {
		return unavailableFailure(Operation(""), Dimension(""), errors.New("rate limit consumption requests are required"))
	}
	policy, err := PolicyFor(requests[0].operation)
	if err != nil {
		return unavailableFailure(requests[0].operation, requests[0].bucket.dimension, err)
	}
	if len(requests) != int(policy.count) {
		return unavailableFailure(requests[0].operation, requests[0].bucket.dimension, fmt.Errorf("rate limit operation %q requires %d buckets, got %d", policy.operation, policy.count, len(requests)))
	}
	for index, request := range requests {
		if err := request.Validate(); err != nil {
			return unavailableFailure(request.operation, request.bucket.dimension, err)
		}
		if request.operation != policy.operation {
			return unavailableFailure(request.operation, request.bucket.dimension, errors.New("rate limit consumption requests cannot mix operations"))
		}
		if expected := policy.dimensions[index]; request.bucket.dimension != expected {
			return unavailableFailure(request.operation, request.bucket.dimension, fmt.Errorf("rate limit operation %q bucket %d requires dimension %q", policy.operation, index, expected))
		}
	}
	if ctx == nil {
		return unavailableFailure(requests[0].operation, requests[0].bucket.dimension, errors.New("rate limit context is required"))
	}
	if limiter == nil {
		return unavailableFailure(requests[0].operation, requests[0].bucket.dimension, errors.New("rate limiter is required"))
	}

	for _, request := range requests {
		if err := ctx.Err(); err != nil {
			return unavailableFailure(request.operation, request.bucket.dimension, err)
		}
		decision, err := limiter.Consume(ctx, request)
		if err != nil {
			return unavailableFailure(request.operation, request.bucket.dimension, err)
		}
		if err := decision.validate(); err != nil {
			return unavailableFailure(request.operation, request.bucket.dimension, err)
		}
		if !decision.Allowed {
			return rejectedFailure(request.operation, request.bucket.dimension, decision.RetryAfter)
		}
	}
	return nil
}

// Failure records only stable labels and retry metadata; the sensitive bucket value is never retained.
type Failure struct {
	kind       FailureKind
	operation  Operation
	dimension  Dimension
	retryAfter time.Duration
	cause      error
}

// Kind distinguishes authoritative throttling from fail-closed unavailability.
func (failure *Failure) Kind() FailureKind {
	return failure.kind
}

// Operation identifies the reviewed policy that stopped without exposing actor or bucket input.
func (failure *Failure) Operation() Operation {
	return failure.operation
}

// Dimension identifies the non-sensitive bucket class that stopped.
func (failure *Failure) Dimension() Dimension {
	return failure.dimension
}

// RetryAfter returns the limiter-provided delay for rejections and zero for unavailable failures.
func (failure *Failure) RetryAfter() time.Duration {
	return failure.retryAfter
}

// Error contains stable labels only; dependency causes are deliberately not rendered to avoid secret-bearing errors.
func (failure *Failure) Error() string {
	return fmt.Sprintf("rate limit %s for operation %q dimension %q", failure.kind, failure.operation, failure.dimension)
}

// Unwrap preserves machine-readable dependency errors for cancellation and diagnostics without formatting them.
func (failure *Failure) Unwrap() error {
	return failure.cause
}

// Is supports errors.Is checks against ErrRejected and ErrUnavailable.
func (failure *Failure) Is(target error) bool {
	switch target {
	case ErrRejected:
		return failure.kind == FailureRejected
	case ErrUnavailable:
		return failure.kind == FailureUnavailable
	default:
		return false
	}
}

func rejectedFailure(operation Operation, dimension Dimension, retryAfter time.Duration) *Failure {
	return &Failure{
		kind:       FailureRejected,
		operation:  operation,
		dimension:  dimension,
		retryAfter: retryAfter,
	}
}

func unavailableFailure(operation Operation, dimension Dimension, cause error) *Failure {
	return &Failure{
		kind:      FailureUnavailable,
		operation: operation,
		dimension: dimension,
		cause:     cause,
	}
}
