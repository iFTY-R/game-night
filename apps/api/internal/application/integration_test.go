package application

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/apps/api/internal/bootstrap"
	apiConfig "github.com/iFTY-R/game-night/apps/api/internal/config"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/adminauth"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/cookies"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/csrf"
	identitytransport "github.com/iFTY-R/game-night/apps/api/internal/transport/identity"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/logging"
	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
	adminv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1/adminv1connect"
	commonv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/common/v1"
	identityv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/identity/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/identity/v1/identityv1connect"
	roomv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/room/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/room/v1/roomv1connect"
	"github.com/iFTY-R/game-night/internal/integrationtest"
	"github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/pressly/goose/v3"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// Integration values are deliberate markers used to prove configured secrets and PII never enter structured logs.
	applicationTestDatabaseEnvironment = "GAME_NIGHT_TEST_DATABASE_URL"
	applicationTestRedisEnvironment    = "GAME_NIGHT_TEST_REDIS_URL"
	applicationUserOrigin              = "https://play.example.test"
	applicationAdminOrigin             = "https://admin.example.test"
	applicationBootstrapPassword       = "Night-admin-bootstrap-2026!"
	applicationActivePassword          = "Night-admin-active-2026!"
	applicationTestRealName            = "Integration Real Name"
)

// TestApplicationConnectIdentityAndAdminIntegration exercises the real application graph through browser-style TLS Connect clients.
func TestApplicationConnectIdentityAndAdminIntegration(t *testing.T) {
	runtime := newApplicationIntegrationRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	identityClient := identityv1connect.NewIdentityServiceClient(runtime.client, runtime.baseURL)
	identity := onboardAndRecoverIdentity(t, ctx, runtime, identityClient)
	exerciseRoomLifecycle(t, ctx, runtime, roomv1connect.NewRoomServiceClient(runtime.client, runtime.baseURL))

	authClient := adminv1connect.NewAdminAuthServiceClient(runtime.client, runtime.baseURL)
	adminIdentityClient := adminv1connect.NewAdminIdentityServiceClient(runtime.client, runtime.baseURL)
	activateAdministrator(t, ctx, runtime, authClient)
	exerciseAdminIdentity(t, ctx, runtime, adminIdentityClient, identity)

	devicesRequest := connect.NewRequest(&identityv1.ListDevicesRequest{IncludeRevoked: true, Page: &commonv1.PageRequest{PageSize: 10}})
	devices, err := identityClient.ListDevices(ctx, devicesRequest)
	if err != nil || len(devices.Msg.GetDevices()) != 2 {
		t.Fatalf("list devices after administrator revocation: count=%d err=%v", len(devices.Msg.GetDevices()), err)
	}
	revokeRequest := connect.NewRequest(&identityv1.RevokeDeviceRequest{CredentialId: identity.currentCredentialID, Reason: "user_requested"})
	runtime.authorizeUserWrite(t, revokeRequest)
	revokeRequest.Header().Set(identitytransport.RequestIDHeader, "request-"+uuid.NewString())
	revoked, err := identityClient.RevokeDevice(ctx, revokeRequest)
	if err != nil || !revoked.Msg.GetCurrentDeviceRevoked() {
		t.Fatalf("revoke current device: revoked=%t err=%v", revoked.Msg.GetCurrentDeviceRevoked(), err)
	}

	suspendRequest := connect.NewRequest(&adminv1.SuspendUserRequest{UserId: identity.userID, Reason: "temporary moderation hold"})
	runtime.authorizeAdminIdentity(t, suspendRequest)
	suspended, err := adminIdentityClient.SuspendUser(ctx, suspendRequest)
	if err != nil {
		t.Fatalf("suspend integrated user: %v", err)
	}
	if suspended.Msg.GetUser().GetStatus() != identityv1.UserStatus_USER_STATUS_SUSPENDED {
		t.Fatalf("suspend integrated user: status=%s", suspended.Msg.GetUser().GetStatus())
	}
	unsuspendRequest := connect.NewRequest(&adminv1.UnsuspendUserRequest{UserId: identity.userID, Reason: "moderation hold cleared"})
	runtime.authorizeAdminIdentity(t, unsuspendRequest)
	unsuspended, err := adminIdentityClient.UnsuspendUser(ctx, unsuspendRequest)
	if err != nil {
		t.Fatalf("unsuspend integrated user: %v", err)
	}
	if unsuspended.Msg.GetUser().GetStatus() != identityv1.UserStatus_USER_STATUS_ACTIVE {
		t.Fatalf("unsuspend integrated user: status=%s", unsuspended.Msg.GetUser().GetStatus())
	}

	deleteRequest := connect.NewRequest(&adminv1.DeleteUserRequest{UserId: identity.userID, Reason: "completed integration lifecycle"})
	runtime.authorizeAdminIdentity(t, deleteRequest)
	deleted, err := adminIdentityClient.DeleteUser(ctx, deleteRequest)
	if err != nil || deleted.Msg.GetUser().GetStatus() != identityv1.UserStatus_USER_STATUS_DELETED {
		t.Fatalf("delete integrated user: status=%s err=%v", deleted.Msg.GetUser().GetStatus(), err)
	}
	auditRequest := connect.NewRequest(&adminv1.ListAuditEventsRequest{TargetUserId: identity.userID, Page: &commonv1.PageRequest{PageSize: 100}})
	runtime.authorizeAdminIdentity(t, auditRequest)
	auditEvents, err := adminIdentityClient.ListAuditEvents(ctx, auditRequest)
	if err != nil || len(auditEvents.Msg.GetEvents()) == 0 {
		t.Fatalf("list integrated audit events: count=%d err=%v", len(auditEvents.Msg.GetEvents()), err)
	}

	logoutRequest := connect.NewRequest(&adminv1.LogoutAllAdminSessionsRequest{})
	runtime.authorizeAdminSession(t, logoutRequest)
	logout, err := authClient.LogoutAllAdminSessions(ctx, logoutRequest)
	if err != nil || logout.Msg.GetRevokedSessions() < 1 {
		t.Fatalf("logout administrator sessions: count=%d err=%v", logout.Msg.GetRevokedSessions(), err)
	}
	if strings.Contains(runtime.logs.String(), applicationBootstrapPassword) ||
		strings.Contains(runtime.logs.String(), applicationActivePassword) ||
		strings.Contains(runtime.logs.String(), applicationTestRealName) {
		t.Fatal("application logs contain a configured password or real name")
	}
}

// exerciseRoomLifecycle proves recovered device authority reaches room PostgreSQL mutations through TLS Connect.
func exerciseRoomLifecycle(
	t testing.TB,
	ctx context.Context,
	runtime *applicationIntegrationRuntime,
	client roomv1connect.RoomServiceClient,
) {
	t.Helper()
	createRequest := connect.NewRequest(&roomv1.CreateRoomRequest{
		Visibility: roomv1.RoomVisibility_ROOM_VISIBILITY_PUBLIC, ParticipantCapacity: 4,
		ParticipantAdmission: roomv1.AdmissionMode_ADMISSION_MODE_OPEN,
		SpectatorAdmission:   roomv1.AdmissionMode_ADMISSION_MODE_APPROVAL,
	})
	runtime.authorizeUserWrite(t, createRequest)
	created, err := client.CreateRoom(ctx, createRequest)
	if err != nil {
		t.Fatalf("create integrated room: %v", err)
	}
	if created.Msg.GetRoom().GetRoomId() == "" || created.Msg.GetRoom().GetRoomCode() == "" {
		t.Fatalf("create integrated room: room=%+v", created.Msg.GetRoom())
	}
	listed, err := client.ListPublicRooms(ctx, connect.NewRequest(&roomv1.ListPublicRoomsRequest{
		Filter: &roomv1.PublicRoomFilter{Statuses: []roomv1.RoomStatus{roomv1.RoomStatus_ROOM_STATUS_LOBBY}},
		Page:   &commonv1.PageRequest{PageSize: 1},
	}))
	if err != nil || len(listed.Msg.GetRooms()) != 1 {
		t.Fatalf("list integrated public rooms: response=%+v err=%v", listed, err)
	}
	card := listed.Msg.GetRooms()[0]
	if card.GetRoomId() != created.Msg.GetRoom().GetRoomId() || card.GetHostUsername() != "ConnectUser9" ||
		card.GetPrimaryAction() != roomv1.PublicRoomPrimaryAction_PUBLIC_ROOM_PRIMARY_ACTION_ENTER_ROOM ||
		card.GetParticipantCount() != 1 || listed.Msg.GetPage().GetNextPageToken() != "" {
		t.Fatalf("list integrated public rooms: card=%+v", card)
	}
	assertNoStore(t, listed.Header())
	loaded, err := client.GetRoom(ctx, connect.NewRequest(&roomv1.GetRoomRequest{RoomId: created.Msg.GetRoom().GetRoomId()}))
	if err != nil {
		t.Fatalf("get integrated room: %v", err)
	}
	if loaded.Msg.GetRoom().GetRoomCode() != created.Msg.GetRoom().GetRoomCode() {
		t.Fatalf("get integrated room: room=%+v", loaded.Msg.GetRoom())
	}
	setRequest := connect.NewRequest(&roomv1.SetAdmissionRequest{
		RoomId: created.Msg.GetRoom().GetRoomId(), ParticipantAdmission: roomv1.AdmissionMode_ADMISSION_MODE_CLOSED,
		SpectatorAdmission: roomv1.AdmissionMode_ADMISSION_MODE_OPEN, ExpectedVersion: created.Msg.GetRoom().GetVersion(),
	})
	runtime.authorizeUserWrite(t, setRequest)
	updated, err := client.SetAdmission(ctx, setRequest)
	if err != nil {
		t.Fatalf("set integrated room admission: %v", err)
	}
	if updated.Msg.GetRoom().GetParticipantAdmission() != roomv1.AdmissionMode_ADMISSION_MODE_CLOSED {
		t.Fatalf("set integrated room admission: room=%+v", updated.Msg.GetRoom())
	}
	closeRequest := connect.NewRequest(&roomv1.CloseRoomRequest{
		RoomId: updated.Msg.GetRoom().GetRoomId(), ExpectedVersion: updated.Msg.GetRoom().GetVersion(),
	})
	runtime.authorizeUserWrite(t, closeRequest)
	closed, err := client.CloseRoom(ctx, closeRequest)
	if err != nil {
		t.Fatalf("close integrated room: %v", err)
	}
	if closed.Msg.GetRoom().GetStatus() != roomv1.RoomStatus_ROOM_STATUS_CLOSED {
		t.Fatalf("close integrated room: room=%+v", closed.Msg.GetRoom())
	}
}

type integratedIdentity struct {
	userID              string
	initialCredentialID string
	currentCredentialID string
}

// onboardAndRecoverIdentity covers a fresh browser, secret receipt, recovery, and cookie replacement.
func onboardAndRecoverIdentity(
	t testing.TB,
	ctx context.Context,
	runtime *applicationIntegrationRuntime,
	client identityv1connect.IdentityServiceClient,
) integratedIdentity {
	t.Helper()
	flowID := "bootstrap-" + uuid.NewString()
	beginRequest := connect.NewRequest(&identityv1.BeginIdentityBootstrapRequest{RequestFlowId: flowID})
	runtime.setOrigin(beginRequest, applicationUserOrigin)
	begin, err := client.BeginIdentityBootstrap(ctx, beginRequest)
	if err != nil || begin.Msg.GetChallenge().GetChallengeProof() == "" {
		t.Fatalf("begin identity bootstrap: err=%v", err)
	}
	bootstrapOperation := applicationOperationID(t)
	bootstrapRequest := connect.NewRequest(&identityv1.BootstrapIdentityRequest{
		ChallengeProof: begin.Msg.GetChallenge().GetChallengeProof(), OperationId: bootstrapOperation, DeviceLabel: "First Phone",
	})
	runtime.setOrigin(bootstrapRequest, applicationUserOrigin)
	bootstrapRequest.Header().Set(identitytransport.RequestFlowIDHeader, flowID)
	bootstrap, err := client.BootstrapIdentity(ctx, bootstrapRequest)
	if err != nil || bootstrap.Msg.GetUser().GetUserId() == "" || bootstrap.Msg.GetDevice().GetCredentialId() == "" {
		t.Fatalf("bootstrap identity: err=%v", err)
	}
	assertNoStore(t, bootstrap.Header())

	onboardingOperation := applicationOperationID(t)
	onboardingRequest := connect.NewRequest(&identityv1.CompleteOnboardingRequest{Username: "ConnectUser9", OperationId: onboardingOperation})
	runtime.authorizeUserWrite(t, onboardingRequest)
	onboarding, err := client.CompleteOnboarding(ctx, onboardingRequest)
	if err != nil || onboarding.Msg.GetUser().GetStatus() != identityv1.UserStatus_USER_STATUS_ACTIVE || onboarding.Msg.GetRecoveryCode() == "" {
		t.Fatalf("complete identity onboarding: status=%s err=%v", onboarding.Msg.GetUser().GetStatus(), err)
	}
	confirmUserSecret(t, ctx, runtime, client, identityv1.IdentitySecretOperation_IDENTITY_SECRET_OPERATION_BOOTSTRAP, bootstrap.Msg.GetResult())
	confirmUserSecret(t, ctx, runtime, client, identityv1.IdentitySecretOperation_IDENTITY_SECRET_OPERATION_ONBOARDING, onboarding.Msg.GetResult())

	recoveryFlowID := "recovery-" + uuid.NewString()
	recoveryChallengeRequest := connect.NewRequest(&identityv1.BeginRecoveryChallengeRequest{RequestFlowId: recoveryFlowID})
	runtime.setOrigin(recoveryChallengeRequest, applicationUserOrigin)
	recoveryChallenge, err := client.BeginRecoveryChallenge(ctx, recoveryChallengeRequest)
	if err != nil {
		t.Fatalf("begin recovery challenge: %v", err)
	}
	beginRecoveryRequest := connect.NewRequest(&identityv1.BeginRecoveryRequest{
		ChallengeProof: recoveryChallenge.Msg.GetChallenge().GetChallengeProof(), RecoveryCode: onboarding.Msg.GetRecoveryCode(),
	})
	runtime.setOrigin(beginRecoveryRequest, applicationUserOrigin)
	beginRecoveryRequest.Header().Set(identitytransport.RequestFlowIDHeader, recoveryFlowID)
	beginRecovery, err := client.BeginRecovery(ctx, beginRecoveryRequest)
	if err != nil || beginRecovery.Msg.GetRecoveryGrant() == "" {
		t.Fatalf("begin identity recovery: err=%v", err)
	}
	recoveryRequest := connect.NewRequest(&identityv1.CompleteRecoveryRequest{
		RecoveryGrant: beginRecovery.Msg.GetRecoveryGrant(), OperationId: applicationOperationID(t), DeviceLabel: "Recovered Phone",
		DevicePolicy: identityv1.RecoveryDevicePolicy_RECOVERY_DEVICE_POLICY_KEEP_OTHER_DEVICES,
	})
	runtime.setOrigin(recoveryRequest, applicationUserOrigin)
	recoveryRequest.Header().Set(identitytransport.RequestIDHeader, "request-"+uuid.NewString())
	recovery, err := client.CompleteRecovery(ctx, recoveryRequest)
	if err != nil || recovery.Msg.GetUser().GetUserId() != bootstrap.Msg.GetUser().GetUserId() || recovery.Msg.GetRecoveryCode() == "" {
		t.Fatalf("complete identity recovery: err=%v", err)
	}
	confirmUserSecret(t, ctx, runtime, client, identityv1.IdentitySecretOperation_IDENTITY_SECRET_OPERATION_RECOVERY, recovery.Msg.GetResult())
	return integratedIdentity{
		userID: bootstrap.Msg.GetUser().GetUserId(), initialCredentialID: bootstrap.Msg.GetDevice().GetCredentialId(),
		currentCredentialID: recovery.Msg.GetDevice().GetCredentialId(),
	}
}

// confirmUserSecret proves result envelopes are erased only by the current device plus CSRF authority.
func confirmUserSecret(
	t testing.TB,
	ctx context.Context,
	runtime *applicationIntegrationRuntime,
	client identityv1connect.IdentityServiceClient,
	operation identityv1.IdentitySecretOperation,
	result *commonv1.OperationResult,
) {
	t.Helper()
	request := connect.NewRequest(&identityv1.ConfirmSecretReceiptRequest{
		Operation: operation, OperationId: result.GetOperationId(), ResultId: result.GetResultId(),
	})
	runtime.authorizeUserWrite(t, request)
	response, err := client.ConfirmSecretReceipt(ctx, request)
	if err != nil || !response.Msg.GetConfirmed() {
		t.Fatalf("confirm user secret receipt: confirmed=%t err=%v", response.Msg.GetConfirmed(), err)
	}
}

// activateAdministrator completes the one-time password and TOTP setup through the isolated admin Cookie namespace.
func activateAdministrator(
	t testing.TB,
	ctx context.Context,
	runtime *applicationIntegrationRuntime,
	client adminv1connect.AdminAuthServiceClient,
) {
	t.Helper()
	state, err := client.GetSetupState(ctx, connect.NewRequest(&adminv1.GetSetupStateRequest{}))
	if err != nil || state.Msg.GetState() != adminv1.AdminSetupState_ADMIN_SETUP_STATE_SETUP_REQUIRED {
		t.Fatalf("administrator setup state: state=%s err=%v", state.Msg.GetState(), err)
	}
	flowID := "admin-login-" + uuid.NewString()
	beginRequest := connect.NewRequest(&adminv1.BeginAdminLoginRequest{RequestFlowId: flowID})
	runtime.setOrigin(beginRequest, applicationAdminOrigin)
	begin, err := client.BeginAdminLogin(ctx, beginRequest)
	if err != nil {
		t.Fatalf("begin administrator login: %v", err)
	}
	loginRequest := connect.NewRequest(&adminv1.LoginPasswordRequest{
		ChallengeProof: begin.Msg.GetChallenge().GetChallengeProof(), Password: applicationBootstrapPassword,
	})
	runtime.setOrigin(loginRequest, applicationAdminOrigin)
	loginRequest.Header().Set(adminauth.RequestFlowIDHeader, flowID)
	login, err := client.LoginPassword(ctx, loginRequest)
	if err != nil || login.Msg.GetNextStep() != adminv1.AdminNextStep_ADMIN_NEXT_STEP_CHANGE_PASSWORD {
		t.Fatalf("administrator bootstrap login: next=%s err=%v", login.Msg.GetNextStep(), err)
	}
	passwordRequest := connect.NewRequest(&adminv1.ChangeInitialPasswordRequest{NewPassword: applicationActivePassword})
	runtime.authorizeAdminSession(t, passwordRequest)
	password, err := client.ChangeInitialPassword(ctx, passwordRequest)
	if err != nil || password.Msg.GetNextStep() != adminv1.AdminNextStep_ADMIN_NEXT_STEP_ENROLL_TOTP {
		t.Fatalf("change initial administrator password: next=%s err=%v", password.Msg.GetNextStep(), err)
	}

	enrollmentRequest := connect.NewRequest(&adminv1.BeginTotpEnrollmentRequest{OperationId: applicationOperationID(t)})
	runtime.authorizeAdminSession(t, enrollmentRequest)
	enrollment, err := client.BeginTotpEnrollment(ctx, enrollmentRequest)
	if err != nil || enrollment.Msg.GetTotpSecret() == "" || enrollment.Msg.GetResult() == nil {
		t.Fatalf("begin TOTP enrollment: err=%v", err)
	}
	code, err := admin.GenerateTOTPCode(enrollment.Msg.GetTotpSecret(), time.Now().UTC())
	if err != nil {
		t.Fatal("generate TOTP enrollment code")
	}
	completeRequest := connect.NewRequest(&adminv1.CompleteTotpEnrollmentRequest{
		EnrollmentOperationId: enrollment.Msg.GetResult().GetOperationId(), RecoveryCodesOperationId: applicationOperationID(t), TotpCode: code,
	})
	runtime.authorizeAdminSession(t, completeRequest)
	complete, err := client.CompleteTotpEnrollment(ctx, completeRequest)
	if err != nil {
		t.Fatalf("complete TOTP enrollment: %v", err)
	}
	if complete.Msg.GetSession().GetKind() != adminv1.AdminSessionKind_ADMIN_SESSION_KIND_FULL || len(complete.Msg.GetRecoveryCodes()) == 0 {
		t.Fatalf("complete TOTP enrollment: session=%s codes=%d", complete.Msg.GetSession().GetKind(), len(complete.Msg.GetRecoveryCodes()))
	}
	confirmAdminSecret(t, ctx, runtime, client, adminv1.AdminSecretOperation_ADMIN_SECRET_OPERATION_TOTP_ENROLLMENT, enrollment.Msg.GetResult())
	confirmAdminSecret(t, ctx, runtime, client, adminv1.AdminSecretOperation_ADMIN_SECRET_OPERATION_INITIAL_RECOVERY_CODES, complete.Msg.GetResult())
}

// confirmAdminSecret verifies full-session authorization before deleting an administrator result envelope.
func confirmAdminSecret(
	t testing.TB,
	ctx context.Context,
	runtime *applicationIntegrationRuntime,
	client adminv1connect.AdminAuthServiceClient,
	operation adminv1.AdminSecretOperation,
	result *commonv1.OperationResult,
) {
	t.Helper()
	request := connect.NewRequest(&adminv1.ConfirmAdminSecretReceiptRequest{
		Operation: operation, OperationId: result.GetOperationId(), ResultId: result.GetResultId(),
	})
	runtime.authorizeAdminSession(t, request)
	response, err := client.ConfirmAdminSecretReceipt(ctx, request)
	if err != nil || !response.Msg.GetConfirmed() {
		t.Fatalf("confirm administrator secret receipt: confirmed=%t err=%v", response.Msg.GetConfirmed(), err)
	}
}

// exerciseAdminIdentity covers PII round trips, export lifecycle, governance, assisted recovery, device revocation, and audit reads.
func exerciseAdminIdentity(
	t testing.TB,
	ctx context.Context,
	runtime *applicationIntegrationRuntime,
	client adminv1connect.AdminIdentityServiceClient,
	identity integratedIdentity,
) {
	t.Helper()
	getUserRequest := connect.NewRequest(&adminv1.GetUserRequest{Lookup: &adminv1.GetUserRequest_UserId{UserId: identity.userID}})
	runtime.authorizeAdminIdentity(t, getUserRequest)
	user, err := client.GetUser(ctx, getUserRequest)
	if err != nil || user.Msg.GetUser().GetUserId() != identity.userID {
		t.Fatalf("administrator get user: err=%v", err)
	}

	updateNameRequest := connect.NewRequest(&adminv1.UpdateRealNameRequest{
		UserId: identity.userID, RealName: applicationTestRealName, Reason: "verified account ownership",
	})
	runtime.authorizeAdminIdentity(t, updateNameRequest)
	updatedName, err := client.UpdateRealName(ctx, updateNameRequest)
	if err != nil || updatedName.Msg.GetProfile().GetRealName() != applicationTestRealName {
		t.Fatalf("administrator update real name: err=%v", err)
	}
	getNameRequest := connect.NewRequest(&adminv1.GetRealNameRequest{UserId: identity.userID, Reason: "support verification"})
	runtime.authorizeAdminIdentity(t, getNameRequest)
	readName, err := client.GetRealName(ctx, getNameRequest)
	if err != nil || readName.Msg.GetProfile().GetRealName() != applicationTestRealName {
		t.Fatalf("administrator read real name: err=%v", err)
	}

	createExportRequest := connect.NewRequest(&adminv1.CreateUserProfileExportRequest{
		Filter: &adminv1.ProfileExportFilter{UserIds: []string{identity.userID}},
		Fields: []adminv1.ProfileField{adminv1.ProfileField_PROFILE_FIELD_REAL_NAME}, Reason: "subject access export",
	})
	runtime.authorizeAdminIdentity(t, createExportRequest)
	export, err := client.CreateUserProfileExport(ctx, createExportRequest)
	if err != nil || export.Msg.GetExportId() == "" {
		t.Fatalf("create profile export: err=%v", err)
	}
	exportPageRequest := connect.NewRequest(&adminv1.GetUserProfileExportPageRequest{ExportId: export.Msg.GetExportId(), PageSize: 10})
	runtime.authorizeAdminIdentity(t, exportPageRequest)
	exportPage, err := client.GetUserProfileExportPage(ctx, exportPageRequest)
	if err != nil || len(exportPage.Msg.GetRecords()) != 1 || exportPage.Msg.GetRecords()[0].GetRealName() != applicationTestRealName {
		t.Fatalf("read profile export page: count=%d err=%v", len(exportPage.Msg.GetRecords()), err)
	}
	completeExportRequest := connect.NewRequest(&adminv1.CompleteUserProfileExportRequest{ExportId: export.Msg.GetExportId()})
	runtime.authorizeAdminIdentity(t, completeExportRequest)
	completedExport, err := client.CompleteUserProfileExport(ctx, completeExportRequest)
	if err != nil || !completedExport.Msg.GetCompleted() {
		t.Fatalf("complete profile export: completed=%t err=%v", completedExport.Msg.GetCompleted(), err)
	}

	abortSourceRequest := connect.NewRequest(&adminv1.CreateUserProfileExportRequest{
		Filter: &adminv1.ProfileExportFilter{UserIds: []string{identity.userID}},
		Fields: []adminv1.ProfileField{adminv1.ProfileField_PROFILE_FIELD_REAL_NAME}, Reason: "cancelled subject access export",
	})
	runtime.authorizeAdminIdentity(t, abortSourceRequest)
	abortSource, err := client.CreateUserProfileExport(ctx, abortSourceRequest)
	if err != nil {
		t.Fatalf("create abortable profile export: %v", err)
	}
	abortRequest := connect.NewRequest(&adminv1.AbortUserProfileExportRequest{ExportId: abortSource.Msg.GetExportId(), Reason: "operator cancelled export"})
	runtime.authorizeAdminIdentity(t, abortRequest)
	aborted, err := client.AbortUserProfileExport(ctx, abortRequest)
	if err != nil || !aborted.Msg.GetAborted() {
		t.Fatalf("abort profile export: aborted=%t err=%v", aborted.Msg.GetAborted(), err)
	}

	grantRequest := connect.NewRequest(&adminv1.CreateAssistedRecoveryGrantRequest{
		UserId: identity.userID, OperationId: applicationOperationID(t), Reason: "verified assisted recovery",
	})
	runtime.authorizeAdminIdentity(t, grantRequest)
	grant, err := client.CreateAssistedRecoveryGrant(ctx, grantRequest)
	if err != nil || grant.Msg.GetAssistedRecoveryGrant() == "" {
		t.Fatalf("create assisted recovery grant: err=%v", err)
	}
	usernameRequest := connect.NewRequest(&adminv1.ForceChangeUsernameRequest{
		UserId: identity.userID, Username: "ConnectAdmin9", Reason: "moderated username change",
	})
	runtime.authorizeAdminIdentity(t, usernameRequest)
	username, err := client.ForceChangeUsername(ctx, usernameRequest)
	if err != nil || username.Msg.GetUser().GetUsername() != "ConnectAdmin9" {
		t.Fatalf("force username change: err=%v", err)
	}

	revokeDeviceRequest := connect.NewRequest(&adminv1.RevokeUserDeviceRequest{
		UserId: identity.userID, CredentialId: identity.initialCredentialID, Reason: "stale device removed",
	})
	runtime.authorizeAdminIdentity(t, revokeDeviceRequest)
	revoked, err := client.RevokeUserDevice(ctx, revokeDeviceRequest)
	if err != nil {
		t.Fatalf("administrator revoke user device: %v", err)
	}
	if !revoked.Msg.GetRevoked() {
		t.Fatal("administrator did not revoke the stale user device")
	}
}

type applicationIntegrationRuntime struct {
	// application owns the real PostgreSQL, Redis, Argon2, handler, and shutdown lifecycle used by the test client.
	application *Application
	// client retains both the generated TLS trust root and browser Cookie Jar across user and administrator calls.
	client *http.Client
	// serverURL is the parsed Cookie origin used to retrieve double-submit values from the Jar.
	serverURL *url.URL
	// baseURL is passed unchanged to every generated Connect client.
	baseURL string
	// serveErrors proves graceful shutdown normalized the runtime's terminal result.
	serveErrors chan error
	// redisPrefix isolates rate-limit keys so cleanup never scans or deletes another test run's namespace.
	redisPrefix string
	// logs captures structured output for the final secret and PII disclosure assertion.
	logs *bytes.Buffer
}

// newApplicationIntegrationRuntime starts the production dependency graph while retaining TLS and Cookie behavior in-process.
func newApplicationIntegrationRuntime(t testing.TB) *applicationIntegrationRuntime {
	t.Helper()
	values := integrationtest.RequireEnvironment(t, integrationtest.DependencyPostgres, applicationTestDatabaseEnvironment)
	redisValues := integrationtest.RequireEnvironment(t, integrationtest.DependencyRedis, applicationTestRedisEnvironment)
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	applyApplicationIntegrationMigrations(t, ctx, fixture)
	keyrings := applicationIntegrationKeyrings(t, time.Now().UTC())
	bootstrapFile := writeApplicationReadOnlyFile(t, "admin-bootstrap.txt", []byte(applicationBootstrapPassword+"\n"))
	if secret, mounted, err := bootstrap.ReadSecret(bootstrapFile); err != nil || !mounted || secret != applicationBootstrapPassword {
		t.Fatalf("read integrated administrator bootstrap secret: mounted=%t err=%v", mounted, err)
	}
	redisPrefix := "gn:e2e:" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16] + ":"
	config := apiConfig.Config{
		Shared: sharedconfig.Config{
			Environment: sharedconfig.EnvironmentTest,
			PostgreSQL: sharedconfig.PostgreSQLConfig{
				DSN: values[0], Schema: fixture.Name, MinConnections: 1, MaxConnections: 4,
				MaxConnectionLifetime: time.Hour, MaxConnectionIdleTime: 5 * time.Minute, HealthCheckPeriod: time.Minute,
			},
			Redis: sharedconfig.RedisConfig{URL: redisValues[0], Timeout: 2 * time.Second, KeyPrefix: redisPrefix},
			Network: sharedconfig.NetworkConfig{
				UserOrigins:    sharedconfig.OriginAllowlist{sharedconfig.Origin(applicationUserOrigin)},
				AdminOrigins:   sharedconfig.OriginAllowlist{sharedconfig.Origin(applicationAdminOrigin)},
				TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, CookieSecure: true,
			},
			Checkpoint: sharedconfig.CheckpointConfig{MaxEvents: 100, MaxInterval: 5 * time.Minute},
			Keyrings:   keyrings, BootstrapSecretFile: sharedconfig.BootstrapSecretFile(bootstrapFile),
		},
		Listener: apiConfig.ListenerConfig{
			Address: "127.0.0.1:8080", ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
			WriteTimeout: 30 * time.Second, IdleTimeout: time.Minute, ShutdownTimeout: 5 * time.Second, MaxHeaderBytes: 1 << 20,
		},
		Argon2: apiConfig.Argon2Config{Workers: 1, QueueCapacity: 16},
	}
	logs := &bytes.Buffer{}
	application, err := New(ctx, config, Options{
		Logger: slog.New(logging.NewJSONHandler(logs, slog.LevelDebug)), Metrics: prometheus.NewRegistry(),
		CheckpointSink: audit.SinkReadinessFunc(func(context.Context) bool { return true }),
	})
	if err != nil {
		var status string
		var passwordPresent bool
		if queryErr := fixture.Pool.QueryRow(ctx, `
			SELECT status, password_hash IS NOT NULL FROM admin_accounts WHERE singleton_id = 1
		`).Scan(&status, &passwordPresent); queryErr != nil {
			t.Fatalf("build integrated API application: %v; inspect administrator state: %v", err, queryErr)
		}
		t.Fatalf("build integrated API application: %v; administrator status=%s password_present=%t", err, status, passwordPresent)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		_ = application.closeDependencies()
		t.Fatal(err)
	}
	certificate, roots := applicationIntegrationTLSIdentity(t)
	tlsListener := tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- application.runtime.Serve(tlsListener) }()
	baseURL := "https://" + listener.Addr().String()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: roots, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12,
	}}}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client.Jar = jar
	serverURL, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &applicationIntegrationRuntime{
		application: application, client: client, serverURL: serverURL, baseURL: baseURL,
		serveErrors: serveErrors, redisPrefix: redisPrefix, logs: logs,
	}
	t.Cleanup(func() {
		cleanupApplicationIntegrationRedis(t, application, redisPrefix)
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := application.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutdown integrated API application: %v", err)
		}
		if err := <-serveErrors; err != nil {
			t.Errorf("serve integrated API application: %v", err)
		}
	})
	return runtime
}

// authorizeUserWrite applies the exact browser Origin and double-submit token expected by user mutations.
func (runtime *applicationIntegrationRuntime) authorizeUserWrite(t testing.TB, request interface{ Header() http.Header }) {
	t.Helper()
	runtime.setOriginHeader(request.Header(), applicationUserOrigin)
	request.Header().Set(csrf.HeaderName, runtime.cookie(t, cookies.UserCSRFCookieName))
}

// authorizeAdminSession applies administrator Origin and CSRF without adding identity-only audit metadata.
func (runtime *applicationIntegrationRuntime) authorizeAdminSession(t testing.TB, request interface{ Header() http.Header }) {
	t.Helper()
	runtime.setOriginHeader(request.Header(), applicationAdminOrigin)
	request.Header().Set(csrf.HeaderName, runtime.cookie(t, cookies.AdminCSRFCookieName))
}

// authorizeAdminIdentity adds the mandatory correlation ID after establishing the administrator session proof.
func (runtime *applicationIntegrationRuntime) authorizeAdminIdentity(t testing.TB, request interface{ Header() http.Header }) {
	t.Helper()
	runtime.authorizeAdminSession(t, request)
	request.Header().Set(adminauth.RequestIDHeader, "request-"+uuid.NewString())
}

func (runtime *applicationIntegrationRuntime) setOrigin(request interface{ Header() http.Header }, origin string) {
	runtime.setOriginHeader(request.Header(), origin)
}

func (runtime *applicationIntegrationRuntime) setOriginHeader(header http.Header, origin string) {
	header.Set("Origin", origin)
}

func (runtime *applicationIntegrationRuntime) cookie(t testing.TB, name string) string {
	t.Helper()
	for _, cookie := range runtime.client.Jar.Cookies(runtime.serverURL) {
		if cookie.Name == name && cookie.Value != "" {
			return cookie.Value
		}
	}
	t.Fatalf("browser Cookie %s is unavailable", name)
	return ""
}

// applyApplicationIntegrationMigrations binds all deployment roles to the isolated schema owner for this non-privilege suite.
func applyApplicationIntegrationMigrations(t testing.TB, ctx context.Context, fixture *integrationtest.PostgresSchema) {
	t.Helper()
	var currentUser string
	if err := fixture.Pool.QueryRow(ctx, "SELECT current_user").Scan(&currentUser); err != nil {
		t.Fatal(err)
	}
	database := fixture.OpenSQLDB(t, map[string]string{
		"game_night.owner_role": currentUser, "game_night.audit_writer_role": currentUser,
		"game_night.migration_role": currentUser, "game_night.runtime_role": currentUser, "game_night.worker_role": currentUser,
	})
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate application integration test source")
	}
	migrations := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", "..", "infra", "migrations"))
	if err := goose.UpContext(ctx, database, migrations); err != nil {
		t.Fatalf("apply application integration migrations: %v", err)
	}
}

// applicationIntegrationKeyrings creates one independently generated key per cryptographic purpose.
func applicationIntegrationKeyrings(t testing.TB, now time.Time) sharedconfig.KeyringFiles {
	t.Helper()
	symmetric := func(name string) string {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			t.Fatal(err)
		}
		document := map[string]any{
			"active_version": 1,
			"keys": []map[string]any{{
				"version": 1, "key": base64.StdEncoding.EncodeToString(key), "not_before": now.Add(-time.Hour),
			}},
		}
		contents, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		return writeApplicationReadOnlyFile(t, name, contents)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auditDocument := map[string]any{
		"active_version": 1,
		"keys": []map[string]any{{
			"version": 1, "public_key": base64.StdEncoding.EncodeToString(publicKey),
			"private_key": base64.StdEncoding.EncodeToString(privateKey), "not_before": now.Add(-time.Hour),
		}},
	}
	auditContents, err := json.Marshal(auditDocument)
	if err != nil {
		t.Fatal(err)
	}
	return sharedconfig.KeyringFiles{
		PII:            sharedconfig.PIIKeyringFile(symmetric("pii.json")),
		TOTP:           sharedconfig.TOTPKeyringFile(symmetric("totp.json")),
		ResultEnvelope: sharedconfig.ResultEnvelopeKeyringFile(symmetric("result-envelope.json")),
		Device:         sharedconfig.DeviceKeyringFile(symmetric("device.json")),
		RateLimit:      sharedconfig.RateLimitKeyringFile(symmetric("rate-limit.json")),
		UserChallenge:  sharedconfig.UserChallengeKeyringFile(symmetric("user-challenge.json")),
		AdminChallenge: sharedconfig.AdminChallengeKeyringFile(symmetric("admin-challenge.json")),
		AdminSession:   sharedconfig.AdminSessionKeyringFile(symmetric("admin-session.json")),
		Audit:          sharedconfig.AuditKeyringFile(writeApplicationReadOnlyFile(t, "audit.json", auditContents)),
	}
}

func writeApplicationReadOnlyFile(t testing.TB, name string, contents []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	mode := os.FileMode(0o400)
	if runtime.GOOS == "windows" {
		mode = 0o444
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	return path
}

// applicationIntegrationTLSIdentity creates a trusted loopback certificate so Secure Cookies follow browser rules.
func applicationIntegrationTLSIdentity(t testing.TB) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privateDER})
	certificate, err := tls.X509KeyPair(certificatePEM, privatePEM)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificatePEM) {
		t.Fatal("trust integrated API certificate")
	}
	return certificate, roots
}

func cleanupApplicationIntegrationRedis(t testing.TB, application *Application, prefix string) {
	t.Helper()
	if application == nil || application.redis == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var cursor uint64
	for {
		keys, next, err := application.redis.Scan(ctx, cursor, prefix+"*", 100).Result()
		if err != nil {
			t.Errorf("scan integrated Redis keys")
			return
		}
		if len(keys) > 0 {
			if err := application.redis.Unlink(ctx, keys...).Err(); err != nil {
				t.Errorf("delete integrated Redis keys")
				return
			}
		}
		cursor = next
		if cursor == 0 {
			return
		}
	}
}

func applicationOperationID(t testing.TB) string {
	t.Helper()
	entropy := make([]byte, 16)
	if _, err := rand.Read(entropy); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(entropy)
}

func assertNoStore(t testing.TB, header http.Header) {
	t.Helper()
	if header.Get("Cache-Control") != "no-store" || header.Get("Pragma") != "no-cache" {
		t.Fatalf("sensitive response cache policy = %q / %q", header.Get("Cache-Control"), header.Get("Pragma"))
	}
}
