package identity

import (
	"bytes"
	"encoding/binary"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/secretaccess"
	"github.com/iFTY-R/game-night/platform/security"
)

const (
	// RecoveryGrantVersion distinguishes short-lived recovery authority from long-lived recovery codes.
	RecoveryGrantVersion = "rg1"
	// RecoveryGrantSelectorBytes provides 128 bits of indexed public entropy.
	RecoveryGrantSelectorBytes = 16
	// RecoveryGrantSecretBytes provides 256 bits of bearer entropy.
	RecoveryGrantSecretBytes = 32
	// RecoveryAttemptTTL bounds the Begin-to-Complete window.
	RecoveryAttemptTTL = 5 * time.Minute
	// RecoveryAttemptMaxAttempts closes a grant after repeated authentication failures.
	RecoveryAttemptMaxAttempts uint32 = 5
)

// recoveryGrantDomain prevents an attempt MAC from authenticating another token family.
const recoveryGrantDomain = "game-night:recovery-grant:v1\x00"

// RecoveryAttemptStatus is the persisted two-phase recovery lifecycle.
type RecoveryAttemptStatus string

const (
	RecoveryAttemptActive   RecoveryAttemptStatus = "active"
	RecoveryAttemptConsumed RecoveryAttemptStatus = "consumed"
	RecoveryAttemptExpired  RecoveryAttemptStatus = "expired"
	RecoveryAttemptRevoked  RecoveryAttemptStatus = "revoked"
)

// RecoveryAttemptBinding fixes the user, anonymous challenge, request semantics, and one source authority.
type RecoveryAttemptBinding struct {
	UserID      uuid.UUID
	ChallengeID uuid.UUID
	Origin      challenge.OriginDigest

	// RequestDigestSet is false during Begin and becomes immutable in the winning Complete transaction.
	RequestDigestSet bool
	RequestDigest    idempotency.Digest

	RecoveryCredentialID      uuid.UUID
	RecoveryCredentialVersion uint64
	AssistedGrantID           uuid.UUID
}

// RecoveryAttemptSnapshot is the persistence-neutral short-lived recovery grant row.
type RecoveryAttemptSnapshot struct {
	ID       uuid.UUID
	Selector identifier.Selector
	GrantMAC security.MAC[security.UserChallengeKeyPurpose]
	Binding  RecoveryAttemptBinding

	AttemptCount uint32
	MaxAttempts  uint32
	Status       RecoveryAttemptStatus
	CreatedAt    time.Time
	ExpiresAt    time.Time
	ConsumedAt   time.Time
	RevokedAt    time.Time
	ResultID     uuid.UUID
}

// RecoveryAttempt is immutable so only reviewed state transitions can reach persistence adapters.
type RecoveryAttempt struct{ snapshot RecoveryAttemptSnapshot }

// IssuedRecoveryAttempt combines the hash-only aggregate and one-time bearer grant returned by BeginRecovery.
type IssuedRecoveryAttempt struct {
	Attempt   RecoveryAttempt
	Grant     string
	ExpiresAt time.Time
}

// RecoveryAttemptAuthorization is an in-process capability bound to one exact restored attempt snapshot.
type RecoveryAttemptAuthorization struct {
	attemptID     uuid.UUID
	selector      identifier.Selector
	grantMAC      security.MAC[security.UserChallengeKeyPurpose]
	binding       RecoveryAttemptBinding
	status        RecoveryAttemptStatus
	resultID      uuid.UUID
	authorizedAt  time.Time
	attemptCount  uint32
	maxAttempts   uint32
	expiresAt     time.Time
	requestDigest idempotency.Digest
}

// RecoveryAttemptService owns grant entropy, HMAC authentication, and Origin/request binding.
type RecoveryAttemptService struct {
	keyring *security.HMACKeyring[security.UserChallengeKeyPurpose]
	clock   clock.Clock
}

// NewRecoveryAttemptService requires the user-challenge keyring because recovery grants share its anonymous boundary.
func NewRecoveryAttemptService(
	keyring *security.HMACKeyring[security.UserChallengeKeyPurpose],
	source clock.Clock,
) (*RecoveryAttemptService, error) {
	if keyring == nil || source == nil {
		return nil, ErrInvalidRecoveryAttempt
	}
	return &RecoveryAttemptService{keyring: keyring, clock: source}, nil
}

// Issue creates a five-minute grant without consuming its long-lived recovery source.
func (service *RecoveryAttemptService) Issue(binding RecoveryAttemptBinding) (IssuedRecoveryAttempt, error) {
	if service == nil || service.keyring == nil || service.clock == nil || validateRecoveryAttemptBinding(binding) != nil ||
		binding.RequestDigestSet || binding.RequestDigest != (idempotency.Digest{}) {
		return IssuedRecoveryAttempt{}, ErrInvalidRecoveryAttempt
	}
	id, err := uuid.NewV7()
	if err != nil {
		return IssuedRecoveryAttempt{}, ErrInvalidRecoveryAttempt
	}
	selectorEntropy, err := security.RandomBytes(RecoveryGrantSelectorBytes)
	if err != nil {
		return IssuedRecoveryAttempt{}, err
	}
	defer clear(selectorEntropy)
	selector, err := identifier.NewSelector(selectorEntropy)
	if err != nil {
		return IssuedRecoveryAttempt{}, ErrInvalidRecoveryAttempt
	}
	secret, err := security.RandomBytes(RecoveryGrantSecretBytes)
	if err != nil {
		return IssuedRecoveryAttempt{}, err
	}
	defer clear(secret)
	now := canonicalUserTime(service.clock.Now())
	expiresAt := now.Add(RecoveryAttemptTTL)
	claims := recoveryGrantClaims(id, selector, binding, expiresAt, secret)
	grantMAC, err := service.keyring.Sum(claims)
	clear(claims)
	if err != nil || grantMAC.KeyVersion == 0 || len(grantMAC.Value) != 32 {
		return IssuedRecoveryAttempt{}, ErrInvalidRecoveryAttempt
	}
	attempt, err := RestoreRecoveryAttempt(RecoveryAttemptSnapshot{
		ID: id, Selector: selector, GrantMAC: grantMAC, Binding: binding,
		MaxAttempts: RecoveryAttemptMaxAttempts, Status: RecoveryAttemptActive,
		CreatedAt: now, ExpiresAt: expiresAt,
	})
	if err != nil {
		return IssuedRecoveryAttempt{}, err
	}
	grant, err := security.FormatToken(RecoveryGrantVersion, selector.Value(), secret)
	if err != nil {
		return IssuedRecoveryAttempt{}, ErrInvalidRecoveryAttempt
	}
	return IssuedRecoveryAttempt{Attempt: attempt, Grant: grant, ExpiresAt: expiresAt}, nil
}

// SelectorFromGrant extracts the bounded public selector used for the pre-lock attempt lookup.
func (service *RecoveryAttemptService) SelectorFromGrant(encoded string) (identifier.Selector, error) {
	parsed, err := security.ParseToken(encoded, security.TokenPolicy{
		Version: RecoveryGrantVersion, MinSecretBytes: RecoveryGrantSecretBytes, MaxSecretBytes: RecoveryGrantSecretBytes,
	})
	if err != nil {
		return identifier.Selector{}, ErrRecoveryInvalid
	}
	defer clear(parsed.Secret)
	selector, err := identifier.ParseSelector(parsed.Selector)
	if err != nil || selector.ByteLength() != RecoveryGrantSelectorBytes {
		return identifier.Selector{}, ErrRecoveryInvalid
	}
	return selector, nil
}

// Authorize verifies the concrete grant before revealing digest conflicts or terminal replay authority.
func (service *RecoveryAttemptService) Authorize(
	attempt RecoveryAttempt,
	encoded string,
	origin challenge.OriginDigest,
	digest idempotency.Digest,
) (RecoveryAttemptAuthorization, error) {
	if service == nil || service.keyring == nil || service.clock == nil {
		return RecoveryAttemptAuthorization{}, ErrRecoveryInvalid
	}
	parsed, err := security.ParseToken(encoded, security.TokenPolicy{
		Version: RecoveryGrantVersion, MinSecretBytes: RecoveryGrantSecretBytes, MaxSecretBytes: RecoveryGrantSecretBytes,
	})
	if err != nil {
		return RecoveryAttemptAuthorization{}, ErrRecoveryInvalid
	}
	defer clear(parsed.Secret)
	selector, err := identifier.ParseSelector(parsed.Selector)
	snapshot := attempt.Snapshot()
	if err != nil || selector.ByteLength() != RecoveryGrantSelectorBytes || selector != snapshot.Selector {
		return RecoveryAttemptAuthorization{}, ErrRecoveryInvalid
	}
	claims := recoveryGrantClaims(snapshot.ID, snapshot.Selector, snapshot.Binding, snapshot.ExpiresAt, parsed.Secret)
	matched, verifyErr := service.keyring.Verify(claims, snapshot.GrantMAC)
	clear(claims)
	if verifyErr != nil || !matched {
		return RecoveryAttemptAuthorization{}, ErrRecoveryInvalid
	}
	now := canonicalUserTime(service.clock.Now())
	if snapshot.Binding.Origin != origin || (snapshot.Status != RecoveryAttemptActive && snapshot.Status != RecoveryAttemptConsumed) ||
		!now.Before(snapshot.ExpiresAt) || snapshot.AttemptCount >= snapshot.MaxAttempts {
		return RecoveryAttemptAuthorization{}, ErrRecoveryInvalid
	}
	if snapshot.Binding.RequestDigestSet && snapshot.Binding.RequestDigest != digest {
		return RecoveryAttemptAuthorization{}, idempotency.ErrConflict
	}
	return recoveryAttemptAuthorization(snapshot, now, digest), nil
}

// ResultGrant converts a consumed exact-attempt authorization into a signed envelope capability.
func (service *RecoveryAttemptService) ResultGrant(
	attempt RecoveryAttempt,
	authorization RecoveryAttemptAuthorization,
	resultID uuid.UUID,
	resultExpiresAt time.Time,
) (secretaccess.RecoveryGrant, error) {
	if service == nil || service.keyring == nil || resultID == uuid.Nil ||
		!authorization.AllowsResultReplay(attempt, resultID) {
		return secretaccess.RecoveryGrant{}, ErrRecoveryInvalid
	}
	snapshot := attempt.Snapshot()
	validUntil := canonicalUserTime(resultExpiresAt)
	if snapshot.ExpiresAt.Before(validUntil) {
		validUntil = snapshot.ExpiresAt
	}
	grant, err := secretaccess.MintRecoveryGrant(
		service.keyring, snapshot.ID, snapshot.Binding.UserID, resultID, validUntil,
	)
	if err != nil {
		return secretaccess.RecoveryGrant{}, ErrRecoveryInvalid
	}
	return grant, nil
}

// RestoreRecoveryAttempt rejects database rows that could weaken source, expiry, or terminal-state binding.
func RestoreRecoveryAttempt(snapshot RecoveryAttemptSnapshot) (RecoveryAttempt, error) {
	snapshot = cloneRecoveryAttemptSnapshot(snapshot)
	snapshot.CreatedAt = canonicalUserTime(snapshot.CreatedAt)
	snapshot.ExpiresAt = canonicalUserTime(snapshot.ExpiresAt)
	snapshot.ConsumedAt = canonicalUserOptionalTime(snapshot.ConsumedAt)
	snapshot.RevokedAt = canonicalUserOptionalTime(snapshot.RevokedAt)
	if snapshot.ID == uuid.Nil || snapshot.Selector.ByteLength() != RecoveryGrantSelectorBytes ||
		snapshot.GrantMAC.KeyVersion == 0 || snapshot.GrantMAC.KeyVersion > math.MaxInt32 || len(snapshot.GrantMAC.Value) != 32 ||
		validateRecoveryAttemptBinding(snapshot.Binding) != nil || snapshot.MaxAttempts == 0 ||
		snapshot.MaxAttempts > RecoveryAttemptMaxAttempts || snapshot.AttemptCount > snapshot.MaxAttempts ||
		snapshot.CreatedAt.IsZero() || !snapshot.ExpiresAt.Equal(snapshot.CreatedAt.Add(RecoveryAttemptTTL)) {
		return RecoveryAttempt{}, ErrInvalidRecoveryAttempt
	}
	switch snapshot.Status {
	case RecoveryAttemptActive:
		if snapshot.Binding.RequestDigestSet || snapshot.Binding.RequestDigest != (idempotency.Digest{}) ||
			snapshot.AttemptCount >= snapshot.MaxAttempts || !snapshot.ConsumedAt.IsZero() || !snapshot.RevokedAt.IsZero() || snapshot.ResultID != uuid.Nil {
			return RecoveryAttempt{}, ErrInvalidRecoveryAttempt
		}
	case RecoveryAttemptConsumed:
		if !snapshot.Binding.RequestDigestSet || snapshot.ResultID == uuid.Nil || snapshot.ConsumedAt.Before(snapshot.CreatedAt) || !snapshot.ConsumedAt.Before(snapshot.ExpiresAt) ||
			!snapshot.RevokedAt.IsZero() || snapshot.AttemptCount >= snapshot.MaxAttempts {
			return RecoveryAttempt{}, ErrInvalidRecoveryAttempt
		}
	case RecoveryAttemptExpired:
		// Expiry may be caused by TTL cleanup before the authentication-attempt ceiling is reached.
		if snapshot.Binding.RequestDigestSet || snapshot.Binding.RequestDigest != (idempotency.Digest{}) ||
			!snapshot.ConsumedAt.IsZero() || !snapshot.RevokedAt.IsZero() || snapshot.ResultID != uuid.Nil {
			return RecoveryAttempt{}, ErrInvalidRecoveryAttempt
		}
	case RecoveryAttemptRevoked:
		if snapshot.Binding.RequestDigestSet || snapshot.Binding.RequestDigest != (idempotency.Digest{}) ||
			snapshot.RevokedAt.Before(snapshot.CreatedAt) || !snapshot.ConsumedAt.IsZero() || snapshot.ResultID != uuid.Nil {
			return RecoveryAttempt{}, ErrInvalidRecoveryAttempt
		}
	default:
		return RecoveryAttempt{}, ErrInvalidRecoveryAttempt
	}
	return RecoveryAttempt{snapshot: snapshot}, nil
}

// Snapshot returns a deep copy suitable for persistence.
func (attempt RecoveryAttempt) Snapshot() RecoveryAttemptSnapshot {
	return cloneRecoveryAttemptSnapshot(attempt.snapshot)
}

// RecordFailure increments authentication failures and closes the attempt exactly at its configured limit.
func (attempt RecoveryAttempt) RecordFailure(at time.Time) (RecoveryAttempt, error) {
	at = canonicalUserTime(at)
	snapshot := attempt.Snapshot()
	if snapshot.Status != RecoveryAttemptActive || !at.Before(snapshot.ExpiresAt) || at.Before(snapshot.CreatedAt) ||
		snapshot.AttemptCount >= snapshot.MaxAttempts {
		return RecoveryAttempt{}, ErrRecoveryConcurrentTransition
	}
	snapshot.AttemptCount++
	if snapshot.AttemptCount == snapshot.MaxAttempts {
		snapshot.Status = RecoveryAttemptExpired
	}
	return RestoreRecoveryAttempt(snapshot)
}

// Consume binds the one winning CompleteRecovery transition to its exact result envelope.
func (attempt RecoveryAttempt) Consume(
	authorization RecoveryAttemptAuthorization,
	resultID uuid.UUID,
	digest idempotency.Digest,
	at time.Time,
) (RecoveryAttempt, error) {
	at = canonicalUserTime(at)
	snapshot := attempt.Snapshot()
	if resultID == uuid.Nil || !authorization.AllowsFirstUse(attempt) || snapshot.Status != RecoveryAttemptActive ||
		at.Before(snapshot.CreatedAt) || !at.Before(snapshot.ExpiresAt) || authorization.requestDigest != digest {
		return RecoveryAttempt{}, ErrRecoveryConcurrentTransition
	}
	snapshot.Binding.RequestDigestSet = true
	snapshot.Binding.RequestDigest = digest
	snapshot.Status = RecoveryAttemptConsumed
	snapshot.ConsumedAt = at
	snapshot.ResultID = resultID
	return RestoreRecoveryAttempt(snapshot)
}

// Revoke closes an unused attempt after its source authority has been invalidated.
func (attempt RecoveryAttempt) Revoke(at time.Time) (RecoveryAttempt, error) {
	at = canonicalUserTime(at)
	snapshot := attempt.Snapshot()
	if snapshot.Status != RecoveryAttemptActive || at.Before(snapshot.CreatedAt) {
		return RecoveryAttempt{}, ErrRecoveryConcurrentTransition
	}
	snapshot.Status = RecoveryAttemptRevoked
	snapshot.RevokedAt = at
	return RestoreRecoveryAttempt(snapshot)
}

// AllowsFirstUse requires the exact immutable attempt from which authorization was minted.
func (authorization RecoveryAttemptAuthorization) AllowsFirstUse(attempt RecoveryAttempt) bool {
	snapshot := attempt.Snapshot()
	return authorization.status == RecoveryAttemptActive && snapshot.Status == RecoveryAttemptActive &&
		authorization.matches(snapshot) && authorization.authorizedAt.Before(snapshot.ExpiresAt)
}

// AllowsResultReplay binds a consumed authorization to the exact committed result.
func (authorization RecoveryAttemptAuthorization) AllowsResultReplay(attempt RecoveryAttempt, resultID uuid.UUID) bool {
	snapshot := attempt.Snapshot()
	return resultID != uuid.Nil && authorization.status == RecoveryAttemptConsumed && snapshot.Status == RecoveryAttemptConsumed &&
		authorization.resultID == resultID && snapshot.ResultID == resultID && authorization.matches(snapshot) &&
		authorization.authorizedAt.Before(snapshot.ExpiresAt)
}

func (authorization RecoveryAttemptAuthorization) matches(snapshot RecoveryAttemptSnapshot) bool {
	return authorization.attemptID == snapshot.ID && authorization.selector == snapshot.Selector &&
		authorization.binding == snapshot.Binding && authorization.attemptCount == snapshot.AttemptCount &&
		authorization.maxAttempts == snapshot.MaxAttempts && authorization.expiresAt.Equal(snapshot.ExpiresAt) &&
		authorization.grantMAC.KeyVersion == snapshot.GrantMAC.KeyVersion &&
		bytes.Equal(authorization.grantMAC.Value, snapshot.GrantMAC.Value)
}

func recoveryAttemptAuthorization(
	snapshot RecoveryAttemptSnapshot,
	at time.Time,
	requestDigest idempotency.Digest,
) RecoveryAttemptAuthorization {
	return RecoveryAttemptAuthorization{
		attemptID: snapshot.ID, selector: snapshot.Selector,
		grantMAC: security.MAC[security.UserChallengeKeyPurpose]{
			KeyVersion: snapshot.GrantMAC.KeyVersion, Value: bytes.Clone(snapshot.GrantMAC.Value),
		},
		binding: snapshot.Binding, status: snapshot.Status, resultID: snapshot.ResultID,
		authorizedAt: at, attemptCount: snapshot.AttemptCount, maxAttempts: snapshot.MaxAttempts,
		expiresAt: snapshot.ExpiresAt, requestDigest: requestDigest,
	}
}

func validateRecoveryAttemptBinding(binding RecoveryAttemptBinding) error {
	ordinary := binding.RecoveryCredentialID != uuid.Nil && binding.RecoveryCredentialVersion > 0 && binding.AssistedGrantID == uuid.Nil
	assisted := binding.RecoveryCredentialID == uuid.Nil && binding.RecoveryCredentialVersion == 0 && binding.AssistedGrantID != uuid.Nil
	if binding.UserID == uuid.Nil || binding.ChallengeID == uuid.Nil || (!ordinary && !assisted) ||
		binding.RecoveryCredentialVersion > math.MaxInt64 {
		return ErrInvalidRecoveryAttempt
	}
	return nil
}

func recoveryGrantClaims(
	id uuid.UUID,
	selector identifier.Selector,
	binding RecoveryAttemptBinding,
	expiresAt time.Time,
	secret []byte,
) []byte {
	claims := make([]byte, 0, 256)
	claims = append(claims, recoveryGrantDomain...)
	claims = append(claims, id[:]...)
	claims = appendLengthPrefixedIdentity(claims, selector.Value())
	claims = append(claims, binding.UserID[:]...)
	claims = append(claims, binding.ChallengeID[:]...)
	claims = append(claims, binding.Origin[:]...)
	claims = append(claims, binding.RecoveryCredentialID[:]...)
	claims = binary.BigEndian.AppendUint64(claims, binding.RecoveryCredentialVersion)
	claims = append(claims, binding.AssistedGrantID[:]...)
	claims = binary.BigEndian.AppendUint64(claims, uint64(canonicalUserTime(expiresAt).UnixMicro()))
	claims = append(claims, secret...)
	return claims
}

func appendLengthPrefixedIdentity(target []byte, value string) []byte {
	target = binary.BigEndian.AppendUint32(target, uint32(len(value)))
	return append(target, value...)
}

func cloneRecoveryAttemptSnapshot(snapshot RecoveryAttemptSnapshot) RecoveryAttemptSnapshot {
	snapshot.GrantMAC.Value = bytes.Clone(snapshot.GrantMAC.Value)
	return snapshot
}
