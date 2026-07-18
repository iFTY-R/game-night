// Package local provides a rooted, append-only checkpoint sink for development
// and tests. It is intentionally incapable of satisfying production readiness.
package local

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iFTY-R/game-night/platform/persistence/objectstorage"
)

const (
	// envelopeMagic versions the local metadata-and-content envelope used for exact read-back verification.
	envelopeMagic = "GNWORM01"
	// temporaryPrefix identifies incomplete files that are never treated as published objects.
	temporaryPrefix = ".worm-tmp-"
	// fileMode keeps local checkpoint payloads private to the development process owner.
	fileMode = 0o600
	// directoryMode prevents other local users from replacing checkpoint path components.
	directoryMode = 0o700
)

// Sink atomically publishes immutable envelope files beneath a confined root.
// The local filesystem cannot guarantee retention against privileged deletion,
// so CheckProductionReady always returns ErrNonProductionSink.
type Sink struct {
	rootPath string
}

// New creates and validates a non-symlink storage root.
func New(rootPath string) (*Sink, error) {
	if strings.TrimSpace(rootPath) == "" {
		return nil, objectstorage.ErrInvalidInput
	}
	absolutePath, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, objectstorage.ErrInvalidInput
	}
	if err := os.MkdirAll(absolutePath, directoryMode); err != nil {
		return nil, objectstorage.ErrUnavailable
	}
	root, err := openVerifiedRoot(absolutePath)
	if err != nil {
		return nil, err
	}
	if err := root.Close(); err != nil {
		return nil, objectstorage.ErrUnavailable
	}
	return &Sink{rootPath: absolutePath}, nil
}

// CheckProductionReady explicitly prevents accidental use of the local sink in production.
func (sink *Sink) CheckProductionReady(context.Context) error {
	return objectstorage.ErrNonProductionSink
}

// Write creates a complete envelope and atomically links it to the deterministic
// target. An existing target is opened without following a final symlink and is
// accepted only when every byte, including digest and metadata, matches.
func (sink *Sink) Write(ctx context.Context, object objectstorage.Object) error {
	if sink == nil || sink.rootPath == "" || !object.Valid() {
		return objectstorage.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	envelope, err := encodeEnvelope(object)
	if err != nil {
		return err
	}
	root, err := openVerifiedRoot(sink.rootPath)
	if err != nil {
		return err
	}
	defer root.Close()

	parent, targetName, closeParent, err := openVerifiedParent(root, object.Key().String())
	if err != nil {
		return err
	}
	if closeParent {
		defer parent.Close()
	}

	temporaryName, temporary, err := createTemporary(parent)
	if err != nil {
		return mapFilesystemError(ctx, err)
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = parent.Remove(temporaryName)
		}
	}()

	if err := writeComplete(temporary, envelope); err != nil {
		_ = temporary.Close()
		return mapFilesystemError(ctx, err)
	}
	if err := temporary.Close(); err != nil {
		return mapFilesystemError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := parent.Link(temporaryName, targetName); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return mapFilesystemError(ctx, err)
		}
		return verifyExisting(parent, targetName, envelope)
	}
	// Persist the published target before removing the temporary name so a crash
	// cannot erase the only durable directory entry after Write reports success.
	if err := syncDirectory(parent); err != nil {
		return mapFilesystemError(ctx, err)
	}
	if err := parent.Remove(temporaryName); err != nil {
		return mapFilesystemError(ctx, err)
	}
	removeTemporary = false
	if err := syncDirectory(parent); err != nil {
		return mapFilesystemError(ctx, err)
	}
	return verifyExisting(parent, targetName, envelope)
}

func encodeEnvelope(object objectstorage.Object) ([]byte, error) {
	metadata, err := json.Marshal(object.Metadata())
	if err != nil {
		return nil, objectstorage.ErrInvalidInput
	}
	content := object.Content()
	digest := object.SHA256()
	capacity := len(envelopeMagic) + 4 + 8 + len(digest) + len(metadata) + len(content)
	buffer := bytes.NewBuffer(make([]byte, 0, capacity))
	buffer.WriteString(envelopeMagic)
	_ = binary.Write(buffer, binary.BigEndian, uint32(len(metadata)))
	_ = binary.Write(buffer, binary.BigEndian, uint64(len(content)))
	buffer.Write(digest[:])
	buffer.Write(metadata)
	buffer.Write(content)
	return buffer.Bytes(), nil
}

func openVerifiedRoot(rootPath string) (*os.Root, error) {
	before, err := os.Lstat(rootPath)
	if err != nil {
		return nil, objectstorage.ErrUnavailable
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
		return nil, objectstorage.ErrIntegrity
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, objectstorage.ErrUnavailable
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		_ = root.Close()
		return nil, objectstorage.ErrIntegrity
	}
	return root, nil
}

func openVerifiedParent(root *os.Root, key string) (*os.Root, string, bool, error) {
	components := strings.Split(key, "/")
	current := root
	owned := false
	for _, component := range components[:len(components)-1] {
		if err := current.Mkdir(component, directoryMode); err != nil && !errors.Is(err, os.ErrExist) {
			if owned {
				_ = current.Close()
			}
			return nil, "", false, objectstorage.ErrUnavailable
		}
		before, err := current.Lstat(component)
		if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
			if owned {
				_ = current.Close()
			}
			return nil, "", false, objectstorage.ErrIntegrity
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			if owned {
				_ = current.Close()
			}
			return nil, "", false, objectstorage.ErrUnavailable
		}
		after, err := next.Stat(".")
		if err != nil || !os.SameFile(before, after) {
			_ = next.Close()
			if owned {
				_ = current.Close()
			}
			return nil, "", false, objectstorage.ErrIntegrity
		}
		if owned {
			_ = current.Close()
		}
		current = next
		owned = true
	}
	return current, components[len(components)-1], owned, nil
}

func createTemporary(parent *os.Root) (string, *os.File, error) {
	for range 8 {
		random := make([]byte, 16)
		if _, err := io.ReadFull(rand.Reader, random); err != nil {
			return "", nil, err
		}
		name := temporaryPrefix + hex.EncodeToString(random)
		file, err := parent.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
		if err == nil {
			return name, file, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", nil, err
		}
	}
	return "", nil, objectstorage.ErrUnavailable
}

func writeComplete(file *os.File, envelope []byte) error {
	if _, err := io.Copy(file, bytes.NewReader(envelope)); err != nil {
		return err
	}
	return file.Sync()
}

func verifyExisting(parent *os.Root, targetName string, expected []byte) error {
	before, err := parent.Lstat(targetName)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Size() != int64(len(expected)) {
		return objectstorage.ErrIntegrity
	}
	file, err := parent.Open(targetName)
	if err != nil {
		return objectstorage.ErrUnavailable
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() {
		return objectstorage.ErrIntegrity
	}
	actual, err := io.ReadAll(io.LimitReader(file, int64(len(expected))+1))
	if err != nil {
		return objectstorage.ErrUnavailable
	}
	if !bytes.Equal(actual, expected) {
		return objectstorage.ErrIntegrity
	}
	return nil
}

func mapFilesystemError(ctx context.Context, err error) error {
	if contextError := ctx.Err(); contextError != nil {
		return contextError
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, objectstorage.ErrIntegrity) || errors.Is(err, objectstorage.ErrInvalidInput) {
		return err
	}
	return objectstorage.ErrUnavailable
}
