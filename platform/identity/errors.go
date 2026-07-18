package identity

import "errors"

var (
	// ErrInvalidIdentityRequest rejects incomplete service commands without echoing submitted credentials.
	ErrInvalidIdentityRequest = errors.New("invalid identity request")
	// ErrInvalidUserInput rejects malformed user or username-claim snapshots.
	ErrInvalidUserInput = errors.New("invalid user input")
	// ErrUserStatus rejects an operation that is not permitted in the current lifecycle state.
	ErrUserStatus = errors.New("user status does not permit operation")
	// ErrOnboardingExpired prevents abandoned anonymous identities from being activated after 24 hours.
	ErrOnboardingExpired = errors.New("identity onboarding expired")
	// ErrUsernameChangeCooldown enforces the 30-day user-initiated rename interval.
	ErrUsernameChangeCooldown = errors.New("username change cooldown is active")
	// ErrUsernameUnchanged prevents a claim transaction for an equivalent folded username key.
	ErrUsernameUnchanged = errors.New("username is unchanged")
	// ErrUsernameUnavailable collapses active, reserved, and concurrent username claim conflicts.
	ErrUsernameUnavailable = errors.New("username is unavailable")
	// ErrUserNotFound is a repository absence result and must not be used to infer credential validity externally.
	ErrUserNotFound = errors.New("user not found")
	// ErrIdentityConcurrentTransition reports a stale user, claim, recovery, or device CAS.
	ErrIdentityConcurrentTransition = errors.New("identity transition lost concurrency race")
	// ErrIdentityRepositoryUnavailable hides PostgreSQL and generated-query details from the domain.
	ErrIdentityRepositoryUnavailable = errors.New("identity repository unavailable")
	// ErrIdentityIntegrity reports persisted cross-aggregate state that violates identity invariants.
	ErrIdentityIntegrity = errors.New("identity persistence integrity failure")
	// ErrInvalidRecoveryCredential rejects malformed generated or restored recovery records.
	ErrInvalidRecoveryCredential = errors.New("invalid recovery credential")
	// ErrRecoveryInvalid collapses malformed, unknown, expired, consumed, and mismatched recovery material.
	ErrRecoveryInvalid = errors.New("recovery credential is invalid")
	// ErrRecoveryConcurrentTransition reports a stale recovery credential, attempt, or assisted-grant CAS.
	ErrRecoveryConcurrentTransition = errors.New("recovery transition lost concurrency race")
	// ErrInvalidRecoveryAttempt rejects malformed grant claims or corrupted persisted attempt state.
	ErrInvalidRecoveryAttempt = errors.New("invalid recovery attempt")
	// ErrInvalidAssistedRecoveryGrant rejects malformed or corrupted administrator-assisted recovery state.
	ErrInvalidAssistedRecoveryGrant = errors.New("invalid assisted recovery grant")
	// ErrInvalidDeviceInput rejects malformed issue parameters and corrupted snapshots without echoing secrets.
	ErrInvalidDeviceInput = errors.New("invalid device credential input")
	// ErrDeviceAuthentication merges malformed tokens, selector mismatches, and wrong secrets.
	ErrDeviceAuthentication = errors.New("device credential authentication failed")
	// ErrDeviceUnavailable covers revoked and expired credentials after their secret has been verified.
	ErrDeviceUnavailable = errors.New("device credential unavailable")
	// ErrDeviceRotationNotDue prevents routine rotation from becoming an unbounded token churn primitive.
	ErrDeviceRotationNotDue = errors.New("device credential rotation is not due")
	// ErrDeviceConcurrentTransition reports stale generation or lifecycle authority.
	ErrDeviceConcurrentTransition = errors.New("device credential transition lost concurrency race")
	// ErrDeviceIntegrity reports persisted cryptographic metadata that cannot be verified safely.
	ErrDeviceIntegrity = errors.New("device credential integrity failure")
)
