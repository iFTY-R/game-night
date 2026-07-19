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

func openGameSessionFixture(t *testing.T) (*integrationtest.PostgresSchema, *GameSessionRepository, gameruntime.Session, time.Time) {
	t.Helper()
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	now := databaseIntegrationTime(t, ctx, fixture)
	hostID, playerID := uuid.New(), uuid.New()
	createRoomTestUser(t, ctx, fixture, hostID, "GameHost1", now)
	createRoomTestUser(t, ctx, fixture, playerID, "GamePlayer2", now)
	room, err := roomDomain.New(uuid.New(), hostID, "GAMEROOM1", roomDomain.VisibilityPrivate, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRoomRepository(fixture.Pool).Create(ctx, room); err != nil {
		t.Fatal(err)
	}
	request := gameSessionCreateRequest(uuid.New(), room.Snapshot().ID, hostID, playerID, now)
	session, batch, err := gameruntime.NewSession(request)
	if err != nil {
		t.Fatal(err)
	}
	createdEvent := newGameSessionOutboxEvent(t, gameruntime.GameSessionCreatedEventType, session.Snapshot().ID, uuid.New(), now, []byte("created"))
	repository := NewGameSessionRepository(fixture.Pool)
	if _, err := repository.Create(ctx, gameruntime.CreationCommit{Session: session, Batch: batch, OutboxEvents: []outbox.Event{createdEvent}}); err != nil {
		t.Fatal(err)
	}
	return fixture, repository, session, now
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
	t.Helper()
	actionID := gameSessionOperationID(t, 7)
	action := gameruntime.ActionTransitionRequest{
		BatchID: uuid.New(), OwnershipEpoch: before.Snapshot().OwnershipEpoch, ActorUserID: before.Snapshot().Participants[0].UserID,
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
