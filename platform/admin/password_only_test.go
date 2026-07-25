package admin

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/security"
)

func TestPasswordLoginSessionKindDependsOnActiveEnrollment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		status              AccountStatus
		hasActiveEnrollment bool
		wantKind            SessionKind
		wantErr             error
	}{
		{name: "setup requires initial password change", status: AccountStatusSetupRequired, wantKind: SessionKindSetupPasswordPending},
		{name: "active without enrollment becomes full", status: AccountStatusActive, wantKind: SessionKindFull},
		{name: "active with enrollment requires mfa", status: AccountStatusActive, hasActiveEnrollment: true, wantKind: SessionKindMFAPending},
		{name: "bootstrap cannot log in", status: AccountStatusBootstrapPending, wantErr: ErrUnavailable},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			kind, err := PasswordLoginSessionKind(test.status, test.hasActiveEnrollment)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if kind != test.wantKind {
				t.Fatalf("kind = %q, want %q", kind, test.wantKind)
			}
		})
	}
}

func TestSetupAndMFAPendingSessionsHaveNoBusinessPermissions(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Truncate(time.Microsecond)
	authorizer := NewAdminAuthorizer()
	for _, kind := range []SessionKind{SessionKindSetupPasswordPending, SessionKindMFAPending} {
		session, err := RestoreSession(SessionSnapshot{
			ID:                uuid.New(),
			AdminID:           uuid.New(),
			Selector:          "AAAAAAAAAAAAAAAAAAAAAA",
			SecretMAC:         security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: 1, Value: make([]byte, 32)},
			CSRFHash:          security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: 1, Value: make([]byte, 32)},
			Kind:              kind,
			AdminVersion:      2,
			PasswordVersion:   1,
			SessionVersion:    1,
			MaxAttempts:       5,
			CreatedAt:         now,
			LastSeenAt:        now,
			IdleExpiresAt:     now.Add(time.Minute),
			AbsoluteExpiresAt: now.Add(time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := authorizer.Authorize(session, PermissionSecurityRead, fixedClock{now: now}); !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("kind %q should not authorize business permissions, got %v", kind, err)
		}
	}
}

func TestAdminAuthorizerFailsClosedForUnknownPermissions(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Truncate(time.Microsecond)
	session, err := RestoreSession(SessionSnapshot{
		ID:                uuid.New(),
		AdminID:           uuid.New(),
		Selector:          "AAAAAAAAAAAAAAAAAAAAAA",
		SecretMAC:         security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: 1, Value: make([]byte, 32)},
		CSRFHash:          security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: 1, Value: make([]byte, 32)},
		Kind:              SessionKindFull,
		AdminVersion:      2,
		PasswordVersion:   1,
		SessionVersion:    4,
		MaxAttempts:       5,
		CreatedAt:         now,
		LastSeenAt:        now,
		IdleExpiresAt:     now.Add(time.Minute),
		AbsoluteExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := NewAdminAuthorizer().Authorize(session, Permission("legacy.permission"), fixedClock{now: now}); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("expected fail-closed denial, got %v", err)
	}
}
