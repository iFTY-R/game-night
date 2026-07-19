package profile

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/security"
	"golang.org/x/text/unicode/norm"
)

const (
	// MaximumRealNameCodePoints bounds normalization and prevents oversized PII records.
	MaximumRealNameCodePoints = 128
	// MaximumRealNameInputBytes bounds normalization work before the code-point limit is applied.
	MaximumRealNameInputBytes = MaximumRealNameCodePoints * 16
)

const piiAADDomain = "game-night:profile:pii-aad:v1\x00"

// PIIAssociatedData constructs canonical, field-bound data for AES-GCM authentication.
// The user UUID and field are separated and the schema version is fixed-width to prevent ambiguity.
func PIIAssociatedData(userID uuid.UUID, field Field, schemaVersion uint32) ([]byte, error) {
	if userID == uuid.Nil || !validAADField(field) || schemaVersion == 0 || schemaVersion > math.MaxInt32 {
		return nil, ErrInvalidProfileInput
	}
	data := make([]byte, 0, len(piiAADDomain)+36+1+len(field)+1+4)
	data = append(data, piiAADDomain...)
	data = append(data, userID.String()...)
	data = append(data, 0)
	data = append(data, string(field)...)
	data = append(data, 0)
	var version [4]byte
	binary.BigEndian.PutUint32(version[:], schemaVersion)
	data = append(data, version[:]...)
	return data, nil
}

func validAADField(field Field) bool {
	value := string(field)
	if len(value) == 0 || len(value) > 64 || !utf8.ValidString(value) || strings.TrimSpace(value) != value || strings.ContainsRune(value, '\x00') {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return false
		}
	}
	return true
}

// BuildPIIAAD is an explicit alias for callers that prefer a builder-style name.
func BuildPIIAAD(userID uuid.UUID, field Field, schemaVersion uint32) ([]byte, error) {
	return PIIAssociatedData(userID, field, schemaVersion)
}

// PIIProtector owns the PII-specific AES keyring and associated-data schema.
type PIIProtector struct {
	keyring       *security.AESKeyring[security.PIIKeyPurpose]
	schemaVersion uint32
}

// NewPIIProtector binds a validated PII keyring to one explicit schema version.
func NewPIIProtector(keyring *security.AESKeyring[security.PIIKeyPurpose], schemaVersion uint32) (*PIIProtector, error) {
	if keyring == nil || schemaVersion == 0 || schemaVersion > math.MaxInt32 {
		return nil, ErrInvalidProfileInput
	}
	return &PIIProtector{keyring: keyring, schemaVersion: schemaVersion}, nil
}

// NewDefaultPIIProtector uses the current profile schema for a validated PII keyring.
func NewDefaultPIIProtector(keyring *security.AESKeyring[security.PIIKeyPurpose]) (*PIIProtector, error) {
	return NewPIIProtector(keyring, ProfileSchemaVersion)
}

// SchemaVersion returns the AAD schema version used by this protector.
func (protector *PIIProtector) SchemaVersion() uint32 {
	if protector == nil {
		return 0
	}
	return protector.schemaVersion
}

// ActiveKeyVersion returns the version used for newly encrypted records.
func (protector *PIIProtector) ActiveKeyVersion() uint32 {
	if protector == nil || protector.keyring == nil {
		return 0
	}
	return protector.keyring.ActiveVersion()
}

// Encrypt authenticates a normalized field value under user- and field-specific AAD.
func (protector *PIIProtector) Encrypt(userID uuid.UUID, field Field, plaintext []byte) (EncryptedValue, error) {
	if protector == nil || protector.keyring == nil {
		return EncryptedValue{}, ErrInvalidProfileInput
	}
	normalized, err := normalizePIIValue(plaintext)
	if err != nil {
		return EncryptedValue{}, err
	}
	defer clearBytes(normalized)
	aad, err := PIIAssociatedData(userID, field, protector.schemaVersion)
	if err != nil {
		return EncryptedValue{}, err
	}
	payload, err := protector.keyring.Encrypt(normalized, aad)
	if err != nil {
		return EncryptedValue{}, mapPIIKeyringError(err)
	}
	return RestoreEncryptedValue(EncryptedValue{KeyVersion: payload.KeyVersion, Nonce: payload.Nonce, Ciphertext: payload.Ciphertext})
}

// Decrypt authenticates the payload and returns a newly allocated normalized plaintext buffer.
func (protector *PIIProtector) Decrypt(userID uuid.UUID, field Field, payload EncryptedValue) ([]byte, error) {
	if protector == nil || protector.keyring == nil {
		return nil, ErrInvalidProfileInput
	}
	validated, err := RestoreEncryptedValue(payload)
	if err != nil {
		return nil, ErrPIIAuthentication
	}
	aad, err := PIIAssociatedData(userID, field, protector.schemaVersion)
	if err != nil {
		return nil, err
	}
	plaintext, err := protector.keyring.Decrypt(security.Encrypted[security.PIIKeyPurpose]{
		KeyVersion: validated.KeyVersion, Nonce: validated.Nonce, Ciphertext: validated.Ciphertext,
	}, aad)
	if err != nil {
		return nil, mapPIIKeyringError(err)
	}
	return plaintext, nil
}

// EncryptRealName applies the real-name field marker without allowing callers to choose another field.
func (protector *PIIProtector) EncryptRealName(userID uuid.UUID, realName string) (EncryptedValue, error) {
	return protector.Encrypt(userID, FieldRealName, []byte(realName))
}

// DecryptRealName authenticates the real-name field marker and converts the result to a string.
func (protector *PIIProtector) DecryptRealName(userID uuid.UUID, payload EncryptedValue) (string, error) {
	plaintext, err := protector.Decrypt(userID, FieldRealName, payload)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// RequiresRotation identifies ciphertext that should be rewritten under the active key version.
func (protector *PIIProtector) RequiresRotation(payload EncryptedValue) bool {
	return protector != nil && payload.KeyVersion != 0 && payload.KeyVersion != protector.ActiveKeyVersion()
}

// Reencrypt decrypts with the stored version and seals a fresh payload with the active version.
func (protector *PIIProtector) Reencrypt(userID uuid.UUID, field Field, payload EncryptedValue) (EncryptedValue, error) {
	plaintext, err := protector.Decrypt(userID, field, payload)
	if err != nil {
		return EncryptedValue{}, err
	}
	defer clearBytes(plaintext)
	return protector.Encrypt(userID, field, plaintext)
}

func normalizePIIValue(input []byte) ([]byte, error) {
	if len(input) == 0 || len(input) > MaximumRealNameInputBytes || !utf8.Valid(input) {
		return nil, ErrInvalidProfileInput
	}
	value := norm.NFKC.String(string(input))
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > MaximumRealNameCodePoints {
		return nil, ErrInvalidProfileInput
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return nil, ErrInvalidProfileInput
		}
	}
	return []byte(value), nil
}

func mapPIIKeyringError(err error) error {
	if errors.Is(err, security.ErrUnknownKeyVersion) || errors.Is(err, security.ErrInvalidKeyring) {
		return ErrPIIKeyUnavailable
	}
	if errors.Is(err, security.ErrAuthenticationFailed) {
		return ErrPIIAuthentication
	}
	return ErrPIIAuthentication
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

// PIIValueDigest returns a stable digest for audit details without exposing plaintext.
func PIIValueDigest(value []byte) [sha256.Size]byte { return sha256.Sum256(value) }
