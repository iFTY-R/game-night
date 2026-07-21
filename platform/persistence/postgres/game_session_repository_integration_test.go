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
	"github.com/iFTY-R/game-night/platform/replay"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

// gameSessionRepositoryIntegrationTimeout covers isolated migrations plus lock-based concurrency checks.
const gameSessionRepositoryIntegrationTimeout = 90 * time.Second

func TestGameSessionRepositoryPersistsExactStateAndFencesStaleEpoch(t *testing.T) {
	fixture, repository, session, now := openGameSessionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
	defer cancel()

	loaded, err := repository.Get(ctx, session.Snapshot().ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Snapshot().VersionKey != session.Snapshot().VersionKey || loaded.Snapshot().State.StateVersion != 1 ||
		loaded.Snapshot().OwnershipEpoch != 0 || len(loaded.Snapshot().Timers) != 1 {
		t.Fatalf("loaded session = %+v", loaded.Snapshot())
	}
	ownerOne, err := session.AcquireOwnership(0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ownerOne, err = repository.AcquireOwnershipCAS(ctx, session, ownerOne)
	if err != nil {
		t.Fatal(err)
	}
	ownerTwo, err := ownerOne.AcquireOwnership(1, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ownerTwo, err = repository.AcquireOwnershipCAS(ctx, ownerOne, ownerTwo)
	if err != nil {
		t.Fatal(err)
	}

	staleCommit := buildGameActionCommit(t, ownerOne, now.Add(3*time.Second), 1, []byte("stale"))
	if _, err := repository.CommitAction(ctx, staleCommit); !errors.Is(err, gameruntime.ErrOwnershipLost) {
		t.Fatalf("stale epoch error = %v", err)
	}
	assertGameSessionCounts(t, ctx, fixture, session.Snapshot().ID, 1, 0, 1, 1)

	validCommit := buildGameActionCommit(t, ownerTwo, now.Add(4*time.Second), 2, []byte("valid"))
	result, err := repository.CommitAction(ctx, validCommit)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed || result.Session.Snapshot().State.StateVersion != 2 || result.Session.Snapshot().OwnershipEpoch != 2 {
		t.Fatalf("commit result = %+v", result)
	}
	key := validCommit.Receipt().Snapshot().Key
	if _, err := repository.GetActionReceipt(ctx, key, validCommit.Receipt().Snapshot().RequestDigest); err != nil {
		t.Fatalf("durable receipt lookup: %v", err)
	}
	if _, err := repository.GetActionReceipt(ctx, key, digestForGameTest("different")); !errors.Is(err, idempotency.ErrConflict) {
		t.Fatalf("receipt digest conflict = %v", err)
	}
}

func TestGameSessionRepositoryConcurrentDuplicateActionCreatesOneBatch(t *testing.T) {
	fixture, repository, session, now := openGameSessionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
	defer cancel()
	owner, err := session.AcquireOwnership(0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	owner, err = repository.AcquireOwnershipCAS(ctx, session, owner)
	if err != nil {
		t.Fatal(err)
	}
	first := buildGameActionCommit(t, owner, now.Add(2*time.Second), 3, []byte("same"))
	second := buildGameActionCommit(t, owner, now.Add(2*time.Second), 3, []byte("same"))
	start := make(chan struct{})
	results := make(chan struct {
		result gameruntime.ActionCommitResult
		err    error
	}, 2)
	var waitGroup sync.WaitGroup
	for _, commit := range []gameruntime.ActionCommit{first, second} {
		waitGroup.Add(1)
		go func(commit gameruntime.ActionCommit) {
			defer waitGroup.Done()
			<-start
			result, err := repository.CommitAction(ctx, commit)
			results <- struct {
				result gameruntime.ActionCommitResult
				err    error
			}{result: result, err: err}
		}(commit)
	}
	close(start)
	waitGroup.Wait()
	close(results)
	var committed, replayed gameruntime.ActionCommitResult
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.result.Replayed {
			replayed = result.result
		} else {
			committed = result.result
		}
	}
	if committed.Receipt.Snapshot() != replayed.Receipt.Snapshot() || committed.Session.Snapshot().State.StateVersion != 2 ||
		replayed.Session.Snapshot().State.StateVersion != 2 {
		t.Fatalf("committed=%+v replayed=%+v", committed, replayed)
	}
	assertGameSessionCounts(t, ctx, fixture, session.Snapshot().ID, 2, 1, 2, 2)
}

func TestGameSessionRepositoryConcurrentActionIDDigestReuseConflicts(t *testing.T) {
	fixture, repository, session, now := openGameSessionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
	defer cancel()
	owner, err := session.AcquireOwnership(0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	owner, err = repository.AcquireOwnershipCAS(ctx, session, owner)
	if err != nil {
		t.Fatal(err)
	}
	first := buildGameActionCommitWithDigest(t, owner, now.Add(2*time.Second), 8, []byte("first"), digestForGameTest("first-request"))
	second := buildGameActionCommitWithDigest(t, owner, now.Add(2*time.Second), 8, []byte("second"), digestForGameTest("second-request"))
	start := make(chan struct{})
	errorsSeen := make(chan error, 2)
	var waitGroup sync.WaitGroup
	for _, commit := range []gameruntime.ActionCommit{first, second} {
		waitGroup.Add(1)
		go func(commit gameruntime.ActionCommit) {
			defer waitGroup.Done()
			<-start
			_, err := repository.CommitAction(ctx, commit)
			errorsSeen <- err
		}(commit)
	}
	close(start)
	waitGroup.Wait()
	close(errorsSeen)
	succeeded, conflicted := 0, 0
	for err := range errorsSeen {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, idempotency.ErrConflict):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent result: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	assertGameSessionCounts(t, ctx, fixture, session.Snapshot().ID, 2, 1, 2, 2)
}

func TestGameSessionRepositoryOutboxFailureRollsBackWholeAction(t *testing.T) {
	fixture, repository, session, now := openGameSessionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
	defer cancel()
	owner, err := session.AcquireOwnership(0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	owner, err = repository.AcquireOwnershipCAS(ctx, session, owner)
	if err != nil {
		t.Fatal(err)
	}
	commit := buildGameActionCommit(t, owner, now.Add(2*time.Second), 5, []byte("new"))
	conflictEvent := commit.OutboxEvents()[0].Snapshot()
	if _, err := fixture.Pool.Exec(ctx, `
        INSERT INTO outbox_events (event_id, event_type, aggregate_type, aggregate_id, payload, created_at, available_at)
        VALUES ($1, $2, $3, $4, $5, $6, $6)
    `, conflictEvent.ID, conflictEvent.Type.Value(), conflictEvent.AggregateType.Value(), conflictEvent.AggregateID, []byte("existing"), conflictEvent.CreatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CommitAction(ctx, commit); err == nil {
		t.Fatal("expected outbox conflict")
	}
	assertGameSessionCounts(t, ctx, fixture, session.Snapshot().ID, 1, 0, 1, 2)
	loaded, err := repository.Get(ctx, session.Snapshot().ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Snapshot().State.StateVersion != 1 || loaded.Snapshot().OwnershipEpoch != 1 || len(loaded.Snapshot().Timers) != 1 {
		t.Fatalf("rolled-back session = %+v", loaded.Snapshot())
	}
}

func TestGameSessionRepositoryParticipantFenceRejectsReceiptAfterRemoval(t *testing.T) {
	fixture, repository, session, now := openGameSessionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
	defer cancel()
	owner, err := session.AcquireOwnership(0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	owner, err = repository.AcquireOwnershipCAS(ctx, session, owner)
	if err != nil {
		t.Fatal(err)
	}
	hostID, playerID := owner.Snapshot().Participants[0].UserID, owner.Snapshot().Participants[1].UserID
	commit := buildGameActionCommitForActor(t, owner, playerID, now.Add(2*time.Second), 11, []byte("player-action"), digestForGameTest("player-action"))
	if _, err := repository.CommitAction(ctx, commit); err != nil {
		t.Fatal(err)
	}
	roomRepository := NewRoomRepository(fixture.Pool)
	room, err := roomRepository.GetByID(ctx, owner.Snapshot().RoomID)
	if err != nil {
		t.Fatal(err)
	}
	nextRoom, _, err := room.RemoveMember(hostID, playerID, room.Version(), now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := roomRepository.UpdateCAS(ctx, room, nextRoom); err != nil {
		t.Fatal(err)
	}
	key := commit.Receipt().Snapshot().Key
	if _, err := repository.GetActionReceipt(ctx, key, commit.Receipt().Snapshot().RequestDigest); !errors.Is(err, gameruntime.ErrParticipantNotActive) {
		t.Fatalf("removed receipt lookup error = %v", err)
	}
	if _, err := repository.CommitAction(ctx, commit); !errors.Is(err, gameruntime.ErrParticipantNotActive) {
		t.Fatalf("removed action replay error = %v", err)
	}
}

func TestGameSessionRepositoryTimerCommitReplaysOneDurableResult(t *testing.T) {
	fixture, repository, session, now := openGameSessionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
	defer cancel()
	owner, err := session.AcquireOwnership(0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	owner, err = repository.AcquireOwnershipCAS(ctx, session, owner)
	if err != nil {
		t.Fatal(err)
	}
	commit := buildGameTimerCommit(t, owner, now.Add(31*time.Second))
	first, err := repository.CommitTimer(ctx, commit)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.CommitTimer(ctx, commit)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || !second.Replayed || first.Receipt.Snapshot() != second.Receipt.Snapshot() ||
		second.Session.Snapshot().State.StateVersion != 2 {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	var receiptCount int
	if err := fixture.Pool.QueryRow(ctx, "SELECT count(*) FROM game_timer_receipts WHERE session_id = $1", session.Snapshot().ID).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if receiptCount != 1 {
		t.Fatalf("timer receipts = %d", receiptCount)
	}
}

func TestGameSessionRepositorySystemOperationReplaysAndRejectsDigestReuse(t *testing.T) {
	_, repository, session, now := openGameSessionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
	defer cancel()
	owner, err := session.AcquireOwnership(0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	owner, err = repository.AcquireOwnershipCAS(ctx, session, owner)
	if err != nil {
		t.Fatal(err)
	}
	operationID := gameSessionOperationID(t, 12)
	source := gameruntime.SystemSource{Kind: gameruntime.SystemSourceRoomOutbox, EventID: uuid.New()}
	commit := buildGameSystemCommit(t, owner, operationID, source, digestForGameTest("system-request"), now.Add(2*time.Second))
	first, err := repository.CommitSystem(ctx, commit)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.CommitSystem(ctx, commit)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || !second.Replayed || first.Receipt.Snapshot() != second.Receipt.Snapshot() {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	conflicting := buildGameSystemCommit(t, owner, operationID, source, digestForGameTest("different-system-request"), now.Add(2*time.Second))
	if _, err := repository.CommitSystem(ctx, conflicting); !errors.Is(err, idempotency.ErrConflict) {
		t.Fatalf("system digest conflict = %v", err)
	}
}

func TestRoomGameSessionRepositoryFinishesActionAndRoomAtomically(t *testing.T) {
	fixture, repository, session, now := openGameSessionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
	defer cancel()
	owner, err := session.AcquireOwnership(0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	owner, err = repository.AcquireOwnershipCAS(ctx, session, owner)
	if err != nil {
		t.Fatal(err)
	}
	finishedAt := now.Add(2 * time.Second)
	commit := buildGameFinishedActionCommit(t, owner, finishedAt)
	room, err := NewRoomRepository(fixture.Pool).GetByID(ctx, owner.Snapshot().RoomID)
	if err != nil {
		t.Fatal(err)
	}
	spectatorID := uuid.New()
	createRoomTestUser(t, ctx, fixture, spectatorID, "ReplayViewer3", now)
	roomWithSpectator, _, err := room.Join(spectatorID, roomDomain.JoinIntentSpectator, room.Version(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	room, err = NewRoomRepository(fixture.Pool).UpdateCAS(ctx, room, roomWithSpectator)
	if err != nil {
		t.Fatal(err)
	}
	nextRoom, err := room.FinishSession(owner.Snapshot().ID, room.Version(), finishedAt)
	if err != nil {
		t.Fatal(err)
	}
	storedRoom, result, err := NewRoomGameSessionRepository(fixture.Pool).FinishAction(ctx, room, nextRoom, commit)
	if err != nil {
		t.Fatal(err)
	}
	if storedRoom.Snapshot().Status != roomDomain.RoomStatusPostGame || storedRoom.Snapshot().ActiveSessionID != uuid.Nil ||
		storedRoom.Snapshot().LastFinishedSessionID != owner.Snapshot().ID ||
		result.Session.Snapshot().Status != gameruntime.StatusFinished {
		t.Fatalf("room=%+v result=%+v", storedRoom.Snapshot(), result)
	}
	var storedPolicy string
	var policyVersion int64
	var memberSnapshotAt time.Time
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT policy, policy_version, member_snapshot_completed_at
		FROM game_session_replay_access WHERE session_id = $1
	`, owner.Snapshot().ID).Scan(&storedPolicy, &policyVersion, &memberSnapshotAt); err != nil {
		t.Fatal(err)
	}
	if storedPolicy != "participant" || policyVersion != 1 || memberSnapshotAt.IsZero() {
		t.Fatalf("replay access policy=%q version=%d snapshot_at=%v", storedPolicy, policyVersion, memberSnapshotAt)
	}
	var memberCount int
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT count(*) FROM game_session_replay_members WHERE session_id = $1
	`, owner.Snapshot().ID).Scan(&memberCount); err != nil {
		t.Fatal(err)
	}
	if memberCount != len(owner.Snapshot().Participants)+1 {
		t.Fatalf("replay member count=%d participants=%d", memberCount, len(owner.Snapshot().Participants))
	}
	replayRepository := NewReplayAccessRepository(fixture.Pool)
	participant := owner.Snapshot().Participants[0].UserID
	projectionPolicy, err := replayRepository.Authorize(ctx, participant, owner.Snapshot().RoomID, owner.Snapshot().ID)
	if err != nil || projectionPolicy != game.ReplayAccessParticipant {
		t.Fatalf("participant replay policy=%q err=%v", projectionPolicy, err)
	}
	if _, err := replayRepository.Authorize(ctx, spectatorID, owner.Snapshot().RoomID, owner.Snapshot().ID); !errors.Is(err, replay.ErrAccessDenied) {
		t.Fatalf("default spectator replay error=%v", err)
	}
	access, err := replayRepository.SetPolicy(ctx, replay.SetPolicyCommand{
		ActorUserID: participant, RoomID: owner.Snapshot().RoomID, SessionID: owner.Snapshot().ID,
		Policy: replay.PolicyRoomMember, ExpectedVersion: 1, UpdatedAt: finishedAt.Add(time.Second),
	})
	if err != nil || access.Policy != replay.PolicyRoomMember || access.Version != 2 {
		t.Fatalf("updated replay access=%+v err=%v", access, err)
	}
	projectionPolicy, err = replayRepository.Authorize(ctx, spectatorID, owner.Snapshot().RoomID, owner.Snapshot().ID)
	if err != nil || projectionPolicy != game.ReplayAccessRoomMember {
		t.Fatalf("historical member replay policy=%q err=%v", projectionPolicy, err)
	}
	if _, err := replayRepository.SetPolicy(ctx, replay.SetPolicyCommand{
		ActorUserID: participant, RoomID: owner.Snapshot().RoomID, SessionID: owner.Snapshot().ID,
		Policy: replay.PolicyPublic, ExpectedVersion: 2, UpdatedAt: finishedAt.Add(time.Second),
	}); !errors.Is(err, replay.ErrPolicyUnavailable) {
		t.Fatalf("private public replay policy error=%v", err)
	}
}

func TestReplayAccessRepositoryAllowsPublicPolicyOnlyForPublicRoom(t *testing.T) {
	fixture, repository, session, now := openGameSessionFixtureWithVisibility(t, roomDomain.VisibilityPublic)
	ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
	defer cancel()
	owner := acquireGameSessionForTest(t, ctx, repository, session, now.Add(time.Second))
	finishedAt := now.Add(2 * time.Second)
	room, err := NewRoomRepository(fixture.Pool).GetByID(ctx, owner.Snapshot().RoomID)
	if err != nil {
		t.Fatal(err)
	}
	nextRoom, err := room.FinishSession(owner.Snapshot().ID, room.Version(), finishedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewRoomGameSessionRepository(fixture.Pool).FinishAction(
		ctx, room, nextRoom, buildGameFinishedActionCommit(t, owner, finishedAt),
	); err != nil {
		t.Fatal(err)
	}
	outsiderID := uuid.New()
	createRoomTestUser(t, ctx, fixture, outsiderID, "ReplayPublic4", now)
	replayRepository := NewReplayAccessRepository(fixture.Pool)
	if _, err := replayRepository.Authorize(ctx, outsiderID, owner.Snapshot().RoomID, owner.Snapshot().ID); !errors.Is(err, replay.ErrAccessDenied) {
		t.Fatalf("default public-room outsider replay error=%v", err)
	}
	hostID := owner.Snapshot().Participants[0].UserID
	access, err := replayRepository.SetPolicy(ctx, replay.SetPolicyCommand{
		ActorUserID: hostID, RoomID: owner.Snapshot().RoomID, SessionID: owner.Snapshot().ID,
		Policy: replay.PolicyPublic, ExpectedVersion: 1, UpdatedAt: finishedAt.Add(time.Second),
	})
	if err != nil || access.Policy != replay.PolicyPublic {
		t.Fatalf("public replay access=%+v err=%v", access, err)
	}
	policy, err := replayRepository.Authorize(ctx, outsiderID, owner.Snapshot().RoomID, owner.Snapshot().ID)
	if err != nil || policy != game.ReplayAccessPublic {
		t.Fatalf("public replay policy=%q err=%v", policy, err)
	}
}

func TestRoomGameSessionRepositoryConcurrentTerminalActionReplaysCommittedResult(t *testing.T) {
	fixture, repository, session, now := openGameSessionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
	defer cancel()
	owner, err := session.AcquireOwnership(0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	owner, err = repository.AcquireOwnershipCAS(ctx, session, owner)
	if err != nil {
		t.Fatal(err)
	}
	room, err := NewRoomRepository(fixture.Pool).GetByID(ctx, owner.Snapshot().RoomID)
	if err != nil {
		t.Fatal(err)
	}
	finishedAt := now.Add(2 * time.Second)
	nextRoom, err := room.FinishSession(owner.Snapshot().ID, room.Version(), finishedAt)
	if err != nil {
		t.Fatal(err)
	}
	commit := buildGameFinishedActionCommit(t, owner, finishedAt)
	start := make(chan struct{})
	results := make(chan struct {
		result gameruntime.ActionCommitResult
		err    error
	}, 2)
	for range 2 {
		go func() {
			<-start
			_, result, err := NewRoomGameSessionRepository(fixture.Pool).FinishAction(ctx, room, nextRoom, commit)
			results <- struct {
				result gameruntime.ActionCommitResult
				err    error
			}{result: result, err: err}
		}()
	}
	close(start)
	committed, replayed := 0, 0
	var receipt gameruntime.ActionReceiptSnapshot
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.result.Replayed {
			replayed++
		} else {
			committed++
		}
		if receipt.Key.SessionID == uuid.Nil {
			receipt = result.result.Receipt.Snapshot()
		} else if receipt != result.result.Receipt.Snapshot() {
			t.Fatalf("receipt mismatch: first=%+v next=%+v", receipt, result.result.Receipt.Snapshot())
		}
	}
	if committed != 1 || replayed != 1 {
		t.Fatalf("committed=%d replayed=%d", committed, replayed)
	}
	storedRoom, err := NewRoomRepository(fixture.Pool).GetByID(ctx, room.Snapshot().ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedRoom.Snapshot().RoomVersion != room.Snapshot().RoomVersion+1 || storedRoom.Snapshot().Status != roomDomain.RoomStatusPostGame {
		t.Fatalf("stored room = %+v", storedRoom.Snapshot())
	}
	assertGameSessionCounts(t, ctx, fixture, owner.Snapshot().ID, 2, 1, 2, 2)
}

func TestRoomGameSessionRepositoryReplaysTerminalTimerAndSystem(t *testing.T) {
	t.Run("timer", func(t *testing.T) {
		fixture, repository, session, now := openGameSessionFixture(t)
		ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
		defer cancel()
		owner, err := session.AcquireOwnership(0, now.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		owner, err = repository.AcquireOwnershipCAS(ctx, session, owner)
		if err != nil {
			t.Fatal(err)
		}
		finishedAt := now.Add(31 * time.Second)
		commit := buildGameFinishedTimerCommit(t, owner, finishedAt)
		room, err := NewRoomRepository(fixture.Pool).GetByID(ctx, owner.Snapshot().RoomID)
		if err != nil {
			t.Fatal(err)
		}
		nextRoom, err := room.FinishSession(owner.Snapshot().ID, room.Version(), finishedAt)
		if err != nil {
			t.Fatal(err)
		}
		crossRepository := NewRoomGameSessionRepository(fixture.Pool)
		firstRoom, first, err := crossRepository.FinishTimer(ctx, room, nextRoom, commit)
		if err != nil {
			t.Fatal(err)
		}
		secondRoom, second, err := crossRepository.FinishTimer(ctx, room, nextRoom, commit)
		if err != nil {
			t.Fatal(err)
		}
		if first.Replayed || !second.Replayed || first.Receipt.Snapshot() != second.Receipt.Snapshot() ||
			firstRoom.Snapshot().RoomVersion != secondRoom.Snapshot().RoomVersion {
			t.Fatalf("first=%+v second=%+v firstRoom=%+v secondRoom=%+v", first, second, firstRoom.Snapshot(), secondRoom.Snapshot())
		}
		assertGameSessionCounts(t, ctx, fixture, owner.Snapshot().ID, 2, 0, 2, 2)
	})

	t.Run("system", func(t *testing.T) {
		fixture, repository, session, now := openGameSessionFixture(t)
		ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
		defer cancel()
		owner, err := session.AcquireOwnership(0, now.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		owner, err = repository.AcquireOwnershipCAS(ctx, session, owner)
		if err != nil {
			t.Fatal(err)
		}
		finishedAt := now.Add(2 * time.Second)
		commit := buildGameFinishedSystemCommit(
			t, owner, gameSessionOperationID(t, 26),
			gameruntime.SystemSource{Kind: gameruntime.SystemSourcePlatform, EventID: uuid.New()},
			digestForGameTest("terminal-system"), finishedAt,
		)
		room, err := NewRoomRepository(fixture.Pool).GetByID(ctx, owner.Snapshot().RoomID)
		if err != nil {
			t.Fatal(err)
		}
		nextRoom, err := room.FinishSession(owner.Snapshot().ID, room.Version(), finishedAt)
		if err != nil {
			t.Fatal(err)
		}
		crossRepository := NewRoomGameSessionRepository(fixture.Pool)
		firstRoom, first, err := crossRepository.FinishSystem(ctx, room, nextRoom, uuid.Nil, commit)
		if err != nil {
			t.Fatal(err)
		}
		secondRoom, second, err := crossRepository.FinishSystem(ctx, room, nextRoom, uuid.Nil, commit)
		if err != nil {
			t.Fatal(err)
		}
		if first.Replayed || !second.Replayed || first.Receipt.Snapshot() != second.Receipt.Snapshot() ||
			firstRoom.Snapshot().RoomVersion != secondRoom.Snapshot().RoomVersion {
			t.Fatalf("first=%+v second=%+v firstRoom=%+v secondRoom=%+v", first, second, firstRoom.Snapshot(), secondRoom.Snapshot())
		}
		assertGameSessionCounts(t, ctx, fixture, owner.Snapshot().ID, 2, 0, 2, 2)
	})
}

func TestRoomGameSessionRepositoryRequiresCurrentHostForHostFinish(t *testing.T) {
	fixture, repository, session, now := openGameSessionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
	defer cancel()
	owner, err := session.AcquireOwnership(0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	owner, err = repository.AcquireOwnershipCAS(ctx, session, owner)
	if err != nil {
		t.Fatal(err)
	}
	hostID, playerID := owner.Snapshot().Participants[0].UserID, owner.Snapshot().Participants[1].UserID
	finishedAt := now.Add(2 * time.Second)
	source := gameruntime.SystemSource{Kind: gameruntime.SystemSourceHostAPI, EventID: uuid.New(), RequestedByUserID: playerID}
	commit := buildGameFinishedSystemCommit(t, owner, gameSessionOperationID(t, 27), source, digestForGameTest("unauthorized-host-finish"), finishedAt)
	room, err := NewRoomRepository(fixture.Pool).GetByID(ctx, owner.Snapshot().RoomID)
	if err != nil {
		t.Fatal(err)
	}
	nextRoom, err := room.FinishSession(owner.Snapshot().ID, room.Version(), finishedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewRoomGameSessionRepository(fixture.Pool).FinishSystem(ctx, room, nextRoom, playerID, commit); !errors.Is(err, roomDomain.ErrHostRequired) {
		t.Fatalf("non-host finish error = %v", err)
	}
	stored, err := repository.Get(ctx, owner.Snapshot().ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Snapshot().Status != gameruntime.StatusActive || stored.Snapshot().State.StateVersion != 1 {
		t.Fatalf("unauthorized finish changed session: %+v", stored.Snapshot())
	}
	var operations int
	if err := fixture.Pool.QueryRow(ctx, "SELECT count(*) FROM game_system_operations WHERE session_id = $1", owner.Snapshot().ID).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if operations != 0 || room.Snapshot().HostUserID != hostID {
		t.Fatalf("operations=%d room=%+v", operations, room.Snapshot())
	}
	authorizedAt := now.Add(3 * time.Second)
	authorizedSource := gameruntime.SystemSource{Kind: gameruntime.SystemSourceHostAPI, EventID: uuid.New(), RequestedByUserID: hostID}
	authorizedCommit := buildGameFinishedSystemCommit(
		t, owner, gameSessionOperationID(t, 28), authorizedSource, digestForGameTest("authorized-host-finish"), authorizedAt,
	)
	authorizedRoom, err := room.FinishSession(owner.Snapshot().ID, room.Version(), authorizedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewRoomGameSessionRepository(fixture.Pool).FinishSystem(
		ctx, room, authorizedRoom, hostID, authorizedCommit,
	); err != nil {
		t.Fatal(err)
	}
	batches, err := repository.ReadEventBatches(ctx, owner.Snapshot().ID, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 1 || batches[0].Snapshot().SystemSource != authorizedSource {
		t.Fatalf("host finish batches = %+v", batches)
	}
}

func TestGameSystemOperationConstraintsRejectInvalidSourceRequesterShape(t *testing.T) {
	fixture, _, session, now := openGameSessionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
	defer cancel()
	hostID := session.Snapshot().Participants[0].UserID
	for _, test := range []struct {
		name      string
		kind      string
		eventID   any
		requester any
	}{
		{name: "missing source event", kind: string(gameruntime.SystemSourcePlatform), requester: nil},
		{name: "host missing requester", kind: string(gameruntime.SystemSourceHostAPI), eventID: uuid.New(), requester: nil},
		{name: "room outbox with requester", kind: string(gameruntime.SystemSourceRoomOutbox), eventID: uuid.New(), requester: hostID},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := fixture.Pool.Exec(ctx, `
				INSERT INTO game_system_operations (
					session_id, operation_id, source_kind, source_event_id, requested_by_user_id,
					logical_digest, status, created_at
				) VALUES ($1, $2, $3, $4, $5, $6, 'pending', $7)
			`, session.Snapshot().ID, gameSessionOperationID(t, 29).Value(), test.kind, test.eventID, test.requester,
				digestForGameTest("invalid-source-shape").Bytes(), now)
			if err == nil {
				t.Fatal("expected source/requester constraint failure")
			}
		})
	}
}

func TestGameSessionRepositoryRecomputesDurablePendingSystemOperation(t *testing.T) {
	fixture, repository, session, now := openGameSessionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
	defer cancel()
	owner, err := session.AcquireOwnership(0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	owner, err = repository.AcquireOwnershipCAS(ctx, session, owner)
	if err != nil {
		t.Fatal(err)
	}
	operationID := gameSessionOperationID(t, 21)
	source := gameruntime.SystemSource{Kind: gameruntime.SystemSourceRoomOutbox, EventID: uuid.New()}
	digest := digestForGameTest("pending-system-request")
	staleSystem := buildGameSystemCommit(t, owner, operationID, source, digest, now.Add(3*time.Second))
	action := buildGameActionCommit(t, owner, now.Add(2*time.Second), 22, []byte("concurrent-action"))
	if _, err := repository.CommitAction(ctx, action); err != nil {
		t.Fatal(err)
	}
	pending, err := repository.CommitSystem(ctx, staleSystem)
	if err != nil {
		t.Fatal(err)
	}
	if !pending.Retry || pending.Session.Snapshot().State.StateVersion != 2 {
		t.Fatalf("pending result = %+v", pending)
	}
	recomputed := buildGameSystemCommit(t, pending.Session, operationID, source, digest, now.Add(4*time.Second))
	completed, err := repository.CommitSystem(ctx, recomputed)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Retry || completed.Replayed || completed.Session.Snapshot().State.StateVersion != 3 {
		t.Fatalf("completed result = %+v", completed)
	}
	replayed, err := repository.CommitSystem(ctx, recomputed)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.Receipt.Snapshot() != completed.Receipt.Snapshot() {
		t.Fatalf("replayed result = %+v", replayed)
	}
	var status string
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT status FROM game_system_operations WHERE session_id = $1 AND operation_id = $2
	`, session.Snapshot().ID, operationID.Value()).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Fatalf("system operation status = %q", status)
	}
	assertGameSessionCounts(t, ctx, fixture, session.Snapshot().ID, 3, 1, 3, 3)
}

func TestGameSessionRepositoryCompletesTerminalSystemNoopOnce(t *testing.T) {
	fixture, repository, session, now := openGameSessionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
	defer cancel()
	owner, err := session.AcquireOwnership(0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	owner, err = repository.AcquireOwnershipCAS(ctx, session, owner)
	if err != nil {
		t.Fatal(err)
	}
	finish := buildGameFinishedActionCommit(t, owner, now.Add(2*time.Second))
	roomRepository := NewRoomRepository(fixture.Pool)
	room, err := roomRepository.GetByID(ctx, owner.Snapshot().RoomID)
	if err != nil {
		t.Fatal(err)
	}
	nextRoom, err := room.FinishSession(owner.Snapshot().ID, room.Version(), now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewRoomGameSessionRepository(fixture.Pool).FinishAction(ctx, room, nextRoom, finish); err != nil {
		t.Fatal(err)
	}
	key := gameruntime.SystemKey{
		SessionID: owner.Snapshot().ID, OperationID: gameSessionOperationID(t, 23),
		Source: gameruntime.SystemSource{Kind: gameruntime.SystemSourcePlatform, EventID: uuid.New()},
	}
	digest := digestForGameTest("late-terminal-system")
	first, err := repository.CompleteSystemNoop(ctx, key, digest, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.CompleteSystemNoop(ctx, key, digest, now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || !second.Replayed || first.Receipt.Snapshot() != second.Receipt.Snapshot() ||
		first.Receipt.Snapshot().ResultCode != gameruntime.ResultCodeNoopTerminal {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	if _, err := repository.CompleteSystemNoop(ctx, key, digestForGameTest("different-terminal-system"), now.Add(5*time.Second)); !errors.Is(err, idempotency.ErrConflict) {
		t.Fatalf("terminal digest conflict = %v", err)
	}
	var batchID *uuid.UUID
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT batch_id FROM game_system_operations WHERE session_id = $1 AND operation_id = $2
	`, owner.Snapshot().ID, key.OperationID.Value()).Scan(&batchID); err != nil {
		t.Fatal(err)
	}
	if batchID != nil {
		t.Fatalf("terminal no-op unexpectedly references batch %v", *batchID)
	}
	assertGameSessionCounts(t, ctx, fixture, owner.Snapshot().ID, 2, 1, 2, 2)
}

func TestGameSessionRepositoryPersistsSuspendResumeAndAtomicCancel(t *testing.T) {
	fixture, repository, session, now := openGameSessionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
	defer cancel()
	owner, err := session.AcquireOwnership(0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	owner, err = repository.AcquireOwnershipCAS(ctx, session, owner)
	if err != nil {
		t.Fatal(err)
	}
	suspended, err := owner.Suspend(owner.Snapshot().OwnershipEpoch, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	suspendCommit := newGameLifecycleCommit(t, owner, suspended, gameruntime.GameSessionSuspendedEventType)
	suspended, err = repository.CommitLifecycle(ctx, suspendCommit)
	if err != nil {
		t.Fatal(err)
	}
	due, err := repository.ListDueTimers(ctx, now.Add(31*time.Second), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("suspended timers were scheduled: %+v", due)
	}
	resumed, err := suspended.Resume(suspended.Snapshot().OwnershipEpoch, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	resumed, err = repository.CommitLifecycle(ctx, newGameLifecycleCommit(t, suspended, resumed, gameruntime.GameSessionResumedEventType))
	if err != nil {
		t.Fatal(err)
	}
	due, err = repository.ListDueTimers(ctx, now.Add(31*time.Second), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].SessionID != owner.Snapshot().ID {
		t.Fatalf("resumed due timers = %+v", due)
	}
	suspendedAgain, err := resumed.Suspend(resumed.Snapshot().OwnershipEpoch, now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	suspendedAgain, err = repository.CommitLifecycle(ctx, newGameLifecycleCommit(t, resumed, suspendedAgain, gameruntime.GameSessionSuspendedEventType))
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := suspendedAgain.Cancel(suspendedAgain.Snapshot().OwnershipEpoch, now.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	room, err := NewRoomRepository(fixture.Pool).GetByID(ctx, owner.Snapshot().RoomID)
	if err != nil {
		t.Fatal(err)
	}
	nextRoom, err := room.CancelSession(owner.Snapshot().ID, room.Version(), now.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	storedRoom, storedSession, err := NewRoomGameSessionRepository(fixture.Pool).Cancel(
		ctx, room, nextRoom, newGameLifecycleCommit(t, suspendedAgain, cancelled, gameruntime.GameSessionCancelledEventType),
	)
	if err != nil {
		t.Fatal(err)
	}
	if storedRoom.Snapshot().Status != roomDomain.RoomStatusLobby || storedSession.Snapshot().Status != gameruntime.StatusCancelled ||
		len(storedSession.Snapshot().Timers) != 0 || storedSession.Snapshot().State.StateVersion != 1 {
		t.Fatalf("room=%+v session=%+v", storedRoom.Snapshot(), storedSession.Snapshot())
	}
	assertGameSessionCounts(t, ctx, fixture, owner.Snapshot().ID, 1, 0, 1, 5)
}

func TestRoomGameSessionRepositoryRollsBackTerminalSessionWhenRoomFinishFails(t *testing.T) {
	fixture, repository, session, now := openGameSessionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
	defer cancel()
	owner, err := session.AcquireOwnership(0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	owner, err = repository.AcquireOwnershipCAS(ctx, session, owner)
	if err != nil {
		t.Fatal(err)
	}
	roomRepository := NewRoomRepository(fixture.Pool)
	room, err := roomRepository.GetByID(ctx, owner.Snapshot().RoomID)
	if err != nil {
		t.Fatal(err)
	}
	nextRoom, err := room.FinishSession(owner.Snapshot().ID, room.Version(), now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Pool.Exec(ctx, `
		CREATE FUNCTION reject_game_test_room_finish() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'injected room finish failure';
		END;
		$$;
		CREATE TRIGGER reject_game_test_room_finish
		BEFORE UPDATE ON party_rooms
		FOR EACH ROW WHEN (OLD.status = 'playing' AND NEW.status = 'post_game')
		EXECUTE FUNCTION reject_game_test_room_finish();
	`); err != nil {
		t.Fatal(err)
	}
	finish := buildGameFinishedActionCommit(t, owner, now.Add(2*time.Second))
	if _, _, err := NewRoomGameSessionRepository(fixture.Pool).FinishAction(ctx, room, nextRoom, finish); err == nil {
		t.Fatal("expected injected room finish failure")
	}
	if _, err := fixture.Pool.Exec(ctx, `
		DROP TRIGGER reject_game_test_room_finish ON party_rooms;
		DROP FUNCTION reject_game_test_room_finish();
	`); err != nil {
		t.Fatal(err)
	}
	storedRoom, err := roomRepository.GetByID(ctx, room.Snapshot().ID)
	if err != nil {
		t.Fatal(err)
	}
	storedSession, err := repository.Get(ctx, owner.Snapshot().ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedRoom.Snapshot().Status != roomDomain.RoomStatusPlaying || storedRoom.Snapshot().ActiveSessionID != owner.Snapshot().ID ||
		storedSession.Snapshot().Status != gameruntime.StatusActive || storedSession.Snapshot().State.StateVersion != 1 {
		t.Fatalf("room=%+v session=%+v", storedRoom.Snapshot(), storedSession.Snapshot())
	}
	assertGameSessionCounts(t, ctx, fixture, owner.Snapshot().ID, 1, 0, 1, 1)
}

func TestGameSessionRepositoryRemovalLockedBeforeActionRejectsRemovedParticipant(t *testing.T) {
	fixture, repository, session, now := openGameSessionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
	defer cancel()
	owner, err := session.AcquireOwnership(0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	owner, err = repository.AcquireOwnershipCAS(ctx, session, owner)
	if err != nil {
		t.Fatal(err)
	}
	hostID, playerID := owner.Snapshot().Participants[0].UserID, owner.Snapshot().Participants[1].UserID
	action := buildGameActionCommitForActor(t, owner, playerID, now.Add(2*time.Second), 24, []byte("racing-action"), digestForGameTest("racing-action"))
	roomRepository := NewRoomRepository(fixture.Pool)
	room, err := roomRepository.GetByID(ctx, owner.Snapshot().RoomID)
	if err != nil {
		t.Fatal(err)
	}
	removedRoom, _, err := room.RemoveMember(hostID, playerID, room.Version(), now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	removalTransaction, err := fixture.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Rollback releases the room lock on every early test exit and is a no-op after the explicit commit.
	t.Cleanup(func() { _ = removalTransaction.Rollback(context.Background()) })
	if _, err := updateRoomAggregateCAS(ctx, sqlcgen.New(removalTransaction), room.Snapshot(), removedRoom.Snapshot()); err != nil {
		t.Fatal(err)
	}

	actionStarted := make(chan struct{})
	actionResult := make(chan error, 1)
	go func() {
		close(actionStarted)
		_, err := repository.CommitAction(ctx, action)
		actionResult <- err
	}()
	<-actionStarted
	if err := removalTransaction.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	actionErr := <-actionResult
	if !errors.Is(actionErr, gameruntime.ErrParticipantNotActive) {
		t.Fatalf("action error = %v", actionErr)
	}
	storedRoom, err := roomRepository.GetByID(ctx, room.Snapshot().ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := storedRoom.Member(playerID); found {
		t.Fatalf("removed player remains in room: %+v", storedRoom.Snapshot())
	}
	storedSession, err := repository.Get(ctx, owner.Snapshot().ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedSession.Snapshot().State.StateVersion != 1 {
		t.Fatalf("session version = %d, want 1", storedSession.Snapshot().State.StateVersion)
	}
}

func TestGameSessionRepositoryActionLockedBeforeTimerCommitsOneVersion(t *testing.T) {
	fixture, repository, session, now := openGameSessionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
	defer cancel()
	owner, err := session.AcquireOwnership(0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	owner, err = repository.AcquireOwnershipCAS(ctx, session, owner)
	if err != nil {
		t.Fatal(err)
	}
	action := buildGameActionCommit(t, owner, now.Add(31*time.Second), 25, []byte("action-winner"))
	timer := buildGameTimerCommit(t, owner, now.Add(31*time.Second))
	actionTransaction, err := fixture.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Rollback releases both room and session locks on early failure and is a no-op after commit.
	t.Cleanup(func() { _ = actionTransaction.Rollback(context.Background()) })
	actionQueries := sqlcgen.New(actionTransaction)
	actorUserID := action.Receipt().Snapshot().Key.ActorUserID
	if err := lockActionParticipantFence(ctx, actionQueries, owner.Snapshot(), actorUserID); err != nil {
		t.Fatal(err)
	}
	actionResult, err := commitActionAfterRoomLock(ctx, actionQueries, action)
	if err != nil {
		t.Fatal(err)
	}
	timerStarted := make(chan struct{})
	timerResult := make(chan error, 1)
	go func() {
		close(timerStarted)
		_, timerErr := repository.CommitTimer(ctx, timer)
		timerResult <- timerErr
	}()
	<-timerStarted
	if err := actionTransaction.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if timerErr := <-timerResult; !errors.Is(timerErr, gameruntime.ErrStateVersionConflict) {
		t.Fatalf("timer error = %v", timerErr)
	}
	if actionResult.Replayed || actionResult.Session.Snapshot().State.StateVersion != 2 {
		t.Fatalf("action result = %+v", actionResult)
	}
	stored, err := repository.Get(ctx, owner.Snapshot().ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Snapshot().State.StateVersion != 2 {
		t.Fatalf("stored session = %+v", stored.Snapshot())
	}
	var actionReceipts, timerReceipts int
	if err := fixture.Pool.QueryRow(ctx, "SELECT count(*) FROM game_action_receipts WHERE session_id = $1", owner.Snapshot().ID).Scan(&actionReceipts); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Pool.QueryRow(ctx, "SELECT count(*) FROM game_timer_receipts WHERE session_id = $1", owner.Snapshot().ID).Scan(&timerReceipts); err != nil {
		t.Fatal(err)
	}
	if actionReceipts != 1 || timerReceipts != 0 {
		t.Fatalf("action receipts=%d timer receipts=%d", actionReceipts, timerReceipts)
	}
	assertGameSessionCounts(t, ctx, fixture, owner.Snapshot().ID, 2, actionReceipts, 2, 2)
}

func TestRoomRepositoryCommitRemovalPersistsVerifiableSystemInbox(t *testing.T) {
	fixture, repository, session, now := openGameSessionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
	defer cancel()
	_, event, _ := commitParticipantRemovalForTest(t, ctx, fixture, session, now.Add(time.Second))
	snapshot := event.Snapshot()
	digest := idempotency.Digest(sha256.Sum256(snapshot.Payload))
	key := gameruntime.SystemInboxKey{SessionID: session.Snapshot().ID, SourceEventID: snapshot.ID}

	record, err := repository.GetSystemInbox(ctx, key, digest)
	if err != nil {
		t.Fatal(err)
	}
	if record.Snapshot().Status != gameruntime.SystemInboxPending || record.Snapshot().EventType != roomDomain.ParticipantRevokedEventType ||
		record.Snapshot().PayloadDigest != digest || !record.Snapshot().CreatedAt.Equal(snapshot.CreatedAt) {
		t.Fatalf("inbox=%+v", record.Snapshot())
	}
	if _, err := repository.GetSystemInbox(ctx, key, digestForGameTest("different-revocation")); !errors.Is(err, idempotency.ErrConflict) {
		t.Fatalf("digest conflict error=%v", err)
	}
	completed, err := repository.CompleteSystemInbox(ctx, key, digest, 2, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := repository.CompleteSystemInbox(ctx, key, digest, 2, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if completed.Snapshot() != replayed.Snapshot() || replayed.Snapshot().Status != gameruntime.SystemInboxCompleted {
		t.Fatalf("completed=%+v replayed=%+v", completed.Snapshot(), replayed.Snapshot())
	}
	if _, err := repository.CompleteSystemInbox(ctx, key, digest, 3, now.Add(3*time.Second)); !errors.Is(err, gameruntime.ErrGameSessionIntegrity) {
		t.Fatalf("completed state conflict error=%v", err)
	}
}

func TestRoomRepositoryCommitRemovalRollsBackEveryAtomicWriteOnFailure(t *testing.T) {
	t.Run("outbox insert", func(t *testing.T) {
		fixture, _, session, now := openGameSessionFixture(t)
		ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
		defer cancel()
		roomRepository := NewRoomRepository(fixture.Pool)
		room, next, event, removedUserID := prepareParticipantRemovalForTest(t, ctx, fixture, session, now.Add(time.Second))
		snapshot := event.Snapshot()
		if _, err := fixture.Pool.Exec(ctx, `
			INSERT INTO outbox_events (event_id, event_type, aggregate_type, aggregate_id, payload, created_at, available_at)
			VALUES ($1, $2, $3, $4, $5, $6, $6)
		`, snapshot.ID, snapshot.Type.Value(), snapshot.AggregateType.Value(), snapshot.AggregateID, []byte("conflict"), snapshot.CreatedAt); err != nil {
			t.Fatal(err)
		}
		if _, err := roomRepository.CommitRemoval(ctx, room, next, event); err == nil {
			t.Fatal("expected outbox insert failure")
		}
		assertParticipantRemovalRolledBack(t, ctx, fixture, room, removedUserID, snapshot.ID, 1)
	})

	t.Run("system inbox insert", func(t *testing.T) {
		fixture, _, session, now := openGameSessionFixture(t)
		ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
		defer cancel()
		roomRepository := NewRoomRepository(fixture.Pool)
		room, next, event, removedUserID := prepareParticipantRemovalForTest(t, ctx, fixture, session, now.Add(time.Second))
		if _, err := fixture.Pool.Exec(ctx, `
			CREATE FUNCTION reject_game_system_inbox_test_insert() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
				RAISE EXCEPTION 'injected game system inbox failure';
			END;
			$$;
			CREATE TRIGGER reject_game_system_inbox_test_insert
			BEFORE INSERT ON game_system_inbox
			FOR EACH ROW EXECUTE FUNCTION reject_game_system_inbox_test_insert();
		`); err != nil {
			t.Fatal(err)
		}
		if _, err := roomRepository.CommitRemoval(ctx, room, next, event); err == nil {
			t.Fatal("expected system inbox insert failure")
		}
		assertParticipantRemovalRolledBack(t, ctx, fixture, room, removedUserID, event.Snapshot().ID, 0)
	})
}

func TestPendingParticipantRevocationFencesOnlyTerminalTransitions(t *testing.T) {
	t.Run("action", func(t *testing.T) {
		fixture, repository, session, now := openGameSessionFixture(t)
		ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
		defer cancel()
		owner := acquireGameSessionForTest(t, ctx, repository, session, now.Add(time.Second))
		removedRoom, _, _ := commitParticipantRemovalForTest(t, ctx, fixture, owner, now.Add(2*time.Second))

		ordinary := buildGameActionCommit(t, owner, now.Add(3*time.Second), 51, []byte("host-continues"))
		ordinaryResult, err := repository.CommitAction(ctx, ordinary)
		if err != nil {
			t.Fatalf("non-terminal action: %v", err)
		}
		finish := buildGameFinishedActionCommit(t, ordinaryResult.Session, now.Add(4*time.Second))
		nextRoom, err := removedRoom.FinishSession(owner.Snapshot().ID, removedRoom.Version(), now.Add(4*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := NewRoomGameSessionRepository(fixture.Pool).FinishAction(ctx, removedRoom, nextRoom, finish); !errors.Is(err, gameruntime.ErrSystemOperationPending) {
			t.Fatalf("terminal action error=%v", err)
		}
		assertRoomAndSessionRemainPlaying(t, ctx, fixture, owner.Snapshot().RoomID, owner.Snapshot().ID, 2)
	})

	t.Run("timer", func(t *testing.T) {
		fixture, repository, session, now := openGameSessionFixture(t)
		ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
		defer cancel()
		owner := acquireGameSessionForTest(t, ctx, repository, session, now.Add(time.Second))
		removedRoom, _, _ := commitParticipantRemovalForTest(t, ctx, fixture, owner, now.Add(2*time.Second))
		finish := buildGameFinishedTimerCommit(t, owner, now.Add(31*time.Second))
		nextRoom, err := removedRoom.FinishSession(owner.Snapshot().ID, removedRoom.Version(), now.Add(31*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := NewRoomGameSessionRepository(fixture.Pool).FinishTimer(ctx, removedRoom, nextRoom, finish); !errors.Is(err, gameruntime.ErrSystemOperationPending) {
			t.Fatalf("terminal timer error=%v", err)
		}
		assertRoomAndSessionRemainPlaying(t, ctx, fixture, owner.Snapshot().RoomID, owner.Snapshot().ID, 1)
	})

	t.Run("different system source", func(t *testing.T) {
		fixture, repository, session, now := openGameSessionFixture(t)
		ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
		defer cancel()
		owner := acquireGameSessionForTest(t, ctx, repository, session, now.Add(time.Second))
		removedRoom, _, _ := commitParticipantRemovalForTest(t, ctx, fixture, owner, now.Add(2*time.Second))
		source := gameruntime.SystemSource{Kind: gameruntime.SystemSourceRoomOutbox, EventID: uuid.New()}
		finish := buildGameFinishedSystemCommit(
			t, owner, gameSessionOperationID(t, 52), source, digestForGameTest("different-system-source"), now.Add(3*time.Second),
		)
		nextRoom, err := removedRoom.FinishSession(owner.Snapshot().ID, removedRoom.Version(), now.Add(3*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := NewRoomGameSessionRepository(fixture.Pool).FinishSystem(ctx, removedRoom, nextRoom, uuid.Nil, finish); !errors.Is(err, gameruntime.ErrSystemOperationPending) {
			t.Fatalf("terminal system error=%v", err)
		}
		assertRoomAndSessionRemainPlaying(t, ctx, fixture, owner.Snapshot().RoomID, owner.Snapshot().ID, 1)
	})

	t.Run("matching revocation source", func(t *testing.T) {
		fixture, repository, session, now := openGameSessionFixture(t)
		ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
		defer cancel()
		owner := acquireGameSessionForTest(t, ctx, repository, session, now.Add(time.Second))
		removedRoom, event, _ := commitParticipantRemovalForTest(t, ctx, fixture, owner, now.Add(2*time.Second))
		source := gameruntime.SystemSource{Kind: gameruntime.SystemSourceRoomOutbox, EventID: event.Snapshot().ID}
		finish := buildGameFinishedSystemCommit(
			t, owner, gameSessionOperationID(t, 53), source, digestForGameTest("matching-revocation-source"), now.Add(3*time.Second),
		)
		nextRoom, err := removedRoom.FinishSession(owner.Snapshot().ID, removedRoom.Version(), now.Add(3*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		storedRoom, result, err := NewRoomGameSessionRepository(fixture.Pool).FinishSystem(
			ctx, removedRoom, nextRoom, uuid.Nil, finish,
		)
		if err != nil {
			t.Fatal(err)
		}
		if storedRoom.Snapshot().Status != roomDomain.RoomStatusPostGame || result.Session.Snapshot().Status != gameruntime.StatusFinished {
			t.Fatalf("room=%+v result=%+v", storedRoom.Snapshot(), result)
		}
	})
}

func openGameSessionFixture(t *testing.T) (*integrationtest.PostgresSchema, *GameSessionRepository, gameruntime.Session, time.Time) {
	return openGameSessionFixtureWithVisibility(t, roomDomain.VisibilityPrivate)
}

func openGameSessionFixtureWithVisibility(
	t *testing.T,
	visibility roomDomain.Visibility,
) (*integrationtest.PostgresSchema, *GameSessionRepository, gameruntime.Session, time.Time) {
	t.Helper()
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	now := databaseIntegrationTime(t, ctx, fixture)
	hostID, playerID := uuid.New(), uuid.New()
	createRoomTestUser(t, ctx, fixture, hostID, "GameHost1", now)
	createRoomTestUser(t, ctx, fixture, playerID, "GamePlayer2", now)
	room, err := roomDomain.New(uuid.New(), hostID, "GAMEROOM1", visibility, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	roomRepository := NewRoomRepository(fixture.Pool)
	room, err = roomRepository.Create(ctx, room)
	if err != nil {
		t.Fatal(err)
	}
	joined, _, err := room.Join(playerID, roomDomain.JoinIntentParticipant, room.Version(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	room, err = roomRepository.UpdateCAS(ctx, room, joined)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := uuid.New()
	playing, start, err := room.StartSession(hostID, sessionID, "dice", 2, 9, room.Version(), now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	request := gameSessionCreateRequest(sessionID, room.Snapshot().ID, hostID, playerID, start.StartedAt)
	session, batch, err := gameruntime.NewSession(request)
	if err != nil {
		t.Fatal(err)
	}
	createdEvent := newGameSessionOutboxEvent(t, gameruntime.GameSessionCreatedEventType, session.Snapshot().ID, uuid.New(), start.StartedAt, []byte("created"))
	commit := gameruntime.CreationCommit{Session: session, Batch: batch, OutboxEvents: []outbox.Event{createdEvent}}
	_, session, _, err = NewRoomGameSessionRepository(fixture.Pool).Start(
		ctx, room, playing, commit, gameSessionStartReceiptForTest(t, room, commit, "game-session-fixture"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, NewGameSessionRepository(fixture.Pool), session, start.StartedAt
}

func acquireGameSessionForTest(
	t testing.TB,
	ctx context.Context,
	repository *GameSessionRepository,
	session gameruntime.Session,
	at time.Time,
) gameruntime.Session {
	t.Helper()
	owned, err := session.AcquireOwnership(session.Snapshot().OwnershipEpoch, at)
	if err != nil {
		t.Fatal(err)
	}
	owned, err = repository.AcquireOwnershipCAS(ctx, session, owned)
	if err != nil {
		t.Fatal(err)
	}
	return owned
}

func prepareParticipantRemovalForTest(
	t testing.TB,
	ctx context.Context,
	fixture *integrationtest.PostgresSchema,
	session gameruntime.Session,
	at time.Time,
) (roomDomain.Room, roomDomain.Room, outbox.Event, uuid.UUID) {
	t.Helper()
	roomRepository := NewRoomRepository(fixture.Pool)
	room, err := roomRepository.GetByID(ctx, session.Snapshot().RoomID)
	if err != nil {
		t.Fatal(err)
	}
	hostID, removedUserID := session.Snapshot().Participants[0].UserID, session.Snapshot().Participants[1].UserID
	next, result, err := room.RemoveMember(hostID, removedUserID, room.Version(), at)
	if err != nil {
		t.Fatal(err)
	}
	event, err := roomDomain.NewParticipantRevokedEvent(roomDomain.ParticipantRevocationFact{
		EventID: uuid.New(), RoomID: room.Snapshot().ID, SessionID: result.SessionID, UserID: removedUserID,
		ActorKind: roomDomain.RemovalActorHost, ActorID: hostID, Reason: roomDomain.RemovalReasonHostRemoved,
		MembershipVersion: next.Version().Membership, OccurredAt: next.Snapshot().UpdatedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return room, next, event, removedUserID
}

func commitParticipantRemovalForTest(
	t testing.TB,
	ctx context.Context,
	fixture *integrationtest.PostgresSchema,
	session gameruntime.Session,
	at time.Time,
) (roomDomain.Room, outbox.Event, uuid.UUID) {
	t.Helper()
	room, next, event, removedUserID := prepareParticipantRemovalForTest(t, ctx, fixture, session, at)
	stored, err := NewRoomRepository(fixture.Pool).CommitRemoval(ctx, room, next, event)
	if err != nil {
		t.Fatal(err)
	}
	return stored, event, removedUserID
}

func assertParticipantRemovalRolledBack(
	t testing.TB,
	ctx context.Context,
	fixture *integrationtest.PostgresSchema,
	before roomDomain.Room,
	removedUserID uuid.UUID,
	eventID uuid.UUID,
	wantOutboxCount int,
) {
	t.Helper()
	stored, err := NewRoomRepository(fixture.Pool).GetByID(ctx, before.Snapshot().ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Version() != before.Version() {
		t.Fatalf("stored version=%+v want=%+v", stored.Version(), before.Version())
	}
	if _, found := stored.Member(removedUserID); !found {
		t.Fatalf("participant %s was removed despite rollback", removedUserID)
	}
	var inboxCount int
	if err := fixture.Pool.QueryRow(ctx, "SELECT count(*) FROM game_system_inbox WHERE source_event_id = $1", eventID).Scan(&inboxCount); err != nil {
		t.Fatal(err)
	}
	if inboxCount != 0 {
		t.Fatalf("system inbox rows=%d, want 0", inboxCount)
	}
	var outboxCount int
	if err := fixture.Pool.QueryRow(ctx, "SELECT count(*) FROM outbox_events WHERE event_id = $1", eventID).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if outboxCount != wantOutboxCount {
		t.Fatalf("outbox rows=%d, want %d", outboxCount, wantOutboxCount)
	}
}

func assertRoomAndSessionRemainPlaying(
	t testing.TB,
	ctx context.Context,
	fixture *integrationtest.PostgresSchema,
	roomID uuid.UUID,
	sessionID uuid.UUID,
	stateVersion uint64,
) {
	t.Helper()
	room, err := NewRoomRepository(fixture.Pool).GetByID(ctx, roomID)
	if err != nil {
		t.Fatal(err)
	}
	session, err := NewGameSessionRepository(fixture.Pool).Get(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if room.Snapshot().Status != roomDomain.RoomStatusPlaying || room.Snapshot().ActiveSessionID != sessionID ||
		session.Snapshot().Status != gameruntime.StatusActive || session.Snapshot().State.StateVersion != stateVersion {
		t.Fatalf("room=%+v session=%+v", room.Snapshot(), session.Snapshot())
	}
}

func gameSessionCreateRequest(sessionID, roomID, firstUser, secondUser uuid.UUID, now time.Time) gameruntime.CreateRequest {
	return gameruntime.CreateRequest{
		SessionID: sessionID, RoomID: roomID,
		VersionKey:   game.VersionKey{GameID: "dice", Engine: "1.2.3", Protocol: "2.3.4", Client: "3.4.5"},
		Participants: []gameruntime.Participant{{UserID: firstUser, SeatIndex: 0}, {UserID: secondUser, SeatIndex: 1}},
		BatchID:      uuid.New(), Execution: gameSessionExecution(now), Input: gameSessionMessage("round.config", []byte("config")),
		Transition: gameSessionTransition(1, false, gameSessionTimer(now.Add(30*time.Second))),
	}
}

func buildGameActionCommit(t testing.TB, before gameruntime.Session, now time.Time, marker byte, outboxPayload []byte) gameruntime.ActionCommit {
	return buildGameActionCommitWithDigest(t, before, now, marker, outboxPayload, digestForGameTest("same-request"))
}

func buildGameActionCommitWithDigest(
	t testing.TB,
	before gameruntime.Session,
	now time.Time,
	marker byte,
	outboxPayload []byte,
	requestDigest idempotency.Digest,
) gameruntime.ActionCommit {
	return buildGameActionCommitForActor(t, before, before.Snapshot().Participants[0].UserID, now, marker, outboxPayload, requestDigest)
}

func buildGameActionCommitForActor(
	t testing.TB,
	before gameruntime.Session,
	actorUserID uuid.UUID,
	now time.Time,
	marker byte,
	outboxPayload []byte,
	requestDigest idempotency.Digest,
) gameruntime.ActionCommit {
	t.Helper()
	actionID := gameSessionOperationID(t, 7)
	action := gameruntime.ActionTransitionRequest{
		BatchID: uuid.New(), OwnershipEpoch: before.Snapshot().OwnershipEpoch, ActorUserID: actorUserID,
		ActionID: actionID, Execution: gameSessionExecution(now), Input: gameSessionMessage("round.roll", []byte{marker}),
		Transition: gameSessionTransition(before.Snapshot().State.StateVersion+1, false),
	}
	after, batch, err := before.ApplyAction(action)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := gameruntime.NewActionReceipt(gameruntime.ActionReceiptSnapshot{
		Key:           gameruntime.ActionKey{SessionID: before.Snapshot().ID, ActorUserID: action.ActorUserID, ActionID: actionID},
		RequestDigest: requestDigest, ResultCode: gameruntime.ResultCodeAccepted,
		ResultDigest: digestForGameTest("safe-result"), StateVersion: after.Snapshot().State.StateVersion, CommittedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	event := newGameSessionOutboxEvent(t, gameruntime.GameSessionTransitionedEventType, before.Snapshot().ID, uuid.New(), now, outboxPayload)
	commit, err := gameruntime.NewActionCommit(before, after, batch, receipt, []outbox.Event{event})
	if err != nil {
		t.Fatal(err)
	}
	return commit
}

func buildGameTimerCommit(t testing.TB, before gameruntime.Session, firedAt time.Time) gameruntime.TimerCommit {
	t.Helper()
	timer := before.Snapshot().Timers[0]
	after, batch, err := before.ApplyTimer(gameruntime.TimerTransitionRequest{
		BatchID: uuid.New(), OwnershipEpoch: before.Snapshot().OwnershipEpoch, TimerID: timer.TimerID,
		ExpectedStateVersion: timer.ExpectedStateVersion, Execution: gameSessionExecution(firedAt),
		Input: timer.Message, Transition: gameSessionTransition(before.Snapshot().State.StateVersion+1, false),
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := gameruntime.NewTimerReceipt(gameruntime.TimerReceiptSnapshot{
		Key:        gameruntime.TimerKey{SessionID: before.Snapshot().ID, TimerID: timer.TimerID, ExpectedStateVersion: timer.ExpectedStateVersion},
		ResultCode: gameruntime.ResultCodeAccepted, ResultDigest: digestForGameTest("timer-result"),
		StateVersion: after.Snapshot().State.StateVersion, CommittedAt: firedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	event := newGameSessionOutboxEvent(t, gameruntime.GameSessionTransitionedEventType, before.Snapshot().ID, uuid.New(), firedAt, []byte("timer"))
	commit, err := gameruntime.NewTimerCommit(before, after, batch, receipt, []outbox.Event{event})
	if err != nil {
		t.Fatal(err)
	}
	return commit
}

func buildGameFinishedTimerCommit(t testing.TB, before gameruntime.Session, firedAt time.Time) gameruntime.TimerCommit {
	t.Helper()
	timer := before.Snapshot().Timers[0]
	after, batch, err := before.ApplyTimer(gameruntime.TimerTransitionRequest{
		BatchID: uuid.New(), OwnershipEpoch: before.Snapshot().OwnershipEpoch, TimerID: timer.TimerID,
		ExpectedStateVersion: timer.ExpectedStateVersion, Execution: gameSessionExecution(firedAt),
		Input: timer.Message, Transition: gameSessionTransition(before.Snapshot().State.StateVersion+1, true),
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := gameruntime.NewTimerReceipt(gameruntime.TimerReceiptSnapshot{
		Key:        gameruntime.TimerKey{SessionID: before.Snapshot().ID, TimerID: timer.TimerID, ExpectedStateVersion: timer.ExpectedStateVersion},
		ResultCode: gameruntime.ResultCodeAccepted, ResultDigest: digestForGameTest("finished-timer-result"),
		StateVersion: after.Snapshot().State.StateVersion, CommittedAt: firedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	event := newGameSessionOutboxEvent(t, gameruntime.GameSessionTransitionedEventType, before.Snapshot().ID, uuid.New(), firedAt, []byte("timer-finished"))
	commit, err := gameruntime.NewTimerCommit(before, after, batch, receipt, []outbox.Event{event})
	if err != nil {
		t.Fatal(err)
	}
	return commit
}

func buildGameSystemCommit(
	t testing.TB,
	before gameruntime.Session,
	operationID idempotency.OperationID,
	source gameruntime.SystemSource,
	requestDigest idempotency.Digest,
	at time.Time,
) gameruntime.SystemCommit {
	t.Helper()
	after, batch, err := before.ApplySystem(gameruntime.SystemTransitionRequest{
		BatchID: uuid.New(), OwnershipEpoch: before.Snapshot().OwnershipEpoch,
		ExpectedStateVersion: before.Snapshot().State.StateVersion, SystemOperationID: operationID,
		Source: source, RequestDigest: requestDigest, Execution: gameSessionExecution(at),
		Input:      gameSessionMessage("participant.revoked", []byte("user")),
		Transition: gameSessionTransition(before.Snapshot().State.StateVersion+1, false),
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := gameruntime.NewSystemReceipt(gameruntime.SystemReceiptSnapshot{
		Key:           gameruntime.SystemKey{SessionID: before.Snapshot().ID, OperationID: operationID, Source: source},
		RequestDigest: requestDigest, ResultCode: gameruntime.ResultCodeAccepted,
		ResultDigest: digestForGameTest("system-result"), StateVersion: after.Snapshot().State.StateVersion, CommittedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	event := newGameSessionOutboxEvent(t, gameruntime.GameSessionTransitionedEventType, before.Snapshot().ID, uuid.New(), at, []byte("system"))
	commit, err := gameruntime.NewSystemCommit(before, after, batch, receipt, []outbox.Event{event})
	if err != nil {
		t.Fatal(err)
	}
	return commit
}

func buildGameFinishedSystemCommit(
	t testing.TB,
	before gameruntime.Session,
	operationID idempotency.OperationID,
	source gameruntime.SystemSource,
	requestDigest idempotency.Digest,
	at time.Time,
) gameruntime.SystemCommit {
	t.Helper()
	after, batch, err := before.ApplySystem(gameruntime.SystemTransitionRequest{
		BatchID: uuid.New(), OwnershipEpoch: before.Snapshot().OwnershipEpoch,
		ExpectedStateVersion: before.Snapshot().State.StateVersion, SystemOperationID: operationID,
		Source: source, RequestDigest: requestDigest, Execution: gameSessionExecution(at),
		Input: gameSessionMessage("session.finish", nil), Transition: gameSessionTransition(before.Snapshot().State.StateVersion+1, true),
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := gameruntime.NewSystemReceipt(gameruntime.SystemReceiptSnapshot{
		Key:           gameruntime.SystemKey{SessionID: before.Snapshot().ID, OperationID: operationID, Source: source},
		RequestDigest: requestDigest, ResultCode: gameruntime.ResultCodeAccepted,
		ResultDigest: digestForGameTest("finished-system-result"), StateVersion: after.Snapshot().State.StateVersion, CommittedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	event := newGameSessionOutboxEvent(t, gameruntime.GameSessionTransitionedEventType, before.Snapshot().ID, uuid.New(), at, []byte("system-finished"))
	commit, err := gameruntime.NewSystemCommit(before, after, batch, receipt, []outbox.Event{event})
	if err != nil {
		t.Fatal(err)
	}
	return commit
}

func buildGameFinishedActionCommit(t testing.TB, before gameruntime.Session, at time.Time) gameruntime.ActionCommit {
	t.Helper()
	actionID := gameSessionOperationID(t, 13)
	action := gameruntime.ActionTransitionRequest{
		BatchID: uuid.New(), OwnershipEpoch: before.Snapshot().OwnershipEpoch,
		ActorUserID: before.Snapshot().Participants[0].UserID, ActionID: actionID,
		Execution: gameSessionExecution(at), Input: gameSessionMessage("session.finish", nil),
		Transition: gameSessionTransition(before.Snapshot().State.StateVersion+1, true),
	}
	after, batch, err := before.ApplyAction(action)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := gameruntime.NewActionReceipt(gameruntime.ActionReceiptSnapshot{
		Key:           gameruntime.ActionKey{SessionID: before.Snapshot().ID, ActorUserID: action.ActorUserID, ActionID: actionID},
		RequestDigest: digestForGameTest("finish-request"), ResultCode: gameruntime.ResultCodeAccepted,
		ResultDigest: digestForGameTest("finish-result"), StateVersion: after.Snapshot().State.StateVersion, CommittedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	event := newGameSessionOutboxEvent(t, gameruntime.GameSessionTransitionedEventType, before.Snapshot().ID, uuid.New(), at, []byte("finished"))
	commit, err := gameruntime.NewActionCommit(before, after, batch, receipt, []outbox.Event{event})
	if err != nil {
		t.Fatal(err)
	}
	return commit
}

func newGameLifecycleCommit(
	t testing.TB,
	before gameruntime.Session,
	after gameruntime.Session,
	eventType outbox.EventType,
) gameruntime.LifecycleCommit {
	t.Helper()
	event := newGameSessionOutboxEvent(
		t, eventType, after.Snapshot().ID, uuid.New(), after.Snapshot().UpdatedAt, []byte(eventType.Value()),
	)
	commit, err := gameruntime.NewLifecycleCommit(before, after, []outbox.Event{event})
	if err != nil {
		t.Fatal(err)
	}
	return commit
}

func newGameSessionOutboxEvent(t testing.TB, eventType outbox.EventType, aggregateID, eventID uuid.UUID, at time.Time, payload []byte) outbox.Event {
	t.Helper()
	event, err := outbox.NewEvent(eventID, eventType, gameruntime.GameSessionAggregateType, aggregateID, payload, at, at)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func gameSessionExecution(now time.Time) game.DeterministicContext {
	var seed [game.RandomSeedBytes]byte
	seed[0] = 9
	return game.DeterministicContext{Now: now, RandomSeed: seed, AllocatedIDs: []game.Identifier{"allocated-1"}}
}

func gameSessionMessage(messageType game.Identifier, payload []byte) game.Message {
	return game.Message{MessageType: messageType, SchemaVersion: 1, Payload: append([]byte(nil), payload...)}
}

func gameSessionTransition(stateVersion uint64, finished bool, timers ...game.TimerIntent) game.Transition {
	return game.Transition{
		Snapshot: game.Snapshot{SnapshotVersion: 1, StateVersion: stateVersion, State: gameSessionMessage("round.state", []byte{byte(stateVersion), 1})},
		Events:   []game.Event{{Message: gameSessionMessage("round.changed", []byte{byte(stateVersion), 2})}}, Timers: timers, Finished: finished,
	}
}

func gameSessionTimer(dueAt time.Time) game.TimerIntent {
	return game.TimerIntent{TimerID: "turn.timeout", DueAt: dueAt, Message: gameSessionMessage("round.timer", []byte("timer"))}
}

func gameSessionOperationID(t testing.TB, marker byte) idempotency.OperationID {
	t.Helper()
	value := make([]byte, 16)
	for index := range value {
		value[index] = marker
	}
	operationID, err := idempotency.NewOperationID(value)
	if err != nil {
		t.Fatal(err)
	}
	return operationID
}

func digestForGameTest(value string) idempotency.Digest {
	return idempotency.Digest(sha256.Sum256([]byte(value)))
}

func assertGameSessionCounts(t testing.TB, ctx context.Context, fixture *integrationtest.PostgresSchema, sessionID uuid.UUID, batches, receipts, sessionEvents, outboxEvents int) {
	t.Helper()
	queries := []struct {
		name      string
		statement string
		want      int
	}{
		{name: "batches", statement: "SELECT count(*) FROM game_session_event_batches WHERE session_id = $1", want: batches},
		{name: "receipts", statement: "SELECT count(*) FROM game_action_receipts WHERE session_id = $1", want: receipts},
		{name: "events", statement: "SELECT count(*) FROM game_session_events AS event JOIN game_session_event_batches AS batch USING (batch_id) WHERE batch.session_id = $1", want: sessionEvents},
	}
	for _, query := range queries {
		var count int
		if err := fixture.Pool.QueryRow(ctx, query.statement, sessionID).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", query.name, err)
		}
		if count != query.want {
			t.Fatalf("%s = %d, want %d", query.name, count, query.want)
		}
	}
	var count int
	if err := fixture.Pool.QueryRow(ctx, "SELECT count(*) FROM outbox_events WHERE aggregate_id = $1", sessionID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != outboxEvents {
		t.Fatalf("outbox events = %d, want %d", count, outboxEvents)
	}
}
