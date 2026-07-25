package admin

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/security"
)

func TestElevationGrantBindsVersionsScopeAndTTL(t *testing.T) {
	now := time.Date(2026, 7, 19, 15, 0, 0, 0, time.UTC)
	session := mustRestoreTestSession(t, SessionSnapshot{
		ID:                uuid.New(),
		AdminID:           uuid.New(),
		Selector:          "AAAAAAAAAAAAAAAAAAAAAA",
		SecretMAC:         security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: 1, Value: make([]byte, 32)},
		CSRFHash:          security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: 1, Value: make([]byte, 32)},
		Kind:              SessionKindFull,
		AdminVersion:      7,
		PasswordVersion:   3,
		SessionVersion:    11,
		MaxAttempts:       5,
		CreatedAt:         now,
		LastSeenAt:        now,
		IdleExpiresAt:     now.Add(10 * time.Minute),
		AbsoluteExpiresAt: now.Add(time.Hour),
	})
	grant, err := NewElevation(session, 9, ElevationScopeSecurityDisableMFA, now, now.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := grant.Validate(session, 9, ElevationScopeSecurityDisableMFA, now.Add(time.Minute)); err != nil {
		t.Fatalf("expected valid grant, got %v", err)
	}
	if err := grant.Validate(session, 9, ElevationScopeSecurityDisableMFA, now.Add(-time.Second)); !errors.Is(err, ErrElevationDenied) {
		t.Fatalf("expected pre-issuance denial, got %v", err)
	}
	if err := grant.Validate(session, 9, ElevationScopeSecurityDisableMFA, now.Add(6*time.Minute)); !errors.Is(err, ErrElevationExpired) {
		t.Fatalf("expected ttl expiry, got %v", err)
	}
	if err := grant.Validate(session, 10, ElevationScopeSecurityDisableMFA, now.Add(time.Minute)); !errors.Is(err, ErrElevationDenied) {
		t.Fatalf("expected enrollment version mismatch denial, got %v", err)
	}
	mutatedSession := mustRestoreTestSession(t, func() SessionSnapshot {
		snapshot := session.Snapshot()
		snapshot.SessionVersion++
		return snapshot
	}())
	if err := grant.Validate(mutatedSession, 9, ElevationScopeSecurityDisableMFA, now.Add(time.Minute)); !errors.Is(err, ErrElevationDenied) {
		t.Fatalf("expected session version mismatch denial, got %v", err)
	}
	revokedSession, err := session.Revoke("test security change", now.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := grant.Validate(revokedSession, 9, ElevationScopeSecurityDisableMFA, now.Add(time.Minute)); !errors.Is(err, ErrElevationDenied) {
		t.Fatalf("expected revoked-session denial, got %v", err)
	}
	adminVersionChanged := mustRestoreTestSession(t, func() SessionSnapshot {
		snapshot := session.Snapshot()
		snapshot.AdminVersion++
		return snapshot
	}())
	if err := grant.Validate(adminVersionChanged, 9, ElevationScopeSecurityDisableMFA, now.Add(time.Minute)); !errors.Is(err, ErrElevationDenied) {
		t.Fatalf("expected admin-version mismatch denial, got %v", err)
	}
	passwordChanged := mustRestoreTestSession(t, func() SessionSnapshot {
		snapshot := session.Snapshot()
		snapshot.PasswordVersion++
		return snapshot
	}())
	if err := grant.Validate(passwordChanged, 9, ElevationScopeSecurityDisableMFA, now.Add(time.Minute)); !errors.Is(err, ErrElevationDenied) {
		t.Fatalf("expected password-version mismatch denial, got %v", err)
	}
	revokedGrant, err := grant.Revoke(now.Add(30 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := revokedGrant.Validate(session, 9, ElevationScopeSecurityDisableMFA, now.Add(time.Minute)); !errors.Is(err, ErrElevationDenied) {
		t.Fatalf("expected explicitly revoked grant denial, got %v", err)
	}
	if _, err := NewElevation(session, 9, ElevationScopeSecurityDisableMFA, now, now.Add(6*time.Minute)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ttl cap rejection, got %v", err)
	}
}

func TestRecoveryCodeSubstitutionIsScopeBound(t *testing.T) {
	t.Parallel()

	if !ElevationScopeSecurityDisableMFA.AllowsRecoveryCodeSubstitution() {
		t.Fatal("disable_mfa must allow recovery-code substitution")
	}
	if !ElevationScopeSecurityRegenerateRecoveryCodes.AllowsRecoveryCodeSubstitution() {
		t.Fatal("regenerate_recovery_codes must allow recovery-code substitution")
	}
	for _, scope := range []ElevationScope{
		ElevationScopeSecurityRevokeSessions,
		ElevationScopeUsersDelete,
		ElevationScopeAuditExportSensitive,
		ElevationScope("unknown.scope"),
	} {
		if scope.AllowsRecoveryCodeSubstitution() {
			t.Fatalf("scope %q must reject recovery-code substitution", scope)
		}
	}
}

func TestActorContextRequireElevationUsesImmutableSets(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 19, 16, 0, 0, 0, time.UTC)
	session := mustRestoreTestSession(t, SessionSnapshot{
		ID:                uuid.New(),
		AdminID:           uuid.New(),
		Selector:          "AAAAAAAAAAAAAAAAAAAAAA",
		SecretMAC:         security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: 1, Value: make([]byte, 32)},
		CSRFHash:          security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: 1, Value: make([]byte, 32)},
		Kind:              SessionKindFull,
		AdminVersion:      4,
		PasswordVersion:   2,
		SessionVersion:    8,
		MaxAttempts:       5,
		CreatedAt:         now,
		LastSeenAt:        now,
		IdleExpiresAt:     now.Add(10 * time.Minute),
		AbsoluteExpiresAt: now.Add(time.Hour),
	})
	permissions, err := NewPermissionSet(PermissionSecurityRead)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := NewElevation(session, 6, ElevationScopeSecurityRegenerateRecoveryCodes, now, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	elevations, err := NewElevationSet(grant)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := NewActorContext(session.Snapshot().AdminID, session.Snapshot().ID, session, permissions, elevations, 6, "req-1", "https://admin.example.test", "203.0.113.10", "GameNightAdmin/1.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := actor.Require(PermissionSecurityRead); err != nil {
		t.Fatalf("expected permission success, got %v", err)
	}
	if err := actor.Require(PermissionOperationsMaintain); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("expected permission denial, got %v", err)
	}
	if err := actor.RequireElevation(ElevationScopeSecurityRegenerateRecoveryCodes, now.Add(time.Minute)); err != nil {
		t.Fatalf("expected elevation success, got %v", err)
	}
	if err := actor.RequireElevation(ElevationScopeSecurityRevokeSessions, now.Add(time.Minute)); !errors.Is(err, ErrElevationDenied) {
		t.Fatalf("expected scope denial, got %v", err)
	}
}

func mustRestoreTestSession(t *testing.T, snapshot SessionSnapshot) Session {
	t.Helper()

	session, err := RestoreSession(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return session
}
