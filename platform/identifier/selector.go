package identifier

import (
	"encoding/base64"
	"errors"
)

const (
	// MinimumSelectorBytes enforces the design's 128-bit lower bound for public recovery and challenge selectors.
	MinimumSelectorBytes = 16
	// MaximumSelectorBytes bounds untrusted token components before they reach indexes, caches, or logs.
	MaximumSelectorBytes = 64
	// maximumSelectorEncodedBytes rejects oversized input before Base64 decoding allocates proportional memory.
	maximumSelectorEncodedBytes = (MaximumSelectorBytes*8 + 5) / 6
)

// ErrInvalidSelector is intentionally input-agnostic so token-shaped values cannot leak through error wrapping.
var ErrInvalidSelector = errors.New("selector is invalid")

// Selector is a canonical, unpadded Base64URL public lookup component.
// It is not a secret, but strict canonical encoding prevents multiple textual keys from naming the same bytes.
type Selector struct {
	value      string
	byteLength uint8
}

// NewSelector encodes validated selector entropy using canonical unpadded Base64URL.
func NewSelector(entropy []byte) (Selector, error) {
	if len(entropy) < MinimumSelectorBytes || len(entropy) > MaximumSelectorBytes {
		return Selector{}, ErrInvalidSelector
	}
	return Selector{
		value:      base64.RawURLEncoding.EncodeToString(entropy),
		byteLength: uint8(len(entropy)),
	}, nil
}

// ParseSelector validates a canonical token component without trimming or otherwise repairing untrusted input.
func ParseSelector(input string) (Selector, error) {
	if len(input) > maximumSelectorEncodedBytes {
		return Selector{}, ErrInvalidSelector
	}
	decoded, err := base64.RawURLEncoding.DecodeString(input)
	if err != nil || len(decoded) < MinimumSelectorBytes || len(decoded) > MaximumSelectorBytes {
		return Selector{}, ErrInvalidSelector
	}
	if base64.RawURLEncoding.EncodeToString(decoded) != input {
		return Selector{}, ErrInvalidSelector
	}
	return Selector{value: input, byteLength: uint8(len(decoded))}, nil
}

// Value returns the canonical representation used in tokens, database lookups, and keyed rate limits.
func (selector Selector) Value() string {
	return selector.value
}

// ByteLength reports the decoded entropy size without exposing or reallocating the selector bytes.
func (selector Selector) ByteLength() int {
	return int(selector.byteLength)
}
