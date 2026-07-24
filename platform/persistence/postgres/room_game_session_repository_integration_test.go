package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/internal/integrationtest"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
)

func TestRoomGameSessionRepositoryStartsRoomAndSessionAtomically(t *testing.T) {
	fixture := openRoomGameSessionStartFixture(t)
	repository := NewRoomGameSessionRepository(fixture.fixture.Pool)

	storedRoom, storedSession, replayed, err := repository.Start(fixture.ctx, fixture.before, fixture.after, fixture.commit, fixture.receipt)
	if err != nil {
		t.Fatal(err)
	}
	if replayed {
		t.Fatal("first start was reported as a replay")
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
	if loadedSession.Snapshot().Start.Config.MessageType != fixture.commit.Session.Snapshot().Start.Config.MessageType ||
		loadedSession.Snapshot().Start.Config.SchemaVersion != fixture.commit.Session.Snapshot().Start.Config.SchemaVersion ||
		string(loadedSession.Snapshot().Start.Config.Payload) != string(fixture.commit.Session.Snapshot().Start.Config.Payload) ||
		loadedSession.Snapshot().Start.ConfigDigest != fixture.commit.Session.Snapshot().Start.ConfigDigest ||
		loadedSession.Snapshot().Start.ConfigRevision != 7 ||
		loadedSession.Snapshot().Start.RoomVersion != fixture.before.Version().Room ||
		loadedSession.Snapshot().Start.MembershipVersion != fixture.before.Version().Membership ||
		loadedSession.Snapshot().Start.RoomOwnershipEpoch != fixture.before.Snapshot().OwnershipEpoch {
		t.Fatalf("loaded frozen start = %+v", loadedSession.Snapshot().Start)
	}
	assertGameSessionCounts(t, fixture.ctx, fixture.fixture, fixture.sessionID, 1, 0, 1, 1)
	loadedReceipt, err := repository.GetStartReceipt(fixture.ctx, fixture.receipt.Snapshot().Key, fixture.receipt.Snapshot().RequestDigest)
	if err != nil || loadedReceipt.Snapshot() != fixture.receipt.Snapshot() {
		t.Fatalf("loaded start receipt=%+v error=%v", loadedReceipt.Snapshot(), err)
	}
}

func TestRoomGameSessionRepositoryConsumesPendingStartProofAtomically(t *testing.T) {
	fixture := openRoomGameSessionStartFixtureWithPendingProof(t, 7)

	storedRoom, storedSession, replayed, err := NewRoomGameSessionRepository(fixture.fixture.Pool).Start(
		fixture.ctx, fixture.before, fixture.after, fixture.commit, fixture.receipt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if replayed || storedRoom.Snapshot().ActiveSessionID != fixture.sessionID || storedSession.Snapshot().ID != fixture.sessionID {
		t.Fatalf("stored room=%+v session=%+v replayed=%v", storedRoom.Snapshot(), storedSession.Snapshot(), replayed)
	}
	pending := loadLatestPendingStartRow(t, fixture)
	if !pending.ConsumedAt.Valid || pending.CancelledAt.Valid || uuid.UUID(pending.PendingStartID.Bytes) != fixture.pending.ID {
		t.Fatalf("pending row = %+v", pending)
	}
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

	if _, _, _, err := NewRoomGameSessionRepository(fixture.fixture.Pool).Start(
		fixture.ctx, fixture.before, fixture.after, fixture.commit, fixture.receipt,
	); err == nil {
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

func TestRoomGameSessionRepositoryRollsBackPendingConsumptionWhenOutboxInsertFails(t *testing.T) {
	fixture := openRoomGameSessionStartFixtureWithPendingProof(t, 7)
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

	if _, _, _, err := NewRoomGameSessionRepository(fixture.fixture.Pool).Start(
		fixture.ctx, fixture.before, fixture.after, fixture.commit, fixture.receipt,
	); err == nil {
		t.Fatal("expected outbox conflict to abort the cross-aggregate start")
	}
	pending := loadLatestPendingStartRow(t, fixture)
	if pending.ConsumedAt.Valid || pending.CancelledAt.Valid {
		t.Fatalf("pending row escaped rollback: %+v", pending)
	}
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

	if _, _, _, err := NewRoomGameSessionRepository(fixture.fixture.Pool).Start(
		fixture.ctx, fixture.before, fixture.after, fixture.commit, fixture.receipt,
	); !errors.Is(err, roomDomain.ErrRoomVersionConflict) {
		t.Fatalf("stale start error = %v", err)
	}
	if _, err := NewGameSessionRepository(fixture.fixture.Pool).Get(fixture.ctx, fixture.sessionID); !errors.Is(err, gameruntime.ErrSessionNotFound) {
		t.Fatalf("session after stale start error = %v", err)
	}
}

func TestRoomGameSessionRepositorySerializesConcurrentIdempotentStarts(t *testing.T) {
	fixture := openRoomGameSessionStartFixture(t)
	type startResult struct {
		replayed bool
		err      error
	}
	results := make(chan startResult, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _, replayed, err := NewRoomGameSessionRepository(fixture.fixture.Pool).Start(
				fixture.ctx, fixture.before, fixture.after, fixture.commit, fixture.receipt,
			)
			results <- startResult{replayed: replayed, err: err}
		}()
	}
	wait.Wait()
	close(results)

	successes, replays := 0, 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent start error = %v", result.err)
		}
		successes++
		if result.replayed {
			replays++
		}
	}
	if successes != 2 || replays != 1 {
		t.Fatalf("concurrent starts: successes=%d replays=%d", successes, replays)
	}
	assertGameSessionCounts(t, fixture.ctx, fixture.fixture, fixture.sessionID, 1, 0, 1, 1)
}

func TestRoomGameSessionRepositoryReplaysReceiptWithoutPendingDependency(t *testing.T) {
	fixture := openRoomGameSessionStartFixtureWithPendingProof(t, 7)
	repository := NewRoomGameSessionRepository(fixture.fixture.Pool)

	if _, _, _, err := repository.Start(fixture.ctx, fixture.before, fixture.after, fixture.commit, fixture.receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.fixture.Pool.Exec(
		fixture.ctx,
		`UPDATE room_pending_starts SET cancelled_at = $1 WHERE pending_start_id = $2`,
		fixture.commit.Session.Snapshot().StartedAt.Add(time.Second),
		fixture.pending.ID,
	); err != nil {
		t.Fatal(err)
	}

	room, session, replayed, err := repository.Start(fixture.ctx, fixture.before, fixture.after, fixture.commit, fixture.receipt)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed || room.Snapshot().ActiveSessionID != fixture.sessionID || session.Snapshot().ID != fixture.sessionID {
		t.Fatalf("replay room=%+v session=%+v replayed=%v", room.Snapshot(), session.Snapshot(), replayed)
	}
}

func TestRoomGameSessionRepositoryRacesCancelAndStartAtomically(t *testing.T) {
	fixture := openRoomGameSessionStartFixtureWithPendingProof(t, 7)
	repository := NewRoomGameSessionRepository(fixture.fixture.Pool)
	rules := NewRuleRepository(fixture.fixture.Pool)

	ready := make(chan struct{})
	type startResult struct {
		replayed bool
		err      error
	}
	startResults := make(chan startResult, 1)
	cancelResults := make(chan error, 1)

	go func() {
		<-ready
		_, _, replayed, err := repository.Start(fixture.ctx, fixture.before, fixture.after, fixture.commit, fixture.receipt)
		startResults <- startResult{replayed: replayed, err: err}
	}()
	go func() {
		<-ready
		cancelResults <- rules.CancelPendingStart(
			fixture.ctx,
			fixture.before.Snapshot().ID,
			fixture.pending.ID,
			fixture.pending.CancelToken,
			fixture.pending.OwnershipEpoch,
			fixture.pending.RequestDigest,
			fixture.commit.Session.Snapshot().StartedAt,
		)
	}()

	close(ready)
	result := <-startResults
	cancelErr := <-cancelResults

	switch {
	case result.err == nil:
		if cancelErr == nil || !errors.Is(cancelErr, roomDomain.ErrPendingStartInvalid) {
			t.Fatalf("cancel after start err = %v", cancelErr)
		}
		pending := loadLatestPendingStartRow(t, fixture)
		if !pending.ConsumedAt.Valid || pending.CancelledAt.Valid || result.replayed {
			t.Fatalf("pending row after start = %+v replayed=%v", pending, result.replayed)
		}
	case errors.Is(result.err, roomDomain.ErrPendingStartInvalid):
		if cancelErr != nil {
			t.Fatalf("cancel err = %v", cancelErr)
		}
		if _, err := NewGameSessionRepository(fixture.fixture.Pool).Get(fixture.ctx, fixture.sessionID); !errors.Is(err, gameruntime.ErrSessionNotFound) {
			t.Fatalf("session after cancel won error = %v", err)
		}
		pending := loadLatestPendingStartRow(t, fixture)
		if !pending.CancelledAt.Valid || pending.ConsumedAt.Valid {
			t.Fatalf("pending row after cancel = %+v", pending)
		}
	default:
		t.Fatalf("unexpected start error = %v", result.err)
	}
}

func TestRoomGameSessionRepositoryPersistsZeroStartConfigRevision(t *testing.T) {
	fixture := openRoomGameSessionStartFixtureWithOptions(t, 0, false)

	if _, _, _, err := NewRoomGameSessionRepository(fixture.fixture.Pool).Start(
		fixture.ctx, fixture.before, fixture.after, fixture.commit, fixture.receipt,
	); err != nil {
		t.Fatal(err)
	}
	loadedSession, err := NewGameSessionRepository(fixture.fixture.Pool).Get(fixture.ctx, fixture.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedSession.Snapshot().Start.ConfigRevision != 0 {
		t.Fatalf("loaded zero revision start = %+v", loadedSession.Snapshot().Start)
	}
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
	receipt   gameruntime.StartReceipt
	pending   roomDomain.PendingStart
}

func openRoomGameSessionStartFixture(t *testing.T) roomGameSessionStartFixture {
	return openRoomGameSessionStartFixtureWithOptions(t, 7, false)
}

func openRoomGameSessionStartFixtureWithPendingProof(t *testing.T, configRevision uint64) roomGameSessionStartFixture {
	return openRoomGameSessionStartFixtureWithOptions(t, configRevision, true)
}

func openRoomGameSessionStartFixtureWithOptions(t *testing.T, configRevision uint64, withPendingProof bool) roomGameSessionStartFixture {
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
	request := gameSessionCreateRequest(sessionID, before.Snapshot().ID, hostID, playerID, start.StartedAt)
	request.Start = gameruntime.FrozenStartConfig{
		Config:             request.Input.Clone(),
		ConfigRevision:     configRevision,
		RoomVersion:        before.Version().Room,
		MembershipVersion:  before.Version().Membership,
		RoomOwnershipEpoch: before.Snapshot().OwnershipEpoch,
	}
	session, batch, err := gameruntime.NewSession(request)
	if err != nil {
		t.Fatal(err)
	}
	event := newGameSessionOutboxEvent(t, gameruntime.GameSessionCreatedEventType, sessionID, uuid.New(), start.StartedAt, []byte("atomic-created"))
	commit := gameruntime.CreationCommit{Session: session, Batch: batch, OutboxEvents: []outbox.Event{event}}
	receipt := gameSessionStartReceiptForTest(t, before, commit, "atomic-start-request")
	var pending roomDomain.PendingStart
	if withPendingProof {
		pendingRepository := NewRuleRepository(fixture.Pool)
		pending, err = pendingRepository.BeginPendingStart(ctx, roomDomain.PendingStartCreate{
			RoomID:         before.Snapshot().ID,
			ActorUserID:    hostID,
			GameID:         "dice",
			ConfigRevision: configRevision,
			Expected:       before.Version(),
			OwnershipEpoch: before.Snapshot().OwnershipEpoch,
			OperationID:    "pending-start-" + sessionID.String(),
			RequestDigest:  pendingStartDigest("pending-" + sessionID.String()),
			Deadline:       start.StartedAt,
			At:             now.Add(time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
		commit.PendingStartProof = &gameruntime.PendingStartProof{
			PendingStartID: pending.ID,
			CancelToken:    pending.CancelToken,
		}
	}
	return roomGameSessionStartFixture{
		fixture: fixture, ctx: ctx, now: now, hostID: hostID, sessionID: sessionID,
		before: before, after: after, commit: commit,
		receipt: receipt, pending: pending,
	}
}

func loadLatestPendingStartRow(t testing.TB, fixture roomGameSessionStartFixture) sqlcgen.RoomPendingStart {
	t.Helper()
	row, err := sqlcgen.New(fixture.fixture.Pool).GetLatestRoomPendingStart(
		fixture.ctx,
		sqlcgen.GetLatestRoomPendingStartParams{RoomID: uuidToPG(fixture.before.Snapshot().ID)},
	)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func pendingStartDigest(seed string) [32]byte {
	return sha256.Sum256([]byte(seed))
}

func gameSessionStartReceiptForTest(
	t testing.TB,
	room roomDomain.Room,
	commit gameruntime.CreationCommit,
	marker string,
) gameruntime.StartReceipt {
	t.Helper()
	digestBytes := sha256.Sum256([]byte(marker))
	operationID, err := idempotency.NewOperationID(digestBytes[:16])
	if err != nil {
		t.Fatal(err)
	}
	digest, err := idempotency.NewDigest(digestBytes[:])
	if err != nil {
		t.Fatal(err)
	}
	session := commit.Session.Snapshot()
	receipt, err := gameruntime.NewStartReceipt(gameruntime.StartReceiptSnapshot{
		Key: gameruntime.StartKey{
			ActorUserID: room.Snapshot().HostUserID, RoomID: room.Snapshot().ID, OperationID: operationID,
		},
		RequestDigest: digest, SessionID: session.ID, CommittedAt: session.StartedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}
