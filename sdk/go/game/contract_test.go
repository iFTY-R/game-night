package game

import (
	"encoding/base64"
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
		StartContext: SessionStartContext{
			HostUserID: "user-2", StartingSeat: 0,
		},
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

func TestCreateRequestRequiresTrustedHostAndStartingSeatInFrozenParticipants(t *testing.T) {
	request := CreateRequest{
		Context:      deterministicContextFixture(),
		StartContext: SessionStartContext{HostUserID: "user-2", StartingSeat: 4},
		Participants: []Participant{{UserID: "user-1", SeatIndex: 4}, {UserID: "user-2", SeatIndex: 9}},
		Config:       Message{MessageType: "game_config", SchemaVersion: 1},
	}
	limits := ParticipantLimits{Minimum: 2, Maximum: 9}
	if err := request.Validate(limits); err != nil {
		t.Fatal(err)
	}

	invalidHost := request
	invalidHost.StartContext.HostUserID = "user-3"
	if err := invalidHost.Validate(limits); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("unknown host error = %v", err)
	}
	invalidSeat := request
	invalidSeat.StartContext.StartingSeat = 3
	if err := invalidSeat.Validate(limits); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("unknown starting seat error = %v", err)
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
		Context: execution, ActorUserID: "user-1", ActionID: actionIDFixture(1), ExpectedStateVersion: 3, Command: message,
	}
	if !command.Valid() {
		t.Fatal("valid command rejected")
	}
	timer := TimerRequest{Context: execution, TimerID: "turn-1", ExpectedStateVersion: 3, Timer: message}
	if !timer.Valid() {
		t.Fatal("valid timer rejected")
	}
	system := SystemRequest{
		Context: execution, SystemOperationID: actionIDFixture(4), SourceEventID: "event-4",
		ExpectedStateVersion: 3, System: message,
	}
	if !system.Valid() {
		t.Fatal("valid system request rejected")
	}
	system.SourceEventID = "Event-4"
	if system.Valid() {
		t.Fatal("non-canonical system source accepted")
	}
	if !(Viewer{Kind: ViewerPlayer, UserID: "user-1", SeatIndex: 8}).Valid() {
		t.Fatal("valid player viewer rejected")
	}
	if (Viewer{Kind: ViewerSpectator, UserID: "user-1", SeatIndex: 1}).Valid() {
		t.Fatal("spectator with participant seat accepted")
	}
}

func TestEventProjectionContainsOnlyBoundedViewerSafeMessages(t *testing.T) {
	versioned := VersionedEvent{StateVersion: 4, Event: Event{Message: Message{MessageType: "dice_revealed", SchemaVersion: 1}}}
	if !versioned.Valid() {
		t.Fatal("valid versioned event rejected")
	}
	projection := EventProjection{Messages: []Message{{MessageType: "viewer_delta", SchemaVersion: 1}}}
	if !projection.Valid() {
		t.Fatal("valid event projection rejected")
	}
	projection.Messages[0].SchemaVersion = 0
	if projection.Valid() {
		t.Fatal("invalid viewer-safe message accepted")
	}
}

func TestActionIDRequiresCanonicalRawBase64URLWithAtLeast128Bits(t *testing.T) {
	actionID := actionIDFixture(7)
	if !actionID.Valid() {
		t.Fatal("valid action ID rejected")
	}
	for _, value := range []string{"action-1", string(actionID) + "=", base64.RawURLEncoding.EncodeToString(make([]byte, 15))} {
		if _, err := ParseActionID(value); !errors.Is(err, ErrInvalidContract) {
			t.Fatalf("invalid action ID %q error = %v", value, err)
		}
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

func actionIDFixture(marker byte) ActionID {
	value := make([]byte, MinimumActionIDBytes)
	for index := range value {
		value[index] = marker
	}
	return ActionID(base64.RawURLEncoding.EncodeToString(value))
}
