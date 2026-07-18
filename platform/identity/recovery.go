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

const (
	// RecoveryRevokeUserRequested is retained for compatibility with existing persisted user rotations.
	RecoveryRevokeUserRequested = "user_requested"
	// RecoveryRevokeRotated records user-initiated replacement without exposing free-form reasons in credential state.
	RecoveryRevokeRotated = "rotated"
	// RecoveryRevokeAccountSuspended invalidates ordinary recovery while an account is suspended.
	RecoveryRevokeAccountSuspended = "account_suspended"
	// RecoveryRevokeAccountDeleted invalidates all recovery after account deletion.
	RecoveryRevokeAccountDeleted = "account_deleted"
	// RecoveryRevokeAssisted replaces ordinary recovery with an administrator-delivered one-time path.
	RecoveryRevokeAssisted = "assisted_recovery"
)

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
	VerifyOrDummy(context.Context, string, []byte) (matched, needsUpgrade bool, err error)
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

// SelectorFromCode parses only the public lookup component and never retains the submitted secret.
func (service *RecoveryCodeService) SelectorFromCode(encoded string) (identifier.Selector, error) {
	parsed, err := parseRecoveryCode(encoded)
	if err != nil {
		return identifier.Selector{}, ErrRecoveryInvalid
	}
	clear(parsed.secret)
	return parsed.selector, nil
}

// VerifyOrDummy performs exactly one bounded Argon2 verification even for malformed or unknown selectors.
func (service *RecoveryCodeService) VerifyOrDummy(
	ctx context.Context,
	credential *RecoveryCredential,
	encoded string,
) error {
	if service == nil || service.hasher == nil || ctx == nil {
		return ErrRecoveryInvalid
	}
	parsed, parseErr := parseRecoveryCode(encoded)
	if parseErr != nil {
		parsed = dummyRecoveryCode()
	}
	defer clear(parsed.secret)
	input := recoverySecretHashInput(parsed.selector, parsed.secret)
	defer clear(input)

	storedHash := ""
	credentialValid := false
	if credential != nil {
		snapshot := credential.Snapshot()
		credentialValid = snapshot.Selector == parsed.selector && snapshot.Status == RecoveryCredentialActive
		if credentialValid {
			storedHash = snapshot.SecretHash
		}
	}
	matched, _, verifyErr := service.hasher.VerifyOrDummy(ctx, storedHash, input)
	if verifyErr != nil {
		return verifyErr
	}
	if parseErr != nil || !credentialValid || !matched {
		return ErrRecoveryInvalid
	}
	return nil
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
			len(snapshot.RevokeReason) > 64 || !validRecoveryRevokeReason(snapshot.RevokeReason) {
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

// Consume permanently retires one exact active code after CompleteRecovery has won its transaction race.
func (credential RecoveryCredential) Consume(at time.Time) (RecoveryCredential, error) {
	at = canonicalUserTime(at)
	snapshot := credential.Snapshot()
	if snapshot.Status != RecoveryCredentialActive || at.Before(snapshot.CreatedAt) {
		return RecoveryCredential{}, ErrRecoveryConcurrentTransition
	}
	snapshot.Status = RecoveryCredentialConsumed
	snapshot.ConsumedAt = at
	return RestoreRecoveryCredential(snapshot)
}

// Revoke retires an active code for a closed security reason without pretending it was successfully used.
func (credential RecoveryCredential) Revoke(reason string, at time.Time) (RecoveryCredential, error) {
	at = canonicalUserTime(at)
	snapshot := credential.Snapshot()
	if snapshot.Status != RecoveryCredentialActive || at.Before(snapshot.CreatedAt) || !validRecoveryRevokeReason(reason) {
		return RecoveryCredential{}, ErrRecoveryConcurrentTransition
	}
	snapshot.Status = RecoveryCredentialRevoked
	snapshot.RevokedAt = at
	snapshot.RevokeReason = reason
	return RestoreRecoveryCredential(snapshot)
}

type parsedRecoveryCode struct {
	selector identifier.Selector
	secret   []byte
}

func parseRecoveryCode(encoded string) (parsedRecoveryCode, error) {
	parsed, err := security.ParseToken(encoded, security.TokenPolicy{
		Version: RecoveryCodeVersion, MinSecretBytes: RecoverySecretBytes, MaxSecretBytes: RecoverySecretBytes,
	})
	if err != nil {
		return parsedRecoveryCode{}, ErrRecoveryInvalid
	}
	selector, err := identifier.ParseSelector(parsed.Selector)
	if err != nil || selector.ByteLength() != RecoverySelectorBytes {
		clear(parsed.Secret)
		return parsedRecoveryCode{}, ErrRecoveryInvalid
	}
	return parsedRecoveryCode{selector: selector, secret: parsed.Secret}, nil
}

func dummyRecoveryCode() parsedRecoveryCode {
	selector, err := identifier.NewSelector(make([]byte, RecoverySelectorBytes))
	if err != nil {
		panic("fixed recovery dummy selector must be valid")
	}
	return parsedRecoveryCode{selector: selector, secret: make([]byte, RecoverySecretBytes)}
}

func validRecoveryRevokeReason(reason string) bool {
	switch reason {
	case RecoveryRevokeUserRequested, RecoveryRevokeRotated, RecoveryRevokeAccountSuspended,
		RecoveryRevokeAccountDeleted, RecoveryRevokeAssisted:
		return true
	default:
		return false
	}
}

func recoverySecretHashInput(selector identifier.Selector, secret []byte) []byte {
	value := make([]byte, 0, len(recoverySecretDomain)+len(selector.Value())+1+len(secret))
	value = append(value, recoverySecretDomain...)
	value = append(value, selector.Value()...)
	value = append(value, 0)
	return append(value, secret...)
}
