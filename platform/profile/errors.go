package profile

import "errors"

var (
	// ErrInvalidProfileInput rejects malformed profile or export snapshots before persistence.
	ErrInvalidProfileInput = errors.New("invalid profile input")
	// ErrProfileNotFound is the repository absence result and never authorizes disclosure.
	ErrProfileNotFound = errors.New("profile not found")
	// ErrProfileRepositoryUnavailable hides PostgreSQL and generated-query details from domain callers.
	ErrProfileRepositoryUnavailable = errors.New("profile repository unavailable")
	// ErrProfileIntegrity reports persisted profile or export state that violates domain invariants.
	ErrProfileIntegrity = errors.New("profile persistence integrity failure")
	// ErrPIIAuthentication collapses wrong-user, wrong-field, and corrupted PII ciphertext failures.
	ErrPIIAuthentication = errors.New("profile ciphertext authentication failed")
	// ErrPIIKeyUnavailable reports a key version that is no longer available to the process.
	ErrPIIKeyUnavailable = errors.New("profile encryption key unavailable")
	// ErrProfileConcurrentTransition reports a stale profile version or timestamp transition.
	ErrProfileConcurrentTransition = errors.New("profile transition lost concurrency race")
	// ErrProfileExportClosed means a terminal export context cannot serve another page or transition.
	ErrProfileExportClosed = errors.New("profile export is closed")
	// ErrProfileExportExpired reports an active export that reached its expiry boundary.
	ErrProfileExportExpired = errors.New("profile export is expired")
	// ErrProfileExportNotExpired reports an attempted expiry transition before the TTL boundary.
	ErrProfileExportNotExpired = errors.New("profile export is not expired")
	// ErrProfileExportCursor rejects a malformed or non-canonical keyset cursor.
	ErrProfileExportCursor = errors.New("invalid profile export cursor")
)
