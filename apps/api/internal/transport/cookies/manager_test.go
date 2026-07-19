package cookies

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/iFTY-R/game-night/platform/clock"
)

// testCredentialValue exercises the allowed token punctuation without depending on a domain token generator.
const testCredentialValue = "v1.transport-test-secret_123"

type cookieExpectation struct {
	definition cookieDefinition
	httpOnly   bool
	sameSite   http.SameSite
}

// headerOnlyWriter proves Cookie transport does not require status or body-writing capabilities.
type headerOnlyWriter struct {
	header http.Header
}

func newHeaderOnlyWriter() *headerOnlyWriter {
	return &headerOnlyWriter{header: make(http.Header)}
}

func (writer *headerOnlyWriter) Add(key, value string) {
	writer.header.Add(key, value)
}

func (writer *headerOnlyWriter) Header() http.Header { return writer.header }

func TestCredentialCookieDefinitionsEnforceSecurityPolicyAndExpiry(t *testing.T) {
	now := time.Date(2026, time.July, 19, 8, 30, 0, 0, time.UTC)
	expiresAt := now.Add(90*time.Second + 500*time.Millisecond)

	for _, expectation := range cookieExpectations() {
		t.Run(expectation.definition.name, func(t *testing.T) {
			cookie, err := newCredentialCookie(expectation.definition, testCredentialValue, now, expiresAt)
			if err != nil {
				t.Fatalf("new credential Cookie: %v", err)
			}
			assertCookiePolicy(t, cookie, expectation)
			if cookie.Value != testCredentialValue {
				t.Fatalf("Cookie value changed during encoding")
			}
			if cookie.MaxAge != 91 {
				t.Fatalf("MaxAge = %d, want 91 rounded-up seconds", cookie.MaxAge)
			}
			if !cookie.Expires.Equal(expiresAt) {
				t.Fatalf("Expires = %v, want authoritative %v", cookie.Expires, expiresAt)
			}
		})
	}
}

func TestClearOperationsKeepIssuanceScopeAndPolicy(t *testing.T) {
	now := time.Date(2026, time.July, 19, 8, 30, 0, 0, time.UTC)
	manager, err := NewManager(clock.NewFake(now))
	if err != nil {
		t.Fatalf("new Manager: %v", err)
	}
	tests := []struct {
		name         string
		clear        func(HeaderWriter) error
		expectations []cookieExpectation
	}{
		{name: "user device pair", clear: manager.ClearUserDevice, expectations: cookieExpectations()[0:2]},
		{name: "user challenge", clear: manager.ClearUserChallenge, expectations: cookieExpectations()[2:3]},
		{name: "admin session pair", clear: manager.ClearAdminSession, expectations: cookieExpectations()[3:5]},
		{name: "admin challenge", clear: manager.ClearAdminChallenge, expectations: cookieExpectations()[5:6]},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writer := newHeaderOnlyWriter()
			if err := test.clear(writer); err != nil {
				t.Fatalf("clear Cookies: %v", err)
			}
			cookies := (&http.Response{Header: writer.Header()}).Cookies()
			if len(cookies) != len(test.expectations) {
				t.Fatalf("cleared Cookie count = %d, want %d", len(cookies), len(test.expectations))
			}
			for index, expectation := range test.expectations {
				cookie := cookies[index]
				assertCookiePolicy(t, cookie, expectation)
				if cookie.Value != "" || cookie.MaxAge != -1 || !cookie.Expires.Before(now) {
					t.Fatalf("clear Cookie = %+v, want empty value, MaxAge=-1, expired Expires", cookie)
				}
			}
		})
	}
}

func TestManagerRejectsExpiredOrPartialCredentialWrites(t *testing.T) {
	now := time.Date(2026, time.July, 19, 8, 30, 0, 0, time.UTC)
	manager, err := NewManager(clock.NewFake(now))
	if err != nil {
		t.Fatalf("new Manager: %v", err)
	}

	expiredWriter := newHeaderOnlyWriter()
	err = manager.writeSingle(expiredWriter, userChallengeDefinition, testCredentialValue, now)
	assertTransportError(t, err, ErrInvalidInput, testCredentialValue)
	if len(expiredWriter.Header().Values("Set-Cookie")) != 0 {
		t.Fatal("expired issuance wrote a Set-Cookie header")
	}

	partialWriter := newHeaderOnlyWriter()
	invalidSecondValue := "admin-secret;injected"
	err = manager.writePair(
		partialWriter,
		adminSessionDefinition, testCredentialValue,
		adminCSRFDefinition, invalidSecondValue,
		now.Add(time.Hour),
	)
	assertTransportError(t, err, ErrInvalidInput, testCredentialValue, invalidSecondValue)
	if len(partialWriter.Header().Values("Set-Cookie")) != 0 {
		t.Fatal("invalid second credential caused a partial Set-Cookie write")
	}

	nilHeaderWriter := &headerOnlyWriter{}
	err = manager.ClearUserChallenge(nilHeaderWriter)
	assertTransportError(t, err, ErrInvalidInput)
}

func TestCredentialReadersKeepUserAndAdminNamespacesIsolated(t *testing.T) {
	userRequest := requestWithCookies(
		&http.Cookie{Name: UserDeviceCookieName, Value: "user-device-secret"},
		&http.Cookie{Name: UserCSRFCookieName, Value: "user-csrf-secret"},
		&http.Cookie{Name: UserChallengeCookieName, Value: "user-challenge-secret"},
	)
	user, err := ReadUserDevice(userRequest)
	if err != nil || user.CookieToken() != "user-device-secret" || user.CSRFToken() != "user-csrf-secret" {
		t.Fatalf("read user credentials: value=%v, err=%v", user, err)
	}
	userChallenge, err := ReadUserChallenge(userRequest)
	if err != nil || userChallenge.CookieToken() != "user-challenge-secret" {
		t.Fatalf("read user challenge: value=%v, err=%v", userChallenge, err)
	}
	if _, err := ReadAdminSession(userRequest); !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("administrator reader accepted user credentials: %v", err)
	}
	if _, err := ReadAdminChallenge(userRequest); !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("administrator challenge reader accepted user credentials: %v", err)
	}

	adminRequest := requestWithCookies(
		&http.Cookie{Name: AdminSessionCookieName, Value: "admin-session-secret"},
		&http.Cookie{Name: AdminCSRFCookieName, Value: "admin-csrf-secret"},
		&http.Cookie{Name: AdminChallengeCookieName, Value: "admin-challenge-secret"},
	)
	adminCredentials, err := ReadAdminSession(adminRequest)
	if err != nil || adminCredentials.CookieToken() != "admin-session-secret" || adminCredentials.CSRFToken() != "admin-csrf-secret" {
		t.Fatalf("read administrator credentials: value=%v, err=%v", adminCredentials, err)
	}
	adminChallenge, err := ReadAdminChallenge(adminRequest)
	if err != nil || adminChallenge.CookieToken() != "admin-challenge-secret" {
		t.Fatalf("read administrator challenge: value=%v, err=%v", adminChallenge, err)
	}
	if _, err := ReadUserDevice(adminRequest); !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("user reader accepted administrator credentials: %v", err)
	}
	if _, err := ReadUserChallenge(adminRequest); !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("user challenge reader accepted administrator credentials: %v", err)
	}
}

func TestReadOptionalUserDeviceDistinguishesAbsentFromInvalidPairs(t *testing.T) {
	valid := requestWithCookies(
		&http.Cookie{Name: UserDeviceCookieName, Value: "user-device-secret"},
		&http.Cookie{Name: UserCSRFCookieName, Value: "user-csrf-secret"},
	)
	duplicate := requestWithCookies(
		&http.Cookie{Name: UserDeviceCookieName, Value: "user-device-secret"},
		&http.Cookie{Name: UserDeviceCookieName, Value: "duplicate-device-secret"},
		&http.Cookie{Name: UserCSRFCookieName, Value: "user-csrf-secret"},
	)
	malformed := requestWithRawCookieHeader(
		UserDeviceCookieName + "=malformed device secret; " + UserCSRFCookieName + "=user-csrf-secret",
	)
	adminOnly := requestWithCookies(
		&http.Cookie{Name: AdminSessionCookieName, Value: "admin-session-secret"},
		&http.Cookie{Name: AdminCSRFCookieName, Value: "admin-csrf-secret"},
	)
	tests := []struct {
		name        string
		request     *http.Request
		wantPresent bool
		wantError   bool
	}{
		{name: "no Cookies", request: requestWithCookies(), wantPresent: false},
		{name: "administrator Cookies only", request: adminOnly, wantPresent: false},
		{name: "complete valid pair", request: valid, wantPresent: true},
		{name: "device only", request: requestWithCookies(&http.Cookie{Name: UserDeviceCookieName, Value: "user-device-secret"}), wantPresent: true, wantError: true},
		{name: "CSRF only", request: requestWithCookies(&http.Cookie{Name: UserCSRFCookieName, Value: "user-csrf-secret"}), wantPresent: true, wantError: true},
		{name: "duplicate device", request: duplicate, wantPresent: true, wantError: true},
		{name: "malformed device", request: malformed, wantPresent: true, wantError: true},
		{name: "nil request", request: nil, wantPresent: false, wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			credentials, present, err := ReadOptionalUserDevice(test.request)
			if present != test.wantPresent {
				t.Fatalf("present = %v, want %v", present, test.wantPresent)
			}
			if test.wantError {
				assertTransportError(t, err, ErrCredentialUnavailable, "user-device-secret", "user-csrf-secret", "malformed device secret")
				return
			}
			if err != nil {
				t.Fatalf("optional user credentials: %v", err)
			}
			if present && (credentials.CookieToken() != "user-device-secret" || credentials.CSRFToken() != "user-csrf-secret") {
				t.Fatalf("optional user credentials did not preserve the valid pair: %v", credentials)
			}
		})
	}
}

func TestReadersRejectDuplicateAndMalformedCookiesWithoutLeakingValues(t *testing.T) {
	for _, expectation := range cookieExpectations() {
		t.Run(expectation.definition.name+" duplicate", func(t *testing.T) {
			secret := "submitted-duplicate-secret"
			request := requestWithRawCookieHeader(expectation.definition.name + "=" + secret + "; " + expectation.definition.name + "=" + secret)
			_, err := readExactlyOne(request, expectation.definition)
			assertTransportError(t, err, ErrCredentialUnavailable, secret)
		})
		t.Run(expectation.definition.name+" malformed", func(t *testing.T) {
			secret := "submitted malformed secret"
			request := requestWithRawCookieHeader(expectation.definition.name + "=" + secret)
			_, err := readExactlyOne(request, expectation.definition)
			assertTransportError(t, err, ErrCredentialUnavailable, secret)
		})
		t.Run(expectation.definition.name+" malformed duplicate", func(t *testing.T) {
			validSecret := "valid-secret"
			malformedSecret := "submitted malformed secret"
			request := requestWithRawCookieHeader(
				expectation.definition.name + "=" + validSecret + "; " + expectation.definition.name + "=" + malformedSecret,
			)
			_, err := readExactlyOne(request, expectation.definition)
			assertTransportError(t, err, ErrCredentialUnavailable, validSecret, malformedSecret)
		})
	}
}

func TestCredentialFormattingAlwaysRedactsSecrets(t *testing.T) {
	secret := "formatting-must-not-leak"
	material := &credentialMaterial{cookieToken: secret, csrfToken: secret}
	credentials := []any{
		UserDeviceCredentials{material: material},
		AdminSessionCredentials{material: material},
		UserChallengeCredential{material: material},
		AdminChallengeCredential{material: material},
	}
	formats := []string{"%s", "%q", "%v", "%+v", "%#v", "%x", "%X", "%d", "%p", "%t"}
	for _, credential := range credentials {
		for _, format := range formats {
			formatted := fmt.Sprintf(format, credential)
			if strings.Contains(formatted, secret) {
				t.Fatalf("format %q produced unsafe credential output %q", format, formatted)
			}
			if format != "%p" && formatted != "[REDACTED]" {
				t.Fatalf("format %q did not use the redacted representation: %q", format, formatted)
			}
		}
	}
}

func cookieExpectations() []cookieExpectation {
	return []cookieExpectation{
		{definition: userDeviceDefinition, httpOnly: true, sameSite: http.SameSiteLaxMode},
		{definition: userCSRFDefinition, httpOnly: false, sameSite: http.SameSiteLaxMode},
		{definition: userChallengeDefinition, httpOnly: true, sameSite: http.SameSiteLaxMode},
		{definition: adminSessionDefinition, httpOnly: true, sameSite: http.SameSiteStrictMode},
		{definition: adminCSRFDefinition, httpOnly: false, sameSite: http.SameSiteStrictMode},
		{definition: adminChallengeDefinition, httpOnly: true, sameSite: http.SameSiteStrictMode},
	}
}

func assertCookiePolicy(t *testing.T, cookie *http.Cookie, expectation cookieExpectation) {
	t.Helper()
	if cookie.Name != expectation.definition.name || cookie.Path != "/" || cookie.Domain != "" ||
		!cookie.Secure || cookie.HttpOnly != expectation.httpOnly || cookie.SameSite != expectation.sameSite {
		t.Fatalf("Cookie policy = %+v, want name=%q path=/ host-only Secure=%v HttpOnly=%v SameSite=%v",
			cookie, expectation.definition.name, true, expectation.httpOnly, expectation.sameSite)
	}
}

func assertTransportError(t *testing.T, err, target error, sensitive ...string) {
	t.Helper()
	if !errors.Is(err, target) || err.Error() != target.Error() {
		t.Fatalf("transport error = %v, want stable %v", err, target)
	}
	for _, value := range sensitive {
		if value != "" && strings.Contains(err.Error(), value) {
			t.Fatal("transport error leaked submitted credential")
		}
	}
}

func requestWithCookies(cookies ...*http.Cookie) *http.Request {
	request := &http.Request{Header: make(http.Header)}
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	return request
}

func requestWithRawCookieHeader(value string) *http.Request {
	return &http.Request{Header: http.Header{"Cookie": {value}}}
}

var _ HeaderWriter = (*headerOnlyWriter)(nil)
