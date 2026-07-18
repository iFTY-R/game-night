package security

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"time"
)

type auditKeyringDocument struct {
	ActiveVersion uint32             `json:"active_version"`
	Keys          []auditKeyDocument `json:"keys"`
}

type auditKeyDocument struct {
	Version     uint32    `json:"version"`
	PublicKey   string    `json:"public_key"`
	PrivateKey  string    `json:"private_key,omitempty"`
	NotBefore   time.Time `json:"not_before"`
	RetireAfter time.Time `json:"retire_after,omitempty"`
}

type auditKey struct {
	publicKey  ed25519.PublicKey
	privateKey ed25519.PrivateKey
}

// AuditSignature records the public verification key version alongside Ed25519 signature bytes.
type AuditSignature struct {
	KeyVersion uint32
	Value      []byte
}

// AuditKeyring owns one active private key and historical public verification keys.
type AuditKeyring struct {
	activeVersion uint32
	keys          map[uint32]auditKey
}

// LoadAuditKeyring validates read-only key material and requires the active version to include a private key.
func LoadAuditKeyring(path string, now time.Time) (*AuditKeyring, error) {
	var document auditKeyringDocument
	if err := readReadOnlyJSON(path, &document); err != nil {
		return nil, err
	}
	if document.ActiveVersion == 0 || len(document.Keys) == 0 {
		return nil, ErrInvalidKeyring
	}
	keys := make(map[uint32]auditKey, len(document.Keys))
	seenPublicKeys := make(map[[sha256.Size]byte]struct{}, len(document.Keys))
	var active auditKeyDocument
	for _, record := range document.Keys {
		if record.Version == 0 || record.NotBefore.IsZero() {
			return nil, ErrInvalidKeyring
		}
		if !record.RetireAfter.IsZero() && !record.RetireAfter.After(record.NotBefore) {
			return nil, ErrInvalidKeyring
		}
		if _, duplicate := keys[record.Version]; duplicate {
			return nil, ErrInvalidKeyring
		}
		publicKey, err := base64.StdEncoding.Strict().DecodeString(record.PublicKey)
		if err != nil || base64.StdEncoding.EncodeToString(publicKey) != record.PublicKey || len(publicKey) != ed25519.PublicKeySize {
			return nil, ErrInvalidKeyring
		}
		publicFingerprint := sha256.Sum256(publicKey)
		if _, duplicate := seenPublicKeys[publicFingerprint]; duplicate {
			return nil, ErrInvalidKeyring
		}
		seenPublicKeys[publicFingerprint] = struct{}{}
		key := auditKey{publicKey: bytes.Clone(publicKey)}
		if record.PrivateKey != "" {
			// Historical versions retain verification material only; old signing authority must leave process memory.
			if record.Version != document.ActiveVersion {
				return nil, ErrInvalidKeyring
			}
			privateKey, err := base64.StdEncoding.Strict().DecodeString(record.PrivateKey)
			if err != nil || base64.StdEncoding.EncodeToString(privateKey) != record.PrivateKey || len(privateKey) != ed25519.PrivateKeySize {
				return nil, ErrInvalidKeyring
			}
			derivedPrivateKey := ed25519.NewKeyFromSeed(privateKey[:ed25519.SeedSize])
			if !bytes.Equal(derivedPrivateKey, privateKey) || !bytes.Equal(derivedPrivateKey[ed25519.SeedSize:], publicKey) {
				return nil, ErrInvalidKeyring
			}
			key.privateKey = bytes.Clone(privateKey)
		}
		keys[record.Version] = key
		if record.Version == document.ActiveVersion {
			active = record
		}
	}
	activeKey, exists := keys[document.ActiveVersion]
	if !exists || len(activeKey.privateKey) != ed25519.PrivateKeySize || now.Before(active.NotBefore) ||
		(!active.RetireAfter.IsZero() && !now.Before(active.RetireAfter)) {
		return nil, ErrInvalidKeyring
	}
	return &AuditKeyring{activeVersion: document.ActiveVersion, keys: keys}, nil
}

// Sign signs canonical audit bytes with the active private key.
func (keyring *AuditKeyring) Sign(canonicalEvent []byte) (AuditSignature, error) {
	if len(canonicalEvent) == 0 {
		return AuditSignature{}, ErrAuthenticationInput
	}
	key := keyring.keys[keyring.activeVersion]
	return AuditSignature{
		KeyVersion: keyring.activeVersion,
		Value:      ed25519.Sign(key.privateKey, canonicalEvent),
	}, nil
}

// Verify selects a historical public key by version and rejects malformed signatures.
func (keyring *AuditKeyring) Verify(canonicalEvent []byte, signature AuditSignature) bool {
	key, exists := keyring.keys[signature.KeyVersion]
	return exists && len(canonicalEvent) > 0 && len(signature.Value) == ed25519.SignatureSize &&
		ed25519.Verify(key.publicKey, canonicalEvent, signature.Value)
}

func (keyring *AuditKeyring) fingerprints() [][sha256.Size]byte {
	result := make([][sha256.Size]byte, 0, len(keyring.keys)*2)
	for _, key := range keyring.keys {
		result = append(result, sha256.Sum256(key.publicKey))
		if len(key.privateKey) > 0 {
			result = append(result, sha256.Sum256(key.privateKey))
			// Ed25519's first 32 private-key bytes are the reusable seed and must not double as a symmetric key.
			result = append(result, sha256.Sum256(key.privateKey[:ed25519.SeedSize]))
		}
	}
	return result
}
