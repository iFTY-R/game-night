package module

import (
	"bytes"
	"reflect"
	"testing"
	"time"

	"github.com/iFTY-R/game-night/games/meet-by-chance/engine"
	meetv1 "github.com/iFTY-R/game-night/games/meet-by-chance/gen/go/game/meet_by_chance/v1"
	"github.com/iFTY-R/game-night/games/meet-by-chance/projection"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"google.golang.org/protobuf/proto"
)

func TestCreateConfigAndStateRoundTrip(t *testing.T) {
	config := engine.DefaultConfig()
	config.Straight234 = false
	config.Straight345 = false
	encodedConfig, err := EncodeConfigForPlayers(config, 4)
	if err != nil {
		t.Fatal(err)
	}
	decodedConfig, err := DecodeConfig(encodedConfig, 4)
	if err != nil || decodedConfig != config {
		t.Fatalf("config round trip: got=%+v err=%v", decodedConfig, err)
	}
	defaultConfig, err := DecodeConfig(game.Message{MessageType: ConfigMessageType, SchemaVersion: ProtocolSchemaVersion}, 4)
	if err != nil || defaultConfig != engine.DefaultConfig() {
		t.Fatalf("empty config did not select defaults: got=%+v err=%v", defaultConfig, err)
	}

	request := meetCreateRequest(t, 4)
	request.Config = encodedConfig
	m := New()
	created, err := m.Create(request)
	if err != nil {
		t.Fatal(err)
	}
	if created.Snapshot.StateVersion != 1 || created.Snapshot.SnapshotVersion != SnapshotVersion || created.Finished || len(created.Events) == 0 || len(created.Timers) != 1 {
		t.Fatalf("create transition=%+v", created)
	}
	state, err := DecodeState(created.Snapshot.State)
	if err != nil {
		t.Fatal(err)
	}
	if state.HostUserID != "user-1" || state.Config != config || state.Round != 1 || state.Phase != engine.PhaseTargetDecision || state.TargetUserID == "" {
		t.Fatalf("created state=%+v", state)
	}
	reencoded, err := EncodeState(state)
	if err != nil || !bytes.Equal(reencoded.Payload, created.Snapshot.State.Payload) {
		t.Fatalf("state round trip is not canonical: err=%v", err)
	}
	second, err := m.Create(request)
	if err != nil || !reflect.DeepEqual(created, second) {
		t.Fatalf("create is not deterministic: err=%v", err)
	}
}

func TestCommandsEnforceActorPayloadAndStateVersion(t *testing.T) {
	m := New()
	created, err := m.Create(meetCreateRequest(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	state := decodeState(t, created.Snapshot)
	nonTarget := firstOtherUser(state, state.TargetUserID)
	stand := &meetv1.Command{Command: &meetv1.Command_Stand{Stand: &meetv1.Stand{}}}
	if _, err := m.HandleCommand(created.Snapshot, meetCommandRequest(t, 1, nonTarget, projection.ActionStand, stand)); engine.ErrorCodeOf(err) != engine.CodeNotCurrentTarget {
		t.Fatalf("non-target stand error=%v", err)
	}

	settled, err := m.HandleCommand(created.Snapshot, meetCommandRequest(t, 1, state.TargetUserID, projection.ActionStand, stand))
	if err != nil {
		t.Fatal(err)
	}
	next := decodeState(t, settled.Snapshot)
	if settled.Snapshot.StateVersion != 2 || next.Round != 2 || next.LastSettlement.Round != 1 || len(next.RoundHistory) != 1 || len(settled.Timers) != 1 {
		t.Fatalf("stand transition=%+v state=%+v", settled, next)
	}

	stale := meetCommandRequest(t, 2, state.TargetUserID, projection.ActionStand, stand)
	if _, err := m.HandleCommand(created.Snapshot, stale); engine.ErrorCodeOf(err) != engine.CodeMalformedPayload {
		t.Fatalf("stale state version error=%v", err)
	}
	mismatched := meetCommandRequest(t, 1, state.TargetUserID, projection.ActionStand, &meetv1.Command{
		Command: &meetv1.Command_Reroll{Reroll: &meetv1.Reroll{}},
	})
	if _, err := m.HandleCommand(created.Snapshot, mismatched); engine.ErrorCodeOf(err) != engine.CodeMalformedPayload {
		t.Fatalf("mismatched oneof error=%v", err)
	}
}

func TestTimerRejectsStaleTargetCounters(t *testing.T) {
	m := New()
	created, err := m.Create(meetCreateRequest(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Timers) != 1 {
		t.Fatalf("timer count=%d", len(created.Timers))
	}
	var timer meetv1.ActionTimer
	if err := unmarshalStrict(created.Timers[0].Message.Payload, &timer); err != nil {
		t.Fatal(err)
	}
	due := time.UnixMilli(timer.GetDeadlineUnixMillis()).UTC()
	tests := []struct {
		name   string
		mutate func(*meetv1.ActionTimer)
	}{
		{name: "target streak", mutate: func(value *meetv1.ActionTimer) { value.TargetStreak++ }},
		{name: "match resolution count", mutate: func(value *meetv1.ActionTimer) { value.MatchResolutionCount++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stale := proto.Clone(&timer).(*meetv1.ActionTimer)
			test.mutate(stale)
			payload, err := marshalDeterministic(stale)
			if err != nil {
				t.Fatal(err)
			}
			request := game.TimerRequest{
				Context: meetContextAt(due), TimerID: TimerID, ExpectedStateVersion: created.Snapshot.StateVersion,
				Timer: game.Message{MessageType: TimerMessageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload},
			}
			if _, err := m.HandleTimer(created.Snapshot, request); engine.ErrorCodeOf(err) != engine.CodeTimerMismatch {
				t.Fatalf("stale timer error=%v", err)
			}
		})
	}
}

func TestSystemRevocationFinishAndMigration(t *testing.T) {
	m := New()
	created, err := m.Create(meetCreateRequest(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	state := decodeState(t, created.Snapshot)
	nonTarget := firstOtherUser(state, state.TargetUserID)

	revoked, err := m.HandleSystem(created.Snapshot, meetSystemRequest(t, 1, EventParticipantRevokedMessage, &meetv1.ParticipantRevoked{UserId: nonTarget}))
	if err != nil {
		t.Fatal(err)
	}
	revokedState := decodeState(t, revoked.Snapshot)
	if playerActive(revokedState, nonTarget) || revokedState.Round != 1 || revoked.Finished || len(revoked.Timers) != 1 {
		t.Fatalf("non-target revoke=%+v state=%+v", revoked, revokedState)
	}
	revocation := findRevocation(t, revoked.Events)
	if revocation.GetWasTarget() || revocation.GetRoundCancelled() || revocation.GetNextRound() != 0 || revocation.GetActivePlayerCount() != 3 {
		t.Fatalf("non-target revoke audit=%+v", revocation)
	}

	targetRevoked, err := m.HandleSystem(created.Snapshot, meetSystemRequest(t, 1, EventParticipantRevokedMessage, &meetv1.ParticipantRevoked{UserId: state.TargetUserID}))
	if err != nil {
		t.Fatal(err)
	}
	targetState := decodeState(t, targetRevoked.Snapshot)
	targetAudit := findRevocation(t, targetRevoked.Events)
	if targetState.Round != 2 || !targetAudit.GetWasTarget() || !targetAudit.GetRoundCancelled() || targetAudit.GetNextRound() != 2 || targetAudit.GetActivePlayerCount() != 3 {
		t.Fatalf("target revoke audit=%+v state=%+v", targetAudit, targetState)
	}

	three, err := m.Create(meetCreateRequest(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	threeState := decodeState(t, three.Snapshot)
	insufficientUser := firstOtherUser(threeState, threeState.TargetUserID)
	insufficient, err := m.HandleSystem(three.Snapshot, meetSystemRequest(t, 1, EventParticipantRevokedMessage, &meetv1.ParticipantRevoked{UserId: insufficientUser}))
	if err != nil {
		t.Fatal(err)
	}
	if !insufficient.Finished || len(insufficient.Timers) != 0 || decodeState(t, insufficient.Snapshot).FinishReason != engine.FinishInsufficientParticipants {
		t.Fatalf("insufficient transition=%+v", insufficient)
	}
	if findRevocation(t, insufficient.Events).GetActivePlayerCount() != 2 || findFinish(t, insufficient.Events).GetOperatorUserId() != "" {
		t.Fatalf("insufficient events=%+v", insufficient.Events)
	}

	hostFinish := &meetv1.Command{Command: &meetv1.Command_Finish{Finish: &meetv1.Finish{
		Reason: engine.FinishHostRequested, OperatorUserId: "user-2",
	}}}
	finished, err := m.HandleSystem(created.Snapshot, meetSystemRequest(t, 1, SystemFinishMessage, hostFinish))
	if err != nil || !finished.Finished || findFinish(t, finished.Events).GetOperatorUserId() != "user-2" {
		t.Fatalf("runtime host finish=%+v err=%v", finished, err)
	}
	missingOperator := &meetv1.Command{Command: &meetv1.Command_Finish{Finish: &meetv1.Finish{Reason: engine.FinishHostRequested}}}
	if _, err := m.HandleSystem(created.Snapshot, meetSystemRequest(t, 1, SystemFinishMessage, missingOperator)); engine.ErrorCodeOf(err) != engine.CodeMalformedPayload {
		t.Fatalf("operator-less host finish error=%v", err)
	}
	platformWithOperator := &meetv1.Command{Command: &meetv1.Command_Finish{Finish: &meetv1.Finish{
		Reason: engine.FinishPlatformCancelled, OperatorUserId: "user-2",
	}}}
	if _, err := m.HandleSystem(created.Snapshot, meetSystemRequest(t, 1, SystemFinishMessage, platformWithOperator)); engine.ErrorCodeOf(err) != engine.CodeMalformedPayload {
		t.Fatalf("platform finish with operator error=%v", err)
	}
	platformFinish := &meetv1.Command{Command: &meetv1.Command_Finish{Finish: &meetv1.Finish{Reason: engine.FinishPlatformCancelled}}}
	platform, err := m.HandleSystem(created.Snapshot, meetSystemRequest(t, 1, SystemFinishMessage, platformFinish))
	if err != nil || !platform.Finished || findFinish(t, platform.Events).GetCause() != meetv1.ResolutionCause_RESOLUTION_CAUSE_PLATFORM_CANCELLED {
		t.Fatalf("platform finish=%+v err=%v", platform, err)
	}

	migrated, err := m.Migrate(created.Snapshot, ProtocolSchemaVersion, ProtocolSchemaVersion)
	if err != nil || !bytes.Equal(migrated.State.Payload, created.Snapshot.State.Payload) {
		t.Fatalf("migration changed canonical state: err=%v", err)
	}
	if _, err := m.Migrate(created.Snapshot, ProtocolSchemaVersion, ProtocolSchemaVersion+1); engine.ErrorCodeOf(err) != engine.CodeUnsupportedMigration {
		t.Fatalf("unsupported migration error=%v", err)
	}
}

func TestStrictPayloadsAndViewerSafeDelta(t *testing.T) {
	canonical, err := marshalDeterministic(&meetv1.Command{Command: &meetv1.Command_Stand{Stand: &meetv1.Stand{}}})
	if err != nil {
		t.Fatal(err)
	}
	var command meetv1.Command
	if err := unmarshalStrict(canonical, &command); err != nil {
		t.Fatal(err)
	}
	unknown := append(append([]byte(nil), canonical...), 0x78, 0x01)
	if err := unmarshalStrict(unknown, &meetv1.Command{}); err == nil {
		t.Fatal("unknown command field was accepted")
	}
	nonCanonical := append(append([]byte(nil), canonical...), canonical...)
	if err := unmarshalStrict(nonCanonical, &meetv1.Command{}); err == nil {
		t.Fatal("non-canonical command was accepted")
	}
	configMessage, err := EncodeConfigForPlayers(engine.DefaultConfig(), 4)
	if err != nil {
		t.Fatal(err)
	}
	unknownConfig := configMessage
	unknownConfig.Payload = append(append([]byte(nil), configMessage.Payload...), 0x78, 0x01)
	if _, err := DecodeConfig(unknownConfig, 4); err == nil {
		t.Fatal("config with an unknown field was accepted")
	}
	nonCanonicalConfig := configMessage
	nonCanonicalConfig.Payload = append(append([]byte(nil), configMessage.Payload...), configMessage.Payload...)
	if _, err := DecodeConfig(nonCanonicalConfig, 4); err == nil {
		t.Fatal("non-canonical config was accepted")
	}

	m := New()
	created, err := m.Create(meetCreateRequest(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	target := decodeState(t, created.Snapshot).TargetUserID
	viewer := playerViewer(target, playerSeat(decodeState(t, created.Snapshot), target))
	delta, err := m.ProjectEvents(created.Snapshot, []game.VersionedEvent{{StateVersion: 1, Event: created.Events[0]}}, viewer)
	if err != nil || len(delta.Messages) != 1 || delta.Messages[0].MessageType != ViewDeltaMessageType {
		t.Fatalf("delta=%+v err=%v", delta, err)
	}
	var value meetv1.ViewDelta
	if err := unmarshalStrict(delta.Messages[0].Payload, &value); err != nil || value.GetView() == nil || len(value.GetView().GetPlayers()) != 0 || len(value.GetView().GetPublicPlayers()) != 4 {
		t.Fatalf("unsafe delta=%+v err=%v", &value, err)
	}
	unknownEvent := created.Events[0]
	unknownEvent.Message.MessageType = "unknown.event"
	if _, err := m.ProjectEvents(created.Snapshot, []game.VersionedEvent{{StateVersion: 1, Event: unknownEvent}}, viewer); err == nil {
		t.Fatal("unknown event was accepted for delta projection")
	}
	regressing := []game.VersionedEvent{
		{StateVersion: 2, Event: created.Events[0]},
		{StateVersion: 1, Event: created.Events[1]},
	}
	if _, err := m.ProjectEvents(created.Snapshot, regressing, viewer); err == nil {
		t.Fatal("regressing event versions were accepted")
	}
	unknownState := created.Snapshot.State
	unknownState.Payload = append(append([]byte(nil), unknownState.Payload...), 0xf8, 0x07, 0x01)
	if _, err := DecodeState(unknownState); err == nil {
		t.Fatal("state with an unknown field was accepted")
	}
	nonCanonicalState := created.Snapshot.State
	nonCanonicalState.Payload = append(append([]byte(nil), nonCanonicalState.Payload...), created.Snapshot.State.Payload...)
	if _, err := DecodeState(nonCanonicalState); err == nil {
		t.Fatal("non-canonical state was accepted")
	}
}

func meetCreateRequest(t *testing.T, count int) game.CreateRequest {
	t.Helper()
	config, err := EncodeConfigForPlayers(engine.DefaultConfig(), count)
	if err != nil {
		t.Fatal(err)
	}
	participants := make([]game.Participant, count)
	for index := range participants {
		participants[index] = game.Participant{UserID: game.Identifier("user-" + string(rune('1'+index))), SeatIndex: uint32(index)}
	}
	return game.CreateRequest{
		Context:      meetContextAt(time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)),
		StartContext: game.SessionStartContext{HostUserID: participants[0].UserID, StartingSeat: participants[0].SeatIndex},
		Participants: participants,
		Config:       config,
	}
}

func meetContextAt(now time.Time) game.DeterministicContext {
	return game.DeterministicContext{Now: now.Round(0).UTC(), RandomSeed: [32]byte{1}}
}

func meetCommandRequest(t *testing.T, version uint64, actor string, messageType game.Identifier, command proto.Message) game.CommandRequest {
	t.Helper()
	payload, err := marshalDeterministic(command)
	if err != nil {
		t.Fatal(err)
	}
	return game.CommandRequest{
		Context:     meetContextAt(time.Date(2026, 7, 21, 12, 0, 1, 0, time.UTC)),
		ActorUserID: game.Identifier(actor), ActionID: game.ActionID("AAAAAAAAAAAAAAAAAAAAAA"), ExpectedStateVersion: version,
		Command: game.Message{MessageType: messageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload},
	}
}

func meetSystemRequest(t *testing.T, version uint64, messageType game.Identifier, message proto.Message) game.SystemRequest {
	t.Helper()
	payload, err := marshalDeterministic(message)
	if err != nil {
		t.Fatal(err)
	}
	return game.SystemRequest{
		Context:           meetContextAt(time.Date(2026, 7, 21, 12, 0, 1, 0, time.UTC)),
		SystemOperationID: game.ActionID("AQEBAQEBAQEBAQEBAQEBAQ"), SourceEventID: "source-event", ExpectedStateVersion: version,
		System: game.Message{MessageType: messageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload},
	}
}

func decodeState(t *testing.T, snapshot game.Snapshot) engine.State {
	t.Helper()
	state, err := DecodeState(snapshot.State)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func firstOtherUser(state engine.State, excluded string) string {
	for _, player := range state.Players {
		if player.UserID != excluded && player.Active {
			return player.UserID
		}
	}
	return ""
}

func playerActive(state engine.State, userID string) bool {
	for _, player := range state.Players {
		if player.UserID == userID {
			return player.Active
		}
	}
	return false
}

func playerSeat(state engine.State, userID string) uint32 {
	for _, player := range state.Players {
		if player.UserID == userID {
			return player.SeatIndex
		}
	}
	return 0
}

func playerViewer(userID string, seat uint32) game.Viewer {
	return game.Viewer{Kind: game.ViewerPlayer, UserID: game.Identifier(userID), SeatIndex: seat}
}

func findRevocation(t *testing.T, events []game.Event) *meetv1.ParticipantRevoked {
	t.Helper()
	for _, event := range events {
		if event.Message.MessageType != EventParticipantRevokedMessage {
			continue
		}
		var value meetv1.ParticipantRevoked
		if err := unmarshalStrict(event.Message.Payload, &value); err != nil {
			t.Fatal(err)
		}
		return &value
	}
	t.Fatal("participant.revoked event is missing")
	return nil
}

func findFinish(t *testing.T, events []game.Event) *meetv1.SessionFinished {
	t.Helper()
	for _, event := range events {
		if event.Message.MessageType != EventSessionFinishedMessage {
			continue
		}
		var value meetv1.SessionFinished
		if err := unmarshalStrict(event.Message.Payload, &value); err != nil {
			t.Fatal(err)
		}
		return &value
	}
	t.Fatal("session.finished event is missing")
	return nil
}
