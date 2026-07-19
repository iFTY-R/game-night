package gameruntime

import (
	"bytes"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

func TestNewSessionFreezesExactVersionParticipantsAndDeterministicBatch(t *testing.T) {
	now := time.Date(2026, time.July, 19, 8, 0, 0, 0, time.UTC)
	firstUser, secondUser := uuid.New(), uuid.New()
	participants := []Participant{
		{UserID: firstUser, SeatIndex: 4},
		{UserID: secondUser, SeatIndex: 1},
	}
	input := testRuntimeMessage("round.config", []byte("config"))
	transition := testRuntimeTransition(1, false,
		testRuntimeTimer("turn.timeout", now.Add(30*time.Second)),
		testRuntimeTimer("room.timeout", now.Add(2*time.Minute)),
	)
	request := CreateRequest{
		SessionID: uuid.New(), RoomID: uuid.New(), VersionKey: testRuntimeVersionKey(),
		Participants: participants, BatchID: uuid.New(), Execution: testRuntimeExecution(now),
		Input: input, Transition: transition,
	}

	session, batch, err := NewSession(request)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := session.Snapshot()
	if snapshot.VersionKey != request.VersionKey || snapshot.State.StateVersion != 1 || snapshot.OwnershipEpoch != 0 ||
		snapshot.Status != StatusActive || !snapshot.NextDeadlineAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("unexpected session snapshot: %+v", snapshot)
	}
	wantParticipants := []Participant{{UserID: secondUser, SeatIndex: 1}, {UserID: firstUser, SeatIndex: 4}}
	if !reflect.DeepEqual(snapshot.Participants, wantParticipants) {
		t.Fatalf("participants = %+v, want %+v", snapshot.Participants, wantParticipants)
	}
	batchSnapshot := batch.Snapshot()
	if batchSnapshot.Cause != EventCauseCreated || batchSnapshot.StateVersion != 1 || batchSnapshot.OwnershipEpoch != 0 ||
		!reflect.DeepEqual(batchSnapshot.Execution, request.Execution) || !reflect.DeepEqual(batchSnapshot.Input, input) ||
		!reflect.DeepEqual(batchSnapshot.Events, transition.Events) {
		t.Fatalf("unexpected initial batch: %+v", batchSnapshot)
	}

	participants[0].SeatIndex = 99
	request.Execution.AllocatedIDs[0] = "mutated"
	input.Payload[0] = 'X'
	transition.Snapshot.State.Payload[0] = 'X'
	transition.Events[0].Message.Payload[0] = 'X'
	snapshot.Participants[0].SeatIndex = 99
	snapshot.State.State.Payload[0] = 'Y'
	snapshot.Timers[0].Message.Payload[0] = 'Y'
	batchSnapshot.Execution.AllocatedIDs[0] = "mutated"
	batchSnapshot.Input.Payload[0] = 'Y'
	batchSnapshot.Events[0].Message.Payload[0] = 'Y'

	secondSnapshot := session.Snapshot()
	secondBatch := batch.Snapshot()
	if !reflect.DeepEqual(secondSnapshot.Participants, wantParticipants) ||
		bytes.Equal(secondSnapshot.State.State.Payload, snapshot.State.State.Payload) ||
		secondSnapshot.Timers[0].Message.Payload[0] == 'Y' ||
		secondBatch.Execution.AllocatedIDs[0] == "mutated" || secondBatch.Input.Payload[0] == 'Y' ||
		secondBatch.Events[0].Message.Payload[0] == 'Y' {
		t.Fatal("session or batch retained caller-owned mutable data")
	}
}

func TestNewSessionRejectsDuplicateFrozenUsersAndSeats(t *testing.T) {
	now := time.Date(2026, time.July, 19, 8, 0, 0, 0, time.UTC)
	firstUser, secondUser := uuid.New(), uuid.New()
	base := CreateRequest{
		SessionID: uuid.New(), RoomID: uuid.New(), VersionKey: testRuntimeVersionKey(), BatchID: uuid.New(),
		Execution: testRuntimeExecution(now), Input: testRuntimeMessage("round.config", []byte("config")),
		Transition: testRuntimeTransition(1, false),
	}
	for name, participants := range map[string][]Participant{
		"duplicate user": {{UserID: firstUser, SeatIndex: 0}, {UserID: firstUser, SeatIndex: 1}},
		"duplicate seat": {{UserID: firstUser, SeatIndex: 0}, {UserID: secondUser, SeatIndex: 0}},
	} {
		t.Run(name, func(t *testing.T) {
			request := base
			request.Participants = participants
			if _, _, err := NewSession(request); !errors.Is(err, ErrInvalidSessionInput) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestRestoreSessionRejectsMoreTimersThanOneTransitionCanOwn(t *testing.T) {
	now := time.Date(2026, time.July, 19, 8, 0, 0, 0, time.UTC)
	session, _, err := NewSession(testRuntimeCreateRequest(now))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := session.Snapshot()
	snapshot.Timers = make([]TimerSnapshot, game.MaximumTransitionTimers+1)
	for index := range snapshot.Timers {
		snapshot.Timers[index] = TimerSnapshot{
			TimerID: game.Identifier("timer-" + strconv.Itoa(index)), ExpectedStateVersion: 1,
			DueAt: now.Add(time.Minute), Message: testRuntimeMessage("round.timer", []byte("timer")),
		}
	}
	if _, err := RestoreSession(snapshot); !errors.Is(err, ErrInvalidSessionInput) {
		t.Fatalf("oversized timer set error = %v", err)
	}
}

func TestSessionOwnershipEpochFencesStaleRuntime(t *testing.T) {
	now := time.Date(2026, time.July, 19, 8, 0, 0, 0, time.UTC)
	session, _, err := NewSession(testRuntimeCreateRequest(now))
	if err != nil {
		t.Fatal(err)
	}
	firstOwner, err := session.AcquireOwnership(0, now.Add(time.Second))
	if err != nil || firstOwner.Snapshot().OwnershipEpoch != 1 {
		t.Fatalf("first ownership = %+v, err = %v", firstOwner.Snapshot(), err)
	}
	secondOwner, err := firstOwner.AcquireOwnership(1, now.Add(2*time.Second))
	if err != nil || secondOwner.Snapshot().OwnershipEpoch != 2 {
		t.Fatalf("second ownership = %+v, err = %v", secondOwner.Snapshot(), err)
	}
	if _, err := secondOwner.AcquireOwnership(1, now.Add(3*time.Second)); !errors.Is(err, ErrOwnershipLost) {
		t.Fatalf("stale ownership error = %v", err)
	}
}

func TestApplyActionRequiresExactEpochAndStateStepAndFinishesTerminally(t *testing.T) {
	now := time.Date(2026, time.July, 19, 8, 0, 0, 0, time.UTC)
	session, _, err := NewSession(testRuntimeCreateRequest(now))
	if err != nil {
		t.Fatal(err)
	}
	session, err = session.AcquireOwnership(0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	actionID := testRuntimeOperationID(t, 1)
	request := ActionTransitionRequest{
		BatchID: uuid.New(), OwnershipEpoch: 1, ActorUserID: session.Snapshot().Participants[0].UserID,
		ActionID: actionID, Execution: testRuntimeExecution(now.Add(2 * time.Second)),
		Input:      testRuntimeMessage("round.roll", []byte("roll")),
		Transition: testRuntimeTransition(2, false, testRuntimeTimer("turn.timeout", now.Add(32*time.Second))),
	}
	if _, _, err := session.ApplyAction(ActionTransitionRequest{
		BatchID: request.BatchID, OwnershipEpoch: 2, ActorUserID: request.ActorUserID, ActionID: request.ActionID,
		Execution: request.Execution, Input: request.Input, Transition: request.Transition,
	}); !errors.Is(err, ErrOwnershipLost) {
		t.Fatalf("stale epoch error = %v", err)
	}

	next, batch, err := session.ApplyAction(request)
	if err != nil {
		t.Fatal(err)
	}
	if next.Snapshot().State.StateVersion != 2 || batch.Snapshot().StateVersion != 2 || batch.Snapshot().OwnershipEpoch != 1 {
		t.Fatalf("next = %+v, batch = %+v", next.Snapshot(), batch.Snapshot())
	}
	gapped := request
	gapped.BatchID = uuid.New()
	gapped.Transition = testRuntimeTransition(4, false)
	if _, _, err := next.ApplyAction(gapped); !errors.Is(err, ErrStateVersionConflict) {
		t.Fatalf("gapped transition error = %v", err)
	}

	finish := request
	finish.BatchID = uuid.New()
	finish.ActionID = testRuntimeOperationID(t, 2)
	finish.Execution = testRuntimeExecution(now.Add(3 * time.Second))
	finish.Transition = testRuntimeTransition(3, true)
	finished, _, err := next.ApplyAction(finish)
	if err != nil {
		t.Fatal(err)
	}
	finishedSnapshot := finished.Snapshot()
	if finishedSnapshot.Status != StatusFinished || !finishedSnapshot.EndedAt.Equal(finish.Execution.Now) ||
		!finishedSnapshot.NextDeadlineAt.IsZero() || len(finishedSnapshot.Timers) != 0 {
		t.Fatalf("finished snapshot = %+v", finishedSnapshot)
	}
	finish.BatchID = uuid.New()
	finish.ActionID = testRuntimeOperationID(t, 3)
	finish.Transition = testRuntimeTransition(4, false)
	if _, _, err := finished.ApplyAction(finish); !errors.Is(err, ErrSessionTerminal) {
		t.Fatalf("terminal action error = %v", err)
	}
}

func testRuntimeCreateRequest(now time.Time) CreateRequest {
	return CreateRequest{
		SessionID: uuid.New(), RoomID: uuid.New(), VersionKey: testRuntimeVersionKey(),
		Participants: []Participant{{UserID: uuid.New(), SeatIndex: 0}, {UserID: uuid.New(), SeatIndex: 1}},
		BatchID:      uuid.New(), Execution: testRuntimeExecution(now),
		Input: testRuntimeMessage("round.config", []byte("config")), Transition: testRuntimeTransition(1, false),
	}
}

func testRuntimeVersionKey() game.VersionKey {
	return game.VersionKey{GameID: "dice", Engine: "1.2.3", Protocol: "2.3.4", Client: "3.4.5"}
}

func testRuntimeExecution(now time.Time) game.DeterministicContext {
	var seed [game.RandomSeedBytes]byte
	seed[0] = 1
	return game.DeterministicContext{Now: now, RandomSeed: seed, AllocatedIDs: []game.Identifier{"allocated-1"}}
}

func testRuntimeMessage(messageType game.Identifier, payload []byte) game.Message {
	return game.Message{MessageType: messageType, SchemaVersion: 1, Payload: append([]byte(nil), payload...)}
}

func testRuntimeTransition(stateVersion uint64, finished bool, timers ...game.TimerIntent) game.Transition {
	return game.Transition{
		Snapshot: game.Snapshot{
			SnapshotVersion: 1, StateVersion: stateVersion,
			State: testRuntimeMessage("round.state", []byte{byte(stateVersion), 2, 3}),
		},
		Events: []game.Event{{Message: testRuntimeMessage("round.changed", []byte{byte(stateVersion), 4, 5})}},
		Timers: timers, Finished: finished,
	}
}

func testRuntimeTimer(timerID game.Identifier, dueAt time.Time) game.TimerIntent {
	return game.TimerIntent{TimerID: timerID, DueAt: dueAt, Message: testRuntimeMessage("round.timer", []byte("timer"))}
}
