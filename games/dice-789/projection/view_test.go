package projection

import (
	"testing"
	"time"

	"github.com/iFTY-R/game-night/games/dice-789/engine"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

func TestBuildViewScopesPlayerSpectatorAndSeatActions(t *testing.T) {
	state := projectionState(t, engine.DefaultConfig(false))
	player, actions, err := BuildView(state, playerViewer("user-1", 0))
	if err != nil || len(actions) != 1 || actions[0] != ActionRoll || len(player.GetAllowedActions()) != 1 {
		t.Fatalf("current player did not receive roll: actions=%v view=%+v error=%v", actions, player, err)
	}
	spectator, actions, err := BuildView(state, game.Viewer{Kind: game.ViewerSpectator, UserID: "viewer-1"})
	if err != nil || len(actions) != 0 || len(spectator.GetAllowedActions()) != 0 {
		t.Fatalf("spectator received actions: actions=%v view=%+v error=%v", actions, spectator, err)
	}
	if _, _, err := BuildView(state, playerViewer("user-1", 1)); err == nil {
		t.Fatal("player projection accepted the wrong stable seat")
	}
}

func TestResultPendingActionsBelongToHostNotSource(t *testing.T) {
	state := projectionState(t, engine.DefaultConfig(false))
	state.Phase = engine.PhaseResultPending
	state.CurrentUserID = "user-2"
	state.SourceUserID = "user-2"
	state.DieOne, state.DieTwo, state.Sum = 2, 5, 7
	state.PendingResult = engine.ResultSeven
	state.PendingPoolBeforeTicks = state.TotalPoolTicks
	state.PendingDirectionBefore = state.Direction
	state.ActionDeadlineUnixMillis = projectionNow().Add(5 * time.Second).UnixMilli()
	if err := state.Validate(); err != nil {
		t.Fatal(err)
	}
	host, actions, err := BuildView(state, playerViewer("user-1", 0))
	if err != nil || len(actions) != 2 || actions[0] != ActionConfirmLanded || actions[1] != ActionReportDropped || !host.GetViewerIsHost() {
		t.Fatalf("host confirmation scope is wrong: actions=%v view=%+v error=%v", actions, host, err)
	}
	source, actions, err := BuildView(state, playerViewer("user-2", 1))
	if err != nil || len(actions) != 0 || source.GetViewerIsHost() {
		t.Fatalf("non-host source received host actions: actions=%v view=%+v error=%v", actions, source, err)
	}
}

func TestTargetAndAddConstraintsAreViewerScoped(t *testing.T) {
	targetState := projectionState(t, engine.DefaultConfig(false))
	targetState.Phase = engine.PhaseAwaitingTarget
	targetState.SourceUserID = "user-1"
	targetState.CurrentUserID = "user-1"
	targetState.DieOne, targetState.DieTwo, targetState.Sum = 1, 1, 2
	targetState.PendingResult = engine.ResultDoubleOne
	targetState.PendingPoolBeforeTicks = targetState.TotalPoolTicks
	targetState.PendingDirectionBefore = targetState.Direction
	targetState.ActionDeadlineUnixMillis = projectionNow().Add(30 * time.Second).UnixMilli()
	if err := targetState.Validate(); err != nil {
		t.Fatal(err)
	}
	targetView, actions, err := BuildView(targetState, playerViewer("user-1", 0))
	if err != nil || len(actions) != 1 || actions[0] != ActionChooseTarget || targetView.GetActionConstraints() == nil || len(targetView.GetActionConstraints().GetTargetUserIds()) != 2 {
		t.Fatalf("target constraints are incomplete: actions=%v view=%+v error=%v", actions, targetView, err)
	}
	otherView, actions, err := BuildView(targetState, playerViewer("user-2", 1))
	if err != nil || len(actions) != 0 || otherView.GetActionConstraints() != nil {
		t.Fatalf("target constraints leaked to another player: actions=%v view=%+v error=%v", actions, otherView, err)
	}

	config := engine.DefaultConfig(false)
	config.InitialPoolTicks = 5
	config.AddStepTicks = 2
	addState := projectionState(t, config)
	addState.Phase = engine.PhaseAwaitingAdd
	addState.SourceUserID = "user-1"
	addState.TargetUserID = "user-2"
	addState.CurrentUserID = "user-2"
	addState.DieOne, addState.DieTwo, addState.Sum = 6, 6, 12
	addState.PendingResult = engine.ResultDoubleSix
	addState.PendingPoolBeforeTicks = addState.TotalPoolTicks
	addState.PendingDirectionBefore = addState.Direction
	if err := addState.Validate(); err != nil {
		t.Fatal(err)
	}
	addView, actions, err := BuildView(addState, playerViewer("user-2", 1))
	constraints := addView.GetActionConstraints()
	if err != nil || len(actions) != 1 || actions[0] != ActionAdd || constraints == nil || constraints.GetMinimumAddTicks() != 2 || constraints.GetMaximumAddTicks() != 3 || !constraints.GetAllowCapacityRemainder() {
		t.Fatalf("add constraints are incomplete: actions=%v constraints=%+v error=%v", actions, constraints, err)
	}
	nonActor, actions, err := BuildView(addState, playerViewer("user-1", 0))
	if err != nil || len(actions) != 0 || nonActor.GetActionConstraints() != nil {
		t.Fatalf("add constraints leaked to source: actions=%v view=%+v error=%v", actions, nonActor, err)
	}
}

func projectionState(t *testing.T, config engine.Config) engine.State {
	t.Helper()
	seed := [32]byte{1}
	state, _, err := engine.NewState(config, []engine.Participant{{UserID: "user-1", SeatIndex: 0}, {UserID: "user-2", SeatIndex: 1}, {UserID: "user-3", SeatIndex: 2}}, "user-1", 0, projectionNow().UnixMilli(), seed)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func projectionNow() time.Time {
	return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
}

func playerViewer(userID string, seat uint32) game.Viewer {
	return game.Viewer{Kind: game.ViewerPlayer, UserID: game.Identifier(userID), SeatIndex: seat}
}
