package main

import (
	"testing"

	"github.com/iFTY-R/game-night/platform/audit"
)

func TestAdminResetAuditTargetUsesStableSystemIdentifier(t *testing.T) {
	target, err := adminResetAuditTarget()
	if err != nil {
		t.Fatal(err)
	}
	if target.Type() != audit.TargetSystem || target.ID() != adminResetAuditTargetID {
		t.Fatalf("reset target = (%v, %q)", target.Type(), target.ID())
	}
}
