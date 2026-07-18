package objectstorage

import (
	"errors"
	"testing"
)

func TestNewKeyRejectsUnsafePaths(t *testing.T) {
	t.Parallel()

	unsafe := []string{"", "/absolute", "trailing/", "double//segment", "a/../b", "a/./b", `a\b`, "控制"}
	for _, candidate := range unsafe {
		candidate := candidate
		t.Run(candidate, func(t *testing.T) {
			t.Parallel()
			if _, err := NewKey(candidate); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("NewKey(%q) error = %v, want ErrInvalidInput", candidate, err)
			}
		})
	}
}

func TestObjectSnapshotsContentAndMetadata(t *testing.T) {
	t.Parallel()

	key, err := NewKey("audit/checkpoints/00000001.pb")
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"Chain-Sequence": "1"}
	metadata, err := NewMetadata(values)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("signed-checkpoint")
	object, err := NewObject(key, content, metadata)
	if err != nil {
		t.Fatal(err)
	}

	content[0] = 'X'
	values["Chain-Sequence"] = "2"
	if got := string(object.Content()); got != "signed-checkpoint" {
		t.Fatalf("Content() = %q, want immutable snapshot", got)
	}
	if got := object.Metadata()["chain-sequence"]; got != "1" {
		t.Fatalf("Metadata() = %q, want canonical immutable snapshot", got)
	}
	if !object.Valid() {
		t.Fatal("object should remain valid after caller mutation")
	}
}

func TestMetadataRejectsReservedDigestKey(t *testing.T) {
	t.Parallel()

	if _, err := NewMetadata(map[string]string{ContentSHA256MetadataKey: "forged"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("NewMetadata() error = %v, want ErrInvalidInput", err)
	}
}
