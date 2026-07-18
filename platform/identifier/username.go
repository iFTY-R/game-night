// Package identifier validates and normalizes untrusted public identifiers into immutable value objects.
package identifier

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	// MinimumUsernameCodePoints is measured after NFKC normalization and trimming.
	MinimumUsernameCodePoints = 2
	// MaximumUsernameCodePoints bounds the normalized display; case-folded claim keys may expand beyond it.
	MaximumUsernameCodePoints = 20
	// maximumUsernameInputBytes bounds normalization work while allowing combining forms around the display limit.
	maximumUsernameInputBytes = MaximumUsernameCodePoints * 16
)

var (
	// ErrUsernameLength identifies a normalized username outside the public 2-20 code point contract.
	ErrUsernameLength = errors.New("username length is invalid")
	// ErrUsernameCharacters covers malformed UTF-8 and characters outside Han, Latin, decimal digits, and underscore.
	ErrUsernameCharacters = errors.New("username contains unsupported characters")
	// ErrUsernameUnderscores prevents visually noisy and easily confused runs of underscores.
	ErrUsernameUnderscores = errors.New("username cannot contain consecutive underscores")
	// ErrUsernameUnavailable deliberately gives reserved and blocked terms the same external result.
	ErrUsernameUnavailable = errors.New("username is unavailable")
	// ErrInvalidUsernamePolicy reports a bad deny-list entry without echoing configuration content.
	ErrInvalidUsernamePolicy = errors.New("username validation policy is invalid")
)

// builtInReservedUsernameKeys protects platform, administrative, and support identities from impersonation.
// Product-specific sensitive terms stay injectable because that list has an independent operational lifecycle.
var builtInReservedUsernameKeys = [...]string{
	"admin",
	"administrator",
	"api",
	"mod",
	"moderator",
	"null",
	"official",
	"root",
	"staff",
	"support",
	"system",
	"undefined",
	"www",
	"官方",
	"客服",
	"管理员",
	"系统",
}

// Username carries the normalized public display value and its case-insensitive claim key.
// Fields remain private so invalid or non-normalized values cannot cross domain boundaries.
type Username struct {
	display string
	key     string
}

// Display returns the NFKC-normalized, trimmed spelling shown to users.
func (username Username) Display() string {
	return username.display
}

// Key returns the NFKC case-folded value used for uniqueness, lookup, and rate-limit dimensions.
func (username Username) Key() string {
	return username.key
}

// CodePointCount reports the display length using the same unit enforced by ParseUsername.
func (username Username) CodePointCount() int {
	return utf8.RuneCountInString(username.display)
}

// UsernameValidator combines built-in anti-impersonation names with deployment-specific deny rules.
// It is immutable after construction and safe for concurrent use.
type UsernameValidator struct {
	reservedKeys     map[string]struct{}
	blockedFragments []string
}

// NewUsernameValidator creates a validator with built-in reserved names, additional exact reserved names,
// and blocked fragments. Policy errors never include the original policy value.
func NewUsernameValidator(additionalReserved, blockedFragments []string) (UsernameValidator, error) {
	reserved := make(map[string]struct{}, len(builtInReservedUsernameKeys)+len(additionalReserved))
	for _, value := range builtInReservedUsernameKeys {
		_, key, err := normalizeUsernameSyntax(value)
		if err != nil {
			return UsernameValidator{}, ErrInvalidUsernamePolicy
		}
		reserved[key] = struct{}{}
	}
	for _, value := range additionalReserved {
		_, key, err := normalizeUsernameSyntax(value)
		if err != nil {
			return UsernameValidator{}, ErrInvalidUsernamePolicy
		}
		reserved[key] = struct{}{}
	}

	blocked := make([]string, 0, len(blockedFragments))
	seenBlocked := make(map[string]struct{}, len(blockedFragments))
	for _, value := range blockedFragments {
		key, err := normalizeUsernameFragment(value)
		if err != nil {
			return UsernameValidator{}, ErrInvalidUsernamePolicy
		}
		if _, exists := seenBlocked[key]; exists {
			continue
		}
		seenBlocked[key] = struct{}{}
		blocked = append(blocked, key)
	}

	return UsernameValidator{reservedKeys: reserved, blockedFragments: blocked}, nil
}

// ParseUsername validates an untrusted username with the built-in platform reservation policy.
func ParseUsername(input string) (Username, error) {
	return defaultUsernameValidator.Parse(input)
}

// Parse applies syntax normalization and policy checks, returning only validated values.
func (validator UsernameValidator) Parse(input string) (Username, error) {
	// A zero-value validator still enforces built-in reservations instead of silently disabling policy.
	if validator.reservedKeys == nil {
		validator = defaultUsernameValidator
	}

	display, key, err := normalizeUsernameSyntax(input)
	if err != nil {
		return Username{}, err
	}
	if _, reserved := validator.reservedKeys[key]; reserved {
		return Username{}, ErrUsernameUnavailable
	}
	for _, fragment := range validator.blockedFragments {
		if strings.Contains(key, fragment) {
			return Username{}, ErrUsernameUnavailable
		}
	}
	return Username{display: display, key: key}, nil
}

func normalizeUsernameSyntax(input string) (display, key string, err error) {
	if len(input) > maximumUsernameInputBytes {
		return "", "", ErrUsernameLength
	}
	if !utf8.ValidString(input) {
		return "", "", ErrUsernameCharacters
	}
	// Reject control and format code points before trimming so normalization never repairs forbidden input.
	for _, character := range input {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return "", "", ErrUsernameCharacters
		}
	}

	display = strings.TrimSpace(norm.NFKC.String(input))
	codePoints := utf8.RuneCountInString(display)
	if codePoints < MinimumUsernameCodePoints || codePoints > MaximumUsernameCodePoints {
		return "", "", ErrUsernameLength
	}

	previousUnderscore := false
	for _, character := range display {
		if character == '_' {
			if previousUnderscore {
				return "", "", ErrUsernameUnderscores
			}
			previousUnderscore = true
			continue
		}
		previousUnderscore = false
		if !isAllowedUsernameCharacter(character) {
			return "", "", ErrUsernameCharacters
		}
	}

	return display, foldUsername(display), nil
}

func normalizeUsernameFragment(input string) (string, error) {
	if len(input) > maximumUsernameInputBytes {
		return "", ErrInvalidUsernamePolicy
	}
	if !utf8.ValidString(input) {
		return "", ErrInvalidUsernamePolicy
	}
	normalized := strings.TrimSpace(norm.NFKC.String(input))
	codePoints := utf8.RuneCountInString(normalized)
	if codePoints == 0 || codePoints > MaximumUsernameCodePoints {
		return "", ErrInvalidUsernamePolicy
	}
	for _, character := range normalized {
		if character != '_' && !isAllowedUsernameCharacter(character) {
			return "", ErrInvalidUsernamePolicy
		}
	}
	return foldUsername(normalized), nil
}

func isAllowedUsernameCharacter(character rune) bool {
	if unicode.Is(unicode.Nd, character) {
		return true
	}
	return unicode.IsLetter(character) && (unicode.Is(unicode.Han, character) || unicode.Is(unicode.Latin, character))
}

// foldUsername applies the stable Unicode uniqueness transform after display normalization.
func foldUsername(value string) string {
	return norm.NFKC.String(cases.Fold().String(value))
}

func mustDefaultUsernameValidator() UsernameValidator {
	validator, err := NewUsernameValidator(nil, nil)
	if err != nil {
		panic("invalid built-in username policy")
	}
	return validator
}

// defaultUsernameValidator is initialized once and only read after startup, making concurrent parsing lock-free.
var defaultUsernameValidator = mustDefaultUsernameValidator()
