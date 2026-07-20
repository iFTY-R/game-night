package module

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/iFTY-R/game-night/games/dice-789/engine"
	dice789v1 "github.com/iFTY-R/game-night/games/dice-789/gen/go/game/dice789/v1"
	"github.com/iFTY-R/game-night/games/dice-789/projection"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"google.golang.org/protobuf/proto"
)

func TestModuleCreateRoundTripProjectionAndTimer(t *testing.T) {
	m := New()
	created, err := m.Create(testCreateRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if created.Snapshot.StateVersion != 1 || len(created.Events) != 1 || len(created.Timers) != 1 || created.Finished {
		t.Fatalf("unexpected create transition: %+v", created)
	}
	state, err := DecodeState(created.Snapshot.State)
	if err != nil {
		t.Fatal(err)
	}
	if state.HostUserID != "user-1" || state.CurrentUserID != "user-1" || state.Phase != engine.PhaseAwaitingRoll {
		t.Fatalf("unexpected state: %+v", state)
	}
	reencoded, err := EncodeState(state)
	if err != nil || !bytes.Equal(reencoded.Payload, created.Snapshot.State.Payload) {
		t.Fatalf("state round-trip is not deterministic: %v", err)
	}
	player, err := m.Project(created.Snapshot, game.Viewer{Kind: game.ViewerPlayer, UserID: "user-1", SeatIndex: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(player.AllowedActions) != 1 || player.AllowedActions[0] != projection.ActionRoll {
		t.Fatalf("unexpected player actions: %v", player.AllowedActions)
	}
	spectator, err := m.Project(created.Snapshot, game.Viewer{Kind: game.ViewerSpectator, UserID: "viewer-1"})
	if err != nil || len(spectator.AllowedActions) != 0 {
		t.Fatalf("spectator received actions: actions=%v error=%v", spectator.AllowedActions, err)
	}
}

func TestResultPendingActionsBelongOnlyToHost(t *testing.T) {
	m := New()
	created, err := m.Create(testCreateRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	rolled, err := m.HandleCommand(created.Snapshot, commandRequest(t, created.Snapshot.StateVersion, "user-1", "AAAAAAAAAAAAAAAAAAAAAA", projection.ActionRoll, &dice789v1.Command{Command: &dice789v1.Command_Roll{Roll: &dice789v1.Roll{}}}))
	if err != nil {
		t.Fatal(err)
	}
	host, err := m.Project(rolled.Snapshot, game.Viewer{Kind: game.ViewerPlayer, UserID: "user-1", SeatIndex: 0})
	if err != nil || len(host.AllowedActions) != 2 {
		t.Fatalf("host actions were not projected: %v %v", host.AllowedActions, err)
	}
	other, err := m.Project(rolled.Snapshot, game.Viewer{Kind: game.ViewerPlayer, UserID: "user-2", SeatIndex: 1})
	if err != nil || len(other.AllowedActions) != 0 {
		t.Fatalf("non-host received confirmation actions: %v %v", other.AllowedActions, err)
	}
	confirmed, err := m.HandleCommand(rolled.Snapshot, commandRequest(t, rolled.Snapshot.StateVersion, "user-1", "AgICAgICAgICAgICAgICAg", projection.ActionConfirmLanded, &dice789v1.Command{Command: &dice789v1.Command_ConfirmLanded{ConfirmLanded: &dice789v1.ConfirmLanded{}}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeState(confirmed.Snapshot.State); err != nil {
		t.Fatalf("confirmed transition did not persist a canonical state: %v", err)
	}
	history := append(append(append([]game.Event(nil), created.Events...), rolled.Events...), confirmed.Events...)
	if _, err := m.ProjectReplay(history, game.Viewer{Kind: game.ViewerReplay, UserID: "user-1"}, game.ReplayAccessParticipant); err != nil {
		t.Fatalf("confirmed public events did not form a valid replay: %v", err)
	}
}

func TestReplayPreservesAppliedResolutionCause(t *testing.T) {
	tests := []struct {
		name    string
		timeout bool
		cause   dice789v1.ResolutionCause
	}{
		{name: "host confirmation", cause: dice789v1.ResolutionCause_RESOLUTION_CAUSE_HOST_CONFIRMED},
		{name: "result timeout", timeout: true, cause: dice789v1.ResolutionCause_RESOLUTION_CAUSE_TIMEOUT},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := New()
			created, err := m.Create(testCreateRequest(t))
			if err != nil {
				t.Fatal(err)
			}
			rolled := rollTransitionForResult(t, m, created.Snapshot, engine.ResultDoubleFour)

			var resolved game.Transition
			if test.timeout {
				if len(rolled.Timers) != 1 {
					t.Fatalf("result pending timer count = %d", len(rolled.Timers))
				}
				resolved, err = m.HandleTimer(rolled.Snapshot, game.TimerRequest{
					Context:              testContext(time.Hour),
					TimerID:              rolled.Timers[0].TimerID,
					ExpectedStateVersion: rolled.Snapshot.StateVersion,
					Timer:                rolled.Timers[0].Message,
				})
			} else {
				resolved, err = m.HandleCommand(rolled.Snapshot, commandRequest(t, rolled.Snapshot.StateVersion, "user-1", "AgICAgICAgICAgICAgICAg", projection.ActionConfirmLanded, &dice789v1.Command{
					Command: &dice789v1.Command_ConfirmLanded{ConfirmLanded: &dice789v1.ConfirmLanded{}},
				}))
			}
			if err != nil {
				t.Fatal(err)
			}

			history := append(append(append([]game.Event(nil), created.Events...), rolled.Events...), resolved.Events...)
			projected, err := m.ProjectReplay(history, game.Viewer{Kind: game.ViewerReplay, UserID: "user-1"}, game.ReplayAccessParticipant)
			if err != nil {
				t.Fatal(err)
			}
			var replay dice789v1.Replay
			if err := unmarshalStrict(projected.View.Payload, &replay); err != nil {
				t.Fatal(err)
			}
			if len(replay.GetTurns()) != 1 || replay.GetTurns()[0].GetSummary().GetCause() != test.cause {
				t.Fatalf("turn summary did not preserve %s: %+v", test.cause, replay.GetTurns())
			}

			seenEffect, seenPool, seenPenalty, seenSettlement := false, false, false, false
			seenNextTurn := false
			for _, entry := range replay.GetEntries() {
				event := entry.GetEvent()
				switch {
				case event.GetTurnStarted() != nil && event.GetTurnStarted().GetTurn() == 2:
					seenNextTurn = true
					if event.GetTurnStarted().GetCause() != test.cause {
						t.Fatalf("next turn cause = %s, want %s", event.GetTurnStarted().GetCause(), test.cause)
					}
				case event.GetEffectSelected() != nil:
					seenEffect = true
					if event.GetEffectSelected().GetCause() != test.cause || event.GetEffectSelected().GetNextPhase() != dice789v1.Phase_PHASE_AWAITING_ROLL {
						t.Fatalf("effect.selected cause = %s, want %s", event.GetEffectSelected().GetCause(), test.cause)
					}
				case event.GetPoolChanged() != nil:
					seenPool = true
					if event.GetPoolChanged().GetCause() != test.cause {
						t.Fatalf("pot.changed cause = %s, want %s", event.GetPoolChanged().GetCause(), test.cause)
					}
				case event.GetPenaltyRecorded() != nil:
					seenPenalty = true
					penalty := event.GetPenaltyRecorded()
					if penalty.GetCause() != test.cause || penalty.GetAfterTotalTicks()-penalty.GetBeforeTotalTicks() != penalty.GetTicks() {
						t.Fatalf("penalty.recorded cause = %s, want %s", event.GetPenaltyRecorded().GetCause(), test.cause)
					}
				case event.GetTurnSettled() != nil:
					seenSettlement = true
					if event.GetTurnSettled().GetCause() != test.cause {
						t.Fatalf("turn.settled cause = %s, want %s", event.GetTurnSettled().GetCause(), test.cause)
					}
				}
			}
			if !seenEffect || !seenPool || !seenPenalty || !seenSettlement || !seenNextTurn {
				t.Fatalf("applied fact coverage: effect=%t pool=%t penalty=%t settlement=%t next=%t", seenEffect, seenPool, seenPenalty, seenSettlement, seenNextTurn)
			}
		})
	}
}

func TestTargetTimeoutCauseAppliesToWholeBatch(t *testing.T) {
	m := New()
	created, err := m.Create(testCreateRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	rolled := rollTransitionForResult(t, m, created.Snapshot, engine.ResultDoubleOne)
	confirmed, err := m.HandleCommand(rolled.Snapshot, commandRequest(t, rolled.Snapshot.StateVersion, "user-1", "AgICAgICAgICAgICAgICAg", projection.ActionConfirmLanded, &dice789v1.Command{
		Command: &dice789v1.Command_ConfirmLanded{ConfirmLanded: &dice789v1.ConfirmLanded{}},
	}))
	if err != nil || len(confirmed.Timers) != 1 {
		t.Fatalf("confirmed=%+v err=%v", confirmed, err)
	}
	timedOut, err := m.HandleTimer(confirmed.Snapshot, game.TimerRequest{
		Context: testContext(time.Hour), TimerID: confirmed.Timers[0].TimerID,
		ExpectedStateVersion: confirmed.Snapshot.StateVersion, Timer: confirmed.Timers[0].Message,
	})
	if err != nil {
		t.Fatal(err)
	}
	history := append(append(append(append([]game.Event(nil), created.Events...), rolled.Events...), confirmed.Events...), timedOut.Events...)
	projected, err := m.ProjectReplay(history, game.Viewer{Kind: game.ViewerReplay, UserID: "user-1"}, game.ReplayAccessParticipant)
	if err != nil {
		t.Fatal(err)
	}
	var replay dice789v1.Replay
	if err := unmarshalStrict(projected.View.Payload, &replay); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, entry := range replay.GetEntries() {
		event := entry.GetEvent()
		causes := []dice789v1.ResolutionCause{}
		switch {
		case event.GetTargetSelected() != nil:
			causes = append(causes, event.GetTargetSelected().GetCause())
		case event.GetPoolChanged() != nil:
			causes = append(causes, event.GetPoolChanged().GetCause())
		case event.GetPenaltyRecorded() != nil:
			causes = append(causes, event.GetPenaltyRecorded().GetCause())
		case event.GetTurnSettled() != nil:
			causes = append(causes, event.GetTurnSettled().GetCause())
		case event.GetTurnStarted() != nil && event.GetTurnStarted().GetTurn() == 2:
			causes = append(causes, event.GetTurnStarted().GetCause())
		}
		for _, cause := range causes {
			seen++
			if cause != dice789v1.ResolutionCause_RESOLUTION_CAUSE_TIMEOUT {
				t.Fatalf("target timeout batch cause = %s", cause)
			}
		}
	}
	if seen != 5 {
		t.Fatalf("target timeout audited event count = %d", seen)
	}
}

func TestPlatformCancellationAndAppliedEffectFinish(t *testing.T) {
	m := New()
	tests := []struct {
		name    string
		applied bool
	}{
		{name: "pending public roll"},
		{name: "applied effect", applied: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			created, err := m.Create(testCreateRequest(t))
			if err != nil {
				t.Fatal(err)
			}
			rolled := rollTransitionForResult(t, m, created.Snapshot, engine.ResultEight)
			last := rolled
			history := append(append([]game.Event(nil), created.Events...), rolled.Events...)
			if test.applied {
				last, err = m.HandleCommand(rolled.Snapshot, commandRequest(t, rolled.Snapshot.StateVersion, "user-1", "AgICAgICAgICAgICAgICAg", projection.ActionConfirmLanded, &dice789v1.Command{
					Command: &dice789v1.Command_ConfirmLanded{ConfirmLanded: &dice789v1.ConfirmLanded{}},
				}))
				if err != nil {
					t.Fatal(err)
				}
				history = append(history, last.Events...)
			}
			cancelled, err := m.HandleSystem(last.Snapshot, systemRequest(t, last.Snapshot.StateVersion, SystemFinishMessage, &dice789v1.Command{
				Command: &dice789v1.Command_Finish{Finish: &dice789v1.Finish{Reason: engine.FinishPlatformCancelled}},
			}))
			if err != nil || !cancelled.Finished {
				t.Fatalf("cancelled=%+v err=%v", cancelled, err)
			}
			state, err := DecodeState(cancelled.Snapshot.State)
			if err != nil || state.FinishReason != engine.FinishPlatformCancelled {
				t.Fatalf("state=%+v err=%v", state, err)
			}
			history = append(history, cancelled.Events...)
			projected, err := m.ProjectReplay(history, game.Viewer{Kind: game.ViewerReplay, UserID: "user-1"}, game.ReplayAccessParticipant)
			if err != nil {
				t.Fatal(err)
			}
			var replay dice789v1.Replay
			if err := unmarshalStrict(projected.View.Payload, &replay); err != nil {
				t.Fatal(err)
			}
			finished := replay.GetEntries()[len(replay.GetEntries())-1].GetEvent().GetSessionFinished()
			if finished == nil || finished.GetCause() != dice789v1.ResolutionCause_RESOLUTION_CAUSE_PLATFORM_CANCELLED || finished.GetOperatorUserId() != "" {
				t.Fatalf("finish audit=%+v", finished)
			}
			if len(replay.GetTurns()) != 1 || replay.GetTurns()[0].GetSummary().GetOutcome() != dice789v1.TurnOutcome_TURN_OUTCOME_SESSION_FINISHED ||
				replay.GetTurns()[0].GetSummary().GetCause() != dice789v1.ResolutionCause_RESOLUTION_CAUSE_PLATFORM_CANCELLED {
				t.Fatalf("terminal replay summary=%+v", replay.GetTurns())
			}
			if test.applied {
				viewProjection, err := m.Project(cancelled.Snapshot, game.Viewer{Kind: game.ViewerPlayer, UserID: "user-1", SeatIndex: 0})
				if err != nil {
					t.Fatal(err)
				}
				var view dice789v1.View
				if err := unmarshalStrict(viewProjection.View.Payload, &view); err != nil {
					t.Fatal(err)
				}
				if view.GetLastSettlement().GetOutcome() != dice789v1.TurnOutcome_TURN_OUTCOME_SESSION_FINISHED ||
					view.GetLastSettlement().GetCause() != dice789v1.ResolutionCause_RESOLUTION_CAUSE_PLATFORM_CANCELLED {
					t.Fatalf("terminal live summary=%+v", view.GetLastSettlement())
				}
			}
		})
	}
}

func TestFinishAfterSevenAddFormsValidReplay(t *testing.T) {
	m := New()
	created, err := m.Create(testCreateRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	rolled := rollTransitionForResult(t, m, created.Snapshot, engine.ResultSeven)
	confirmed, err := m.HandleCommand(rolled.Snapshot, commandRequest(t, rolled.Snapshot.StateVersion, "user-1", "AgICAgICAgICAgICAgICAg", projection.ActionConfirmLanded, &dice789v1.Command{
		Command: &dice789v1.Command_ConfirmLanded{ConfirmLanded: &dice789v1.ConfirmLanded{}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	added, err := m.HandleCommand(confirmed.Snapshot, commandRequest(t, confirmed.Snapshot.StateVersion, "user-1", "AwMDAwMDAwMDAwMDAwMDAw", projection.ActionAdd, &dice789v1.Command{
		Command: &dice789v1.Command_AddToPool{AddToPool: &dice789v1.AddToPool{Ticks: 1}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	finished, err := m.HandleSystem(added.Snapshot, systemRequest(t, added.Snapshot.StateVersion, SystemFinishMessage, &dice789v1.Command{
		Command: &dice789v1.Command_Finish{Finish: &dice789v1.Finish{Reason: engine.FinishPlatformCancelled}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	history := append(append(append(append(append([]game.Event(nil), created.Events...), rolled.Events...), confirmed.Events...), added.Events...), finished.Events...)
	projected, err := m.ProjectReplay(history, game.Viewer{Kind: game.ViewerReplay, UserID: "user-1"}, game.ReplayAccessParticipant)
	if err != nil {
		t.Fatal(err)
	}
	var replay dice789v1.Replay
	if err := unmarshalStrict(projected.View.Payload, &replay); err != nil {
		t.Fatal(err)
	}
	if len(replay.GetTurns()) != 1 || replay.GetTurns()[0].GetSummary().GetOutcome() != dice789v1.TurnOutcome_TURN_OUTCOME_SESSION_FINISHED {
		t.Fatalf("seven-add terminal replay=%+v", replay.GetTurns())
	}
}

func TestRevocationEventRejectsContradictoryAuditFields(t *testing.T) {
	tests := map[string]*dice789v1.ParticipantRevoked{
		"missing phase": {UserId: "user-2", Turn: 1, SourceUserId: "user-1"},
		"cancel after applied effect": {
			UserId: "user-1", Turn: 1, PhaseBefore: dice789v1.Phase_PHASE_AWAITING_CONTINUE,
			Effect: dice789v1.Effect_EFFECT_SUM_EIGHT_HALF_POOL, SourceUserId: "user-1", PendingEffectCancelled: true,
		},
		"reopen without target": {
			UserId: "user-2", Turn: 1, PhaseBefore: dice789v1.Phase_PHASE_AWAITING_ADD,
			Effect: dice789v1.Effect_EFFECT_DOUBLE_SIX_TARGET_ADD, SourceUserId: "user-1", TargetSelectionReopened: true,
		},
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			payload, err := marshalDeterministic(value)
			if err != nil {
				t.Fatal(err)
			}
			_, err = decodeEvent(game.Message{MessageType: EventParticipantRevokedMessage, SchemaVersion: ProtocolSchemaVersion, Payload: payload})
			if engine.ErrorCodeOf(err) != engine.CodeMalformedPayload {
				t.Fatalf("contradictory revocation error=%v", err)
			}
		})
	}
}

func TestReplayRejectsRevocationContextTampering(t *testing.T) {
	m := New()
	created, err := m.Create(testCreateRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]*dice789v1.ParticipantRevoked{
		"fabricated pending phase": {
			UserId: "user-2", Turn: 1, PhaseBefore: dice789v1.Phase_PHASE_RESULT_PENDING,
			Effect: dice789v1.Effect_EFFECT_SUM_SEVEN_ADD, SourceUserId: "user-3",
		},
		"wrong current source": {
			UserId: "user-2", Turn: 1, PhaseBefore: dice789v1.Phase_PHASE_AWAITING_ROLL,
			SourceUserId: "user-3",
		},
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			payload, err := marshalDeterministic(value)
			if err != nil {
				t.Fatal(err)
			}
			event := game.Event{Message: game.Message{MessageType: EventParticipantRevokedMessage, SchemaVersion: ProtocolSchemaVersion, Payload: payload}}
			history := append(append([]game.Event(nil), created.Events...), event)
			if _, err := m.ProjectReplay(history, game.Viewer{Kind: game.ViewerReplay, UserID: "user-1"}, game.ReplayAccessParticipant); err == nil {
				t.Fatal("context-tampered revocation was accepted")
			}
		})
	}
}

func TestEffectSelectionRejectsPriorityAndPhaseTampering(t *testing.T) {
	m := New()
	created, err := m.Create(testCreateRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	rolled := rollTransitionForResult(t, m, created.Snapshot, engine.ResultSeven)
	confirmed, err := m.HandleCommand(rolled.Snapshot, commandRequest(t, rolled.Snapshot.StateVersion, "user-1", "AgICAgICAgICAgICAgICAg", projection.ActionConfirmLanded, &dice789v1.Command{
		Command: &dice789v1.Command_ConfirmLanded{ConfirmLanded: &dice789v1.ConfirmLanded{}},
	}))
	if err != nil || len(confirmed.Events) != 1 {
		t.Fatalf("confirmed=%+v err=%v", confirmed, err)
	}

	tests := map[string]func(*dice789v1.EffectSelected){
		"priority": func(value *dice789v1.EffectSelected) { value.RulePriority++ },
		"phase":    func(value *dice789v1.EffectSelected) { value.NextPhase = dice789v1.Phase_PHASE_AWAITING_CONTINUE },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			var selected dice789v1.EffectSelected
			if err := unmarshalStrict(confirmed.Events[0].Message.Payload, &selected); err != nil {
				t.Fatal(err)
			}
			mutate(&selected)
			payload, err := marshalDeterministic(&selected)
			if err != nil {
				t.Fatal(err)
			}
			altered := confirmed.Events[0]
			altered.Message.Payload = payload
			history := append(append(append([]game.Event(nil), created.Events...), rolled.Events...), altered)
			if _, err := m.ProjectReplay(history, game.Viewer{Kind: game.ViewerReplay, UserID: "user-1"}, game.ReplayAccessParticipant); err == nil {
				t.Fatal("tampered effect selection was accepted")
			}
		})
	}
}

func TestRevocationReplayPreservesPreTransitionContext(t *testing.T) {
	m := New()
	created, err := m.Create(testCreateRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	rolled := rollTransitionForResult(t, m, created.Snapshot, engine.ResultDoubleSix)
	confirmed, err := m.HandleCommand(rolled.Snapshot, commandRequest(t, rolled.Snapshot.StateVersion, "user-1", "AgICAgICAgICAgICAgICAg", projection.ActionConfirmLanded, &dice789v1.Command{
		Command: &dice789v1.Command_ConfirmLanded{ConfirmLanded: &dice789v1.ConfirmLanded{}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	chosen, err := m.HandleCommand(confirmed.Snapshot, commandRequest(t, confirmed.Snapshot.StateVersion, "user-1", "AwMDAwMDAwMDAwMDAwMDAw", projection.ActionChooseTarget, &dice789v1.Command{
		Command: &dice789v1.Command_ChooseTarget{ChooseTarget: &dice789v1.ChooseTarget{UserId: "user-2"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := m.HandleSystem(chosen.Snapshot, systemRequest(t, chosen.Snapshot.StateVersion, EventParticipantRevokedMessage, &dice789v1.ParticipantRevoked{UserId: "user-2"}))
	if err != nil || len(revoked.Events) != 1 {
		t.Fatalf("revoked=%+v err=%v", revoked, err)
	}
	decoded, err := decodeEvent(revoked.Events[0].Message)
	if err != nil {
		t.Fatal(err)
	}
	audit := decoded.ProtocolEvent.GetParticipantRevoked()
	if audit == nil || audit.GetPhaseBefore() != dice789v1.Phase_PHASE_AWAITING_ADD ||
		audit.GetEffect() != dice789v1.Effect_EFFECT_DOUBLE_SIX_TARGET_ADD || audit.GetSourceUserId() != "user-1" ||
		audit.GetTargetUserId() != "user-2" || !audit.GetTargetSelectionReopened() || audit.GetPendingEffectCancelled() {
		t.Fatalf("revocation audit=%+v", audit)
	}

	history := append(append(append(append(append([]game.Event(nil), created.Events...), rolled.Events...), confirmed.Events...), chosen.Events...), revoked.Events...)
	projected, err := m.ProjectReplay(history, game.Viewer{Kind: game.ViewerReplay, UserID: "user-1"}, game.ReplayAccessParticipant)
	if err != nil {
		t.Fatal(err)
	}
	var replay dice789v1.Replay
	if err := unmarshalStrict(projected.View.Payload, &replay); err != nil {
		t.Fatal(err)
	}
	lastEntry := replay.GetEntries()[len(replay.GetEntries())-1].GetEvent()
	if !proto.Equal(lastEntry, decoded.ProtocolEvent) || len(replay.GetTurns()) != 1 || replay.GetTurns()[0].GetSummary().GetTargetUserId() != "" {
		t.Fatalf("revocation replay entry=%+v turns=%+v", lastEntry, replay.GetTurns())
	}
}

func TestAwaitingRollSourceRevocationFormsValidReplay(t *testing.T) {
	m := New()
	created, err := m.Create(testCreateRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := m.HandleSystem(created.Snapshot, systemRequest(t, created.Snapshot.StateVersion, EventParticipantRevokedMessage, &dice789v1.ParticipantRevoked{UserId: "user-1"}))
	if err != nil {
		t.Fatal(err)
	}
	history := append(append([]game.Event(nil), created.Events...), revoked.Events...)
	projected, err := m.ProjectReplay(history, game.Viewer{Kind: game.ViewerReplay, UserID: "user-2"}, game.ReplayAccessParticipant)
	if err != nil {
		t.Fatal(err)
	}
	var replay dice789v1.Replay
	if err := unmarshalStrict(projected.View.Payload, &replay); err != nil {
		t.Fatal(err)
	}
	if len(replay.GetTurns()) != 1 || replay.GetTurns()[0].GetSummary().GetOutcome() != dice789v1.TurnOutcome_TURN_OUTCOME_SOURCE_REVOKED ||
		replay.GetTurns()[0].GetSummary().GetNextUserId() != "user-2" {
		t.Fatalf("awaiting-roll revocation replay=%+v", replay.GetTurns())
	}
}

func TestProjectEventsRejectsUnknownAndEmitsViewDelta(t *testing.T) {
	m := New()
	created, err := m.Create(testCreateRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	delta, err := m.ProjectEvents(created.Snapshot, []game.VersionedEvent{{StateVersion: 1, Event: created.Events[0]}}, game.Viewer{Kind: game.ViewerPlayer, UserID: "user-1", SeatIndex: 0})
	if err != nil || len(delta.Messages) != 1 || delta.Messages[0].MessageType != ViewDeltaMessageType {
		t.Fatalf("view delta failed: %+v %v", delta, err)
	}
	var value dice789v1.ViewDelta
	if err := unmarshalStrict(delta.Messages[0].Payload, &value); err != nil || value.GetView() == nil {
		t.Fatalf("view delta cannot be decoded: %v", err)
	}
	unknown := created.Events[0]
	unknown.Message.MessageType = "unknown.event"
	if _, err := m.ProjectEvents(created.Snapshot, []game.VersionedEvent{{StateVersion: 1, Event: unknown}}, game.Viewer{Kind: game.ViewerSpectator, UserID: "viewer-1"}); err == nil {
		t.Fatal("unknown event was accepted")
	}
}

func TestReplayKeepsPublicPendingTurn(t *testing.T) {
	m := New()
	created, err := m.Create(testCreateRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	rolled, err := m.HandleCommand(created.Snapshot, commandRequest(t, created.Snapshot.StateVersion, "user-1", "AAAAAAAAAAAAAAAAAAAAAA", projection.ActionRoll, &dice789v1.Command{Command: &dice789v1.Command_Roll{Roll: &dice789v1.Roll{}}}))
	if err != nil {
		t.Fatal(err)
	}
	events := append(append([]game.Event(nil), created.Events...), rolled.Events...)
	replayProjection, err := m.ProjectReplay(events, game.Viewer{Kind: game.ViewerReplay, UserID: "user-1"}, game.ReplayAccessParticipant)
	if err != nil {
		t.Fatal(err)
	}
	var replay dice789v1.Replay
	if err := unmarshalStrict(replayProjection.View.Payload, &replay); err != nil {
		t.Fatal(err)
	}
	if len(replay.GetTurns()) != 1 || replay.GetTurns()[0].GetSettled() || replay.GetTurns()[0].GetSummary().GetDieOne() == 0 {
		t.Fatalf("pending public turn is missing: %+v", replay.GetTurns())
	}
}

func TestDroppedReplayKeepsHostAuditAndOverridesRollEffect(t *testing.T) {
	m := New()
	created, err := m.Create(testCreateRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	rolled, err := m.HandleCommand(created.Snapshot, commandRequest(t, 1, "user-1", "AQEBAQEBAQEBAQEBAQEBAQ", projection.ActionRoll, &dice789v1.Command{Command: &dice789v1.Command_Roll{Roll: &dice789v1.Roll{}}}))
	if err != nil {
		t.Fatal(err)
	}
	dropped, err := m.HandleCommand(rolled.Snapshot, commandRequest(t, 2, "user-1", "AwMDAwMDAwMDAwMDAwMDAw", projection.ActionReportDropped, &dice789v1.Command{Command: &dice789v1.Command_ReportDropped{ReportDropped: &dice789v1.ReportDropped{Reason: "manual"}}}))
	if err != nil {
		t.Fatal(err)
	}
	droppedState, err := DecodeState(dropped.Snapshot.State)
	if err != nil || droppedState.LastSettlement.DropReason != "manual" {
		t.Fatalf("drop reason was not persisted: settlement=%+v error=%v", droppedState.LastSettlement, err)
	}
	history := append(append(append([]game.Event(nil), created.Events...), rolled.Events...), dropped.Events...)
	projected, err := m.ProjectReplay(history, game.Viewer{Kind: game.ViewerReplay, UserID: "user-1"}, game.ReplayAccessParticipant)
	if err != nil {
		t.Fatal(err)
	}
	var replay dice789v1.Replay
	if err := unmarshalStrict(projected.View.Payload, &replay); err != nil {
		t.Fatal(err)
	}
	if len(replay.GetTurns()) != 1 || replay.GetTurns()[0].GetSummary().GetEffect() != dice789v1.Effect_EFFECT_DROPPED_DRAIN_POOL ||
		replay.GetTurns()[0].GetSummary().GetAuditRef() != "AwMDAwMDAwMDAwMDAwMDAw" || replay.GetTurns()[0].GetSummary().GetDropReason() != "manual" {
		t.Fatalf("dropped replay audit is incomplete: %+v", replay.GetTurns())
	}
}

func TestTimerPayloadBindsPendingIdentity(t *testing.T) {
	m := New()
	created, err := m.Create(testCreateRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	var timer dice789v1.ActionTimer
	if err := unmarshalStrict(created.Timers[0].Message.Payload, &timer); err != nil {
		t.Fatal(err)
	}
	timer.CurrentUserId = "user-2"
	payload, err := marshalDeterministic(&timer)
	if err != nil {
		t.Fatal(err)
	}
	request := game.TimerRequest{
		Context: testContext(31 * time.Second), TimerID: TimerID, ExpectedStateVersion: 1,
		Timer: game.Message{MessageType: TimerMessageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload},
	}
	if _, err := m.HandleTimer(created.Snapshot, request); engine.ErrorCodeOf(err) != engine.CodeTimerMismatch {
		t.Fatalf("mismatched timer was not rejected: %v", err)
	}
}

func TestContinueAfterSnapshotRoundTripPreservesAppliedEffect(t *testing.T) {
	m := New()
	created, err := m.Create(testCreateRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	rolled := rollTransitionForResult(t, m, created.Snapshot, engine.ResultEight)
	confirmedRequest := commandRequest(t, rolled.Snapshot.StateVersion, "user-1", "AgICAgICAgICAgICAgICAg", projection.ActionConfirmLanded, &dice789v1.Command{
		Command: &dice789v1.Command_ConfirmLanded{ConfirmLanded: &dice789v1.ConfirmLanded{}},
	})
	confirmedRequest.Context = testContext(2 * time.Second)
	confirmed, err := m.HandleCommand(rolled.Snapshot, confirmedRequest)
	if err != nil {
		t.Fatal(err)
	}
	before, err := DecodeState(confirmed.Snapshot.State)
	if err != nil || before.Phase != engine.PhaseAwaitingContinue || before.LastSettlement.PenaltyTicks == 0 {
		t.Fatalf("confirmed state=%+v error=%v", before, err)
	}
	passRequest := commandRequest(t, confirmed.Snapshot.StateVersion, before.SourceUserID, "AwMDAwMDAwMDAwMDAwMDAw", projection.ActionPass, &dice789v1.Command{
		Command: &dice789v1.Command_Pass{Pass: &dice789v1.Pass{}},
	})
	passRequest.Context = testContext(3 * time.Second)
	passed, err := m.HandleCommand(confirmed.Snapshot, passRequest)
	if err != nil {
		t.Fatal(err)
	}
	after, err := DecodeState(passed.Snapshot.State)
	if err != nil {
		t.Fatal(err)
	}
	if after.LastSettlement.PenaltyTicks != before.LastSettlement.PenaltyTicks ||
		after.LastSettlement.PoolBeforeTicks != before.LastSettlement.PoolBeforeTicks ||
		after.LastSettlement.PoolAfterTicks != before.LastSettlement.PoolAfterTicks {
		t.Fatalf("effect changed after snapshot restore: before=%+v after=%+v", before.LastSettlement, after.LastSettlement)
	}
}

func TestCommandCodecRejectsNonCanonicalAndMismatchedOneof(t *testing.T) {
	m := New()
	created, err := m.Create(testCreateRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	roll := &dice789v1.Command{Command: &dice789v1.Command_Roll{Roll: &dice789v1.Roll{}}}
	payload, err := marshalDeterministic(roll)
	if err != nil {
		t.Fatal(err)
	}
	nonCanonical := append(append([]byte(nil), payload...), payload...)
	request := commandRequest(t, 1, "user-1", "AQEBAQEBAQEBAQEBAQEBAQ", projection.ActionRoll, roll)
	request.Command.Payload = nonCanonical
	if _, err := m.HandleCommand(created.Snapshot, request); engine.ErrorCodeOf(err) != engine.CodeMalformedPayload || !errors.Is(err, game.ErrInvalidContract) {
		t.Fatalf("non-canonical command error = %v", err)
	}
	mismatched := commandRequest(t, 1, "user-1", "AgICAgICAgICAgICAgICAg", projection.ActionRoll, &dice789v1.Command{
		Command: &dice789v1.Command_Pass{Pass: &dice789v1.Pass{}},
	})
	if _, err := m.HandleCommand(created.Snapshot, mismatched); engine.ErrorCodeOf(err) != engine.CodeMalformedPayload {
		t.Fatalf("mismatched oneof error = %v", err)
	}
}

func TestSystemRevocationFinishAndMigration(t *testing.T) {
	m := New()
	created, err := m.Create(testCreateRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := m.HandleSystem(created.Snapshot, systemRequest(t, 1, EventParticipantRevokedMessage, &dice789v1.ParticipantRevoked{UserId: "user-2"}))
	if err != nil {
		t.Fatal(err)
	}
	state, err := DecodeState(revoked.Snapshot.State)
	if err != nil || state.Players[1].Active || len(revoked.Timers) != 1 {
		t.Fatalf("revoked state=%+v timers=%d error=%v", state, len(revoked.Timers), err)
	}
	finished, err := m.HandleSystem(created.Snapshot, systemRequest(t, 1, SystemFinishMessage, &dice789v1.Command{
		Command: &dice789v1.Command_Finish{Finish: &dice789v1.Finish{OperatorUserId: "user-1"}},
	}))
	if err != nil || !finished.Finished || len(finished.Timers) != 0 {
		t.Fatalf("finish transition=%+v error=%v", finished, err)
	}
	finishEvent, err := decodeEvent(finished.Events[0].Message)
	if err != nil || finishEvent.ProtocolEvent.GetSessionFinished().GetOperatorUserId() != "user-1" {
		t.Fatalf("host finish audit=%+v error=%v", finishEvent.ProtocolEvent, err)
	}
	if _, err := m.HandleSystem(created.Snapshot, systemRequest(t, 1, SystemFinishMessage, &dice789v1.Command{
		Command: &dice789v1.Command_Finish{Finish: &dice789v1.Finish{}},
	})); engine.ErrorCodeOf(err) != engine.CodeMalformedPayload {
		t.Fatalf("operator-less host finish error=%v", err)
	}
	migrated, err := m.Migrate(created.Snapshot, 1, 1)
	if err != nil || !bytes.Equal(migrated.State.Payload, created.Snapshot.State.Payload) {
		t.Fatalf("migration changed canonical state: error=%v", err)
	}
	if _, err := m.Migrate(created.Snapshot, 1, 2); engine.ErrorCodeOf(err) != engine.CodeUnsupportedMigration {
		t.Fatalf("unsupported migration error=%v", err)
	}
}

func TestModuleTransitionsAreDeterministic(t *testing.T) {
	m := New()
	request := testCreateRequest(t)
	first, err := m.Create(request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Create(request)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("create transition differs: error=%v", err)
	}
	command := commandRequest(t, 1, "user-1", "AQEBAQEBAQEBAQEBAQEBAQ", projection.ActionRoll, &dice789v1.Command{
		Command: &dice789v1.Command_Roll{Roll: &dice789v1.Roll{}},
	})
	firstRoll, err := m.HandleCommand(first.Snapshot, command)
	if err != nil {
		t.Fatal(err)
	}
	secondRoll, err := m.HandleCommand(first.Snapshot, command)
	if err != nil || !reflect.DeepEqual(firstRoll, secondRoll) {
		t.Fatalf("roll transition differs: error=%v", err)
	}
}

func testCreateRequest(t *testing.T) game.CreateRequest {
	t.Helper()
	config, err := EncodeConfigForPlayers(engine.DefaultConfig(false), 3)
	if err != nil {
		t.Fatal(err)
	}
	return game.CreateRequest{
		Context: testContext(0), StartContext: game.SessionStartContext{HostUserID: "user-1", StartingSeat: 0},
		Participants: []game.Participant{{UserID: "user-1", SeatIndex: 0}, {UserID: "user-2", SeatIndex: 1}, {UserID: "user-3", SeatIndex: 2}},
		Config:       config,
	}
}

func testContext(offset time.Duration) game.DeterministicContext {
	seed := [32]byte{}
	seed[0] = 1
	return game.DeterministicContext{Now: time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC).Add(offset), RandomSeed: seed}
}

func commandRequest(t *testing.T, version uint64, actor, actionID string, messageType game.Identifier, command proto.Message) game.CommandRequest {
	t.Helper()
	payload, err := marshalDeterministic(command)
	if err != nil {
		t.Fatal(err)
	}
	return game.CommandRequest{
		Context: testContext(time.Second), ActorUserID: game.Identifier(actor), ActionID: game.ActionID(actionID), ExpectedStateVersion: version,
		Command: game.Message{MessageType: messageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload},
	}
}

func systemRequest(t *testing.T, version uint64, messageType game.Identifier, message proto.Message) game.SystemRequest {
	t.Helper()
	payload, err := marshalDeterministic(message)
	if err != nil {
		t.Fatal(err)
	}
	return game.SystemRequest{
		Context: testContext(time.Second), SystemOperationID: game.ActionID("AQEBAQEBAQEBAQEBAQEBAQ"),
		SourceEventID: "source-event", ExpectedStateVersion: version,
		System: game.Message{MessageType: messageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload},
	}
}

func rollTransitionForResult(t *testing.T, module *Module, snapshot game.Snapshot, wanted engine.ResultKind) game.Transition {
	t.Helper()
	for value := uint32(1); value < 65_536; value++ {
		request := commandRequest(t, snapshot.StateVersion, "user-1", "AAAAAAAAAAAAAAAAAAAAAA", projection.ActionRoll, &dice789v1.Command{
			Command: &dice789v1.Command_Roll{Roll: &dice789v1.Roll{}},
		})
		request.Context.RandomSeed[0] = byte(value)
		request.Context.RandomSeed[1] = byte(value >> 8)
		transition, err := module.HandleCommand(snapshot, request)
		if err != nil {
			t.Fatal(err)
		}
		state, err := DecodeState(transition.Snapshot.State)
		if err != nil {
			t.Fatal(err)
		}
		if state.PendingResult == wanted {
			return transition
		}
	}
	t.Fatalf("could not produce result %s", wanted)
	return game.Transition{}
}
