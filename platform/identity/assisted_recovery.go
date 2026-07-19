package identity

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/security"
)

const (
	// AssistedRecoveryCodeVersion lets the shared recovery_code field distinguish administrator-issued grants.
	AssistedRecoveryCodeVersion = "ar1"
	// AssistedRecoverySelectorBytes provides the required 128-bit lookup selector.
	AssistedRecoverySelectorBytes = 16
	// AssistedRecoverySecretBytes provides the required 256-bit delivered bearer secret.
	AssistedRecoverySecretBytes = 32
	// AssistedRecoveryTTL is the fixed administrator-delivery window.
	AssistedRecoveryTTL = 15 * time.Minute
	// AssistedRecoveryMaxAttempts closes an online grant after repeated secret failures.
	AssistedRecoveryMaxAttempts uint32 = 5
	// AssistedRecoveryPurpose is persisted and verified across Begin and Complete.
	AssistedRecoveryPurpose = "identity.assisted_recovery"
)

const assistedRecoverySecretDomain = "game-night:assisted-recovery:v1\x00"

// AssistedRecoveryGrantStatus is the administrator-issued one-time recovery lifecycle.
type AssistedRecoveryGrantStatus string

const (
	AssistedRecoveryGrantActive   AssistedRecoveryGrantStatus = "active"
	AssistedRecoveryGrantConsumed AssistedRecoveryGrantStatus = "consumed"
	AssistedRecoveryGrantRevoked  AssistedRecoveryGrantStatus = "revoked"
	AssistedRecoveryGrantExpired  AssistedRecoveryGrantStatus = "expired"
)

// AssistedRecoveryGrantSnapshot is the persistence-neutral administrator grant row.
type AssistedRecoveryGrantSnapshot struct {
	ID               uuid.UUID
	UserID           uuid.UUID
	Selector         identifier.Selector
	SecretHash       string
	Purpose          string
	Status           AssistedRecoveryGrantStatus
	AttemptCount     uint32
	MaxAttempts      uint32
	CreatedByAdminID uuid.UUID
	CreatedAt        time.Time
	ExpiresAt        time.Time
	ConsumedAt       time.Time
	RevokedAt        time.Time
	ResultID         uuid.UUID
}

// AssistedRecoveryGrant is immutable so identity and admin services share one state machine.
type AssistedRecoveryGrant struct{ snapshot AssistedRecoveryGrantSnapshot }

// IssuedAssistedRecoveryGrant separates the hash-only aggregate from the one-time administrator-delivered code.
type IssuedAssistedRecoveryGrant struct {
	Grant AssistedRecoveryGrant
	Code  string
}

// IssueAssisted creates a 15-minute grant with independent selector and 256-bit secret entropy.
func (service *RecoveryCodeService) IssueAssisted(
	ctx context.Context,
	userID, adminID uuid.UUID,
	at time.Time,
) (IssuedAssistedRecoveryGrant, error) {
	if service == nil || service.hasher == nil || ctx == nil || userID == uuid.Nil || adminID == uuid.Nil {
		return IssuedAssistedRecoveryGrant{}, ErrInvalidAssistedRecoveryGrant
	}
	selectorEntropy, err := security.RandomBytes(AssistedRecoverySelectorBytes)
	if err != nil {
		return IssuedAssistedRecoveryGrant{}, err
	}
	defer clear(selectorEntropy)
	selector, err := identifier.NewSelector(selectorEntropy)
	if err != nil {
		return IssuedAssistedRecoveryGrant{}, ErrInvalidAssistedRecoveryGrant
	}
	secret, err := security.RandomBytes(AssistedRecoverySecretBytes)
	if err != nil {
		return IssuedAssistedRecoveryGrant{}, err
	}
	defer clear(secret)
	hashInput := assistedRecoverySecretHashInput(selector, secret)
	secretHash, err := service.hasher.Hash(ctx, hashInput)
	clear(hashInput)
	if err != nil {
		return IssuedAssistedRecoveryGrant{}, err
	}
	grantID, err := uuid.NewV7()
	if err != nil {
		return IssuedAssistedRecoveryGrant{}, ErrInvalidAssistedRecoveryGrant
	}
	grant, err := RestoreAssistedRecoveryGrant(AssistedRecoveryGrantSnapshot{
		ID: grantID, UserID: userID, Selector: selector, SecretHash: secretHash,
		Purpose: AssistedRecoveryPurpose, Status: AssistedRecoveryGrantActive,
		MaxAttempts: AssistedRecoveryMaxAttempts, CreatedByAdminID: adminID,
		CreatedAt: at, ExpiresAt: at.Add(AssistedRecoveryTTL),
	})
	if err != nil {
		return IssuedAssistedRecoveryGrant{}, err
	}
	code, err := security.FormatToken(AssistedRecoveryCodeVersion, selector.Value(), secret)
	if err != nil {
		return IssuedAssistedRecoveryGrant{}, ErrInvalidAssistedRecoveryGrant
	}
	return IssuedAssistedRecoveryGrant{Grant: grant, Code: code}, nil
}

// AssistedSelectorFromCode parses only the public lookup component and clears the submitted secret.
func (service *RecoveryCodeService) AssistedSelectorFromCode(encoded string) (identifier.Selector, error) {
	parsed, err := parseAssistedRecoveryCode(encoded)
	if err != nil {
		return identifier.Selector{}, ErrRecoveryInvalid
	}
	clear(parsed.secret)
	return parsed.selector, nil
}

// RestoreAssistedRecoveryGrant validates TTL, attempts, source ownership, and terminal state.
func RestoreAssistedRecoveryGrant(snapshot AssistedRecoveryGrantSnapshot) (AssistedRecoveryGrant, error) {
	snapshot.CreatedAt = canonicalUserTime(snapshot.CreatedAt)
	snapshot.ExpiresAt = canonicalUserTime(snapshot.ExpiresAt)
	snapshot.ConsumedAt = canonicalUserOptionalTime(snapshot.ConsumedAt)
	snapshot.RevokedAt = canonicalUserOptionalTime(snapshot.RevokedAt)
	if snapshot.ID == uuid.Nil || snapshot.UserID == uuid.Nil || snapshot.CreatedByAdminID == uuid.Nil ||
		snapshot.Selector.ByteLength() != AssistedRecoverySelectorBytes || security.ValidateArgon2Hash(snapshot.SecretHash) != nil ||
		snapshot.Purpose != AssistedRecoveryPurpose || snapshot.MaxAttempts == 0 || snapshot.MaxAttempts > AssistedRecoveryMaxAttempts ||
		snapshot.AttemptCount > snapshot.MaxAttempts || snapshot.CreatedAt.IsZero() ||
		!snapshot.ExpiresAt.Equal(snapshot.CreatedAt.Add(AssistedRecoveryTTL)) {
		return AssistedRecoveryGrant{}, ErrInvalidAssistedRecoveryGrant
	}
	switch snapshot.Status {
	case AssistedRecoveryGrantActive:
		if snapshot.AttemptCount >= snapshot.MaxAttempts || !snapshot.ConsumedAt.IsZero() || !snapshot.RevokedAt.IsZero() || snapshot.ResultID != uuid.Nil {
			return AssistedRecoveryGrant{}, ErrInvalidAssistedRecoveryGrant
		}
	case AssistedRecoveryGrantConsumed:
		if snapshot.ResultID == uuid.Nil || snapshot.ConsumedAt.Before(snapshot.CreatedAt) || !snapshot.ConsumedAt.Before(snapshot.ExpiresAt) ||
			!snapshot.RevokedAt.IsZero() || snapshot.AttemptCount >= snapshot.MaxAttempts {
			return AssistedRecoveryGrant{}, ErrInvalidAssistedRecoveryGrant
		}
	case AssistedRecoveryGrantRevoked:
		if snapshot.RevokedAt.Before(snapshot.CreatedAt) || !snapshot.ConsumedAt.IsZero() || snapshot.ResultID != uuid.Nil {
			return AssistedRecoveryGrant{}, ErrInvalidAssistedRecoveryGrant
		}
	case AssistedRecoveryGrantExpired:
		if !snapshot.ConsumedAt.IsZero() || !snapshot.RevokedAt.IsZero() || snapshot.ResultID != uuid.Nil {
			return AssistedRecoveryGrant{}, ErrInvalidAssistedRecoveryGrant
		}
	default:
		return AssistedRecoveryGrant{}, ErrInvalidAssistedRecoveryGrant
	}
	return AssistedRecoveryGrant{snapshot: snapshot}, nil
}

// Snapshot returns the validated persistence representation.
func (grant AssistedRecoveryGrant) Snapshot() AssistedRecoveryGrantSnapshot { return grant.snapshot }

// State derives half-open expiry and attempt exhaustion without mutating a row during authentication.
func (grant AssistedRecoveryGrant) State(at time.Time) AssistedRecoveryGrantStatus {
	at = canonicalUserTime(at)
	snapshot := grant.snapshot
	if snapshot.Status == AssistedRecoveryGrantActive &&
		(!at.Before(snapshot.ExpiresAt) || snapshot.AttemptCount >= snapshot.MaxAttempts) {
		return AssistedRecoveryGrantExpired
	}
	return snapshot.Status
}

// RecordFailure increments the online guess counter and records exhaustion as a terminal state.
func (grant AssistedRecoveryGrant) RecordFailure(at time.Time) (AssistedRecoveryGrant, error) {
	at = canonicalUserTime(at)
	snapshot := grant.Snapshot()
	if snapshot.Status != AssistedRecoveryGrantActive || at.Before(snapshot.CreatedAt) || !at.Before(snapshot.ExpiresAt) ||
		snapshot.AttemptCount >= snapshot.MaxAttempts {
		return AssistedRecoveryGrant{}, ErrRecoveryConcurrentTransition
	}
	snapshot.AttemptCount++
	if snapshot.AttemptCount == snapshot.MaxAttempts {
		snapshot.Status = AssistedRecoveryGrantExpired
	}
	return RestoreAssistedRecoveryGrant(snapshot)
}

// Consume binds the one winning recovery transaction to its durable result envelope.
func (grant AssistedRecoveryGrant) Consume(resultID uuid.UUID, at time.Time) (AssistedRecoveryGrant, error) {
	at = canonicalUserTime(at)
	snapshot := grant.Snapshot()
	if resultID == uuid.Nil || snapshot.Status != AssistedRecoveryGrantActive || snapshot.AttemptCount >= snapshot.MaxAttempts ||
		at.Before(snapshot.CreatedAt) || !at.Before(snapshot.ExpiresAt) {
		return AssistedRecoveryGrant{}, ErrRecoveryConcurrentTransition
	}
	snapshot.Status = AssistedRecoveryGrantConsumed
	snapshot.ConsumedAt = at
	snapshot.ResultID = resultID
	return RestoreAssistedRecoveryGrant(snapshot)
}

// Revoke closes a still-active grant when another recovery path wins or account authority changes.
func (grant AssistedRecoveryGrant) Revoke(at time.Time) (AssistedRecoveryGrant, error) {
	at = canonicalUserTime(at)
	snapshot := grant.Snapshot()
	if snapshot.Status != AssistedRecoveryGrantActive || at.Before(snapshot.CreatedAt) {
		return AssistedRecoveryGrant{}, ErrRecoveryConcurrentTransition
	}
	snapshot.Status = AssistedRecoveryGrantRevoked
	snapshot.RevokedAt = at
	return RestoreAssistedRecoveryGrant(snapshot)
}

// VerifyAssistedOrDummy performs one Argon2 job for both known and unknown selectors.
func (service *RecoveryCodeService) VerifyAssistedOrDummy(
	ctx context.Context,
	grant *AssistedRecoveryGrant,
	encoded string,
	at time.Time,
) error {
	if service == nil || service.hasher == nil || ctx == nil {
		return ErrRecoveryInvalid
	}
	parsed, parseErr := parseAssistedRecoveryCode(encoded)
	if parseErr != nil {
		parsed = dummyAssistedRecoveryCode()
	}
	defer clear(parsed.secret)
	input := assistedRecoverySecretHashInput(parsed.selector, parsed.secret)
	defer clear(input)
	storedHash := ""
	grantValid := false
	if grant != nil {
		snapshot := grant.Snapshot()
		grantValid = snapshot.Selector == parsed.selector
		if grantValid {
			storedHash = snapshot.SecretHash
		}
	}
	matched, _, err := service.hasher.VerifyOrDummy(ctx, storedHash, input)
	if err != nil {
		return err
	}
	if parseErr != nil || !grantValid || !matched || grant.State(at) != AssistedRecoveryGrantActive {
		return ErrRecoveryInvalid
	}
	return nil
}

type parsedAssistedRecoveryCode struct {
	selector identifier.Selector
	secret   []byte
}

func parseAssistedRecoveryCode(encoded string) (parsedAssistedRecoveryCode, error) {
	parsed, err := security.ParseToken(encoded, security.TokenPolicy{
		Version:        AssistedRecoveryCodeVersion,
		MinSecretBytes: AssistedRecoverySecretBytes,
		MaxSecretBytes: AssistedRecoverySecretBytes,
	})
	if err != nil {
		return parsedAssistedRecoveryCode{}, ErrRecoveryInvalid
	}
	selector, err := identifier.ParseSelector(parsed.Selector)
	if err != nil || selector.ByteLength() != AssistedRecoverySelectorBytes {
		clear(parsed.Secret)
		return parsedAssistedRecoveryCode{}, ErrRecoveryInvalid
	}
	return parsedAssistedRecoveryCode{selector: selector, secret: parsed.Secret}, nil
}

func dummyAssistedRecoveryCode() parsedAssistedRecoveryCode {
	selector, err := identifier.NewSelector(make([]byte, AssistedRecoverySelectorBytes))
	if err != nil {
		panic("fixed assisted recovery dummy selector must be valid")
	}
	return parsedAssistedRecoveryCode{selector: selector, secret: make([]byte, AssistedRecoverySecretBytes)}
}

func assistedRecoverySecretHashInput(selector identifier.Selector, secret []byte) []byte {
	value := make([]byte, 0, len(assistedRecoverySecretDomain)+len(selector.Value())+1+len(secret))
	value = append(value, assistedRecoverySecretDomain...)
	value = append(value, selector.Value()...)
	value = append(value, 0)
	return append(value, secret...)
}

func assistedRecoveryTokenFamily(encoded string) bool {
	return strings.HasPrefix(encoded, AssistedRecoveryCodeVersion+".")
}
