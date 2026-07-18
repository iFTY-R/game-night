package security

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"
	"time"
)

// maxKeyringFileBytes bounds startup parsing of mounted secret files.
const maxKeyringFileBytes = 1 << 20

var (
	// ErrInvalidKeyring reports an unsafe or malformed keyring without exposing key bytes.
	ErrInvalidKeyring = errors.New("invalid keyring")
	// ErrUnknownKeyVersion prevents callers from silently falling back to an active but incorrect key.
	ErrUnknownKeyVersion = errors.New("unknown key version")
)

// Purpose markers make independently managed keyrings impossible to interchange at compile time.
type (
	PIIKeyPurpose            struct{}
	TOTPKeyPurpose           struct{}
	ResultEnvelopeKeyPurpose struct{}
	DeviceHMACKeyPurpose     struct{}
	RateLimitHMACKeyPurpose  struct{}
	UserChallengeKeyPurpose  struct{}
	AdminChallengeKeyPurpose struct{}
)

// AESKeyPurpose restricts encryption keyrings to the three independently encrypted data domains.
type AESKeyPurpose interface {
	PIIKeyPurpose | TOTPKeyPurpose | ResultEnvelopeKeyPurpose
}

// HMACKeyPurpose restricts digest keyrings to one authentication or indexing domain.
type HMACKeyPurpose interface {
	DeviceHMACKeyPurpose | RateLimitHMACKeyPurpose | UserChallengeKeyPurpose | AdminChallengeKeyPurpose
}

type keyringDocument struct {
	ActiveVersion uint32        `json:"active_version"`
	Keys          []keyDocument `json:"keys"`
}

type keyDocument struct {
	Version     uint32    `json:"version"`
	Key         string    `json:"key"`
	NotBefore   time.Time `json:"not_before"`
	RetireAfter time.Time `json:"retire_after,omitempty"`
}

type symmetricKeys struct {
	activeVersion uint32
	keys          map[uint32][]byte
}

func loadSymmetricKeys(path string, now time.Time, exactLength int) (*symmetricKeys, error) {
	var document keyringDocument
	if err := readReadOnlyJSON(path, &document); err != nil {
		return nil, err
	}
	if document.ActiveVersion == 0 || len(document.Keys) == 0 {
		return nil, ErrInvalidKeyring
	}

	keys := make(map[uint32][]byte, len(document.Keys))
	seenMaterial := make(map[[sha256.Size]byte]struct{}, len(document.Keys))
	var active keyDocument
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
		material, err := base64.StdEncoding.Strict().DecodeString(record.Key)
		if err != nil || base64.StdEncoding.EncodeToString(material) != record.Key ||
			len(material) < 32 || (exactLength > 0 && len(material) != exactLength) {
			return nil, ErrInvalidKeyring
		}
		fingerprint := sha256.Sum256(material)
		if _, duplicate := seenMaterial[fingerprint]; duplicate {
			return nil, ErrInvalidKeyring
		}
		seenMaterial[fingerprint] = struct{}{}
		keys[record.Version] = bytes.Clone(material)
		if record.Version == document.ActiveVersion {
			active = record
		}
	}
	if active.Version == 0 || now.Before(active.NotBefore) ||
		(!active.RetireAfter.IsZero() && !now.Before(active.RetireAfter)) {
		return nil, ErrInvalidKeyring
	}
	return &symmetricKeys{activeVersion: document.ActiveVersion, keys: keys}, nil
}

func (keys *symmetricKeys) active() (uint32, []byte) {
	return keys.activeVersion, keys.keys[keys.activeVersion]
}

func (keys *symmetricKeys) version(version uint32) ([]byte, error) {
	key, exists := keys.keys[version]
	if !exists {
		return nil, ErrUnknownKeyVersion
	}
	return key, nil
}

func (keys *symmetricKeys) fingerprints() [][sha256.Size]byte {
	result := make([][sha256.Size]byte, 0, len(keys.keys))
	for _, key := range keys.keys {
		result = append(result, sha256.Sum256(key))
	}
	return result
}

func readReadOnlyJSON(path string, target any) error {
	info, err := os.Lstat(path)
	if err != nil || !secureKeyringFileMode(info.Mode()) {
		return ErrInvalidKeyring
	}
	file, err := os.Open(path)
	if err != nil {
		return ErrInvalidKeyring
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	// Revalidate the opened object so a path replacement cannot bypass the mode and regular-file checks.
	if err != nil || !secureKeyringFileMode(openedInfo.Mode()) || !os.SameFile(info, openedInfo) {
		return ErrInvalidKeyring
	}
	contents, err := io.ReadAll(io.LimitReader(file, maxKeyringFileBytes+1))
	if err != nil || len(contents) > maxKeyringFileBytes {
		return ErrInvalidKeyring
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrInvalidKeyring
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrInvalidKeyring
	}
	return nil
}

func secureKeyringFileMode(mode os.FileMode) bool {
	if !mode.IsRegular() {
		return false
	}
	// Go exposes Windows' read-only attribute as 0444 without the ACL owner/group distinction.
	if runtime.GOOS == "windows" {
		return mode.Perm()&0o222 == 0
	}
	return mode.Perm() == 0o400
}
