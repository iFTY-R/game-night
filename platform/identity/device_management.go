package identity

import (
	"time"

	"github.com/google/uuid"
)

const (
	// MaximumDevicePageSize bounds one user-owned device page and its transaction memory.
	MaximumDevicePageSize uint32 = 100
)

// RecoveryDevicePolicy forces callers to make the retention decision explicitly.
type RecoveryDevicePolicy uint8

const (
	RecoveryDevicePolicyKeepOtherDevices RecoveryDevicePolicy = iota + 1
	RecoveryDevicePolicyRevokeOtherDevices
)

// Valid rejects the transport enum zero value before any recovery state changes.
func (policy RecoveryDevicePolicy) Valid() bool {
	return policy == RecoveryDevicePolicyKeepOtherDevices || policy == RecoveryDevicePolicyRevokeOtherDevices
}

// DeviceSummarySnapshot contains only non-secret persistence fields used for device management.
type DeviceSummarySnapshot struct {
	CredentialID      uuid.UUID
	UserID            uuid.UUID
	Label             string
	CreatedAt         time.Time
	LastSeenAt        time.Time
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
	RevokedAt         time.Time
}

// DeviceSummary is safe for API mapping and deliberately cannot expose token, HMAC, CSRF, or generation material.
type DeviceSummary struct {
	CredentialID      uuid.UUID
	Label             string
	Status            DeviceState
	CurrentDevice     bool
	CreatedAt         time.Time
	LastSeenAt        time.Time
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
	RevokedAt         time.Time

	userID uuid.UUID
}

// RestoreDeviceSummary validates one redacted repository row and derives its half-open status at a fixed read time.
func RestoreDeviceSummary(snapshot DeviceSummarySnapshot, listedAt time.Time) (DeviceSummary, error) {
	snapshot.CreatedAt = canonicalDeviceTime(snapshot.CreatedAt)
	snapshot.LastSeenAt = canonicalDeviceTime(snapshot.LastSeenAt)
	snapshot.IdleExpiresAt = canonicalDeviceTime(snapshot.IdleExpiresAt)
	snapshot.AbsoluteExpiresAt = canonicalDeviceTime(snapshot.AbsoluteExpiresAt)
	snapshot.RevokedAt = canonicalDeviceOptionalTime(snapshot.RevokedAt)
	listedAt = canonicalDeviceTime(listedAt)
	label, err := normalizeDeviceLabel(snapshot.Label)
	if err != nil || label != snapshot.Label || snapshot.CredentialID == uuid.Nil || snapshot.UserID == uuid.Nil ||
		snapshot.CreatedAt.IsZero() || snapshot.LastSeenAt.Before(snapshot.CreatedAt) ||
		!snapshot.LastSeenAt.Before(snapshot.IdleExpiresAt) || !snapshot.CreatedAt.Before(snapshot.AbsoluteExpiresAt) ||
		!snapshot.LastSeenAt.Before(snapshot.AbsoluteExpiresAt) || listedAt.Before(snapshot.CreatedAt) ||
		(!snapshot.RevokedAt.IsZero() && snapshot.RevokedAt.Before(snapshot.LastSeenAt)) {
		return DeviceSummary{}, ErrInvalidDeviceInput
	}
	status := DeviceStateActive
	if !snapshot.RevokedAt.IsZero() {
		status = DeviceStateRevoked
	} else if !listedAt.Before(snapshot.IdleExpiresAt) || !listedAt.Before(snapshot.AbsoluteExpiresAt) {
		status = DeviceStateExpired
	}
	return DeviceSummary{
		CredentialID: snapshot.CredentialID, Label: snapshot.Label, Status: status,
		CreatedAt: snapshot.CreatedAt, LastSeenAt: snapshot.LastSeenAt,
		IdleExpiresAt: snapshot.IdleExpiresAt, AbsoluteExpiresAt: snapshot.AbsoluteExpiresAt,
		RevokedAt: snapshot.RevokedAt, userID: snapshot.UserID,
	}, nil
}

// UserID returns the owning aggregate only to domain/adapters; transports should omit it from device payloads.
func (summary DeviceSummary) UserID() uuid.UUID { return summary.userID }

// MarkCurrent returns a copy that identifies the credential used to authenticate this list operation.
func (summary DeviceSummary) MarkCurrent(currentID uuid.UUID) DeviceSummary {
	summary.CurrentDevice = currentID != uuid.Nil && summary.CredentialID == currentID
	return summary
}

// DevicePageCursor is the stable `(created_at, credential_id)` keyset position.
type DevicePageCursor struct {
	CreatedAt    time.Time
	CredentialID uuid.UUID
}

// DeviceListRequest is the validated repository query and fixed status-evaluation timestamp.
type DeviceListRequest struct {
	UserID         uuid.UUID
	IncludeRevoked bool
	After          DevicePageCursor
	PageSize       uint32
	ListedAt       time.Time
}

// NewDeviceListRequest validates pagination before a repository allocates or queries a page.
func NewDeviceListRequest(
	userID uuid.UUID,
	includeRevoked bool,
	after DevicePageCursor,
	pageSize uint32,
	listedAt time.Time,
) (DeviceListRequest, error) {
	listedAt = canonicalDeviceTime(listedAt)
	if userID == uuid.Nil || pageSize == 0 || pageSize > MaximumDevicePageSize || listedAt.IsZero() ||
		(after.CreatedAt.IsZero() != (after.CredentialID == uuid.Nil)) {
		return DeviceListRequest{}, ErrInvalidDeviceInput
	}
	if after.CreatedAt.IsZero() {
		// PostgreSQL timestamptz and UUID tuple comparison needs a concrete lower-bound cursor.
		after.CreatedAt = time.Unix(0, 0).UTC()
	} else {
		after.CreatedAt = canonicalDeviceTime(after.CreatedAt)
		if after.CreatedAt.After(listedAt) {
			return DeviceListRequest{}, ErrInvalidDeviceInput
		}
	}
	return DeviceListRequest{
		UserID: userID, IncludeRevoked: includeRevoked, After: after,
		PageSize: pageSize, ListedAt: listedAt,
	}, nil
}

// NextDeviceCursor returns the terminal item position or zero when the page is empty.
func NextDeviceCursor(devices []DeviceSummary) DevicePageCursor {
	if len(devices) == 0 {
		return DevicePageCursor{}
	}
	last := devices[len(devices)-1]
	return DevicePageCursor{CreatedAt: last.CreatedAt, CredentialID: last.CredentialID}
}
