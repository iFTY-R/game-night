package identifier

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestSelectorRoundTrip(t *testing.T) {
	for _, size := range []int{MinimumSelectorBytes, MaximumSelectorBytes} {
		raw := bytes.Repeat([]byte{0xa5}, size)
		selector, err := NewSelector(raw)
		if err != nil {
			t.Fatal(err)
		}
		if selector.ByteLength() != len(raw) {
			t.Fatalf("selector byte length = %d, want %d", selector.ByteLength(), len(raw))
		}

		parsed, err := ParseSelector(selector.Value())
		if err != nil {
			t.Fatal(err)
		}
		if parsed != selector {
			t.Fatalf("parsed selector = %#v, want %#v", parsed, selector)
		}
	}

	selector, err := NewSelector(bytes.Repeat([]byte{0xa5}, MinimumSelectorBytes))
	if err != nil {
		t.Fatal(err)
	}
	if selector.Value() != "paWlpaWlpaWlpaWlpaWlpQ" {
		t.Fatalf("selector value = %q", selector.Value())
	}
}

func TestSelectorRejectsMalformedOrNonCanonicalValues(t *testing.T) {
	validBytes := bytes.Repeat([]byte{0x3c}, MinimumSelectorBytes)
	valid, err := NewSelector(validBytes)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "whitespace", input: " " + valid.Value()},
		{name: "padding", input: valid.Value() + "=="},
		{name: "standard base64 alphabet", input: strings.Repeat("/", 22)},
		{name: "too short", input: "YWJj"},
		{name: "too long", input: strings.Repeat("A", 87)},
		{name: "far above decode bound", input: strings.Repeat("A", 1<<20)},
		{name: "non canonical trailing bits", input: valid.Value()[:len(valid.Value())-1] + "R"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseSelector(test.input)
			if !errors.Is(err, ErrInvalidSelector) {
				t.Fatalf("parse error = %v, want ErrInvalidSelector", err)
			}
			if strings.Contains(err.Error(), test.input) && test.input != "" {
				t.Fatalf("selector error leaked input: %v", err)
			}
		})
	}
}

func TestNewSelectorRejectsUnsafeLengths(t *testing.T) {
	for _, size := range []int{0, MinimumSelectorBytes - 1, MaximumSelectorBytes + 1} {
		_, err := NewSelector(make([]byte, size))
		if !errors.Is(err, ErrInvalidSelector) {
			t.Errorf("size %d error = %v, want ErrInvalidSelector", size, err)
		}
	}
}
