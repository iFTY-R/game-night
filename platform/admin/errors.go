package admin

import "errors"

var (
	// ErrInvalidInput is returned before any state-changing repository call for malformed input.
	ErrInvalidInput = errors.New("invalid administrator input")
	// ErrAuthentication deliberately collapses password, challenge, TOTP, and session mismatches.
	ErrAuthentication = errors.New("administrator authentication failed")
	// ErrUnavailable means the account or security artifact cannot authorize an operation.
	ErrUnavailable = errors.New("administrator security state unavailable")
	// ErrNotFound is an internal absence result used by repository adapters before normalization.
	ErrNotFound = errors.New("administrator record not found")
	// ErrConcurrentTransition identifies a stale CAS transition that lost a race.
	ErrConcurrentTransition = errors.New("administrator state changed concurrently")
	// ErrRepositoryUnavailable keeps database diagnostics behind the admin domain boundary.
	ErrRepositoryUnavailable = errors.New("administrator repository unavailable")
	// ErrIntegrity reports persisted data that cannot satisfy the domain invariants.
	ErrIntegrity = errors.New("administrator persistence integrity violation")
	// ErrPasswordPolicy is intentionally stable so transports can expose one validation detail.
	ErrPasswordPolicy = errors.New("administrator password does not satisfy policy")
	// ErrTOTPInvalid rejects malformed or already accepted one-time codes.
	ErrTOTPInvalid = errors.New("administrator TOTP code is invalid")
	// ErrSessionExpired and ErrSessionRevoked distinguish terminal local metadata from bad proofs.
	ErrSessionExpired = errors.New("administrator session expired")
	ErrSessionRevoked = errors.New("administrator session revoked")
	// ErrPermissionDenied is the default-deny authorizer result for every non-full session.
	ErrPermissionDenied = errors.New("administrator permission denied")
	// ErrRecoveryInvalid does not reveal whether a selector or Argon2 secret was wrong.
	ErrRecoveryInvalid = errors.New("administrator recovery code is invalid")
	// ErrIdempotencyConflict prevents operation IDs from authorizing a different request body.
	ErrIdempotencyConflict = errors.New("administrator operation idempotency conflict")
	// ErrBootstrapSecretMismatch is used by readiness/bootstrap coordination without exposing the secret.
	ErrBootstrapSecretMismatch = errors.New("administrator bootstrap secret mismatch")
)
