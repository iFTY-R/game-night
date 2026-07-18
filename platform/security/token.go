package security

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
)

const (
	// maxEncodedTokenLength bounds parser work before splitting attacker-controlled input.
	maxEncodedTokenLength = 1024
	// maxTokenSelectorLength bounds public selectors used as indexed database lookup keys.
	maxTokenSelectorLength = 128
)

var (
	// ErrInvalidToken is intentionally stable and never includes submitted token material.
	ErrInvalidToken  = errors.New("invalid token")
	tokenPartPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// TokenPolicy declares the accepted version and decoded secret-size range for one token family.
type TokenPolicy struct {
	Version        string
	MinSecretBytes int
	MaxSecretBytes int
}

// ParsedToken separates the public selector from secret bytes without retaining the encoded input.
type ParsedToken struct {
	Version  string
	Selector string
	Secret   []byte
}

// FormatToken constructs the strict version.selector.secret wire format used by long-lived credentials.
func FormatToken(version, selector string, secret []byte) (string, error) {
	if !validTokenPart(version, 16) || !validTokenPart(selector, maxTokenSelectorLength) || len(secret) == 0 {
		return "", ErrInvalidToken
	}
	return version + "." + selector + "." + base64.RawURLEncoding.EncodeToString(secret), nil
}

// ParseToken performs bounded structural validation and canonical base64url decoding.
func ParseToken(encoded string, policy TokenPolicy) (ParsedToken, error) {
	if len(encoded) == 0 || len(encoded) > maxEncodedTokenLength || !validPolicy(policy) {
		return ParsedToken{}, ErrInvalidToken
	}
	parts := strings.Split(encoded, ".")
	if len(parts) != 3 || parts[0] != policy.Version || !validTokenPart(parts[1], maxTokenSelectorLength) {
		return ParsedToken{}, ErrInvalidToken
	}
	secret, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil || len(secret) < policy.MinSecretBytes || len(secret) > policy.MaxSecretBytes {
		return ParsedToken{}, ErrInvalidToken
	}
	if base64.RawURLEncoding.EncodeToString(secret) != parts[2] {
		return ParsedToken{}, ErrInvalidToken
	}
	return ParsedToken{Version: parts[0], Selector: parts[1], Secret: secret}, nil
}

// ConstantTimeEqual compares authentication material without data-dependent byte comparison.
func ConstantTimeEqual(left, right []byte) bool {
	return subtle.ConstantTimeCompare(left, right) == 1
}

func validTokenPart(value string, maxLength int) bool {
	return len(value) > 0 && len(value) <= maxLength && tokenPartPattern.MatchString(value)
}

func validPolicy(policy TokenPolicy) bool {
	return validTokenPart(policy.Version, 16) && policy.MinSecretBytes > 0 &&
		policy.MaxSecretBytes >= policy.MinSecretBytes && policy.MaxSecretBytes <= maxRandomBytes
}
