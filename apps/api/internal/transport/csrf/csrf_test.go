package csrf

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestValidatorsKeepUserAndAdminCookiesIsolated(t *testing.T) {
	userToken := testToken(1)
	adminToken := testAdminToken(1)
	userRequest := csrfRequest(UserCookieName, userToken, userToken)
	got, err := NewUserValidator().Validate(userRequest)
	if err != nil || got != userToken {
		t.Fatalf("user token = %q, err = %v", got, err)
	}
	if _, err := NewAdminValidator().Validate(userRequest); !errors.Is(err, ErrInvalid) {
		t.Fatalf("admin accepted user CSRF Cookie: %v", err)
	}

	adminRequest := csrfRequest(AdminCookieName, adminToken, adminToken)
	got, err = NewAdminValidator().Validate(adminRequest)
	if err != nil || got != adminToken {
		t.Fatalf("admin token = %q, err = %v", got, err)
	}
	if _, err := NewUserValidator().Validate(adminRequest); !errors.Is(err, ErrInvalid) {
		t.Fatalf("user accepted admin CSRF Cookie: %v", err)
	}
}

func TestValidatorFailsClosedForMissingDuplicateAndConflictingHeaders(t *testing.T) {
	token, other := testToken(2), testToken(3)
	tests := []struct {
		name    string
		request *http.Request
	}{
		{name: "missing header", request: cookieOnlyRequest(UserCookieName, token)},
		{name: "empty header", request: csrfRequest(UserCookieName, token, "")},
		{name: "duplicate same", request: requestWithHeaders(UserCookieName, token, http.Header{HeaderName: {token, token}})},
		{name: "duplicate conflicting", request: requestWithHeaders(UserCookieName, token, http.Header{HeaderName: {token, other}})},
		{name: "case-conflicting keys", request: requestWithHeaders(UserCookieName, token, http.Header{HeaderName: {token}, "x-csrf-token": {other}})},
		{name: "combined header", request: csrfRequest(UserCookieName, token, token+","+other)},
		{name: "mismatched", request: csrfRequest(UserCookieName, token, other)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewUserValidator().Validate(test.request)
			assertSafeCSRFError(t, err, token, other)
		})
	}
}

func TestValidatorRejectsMissingDuplicateAndMalformedCookies(t *testing.T) {
	token := testToken(4)
	missing := &http.Request{Header: http.Header{HeaderName: {token}}}
	duplicate := csrfRequest(UserCookieName, token, token)
	duplicate.Header.Add("Cookie", UserCookieName+"="+token)
	malformed := csrfRequest(UserCookieName, "not-base64", "not-base64")
	nonCanonical := csrfRequest(UserCookieName, token+"=", token+"=")
	short := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, TokenBytes-1))
	shortRequest := csrfRequest(UserCookieName, short, short)

	for name, request := range map[string]*http.Request{
		"missing": missing, "duplicate": duplicate, "malformed": malformed,
		"non-canonical": nonCanonical, "short": shortRequest,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewUserValidator().Validate(request)
			assertSafeCSRFError(t, err, token, "not-base64")
		})
	}
}

func TestAdminValidatorRejectsMalformedSessionBoundTokens(t *testing.T) {
	valid := testAdminToken(5)
	wrongVersion := strings.Replace(valid, AdminSessionTokenVersion+".", "v2.", 1)
	shortSelector := AdminSessionTokenVersion + "." + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, adminSessionSelectorBytes-1)) + "." + testToken(6)
	rawUserToken := testToken(7)
	for name, token := range map[string]string{
		"wrong version": wrongVersion, "short selector": shortSelector, "raw user token": rawUserToken,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewAdminValidator().Validate(csrfRequest(AdminCookieName, token, token))
			assertSafeCSRFError(t, err, token)
		})
	}
}

func TestNilValidatorsAndRequestsFailClosed(t *testing.T) {
	var user *UserValidator
	var admin *AdminValidator
	if _, err := user.Validate(&http.Request{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil user validator error = %v", err)
	}
	if _, err := admin.Validate(&http.Request{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil admin validator error = %v", err)
	}
	if _, err := NewUserValidator().Validate(nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil request error = %v", err)
	}
}

func csrfRequest(cookieName, cookieValue, headerValue string) *http.Request {
	request := &http.Request{Header: make(http.Header)}
	request.Header.Set(HeaderName, headerValue)
	request.AddCookie(&http.Cookie{Name: cookieName, Value: cookieValue})
	return request
}

func cookieOnlyRequest(cookieName, cookieValue string) *http.Request {
	request := &http.Request{Header: make(http.Header)}
	request.AddCookie(&http.Cookie{Name: cookieName, Value: cookieValue})
	return request
}

func requestWithHeaders(cookieName, cookieValue string, header http.Header) *http.Request {
	request := &http.Request{Header: header}
	request.AddCookie(&http.Cookie{Name: cookieName, Value: cookieValue})
	return request
}

func testToken(seed byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{seed}, TokenBytes))
}

func testAdminToken(seed byte) string {
	selector := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{seed}, adminSessionSelectorBytes))
	return AdminSessionTokenVersion + "." + selector + "." + testToken(seed+1)
}

func assertSafeCSRFError(t *testing.T, err error, sensitive ...string) {
	t.Helper()
	if !errors.Is(err, ErrInvalid) || err.Error() != ErrInvalid.Error() {
		t.Fatalf("expected stable CSRF error, got %v", err)
	}
	for _, value := range sensitive {
		if value != "" && strings.Contains(err.Error(), value) {
			t.Fatalf("CSRF error leaked submitted token")
		}
	}
}
