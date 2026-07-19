package profile

import (
	"bytes"
	"math"
	"time"

	"github.com/google/uuid"
)

const (
	// ProfileSchemaVersion identifies the associated-data and export record schema.
	ProfileSchemaVersion uint32 = 1
	// ProfileAESNonceSize is the nonce size produced by crypto/aes GCM.
	ProfileAESNonceSize = 12
	// ProfileAESOverhead is the authentication tag size produced by crypto/aes GCM.
	ProfileAESOverhead = 16
)

// Field is the closed set of encrypted profile fields exposed by the current API.
type Field string

const (
	FieldRealName Field = "real_name"
)

// Valid prevents a caller from reusing a valid AAD for an unprovisioned field.
func (field Field) Valid() bool { return field == FieldRealName }

// EncryptedValue is a persistence-neutral AES-GCM payload with its rotation version.
type EncryptedValue struct {
	KeyVersion uint32
	Nonce      []byte
	Ciphertext []byte
}

// RestoreEncryptedValue validates a payload before it can be persisted or decrypted.
func RestoreEncryptedValue(value EncryptedValue) (EncryptedValue, error) {
	value.Nonce = bytes.Clone(value.Nonce)
	value.Ciphertext = bytes.Clone(value.Ciphertext)
	if value.KeyVersion == 0 || value.KeyVersion > math.MaxInt32 ||
		len(value.Nonce) != ProfileAESNonceSize || len(value.Ciphertext) < ProfileAESOverhead {
		return EncryptedValue{}, ErrInvalidProfileInput
	}
	return value, nil
}

// Clone returns independent ciphertext buffers for adapters and callers.
func (value EncryptedValue) Clone() EncryptedValue {
	value.Nonce = bytes.Clone(value.Nonce)
	value.Ciphertext = bytes.Clone(value.Ciphertext)
	return value
}

// Snapshot returns an independent copy for persistence adapters.
func (value EncryptedValue) Snapshot() EncryptedValue { return value.Clone() }

// UserProfileSnapshot is the persistence-neutral encrypted real-name record.
type UserProfileSnapshot struct {
	UserID             uuid.UUID
	RealNameCiphertext []byte
	RealNameNonce      []byte
	RealNameKeyVersion uint32
	ProfileVersion     uint64
	RealNameUpdatedAt  time.Time
	RealNameUpdatedBy  uuid.UUID
}

// UserProfile is an immutable aggregate; plaintext never appears in its snapshot.
type UserProfile struct{ snapshot UserProfileSnapshot }

// NewUserProfile creates the first version of a user's encrypted profile.
func NewUserProfile(userID uuid.UUID, value EncryptedValue, updatedAt time.Time, updatedBy uuid.UUID) (UserProfile, error) {
	value, err := RestoreEncryptedValue(value)
	if err != nil {
		return UserProfile{}, err
	}
	return RestoreUserProfile(UserProfileSnapshot{
		UserID: userID, RealNameCiphertext: value.Ciphertext, RealNameNonce: value.Nonce,
		RealNameKeyVersion: value.KeyVersion, ProfileVersion: 1,
		RealNameUpdatedAt: updatedAt, RealNameUpdatedBy: updatedBy,
	})
}

// RestoreUserProfile validates persisted state before authorization or decryption.
func RestoreUserProfile(snapshot UserProfileSnapshot) (UserProfile, error) {
	snapshot.RealNameCiphertext = bytes.Clone(snapshot.RealNameCiphertext)
	snapshot.RealNameNonce = bytes.Clone(snapshot.RealNameNonce)
	snapshot.RealNameUpdatedAt = canonicalProfileTime(snapshot.RealNameUpdatedAt)
	if snapshot.UserID == uuid.Nil || snapshot.RealNameUpdatedBy == uuid.Nil || snapshot.ProfileVersion == 0 ||
		snapshot.RealNameKeyVersion == 0 || snapshot.RealNameKeyVersion > math.MaxInt32 ||
		len(snapshot.RealNameNonce) != ProfileAESNonceSize || len(snapshot.RealNameCiphertext) < ProfileAESOverhead ||
		snapshot.RealNameUpdatedAt.IsZero() {
		return UserProfile{}, ErrInvalidProfileInput
	}
	return UserProfile{snapshot: snapshot}, nil
}

// Snapshot returns a deep copy that is safe for repository adapters to retain.
func (profile UserProfile) Snapshot() UserProfileSnapshot {
	snapshot := profile.snapshot
	snapshot.RealNameCiphertext = bytes.Clone(snapshot.RealNameCiphertext)
	snapshot.RealNameNonce = bytes.Clone(snapshot.RealNameNonce)
	return snapshot
}

// EncryptedRealName returns the current ciphertext without exposing mutable storage.
func (profile UserProfile) EncryptedRealName() EncryptedValue {
	snapshot := profile.snapshot
	return EncryptedValue{KeyVersion: snapshot.RealNameKeyVersion, Nonce: bytes.Clone(snapshot.RealNameNonce), Ciphertext: bytes.Clone(snapshot.RealNameCiphertext)}
}

// UpdateEncrypted replaces the ciphertext using the exact current version as a CAS precondition.
func (profile UserProfile) UpdateEncrypted(currentVersion uint64, value EncryptedValue, updatedAt time.Time, updatedBy uuid.UUID) (UserProfile, error) {
	if currentVersion != profile.snapshot.ProfileVersion {
		return UserProfile{}, ErrProfileConcurrentTransition
	}
	value, err := RestoreEncryptedValue(value)
	if err != nil || updatedBy == uuid.Nil {
		return UserProfile{}, ErrInvalidProfileInput
	}
	updatedAt = canonicalProfileTime(updatedAt)
	if updatedAt.IsZero() || updatedAt.Before(profile.snapshot.RealNameUpdatedAt) || profile.snapshot.ProfileVersion == math.MaxUint64 {
		return UserProfile{}, ErrProfileConcurrentTransition
	}
	next := profile.snapshot
	next.RealNameCiphertext = value.Ciphertext
	next.RealNameNonce = value.Nonce
	next.RealNameKeyVersion = value.KeyVersion
	next.ProfileVersion++
	next.RealNameUpdatedAt = updatedAt
	next.RealNameUpdatedBy = updatedBy
	return RestoreUserProfile(next)
}

// ProfileVersion returns the monotonic CAS version of this profile.
func (profile UserProfile) ProfileVersion() uint64 { return profile.snapshot.ProfileVersion }

// UserID returns the stable owner identifier; it never contains profile data.
func (profile UserProfile) UserID() uuid.UUID { return profile.snapshot.UserID }

// RealNameUpdatedAt returns the canonical timestamp of the last encrypted write.
func (profile UserProfile) RealNameUpdatedAt() time.Time { return profile.snapshot.RealNameUpdatedAt }

// RealNameUpdatedBy returns the administrator identifier that performed the last write.
func (profile UserProfile) RealNameUpdatedBy() uuid.UUID { return profile.snapshot.RealNameUpdatedBy }

func canonicalProfileTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC().Truncate(time.Microsecond)
}
