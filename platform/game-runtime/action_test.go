package gameruntime

import (
	"crypto/sha256"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/outbox"
)

func TestActionReceiptReplaysOnlyTheOriginalRequestDigest(t *testing.T) {
	now := time.Date(2026, time.July, 19, 9, 0, 0, 0, time.UTC)
	requestDigest := idempotency.Digest(sha256.Sum256([]byte("request")))
	resultDigest := idempotency.Digest(sha256.Sum256([]byte("safe-result")))
	receipt, err := NewActionReceipt(ActionReceiptSnapshot{
		Key:           ActionKey{SessionID: uuid.New(), ActorUserID: uuid.New(), ActionID: testRuntimeOperationID(t, 5)},
		RequestDigest: requestDigest, ResultCode: ResultCodeAccepted, ResultDigest: resultDigest,
		StateVersion: 4, CommittedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := receipt.Replay(requestDigest)
	if err != nil || !reflect.DeepEqual(replayed.Snapshot(), receipt.Snapshot()) {
		t.Fatalf("replayed = %+v, err = %v", replayed.Snapshot(), err)
	}
	conflicting := idempotency.Digest(sha256.Sum256([]byte("different")))
	if _, err := receipt.Replay(conflicting); !errors.Is(err, idempotency.ErrConflict) {
		t.Fatalf("digest conflict error = %v", err)
	}
}

func TestSystemSourceBindsRequesterOnlyToHostAPI(t *testing.T) {
	hostID := uuid.New()
	if !(SystemSource{Kind: SystemSourceHostAPI, EventID: uuid.New(), RequestedByUserID: hostID}).Valid() {
		t.Fatal("host API source with an authenticated requester should be valid")
	}
	for _, source := range []SystemSource{
		{Kind: SystemSourceHostAPI, EventID: uuid.New()},
		{Kind: SystemSourceRoomOutbox, EventID: uuid.New(), RequestedByUserID: hostID},
		{Kind: SystemSourcePlatform, EventID: uuid.New(), RequestedByUserID: hostID},
	} {
		if source.Valid() {
			t.Fatalf("invalid requester binding accepted: %+v", source)
		}
	}
}

func TestRestoreEventBatchCanonicalizesPersistedExecutionTime(t *testing.T) {
	now := time.Date(2026, time.July, 19, 9, 0, 0, 0, time.UTC)
	session, _, err := NewSession(testRuntimeCreateRequest(now))
	if err != nil {
		t.Fatal(err)
	}
	session, err = session.AcquireOwnership(0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	_, batch, err := session.ApplyAction(ActionTransitionRequest{
		BatchID: uuid.New(), OwnershipEpoch: session.Snapshot().OwnershipEpoch,
		ActorUserID: session.Snapshot().Participants[0].UserID, ActionID: testRuntimeOperationID(t, 7),
		Execution: testRuntimeExecution(now.Add(2 * time.Second)), Input: testRuntimeMessage("round.roll", nil),
		Transition: testRuntimeTransition(2, false),
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := batch.Snapshot()
	databaseZone := time.FixedZone("database-local", 8*60*60)
	snapshot.Execution.Now = snapshot.Execution.Now.In(databaseZone)
	snapshot.CommittedAt = snapshot.CommittedAt.In(databaseZone)

	restored, err := RestoreEventBatch(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	restoredSnapshot := restored.Snapshot()
	if restoredSnapshot.Execution.Now.Location() != time.UTC || restoredSnapshot.CommittedAt.Location() != time.UTC ||
		!restoredSnapshot.Execution.Now.Equal(now.Add(2*time.Second)) {
		t.Fatalf("restored times = execution %v, committed %v", restoredSnapshot.Execution.Now, restoredSnapshot.CommittedAt)
	}
}

func TestActionCommitRequiresOneCoherentAtomicTransition(t *testing.T) {
	now := time.Date(2026, time.July, 19, 9, 0, 0, 0, time.UTC)
	before, _, err := NewSession(testRuntimeCreateRequest(now))
	if err != nil {
		t.Fatal(err)
	}
	before, err = before.AcquireOwnership(0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	actionID := testRuntimeOperationID(t, 6)
	action := ActionTransitionRequest{
		BatchID: uuid.New(), OwnershipEpoch: 1, ActorUserID: before.Snapshot().Participants[0].UserID,
		ActionID: actionID, Execution: testRuntimeExecution(now.Add(2 * time.Second)),
		Input: testRuntimeMessage("round.roll", []byte("roll")), Transition: testRuntimeTransition(2, false),
	}
	after, batch, err := before.ApplyAction(action)
	if err != nil {
		t.Fatal(err)
	}
	requestDigest := idempotency.Digest(sha256.Sum256([]byte("request")))
	receipt, err := NewActionReceipt(ActionReceiptSnapshot{
		Key:           ActionKey{SessionID: before.Snapshot().ID, ActorUserID: action.ActorUserID, ActionID: actionID},
		RequestDigest: requestDigest, ResultCode: ResultCodeAccepted,
		ResultDigest: idempotency.Digest(sha256.Sum256([]byte("safe-result"))),
		StateVersion: 2, CommittedAt: action.Execution.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	eventType, err := outbox.ParseEventType("game.session.transitioned.v1")
	if err != nil {
		t.Fatal(err)
	}
	aggregateType, err := outbox.ParseAggregateType("game.session")
	if err != nil {
		t.Fatal(err)
	}
	durableEvent, err := outbox.NewEvent(
		uuid.New(), eventType, aggregateType, before.Snapshot().ID, []byte(`{"stateVersion":2}`),
		action.Execution.Now, action.Execution.Now,
	)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := NewActionCommit(before, after, batch, receipt, []outbox.Event{durableEvent})
	if err != nil {
		t.Fatal(err)
	}
	if commit.Before().Snapshot().State.StateVersion != 1 || commit.After().Snapshot().State.StateVersion != 2 ||
		commit.Batch().Snapshot().StateVersion != 2 || commit.Receipt().Snapshot().StateVersion != 2 ||
		len(commit.OutboxEvents()) != 1 {
		t.Fatalf("unexpected commit: before=%+v after=%+v", commit.Before().Snapshot(), commit.After().Snapshot())
	}

	wrongReceipt, err := NewActionReceipt(ActionReceiptSnapshot{
		Key: receipt.Snapshot().Key, RequestDigest: requestDigest, ResultCode: ResultCodeAccepted,
		ResultDigest: receipt.Snapshot().ResultDigest, StateVersion: 3, CommittedAt: action.Execution.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewActionCommit(before, after, batch, wrongReceipt, []outbox.Event{durableEvent}); !errors.Is(err, ErrInvalidActionCommit) {
		t.Fatalf("incoherent receipt error = %v", err)
	}
}

func TestSystemReceiptReplaysOnlyMatchingOperationSourceAndDigest(t *testing.T) {
	now := time.Date(2026, time.July, 19, 13, 0, 0, 0, time.UTC)
	digest := idempotency.Digest(sha256.Sum256([]byte("system-request")))
	receipt, err := NewSystemReceipt(SystemReceiptSnapshot{
		Key: SystemKey{
			SessionID: uuid.New(), OperationID: testRuntimeOperationID(t, 41),
			Source: SystemSource{Kind: SystemSourceRoomOutbox, EventID: uuid.New()},
		},
		RequestDigest: digest, ResultCode: ResultCodeAccepted,
		ResultDigest: idempotency.Digest(sha256.Sum256([]byte("system-result"))),
		StateVersion: 7, CommittedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := receipt.Replay(digest)
	if err != nil || !reflect.DeepEqual(replayed.Snapshot(), receipt.Snapshot()) {
		t.Fatalf("replayed = %+v, err = %v", replayed.Snapshot(), err)
	}
	conflicting := idempotency.Digest(sha256.Sum256([]byte("different-system-request")))
	if _, err := receipt.Replay(conflicting); !errors.Is(err, idempotency.ErrConflict) {
		t.Fatalf("digest conflict error = %v", err)
	}
}

func TestTimerCommitRequiresScheduledTimerReceiptAndActorlessBatch(t *testing.T) {
	now := time.Date(2026, time.July, 19, 14, 0, 0, 0, time.UTC)
	dueAt := now.Add(10 * time.Second)
	create := testRuntimeCreateRequest(now)
	create.Transition = testRuntimeTransition(1, false, testRuntimeTimer("turn.timeout", dueAt))
	before, _, err := NewSession(create)
	if err != nil {
		t.Fatal(err)
	}
	before, err = before.AcquireOwnership(0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	timer := before.Snapshot().Timers[0]
	after, batch, err := before.ApplyTimer(TimerTransitionRequest{
		BatchID: uuid.New(), OwnershipEpoch: 1, TimerID: timer.TimerID,
		ExpectedStateVersion: timer.ExpectedStateVersion, Execution: testRuntimeExecution(dueAt),
		Input: timer.Message, Transition: testRuntimeTransition(2, false),
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := NewTimerReceipt(TimerReceiptSnapshot{
		Key:        TimerKey{SessionID: before.Snapshot().ID, TimerID: timer.TimerID, ExpectedStateVersion: 1},
		ResultCode: ResultCodeAccepted, ResultDigest: idempotency.Digest(sha256.Sum256([]byte("timer-result"))),
		StateVersion: 2, CommittedAt: dueAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	commit, err := NewTimerCommit(before, after, batch, receipt, []outbox.Event{testRuntimeTransitionOutboxEvent(t, before.Snapshot().ID, 2, dueAt)})
	if err != nil || !commit.Valid() {
		t.Fatalf("timer commit valid = %v, err = %v", commit.Valid(), err)
	}
	if commit.Batch().Snapshot().ActorUserID != uuid.Nil || commit.Batch().Snapshot().ActionID.Valid() {
		t.Fatal("timer commit batch impersonated a player action")
	}

	wrongReceipt, err := NewTimerReceipt(TimerReceiptSnapshot{
		Key:        TimerKey{SessionID: before.Snapshot().ID, TimerID: "other.timeout", ExpectedStateVersion: 1},
		ResultCode: ResultCodeAccepted, StateVersion: 2, CommittedAt: dueAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewTimerCommit(before, after, batch, wrongReceipt, commit.OutboxEvents()); !errors.Is(err, ErrInvalidTimerCommit) {
		t.Fatalf("wrong timer receipt error = %v", err)
	}
}

func TestSystemCommitBindsOperationSourceAndDigestToActorlessBatch(t *testing.T) {
	now := time.Date(2026, time.July, 19, 15, 0, 0, 0, time.UTC)
	before, _, err := NewSession(testRuntimeCreateRequest(now))
	if err != nil {
		t.Fatal(err)
	}
	before, err = before.AcquireOwnership(0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	operationID := testRuntimeOperationID(t, 51)
	source := SystemSource{Kind: SystemSourceRoomOutbox, EventID: uuid.New()}
	digest := idempotency.Digest(sha256.Sum256([]byte("system-request")))
	executedAt := now.Add(2 * time.Second)
	after, batch, err := before.ApplySystem(SystemTransitionRequest{
		BatchID: uuid.New(), OwnershipEpoch: 1, ExpectedStateVersion: 1,
		SystemOperationID: operationID, Source: source, RequestDigest: digest,
		Execution: testRuntimeExecution(executedAt), Input: testRuntimeMessage("participant.revoked", []byte("user")),
		Transition: testRuntimeTransition(2, false),
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := NewSystemReceipt(SystemReceiptSnapshot{
		Key:           SystemKey{SessionID: before.Snapshot().ID, OperationID: operationID, Source: source},
		RequestDigest: digest, ResultCode: ResultCodeAccepted,
		ResultDigest: idempotency.Digest(sha256.Sum256([]byte("system-result"))),
		StateVersion: 2, CommittedAt: executedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	commit, err := NewSystemCommit(before, after, batch, receipt, []outbox.Event{testRuntimeTransitionOutboxEvent(t, before.Snapshot().ID, 2, executedAt)})
	if err != nil || !commit.Valid() {
		t.Fatalf("system commit valid = %v, err = %v", commit.Valid(), err)
	}
	if commit.Batch().Snapshot().ActorUserID != uuid.Nil || commit.Batch().Snapshot().ActionID.Valid() {
		t.Fatal("system commit batch impersonated a player action")
	}

	wrongDigestReceipt, err := NewSystemReceipt(SystemReceiptSnapshot{
		Key: receipt.Snapshot().Key, RequestDigest: idempotency.Digest(sha256.Sum256([]byte("different"))),
		ResultCode: ResultCodeAccepted, StateVersion: 2, CommittedAt: executedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSystemCommit(before, after, batch, wrongDigestReceipt, commit.OutboxEvents()); !errors.Is(err, ErrInvalidSystemCommit) {
		t.Fatalf("wrong system digest error = %v", err)
	}
}

func testRuntimeTransitionOutboxEvent(t testing.TB, sessionID uuid.UUID, stateVersion uint64, at time.Time) outbox.Event {
	t.Helper()
	eventType, err := outbox.ParseEventType("game.session.transitioned.v1")
	if err != nil {
		t.Fatal(err)
	}
	aggregateType, err := outbox.ParseAggregateType("game.session")
	if err != nil {
		t.Fatal(err)
	}
	event, err := outbox.NewEvent(
		uuid.New(), eventType, aggregateType, sessionID, []byte(`{"stateVersion":`+strconv.FormatUint(stateVersion, 10)+`}`), at, at,
	)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

// testRuntimeOperationID returns a stable, valid caller-generated idempotency key for domain tests.
func testRuntimeOperationID(t testing.TB, marker byte) idempotency.OperationID {
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
