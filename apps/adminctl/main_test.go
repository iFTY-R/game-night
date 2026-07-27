package main

import (
	"context"
	"testing"

	"github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/audit"
)

func TestAdminResetPasswordHasherAcceptsInitialHash(t *testing.T) {
	hasher, err := newAdminResetPasswordHasher()
	if err != nil {
		t.Fatal(err)
	}
	defer hasher.Close()
	record, err := admin.HashPassword(context.Background(), hasher, admin.DefaultPasswordPolicy(), "admin", "admin-reset-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if record.Hash == "" {
		t.Fatal("expected password hash")
	}
}

func TestAdminResetAuditTargetUsesStableSystemIdentifier(t *testing.T) {
	target, err := adminResetAuditTarget()
	if err != nil {
		t.Fatal(err)
	}
	if target.Type() != audit.TargetSystem || target.ID() != adminResetAuditTargetID {
		t.Fatalf("reset target = (%v, %q)", target.Type(), target.ID())
	}
}
