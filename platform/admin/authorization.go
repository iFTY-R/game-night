package admin

import "time"

// Permission is a stable capability label; authorization never infers access from an account boolean.
type Permission string

const (
	PermissionGetUser        Permission = "get_user"
	PermissionGetRealName    Permission = "get_real_name"
	PermissionUpdateRealName Permission = "update_real_name"
	PermissionExportProfile  Permission = "export_profile"
	PermissionManageRecovery Permission = "manage_recovery"
	PermissionForceUsername  Permission = "force_username"
	PermissionSuspendUser    Permission = "suspend_user"
	PermissionDeleteUser     Permission = "delete_user"
	PermissionRevokeDevice   Permission = "revoke_device"
	PermissionReadAudit      Permission = "read_audit"
)

var allPermissions = map[Permission]struct{}{
	PermissionGetUser: {}, PermissionGetRealName: {}, PermissionUpdateRealName: {}, PermissionExportProfile: {},
	PermissionManageRecovery: {}, PermissionForceUsername: {}, PermissionSuspendUser: {}, PermissionDeleteUser: {},
	PermissionRevokeDevice: {}, PermissionReadAudit: {},
}

// AdminAuthorizer is deliberately default-deny and grants capabilities only to full sessions.
type AdminAuthorizer struct{}

func NewAdminAuthorizer() AdminAuthorizer { return AdminAuthorizer{} }

func (AdminAuthorizer) Authorize(session Session, permission Permission, now interface{ Now() time.Time }) error {
	if _, known := allPermissions[permission]; !known || session.Snapshot().Kind != SessionKindFull || !session.Active(now.Now()) {
		return ErrPermissionDenied
	}
	return nil
}
