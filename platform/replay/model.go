// Package replay owns resource authorization independently from game-owned field projection.
package replay

import (
	"errors"
	"time"

	"github.com/google/uuid"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

var (
	ErrInvalidInput      = errors.New("invalid replay access input")
	ErrAccessDenied      = errors.New("replay access denied")
	ErrPolicyConflict    = errors.New("replay access policy conflict")
	ErrPolicyUnavailable = errors.New("replay access policy unavailable")
)

// Policy controls which authenticated users may request a game-owned replay projection.
type Policy string

const (
	PolicyParticipant Policy = "participant"
	PolicyRoomMember  Policy = "room_member"
	PolicyPublic      Policy = "public"
)

// Valid rejects policy values that have no reviewed resource-authorization semantics.
func (policy Policy) Valid() bool {
	return policy == PolicyParticipant || policy == PolicyRoomMember || policy == PolicyPublic
}

// ProjectionPolicy converts the authorized resource scope to the SDK scope enforced by each game module.
func (policy Policy) ProjectionPolicy() game.ReplayAccessPolicy {
	return game.ReplayAccessPolicy(policy)
}

// Access is the versioned host-controlled policy attached to one immutable game session.
type Access struct {
	SessionID                 uuid.UUID
	RoomID                    uuid.UUID
	Policy                    Policy
	Version                   uint64
	MemberSnapshotCompletedAt time.Time
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

// Valid verifies canonical timestamps and the complete persisted policy identity.
func (access Access) Valid() bool {
	if access.SessionID == uuid.Nil || access.RoomID == uuid.Nil || !access.Policy.Valid() || access.Version == 0 ||
		access.CreatedAt.IsZero() || access.UpdatedAt.Before(access.CreatedAt) {
		return false
	}
	canonical := func(value time.Time) bool {
		return value.IsZero() || value == value.Round(0).UTC()
	}
	return canonical(access.CreatedAt) && canonical(access.UpdatedAt) && canonical(access.MemberSnapshotCompletedAt) &&
		(access.MemberSnapshotCompletedAt.IsZero() || !access.MemberSnapshotCompletedAt.Before(access.CreatedAt))
}

// SetPolicyCommand requires current-host authority and a CAS version for one terminal session policy update.
type SetPolicyCommand struct {
	ActorUserID     uuid.UUID
	RoomID          uuid.UUID
	SessionID       uuid.UUID
	Policy          Policy
	ExpectedVersion uint64
	UpdatedAt       time.Time
}

// Valid rejects unversioned writes and process-local timestamps before persistence authorization.
func (command SetPolicyCommand) Valid() bool {
	return command.ActorUserID != uuid.Nil && command.RoomID != uuid.Nil && command.SessionID != uuid.Nil &&
		command.Policy.Valid() && command.ExpectedVersion > 0 && !command.UpdatedAt.IsZero() &&
		command.UpdatedAt == command.UpdatedAt.Round(0).UTC()
}
