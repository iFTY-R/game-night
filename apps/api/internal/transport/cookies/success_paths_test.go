package cookies

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/security"
)

func TestSetAdminChallengeWritesIssuedLoginCredential(t *testing.T) {
	now := time.Date(2026, time.July, 19, 14, 0, 0, 0, time.UTC)
	source := clock.NewFake(now)
	challengeService, err := admin.NewChallengeService(loadTestHMACKeyring[security.AdminChallengeKeyPurpose](t, now), source)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := challengeService.Issue(
		admin.ChallengePurposeLogin,
		uuid.New(),
		1,
		1,
		"https://admin.example.test",
		challenge.RequestFlowID("flow_admin_cookie_success"),
		5,
	)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(source)
	if err != nil {
		t.Fatal(err)
	}
	response := connect.NewResponse(&struct{}{})
	if err := manager.SetAdminChallenge(responseHeaderWriter{response.Header()}, issued); err != nil {
		t.Fatal(err)
	}

	setCookies := parseResponseCookies(t, response.Header(), 1)
	requireIssuedCookie(
		t,
		setCookies[AdminChallengeCookieName],
		AdminChallengeCookieName,
		issued.Credentials.CookieToken,
		issued.Challenge.Snapshot().ExpiresAt,
		now,
		true,
		http.SameSiteStrictMode,
	)
}

func TestSetAdminSessionWritesPasswordAndMFAStepCredentials(t *testing.T) {
	now := time.Date(2026, time.July, 19, 14, 0, 0, 0, time.UTC)
	source := clock.NewFake(now)
	sessionService, manager := newAdminSessionCookieServices(t, source, now)
	tests := []struct {
		name string
		kind admin.SessionKind
	}{
		{name: "password setup", kind: admin.SessionKindSetupPasswordPending},
		{name: "MFA verification", kind: admin.SessionKindMFAPending},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			issued, err := sessionService.Issue(uuid.New(), test.kind, 1, 1, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := sessionService.Authenticate(issued.Session, issued.Token, issued.CSRFToken, now); err != nil {
				t.Fatal("issued administrator session did not authenticate")
			}
			response := connect.NewResponse(&struct{}{})
			if err := manager.SetAdminSession(responseHeaderWriter{response.Header()}, issued); err != nil {
				t.Fatal(err)
			}

			setCookies := parseResponseCookies(t, response.Header(), 2)
			expiresAt := issued.Session.Snapshot().IdleExpiresAt
			requireIssuedCookie(t, setCookies[AdminSessionCookieName], AdminSessionCookieName, issued.Token, expiresAt, now, true, http.SameSiteStrictMode)
			requireIssuedCookie(t, setCookies[AdminCSRFCookieName], AdminCSRFCookieName, issued.CSRFToken, expiresAt, now, false, http.SameSiteStrictMode)
		})
	}
}

func TestSetAdminSessionRotationWritesOnlyReplacementCredentials(t *testing.T) {
	now := time.Date(2026, time.July, 19, 14, 0, 0, 0, time.UTC)
	source := clock.NewFake(now)
	sessionService, manager := newAdminSessionCookieServices(t, source, now)
	adminID := uuid.New()
	previous, err := sessionService.Issue(adminID, admin.SessionKindMFAPending, 1, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := sessionService.Issue(adminID, admin.SessionKindFull, 1, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if previous.Token == replacement.Token || previous.CSRFToken == replacement.CSRFToken {
		t.Fatal("administrator session rotation reused credential material")
	}
	response := connect.NewResponse(&struct{}{})
	if err := manager.SetAdminSession(responseHeaderWriter{response.Header()}, replacement); err != nil {
		t.Fatal(err)
	}

	setCookies := parseResponseCookies(t, response.Header(), 2)
	expiresAt := replacement.Session.Snapshot().IdleExpiresAt
	requireIssuedCookie(t, setCookies[AdminSessionCookieName], AdminSessionCookieName, replacement.Token, expiresAt, now, true, http.SameSiteStrictMode)
	requireIssuedCookie(t, setCookies[AdminCSRFCookieName], AdminCSRFCookieName, replacement.CSRFToken, expiresAt, now, false, http.SameSiteStrictMode)
	if setCookies[AdminSessionCookieName].Value == previous.Token || setCookies[AdminCSRFCookieName].Value == previous.CSRFToken {
		t.Fatal("administrator session rotation wrote a superseded credential")
	}
}

func TestClearAdminSessionWritesLogoutDeletionCookies(t *testing.T) {
	now := time.Date(2026, time.July, 19, 14, 0, 0, 0, time.UTC)
	manager, err := NewManager(clock.NewFake(now))
	if err != nil {
		t.Fatal(err)
	}
	response := connect.NewResponse(&struct{}{})
	if err := manager.ClearAdminSession(responseHeaderWriter{response.Header()}); err != nil {
		t.Fatal(err)
	}

	setCookies := parseResponseCookies(t, response.Header(), 2)
	requireClearedCookie(t, setCookies[AdminSessionCookieName], AdminSessionCookieName, true, http.SameSiteStrictMode, now)
	requireClearedCookie(t, setCookies[AdminCSRFCookieName], AdminCSRFCookieName, false, http.SameSiteStrictMode, now)
}

type responseHeaderWriter struct{ header http.Header }

func (writer responseHeaderWriter) Add(key, value string) { writer.header.Add(key, value) }

func newAdminSessionCookieServices(
	t testing.TB,
	source *clock.Fake,
	now time.Time,
) (*admin.SessionService, *Manager) {
	t.Helper()
	sessionService, err := admin.NewSessionService(loadTestHMACKeyring[security.AdminSessionKeyPurpose](t, now), source)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(source)
	if err != nil {
		t.Fatal(err)
	}
	return sessionService, manager
}

func parseResponseCookies(t testing.TB, header http.Header, expectedCount int) map[string]*http.Cookie {
	t.Helper()
	values := header.Values("Set-Cookie")
	if len(values) != expectedCount {
		t.Fatalf("Set-Cookie count = %d, want %d", len(values), expectedCount)
	}
	result := make(map[string]*http.Cookie, len(values))
	for index, value := range values {
		cookie, err := http.ParseSetCookie(value)
		if err != nil {
			t.Fatalf("Set-Cookie header %d could not be parsed", index)
		}
		if _, duplicate := result[cookie.Name]; duplicate {
			t.Fatalf("duplicate Set-Cookie name %q", cookie.Name)
		}
		result[cookie.Name] = cookie
	}
	return result
}

func requireIssuedCookie(
	t testing.TB,
	cookie *http.Cookie,
	name string,
	expectedValue string,
	expiresAt time.Time,
	now time.Time,
	httpOnly bool,
	sameSite http.SameSite,
) {
	t.Helper()
	if cookie == nil {
		t.Fatalf("Cookie %q was not written", name)
	}
	if cookie.Value != expectedValue {
		t.Fatalf("Cookie %q did not contain the issued credential", name)
	}
	requireCookieScope(t, cookie, name, httpOnly, sameSite)
	if !cookie.Expires.Equal(expiresAt) {
		t.Fatalf("Cookie %q did not preserve the authoritative expiry", name)
	}
	remaining := expiresAt.Sub(now)
	wantMaxAge := int(remaining / time.Second)
	if remaining%time.Second != 0 {
		wantMaxAge++
	}
	if cookie.MaxAge != wantMaxAge {
		t.Fatalf("Cookie %q MaxAge = %d, want %d", name, cookie.MaxAge, wantMaxAge)
	}
}

func requireClearedCookie(
	t testing.TB,
	cookie *http.Cookie,
	name string,
	httpOnly bool,
	sameSite http.SameSite,
	now time.Time,
) {
	t.Helper()
	if cookie == nil {
		t.Fatalf("Cookie %q was not cleared", name)
	}
	requireCookieScope(t, cookie, name, httpOnly, sameSite)
	if cookie.Value != "" || cookie.MaxAge != -1 || !cookie.Expires.Before(now) {
		t.Fatalf("Cookie %q did not use the reviewed deletion policy", name)
	}
}

func requireCookieScope(t testing.TB, cookie *http.Cookie, name string, httpOnly bool, sameSite http.SameSite) {
	t.Helper()
	if cookie.Name != name || cookie.Path != "/" || cookie.Domain != "" || !cookie.Secure ||
		cookie.HttpOnly != httpOnly || cookie.SameSite != sameSite {
		t.Fatalf("Cookie %q did not use the reviewed scope and security policy", name)
	}
}

func loadTestHMACKeyring[P security.HMACKeyPurpose](t testing.TB, now time.Time) *security.HMACKeyring[P] {
	t.Helper()
	material := make([]byte, 32)
	if _, err := rand.Read(material); err != nil {
		t.Fatal(err)
	}
	document := struct {
		ActiveVersion uint32 `json:"active_version"`
		Keys          []struct {
			Version   uint32    `json:"version"`
			Key       string    `json:"key"`
			NotBefore time.Time `json:"not_before"`
		} `json:"keys"`
	}{ActiveVersion: 1}
	document.Keys = append(document.Keys, struct {
		Version   uint32    `json:"version"`
		Key       string    `json:"key"`
		NotBefore time.Time `json:"not_before"`
	}{Version: 1, Key: base64.StdEncoding.EncodeToString(material), NotBefore: now.Add(-time.Hour)})
	clear(material)
	contents, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	document.Keys[0].Key = ""
	path := filepath.Join(t.TempDir(), "hmac-keyring.json")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	clear(contents)
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(path, 0o600)
		_ = os.Remove(path)
	})
	keyring, err := security.LoadHMACKeyring[P](path, now)
	if err != nil {
		t.Fatal(err)
	}
	return keyring
}
