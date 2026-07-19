package origin

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
)

func TestValidatorsKeepUserAndAdminOriginsIsolated(t *testing.T) {
	user, err := NewUserValidator(sharedconfig.OriginAllowlist{"https://play.example.test", "http://localhost:3000"})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := NewAdminValidator(sharedconfig.OriginAllowlist{"https://admin.example.test"})
	if err != nil {
		t.Fatal(err)
	}

	userRequest := requestWithOrigin("https://play.example.test")
	acceptedUser, err := user.Validate(userRequest)
	if err != nil || acceptedUser.Canonical() != "https://play.example.test" {
		t.Fatalf("user origin = %q, err = %v", acceptedUser.Canonical(), err)
	}
	if _, err := admin.Validate(userRequest); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("admin accepted user origin: %v", err)
	}

	adminRequest := requestWithOrigin("https://admin.example.test")
	acceptedAdmin, err := admin.Validate(adminRequest)
	if err != nil || acceptedAdmin.Canonical() != "https://admin.example.test" {
		t.Fatalf("admin origin = %q, err = %v", acceptedAdmin.Canonical(), err)
	}
	if _, err := user.Validate(adminRequest); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("user accepted admin origin: %v", err)
	}
}

func TestValidatorFailsClosedForMissingDuplicateConflictingAndNonCanonicalHeaders(t *testing.T) {
	validator, err := NewUserValidator(sharedconfig.OriginAllowlist{"https://play.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		header http.Header
	}{
		{name: "missing", header: http.Header{}},
		{name: "empty", header: http.Header{"Origin": {""}}},
		{name: "duplicate same", header: http.Header{"Origin": {"https://play.example.test", "https://play.example.test"}}},
		{name: "duplicate conflicting", header: http.Header{"Origin": {"https://play.example.test", "https://other.example.test"}}},
		{name: "case-conflicting keys", header: http.Header{"Origin": {"https://play.example.test"}, "origin": {"https://other.example.test"}}},
		{name: "combined values", header: http.Header{"Origin": {"https://play.example.test, https://other.example.test"}}},
		{name: "null", header: http.Header{"Origin": {"null"}}},
		{name: "uppercase", header: http.Header{"Origin": {"https://PLAY.example.test"}}},
		{name: "trailing slash", header: http.Header{"Origin": {"https://play.example.test/"}}},
		{name: "path", header: http.Header{"Origin": {"https://play.example.test/callback"}}},
		{name: "credentials", header: http.Header{"Origin": {"https://secret@play.example.test"}}},
		{name: "query", header: http.Header{"Origin": {"https://play.example.test?secret"}}},
		{name: "wildcard", header: http.Header{"Origin": {"https://*.example.test"}}},
		{name: "invalid port", header: http.Header{"Origin": {"https://play.example.test:99999"}}},
		{name: "unlisted", header: http.Header{"Origin": {"https://other.example.test"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := &http.Request{Header: test.header}
			_, err := validator.Validate(request)
			if !errors.Is(err, ErrNotAllowed) || err.Error() != ErrNotAllowed.Error() {
				t.Fatalf("expected stable origin rejection, got %v", err)
			}
			for _, values := range test.header {
				for _, value := range values {
					if value != "" && strings.Contains(err.Error(), value) {
						t.Fatalf("error leaked rejected origin %q", value)
					}
				}
			}
		})
	}
}

func TestValidatorRejectsUnsafeConfigurationWithoutLeakingValues(t *testing.T) {
	tests := []sharedconfig.OriginAllowlist{
		nil,
		{"*"},
		{"https://secret.example.test/path"},
		{"https://secret.example.test", "https://secret.example.test"},
		{"https://*.example.test"},
		{"https://secret.example.test:99999"},
		{"HTTPS://secret.example.test"},
		{"https://secret.example.test/"},
	}
	for _, allowlist := range tests {
		_, err := NewUserValidator(allowlist)
		if !errors.Is(err, ErrInvalidConfig) || err.Error() != ErrInvalidConfig.Error() {
			t.Fatalf("expected stable config rejection, got %v", err)
		}
		for _, configured := range allowlist {
			if configured != "" && strings.Contains(err.Error(), string(configured)) {
				t.Fatalf("config error leaked origin %q", configured)
			}
		}
	}
}

func requestWithOrigin(value string) *http.Request {
	return &http.Request{Header: http.Header{"Origin": {value}}}
}
