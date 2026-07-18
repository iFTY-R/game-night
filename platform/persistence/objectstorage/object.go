// Package objectstorage defines the append-only object contract used by durable
// checkpoint dispatchers. Implementations must never expose overwrite or delete
// operations.
package objectstorage

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
)

const (
	// MaximumObjectBytes bounds checkpoint buffering and read-back verification.
	MaximumObjectBytes = 16 << 20
	// ContentSHA256MetadataKey is reserved for the sink-computed payload digest.
	ContentSHA256MetadataKey = "game-night-content-sha256"
	// Internal bounds match the portable S3/filesystem subset and prevent metadata amplification.
	maximumKeyBytes           = 1024
	maximumKeySegmentBytes    = 255
	maximumMetadataEntries    = 32
	maximumMetadataKeyBytes   = 64
	maximumMetadataValueBytes = 1024
	maximumMetadataBytes      = 8 << 10
)

var (
	// ErrInvalidInput reports an object or sink configuration outside the bounded contract.
	ErrInvalidInput = errors.New("object storage input is invalid")
	// ErrIntegrity reports that a deterministic key already contains different or malformed data.
	ErrIntegrity = errors.New("object storage integrity violation")
	// ErrUnavailable hides implementation-specific filesystem or remote service failures.
	ErrUnavailable = errors.New("object storage unavailable")
	// ErrNonProductionSink marks an implementation that must never satisfy production readiness.
	ErrNonProductionSink = errors.New("object storage sink is not production capable")
	// ErrProductionReadiness reports missing immutable-retention guarantees.
	ErrProductionReadiness = errors.New("object storage production readiness failed")
)

// Sink stores an object exactly once and verifies the stored representation.
// Repeating the same object is idempotent; reusing its key for different data fails.
type Sink interface {
	Write(context.Context, Object) error
	CheckProductionReady(context.Context) error
}

// Key is a validated, slash-delimited object key safe for both S3 and rooted local storage.
type Key struct {
	value string
}

// NewKey validates a deterministic object key. Backslashes, empty components,
// traversal components, absolute paths, control bytes, and non-ASCII bytes are rejected.
func NewKey(value string) (Key, error) {
	if value == "" || len(value) > maximumKeyBytes || value[0] == '/' || value[len(value)-1] == '/' ||
		strings.Contains(value, "\\") {
		return Key{}, ErrInvalidInput
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." || len(segment) > maximumKeySegmentBytes {
			return Key{}, ErrInvalidInput
		}
		for index := range len(segment) {
			if !validKeyByte(segment[index]) {
				return Key{}, ErrInvalidInput
			}
		}
	}
	return Key{value: value}, nil
}

// String returns the canonical object key.
func (key Key) String() string {
	return key.value
}

// Valid reports whether the key was created through NewKey.
func (key Key) Valid() bool {
	validated, err := NewKey(key.value)
	return err == nil && validated == key
}

// Metadata is an immutable set of canonical lowercase user metadata.
type Metadata struct {
	values map[string]string
}

// NewMetadata validates metadata against the common safe subset supported by
// local envelopes and S3 user metadata. The sink-owned digest key is reserved.
func NewMetadata(values map[string]string) (Metadata, error) {
	if len(values) > maximumMetadataEntries {
		return Metadata{}, ErrInvalidInput
	}
	canonical := make(map[string]string, len(values))
	totalBytes := 0
	for rawKey, value := range values {
		key := strings.ToLower(rawKey)
		if key == ContentSHA256MetadataKey || !validMetadataKey(key) ||
			len(value) > maximumMetadataValueBytes || !printableASCII(value) {
			return Metadata{}, ErrInvalidInput
		}
		if _, duplicated := canonical[key]; duplicated {
			return Metadata{}, ErrInvalidInput
		}
		totalBytes += len(key) + len(value)
		if totalBytes > maximumMetadataBytes {
			return Metadata{}, ErrInvalidInput
		}
		canonical[key] = value
	}
	return Metadata{values: canonical}, nil
}

// Values returns a defensive copy suitable for an object-store request.
func (metadata Metadata) Values() map[string]string {
	values := make(map[string]string, len(metadata.values))
	for key, value := range metadata.values {
		values[key] = value
	}
	return values
}

// Object is an immutable write request with a sink-computed SHA-256 digest.
type Object struct {
	key      Key
	content  []byte
	metadata Metadata
	digest   [sha256.Size]byte
}

// NewObject snapshots the payload and metadata so caller mutation cannot change
// the bytes associated with a deterministic key while a write is in progress.
func NewObject(key Key, content []byte, metadata Metadata) (Object, error) {
	if !key.Valid() || len(content) == 0 || len(content) > MaximumObjectBytes || metadata.values == nil {
		return Object{}, ErrInvalidInput
	}
	payload := append([]byte(nil), content...)
	canonicalMetadata, err := NewMetadata(metadata.Values())
	if err != nil {
		return Object{}, ErrInvalidInput
	}
	return Object{
		key:      key,
		content:  payload,
		metadata: canonicalMetadata,
		digest:   sha256.Sum256(payload),
	}, nil
}

// Key returns the validated deterministic key.
func (object Object) Key() Key {
	return object.key
}

// Content returns a defensive copy of the payload.
func (object Object) Content() []byte {
	return append([]byte(nil), object.content...)
}

// Metadata returns a defensive copy of user metadata.
func (object Object) Metadata() map[string]string {
	return object.metadata.Values()
}

// SHA256 returns the payload digest computed when the immutable object was created.
func (object Object) SHA256() [sha256.Size]byte {
	return object.digest
}

// Valid reports whether the immutable snapshot still satisfies all invariants.
func (object Object) Valid() bool {
	if !object.key.Valid() || len(object.content) == 0 || len(object.content) > MaximumObjectBytes || object.metadata.values == nil {
		return false
	}
	metadata, err := NewMetadata(object.metadata.Values())
	return err == nil && len(metadata.values) == len(object.metadata.values) && sha256.Sum256(object.content) == object.digest
}

func validKeyByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' || value == '-' || value == '_' || value == '.'
}

func validMetadataKey(value string) bool {
	if value == "" || len(value) > maximumMetadataKeyBytes || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for index := range len(value) {
		current := value[index]
		if current >= 'a' && current <= 'z' || current >= '0' && current <= '9' ||
			current == '-' || current == '_' || current == '.' {
			continue
		}
		return false
	}
	return true
}

func printableASCII(value string) bool {
	for index := range len(value) {
		if value[index] < 0x20 || value[index] > 0x7e {
			return false
		}
	}
	return true
}
