package identity

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/security"
)

const (
	// RecoveryCodeVersion is the stable selector/secret wire format.
	RecoveryCodeVersion = "v1"
	// RecoverySelectorBytes provides the required 128-bit public lookup selector.
	RecoverySelectorBytes = 16
	// RecoverySecretBytes provides the required 128-bit offline recovery secret.
	RecoverySecretBytes = 16
)

// recoverySecretDomain binds the secret hash to its selector and prevents cross-protocol reuse.
const recoverySecretDomain = "game-night:recovery-code:v1\x00"

// RecoveryCredentialStatus is the closed long-lived recovery-code state machine.
type RecoveryCredentialStatus string

const (
	RecoveryCredentialActive   RecoveryCredentialStatus = "active"
	RecoveryCredentialConsumed RecoveryCredentialStatus = "consumed"
	RecoveryCredentialRevoked  RecoveryCredentialStatus = "revoked"
)

// RecoveryCredentialSnapshot is the persistence-neutral recovery row.
type RecoveryCredentialSnapshot struct {
	ID           uuid.UUID
	UserID       uuid.UUID
	Selector     identifier.Selector
	SecretHash   string
	Version      uint64
	Status       RecoveryCredentialStatus
	CreatedAt    time.Time
	ConsumedAt   time.Time
	RevokedAt    time.Time
	RevokeReason string
}

// RecoveryCredential is immutable so PostgreSQL adapters cannot bypass lifecycle invariants.
type RecoveryCredential struct {
	snapshot RecoveryCredentialSnapshot
}

// IssuedRecoveryCredential carries the hash-only aggregate and one-time plaintext code.
type IssuedRecoveryCredential struct {
	Credential RecoveryCredential
	Code       string
}

// RecoverySecretHasher is implemented by the bounded Argon2 worker service.
type RecoverySecretHasher interface {
	Hash(context.Context, []byte) (string, error)
}

// RecoveryCodeService issues initial codes; verification and consumption are extended in Task 10.
type RecoveryCodeService struct {
	hasher RecoverySecretHasher
}

// NewRecoveryCodeService requires the bounded process-wide password hashing service.
func NewRecoveryCodeService(hasher RecoverySecretHasher) (*RecoveryCodeService, error) {
	if hasher == nil {
		return nil, ErrInvalidRecoveryCredential
	}
	return &RecoveryCodeService{hasher: hasher}, nil
}

// Issue creates independent selector and secret entropy and persists only an Argon2 hash.
func (service *RecoveryCodeService) Issue(ctx context.Context, userID uuid.UUID, at time.Time) (IssuedRecoveryCredential, error) {
	if service == nil || service.hasher == nil || ctx == nil || userID == uuid.Nil {
		return IssuedRecoveryCredential{}, ErrInvalidRecoveryCredential
	}
	selectorEntropy, err := security.RandomBytes(RecoverySelectorBytes)
	if err != nil {
		return IssuedRecoveryCredential{}, err
	}
	defer clear(selectorEntropy)
	selector, err := identifier.NewSelector(selectorEntropy)
	if err != nil {
		return IssuedRecoveryCredential{}, ErrInvalidRecoveryCredential
	}
	secret, err := security.RandomBytes(RecoverySecretBytes)
	if err != nil {
		return IssuedRecoveryCredential{}, err
	}
	defer clear(secret)
	value := recoverySecretHashInput(selector, secret)
	secretHash, err := service.hasher.Hash(ctx, value)
	clear(value)
	if err != nil {
		return IssuedRecoveryCredential{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return IssuedRecoveryCredential{}, ErrInvalidRecoveryCredential
	}
	record, err := RestoreRecoveryCredential(RecoveryCredentialSnapshot{
		ID: id, UserID: userID, Selector: selector, SecretHash: secretHash,
		Version: 1, Status: RecoveryCredentialActive, CreatedAt: at,
	})
	if err != nil {
		return IssuedRecoveryCredential{}, err
	}
	code, err := security.FormatToken(RecoveryCodeVersion, selector.Value(), secret)
	if err != nil {
		return IssuedRecoveryCredential{}, ErrInvalidRecoveryCredential
	}
	return IssuedRecoveryCredential{Credential: record, Code: code}, nil
}

// RestoreRecoveryCredential validates a database row without parsing or exposing its Argon2 PHC content.
func RestoreRecoveryCredential(snapshot RecoveryCredentialSnapshot) (RecoveryCredential, error) {
	snapshot.CreatedAt = canonicalUserTime(snapshot.CreatedAt)
	snapshot.ConsumedAt = canonicalUserOptionalTime(snapshot.ConsumedAt)
	snapshot.RevokedAt = canonicalUserOptionalTime(snapshot.RevokedAt)
	if snapshot.ID == uuid.Nil || snapshot.UserID == uuid.Nil || snapshot.Selector.ByteLength() != RecoverySelectorBytes ||
		security.ValidateArgon2Hash(snapshot.SecretHash) != nil || snapshot.Version == 0 || snapshot.Version > math.MaxInt64 ||
		snapshot.CreatedAt.IsZero() {
		return RecoveryCredential{}, ErrInvalidRecoveryCredential
	}
	switch snapshot.Status {
	case RecoveryCredentialActive:
		if !snapshot.ConsumedAt.IsZero() || !snapshot.RevokedAt.IsZero() || snapshot.RevokeReason != "" {
			return RecoveryCredential{}, ErrInvalidRecoveryCredential
		}
	case RecoveryCredentialConsumed:
		if snapshot.ConsumedAt.Before(snapshot.CreatedAt) || !snapshot.RevokedAt.IsZero() || snapshot.RevokeReason != "" {
			return RecoveryCredential{}, ErrInvalidRecoveryCredential
		}
	case RecoveryCredentialRevoked:
		if snapshot.RevokedAt.Before(snapshot.CreatedAt) || !snapshot.ConsumedAt.IsZero() ||
			strings.TrimSpace(snapshot.RevokeReason) == "" || strings.TrimSpace(snapshot.RevokeReason) != snapshot.RevokeReason ||
			len(snapshot.RevokeReason) > 64 {
			return RecoveryCredential{}, ErrInvalidRecoveryCredential
		}
	default:
		return RecoveryCredential{}, ErrInvalidRecoveryCredential
	}
	return RecoveryCredential{snapshot: snapshot}, nil
}

// Snapshot returns the validated persistence representation.
func (credential RecoveryCredential) Snapshot() RecoveryCredentialSnapshot {
	return credential.snapshot
}

func recoverySecretHashInput(selector identifier.Selector, secret []byte) []byte {
	value := make([]byte, 0, len(recoverySecretDomain)+len(selector.Value())+1+len(secret))
	value = append(value, recoverySecretDomain...)
	value = append(value, selector.Value()...)
	value = append(value, 0)
	return append(value, secret...)
}
