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
		{name: "trim and full width", input: "  Ａlice  ", display: "Alice", key: "alice"},
		{name: "composed latin", input: "A\u030Angstrom", display: "Ångstrom", key: "ångstrom"},
		{name: "case fold expansion", input: "Straße", display: "Straße", key: "strasse"},
		{name: "han digit underscore", input: "玩家_９", display: "玩家_9", key: "玩家_9"},
		{name: "unicode decimal digits", input: "玩家١٢", display: "玩家١٢", key: "玩家١٢"},
		{
			name:    "maximum code points",
			input:   strings.Repeat("界", MaximumUsernameCodePoints),
			display: strings.Repeat("界", MaximumUsernameCodePoints),
			key:     strings.Repeat("界", MaximumUsernameCodePoints),
		},
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
	first, err := ParseUsername("Alice")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ParseUsername("ａｌｉｃｅ")
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
		{name: "empty", input: " \t\n ", err: ErrUsernameLength},
		{name: "too short", input: "a", err: ErrUsernameLength},
		{name: "too long", input: strings.Repeat("界", MaximumUsernameCodePoints+1), err: ErrUsernameLength},
		{name: "far above normalization bound", input: strings.Repeat("Ａ", 1<<16), err: ErrUsernameLength},
		{name: "consecutive underscores", input: "ab__cd", err: ErrUsernameUnderscores},
		{name: "path separator", input: "ab/cd", err: ErrUsernameCharacters},
		{name: "emoji", input: "ab😀", err: ErrUsernameCharacters},
		{name: "internal whitespace", input: "ab cd", err: ErrUsernameCharacters},
		{name: "format character", input: "ab\u200dcd", err: ErrUsernameCharacters},
		{name: "control character", input: "ab\u0000cd", err: ErrUsernameCharacters},
		{name: "unsupported script", input: "абв", err: ErrUsernameCharacters},
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

func TestUsernameValidatorRejectsReservedAndBlockedNames(t *testing.T) {
	validator, err := NewUsernameValidator(
		[]string{"FoundersClub"},
		[]string{"坏词"},
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, input := range []string{"ＡＤＭＩＮ", "官方", "FOUNDERSCLUB", "好坏词名"} {
		if _, err := validator.Parse(input); !errors.Is(err, ErrUsernameUnavailable) {
			t.Errorf("validator accepted unavailable username %q: %v", input, err)
		}
	}

	username, err := validator.Parse("普通玩家")
	if err != nil {
		t.Fatal(err)
	}
	if username.Display() != "普通玩家" {
		t.Fatalf("display = %q, want 普通玩家", username.Display())
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
	validator, err := NewUsernameValidator([]string{"FoundersClub"}, []string{"坏词"})
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
				username, parseErr := validator.Parse("普通_Player9")
				if parseErr != nil {
					t.Errorf("parse: %v", parseErr)
					return
				}
				if username.Key() != "普通_player9" {
					t.Errorf("key = %q", username.Key())
					return
				}
			}
		}()
	}
	waitGroup.Wait()
}
