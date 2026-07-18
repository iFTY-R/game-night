package identity

import (
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/identifier"
)

const (
	// OnboardingTTL is the maximum lifetime of an identity without a claimed public username.
	OnboardingTTL = 24 * time.Hour
	// UsernameChangeCooldown limits user-initiated public identity churn.
	UsernameChangeCooldown = 30 * 24 * time.Hour
	// UsernameReservationTTL protects a released historical name from immediate reassignment.
	UsernameReservationTTL = 90 * 24 * time.Hour
)

// UserStatus is the closed persisted lifecycle for a platform user.
type UserStatus string

const (
	UserStatusOnboarding UserStatus = "onboarding"
	UserStatusActive     UserStatus = "active"
	UserStatusSuspended  UserStatus = "suspended"
	UserStatusDeleted    UserStatus = "deleted"
)

// Valid reports whether status belongs to the reviewed user state machine.
func (status UserStatus) Valid() bool {
	switch status {
	case UserStatusOnboarding, UserStatusActive, UserStatusSuspended, UserStatusDeleted:
		return true
	default:
		return false
	}
}

// UserSnapshot is the persistence-neutral representation accepted by RestoreUser.
type UserSnapshot struct {
	ID                 uuid.UUID
	Status             UserStatus
	Username           string
	CurrentUsernameKey string
	UsernameChangedAt  time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// User is an immutable validated aggregate.
type User struct {
	snapshot UserSnapshot
}

// UsernameChangePlan binds the next user state to the old claim reservation written in the same transaction.
type UsernameChangePlan struct {
	Next                 User
	PreviousUsernameKey  string
	ChangedAt            time.Time
	ReservePreviousUntil time.Time
}

// NewOnboardingUser creates a username-less identity with a 24-hour completion window.
func NewOnboardingUser(id uuid.UUID, createdAt time.Time) (User, error) {
	createdAt = canonicalUserTime(createdAt)
	return RestoreUser(UserSnapshot{
		ID: id, Status: UserStatusOnboarding, CreatedAt: createdAt, UpdatedAt: createdAt,
	})
}

// RestoreUser validates persistence state before service authorization or mutation.
func RestoreUser(snapshot UserSnapshot) (User, error) {
	snapshot.CreatedAt = canonicalUserTime(snapshot.CreatedAt)
	snapshot.UpdatedAt = canonicalUserTime(snapshot.UpdatedAt)
	snapshot.UsernameChangedAt = canonicalUserOptionalTime(snapshot.UsernameChangedAt)
	if snapshot.ID == uuid.Nil || !snapshot.Status.Valid() || snapshot.CreatedAt.IsZero() ||
		snapshot.UpdatedAt.Before(snapshot.CreatedAt) {
		return User{}, ErrInvalidUserInput
	}
	switch snapshot.Status {
	case UserStatusOnboarding:
		if snapshot.Username != "" || snapshot.CurrentUsernameKey != "" || !snapshot.UsernameChangedAt.IsZero() {
			return User{}, ErrInvalidUserInput
		}
	case UserStatusActive, UserStatusSuspended:
		if err := validatePersistedUsername(snapshot); err != nil {
			return User{}, err
		}
	case UserStatusDeleted:
		if snapshot.Username != "" || snapshot.CurrentUsernameKey != "" {
			return User{}, ErrInvalidUserInput
		}
		if !snapshot.UsernameChangedAt.IsZero() &&
			(snapshot.UsernameChangedAt.Before(snapshot.CreatedAt) || snapshot.UsernameChangedAt.After(snapshot.UpdatedAt)) {
			return User{}, ErrInvalidUserInput
		}
	}
	return User{snapshot: snapshot}, nil
}

// Snapshot returns the immutable aggregate state in persistence form.
func (user User) Snapshot() UserSnapshot {
	return user.snapshot
}

// OnboardingExpired applies a half-open 24-hour completion interval.
func (user User) OnboardingExpired(at time.Time) bool {
	at = canonicalUserTime(at)
	return user.snapshot.Status != UserStatusOnboarding || !at.Before(user.snapshot.CreatedAt.Add(OnboardingTTL))
}

// CompleteOnboarding activates a pending identity after a valid username claim has been acquired.
func (user User) CompleteOnboarding(username identifier.Username, at time.Time) (User, error) {
	at = canonicalUserTime(at)
	if user.snapshot.Status != UserStatusOnboarding {
		return User{}, ErrUserStatus
	}
	if user.OnboardingExpired(at) {
		return User{}, ErrOnboardingExpired
	}
	if username.Display() == "" || username.Key() == "" || at.Before(user.snapshot.CreatedAt) {
		return User{}, ErrInvalidUserInput
	}
	if at.Before(user.snapshot.UpdatedAt) {
		return User{}, ErrIdentityConcurrentTransition
	}
	next := user.snapshot
	next.Status = UserStatusActive
	next.Username = username.Display()
	next.CurrentUsernameKey = username.Key()
	next.UsernameChangedAt = at
	next.UpdatedAt = at
	return RestoreUser(next)
}

// PlanUsernameChange enforces active status and cooldown while producing both sides of the claim transaction.
func (user User) PlanUsernameChange(username identifier.Username, at time.Time) (UsernameChangePlan, error) {
	at = canonicalUserTime(at)
	if user.snapshot.Status != UserStatusActive {
		return UsernameChangePlan{}, ErrUserStatus
	}
	if username.Display() == "" || username.Key() == "" {
		return UsernameChangePlan{}, ErrInvalidUserInput
	}
	if username.Key() == user.snapshot.CurrentUsernameKey {
		return UsernameChangePlan{}, ErrUsernameUnchanged
	}
	if at.Before(user.snapshot.UsernameChangedAt.Add(UsernameChangeCooldown)) {
		return UsernameChangePlan{}, ErrUsernameChangeCooldown
	}
	if at.Before(user.snapshot.UpdatedAt) {
		return UsernameChangePlan{}, ErrIdentityConcurrentTransition
	}
	next := user.snapshot
	next.Username = username.Display()
	next.CurrentUsernameKey = username.Key()
	next.UsernameChangedAt = at
	next.UpdatedAt = at
	nextUser, err := RestoreUser(next)
	if err != nil {
		return UsernameChangePlan{}, err
	}
	return UsernameChangePlan{
		Next: nextUser, PreviousUsernameKey: user.snapshot.CurrentUsernameKey, ChangedAt: at,
		ReservePreviousUntil: at.Add(UsernameReservationTTL),
	}, nil
}

// UsernameClaimStatus is the closed state of the global username registry.
type UsernameClaimStatus string

const (
	UsernameClaimActive   UsernameClaimStatus = "active"
	UsernameClaimReserved UsernameClaimStatus = "reserved"
)

// UsernameClaimSnapshot is the persistence-neutral username registry row.
type UsernameClaimSnapshot struct {
	UsernameKey     string
	DisplayUsername string
	Status          UsernameClaimStatus
	OwnerUserID     uuid.UUID
	ReservedUntil   time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// UsernameClaim is an immutable current or historical ownership record.
type UsernameClaim struct {
	snapshot UsernameClaimSnapshot
}

// NewActiveUsernameClaim creates the registry value inserted before a matching user CAS.
func NewActiveUsernameClaim(username identifier.Username, ownerUserID uuid.UUID, claimedAt time.Time) (UsernameClaim, error) {
	return RestoreUsernameClaim(UsernameClaimSnapshot{
		UsernameKey: username.Key(), DisplayUsername: username.Display(), Status: UsernameClaimActive,
		OwnerUserID: ownerUserID, CreatedAt: claimedAt, UpdatedAt: claimedAt,
	})
}

// RestoreUsernameClaim validates active/reserved ownership and canonical username key equivalence.
func RestoreUsernameClaim(snapshot UsernameClaimSnapshot) (UsernameClaim, error) {
	snapshot.CreatedAt = canonicalUserTime(snapshot.CreatedAt)
	snapshot.UpdatedAt = canonicalUserTime(snapshot.UpdatedAt)
	snapshot.ReservedUntil = canonicalUserOptionalTime(snapshot.ReservedUntil)
	username, err := identifier.ParseUsername(snapshot.DisplayUsername)
	if err != nil || username.Key() != snapshot.UsernameKey || snapshot.OwnerUserID == uuid.Nil ||
		snapshot.CreatedAt.IsZero() || snapshot.UpdatedAt.Before(snapshot.CreatedAt) {
		return UsernameClaim{}, ErrInvalidUserInput
	}
	switch snapshot.Status {
	case UsernameClaimActive:
		if !snapshot.ReservedUntil.IsZero() {
			return UsernameClaim{}, ErrInvalidUserInput
		}
	case UsernameClaimReserved:
		if !snapshot.ReservedUntil.After(snapshot.UpdatedAt) {
			return UsernameClaim{}, ErrInvalidUserInput
		}
	default:
		return UsernameClaim{}, ErrInvalidUserInput
	}
	return UsernameClaim{snapshot: snapshot}, nil
}

// Snapshot returns a copy of the claim state.
func (claim UsernameClaim) Snapshot() UsernameClaimSnapshot {
	return claim.snapshot
}

// Reserve transitions active ownership into a protected historical claim.
func (claim UsernameClaim) Reserve(until, changedAt time.Time) (UsernameClaim, error) {
	until = canonicalUserTime(until)
	changedAt = canonicalUserTime(changedAt)
	if claim.snapshot.Status != UsernameClaimActive || !until.After(changedAt) || changedAt.Before(claim.snapshot.UpdatedAt) {
		return UsernameClaim{}, ErrIdentityConcurrentTransition
	}
	next := claim.snapshot
	next.Status = UsernameClaimReserved
	next.ReservedUntil = until
	next.UpdatedAt = changedAt
	return RestoreUsernameClaim(next)
}

// AvailableAt reports whether a reserved claim may be atomically acquired at the half-open boundary.
func (claim UsernameClaim) AvailableAt(at time.Time) bool {
	at = canonicalUserTime(at)
	return claim.snapshot.Status == UsernameClaimReserved && !at.Before(claim.snapshot.ReservedUntil)
}

func validatePersistedUsername(snapshot UserSnapshot) error {
	username, err := identifier.ParseUsername(snapshot.Username)
	if err != nil || username.Key() != snapshot.CurrentUsernameKey || snapshot.UsernameChangedAt.IsZero() ||
		snapshot.UsernameChangedAt.Before(snapshot.CreatedAt) || snapshot.UsernameChangedAt.After(snapshot.UpdatedAt) {
		return ErrInvalidUserInput
	}
	return nil
}

func canonicalUserTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC().Truncate(time.Microsecond)
}

func canonicalUserOptionalTime(value time.Time) time.Time {
	return canonicalUserTime(value)
}
