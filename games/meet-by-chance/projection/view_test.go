package projection

import (
	"slices"
	"testing"
	"time"

	"github.com/iFTY-R/game-night/games/meet-by-chance/engine"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

func TestBuildViewPlayerSpectatorAndHostMatrix(t *testing.T) {
	state := projectionState(t, engine.DefaultConfig())
	target := playerByID(state, state.TargetUserID)
	targetView, actions, err := BuildView(state, playerViewer(target.UserID, target.SeatIndex))
	if err != nil || !slices.Equal(actions, []game.Identifier{ActionReroll, ActionStand}) || len(targetView.GetAllowedActions()) != 2 {
		t.Fatalf("target actions=%v view=%+v err=%v", actions, targetView, err)
	}
	if len(targetView.GetPlayers()) != 0 || len(targetView.GetPublicPlayers()) != len(state.Players) {
		t.Fatalf("authoritative players leaked into view: %+v", targetView)
	}

	nonTarget := firstOtherPlayer(state, target.UserID)
	nonTargetView, actions, err := BuildView(state, playerViewer(nonTarget.UserID, nonTarget.SeatIndex))
	if err != nil || len(actions) != 0 || len(nonTargetView.GetAllowedActions()) != 0 {
		t.Fatalf("non-target actions=%v view=%+v err=%v", actions, nonTargetView, err)
	}
	spectator, actions, err := BuildView(state, game.Viewer{Kind: game.ViewerSpectator, UserID: "spectator"})
	if err != nil || len(actions) != 0 || len(spectator.GetAllowedActions()) != 0 || spectator.GetViewerIsHost() {
		t.Fatalf("spectator actions=%v view=%+v err=%v", actions, spectator, err)
	}
	if _, _, err := BuildView(state, playerViewer(target.UserID, target.SeatIndex+1)); err == nil {
		t.Fatal("wrong stable seat was accepted")
	}

	host := playerByID(state, state.HostUserID)
	hostView, _, err := BuildView(state, playerViewer(host.UserID, host.SeatIndex))
	if err != nil || !hostView.GetViewerIsHost() {
		t.Fatalf("host flag missing: view=%+v err=%v", hostView, err)
	}
}

func TestBuildViewRerollLimitActionMatrix(t *testing.T) {
	tests := []struct {
		name    string
		limit   uint32
		count   uint32
		actions []game.Identifier
	}{
		{name: "below limit", limit: 2, count: 1, actions: []game.Identifier{ActionReroll, ActionStand}},
		{name: "at limit", limit: 2, count: 2, actions: []game.Identifier{ActionStand}},
		{name: "zero limit", limit: 0, count: 0, actions: []game.Identifier{ActionStand}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := engine.DefaultConfig()
			config.TargetRerollLimit = test.limit
			state := projectionState(t, config)
			state.TargetRerollCount = test.count
			state.TargetStreak = 0
			if err := state.Validate(); err != nil {
				t.Fatal(err)
			}
			target := playerByID(state, state.TargetUserID)
			view, actions, err := BuildView(state, playerViewer(target.UserID, target.SeatIndex))
			if err != nil || !slices.Equal(actions, test.actions) {
				t.Fatalf("actions=%v want=%v view=%+v err=%v", actions, test.actions, view, err)
			}
			wireActions := make([]game.Identifier, len(view.GetAllowedActions()))
			for index, action := range view.GetAllowedActions() {
				wireActions[index] = game.Identifier(action)
			}
			if !slices.Equal(wireActions, test.actions) {
				t.Fatalf("wire actions=%v want=%v", wireActions, test.actions)
			}
		})
	}
}

func TestBuildViewFinishedStateHasNoActions(t *testing.T) {
	state := projectionState(t, engine.DefaultConfig())
	finished, _, err := engine.Finish(state, engine.FinishPlatformCancelled, "")
	if err != nil {
		t.Fatal(err)
	}
	viewer := playerByID(finished, finished.HostUserID)
	view, actions, err := BuildView(finished, playerViewer(viewer.UserID, viewer.SeatIndex))
	if err != nil || len(actions) != 0 || len(view.GetAllowedActions()) != 0 || view.GetFinishReason() != engine.FinishPlatformCancelled {
		t.Fatalf("finished actions=%v view=%+v err=%v", actions, view, err)
	}
}

func projectionState(t *testing.T, config engine.Config) engine.State {
	t.Helper()
	participants := []engine.Participant{
		{UserID: "user-1", SeatIndex: 0},
		{UserID: "user-2", SeatIndex: 1},
		{UserID: "user-3", SeatIndex: 2},
		{UserID: "user-4", SeatIndex: 3},
	}
	state, _, err := engine.NewState(config, participants, participants[0].UserID, projectionNow().UnixMilli(), [32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func projectionNow() time.Time {
	return time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
}

func playerViewer(userID string, seat uint32) game.Viewer {
	return game.Viewer{Kind: game.ViewerPlayer, UserID: game.Identifier(userID), SeatIndex: seat}
}

func playerByID(state engine.State, userID string) engine.PlayerState {
	for _, player := range state.Players {
		if player.UserID == userID {
			return player
		}
	}
	return engine.PlayerState{}
}

func firstOtherPlayer(state engine.State, excluded string) engine.PlayerState {
	for _, player := range state.Players {
		if player.UserID != excluded && player.Active {
			return player
		}
	}
	return engine.PlayerState{}
}
