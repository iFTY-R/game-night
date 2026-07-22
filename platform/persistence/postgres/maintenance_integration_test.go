package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/internal/integrationtest"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
)

func TestExpiryCleanupFunctionIsRepeatableOnRealPostgres(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	cleanup := NewExpiryCleanup(fixture.Pool, 10*time.Minute)
	first, err := cleanup.RunReport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cleanup.RunReport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("empty cleanup was not repeatable: first=%+v second=%+v", first, second)
	}

	if _, err := fixture.Pool.Exec(ctx, "SELECT read_checkpoint_consumer_sequence()"); err != nil {
		t.Fatalf("checkpoint health reader function unavailable: %v", err)
	}
}

func TestExpiryCleanupClosesOnlyIdleLobbyRooms(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)

	now := databaseIntegrationTime(t, ctx, fixture)
	inactiveHostID, activeHostID := uuid.New(), uuid.New()
	createRoomTestUser(t, ctx, fixture, inactiveHostID, "IdleRoomHost", now)
	createRoomTestUser(t, ctx, fixture, activeHostID, "LiveRoomHost", now)
	repository := NewRoomRepository(fixture.Pool)
	inactive := createMaintenanceRoom(t, ctx, repository, inactiveHostID, "IDLE01", now)
	active := createMaintenanceRoom(t, ctx, repository, activeHostID, "LIVE01", now)
	ageRoomActivity(t, ctx, fixture, inactive.Snapshot().ID, now.Add(-11*time.Minute))
	ageRoomActivity(t, ctx, fixture, active.Snapshot().ID, now.Add(-11*time.Minute))
	if observedAt, err := repository.RecordRoomPresence(ctx, active.Snapshot().ID, activeHostID); err != nil || observedAt.IsZero() {
		t.Fatalf("renew active room: observed_at=%v err=%v", observedAt, err)
	}

	cleanup := NewExpiryCleanup(fixture.Pool, 10*time.Minute)
	first, err := cleanup.RunReport(ctx)
	if err != nil || first.ClosedRooms != 1 {
		t.Fatalf("first idle cleanup: report=%+v err=%v", first, err)
	}
	closed, err := repository.GetByID(ctx, inactive.Snapshot().ID)
	if err != nil || closed.Snapshot().Status != roomDomain.RoomStatusClosed ||
		closed.Snapshot().ParticipantAdmission != roomDomain.AdmissionClosed ||
		closed.Snapshot().SpectatorAdmission != roomDomain.AdmissionClosed ||
		closed.Snapshot().RoomVersion != inactive.Snapshot().RoomVersion+1 {
		t.Fatalf("inactive room was not softly closed: room=%+v err=%v", closed.Snapshot(), err)
	}
	stillActive, err := repository.GetByID(ctx, active.Snapshot().ID)
	if err != nil || stillActive.Snapshot().Status != roomDomain.RoomStatusLobby || stillActive.Version() != active.Version() {
		t.Fatalf("renewed room was closed: room=%+v err=%v", stillActive.Snapshot(), err)
	}
	second, err := cleanup.RunReport(ctx)
	if err != nil || second.ClosedRooms != 0 {
		t.Fatalf("repeat idle cleanup: report=%+v err=%v", second, err)
	}
}

func TestExpiryCleanupDoesNotClosePlayingRooms(t *testing.T) {
	fixture := openRoomGameSessionStartFixture(t)
	repository := NewRoomGameSessionRepository(fixture.fixture.Pool)
	playing, _, _, err := repository.Start(fixture.ctx, fixture.before, fixture.after, fixture.commit, fixture.receipt)
	if err != nil {
		t.Fatal(err)
	}
	ageRoomActivity(t, fixture.ctx, fixture.fixture, playing.Snapshot().ID, fixture.now.Add(-11*time.Minute))

	report, err := NewExpiryCleanup(fixture.fixture.Pool, 10*time.Minute).RunReport(fixture.ctx)
	if err != nil || report.ClosedRooms != 0 {
		t.Fatalf("playing cleanup: report=%+v err=%v", report, err)
	}
	loaded, err := NewRoomRepository(fixture.fixture.Pool).GetByID(fixture.ctx, playing.Snapshot().ID)
	if err != nil || loaded.Snapshot().Status != roomDomain.RoomStatusPlaying || loaded.Version() != playing.Version() {
		t.Fatalf("playing room changed during idle cleanup: room=%+v err=%v", loaded.Snapshot(), err)
	}
}

func TestExpiryCleanupClosesIdlePostGameRoomWithoutDiscardingReplayLink(t *testing.T) {
	fixture, sessionRepository, session, now := openGameSessionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	owner, err := session.AcquireOwnership(0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	owner, err = sessionRepository.AcquireOwnershipCAS(ctx, session, owner)
	if err != nil {
		t.Fatal(err)
	}
	roomRepository := NewRoomRepository(fixture.Pool)
	playing, err := roomRepository.GetByID(ctx, owner.Snapshot().RoomID)
	if err != nil {
		t.Fatal(err)
	}
	finishedAt := now.Add(2 * time.Second)
	postGame, err := playing.FinishSession(owner.Snapshot().ID, playing.Version(), finishedAt)
	if err != nil {
		t.Fatal(err)
	}
	postGame, _, err = NewRoomGameSessionRepository(fixture.Pool).FinishAction(
		ctx, playing, postGame, buildGameFinishedActionCommit(t, owner, finishedAt),
	)
	if err != nil {
		t.Fatal(err)
	}
	ageRoomActivity(t, ctx, fixture, postGame.Snapshot().ID, now.Add(-11*time.Minute))

	report, err := NewExpiryCleanup(fixture.Pool, 10*time.Minute).RunReport(ctx)
	if err != nil || report.ClosedRooms != 1 {
		t.Fatalf("post-game cleanup: report=%+v err=%v", report, err)
	}
	closed, err := roomRepository.GetByID(ctx, postGame.Snapshot().ID)
	if err != nil || closed.Snapshot().Status != roomDomain.RoomStatusClosed ||
		closed.Snapshot().LastFinishedSessionID != postGame.Snapshot().LastFinishedSessionID ||
		closed.Snapshot().LastFinishedGameID != postGame.Snapshot().LastFinishedGameID {
		t.Fatalf("post-game replay link changed during cleanup: room=%+v err=%v", closed.Snapshot(), err)
	}
}

func createMaintenanceRoom(
	t testing.TB,
	ctx context.Context,
	repository *RoomRepository,
	hostID uuid.UUID,
	code string,
	at time.Time,
) roomDomain.Room {
	t.Helper()
	room, err := roomDomain.New(uuid.New(), hostID, code, roomDomain.VisibilityPublic, 4, at)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := repository.Create(ctx, room)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

// ageRoomActivity moves both idle guards together so tests exercise the same boundary as production cleanup.
func ageRoomActivity(
	t testing.TB,
	ctx context.Context,
	fixture *integrationtest.PostgresSchema,
	roomID uuid.UUID,
	at time.Time,
) {
	t.Helper()
	transaction, err := fixture.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	if _, err := transaction.Exec(ctx, "UPDATE room_activity_leases SET last_seen_at = $2 WHERE room_id = $1", roomID, at); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE room_members
		SET joined_at = LEAST(joined_at, $2), last_seen_at = $2
		WHERE room_id = $1
	`, roomID, at); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE party_rooms
		SET created_at = LEAST(created_at, $2), updated_at = $2
		WHERE room_id = $1
	`, roomID, at); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
