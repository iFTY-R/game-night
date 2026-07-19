package gameruntime

import (
	"crypto/sha256"
	"errors"
	"reflect"
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
