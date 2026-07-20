package module

import (
	"bytes"
	"testing"
	"time"

	"github.com/iFTY-R/game-night/games/liars-dice/engine"
	liarsdicev1 "github.com/iFTY-R/game-night/games/liars-dice/gen/go/game/liars_dice/v1"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"google.golang.org/protobuf/proto"
)

func TestCreateAndProjectKeepPrivateDiceViewerScoped(t *testing.T) {
	m := New()
	manifest, err := game.ValidateManifest(m.Manifest())
	if err != nil || manifest.GameID != GameID || manifest.Participants.Minimum != 2 || manifest.Participants.Maximum != 8 {
		t.Fatalf("manifest = %+v, error=%v", manifest, err)
	}
	request := createRequest(t, 4)
	transition, err := m.Create(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(transition.Timers) != 1 || transition.Timers[0].TimerID != TimerID {
		t.Fatalf("timers = %+v", transition.Timers)
	}
	state, err := DecodeState(transition.Snapshot.State)
	if err != nil {
		t.Fatal(err)
	}
	actor := state.CurrentActorUserID
	actorSeat := seatOf(t, state, actor)
	playerProjection, err := m.Project(transition.Snapshot, game.Viewer{Kind: game.ViewerPlayer, UserID: game.Identifier(actor), SeatIndex: actorSeat})
	if err != nil {
		t.Fatal(err)
	}
	var playerView liarsdicev1.View
	if err := proto.Unmarshal(playerProjection.View.Payload, &playerView); err != nil {
		t.Fatal(err)
	}
	if len(playerView.GetOwnDice()) != int(state.Config.DicePerPlayer) || len(playerView.GetRevealedDice()) != 0 {
		t.Fatalf("player view leaked/omitted dice: %+v", &playerView)
	}
	for _, player := range state.Players {
		projected, projectErr := m.Project(transition.Snapshot, game.Viewer{
			Kind: game.ViewerPlayer, UserID: game.Identifier(player.UserID), SeatIndex: player.SeatIndex,
		})
		if projectErr != nil {
			t.Fatal(projectErr)
		}
		var playerSpecific liarsdicev1.View
		if err := proto.Unmarshal(projected.View.Payload, &playerSpecific); err != nil {
			t.Fatal(err)
		}
		if len(playerSpecific.GetOwnDice()) != int(state.Config.DicePerPlayer) || len(playerSpecific.GetRevealedDice()) != 0 {
			t.Fatalf("player %s projection leaked or omitted private dice: %+v", player.UserID, &playerSpecific)
		}
	}
	spectatorProjection, err := m.Project(transition.Snapshot, game.Viewer{Kind: game.ViewerSpectator, UserID: "spectator"})
	if err != nil {
		t.Fatal(err)
	}
	var spectatorView liarsdicev1.View
	if err := proto.Unmarshal(spectatorProjection.View.Payload, &spectatorView); err != nil {
		t.Fatal(err)
	}
	if len(spectatorView.GetOwnDice()) != 0 || len(spectatorView.GetRevealedDice()) != 0 {
		t.Fatalf("spectator view leaked private dice: %+v", &spectatorView)
	}
}

func TestCommandRoundTripOpenRevealAndFinish(t *testing.T) {
	m := New()
	created, err := m.Create(createRequest(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	state, err := DecodeState(created.Snapshot.State)
	if err != nil {
		t.Fatal(err)
	}
	bidPayload := bidCommandPayload(t, 2, 4, liarsdicev1.BidMode_BID_MODE_FLYING)
	bidTransition, err := m.HandleCommand(created.Snapshot, commandRequest(
		created.Snapshot.StateVersion, state.CurrentActorUserID, "round.bid", bidPayload, executionAt(time.Unix(101, 0).UTC(), 2),
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(bidTransition.Timers) != 1 || bidTransition.Timers[0].DueAt.Equal(created.Timers[0].DueAt) {
		t.Fatalf("bid timer was not replaced: %+v", bidTransition.Timers)
	}
	bidState, err := DecodeState(bidTransition.Snapshot.State)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := m.HandleCommand(bidTransition.Snapshot, commandRequest(
		bidTransition.Snapshot.StateVersion, bidState.CurrentActorUserID, "round.open", openCommandPayload(t), executionAt(time.Unix(102, 0).UTC(), 3),
	))
	if err != nil {
		t.Fatal(err)
	}
	if eventCount(opened.Events, EventDiceRevealedMessage) != 1 || eventCount(opened.Events, EventRoundSettledMessage) != 1 {
		t.Fatalf("open events = %+v", opened.Events)
	}
	openState, err := DecodeState(opened.Snapshot.State)
	if err != nil {
		t.Fatal(err)
	}
	spectatorProjection, err := m.Project(opened.Snapshot, game.Viewer{Kind: game.ViewerSpectator, UserID: "spectator"})
	if err != nil {
		t.Fatal(err)
	}
	var view liarsdicev1.View
	if err := proto.Unmarshal(spectatorProjection.View.Payload, &view); err != nil {
		t.Fatal(err)
	}
	if len(view.GetRevealedDice()) != 2 || openState.LastSettlement.Round != 1 {
		t.Fatalf("settled public view = %+v", &view)
	}
	finished, err := m.HandleSystem(opened.Snapshot, systemRequest(
		opened.Snapshot.StateVersion, "session.finish", finishCommandPayload(t), executionAt(time.Unix(103, 0).UTC(), 4),
	))
	if err != nil {
		t.Fatal(err)
	}
	if !finished.Finished || len(finished.Timers) != 0 || eventCount(finished.Events, EventSessionFinishedMessage) != 1 {
		t.Fatalf("finish transition = %+v", finished)
	}
}

func TestTimerTimeoutDoesNotRevealDice(t *testing.T) {
	m := New()
	created, err := m.Create(createRequest(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	timer := created.Timers[0]
	timedOut, err := m.HandleTimer(created.Snapshot, game.TimerRequest{
		Context: executionAt(timer.DueAt, 2), TimerID: timer.TimerID,
		ExpectedStateVersion: created.Snapshot.StateVersion, Timer: timer.Message,
	})
	if err != nil {
		t.Fatal(err)
	}
	if eventCount(timedOut.Events, EventDiceRevealedMessage) != 0 || eventCount(timedOut.Events, EventRoundSettledMessage) != 1 {
		t.Fatalf("timeout events leaked dice: %+v", timedOut.Events)
	}
	state, err := DecodeState(timedOut.Snapshot.State)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.LastSettlement.RevealedDice) != 0 || state.LastSettlement.Reason != engine.SettlementTimeout {
		t.Fatalf("timeout settlement = %+v", state.LastSettlement)
	}
}

func TestDisabledTimerAndPastDueRevocationReplacement(t *testing.T) {
	m := New()
	disabledRequest := createRequest(t, 3)
	disabledConfig := engine.DefaultConfig(3)
	disabledConfig.ActionTimeoutSeconds = 0
	var err error
	disabledRequest.Config, err = EncodeConfigForPlayers(disabledConfig, 3)
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := m.Create(disabledRequest)
	if err != nil {
		t.Fatal(err)
	}
	if len(disabled.Timers) != 0 {
		t.Fatalf("disabled timeout scheduled timers: %+v", disabled.Timers)
	}

	created, err := m.Create(createRequest(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	state, err := DecodeState(created.Snapshot.State)
	if err != nil {
		t.Fatal(err)
	}
	var revokedUser string
	for _, player := range state.Players {
		if player.UserID != state.CurrentActorUserID {
			revokedUser = player.UserID
			break
		}
	}
	revokePayload := mustProto(t, &liarsdicev1.ParticipantRevoked{UserId: revokedUser})
	afterDeadline := time.UnixMilli(state.ActionDeadlineUnixMillis + 1000).UTC()
	revoked, err := m.HandleSystem(created.Snapshot, systemRequest(1, "participant.revoked", revokePayload, executionAt(afterDeadline, 2)))
	if err != nil {
		t.Fatal(err)
	}
	if len(revoked.Timers) != 1 || !revoked.Timers[0].DueAt.Equal(afterDeadline) {
		t.Fatalf("past-due timer replacement = %+v", revoked.Timers)
	}
	var timer liarsdicev1.ActionTimer
	if err := proto.Unmarshal(revoked.Timers[0].Message.Payload, &timer); err != nil {
		t.Fatal(err)
	}
	if timer.GetDeadlineUnixMillis() != state.ActionDeadlineUnixMillis {
		t.Fatalf("timer token changed from %d to %d", state.ActionDeadlineUnixMillis, timer.GetDeadlineUnixMillis())
	}
	timedOut, err := m.HandleTimer(revoked.Snapshot, game.TimerRequest{
		Context: executionAt(afterDeadline, 3), TimerID: TimerID, ExpectedStateVersion: 2, Timer: revoked.Timers[0].Message,
	})
	if err != nil {
		t.Fatal(err)
	}
	if eventCount(timedOut.Events, EventRoundSettledMessage) != 1 || eventCount(timedOut.Events, EventDiceRevealedMessage) != 0 {
		t.Fatalf("replacement timeout events = %+v", timedOut.Events)
	}
}

func TestProjectEventsEmitsCurrentViewerSafeDeltaFromMidRoundCursor(t *testing.T) {
	m := New()
	created, err := m.Create(createRequest(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	state, err := DecodeState(created.Snapshot.State)
	if err != nil {
		t.Fatal(err)
	}
	bidPayload := bidCommandPayload(t, 3, 3, liarsdicev1.BidMode_BID_MODE_FLYING)
	bidTransition, err := m.HandleCommand(created.Snapshot, commandRequest(
		created.Snapshot.StateVersion, state.CurrentActorUserID, "round.bid", bidPayload, executionAt(time.Unix(101, 0).UTC(), 2),
	))
	if err != nil {
		t.Fatal(err)
	}
	events := []game.VersionedEvent{{StateVersion: bidTransition.Snapshot.StateVersion, Event: bidTransition.Events[0]}}
	delta, err := m.ProjectEvents(bidTransition.Snapshot, events, game.Viewer{Kind: game.ViewerSpectator, UserID: "spectator"})
	if err != nil {
		t.Fatal(err)
	}
	if len(delta.Messages) != 1 || delta.Messages[0].MessageType != ViewDeltaMessageType {
		t.Fatalf("delta = %+v", delta)
	}
	var value liarsdicev1.ViewDelta
	if err := proto.Unmarshal(delta.Messages[0].Payload, &value); err != nil {
		t.Fatal(err)
	}
	if value.GetView() == nil || len(value.GetView().GetOwnDice()) != 0 || len(value.GetView().GetRevealedDice()) != 0 {
		t.Fatalf("delta leaked private dice: %+v", value.GetView())
	}
}

func TestReplayIncludesOnlySettledRounds(t *testing.T) {
	m := New()
	created, err := m.Create(createRequest(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	state, err := DecodeState(created.Snapshot.State)
	if err != nil {
		t.Fatal(err)
	}
	bidPayload := bidCommandPayload(t, 2, 2, liarsdicev1.BidMode_BID_MODE_FLYING)
	bidTransition, err := m.HandleCommand(created.Snapshot, commandRequest(1, state.CurrentActorUserID, "round.bid", bidPayload, executionAt(time.Unix(101, 0).UTC(), 2)))
	if err != nil {
		t.Fatal(err)
	}
	bidState, err := DecodeState(bidTransition.Snapshot.State)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := m.HandleCommand(bidTransition.Snapshot, commandRequest(2, bidState.CurrentActorUserID, "round.open", openCommandPayload(t), executionAt(time.Unix(102, 0).UTC(), 3)))
	if err != nil {
		t.Fatal(err)
	}
	allEvents := append(append(append([]game.Event{}, created.Events...), bidTransition.Events...), opened.Events...)
	projection, err := m.ProjectReplay(allEvents, game.Viewer{Kind: game.ViewerReplay, UserID: "user-1"}, game.ReplayAccessParticipant)
	if err != nil {
		t.Fatal(err)
	}
	var replay liarsdicev1.Replay
	if err := proto.Unmarshal(projection.View.Payload, &replay); err != nil {
		t.Fatal(err)
	}
	if len(replay.GetRounds()) != 1 || !replay.GetRounds()[0].GetDiceRevealed() || len(replay.GetRounds()[0].GetDice()) != 2 || replay.GetRounds()[0].GetRound() != 1 {
		t.Fatalf("replay = %+v", replay.GetRounds())
	}
}

func TestReplayKeepsTimeoutAndRevokedRoundsPrivate(t *testing.T) {
	m := New()
	created, err := m.Create(createRequest(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	timer := created.Timers[0]
	timedOut, err := m.HandleTimer(created.Snapshot, game.TimerRequest{
		Context: executionAt(timer.DueAt, 2), TimerID: TimerID, ExpectedStateVersion: 1, Timer: timer.Message,
	})
	if err != nil {
		t.Fatal(err)
	}
	timeoutEvents := append(append([]game.Event{}, created.Events...), timedOut.Events...)
	timeoutReplay, err := m.ProjectReplay(timeoutEvents, game.Viewer{Kind: game.ViewerReplay, UserID: "user-1"}, game.ReplayAccessParticipant)
	if err != nil {
		t.Fatal(err)
	}
	var replay liarsdicev1.Replay
	if err := proto.Unmarshal(timeoutReplay.View.Payload, &replay); err != nil {
		t.Fatal(err)
	}
	if len(replay.GetRounds()) != 1 || replay.GetRounds()[0].GetDiceRevealed() || len(replay.GetRounds()[0].GetDice()) != 0 {
		t.Fatalf("timeout replay leaked dice: %+v", replay.GetRounds())
	}

	state, err := DecodeState(created.Snapshot.State)
	if err != nil {
		t.Fatal(err)
	}
	revokePayload := mustProto(t, &liarsdicev1.ParticipantRevoked{UserId: state.CurrentActorUserID})
	revoked, err := m.HandleSystem(created.Snapshot, systemRequest(
		1, "participant.revoked", revokePayload, executionAt(time.Unix(101, 0).UTC(), 3),
	))
	if err != nil {
		t.Fatal(err)
	}
	revokeEvents := append(append([]game.Event{}, created.Events...), revoked.Events...)
	revokeReplay, err := m.ProjectReplay(revokeEvents, game.Viewer{Kind: game.ViewerReplay, UserID: "user-1"}, game.ReplayAccessParticipant)
	if err != nil {
		t.Fatal(err)
	}
	replay.Reset()
	if err := proto.Unmarshal(revokeReplay.View.Payload, &replay); err != nil {
		t.Fatal(err)
	}
	if len(replay.GetRounds()) != 0 || len(replay.GetRevokedUserIds()) != 1 {
		t.Fatalf("revoked round entered replay: %+v", &replay)
	}
}

func TestModuleIsByteDeterministicAndRejectsNonCanonicalPayloads(t *testing.T) {
	m := New()
	request := createRequest(t, 4)
	first, err := m.Create(request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Create(request)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Snapshot.State.Payload, second.Snapshot.State.Payload) || !equalEventBytes(first.Events, second.Events) || !bytes.Equal(first.Timers[0].Message.Payload, second.Timers[0].Message.Payload) {
		t.Fatal("same deterministic inputs produced different protobuf bytes")
	}
	state, err := DecodeState(first.Snapshot.State)
	if err != nil {
		t.Fatal(err)
	}
	canonical := bidCommandPayload(t, 4, 3, liarsdicev1.BidMode_BID_MODE_FLYING)
	nonCanonical := append([]byte{0x08, 0x00}, canonical...)
	_, err = m.HandleCommand(first.Snapshot, commandRequest(1, state.CurrentActorUserID, "round.bid", nonCanonical, executionAt(time.Unix(101, 0).UTC(), 2)))
	if engine.ErrorCodeOf(err) != engine.CodeMalformedPayload {
		t.Fatalf("non-canonical payload error = %v", err)
	}
	mismatched := openCommandPayload(t)
	_, err = m.HandleCommand(first.Snapshot, commandRequest(1, state.CurrentActorUserID, "round.bid", mismatched, executionAt(time.Unix(101, 0).UTC(), 2)))
	if engine.ErrorCodeOf(err) != engine.CodeMalformedPayload {
		t.Fatalf("mismatched oneof error = %v", err)
	}

	bidRequest := commandRequest(1, state.CurrentActorUserID, "round.bid", canonical, executionAt(time.Unix(101, 0).UTC(), 3))
	firstBid, err := m.HandleCommand(first.Snapshot, bidRequest)
	if err != nil {
		t.Fatal(err)
	}
	secondBid, err := m.HandleCommand(first.Snapshot, bidRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !equalTransitionBytes(firstBid, secondBid) {
		t.Fatal("same bid input produced different transition bytes")
	}
	bidState, err := DecodeState(firstBid.Snapshot.State)
	if err != nil {
		t.Fatal(err)
	}
	openRequest := commandRequest(2, bidState.CurrentActorUserID, "round.open", openCommandPayload(t), executionAt(time.Unix(102, 0).UTC(), 4))
	firstOpen, err := m.HandleCommand(firstBid.Snapshot, openRequest)
	if err != nil {
		t.Fatal(err)
	}
	secondOpen, err := m.HandleCommand(firstBid.Snapshot, openRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !equalTransitionBytes(firstOpen, secondOpen) {
		t.Fatal("same open input produced different transition bytes")
	}
	history := append(append(append([]game.Event{}, first.Events...), firstBid.Events...), firstOpen.Events...)
	replayViewer := game.Viewer{Kind: game.ViewerReplay, UserID: "user-1"}
	firstReplay, err := m.ProjectReplay(history, replayViewer, game.ReplayAccessParticipant)
	if err != nil {
		t.Fatal(err)
	}
	secondReplay, err := m.ProjectReplay(history, replayViewer, game.ReplayAccessParticipant)
	if err != nil || !bytes.Equal(firstReplay.View.Payload, secondReplay.View.Payload) {
		t.Fatal("same history produced different replay bytes")
	}
}

func TestMigrationCanonicalizesCurrentSchemaAndRejectsUnknownVersion(t *testing.T) {
	m := New()
	created, err := m.Create(createRequest(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := m.Migrate(created.Snapshot, 1, 1)
	if err != nil || !bytes.Equal(migrated.State.Payload, created.Snapshot.State.Payload) {
		t.Fatalf("migration = %+v, error=%v", migrated, err)
	}
	if _, err := m.Migrate(created.Snapshot, 1, 2); engine.ErrorCodeOf(err) != engine.CodeUnsupportedMigration {
		t.Fatalf("unsupported migration error = %v", err)
	}
}

func createRequest(t *testing.T, count int) game.CreateRequest {
	t.Helper()
	participants := make([]game.Participant, count)
	for index := range participants {
		participants[index] = game.Participant{UserID: game.Identifier("user-" + string(rune('1'+index))), SeatIndex: uint32(index)}
	}
	config, err := EncodeConfigForPlayers(engine.DefaultConfig(uint32(count)), count)
	if err != nil {
		t.Fatal(err)
	}
	return game.CreateRequest{Context: executionAt(time.Unix(100, 0).UTC(), 1), StartContext: game.SessionStartContext{HostUserID: participants[0].UserID, StartingSeat: 0}, Participants: participants, Config: config}
}

func executionAt(now time.Time, seedByte byte) game.DeterministicContext {
	var seed [game.RandomSeedBytes]byte
	seed[0] = seedByte
	return game.DeterministicContext{Now: now, RandomSeed: seed}
}
func commandRequest(version uint64, actor, action string, payload []byte, execution game.DeterministicContext) game.CommandRequest {
	return game.CommandRequest{Context: execution, ActorUserID: game.Identifier(actor), ActionID: testActionID(), ExpectedStateVersion: version, Command: game.Message{MessageType: game.Identifier(action), SchemaVersion: ProtocolSchemaVersion, Payload: payload}}
}
func systemRequest(version uint64, action string, payload []byte, execution game.DeterministicContext) game.SystemRequest {
	return game.SystemRequest{Context: execution, SystemOperationID: testActionID(), SourceEventID: "source-event", ExpectedStateVersion: version, System: game.Message{MessageType: game.Identifier(action), SchemaVersion: ProtocolSchemaVersion, Payload: payload}}
}
func testActionID() game.ActionID { return game.ActionID("AQEBAQEBAQEBAQEBAQEBAQ") }
func mustProto(t *testing.T, message proto.Message) []byte {
	t.Helper()
	value, err := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func bidCommandPayload(t *testing.T, quantity, face uint32, mode liarsdicev1.BidMode) []byte {
	t.Helper()
	return mustProto(t, &liarsdicev1.Command{Command: &liarsdicev1.Command_PlaceBid{PlaceBid: &liarsdicev1.PlaceBid{Bid: &liarsdicev1.Bid{Quantity: quantity, Face: face, Mode: mode}}}})
}

func openCommandPayload(t *testing.T) []byte {
	t.Helper()
	return mustProto(t, &liarsdicev1.Command{Command: &liarsdicev1.Command_OpenDice{OpenDice: &liarsdicev1.OpenDice{}}})
}

func finishCommandPayload(t *testing.T) []byte {
	t.Helper()
	return mustProto(t, &liarsdicev1.Command{Command: &liarsdicev1.Command_Finish{Finish: &liarsdicev1.Finish{}}})
}
func seatOf(t *testing.T, state engine.State, userID string) uint32 {
	t.Helper()
	for _, player := range state.Players {
		if player.UserID == userID {
			return player.SeatIndex
		}
	}
	t.Fatalf("missing player %s", userID)
	return 0
}
func eventCount(events []game.Event, messageType game.Identifier) int {
	count := 0
	for _, event := range events {
		if event.Message.MessageType == messageType {
			count++
		}
	}
	return count
}
func equalEventBytes(left, right []game.Event) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Message.MessageType != right[index].Message.MessageType || !bytes.Equal(left[index].Message.Payload, right[index].Message.Payload) {
			return false
		}
	}
	return true
}

func equalTransitionBytes(left, right game.Transition) bool {
	if left.Snapshot.StateVersion != right.Snapshot.StateVersion || left.Finished != right.Finished ||
		!bytes.Equal(left.Snapshot.State.Payload, right.Snapshot.State.Payload) || !equalEventBytes(left.Events, right.Events) ||
		len(left.Timers) != len(right.Timers) {
		return false
	}
	for index := range left.Timers {
		if left.Timers[index].TimerID != right.Timers[index].TimerID ||
			!left.Timers[index].DueAt.Equal(right.Timers[index].DueAt) ||
			!bytes.Equal(left.Timers[index].Message.Payload, right.Timers[index].Message.Payload) {
			return false
		}
	}
	return true
}
