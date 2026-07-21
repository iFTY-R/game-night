package projection_test

import (
	"testing"
	"time"

	"github.com/iFTY-R/game-night/games/meet-by-chance/engine"
	meetv1 "github.com/iFTY-R/game-night/games/meet-by-chance/gen/go/game/meet_by_chance/v1"
	"github.com/iFTY-R/game-night/games/meet-by-chance/module"
	"github.com/iFTY-R/game-night/games/meet-by-chance/projection"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"google.golang.org/protobuf/proto"
)

func TestReplayContainsSettledRoundsOnly(t *testing.T) {
	m := module.New()
	created := createTransition(t, m, 4)
	pending, err := projection.BuildReplay(created.Events, replayViewer(), game.ReplayAccessParticipant)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending.GetRounds()) != 0 || len(pending.GetEntries()) != 0 {
		t.Fatalf("pending round leaked into replay: %+v", pending)
	}

	state := decodeModuleState(t, created.Snapshot)
	settled, err := m.HandleCommand(created.Snapshot, commandRequest(t, created.Snapshot.StateVersion, state.TargetUserID, projection.ActionStand,
		&meetv1.Command{Command: &meetv1.Command_Stand{Stand: &meetv1.Stand{}}}))
	if err != nil {
		t.Fatal(err)
	}
	history := append(append([]game.Event(nil), created.Events...), settled.Events...)
	replay, err := projection.BuildReplay(history, replayViewer(), game.ReplayAccessParticipant)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.GetRounds()) != 1 || replay.GetRounds()[0].GetSummary().GetRound() != 1 || len(replay.GetEntries()) == 0 {
		t.Fatalf("settled replay=%+v", replay)
	}
	for _, entry := range replay.GetEntries() {
		if eventRound(entry.GetEvent()) != 1 {
			t.Fatalf("pending round entry leaked into replay: %+v", entry)
		}
	}
}

func TestReplayAcceptsNonTargetRevokeThenTargetReroll(t *testing.T) {
	m := module.New()
	created := createTransition(t, m, 4)
	state := decodeModuleState(t, created.Snapshot)
	revokedUser := firstOtherUser(state, state.TargetUserID)
	revoked, err := m.HandleSystem(created.Snapshot, systemRequest(t, created.Snapshot.StateVersion, module.EventParticipantRevokedMessage,
		&meetv1.ParticipantRevoked{UserId: revokedUser}))
	if err != nil {
		t.Fatal(err)
	}
	revokedState := decodeModuleState(t, revoked.Snapshot)
	rerolled, err := m.HandleCommand(revoked.Snapshot, commandRequest(t, revoked.Snapshot.StateVersion, revokedState.TargetUserID, projection.ActionReroll,
		&meetv1.Command{Command: &meetv1.Command_Reroll{Reroll: &meetv1.Reroll{}}}))
	if err != nil {
		t.Fatal(err)
	}
	assertRevealExcludes(t, rerolled.Events, revokedUser)

	history := append(append(append([]game.Event(nil), created.Events...), revoked.Events...), rerolled.Events...)
	last := rerolled
	afterReroll := decodeModuleState(t, rerolled.Snapshot)
	if afterReroll.Round == state.Round {
		last, err = m.HandleCommand(rerolled.Snapshot, commandRequest(t, rerolled.Snapshot.StateVersion, afterReroll.TargetUserID, projection.ActionStand,
			&meetv1.Command{Command: &meetv1.Command_Stand{Stand: &meetv1.Stand{}}}))
		if err != nil {
			t.Fatal(err)
		}
		history = append(history, last.Events...)
	}
	replay, err := projection.BuildReplay(history, replayViewer(), game.ReplayAccessParticipant)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.GetRounds()) == 0 {
		t.Fatalf("settled revoke/reroll flow missing from replay: state=%+v", decodeModuleState(t, last.Snapshot))
	}
}

func TestValidateEventRejectsLegacyKeysUnknownAndNonCanonicalPayloads(t *testing.T) {
	m := module.New()
	created := createTransition(t, m, 4)
	handEvent := findMessage(t, created.Events, module.EventHandClassifiedMessage)
	if err := projection.ValidateEvent(handEvent.Message); err != nil {
		t.Fatalf("canonical hand event rejected: %v", err)
	}
	var hand meetv1.HandClassified
	if err := proto.Unmarshal(handEvent.Message.Payload, &hand); err != nil {
		t.Fatal(err)
	}
	hand.FullKey = []int32{1}
	hand.TieKey = []int32{1}
	leakingHand := handEvent.Message
	leakingHand.Payload = marshal(t, &hand)
	if err := projection.ValidateEvent(leakingHand); err == nil {
		t.Fatal("legacy hand comparison keys were accepted")
	}

	revealEvent := findMessage(t, created.Events, module.EventDiceRevealedMessage)
	var reveal meetv1.DiceRevealed
	if err := proto.Unmarshal(revealEvent.Message.Payload, &reveal); err != nil {
		t.Fatal(err)
	}
	reveal.Players = []*meetv1.PlayerState{{UserId: "user-1", FullKey: []int32{1}, TieKey: []int32{1}}}
	leakingReveal := revealEvent.Message
	leakingReveal.Payload = marshal(t, &reveal)
	if err := projection.ValidateEvent(leakingReveal); err == nil {
		t.Fatal("deprecated authoritative reveal players were accepted")
	}

	match := &meetv1.MatchResolved{
		Round: 1, Cause: meetv1.ResolutionCause_RESOLUTION_CAUSE_MATCH_REROLL,
		Batch: &meetv1.MatchBatch{
			Round: 1, BatchIndex: 1, ResolutionCount: 1,
			Groups:          []*meetv1.MatchGroup{{Kind: meetv1.MatchKind_MATCH_KIND_EXACT, UserIds: []string{"user-1", "user-2"}, PenaltyTicks: 4}},
			RerolledUserIds: []string{"user-1", "user-2"},
		},
	}
	matchMessage := game.Message{MessageType: projection.EventMatchResolved, SchemaVersion: engine.CurrentSchemaVersion, Payload: marshal(t, match)}
	if err := projection.ValidateEvent(matchMessage); err != nil {
		t.Fatalf("canonical match event rejected: %v", err)
	}
	match.UserIds = []string{"user-1", "user-2"}
	match.Kind = "exact"
	match.PenaltyTicks = 4
	matchMessage.Payload = marshal(t, match)
	if err := projection.ValidateEvent(matchMessage); err == nil {
		t.Fatal("legacy match summary fields were accepted")
	}

	unknown := handEvent.Message
	unknown.Payload = append(append([]byte(nil), unknown.Payload...), 0xf8, 0x07, 0x01)
	if err := projection.ValidateEvent(unknown); err == nil {
		t.Fatal("unknown event field was accepted")
	}
	nonCanonical := handEvent.Message
	nonCanonical.Payload = append(append([]byte(nil), nonCanonical.Payload...), handEvent.Message.Payload...)
	if err := projection.ValidateEvent(nonCanonical); err == nil {
		t.Fatal("non-canonical event payload was accepted")
	}
}

func TestReplayRejectsTamperedLifecycle(t *testing.T) {
	m := module.New()
	created := createTransition(t, m, 4)
	state := decodeModuleState(t, created.Snapshot)
	settled, err := m.HandleCommand(created.Snapshot, commandRequest(t, 1, state.TargetUserID, projection.ActionStand,
		&meetv1.Command{Command: &meetv1.Command_Stand{Stand: &meetv1.Stand{}}}))
	if err != nil {
		t.Fatal(err)
	}
	history := append(append([]game.Event(nil), created.Events...), settled.Events...)

	t.Run("event before initialization", func(t *testing.T) {
		if _, err := projection.BuildReplay(created.Events[1:], replayViewer(), game.ReplayAccessParticipant); err == nil {
			t.Fatal("event stream without round.start was accepted")
		}
	})
	t.Run("settlement targets another round", func(t *testing.T) {
		tampered := cloneEvents(history)
		for index := range tampered {
			if tampered[index].Message.MessageType != module.EventRoundSettledMessage {
				continue
			}
			var value meetv1.RoundSettled
			if err := proto.Unmarshal(tampered[index].Message.Payload, &value); err != nil {
				t.Fatal(err)
			}
			value.Summary.Round++
			tampered[index].Message.Payload = marshal(t, &value)
			break
		}
		if _, err := projection.BuildReplay(tampered, replayViewer(), game.ReplayAccessParticipant); err == nil {
			t.Fatal("cross-round settlement was accepted")
		}
	})
	t.Run("later round repeats initialization", func(t *testing.T) {
		tampered := cloneEvents(history)
		for index := range tampered {
			if tampered[index].Message.MessageType != module.EventRoundStartedMessage {
				continue
			}
			var value meetv1.RoundStarted
			if err := proto.Unmarshal(tampered[index].Message.Payload, &value); err != nil {
				t.Fatal(err)
			}
			if value.GetRound() == 2 {
				value.Config = configProto(engine.DefaultConfig())
				tampered[index].Message.Payload = marshal(t, &value)
				break
			}
		}
		if _, err := projection.BuildReplay(tampered, replayViewer(), game.ReplayAccessParticipant); err == nil {
			t.Fatal("later round initialization was accepted")
		}
	})
	t.Run("unknown message type", func(t *testing.T) {
		tampered := cloneEvents(history)
		tampered[1].Message.MessageType = "unknown.event"
		if _, err := projection.BuildReplay(tampered, replayViewer(), game.ReplayAccessParticipant); err == nil {
			t.Fatal("unknown event type was accepted")
		}
	})
}

func createTransition(t *testing.T, m *module.Module, count int) game.Transition {
	t.Helper()
	config, err := module.EncodeConfigForPlayers(engine.DefaultConfig(), count)
	if err != nil {
		t.Fatal(err)
	}
	participants := make([]game.Participant, count)
	for index := range participants {
		participants[index] = game.Participant{UserID: game.Identifier("user-" + string(rune('1'+index))), SeatIndex: uint32(index)}
	}
	transition, err := m.Create(game.CreateRequest{
		Context:      contextAt(replayNow()),
		StartContext: game.SessionStartContext{HostUserID: participants[0].UserID, StartingSeat: participants[0].SeatIndex},
		Participants: participants, Config: config,
	})
	if err != nil {
		t.Fatal(err)
	}
	return transition
}

func commandRequest(t *testing.T, version uint64, actor string, messageType game.Identifier, value proto.Message) game.CommandRequest {
	t.Helper()
	return game.CommandRequest{
		Context: contextAt(replayNow().Add(time.Second)), ActorUserID: game.Identifier(actor),
		ActionID: game.ActionID("AAAAAAAAAAAAAAAAAAAAAA"), ExpectedStateVersion: version,
		Command: game.Message{MessageType: messageType, SchemaVersion: module.ProtocolSchemaVersion, Payload: marshal(t, value)},
	}
}

func systemRequest(t *testing.T, version uint64, messageType game.Identifier, value proto.Message) game.SystemRequest {
	t.Helper()
	return game.SystemRequest{
		Context: contextAt(replayNow().Add(time.Second)), SystemOperationID: game.ActionID("AQEBAQEBAQEBAQEBAQEBAQ"),
		SourceEventID: "source-event", ExpectedStateVersion: version,
		System: game.Message{MessageType: messageType, SchemaVersion: module.ProtocolSchemaVersion, Payload: marshal(t, value)},
	}
}

func contextAt(now time.Time) game.DeterministicContext {
	return game.DeterministicContext{Now: now.Round(0).UTC(), RandomSeed: [32]byte{1}}
}

func replayNow() time.Time {
	return time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
}

func replayViewer() game.Viewer {
	return game.Viewer{Kind: game.ViewerReplay, UserID: "user-1"}
}

func decodeModuleState(t *testing.T, snapshot game.Snapshot) engine.State {
	t.Helper()
	state, err := module.DecodeState(snapshot.State)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func firstOtherUser(state engine.State, excluded string) string {
	for _, player := range state.Players {
		if player.Active && player.UserID != excluded {
			return player.UserID
		}
	}
	return ""
}

func findMessage(t *testing.T, events []game.Event, messageType game.Identifier) game.Event {
	t.Helper()
	for _, event := range events {
		if event.Message.MessageType == messageType {
			return event
		}
	}
	t.Fatalf("event %s is missing", messageType)
	return game.Event{}
}

func assertRevealExcludes(t *testing.T, events []game.Event, excluded string) {
	t.Helper()
	found := false
	for _, event := range events {
		if event.Message.MessageType != module.EventDiceRevealedMessage {
			continue
		}
		found = true
		var value meetv1.DiceRevealed
		if err := proto.Unmarshal(event.Message.Payload, &value); err != nil {
			t.Fatal(err)
		}
		for _, publicDice := range value.GetPublicDice() {
			if publicDice.GetUserId() == excluded {
				t.Fatalf("revoked player leaked into reveal: %+v", &value)
			}
		}
	}
	if !found {
		t.Fatal("target reroll emitted no dice reveal")
	}
}

func eventRound(value *meetv1.Event) uint32 {
	switch {
	case value.GetRoundStarted() != nil:
		return value.GetRoundStarted().GetRound()
	case value.GetDiceRevealed() != nil:
		return value.GetDiceRevealed().GetRound()
	case value.GetHandClassified() != nil:
		return value.GetHandClassified().GetRound()
	case value.GetMatchResolved() != nil:
		return value.GetMatchResolved().GetRound()
	case value.GetTargetSelected() != nil:
		return value.GetTargetSelected().GetRound()
	case value.GetTargetRerolled() != nil:
		return value.GetTargetRerolled().GetRound()
	case value.GetPenaltyRecorded() != nil:
		return value.GetPenaltyRecorded().GetRound()
	case value.GetParticipantRevoked() != nil:
		return value.GetParticipantRevoked().GetRound()
	case value.GetSessionFinished() != nil:
		return value.GetSessionFinished().GetRound()
	case value.GetSpecial_235Evaluated() != nil:
		return value.GetSpecial_235Evaluated().GetRound()
	case value.GetRoundSettled() != nil:
		return value.GetRoundSettled().GetSummary().GetRound()
	default:
		return 0
	}
}

func cloneEvents(values []game.Event) []game.Event {
	clones := make([]game.Event, len(values))
	copy(clones, values)
	for index := range clones {
		clones[index].Message.Payload = append([]byte(nil), clones[index].Message.Payload...)
	}
	return clones
}

func marshal(t *testing.T, value proto.Message) []byte {
	t.Helper()
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func configProto(value engine.Config) *meetv1.Config {
	return &meetv1.Config{
		Straight_123: value.Straight123, Straight_234: value.Straight234,
		Straight_345: value.Straight345, Straight_456: value.Straight456,
		Special_235Enabled: value.Special235Enabled, OnesWild: value.OnesWild,
		TargetPenaltyTicks: uint32(value.TargetPenaltyTicks), RerollPenaltyTicks: uint32(value.RerollPenaltyTicks),
		MatchPenaltyTicks: uint32(value.MatchPenaltyTicks), WeakExtraPenaltyTicks: uint32(value.WeakExtraPenaltyTicks),
		TargetRerollLimit: value.TargetRerollLimit, MatchResolutionLimit: value.MatchResolutionLimit,
		ActionTimeoutSeconds: value.ActionTimeoutSeconds,
	}
}
