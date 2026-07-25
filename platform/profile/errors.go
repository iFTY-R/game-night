package profile

import "errors"

var (
	// ErrInvalidProfileInput rejects malformed profile snapshots before persistence.
	ErrInvalidProfileInput = errors.New("invalid profile input")
	// ErrProfileNotFound is the repository absence result and never authorizes disclosure.
	ErrProfileNotFound = errors.New("profile not found")
	// ErrProfileRepositoryUnavailable hides PostgreSQL and generated-query details from domain callers.
	ErrProfileRepositoryUnavailable = errors.New("profile repository unavailable")
	// ErrProfileIntegrity reports persisted profile state that violates domain invariants.
	ErrProfileIntegrity = errors.New("profile persistence integrity failure")
	// ErrPIIAuthentication collapses wrong-user, wrong-field, and corrupted PII ciphertext failures.
	ErrPIIAuthentication = errors.New("profile ciphertext authentication failed")
	// ErrPIIKeyUnavailable reports a key version that is no longer available to the process.
	ErrPIIKeyUnavailable = errors.New("profile encryption key unavailable")
	// ErrProfileConcurrentTransition reports a stale profile version or timestamp transition.
	ErrProfileConcurrentTransition = errors.New("profile transition lost concurrency race")
)
