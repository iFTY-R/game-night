package gameruntime

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/idempotency"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

func TestRuntimeServiceStartUsesTrustedRoomContextAndAcquiresOwnership(t *testing.T) {
	fixture := newRuntimeServiceFixture(t)
	storedRoom, session, err := fixture.service.Start(t.Context(), StartCommand{
		ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
		Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 1), Config: runtimeServiceMessage("game.config", []byte("client-config")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if fixture.module.createRequest.StartContext.HostUserID != game.Identifier(fixture.hostID.String()) ||
		fixture.module.createRequest.StartContext.StartingSeat != 4 {
		t.Fatalf("trusted start context = %+v", fixture.module.createRequest.StartContext)
	}
	if storedRoom.Snapshot().Status != roomDomain.RoomStatusPlaying || session.Snapshot().OwnershipEpoch != 1 ||
		session.Snapshot().VersionKey != fixture.module.manifest.Key() {
		t.Fatalf("room=%+v session=%+v", storedRoom.Snapshot(), session.Snapshot())
	}
	if fixture.module.createRequest.Config.Payload[0] != 'c' {
		t.Fatal("module did not receive the cloned config payload")
	}
}

func TestRuntimeServiceStartReplaysDurableReceiptAndRejectsOperationReuse(t *testing.T) {
	fixture := newRuntimeServiceFixture(t)
	command := StartCommand{
		ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
		Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 2),
		Config: runtimeServiceMessage("game.config", []byte("original")),
	}
	digest := startDigest(command)
	command.RequestDigest = &digest
	_, first, err := fixture.service.Start(t.Context(), command)
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := fixture.service.Start(t.Context(), command)
	if err != nil {
		t.Fatal(err)
	}
	if first.Snapshot().ID != second.Snapshot().ID || first.Snapshot().OwnershipEpoch != second.Snapshot().OwnershipEpoch ||
		fixture.module.createCalls != 1 {
		t.Fatalf("first=%+v second=%+v create calls=%d", first.Snapshot(), second.Snapshot(), fixture.module.createCalls)
	}
	command.Config = runtimeServiceMessage("game.config", []byte("changed"))
	changedDigest := startDigest(command)
	command.RequestDigest = &changedDigest
	if _, _, err := fixture.service.Start(t.Context(), command); !errors.Is(err, idempotency.ErrConflict) || fixture.module.createCalls != 1 {
		t.Fatalf("operation reuse error=%v create calls=%d", err, fixture.module.createCalls)
	}
}

func TestTrustedStartingSeatFallsBackToMinimumFrozenSeat(t *testing.T) {
	hostID := uuid.New()
	seat, ok := trustedStartingSeat(hostID, []roomDomain.FrozenParticipant{
		{UserID: uuid.New(), SeatIndex: 7}, {UserID: uuid.New(), SeatIndex: 2},
	})
	if !ok || seat != 2 {
		t.Fatalf("fallback seat = %d, ok = %v", seat, ok)
	}
}

func TestRuntimeServiceActionReplaysBeforeRejectingStaleExpectedVersion(t *testing.T) {
	fixture := newRuntimeServiceFixture(t)
	_, session, err := fixture.service.Start(t.Context(), StartCommand{
		ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
		Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 1), Config: runtimeServiceMessage("game.config", nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fixture.clock.Advance(time.Second)
	command := ActionCommand{
		SessionID: session.Snapshot().ID, ActorUserID: fixture.hostID, ActionID: runtimeServiceActionID(t, 7),
		ExpectedStateVersion: 1, OwnershipEpoch: session.Snapshot().OwnershipEpoch,
		VersionKey: session.Snapshot().VersionKey, Command: runtimeServiceMessage("round.roll", []byte("roll")),
	}
	first, err := fixture.service.HandleAction(t.Context(), command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.service.HandleAction(t.Context(), command)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || !second.Replayed || first.Receipt.Snapshot() != second.Receipt.Snapshot() ||
		second.Session.Snapshot().State.StateVersion != 2 || fixture.module.commandCalls != 1 {
		t.Fatalf("first=%+v second=%+v command calls=%d", first, second, fixture.module.commandCalls)
	}
}

func TestRuntimeServiceValidatesOptionalClientRequestDigests(t *testing.T) {
	t.Run("matching action digest", func(t *testing.T) {
		fixture := newRuntimeServiceFixture(t)
		_, session, err := fixture.service.Start(t.Context(), StartCommand{
			ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
			Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 1), Config: runtimeServiceMessage("game.config", nil),
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fixture.clock.Advance(time.Second)
		command := ActionCommand{
			SessionID: session.Snapshot().ID, ActorUserID: fixture.hostID, ActionID: runtimeServiceActionID(t, 31),
			ExpectedStateVersion: 1, OwnershipEpoch: session.Snapshot().OwnershipEpoch,
			VersionKey: session.Snapshot().VersionKey, Command: runtimeServiceMessage("round.roll", nil),
		}
		digest := actionDigest(command)
		command.RequestDigest = &digest
		if _, err := fixture.service.HandleAction(t.Context(), command); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("mismatched action digest", func(t *testing.T) {
		fixture := newRuntimeServiceFixture(t)
		_, session, err := fixture.service.Start(t.Context(), StartCommand{
			ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
			Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 1), Config: runtimeServiceMessage("game.config", nil),
		})
		if err != nil {
			t.Fatal(err)
		}
		mismatch := idempotency.Digest{1}
		_, err = fixture.service.HandleAction(t.Context(), ActionCommand{
			SessionID: session.Snapshot().ID, ActorUserID: fixture.hostID, ActionID: runtimeServiceActionID(t, 32),
			ExpectedStateVersion: 1, OwnershipEpoch: session.Snapshot().OwnershipEpoch,
			VersionKey: session.Snapshot().VersionKey, Command: runtimeServiceMessage("round.roll", nil), RequestDigest: &mismatch,
		})
		if !errors.Is(err, idempotency.ErrConflict) || fixture.module.commandCalls != 0 {
			t.Fatalf("error=%v command calls=%d", err, fixture.module.commandCalls)
		}
	})

	t.Run("matching and mismatched system digest", func(t *testing.T) {
		fixture := newRuntimeServiceFixture(t)
		_, session, err := fixture.service.Start(t.Context(), StartCommand{
			ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
			Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 1), Config: runtimeServiceMessage("game.config", nil),
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fixture.clock.Advance(time.Second)
		command := SystemCommand{
			SessionID: session.Snapshot().ID, OperationID: runtimeServiceOperationID(t, 33),
			Source: SystemSource{Kind: SystemSourceRoomOutbox, EventID: uuid.New()}, ExpectedStateVersion: 1,
			OwnershipEpoch: session.Snapshot().OwnershipEpoch, VersionKey: session.Snapshot().VersionKey,
			Message: runtimeServiceMessage("participant.revoked", nil),
		}
		digest := systemDigest(command)
		command.RequestDigest = &digest
		if _, err := fixture.service.HandleSystem(t.Context(), command); err != nil {
			t.Fatal(err)
		}
		mismatch := idempotency.Digest{1}
		command.OperationID = runtimeServiceOperationID(t, 34)
		command.ExpectedStateVersion = fixture.authority.session.Snapshot().State.StateVersion
		command.RequestDigest = &mismatch
		if _, err := fixture.service.HandleSystem(t.Context(), command); !errors.Is(err, idempotency.ErrConflict) || fixture.module.systemCalls != 1 {
			t.Fatalf("error=%v system calls=%d", err, fixture.module.systemCalls)
		}
	})

	t.Run("host digest binds optimistic version only", func(t *testing.T) {
		hostID := uuid.New()
		command := SystemCommand{
			SessionID: uuid.New(), OperationID: runtimeServiceOperationID(t, 40),
			Source:               SystemSource{Kind: SystemSourceHostAPI, EventID: uuid.New(), RequestedByUserID: hostID},
			ExpectedStateVersion: 1, OwnershipEpoch: 1, VersionKey: runtimeServiceManifest().Key(),
			Message: runtimeServiceMessage("session.finish", nil),
		}
		first := systemDigest(command)
		command.ExpectedStateVersion++
		if first == systemDigest(command) {
			t.Fatal("host digest did not bind expected state version")
		}
		command.Source = SystemSource{Kind: SystemSourceRoomOutbox, EventID: command.Source.EventID}
		second := systemDigest(command)
		command.ExpectedStateVersion++
		if second != systemDigest(command) {
			t.Fatal("durable system digest changed with recomputed state version")
		}
	})
}

func TestRuntimeServiceSystemRetryKeepsLogicalDigestAcrossVersionRecompute(t *testing.T) {
	fixture := newRuntimeServiceFixture(t)
	_, session, err := fixture.service.Start(t.Context(), StartCommand{
		ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
		Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 1), Config: runtimeServiceMessage("game.config", nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.authority.retrySystemOnce = true
	_, _ = fixture.clock.Advance(time.Second)
	result, err := fixture.service.HandleSystem(t.Context(), SystemCommand{
		SessionID: session.Snapshot().ID, OperationID: runtimeServiceOperationID(t, 9),
		Source:               SystemSource{Kind: SystemSourceRoomOutbox, EventID: uuid.New()},
		ExpectedStateVersion: 1, OwnershipEpoch: session.Snapshot().OwnershipEpoch,
		VersionKey: session.Snapshot().VersionKey, Message: runtimeServiceMessage("participant.revoked", []byte(fixture.playerID.String())),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Retry || result.Session.Snapshot().State.StateVersion != 3 || len(fixture.authority.systemDigests) != 2 ||
		fixture.authority.systemDigests[0] != fixture.authority.systemDigests[1] {
		t.Fatalf("result=%+v digests=%v", result, fixture.authority.systemDigests)
	}
}

func TestRuntimeServiceSuspendsWhenExactModuleDisappears(t *testing.T) {
	fixture := newRuntimeServiceFixture(t)
	_, session, err := fixture.service.Start(t.Context(), StartCommand{
		ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
		Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 1), Config: runtimeServiceMessage("game.config", nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.registry.missing = true
	_, _ = fixture.clock.Advance(time.Second)
	_, err = fixture.service.HandleSystem(t.Context(), SystemCommand{
		SessionID: session.Snapshot().ID, OperationID: runtimeServiceOperationID(t, 10),
		Source:               SystemSource{Kind: SystemSourcePlatform, EventID: uuid.New()},
		ExpectedStateVersion: 1, OwnershipEpoch: session.Snapshot().OwnershipEpoch,
		VersionKey: session.Snapshot().VersionKey, Message: runtimeServiceMessage("platform.recover", nil),
	})
	if !errors.Is(err, ErrModuleUnavailable) {
		t.Fatalf("missing module error = %v", err)
	}
	if fixture.authority.session.Snapshot().Status != StatusSuspended {
		t.Fatalf("session = %+v", fixture.authority.session.Snapshot())
	}
}

func TestRuntimeServiceActionSuspendsWhenExactModuleDisappears(t *testing.T) {
	fixture := newRuntimeServiceFixture(t)
	_, session, err := fixture.service.Start(t.Context(), StartCommand{
		ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
		Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 1), Config: runtimeServiceMessage("game.config", nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.registry.missing = true
	_, _ = fixture.clock.Advance(time.Second)
	_, err = fixture.service.HandleAction(t.Context(), ActionCommand{
		SessionID: session.Snapshot().ID, ActorUserID: fixture.hostID, ActionID: runtimeServiceActionID(t, 12),
		ExpectedStateVersion: 1, OwnershipEpoch: session.Snapshot().OwnershipEpoch,
		VersionKey: session.Snapshot().VersionKey, Command: runtimeServiceMessage("round.roll", nil),
	})
	if !errors.Is(err, ErrModuleUnavailable) || fixture.authority.session.Snapshot().Status != StatusSuspended {
		t.Fatalf("error=%v session=%+v", err, fixture.authority.session.Snapshot())
	}
	fixture.registry.missing = false
	_, _ = fixture.clock.Advance(time.Second)
	resumed, err := fixture.service.Resume(t.Context(), ResumeCommand{
		SessionID: session.Snapshot().ID, OwnershipEpoch: session.Snapshot().OwnershipEpoch,
	})
	if err != nil || resumed.Snapshot().Status != StatusActive {
		t.Fatalf("resumed=%+v error=%v", resumed.Snapshot(), err)
	}
	_, _ = fixture.clock.Advance(time.Second)
	result, err := fixture.service.HandleAction(t.Context(), ActionCommand{
		SessionID: resumed.Snapshot().ID, ActorUserID: fixture.hostID, ActionID: runtimeServiceActionID(t, 15),
		ExpectedStateVersion: 1, OwnershipEpoch: resumed.Snapshot().OwnershipEpoch,
		VersionKey: resumed.Snapshot().VersionKey, Command: runtimeServiceMessage("round.roll", nil),
	})
	if err != nil || result.Session.Snapshot().State.StateVersion != 2 {
		t.Fatalf("resumed action=%+v error=%v", result, err)
	}
}

func TestRuntimeServiceTimerAndSystemReplayBeforeCallingModule(t *testing.T) {
	t.Run("terminal timer", func(t *testing.T) {
		fixture := newRuntimeServiceFixture(t)
		dueAt := fixture.clock.Now().Add(2 * time.Second)
		fixture.module.createTimers = []game.TimerIntent{{
			TimerID: "session.finish", DueAt: dueAt, Message: runtimeServiceMessage("session.finish", nil),
		}}
		_, session, err := fixture.service.Start(t.Context(), StartCommand{
			ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
			Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 1), Config: runtimeServiceMessage("game.config", nil),
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fixture.clock.Advance(3 * time.Second)
		due := DueTimer{
			SessionID: session.Snapshot().ID, TimerID: "session.finish", ExpectedStateVersion: 1,
			DueAt: dueAt, Message: runtimeServiceMessage("session.finish", nil),
		}
		first, err := fixture.service.HandleTimer(t.Context(), due, session.Snapshot().OwnershipEpoch)
		if err != nil {
			t.Fatal(err)
		}
		second, err := fixture.service.HandleTimer(t.Context(), due, session.Snapshot().OwnershipEpoch)
		if err != nil {
			t.Fatal(err)
		}
		room := fixture.authority.room.Snapshot()
		if first.Replayed || !second.Replayed || first.Receipt.Snapshot() != second.Receipt.Snapshot() ||
			fixture.module.timerCalls != 1 || room.Status != roomDomain.RoomStatusPostGame ||
			room.LastFinishedSessionID != session.Snapshot().ID || room.LastFinishedGameID != string(session.Snapshot().VersionKey.GameID) {
			t.Fatalf("first=%+v second=%+v timerCalls=%d room=%+v", first, second, fixture.module.timerCalls, fixture.authority.room.Snapshot())
		}
	})

	t.Run("completed system", func(t *testing.T) {
		fixture := newRuntimeServiceFixture(t)
		_, session, err := fixture.service.Start(t.Context(), StartCommand{
			ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
			Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 1), Config: runtimeServiceMessage("game.config", nil),
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fixture.clock.Advance(time.Second)
		command := SystemCommand{
			SessionID: session.Snapshot().ID, OperationID: runtimeServiceOperationID(t, 13),
			Source:               SystemSource{Kind: SystemSourceRoomOutbox, EventID: uuid.New()},
			ExpectedStateVersion: 1, OwnershipEpoch: session.Snapshot().OwnershipEpoch,
			VersionKey: session.Snapshot().VersionKey, Message: runtimeServiceMessage("participant.revoked", []byte(fixture.playerID.String())),
		}
		first, err := fixture.service.HandleSystem(t.Context(), command)
		if err != nil {
			t.Fatal(err)
		}
		second, err := fixture.service.HandleSystem(t.Context(), command)
		if err != nil {
			t.Fatal(err)
		}
		if first.Replayed || !second.Replayed || first.Receipt.Snapshot() != second.Receipt.Snapshot() || fixture.module.systemCalls != 1 {
			t.Fatalf("first=%+v second=%+v systemCalls=%d", first, second, fixture.module.systemCalls)
		}
	})
}

func TestRuntimeServiceRejectsWrongDefaultGameAndNonterminalHostSystem(t *testing.T) {
	t.Run("wrong registry default", func(t *testing.T) {
		fixture := newRuntimeServiceFixture(t)
		fixture.module.manifest.GameID = "meet-by-chance"
		_, _, err := fixture.service.Start(t.Context(), StartCommand{
			ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: "liars-dice",
			Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 1), Config: runtimeServiceMessage("game.config", nil),
		})
		if !errors.Is(err, ErrModuleUnavailable) {
			t.Fatalf("wrong default error = %v", err)
		}
	})

	t.Run("non-host host API", func(t *testing.T) {
		fixture := newRuntimeServiceFixture(t)
		_, session, err := fixture.service.Start(t.Context(), StartCommand{
			ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
			Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 1), Config: runtimeServiceMessage("game.config", nil),
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fixture.clock.Advance(time.Second)
		_, err = fixture.service.HandleSystem(t.Context(), SystemCommand{
			SessionID: session.Snapshot().ID, OperationID: runtimeServiceOperationID(t, 14),
			Source:               SystemSource{Kind: SystemSourceHostAPI, EventID: uuid.New(), RequestedByUserID: fixture.playerID},
			ExpectedStateVersion: 1, OwnershipEpoch: session.Snapshot().OwnershipEpoch,
			VersionKey: session.Snapshot().VersionKey, Message: runtimeServiceMessage("host.not-finish", nil),
		})
		if !errors.Is(err, roomDomain.ErrHostRequired) || fixture.authority.session.Snapshot().State.StateVersion != 1 || fixture.module.systemCalls != 0 {
			t.Fatalf("error=%v session=%+v", err, fixture.authority.session.Snapshot())
		}
	})

	t.Run("nonterminal current-host API", func(t *testing.T) {
		fixture := newRuntimeServiceFixture(t)
		_, session, err := fixture.service.Start(t.Context(), StartCommand{
			ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
			Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 1), Config: runtimeServiceMessage("game.config", nil),
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fixture.clock.Advance(time.Second)
		_, err = fixture.service.HandleSystem(t.Context(), SystemCommand{
			SessionID: session.Snapshot().ID, OperationID: runtimeServiceOperationID(t, 16),
			Source:               SystemSource{Kind: SystemSourceHostAPI, EventID: uuid.New(), RequestedByUserID: fixture.hostID},
			ExpectedStateVersion: 1, OwnershipEpoch: session.Snapshot().OwnershipEpoch,
			VersionKey: session.Snapshot().VersionKey, Message: runtimeServiceMessage("host.not-finish", nil),
		})
		if !errors.Is(err, ErrInvalidSystemCommit) || fixture.authority.session.Snapshot().State.StateVersion != 1 || fixture.module.systemCalls != 1 {
			t.Fatalf("error=%v session=%+v", err, fixture.authority.session.Snapshot())
		}
	})
}

func TestRuntimeServiceHostFinishRequiresExactFirstVersionAndStillReplaysReceipt(t *testing.T) {
	t.Run("stale first request", func(t *testing.T) {
		fixture := newRuntimeServiceFixture(t)
		_, session, err := fixture.service.Start(t.Context(), StartCommand{
			ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
			Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 1), Config: runtimeServiceMessage("game.config", nil),
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fixture.clock.Advance(time.Second)
		_, err = fixture.service.HandleAction(t.Context(), ActionCommand{
			SessionID: session.Snapshot().ID, ActorUserID: fixture.hostID, ActionID: runtimeServiceActionID(t, 35),
			ExpectedStateVersion: 1, OwnershipEpoch: session.Snapshot().OwnershipEpoch,
			VersionKey: session.Snapshot().VersionKey, Command: runtimeServiceMessage("round.roll", nil),
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = fixture.service.HandleSystem(t.Context(), SystemCommand{
			SessionID: session.Snapshot().ID, OperationID: runtimeServiceOperationID(t, 36),
			Source:               SystemSource{Kind: SystemSourceHostAPI, EventID: uuid.New(), RequestedByUserID: fixture.hostID},
			ExpectedStateVersion: 1, OwnershipEpoch: session.Snapshot().OwnershipEpoch,
			VersionKey: session.Snapshot().VersionKey, Message: runtimeServiceMessage("session.finish", nil),
		})
		if !errors.Is(err, ErrStateVersionConflict) || fixture.module.systemCalls != 0 ||
			fixture.authority.session.Snapshot().State.StateVersion != 2 {
			t.Fatalf("error=%v system calls=%d session=%+v", err, fixture.module.systemCalls, fixture.authority.session.Snapshot())
		}
	})

	t.Run("completed receipt", func(t *testing.T) {
		fixture := newRuntimeServiceFixture(t)
		_, session, err := fixture.service.Start(t.Context(), StartCommand{
			ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
			Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 1), Config: runtimeServiceMessage("game.config", nil),
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fixture.clock.Advance(time.Second)
		command := SystemCommand{
			SessionID: session.Snapshot().ID, OperationID: runtimeServiceOperationID(t, 37),
			Source:               SystemSource{Kind: SystemSourceHostAPI, EventID: uuid.New(), RequestedByUserID: fixture.hostID},
			ExpectedStateVersion: 1, OwnershipEpoch: session.Snapshot().OwnershipEpoch,
			VersionKey: session.Snapshot().VersionKey, Message: runtimeServiceMessage("session.finish", nil),
		}
		digest := systemDigest(command)
		command.RequestDigest = &digest
		first, err := fixture.service.HandleSystem(t.Context(), command)
		if err != nil {
			t.Fatal(err)
		}
		second, err := fixture.service.HandleSystem(t.Context(), command)
		if err != nil {
			t.Fatal(err)
		}
		if first.Replayed || !second.Replayed || fixture.module.systemCalls != 1 || second.Session.Snapshot().Status != StatusFinished {
			t.Fatalf("first=%+v second=%+v system calls=%d", first, second, fixture.module.systemCalls)
		}
	})

	t.Run("concurrent retry does not recompute", func(t *testing.T) {
		fixture := newRuntimeServiceFixture(t)
		_, session, err := fixture.service.Start(t.Context(), StartCommand{
			ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
			Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 1), Config: runtimeServiceMessage("game.config", nil),
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fixture.clock.Advance(time.Second)
		fixture.authority.retrySystemOnce = true
		_, err = fixture.service.HandleSystem(t.Context(), SystemCommand{
			SessionID: session.Snapshot().ID, OperationID: runtimeServiceOperationID(t, 38),
			Source:               SystemSource{Kind: SystemSourceHostAPI, EventID: uuid.New(), RequestedByUserID: fixture.hostID},
			ExpectedStateVersion: 1, OwnershipEpoch: session.Snapshot().OwnershipEpoch, VersionKey: session.Snapshot().VersionKey,
			Message: runtimeServiceMessage("session.finish", nil),
		})
		if !errors.Is(err, ErrStateVersionConflict) || fixture.module.systemCalls != 1 {
			t.Fatalf("error=%v system calls=%d", err, fixture.module.systemCalls)
		}
	})
}

func TestRuntimeServiceCancelReplaysAlreadyCancelledSession(t *testing.T) {
	fixture := newRuntimeServiceFixture(t)
	playing, session, err := fixture.service.Start(t.Context(), StartCommand{
		ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
		Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 1), Config: runtimeServiceMessage("game.config", nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fixture.clock.Advance(time.Second)
	command := CancelCommand{
		RoomID: playing.Snapshot().ID, SessionID: session.Snapshot().ID,
		ExpectedRoom: playing.Version(), OwnershipEpoch: session.Snapshot().OwnershipEpoch,
	}
	firstRoom, firstSession, err := fixture.service.Cancel(t.Context(), command)
	if err != nil {
		t.Fatal(err)
	}
	secondRoom, secondSession, err := fixture.service.Cancel(t.Context(), command)
	if err != nil {
		t.Fatal(err)
	}
	if firstRoom.Snapshot().Status != roomDomain.RoomStatusLobby || secondRoom.Snapshot().RoomVersion != firstRoom.Snapshot().RoomVersion ||
		firstSession.Snapshot().Status != StatusCancelled || secondSession.Snapshot().ID != firstSession.Snapshot().ID {
		t.Fatalf("first room=%+v session=%+v second room=%+v session=%+v", firstRoom.Snapshot(), firstSession.Snapshot(), secondRoom.Snapshot(), secondSession.Snapshot())
	}
}

func TestRuntimeServiceProjectsViewerDeltaAndFallsBackToSafeSnapshot(t *testing.T) {
	fixture := newRuntimeServiceFixture(t)
	_, session, err := fixture.service.Start(t.Context(), StartCommand{
		ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
		Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 1), Config: runtimeServiceMessage("game.config", nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	viewer := game.Viewer{Kind: game.ViewerPlayer, UserID: game.Identifier(fixture.hostID.String()), SeatIndex: 4}
	delta, snapshot, fallback, err := fixture.service.ProjectEvents(t.Context(), session.Snapshot().ID, 0, viewer)
	if err != nil {
		t.Fatal(err)
	}
	if fallback || !delta.Valid() || snapshot.Valid() {
		t.Fatalf("delta=%+v snapshot=%+v fallback=%v", delta, snapshot, fallback)
	}
	fixture.module.unsafeEventProjection = true
	delta, snapshot, fallback, err = fixture.service.ProjectEvents(t.Context(), session.Snapshot().ID, 0, viewer)
	if err != nil {
		t.Fatal(err)
	}
	if !fallback || delta.Valid() || !snapshot.Valid() {
		t.Fatalf("delta=%+v snapshot=%+v fallback=%v", delta, snapshot, fallback)
	}
}

func TestRuntimeServiceProjectsDeltaOnlyFromCompleteEventHistory(t *testing.T) {
	viewerFor := func(fixture runtimeServiceFixture) game.Viewer {
		return game.Viewer{Kind: game.ViewerPlayer, UserID: game.Identifier(fixture.hostID.String()), SeatIndex: 4}
	}

	t.Run("read limit truncates current history", func(t *testing.T) {
		fixture := newRuntimeServiceFixture(t)
		_, session, err := fixture.service.Start(t.Context(), StartCommand{
			ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
			Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 1), Config: runtimeServiceMessage("game.config", nil),
		})
		if err != nil {
			t.Fatal(err)
		}
		for marker := 0; marker < 256; marker++ {
			_, _ = fixture.clock.Advance(time.Microsecond)
			result, actionErr := fixture.service.HandleAction(t.Context(), ActionCommand{
				SessionID: session.Snapshot().ID, ActorUserID: fixture.hostID, ActionID: runtimeServiceActionID(t, byte(marker)),
				ExpectedStateVersion: session.Snapshot().State.StateVersion, OwnershipEpoch: session.Snapshot().OwnershipEpoch,
				VersionKey: session.Snapshot().VersionKey, Command: runtimeServiceMessage("round.roll", nil),
			})
			if actionErr != nil {
				t.Fatalf("action %d: %v", marker, actionErr)
			}
			session = result.Session
		}

		current, delta, snapshot, fallback, err := fixture.service.ProjectEventsCurrent(
			t.Context(), session.Snapshot().ID, 0, viewerFor(fixture),
		)
		if err != nil {
			t.Fatal(err)
		}
		if current.Snapshot().State.StateVersion != 257 || !fallback || delta.Valid() || !snapshot.Valid() || fixture.module.projectEventsCalls != 0 {
			t.Fatalf("current=%d delta=%+v snapshot=%+v fallback=%v project calls=%d",
				current.Snapshot().State.StateVersion, delta, snapshot, fallback, fixture.module.projectEventsCalls)
		}
	})

	t.Run("missing batch falls back to snapshot", func(t *testing.T) {
		fixture := newRuntimeServiceFixture(t)
		_, session, err := fixture.service.Start(t.Context(), StartCommand{
			ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
			Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 1), Config: runtimeServiceMessage("game.config", nil),
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fixture.clock.Advance(time.Microsecond)
		result, err := fixture.service.HandleAction(t.Context(), ActionCommand{
			SessionID: session.Snapshot().ID, ActorUserID: fixture.hostID, ActionID: runtimeServiceActionID(t, 2),
			ExpectedStateVersion: 1, OwnershipEpoch: session.Snapshot().OwnershipEpoch,
			VersionKey: session.Snapshot().VersionKey, Command: runtimeServiceMessage("round.roll", nil),
		})
		if err != nil {
			t.Fatal(err)
		}
		// Simulate retention or corruption removing version one while the session has already reached version two.
		fixture.authority.eventBatches = fixture.authority.eventBatches[1:]
		_, delta, snapshot, fallback, err := fixture.service.ProjectEventsCurrent(
			t.Context(), result.Session.Snapshot().ID, 0, viewerFor(fixture),
		)
		if err != nil {
			t.Fatal(err)
		}
		if !fallback || delta.Valid() || !snapshot.Valid() || fixture.module.projectEventsCalls != 0 {
			t.Fatalf("delta=%+v snapshot=%+v fallback=%v project calls=%d", delta, snapshot, fallback, fixture.module.projectEventsCalls)
		}
	})

	t.Run("future and current cursors are explicit", func(t *testing.T) {
		fixture := newRuntimeServiceFixture(t)
		_, session, err := fixture.service.Start(t.Context(), StartCommand{
			ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
			Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 1), Config: runtimeServiceMessage("game.config", nil),
		})
		if err != nil {
			t.Fatal(err)
		}
		currentVersion := session.Snapshot().State.StateVersion
		if _, _, _, _, err := fixture.service.ProjectEventsCurrent(
			t.Context(), session.Snapshot().ID, currentVersion+1, viewerFor(fixture),
		); !errors.Is(err, ErrStateVersionConflict) {
			t.Fatalf("future cursor error = %v", err)
		}
		current, delta, snapshot, fallback, err := fixture.service.ProjectEventsCurrent(
			t.Context(), session.Snapshot().ID, currentVersion, viewerFor(fixture),
		)
		if err != nil || current.Snapshot().State.StateVersion != currentVersion || delta.Valid() || snapshot.Valid() || fallback {
			t.Fatalf("current=%+v delta=%+v snapshot=%+v fallback=%v error=%v", current.Snapshot(), delta, snapshot, fallback, err)
		}
	})
}

func TestRuntimeServiceProjectsOnlyBoundedTerminalReplay(t *testing.T) {
	fixture := newRuntimeServiceFixture(t)
	_, session, err := fixture.service.Start(t.Context(), StartCommand{
		ActorUserID: fixture.hostID, RoomID: fixture.room.Snapshot().ID, GameID: fixture.module.manifest.GameID,
		Expected: fixture.room.Version(), OperationID: runtimeServiceOperationID(t, 1), Config: runtimeServiceMessage("game.config", nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	viewer := game.Viewer{Kind: game.ViewerReplay, UserID: game.Identifier(fixture.hostID.String())}
	if _, err := fixture.service.ProjectReplay(t.Context(), session.Snapshot().ID, viewer, game.ReplayAccessParticipant); !errors.Is(err, ErrReplayUnavailable) || fixture.module.projectReplayCalls != 0 {
		t.Fatalf("live replay error=%v calls=%d", err, fixture.module.projectReplayCalls)
	}
	if _, err := fixture.service.Project(t.Context(), session.Snapshot().ID, viewer); !errors.Is(err, ErrReplayUnavailable) {
		t.Fatalf("live projection accepted replay viewer: %v", err)
	}
	if _, _, _, err := fixture.service.ProjectEvents(t.Context(), session.Snapshot().ID, 0, viewer); !errors.Is(err, ErrReplayUnavailable) {
		t.Fatalf("event projection accepted replay viewer: %v", err)
	}

	_, _ = fixture.clock.Advance(time.Second)
	_, err = fixture.service.HandleAction(t.Context(), ActionCommand{
		SessionID: session.Snapshot().ID, ActorUserID: fixture.hostID, ActionID: runtimeServiceActionID(t, 39),
		ExpectedStateVersion: 1, OwnershipEpoch: session.Snapshot().OwnershipEpoch,
		VersionKey: session.Snapshot().VersionKey, Command: runtimeServiceMessage("session.finish", nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	projection, err := fixture.service.ProjectReplay(t.Context(), session.Snapshot().ID, viewer, game.ReplayAccessParticipant)
	if err != nil || !projection.Valid() || fixture.module.projectReplayCalls != 1 || len(fixture.module.replayEvents) != 2 {
		t.Fatalf("projection=%+v error=%v calls=%d events=%d", projection, err, fixture.module.projectReplayCalls, len(fixture.module.replayEvents))
	}

	fixture.module.unsafeReplayProjection = true
	if _, err := fixture.service.ProjectReplay(t.Context(), session.Snapshot().ID, viewer, game.ReplayAccessParticipant); !errors.Is(err, ErrProjectionUnsafe) {
		t.Fatalf("unsafe replay error=%v", err)
	}
	fixture.module.unsafeReplayProjection = false
	fixture.authority.oversizedReplay = true
	if _, err := fixture.service.ProjectReplay(t.Context(), session.Snapshot().ID, viewer, game.ReplayAccessParticipant); !errors.Is(err, ErrReplayUnavailable) {
		t.Fatalf("oversized replay error=%v", err)
	}
}

type runtimeServiceFixture struct {
	service   *Service
	authority *runtimeServiceAuthority
	registry  *runtimeServiceRegistry
	module    *runtimeServiceModule
	clock     *clock.Fake
	room      roomDomain.Room
	hostID    uuid.UUID
	playerID  uuid.UUID
}

func newRuntimeServiceFixture(t *testing.T) runtimeServiceFixture {
	t.Helper()
	now := time.Date(2026, time.July, 20, 6, 0, 0, 0, time.UTC)
	hostID, playerID := uuid.New(), uuid.New()
	room, err := roomDomain.Restore(roomDomain.RoomSnapshot{
		ID: uuid.New(), RoomCode: "RUNTIME1", Visibility: roomDomain.VisibilityPrivate, Status: roomDomain.RoomStatusLobby,
		HostUserID: hostID, ParticipantCapacity: 9, ParticipantAdmission: roomDomain.AdmissionOpen,
		SpectatorAdmission: roomDomain.AdmissionOpen,
		Members: []roomDomain.MemberSnapshot{
			{UserID: playerID, Role: roomDomain.MemberRoleParticipant, SeatIndex: 1, JoinedAt: now, LastSeenAt: now},
			{UserID: hostID, Role: roomDomain.MemberRoleParticipant, SeatIndex: 4, JoinedAt: now, LastSeenAt: now},
		},
		RoomVersion: 1, MembershipVersion: 1, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	module := &runtimeServiceModule{manifest: runtimeServiceManifest()}
	registry := &runtimeServiceRegistry{module: module}
	authority := &runtimeServiceAuthority{
		room: room, startReceipts: make(map[StartKey]StartReceipt), actionReceipts: make(map[ActionKey]ActionReceipt),
		timerReceipts: make(map[TimerKey]TimerReceipt), systemReceipts: make(map[SystemKey]SystemReceipt),
	}
	fakeClock := clock.NewFake(now.Add(time.Second))
	service, err := NewService(registry, authority, &runtimeServiceRooms{authority: authority}, authority, fakeClock, &runtimeServiceGenerator{})
	if err != nil {
		t.Fatal(err)
	}
	return runtimeServiceFixture{
		service: service, authority: authority, registry: registry, module: module,
		clock: fakeClock, room: room, hostID: hostID, playerID: playerID,
	}
}

type runtimeServiceRegistry struct {
	module  *runtimeServiceModule
	missing bool
}

func (registry *runtimeServiceRegistry) DefaultManifest(context.Context, game.GameID) (game.Manifest, error) {
	return registry.module.manifest.Clone(), nil
}

func (registry *runtimeServiceRegistry) DefaultModule(context.Context, game.GameID) (game.ServerGameModule, error) {
	return registry.module, nil
}

func (registry *runtimeServiceRegistry) Resolve(game.VersionKey) (game.ServerGameModule, error) {
	if registry.missing {
		return nil, game.ErrVersionNotRegistered
	}
	return registry.module, nil
}

type runtimeServiceModule struct {
	manifest               game.Manifest
	createRequest          game.CreateRequest
	createTimers           []game.TimerIntent
	createCalls            int
	commandCalls           int
	timerCalls             int
	systemCalls            int
	unsafeEventProjection  bool
	unsafeReplayProjection bool
	projectEventsCalls     int
	projectReplayCalls     int
	replayEvents           []game.Event
}

func (module *runtimeServiceModule) Manifest() game.Manifest { return module.manifest.Clone() }

func (module *runtimeServiceModule) Create(request game.CreateRequest) (game.Transition, error) {
	module.createCalls++
	module.createRequest = request
	return runtimeServiceTransition(1, false, request.Context.Now, module.createTimers...), nil
}

func (module *runtimeServiceModule) HandleCommand(snapshot game.Snapshot, request game.CommandRequest) (game.Transition, error) {
	module.commandCalls++
	return runtimeServiceTransition(snapshot.StateVersion+1, request.Command.MessageType == "session.finish", request.Context.Now), nil
}

func (module *runtimeServiceModule) HandleTimer(snapshot game.Snapshot, request game.TimerRequest) (game.Transition, error) {
	module.timerCalls++
	return runtimeServiceTransition(snapshot.StateVersion+1, request.Timer.MessageType == "session.finish", request.Context.Now), nil
}

func (module *runtimeServiceModule) HandleSystem(snapshot game.Snapshot, request game.SystemRequest) (game.Transition, error) {
	module.systemCalls++
	return runtimeServiceTransition(snapshot.StateVersion+1, request.System.MessageType == "session.finish", request.Context.Now), nil
}

func (*runtimeServiceModule) Project(snapshot game.Snapshot, _ game.Viewer) (game.Projection, error) {
	return game.Projection{View: runtimeServiceMessage("player.view", []byte{byte(snapshot.StateVersion)}), AllowedActions: []game.Identifier{"roll"}}, nil
}

func (module *runtimeServiceModule) ProjectEvents(_ game.Snapshot, _ []game.VersionedEvent, _ game.Viewer) (game.EventProjection, error) {
	module.projectEventsCalls++
	if module.unsafeEventProjection {
		return game.EventProjection{}, nil
	}
	return game.EventProjection{Messages: []game.Message{runtimeServiceMessage("viewer.delta", nil)}}, nil
}

func (module *runtimeServiceModule) ProjectReplay(events []game.Event, _ game.Viewer, _ game.ReplayAccessPolicy) (game.Projection, error) {
	module.projectReplayCalls++
	module.replayEvents = append([]game.Event(nil), events...)
	if module.unsafeReplayProjection {
		return game.Projection{}, nil
	}
	return game.Projection{View: runtimeServiceMessage("replay.view", nil)}, nil
}

func (*runtimeServiceModule) Migrate(snapshot game.Snapshot, _, _ uint32) (game.Snapshot, error) {
	return snapshot, nil
}

type runtimeServiceGenerator struct{ next uint64 }

func (generator *runtimeServiceGenerator) NewID() (uuid.UUID, error) {
	generator.next++
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(strconv.FormatUint(generator.next, 10))), nil
}

func (generator *runtimeServiceGenerator) NewExecution(at time.Time) (game.DeterministicContext, error) {
	generator.next++
	var seed [game.RandomSeedBytes]byte
	seed[0] = byte(generator.next%255 + 1)
	return game.DeterministicContext{
		Now: at.Round(0).UTC(), RandomSeed: seed,
		AllocatedIDs: []game.Identifier{game.Identifier("allocated-" + strconv.FormatUint(generator.next, 10))},
	}, nil
}

type runtimeServiceAuthority struct {
	room            roomDomain.Room
	session         Session
	startReceipts   map[StartKey]StartReceipt
	actionReceipts  map[ActionKey]ActionReceipt
	timerReceipts   map[TimerKey]TimerReceipt
	systemReceipts  map[SystemKey]SystemReceipt
	eventBatches    []EventBatch
	retrySystemOnce bool
	systemDigests   []idempotency.Digest
	oversizedReplay bool
}

func (authority *runtimeServiceAuthority) Create(context.Context, CreationCommit) (Session, error) {
	return Session{}, ErrInvalidSessionInput
}

func (authority *runtimeServiceAuthority) GetStartReceipt(_ context.Context, key StartKey, digest idempotency.Digest) (StartReceipt, error) {
	receipt, ok := authority.startReceipts[key]
	if !ok {
		return StartReceipt{}, ErrStartReceiptNotFound
	}
	return receipt.Replay(digest)
}

func (authority *runtimeServiceAuthority) Get(_ context.Context, sessionID uuid.UUID) (Session, error) {
	if authority.session.Snapshot().ID != sessionID {
		return Session{}, ErrSessionNotFound
	}
	return authority.session, nil
}

func (authority *runtimeServiceAuthority) AcquireOwnershipCAS(_ context.Context, _ Session, next Session) (Session, error) {
	authority.session = next
	return next, nil
}

func (authority *runtimeServiceAuthority) GetActionReceipt(_ context.Context, key ActionKey, digest idempotency.Digest) (ActionReceipt, error) {
	receipt, ok := authority.actionReceipts[key]
	if !ok {
		return ActionReceipt{}, ErrActionReceiptNotFound
	}
	return receipt.Replay(digest)
}

func (authority *runtimeServiceAuthority) CommitAction(_ context.Context, commit ActionCommit) (ActionCommitResult, error) {
	snapshot := commit.Receipt().Snapshot()
	if receipt, ok := authority.actionReceipts[snapshot.Key]; ok {
		replayed, err := receipt.Replay(snapshot.RequestDigest)
		return ActionCommitResult{Session: authority.session, Receipt: replayed, Replayed: true}, err
	}
	authority.session = commit.After()
	authority.actionReceipts[snapshot.Key] = commit.Receipt()
	authority.eventBatches = append(authority.eventBatches, commit.Batch())
	return ActionCommitResult{Session: authority.session, Receipt: commit.Receipt()}, nil
}

func (authority *runtimeServiceAuthority) GetTimerReceipt(_ context.Context, key TimerKey) (TimerReceipt, error) {
	receipt, ok := authority.timerReceipts[key]
	if !ok {
		return TimerReceipt{}, ErrTimerReceiptNotFound
	}
	return receipt, nil
}

func (authority *runtimeServiceAuthority) CommitTimer(_ context.Context, commit TimerCommit) (TimerCommitResult, error) {
	key := commit.Receipt().Snapshot().Key
	if receipt, ok := authority.timerReceipts[key]; ok {
		return TimerCommitResult{Session: authority.session, Receipt: receipt, Replayed: true}, nil
	}
	authority.session = commit.After()
	authority.timerReceipts[key] = commit.Receipt()
	authority.eventBatches = append(authority.eventBatches, commit.Batch())
	return TimerCommitResult{Session: authority.session, Receipt: commit.Receipt()}, nil
}

func (authority *runtimeServiceAuthority) GetSystemReceipt(_ context.Context, key SystemKey, digest idempotency.Digest) (SystemReceipt, error) {
	receipt, ok := authority.systemReceipts[key]
	if !ok {
		return SystemReceipt{}, ErrSystemReceiptNotFound
	}
	return receipt.Replay(digest)
}

func (authority *runtimeServiceAuthority) CommitSystem(_ context.Context, commit SystemCommit) (SystemCommitResult, error) {
	authority.systemDigests = append(authority.systemDigests, commit.Receipt().Snapshot().RequestDigest)
	if authority.retrySystemOnce {
		authority.retrySystemOnce = false
		concurrent := commit.After().Snapshot()
		concurrent.State.State = runtimeServiceMessage("concurrent.state", []byte{byte(concurrent.State.StateVersion)})
		advanced, err := RestoreSession(concurrent)
		if err != nil {
			return SystemCommitResult{}, err
		}
		authority.session = advanced
		return SystemCommitResult{Session: authority.session, Retry: true}, nil
	}
	authority.session = commit.After()
	authority.systemReceipts[commit.Receipt().Snapshot().Key] = commit.Receipt()
	authority.eventBatches = append(authority.eventBatches, commit.Batch())
	return SystemCommitResult{Session: authority.session, Receipt: commit.Receipt()}, nil
}

func (authority *runtimeServiceAuthority) CompleteSystemNoop(
	context.Context, SystemKey, idempotency.Digest, time.Time,
) (SystemCommitResult, error) {
	return SystemCommitResult{Session: authority.session}, nil
}

func (authority *runtimeServiceAuthority) CommitLifecycle(_ context.Context, commit LifecycleCommit) (Session, error) {
	authority.session = commit.After()
	return authority.session, nil
}

func (*runtimeServiceAuthority) ListDueTimers(context.Context, time.Time, uint32) ([]DueTimer, error) {
	return []DueTimer{}, nil
}

func (authority *runtimeServiceAuthority) ReadEventBatches(_ context.Context, sessionID uuid.UUID, after uint64, limit uint32) ([]EventBatch, error) {
	if authority.oversizedReplay && len(authority.eventBatches) > 0 {
		result := make([]EventBatch, maximumReplayBatches+1)
		for index := range result {
			result[index] = authority.eventBatches[0]
		}
		return result, nil
	}
	result := make([]EventBatch, 0, len(authority.eventBatches))
	for _, batch := range authority.eventBatches {
		snapshot := batch.Snapshot()
		if snapshot.SessionID == sessionID && snapshot.StateVersion > after {
			result = append(result, batch)
			if uint32(len(result)) == limit {
				break
			}
		}
	}
	return result, nil
}

func (authority *runtimeServiceAuthority) Start(
	_ context.Context, _ roomDomain.Room, after roomDomain.Room, commit CreationCommit, receipt StartReceipt,
) (roomDomain.Room, Session, bool, error) {
	key := receipt.Snapshot().Key
	if existing, ok := authority.startReceipts[key]; ok {
		if _, err := existing.Replay(receipt.Snapshot().RequestDigest); err != nil {
			return roomDomain.Room{}, Session{}, false, err
		}
		return authority.room, authority.session, true, nil
	}
	authority.room, authority.session = after, commit.Session
	authority.startReceipts[key] = receipt
	authority.eventBatches = append(authority.eventBatches, commit.Batch)
	return after, commit.Session, false, nil
}

func (authority *runtimeServiceAuthority) FinishAction(
	_ context.Context, _ roomDomain.Room, after roomDomain.Room, commit ActionCommit,
) (roomDomain.Room, ActionCommitResult, error) {
	result, err := authority.CommitAction(context.Background(), commit)
	authority.room = after
	return after, result, err
}

func (authority *runtimeServiceAuthority) FinishTimer(
	_ context.Context, _ roomDomain.Room, after roomDomain.Room, commit TimerCommit,
) (roomDomain.Room, TimerCommitResult, error) {
	result, err := authority.CommitTimer(context.Background(), commit)
	authority.room = after
	return after, result, err
}

func (authority *runtimeServiceAuthority) FinishSystem(
	_ context.Context, _ roomDomain.Room, after roomDomain.Room, _ uuid.UUID, commit SystemCommit,
) (roomDomain.Room, SystemCommitResult, error) {
	result, err := authority.CommitSystem(context.Background(), commit)
	if !result.Retry {
		authority.room = after
	}
	return authority.room, result, err
}

func (authority *runtimeServiceAuthority) Cancel(
	_ context.Context, _ roomDomain.Room, after roomDomain.Room, commit LifecycleCommit,
) (roomDomain.Room, Session, error) {
	session, err := authority.CommitLifecycle(context.Background(), commit)
	authority.room = after
	return after, session, err
}

type runtimeServiceRooms struct{ authority *runtimeServiceAuthority }

func (rooms *runtimeServiceRooms) Create(_ context.Context, room roomDomain.Room) (roomDomain.Room, error) {
	rooms.authority.room = room
	return room, nil
}

func (rooms *runtimeServiceRooms) GetByID(_ context.Context, roomID uuid.UUID) (roomDomain.Room, error) {
	if rooms.authority.room.Snapshot().ID != roomID {
		return roomDomain.Room{}, roomDomain.ErrRoomNotFound
	}
	return rooms.authority.room, nil
}

func (*runtimeServiceRooms) GetByCode(context.Context, string) (roomDomain.Room, error) {
	return roomDomain.Room{}, roomDomain.ErrRoomNotFound
}

func (rooms *runtimeServiceRooms) UpdateCAS(_ context.Context, _ roomDomain.Room, after roomDomain.Room) (roomDomain.Room, error) {
	rooms.authority.room = after
	return after, nil
}

func runtimeServiceManifest() game.Manifest {
	return game.Manifest{
		GameID: "liars-dice", Versions: game.VersionSet{Engine: "1.0.0", Protocol: "1.0.0", Client: "1.0.0"},
		Participants: game.ParticipantLimits{Minimum: 2, Maximum: 9},
		Capabilities: game.Capabilities{Submission: game.SubmissionModeTurnBased, Timers: true, Spectating: true, Replay: true, Reveal: game.RevealPolicyRuleControlled},
		Presentation: game.PresentationPreferences{TableShape: game.TableShapeAdaptive, Orientation: game.OrientationPortraitPreferred, ActionDock: game.ActionDockSeatAnchored},
		Themes:       game.ThemePreferences{Default: "classic", Fallback: "classic", Variants: []game.Identifier{"classic"}},
	}
}

func runtimeServiceTransition(stateVersion uint64, finished bool, at time.Time, timers ...game.TimerIntent) game.Transition {
	return game.Transition{
		Snapshot: game.Snapshot{SnapshotVersion: 1, StateVersion: stateVersion, State: runtimeServiceMessage("game.state", []byte{byte(stateVersion)})},
		Events:   []game.Event{{Message: runtimeServiceMessage("game.changed", []byte{byte(stateVersion)})}}, Timers: timers,
		Finished: finished,
	}
}

func runtimeServiceMessage(messageType game.Identifier, payload []byte) game.Message {
	return game.Message{MessageType: messageType, SchemaVersion: 1, Payload: append([]byte(nil), payload...)}
}

func runtimeServiceActionID(t testing.TB, marker byte) game.ActionID {
	t.Helper()
	return game.ActionID(runtimeServiceOperationID(t, marker).Value())
}

func runtimeServiceOperationID(t testing.TB, marker byte) idempotency.OperationID {
	t.Helper()
	value := make([]byte, 16)
	for index := range value {
		value[index] = marker
	}
	operationID, err := idempotency.NewOperationID(value)
	if err != nil {
		t.Fatal(err)
	}
	return operationID
}

var _ game.RuntimeServerGameModule = (*runtimeServiceModule)(nil)
var _ Store = (*runtimeServiceAuthority)(nil)
var _ roomDomain.Repository = (*runtimeServiceRooms)(nil)
var _ RoomSessionStore = (*runtimeServiceAuthority)(nil)
