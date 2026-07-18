// Package challenge implements short-lived, dual-proof authorization challenges.
package challenge

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/security"
)

const (
	// TTL is fixed so callers cannot accidentally create a longer login-CSRF authorization window.
	TTL = 5 * time.Minute
	// SelectorBytes provides 128 bits of public lookup entropy for every challenge cookie.
	SelectorBytes = 16
	// SecretBytes provides 256 bits of bearer entropy kept only in the HttpOnly challenge cookie.
	SecretBytes = 32
	// DefaultMaxAttempts is the conservative retry allowance used when a flow has no stricter policy.
	DefaultMaxAttempts uint32 = 5
	// MaximumAttempts bounds online guessing and prevents malformed persisted counters from remaining active.
	MaximumAttempts uint32 = 32
	// MaximumReplayTTL caps how long a consumed challenge may authorize its exact one-time result.
	MaximumReplayTTL = 10 * time.Minute
)

var (
	// ErrInvalidInput rejects malformed issue parameters or persisted state without exposing submitted secrets.
	ErrInvalidInput = errors.New("invalid challenge input")
	// ErrAuthentication merges cookie, proof, key, and request-binding mismatches into one external result.
	ErrAuthentication = errors.New("challenge authentication failed")
	// ErrUnavailable covers expired, revoked, exhausted, consumed-without-replay, and missing challenges.
	ErrUnavailable = errors.New("challenge unavailable")
	// ErrConcurrentTransition reports a stale aggregate that lost an attempt, consume, or revoke CAS.
	ErrConcurrentTransition = errors.New("challenge transition lost concurrency race")
	// ErrNotFound is the repository absence result and never authorizes a new operation by itself.
	ErrNotFound = errors.New("challenge not found")
	// ErrRepositoryUnavailable prevents infrastructure-specific errors from crossing the domain boundary.
	ErrRepositoryUnavailable = errors.New("challenge repository unavailable")
	// ErrIntegrity reports a persisted row that cannot satisfy domain invariants without echoing stored values.
	ErrIntegrity = errors.New("challenge persistence integrity violation")

	bindingValuePattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)
	flowValuePattern    = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// Purpose identifies one closed workflow value supplied by the identity or admin wrapper package.
type Purpose string

// Audience prevents a valid challenge from crossing Connect service boundaries.
type Audience string

// RequestFlowID correlates a Begin response with exactly one client-side completion flow.
type RequestFlowID string

// OriginDigest stores SHA-256 over the already validated canonical Origin without retaining the URL in challenge rows.
type OriginDigest [sha256.Size]byte

// DigestOrigin binds a challenge to the exact canonical Origin accepted by the transport policy.
func DigestOrigin(canonicalOrigin string) (OriginDigest, error) {
	if canonicalOrigin == "" || len(canonicalOrigin) > 2048 {
		return OriginDigest{}, ErrInvalidInput
	}
	return sha256.Sum256([]byte(canonicalOrigin)), nil
}

// NewOriginDigest restores an exact persisted SHA-256 value without sharing its backing memory.
func NewOriginDigest(value []byte) (OriginDigest, error) {
	if len(value) != sha256.Size {
		return OriginDigest{}, ErrInvalidInput
	}
	var digest OriginDigest
	copy(digest[:], value)
	return digest, nil
}

// Bytes returns an independent persistence representation of the Origin digest.
func (digest OriginDigest) Bytes() []byte {
	return bytes.Clone(digest[:])
}

// SubjectBinding commits an administrator identity and credential generations into a challenge.
// The zero value means an anonymous identity-side challenge and is also included in canonical claims.
type SubjectBinding struct {
	ID                uuid.UUID
	Version           int64
	CredentialVersion int64
}

// Bound reports whether the challenge is tied to an authenticated subject generation.
func (binding SubjectBinding) Bound() bool {
	return binding.ID != uuid.Nil
}

// Binding contains every request-context value authenticated by the body proof.
type Binding struct {
	Purpose       Purpose
	Audience      Audience
	Origin        OriginDigest
	RequestFlowID RequestFlowID
	Subject       SubjectBinding
}

// Validate rejects free-form or incomplete values before they can be signed or restored.
func (binding Binding) Validate() error {
	if !validBindingValue(string(binding.Purpose)) || !validBindingValue(string(binding.Audience)) ||
		!validFlowValue(string(binding.RequestFlowID)) || binding.Origin == (OriginDigest{}) {
		return ErrInvalidInput
	}
	if binding.Subject.ID == uuid.Nil {
		if binding.Subject.Version != 0 || binding.Subject.CredentialVersion != 0 {
			return ErrInvalidInput
		}
		return nil
	}
	if binding.Subject.Version <= 0 || binding.Subject.CredentialVersion < 0 {
		return ErrInvalidInput
	}
	return nil
}

// ReplayAuthorization is the only state retained after first consumption that can authorize result retrieval.
type ReplayAuthorization struct {
	OperationID   idempotency.OperationID
	RequestDigest idempotency.Digest
	ResultID      uuid.UUID
	ReplayUntil   time.Time
}

// Snapshot is the persistence-neutral representation accepted by Restore.
type Snapshot[P security.HMACKeyPurpose] struct {
	ID           uuid.UUID
	Selector     identifier.Selector
	SecretMAC    security.MAC[P]
	Binding      Binding
	AttemptCount uint32
	MaxAttempts  uint32
	CreatedAt    time.Time
	ExpiresAt    time.Time
	ConsumedAt   time.Time
	RevokedAt    time.Time
	// PersistedState preserves cleanup-owned expiry even when stored timestamps are skewed or malformed.
	PersistedState State
	Replay         *ReplayAuthorization
}

// Challenge is an immutable validated aggregate. Transitions return a new value for repository CAS operations.
type Challenge[P security.HMACKeyPurpose] struct {
	snapshot Snapshot[P]
}

// Restore validates storage state before it can participate in authentication or replay authorization.
func Restore[P security.HMACKeyPurpose](snapshot Snapshot[P]) (Challenge[P], error) {
	snapshot = cloneSnapshot(snapshot)
	snapshot.CreatedAt = canonicalTime(snapshot.CreatedAt)
	snapshot.ExpiresAt = canonicalTime(snapshot.ExpiresAt)
	snapshot.ConsumedAt = canonicalOptionalTime(snapshot.ConsumedAt)
	snapshot.RevokedAt = canonicalOptionalTime(snapshot.RevokedAt)
	if snapshot.Replay != nil {
		snapshot.Replay.ReplayUntil = canonicalTime(snapshot.Replay.ReplayUntil)
	}

	if snapshot.ID == uuid.Nil || snapshot.Selector.ByteLength() != SelectorBytes ||
		snapshot.SecretMAC.KeyVersion == 0 || len(snapshot.SecretMAC.Value) != sha256.Size ||
		snapshot.Binding.Validate() != nil || snapshot.MaxAttempts == 0 || snapshot.MaxAttempts > MaximumAttempts ||
		snapshot.AttemptCount > snapshot.MaxAttempts || snapshot.CreatedAt.IsZero() ||
		!snapshot.ExpiresAt.Equal(snapshot.CreatedAt.Add(TTL)) ||
		(snapshot.PersistedState != 0 && snapshot.PersistedState != StateExpired) {
		return Challenge[P]{}, ErrInvalidInput
	}

	consumed := !snapshot.ConsumedAt.IsZero()
	revoked := !snapshot.RevokedAt.IsZero()
	if consumed && revoked || snapshot.PersistedState == StateExpired && (consumed || revoked || snapshot.Replay != nil) {
		return Challenge[P]{}, ErrInvalidInput
	}
	if consumed {
		if snapshot.AttemptCount >= snapshot.MaxAttempts || snapshot.ConsumedAt.Before(snapshot.CreatedAt) ||
			!snapshot.ConsumedAt.Before(snapshot.ExpiresAt) ||
			(snapshot.Replay != nil && !validReplay(*snapshot.Replay, snapshot.ConsumedAt)) {
			return Challenge[P]{}, ErrInvalidInput
		}
	} else if snapshot.Replay != nil {
		return Challenge[P]{}, ErrInvalidInput
	}
	if revoked && snapshot.RevokedAt.Before(snapshot.CreatedAt) {
		return Challenge[P]{}, ErrInvalidInput
	}
	return Challenge[P]{snapshot: snapshot}, nil
}

// Snapshot returns a deep copy so adapters cannot mutate authentication material after validation.
func (challenge Challenge[P]) Snapshot() Snapshot[P] {
	return cloneSnapshot(challenge.snapshot)
}

// State derives availability from terminal metadata, expiry, and attempt count instead of trusting a mutable label.
func (challenge Challenge[P]) State(now time.Time) State {
	now = canonicalTime(now)
	// A cleanup worker may persist expiry before the wall clock reaches a malformed or skewed expires_at value.
	if challenge.snapshot.PersistedState == StateExpired {
		return StateExpired
	}
	if now.Before(challenge.snapshot.CreatedAt) {
		return StateExpired
	}
	if !challenge.snapshot.ConsumedAt.IsZero() {
		return StateConsumed
	}
	if !challenge.snapshot.RevokedAt.IsZero() {
		return StateRevoked
	}
	if challenge.snapshot.AttemptCount >= challenge.snapshot.MaxAttempts {
		return StateExhausted
	}
	if !now.Before(challenge.snapshot.ExpiresAt) {
		return StateExpired
	}
	return StateActive
}

// RecordFailure returns the next attempt state. Repositories still enforce the corresponding atomic increment.
func (challenge Challenge[P]) RecordFailure(at time.Time) (Challenge[P], error) {
	if challenge.State(at) != StateActive {
		return Challenge[P]{}, ErrConcurrentTransition
	}
	snapshot := challenge.Snapshot()
	snapshot.AttemptCount++
	return Restore(snapshot)
}

// Consume commits exact replay authorization after the surrounding transaction has produced the result ID.
func (challenge Challenge[P]) Consume(at time.Time, replay ReplayAuthorization) (Challenge[P], error) {
	consumedAt := canonicalTime(at)
	if challenge.State(consumedAt) != StateActive || !validReplay(replay, consumedAt) {
		return Challenge[P]{}, ErrConcurrentTransition
	}
	snapshot := challenge.Snapshot()
	snapshot.ConsumedAt = consumedAt
	replay.ReplayUntil = canonicalTime(replay.ReplayUntil)
	snapshot.Replay = &replay
	return Restore(snapshot)
}

// ConsumeWithoutReplay terminates flows that return no one-time result and therefore must never be replay-authorized.
func (challenge Challenge[P]) ConsumeWithoutReplay(at time.Time) (Challenge[P], error) {
	consumedAt := canonicalTime(at)
	if challenge.State(consumedAt) != StateActive {
		return Challenge[P]{}, ErrConcurrentTransition
	}
	snapshot := challenge.Snapshot()
	snapshot.ConsumedAt = consumedAt
	return Restore(snapshot)
}

// Revoke terminates an active challenge without creating any replay authorization.
func (challenge Challenge[P]) Revoke(at time.Time) (Challenge[P], error) {
	revokedAt := canonicalTime(at)
	if challenge.State(revokedAt) != StateActive || revokedAt.Before(challenge.snapshot.CreatedAt) {
		return Challenge[P]{}, ErrConcurrentTransition
	}
	snapshot := challenge.Snapshot()
	snapshot.RevokedAt = revokedAt
	return Restore(snapshot)
}

// State is the derived lifecycle result used by services and cleanup workers.
type State uint8

const (
	StateActive State = iota + 1
	StateConsumed
	StateExpired
	StateRevoked
	StateExhausted
)

func validReplay(replay ReplayAuthorization, consumedAt time.Time) bool {
	replayUntil := canonicalTime(replay.ReplayUntil)
	return replay.OperationID.Valid() && replay.ResultID != uuid.Nil && replayUntil.After(consumedAt) &&
		replayUntil.Sub(consumedAt) <= MaximumReplayTTL
}

func validBindingValue(value string) bool {
	return len(value) > 0 && len(value) <= 64 && bindingValuePattern.MatchString(value)
}

func validFlowValue(value string) bool {
	return len(value) > 0 && len(value) <= 128 && flowValuePattern.MatchString(value)
}

func cloneSnapshot[P security.HMACKeyPurpose](snapshot Snapshot[P]) Snapshot[P] {
	snapshot.SecretMAC.Value = bytes.Clone(snapshot.SecretMAC.Value)
	if snapshot.Replay != nil {
		replay := *snapshot.Replay
		snapshot.Replay = &replay
	}
	return snapshot
}

func canonicalTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC().Truncate(time.Microsecond)
}

func canonicalOptionalTime(value time.Time) time.Time {
	return canonicalTime(value)
}
