package security

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestTokenRoundTrip(t *testing.T) {
	secret := bytes.Repeat([]byte{0x42}, 32)
	encoded, err := FormatToken("v1", "72bcad55-4d5d-4142-a4b8-5f1fcceb32fd", secret)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseToken(encoded, TokenPolicy{
		Version:        "v1",
		MinSecretBytes: 32,
		MaxSecretBytes: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Version != "v1" || parsed.Selector != "72bcad55-4d5d-4142-a4b8-5f1fcceb32fd" {
		t.Fatalf("unexpected parsed token metadata: %+v", parsed)
	}
	if !bytes.Equal(parsed.Secret, secret) {
		t.Fatal("parsed token secret differs")
	}
}

func TestTokenParserRejectsMalformedInputWithoutEcho(t *testing.T) {
	secretInput := "v1.selector.super-secret.invalid"
	tests := []string{
		secretInput,
		"v2.selector.c2VjcmV0",
		"v1.bad selector.c2VjcmV0",
		"v1.selector.***",
		strings.Repeat("x", maxEncodedTokenLength+1),
	}
	for _, raw := range tests {
		_, err := ParseToken(raw, TokenPolicy{Version: "v1", MinSecretBytes: 6, MaxSecretBytes: 32})
		if !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("expected invalid token for input length %d, got %v", len(raw), err)
		}
		if strings.Contains(err.Error(), raw) || strings.Contains(err.Error(), "super-secret") {
			t.Fatalf("token error echoed input: %v", err)
		}
	}
}

func TestConstantTimeEqual(t *testing.T) {
	if !ConstantTimeEqual([]byte("same"), []byte("same")) {
		t.Fatal("equal values did not match")
	}
	if ConstantTimeEqual([]byte("same"), []byte("different")) {
		t.Fatal("different values matched")
	}
}
