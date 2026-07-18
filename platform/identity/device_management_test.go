package identity

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRestoreDeviceSummaryDerivesStatusWithoutSecrets(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	snapshot := DeviceSummarySnapshot{
		CredentialID: uuid.New(), UserID: uuid.New(), Label: "Phone",
		CreatedAt: now.Add(-time.Hour), LastSeenAt: now.Add(-time.Minute),
		IdleExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(24 * time.Hour),
	}
	summary, err := RestoreDeviceSummary(snapshot, now)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != DeviceStateActive || summary.CredentialID != snapshot.CredentialID {
		t.Fatalf("unexpected active summary: %#v", summary)
	}

	expired, err := RestoreDeviceSummary(snapshot, snapshot.IdleExpiresAt)
	if err != nil || expired.Status != DeviceStateExpired {
		t.Fatalf("half-open expiry not applied: summary=%#v err=%v", expired, err)
	}
	revokedSnapshot := snapshot
	revokedSnapshot.RevokedAt = now
	revoked, err := RestoreDeviceSummary(revokedSnapshot, now)
	if err != nil || revoked.Status != DeviceStateRevoked {
		t.Fatalf("revoked status not applied: summary=%#v err=%v", revoked, err)
	}
}

func TestDeviceListRequestValidatesStableCursorAndBounds(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	userID := uuid.New()
	request, err := NewDeviceListRequest(userID, false, DevicePageCursor{}, 50, now)
	if err != nil {
		t.Fatal(err)
	}
	if request.PageSize != 50 || request.After.CreatedAt.IsZero() || request.After.CredentialID != uuid.Nil {
		t.Fatalf("unexpected first-page normalization: %#v", request)
	}
	if _, err := NewDeviceListRequest(userID, false, DevicePageCursor{}, MaximumDevicePageSize+1, now); !errors.Is(err, ErrInvalidDeviceInput) {
		t.Fatalf("oversized page was accepted: %v", err)
	}
	if _, err := NewDeviceListRequest(userID, false, DevicePageCursor{CreatedAt: now}, 10, now); !errors.Is(err, ErrInvalidDeviceInput) {
		t.Fatalf("partial cursor was accepted: %v", err)
	}
}

func TestRecoveryDevicePolicyRejectsUnspecified(t *testing.T) {
	if RecoveryDevicePolicy(0).Valid() || !RecoveryDevicePolicyKeepOtherDevices.Valid() ||
		!RecoveryDevicePolicyRevokeOtherDevices.Valid() {
		t.Fatal("recovery device policy closure is incorrect")
	}
}
