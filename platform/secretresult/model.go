// Package secretresult protects short-lived one-time secrets and their idempotency tombstones.
package secretresult

import (
	"bytes"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/idempotency"
)

const (
	// DigestSize is the fixed SHA-256/HMAC-SHA-256 binding length stored by PostgreSQL.
	DigestSize = idempotency.DigestSize
	// MaximumSecretTTL matches the longest reliable-delivery window allowed by the platform design.
	MaximumSecretTTL = 10 * time.Minute
	// MinimumTombstoneRetention prevents delayed retries from executing again after secret erasure.
	MinimumTombstoneRetention = 30 * 24 * time.Hour
)

var (
	// ErrInvalidInput rejects malformed or incomplete result bindings without echoing sensitive values.
	ErrInvalidInput = errors.New("invalid secret result input")
	// ErrIdempotencyConflict means the same operation key was reused with a different request digest.
	ErrIdempotencyConflict = idempotency.ErrConflict
	// ErrReplayUnauthorized means actor, scope, result type, or version did not match the committed operation.
	ErrReplayUnauthorized = errors.New("secret result replay is unauthorized")
	// ErrSecretNoLongerAvailable reports an intact tombstone whose secret columns were already erased.
	ErrSecretNoLongerAvailable = errors.New("secret result is no longer available")
	// ErrConcurrentTransition reports a lost CAS that callers must resolve by rereading the tombstone.
	ErrConcurrentTransition = errors.New("secret result transition lost concurrency race")
	// ErrNotFound is the repository-level absence result and never authorizes execution by itself.
	ErrNotFound = errors.New("secret result not found")
	// ErrRepositoryUnavailable hides infrastructure-specific database errors from domain callers.
	ErrRepositoryUnavailable = errors.New("secret result repository unavailable")
	// ErrIntegrity reports persisted state that violates domain invariants without exposing stored values.
	ErrIntegrity = errors.New("secret result integrity failure")
	// ErrEnvelopeAuthentication merges malformed, wrong-key, and wrong-binding ciphertext failures.
	ErrEnvelopeAuthentication = errors.New("secret result envelope authentication failed")
)

// Scope identifies one operation family independently from the returned secret representation.
type Scope string

const (
	ScopeIdentityBootstrap            Scope = "identity.bootstrap"
	ScopeIdentityOnboarding           Scope = "identity.onboarding"
	ScopeIdentityRecovery             Scope = "identity.recovery"
	ScopeIdentityRecoveryCodeRotation Scope = "identity.recovery_code_rotation"
	ScopeAdminTOTPEnrollment          Scope = "admin.totp_enrollment"
	ScopeAdminInitialRecoveryCodes    Scope = "admin.initial_recovery_codes"
	ScopeAdminTOTPRebind              Scope = "admin.totp_rebind"
	ScopeAdminRegenerateRecoveryCodes Scope = "admin.regenerate_recovery_codes"
	ScopeAdminAssistedRecoveryGrant   Scope = "admin.assisted_recovery_grant"
)

// Valid rejects free-form scopes so protocol and database values cannot silently drift.
func (scope Scope) Valid() bool {
	switch scope {
	case ScopeIdentityBootstrap, ScopeIdentityOnboarding, ScopeIdentityRecovery,
		ScopeIdentityRecoveryCodeRotation, ScopeAdminTOTPEnrollment,
		ScopeAdminInitialRecoveryCodes, ScopeAdminTOTPRebind,
		ScopeAdminRegenerateRecoveryCodes, ScopeAdminAssistedRecoveryGrant:
		return true
	default:
		return false
	}
}

// IsIdentity reports whether the operation belongs to an anonymous or user-owned identity workflow.
func (scope Scope) IsIdentity() bool {
	switch scope {
	case ScopeIdentityBootstrap, ScopeIdentityOnboarding, ScopeIdentityRecovery, ScopeIdentityRecoveryCodeRotation:
		return true
	default:
		return false
	}
}

// IsAdmin reports whether the operation belongs to an administrator security workflow.
func (scope Scope) IsAdmin() bool {
	switch scope {
	case ScopeAdminTOTPEnrollment, ScopeAdminInitialRecoveryCodes, ScopeAdminTOTPRebind,
		ScopeAdminRegenerateRecoveryCodes, ScopeAdminAssistedRecoveryGrant:
		return true
	default:
		return false
	}
}

// ResultType identifies the plaintext schema and is deliberately separate from operation scope.
type ResultType string

const (
	ResultTypeIdentityDeviceCredential   ResultType = "identity.device_credential"
	ResultTypeIdentityRecoveryCode       ResultType = "identity.recovery_code"
	ResultTypeIdentityRecoveryBundle     ResultType = "identity.recovery_bundle"
	ResultTypeAdminTOTPEnrollment        ResultType = "admin.totp_enrollment"
	ResultTypeAdminRecoveryCodes         ResultType = "admin.recovery_codes"
	ResultTypeAdminAssistedRecoveryGrant ResultType = "admin.assisted_recovery_grant"
)

// Valid rejects result schemas that have not been explicitly versioned by the domain.
func (resultType ResultType) Valid() bool {
	switch resultType {
	case ResultTypeIdentityDeviceCredential, ResultTypeIdentityRecoveryCode,
		ResultTypeIdentityRecoveryBundle, ResultTypeAdminTOTPEnrollment,
		ResultTypeAdminRecoveryCodes, ResultTypeAdminAssistedRecoveryGrant:
		return true
	default:
		return false
	}
}

// Digest is the neutral immutable request binding shared with challenge authorization.
type Digest = idempotency.Digest

// NewDigest copies an exact 256-bit digest into a strongly typed value.
func NewDigest(value []byte) (Digest, error) {
	digest, err := idempotency.NewDigest(value)
	if err != nil {
		return Digest{}, ErrInvalidInput
	}
	return digest, nil
}

// OperationID is the neutral canonical operation identifier shared with challenge authorization.
type OperationID = idempotency.OperationID

// NewOperationID encodes caller-generated entropy without weakening the 128-bit lower bound.
func NewOperationID(entropy []byte) (OperationID, error) {
	operationID, err := idempotency.NewOperationID(entropy)
	if err != nil {
		return OperationID{}, ErrInvalidInput
	}
	return operationID, nil
}

// ParseOperationID validates the canonical wire and database representation.
func ParseOperationID(value string) (OperationID, error) {
	operationID, err := idempotency.ParseOperationID(value)
	if err != nil {
		return OperationID{}, ErrInvalidInput
	}
	return operationID, nil
}

// Key scopes an operation ID to one actor or challenge so global preemption is impossible.
type Key struct {
	Scope       Scope
	ActorID     uuid.UUID
	OperationID OperationID
}

// Validate enforces every part of the composite idempotency key.
func (key Key) Validate() error {
	if !key.Scope.Valid() || key.ActorID == uuid.Nil || !key.OperationID.Valid() {
		return ErrInvalidInput
	}
	return nil
}

// Binding contains every authorization and plaintext-schema field committed into envelope AAD.
type Binding struct {
	Key           Key
	RequestDigest Digest
	ResultType    ResultType
	ResultVersion uint32
}

// Validate prevents incomplete values from reaching encryption or persistence.
func (binding Binding) Validate() error {
	if err := binding.Key.Validate(); err != nil || !binding.ResultType.Valid() || binding.ResultVersion == 0 ||
		!scopeAllowsResultType(binding.Key.Scope, binding.ResultType) {
		return ErrInvalidInput
	}
	return nil
}

func scopeAllowsResultType(scope Scope, resultType ResultType) bool {
	switch scope {
	case ScopeIdentityBootstrap:
		return resultType == ResultTypeIdentityDeviceCredential
	case ScopeIdentityOnboarding, ScopeIdentityRecoveryCodeRotation:
		return resultType == ResultTypeIdentityRecoveryCode
	case ScopeIdentityRecovery:
		return resultType == ResultTypeIdentityRecoveryBundle
	case ScopeAdminTOTPEnrollment:
		return resultType == ResultTypeAdminTOTPEnrollment
	case ScopeAdminInitialRecoveryCodes, ScopeAdminRegenerateRecoveryCodes:
		return resultType == ResultTypeAdminRecoveryCodes
	case ScopeAdminTOTPRebind:
		return resultType == ResultTypeAdminTOTPEnrollment || resultType == ResultTypeAdminRecoveryCodes
	case ScopeAdminAssistedRecoveryGrant:
		return resultType == ResultTypeAdminAssistedRecoveryGrant
	default:
		return false
	}
}

// Status records whether the short-lived secret is present or only its idempotency tombstone remains.
type Status string

const (
	StatusAvailable Status = "available"
	StatusConfirmed Status = "confirmed"
	StatusExpired   Status = "expired"
)

// EncryptedPayload carries the data ciphertext and the master-key-wrapped random data key.
type EncryptedPayload struct {
	Ciphertext     []byte
	Nonce          []byte
	WrappedDataKey []byte
	KeyVersion     uint32
}

// Empty reports whether all recoverable secret material has been erased.
func (payload EncryptedPayload) Empty() bool {
	return len(payload.Ciphertext) == 0 && len(payload.Nonce) == 0 && len(payload.WrappedDataKey) == 0
}

// Snapshot is the persistence-neutral representation used to restore a validated Result.
type Snapshot struct {
	ID                 uuid.UUID
	Binding            Binding
	Payload            EncryptedPayload
	Status             Status
	SecretExpiresAt    time.Time
	ConfirmedAt        time.Time
	CompletedAt        time.Time
	TombstoneExpiresAt time.Time
}

// Result is an immutable validated state-machine value.
type Result struct {
	snapshot Snapshot
}

// NewAvailable creates the only non-terminal result state after envelope encryption.
func NewAvailable(id uuid.UUID, binding Binding, payload EncryptedPayload, completedAt, secretExpiresAt, tombstoneExpiresAt time.Time) (Result, error) {
	return Restore(Snapshot{
		ID: id, Binding: binding, Payload: payload, Status: StatusAvailable,
		SecretExpiresAt: secretExpiresAt, CompletedAt: completedAt, TombstoneExpiresAt: tombstoneExpiresAt,
	})
}

// Restore validates database state before it can participate in replay authorization.
func Restore(snapshot Snapshot) (Result, error) {
	snapshot = cloneSnapshot(snapshot)
	snapshot.SecretExpiresAt = canonicalTime(snapshot.SecretExpiresAt)
	snapshot.ConfirmedAt = canonicalOptionalTime(snapshot.ConfirmedAt)
	snapshot.CompletedAt = canonicalTime(snapshot.CompletedAt)
	snapshot.TombstoneExpiresAt = canonicalTime(snapshot.TombstoneExpiresAt)
	if snapshot.ID == uuid.Nil || snapshot.Binding.Validate() != nil || snapshot.CompletedAt.IsZero() ||
		!snapshot.SecretExpiresAt.After(snapshot.CompletedAt) ||
		snapshot.SecretExpiresAt.Sub(snapshot.CompletedAt) > MaximumSecretTTL ||
		snapshot.TombstoneExpiresAt.Before(snapshot.SecretExpiresAt.Add(MinimumTombstoneRetention)) {
		return Result{}, ErrInvalidInput
	}
	switch snapshot.Status {
	case StatusAvailable:
		if snapshot.Payload.KeyVersion == 0 || snapshot.Payload.Empty() || len(snapshot.Payload.Ciphertext) == 0 ||
			len(snapshot.Payload.Nonce) == 0 || len(snapshot.Payload.WrappedDataKey) == 0 || !snapshot.ConfirmedAt.IsZero() {
			return Result{}, ErrInvalidInput
		}
	case StatusConfirmed:
		if !snapshot.Payload.Empty() || snapshot.Payload.KeyVersion == 0 || snapshot.ConfirmedAt.IsZero() ||
			snapshot.ConfirmedAt.Before(snapshot.CompletedAt) || !snapshot.ConfirmedAt.Before(snapshot.SecretExpiresAt) {
			return Result{}, ErrInvalidInput
		}
	case StatusExpired:
		if !snapshot.Payload.Empty() || snapshot.Payload.KeyVersion == 0 || !snapshot.ConfirmedAt.IsZero() {
			return Result{}, ErrInvalidInput
		}
	default:
		return Result{}, ErrInvalidInput
	}
	return Result{snapshot: snapshot}, nil
}

// Snapshot returns a deep copy so repository adapters cannot mutate domain state after validation.
func (result Result) Snapshot() Snapshot {
	return cloneSnapshot(result.snapshot)
}

// Resolve classifies a request without letting operation ID alone authorize replay.
func (result Result) Resolve(binding Binding, now time.Time) (Resolution, error) {
	if binding.Validate() != nil {
		return Resolution{}, ErrInvalidInput
	}
	committed := result.snapshot.Binding
	if committed.Key != binding.Key || committed.ResultType != binding.ResultType || committed.ResultVersion != binding.ResultVersion {
		return Resolution{}, ErrReplayUnauthorized
	}
	if committed.RequestDigest != binding.RequestDigest {
		return Resolution{}, ErrIdempotencyConflict
	}
	if result.snapshot.Status != StatusAvailable || !canonicalTime(now).Before(result.snapshot.SecretExpiresAt) {
		return Resolution{Kind: ReplayUnavailable, Result: result}, nil
	}
	return Resolution{Kind: ReplayAvailable, Result: result}, nil
}

// Confirm erases recoverable columns and retains an idempotent confirmed tombstone.
func (result Result) Confirm(at time.Time) (Result, error) {
	if result.snapshot.Status == StatusConfirmed {
		return result, nil
	}
	confirmedAt := canonicalTime(at)
	if result.snapshot.Status != StatusAvailable || confirmedAt.IsZero() || !confirmedAt.Before(result.snapshot.SecretExpiresAt) {
		return Result{}, ErrSecretNoLongerAvailable
	}
	snapshot := result.Snapshot()
	snapshot.Status = StatusConfirmed
	snapshot.Payload = EncryptedPayload{KeyVersion: snapshot.Payload.KeyVersion}
	snapshot.ConfirmedAt = confirmedAt
	return Restore(snapshot)
}

// Expire erases an unconfirmed secret at its TTL boundary and keeps the same operation tombstone.
func (result Result) Expire(at time.Time) (Result, error) {
	if result.snapshot.Status == StatusExpired {
		return result, nil
	}
	expiredAt := canonicalTime(at)
	if result.snapshot.Status != StatusAvailable || expiredAt.Before(result.snapshot.SecretExpiresAt) {
		return Result{}, ErrConcurrentTransition
	}
	snapshot := result.Snapshot()
	snapshot.Status = StatusExpired
	snapshot.Payload = EncryptedPayload{KeyVersion: snapshot.Payload.KeyVersion}
	return Restore(snapshot)
}

// ResolutionKind separates first execution from available and erased exact replays.
type ResolutionKind uint8

const (
	ExecuteNew ResolutionKind = iota + 1
	ReplayAvailable
	ReplayUnavailable
)

// Resolution carries the committed result when an operation key already exists.
type Resolution struct {
	Kind   ResolutionKind
	Result Result
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	snapshot.Payload.Ciphertext = bytes.Clone(snapshot.Payload.Ciphertext)
	snapshot.Payload.Nonce = bytes.Clone(snapshot.Payload.Nonce)
	snapshot.Payload.WrappedDataKey = bytes.Clone(snapshot.Payload.WrappedDataKey)
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
