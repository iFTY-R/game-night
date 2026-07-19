package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"time"
)

// ErrAuthenticationInput rejects empty values that would otherwise create shared constant digests.
var ErrAuthenticationInput = errors.New("invalid authentication input")

// MAC binds a digest to the key version needed after rotations.
type MAC[P HMACKeyPurpose] struct {
	KeyVersion uint32
	Value      []byte
}

// HMACKeyring indexes or authenticates exactly one purpose with HMAC-SHA-256.
type HMACKeyring[P HMACKeyPurpose] struct {
	keys *symmetricKeys
}

// LoadHMACKeyring validates a read-only JSON keyring with keys of at least 256 bits.
func LoadHMACKeyring[P HMACKeyPurpose](path string, now time.Time) (*HMACKeyring[P], error) {
	keys, err := loadSymmetricKeys(path, now, 0)
	if err != nil {
		return nil, err
	}
	return &HMACKeyring[P]{keys: keys}, nil
}

// ActiveVersion returns the immutable version used for newly created digests and credential proofs.
func (keyring *HMACKeyring[P]) ActiveVersion() uint32 {
	if keyring == nil || keyring.keys == nil {
		return 0
	}
	version, _ := keyring.keys.active()
	return version
}

// Sum returns a versioned HMAC digest without retaining the input value.
func (keyring *HMACKeyring[P]) Sum(value []byte) (MAC[P], error) {
	if len(value) == 0 {
		return MAC[P]{}, ErrAuthenticationInput
	}
	version, key := keyring.keys.active()
	return MAC[P]{KeyVersion: version, Value: sumHMAC(key, value)}, nil
}

// Verify selects the recorded historical key and compares the digest in constant time.
func (keyring *HMACKeyring[P]) Verify(value []byte, expected MAC[P]) (bool, error) {
	if len(value) == 0 || len(expected.Value) != sha256.Size {
		return false, ErrAuthenticationInput
	}
	key, err := keyring.keys.version(expected.KeyVersion)
	if err != nil {
		return false, err
	}
	return hmac.Equal(sumHMAC(key, value), expected.Value), nil
}

func sumHMAC(key, value []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(value)
	return mac.Sum(nil)
}
