package adminauth

import "github.com/iFTY-R/game-night/platform/admin"

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
	elevation             admin.ElevationScope
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
		return admin.PermissionUsersReadPII, true
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
