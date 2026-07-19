package profile

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	// DefaultExportTTL keeps materialized snapshots short-lived while allowing several unary pages.
	DefaultExportTTL = 15 * time.Minute
	// MaximumExportPageSize bounds database work and response memory independently of transport limits.
	MaximumExportPageSize = 100
	// MaximumExportReasonBytes bounds audit-linked operator explanations.
	MaximumExportReasonBytes = 512
)

// ExportStatus is the closed lifecycle of a materialized profile export.
type ExportStatus string

const (
	ExportStatusActive    ExportStatus = "active"
	ExportStatusCompleted ExportStatus = "completed"
	ExportStatusAborted   ExportStatus = "aborted"
	ExportStatusExpired   ExportStatus = "expired"
)

// Valid rejects unrecognized persisted export states.
func (status ExportStatus) Valid() bool {
	switch status {
	case ExportStatusActive, ExportStatusCompleted, ExportStatusAborted, ExportStatusExpired:
		return true
	default:
		return false
	}
}

// ProfileExportContextSnapshot is the persistence-neutral export state and audit scope.
type ProfileExportContextSnapshot struct {
	ExportID         uuid.UUID
	CreatedByAdminID uuid.UUID
	FilterDigest     []byte
	RequestedFields  []Field
	SchemaVersion    uint32
	ItemCount        uint64
	Status           ExportStatus
	Reason           string
	CreatedAt        time.Time
	ExpiresAt        time.Time
	CompletedAt      time.Time
	AbortedAt        time.Time
	ExpiredAt        time.Time
}

// ProfileExportContext is an immutable state machine for one materialized export.
type ProfileExportContext struct{ snapshot ProfileExportContextSnapshot }

// NewProfileExportContext creates an active export with a caller-chosen expiry boundary.
func NewProfileExportContext(exportID, adminID uuid.UUID, filterDigest []byte, fields []Field, schemaVersion uint32, itemCount uint64, reason string, createdAt, expiresAt time.Time) (ProfileExportContext, error) {
	return RestoreProfileExportContext(ProfileExportContextSnapshot{
		ExportID: exportID, CreatedByAdminID: adminID, FilterDigest: filterDigest,
		RequestedFields: fields, SchemaVersion: schemaVersion, ItemCount: itemCount,
		Status: ExportStatusActive, Reason: reason, CreatedAt: createdAt, ExpiresAt: expiresAt,
	})
}

// RestoreProfileExportContext validates state read from the export context table.
func RestoreProfileExportContext(snapshot ProfileExportContextSnapshot) (ProfileExportContext, error) {
	snapshot.FilterDigest = bytes.Clone(snapshot.FilterDigest)
	snapshot.RequestedFields = append([]Field(nil), snapshot.RequestedFields...)
	snapshot.CreatedAt = canonicalProfileTime(snapshot.CreatedAt)
	snapshot.ExpiresAt = canonicalProfileTime(snapshot.ExpiresAt)
	snapshot.CompletedAt = canonicalOptionalProfileTime(snapshot.CompletedAt)
	snapshot.AbortedAt = canonicalOptionalProfileTime(snapshot.AbortedAt)
	snapshot.ExpiredAt = canonicalOptionalProfileTime(snapshot.ExpiredAt)
	if snapshot.ExportID == uuid.Nil || snapshot.CreatedByAdminID == uuid.Nil || len(snapshot.FilterDigest) != sha256.Size ||
		snapshot.SchemaVersion == 0 || snapshot.SchemaVersion > math.MaxInt32 || !snapshot.Status.Valid() ||
		snapshot.ItemCount > math.MaxInt64 || snapshot.CreatedAt.IsZero() || !snapshot.ExpiresAt.After(snapshot.CreatedAt) || !validExportReason(snapshot.Reason) ||
		!validExportFields(snapshot.RequestedFields) {
		return ProfileExportContext{}, ErrInvalidProfileInput
	}
	if err := validateExportTerminalTimes(snapshot); err != nil {
		return ProfileExportContext{}, err
	}
	return ProfileExportContext{snapshot: snapshot}, nil
}

// Snapshot returns a deep copy suitable for persistence adapters.
func (context ProfileExportContext) Snapshot() ProfileExportContextSnapshot {
	snapshot := context.snapshot
	snapshot.FilterDigest = bytes.Clone(snapshot.FilterDigest)
	snapshot.RequestedFields = append([]Field(nil), snapshot.RequestedFields...)
	return snapshot
}

// ExportID returns the stable identifier used to bind page cursors and audit entries.
func (context ProfileExportContext) ExportID() uuid.UUID { return context.snapshot.ExportID }

// Status returns the current terminal-state marker.
func (context ProfileExportContext) Status() ExportStatus { return context.snapshot.Status }

// ExpiresAt returns the half-open expiry boundary.
func (context ProfileExportContext) ExpiresAt() time.Time { return context.snapshot.ExpiresAt }

// IsReadableAt reports whether a page may be read at the supplied application time.
func (context ProfileExportContext) IsReadableAt(at time.Time) bool {
	at = canonicalProfileTime(at)
	return context.snapshot.Status == ExportStatusActive && !at.IsZero() &&
		!at.Before(context.snapshot.CreatedAt) && at.Before(context.snapshot.ExpiresAt)
}

// Complete transitions an active context to completed before its expiry boundary.
func (context ProfileExportContext) Complete(at time.Time) (ProfileExportContext, error) {
	if context.snapshot.Status == ExportStatusCompleted {
		return context, nil
	}
	if context.snapshot.Status != ExportStatusActive {
		return ProfileExportContext{}, ErrProfileExportClosed
	}
	at = canonicalProfileTime(at)
	if at.IsZero() || !at.Before(context.snapshot.ExpiresAt) {
		return ProfileExportContext{}, ErrProfileExportExpired
	}
	if at.Before(context.snapshot.CreatedAt) {
		return ProfileExportContext{}, ErrProfileConcurrentTransition
	}
	snapshot := context.Snapshot()
	snapshot.Status, snapshot.CompletedAt = ExportStatusCompleted, at
	return RestoreProfileExportContext(snapshot)
}

// Abort transitions an active context to aborted before expiry.
func (context ProfileExportContext) Abort(at time.Time) (ProfileExportContext, error) {
	if context.snapshot.Status == ExportStatusAborted {
		return context, nil
	}
	if context.snapshot.Status != ExportStatusActive {
		return ProfileExportContext{}, ErrProfileExportClosed
	}
	at = canonicalProfileTime(at)
	if at.IsZero() || !at.Before(context.snapshot.ExpiresAt) {
		return ProfileExportContext{}, ErrProfileExportExpired
	}
	if at.Before(context.snapshot.CreatedAt) {
		return ProfileExportContext{}, ErrProfileConcurrentTransition
	}
	snapshot := context.Snapshot()
	snapshot.Status, snapshot.AbortedAt = ExportStatusAborted, at
	return RestoreProfileExportContext(snapshot)
}

// Expire transitions an active context at or after its expiry boundary.
func (context ProfileExportContext) Expire(at time.Time) (ProfileExportContext, error) {
	if context.snapshot.Status == ExportStatusExpired {
		return context, nil
	}
	if context.snapshot.Status != ExportStatusActive {
		return ProfileExportContext{}, ErrProfileExportClosed
	}
	at = canonicalProfileTime(at)
	if at.IsZero() || at.Before(context.snapshot.ExpiresAt) {
		return ProfileExportContext{}, ErrProfileExportNotExpired
	}
	snapshot := context.Snapshot()
	snapshot.Status, snapshot.ExpiredAt = ExportStatusExpired, at
	return RestoreProfileExportContext(snapshot)
}

// ProfileExportItemSnapshot stores one immutable row of an export materialization.
type ProfileExportItemSnapshot struct {
	ExportID           uuid.UUID
	Ordinal            uint64
	UserID             uuid.UUID
	Username           string
	ProfileVersion     uint64
	RealNameCiphertext []byte
	RealNameNonce      []byte
	RealNameKeyVersion uint32
}

// ProfileExportItem is a validated, plaintext-free export row.
type ProfileExportItem struct{ snapshot ProfileExportItemSnapshot }

// ProfileExportSource is the identity query result used to materialize one stable export row.
// RealName is optional because users may exist before a profile is populated.
type ProfileExportSource struct {
	UserID         uuid.UUID
	Username       string
	ProfileVersion uint64
	RealName       *EncryptedValue
}

// ExportSource is kept as a short alias for source-oriented callers.
type ExportSource = ProfileExportSource

// RestoreExportSource validates the non-PII source fields before snapshot materialization.
func RestoreExportSource(source ExportSource) (ExportSource, error) {
	if source.UserID == uuid.Nil || !validExportUsername(source.Username) {
		return ExportSource{}, ErrInvalidProfileInput
	}
	if source.ProfileVersion == 0 {
		if source.RealName != nil {
			return ExportSource{}, ErrInvalidProfileInput
		}
	} else if source.RealName == nil {
		return ExportSource{}, ErrInvalidProfileInput
	} else if _, err := RestoreEncryptedValue(*source.RealName); err != nil {
		return ExportSource{}, err
	}
	if source.RealName != nil {
		copy := source.RealName.Clone()
		source.RealName = &copy
	}
	return source, nil
}

// Materialize converts a validated source into an immutable encrypted export row.
func (source ProfileExportSource) Materialize(exportID uuid.UUID, ordinal uint64) (ProfileExportItem, error) {
	validated, err := RestoreExportSource(source)
	if err != nil {
		return ProfileExportItem{}, err
	}
	return NewProfileExportItem(exportID, ordinal, validated.UserID, validated.Username, validated.ProfileVersion, validated.RealName)
}

// NewProfileExportItem creates a row; a zero profile version represents a user without profile data.
func NewProfileExportItem(exportID uuid.UUID, ordinal uint64, userID uuid.UUID, username string, profileVersion uint64, value *EncryptedValue) (ProfileExportItem, error) {
	snapshot := ProfileExportItemSnapshot{ExportID: exportID, Ordinal: ordinal, UserID: userID, Username: username, ProfileVersion: profileVersion}
	if value != nil {
		validated, err := RestoreEncryptedValue(*value)
		if err != nil {
			return ProfileExportItem{}, err
		}
		snapshot.RealNameCiphertext, snapshot.RealNameNonce, snapshot.RealNameKeyVersion = validated.Ciphertext, validated.Nonce, validated.KeyVersion
	}
	return RestoreProfileExportItem(snapshot)
}

// RestoreProfileExportItem validates a materialized row read from PostgreSQL.
func RestoreProfileExportItem(snapshot ProfileExportItemSnapshot) (ProfileExportItem, error) {
	snapshot.RealNameCiphertext = bytes.Clone(snapshot.RealNameCiphertext)
	snapshot.RealNameNonce = bytes.Clone(snapshot.RealNameNonce)
	if snapshot.ExportID == uuid.Nil || snapshot.Ordinal == 0 || snapshot.UserID == uuid.Nil || !validExportUsername(snapshot.Username) {
		return ProfileExportItem{}, ErrInvalidProfileInput
	}
	if snapshot.ProfileVersion == 0 {
		if len(snapshot.RealNameCiphertext) != 0 || len(snapshot.RealNameNonce) != 0 || snapshot.RealNameKeyVersion != 0 {
			return ProfileExportItem{}, ErrInvalidProfileInput
		}
	} else if _, err := RestoreEncryptedValue(EncryptedValue{KeyVersion: snapshot.RealNameKeyVersion, Nonce: snapshot.RealNameNonce, Ciphertext: snapshot.RealNameCiphertext}); err != nil {
		return ProfileExportItem{}, ErrInvalidProfileInput
	}
	return ProfileExportItem{snapshot: snapshot}, nil
}

// Snapshot returns a deep copy for export repositories.
func (item ProfileExportItem) Snapshot() ProfileExportItemSnapshot {
	snapshot := item.snapshot
	snapshot.RealNameCiphertext = bytes.Clone(snapshot.RealNameCiphertext)
	snapshot.RealNameNonce = bytes.Clone(snapshot.RealNameNonce)
	return snapshot
}

// EncryptedRealName returns nil for rows that were materialized without a profile.
func (item ProfileExportItem) EncryptedRealName() *EncryptedValue {
	if item.snapshot.ProfileVersion == 0 {
		return nil
	}
	value := EncryptedValue{KeyVersion: item.snapshot.RealNameKeyVersion, Nonce: bytes.Clone(item.snapshot.RealNameNonce), Ciphertext: bytes.Clone(item.snapshot.RealNameCiphertext)}
	return &value
}

// ExportCursor binds an ordinal to one export context to prevent cross-export cursor substitution.
type ExportCursor struct {
	ExportID uuid.UUID
	Ordinal  uint64
}

// NewExportCursor validates a keyset position, where ordinal zero means the first page.
func NewExportCursor(exportID uuid.UUID, ordinal uint64) (ExportCursor, error) {
	if exportID == uuid.Nil {
		return ExportCursor{}, ErrProfileExportCursor
	}
	return ExportCursor{ExportID: exportID, Ordinal: ordinal}, nil
}

// Encode serializes the cursor as canonical unpadded Base64URL binary.
func (cursor ExportCursor) Encode() (string, error) {
	if cursor.ExportID == uuid.Nil {
		return "", ErrProfileExportCursor
	}
	var raw [25]byte
	raw[0] = 1
	copy(raw[1:17], cursor.ExportID[:])
	binary.BigEndian.PutUint64(raw[17:], cursor.Ordinal)
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// DecodeExportCursor parses only canonical cursor bytes and rejects trailing or alternate encodings.
func DecodeExportCursor(encoded string) (ExportCursor, error) {
	if encoded == "" || len(encoded) > 64 {
		return ExportCursor{}, ErrProfileExportCursor
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || len(raw) != 25 || base64.RawURLEncoding.EncodeToString(raw) != encoded || raw[0] != 1 {
		return ExportCursor{}, ErrProfileExportCursor
	}
	exportID, err := uuid.FromBytes(raw[1:17])
	if err != nil || exportID == uuid.Nil {
		return ExportCursor{}, ErrProfileExportCursor
	}
	return ExportCursor{ExportID: exportID, Ordinal: binary.BigEndian.Uint64(raw[17:])}, nil
}

// EncodeCursor is a convenience wrapper for callers that have an export ID and ordinal.
func EncodeCursor(exportID uuid.UUID, ordinal uint64) (string, error) {
	cursor, err := NewExportCursor(exportID, ordinal)
	if err != nil {
		return "", err
	}
	return cursor.Encode()
}

// TargetUserDigest returns a stable SHA-256 digest over a sorted, duplicate-free user set.
func TargetUserDigest(userIDs []uuid.UUID) ([]byte, error) {
	ids := append([]uuid.UUID(nil), userIDs...)
	for _, id := range ids {
		if id == uuid.Nil {
			return nil, ErrInvalidProfileInput
		}
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left].String() < ids[right].String() })
	for index := 1; index < len(ids); index++ {
		if ids[index] == ids[index-1] {
			return nil, ErrInvalidProfileInput
		}
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte("game-night:profile:target-users:v1\x00"))
	for _, id := range ids {
		_, _ = digest.Write([]byte(id.String()))
		_, _ = digest.Write([]byte{0})
	}
	return digest.Sum(nil), nil
}

func validExportFields(fields []Field) bool {
	if len(fields) == 0 {
		return false
	}
	seen := make(map[Field]struct{}, len(fields))
	for _, field := range fields {
		if !field.Valid() {
			return false
		}
		if _, exists := seen[field]; exists {
			return false
		}
		seen[field] = struct{}{}
	}
	return true
}

func validExportReason(reason string) bool {
	if len(reason) == 0 || len(reason) > MaximumExportReasonBytes || !utf8.ValidString(reason) || strings.TrimSpace(reason) != reason {
		return false
	}
	for _, character := range reason {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return false
		}
	}
	return true
}

func validExportUsername(username string) bool {
	// Deleted identities intentionally materialize with an empty username because
	// the identity aggregate clears its current claim before terminal deletion.
	if len(username) > 256 || !utf8.ValidString(username) || strings.TrimSpace(username) != username {
		return false
	}
	for _, character := range username {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return false
		}
	}
	return true
}

func validateExportTerminalTimes(snapshot ProfileExportContextSnapshot) error {
	switch snapshot.Status {
	case ExportStatusActive:
		if !snapshot.CompletedAt.IsZero() || !snapshot.AbortedAt.IsZero() || !snapshot.ExpiredAt.IsZero() {
			return ErrInvalidProfileInput
		}
	case ExportStatusCompleted:
		if snapshot.CompletedAt.IsZero() || !snapshot.AbortedAt.IsZero() || !snapshot.ExpiredAt.IsZero() || snapshot.CompletedAt.Before(snapshot.CreatedAt) || !snapshot.CompletedAt.Before(snapshot.ExpiresAt) {
			return ErrInvalidProfileInput
		}
	case ExportStatusAborted:
		if snapshot.AbortedAt.IsZero() || !snapshot.CompletedAt.IsZero() || !snapshot.ExpiredAt.IsZero() || snapshot.AbortedAt.Before(snapshot.CreatedAt) || !snapshot.AbortedAt.Before(snapshot.ExpiresAt) {
			return ErrInvalidProfileInput
		}
	case ExportStatusExpired:
		if snapshot.ExpiredAt.IsZero() || !snapshot.CompletedAt.IsZero() || !snapshot.AbortedAt.IsZero() || snapshot.ExpiredAt.Before(snapshot.ExpiresAt) {
			return ErrInvalidProfileInput
		}
	default:
		return ErrInvalidProfileInput
	}
	return nil
}

func canonicalOptionalProfileTime(value time.Time) time.Time { return canonicalProfileTime(value) }
