package contracttest

import (
	"strings"
	"testing"

	adminv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1"
	auditv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/audit/v1"
	commonv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/common/v1"
	identityv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/identity/v1"
	roomv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/room/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestServiceMethodsMatchApprovedContract(t *testing.T) {
	t.Parallel()

	assertServiceMethods(t, identityv1.File_platform_identity_v1_identity_proto, "IdentityService", []string{
		"BeginIdentityBootstrap",
		"BootstrapIdentity",
		"CompleteOnboarding",
		"GetCurrentIdentity",
		"ChangeUsername",
		"RotateRecoveryCode",
		"BeginRecoveryChallenge",
		"BeginRecovery",
		"CompleteRecovery",
		"ConfirmSecretReceipt",
		"ListDevices",
		"RevokeDevice",
	})
	assertServiceMethods(t, adminv1.File_platform_admin_v1_admin_auth_proto, "AdminAuthService", []string{
		"GetSetupState",
		"BeginAdminLogin",
		"LoginPassword",
		"VerifyTotp",
		"ChangeInitialPassword",
		"BeginTotpEnrollment",
		"CompleteTotpEnrollment",
		"ConfirmAdminSecretReceipt",
		"RecoverAdmin",
		"ChangeAdminPassword",
		"BeginTotpRebind",
		"CompleteTotpRebind",
		"RegenerateAdminRecoveryCodes",
		"LogoutAdmin",
		"LogoutAllAdminSessions",
	})
	assertServiceMethods(t, adminv1.File_platform_admin_v1_admin_identity_proto, "AdminIdentityService", []string{
		"GetUser",
		"GetRealName",
		"UpdateRealName",
		"CreateUserProfileExport",
		"GetUserProfileExportPage",
		"CompleteUserProfileExport",
		"AbortUserProfileExport",
		"CreateAssistedRecoveryGrant",
		"ForceChangeUsername",
		"SuspendUser",
		"UnsuspendUser",
		"DeleteUser",
		"RevokeUserDevice",
		"ListAuditEvents",
	})
	assertServiceMethods(t, roomv1.File_platform_room_v1_room_proto, "RoomService", []string{
		"CreateRoom",
		"GetRoom",
		"ListMyRooms",
		"ListPublicRooms",
		"JoinRoom",
		"ApproveMember",
		"SetAdmission",
		"StartGame",
		"FinishGame",
		"RemoveMember",
		"CloseRoom",
	})
}

func TestBusinessErrorCodesMatchApprovedContract(t *testing.T) {
	t.Parallel()

	enum := commonv1.BusinessErrorCode(0).Descriptor()
	want := []string{
		"BUSINESS_ERROR_CODE_UNSPECIFIED",
		"BUSINESS_ERROR_CODE_IDENTITY_ONBOARDING_REQUIRED",
		"BUSINESS_ERROR_CODE_USERNAME_INVALID",
		"BUSINESS_ERROR_CODE_USERNAME_TAKEN",
		"BUSINESS_ERROR_CODE_USERNAME_CHANGE_COOLDOWN",
		"BUSINESS_ERROR_CODE_DEVICE_CREDENTIAL_INVALID",
		"BUSINESS_ERROR_CODE_DEVICE_REVOKED",
		"BUSINESS_ERROR_CODE_ACCOUNT_SUSPENDED",
		"BUSINESS_ERROR_CODE_ACCOUNT_DELETED",
		"BUSINESS_ERROR_CODE_RECOVERY_INVALID",
		"BUSINESS_ERROR_CODE_IDEMPOTENCY_CONFLICT",
		"BUSINESS_ERROR_CODE_SECRET_RESULT_NO_LONGER_AVAILABLE",
		"BUSINESS_ERROR_CODE_CSRF_INVALID",
		"BUSINESS_ERROR_CODE_ORIGIN_NOT_ALLOWED",
		"BUSINESS_ERROR_CODE_RATE_LIMITED",
		"BUSINESS_ERROR_CODE_ADMIN_SETUP_REQUIRED",
		"BUSINESS_ERROR_CODE_ADMIN_PASSWORD_CHANGE_REQUIRED",
		"BUSINESS_ERROR_CODE_MFA_REQUIRED",
		"BUSINESS_ERROR_CODE_MFA_INVALID",
		"BUSINESS_ERROR_CODE_AUTH_INVALID",
		"BUSINESS_ERROR_CODE_PII_KEY_UNAVAILABLE",
		"BUSINESS_ERROR_CODE_AUDIT_WRITE_FAILED",
		"BUSINESS_ERROR_CODE_SERVICE_TEMPORARILY_UNAVAILABLE",
		"BUSINESS_ERROR_CODE_ROOM_NOT_FOUND",
		"BUSINESS_ERROR_CODE_ROOM_VERSION_CONFLICT",
		"BUSINESS_ERROR_CODE_ROOM_ADMISSION_CLOSED",
		"BUSINESS_ERROR_CODE_ROOM_FULL",
		"BUSINESS_ERROR_CODE_ROOM_HOST_REQUIRED",
		"BUSINESS_ERROR_CODE_ROOM_STATUS_INVALID",
		"BUSINESS_ERROR_CODE_ROOM_MEMBER_NOT_FOUND",
		"BUSINESS_ERROR_CODE_ROOM_CODE_UNAVAILABLE",
		"BUSINESS_ERROR_CODE_GAME_SESSION_NOT_FOUND",
		"BUSINESS_ERROR_CODE_GAME_STATE_VERSION_CONFLICT",
		"BUSINESS_ERROR_CODE_GAME_OWNERSHIP_LOST",
		"BUSINESS_ERROR_CODE_GAME_SESSION_SUSPENDED",
		"BUSINESS_ERROR_CODE_GAME_SESSION_TERMINAL",
		"BUSINESS_ERROR_CODE_GAME_PARTICIPANT_NOT_ACTIVE",
		"BUSINESS_ERROR_CODE_GAME_MODULE_UNAVAILABLE",
		"BUSINESS_ERROR_CODE_GAME_PROJECTION_UNSAFE",
		"BUSINESS_ERROR_CODE_GAME_REQUEST_DIGEST_INVALID",
		"BUSINESS_ERROR_CODE_GAME_REPLAY_FORBIDDEN",
		"BUSINESS_ERROR_CODE_GAME_SUBSCRIPTION_UNAVAILABLE",
		"BUSINESS_ERROR_CODE_GAME_REPLAY_ACCESS_CONFLICT",
		"BUSINESS_ERROR_CODE_GAME_REPLAY_ACCESS_UNAVAILABLE",
	}
	if enum.Values().Len() != len(want) {
		t.Fatalf("expected %d business error codes, got %d", len(want), enum.Values().Len())
	}
	for index, name := range want {
		if got := string(enum.Values().Get(index).Name()); got != name {
			t.Fatalf("business error code %d: expected %q, got %q", index, name, got)
		}
	}
}

func TestDescriptorsUseBoundedPortableFields(t *testing.T) {
	t.Parallel()

	files := []protoreflect.FileDescriptor{
		commonv1.File_platform_common_v1_common_proto,
		commonv1.File_platform_common_v1_error_proto,
		identityv1.File_platform_identity_v1_identity_proto,
		adminv1.File_platform_admin_v1_admin_auth_proto,
		adminv1.File_platform_admin_v1_admin_identity_proto,
		auditv1.File_platform_audit_v1_audit_proto,
		roomv1.File_platform_room_v1_room_proto,
	}
	for _, file := range files {
		assertEnumsHaveUnspecifiedZero(t, file.Enums(), file.Path())
		assertMessagesUsePortableFields(t, file.Messages(), file.Path())
	}
}

// assertServiceMethods compares the full ordered method set so generated unimplemented methods cannot hide scope gaps.
func assertServiceMethods(t *testing.T, file protoreflect.FileDescriptor, serviceName protoreflect.Name, want []string) {
	t.Helper()

	service := file.Services().ByName(serviceName)
	if service == nil {
		t.Fatalf("%s: missing service %s", file.Path(), serviceName)
	}
	if service.Methods().Len() != len(want) {
		t.Fatalf("%s: expected %d methods, got %d", service.FullName(), len(want), service.Methods().Len())
	}
	for index, name := range want {
		if got := string(service.Methods().Get(index).Name()); got != name {
			t.Fatalf("%s method %d: expected %q, got %q", service.FullName(), index, name, got)
		}
	}
}

func assertEnumsHaveUnspecifiedZero(t *testing.T, enums protoreflect.EnumDescriptors, owner string) {
	t.Helper()

	for index := 0; index < enums.Len(); index++ {
		enum := enums.Get(index)
		zero := enum.Values().Get(0)
		if zero.Number() != 0 || !strings.HasSuffix(string(zero.Name()), "_UNSPECIFIED") {
			t.Errorf("%s: enum %s must start with a zero UNSPECIFIED value", owner, enum.FullName())
		}
	}
}

func assertMessagesUsePortableFields(t *testing.T, messages protoreflect.MessageDescriptors, owner string) {
	t.Helper()

	for messageIndex := 0; messageIndex < messages.Len(); messageIndex++ {
		message := messages.Get(messageIndex)
		assertEnumsHaveUnspecifiedZero(t, message.Enums(), string(message.FullName()))
		assertMessagesUsePortableFields(t, message.Messages(), string(message.FullName()))
		for fieldIndex := 0; fieldIndex < message.Fields().Len(); fieldIndex++ {
			field := message.Fields().Get(fieldIndex)
			name := string(field.Name())
			if field.Message() != nil && field.Message().FullName() == "google.protobuf.Struct" {
				t.Errorf("%s: field %s cannot use google.protobuf.Struct", owner, field.FullName())
			}
			if strings.Contains(name, "json") {
				t.Errorf("%s: field %s cannot carry an unbounded JSON payload", owner, field.FullName())
			}
			if (strings.HasSuffix(name, "_id") || strings.HasSuffix(name, "_ids")) && field.Kind() != protoreflect.StringKind {
				t.Errorf("%s: ID field %s must use string transport", owner, field.FullName())
			}
			if strings.HasSuffix(name, "_at") || strings.HasSuffix(name, "_until") {
				if field.Message() == nil || field.Message().FullName() != "google.protobuf.Timestamp" {
					t.Errorf("%s: time field %s must use google.protobuf.Timestamp", owner, field.FullName())
				}
			}
		}
	}
}
