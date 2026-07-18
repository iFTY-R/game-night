// Package security provides secret generation, hashing, signing, and encryption primitives.
package security

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
)

// maxRandomBytes prevents accidental unbounded allocation through a security helper.
const maxRandomBytes = 4096

// ErrInvalidRandomLength reports a caller programming error without allocating or reading entropy.
var ErrInvalidRandomLength = errors.New("invalid random byte length")

// RandomBytes returns exactly length bytes from the operating system CSPRNG.
func RandomBytes(length int) ([]byte, error) {
	if length <= 0 || length > maxRandomBytes {
		return nil, ErrInvalidRandomLength
	}
	value := make([]byte, length)
	if _, err := rand.Read(value); err != nil {
		return nil, errors.New("read cryptographic randomness")
	}
	return value, nil
}

// RandomBase64URL returns a canonical, unpadded URL-safe encoding of random bytes.
func RandomBase64URL(byteLength int) (string, error) {
	value, err := RandomBytes(byteLength)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
