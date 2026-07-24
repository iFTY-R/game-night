package identifier

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestParseUsernameNormalizesDisplayAndKey(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		display string
		key     string
	}{
		{name: "trim and full width", input: "  Ａb  ", display: "Ab", key: "ab"},
		{name: "unicode decimal digits", input: "玩家١٢", display: "玩家١٢", key: "玩家١٢"},
		{name: "nfkc digit folding", input: "A９界2", display: "A9界2", key: "a9界2"},
		{name: "maximum code points", input: "界A9玩", display: "界A9玩", key: "界a9玩"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			username, err := ParseUsername(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if got := username.Display(); got != test.display {
				t.Fatalf("display = %q, want %q", got, test.display)
			}
			if got := username.Key(); got != test.key {
				t.Fatalf("key = %q, want %q", got, test.key)
			}
			if got := username.CodePointCount(); got < MinimumUsernameCodePoints || got > MaximumUsernameCodePoints {
				t.Fatalf("code point count = %d, outside allowed range", got)
			}
		})
	}
}

func TestUsernameCaseVariantsShareClaimKey(t *testing.T) {
	first, err := ParseUsername("Ab")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ParseUsername("ａＢ")
	if err != nil {
		t.Fatal(err)
	}
	if first.Key() != second.Key() {
		t.Fatalf("case variants produced keys %q and %q", first.Key(), second.Key())
	}
}

func TestParseUsernameRejectsInvalidSyntax(t *testing.T) {
	tests := []struct {
		name  string
		input string
		err   error
	}{
		{name: "empty", input: "  \u3000 ", err: ErrUsernameLength},
		{name: "too short", input: "你", err: ErrUsernameLength},
		{name: "too long", input: "A9界2玩", err: ErrUsernameLength},
		{name: "far above normalization bound", input: strings.Repeat("Ａ", 1<<16), err: ErrUsernameLength},
		{name: "underscore", input: "ab_1", err: ErrUsernameCharacters},
		{name: "path separator", input: "a/cd", err: ErrUsernameCharacters},
		{name: "emoji", input: "ab😀", err: ErrUsernameCharacters},
		{name: "internal whitespace", input: "ab c", err: ErrUsernameCharacters},
		{name: "format character", input: "ab\u200d", err: ErrUsernameCharacters},
		{name: "control character", input: "ab\u0000", err: ErrUsernameCharacters},
		{name: "trimmed controls remain forbidden", input: "\tAb\n", err: ErrUsernameCharacters},
		{name: "byte order mark remains forbidden", input: "\ufeffAb", err: ErrUsernameCharacters},
		{name: "compatibility expansion exceeds maximum", input: strings.Repeat("Ⅳ", 5), err: ErrUsernameLength},
		{name: "unsupported script", input: "абв", err: ErrUsernameCharacters},
		{name: "uncomposed mark", input: "q\u0301", err: ErrUsernameCharacters},
		{name: "invalid utf8", input: string([]byte{'a', 0xff, 'b'}), err: ErrUsernameCharacters},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseUsername(test.input)
			if !errors.Is(err, test.err) {
				t.Fatalf("parse error = %v, want %v", err, test.err)
			}
		})
	}
}

func TestUsernameDisplayLimitAllowsFoldedKeyExpansion(t *testing.T) {
	username, err := ParseUsername(strings.Repeat("ß", MaximumUsernameCodePoints))
	if err != nil {
		t.Fatal(err)
	}
	if got := username.CodePointCount(); got != MaximumUsernameCodePoints {
		t.Fatalf("display code point count = %d, want %d", got, MaximumUsernameCodePoints)
	}
	if got := username.Key(); got != strings.Repeat("ss", MaximumUsernameCodePoints) {
		t.Fatalf("folded key = %q", got)
	}
}

func TestUsernameValidatorRejectsReservedAndBlockedNames(t *testing.T) {
	validator, err := NewUsernameValidator(
		[]string{"A9界2"},
		[]string{"玩家"},
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, input := range []string{"Ａ9界2", "官方", "好玩家"} {
		if _, err := validator.Parse(input); !errors.Is(err, ErrUsernameUnavailable) {
			t.Errorf("validator accepted unavailable username %q: %v", input, err)
		}
	}

	username, err := validator.Parse("普通人")
	if err != nil {
		t.Fatal(err)
	}
	if username.Display() != "普通人" {
		t.Fatalf("display = %q, want 普通人", username.Display())
	}
}

func TestIdentifierErrorsDoNotEchoInput(t *testing.T) {
	privateUsername := "private/秘密😀"
	if _, err := ParseUsername(privateUsername); err == nil || strings.Contains(err.Error(), privateUsername) {
		t.Fatalf("username error leaked input: %v", err)
	}

	privatePolicyTerm := "private/policy-value"
	if _, err := NewUsernameValidator(nil, []string{privatePolicyTerm}); err == nil || strings.Contains(err.Error(), privatePolicyTerm) {
		t.Fatalf("policy error leaked input: %v", err)
	}
}

func TestUsernameValidatorRejectsOversizedPolicyBeforeNormalization(t *testing.T) {
	oversized := strings.Repeat("Ａ", 1<<16)
	if _, err := NewUsernameValidator([]string{oversized}, nil); !errors.Is(err, ErrInvalidUsernamePolicy) {
		t.Fatalf("oversized policy error = %v, want ErrInvalidUsernamePolicy", err)
	}
}

func TestUsernameValidatorIsConcurrentSafe(t *testing.T) {
	validator, err := NewUsernameValidator([]string{"A9界2"}, []string{"玩家"})
	if err != nil {
		t.Fatal(err)
	}

	const goroutines = 24
	const parsesPerGoroutine = 100
	var waitGroup sync.WaitGroup
	waitGroup.Add(goroutines)
	for range goroutines {
		go func() {
			defer waitGroup.Done()
			for range parsesPerGoroutine {
				username, parseErr := validator.Parse("A9界2")
				if !errors.Is(parseErr, ErrUsernameUnavailable) {
					t.Errorf("parse: %v", parseErr)
					return
				}
				username, parseErr = validator.Parse("普通人")
				if parseErr != nil {
					t.Errorf("parse: %v", parseErr)
					return
				}
				if username.Key() != "普通人" {
					t.Errorf("key = %q", username.Key())
					return
				}
			}
		}()
	}
	waitGroup.Wait()
}
