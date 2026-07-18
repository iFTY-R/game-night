package identity

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/identifier"
)

func TestOnboardingUserExpiresAtTwentyFourHourBoundary(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	user, err := NewOnboardingUser(uuid.New(), now)
	if err != nil {
		t.Fatal(err)
	}
	username, err := identifier.ParseUsername("玩家_Alice9")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := user.CompleteOnboarding(username, now.Add(OnboardingTTL-time.Microsecond)); err != nil {
		t.Fatalf("complete just before expiry: %v", err)
	}
	if _, err := user.CompleteOnboarding(username, now.Add(OnboardingTTL)); !errors.Is(err, ErrOnboardingExpired) {
		t.Fatalf("completion at expiry error = %v", err)
	}
}

func TestUserOnboardingAndUsernameChangePlan(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	user, err := NewOnboardingUser(uuid.New(), now)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := identifier.ParseUsername("Alice9")
	active, err := user.CompleteOnboarding(first, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	activeSnapshot := active.Snapshot()
	if activeSnapshot.Status != UserStatusActive || activeSnapshot.Username != first.Display() ||
		activeSnapshot.CurrentUsernameKey != first.Key() {
		t.Fatalf("unexpected active snapshot: %+v", activeSnapshot)
	}

	second, _ := identifier.ParseUsername("Bob9")
	if _, err := active.PlanUsernameChange(second, activeSnapshot.UsernameChangedAt.Add(UsernameChangeCooldown-time.Microsecond)); !errors.Is(err, ErrUsernameChangeCooldown) {
		t.Fatalf("early rename error = %v", err)
	}
	plan, err := active.PlanUsernameChange(second, activeSnapshot.UsernameChangedAt.Add(UsernameChangeCooldown))
	if err != nil {
		t.Fatal(err)
	}
	if plan.PreviousUsernameKey != first.Key() || plan.Next.Snapshot().Username != second.Display() ||
		!plan.ReservePreviousUntil.Equal(plan.ChangedAt.Add(UsernameReservationTTL)) {
		t.Fatalf("unexpected username change plan: %+v", plan)
	}
	if _, err := active.PlanUsernameChange(first, activeSnapshot.UsernameChangedAt.Add(UsernameChangeCooldown)); !errors.Is(err, ErrUsernameUnchanged) {
		t.Fatalf("same-key rename error = %v", err)
	}
}

func TestUserStatusMatrixRejectsInvalidUsernameMutations(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	username, _ := identifier.ParseUsername("Alice9")
	onboarding, _ := NewOnboardingUser(uuid.New(), now)
	active, _ := onboarding.CompleteOnboarding(username, now.Add(time.Hour))

	for _, status := range []UserStatus{UserStatusSuspended, UserStatusDeleted} {
		snapshot := active.Snapshot()
		snapshot.Status = status
		snapshot.UpdatedAt = now.Add(2 * time.Hour)
		if status == UserStatusDeleted {
			snapshot.Username = ""
			snapshot.CurrentUsernameKey = ""
		}
		user, err := RestoreUser(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := user.PlanUsernameChange(username, now.Add(UsernameChangeCooldown+time.Hour)); !errors.Is(err, ErrUserStatus) {
			t.Fatalf("status %s rename error = %v", status, err)
		}
	}

	invalid := active.Snapshot()
	invalid.Status = UserStatusActive
	invalid.Username = ""
	invalid.CurrentUsernameKey = ""
	if _, err := RestoreUser(invalid); !errors.Is(err, ErrInvalidUserInput) {
		t.Fatalf("invalid persisted user error = %v", err)
	}
}

func TestUsernameClaimReservationUsesHalfOpenNinetyDayWindow(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	username, _ := identifier.ParseUsername("Alice9")
	claim, err := NewActiveUsernameClaim(username, uuid.New(), now)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := claim.Reserve(now.Add(UsernameReservationTTL), now)
	if err != nil {
		t.Fatal(err)
	}
	if reserved.AvailableAt(now.Add(UsernameReservationTTL - time.Microsecond)) {
		t.Fatal("reserved username became available before its boundary")
	}
	if !reserved.AvailableAt(now.Add(UsernameReservationTTL)) {
		t.Fatal("reserved username remained unavailable at its boundary")
	}
}

func TestUsernameChangeRejectsClockRollbackBehindLatestUserUpdate(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	first, _ := identifier.ParseUsername("Alice9")
	second, _ := identifier.ParseUsername("Bob9")
	user, _ := NewOnboardingUser(uuid.New(), now)
	active, _ := user.CompleteOnboarding(first, now.Add(time.Hour))
	snapshot := active.Snapshot()
	snapshot.UpdatedAt = now.Add(60 * 24 * time.Hour)
	updated, err := RestoreUser(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := updated.PlanUsernameChange(second, now.Add(31*24*time.Hour)); !errors.Is(err, ErrIdentityConcurrentTransition) {
		t.Fatalf("clock rollback rename error = %v", err)
	}
}

func TestOnboardingCompletionRejectsClockRollbackBehindLatestUserUpdate(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	username, _ := identifier.ParseUsername("Alice9")
	user, _ := NewOnboardingUser(uuid.New(), now)
	snapshot := user.Snapshot()
	snapshot.UpdatedAt = now.Add(time.Hour)
	updated, err := RestoreUser(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := updated.CompleteOnboarding(username, now.Add(30*time.Minute)); !errors.Is(err, ErrIdentityConcurrentTransition) {
		t.Fatalf("clock rollback onboarding error = %v", err)
	}
}
