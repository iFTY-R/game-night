package adminauth

import (
	"time"

	adminv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1/adminv1connect"
	"github.com/iFTY-R/game-night/platform/admin"
)

type sessionRequirement uint8

const (
	sessionRequirementNone sessionRequirement = iota
	sessionRequirementAuthenticated
	sessionRequirementSetup
	sessionRequirementMFAPending
	sessionRequirementFull
)

type procedurePolicy struct {
	procedure             string
	session               sessionRequirement
	requiresChallenge     bool
	requiresCSRF          bool
	requiresRequestID     bool
	requiresRequestFlowID bool
	permission            admin.Permission
	// permissionsAny is reserved for payload-dispatched RPCs whose exact permission is narrowed again by the domain handler.
	permissionsAny []admin.Permission
	elevation      admin.ElevationScope
}

var procedurePolicies = map[string]procedurePolicy{
	"/platform.admin.v1.AdminAuthService/GetSetupState": {
		procedure: "/platform.admin.v1.AdminAuthService/GetSetupState",
	},
	"/platform.admin.v1.AdminAuthService/GetCurrentAdminSession": {
		procedure:    "/platform.admin.v1.AdminAuthService/GetCurrentAdminSession",
		session:      sessionRequirementAuthenticated,
		requiresCSRF: true,
	},
	"/platform.admin.v1.AdminAuthService/GetRuntimeReadiness": {
		procedure:    "/platform.admin.v1.AdminAuthService/GetRuntimeReadiness",
		session:      sessionRequirementFull,
		requiresCSRF: true,
		permission:   admin.PermissionOperationsRead,
	},
	"/platform.admin.v1.AdminAuthService/BeginAdminLogin": {
		procedure: "/platform.admin.v1.AdminAuthService/BeginAdminLogin",
	},
	"/platform.admin.v1.AdminAuthService/LoginPassword": {
		procedure:             "/platform.admin.v1.AdminAuthService/LoginPassword",
		requiresChallenge:     true,
		requiresRequestID:     true,
		requiresRequestFlowID: true,
	},
	"/platform.admin.v1.AdminAuthService/VerifyAdminTotp": {
		procedure:         "/platform.admin.v1.AdminAuthService/VerifyAdminTotp",
		session:           sessionRequirementMFAPending,
		requiresCSRF:      true,
		requiresRequestID: true,
	},
	"/platform.admin.v1.AdminAuthService/VerifyAdminRecoveryCode": {
		procedure:         "/platform.admin.v1.AdminAuthService/VerifyAdminRecoveryCode",
		session:           sessionRequirementMFAPending,
		requiresCSRF:      true,
		requiresRequestID: true,
	},
	"/platform.admin.v1.AdminAuthService/ChangeInitialPassword": {
		procedure:         "/platform.admin.v1.AdminAuthService/ChangeInitialPassword",
		session:           sessionRequirementSetup,
		requiresCSRF:      true,
		requiresRequestID: true,
	},
	"/platform.admin.v1.AdminAuthService/ChangeAdminPassword": {
		procedure:         "/platform.admin.v1.AdminAuthService/ChangeAdminPassword",
		session:           sessionRequirementFull,
		requiresCSRF:      true,
		requiresRequestID: true,
		permission:        admin.PermissionSecurityManagePassword,
	},
	"/platform.admin.v1.AdminAuthService/BeginTotpEnrollment": {
		procedure:         "/platform.admin.v1.AdminAuthService/BeginTotpEnrollment",
		session:           sessionRequirementFull,
		requiresCSRF:      true,
		requiresRequestID: true,
		permission:        admin.PermissionSecurityManageMFA,
	},
	"/platform.admin.v1.AdminAuthService/CompleteTotpEnrollment": {
		procedure:         "/platform.admin.v1.AdminAuthService/CompleteTotpEnrollment",
		session:           sessionRequirementFull,
		requiresCSRF:      true,
		requiresRequestID: true,
		permission:        admin.PermissionSecurityManageMFA,
	},
	"/platform.admin.v1.AdminAuthService/DisableTotp": {
		procedure:         "/platform.admin.v1.AdminAuthService/DisableTotp",
		session:           sessionRequirementFull,
		requiresCSRF:      true,
		requiresRequestID: true,
		permission:        admin.PermissionSecurityManageMFA,
		elevation:         admin.ElevationScopeSecurityDisableMFA,
	},
	"/platform.admin.v1.AdminAuthService/RegenerateAdminRecoveryCodes": {
		procedure:         "/platform.admin.v1.AdminAuthService/RegenerateAdminRecoveryCodes",
		session:           sessionRequirementFull,
		requiresCSRF:      true,
		requiresRequestID: true,
		permission:        admin.PermissionSecurityManageMFA,
		elevation:         admin.ElevationScopeSecurityRegenerateRecoveryCodes,
	},
	"/platform.admin.v1.AdminAuthService/ConfirmAdminSecretReceipt": {
		procedure:    "/platform.admin.v1.AdminAuthService/ConfirmAdminSecretReceipt",
		session:      sessionRequirementFull,
		requiresCSRF: true,
		permission:   admin.PermissionSecurityManageMFA,
	},
	"/platform.admin.v1.AdminAuthService/ElevateAdminSession": {
		procedure:         "/platform.admin.v1.AdminAuthService/ElevateAdminSession",
		session:           sessionRequirementFull,
		requiresCSRF:      true,
		requiresRequestID: true,
	},
	"/platform.admin.v1.AdminAuthService/RevokeCurrentAdminElevation": {
		procedure:         "/platform.admin.v1.AdminAuthService/RevokeCurrentAdminElevation",
		session:           sessionRequirementFull,
		requiresCSRF:      true,
		requiresRequestID: true,
	},
	"/platform.admin.v1.AdminAuthService/ListAdminSessions": {
		procedure:    "/platform.admin.v1.AdminAuthService/ListAdminSessions",
		session:      sessionRequirementFull,
		requiresCSRF: true,
		permission:   admin.PermissionSecurityManageSessions,
	},
	"/platform.admin.v1.AdminAuthService/RevokeAdminSession": {
		procedure:         "/platform.admin.v1.AdminAuthService/RevokeAdminSession",
		session:           sessionRequirementFull,
		requiresCSRF:      true,
		requiresRequestID: true,
		permission:        admin.PermissionSecurityManageSessions,
	},
	"/platform.admin.v1.AdminAuthService/PreviewRevokeOtherAdminSessions": {
		procedure:    "/platform.admin.v1.AdminAuthService/PreviewRevokeOtherAdminSessions",
		session:      sessionRequirementFull,
		requiresCSRF: true,
		permission:   admin.PermissionSecurityManageSessions,
	},
	"/platform.admin.v1.AdminAuthService/RevokeOtherAdminSessions": {
		procedure:         "/platform.admin.v1.AdminAuthService/RevokeOtherAdminSessions",
		session:           sessionRequirementFull,
		requiresCSRF:      true,
		requiresRequestID: true,
		permission:        admin.PermissionSecurityManageSessions,
		elevation:         admin.ElevationScopeSecurityRevokeSessions,
	},
	"/platform.admin.v1.AdminAuthService/LogoutAdmin": {
		procedure:    "/platform.admin.v1.AdminAuthService/LogoutAdmin",
		session:      sessionRequirementAuthenticated,
		requiresCSRF: true,
	},
	adminv1connect.AdminUserServiceListUsersProcedure: adminUserProcedurePolicy(
		adminv1connect.AdminUserServiceListUsersProcedure, admin.PermissionUsersRead, false, "",
	),
	adminv1connect.AdminUserServiceGetUserProcedure: adminUserProcedurePolicy(
		adminv1connect.AdminUserServiceGetUserProcedure, admin.PermissionUsersRead, false, "",
	),
	adminv1connect.AdminUserServiceGetUserPIIProcedure: adminUserProcedurePolicy(
		adminv1connect.AdminUserServiceGetUserPIIProcedure, admin.PermissionUsersReadPII, true, "",
	),
	adminv1connect.AdminUserServiceListUserTagsProcedure: adminUserProcedurePolicy(
		adminv1connect.AdminUserServiceListUserTagsProcedure, admin.PermissionUsersRead, false, "",
	),
	adminv1connect.AdminUserServiceCreateUserTagProcedure: adminUserProcedurePolicy(
		adminv1connect.AdminUserServiceCreateUserTagProcedure, admin.PermissionUsersAnnotate, true, "",
	),
	adminv1connect.AdminUserServiceUpdateUserTagProcedure: adminUserProcedurePolicy(
		adminv1connect.AdminUserServiceUpdateUserTagProcedure, admin.PermissionUsersAnnotate, true, "",
	),
	adminv1connect.AdminUserServiceDeleteUserTagProcedure: adminUserProcedurePolicy(
		adminv1connect.AdminUserServiceDeleteUserTagProcedure, admin.PermissionUsersAnnotate, true, "",
	),
	adminv1connect.AdminUserServiceSetUserTagsProcedure: adminUserProcedurePolicy(
		adminv1connect.AdminUserServiceSetUserTagsProcedure, admin.PermissionUsersAnnotate, true, "",
	),
	adminv1connect.AdminUserServiceListUserNotesProcedure: adminUserProcedurePolicy(
		adminv1connect.AdminUserServiceListUserNotesProcedure, admin.PermissionUsersRead, false, "",
	),
	adminv1connect.AdminUserServiceAppendUserNoteProcedure: adminUserProcedurePolicy(
		adminv1connect.AdminUserServiceAppendUserNoteProcedure, admin.PermissionUsersAnnotate, true, "",
	),
	// The command payload selects the exact room-control or user-governance rule and any required elevation.
	// enforceRequestPolicy checks it before dispatch; the domain handler repeats the check against current resource state.
	adminv1connect.AdminUserServicePreviewUserCommandProcedure: adminUserPayloadProcedurePolicy(
		adminv1connect.AdminUserServicePreviewUserCommandProcedure, true, admin.PermissionUsersGovern, admin.PermissionRoomsControl,
	),
	adminv1connect.AdminUserServiceExecuteUserCommandProcedure: adminUserPayloadProcedurePolicy(
		adminv1connect.AdminUserServiceExecuteUserCommandProcedure, true, admin.PermissionUsersGovern, admin.PermissionRoomsControl,
	),
	adminv1connect.AdminUserServicePreviewBatchUserOperationProcedure: adminUserProcedurePolicy(
		adminv1connect.AdminUserServicePreviewBatchUserOperationProcedure, admin.PermissionUsersGovern, true, "",
	),
	adminv1connect.AdminUserServiceStartBatchUserOperationProcedure: adminUserProcedurePolicy(
		adminv1connect.AdminUserServiceStartBatchUserOperationProcedure, admin.PermissionUsersGovern, true, admin.ElevationScopeUsersBulkGovernance,
	),
	adminv1connect.AdminUserServiceGetBatchUserOperationProcedure: adminUserProcedurePolicy(
		adminv1connect.AdminUserServiceGetBatchUserOperationProcedure, admin.PermissionUsersGovern, false, "",
	),
	adminv1connect.AdminUserServiceListBatchUserOperationsProcedure: adminUserProcedurePolicy(
		adminv1connect.AdminUserServiceListBatchUserOperationsProcedure, admin.PermissionUsersGovern, false, "",
	),
	adminv1connect.AdminUserServiceListBatchUserOperationItemsProcedure: adminUserProcedurePolicy(
		adminv1connect.AdminUserServiceListBatchUserOperationItemsProcedure, admin.PermissionUsersGovern, false, "",
	),
	adminv1connect.AdminUserServiceCancelBatchUserOperationProcedure: adminUserProcedurePolicy(
		adminv1connect.AdminUserServiceCancelBatchUserOperationProcedure, admin.PermissionUsersGovern, true, admin.ElevationScopeUsersBulkGovernance,
	),
	adminv1connect.AdminUserServiceRetryBatchUserOperationProcedure: adminUserProcedurePolicy(
		adminv1connect.AdminUserServiceRetryBatchUserOperationProcedure, admin.PermissionUsersGovern, true, admin.ElevationScopeUsersBulkGovernance,
	),
	// Sensitive fields are data-dependent; enforceRequestPolicy requires audit.export_sensitive elevation before dispatch.
	adminv1connect.AdminUserServiceCreateUserExportProcedure: adminUserProcedurePolicy(
		adminv1connect.AdminUserServiceCreateUserExportProcedure, admin.PermissionUsersExport, true, "",
	),
	adminv1connect.AdminUserServiceGetUserExportProcedure: adminUserProcedurePolicy(
		adminv1connect.AdminUserServiceGetUserExportProcedure, admin.PermissionUsersExport, false, "",
	),
	adminv1connect.AdminUserServiceListUserExportsProcedure: adminUserProcedurePolicy(
		adminv1connect.AdminUserServiceListUserExportsProcedure, admin.PermissionUsersExport, false, "",
	),
	adminv1connect.AdminUserServiceCreateExportDownloadGrantProcedure: adminUserProcedurePolicy(
		adminv1connect.AdminUserServiceCreateExportDownloadGrantProcedure, admin.PermissionUsersExport, true, "",
	),
	adminv1connect.AdminUserServiceDeleteExportResultProcedure: adminUserProcedurePolicy(
		adminv1connect.AdminUserServiceDeleteExportResultProcedure, admin.PermissionUsersExport, true, "",
	),
}

// adminUserProcedurePolicy applies the invariant full-session and CSRF boundary to every user-center RPC.
func adminUserProcedurePolicy(procedure string, permission admin.Permission, requiresRequestID bool, elevation admin.ElevationScope) procedurePolicy {
	return procedurePolicy{
		procedure:         procedure,
		session:           sessionRequirementFull,
		requiresCSRF:      true,
		requiresRequestID: requiresRequestID,
		permission:        permission,
		elevation:         elevation,
	}
}

// adminUserPayloadProcedurePolicy admits only actors with a relevant capability before enforceRequestPolicy narrows the typed command.
func adminUserPayloadProcedurePolicy(procedure string, requiresRequestID bool, permissions ...admin.Permission) procedurePolicy {
	return procedurePolicy{
		procedure:         procedure,
		session:           sessionRequirementFull,
		requiresCSRF:      true,
		requiresRequestID: requiresRequestID,
		permissionsAny:    append([]admin.Permission(nil), permissions...),
	}
}

// enforceRequestPolicy applies the payload-dependent permission and elevation rules that a per-procedure map cannot express.
func enforceRequestPolicy(policy procedurePolicy, actor admin.ActorContext, message any, at time.Time) error {
	switch policy.procedure {
	case adminv1connect.AdminUserServicePreviewUserCommandProcedure:
		request, ok := message.(*adminv1.PreviewUserCommandRequest)
		if !ok {
			return admin.ErrPermissionDenied
		}
		return enforceUserCommandPolicy(actor, request.GetCommand(), false, at)
	case adminv1connect.AdminUserServiceExecuteUserCommandProcedure:
		request, ok := message.(*adminv1.ExecuteUserCommandRequest)
		if !ok {
			return admin.ErrPermissionDenied
		}
		return enforceUserCommandPolicy(actor, request.GetCommand(), true, at)
	case adminv1connect.AdminUserServiceCreateUserExportProcedure:
		request, ok := message.(*adminv1.CreateUserExportRequest)
		if !ok {
			return admin.ErrPermissionDenied
		}
		return enforceSensitiveUserExportPolicy(actor, request.GetMaskingPolicy(), at)
	case adminv1connect.AdminUserServiceCreateExportDownloadGrantProcedure:
		request, ok := message.(*adminv1.CreateExportDownloadGrantRequest)
		if !ok {
			return admin.ErrPermissionDenied
		}
		return enforceSensitiveUserExportPolicy(actor, request.GetExpectedMaskingPolicy(), at)
	default:
		return nil
	}
}

func enforceUserCommandPolicy(actor admin.ActorContext, command *adminv1.AdminUserCommand, requireElevation bool, at time.Time) error {
	if command == nil {
		return admin.ErrPermissionDenied
	}
	switch command.GetType() {
	case adminv1.AdminUserCommandType_ADMIN_USER_COMMAND_TYPE_SUSPEND,
		adminv1.AdminUserCommandType_ADMIN_USER_COMMAND_TYPE_UNSUSPEND:
		return actor.Require(admin.PermissionUsersGovern)
	case adminv1.AdminUserCommandType_ADMIN_USER_COMMAND_TYPE_REMOVE_FROM_CURRENT_ROOM:
		return actor.Require(admin.PermissionRoomsControl)
	case adminv1.AdminUserCommandType_ADMIN_USER_COMMAND_TYPE_REVOKE_ALL_DEVICES:
		if err := actor.Require(admin.PermissionUsersGovern); err != nil || !requireElevation {
			return err
		}
		return actor.RequireElevation(admin.ElevationScopeUsersRevokeDevices, at)
	case adminv1.AdminUserCommandType_ADMIN_USER_COMMAND_TYPE_DELETE:
		if err := actor.Require(admin.PermissionUsersGovern); err != nil || !requireElevation {
			return err
		}
		return actor.RequireElevation(admin.ElevationScopeUsersDelete, at)
	default:
		return admin.ErrPermissionDenied
	}
}

func enforceSensitiveUserExportPolicy(actor admin.ActorContext, maskingPolicy adminv1.AdminUserExportMaskingPolicy, at time.Time) error {
	switch maskingPolicy {
	case adminv1.AdminUserExportMaskingPolicy_ADMIN_USER_EXPORT_MASKING_POLICY_REDACT_PII:
		return nil
	case adminv1.AdminUserExportMaskingPolicy_ADMIN_USER_EXPORT_MASKING_POLICY_INCLUDE_AUTHORIZED_PII:
		return actor.RequireElevation(admin.ElevationScopeAuditExportSensitive, at)
	default:
		return admin.ErrPermissionDenied
	}
}

func policyForProcedure(procedure string) (procedurePolicy, bool) {
	policy, ok := procedurePolicies[procedure]
	return policy, ok
}

func permissionForElevationScope(scope admin.ElevationScope) (admin.Permission, bool) {
	switch scope {
	case admin.ElevationScopeUsersBulkGovernance, admin.ElevationScopeUsersDelete:
		return admin.PermissionUsersGovern, true
	case admin.ElevationScopeUsersRevokeDevices:
		return admin.PermissionUsersGovern, true
	case admin.ElevationScopeRoomsForceClose:
		return admin.PermissionRoomsControl, true
	case admin.ElevationScopeGamesForceTerminate:
		return admin.PermissionGamesControl, true
	case admin.ElevationScopeGamesEmergencyRepair:
		return admin.PermissionGamesRepair, true
	case admin.ElevationScopeOperationsMaintenance:
		return admin.PermissionOperationsMaintain, true
	case admin.ElevationScopeSecurityDisableMFA, admin.ElevationScopeSecurityRegenerateRecoveryCodes:
		return admin.PermissionSecurityManageMFA, true
	case admin.ElevationScopeSecurityRevokeSessions:
		return admin.PermissionSecurityManageSessions, true
	case admin.ElevationScopeAuditExportSensitive:
		return admin.PermissionAuditExport, true
	default:
		return "", false
	}
}
