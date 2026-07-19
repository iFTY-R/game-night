package cookies

import (
	"fmt"
	"net/http"
	"strings"
)

// credentialMaterial keeps secrets behind one private indirection because fmt bypasses Formatter for %p on values.
// Credential values are immutable after a successful read.
type credentialMaterial struct {
	cookieToken string
	csrfToken   string
}

// UserDeviceCredentials carries only the exact user Cookie pair requested by identity handlers.
type UserDeviceCredentials struct {
	material *credentialMaterial
}

// CookieToken reveals the user bearer only at the explicit domain-command boundary.
func (credentials UserDeviceCredentials) CookieToken() string {
	if credentials.material == nil {
		return ""
	}
	return credentials.material.cookieToken
}

// CSRFToken reveals the user double-submit value only at the explicit CSRF boundary.
func (credentials UserDeviceCredentials) CSRFToken() string {
	if credentials.material == nil {
		return ""
	}
	return credentials.material.csrfToken
}

func (UserDeviceCredentials) String() string                    { return "[REDACTED]" }
func (UserDeviceCredentials) Format(state fmt.State, verb rune) { redactCredential(state, verb) }

// AdminSessionCredentials cannot be substituted for user device credentials at compile time.
type AdminSessionCredentials struct {
	material *credentialMaterial
}

// CookieToken reveals the administrator bearer only at the explicit admin-command boundary.
func (credentials AdminSessionCredentials) CookieToken() string {
	if credentials.material == nil {
		return ""
	}
	return credentials.material.cookieToken
}

// CSRFToken reveals the administrator double-submit value only at the explicit CSRF boundary.
func (credentials AdminSessionCredentials) CSRFToken() string {
	if credentials.material == nil {
		return ""
	}
	return credentials.material.csrfToken
}

func (AdminSessionCredentials) String() string                    { return "[REDACTED]" }
func (AdminSessionCredentials) Format(state fmt.State, verb rune) { redactCredential(state, verb) }

// UserChallengeCredential keeps the user challenge namespace distinct from all authenticated Cookie pairs.
type UserChallengeCredential struct{ material *credentialMaterial }

// CookieToken reveals the user challenge token only for the matching Begin/Complete flow.
func (credential UserChallengeCredential) CookieToken() string {
	if credential.material == nil {
		return ""
	}
	return credential.material.cookieToken
}

func (UserChallengeCredential) String() string                    { return "[REDACTED]" }
func (UserChallengeCredential) Format(state fmt.State, verb rune) { redactCredential(state, verb) }

// AdminChallengeCredential keeps the administrator challenge namespace distinct from the user challenge.
type AdminChallengeCredential struct{ material *credentialMaterial }

// CookieToken reveals the administrator challenge token only for the matching Begin/Complete flow.
func (credential AdminChallengeCredential) CookieToken() string {
	if credential.material == nil {
		return ""
	}
	return credential.material.cookieToken
}

func (AdminChallengeCredential) String() string                    { return "[REDACTED]" }
func (AdminChallengeCredential) Format(state fmt.State, verb rune) { redactCredential(state, verb) }

// ReadUserDevice requires exactly one bearer and one CSRF Cookie from the user namespace.
func ReadUserDevice(request *http.Request) (UserDeviceCredentials, error) {
	bearer, err := readExactlyOne(request, userDeviceDefinition)
	if err != nil {
		return UserDeviceCredentials{}, err
	}
	csrf, err := readExactlyOne(request, userCSRFDefinition)
	if err != nil {
		return UserDeviceCredentials{}, err
	}
	return UserDeviceCredentials{material: &credentialMaterial{cookieToken: bearer, csrfToken: csrf}}, nil
}

// ReadOptionalUserDevice distinguishes a genuinely new browser from a partial or corrupted credential pair.
// Any raw occurrence enters the fail-closed path, even when net/http rejects its malformed value during parsing.
func ReadOptionalUserDevice(request *http.Request) (UserDeviceCredentials, bool, error) {
	if request == nil {
		return UserDeviceCredentials{}, false, ErrCredentialUnavailable
	}
	deviceCount := rawCookieNameCount(request, UserDeviceCookieName)
	csrfCount := rawCookieNameCount(request, UserCSRFCookieName)
	if deviceCount == 0 && csrfCount == 0 {
		return UserDeviceCredentials{}, false, nil
	}
	credentials, err := ReadUserDevice(request)
	if err != nil {
		return UserDeviceCredentials{}, true, err
	}
	return credentials, true, nil
}

// ReadAdminSession requires exactly one bearer and one CSRF Cookie from the administrator namespace.
func ReadAdminSession(request *http.Request) (AdminSessionCredentials, error) {
	bearer, err := readExactlyOne(request, adminSessionDefinition)
	if err != nil {
		return AdminSessionCredentials{}, err
	}
	csrf, err := readExactlyOne(request, adminCSRFDefinition)
	if err != nil {
		return AdminSessionCredentials{}, err
	}
	return AdminSessionCredentials{material: &credentialMaterial{cookieToken: bearer, csrfToken: csrf}}, nil
}

// ReadUserChallenge reads only the user challenge namespace and rejects duplicates.
func ReadUserChallenge(request *http.Request) (UserChallengeCredential, error) {
	value, err := readExactlyOne(request, userChallengeDefinition)
	if err != nil {
		return UserChallengeCredential{}, err
	}
	return UserChallengeCredential{material: &credentialMaterial{cookieToken: value}}, nil
}

// ReadAdminChallenge reads only the administrator challenge namespace and rejects duplicates.
func ReadAdminChallenge(request *http.Request) (AdminChallengeCredential, error) {
	value, err := readExactlyOne(request, adminChallengeDefinition)
	if err != nil {
		return AdminChallengeCredential{}, err
	}
	return AdminChallengeCredential{material: &credentialMaterial{cookieToken: value}}, nil
}

// readExactlyOne requires the raw and parsed views to agree on one safe value for the requested namespace.
func readExactlyOne(request *http.Request, definition cookieDefinition) (string, error) {
	if request == nil || !validDefinition(definition) {
		return "", ErrCredentialUnavailable
	}
	// Count the raw name first so a malformed duplicate discarded by net/http cannot shadow a valid value.
	if rawCookieNameCount(request, definition.name) != 1 {
		return "", ErrCredentialUnavailable
	}
	value := ""
	count := 0
	for _, cookie := range request.Cookies() {
		if cookie.Name != definition.name {
			continue
		}
		count++
		value = cookie.Value
	}
	if count != 1 || !validCredentialValue(value) {
		return "", ErrCredentialUnavailable
	}
	return value, nil
}

// rawCookieNameCount observes names even when net/http deliberately discards an invalid Cookie value.
func rawCookieNameCount(request *http.Request, target string) int {
	if request == nil || target == "" {
		return 0
	}
	count := 0
	for _, line := range request.Header.Values("Cookie") {
		for _, part := range strings.Split(line, ";") {
			name, _, _ := strings.Cut(strings.TrimSpace(part), "=")
			if strings.TrimSpace(name) == target {
				count++
			}
		}
	}
	return count
}
