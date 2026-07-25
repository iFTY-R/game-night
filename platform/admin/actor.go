package admin

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// ActorContext is the server-constructed authenticated admin request context used by command handlers.
type ActorContext struct {
	adminID           uuid.UUID
	sessionID         uuid.UUID
	session           Session
	permissions       PermissionSet
	elevations        ElevationSet
	enrollmentVersion int64
	requestID         string
	origin            string
	clientIP          string
	userAgent         string
}

// NewActorContext validates and freezes the request-scoped authorization context on the server side only.
func NewActorContext(adminID, sessionID uuid.UUID, session Session, permissions PermissionSet, elevations ElevationSet, enrollmentVersion int64, requestID, origin, clientIP, userAgent string) (ActorContext, error) {
	snapshot := session.Snapshot()
	if adminID == uuid.Nil || sessionID == uuid.Nil || snapshot.AdminID != adminID || snapshot.ID != sessionID || enrollmentVersion < 0 {
		return ActorContext{}, ErrAuthentication
	}
	return ActorContext{
		adminID:           adminID,
		sessionID:         sessionID,
		session:           session,
		permissions:       PermissionSet{values: clonePermissionMap(permissions.values)},
		elevations:        ElevationSet{values: cloneElevationMap(elevations.values)},
		enrollmentVersion: enrollmentVersion,
		requestID:         strings.TrimSpace(requestID),
		origin:            strings.TrimSpace(origin),
		clientIP:          strings.TrimSpace(clientIP),
		userAgent:         strings.TrimSpace(userAgent),
	}, nil
}

// AdminID returns the authenticated administrator identifier.
func (actor ActorContext) AdminID() uuid.UUID { return actor.adminID }

// SessionID returns the authenticated session identifier.
func (actor ActorContext) SessionID() uuid.UUID { return actor.sessionID }

// Session returns the immutable authenticated session snapshot for downstream policy checks.
func (actor ActorContext) Session() Session { return actor.session }

// EnrollmentVersion returns the current MFA enrollment version bound into elevation checks.
func (actor ActorContext) EnrollmentVersion() int64 { return actor.enrollmentVersion }

// RequestID returns the normalized request identifier.
func (actor ActorContext) RequestID() string { return actor.requestID }

// Origin returns the normalized request origin.
func (actor ActorContext) Origin() string { return actor.origin }

// ClientIP returns the normalized client address summary.
func (actor ActorContext) ClientIP() string { return actor.clientIP }

// UserAgent returns the normalized user-agent summary.
func (actor ActorContext) UserAgent() string { return actor.userAgent }

// Permissions returns a stable copy of the granted permissions without exposing mutable backing storage.
func (actor ActorContext) Permissions() []Permission { return actor.permissions.AllPermissions() }

// Elevations returns a stable copy of the active elevations without exposing mutable backing storage.
func (actor ActorContext) Elevations() []Elevation { return actor.elevations.AllElevations() }

// Require enforces one stable permission against the immutable permission set and full-session semantics.
func (actor ActorContext) Require(permission Permission) error {
	if actor.session.Snapshot().Kind != SessionKindFull || !actor.permissions.Has(permission) {
		return ErrPermissionDenied
	}
	return nil
}

// RequireElevation enforces one stable step-up scope against the immutable elevation set and current security versions.
func (actor ActorContext) RequireElevation(scope ElevationScope, at time.Time) error {
	return actor.elevations.Require(scope, actor.session, actor.enrollmentVersion, at)
}

func clonePermissionMap(source map[Permission]struct{}) map[Permission]struct{} {
	if len(source) == 0 {
		return nil
	}
	clone := make(map[Permission]struct{}, len(source))
	for key := range source {
		clone[key] = struct{}{}
	}
	return clone
}

func cloneElevationMap(source map[ElevationScope]Elevation) map[ElevationScope]Elevation {
	if len(source) == 0 {
		return nil
	}
	clone := make(map[ElevationScope]Elevation, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
