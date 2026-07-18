package ratelimit

import (
	"errors"
	"fmt"
	"strings"
)

// Dimension is a stable, non-sensitive bucket label used by policy checks, metrics, and adapters.
// Values remain deliberately closed so a caller cannot silently introduce an unreviewed limiter key space.
type Dimension string

const (
	// DimensionIP limits a client address after trusted-proxy resolution; its value must never be logged raw.
	DimensionIP Dimension = "ip"
	// DimensionDevice preserves per-device capacity when a username or challenge value changes.
	DimensionDevice Dimension = "device"
	// DimensionUsername isolates normalized username probes and claims from device and IP capacity.
	DimensionUsername Dimension = "username"
	// DimensionRecoverySelector prevents recovery selector rotation from replenishing the IP bucket.
	DimensionRecoverySelector Dimension = "recovery_selector"
	// DimensionUser is consumed only after recovery has resolved a selector to an authoritative user.
	DimensionUser Dimension = "user"
	// DimensionAdminAccount preserves account capacity across refreshed admin login challenges.
	DimensionAdminAccount Dimension = "admin_account"
	// DimensionFlowPurpose separates fixed admin second-factor flows without using a refreshable challenge as capacity.
	DimensionFlowPurpose Dimension = "flow_purpose"
	// DimensionAdminSession limits privileged profile access independently for each authenticated admin session.
	DimensionAdminSession Dimension = "admin_session"
	// DimensionTargetUser protects one profile target from access spread across multiple admin sessions.
	DimensionTargetUser Dimension = "target_user"
)

// Valid reports whether the dimension belongs to the reviewed domain contract.
func (dimension Dimension) Valid() bool {
	switch dimension {
	case DimensionIP,
		DimensionDevice,
		DimensionUsername,
		DimensionRecoverySelector,
		DimensionUser,
		DimensionAdminAccount,
		DimensionFlowPurpose,
		DimensionAdminSession,
		DimensionTargetUser:
		return true
	default:
		return false
	}
}

// String returns the stable dimension label and never includes its associated sensitive value.
func (dimension Dimension) String() string {
	return string(dimension)
}

// BucketValue owns the raw input that a persistence adapter will convert into a versioned HMAC key.
// Its fields are private and its formatter is redacted to reduce accidental disclosure in logs and test failures.
type BucketValue struct {
	raw string
}

// NewBucketValue validates presence without normalizing the value; canonicalization remains the owning domain's job.
func NewBucketValue(raw string) (BucketValue, error) {
	if strings.TrimSpace(raw) == "" {
		return BucketValue{}, errors.New("rate limit bucket value is required")
	}
	return BucketValue{raw: raw}, nil
}

// Reveal explicitly returns sensitive key material for HMAC derivation by the rate-limit persistence adapter.
// Callers must not place the result in logs, metrics, traces, or user-visible errors.
func (value BucketValue) Reveal() string {
	return value.raw
}

// String redacts sensitive bucket material under ordinary formatting.
func (value BucketValue) String() string {
	return "[REDACTED]"
}

// Format overrides every fmt verb, including %#v, so reflection-style formatting cannot expose the private raw field.
func (value BucketValue) Format(state fmt.State, _ rune) {
	_, _ = state.Write([]byte("[REDACTED]"))
}

func (value BucketValue) valid() bool {
	return strings.TrimSpace(value.raw) != ""
}

// BucketKey pairs one reviewed dimension with sensitive input while keeping both strongly typed.
type BucketKey struct {
	dimension Dimension
	value     BucketValue
}

// NewBucketKey rejects zero values and unknown dimensions before any limiter capacity can be partially consumed.
func NewBucketKey(dimension Dimension, value BucketValue) (BucketKey, error) {
	if !dimension.Valid() {
		return BucketKey{}, errors.New("rate limit bucket dimension is invalid")
	}
	if !value.valid() {
		return BucketKey{}, errors.New("rate limit bucket value is invalid")
	}
	return BucketKey{dimension: dimension, value: value}, nil
}

// Dimension returns the stable, non-sensitive label used to select a bucket policy.
func (key BucketKey) Dimension() Dimension {
	return key.dimension
}

// Value returns the redacting value object; raw access still requires an explicit Reveal call.
func (key BucketKey) Value() BucketValue {
	return key.value
}

// String exposes only the dimension so formatting a key cannot reveal its raw input.
func (key BucketKey) String() string {
	return key.dimension.String() + ":[REDACTED]"
}

// Format overrides every fmt verb so nested Go-syntax formatting cannot inspect the key's private fields.
func (key BucketKey) Format(state fmt.State, _ rune) {
	_, _ = state.Write([]byte(key.String()))
}

func (key BucketKey) validate() error {
	if !key.dimension.Valid() {
		return errors.New("rate limit bucket dimension is invalid")
	}
	if !key.value.valid() {
		return errors.New("rate limit bucket value is invalid")
	}
	return nil
}
