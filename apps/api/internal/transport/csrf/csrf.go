// Package csrf validates domain-isolated double-submit CSRF proofs before service authentication.
package csrf

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/security"
)

const (
	// HeaderName is the sole request header accepted for double-submit proof.
	HeaderName = "X-CSRF-Token"
	// UserCookieName is readable only by the user-side browser application.
	UserCookieName = "__Host-gn_csrf"
	// AdminCookieName is isolated from user credentials and uses stricter Cookie attributes at issuance.
	AdminCookieName = "__Host-gn_admin_csrf"
	// TokenBytes matches device and administrator session CSRF entropy.
	TokenBytes = 32
	// AdminSessionTokenVersion matches the independently verified administrator session token protocol.
	AdminSessionTokenVersion = "v1"
	// adminSessionSelectorBytes fixes the public selector size used by administrator session records.
	adminSessionSelectorBytes = 16
	// maximumUserEncodedTokenBytes bounds request parsing before strict Base64URL decoding.
	maximumUserEncodedTokenBytes = 64
	// maximumAdminEncodedTokenBytes bounds the version, selector, and CSRF secret before token parsing.
	maximumAdminEncodedTokenBytes = 96
)

var (
	// ErrCSRFInvalid merges missing, duplicate, malformed, conflicting, and mismatched CSRF proofs.
	ErrCSRFInvalid = errors.New("csrf validation failed")
	// ErrInvalid is a concise compatibility name for ErrCSRFInvalid.
	ErrInvalid = ErrCSRFInvalid
)

// UserValidator accepts only the user CSRF Cookie name.
type UserValidator struct{ verifier verifier }

// AdminValidator accepts only the administrator CSRF Cookie name.
type AdminValidator struct{ verifier verifier }

// NewUserValidator returns an isolated user-domain double-submit validator.
func NewUserValidator() *UserValidator {
	return &UserValidator{verifier: verifier{cookieName: UserCookieName, format: tokenFormatUser}}
}

// NewAdminValidator returns an isolated administrator-domain double-submit validator.
func NewAdminValidator() *AdminValidator {
	return &AdminValidator{verifier: verifier{cookieName: AdminCookieName, format: tokenFormatAdmin}}
}

// Validate authenticates Cookie/header equality and returns the token for device-bound verification.
func (validator *UserValidator) Validate(request *http.Request) (string, error) {
	if validator == nil {
		return "", ErrInvalid
	}
	return validator.verifier.validate(request)
}

// Validate authenticates Cookie/header equality and returns the token for admin-session-bound verification.
func (validator *AdminValidator) Validate(request *http.Request) (string, error) {
	if validator == nil {
		return "", ErrInvalid
	}
	return validator.verifier.validate(request)
}

type tokenFormat uint8

const (
	tokenFormatUser tokenFormat = iota + 1
	tokenFormatAdmin
)

type verifier struct {
	cookieName string
	format     tokenFormat
}

func (verifier verifier) validate(request *http.Request) (string, error) {
	if request == nil || verifier.cookieName == "" {
		return "", ErrInvalid
	}
	headerValues := matchingHeaderValues(request.Header, HeaderName)
	if len(headerValues) != 1 {
		return "", ErrInvalid
	}
	cookieValue, found := singleCookie(request, verifier.cookieName)
	if !found {
		return "", ErrInvalid
	}
	if !validToken(headerValues[0], verifier.format) {
		return "", ErrInvalid
	}
	if !validToken(cookieValue, verifier.format) {
		return "", ErrInvalid
	}
	// Strict format validation fixes each domain's encoded length before direct constant-time comparison.
	matched := security.ConstantTimeEqual([]byte(cookieValue), []byte(headerValues[0]))
	if !matched {
		return "", ErrInvalid
	}
	return headerValues[0], nil
}

func matchingHeaderValues(header http.Header, name string) []string {
	var values []string
	for key, current := range header {
		if strings.EqualFold(key, name) {
			values = append(values, current...)
		}
	}
	return values
}

func singleCookie(request *http.Request, name string) (string, bool) {
	var value string
	found := false
	for _, cookie := range request.Cookies() {
		if cookie.Name != name {
			continue
		}
		if found {
			return "", false
		}
		found, value = true, cookie.Value
	}
	return value, found
}

func validToken(encoded string, format tokenFormat) bool {
	if strings.TrimSpace(encoded) != encoded {
		return false
	}
	switch format {
	case tokenFormatUser:
		return validUserToken(encoded)
	case tokenFormatAdmin:
		return validAdminToken(encoded)
	default:
		return false
	}
}

func validUserToken(encoded string) bool {
	if len(encoded) == 0 || len(encoded) > maximumUserEncodedTokenBytes {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || len(decoded) != TokenBytes || base64.RawURLEncoding.EncodeToString(decoded) != encoded {
		clear(decoded)
		return false
	}
	clear(decoded)
	return true
}

func validAdminToken(encoded string) bool {
	if len(encoded) == 0 || len(encoded) > maximumAdminEncodedTokenBytes {
		return false
	}
	parsed, err := security.ParseToken(encoded, security.TokenPolicy{
		Version: AdminSessionTokenVersion, MinSecretBytes: TokenBytes, MaxSecretBytes: TokenBytes,
	})
	if err != nil {
		return false
	}
	defer clear(parsed.Secret)
	selector, err := identifier.ParseSelector(parsed.Selector)
	return err == nil && selector.ByteLength() == adminSessionSelectorBytes
}
