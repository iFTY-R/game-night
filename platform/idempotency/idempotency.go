// Package idempotency defines neutral operation identifiers and request digests shared by authorization domains.
package idempotency

import (
	"bytes"
	"errors"

	"github.com/iFTY-R/game-night/platform/identifier"
)

// DigestSize fixes request bindings to SHA-256 or HMAC-SHA-256 output width.
const DigestSize = 32

var (
	// ErrInvalidInput rejects malformed operation identifiers and digest lengths without echoing values.
	ErrInvalidInput = errors.New("invalid idempotency input")
	// ErrConflict means one scoped operation ID was reused with a different request digest.
	ErrConflict = errors.New("idempotency conflict")
)

// Digest is a comparable SHA-256/HMAC-SHA-256 request binding.
type Digest [DigestSize]byte

// NewDigest copies an exact 256-bit digest into an immutable value.
func NewDigest(value []byte) (Digest, error) {
	if len(value) != DigestSize {
		return Digest{}, ErrInvalidInput
	}
	var digest Digest
	copy(digest[:], value)
	return digest, nil
}

// Bytes returns an independent persistence representation.
func (digest Digest) Bytes() []byte {
	return bytes.Clone(digest[:])
}

// OperationID is canonical Raw Base64URL containing at least 128 bits of caller randomness.
type OperationID struct {
	selector identifier.Selector
}

// NewOperationID encodes caller-generated entropy without weakening the 128-bit lower bound.
func NewOperationID(entropy []byte) (OperationID, error) {
	selector, err := identifier.NewSelector(entropy)
	if err != nil {
		return OperationID{}, ErrInvalidInput
	}
	return OperationID{selector: selector}, nil
}

// ParseOperationID validates the canonical wire and database representation.
func ParseOperationID(value string) (OperationID, error) {
	selector, err := identifier.ParseSelector(value)
	if err != nil {
		return OperationID{}, ErrInvalidInput
	}
	return OperationID{selector: selector}, nil
}

// Value returns the canonical text used in composite operation keys and AAD.
func (operationID OperationID) Value() string {
	return operationID.selector.Value()
}

// Valid reports whether a constructor established the entropy and encoding invariants.
func (operationID OperationID) Valid() bool {
	return operationID.selector.ByteLength() >= identifier.MinimumSelectorBytes
}
