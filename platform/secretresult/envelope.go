package secretresult

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"time"

	"github.com/iFTY-R/game-night/platform/security"
)

const (
	dataKeyBytes      = 32
	wrappedBlobFormat = 1
)

// EnvelopeCipher performs two-layer encryption with a per-result random data key.
type EnvelopeCipher struct {
	keyring *security.AESKeyring[security.ResultEnvelopeKeyPurpose]
}

// NewEnvelopeCipher binds result sealing to the purpose-specific immutable keyring.
func NewEnvelopeCipher(keyring *security.AESKeyring[security.ResultEnvelopeKeyPurpose]) (*EnvelopeCipher, error) {
	if keyring == nil {
		return nil, ErrInvalidInput
	}
	return &EnvelopeCipher{keyring: keyring}, nil
}

// Seal encrypts the payload with a random DEK and wraps that DEK with the active result-envelope key.
func (envelopeCipher *EnvelopeCipher) Seal(plaintext []byte, binding Binding, secretExpiresAt time.Time) (EncryptedPayload, error) {
	if len(plaintext) == 0 || binding.Validate() != nil || canonicalTime(secretExpiresAt).IsZero() {
		return EncryptedPayload{}, ErrInvalidInput
	}
	keyVersion := envelopeCipher.keyring.ActiveVersion()
	aad := buildAAD(binding, keyVersion, secretExpiresAt)
	dataKey, err := security.RandomBytes(dataKeyBytes)
	if err != nil {
		return EncryptedPayload{}, err
	}
	defer clear(dataKey)

	dataAEAD, err := newDataAEAD(dataKey)
	if err != nil {
		return EncryptedPayload{}, ErrInvalidInput
	}
	nonce, err := security.RandomBytes(dataAEAD.NonceSize())
	if err != nil {
		return EncryptedPayload{}, err
	}
	ciphertext := dataAEAD.Seal(nil, nonce, plaintext, domainAAD(aad, "payload"))
	wrapped, err := envelopeCipher.keyring.Encrypt(dataKey, domainAAD(aad, "data-key"))
	if err != nil || wrapped.KeyVersion != keyVersion {
		return EncryptedPayload{}, ErrEnvelopeAuthentication
	}
	wrappedBlob, err := encodeWrappedDataKey(wrapped)
	if err != nil {
		return EncryptedPayload{}, err
	}
	return EncryptedPayload{
		Ciphertext: ciphertext, Nonce: nonce, WrappedDataKey: wrappedBlob, KeyVersion: keyVersion,
	}, nil
}

// open authenticates every binding field after Service has verified the caller's replay capability.
func (envelopeCipher *EnvelopeCipher) open(payload EncryptedPayload, binding Binding, secretExpiresAt time.Time) ([]byte, error) {
	if binding.Validate() != nil || payload.KeyVersion == 0 || payload.Empty() {
		return nil, ErrEnvelopeAuthentication
	}
	aad := buildAAD(binding, payload.KeyVersion, secretExpiresAt)
	wrapped, err := decodeWrappedDataKey(payload)
	if err != nil {
		return nil, ErrEnvelopeAuthentication
	}
	dataKey, err := envelopeCipher.keyring.Decrypt(wrapped, domainAAD(aad, "data-key"))
	if err != nil || len(dataKey) != dataKeyBytes {
		clear(dataKey)
		return nil, ErrEnvelopeAuthentication
	}
	defer clear(dataKey)
	dataAEAD, err := newDataAEAD(dataKey)
	if err != nil || len(payload.Nonce) != dataAEAD.NonceSize() || len(payload.Ciphertext) < dataAEAD.Overhead() {
		return nil, ErrEnvelopeAuthentication
	}
	plaintext, err := dataAEAD.Open(nil, payload.Nonce, payload.Ciphertext, domainAAD(aad, "payload"))
	if err != nil {
		return nil, ErrEnvelopeAuthentication
	}
	return plaintext, nil
}

func buildAAD(binding Binding, keyVersion uint32, secretExpiresAt time.Time) []byte {
	result := []byte("game-night:secret-result:v1")
	result = appendAADField(result, []byte(binding.Key.Scope))
	result = append(result, binding.Key.ActorID[:]...)
	result = appendAADField(result, []byte(binding.Key.OperationID.Value()))
	result = append(result, binding.RequestDigest[:]...)
	result = appendAADField(result, []byte(binding.ResultType))
	result = binary.BigEndian.AppendUint32(result, binding.ResultVersion)
	result = binary.BigEndian.AppendUint32(result, keyVersion)
	result = binary.BigEndian.AppendUint64(result, uint64(canonicalTime(secretExpiresAt).UnixMicro()))
	return result
}

func appendAADField(target, value []byte) []byte {
	target = binary.BigEndian.AppendUint32(target, uint32(len(value)))
	return append(target, value...)
}

func domainAAD(base []byte, domain string) []byte {
	result := make([]byte, 0, len(base)+len(domain)+4)
	result = append(result, base...)
	return appendAADField(result, []byte(domain))
}

func encodeWrappedDataKey(wrapped security.Encrypted[security.ResultEnvelopeKeyPurpose]) ([]byte, error) {
	if len(wrapped.Nonce) == 0 || len(wrapped.Nonce) > 255 || len(wrapped.Ciphertext) == 0 {
		return nil, ErrEnvelopeAuthentication
	}
	result := make([]byte, 2, 2+len(wrapped.Nonce)+len(wrapped.Ciphertext))
	result[0] = wrappedBlobFormat
	result[1] = byte(len(wrapped.Nonce))
	result = append(result, wrapped.Nonce...)
	result = append(result, wrapped.Ciphertext...)
	return result, nil
}

func decodeWrappedDataKey(payload EncryptedPayload) (security.Encrypted[security.ResultEnvelopeKeyPurpose], error) {
	if len(payload.WrappedDataKey) < 3 || payload.WrappedDataKey[0] != wrappedBlobFormat {
		return security.Encrypted[security.ResultEnvelopeKeyPurpose]{}, ErrEnvelopeAuthentication
	}
	nonceLength := int(payload.WrappedDataKey[1])
	if nonceLength == 0 || len(payload.WrappedDataKey) <= 2+nonceLength {
		return security.Encrypted[security.ResultEnvelopeKeyPurpose]{}, ErrEnvelopeAuthentication
	}
	return security.Encrypted[security.ResultEnvelopeKeyPurpose]{
		KeyVersion: payload.KeyVersion,
		Nonce:      payload.WrappedDataKey[2 : 2+nonceLength],
		Ciphertext: payload.WrappedDataKey[2+nonceLength:],
	}, nil
}

func newDataAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
