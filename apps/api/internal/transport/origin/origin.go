// Package origin validates browser Origin headers against isolated user and administrator allowlists.
package origin

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
)

const (
	// MaximumOriginBytes bounds header parsing before URL validation.
	MaximumOriginBytes = 2048
	// HeaderName is the browser request header covered by this policy.
	HeaderName = "Origin"
)

var (
	// ErrInvalidConfig rejects empty, duplicate, wildcard, or non-canonical allowlists without echoing values.
	ErrInvalidConfig = errors.New("invalid origin policy configuration")
	// ErrOriginNotAllowed merges missing, duplicate, malformed, and unlisted request origins.
	ErrOriginNotAllowed = errors.New("request origin is not allowed")
	// ErrNotAllowed is a concise compatibility name for ErrOriginNotAllowed.
	ErrNotAllowed = ErrOriginNotAllowed
)

// UserOrigin is an origin accepted specifically by the user-domain policy.
type UserOrigin struct{ canonical string }

// Canonical returns the exact value bound into user challenge claims.
func (origin UserOrigin) Canonical() string { return origin.canonical }

// AdminOrigin is an origin accepted specifically by the administration-domain policy.
type AdminOrigin struct{ canonical string }

// Canonical returns the exact value bound into administrator challenge claims.
func (origin AdminOrigin) Canonical() string { return origin.canonical }

// UserValidator owns a private immutable copy of the user-domain allowlist.
type UserValidator struct{ policy policy }

// AdminValidator owns a separate private immutable copy of the administrator-domain allowlist.
type AdminValidator struct{ policy policy }

// NewUserValidator revalidates configured origins before the user service starts accepting requests.
func NewUserValidator(allowlist sharedconfig.OriginAllowlist) (*UserValidator, error) {
	validated, err := newPolicy(allowlist)
	if err != nil {
		return nil, err
	}
	return &UserValidator{policy: validated}, nil
}

// NewAdminValidator revalidates configured origins independently from user-domain configuration.
func NewAdminValidator(allowlist sharedconfig.OriginAllowlist) (*AdminValidator, error) {
	validated, err := newPolicy(allowlist)
	if err != nil {
		return nil, err
	}
	return &AdminValidator{policy: validated}, nil
}

// Validate accepts exactly one canonical Origin header from the user allowlist.
func (validator *UserValidator) Validate(request *http.Request) (UserOrigin, error) {
	if validator == nil || request == nil {
		return UserOrigin{}, ErrNotAllowed
	}
	canonical, err := validator.policy.validate(request.Header)
	if err != nil {
		return UserOrigin{}, err
	}
	return UserOrigin{canonical: canonical}, nil
}

// Validate accepts exactly one canonical Origin header from the administrator allowlist.
func (validator *AdminValidator) Validate(request *http.Request) (AdminOrigin, error) {
	if validator == nil || request == nil {
		return AdminOrigin{}, ErrNotAllowed
	}
	canonical, err := validator.policy.validate(request.Header)
	if err != nil {
		return AdminOrigin{}, err
	}
	return AdminOrigin{canonical: canonical}, nil
}

type policy struct{ allowed map[string]struct{} }

func newPolicy(allowlist sharedconfig.OriginAllowlist) (policy, error) {
	if len(allowlist) == 0 {
		return policy{}, ErrInvalidConfig
	}
	allowed := make(map[string]struct{}, len(allowlist))
	for _, configured := range allowlist {
		canonical, valid := parseCanonical(string(configured))
		if !valid {
			return policy{}, ErrInvalidConfig
		}
		if _, duplicate := allowed[canonical]; duplicate {
			return policy{}, ErrInvalidConfig
		}
		allowed[canonical] = struct{}{}
	}
	return policy{allowed: allowed}, nil
}

func (policy policy) validate(header http.Header) (string, error) {
	if len(policy.allowed) == 0 {
		return "", ErrNotAllowed
	}
	values := headerValues(header, HeaderName)
	if len(values) != 1 {
		return "", ErrNotAllowed
	}
	canonical, valid := parseCanonical(values[0])
	if !valid {
		return "", ErrNotAllowed
	}
	if _, allowed := policy.allowed[canonical]; !allowed {
		return "", ErrNotAllowed
	}
	return canonical, nil
}

func parseCanonical(raw string) (string, bool) {
	if len(raw) == 0 || len(raw) > MaximumOriginBytes || strings.TrimSpace(raw) != raw ||
		strings.ContainsAny(raw, "\x00\r\n\\") {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.User != nil || parsed.Host == "" || parsed.Hostname() == "" ||
		parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		strings.Contains(parsed.Host, "%") || strings.Contains(parsed.Hostname(), "*") {
		return "", false
	}
	if port := parsed.Port(); port != "" {
		value, portErr := strconv.Atoi(port)
		if portErr != nil || value < 1 || value > 65535 {
			return "", false
		}
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	canonical := scheme + "://" + strings.ToLower(parsed.Host)
	// Exact canonical form prevents implicit default-port, case, slash, or encoding equivalence.
	return canonical, raw == canonical
}

func headerValues(header http.Header, name string) []string {
	var values []string
	for key, current := range header {
		if strings.EqualFold(key, name) {
			values = append(values, current...)
		}
	}
	return values
}
