package admin

import "time"

// Permission is a stable capability label shared by backend authorization, transport policy, and UI gating.
type Permission string

const (
	// PermissionOverviewRead authorizes read-only overview and health summaries.
	PermissionOverviewRead Permission = "overview.read"
	// PermissionUsersRead authorizes user-list and user-detail reads that exclude PII fields.
	PermissionUsersRead Permission = "users.read"
	// PermissionUsersReadPII authorizes sensitive identity reads such as real-name fields.
	PermissionUsersReadPII Permission = "users.read_pii"
	// PermissionUsersAnnotate authorizes tag and note mutations on user records.
	PermissionUsersAnnotate Permission = "users.annotate"
	// PermissionUsersGovern authorizes user governance commands that change account accessibility.
	PermissionUsersGovern Permission = "users.govern"
	// PermissionUsersExport authorizes user export job creation and deletion.
	PermissionUsersExport Permission = "users.export"
	// PermissionRoomsRead authorizes room queries and room details.
	PermissionRoomsRead Permission = "rooms.read"
	// PermissionRoomsControl authorizes day-to-day room control commands.
	PermissionRoomsControl Permission = "rooms.control"
	// PermissionGamesRead authorizes game-session queries and game details.
	PermissionGamesRead Permission = "games.read"
	// PermissionGamesControl authorizes ordinary game control commands.
	PermissionGamesControl Permission = "games.control"
	// PermissionGamesRepair authorizes emergency-repair command families before elevation is checked.
	PermissionGamesRepair Permission = "games.repair"
	// PermissionSecurityRead authorizes security settings reads.
	PermissionSecurityRead Permission = "security.read"
	// PermissionSecurityManagePassword authorizes password-change entry points.
	PermissionSecurityManagePassword Permission = "security.manage_password"
	// PermissionSecurityManageMFA authorizes MFA enable, disable, and recovery-code workflows.
	PermissionSecurityManageMFA Permission = "security.manage_mfa"
	// PermissionSecurityManageSessions authorizes admin-session management commands.
	PermissionSecurityManageSessions Permission = "security.manage_sessions"
	// PermissionAuditRead authorizes audit-event reads.
	PermissionAuditRead Permission = "audit.read"
	// PermissionAuditExport authorizes audit export job creation and deletion.
	PermissionAuditExport Permission = "audit.export"
	// PermissionOperationsRead authorizes operations and maintenance state reads.
	PermissionOperationsRead Permission = "operations.read"
	// PermissionOperationsMaintain authorizes controlled maintenance commands before elevation is checked.
	PermissionOperationsMaintain Permission = "operations.maintain"
)

// Legacy permissions remain only so pre-rebuild files still compile until their planned removal.
const (
	PermissionGetUser        Permission = "legacy.get_user"
	PermissionGetRealName    Permission = "legacy.get_real_name"
	PermissionUpdateRealName Permission = "legacy.update_real_name"
	PermissionExportProfile  Permission = "legacy.export_profile"
	PermissionManageRecovery Permission = "legacy.manage_recovery"
	PermissionForceUsername  Permission = "legacy.force_username"
	PermissionSuspendUser    Permission = "legacy.suspend_user"
	PermissionDeleteUser     Permission = "legacy.delete_user"
	PermissionRevokeDevice   Permission = "legacy.revoke_device"
	PermissionReadAudit      Permission = "legacy.read_audit"
)

var orderedPermissions = []Permission{
	PermissionOverviewRead,
	PermissionUsersRead,
	PermissionUsersReadPII,
	PermissionUsersAnnotate,
	PermissionUsersGovern,
	PermissionUsersExport,
	PermissionRoomsRead,
	PermissionRoomsControl,
	PermissionGamesRead,
	PermissionGamesControl,
	PermissionGamesRepair,
	PermissionSecurityRead,
	PermissionSecurityManagePassword,
	PermissionSecurityManageMFA,
	PermissionSecurityManageSessions,
	PermissionAuditRead,
	PermissionAuditExport,
	PermissionOperationsRead,
	PermissionOperationsMaintain,
}

var allPermissions = map[Permission]struct{}{
	PermissionOverviewRead:           {},
	PermissionUsersRead:              {},
	PermissionUsersReadPII:           {},
	PermissionUsersAnnotate:          {},
	PermissionUsersGovern:            {},
	PermissionUsersExport:            {},
	PermissionRoomsRead:              {},
	PermissionRoomsControl:           {},
	PermissionGamesRead:              {},
	PermissionGamesControl:           {},
	PermissionGamesRepair:            {},
	PermissionSecurityRead:           {},
	PermissionSecurityManagePassword: {},
	PermissionSecurityManageMFA:      {},
	PermissionSecurityManageSessions: {},
	PermissionAuditRead:              {},
	PermissionAuditExport:            {},
	PermissionOperationsRead:         {},
	PermissionOperationsMaintain:     {},
}

// Valid reports whether the permission belongs to the rebuilt admin console contract.
func (permission Permission) Valid() bool {
	_, ok := allPermissions[permission]
	return ok
}

// PermissionSet is an immutable default-deny set of validated permission enums.
type PermissionSet struct {
	values map[Permission]struct{}
}

// NewPermissionSet validates every permission up front so unknown values fail closed.
func NewPermissionSet(values ...Permission) (PermissionSet, error) {
	set := PermissionSet{values: make(map[Permission]struct{}, len(values))}
	for _, value := range values {
		if !value.Valid() {
			return PermissionSet{}, ErrPermissionDenied
		}
		set.values[value] = struct{}{}
	}
	return set, nil
}

// AllPermissions returns the stable ordered values in this set without exposing mutable backing storage.
func (set PermissionSet) AllPermissions() []Permission {
	result := make([]Permission, 0, len(set.values))
	for _, permission := range orderedPermissions {
		if _, ok := set.values[permission]; ok {
			result = append(result, permission)
		}
	}
	return result
}

// Has reports whether the set contains one known permission.
func (set PermissionSet) Has(permission Permission) bool {
	if !permission.Valid() {
		return false
	}
	_, ok := set.values[permission]
	return ok
}

// ActiveAdminPermissionSet grants the current singleton administrator every stable permission once the account is active.
func ActiveAdminPermissionSet() PermissionSet {
	values := make(map[Permission]struct{}, len(allPermissions))
	for permission := range allPermissions {
		values[permission] = struct{}{}
	}
	return PermissionSet{values: values}
}

// AdminAuthorizer is deliberately default-deny and grants capabilities only to active full sessions.
type AdminAuthorizer struct{}

// NewAdminAuthorizer constructs the default-deny session authorizer.
func NewAdminAuthorizer() AdminAuthorizer { return AdminAuthorizer{} }

// Authorize denies unknown permissions, non-full sessions, expired sessions, and revoked sessions.
func (AdminAuthorizer) Authorize(session Session, permission Permission, now interface{ Now() time.Time }) error {
	if _, known := allPermissions[permission]; !known || session.Snapshot().Kind != SessionKindFull || !session.Active(now.Now()) {
		return ErrPermissionDenied
	}
	return nil
}
