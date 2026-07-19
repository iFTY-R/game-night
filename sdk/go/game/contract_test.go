package game

import (
	"errors"
	"testing"
	"time"
)

func TestMessageAndDeterministicContextValidation(t *testing.T) {
	message := Message{MessageType: "roll_dice", SchemaVersion: 1, Payload: []byte{1, 2, 3}}
	if !message.Valid() {
		t.Fatal("valid message rejected")
	}
	clone := message.Clone()
	message.Payload[0] = 9
	if clone.Payload[0] != 1 {
		t.Fatal("message clone retained payload buffer")
	}

	execution := deterministicContextFixture()
	if !execution.Valid() {
		t.Fatal("valid deterministic context rejected")
	}
	execution.AllocatedIDs = append(execution.AllocatedIDs, "event-1")
	if execution.Valid() {
		t.Fatal("duplicate allocated IDs accepted")
	}
	execution = deterministicContextFixture()
	execution.RandomSeed = [RandomSeedBytes]byte{}
	if execution.Valid() {
		t.Fatal("missing random seed accepted")
	}
}

func TestSnapshotRequiresIndependentStateAndSnapshotVersions(t *testing.T) {
	message := Message{MessageType: "authoritative_state", SchemaVersion: 2}
	if !(Snapshot{SnapshotVersion: 3, StateVersion: 7, State: message}).Valid() {
		t.Fatal("valid snapshot rejected")
	}
	if (Snapshot{SnapshotVersion: 0, StateVersion: 7, State: message}).Valid() {
		t.Fatal("unversioned snapshot accepted")
	}
}

func TestCreateRequestValidatesFrozenParticipantIdentityAndSeats(t *testing.T) {
	execution := deterministicContextFixture()
	request := CreateRequest{
		Context: execution,
		Participants: []Participant{
			{UserID: "user-1", SeatIndex: 0},
			{UserID: "user-2", SeatIndex: 1},
		},
		Config: Message{MessageType: "game_config", SchemaVersion: 1},
	}
	limits := ParticipantLimits{Minimum: 2, Maximum: 9}
	if err := request.Validate(limits); err != nil {
		t.Fatal(err)
	}
	request.Participants[1].SeatIndex = 0
	if err := request.Validate(limits); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("duplicate seat error = %v", err)
	}
}

func TestTransitionAndProjectionEnforceRuntimeBounds(t *testing.T) {
	now := deterministicContextFixture().Now
	state := Message{MessageType: "authoritative_state", SchemaVersion: 1}
	event := Message{MessageType: "dice_rolled", SchemaVersion: 1}
	timer := Message{MessageType: "turn_timeout", SchemaVersion: 1}
	transition := Transition{
		Snapshot: Snapshot{SnapshotVersion: 1, StateVersion: 8, State: state},
		Events:   []Event{{Message: event}},
		Timers:   []TimerIntent{{TimerID: "turn-1", DueAt: now.Add(time.Second), Message: timer}},
	}
	if err := transition.Validate(7, now); err != nil {
		t.Fatal(err)
	}
	transition.Timers = append(transition.Timers, transition.Timers[0])
	if err := transition.Validate(7, now); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("duplicate timer error = %v", err)
	}

	projection := Projection{View: Message{MessageType: "player_view", SchemaVersion: 1}, AllowedActions: []Identifier{"roll", "challenge"}}
	if !projection.Valid() {
		t.Fatal("valid projection rejected")
	}
	projection.AllowedActions = append(projection.AllowedActions, "roll")
	if projection.Valid() {
		t.Fatal("duplicate projection action accepted")
	}
}

func TestCommandTimerAndViewerRequireCanonicalContext(t *testing.T) {
	execution := deterministicContextFixture()
	message := Message{MessageType: "roll", SchemaVersion: 1}
	command := CommandRequest{
		Context: execution, ActorUserID: "user-1", ActionID: "action-1", ExpectedStateVersion: 3, Command: message,
	}
	if !command.Valid() {
		t.Fatal("valid command rejected")
	}
	timer := TimerRequest{Context: execution, TimerID: "turn-1", ExpectedStateVersion: 3, Timer: message}
	if !timer.Valid() {
		t.Fatal("valid timer rejected")
	}
	if !(Viewer{Kind: ViewerPlayer, UserID: "user-1", SeatIndex: 8}).Valid() {
		t.Fatal("valid player viewer rejected")
	}
	if (Viewer{Kind: ViewerSpectator, UserID: "user-1", SeatIndex: 1}).Valid() {
		t.Fatal("spectator with participant seat accepted")
	}
}

func deterministicContextFixture() DeterministicContext {
	execution := DeterministicContext{
		Now:          time.Date(2026, time.July, 19, 23, 0, 0, 0, time.UTC),
		AllocatedIDs: []Identifier{"event-1", "timer-1"},
	}
	execution.RandomSeed[0] = 1
	return execution
}
