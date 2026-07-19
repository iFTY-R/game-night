// Package cookies owns the isolated browser credential cookies used by the identity and administrator APIs.
package cookies

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/identity"
)

const (
	// UserDeviceCookieName stores the long-lived user device bearer token.
	UserDeviceCookieName = "__Host-gn_device"
	// UserCSRFCookieName stores the browser-readable user CSRF proof.
	UserCSRFCookieName = "__Host-gn_csrf"
	// UserChallengeCookieName stores the short-lived user login-CSRF challenge token.
	UserChallengeCookieName = "__Host-gn_user_challenge"
	// AdminSessionCookieName stores administrator setup, MFA, recovery, or full-session bearer tokens.
	AdminSessionCookieName = "__Host-gn_admin"
	// AdminCSRFCookieName stores the browser-readable administrator CSRF proof.
	AdminCSRFCookieName = "__Host-gn_admin_csrf"
	// AdminChallengeCookieName stores the short-lived administrator login-CSRF challenge token.
	AdminChallengeCookieName = "__Host-gn_admin_challenge"

	// maximumCredentialBytes stays below common per-cookie limits and bounds request-header retention.
	maximumCredentialBytes = 4096
	// maximumCookieLifetime matches the modern browser upper bound while covering the 365-day device lifetime.
	maximumCookieLifetime = 400 * 24 * time.Hour
)

var (
	// ErrInvalidInput rejects nil dependencies, expired authority, and malformed credential metadata.
	ErrInvalidInput = errors.New("invalid cookie transport input")
	// ErrCredentialUnavailable deliberately merges missing, duplicate, and malformed Cookie values.
	ErrCredentialUnavailable = errors.New("browser credential unavailable")
)

var (
	// Definitions are immutable policy records shared by issuance, clearing, and request readers.
	userDeviceDefinition     = cookieDefinition{name: UserDeviceCookieName, httpOnly: true, sameSite: http.SameSiteLaxMode}
	userCSRFDefinition       = cookieDefinition{name: UserCSRFCookieName, sameSite: http.SameSiteLaxMode}
	userChallengeDefinition  = cookieDefinition{name: UserChallengeCookieName, httpOnly: true, sameSite: http.SameSiteLaxMode}
	adminSessionDefinition   = cookieDefinition{name: AdminSessionCookieName, httpOnly: true, sameSite: http.SameSiteStrictMode}
	adminCSRFDefinition      = cookieDefinition{name: AdminCSRFCookieName, sameSite: http.SameSiteStrictMode}
	adminChallengeDefinition = cookieDefinition{name: AdminChallengeCookieName, httpOnly: true, sameSite: http.SameSiteStrictMode}
)

// cookieDefinition contains only attributes that legitimately differ across credential namespaces.
type cookieDefinition struct {
	name     string
	httpOnly bool
	sameSite http.SameSite
}

// HeaderWriter is the narrow response boundary required to append Set-Cookie headers.
// Both Connect responses and standard HTTP response writers satisfy this contract.
type HeaderWriter interface {
	Add(string, string)
}

// Manager applies one injected clock to Expires and Max-Age calculations without allowing Secure to be disabled.
type Manager struct {
	clock clock.Clock
}

// NewManager validates transport wiring before any handler can read or write browser credentials.
func NewManager(source clock.Clock) (*Manager, error) {
	if source == nil {
		return nil, ErrInvalidInput
	}
	return &Manager{clock: source}, nil
}

// SetUserDevice installs bearer and CSRF Cookies only for the exact persisted device generation authorized by identity.
func (manager *Manager) SetUserDevice(writer HeaderWriter, credential identity.DeviceCredential, authority identity.DeviceCookieWrite) error {
	snapshot := credential.Snapshot()
	if snapshot.CredentialID == uuid.Nil ||
		authority.Generation() == 0 || authority.Generation() != snapshot.Generation {
		return ErrInvalidInput
	}
	return manager.writePair(
		writer,
		userDeviceDefinition, authority.Token(),
		userCSRFDefinition, authority.CSRFToken(),
		snapshot.IdleExpiresAt,
	)
}

// ClearUserDevice removes both user authentication Cookies using the same scope and policy as issuance.
func (manager *Manager) ClearUserDevice(writer HeaderWriter) error {
	return manager.clear(writer, userDeviceDefinition, userCSRFDefinition)
}

// SetUserChallenge installs only the HttpOnly user challenge Cookie until the authoritative challenge expiry.
func (manager *Manager) SetUserChallenge(writer HeaderWriter, issued identity.IssuedChallenge) error {
	return manager.writeSingle(writer, userChallengeDefinition, issued.Credentials.CookieToken, issued.Challenge.Snapshot().ExpiresAt)
}

// ClearUserChallenge removes the user challenge without touching administrator challenge state.
func (manager *Manager) ClearUserChallenge(writer HeaderWriter) error {
	return manager.clear(writer, userChallengeDefinition)
}

// SetAdminSession installs the isolated administrator bearer and CSRF Cookies through the strict SameSite policy.
func (manager *Manager) SetAdminSession(writer HeaderWriter, issued admin.IssuedSession) error {
	snapshot := issued.Session.Snapshot()
	if snapshot.ID == uuid.Nil {
		return ErrInvalidInput
	}
	return manager.writePair(
		writer,
		adminSessionDefinition, issued.Token,
		adminCSRFDefinition, issued.CSRFToken,
		snapshot.IdleExpiresAt,
	)
}

// ClearAdminSession removes both administrator authentication Cookies without changing user credentials.
func (manager *Manager) ClearAdminSession(writer HeaderWriter) error {
	return manager.clear(writer, adminSessionDefinition, adminCSRFDefinition)
}

// SetAdminChallenge installs only the HttpOnly administrator challenge Cookie under the strict policy.
func (manager *Manager) SetAdminChallenge(writer HeaderWriter, issued admin.IssuedChallenge) error {
	return manager.writeSingle(writer, adminChallengeDefinition, issued.Credentials.CookieToken, issued.Challenge.Snapshot().ExpiresAt)
}

// ClearAdminChallenge removes the administrator challenge without changing user challenge state.
func (manager *Manager) ClearAdminChallenge(writer HeaderWriter) error {
	return manager.clear(writer, adminChallengeDefinition)
}

// writePair validates and encodes both values before appending either header, preventing partial credential updates.
func (manager *Manager) writePair(
	writer HeaderWriter,
	firstDefinition cookieDefinition,
	firstValue string,
	secondDefinition cookieDefinition,
	secondValue string,
	expiresAt time.Time,
) error {
	if manager == nil || manager.clock == nil || writer == nil {
		return ErrInvalidInput
	}
	now := manager.clock.Now()
	first, err := newCredentialCookie(firstDefinition, firstValue, now, expiresAt)
	if err != nil {
		return err
	}
	second, err := newCredentialCookie(secondDefinition, secondValue, now, expiresAt)
	if err != nil {
		return err
	}
	return appendCookies(writer, first, second)
}

// writeSingle installs one credential using the domain-owned expiry and the Manager's current clock reading.
func (manager *Manager) writeSingle(writer HeaderWriter, definition cookieDefinition, value string, expiresAt time.Time) error {
	if manager == nil || manager.clock == nil || writer == nil {
		return ErrInvalidInput
	}
	cookie, err := newCredentialCookie(definition, value, manager.clock.Now(), expiresAt)
	if err != nil {
		return err
	}
	return appendCookies(writer, cookie)
}

// clear expires every supplied namespace through the same atomic header-append path used for paired issuance.
func (manager *Manager) clear(writer HeaderWriter, definitions ...cookieDefinition) error {
	if manager == nil || manager.clock == nil || writer == nil || len(definitions) == 0 {
		return ErrInvalidInput
	}
	cookies := make([]*http.Cookie, len(definitions))
	for index, definition := range definitions {
		cookies[index] = expiredCookie(definition)
	}
	return appendCookies(writer, cookies...)
}

// newCredentialCookie converts an authoritative expiry to browser Max-Age without shortening fractional seconds.
func newCredentialCookie(definition cookieDefinition, value string, now, expiresAt time.Time) (*http.Cookie, error) {
	now, expiresAt = now.UTC(), expiresAt.UTC()
	if !validDefinition(definition) || !validCredentialValue(value) || now.IsZero() || !expiresAt.After(now) {
		return nil, ErrInvalidInput
	}
	remaining := expiresAt.Sub(now)
	if remaining <= 0 || remaining > maximumCookieLifetime {
		return nil, ErrInvalidInput
	}
	seconds := remaining / time.Second
	if remaining%time.Second != 0 {
		seconds++
	}
	if seconds <= 0 || seconds > time.Duration(math.MaxInt32) {
		return nil, ErrInvalidInput
	}
	return &http.Cookie{
		Name: definition.name, Value: value, Path: "/", Expires: expiresAt,
		MaxAge: int(seconds), Secure: true, HttpOnly: definition.httpOnly, SameSite: definition.sameSite,
	}, nil
}

// expiredCookie preserves namespace policy while instructing browsers to delete the credential immediately.
func expiredCookie(definition cookieDefinition) *http.Cookie {
	return &http.Cookie{
		Name: definition.name, Value: "", Path: "/", Expires: time.Unix(0, 0).UTC(),
		MaxAge: -1, Secure: true, HttpOnly: definition.httpOnly, SameSite: definition.sameSite,
	}
}

// appendCookies validates and serializes the full batch before mutating response headers.
func appendCookies(writer HeaderWriter, cookies ...*http.Cookie) error {
	if writer == nil || len(cookies) == 0 {
		return ErrInvalidInput
	}
	// Test and alternate response adapters may expose their backing map for a nil-capability check.
	if headerProvider, ok := writer.(interface{ Header() http.Header }); ok && headerProvider.Header() == nil {
		return ErrInvalidInput
	}
	encoded := make([]string, len(cookies))
	for index, cookie := range cookies {
		if cookie == nil || cookie.Domain != "" || !cookie.Secure || cookie.Path != "/" || cookie.SameSite == http.SameSiteDefaultMode {
			return ErrInvalidInput
		}
		encoded[index] = cookie.String()
		if encoded[index] == "" {
			return ErrInvalidInput
		}
	}
	for _, value := range encoded {
		writer.Add("Set-Cookie", value)
	}
	return nil
}

// validDefinition enforces the host-only prefix and the two reviewed SameSite policies.
func validDefinition(definition cookieDefinition) bool {
	return strings.HasPrefix(definition.name, "__Host-") &&
		(definition.sameSite == http.SameSiteLaxMode || definition.sameSite == http.SameSiteStrictMode)
}

// validCredentialValue bounds storage and rejects bytes that Cookie serialization could quote, drop, or split.
func validCredentialValue(value string) bool {
	if value == "" || len(value) > maximumCredentialBytes {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character < 0x21 || character > 0x7e || character == '"' || character == ',' || character == ';' || character == '\\' {
			return false
		}
	}
	return true
}

// Format prevents accidental raw credential disclosure through structured or printf-style logging.
func redactCredential(state fmt.State, _ rune) {
	_, _ = state.Write([]byte("[REDACTED]"))
}
