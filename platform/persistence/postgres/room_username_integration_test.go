package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/internal/integrationtest"
	"github.com/iFTY-R/game-night/platform/identifier"
	identityDomain "github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
)

// roomUsernameIntegrationTimeout bounds migration setup plus the room/identity transaction checks.
const roomUsernameIntegrationTimeout = 90 * time.Second

func TestPostgresRoomUsernamesAllowDuplicateGlobalClaims(t *testing.T) {
	fixture, ctx, now := openRoomUsernameFixture(t)

	firstUserID, secondUserID := uuid.New(), uuid.New()
	createRoomTestUser(t, ctx, fixture, firstUserID, "Same", now)
	createRoomTestUser(t, ctx, fixture, secondUserID, "Same", now)

	if got := countUsernameClaimsForKey(t, ctx, fixture, "same"); got != 2 {
		t.Fatalf("shared username claims = %d, want 2", got)
	}
	repository := &identityUserRepository{queries: sqlcgen.New(fixture.Pool)}
	if _, err := repository.GetByUsernameKey(ctx, "same"); !errors.Is(err, identityDomain.ErrUsernameAmbiguous) {
		t.Fatalf("shared username lookup error = %v, want %v", err, identityDomain.ErrUsernameAmbiguous)
	}
}

func TestPostgresRoomUsernamesAllowSameNameAcrossDifferentRooms(t *testing.T) {
	fixture, ctx, now := openRoomUsernameFixture(t)

	hostOneID, hostTwoID := uuid.New(), uuid.New()
	firstSharedID, secondSharedID := uuid.New(), uuid.New()
	createRoomTestUser(t, ctx, fixture, hostOneID, "Host", now)
	createRoomTestUser(t, ctx, fixture, hostTwoID, "Boss", now)
	createRoomTestUser(t, ctx, fixture, firstSharedID, "Same", now)
	createRoomTestUser(t, ctx, fixture, secondSharedID, "Same", now)

	repository := NewRoomRepository(fixture.Pool)
	firstRoom := mustCreateRoom(t, ctx, repository, hostOneID, "ROOMA1", now.Add(time.Second))
	firstRoom = mustJoinRoom(t, ctx, repository, firstRoom, firstSharedID, now.Add(2*time.Second))
	secondRoom := mustCreateRoom(t, ctx, repository, hostTwoID, "ROOMB2", now.Add(3*time.Second))
	secondRoom = mustJoinRoom(t, ctx, repository, secondRoom, secondSharedID, now.Add(4*time.Second))

	assertRoomMemberAliases(t, ctx, fixture, firstSharedID, map[uuid.UUID]roomMemberAlias{
		firstRoom.Snapshot().ID: {displayUsername: "Same", usernameKey: "same"},
	})
	assertRoomMemberAliases(t, ctx, fixture, secondSharedID, map[uuid.UUID]roomMemberAlias{
		secondRoom.Snapshot().ID: {displayUsername: "Same", usernameKey: "same"},
	})
}

func TestPostgresRoomUsernamesRejectNormalizedSameNameInsideOneRoom(t *testing.T) {
	fixture, ctx, now := openRoomUsernameFixture(t)

	hostID, firstSharedID, secondSharedID := uuid.New(), uuid.New(), uuid.New()
	createRoomTestUser(t, ctx, fixture, hostID, "Host", now)
	createNormalizedRoomTestUser(t, ctx, fixture, firstSharedID, " \uff21b ", now)
	createNormalizedRoomTestUser(t, ctx, fixture, secondSharedID, "ab", now)

	repository := NewRoomRepository(fixture.Pool)
	stored := mustCreateRoom(t, ctx, repository, hostID, "ROOMC3", now.Add(time.Second))
	stored = mustJoinRoom(t, ctx, repository, stored, firstSharedID, now.Add(2*time.Second))

	conflicting, _, err := stored.Join(secondSharedID, roomDomain.JoinIntentParticipant, stored.Version(), now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpdateCAS(ctx, stored, conflicting); !errors.Is(err, roomDomain.ErrUsernameConflict) {
		t.Fatalf("same-room duplicate username error = %v, want %v", err, roomDomain.ErrUsernameConflict)
	}

	assertRoomMemberAliases(t, ctx, fixture, firstSharedID, map[uuid.UUID]roomMemberAlias{
		stored.Snapshot().ID: {displayUsername: "Ab", usernameKey: "ab"},
	})
	assertRoomMemberAliases(t, ctx, fixture, secondSharedID, map[uuid.UUID]roomMemberAlias{})
}

func TestPostgresRoomUsernameRenameConflictRollsBackUserAndAllRoomAliases(t *testing.T) {
	fixture, ctx, now := openRoomUsernameFixture(t)

	seededAt := now.Add(-5 * time.Minute)
	roomCreatedAt := seededAt.Add(time.Minute)
	renameAt := now
	hostOneID, hostTwoID := uuid.New(), uuid.New()
	targetUserID, blockerUserID := uuid.New(), uuid.New()
	createRoomTestUser(t, ctx, fixture, hostOneID, "Host", seededAt)
	createRoomTestUser(t, ctx, fixture, hostTwoID, "Boss", seededAt)
	createRoomTestUser(t, ctx, fixture, targetUserID, "Free", seededAt)
	createRoomTestUser(t, ctx, fixture, blockerUserID, "Used", seededAt)

	repository := NewRoomRepository(fixture.Pool)
	conflictRoom := mustCreateRoom(t, ctx, repository, hostOneID, "ROOMD4", roomCreatedAt)
	conflictRoom = mustJoinRoom(t, ctx, repository, conflictRoom, targetUserID, roomCreatedAt.Add(time.Minute))
	conflictRoom = mustJoinRoom(t, ctx, repository, conflictRoom, blockerUserID, roomCreatedAt.Add(2*time.Minute))
	secondaryRoom := mustCreateRoom(t, ctx, repository, hostTwoID, "ROOME5", roomCreatedAt.Add(3*time.Minute))
	secondaryRoom = mustJoinRoom(t, ctx, repository, secondaryRoom, targetUserID, roomCreatedAt.Add(4*time.Minute))

	err := renameIdentityUsername(ctx, NewIdentityUnitOfWork(fixture.Pool), targetUserID, "Used", renameAt)
	if !errors.Is(err, identityDomain.ErrUsernameRoomConflict) {
		t.Fatalf("conflicting rename error = %v, want %v", err, identityDomain.ErrUsernameRoomConflict)
	}

	assertStoredUserUsername(t, ctx, fixture, targetUserID, "Free", "free")
	assertRoomMemberAliases(t, ctx, fixture, targetUserID, map[uuid.UUID]roomMemberAlias{
		conflictRoom.Snapshot().ID:  {displayUsername: "Free", usernameKey: "free"},
		secondaryRoom.Snapshot().ID: {displayUsername: "Free", usernameKey: "free"},
	})
	if got := countUserOwnedClaims(t, ctx, fixture, targetUserID, "used"); got != 0 {
		t.Fatalf("rolled-back new claim count = %d, want 0", got)
	}
}

func TestPostgresRoomUsernameRenameSyncsAllRoomAliases(t *testing.T) {
	fixture, ctx, now := openRoomUsernameFixture(t)

	seededAt := now.Add(-5 * time.Minute)
	roomCreatedAt := seededAt.Add(time.Minute)
	renameAt := now
	hostOneID, hostTwoID := uuid.New(), uuid.New()
	targetUserID := uuid.New()
	createRoomTestUser(t, ctx, fixture, hostOneID, "Host", seededAt)
	createRoomTestUser(t, ctx, fixture, hostTwoID, "Boss", seededAt)
	createRoomTestUser(t, ctx, fixture, targetUserID, "Free", seededAt)

	repository := NewRoomRepository(fixture.Pool)
	firstRoom := mustCreateRoom(t, ctx, repository, hostOneID, "ROOMF6", roomCreatedAt)
	firstRoom = mustJoinRoom(t, ctx, repository, firstRoom, targetUserID, roomCreatedAt.Add(time.Minute))
	secondRoom := mustCreateRoom(t, ctx, repository, hostTwoID, "ROOMG7", roomCreatedAt.Add(2*time.Minute))
	secondRoom = mustJoinRoom(t, ctx, repository, secondRoom, targetUserID, roomCreatedAt.Add(3*time.Minute))

	if err := renameIdentityUsername(ctx, NewIdentityUnitOfWork(fixture.Pool), targetUserID, "Next", renameAt); err != nil {
		t.Fatal(err)
	}

	assertStoredUserUsername(t, ctx, fixture, targetUserID, "Next", "next")
	assertRoomMemberAliases(t, ctx, fixture, targetUserID, map[uuid.UUID]roomMemberAlias{
		firstRoom.Snapshot().ID:  {displayUsername: "Next", usernameKey: "next"},
		secondRoom.Snapshot().ID: {displayUsername: "Next", usernameKey: "next"},
	})
}

func TestPostgresRoomUsernameRenameIgnoresClosedRoomConflicts(t *testing.T) {
	fixture, ctx, now := openRoomUsernameFixture(t)

	seededAt := now.Add(-5 * time.Minute)
	roomCreatedAt := seededAt.Add(time.Minute)
	hostOneID, hostTwoID := uuid.New(), uuid.New()
	targetUserID, blockerUserID := uuid.New(), uuid.New()
	createRoomTestUser(t, ctx, fixture, hostOneID, "Host", seededAt)
	createRoomTestUser(t, ctx, fixture, hostTwoID, "Boss", seededAt)
	createRoomTestUser(t, ctx, fixture, targetUserID, "Free", seededAt)
	createRoomTestUser(t, ctx, fixture, blockerUserID, "Used", seededAt)

	repository := NewRoomRepository(fixture.Pool)
	closedRoom := mustCreateRoom(t, ctx, repository, hostOneID, "ROOMH8", roomCreatedAt)
	closedRoom = mustJoinRoom(t, ctx, repository, closedRoom, targetUserID, roomCreatedAt.Add(time.Minute))
	closedRoom = mustJoinRoom(t, ctx, repository, closedRoom, blockerUserID, roomCreatedAt.Add(2*time.Minute))
	activeRoom := mustCreateRoom(t, ctx, repository, hostTwoID, "ROOMI9", roomCreatedAt.Add(3*time.Minute))
	activeRoom = mustJoinRoom(t, ctx, repository, activeRoom, targetUserID, roomCreatedAt.Add(4*time.Minute))

	closedNext, err := closedRoom.Close(hostOneID, closedRoom.Version(), roomCreatedAt.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	closedRoom, err = repository.UpdateCAS(ctx, closedRoom, closedNext)
	if err != nil {
		t.Fatal(err)
	}
	if err := renameIdentityUsername(ctx, NewIdentityUnitOfWork(fixture.Pool), targetUserID, "Used", now); err != nil {
		t.Fatal(err)
	}

	assertStoredUserUsername(t, ctx, fixture, targetUserID, "Used", "used")
	assertRoomMemberAliases(t, ctx, fixture, targetUserID, map[uuid.UUID]roomMemberAlias{
		closedRoom.Snapshot().ID: {displayUsername: "Free", usernameKey: "free"},
		activeRoom.Snapshot().ID: {displayUsername: "Used", usernameKey: "used"},
	})
}

// openRoomUsernameFixture applies the shared PostgreSQL migrations and inherits the standard skip behavior.
func openRoomUsernameFixture(t *testing.T) (*integrationtest.PostgresSchema, context.Context, time.Time) {
	t.Helper()
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), roomUsernameIntegrationTimeout)
	t.Cleanup(cancel)
	applyTransactionTestMigrations(t, ctx, fixture)
	return fixture, ctx, databaseIntegrationTime(t, ctx, fixture)
}

// createNormalizedRoomTestUser routes fixture input through the same parser used by onboarding before persistence.
func createNormalizedRoomTestUser(
	t testing.TB,
	ctx context.Context,
	fixture *integrationtest.PostgresSchema,
	userID uuid.UUID,
	usernameValue string,
	at time.Time,
) {
	t.Helper()
	username, err := identifier.ParseUsername(usernameValue)
	if err != nil {
		t.Fatal(err)
	}
	createRoomTestUser(t, ctx, fixture, userID, username.Display(), at)
}

// mustCreateRoom persists one host-owned room so later membership assertions observe database triggers, not in-memory state.
func mustCreateRoom(
	t testing.TB,
	ctx context.Context,
	repository *RoomRepository,
	hostUserID uuid.UUID,
	roomCode string,
	createdAt time.Time,
) roomDomain.Room {
	t.Helper()
	room, err := roomDomain.New(uuid.New(), hostUserID, roomCode, roomDomain.VisibilityPrivate, 4, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := repository.Create(ctx, room)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

// mustJoinRoom commits one membership mutation through the repository CAS path so room-member username triggers run.
func mustJoinRoom(
	t testing.TB,
	ctx context.Context,
	repository *RoomRepository,
	current roomDomain.Room,
	userID uuid.UUID,
	joinedAt time.Time,
) roomDomain.Room {
	t.Helper()
	next, _, err := current.Join(userID, roomDomain.JoinIntentParticipant, current.Version(), joinedAt)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := repository.UpdateCAS(ctx, current, next)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

// renameIdentityUsername mirrors the reviewed identity rename order so PostgreSQL trigger failures surface under the real unit-of-work contract.
func renameIdentityUsername(
	ctx context.Context,
	unitOfWork *IdentityUnitOfWork,
	userID uuid.UUID,
	usernameValue string,
	changedAt time.Time,
) error {
	username, err := identifier.ParseUsername(usernameValue)
	if err != nil {
		return err
	}
	return unitOfWork.RunIdentity(ctx, func(ctx context.Context, transaction identityDomain.IdentityTransaction) error {
		user, err := transaction.Users().GetForUpdate(ctx, userID)
		if err != nil {
			return err
		}
		plan, err := user.PlanUsernameChange(username, changedAt)
		if err != nil {
			return err
		}
		claim, err := identityDomain.NewActiveUsernameClaim(username, userID, plan.ChangedAt)
		if err != nil {
			return err
		}
		if _, err = transaction.UsernameClaims().Claim(ctx, claim, plan.ChangedAt); err != nil {
			return err
		}
		previousClaim, err := transaction.UsernameClaims().GetForUpdate(ctx, userID, plan.PreviousUsernameKey)
		if err != nil {
			return err
		}
		if _, err = transaction.Users().ChangeUsernameCAS(ctx, user, plan.Next); err != nil {
			return err
		}
		reserved, err := previousClaim.Reserve(plan.ChangedAt.Add(identityDomain.UsernameReservationTTL), plan.ChangedAt)
		if err != nil {
			return err
		}
		_, err = transaction.UsernameClaims().ReserveCAS(ctx, previousClaim, reserved)
		return err
	})
}

type roomMemberAlias struct {
	displayUsername string
	usernameKey     string
}

func countUsernameClaimsForKey(
	t testing.TB,
	ctx context.Context,
	fixture *integrationtest.PostgresSchema,
	usernameKey string,
) int {
	t.Helper()
	var count int
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM username_claims
		WHERE username_key = $1
	`, usernameKey).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func countUserOwnedClaims(
	t testing.TB,
	ctx context.Context,
	fixture *integrationtest.PostgresSchema,
	userID uuid.UUID,
	usernameKey string,
) int {
	t.Helper()
	var count int
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM username_claims
		WHERE owner_user_id = $1
		  AND username_key = $2
	`, userID, usernameKey).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertStoredUserUsername(
	t testing.TB,
	ctx context.Context,
	fixture *integrationtest.PostgresSchema,
	userID uuid.UUID,
	wantDisplayUsername string,
	wantUsernameKey string,
) {
	t.Helper()
	var gotDisplayUsername, gotUsernameKey string
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT username, current_username_key
		FROM users
		WHERE user_id = $1
	`, userID).Scan(&gotDisplayUsername, &gotUsernameKey); err != nil {
		t.Fatal(err)
	}
	if gotDisplayUsername != wantDisplayUsername || gotUsernameKey != wantUsernameKey {
		t.Fatalf(
			"user username = (%q, %q), want (%q, %q)",
			gotDisplayUsername, gotUsernameKey, wantDisplayUsername, wantUsernameKey,
		)
	}
}

// assertRoomMemberAliases checks every persisted alias row owned by one user, including rooms unaffected by a failed rename.
func assertRoomMemberAliases(
	t testing.TB,
	ctx context.Context,
	fixture *integrationtest.PostgresSchema,
	userID uuid.UUID,
	want map[uuid.UUID]roomMemberAlias,
) {
	t.Helper()
	rows, err := fixture.Pool.Query(ctx, `
		SELECT room_id, display_username, username_key
		FROM room_members
		WHERE user_id = $1
	`, userID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	got := make(map[uuid.UUID]roomMemberAlias, len(want))
	for rows.Next() {
		var roomID uuid.UUID
		var alias roomMemberAlias
		if err := rows.Scan(&roomID, &alias.displayUsername, &alias.usernameKey); err != nil {
			t.Fatal(err)
		}
		got[roomID] = alias
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("room aliases = %+v, want %+v", got, want)
	}
	for roomID, wantAlias := range want {
		gotAlias, ok := got[roomID]
		if !ok {
			t.Fatalf("missing room alias for room %s: got=%+v want=%+v", roomID, got, want)
		}
		if gotAlias != wantAlias {
			t.Fatalf("room alias for room %s = %+v, want %+v", roomID, gotAlias, wantAlias)
		}
	}
}
