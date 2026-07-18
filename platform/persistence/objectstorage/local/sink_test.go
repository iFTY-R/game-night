package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/iFTY-R/game-night/platform/persistence/objectstorage"
)

func TestSinkWriteIsIdempotentAndRejectsDifferentContent(t *testing.T) {
	t.Parallel()

	sink, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := testObject(t, "audit/checkpoints/00000001.pb", "first")
	if err := sink.Write(context.Background(), first); err != nil {
		t.Fatalf("first Write() error = %v", err)
	}
	if err := sink.Write(context.Background(), first); err != nil {
		t.Fatalf("idempotent Write() error = %v", err)
	}

	different := testObject(t, first.Key().String(), "different")
	if err := sink.Write(context.Background(), different); !errors.Is(err, objectstorage.ErrIntegrity) {
		t.Fatalf("conflicting Write() error = %v, want ErrIntegrity", err)
	}
}

func TestSinkConcurrentIdenticalWritesPublishOneCompleteObject(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sink, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	object := testObject(t, "audit/checkpoints/00000002.pb", "checkpoint")

	const writers = 16
	errorsByWriter := make(chan error, writers)
	var group sync.WaitGroup
	for range writers {
		group.Add(1)
		go func() {
			defer group.Done()
			errorsByWriter <- sink.Write(context.Background(), object)
		}()
	}
	group.Wait()
	close(errorsByWriter)
	for err := range errorsByWriter {
		if err != nil {
			t.Fatalf("concurrent Write() error = %v", err)
		}
	}

	entries, err := os.ReadDir(filepath.Join(root, "audit", "checkpoints"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "00000002.pb" {
		t.Fatalf("published entries = %v, want one target and no partial files", entries)
	}
}

func TestSinkRejectsPartialExistingObjectWithoutOverwriting(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	targetDirectory := filepath.Join(root, "audit", "checkpoints")
	if err := os.MkdirAll(targetDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(targetDirectory, "00000003.pb")
	if err := os.WriteFile(target, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	sink, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	if err := sink.Write(context.Background(), testObject(t, "audit/checkpoints/00000003.pb", "complete")); !errors.Is(err, objectstorage.ErrIntegrity) {
		t.Fatalf("Write() error = %v, want ErrIntegrity", err)
	}
	actual, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != "partial" {
		t.Fatalf("existing target was overwritten: %q", actual)
	}
}

func TestSinkRejectsSymlinkPathComponent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "audit")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	sink, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	err = sink.Write(context.Background(), testObject(t, "audit/checkpoints/00000004.pb", "checkpoint"))
	if !errors.Is(err, objectstorage.ErrIntegrity) {
		t.Fatalf("Write() error = %v, want ErrIntegrity", err)
	}
	if entries, readErr := os.ReadDir(outside); readErr != nil || len(entries) != 0 {
		t.Fatalf("outside directory changed: entries=%v error=%v", entries, readErr)
	}
}

func TestSinkIsExplicitlyNonProduction(t *testing.T) {
	t.Parallel()

	sink, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.CheckProductionReady(context.Background()); !errors.Is(err, objectstorage.ErrNonProductionSink) {
		t.Fatalf("CheckProductionReady() error = %v, want ErrNonProductionSink", err)
	}
}

func testObject(t *testing.T, keyValue, content string) objectstorage.Object {
	t.Helper()
	key, err := objectstorage.NewKey(keyValue)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := objectstorage.NewMetadata(map[string]string{"chain-sequence": "1"})
	if err != nil {
		t.Fatal(err)
	}
	object, err := objectstorage.NewObject(key, []byte(content), metadata)
	if err != nil {
		t.Fatal(err)
	}
	return object
}
