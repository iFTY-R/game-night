package admin

import (
	"time"

	"github.com/google/uuid"
)

const (
	// AdminElevationMaxTTL is the hard upper bound for any short-lived admin step-up grant.
	AdminElevationMaxTTL = 5 * time.Minute
	// AdminElevationClockSkewTolerance admits only a bounded backward wall-clock adjustment between grant and immediate use.
	AdminElevationClockSkewTolerance = time.Second
)

// ElevationScope is the stable high-risk command family bound into a short-lived step-up grant.
type ElevationScope string

const (
	// ElevationScopeUsersBulkGovernance authorizes starting, cancelling, or retrying bulk governance jobs.
	ElevationScopeUsersBulkGovernance ElevationScope = "users.bulk_governance"
	// ElevationScopeUsersRevokeDevices authorizes forced sign-out across one user's devices.
	ElevationScopeUsersRevokeDevices ElevationScope = "users.revoke_devices"
	// ElevationScopeUsersDelete authorizes controlled account deletion flows.
	ElevationScopeUsersDelete ElevationScope = "users.delete"
	// ElevationScopeRoomsForceClose authorizes force-closing waiting rooms.
	ElevationScopeRoomsForceClose ElevationScope = "rooms.force_close"
	// ElevationScopeGamesForceTerminate authorizes terminating a live game.
	ElevationScopeGamesForceTerminate ElevationScope = "games.force_terminate"
	// ElevationScopeGamesEmergencyRepair authorizes fixed emergency repair commands.
	ElevationScopeGamesEmergencyRepair ElevationScope = "games.emergency_repair"
	// ElevationScopeOperationsMaintenance authorizes controlled maintenance commands.
	ElevationScopeOperationsMaintenance ElevationScope = "operations.maintenance"
	// ElevationScopeSecurityDisableMFA authorizes disabling the current active MFA enrollment.
	ElevationScopeSecurityDisableMFA ElevationScope = "security.disable_mfa"
	// ElevationScopeSecurityRegenerateRecoveryCodes authorizes rotating the whole recovery-code set.
	ElevationScopeSecurityRegenerateRecoveryCodes ElevationScope = "security.regenerate_recovery_codes"
	// ElevationScopeSecurityRevokeSessions authorizes revoking every other admin session at once.
	ElevationScopeSecurityRevokeSessions ElevationScope = "security.revoke_sessions"
	// ElevationScopeAuditExportSensitive authorizes sensitive audit export creation and download grants.
	ElevationScopeAuditExportSensitive ElevationScope = "audit.export_sensitive"
)

var orderedElevationScopes = []ElevationScope{
	ElevationScopeUsersBulkGovernance,
	ElevationScopeUsersRevokeDevices,
	ElevationScopeUsersDelete,
	ElevationScopeRoomsForceClose,
	ElevationScopeGamesForceTerminate,
	ElevationScopeGamesEmergencyRepair,
	ElevationScopeOperationsMaintenance,
	ElevationScopeSecurityDisableMFA,
	ElevationScopeSecurityRegenerateRecoveryCodes,
	ElevationScopeSecurityRevokeSessions,
	ElevationScopeAuditExportSensitive,
}

var allElevationScopes = map[ElevationScope]struct{}{
	ElevationScopeUsersBulkGovernance:             {},
	ElevationScopeUsersRevokeDevices:              {},
	ElevationScopeUsersDelete:                     {},
	ElevationScopeRoomsForceClose:                 {},
	ElevationScopeGamesForceTerminate:             {},
	ElevationScopeGamesEmergencyRepair:            {},
	ElevationScopeOperationsMaintenance:           {},
	ElevationScopeSecurityDisableMFA:              {},
	ElevationScopeSecurityRegenerateRecoveryCodes: {},
	ElevationScopeSecurityRevokeSessions:          {},
	ElevationScopeAuditExportSensitive:            {},
}

// Valid reports whether the scope belongs to the rebuilt admin console contract.
func (scope ElevationScope) Valid() bool {
	_, ok := allElevationScopes[scope]
	return ok
}

// AllowsRecoveryCodeSubstitution reports whether one unused recovery code may substitute for TOTP when issuing this scope.
func (scope ElevationScope) AllowsRecoveryCodeSubstitution() bool {
	switch scope {
	case ElevationScopeSecurityDisableMFA, ElevationScopeSecurityRegenerateRecoveryCodes:
		return true
	default:
		return false
	}
}

// ElevationSnapshot is the persistence-neutral state of one short-lived step-up grant.
type ElevationSnapshot struct {
	AdminID           uuid.UUID
	SessionID         uuid.UUID
	Scope             ElevationScope
	AdminVersion      int64
	PasswordVersion   int64
	SessionVersion    int64
	EnrollmentVersion int64
	GrantedAt         time.Time
	ExpiresAt         time.Time
	RevokedAt         time.Time
}

// Elevation is immutable and binds one scope to one session and one exact security version tuple.
type Elevation struct{ snapshot ElevationSnapshot }

// NewElevation issues one short-lived step-up grant for the supplied full session and current enrollment version.
func NewElevation(session Session, enrollmentVersion int64, scope ElevationScope, grantedAt, expiresAt time.Time) (Elevation, error) {
	snapshot := session.Snapshot()
	if snapshot.Kind != SessionKindFull || !session.Active(grantedAt) {
		return Elevation{}, ErrElevationDenied
	}
	return RestoreElevation(ElevationSnapshot{
		AdminID:           snapshot.AdminID,
		SessionID:         snapshot.ID,
		Scope:             scope,
		AdminVersion:      snapshot.AdminVersion,
		PasswordVersion:   snapshot.PasswordVersion,
		SessionVersion:    snapshot.SessionVersion,
		EnrollmentVersion: enrollmentVersion,
		GrantedAt:         grantedAt,
		ExpiresAt:         expiresAt,
	})
}

// RestoreElevation validates one persisted or freshly created step-up grant.
func RestoreElevation(snapshot ElevationSnapshot) (Elevation, error) {
	snapshot.GrantedAt = canonicalAdminTime(snapshot.GrantedAt)
	snapshot.ExpiresAt = canonicalAdminTime(snapshot.ExpiresAt)
	snapshot.RevokedAt = canonicalAdminTime(snapshot.RevokedAt)
	if snapshot.AdminID == uuid.Nil || snapshot.SessionID == uuid.Nil || !snapshot.Scope.Valid() ||
		snapshot.AdminVersion <= 0 || snapshot.PasswordVersion < 0 || snapshot.SessionVersion <= 0 ||
		snapshot.EnrollmentVersion < 0 || snapshot.GrantedAt.IsZero() || !snapshot.ExpiresAt.After(snapshot.GrantedAt) ||
		snapshot.ExpiresAt.Sub(snapshot.GrantedAt) > AdminElevationMaxTTL {
		return Elevation{}, ErrInvalidInput
	}
	if !snapshot.RevokedAt.IsZero() && snapshot.RevokedAt.Before(snapshot.GrantedAt) {
		return Elevation{}, ErrIntegrity
	}
	return Elevation{snapshot: snapshot}, nil
}

// Snapshot returns the persistence-ready immutable state.
func (elevation Elevation) Snapshot() ElevationSnapshot { return elevation.snapshot }

// Validate re-checks the scope, TTL, revocation state, session identity, and bound versions before one high-risk command runs.
func (elevation Elevation) Validate(session Session, enrollmentVersion int64, requiredScope ElevationScope, at time.Time) error {
	snapshot := elevation.snapshot
	if !requiredScope.Valid() || snapshot.Scope != requiredScope {
		return ErrElevationDenied
	}
	if !snapshot.RevokedAt.IsZero() {
		return ErrElevationDenied
	}
	now := canonicalAdminTime(at)
	if now.IsZero() || now.Add(AdminElevationClockSkewTolerance).Before(snapshot.GrantedAt) {
		return ErrElevationDenied
	}
	if !now.Before(snapshot.ExpiresAt) {
		return ErrElevationExpired
	}
	sessionSnapshot := session.Snapshot()
	if sessionSnapshot.Kind != SessionKindFull || !session.Active(now) {
		return ErrElevationDenied
	}
	if sessionSnapshot.AdminID != snapshot.AdminID || sessionSnapshot.ID != snapshot.SessionID ||
		sessionSnapshot.AdminVersion != snapshot.AdminVersion || sessionSnapshot.PasswordVersion != snapshot.PasswordVersion ||
		sessionSnapshot.SessionVersion != snapshot.SessionVersion || enrollmentVersion != snapshot.EnrollmentVersion {
		return ErrElevationDenied
	}
	return nil
}

// Revoke closes the grant before its natural TTL so later commands fail closed.
func (elevation Elevation) Revoke(at time.Time) (Elevation, error) {
	if !elevation.snapshot.RevokedAt.IsZero() {
		return Elevation{}, ErrConcurrentTransition
	}
	snapshot := elevation.Snapshot()
	snapshot.RevokedAt = canonicalAdminTime(at)
	return RestoreElevation(snapshot)
}

// ElevationSet is an immutable default-deny set of validated elevation grants keyed by scope.
type ElevationSet struct {
	values map[ElevationScope]Elevation
}

// NewElevationSet validates every grant up front and rejects duplicate scopes so callers cannot shadow one grant with another.
func NewElevationSet(values ...Elevation) (ElevationSet, error) {
	set := ElevationSet{values: make(map[ElevationScope]Elevation, len(values))}
	for _, value := range values {
		scope := value.Snapshot().Scope
		if !scope.Valid() {
			return ElevationSet{}, ErrElevationDenied
		}
		if _, exists := set.values[scope]; exists {
			return ElevationSet{}, ErrInvalidInput
		}
		set.values[scope] = value
	}
	return set, nil
}

// AllElevations returns the grants in stable scope order without exposing mutable backing storage.
func (set ElevationSet) AllElevations() []Elevation {
	result := make([]Elevation, 0, len(set.values))
	for _, scope := range orderedElevationScopes {
		if value, ok := set.values[scope]; ok {
			result = append(result, value)
		}
	}
	return result
}

// Require re-validates one scope against the current session and enrollment version.
func (set ElevationSet) Require(scope ElevationScope, session Session, enrollmentVersion int64, at time.Time) error {
	if !scope.Valid() {
		return ErrElevationDenied
	}
	value, ok := set.values[scope]
	if !ok {
		return ErrElevationDenied
	}
	return value.Validate(session, enrollmentVersion, scope, at)
}
