package security

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"
)

func TestRandomBytesReturnsRequestedIndependentValues(t *testing.T) {
	first, err := RandomBytes(32)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RandomBytes(32)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 32 || len(second) != 32 {
		t.Fatalf("unexpected random lengths: first=%d second=%d", len(first), len(second))
	}
	if bytes.Equal(first, second) {
		t.Fatal("independent random values unexpectedly matched")
	}
}

func TestRandomBytesRejectsUnboundedLength(t *testing.T) {
	for _, length := range []int{0, -1, maxRandomBytes + 1} {
		if _, err := RandomBytes(length); !errors.Is(err, ErrInvalidRandomLength) {
			t.Fatalf("length %d: expected invalid length, got %v", length, err)
		}
	}
}

func TestRandomBase64URLUsesRequestedEntropy(t *testing.T) {
	encoded, err := RandomBase64URL(32)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 32 {
		t.Fatalf("expected 32 decoded bytes, got %d", len(decoded))
	}
}
