package adminauth

import (
	"testing"

	adminv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1"
)

func TestProcedurePoliciesCoverGeneratedAdminAuthServiceExactly(t *testing.T) {
	service := adminv1.File_platform_admin_v1_admin_auth_proto.Services().ByName("AdminAuthService")
	if service == nil {
		t.Fatal("AdminAuthService descriptor is missing")
	}
	if len(procedurePolicies) != service.Methods().Len() {
		t.Fatalf("policy count = %d, want %d", len(procedurePolicies), service.Methods().Len())
	}
	for index := range service.Methods().Len() {
		procedure := "/" + string(service.FullName()) + "/" + string(service.Methods().Get(index).Name())
		policy, ok := policyForProcedure(procedure)
		if !ok {
			t.Fatalf("missing policy for %s", procedure)
		}
		if policy.procedure != procedure {
			t.Fatalf("policy procedure = %s, want %s", policy.procedure, procedure)
		}
	}
	if _, ok := policyForProcedure("/platform.admin.v1.AdminAuthService/Unknown"); ok {
		t.Fatal("unknown procedure must not resolve to a policy")
	}
}

func TestBulkSessionRevokeKeepsPermissionAndElevationSeparate(t *testing.T) {
	preview, ok := policyForProcedure("/platform.admin.v1.AdminAuthService/PreviewRevokeOtherAdminSessions")
	if !ok {
		t.Fatal("missing preview policy")
	}
	if preview.permission != "security.manage_sessions" {
		t.Fatalf("preview permission = %s", preview.permission)
	}
	if preview.elevation != "" {
		t.Fatalf("preview elevation = %s, want none", preview.elevation)
	}
	revoke, ok := policyForProcedure("/platform.admin.v1.AdminAuthService/RevokeOtherAdminSessions")
	if !ok {
		t.Fatal("missing revoke-other policy")
	}
	if revoke.permission != "security.manage_sessions" {
		t.Fatalf("revoke-other permission = %s", revoke.permission)
	}
	if revoke.elevation != "security.revoke_sessions" {
		t.Fatalf("revoke-other elevation = %s", revoke.elevation)
	}
}
