package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"time"
)

var (
	// ErrAuthenticationFailed intentionally merges wrong-key, wrong-AAD, and corrupted-ciphertext failures.
	ErrAuthenticationFailed = errors.New("ciphertext authentication failed")
	// ErrEncryptionInput rejects missing plaintext or AAD before consuming randomness.
	ErrEncryptionInput = errors.New("invalid encryption input")
)

// Encrypted carries the exact key version and nonce required for future rotation-safe decryption.
type Encrypted[P AESKeyPurpose] struct {
	KeyVersion uint32
	Nonce      []byte
	Ciphertext []byte
}

// AESKeyring encrypts one data domain with AES-256-GCM and retains historical decryption keys.
type AESKeyring[P AESKeyPurpose] struct {
	keys *symmetricKeys
}

// LoadAESKeyring validates a read-only JSON keyring and requires every key to be exactly 256 bits.
func LoadAESKeyring[P AESKeyPurpose](path string, now time.Time) (*AESKeyring[P], error) {
	keys, err := loadSymmetricKeys(path, now, 32)
	if err != nil {
		return nil, err
	}
	return &AESKeyring[P]{keys: keys}, nil
}

// Encrypt seals non-empty plaintext under the active key and caller-supplied domain AAD.
func (keyring *AESKeyring[P]) Encrypt(plaintext, associatedData []byte) (Encrypted[P], error) {
	if len(plaintext) == 0 || len(associatedData) == 0 {
		return Encrypted[P]{}, ErrEncryptionInput
	}
	version, key := keyring.keys.active()
	aead, err := newGCM(key)
	if err != nil {
		return Encrypted[P]{}, ErrInvalidKeyring
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Encrypted[P]{}, errors.New("read encryption nonce randomness")
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, associatedData)
	return Encrypted[P]{KeyVersion: version, Nonce: nonce, Ciphertext: ciphertext}, nil
}

// Decrypt authenticates version, nonce, ciphertext, and AAD before returning a new plaintext buffer.
func (keyring *AESKeyring[P]) Decrypt(payload Encrypted[P], associatedData []byte) ([]byte, error) {
	key, err := keyring.keys.version(payload.KeyVersion)
	if err != nil {
		return nil, err
	}
	aead, err := newGCM(key)
	if err != nil {
		return nil, ErrInvalidKeyring
	}
	if len(associatedData) == 0 || len(payload.Nonce) != aead.NonceSize() || len(payload.Ciphertext) < aead.Overhead() {
		return nil, ErrAuthenticationFailed
	}
	plaintext, err := aead.Open(nil, payload.Nonce, payload.Ciphertext, associatedData)
	if err != nil {
		return nil, ErrAuthenticationFailed
	}
	return plaintext, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
