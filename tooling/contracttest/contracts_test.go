package contracttest

import (
	"strings"
	"testing"

	adminv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1"
	auditv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/audit/v1"
	commonv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/common/v1"
	gamev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1"
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
		"GetCurrentAdminSession",
		"GetRuntimeReadiness",
		"BeginAdminLogin",
		"LoginPassword",
		"VerifyAdminTotp",
		"VerifyAdminRecoveryCode",
		"ChangeInitialPassword",
		"ChangeAdminPassword",
		"BeginTotpEnrollment",
		"CompleteTotpEnrollment",
		"DisableTotp",
		"RegenerateAdminRecoveryCodes",
		"ConfirmAdminSecretReceipt",
		"ElevateAdminSession",
		"RevokeCurrentAdminElevation",
		"ListAdminSessions",
		"RevokeAdminSession",
		"PreviewRevokeOtherAdminSessions",
		"RevokeOtherAdminSessions",
		"LogoutAdmin",
	})
	assertServiceMethods(t, adminv1.File_platform_admin_v1_admin_user_proto, "AdminUserService", []string{
		"ListUsers",
		"GetUser",
		"GetUserPII",
		"ListUserTags",
		"CreateUserTag",
		"UpdateUserTag",
		"DeleteUserTag",
		"SetUserTags",
		"ListUserNotes",
		"AppendUserNote",
		"PreviewUserCommand",
		"ExecuteUserCommand",
		"PreviewBatchUserOperation",
		"StartBatchUserOperation",
		"GetBatchUserOperation",
		"ListBatchUserOperations",
		"ListBatchUserOperationItems",
		"CancelBatchUserOperation",
		"RetryBatchUserOperation",
		"CreateUserExport",
		"GetUserExport",
		"ListUserExports",
		"CreateExportDownloadGrant",
		"DeleteExportResult",
	})
	assertServiceMethods(t, adminv1.File_platform_admin_v1_admin_room_proto, "AdminRoomService", []string{
		"ListRooms",
		"GetRoom",
		"ListGames",
		"GetGame",
		"SetRoomAdmission",
		"RemoveRoomMember",
		"ForceCloseRoom",
		"ForceTerminateGame",
		"PreviewEmergencyRepair",
		"ExecuteEmergencyRepair",
		"GetRepairOperation",
	})
	assertServiceMethods(t, adminv1.File_platform_admin_v1_admin_audit_proto, "AdminAuditService", []string{
		"ListAuditEvents",
	})
	assertServiceMethods(t, roomv1.File_platform_room_v1_room_proto, "RoomService", []string{
		"CreateRoom",
		"GetRoom",
		"HeartbeatRoom",
		"ListMyRooms",
		"ListPublicRooms",
		"JoinRoom",
		"ApproveMember",
		"SetAdmission",
		"SelectRoomGame",
		"UpdateGameConfig",
		"ListGameRulePresets",
		"SaveGameRulePreset",
		"DeleteGameRulePreset",
		"BeginGameStart",
		"CancelGameStart",
		"StartGame",
		"RequestRoomPause",
		"RejectRoomPauseRequest",
		"PauseRoomGame",
		"ResumeRoomGame",
		"TransferRoomHost",
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
		"BUSINESS_ERROR_CODE_ELEVATION_REQUIRED",
		"BUSINESS_ERROR_CODE_ELEVATION_EXPIRED",
		"BUSINESS_ERROR_CODE_VERSION_CONFLICT",
		"BUSINESS_ERROR_CODE_RECOVERY_CODE_EXHAUSTED",
		"BUSINESS_ERROR_CODE_MFA_STATE_CONFLICT",
		"BUSINESS_ERROR_CODE_ROOM_PAUSE_REQUEST_EXISTS",
		"BUSINESS_ERROR_CODE_ROOM_PAUSE_REQUEST_NOT_FOUND",
		"BUSINESS_ERROR_CODE_ROOM_GAME_ALREADY_PAUSED",
		"BUSINESS_ERROR_CODE_ROOM_GAME_NOT_PAUSED",
		"BUSINESS_ERROR_CODE_ROOM_HOST_TRANSFER_TARGET_INVALID",
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

func TestRoomPauseAndHostTransferContractShape(t *testing.T) {
	t.Parallel()

	roomFile := roomv1.File_platform_room_v1_room_proto
	assertPauseEnumValues(t, roomFile.Enums().ByName("PauseSource"), []string{
		"PAUSE_SOURCE_UNSPECIFIED",
		"PAUSE_SOURCE_HOST",
		"PAUSE_SOURCE_APPROVED_REQUEST",
	})
	assertPauseMessageFields(t, roomFile.Messages().ByName("PendingPauseRequest"),
		"request_id", "session_id", "requested_by_user_id", "requested_at",
	)
	assertPauseMessageFields(t, roomFile.Messages().ByName("ActivePause"),
		"pause_id", "session_id", "source", "requested_by_user_id", "paused_by_user_id", "paused_at",
	)
	assertPauseMessageFields(t, roomFile.Messages().ByName("RequestRoomPauseRequest"),
		"room_id", "session_id", "expected_version",
	)
	assertPauseMessageFields(t, roomFile.Messages().ByName("RejectRoomPauseRequestRequest"),
		"room_id", "request_id", "expected_version",
	)
	assertPauseMessageFields(t, roomFile.Messages().ByName("PauseRoomGameRequest"),
		"room_id", "session_id", "request_id", "expected_version", "ownership_epoch",
	)
	assertPauseMessageFields(t, roomFile.Messages().ByName("ResumeRoomGameRequest"),
		"room_id", "session_id", "expected_version", "ownership_epoch",
	)
	assertPauseMessageFields(t, roomFile.Messages().ByName("TransferRoomHostRequest"),
		"room_id", "target_user_id", "expected_version", "ownership_epoch",
	)

	roomMessage := roomFile.Messages().ByName("Room")
	assertPauseFieldName(t, roomMessage.Fields().ByNumber(21), "pending_pause_request")
	assertPauseFieldName(t, roomMessage.Fields().ByNumber(22), "active_pause")

	session := gamev1.File_platform_game_v1_game_proto.Messages().ByName("GameSessionSummary")
	assertPauseFieldName(t, session.Fields().ByNumber(8), "suspended_at")
}

func assertPauseEnumValues(t *testing.T, enum protoreflect.EnumDescriptor, want []string) {
	t.Helper()

	if enum == nil || enum.Values().Len() != len(want) {
		t.Fatalf("pause enum: expected %d values, got %v", len(want), enum)
	}
	for index, name := range want {
		value := enum.Values().Get(index)
		if string(value.Name()) != name || value.Number() != protoreflect.EnumNumber(index) {
			t.Fatalf("pause enum value %d: expected %q = %d, got %q = %d", index, name, index, value.Name(), value.Number())
		}
	}
}

func assertPauseMessageFields(t *testing.T, message protoreflect.MessageDescriptor, want ...string) {
	t.Helper()

	if message == nil || message.Fields().Len() != len(want) {
		t.Fatalf("pause message: expected %d fields, got %v", len(want), message)
	}
	for index, name := range want {
		assertPauseFieldName(t, message.Fields().ByNumber(protoreflect.FieldNumber(index+1)), name)
	}
}

func assertPauseFieldName(t *testing.T, field protoreflect.FieldDescriptor, want string) {
	t.Helper()

	if field == nil || string(field.Name()) != want {
		t.Fatalf("expected pause field %q, got %v", want, field)
	}
}

func TestAuditActionContractShape(t *testing.T) {
	t.Parallel()

	assertEnumValues(t, auditv1.File_platform_audit_v1_audit_proto.Enums().ByName("AuditAction"), []string{
		"AUDIT_ACTION_UNSPECIFIED",
		"AUDIT_ACTION_IDENTITY_ONBOARDED",
		"AUDIT_ACTION_IDENTITY_RECOVERED",
		"AUDIT_ACTION_RECOVERY_CODE_ROTATED",
		"AUDIT_ACTION_DEVICE_REVOKED",
		"AUDIT_ACTION_USERNAME_CHANGED",
		"AUDIT_ACTION_USERNAME_FORCE_CHANGED",
		"AUDIT_ACTION_USER_SUSPENDED",
		"AUDIT_ACTION_USER_UNSUSPENDED",
		"AUDIT_ACTION_USER_DELETED",
		"AUDIT_ACTION_ASSISTED_RECOVERY_CREATED",
		"AUDIT_ACTION_REAL_NAME_READ",
		"AUDIT_ACTION_REAL_NAME_UPDATED",
		"AUDIT_ACTION_PROFILE_EXPORT_CREATED",
		"AUDIT_ACTION_PROFILE_EXPORT_PAGE_READ",
		"AUDIT_ACTION_PROFILE_EXPORT_COMPLETED",
		"AUDIT_ACTION_PROFILE_EXPORT_ABORTED",
		"AUDIT_ACTION_PROFILE_EXPORT_EXPIRED",
		"AUDIT_ACTION_ADMIN_SETUP_COMPLETED",
		"AUDIT_ACTION_ADMIN_PASSWORD_CHANGED",
		"AUDIT_ACTION_ADMIN_TOTP_REBOUND",
		"AUDIT_ACTION_ADMIN_RECOVERY_USED",
		"AUDIT_ACTION_ADMIN_SESSIONS_REVOKED",
		"AUDIT_ACTION_ADMIN_OFFLINE_RESET",
		"AUDIT_ACTION_AUDIT_EVENTS_READ",
		"AUDIT_ACTION_KEY_ROTATION_STARTED",
		"AUDIT_ACTION_KEY_ROTATION_BATCH_COMPLETED",
		"AUDIT_ACTION_KEY_ROTATION_COMPLETED",
		"AUDIT_ACTION_ADMIN_MFA_ENABLED",
		"AUDIT_ACTION_ADMIN_MFA_DISABLED",
		"AUDIT_ACTION_ADMIN_RECOVERY_CODES_REGENERATED",
		"AUDIT_ACTION_ADMIN_SESSION_ELEVATED",
		"AUDIT_ACTION_ADMIN_ELEVATION_REVOKED",
		"AUDIT_ACTION_ADMIN_LOGIN_SUCCEEDED",
		"AUDIT_ACTION_ADMIN_LOGIN_FAILED",
		"AUDIT_ACTION_ADMIN_SECRET_RESULT_OPENED",
		"AUDIT_ACTION_ADMIN_SECRET_RESULT_CONFIRMED",
		"AUDIT_ACTION_ADMIN_ELEVATION_DENIED",
		"AUDIT_ACTION_ADMIN_ELEVATION_EXPIRED",
		"AUDIT_ACTION_ADMIN_MAINTENANCE_CHANGED",
		"AUDIT_ACTION_ADMIN_CACHE_REFRESHED",
		"AUDIT_ACTION_ADMIN_TASK_RETRIED",
	})
}

func TestDescriptorsUseBoundedPortableFields(t *testing.T) {
	t.Parallel()

	files := []protoreflect.FileDescriptor{
		commonv1.File_platform_common_v1_common_proto,
		commonv1.File_platform_common_v1_error_proto,
		identityv1.File_platform_identity_v1_identity_proto,
		adminv1.File_platform_admin_v1_admin_common_proto,
		adminv1.File_platform_admin_v1_admin_auth_proto,
		adminv1.File_platform_admin_v1_admin_user_proto,
		adminv1.File_platform_admin_v1_admin_room_proto,
		adminv1.File_platform_admin_v1_admin_audit_proto,
		auditv1.File_platform_audit_v1_audit_proto,
		roomv1.File_platform_room_v1_room_proto,
	}
	for _, file := range files {
		assertEnumsHaveUnspecifiedZero(t, file.Enums(), file.Path())
		assertMessagesUsePortableFields(t, file.Messages(), file.Path())
	}
}

func TestAdminAuditContractShape(t *testing.T) {
	t.Parallel()

	file := adminv1.File_platform_admin_v1_admin_audit_proto
	assertMessageFieldShapes(t, file.Messages().ByName("AdminAuditFilter"), []fieldShape{
		{name: "event_id", kind: protoreflect.StringKind},
		{name: "actions", kind: protoreflect.EnumKind, list: true, typeName: "platform.audit.v1.AuditAction"},
		{name: "actor_types", kind: protoreflect.EnumKind, list: true, typeName: "platform.audit.v1.AuditActorType"},
		{name: "actor_id", kind: protoreflect.StringKind},
		{name: "target_types", kind: protoreflect.EnumKind, list: true, typeName: "platform.audit.v1.AuditTargetType"},
		{name: "target_id", kind: protoreflect.StringKind},
		{name: "request_id", kind: protoreflect.StringKind},
		{name: "reason_code", kind: protoreflect.StringKind},
		{name: "occurred_from", kind: protoreflect.MessageKind, typeName: "google.protobuf.Timestamp"},
		{name: "occurred_to", kind: protoreflect.MessageKind, typeName: "google.protobuf.Timestamp"},
	})
	assertMessageFieldShapes(t, file.Messages().ByName("AdminAuditEvent"), []fieldShape{
		{name: "event_id", kind: protoreflect.StringKind},
		{name: "sequence", kind: protoreflect.Uint64Kind},
		{name: "previous_hash", kind: protoreflect.StringKind},
		{name: "event_hash", kind: protoreflect.StringKind},
		{name: "request_id", kind: protoreflect.StringKind},
		{name: "occurred_at", kind: protoreflect.MessageKind, typeName: "google.protobuf.Timestamp"},
		{name: "actor", kind: protoreflect.MessageKind, typeName: "platform.audit.v1.AuditActor"},
		{name: "target", kind: protoreflect.MessageKind, typeName: "platform.audit.v1.AuditTarget"},
		{name: "action", kind: protoreflect.EnumKind, typeName: "platform.audit.v1.AuditAction"},
		{name: "reason_code", kind: protoreflect.StringKind},
		{name: "detail_digest", kind: protoreflect.StringKind},
		{name: "signing_key_version", kind: protoreflect.Uint32Kind},
		{name: "verified", kind: protoreflect.BoolKind},
	})
	assertMessageFieldShapes(t, file.Messages().ByName("AdminAuditChainHead"), []fieldShape{
		{name: "sequence", kind: protoreflect.Uint64Kind},
		{name: "event_hash", kind: protoreflect.StringKind},
		{name: "updated_at", kind: protoreflect.MessageKind, typeName: "google.protobuf.Timestamp"},
	})
	assertMessageFieldShapes(t, file.Messages().ByName("ListAuditEventsRequest"), []fieldShape{
		{name: "filter", kind: protoreflect.MessageKind, typeName: "platform.admin.v1.AdminAuditFilter"},
		{name: "page_size", kind: protoreflect.Uint32Kind},
		{name: "page_token", kind: protoreflect.StringKind},
	})
	assertMessageFieldShapes(t, file.Messages().ByName("ListAuditEventsResponse"), []fieldShape{
		{name: "events", kind: protoreflect.MessageKind, list: true, typeName: "platform.admin.v1.AdminAuditEvent"},
		{name: "page", kind: protoreflect.MessageKind, typeName: "platform.admin.v1.AdminPageInfo"},
		{name: "chain_head", kind: protoreflect.MessageKind, typeName: "platform.admin.v1.AdminAuditChainHead"},
		{name: "scanned_events", kind: protoreflect.Uint32Kind},
	})
}

func TestAdminCommonContractShape(t *testing.T) {
	t.Parallel()

	file := adminv1.File_platform_admin_v1_admin_common_proto
	assertEnumValues(t, file.Enums().ByName("AdminAccountState"), []string{
		"ADMIN_ACCOUNT_STATE_UNSPECIFIED",
		"ADMIN_ACCOUNT_STATE_BOOTSTRAP_PENDING",
		"ADMIN_ACCOUNT_STATE_SETUP_REQUIRED",
		"ADMIN_ACCOUNT_STATE_ACTIVE",
	})
	assertEnumValues(t, file.Enums().ByName("AdminSessionKind"), []string{
		"ADMIN_SESSION_KIND_UNSPECIFIED",
		"ADMIN_SESSION_KIND_SETUP_PASSWORD_PENDING",
		"ADMIN_SESSION_KIND_MFA_PENDING",
		"ADMIN_SESSION_KIND_FULL",
	})
	assertEnumValues(t, file.Enums().ByName("AdminPermission"), []string{
		"ADMIN_PERMISSION_UNSPECIFIED",
		"ADMIN_PERMISSION_OVERVIEW_READ",
		"ADMIN_PERMISSION_USERS_READ",
		"ADMIN_PERMISSION_USERS_READ_PII",
		"ADMIN_PERMISSION_USERS_ANNOTATE",
		"ADMIN_PERMISSION_USERS_GOVERN",
		"ADMIN_PERMISSION_USERS_EXPORT",
		"ADMIN_PERMISSION_ROOMS_READ",
		"ADMIN_PERMISSION_ROOMS_CONTROL",
		"ADMIN_PERMISSION_GAMES_READ",
		"ADMIN_PERMISSION_GAMES_CONTROL",
		"ADMIN_PERMISSION_GAMES_REPAIR",
		"ADMIN_PERMISSION_SECURITY_READ",
		"ADMIN_PERMISSION_SECURITY_MANAGE_PASSWORD",
		"ADMIN_PERMISSION_SECURITY_MANAGE_MFA",
		"ADMIN_PERMISSION_SECURITY_MANAGE_SESSIONS",
		"ADMIN_PERMISSION_AUDIT_READ",
		"ADMIN_PERMISSION_AUDIT_EXPORT",
		"ADMIN_PERMISSION_OPERATIONS_READ",
		"ADMIN_PERMISSION_OPERATIONS_MAINTAIN",
	})
	assertEnumValues(t, file.Enums().ByName("AdminElevationScope"), []string{
		"ADMIN_ELEVATION_SCOPE_UNSPECIFIED",
		"ADMIN_ELEVATION_SCOPE_USERS_BULK_GOVERNANCE",
		"ADMIN_ELEVATION_SCOPE_USERS_REVOKE_DEVICES",
		"ADMIN_ELEVATION_SCOPE_USERS_DELETE",
		"ADMIN_ELEVATION_SCOPE_ROOMS_FORCE_CLOSE",
		"ADMIN_ELEVATION_SCOPE_GAMES_FORCE_TERMINATE",
		"ADMIN_ELEVATION_SCOPE_GAMES_EMERGENCY_REPAIR",
		"ADMIN_ELEVATION_SCOPE_OPERATIONS_MAINTENANCE",
		"ADMIN_ELEVATION_SCOPE_SECURITY_DISABLE_MFA",
		"ADMIN_ELEVATION_SCOPE_SECURITY_REGENERATE_RECOVERY_CODES",
		"ADMIN_ELEVATION_SCOPE_SECURITY_REVOKE_SESSIONS",
		"ADMIN_ELEVATION_SCOPE_AUDIT_EXPORT_SENSITIVE",
	})
	assertEnumValues(t, file.Enums().ByName("AdminSortDirection"), []string{
		"ADMIN_SORT_DIRECTION_UNSPECIFIED",
		"ADMIN_SORT_DIRECTION_ASCENDING",
		"ADMIN_SORT_DIRECTION_DESCENDING",
	})
	assertEnumValues(t, file.Enums().ByName("AdminJobState"), []string{
		"ADMIN_JOB_STATE_UNSPECIFIED",
		"ADMIN_JOB_STATE_QUEUED",
		"ADMIN_JOB_STATE_RUNNING",
		"ADMIN_JOB_STATE_SUCCEEDED",
		"ADMIN_JOB_STATE_PARTIALLY_SUCCEEDED",
		"ADMIN_JOB_STATE_FAILED",
		"ADMIN_JOB_STATE_CANCELING",
		"ADMIN_JOB_STATE_CANCELED",
		"ADMIN_JOB_STATE_EXPIRED",
		"ADMIN_JOB_STATE_DELETED",
	})

	assertMessageFieldShapes(t, file.Messages().ByName("AdminElevationSummary"), []fieldShape{
		{name: "scope", kind: protoreflect.EnumKind, typeName: "platform.admin.v1.AdminElevationScope"},
		{name: "granted_at", kind: protoreflect.MessageKind, typeName: "google.protobuf.Timestamp"},
		{name: "expires_at", kind: protoreflect.MessageKind, typeName: "google.protobuf.Timestamp"},
	})
	assertMessageFieldShapes(t, file.Messages().ByName("AdminMfaState"), []fieldShape{
		{name: "enabled", kind: protoreflect.BoolKind},
		{name: "recovery_codes_remaining", kind: protoreflect.Int32Kind},
		{name: "enrollment_version", kind: protoreflect.Uint64Kind},
		{name: "recovery_codes_version", kind: protoreflect.Uint64Kind},
	})
	assertMessageFieldShapes(t, file.Messages().ByName("AdminSessionSummary"), []fieldShape{
		{name: "admin_id", kind: protoreflect.StringKind},
		{name: "session_id", kind: protoreflect.StringKind},
		{name: "kind", kind: protoreflect.EnumKind, typeName: "platform.admin.v1.AdminSessionKind"},
		{name: "permissions", kind: protoreflect.EnumKind, list: true, typeName: "platform.admin.v1.AdminPermission"},
		{name: "mfa", kind: protoreflect.MessageKind, typeName: "platform.admin.v1.AdminMfaState"},
		{name: "elevations", kind: protoreflect.MessageKind, list: true, typeName: "platform.admin.v1.AdminElevationSummary"},
		{name: "admin_version", kind: protoreflect.Uint64Kind},
		{name: "password_version", kind: protoreflect.Uint64Kind},
		{name: "session_version", kind: protoreflect.Uint64Kind},
		{name: "idle_expires_at", kind: protoreflect.MessageKind, typeName: "google.protobuf.Timestamp"},
		{name: "absolute_expires_at", kind: protoreflect.MessageKind, typeName: "google.protobuf.Timestamp"},
	})
	assertMessageFieldShapes(t, file.Messages().ByName("AdminSessionInfo"), []fieldShape{
		{name: "session_id", kind: protoreflect.StringKind},
		{name: "kind", kind: protoreflect.EnumKind, typeName: "platform.admin.v1.AdminSessionKind"},
		{name: "current", kind: protoreflect.BoolKind},
		{name: "session_version", kind: protoreflect.Uint64Kind},
		{name: "created_at", kind: protoreflect.MessageKind, typeName: "google.protobuf.Timestamp"},
		{name: "last_activity_at", kind: protoreflect.MessageKind, typeName: "google.protobuf.Timestamp"},
		{name: "idle_expires_at", kind: protoreflect.MessageKind, typeName: "google.protobuf.Timestamp"},
		{name: "absolute_expires_at", kind: protoreflect.MessageKind, typeName: "google.protobuf.Timestamp"},
		{name: "client_ip", kind: protoreflect.StringKind},
		{name: "user_agent", kind: protoreflect.StringKind},
		{name: "active_elevation_scopes", kind: protoreflect.EnumKind, list: true, typeName: "platform.admin.v1.AdminElevationScope"},
	})
	assertMessageFieldShapes(t, file.Messages().ByName("AdminPageInfo"), []fieldShape{
		{name: "next_page_token", kind: protoreflect.StringKind},
		{name: "sampled_at", kind: protoreflect.MessageKind, typeName: "google.protobuf.Timestamp"},
	})
	assertMessageFieldShapes(t, file.Messages().ByName("AdminOperationReceipt"), []fieldShape{
		{name: "operation_id", kind: protoreflect.StringKind},
		{name: "audit_event_id", kind: protoreflect.StringKind},
		{name: "completed_at", kind: protoreflect.MessageKind, typeName: "google.protobuf.Timestamp"},
	})
}

func TestAdminRoomContractShape(t *testing.T) {
	t.Parallel()

	file := adminv1.File_platform_admin_v1_admin_room_proto
	assertEnumValues(t, file.Enums().ByName("AdminRepairType"), []string{
		"ADMIN_REPAIR_TYPE_UNSPECIFIED",
		"ADMIN_REPAIR_TYPE_CLEAR_STALE_OWNER_LEASE",
		"ADMIN_REPAIR_TYPE_TERMINATE_UNRECOVERABLE_GAME",
		"ADMIN_REPAIR_TYPE_REPAIR_ROOM_GAME_LINK",
	})
	assertEnumValues(t, file.Enums().ByName("AdminRoomCommandOutcome"), []string{
		"ADMIN_ROOM_COMMAND_OUTCOME_UNSPECIFIED",
		"ADMIN_ROOM_COMMAND_OUTCOME_EXECUTED",
		"ADMIN_ROOM_COMMAND_OUTCOME_NO_CHANGE",
		"ADMIN_ROOM_COMMAND_OUTCOME_VERSION_CONFLICT",
		"ADMIN_ROOM_COMMAND_OUTCOME_OWNER_UNREACHABLE",
		"ADMIN_ROOM_COMMAND_OUTCOME_REPAIR_REQUIRED",
		"ADMIN_ROOM_COMMAND_OUTCOME_REJECTED",
	})
	assertMessageFieldShapes(t, file.Messages().ByName("ForceTerminateGameRequest"), []fieldShape{
		{name: "operation_id", kind: protoreflect.StringKind},
		{name: "session_id", kind: protoreflect.StringKind},
		{name: "reason", kind: protoreflect.StringKind},
		{name: "expected_state_version", kind: protoreflect.Uint64Kind},
		{name: "expected_ownership_epoch", kind: protoreflect.Uint64Kind},
	})
	assertMessageFieldShapes(t, file.Messages().ByName("ExecuteEmergencyRepairRequest"), []fieldShape{
		{name: "operation_id", kind: protoreflect.StringKind},
		{name: "repair_id", kind: protoreflect.StringKind},
		{name: "reason", kind: protoreflect.StringKind},
		{name: "expected_repair_version", kind: protoreflect.Uint64Kind},
	})
	assertMessageDoesNotContainFields(t, file.Messages().ByName("AdminRepairOperation"), "patch", "json", "state_payload", "replay")
}

func TestAdminCurrentSessionContractShape(t *testing.T) {
	t.Parallel()

	file := adminv1.File_platform_admin_v1_admin_auth_proto
	request := file.Messages().ByName("GetCurrentAdminSessionRequest")
	if request == nil || request.Fields().Len() != 0 {
		t.Fatalf("GetCurrentAdminSessionRequest must stay empty: %+v", request)
	}

	response := file.Messages().ByName("GetCurrentAdminSessionResponse")
	if response == nil {
		t.Fatal("GetCurrentAdminSessionResponse is missing")
	}
	if response.Fields().Len() != 1 {
		t.Fatalf("GetCurrentAdminSessionResponse field count = %d", response.Fields().Len())
	}
	if field := response.Fields().ByNumber(1); field == nil || string(field.Name()) != "session" {
		t.Fatalf("field 1 = %v", field)
	}
	if field := response.Fields().ByName("next_step"); field != nil {
		t.Fatalf("GetCurrentAdminSessionResponse must not expose next_step: %v", field)
	}
}

func TestAdminUserPIIIsIsolatedFromOrdinaryDetails(t *testing.T) {
	t.Parallel()

	file := adminv1.File_platform_admin_v1_admin_user_proto
	assertMessageFields(t, file.Messages().ByName("GetUserRequest"), "user_id")
	assertMessageFields(t, file.Messages().ByName("GetUserResponse"), "user", "sampled_at")
	detail := file.Messages().ByName("AdminUserDetail")
	if detail == nil {
		t.Fatal("AdminUserDetail is missing")
	}
	for _, forbidden := range []protoreflect.Name{"real_name", "pii_values", "values"} {
		if field := detail.Fields().ByName(forbidden); field != nil {
			t.Fatalf("GetUser detail exposes PII field %s", field.FullName())
		}
	}
	assertMessageFields(t, file.Messages().ByName("GetUserPIIRequest"), "user_id", "fields", "reason")
	assertMessageFields(t, file.Messages().ByName("GetUserPIIResponse"), "user_id", "values", "access_audit_event_id", "accessed_at")
}

func TestAdminUserWritesCarryIdempotencyReasonAndVersion(t *testing.T) {
	t.Parallel()

	file := adminv1.File_platform_admin_v1_admin_user_proto
	for _, messageName := range []protoreflect.Name{
		"CreateUserTagRequest",
		"UpdateUserTagRequest",
		"DeleteUserTagRequest",
		"SetUserTagsRequest",
		"AppendUserNoteRequest",
		"ExecuteUserCommandRequest",
		"StartBatchUserOperationRequest",
		"CancelBatchUserOperationRequest",
		"RetryBatchUserOperationRequest",
		"CreateUserExportRequest",
		"CreateExportDownloadGrantRequest",
		"DeleteExportResultRequest",
	} {
		message := file.Messages().ByName(messageName)
		if message == nil {
			t.Fatalf("%s is missing", messageName)
		}
		if message.Fields().ByName("operation_id") == nil || message.Fields().ByName("reason") == nil {
			t.Fatalf("%s must carry operation_id and reason", messageName)
		}
		if message.Fields().ByName("expected_version") == nil && message.Fields().ByName("expected_user_version") == nil {
			t.Fatalf("%s must carry an expected version", messageName)
		}
	}
}

func TestAdminUserBatchAndDownloadContractsStayBounded(t *testing.T) {
	t.Parallel()

	file := adminv1.File_platform_admin_v1_admin_user_proto
	assertMessageFields(t, file.Messages().ByName("ListUsersRequest"), "filter", "sort", "page_size", "page_token")
	assertMessageFields(t, file.Messages().ByName("ListUsersResponse"), "users", "page")
	assertMessageFields(t, file.Messages().ByName("AdminUserCommand"),
		"type", "room_id", "expected_room_version", "expected_membership_version",
	)
	assertEnumValues(t, file.Enums().ByName("AdminBatchUserCommandType"), []string{
		"ADMIN_BATCH_USER_COMMAND_TYPE_UNSPECIFIED",
		"ADMIN_BATCH_USER_COMMAND_TYPE_SUSPEND",
		"ADMIN_BATCH_USER_COMMAND_TYPE_UNSUSPEND",
		"ADMIN_BATCH_USER_COMMAND_TYPE_REMOVE_FROM_CURRENT_ROOM",
	})
	selection := file.Messages().ByName("AdminUserSelection")
	if selection == nil || selection.Oneofs().Len() != 1 || selection.Oneofs().ByName("selection").Fields().Len() != 2 {
		t.Fatalf("AdminUserSelection must contain exactly filter or explicit targets: %v", selection)
	}
	grant := file.Messages().ByName("CreateExportDownloadGrantResponse")
	assertMessageFields(t, grant, "receipt", "download_grant", "expires_at")
	assertMessageFields(t, file.Messages().ByName("CreateExportDownloadGrantRequest"),
		"operation_id", "export_id", "reason", "expected_version", "expected_masking_policy",
	)
	if grant.Fields().ByName("url") != nil || grant.Fields().ByName("download_url") != nil {
		t.Fatal("download grant response must not expose an object URL")
	}
}

func TestAdminLoginPasswordOutcomeContractShape(t *testing.T) {
	t.Parallel()

	file := adminv1.File_platform_admin_v1_admin_auth_proto
	response := file.Messages().ByName("LoginPasswordResponse")
	if response == nil {
		t.Fatal("LoginPasswordResponse is missing")
	}
	if response.Oneofs().Len() != 1 {
		t.Fatalf("LoginPasswordResponse oneof count = %d", response.Oneofs().Len())
	}
	outcome := response.Oneofs().ByName("outcome")
	if outcome == nil || outcome.Fields().Len() != 3 {
		t.Fatalf("LoginPasswordResponse outcome = %v", outcome)
	}
	assertFieldName(t, outcome.Fields().ByNumber(1), "requires_initial_password_change")
	assertFieldName(t, outcome.Fields().ByNumber(2), "requires_mfa")
	assertFieldName(t, outcome.Fields().ByNumber(3), "session")
}

func TestAdminSessionRevocationCommandShapes(t *testing.T) {
	t.Parallel()

	file := adminv1.File_platform_admin_v1_admin_auth_proto
	assertMessageFields(t, file.Messages().ByName("RevokeAdminSessionRequest"),
		"operation_id",
		"session_id",
		"expected_session_version",
	)
	assertMessageFields(t, file.Messages().ByName("PreviewRevokeOtherAdminSessionsRequest"))
	assertMessageFields(t, file.Messages().ByName("RevokeOtherAdminSessionsRequest"),
		"operation_id",
		"preview_version",
		"expected_admin_version",
		"expected_current_session_version",
	)
}

func TestAdminRuntimeReadinessContractShape(t *testing.T) {
	t.Parallel()

	file := adminv1.File_platform_admin_v1_admin_auth_proto
	request := file.Messages().ByName("GetRuntimeReadinessRequest")
	if request == nil || request.Fields().Len() != 0 {
		t.Fatalf("GetRuntimeReadinessRequest must stay empty: %+v", request)
	}

	state := file.Messages().ByName("RuntimeReadinessState")
	if state == nil || state.Fields().Len() != 3 {
		t.Fatalf("RuntimeReadinessState field count is invalid: %+v", state)
	}
	if field := state.Fields().ByNumber(1); field == nil || string(field.Name()) != "mode" {
		t.Fatalf("RuntimeReadinessState field 1 = %v", field)
	}
	if field := state.Fields().ByNumber(2); field == nil || string(field.Name()) != "ready" {
		t.Fatalf("RuntimeReadinessState field 2 = %v", field)
	}
	if field := state.Fields().ByNumber(3); field == nil || string(field.Name()) != "components" || !field.IsMap() {
		t.Fatalf("RuntimeReadinessState field 3 = %v", field)
	}

	response := file.Messages().ByName("GetRuntimeReadinessResponse")
	if response == nil || response.Fields().Len() != 2 {
		t.Fatalf("GetRuntimeReadinessResponse field count is invalid: %+v", response)
	}
	if field := response.Fields().ByNumber(1); field == nil || string(field.Name()) != "ordinary" {
		t.Fatalf("GetRuntimeReadinessResponse field 1 = %v", field)
	}
	if field := response.Fields().ByNumber(2); field == nil || string(field.Name()) != "sensitive" {
		t.Fatalf("GetRuntimeReadinessResponse field 2 = %v", field)
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

func assertMessageFields(t *testing.T, message protoreflect.MessageDescriptor, want ...string) {
	t.Helper()

	if message == nil {
		t.Fatal("message descriptor is missing")
	}
	if message.Fields().Len() != len(want) {
		t.Fatalf("%s: expected %d fields, got %d", message.FullName(), len(want), message.Fields().Len())
	}
	for index, name := range want {
		assertFieldName(t, message.Fields().ByNumber(protoreflect.FieldNumber(index+1)), name)
	}
}

func assertFieldName(t *testing.T, field protoreflect.FieldDescriptor, want string) {
	t.Helper()

	if field == nil || string(field.Name()) != want {
		t.Fatalf("expected field %q, got %v", want, field)
	}
}

type fieldShape struct {
	name     string
	kind     protoreflect.Kind
	list     bool
	typeName protoreflect.FullName
}

func assertEnumValues(t *testing.T, enum protoreflect.EnumDescriptor, want []string) {
	t.Helper()

	if enum == nil {
		t.Fatal("enum descriptor is missing")
	}
	if enum.Values().Len() != len(want) {
		t.Fatalf("%s: expected %d values, got %d", enum.FullName(), len(want), enum.Values().Len())
	}
	for index, name := range want {
		value := enum.Values().Get(index)
		if got := string(value.Name()); got != name || value.Number() != protoreflect.EnumNumber(index) {
			t.Fatalf("%s value %d: expected %q = %d, got %q = %d", enum.FullName(), index, name, index, value.Name(), value.Number())
		}
	}
}

func assertMessageFieldShapes(t *testing.T, message protoreflect.MessageDescriptor, want []fieldShape) {
	t.Helper()

	if message == nil {
		t.Fatal("message descriptor is missing")
	}
	if message.Fields().Len() != len(want) {
		t.Fatalf("%s: expected %d fields, got %d", message.FullName(), len(want), message.Fields().Len())
	}
	for index, expected := range want {
		field := message.Fields().ByNumber(protoreflect.FieldNumber(index + 1))
		if field == nil || string(field.Name()) != expected.name || field.Kind() != expected.kind || field.IsList() != expected.list {
			t.Fatalf("%s field %d: expected %+v, got %v", message.FullName(), index+1, expected, field)
		}
		if expected.typeName == "" {
			continue
		}
		var got protoreflect.FullName
		switch field.Kind() {
		case protoreflect.EnumKind:
			got = field.Enum().FullName()
		case protoreflect.MessageKind:
			got = field.Message().FullName()
		}
		if got != expected.typeName {
			t.Fatalf("%s field %d: expected type %s, got %s", message.FullName(), index+1, expected.typeName, got)
		}
	}
}

func assertMessageDoesNotContainFields(t *testing.T, message protoreflect.MessageDescriptor, blocked ...string) {
	t.Helper()

	if message == nil {
		t.Fatal("message descriptor is missing")
	}
	blockedSet := make(map[string]struct{}, len(blocked))
	for _, name := range blocked {
		blockedSet[name] = struct{}{}
	}
	for index := range message.Fields().Len() {
		field := message.Fields().Get(index)
		if _, exists := blockedSet[string(field.Name())]; exists {
			t.Fatalf("%s contains blocked field %s", message.FullName(), field.Name())
		}
	}
}
