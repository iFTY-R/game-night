package identity

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/secretaccess"
	"github.com/iFTY-R/game-night/platform/security"
	"golang.org/x/text/unicode/norm"
)

const (
	// DeviceTokenVersion is persisted in every long-lived device bearer token.
	DeviceTokenVersion = "v1"
	// DeviceSecretBytes provides exactly 256 bits of bearer entropy.
	DeviceSecretBytes = 32
	// DeviceCSRFSecretBytes keeps the independent browser-readable CSRF secret at 256 bits.
	DeviceCSRFSecretBytes = 32
	// DeviceIdleTTL expires credentials that have not been observed for six months.
	DeviceIdleTTL = 180 * 24 * time.Hour
	// DeviceAbsoluteTTL bounds a credential regardless of activity.
	DeviceAbsoluteTTL = 365 * 24 * time.Hour
	// DeviceRotationInterval is the scheduled bearer-secret rotation cadence.
	DeviceRotationInterval = 30 * 24 * time.Hour
	// DevicePreviousSecretGrace permits already-started reads to finish after a winning rotation.
	DevicePreviousSecretGrace = 2 * time.Minute
	// MaximumDeviceLabelCodePoints bounds user-visible device labels after NFKC normalization.
	MaximumDeviceLabelCodePoints = 64

	// maximumDeviceLabelInputBytes bounds normalization work before the code-point limit is evaluated.
	maximumDeviceLabelInputBytes = MaximumDeviceLabelCodePoints * 16
)

// Domain separators prevent device bearer and CSRF digests from being valid in another HMAC context.
const deviceSecretDomain = "game-night:device-secret:v1\x00"
const deviceCSRFDigestDomain = "game-night:device-csrf:v1\x00"

// DeviceState is derived from persisted lifecycle timestamps; only active state establishes a principal.
type DeviceState uint8

const (
	DeviceStateActive DeviceState = iota + 1
	DeviceStateExpired
	DeviceStateRevoked
)

// DeviceSecretKind records which generation authenticated without exposing the submitted secret.
type DeviceSecretKind uint8

const (
	DeviceSecretCurrent DeviceSecretKind = iota + 1
	DeviceSecretPrevious
)

// DeviceRevokeReason is closed so only account terminal reasons can produce a status inspection response.
type DeviceRevokeReason string

const (
	DeviceRevokeUserRequested    DeviceRevokeReason = "user_requested"
	DeviceRevokeAdminRequested   DeviceRevokeReason = "admin_requested"
	DeviceRevokeRecovery         DeviceRevokeReason = "recovery"
	DeviceRevokeOnboardingExpiry DeviceRevokeReason = "onboarding_expired"
	DeviceRevokeAccountSuspended DeviceRevokeReason = "account_suspended"
	DeviceRevokeAccountDeleted   DeviceRevokeReason = "account_deleted"
)

// AccountInstruction is returned only after a revoked credential secret has been verified successfully.
type AccountInstruction uint8

const (
	AccountInstructionNone AccountInstruction = iota
	AccountInstructionSuspended
	AccountInstructionDeleted
)

// DeviceCredentialSnapshot is the persistence-neutral state accepted by RestoreDeviceCredential.
type DeviceCredentialSnapshot struct {
	CredentialID uuid.UUID
	UserID       uuid.UUID

	SecretMAC                security.MAC[security.DeviceHMACKeyPurpose]
	PreviousSecretMAC        *security.MAC[security.DeviceHMACKeyPurpose]
	PreviousSecretValidUntil time.Time
	CSRFMAC                  []byte
	Generation               uint64
	Label                    string

	CreatedAt         time.Time
	LastSeenAt        time.Time
	RotatedAt         time.Time
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
	RevokedAt         time.Time
	RevokeReason      DeviceRevokeReason
}

// DeviceCredential is an immutable aggregate; repositories persist only validated snapshots.
type DeviceCredential struct {
	snapshot DeviceCredentialSnapshot
}

// issuedDeviceSecrets exists before persistence and cannot cross the application boundary directly.
type issuedDeviceSecrets struct {
	token      string
	csrfToken  string
	generation uint64
}

// IssuedDeviceCredential combines the aggregate to persist with its short-lived response material.
type IssuedDeviceCredential struct {
	Credential DeviceCredential
	secrets    issuedDeviceSecrets
}

// DeviceCookieWrite proves token, CSRF, and generation matched an authoritative persisted device row.
// Private fields prevent transports from constructing Cookie material without the identity service.
type DeviceCookieWrite struct {
	token      string
	csrfToken  string
	generation uint64
}

// Token returns the bearer value that the transport may install in the device Cookie.
func (write DeviceCookieWrite) Token() string {
	return write.token
}

// CSRFToken returns the companion value bound to the same persisted device row.
func (write DeviceCookieWrite) CSRFToken() string {
	return write.csrfToken
}

// Generation identifies the exact credential generation authorized for installation.
func (write DeviceCookieWrite) Generation() uint64 {
	return write.generation
}

// DeviceVerification proves a submitted token matched one persisted current or previous MAC.
type DeviceVerification struct {
	credentialID       uuid.UUID
	userID             uuid.UUID
	observedGeneration uint64
	secretKind         DeviceSecretKind
	state              DeviceState
	revokeReason       DeviceRevokeReason
	verifiedAt         time.Time
}

// DeviceAuthorization is an active verification capability with private, generation-bound state.
type DeviceAuthorization struct {
	verification DeviceVerification
}

// DeviceService owns device secret hashing, parsing, issuance, verification, and rotation.
type DeviceService struct {
	keyring *security.HMACKeyring[security.DeviceHMACKeyPurpose]
	clock   clock.Clock
}

// NewDeviceService requires a purpose-specific keyring and explicit UTC clock.
func NewDeviceService(keyring *security.HMACKeyring[security.DeviceHMACKeyPurpose], source clock.Clock) (*DeviceService, error) {
	if keyring == nil || source == nil {
		return nil, ErrInvalidDeviceInput
	}
	return &DeviceService{keyring: keyring, clock: source}, nil
}

// Issue creates a UUIDv7 credential and independent device/CSRF secrets without retaining plaintext.
func (service *DeviceService) Issue(userID uuid.UUID, label string) (IssuedDeviceCredential, error) {
	if service == nil || service.keyring == nil || service.clock == nil || userID == uuid.Nil {
		return IssuedDeviceCredential{}, ErrInvalidDeviceInput
	}
	normalizedLabel, err := normalizeDeviceLabel(label)
	if err != nil {
		return IssuedDeviceCredential{}, err
	}
	credentialID, err := uuid.NewV7()
	if err != nil {
		return IssuedDeviceCredential{}, ErrInvalidDeviceInput
	}
	secret, err := security.RandomBytes(DeviceSecretBytes)
	if err != nil {
		return IssuedDeviceCredential{}, err
	}
	defer clear(secret)
	csrfSecret, err := security.RandomBytes(DeviceCSRFSecretBytes)
	if err != nil {
		return IssuedDeviceCredential{}, err
	}
	defer clear(csrfSecret)

	secretMAC, err := service.sumSecret(credentialID, secret)
	if err != nil || secretMAC.KeyVersion == 0 || secretMAC.KeyVersion > math.MaxInt32 {
		return IssuedDeviceCredential{}, ErrDeviceIntegrity
	}
	now := canonicalDeviceTime(service.clock.Now())
	credential, err := RestoreDeviceCredential(DeviceCredentialSnapshot{
		CredentialID:      credentialID,
		UserID:            userID,
		SecretMAC:         secretMAC,
		CSRFMAC:           sumDeviceCSRF(credentialID, csrfSecret),
		Generation:        1,
		Label:             normalizedLabel,
		CreatedAt:         now,
		LastSeenAt:        now,
		RotatedAt:         now,
		IdleExpiresAt:     now.Add(DeviceIdleTTL),
		AbsoluteExpiresAt: now.Add(DeviceAbsoluteTTL),
	})
	if err != nil {
		return IssuedDeviceCredential{}, err
	}
	return issuedDeviceCredential(credential, secret, csrfSecret)
}

// CredentialIDFromToken performs a bounded first parse for repository selection and erases the decoded secret.
func CredentialIDFromToken(encoded string) (uuid.UUID, error) {
	parsed, err := parseDeviceToken(encoded)
	if err != nil {
		return uuid.Nil, err
	}
	clear(parsed.Secret)
	credentialID, err := uuid.Parse(parsed.Selector)
	if err != nil || credentialID == uuid.Nil || credentialID.String() != parsed.Selector {
		return uuid.Nil, ErrDeviceAuthentication
	}
	return credentialID, nil
}

// Verify authenticates current or in-grace previous secret without establishing an active principal.
func (service *DeviceService) Verify(record DeviceCredential, encoded string) (DeviceVerification, error) {
	if service == nil || service.keyring == nil || service.clock == nil {
		return DeviceVerification{}, ErrDeviceAuthentication
	}
	parsed, err := parseDeviceToken(encoded)
	if err != nil {
		return DeviceVerification{}, err
	}
	defer clear(parsed.Secret)
	snapshot := record.Snapshot()
	if parsed.Selector != snapshot.CredentialID.String() {
		return DeviceVerification{}, ErrDeviceAuthentication
	}
	now := canonicalDeviceTime(service.clock.Now())
	secretKind := DeviceSecretKind(0)
	observedGeneration := snapshot.Generation
	matched, verifyErr := service.verifySecret(snapshot.CredentialID, parsed.Secret, snapshot.SecretMAC)
	if verifyErr != nil {
		return DeviceVerification{}, verifyErr
	}
	if matched {
		secretKind = DeviceSecretCurrent
	} else if snapshot.PreviousSecretMAC != nil && now.Before(snapshot.PreviousSecretValidUntil) {
		matched, verifyErr = service.verifySecret(snapshot.CredentialID, parsed.Secret, *snapshot.PreviousSecretMAC)
		if verifyErr != nil {
			return DeviceVerification{}, verifyErr
		}
		if matched {
			secretKind = DeviceSecretPrevious
			observedGeneration--
		}
	}
	if !matched || secretKind == 0 {
		return DeviceVerification{}, ErrDeviceAuthentication
	}
	return DeviceVerification{
		credentialID:       snapshot.CredentialID,
		userID:             snapshot.UserID,
		observedGeneration: observedGeneration,
		secretKind:         secretKind,
		state:              record.State(now),
		revokeReason:       snapshot.RevokeReason,
		verifiedAt:         now,
	}, nil
}

// Authenticate accepts only active credentials while preserving the verified secret kind for permission checks.
func (service *DeviceService) Authenticate(record DeviceCredential, encoded string) (DeviceAuthorization, error) {
	verification, err := service.Verify(record, encoded)
	if err != nil {
		return DeviceAuthorization{}, err
	}
	if verification.state != DeviceStateActive {
		return DeviceAuthorization{}, ErrDeviceUnavailable
	}
	return DeviceAuthorization{verification: verification}, nil
}

// VerifyCSRF validates the canonical raw Base64URL CSRF secret against the credential-bound digest.
func (service *DeviceService) VerifyCSRF(record DeviceCredential, encoded string) error {
	if service == nil {
		return ErrDeviceAuthentication
	}
	secret, err := parseDeviceCSRF(encoded)
	if err != nil {
		return err
	}
	defer clear(secret)
	snapshot := record.Snapshot()
	if !security.ConstantTimeEqual(sumDeviceCSRF(snapshot.CredentialID, secret), snapshot.CSRFMAC) {
		return ErrDeviceAuthentication
	}
	return nil
}

// Rotate replaces the current secret at the scheduled boundary and retains exactly one previous generation.
func (service *DeviceService) Rotate(
	record DeviceCredential,
	authorization DeviceAuthorization,
	csrfToken string,
) (IssuedDeviceCredential, error) {
	now := canonicalDeviceTime(service.clock.Now())
	if !authorization.AllowsSensitiveMutation(record) {
		return IssuedDeviceCredential{}, ErrDeviceConcurrentTransition
	}
	snapshot := record.Snapshot()
	// A clock behind persisted activity is a concurrency failure, not a not-due scheduling decision.
	if now.Before(snapshot.LastSeenAt) {
		return IssuedDeviceCredential{}, ErrDeviceConcurrentTransition
	}
	if !record.RotationDue(now) {
		return IssuedDeviceCredential{}, ErrDeviceRotationNotDue
	}
	if snapshot.Generation == math.MaxInt64 {
		return IssuedDeviceCredential{}, ErrDeviceIntegrity
	}
	secret, err := security.RandomBytes(DeviceSecretBytes)
	if err != nil {
		return IssuedDeviceCredential{}, err
	}
	defer clear(secret)
	csrfSecret, err := parseDeviceCSRF(csrfToken)
	if err != nil {
		return IssuedDeviceCredential{}, ErrDeviceAuthentication
	}
	defer clear(csrfSecret)
	if !security.ConstantTimeEqual(sumDeviceCSRF(snapshot.CredentialID, csrfSecret), snapshot.CSRFMAC) {
		return IssuedDeviceCredential{}, ErrDeviceAuthentication
	}
	secretMAC, err := service.sumSecret(snapshot.CredentialID, secret)
	if err != nil || secretMAC.KeyVersion == 0 || secretMAC.KeyVersion > math.MaxInt32 {
		return IssuedDeviceCredential{}, ErrDeviceIntegrity
	}
	previousMAC := cloneDeviceMAC(snapshot.SecretMAC)
	snapshot.PreviousSecretMAC = &previousMAC
	snapshot.PreviousSecretValidUntil = earlierDeviceTime(now.Add(DevicePreviousSecretGrace), snapshot.AbsoluteExpiresAt)
	snapshot.SecretMAC = secretMAC
	snapshot.Generation++
	snapshot.LastSeenAt = now
	snapshot.RotatedAt = now
	snapshot.IdleExpiresAt = earlierDeviceTime(now.Add(DeviceIdleTTL), snapshot.AbsoluteExpiresAt)
	rotated, err := RestoreDeviceCredential(snapshot)
	if err != nil {
		return IssuedDeviceCredential{}, err
	}
	return issuedDeviceCredential(rotated, secret, csrfSecret)
}

// RestoreDeviceCredential validates database state before authentication decisions can consume it.
func RestoreDeviceCredential(snapshot DeviceCredentialSnapshot) (DeviceCredential, error) {
	snapshot = cloneDeviceSnapshot(snapshot)
	snapshot.CreatedAt = canonicalDeviceTime(snapshot.CreatedAt)
	snapshot.LastSeenAt = canonicalDeviceTime(snapshot.LastSeenAt)
	snapshot.RotatedAt = canonicalDeviceTime(snapshot.RotatedAt)
	snapshot.IdleExpiresAt = canonicalDeviceTime(snapshot.IdleExpiresAt)
	snapshot.AbsoluteExpiresAt = canonicalDeviceTime(snapshot.AbsoluteExpiresAt)
	snapshot.PreviousSecretValidUntil = canonicalDeviceOptionalTime(snapshot.PreviousSecretValidUntil)
	snapshot.RevokedAt = canonicalDeviceOptionalTime(snapshot.RevokedAt)

	label, err := normalizeDeviceLabel(snapshot.Label)
	if err != nil || label != snapshot.Label || snapshot.CredentialID == uuid.Nil || snapshot.UserID == uuid.Nil ||
		snapshot.SecretMAC.KeyVersion == 0 || snapshot.SecretMAC.KeyVersion > math.MaxInt32 || len(snapshot.SecretMAC.Value) != sha256.Size ||
		len(snapshot.CSRFMAC) != sha256.Size || snapshot.Generation == 0 || snapshot.Generation > math.MaxInt64 ||
		snapshot.CreatedAt.IsZero() || snapshot.LastSeenAt.Before(snapshot.CreatedAt) || snapshot.RotatedAt.Before(snapshot.CreatedAt) ||
		snapshot.LastSeenAt.Before(snapshot.RotatedAt) || !snapshot.CreatedAt.Before(snapshot.AbsoluteExpiresAt) ||
		!snapshot.RotatedAt.Before(snapshot.AbsoluteExpiresAt) || !snapshot.LastSeenAt.Before(snapshot.IdleExpiresAt) ||
		!snapshot.LastSeenAt.Before(snapshot.AbsoluteExpiresAt) || !snapshot.AbsoluteExpiresAt.Equal(snapshot.CreatedAt.Add(DeviceAbsoluteTTL)) ||
		!snapshot.IdleExpiresAt.Equal(earlierDeviceTime(snapshot.LastSeenAt.Add(DeviceIdleTTL), snapshot.AbsoluteExpiresAt)) {
		return DeviceCredential{}, ErrInvalidDeviceInput
	}
	if snapshot.PreviousSecretMAC == nil {
		if !snapshot.PreviousSecretValidUntil.IsZero() {
			return DeviceCredential{}, ErrInvalidDeviceInput
		}
	} else if snapshot.Generation < 2 || snapshot.PreviousSecretMAC.KeyVersion == 0 ||
		snapshot.PreviousSecretMAC.KeyVersion > math.MaxInt32 || len(snapshot.PreviousSecretMAC.Value) != sha256.Size ||
		!snapshot.PreviousSecretValidUntil.Equal(earlierDeviceTime(snapshot.RotatedAt.Add(DevicePreviousSecretGrace), snapshot.AbsoluteExpiresAt)) {
		return DeviceCredential{}, ErrInvalidDeviceInput
	}
	if (snapshot.RevokedAt.IsZero() != (snapshot.RevokeReason == "")) ||
		(!snapshot.RevokedAt.IsZero() && (snapshot.RevokedAt.Before(snapshot.LastSeenAt) || !validDeviceRevokeReason(snapshot.RevokeReason))) {
		return DeviceCredential{}, ErrInvalidDeviceInput
	}
	return DeviceCredential{snapshot: snapshot}, nil
}

// Snapshot returns a deep copy so adapters cannot mutate verified MAC bytes.
func (credential DeviceCredential) Snapshot() DeviceCredentialSnapshot {
	return cloneDeviceSnapshot(credential.snapshot)
}

// State applies half-open expiry boundaries: equality is expired.
func (credential DeviceCredential) State(now time.Time) DeviceState {
	snapshot := credential.snapshot
	now = canonicalDeviceTime(now)
	if now.IsZero() || now.Before(snapshot.CreatedAt) || now.Before(snapshot.LastSeenAt) {
		return DeviceStateExpired
	}
	if !snapshot.RevokedAt.IsZero() {
		if now.Before(snapshot.RevokedAt) {
			return DeviceStateExpired
		}
		return DeviceStateRevoked
	}
	if !now.Before(snapshot.IdleExpiresAt) || !now.Before(snapshot.AbsoluteExpiresAt) {
		return DeviceStateExpired
	}
	return DeviceStateActive
}

// RotationDue reports the scheduled boundary only for a currently active credential.
func (credential DeviceCredential) RotationDue(now time.Time) bool {
	now = canonicalDeviceTime(now)
	return credential.State(now) == DeviceStateActive && !now.Before(credential.snapshot.RotatedAt.Add(DeviceRotationInterval))
}

// Touch advances activity without changing generation and caps idle expiry at the absolute deadline.
func (credential DeviceCredential) Touch(authorization DeviceAuthorization, at time.Time) (DeviceCredential, error) {
	at = canonicalDeviceTime(at)
	if !authorization.AllowsSensitiveMutation(credential) || credential.State(at) != DeviceStateActive ||
		at.Before(credential.snapshot.LastSeenAt) {
		return DeviceCredential{}, ErrDeviceConcurrentTransition
	}
	if at.Equal(credential.snapshot.LastSeenAt) {
		return credential, nil
	}
	snapshot := credential.Snapshot()
	snapshot.LastSeenAt = at
	snapshot.IdleExpiresAt = earlierDeviceTime(at.Add(DeviceIdleTTL), snapshot.AbsoluteExpiresAt)
	return RestoreDeviceCredential(snapshot)
}

// Revoke increments generation so all previously obtained write authority becomes stale immediately.
func (credential DeviceCredential) Revoke(reason DeviceRevokeReason, at time.Time) (DeviceCredential, error) {
	at = canonicalDeviceTime(at)
	if credential.State(at) != DeviceStateActive || at.Before(credential.snapshot.LastSeenAt) ||
		!validDeviceRevokeReason(reason) || credential.snapshot.Generation == math.MaxInt64 {
		return DeviceCredential{}, ErrDeviceConcurrentTransition
	}
	snapshot := credential.Snapshot()
	snapshot.Generation++
	snapshot.RevokedAt = at
	snapshot.RevokeReason = reason
	return RestoreDeviceCredential(snapshot)
}

// SecretKind identifies current versus previous matching material without exposing it.
func (verification DeviceVerification) SecretKind() DeviceSecretKind {
	return verification.secretKind
}

// AccountInstruction exposes only account suspension/deletion after successful revoked-secret verification.
func (verification DeviceVerification) AccountInstruction() AccountInstruction {
	if verification.state != DeviceStateRevoked {
		return AccountInstructionNone
	}
	switch verification.revokeReason {
	case DeviceRevokeAccountSuspended:
		return AccountInstructionSuspended
	case DeviceRevokeAccountDeleted:
		return AccountInstructionDeleted
	default:
		return AccountInstructionNone
	}
}

// SecretKind reports whether active authorization came from current or previous material.
func (authorization DeviceAuthorization) SecretKind() DeviceSecretKind {
	return authorization.verification.secretKind
}

// AllowsCookieWrite requires current-secret authority over the exact latest generation.
func (authorization DeviceAuthorization) AllowsCookieWrite(current DeviceCredential) bool {
	return authorization.authorizesCurrent(current)
}

// AllowsSensitiveMutation rejects previous secrets and stale generations.
func (authorization DeviceAuthorization) AllowsSensitiveMutation(current DeviceCredential) bool {
	return authorization.authorizesCurrent(current)
}

// authorizesRotationReplay binds a challenge-authorized rotation envelope to its current or immediately previous generation.
func (authorization DeviceAuthorization) authorizesRotationReplay(
	current DeviceCredential,
	resultGeneration uint64,
) bool {
	if resultGeneration != current.snapshot.Generation {
		return false
	}
	if authorization.authorizesCurrent(current) {
		return true
	}
	verification := authorization.verification
	snapshot := current.snapshot
	return verification.secretKind == DeviceSecretPrevious && verification.state == DeviceStateActive &&
		verification.credentialID == snapshot.CredentialID && verification.userID == snapshot.UserID &&
		verification.observedGeneration+1 == snapshot.Generation && current.State(verification.verifiedAt) == DeviceStateActive
}

// cookieWrite mints transport authority only after plaintext secrets verify against the persisted row.
func (service *DeviceService) cookieWrite(current DeviceCredential, secrets issuedDeviceSecrets) (DeviceCookieWrite, error) {
	if secrets.generation == 0 || secrets.generation != current.snapshot.Generation {
		return DeviceCookieWrite{}, ErrDeviceConcurrentTransition
	}
	authorization, err := service.Authenticate(current, secrets.token)
	if err != nil || !authorization.AllowsCookieWrite(current) {
		return DeviceCookieWrite{}, ErrDeviceConcurrentTransition
	}
	if err := service.VerifyCSRF(current, secrets.csrfToken); err != nil {
		return DeviceCookieWrite{}, ErrDeviceConcurrentTransition
	}
	return DeviceCookieWrite{
		token: secrets.token, csrfToken: secrets.csrfToken, generation: secrets.generation,
	}, nil
}

func (service *DeviceService) resultCapability(
	authorization DeviceAuthorization,
	current DeviceCredential,
	resultID uuid.UUID,
	resultExpiresAt time.Time,
) (secretaccess.DeviceGrant, error) {
	if resultID == uuid.Nil || !authorization.AllowsSensitiveMutation(current) {
		return secretaccess.DeviceGrant{}, ErrDeviceAuthentication
	}
	resultExpiresAt = canonicalDeviceTime(resultExpiresAt)
	validUntil := earlierDeviceTime(resultExpiresAt, current.snapshot.IdleExpiresAt)
	validUntil = earlierDeviceTime(validUntil, current.snapshot.AbsoluteExpiresAt)
	if !validUntil.After(authorization.verification.verifiedAt) {
		return secretaccess.DeviceGrant{}, ErrDeviceUnavailable
	}
	grant, err := secretaccess.MintDeviceGrant(
		service.keyring, current.snapshot.CredentialID, current.snapshot.Generation,
		authorization.verification.userID, resultID, validUntil,
	)
	if err != nil {
		return secretaccess.DeviceGrant{}, ErrDeviceIntegrity
	}
	return grant, nil
}

func (service *DeviceService) resultContinuationCapability(
	authorization DeviceAuthorization,
	current DeviceCredential,
	resultID uuid.UUID,
	resultCompletedAt, resultExpiresAt time.Time,
) (secretaccess.DeviceGrant, error) {
	if authorization.SecretKind() == DeviceSecretCurrent {
		return service.resultCapability(authorization, current, resultID, resultExpiresAt)
	}
	verification := authorization.verification
	snapshot := current.snapshot
	resultCompletedAt = canonicalDeviceTime(resultCompletedAt)
	resultExpiresAt = canonicalDeviceTime(resultExpiresAt)
	if verification.secretKind != DeviceSecretPrevious || verification.state != DeviceStateActive ||
		verification.credentialID != snapshot.CredentialID || verification.userID != snapshot.UserID ||
		verification.observedGeneration+1 != snapshot.Generation || snapshot.PreviousSecretMAC == nil ||
		resultID == uuid.Nil || resultCompletedAt.IsZero() || !resultCompletedAt.Before(snapshot.RotatedAt) {
		return secretaccess.DeviceGrant{}, ErrDeviceAuthentication
	}
	validUntil := earlierDeviceTime(resultExpiresAt, snapshot.PreviousSecretValidUntil)
	validUntil = earlierDeviceTime(validUntil, snapshot.IdleExpiresAt)
	validUntil = earlierDeviceTime(validUntil, snapshot.AbsoluteExpiresAt)
	if !validUntil.After(verification.verifiedAt) {
		return secretaccess.DeviceGrant{}, ErrDeviceUnavailable
	}
	grant, err := secretaccess.MintDeviceGrant(
		service.keyring, snapshot.CredentialID, snapshot.Generation, snapshot.UserID, resultID, validUntil,
	)
	if err != nil {
		return secretaccess.DeviceGrant{}, ErrDeviceIntegrity
	}
	return grant, nil
}

func (authorization DeviceAuthorization) authorizesCurrent(current DeviceCredential) bool {
	verification := authorization.verification
	snapshot := current.snapshot
	return verification.secretKind == DeviceSecretCurrent && verification.state == DeviceStateActive &&
		verification.credentialID == snapshot.CredentialID && verification.userID == snapshot.UserID &&
		verification.observedGeneration == snapshot.Generation && current.State(verification.verifiedAt) == DeviceStateActive
}

func (service *DeviceService) sumSecret(credentialID uuid.UUID, secret []byte) (security.MAC[security.DeviceHMACKeyPurpose], error) {
	value := make([]byte, 0, len(deviceSecretDomain)+36+1+len(secret))
	value = append(value, deviceSecretDomain...)
	value = append(value, credentialID.String()...)
	value = append(value, 0)
	value = append(value, secret...)
	defer clear(value)
	return service.keyring.Sum(value)
}

func (service *DeviceService) verifySecret(credentialID uuid.UUID, secret []byte, expected security.MAC[security.DeviceHMACKeyPurpose]) (bool, error) {
	if expected.KeyVersion == 0 || expected.KeyVersion > math.MaxInt32 || len(expected.Value) != sha256.Size {
		return false, ErrDeviceIntegrity
	}
	value := make([]byte, 0, len(deviceSecretDomain)+36+1+len(secret))
	value = append(value, deviceSecretDomain...)
	value = append(value, credentialID.String()...)
	value = append(value, 0)
	value = append(value, secret...)
	defer clear(value)
	matched, err := service.keyring.Verify(value, expected)
	if err != nil {
		return false, ErrDeviceIntegrity
	}
	return matched, nil
}

func issuedDeviceCredential(credential DeviceCredential, secret, csrfSecret []byte) (IssuedDeviceCredential, error) {
	snapshot := credential.Snapshot()
	token, err := security.FormatToken(DeviceTokenVersion, snapshot.CredentialID.String(), secret)
	if err != nil {
		return IssuedDeviceCredential{}, ErrInvalidDeviceInput
	}
	return IssuedDeviceCredential{
		Credential: credential,
		secrets: issuedDeviceSecrets{
			token: token, csrfToken: base64.RawURLEncoding.EncodeToString(csrfSecret), generation: snapshot.Generation,
		},
	}, nil
}

func parseDeviceToken(encoded string) (security.ParsedToken, error) {
	parsed, err := security.ParseToken(encoded, security.TokenPolicy{
		Version: DeviceTokenVersion, MinSecretBytes: DeviceSecretBytes, MaxSecretBytes: DeviceSecretBytes,
	})
	if err != nil {
		return security.ParsedToken{}, ErrDeviceAuthentication
	}
	credentialID, err := uuid.Parse(parsed.Selector)
	if err != nil || credentialID == uuid.Nil || credentialID.String() != parsed.Selector {
		clear(parsed.Secret)
		return security.ParsedToken{}, ErrDeviceAuthentication
	}
	return parsed, nil
}

func parseDeviceCSRF(encoded string) ([]byte, error) {
	if len(encoded) == 0 || len(encoded) > 128 {
		return nil, ErrDeviceAuthentication
	}
	secret, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || len(secret) != DeviceCSRFSecretBytes || base64.RawURLEncoding.EncodeToString(secret) != encoded {
		clear(secret)
		return nil, ErrDeviceAuthentication
	}
	return secret, nil
}

func sumDeviceCSRF(credentialID uuid.UUID, secret []byte) []byte {
	digest := sha256.New()
	_, _ = digest.Write([]byte(deviceCSRFDigestDomain))
	_, _ = digest.Write([]byte(credentialID.String()))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(secret)
	return digest.Sum(nil)
}

func normalizeDeviceLabel(input string) (string, error) {
	if len(input) == 0 || len(input) > maximumDeviceLabelInputBytes || !utf8.ValidString(input) {
		return "", ErrInvalidDeviceInput
	}
	for _, character := range input {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return "", ErrInvalidDeviceInput
		}
	}
	normalized := strings.TrimSpace(norm.NFKC.String(input))
	count := utf8.RuneCountInString(normalized)
	if count == 0 || count > MaximumDeviceLabelCodePoints {
		return "", ErrInvalidDeviceInput
	}
	return normalized, nil
}

func validDeviceRevokeReason(reason DeviceRevokeReason) bool {
	switch reason {
	case DeviceRevokeUserRequested, DeviceRevokeAdminRequested, DeviceRevokeRecovery,
		DeviceRevokeOnboardingExpiry, DeviceRevokeAccountSuspended, DeviceRevokeAccountDeleted:
		return true
	default:
		return false
	}
}

func cloneDeviceSnapshot(snapshot DeviceCredentialSnapshot) DeviceCredentialSnapshot {
	snapshot.SecretMAC = cloneDeviceMAC(snapshot.SecretMAC)
	if snapshot.PreviousSecretMAC != nil {
		previous := cloneDeviceMAC(*snapshot.PreviousSecretMAC)
		snapshot.PreviousSecretMAC = &previous
	}
	snapshot.CSRFMAC = bytes.Clone(snapshot.CSRFMAC)
	return snapshot
}

func cloneDeviceMAC(value security.MAC[security.DeviceHMACKeyPurpose]) security.MAC[security.DeviceHMACKeyPurpose] {
	return security.MAC[security.DeviceHMACKeyPurpose]{KeyVersion: value.KeyVersion, Value: bytes.Clone(value.Value)}
}

func earlierDeviceTime(left, right time.Time) time.Time {
	if left.Before(right) {
		return canonicalDeviceTime(left)
	}
	return canonicalDeviceTime(right)
}

func canonicalDeviceTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC().Truncate(time.Microsecond)
}

func canonicalDeviceOptionalTime(value time.Time) time.Time {
	return canonicalDeviceTime(value)
}
