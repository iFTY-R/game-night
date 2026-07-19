package game

import "encoding/base64"

const (
	// MinimumActionIDBytes enforces the platform-wide 128-bit caller-randomness floor.
	MinimumActionIDBytes = 16
	// MaximumActionIDBytes bounds durable composite keys before they reach indexes, caches, or logs.
	MaximumActionIDBytes = 64
	// maximumActionIDEncodedBytes rejects oversized action IDs before Base64 decoding allocates proportional memory.
	maximumActionIDEncodedBytes = (MaximumActionIDBytes*8 + 5) / 6
)

// ActionID is a canonical, unpadded Raw Base64URL idempotency key supplied by the authenticated caller.
type ActionID string

// ParseActionID validates the SDK representation without depending on platform implementation packages.
func ParseActionID(value string) (ActionID, error) {
	if len(value) > maximumActionIDEncodedBytes {
		return "", ErrInvalidContract
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) < MinimumActionIDBytes || len(decoded) > MaximumActionIDBytes ||
		base64.RawURLEncoding.EncodeToString(decoded) != value {
		return "", ErrInvalidContract
	}
	return ActionID(value), nil
}

// Valid reports whether the value is a canonical bounded caller-generated idempotency key.
func (actionID ActionID) Valid() bool {
	_, err := ParseActionID(string(actionID))
	return err == nil
}
