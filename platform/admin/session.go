package admin

import (
	"crypto/subtle"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/secretaccess"
	"github.com/iFTY-R/game-night/platform/security"
)

const (
	adminSessionTokenVersion  = "v1"
	adminSessionSelectorBytes = 16
	adminSessionSecretBytes   = 32
	adminSessionCSRFBytes     = 32
	// AdminSetupSessionTTL caps the initial-password-change window so stale bootstrap credentials cannot linger.
	AdminSetupSessionTTL = 10 * time.Minute
	// AdminMFASessionTTL caps the second-factor verification window to a short-lived challenge session.
	AdminMFASessionTTL = 5 * time.Minute
	// AdminFullSessionIdleTTL is the rolling inactivity limit for a fully authenticated admin browser session.
	AdminFullSessionIdleTTL = 30 * time.Minute
	// AdminFullSessionAbsoluteTTL is the hard maximum age for a full admin browser session.
	AdminFullSessionAbsoluteTTL = 12 * time.Hour
)

// SessionClientMetadata keeps the normalized client hints later persistence needs for session management UX.
type SessionClientMetadata struct {
	ClientIP  string
	UserAgent string
}

// SessionPolicy keeps short-lived setup and MFA sessions distinct from the long-lived full session.
func SessionPolicy(kind SessionKind) (idleTTL, absoluteTTL time.Duration, err error) {
	switch kind {
	case SessionKindSetupPasswordPending:
		return AdminSetupSessionTTL, AdminSetupSessionTTL, nil
	case SessionKindMFAPending:
		return AdminMFASessionTTL, AdminMFASessionTTL, nil
	case SessionKindFull:
		return AdminFullSessionIdleTTL, AdminFullSessionAbsoluteTTL, nil
	default:
		return 0, 0, ErrInvalidInput
	}
}

// PasswordLoginSessionKind derives the post-password session kind from durable account state and active enrollment.
func PasswordLoginSessionKind(status AccountStatus, hasActiveEnrollment bool) (SessionKind, error) {
	switch status {
	case AccountStatusSetupRequired:
		return SessionKindSetupPasswordPending, nil
	case AccountStatusActive:
		if hasActiveEnrollment {
			return SessionKindMFAPending, nil
		}
		return SessionKindFull, nil
	case AccountStatusBootstrapPending:
		return "", ErrUnavailable
	default:
		return "", ErrInvalidInput
	}
}

// IssuedSession contains the database aggregate and bearer values that must be delivered separately.
type IssuedSession struct {
	Session   Session
	Token     string
	CSRFToken string
}

// SessionService owns bearer generation and proof verification for administrator sessions.
type SessionService struct {
	keyring *security.HMACKeyring[security.AdminSessionKeyPurpose]
	clock   interface{ Now() time.Time }
}

// NewSessionService creates the bearer-token helper used by the admin auth flows.
func NewSessionService(keyring *security.HMACKeyring[security.AdminSessionKeyPurpose], source interface{ Now() time.Time }) (*SessionService, error) {
	if keyring == nil || source == nil {
		return nil, ErrInvalidInput
	}
	return &SessionService{keyring: keyring, clock: source}, nil
}

// Issue keeps backward compatibility for legacy callers while new persistence starts storing client metadata.
func (service *SessionService) Issue(adminID uuid.UUID, kind SessionKind, adminVersion, passwordVersion int64, at time.Time) (IssuedSession, error) {
	return service.IssueWithClient(adminID, kind, adminVersion, passwordVersion, SessionClientMetadata{}, at)
}

// IssueWithClient creates a session bound to current account generations and normalized client metadata.
func (service *SessionService) IssueWithClient(adminID uuid.UUID, kind SessionKind, adminVersion, passwordVersion int64, metadata SessionClientMetadata, at time.Time) (IssuedSession, error) {
	if service == nil || service.keyring == nil || adminID == uuid.Nil || !kind.Valid() || adminVersion <= 0 || passwordVersion < 0 {
		return IssuedSession{}, ErrInvalidInput
	}
	id, err := uuid.NewV7()
	if err != nil {
		return IssuedSession{}, err
	}
	selectorEntropy, err := security.RandomBytes(adminSessionSelectorBytes)
	if err != nil {
		return IssuedSession{}, err
	}
	defer clearSessionBytes(selectorEntropy)
	selector, err := identifier.NewSelector(selectorEntropy)
	if err != nil {
		return IssuedSession{}, ErrInvalidInput
	}
	secret, err := security.RandomBytes(adminSessionSecretBytes)
	if err != nil {
		return IssuedSession{}, err
	}
	defer clearSessionBytes(secret)
	csrf, err := security.RandomBytes(adminSessionCSRFBytes)
	if err != nil {
		return IssuedSession{}, err
	}
	defer clearSessionBytes(csrf)
	secretMAC, err := service.keyring.Sum(secret)
	if err != nil {
		return IssuedSession{}, err
	}
	csrfMAC, err := service.keyring.Sum(csrf)
	if err != nil {
		return IssuedSession{}, err
	}
	idleTTL, absoluteTTL, err := SessionPolicy(kind)
	if err != nil {
		return IssuedSession{}, err
	}
	metadata = normalizeSessionClientMetadata(metadata)
	at = canonicalAdminTime(at)
	session, err := RestoreSession(SessionSnapshot{
		ID:                id,
		AdminID:           adminID,
		Selector:          selector.Value(),
		SecretMAC:         secretMAC,
		CSRFHash:          csrfMAC,
		Kind:              kind,
		AdminVersion:      adminVersion,
		PasswordVersion:   passwordVersion,
		SessionVersion:    1,
		ClientIP:          metadata.ClientIP,
		UserAgent:         metadata.UserAgent,
		MaxAttempts:       5,
		CreatedAt:         at,
		LastSeenAt:        at,
		IdleExpiresAt:     at.Add(idleTTL),
		AbsoluteExpiresAt: at.Add(absoluteTTL),
	})
	if err != nil {
		return IssuedSession{}, err
	}
	token, err := security.FormatToken(adminSessionTokenVersion, selector.Value(), secret)
	if err != nil {
		return IssuedSession{}, err
	}
	csrfToken, err := security.FormatToken(adminSessionTokenVersion, selector.Value(), csrf)
	if err != nil {
		return IssuedSession{}, err
	}
	return IssuedSession{Session: session, Token: token, CSRFToken: csrfToken}, nil
}

// Authenticate verifies token selector, HMAC, generations, expiry, and CSRF in constant time.
func (service *SessionService) Authenticate(session Session, token, csrfToken string, at time.Time) error {
	if service == nil || service.keyring == nil {
		return ErrAuthentication
	}
	snapshot := session.Snapshot()
	selector, secret, err := parseSessionToken(token)
	if err != nil || selector != snapshot.Selector {
		clearSessionBytes(secret)
		return ErrAuthentication
	}
	defer clearSessionBytes(secret)
	matched, err := service.keyring.Verify(secret, snapshot.SecretMAC)
	if err != nil || !matched {
		return ErrAuthentication
	}
	csrfSelector, csrf, err := parseSessionToken(csrfToken)
	if err != nil || csrfSelector != snapshot.Selector {
		clearSessionBytes(csrf)
		return ErrAuthentication
	}
	defer clearSessionBytes(csrf)
	csrfMatched, err := service.keyring.Verify(csrf, snapshot.CSRFHash)
	if err != nil || !csrfMatched || subtle.ConstantTimeCompare([]byte(csrfSelector), []byte(selector)) != 1 {
		return ErrAuthentication
	}
	if snapshot.RevokedAt.IsZero() == false {
		return ErrSessionRevoked
	}
	if !session.Active(at) {
		return ErrSessionExpired
	}
	return nil
}

// ResultGrant converts an already authenticated live session into exact envelope authority.
func (service *SessionService) ResultGrant(session Session, resultID uuid.UUID, at time.Time) (secretaccess.AdminGrant, error) {
	if service == nil || service.keyring == nil || resultID == uuid.Nil || !session.Active(at) {
		return secretaccess.AdminGrant{}, ErrAuthentication
	}
	snapshot := session.Snapshot()
	validUntil := snapshot.IdleExpiresAt
	if snapshot.AbsoluteExpiresAt.Before(validUntil) {
		validUntil = snapshot.AbsoluteExpiresAt
	}
	return secretaccess.MintAdminGrant(service.keyring, snapshot.ID, snapshot.AdminID, resultID, validUntil)
}

func normalizeSessionClientMetadata(metadata SessionClientMetadata) SessionClientMetadata {
	metadata.ClientIP = strings.TrimSpace(metadata.ClientIP)
	metadata.UserAgent = strings.TrimSpace(metadata.UserAgent)
	return metadata
}

func parseSessionToken(encoded string) (string, []byte, error) {
	parsed, err := security.ParseToken(encoded, security.TokenPolicy{Version: adminSessionTokenVersion, MinSecretBytes: adminSessionSecretBytes, MaxSecretBytes: adminSessionSecretBytes})
	if err != nil {
		return "", nil, ErrAuthentication
	}
	selector, err := identifier.ParseSelector(parsed.Selector)
	if err != nil || selector.ByteLength() != adminSessionSelectorBytes {
		clearSessionBytes(parsed.Secret)
		return "", nil, ErrAuthentication
	}
	return selector.Value(), parsed.Secret, nil
}

func clearSessionBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func normalizeReason(reason string) string { return strings.TrimSpace(reason) }
