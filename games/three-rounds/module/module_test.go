package module

import (
	"testing"
	"time"

	"github.com/iFTY-R/game-night/games/three-rounds/engine"
	threeroundsv1 "github.com/iFTY-R/game-night/games/three-rounds/gen/go/game/three_rounds/v1"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

func TestCreateLifecycleAndReplay(t *testing.T) {
	module := New()
	created, err := module.Create(createRequest(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	allEvents := append([]game.Event(nil), created.Events...)
	state := decodeState(t, created.Snapshot)
	if state.Phase != engine.PhaseRoundOneSelecting || state.CurrentRound != 1 || len(created.Timers) != 1 {
		t.Fatalf("created state=%+v timers=%d", state, len(created.Timers))
	}

	latest := created
	for _, player := range decodeState(t, latest.Snapshot).Players {
		latest = submitSelection(t, module, latest.Snapshot, player.UserID, 1, player.RemainingHand[:1])
		allEvents = append(allEvents, latest.Events...)
	}
	roundOne := decodeState(t, latest.Snapshot)
	if roundOne.Phase != engine.PhaseRoundOneResult || len(roundOne.RoundHistory) != 1 {
		t.Fatalf("round-one state=%+v", roundOne)
	}

	latest = fireTimer(t, module, latest.Snapshot, latest.Timers[0], time.Date(2026, 7, 23, 12, 0, 40, 0, time.UTC))
	allEvents = append(allEvents, latest.Events...)
	roundTwoSelecting := decodeState(t, latest.Snapshot)
	if roundTwoSelecting.Phase != engine.PhaseRoundTwoSelecting || roundTwoSelecting.CurrentRound != 2 {
		t.Fatalf("round-two selecting=%+v", roundTwoSelecting)
	}
	for _, player := range roundTwoSelecting.Players {
		latest = submitSelection(t, module, latest.Snapshot, player.UserID, 2, player.RemainingHand[:2])
		allEvents = append(allEvents, latest.Events...)
	}
	roundTwo := decodeState(t, latest.Snapshot)
	if roundTwo.Phase != engine.PhaseRoundTwoResult || len(roundTwo.RoundHistory) != 2 {
		t.Fatalf("round-two state=%+v", roundTwo)
	}

	latest = fireTimer(t, module, latest.Snapshot, latest.Timers[0], time.Date(2026, 7, 23, 12, 1, 0, 0, time.UTC))
	allEvents = append(allEvents, latest.Events...)
	roundThree := decodeState(t, latest.Snapshot)
	if roundThree.Phase != engine.PhaseRoundThreeResult || len(roundThree.RoundHistory) != 3 {
		t.Fatalf("round-three state=%+v", roundThree)
	}
	roundThreeView, err := module.Project(latest.Snapshot, game.Viewer{Kind: game.ViewerPlayer, UserID: "user-1", SeatIndex: 0})
	if err != nil {
		t.Fatal(err)
	}
	var roundThreeProjected threeroundsv1.View
	if err := unmarshalStrict(roundThreeView.View.Payload, &roundThreeProjected); err != nil {
		t.Fatal(err)
	}
	if roundThreeProjected.GetFinalSummary() != nil || roundThreeProjected.GetPublicPlayers()[0].GetFinalRank() != 0 {
		t.Fatalf("round-three view leaked final standings=%+v", &roundThreeProjected)
	}
	latest = fireTimer(t, module, latest.Snapshot, latest.Timers[0], time.Date(2026, 7, 23, 12, 1, 10, 0, time.UTC))
	allEvents = append(allEvents, latest.Events...)
	finalResult := decodeState(t, latest.Snapshot)
	if finalResult.Phase != engine.PhaseFinalResult || len(finalResult.FinalSummary.Standings) == 0 {
		t.Fatalf("final-result state=%+v", finalResult)
	}
	latest = fireTimer(t, module, latest.Snapshot, latest.Timers[0], time.Date(2026, 7, 23, 12, 1, 25, 0, time.UTC))
	allEvents = append(allEvents, latest.Events...)
	finished := decodeState(t, latest.Snapshot)
	if finished.Phase != engine.PhaseFinished || finished.FinishReason != engine.FinishNormalCompleted {
		t.Fatalf("finished state=%+v", finished)
	}
	for _, player := range finished.Players {
		if len(player.InitialHand) != 6 {
			t.Fatalf("player=%s initial hand=%d", player.UserID, len(player.InitialHand))
		}
		current := len(player.RemainingHand) + len(player.RoundOneCards) + len(player.RoundTwoCards) + len(player.RoundThreeCards)
		if current != 6 {
			t.Fatalf("player=%s current card conservation=%d", player.UserID, current)
		}
	}

	replay, err := module.ProjectReplay(allEvents, game.Viewer{Kind: game.ViewerReplay, UserID: "viewer", SeatIndex: 0}, game.ReplayAccessRoomMember)
	if err != nil {
		t.Fatal(err)
	}
	var replayView threeroundsv1.Replay
	if err := unmarshalStrict(replay.View.Payload, &replayView); err != nil {
		t.Fatal(err)
	}
	if replayView.GetFinishReason() != threeroundsv1.FinishReason_FINISH_REASON_NORMAL_COMPLETED || replayView.GetFinalSummary() == nil {
		t.Fatalf("replay=%+v", &replayView)
	}
	for _, player := range replayView.GetPlayers() {
		if len(player.GetInitialHand()) != 6 {
			t.Fatalf("replay player initial hand=%v", player)
		}
	}
}

func TestHandleCommandRejectsSessionFinishAndHandleSystemUsesRequestedByUserID(t *testing.T) {
	module := New()
	created, err := module.Create(createRequest(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	command := game.CommandRequest{
		Context:              contextAt(time.Date(2026, 7, 23, 12, 0, 2, 0, time.UTC)),
		ActorUserID:          "user-1",
		ActionID:             game.ActionID("AAAAAAAAAAAAAAAAAAAAAA"),
		ExpectedStateVersion: created.Snapshot.StateVersion,
		Command:              game.Message{MessageType: SystemFinishMessage, SchemaVersion: ProtocolSchemaVersion},
	}
	if _, err := module.HandleCommand(created.Snapshot, command); engine.ErrorCodeOf(err) != engine.CodeMalformedPayload {
		t.Fatalf("session.finish command error=%v", err)
	}
	request := game.SystemRequest{
		Context:              contextAt(time.Date(2026, 7, 23, 12, 0, 3, 0, time.UTC)),
		SystemOperationID:    game.ActionID("AQEBAQEBAQEBAQEBAQEBAQ"),
		SourceEventID:        "source-event",
		RequestedByUserID:    "user-1",
		ExpectedStateVersion: created.Snapshot.StateVersion,
		System:               game.Message{MessageType: SystemFinishMessage, SchemaVersion: ProtocolSchemaVersion},
	}
	finished, err := module.HandleSystem(created.Snapshot, request)
	if err != nil {
		t.Fatal(err)
	}
	if !finished.Finished || decodeState(t, finished.Snapshot).FinishReason != engine.FinishHostRequested {
		t.Fatalf("host finish transition=%+v", finished)
	}
	request.System.Payload = []byte{0x01}
	if _, err := module.HandleSystem(created.Snapshot, request); engine.ErrorCodeOf(err) != engine.CodeMalformedPayload {
		t.Fatalf("non-empty finish payload error=%v", err)
	}
	request.System.Payload = nil
	request.RequestedByUserID = ""
	if _, err := module.HandleSystem(created.Snapshot, request); engine.ErrorCodeOf(err) != engine.CodeMalformedPayload {
		t.Fatalf("operator-less finish error=%v", err)
	}
}

func TestRevocationRemovesPendingSelectionAndPreservesPrivacy(t *testing.T) {
	module := New()
	created, err := module.Create(createRequest(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	state := decodeState(t, created.Snapshot)
	first := submitSelection(t, module, created.Snapshot, state.Players[0].UserID, 1, state.Players[0].RemainingHand[:1])
	second := submitSelection(t, module, first.Snapshot, state.Players[2].UserID, 1, state.Players[2].RemainingHand[:1])
	revoked, err := module.HandleSystem(second.Snapshot, game.SystemRequest{
		Context:              contextAt(time.Date(2026, 7, 23, 12, 0, 4, 0, time.UTC)),
		SystemOperationID:    game.ActionID("AQEBAQEBAQEBAQEBAQEBAQ"),
		SourceEventID:        "source-event",
		ExpectedStateVersion: second.Snapshot.StateVersion,
		System:               revocationMessage(t, state.Players[1].UserID),
	})
	if err != nil {
		t.Fatal(err)
	}
	next := decodeState(t, revoked.Snapshot)
	if next.Phase != engine.PhaseRoundOneResult || len(next.RoundHistory) != 1 {
		t.Fatalf("revoked state=%+v", next)
	}
	reveals := next.RoundHistory[0].Reveals
	if len(reveals) != 2 {
		t.Fatalf("reveals=%+v", reveals)
	}
	for _, reveal := range reveals {
		if reveal.UserID == state.Players[1].UserID {
			t.Fatalf("revoked player leaked into reveal=%+v", reveal)
		}
	}
	view, err := module.Project(revoked.Snapshot, game.Viewer{Kind: game.ViewerSpectator, UserID: "spectator", SeatIndex: 0})
	if err != nil {
		t.Fatal(err)
	}
	var spectator threeroundsv1.View
	if err := unmarshalStrict(view.View.Payload, &spectator); err != nil {
		t.Fatal(err)
	}
	if spectator.GetSelf() != nil {
		t.Fatal("spectator view exposed a private self payload")
	}
}

func TestAdjustResumedShiftsDeadlineInStateAndTimerToken(t *testing.T) {
	module := New()
	created, err := module.Create(createRequest(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	shiftedTimers := append([]game.TimerIntent(nil), created.Timers...)
	shiftedTimers[0].Message = shiftedTimers[0].Message.Clone()
	shiftedTimers[0].DueAt = shiftedTimers[0].DueAt.Add(30 * time.Second)
	adjustedSnapshot, adjustedTimers, err := module.AdjustResumed(created.Snapshot, shiftedTimers)
	if err != nil {
		t.Fatal(err)
	}
	state := decodeState(t, adjustedSnapshot)
	if state.PhaseDeadlineUnixMillis != shiftedTimers[0].DueAt.UnixMilli() {
		t.Fatalf("state deadline=%d want=%d", state.PhaseDeadlineUnixMillis, shiftedTimers[0].DueAt.UnixMilli())
	}
	var timer threeroundsv1.Timer
	if err := unmarshalStrict(adjustedTimers[0].Message.Payload, &timer); err != nil {
		t.Fatal(err)
	}
	if timer.GetDeadlineUnixMillis() != shiftedTimers[0].DueAt.UnixMilli() {
		t.Fatalf("timer deadline=%d want=%d", timer.GetDeadlineUnixMillis(), shiftedTimers[0].DueAt.UnixMilli())
	}
	if _, err := module.HandleTimer(adjustedSnapshot, game.TimerRequest{
		Context: contextAt(shiftedTimers[0].DueAt), TimerID: adjustedTimers[0].TimerID,
		ExpectedStateVersion: adjustedSnapshot.StateVersion, Timer: adjustedTimers[0].Message,
	}); err != nil {
		t.Fatalf("adjusted timer error=%v", err)
	}
}

func TestProjectReplayV2CancelledHidesUnrevealedHands(t *testing.T) {
	module := New()
	created, err := module.Create(createRequest(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	projected, err := module.ProjectReplayV2(game.ReplayRequest{
		Events: created.Events,
		Viewer: game.Viewer{Kind: game.ViewerReplay, UserID: "viewer", SeatIndex: 0},
		Policy: game.ReplayAccessRoomMember,
		TerminalMeta: game.ReplayTerminalMeta{
			Cancelled:    true,
			EndedAt:      time.Date(2026, 7, 23, 12, 0, 10, 0, time.UTC),
			CancelReason: "runtime_cancelled",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var replay threeroundsv1.Replay
	if err := unmarshalStrict(projected.View.Payload, &replay); err != nil {
		t.Fatal(err)
	}
	if replay.GetFinalSummary() != nil {
		t.Fatalf("cancelled replay exposed final summary=%+v", replay.GetFinalSummary())
	}
	for _, player := range replay.GetPlayers() {
		if len(player.GetInitialHand()) != 0 {
			t.Fatalf("cancelled replay leaked initial hand=%v", player)
		}
	}
}

func TestProjectReplayV2CancelledAfterFinalResultKeepsStandingsButClearsWinners(t *testing.T) {
	module := New()
	created, err := module.Create(createRequest(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	allEvents := append([]game.Event(nil), created.Events...)
	latest := created
	for _, player := range decodeState(t, latest.Snapshot).Players {
		latest = submitSelection(t, module, latest.Snapshot, player.UserID, 1, player.RemainingHand[:1])
		allEvents = append(allEvents, latest.Events...)
	}
	latest = fireTimer(t, module, latest.Snapshot, latest.Timers[0], time.Date(2026, 7, 23, 12, 0, 40, 0, time.UTC))
	allEvents = append(allEvents, latest.Events...)
	for _, player := range decodeState(t, latest.Snapshot).Players {
		latest = submitSelection(t, module, latest.Snapshot, player.UserID, 2, player.RemainingHand[:2])
		allEvents = append(allEvents, latest.Events...)
	}
	latest = fireTimer(t, module, latest.Snapshot, latest.Timers[0], time.Date(2026, 7, 23, 12, 1, 0, 0, time.UTC))
	allEvents = append(allEvents, latest.Events...)
	latest = fireTimer(t, module, latest.Snapshot, latest.Timers[0], time.Date(2026, 7, 23, 12, 1, 10, 0, time.UTC))
	allEvents = append(allEvents, latest.Events...)

	projected, err := module.ProjectReplayV2(game.ReplayRequest{
		Events: allEvents,
		Viewer: game.Viewer{Kind: game.ViewerReplay, UserID: "viewer", SeatIndex: 0},
		Policy: game.ReplayAccessRoomMember,
		TerminalMeta: game.ReplayTerminalMeta{
			Cancelled:    true,
			EndedAt:      time.Date(2026, 7, 23, 12, 1, 12, 0, time.UTC),
			CancelReason: "runtime_cancelled",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var replay threeroundsv1.Replay
	if err := unmarshalStrict(projected.View.Payload, &replay); err != nil {
		t.Fatal(err)
	}
	if replay.GetFinalSummary() == nil || len(replay.GetFinalSummary().GetStandings()) == 0 {
		t.Fatalf("cancelled final-result replay lost standings=%+v", &replay)
	}
	if len(replay.GetFinalSummary().GetWinnerUserIds()) != 0 {
		t.Fatalf("cancelled final-result replay leaked winners=%+v", replay.GetFinalSummary())
	}
	for _, standing := range replay.GetFinalSummary().GetStandings() {
		if standing.GetWinner() {
			t.Fatalf("cancelled final-result replay leaked winner flag=%+v", standing)
		}
	}
	for _, player := range replay.GetPlayers() {
		if len(player.GetInitialHand()) != 0 {
			t.Fatalf("cancelled final-result replay leaked initial hand=%v", player)
		}
	}
}

func TestStrictPayloadsRejectUnknownAndNonCanonicalState(t *testing.T) {
	config, err := EncodeConfigForPlayers(engine.DefaultConfig(), 3)
	if err != nil {
		t.Fatal(err)
	}
	unknownConfig := config
	unknownConfig.Payload = append(append([]byte(nil), config.Payload...), 0x28, 0x01)
	if _, err := DecodeConfig(unknownConfig, 3); err == nil {
		t.Fatal("config with unknown field was accepted")
	}
	module := New()
	created, err := module.Create(createRequest(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	var command threeroundsv1.Command
	payload, err := marshalDeterministic(&threeroundsv1.Command{
		Command: &threeroundsv1.Command_SubmitSelection{SubmitSelection: &threeroundsv1.SubmitSelection{Round: 1, CardIds: []string{"2D"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := unmarshalStrict(payload, &command); err != nil {
		t.Fatal(err)
	}
	if err := unmarshalStrict(append(payload, 0x28, 0x01), &threeroundsv1.Command{}); err == nil {
		t.Fatal("command with unknown field was accepted")
	}
	stateMessage := created.Snapshot.State
	stateMessage.Payload = append(append([]byte(nil), stateMessage.Payload...), stateMessage.Payload...)
	if _, err := DecodeState(stateMessage); err == nil {
		t.Fatal("non-canonical state payload was accepted")
	}
}

func createRequest(t *testing.T, count int) game.CreateRequest {
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
		Context:      contextAt(time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)),
		StartContext: game.SessionStartContext{HostUserID: participants[0].UserID, StartingSeat: participants[0].SeatIndex},
		Participants: participants,
		Config:       config,
	}
}

func contextAt(now time.Time) game.DeterministicContext {
	return game.DeterministicContext{Now: now.Round(0).UTC(), RandomSeed: [32]byte{1}}
}

func decodeState(t *testing.T, snapshot game.Snapshot) engine.State {
	t.Helper()
	state, err := DecodeState(snapshot.State)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func submitSelection(t *testing.T, module *Module, snapshot game.Snapshot, actor string, round uint32, cards []engine.CardID) game.Transition {
	t.Helper()
	command := &threeroundsv1.Command{
		Command: &threeroundsv1.Command_SubmitSelection{
			SubmitSelection: &threeroundsv1.SubmitSelection{Round: round, CardIds: cardIDsToStrings(cards)},
		},
	}
	payload, err := marshalDeterministic(command)
	if err != nil {
		t.Fatal(err)
	}
	transition, err := module.HandleCommand(snapshot, game.CommandRequest{
		Context:              contextAt(time.Date(2026, 7, 23, 12, 0, 1, 0, time.UTC)),
		ActorUserID:          game.Identifier(actor),
		ActionID:             game.ActionID("AAAAAAAAAAAAAAAAAAAAAA"),
		ExpectedStateVersion: snapshot.StateVersion,
		Command:              game.Message{MessageType: CommandSubmitSelectionMessage, SchemaVersion: ProtocolSchemaVersion, Payload: payload},
	})
	if err != nil {
		t.Fatal(err)
	}
	return transition
}

func fireTimer(t *testing.T, module *Module, snapshot game.Snapshot, timer game.TimerIntent, now time.Time) game.Transition {
	t.Helper()
	transition, err := module.HandleTimer(snapshot, game.TimerRequest{
		Context:              contextAt(now),
		TimerID:              timer.TimerID,
		ExpectedStateVersion: snapshot.StateVersion,
		Timer:                timer.Message,
	})
	if err != nil {
		t.Fatal(err)
	}
	return transition
}

func revocationMessage(t *testing.T, userID string) game.Message {
	t.Helper()
	payload, err := marshalDeterministic(&threeroundsv1.ParticipantRevoked{UserId: userID})
	if err != nil {
		t.Fatal(err)
	}
	return game.Message{MessageType: EventParticipantRevokedMessage, SchemaVersion: ProtocolSchemaVersion, Payload: payload}
}
