package adminauth

import (
	"slices"
	"testing"
	"time"

	adminv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1/adminv1connect"
	"github.com/iFTY-R/game-night/platform/admin"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestProcedurePoliciesCoverGeneratedAdminAuthServiceExactly(t *testing.T) {
	authService := adminv1.File_platform_admin_v1_admin_auth_proto.Services().ByName("AdminAuthService")
	userService := adminv1.File_platform_admin_v1_admin_user_proto.Services().ByName("AdminUserService")
	assertServicePolicies(t, authService)
	assertServicePolicies(t, userService)
	if len(procedurePolicies) != authService.Methods().Len()+userService.Methods().Len() {
		t.Fatalf("policy count = %d, want %d", len(procedurePolicies), authService.Methods().Len()+userService.Methods().Len())
	}
	if _, ok := policyForProcedure("/platform.admin.v1.AdminAuthService/Unknown"); ok {
		t.Fatal("unknown procedure must not resolve to a policy")
	}
}

func TestPayloadDispatchedUserCommandRequiresARelevantBasePermission(t *testing.T) {
	now := time.Date(2026, time.July, 26, 10, 0, 0, 0, time.UTC)
	issued, _ := newSessionView(t, now, admin.SessionKindFull, false)
	policy := requireProcedurePolicy(t, "/platform.admin.v1.AdminUserService/ExecuteUserCommand")

	roomsOnly, err := admin.NewPermissionSet(admin.PermissionRoomsControl)
	if err != nil {
		t.Fatal(err)
	}
	actor := newPolicyActor(t, issued.Session, roomsOnly)
	if err = enforceStaticPolicy(policy, actor); err != nil {
		t.Fatalf("rooms.control actor was rejected: %v", err)
	}

	readOnly, err := admin.NewPermissionSet(admin.PermissionUsersRead)
	if err != nil {
		t.Fatal(err)
	}
	if err = enforceStaticPolicy(policy, newPolicyActor(t, issued.Session, readOnly)); err == nil {
		t.Fatal("unrelated read permission unexpectedly authorized a user command")
	}
}

func TestUserCommandRequestPolicyEnforcesExactPermissionAndElevation(t *testing.T) {
	now := time.Date(2026, time.July, 26, 10, 30, 0, 0, time.UTC)
	issued, _ := newSessionView(t, now, admin.SessionKindFull, false)
	govern, err := admin.NewPermissionSet(admin.PermissionUsersGovern)
	if err != nil {
		t.Fatal(err)
	}
	governActor := newPolicyActor(t, issued.Session, govern)
	previewPolicy := requireProcedurePolicy(t, adminv1connect.AdminUserServicePreviewUserCommandProcedure)
	executePolicy := requireProcedurePolicy(t, adminv1connect.AdminUserServiceExecuteUserCommandProcedure)
	revokeCommand := &adminv1.AdminUserCommand{Type: adminv1.AdminUserCommandType_ADMIN_USER_COMMAND_TYPE_REVOKE_ALL_DEVICES}
	if err = enforceRequestPolicy(previewPolicy, governActor, &adminv1.PreviewUserCommandRequest{Command: revokeCommand}, now); err != nil {
		t.Fatalf("device-revoke preview required elevation: %v", err)
	}
	if err = enforceRequestPolicy(executePolicy, governActor, &adminv1.ExecuteUserCommandRequest{Command: revokeCommand}, now); err == nil {
		t.Fatal("device-revoke execution succeeded without elevation")
	}
	revokeActor := newPolicyActorWithElevations(t, issued.Session, govern, now, admin.ElevationScopeUsersRevokeDevices)
	if err = enforceRequestPolicy(executePolicy, revokeActor, &adminv1.ExecuteUserCommandRequest{Command: revokeCommand}, now); err != nil {
		t.Fatalf("elevated device revoke was rejected: %v", err)
	}
	deleteActor := newPolicyActorWithElevations(t, issued.Session, govern, now, admin.ElevationScopeUsersDelete)
	deleteCommand := &adminv1.AdminUserCommand{Type: adminv1.AdminUserCommandType_ADMIN_USER_COMMAND_TYPE_DELETE}
	if err = enforceRequestPolicy(executePolicy, deleteActor, &adminv1.ExecuteUserCommandRequest{Command: deleteCommand}, now); err != nil {
		t.Fatalf("elevated deletion was rejected: %v", err)
	}
	rooms, err := admin.NewPermissionSet(admin.PermissionRoomsControl)
	if err != nil {
		t.Fatal(err)
	}
	roomCommand := &adminv1.AdminUserCommand{Type: adminv1.AdminUserCommandType_ADMIN_USER_COMMAND_TYPE_REMOVE_FROM_CURRENT_ROOM}
	if err = enforceRequestPolicy(executePolicy, newPolicyActor(t, issued.Session, rooms), &adminv1.ExecuteUserCommandRequest{Command: roomCommand}, now); err != nil {
		t.Fatalf("rooms.control actor was rejected for room removal: %v", err)
	}
	if err = enforceRequestPolicy(executePolicy, governActor, &adminv1.ExecuteUserCommandRequest{Command: roomCommand}, now); err == nil {
		t.Fatal("users.govern alone unexpectedly authorized room removal")
	}
}

func TestSensitiveUserExportRequestPolicyRequiresAuditElevation(t *testing.T) {
	now := time.Date(2026, time.July, 26, 11, 0, 0, 0, time.UTC)
	issued, _ := newSessionView(t, now, admin.SessionKindFull, false)
	exportPermission, err := admin.NewPermissionSet(admin.PermissionUsersExport)
	if err != nil {
		t.Fatal(err)
	}
	actor := newPolicyActor(t, issued.Session, exportPermission)
	createPolicy := requireProcedurePolicy(t, adminv1connect.AdminUserServiceCreateUserExportProcedure)
	redacted := &adminv1.CreateUserExportRequest{MaskingPolicy: adminv1.AdminUserExportMaskingPolicy_ADMIN_USER_EXPORT_MASKING_POLICY_REDACT_PII}
	if err = enforceRequestPolicy(createPolicy, actor, redacted, now); err != nil {
		t.Fatalf("redacted export required elevation: %v", err)
	}
	sensitive := &adminv1.CreateUserExportRequest{MaskingPolicy: adminv1.AdminUserExportMaskingPolicy_ADMIN_USER_EXPORT_MASKING_POLICY_INCLUDE_AUTHORIZED_PII}
	if err = enforceRequestPolicy(createPolicy, actor, sensitive, now); err == nil {
		t.Fatal("sensitive export succeeded without elevation")
	}
	elevated := newPolicyActorWithElevations(t, issued.Session, exportPermission, now, admin.ElevationScopeAuditExportSensitive)
	if err = enforceRequestPolicy(createPolicy, elevated, sensitive, now); err != nil {
		t.Fatalf("sensitive export was rejected after elevation: %v", err)
	}
	grantPolicy := requireProcedurePolicy(t, adminv1connect.AdminUserServiceCreateExportDownloadGrantProcedure)
	grant := &adminv1.CreateExportDownloadGrantRequest{ExpectedMaskingPolicy: adminv1.AdminUserExportMaskingPolicy_ADMIN_USER_EXPORT_MASKING_POLICY_INCLUDE_AUTHORIZED_PII}
	if err = enforceRequestPolicy(grantPolicy, elevated, grant, now); err != nil {
		t.Fatalf("sensitive download grant was rejected after elevation: %v", err)
	}
}

func newPolicyActor(t *testing.T, session admin.Session, permissions admin.PermissionSet) admin.ActorContext {
	return newPolicyActorWithElevations(t, session, permissions, session.Snapshot().CreatedAt)
}

func newPolicyActorWithElevations(t *testing.T, session admin.Session, permissions admin.PermissionSet, at time.Time, scopes ...admin.ElevationScope) admin.ActorContext {
	t.Helper()
	snapshot := session.Snapshot()
	elevations := make([]admin.Elevation, 0, len(scopes))
	for _, scope := range scopes {
		elevation, err := admin.NewElevation(session, 0, scope, at, at.Add(2*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		elevations = append(elevations, elevation)
	}
	elevationSet, err := admin.NewElevationSet(elevations...)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := admin.NewActorContext(
		snapshot.AdminID,
		snapshot.ID,
		session,
		permissions,
		elevationSet,
		0,
		"policy-request",
		"https://admin.example.test",
		"127.0.0.1",
		"policy-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	return actor
}

func TestAdminUserPoliciesSeparateReadPIIAndHighRiskCommands(t *testing.T) {
	list := requireProcedurePolicy(t, "/platform.admin.v1.AdminUserService/ListUsers")
	if list.permission != "users.read" || list.elevation != "" || list.session != sessionRequirementFull || !list.requiresCSRF {
		t.Fatalf("list policy = %+v", list)
	}
	pii := requireProcedurePolicy(t, "/platform.admin.v1.AdminUserService/GetUserPII")
	if pii.permission != "users.read_pii" || !pii.requiresRequestID || pii.elevation != "" {
		t.Fatalf("PII policy = %+v", pii)
	}
	startBatch := requireProcedurePolicy(t, "/platform.admin.v1.AdminUserService/StartBatchUserOperation")
	if startBatch.permission != "users.govern" || startBatch.elevation != "users.bulk_governance" {
		t.Fatalf("start batch policy = %+v", startBatch)
	}
	if permission, ok := permissionForElevationScope("users.revoke_devices"); !ok || permission != "users.govern" {
		t.Fatalf("revoke-devices permission = %q, ok = %v", permission, ok)
	}
	command := requireProcedurePolicy(t, "/platform.admin.v1.AdminUserService/ExecuteUserCommand")
	if command.permission != "" || !slices.Equal(command.permissionsAny, []admin.Permission{admin.PermissionUsersGovern, admin.PermissionRoomsControl}) {
		t.Fatalf("payload command policy = %+v", command)
	}
}

func assertServicePolicies(t *testing.T, service interface {
	FullName() protoreflect.FullName
	Methods() protoreflect.MethodDescriptors
}) {
	t.Helper()
	if service == nil {
		t.Fatal("admin service descriptor is missing")
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
}

func requireProcedurePolicy(t *testing.T, procedure string) procedurePolicy {
	t.Helper()
	policy, ok := policyForProcedure(procedure)
	if !ok {
		t.Fatalf("missing policy for %s", procedure)
	}
	return policy
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
