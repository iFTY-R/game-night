package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/internal/integrationtest"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
)

func TestRoomGameSessionRepositoryStartsRoomAndSessionAtomically(t *testing.T) {
	fixture := openRoomGameSessionStartFixture(t)
	repository := NewRoomGameSessionRepository(fixture.fixture.Pool)

	storedRoom, storedSession, err := repository.Start(fixture.ctx, fixture.before, fixture.after, fixture.commit)
	if err != nil {
		t.Fatal(err)
	}
	if storedRoom.Snapshot().Status != roomDomain.RoomStatusPlaying || storedRoom.Snapshot().ActiveSessionID != fixture.sessionID {
		t.Fatalf("stored room = %+v", storedRoom.Snapshot())
	}
	if storedSession.Snapshot().ID != fixture.sessionID || storedSession.Snapshot().RoomID != fixture.before.Snapshot().ID {
		t.Fatalf("stored session = %+v", storedSession.Snapshot())
	}

	loadedRoom, err := NewRoomRepository(fixture.fixture.Pool).GetByID(fixture.ctx, fixture.before.Snapshot().ID)
	if err != nil {
		t.Fatal(err)
	}
	loadedSession, err := NewGameSessionRepository(fixture.fixture.Pool).Get(fixture.ctx, fixture.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedRoom.Snapshot().ActiveSessionID != loadedSession.Snapshot().ID || loadedSession.Snapshot().VersionKey.GameID != "dice" {
		t.Fatalf("room/session link mismatch: room=%+v session=%+v", loadedRoom.Snapshot(), loadedSession.Snapshot())
	}
	assertGameSessionCounts(t, fixture.ctx, fixture.fixture, fixture.sessionID, 1, 0, 1, 1)
}

func TestRoomGameSessionRepositoryRollsBackRoomWhenOutboxInsertFails(t *testing.T) {
	fixture := openRoomGameSessionStartFixture(t)
	conflict := fixture.commit.OutboxEvents[0].Snapshot()
	existing, err := outbox.NewEvent(
		conflict.ID, conflict.Type, conflict.AggregateType, conflict.AggregateID,
		[]byte("different-payload"), conflict.CreatedAt, conflict.AvailableAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newOutboxEventRepository(sqlcgen.New(fixture.fixture.Pool)).Insert(fixture.ctx, existing); err != nil {
		t.Fatal(err)
	}

	if _, _, err := NewRoomGameSessionRepository(fixture.fixture.Pool).Start(fixture.ctx, fixture.before, fixture.after, fixture.commit); err == nil {
		t.Fatal("expected outbox conflict to abort the cross-aggregate start")
	}
	roomAfterFailure, err := NewRoomRepository(fixture.fixture.Pool).GetByID(fixture.ctx, fixture.before.Snapshot().ID)
	if err != nil {
		t.Fatal(err)
	}
	if roomAfterFailure.Snapshot().Status != roomDomain.RoomStatusLobby || roomAfterFailure.Snapshot().RoomVersion != fixture.before.Snapshot().RoomVersion {
		t.Fatalf("room write escaped rollback: %+v", roomAfterFailure.Snapshot())
	}
	if _, err := NewGameSessionRepository(fixture.fixture.Pool).Get(fixture.ctx, fixture.sessionID); !errors.Is(err, gameruntime.ErrSessionNotFound) {
		t.Fatalf("session after rollback error = %v", err)
	}
	assertGameSessionCounts(t, fixture.ctx, fixture.fixture, fixture.sessionID, 0, 0, 0, 1)
}

func TestRoomGameSessionRepositoryRejectsStaleRoomBeforeCreatingSession(t *testing.T) {
	fixture := openRoomGameSessionStartFixture(t)
	latest, err := fixture.before.SetAdmission(
		fixture.hostID, roomDomain.AdmissionClosed, roomDomain.AdmissionOpen,
		fixture.before.Version(), fixture.now.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRoomRepository(fixture.fixture.Pool).UpdateCAS(fixture.ctx, fixture.before, latest); err != nil {
		t.Fatal(err)
	}

	if _, _, err := NewRoomGameSessionRepository(fixture.fixture.Pool).Start(fixture.ctx, fixture.before, fixture.after, fixture.commit); !errors.Is(err, roomDomain.ErrRoomVersionConflict) {
		t.Fatalf("stale start error = %v", err)
	}
	if _, err := NewGameSessionRepository(fixture.fixture.Pool).Get(fixture.ctx, fixture.sessionID); !errors.Is(err, gameruntime.ErrSessionNotFound) {
		t.Fatalf("session after stale start error = %v", err)
	}
}

func TestRoomGameSessionRepositorySerializesConcurrentStarts(t *testing.T) {
	fixture := openRoomGameSessionStartFixture(t)
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _, err := NewRoomGameSessionRepository(fixture.fixture.Pool).Start(
				fixture.ctx, fixture.before, fixture.after, fixture.commit,
			)
			results <- err
		}()
	}
	wait.Wait()
	close(results)

	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, roomDomain.ErrRoomVersionConflict):
			conflicts++
		default:
			t.Fatalf("concurrent start error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent starts: successes=%d conflicts=%d", successes, conflicts)
	}
	assertGameSessionCounts(t, fixture.ctx, fixture.fixture, fixture.sessionID, 1, 0, 1, 1)
}

func TestPartyRoomActiveSessionForeignKeyRejectsRoomOnlyStart(t *testing.T) {
	fixture := openRoomGameSessionStartFixture(t)
	if _, err := NewRoomRepository(fixture.fixture.Pool).UpdateCAS(fixture.ctx, fixture.before, fixture.after); !errors.Is(err, roomDomain.ErrRoomIntegrity) {
		t.Fatalf("room-only start error = %v", err)
	}
	loaded, err := NewRoomRepository(fixture.fixture.Pool).GetByID(fixture.ctx, fixture.before.Snapshot().ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Snapshot().Status != roomDomain.RoomStatusLobby || loaded.Snapshot().ActiveSessionID != uuid.Nil {
		t.Fatalf("invalid room-only start committed: %+v", loaded.Snapshot())
	}
}

type roomGameSessionStartFixture struct {
	fixture   *integrationtest.PostgresSchema
	ctx       context.Context
	now       time.Time
	hostID    uuid.UUID
	sessionID uuid.UUID
	before    roomDomain.Room
	after     roomDomain.Room
	commit    gameruntime.CreationCommit
}

func openRoomGameSessionStartFixture(t *testing.T) roomGameSessionStartFixture {
	t.Helper()
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), roomRepositoryIntegrationTimeout)
	t.Cleanup(cancel)
	applyTransactionTestMigrations(t, ctx, fixture)
	now := databaseIntegrationTime(t, ctx, fixture)
	hostID, playerID := uuid.New(), uuid.New()
	createRoomTestUser(t, ctx, fixture, hostID, "AtomicHost1", now)
	createRoomTestUser(t, ctx, fixture, playerID, "AtomicPlayer2", now)
	before, err := roomDomain.New(uuid.New(), hostID, "ATOMIC1", roomDomain.VisibilityPrivate, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := NewRoomRepository(fixture.Pool).Create(ctx, before)
	if err != nil {
		t.Fatal(err)
	}
	before, _, err = stored.Join(playerID, roomDomain.JoinIntentParticipant, stored.Version(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	before, err = NewRoomRepository(fixture.Pool).UpdateCAS(ctx, stored, before)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := uuid.New()
	after, start, err := before.StartSession(hostID, sessionID, "dice", 2, 9, before.Version(), now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	session, batch, err := gameruntime.NewSession(gameSessionCreateRequest(sessionID, before.Snapshot().ID, hostID, playerID, start.StartedAt))
	if err != nil {
		t.Fatal(err)
	}
	event := newGameSessionOutboxEvent(t, gameruntime.GameSessionCreatedEventType, sessionID, uuid.New(), start.StartedAt, []byte("atomic-created"))
	return roomGameSessionStartFixture{
		fixture: fixture, ctx: ctx, now: now, hostID: hostID, sessionID: sessionID,
		before: before, after: after, commit: gameruntime.CreationCommit{Session: session, Batch: batch, OutboxEvents: []outbox.Event{event}},
	}
}
