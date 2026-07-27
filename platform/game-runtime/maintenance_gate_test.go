package gameruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	adminoperations "github.com/iFTY-R/game-night/platform/admin/operations"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

func TestMaintenanceMutationGateReadsAuthorityForEveryDecision(t *testing.T) {
	reader := &maintenanceStateReaderStub{states: []adminoperations.MaintenanceState{
		{Scope: adminoperations.MaintenanceUserMutations, Version: 4},
		{Enabled: true, Scope: adminoperations.MaintenanceUserMutations, Version: 5},
	}}
	gate, err := NewMaintenanceMutationGate(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.CheckUserMutation(t.Context()); err != nil {
		t.Fatalf("disabled maintenance error = %v", err)
	}
	if err := gate.CheckUserMutation(t.Context()); !errors.Is(err, ErrMutationBlocked) || reader.calls != 2 {
		t.Fatalf("enabled maintenance error = %v, calls = %d", err, reader.calls)
	}
}

func TestMaintenanceMutationGateFailsClosedForUnavailableOrInvalidAuthority(t *testing.T) {
	for _, test := range []struct {
		name   string
		reader *maintenanceStateReaderStub
	}{
		{name: "read error", reader: &maintenanceStateReaderStub{err: errors.New("postgres unavailable")}},
		{name: "missing version", reader: &maintenanceStateReaderStub{states: []adminoperations.MaintenanceState{{Scope: adminoperations.MaintenanceUserMutations}}}},
		{name: "unknown scope", reader: &maintenanceStateReaderStub{states: []adminoperations.MaintenanceState{{Scope: "other", Version: 1}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			gate, err := NewMaintenanceMutationGate(test.reader)
			if err != nil {
				t.Fatal(err)
			}
			if err := gate.CheckUserMutation(t.Context()); !errors.Is(err, ErrMutationStateUnavailable) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestRuntimeServiceMutationGateBlocksOnlyUserStartsAndActions(t *testing.T) {
	t.Run("start", func(t *testing.T) {
		fixture := newRuntimeServiceFixture(t)
		gate := &runtimeMutationGateStub{err: ErrMutationBlocked}
		fixture.service.mutationGate = gate

		_, _, err := fixture.service.Start(t.Context(), StartCommand{
			ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
			Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 81), Config: runtimeServiceMessage("game.config", nil),
		})
		if !errors.Is(err, ErrMutationBlocked) || gate.calls != 1 || fixture.module.createCalls != 0 {
			t.Fatalf("error=%v gate_calls=%d create_calls=%d", err, gate.calls, fixture.module.createCalls)
		}
	})

	t.Run("action", func(t *testing.T) {
		fixture := newRuntimeServiceFixture(t)
		gate := &runtimeMutationGateStub{}
		fixture.service.mutationGate = gate
		_, session, err := fixture.service.Start(t.Context(), StartCommand{
			ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
			Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 82), Config: runtimeServiceMessage("game.config", nil),
		})
		if err != nil {
			t.Fatal(err)
		}
		gate.err = ErrMutationBlocked
		_, err = fixture.service.HandleAction(t.Context(), ActionCommand{
			SessionID: session.Snapshot().ID, ActorUserID: fixture.hostID, ActionID: runtimeServiceActionID(t, 83),
			ExpectedStateVersion: 1, OwnershipEpoch: session.Snapshot().OwnershipEpoch,
			VersionKey: session.Snapshot().VersionKey, Command: runtimeServiceMessage("round.roll", nil),
		})
		if !errors.Is(err, ErrMutationBlocked) || gate.calls != 2 || fixture.module.commandCalls != 0 {
			t.Fatalf("error=%v gate_calls=%d command_calls=%d", err, gate.calls, fixture.module.commandCalls)
		}
	})
}

func TestRuntimeServiceMutationGateDoesNotBlockConvergenceOrReads(t *testing.T) {
	fixture := newRuntimeServiceFixture(t)
	dueAt := fixture.clock.Now().Add(2 * time.Second)
	fixture.module.createTimers = []game.TimerIntent{{
		TimerID: "round.tick", DueAt: dueAt, Message: runtimeServiceMessage("round.tick", nil),
	}}
	gate := &runtimeMutationGateStub{}
	fixture.service.mutationGate = gate
	startedRoom, session, err := fixture.service.Start(t.Context(), StartCommand{
		ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
		Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 84), Config: runtimeServiceMessage("game.config", nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	gate.err = ErrMutationBlocked
	_, _ = fixture.clock.Advance(3 * time.Second)
	timerResult, err := fixture.service.HandleTimer(t.Context(), DueTimer{
		SessionID: session.Snapshot().ID, TimerID: "round.tick", ExpectedStateVersion: 1,
		DueAt: dueAt, Message: runtimeServiceMessage("round.tick", nil),
	}, session.Snapshot().OwnershipEpoch)
	if err != nil {
		t.Fatalf("timer error = %v", err)
	}
	systemResult, err := fixture.service.HandleSystem(t.Context(), SystemCommand{
		SessionID: session.Snapshot().ID, OperationID: runtimeServiceOperationID(t, 85),
		Source:               SystemSource{Kind: SystemSourcePlatform, EventID: uuid.New()},
		ExpectedStateVersion: timerResult.Session.Snapshot().State.StateVersion,
		OwnershipEpoch:       session.Snapshot().OwnershipEpoch, VersionKey: session.Snapshot().VersionKey,
		Message: runtimeServiceMessage("platform.reconcile", nil),
	})
	if err != nil {
		t.Fatalf("system error = %v", err)
	}
	viewer := game.Viewer{Kind: game.ViewerPlayer, UserID: game.Identifier(fixture.hostID.String()), SeatIndex: 4}
	if _, _, err := fixture.service.ProjectCurrent(t.Context(), session.Snapshot().ID, viewer); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	_, _ = fixture.clock.Advance(time.Second)
	_, _, err = fixture.service.Cancel(t.Context(), CancelCommand{
		RoomID: startedRoom.Snapshot().ID, SessionID: session.Snapshot().ID, ExpectedRoom: startedRoom.Version(),
		OwnershipEpoch: systemResult.Session.Snapshot().OwnershipEpoch, Reason: CancelReasonPlatformCancelled,
	})
	if err != nil {
		t.Fatalf("cancel error = %v", err)
	}
	if gate.calls != 1 {
		t.Fatalf("mutation gate calls = %d, want only initial start", gate.calls)
	}
}

type maintenanceStateReaderStub struct {
	states []adminoperations.MaintenanceState
	err    error
	calls  int
}

func (reader *maintenanceStateReaderStub) GetMaintenanceState(context.Context) (adminoperations.MaintenanceState, error) {
	reader.calls++
	if reader.err != nil {
		return adminoperations.MaintenanceState{}, reader.err
	}
	if len(reader.states) == 0 {
		return adminoperations.MaintenanceState{}, nil
	}
	index := reader.calls - 1
	if index >= len(reader.states) {
		index = len(reader.states) - 1
	}
	return reader.states[index], nil
}

type runtimeMutationGateStub struct {
	err   error
	calls int
}

func (gate *runtimeMutationGateStub) CheckUserMutation(context.Context) error {
	gate.calls++
	return gate.err
}
