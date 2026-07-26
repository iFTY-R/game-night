package gameruntime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/outbox"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

const (
	// runtimeAllocatedIDCount gives pure modules a bounded pool without allowing process-local ID generation.
	runtimeAllocatedIDCount = 256
	// maximumSystemRecomputations bounds contention retries while preserving the pending durable operation.
	maximumSystemRecomputations = 4
)

// Registry exposes exact recovery lookup and the explicit default used only for a new session.
type Registry interface {
	DefaultManifest(context.Context, game.GameID) (game.Manifest, error)
	DefaultModule(context.Context, game.GameID) (game.ServerGameModule, error)
	Resolve(game.VersionKey) (game.ServerGameModule, error)
}

// RoomSessionStore is the cross-aggregate transaction boundary for every room pointer transition.
type RoomSessionStore interface {
	GetStartReceipt(context.Context, StartKey, idempotency.Digest) (StartReceipt, error)
	Start(context.Context, roomDomain.Room, roomDomain.Room, CreationCommit, StartReceipt) (roomDomain.Room, Session, bool, error)
	FinishAction(context.Context, roomDomain.Room, roomDomain.Room, ActionCommit) (roomDomain.Room, ActionCommitResult, error)
	FinishTimer(context.Context, roomDomain.Room, roomDomain.Room, TimerCommit) (roomDomain.Room, TimerCommitResult, error)
	FinishSystem(context.Context, roomDomain.Room, roomDomain.Room, uuid.UUID, SystemCommit) (roomDomain.Room, SystemCommitResult, error)
	Suspend(context.Context, roomDomain.Room, roomDomain.Room, LifecycleCommit) (roomDomain.Room, Session, error)
	Resume(context.Context, roomDomain.Room, roomDomain.Room, LifecycleCommit) (roomDomain.Room, Session, error)
	Cancel(context.Context, roomDomain.Room, roomDomain.Room, LifecycleCommit) (roomDomain.Room, Session, error)
}

// Generator creates persisted identifiers and deterministic engine inputs outside pure game modules.
type Generator interface {
	NewID() (uuid.UUID, error)
	NewExecution(time.Time) (game.DeterministicContext, error)
}

// SecureGenerator is the production entropy and UUIDv7 implementation.
type SecureGenerator struct{}

// NewID returns a time-sortable server-owned identifier.
func (SecureGenerator) NewID() (uuid.UUID, error) { return uuid.NewV7() }

// NewExecution fills the complete deterministic ID pool and a cryptographic 256-bit seed.
func (SecureGenerator) NewExecution(at time.Time) (game.DeterministicContext, error) {
	// Runtime timestamps cross PostgreSQL and outbox boundaries, both of which
	// persist microseconds. Modules must receive the same value replay will see.
	at = canonicalRuntimeTime(at)
	if at.IsZero() {
		return game.DeterministicContext{}, ErrInvalidSessionInput
	}
	var execution game.DeterministicContext
	execution.Now = at
	if _, err := rand.Read(execution.RandomSeed[:]); err != nil {
		return game.DeterministicContext{}, ErrInvalidSessionInput
	}
	execution.AllocatedIDs = make([]game.Identifier, runtimeAllocatedIDCount)
	for index := range execution.AllocatedIDs {
		id, err := uuid.NewV7()
		if err != nil {
			return game.DeterministicContext{}, ErrInvalidSessionInput
		}
		execution.AllocatedIDs[index] = game.Identifier(id.String())
	}
	return execution, nil
}

// Service coordinates authenticated PartyRoom state, pure modules, and authoritative persistence.
type Service struct {
	registry     Registry
	sessions     Store
	rooms        roomDomain.Repository
	roomSessions RoomSessionStore
	clock        clock.Clock
	generator    Generator
}

// NewService requires every authority used by creation, transition, projection, and finish flows.
func NewService(
	registry Registry,
	sessions Store,
	rooms roomDomain.Repository,
	roomSessions RoomSessionStore,
	source clock.Clock,
	generator Generator,
) (*Service, error) {
	if registry == nil || sessions == nil || rooms == nil || roomSessions == nil || source == nil || generator == nil {
		return nil, ErrInvalidSessionInput
	}
	return &Service{registry: registry, sessions: sessions, rooms: rooms, roomSessions: roomSessions, clock: source, generator: generator}, nil
}

// StartCommand contains untrusted game configuration and the authenticated room-host CAS input.
type StartCommand struct {
	ActorUserID uuid.UUID
	RoomID      uuid.UUID
	GameID      game.GameID
	Expected    roomDomain.Version
	// ConfigRevision is optional until the transport layer can forward the room-rule revision into runtime starts.
	ConfigRevision uint64
	Config         game.Message
	// PendingStartProof is an optional opaque countdown proof that must be atomically consumed with the start commit.
	PendingStartProof *PendingStartProof
	OperationID       idempotency.OperationID
	// RequestDigest is an optional client echo of the server canonical start binding.
	RequestDigest *idempotency.Digest
}

// Start creates the room transition and GameSession creation commit before publishing both atomically.
func (service *Service) Start(ctx context.Context, command StartCommand) (roomDomain.Room, Session, error) {
	if service == nil || ctx == nil || command.ActorUserID == uuid.Nil || command.RoomID == uuid.Nil ||
		!command.Config.Valid() || !command.OperationID.Valid() || command.Expected.Room == 0 || command.Expected.Membership == 0 {
		return roomDomain.Room{}, Session{}, ErrInvalidSessionInput
	}
	if command.PendingStartProof != nil && !command.PendingStartProof.Valid() {
		return roomDomain.Room{}, Session{}, ErrInvalidSessionInput
	}
	if _, err := game.ParseGameID(string(command.GameID)); err != nil {
		return roomDomain.Room{}, Session{}, ErrInvalidSessionInput
	}
	requestDigest := startDigest(command)
	if command.RequestDigest != nil && *command.RequestDigest != requestDigest {
		return roomDomain.Room{}, Session{}, idempotency.ErrConflict
	}
	startKey := StartKey{ActorUserID: command.ActorUserID, RoomID: command.RoomID, OperationID: command.OperationID}
	if receipt, receiptErr := service.roomSessions.GetStartReceipt(ctx, startKey, requestDigest); receiptErr == nil {
		return service.replayStart(ctx, receipt)
	} else if !errors.Is(receiptErr, ErrStartReceiptNotFound) {
		return roomDomain.Room{}, Session{}, receiptErr
	}
	manifest, module, err := service.defaultRuntimeModule(ctx, command.GameID)
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	room, err := service.rooms.GetByID(ctx, command.RoomID)
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	sessionID, err := service.generator.NewID()
	if err != nil {
		return roomDomain.Room{}, Session{}, ErrInvalidSessionInput
	}
	at := service.clock.Now().Round(0).UTC()
	nextRoom, start, err := room.StartSession(
		command.ActorUserID, sessionID, string(command.GameID), manifest.Participants.Minimum, manifest.Participants.Maximum,
		command.Expected, at,
	)
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	execution, err := service.generator.NewExecution(start.StartedAt)
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	sdkParticipants, runtimeParticipants := mapFrozenParticipants(start.Participants)
	startingSeat, ok := trustedStartingSeat(room.Snapshot().HostUserID, start.Participants)
	if !ok {
		return roomDomain.Room{}, Session{}, ErrInvalidSessionInput
	}
	createRequest := game.CreateRequest{
		Context: execution,
		StartContext: game.SessionStartContext{
			HostUserID: game.Identifier(room.Snapshot().HostUserID.String()), StartingSeat: startingSeat,
		},
		Participants: sdkParticipants,
		Config:       command.Config.Clone(),
	}
	if err := createRequest.Validate(manifest.Participants); err != nil {
		return roomDomain.Room{}, Session{}, ErrInvalidSessionInput
	}
	transition, err := module.Create(createRequest)
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	batchID, err := service.generator.NewID()
	if err != nil {
		return roomDomain.Room{}, Session{}, ErrInvalidSessionInput
	}
	session, batch, err := NewSession(CreateRequest{
		SessionID: sessionID, RoomID: command.RoomID, VersionKey: manifest.Key(), Participants: runtimeParticipants,
		Start: FrozenStartConfig{
			Config:             command.Config.Clone(),
			ConfigDigest:       startConfigDigest(manifest.Key(), command.Config),
			ConfigRevision:     command.ConfigRevision,
			RoomVersion:        command.Expected.Room,
			MembershipVersion:  command.Expected.Membership,
			RoomOwnershipEpoch: room.Snapshot().OwnershipEpoch,
		},
		BatchID: batchID, Execution: execution, Input: command.Config.Clone(), Transition: transition,
	})
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	event, err := service.newOutboxEvent(GameSessionCreatedEventType, sessionID, 1, start.StartedAt)
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	receipt, err := NewStartReceipt(StartReceiptSnapshot{
		Key: startKey, RequestDigest: requestDigest, SessionID: sessionID, CommittedAt: start.StartedAt,
	})
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	storedRoom, storedSession, replayed, err := service.roomSessions.Start(ctx, room, nextRoom, CreationCommit{
		Session: session, Batch: batch, OutboxEvents: []outbox.Event{event}, PendingStartProof: command.PendingStartProof,
	}, receipt)
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	if replayed && storedSession.Snapshot().OwnershipEpoch > 0 {
		return storedRoom, storedSession, nil
	}
	// Epoch zero is never allowed to process commands. Advancing it after the atomic start leaves a recoverable,
	// fail-closed window if this process exits before ownership acquisition commits.
	owned, err := storedSession.AcquireOwnership(0, start.StartedAt.Add(time.Microsecond))
	if err != nil {
		return storedRoom, Session{}, err
	}
	owned, err = service.sessions.AcquireOwnershipCAS(ctx, storedSession, owned)
	if err != nil {
		return storedRoom, Session{}, err
	}
	return storedRoom, owned, nil
}

// replayStart returns the current authoritative aggregates for the original operation result.
// An epoch-zero session is a recoverable crash window and is claimed before it can accept commands.
func (service *Service) replayStart(ctx context.Context, receipt StartReceipt) (roomDomain.Room, Session, error) {
	snapshot := receipt.Snapshot()
	room, err := service.rooms.GetByID(ctx, snapshot.Key.RoomID)
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	session, err := service.sessions.Get(ctx, snapshot.SessionID)
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	if session.Snapshot().RoomID != snapshot.Key.RoomID {
		return roomDomain.Room{}, Session{}, ErrGameSessionIntegrity
	}
	if session.Snapshot().OwnershipEpoch > 0 {
		return room, session, nil
	}
	at := service.clock.Now().Round(0).UTC()
	if !at.After(session.Snapshot().UpdatedAt) {
		at = session.Snapshot().UpdatedAt.Add(time.Microsecond)
	}
	owned, err := session.AcquireOwnership(0, at)
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	owned, err = service.sessions.AcquireOwnershipCAS(ctx, session, owned)
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	return room, owned, nil
}

// ActionCommand is one authenticated player command against an exact session release and ownership epoch.
type ActionCommand struct {
	SessionID            uuid.UUID
	ActorUserID          uuid.UUID
	ActionID             game.ActionID
	ExpectedStateVersion uint64
	OwnershipEpoch       uint64
	VersionKey           game.VersionKey
	Command              game.Message
	// RequestDigest is an optional client echo of the server canonical request binding.
	// It is never trusted as the persisted digest; a mismatch rejects the request before any replay or module call.
	RequestDigest *idempotency.Digest
}

// ActionResult contains only a durable receipt and the caller's viewer-safe current projection.
type ActionResult struct {
	Session    Session
	Receipt    ActionReceipt
	Projection game.Projection
	Replayed   bool
}

// HandleAction invokes a pure module and lets PostgreSQL recheck participant authority before any receipt replay or write.
func (service *Service) HandleAction(ctx context.Context, command ActionCommand) (ActionResult, error) {
	if service == nil || ctx == nil || command.SessionID == uuid.Nil || command.ActorUserID == uuid.Nil ||
		!command.ActionID.Valid() || command.ExpectedStateVersion == 0 || command.OwnershipEpoch == 0 ||
		!command.VersionKey.Valid() || !command.Command.Valid() {
		return ActionResult{}, ErrInvalidSessionInput
	}
	requestDigest := actionDigest(command)
	if command.RequestDigest != nil && *command.RequestDigest != requestDigest {
		return ActionResult{}, idempotency.ErrConflict
	}
	operationID, err := idempotency.ParseOperationID(string(command.ActionID))
	if err != nil {
		return ActionResult{}, ErrInvalidSessionInput
	}
	actionKey := ActionKey{SessionID: command.SessionID, ActorUserID: command.ActorUserID, ActionID: operationID}
	if receipt, receiptErr := service.sessions.GetActionReceipt(ctx, actionKey, requestDigest); receiptErr == nil {
		current, getErr := service.sessions.Get(ctx, command.SessionID)
		if getErr != nil {
			return ActionResult{}, getErr
		}
		module, resolveErr := service.registry.Resolve(current.Snapshot().VersionKey)
		if resolveErr != nil {
			if current.Snapshot().Status == StatusActive {
				return ActionResult{}, service.suspendMissingModule(ctx, current, command.OwnershipEpoch)
			}
			return ActionResult{}, ErrModuleUnavailable
		}
		projection, projectErr := projectPlayer(module, current, command.ActorUserID)
		if projectErr != nil {
			return ActionResult{}, projectErr
		}
		return ActionResult{Session: current, Receipt: receipt, Projection: projection, Replayed: true}, nil
	} else if !errors.Is(receiptErr, ErrActionReceiptNotFound) {
		return ActionResult{}, receiptErr
	}
	before, err := service.sessions.Get(ctx, command.SessionID)
	if err != nil {
		return ActionResult{}, err
	}
	if before.Snapshot().VersionKey != command.VersionKey || before.Snapshot().State.StateVersion != command.ExpectedStateVersion {
		return ActionResult{}, ErrStateVersionConflict
	}
	if before.Snapshot().Status == StatusSuspended {
		return ActionResult{}, ErrSessionSuspended
	}
	if before.Snapshot().Status.Terminal() {
		return ActionResult{}, ErrSessionTerminal
	}
	module, err := service.registry.Resolve(command.VersionKey)
	if err != nil {
		return ActionResult{}, service.suspendMissingModule(ctx, before, command.OwnershipEpoch)
	}
	execution, err := service.generator.NewExecution(service.clock.Now())
	if err != nil {
		return ActionResult{}, err
	}
	transition, err := module.HandleCommand(before.Snapshot().State, game.CommandRequest{
		Context: execution, ActorUserID: game.Identifier(command.ActorUserID.String()), ActionID: command.ActionID,
		ExpectedStateVersion: command.ExpectedStateVersion, Command: command.Command.Clone(),
	})
	if err != nil {
		return ActionResult{}, err
	}
	batchID, err := service.generator.NewID()
	if err != nil {
		return ActionResult{}, ErrInvalidSessionInput
	}
	after, batch, err := before.ApplyAction(ActionTransitionRequest{
		BatchID: batchID, OwnershipEpoch: command.OwnershipEpoch, ActorUserID: command.ActorUserID,
		ActionID: operationID, Execution: execution, Input: command.Command.Clone(), Transition: transition,
	})
	if err != nil {
		return ActionResult{}, err
	}
	receipt, err := NewActionReceipt(ActionReceiptSnapshot{
		Key:           actionKey,
		RequestDigest: requestDigest, ResultCode: ResultCodeAccepted,
		ResultDigest: transitionResultDigest(command.SessionID, after.Snapshot().State.StateVersion, ResultCodeAccepted),
		StateVersion: after.Snapshot().State.StateVersion, CommittedAt: execution.Now,
	})
	if err != nil {
		return ActionResult{}, err
	}
	event, err := service.newOutboxEvent(GameSessionTransitionedEventType, command.SessionID, after.Snapshot().State.StateVersion, execution.Now)
	if err != nil {
		return ActionResult{}, err
	}
	commit, err := NewActionCommit(before, after, batch, receipt, []outbox.Event{event})
	if err != nil {
		return ActionResult{}, err
	}
	var committed ActionCommitResult
	if after.Snapshot().Status == StatusFinished {
		room, nextRoom, finishErr := service.prepareRoomFinish(ctx, before.Snapshot().RoomID, before.Snapshot().ID, execution.Now)
		if finishErr != nil {
			return ActionResult{}, finishErr
		}
		_, committed, err = service.roomSessions.FinishAction(ctx, room, nextRoom, commit)
	} else {
		committed, err = service.sessions.CommitAction(ctx, commit)
	}
	if err != nil {
		return ActionResult{}, err
	}
	projection, err := projectPlayer(module, committed.Session, command.ActorUserID)
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{Session: committed.Session, Receipt: committed.Receipt, Projection: projection, Replayed: committed.Replayed}, nil
}

// HandleTimer executes one persisted scheduling candidate and relies on the store to recheck the timer row under lock.
func (service *Service) HandleTimer(ctx context.Context, due DueTimer, ownershipEpoch uint64) (TimerCommitResult, error) {
	if service == nil || ctx == nil || due.SessionID == uuid.Nil || ownershipEpoch == 0 || due.ExpectedStateVersion == 0 || !due.Message.Valid() {
		return TimerCommitResult{}, ErrInvalidSessionInput
	}
	key := TimerKey{SessionID: due.SessionID, TimerID: due.TimerID, ExpectedStateVersion: due.ExpectedStateVersion}
	if receipt, receiptErr := service.sessions.GetTimerReceipt(ctx, key); receiptErr == nil {
		current, getErr := service.sessions.Get(ctx, due.SessionID)
		if getErr != nil {
			return TimerCommitResult{}, getErr
		}
		return TimerCommitResult{Session: current, Receipt: receipt, Replayed: true}, nil
	} else if !errors.Is(receiptErr, ErrTimerReceiptNotFound) {
		return TimerCommitResult{}, receiptErr
	}
	before, err := service.sessions.Get(ctx, due.SessionID)
	if err != nil {
		return TimerCommitResult{}, err
	}
	if before.Snapshot().Status == StatusSuspended {
		return TimerCommitResult{}, ErrSessionSuspended
	}
	if before.Snapshot().Status.Terminal() {
		return TimerCommitResult{}, ErrSessionTerminal
	}
	module, err := service.registry.Resolve(before.Snapshot().VersionKey)
	if err != nil {
		return TimerCommitResult{}, service.suspendMissingModule(ctx, before, ownershipEpoch)
	}
	execution, err := service.generator.NewExecution(service.clock.Now())
	if err != nil {
		return TimerCommitResult{}, err
	}
	transition, err := module.HandleTimer(before.Snapshot().State, game.TimerRequest{
		Context: execution, TimerID: due.TimerID, ExpectedStateVersion: due.ExpectedStateVersion, Timer: due.Message.Clone(),
	})
	if err != nil {
		return TimerCommitResult{}, err
	}
	batchID, err := service.generator.NewID()
	if err != nil {
		return TimerCommitResult{}, ErrInvalidSessionInput
	}
	after, batch, err := before.ApplyTimer(TimerTransitionRequest{
		BatchID: batchID, OwnershipEpoch: ownershipEpoch, TimerID: due.TimerID,
		ExpectedStateVersion: due.ExpectedStateVersion, Execution: execution, Input: due.Message.Clone(), Transition: transition,
	})
	if err != nil {
		return TimerCommitResult{}, err
	}
	receipt, err := NewTimerReceipt(TimerReceiptSnapshot{
		Key:        TimerKey{SessionID: due.SessionID, TimerID: due.TimerID, ExpectedStateVersion: due.ExpectedStateVersion},
		ResultCode: ResultCodeAccepted, ResultDigest: transitionResultDigest(due.SessionID, after.Snapshot().State.StateVersion, ResultCodeAccepted),
		StateVersion: after.Snapshot().State.StateVersion, CommittedAt: execution.Now,
	})
	if err != nil {
		return TimerCommitResult{}, err
	}
	event, err := service.newOutboxEvent(GameSessionTransitionedEventType, due.SessionID, after.Snapshot().State.StateVersion, execution.Now)
	if err != nil {
		return TimerCommitResult{}, err
	}
	commit, err := NewTimerCommit(before, after, batch, receipt, []outbox.Event{event})
	if err != nil {
		return TimerCommitResult{}, err
	}
	if after.Snapshot().Status == StatusFinished {
		room, nextRoom, finishErr := service.prepareRoomFinish(ctx, before.Snapshot().RoomID, before.Snapshot().ID, execution.Now)
		if finishErr != nil {
			return TimerCommitResult{}, finishErr
		}
		_, result, finishErr := service.roomSessions.FinishTimer(ctx, room, nextRoom, commit)
		return result, finishErr
	}
	return service.sessions.CommitTimer(ctx, commit)
}

// SystemCommand is a durable platform command. Outbox/platform digests exclude recomputed versions;
// HostAPI digests bind the caller's optimistic state version while every source excludes the ownership epoch.
type SystemCommand struct {
	SessionID            uuid.UUID
	OperationID          idempotency.OperationID
	Source               SystemSource
	ExpectedStateVersion uint64
	OwnershipEpoch       uint64
	VersionKey           game.VersionKey
	Message              game.Message
	// RequestDigest is an optional client echo of the server canonical operation binding.
	// Platform-originated callers omit it; a supplied mismatch is rejected before receipt lookup.
	RequestDigest *idempotency.Digest
}

// HandleSystem recomputes durable platform work after contention while keeping HostAPI commands strictly optimistic.
func (service *Service) HandleSystem(ctx context.Context, command SystemCommand) (SystemCommitResult, error) {
	if service == nil || ctx == nil || command.SessionID == uuid.Nil || !command.OperationID.Valid() || !command.Source.Valid() ||
		command.ExpectedStateVersion == 0 || command.OwnershipEpoch == 0 || !command.VersionKey.Valid() || !command.Message.Valid() {
		return SystemCommitResult{}, ErrInvalidSessionInput
	}
	if command.Source.Kind == SystemSourceHostAPI {
		if err := service.authorizeCurrentHostSystem(ctx, command.SessionID, command.Source.RequestedByUserID); err != nil {
			return SystemCommitResult{}, err
		}
	}
	logicalDigest := systemDigest(command)
	if command.RequestDigest != nil && *command.RequestDigest != logicalDigest {
		return SystemCommitResult{}, idempotency.ErrConflict
	}
	key := SystemKey{SessionID: command.SessionID, OperationID: command.OperationID, Source: command.Source}
	if receipt, receiptErr := service.sessions.GetSystemReceipt(ctx, key, logicalDigest); receiptErr == nil {
		current, getErr := service.sessions.Get(ctx, command.SessionID)
		if getErr != nil {
			return SystemCommitResult{}, getErr
		}
		return SystemCommitResult{Session: current, Receipt: receipt, Replayed: true}, nil
	} else if !errors.Is(receiptErr, ErrSystemReceiptNotFound) && !errors.Is(receiptErr, ErrSystemOperationPending) {
		return SystemCommitResult{}, receiptErr
	}
	for range maximumSystemRecomputations {
		before, err := service.sessions.Get(ctx, command.SessionID)
		if err != nil {
			return SystemCommitResult{}, err
		}
		if before.Snapshot().VersionKey != command.VersionKey {
			return SystemCommitResult{}, ErrStateVersionConflict
		}
		// HostAPI is a user-facing optimistic command. Unlike durable room-outbox work,
		// an old finish request must not silently apply to a newer game state.
		if command.Source.Kind == SystemSourceHostAPI && before.Snapshot().State.StateVersion != command.ExpectedStateVersion {
			return SystemCommitResult{}, ErrStateVersionConflict
		}
		if before.Snapshot().Status.Terminal() {
			return service.sessions.CompleteSystemNoop(ctx, key, logicalDigest, service.clock.Now())
		}
		module, err := service.registry.Resolve(before.Snapshot().VersionKey)
		if err != nil {
			return SystemCommitResult{}, service.suspendMissingModule(ctx, before, command.OwnershipEpoch)
		}
		systemModule, ok := module.(game.SystemGameModule)
		if !ok {
			return SystemCommitResult{}, service.suspendMissingModule(ctx, before, command.OwnershipEpoch)
		}
		execution, err := service.generator.NewExecution(service.clock.Now())
		if err != nil {
			return SystemCommitResult{}, err
		}
		operationID := game.ActionID(command.OperationID.Value())
		transition, err := systemModule.HandleSystem(before.Snapshot().State, game.SystemRequest{
			Context: execution, SystemOperationID: operationID,
			SourceEventID:        game.Identifier(command.Source.EventID.String()),
			RequestedByUserID:    game.Identifier(optionalUserIDString(command.Source.RequestedByUserID)),
			ExpectedStateVersion: before.Snapshot().State.StateVersion, System: command.Message.Clone(),
		})
		if err != nil {
			return SystemCommitResult{}, err
		}
		if command.Source.Kind == SystemSourceHostAPI && !transition.Finished {
			return SystemCommitResult{}, ErrInvalidSystemCommit
		}
		batchID, err := service.generator.NewID()
		if err != nil {
			return SystemCommitResult{}, ErrInvalidSessionInput
		}
		after, batch, err := before.ApplySystem(SystemTransitionRequest{
			BatchID: batchID, OwnershipEpoch: command.OwnershipEpoch, ExpectedStateVersion: before.Snapshot().State.StateVersion,
			SystemOperationID: command.OperationID, Source: command.Source, RequestDigest: logicalDigest,
			Execution: execution, Input: command.Message.Clone(), Transition: transition,
		})
		if err != nil {
			return SystemCommitResult{}, err
		}
		receipt, err := NewSystemReceipt(SystemReceiptSnapshot{
			Key: key, RequestDigest: logicalDigest, ResultCode: ResultCodeAccepted,
			ResultDigest: transitionResultDigest(command.SessionID, after.Snapshot().State.StateVersion, ResultCodeAccepted),
			StateVersion: after.Snapshot().State.StateVersion, CommittedAt: execution.Now,
		})
		if err != nil {
			return SystemCommitResult{}, err
		}
		event, err := service.newOutboxEvent(GameSessionTransitionedEventType, command.SessionID, after.Snapshot().State.StateVersion, execution.Now)
		if err != nil {
			return SystemCommitResult{}, err
		}
		commit, err := NewSystemCommit(before, after, batch, receipt, []outbox.Event{event})
		if err != nil {
			return SystemCommitResult{}, err
		}
		var result SystemCommitResult
		if after.Snapshot().Status == StatusFinished {
			room, nextRoom, finishErr := service.prepareRoomFinish(ctx, before.Snapshot().RoomID, before.Snapshot().ID, execution.Now)
			if finishErr != nil {
				return SystemCommitResult{}, finishErr
			}
			_, result, err = service.roomSessions.FinishSystem(ctx, room, nextRoom, command.Source.RequestedByUserID, commit)
		} else {
			result, err = service.sessions.CommitSystem(ctx, commit)
		}
		if err != nil {
			return SystemCommitResult{}, err
		}
		if !result.Retry {
			return result, nil
		}
		if command.Source.Kind == SystemSourceHostAPI {
			return SystemCommitResult{}, ErrStateVersionConflict
		}
		command.ExpectedStateVersion = result.Session.Snapshot().State.StateVersion
		command.OwnershipEpoch = result.Session.Snapshot().OwnershipEpoch
	}
	return SystemCommitResult{}, ErrSystemOperationPending
}

// authorizeCurrentHostSystem protects receipt and terminal no-op fast paths with current PartyRoom authority.
func (service *Service) authorizeCurrentHostSystem(ctx context.Context, sessionID, actorUserID uuid.UUID) error {
	session, err := service.sessions.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	room, err := service.rooms.GetByID(ctx, session.Snapshot().RoomID)
	if err != nil {
		return err
	}
	if room.Snapshot().HostUserID != actorUserID {
		return roomDomain.ErrHostRequired
	}
	return nil
}

// CancelCommand is an already-authorized administrative terminal request.
type CancelCommand struct {
	RoomID         uuid.UUID
	SessionID      uuid.UUID
	ExpectedRoom   roomDomain.Version
	OwnershipEpoch uint64
	// OperationID scopes administrator-triggered cancellation retries when the caller provides one.
	OperationID idempotency.OperationID
	// RequestDigest optionally echoes the canonical cancellation binding and is rejected on mismatch.
	RequestDigest *idempotency.Digest
	// Reason is normalized into the durable cancelled session and replay terminal metadata.
	Reason game.Identifier
	// CloseRoom distinguishes permanent host dissolution from an operational cancellation that returns to the lobby.
	CloseRoom bool
}

// PauseRoomCommand identifies one room-governed active session and the caller allowed to pause it.
type PauseRoomCommand struct {
	ActorUserID    uuid.UUID
	RoomID         uuid.UUID
	SessionID      uuid.UUID
	RequestID      uuid.UUID
	Expected       roomDomain.Version
	OwnershipEpoch uint64
}

// PauseRoom atomically records room governance and freezes the runtime session under the same timestamp fence.
func (service *Service) PauseRoom(ctx context.Context, command PauseRoomCommand) (roomDomain.Room, Session, error) {
	if service == nil || ctx == nil || command.ActorUserID == uuid.Nil || command.RoomID == uuid.Nil ||
		command.SessionID == uuid.Nil || command.Expected.Room == 0 || command.Expected.Membership == 0 ||
		command.OwnershipEpoch == 0 {
		return roomDomain.Room{}, Session{}, ErrInvalidSessionInput
	}
	room, before, err := service.loadRoomSession(ctx, command.RoomID, command.SessionID)
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	pauseID, err := service.generator.NewID()
	if err != nil {
		return roomDomain.Room{}, Session{}, ErrInvalidSessionInput
	}
	at := governedLifecycleTime(service.clock.Now(), room.Snapshot().UpdatedAt, before.Snapshot().UpdatedAt)
	afterRoom, err := room.Pause(
		command.ActorUserID, pauseID, command.SessionID, command.RequestID, command.Expected, at,
	)
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	after, err := before.Suspend(command.OwnershipEpoch, at)
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	event, err := service.newOutboxEvent(GameSessionSuspendedEventType, command.SessionID, after.Snapshot().State.StateVersion, at)
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	commit, err := NewLifecycleCommit(before, after, []outbox.Event{event})
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	return service.roomSessions.Suspend(ctx, room, afterRoom, commit)
}

// ResumeRoomCommand identifies one room-governed suspended session and the caller allowed to reactivate it.
type ResumeRoomCommand struct {
	ActorUserID    uuid.UUID
	RoomID         uuid.UUID
	SessionID      uuid.UUID
	Expected       roomDomain.Version
	OwnershipEpoch uint64
}

// ResumeRoom re-enables a room-governed suspended session only after the exact retained runtime module resolves again.
func (service *Service) ResumeRoom(ctx context.Context, command ResumeRoomCommand) (roomDomain.Room, Session, error) {
	if service == nil || ctx == nil || command.ActorUserID == uuid.Nil || command.RoomID == uuid.Nil ||
		command.SessionID == uuid.Nil || command.Expected.Room == 0 || command.Expected.Membership == 0 ||
		command.OwnershipEpoch == 0 {
		return roomDomain.Room{}, Session{}, ErrInvalidSessionInput
	}
	room, before, err := service.loadRoomSession(ctx, command.RoomID, command.SessionID)
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	module, err := service.registry.Resolve(before.Snapshot().VersionKey)
	if err != nil {
		return roomDomain.Room{}, Session{}, ErrModuleUnavailable
	}
	if runtimeModule, ok := module.(game.RuntimeServerGameModule); !ok || runtimeModule.Manifest().Key() != before.Snapshot().VersionKey {
		return roomDomain.Room{}, Session{}, ErrModuleUnavailable
	}
	at := governedLifecycleTime(service.clock.Now(), room.Snapshot().UpdatedAt, before.Snapshot().UpdatedAt)
	afterRoom, err := room.Resume(command.ActorUserID, command.SessionID, command.Expected, at)
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	after, err := service.resumeWithModule(before, command.OwnershipEpoch, at, module)
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	event, err := service.newOutboxEvent(GameSessionResumedEventType, command.SessionID, after.Snapshot().State.StateVersion, at)
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	commit, err := NewLifecycleCommit(before, after, []outbox.Event{event})
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	return service.roomSessions.Resume(ctx, room, afterRoom, commit)
}

// SuspendCommand keeps the legacy session-only lifecycle surface used by defensive runtime recovery.
type SuspendCommand struct {
	SessionID      uuid.UUID
	OwnershipEpoch uint64
}

// Suspend freezes module transitions and persisted timers without requiring room-governed pause metadata.
func (service *Service) Suspend(ctx context.Context, command SuspendCommand) (Session, error) {
	if service == nil || ctx == nil || command.SessionID == uuid.Nil || command.OwnershipEpoch == 0 {
		return Session{}, ErrInvalidSessionInput
	}
	before, err := service.sessions.Get(ctx, command.SessionID)
	if err != nil {
		return Session{}, err
	}
	return service.commitSuspend(ctx, before, command.OwnershipEpoch)
}

// ResumeCommand keeps the legacy session-only lifecycle surface used after fail-closed module suspension.
type ResumeCommand struct {
	SessionID      uuid.UUID
	OwnershipEpoch uint64
}

// Resume re-enables a suspended session only after its exact complete runtime module resolves again.
func (service *Service) Resume(ctx context.Context, command ResumeCommand) (Session, error) {
	if service == nil || ctx == nil || command.SessionID == uuid.Nil || command.OwnershipEpoch == 0 {
		return Session{}, ErrInvalidSessionInput
	}
	before, err := service.sessions.Get(ctx, command.SessionID)
	if err != nil {
		return Session{}, err
	}
	module, err := service.registry.Resolve(before.Snapshot().VersionKey)
	if err != nil {
		return Session{}, ErrModuleUnavailable
	}
	if runtimeModule, ok := module.(game.RuntimeServerGameModule); !ok || runtimeModule.Manifest().Key() != before.Snapshot().VersionKey {
		return Session{}, ErrModuleUnavailable
	}
	at := service.clock.Now().Round(0).UTC()
	if !at.After(before.Snapshot().UpdatedAt) {
		at = before.Snapshot().UpdatedAt.Add(time.Microsecond)
	}
	after, err := service.resumeWithModule(before, command.OwnershipEpoch, at, module)
	if err != nil {
		return Session{}, err
	}
	event, err := service.newOutboxEvent(GameSessionResumedEventType, command.SessionID, after.Snapshot().State.StateVersion, at)
	if err != nil {
		return Session{}, err
	}
	commit, err := NewLifecycleCommit(before, after, []outbox.Event{event})
	if err != nil {
		return Session{}, err
	}
	return service.sessions.CommitLifecycle(ctx, commit)
}

// Cancel terminates without a module result and clears the room pointer atomically.
func (service *Service) Cancel(ctx context.Context, command CancelCommand) (roomDomain.Room, Session, error) {
	if service == nil || ctx == nil || command.RoomID == uuid.Nil || command.SessionID == uuid.Nil || command.OwnershipEpoch == 0 {
		return roomDomain.Room{}, Session{}, ErrInvalidSessionInput
	}
	requestDigest := cancelDigest(command)
	if command.RequestDigest != nil && *command.RequestDigest != requestDigest {
		return roomDomain.Room{}, Session{}, idempotency.ErrConflict
	}
	room, err := service.rooms.GetByID(ctx, command.RoomID)
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	if room.Version() != command.ExpectedRoom || room.Snapshot().ActiveSessionID != command.SessionID {
		cancelled, getErr := service.sessions.Get(ctx, command.SessionID)
		if getErr == nil && cancelled.Snapshot().RoomID == command.RoomID &&
			cancelled.Snapshot().OwnershipEpoch == command.OwnershipEpoch &&
			cancelled.Snapshot().Status == StatusCancelled && cancelledRoomMatchesCommand(room, command) {
			return room, cancelled, nil
		}
		if getErr != nil && !errors.Is(getErr, ErrSessionNotFound) {
			return roomDomain.Room{}, Session{}, getErr
		}
		return roomDomain.Room{}, Session{}, roomDomain.ErrRoomVersionConflict
	}
	before, err := service.sessions.Get(ctx, command.SessionID)
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	at := service.clock.Now().Round(0).UTC()
	after, err := before.Cancel(command.OwnershipEpoch, at, command.Reason)
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	var nextRoom roomDomain.Room
	if command.CloseRoom {
		nextRoom, err = room.CancelSessionAndClose(room.Snapshot().HostUserID, command.SessionID, room.Version(), at)
	} else {
		nextRoom, err = room.CancelSession(command.SessionID, room.Version(), at)
	}
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	event, err := service.newOutboxEvent(GameSessionCancelledEventType, command.SessionID, after.Snapshot().State.StateVersion, at)
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	commit, err := NewLifecycleCommit(before, after, []outbox.Event{event})
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	return service.roomSessions.Cancel(ctx, room, nextRoom, commit)
}

// cancelledRoomMatchesCommand prevents one cancellation mode or a later room update from satisfying another retry.
func cancelledRoomMatchesCommand(room roomDomain.Room, command CancelCommand) bool {
	snapshot := room.Snapshot()
	if snapshot.RoomVersion != command.ExpectedRoom.Room+1 || snapshot.MembershipVersion != command.ExpectedRoom.Membership ||
		snapshot.ActiveSessionID != uuid.Nil || snapshot.ActiveGameID != "" ||
		snapshot.LastFinishedSessionID != uuid.Nil || snapshot.LastFinishedGameID != "" {
		return false
	}
	if command.CloseRoom {
		return snapshot.Status == roomDomain.RoomStatusClosed && snapshot.ParticipantAdmission == roomDomain.AdmissionClosed &&
			snapshot.SpectatorAdmission == roomDomain.AdmissionClosed
	}
	return snapshot.Status == roomDomain.RoomStatusLobby && snapshot.ParticipantAdmission == roomDomain.AdmissionClosed
}

// Project returns a current viewer-safe snapshot from the exact retained module.
func (service *Service) Project(ctx context.Context, sessionID uuid.UUID, viewer game.Viewer) (game.Projection, error) {
	_, projection, err := service.ProjectCurrent(ctx, sessionID, viewer)
	return projection, err
}

// ProjectCurrent returns the exact session snapshot used to produce the viewer projection and response cursor.
func (service *Service) ProjectCurrent(ctx context.Context, sessionID uuid.UUID, viewer game.Viewer) (Session, game.Projection, error) {
	if service == nil || ctx == nil || sessionID == uuid.Nil || !viewer.Valid() {
		return Session{}, game.Projection{}, ErrInvalidSessionInput
	}
	if viewer.Kind == game.ViewerReplay {
		return Session{}, game.Projection{}, ErrReplayUnavailable
	}
	session, err := service.sessions.Get(ctx, sessionID)
	if err != nil {
		return Session{}, game.Projection{}, err
	}
	module, err := service.registry.Resolve(session.Snapshot().VersionKey)
	if err != nil {
		return Session{}, game.Projection{}, err
	}
	projection, err := module.Project(session.Snapshot().State, viewer)
	if err != nil {
		return Session{}, game.Projection{}, err
	}
	if !projection.Valid() {
		return Session{}, game.Projection{}, ErrProjectionUnsafe
	}
	return session, projection, nil
}

// ProjectEvents returns a viewer delta, falling back to a current viewer snapshot when safe delta projection is unavailable.
func (service *Service) ProjectEvents(
	ctx context.Context,
	sessionID uuid.UUID,
	afterStateVersion uint64,
	viewer game.Viewer,
) (game.EventProjection, game.Projection, bool, error) {
	_, delta, projection, fallback, err := service.ProjectEventsCurrent(ctx, sessionID, afterStateVersion, viewer)
	return delta, projection, fallback, err
}

// ProjectEventsCurrent pairs a complete viewer update with the exact current session cursor.
func (service *Service) ProjectEventsCurrent(
	ctx context.Context,
	sessionID uuid.UUID,
	afterStateVersion uint64,
	viewer game.Viewer,
) (Session, game.EventProjection, game.Projection, bool, error) {
	if service == nil || ctx == nil || sessionID == uuid.Nil || !viewer.Valid() {
		return Session{}, game.EventProjection{}, game.Projection{}, false, ErrInvalidSessionInput
	}
	if viewer.Kind == game.ViewerReplay {
		return Session{}, game.EventProjection{}, game.Projection{}, false, ErrReplayUnavailable
	}
	session, err := service.sessions.Get(ctx, sessionID)
	if err != nil {
		return Session{}, game.EventProjection{}, game.Projection{}, false, err
	}
	currentStateVersion := session.Snapshot().State.StateVersion
	if afterStateVersion > currentStateVersion {
		return Session{}, game.EventProjection{}, game.Projection{}, false, ErrStateVersionConflict
	}
	if afterStateVersion == currentStateVersion {
		return session, game.EventProjection{}, game.Projection{}, false, nil
	}
	module, err := service.registry.Resolve(session.Snapshot().VersionKey)
	if err != nil {
		return Session{}, game.EventProjection{}, game.Projection{}, false, err
	}
	batches, err := service.sessions.ReadEventBatches(ctx, sessionID, afterStateVersion, 256)
	if err == nil {
		complete := len(batches) > 0
		expectedVersion := afterStateVersion + 1
		for _, batch := range batches {
			if batch.Snapshot().StateVersion != expectedVersion {
				complete = false
				break
			}
			expectedVersion++
		}
		complete = complete && expectedVersion-1 == currentStateVersion
		if projector, ok := module.(game.EventProjectingGameModule); ok && complete {
			events := make([]game.VersionedEvent, 0)
			for _, batch := range batches {
				batchSnapshot := batch.Snapshot()
				for _, event := range batchSnapshot.Events {
					events = append(events, game.VersionedEvent{StateVersion: batchSnapshot.StateVersion, Event: event})
				}
			}
			delta, projectErr := projector.ProjectEvents(session.Snapshot().State, events, viewer)
			if projectErr == nil && delta.Valid() {
				return session, delta, game.Projection{}, false, nil
			}
		}
	}
	projection, projectErr := module.Project(session.Snapshot().State, viewer)
	if projectErr != nil {
		return Session{}, game.EventProjection{}, game.Projection{}, false, projectErr
	}
	if !projection.Valid() {
		return Session{}, game.EventProjection{}, game.Projection{}, false, ErrProjectionUnsafe
	}
	return session, game.EventProjection{}, projection, true, nil
}

const (
	// maximumReplayBatches bounds the amount of durable history one replay projection may materialize.
	maximumReplayBatches uint32 = 4096
	// maximumReplayReadPage stays within every Store implementation's bounded event-read contract.
	maximumReplayReadPage uint32 = 256
	// maximumReplayEvents and maximumReplayPayloadBytes cap aggregate module input even when batches are small.
	maximumReplayEvents       = 65536
	maximumReplayPayloadBytes = 16 << 20
)

// ProjectReplay reads only a bounded terminal history and delegates all field disclosure to the retained module.
// Resource authorization is represented by the already-authorized viewer and policy; raw batches never leave this method.
func (service *Service) ProjectReplay(
	ctx context.Context,
	sessionID uuid.UUID,
	viewer game.Viewer,
	policy game.ReplayAccessPolicy,
) (game.Projection, error) {
	_, projection, err := service.ProjectReplayCurrent(ctx, sessionID, viewer, policy)
	return projection, err
}

// ProjectReplayCurrent pairs the terminal session version with the replay-safe projection it produced.
func (service *Service) ProjectReplayCurrent(
	ctx context.Context,
	sessionID uuid.UUID,
	viewer game.Viewer,
	policy game.ReplayAccessPolicy,
) (Session, game.Projection, error) {
	if service == nil || ctx == nil || sessionID == uuid.Nil || !viewer.Valid() || viewer.Kind != game.ViewerReplay || !policy.Valid() {
		return Session{}, game.Projection{}, ErrInvalidSessionInput
	}
	session, err := service.sessions.Get(ctx, sessionID)
	if err != nil {
		return Session{}, game.Projection{}, err
	}
	snapshot := session.Snapshot()
	if !snapshot.Status.Terminal() {
		return Session{}, game.Projection{}, ErrReplayUnavailable
	}
	module, err := service.registry.Resolve(snapshot.VersionKey)
	if err != nil {
		return Session{}, game.Projection{}, err
	}
	manifest := module.Manifest()
	if manifest.Key() != snapshot.VersionKey {
		return Session{}, game.Projection{}, ErrModuleUnavailable
	}
	if !manifest.Capabilities.Replay {
		return Session{}, game.Projection{}, ErrReplayUnavailable
	}
	batches, err := service.readReplayBatches(ctx, sessionID)
	if err != nil {
		return Session{}, game.Projection{}, err
	}
	if len(batches) == 0 || uint32(len(batches)) > maximumReplayBatches {
		return Session{}, game.Projection{}, ErrReplayUnavailable
	}
	events := make([]game.Event, 0)
	var payloadBytes int
	var previousVersion uint64
	for _, batch := range batches {
		batchSnapshot := batch.Snapshot()
		if batchSnapshot.SessionID != sessionID || batchSnapshot.StateVersion != previousVersion+1 {
			return Session{}, game.Projection{}, ErrReplayUnavailable
		}
		previousVersion = batchSnapshot.StateVersion
		for _, event := range batchSnapshot.Events {
			if len(events) >= maximumReplayEvents {
				return Session{}, game.Projection{}, ErrReplayUnavailable
			}
			if len(event.Message.Payload) > maximumReplayPayloadBytes-payloadBytes {
				return Session{}, game.Projection{}, ErrReplayUnavailable
			}
			payloadBytes += len(event.Message.Payload)
			events = append(events, event)
		}
	}
	if previousVersion != snapshot.State.StateVersion {
		return Session{}, game.Projection{}, ErrReplayUnavailable
	}
	var projection game.Projection
	if snapshot.Status == StatusFinished {
		projection, err = module.ProjectReplay(events, viewer, policy)
		if err != nil {
			return Session{}, game.Projection{}, err
		}
	} else {
		projector, ok := module.(game.ReplayProjectingV2GameModule)
		if !ok {
			return Session{}, game.Projection{}, ErrReplayUnavailable
		}
		projection, err = projector.ProjectReplayV2(game.ReplayRequest{
			Events: events, Viewer: viewer, Policy: policy, TerminalMeta: replayTerminalMeta(snapshot),
		})
		if err != nil {
			return Session{}, game.Projection{}, err
		}
	}
	if !projection.Valid() {
		return Session{}, game.Projection{}, ErrProjectionUnsafe
	}
	return session, projection, nil
}

func replayTerminalMeta(snapshot SessionSnapshot) game.ReplayTerminalMeta {
	return game.ReplayTerminalMeta{
		Finished: snapshot.Status == StatusFinished, Cancelled: snapshot.Status == StatusCancelled,
		EndedAt: snapshot.EndedAt, CancelReason: snapshot.CancelReason,
	}
}

func (service *Service) readReplayBatches(ctx context.Context, sessionID uuid.UUID) ([]EventBatch, error) {
	batches := make([]EventBatch, 0)
	var afterStateVersion uint64
	for uint32(len(batches)) <= maximumReplayBatches {
		remaining := maximumReplayBatches + 1 - uint32(len(batches))
		pageLimit := min(maximumReplayReadPage, remaining)
		page, err := service.sessions.ReadEventBatches(ctx, sessionID, afterStateVersion, pageLimit)
		if err != nil {
			return nil, err
		}
		if uint32(len(page)) > pageLimit {
			return nil, ErrReplayUnavailable
		}
		if len(page) == 0 {
			break
		}
		nextAfter := page[len(page)-1].Snapshot().StateVersion
		if nextAfter <= afterStateVersion {
			return nil, ErrReplayUnavailable
		}
		batches = append(batches, page...)
		afterStateVersion = nextAfter
		if uint32(len(page)) < pageLimit {
			break
		}
	}
	return batches, nil
}

func (service *Service) defaultRuntimeModule(ctx context.Context, gameID game.GameID) (game.Manifest, game.RuntimeServerGameModule, error) {
	manifest, err := service.registry.DefaultManifest(ctx, gameID)
	if err != nil {
		return game.Manifest{}, nil, err
	}
	module, err := service.registry.DefaultModule(ctx, gameID)
	if err != nil {
		return game.Manifest{}, nil, err
	}
	runtimeModule, ok := module.(game.RuntimeServerGameModule)
	if !ok || manifest.GameID != gameID || module.Manifest().Key() != manifest.Key() {
		return game.Manifest{}, nil, ErrModuleUnavailable
	}
	return manifest, runtimeModule, nil
}

func (service *Service) prepareRoomFinish(
	ctx context.Context,
	roomID uuid.UUID,
	sessionID uuid.UUID,
	at time.Time,
) (roomDomain.Room, roomDomain.Room, error) {
	room, err := service.rooms.GetByID(ctx, roomID)
	if err != nil {
		return roomDomain.Room{}, roomDomain.Room{}, err
	}
	next, err := room.FinishSession(sessionID, room.Version(), at)
	if err != nil {
		return roomDomain.Room{}, roomDomain.Room{}, err
	}
	return room, next, nil
}

func (service *Service) suspendMissingModule(ctx context.Context, before Session, ownershipEpoch uint64) error {
	if _, err := service.commitSuspend(ctx, before, ownershipEpoch); err != nil {
		return err
	}
	return ErrModuleUnavailable
}

// commitSuspend is shared by host-driven pause and defensive module-unavailable suspension.
func (service *Service) commitSuspend(ctx context.Context, before Session, ownershipEpoch uint64) (Session, error) {
	at := service.clock.Now().Round(0).UTC()
	if !at.After(before.Snapshot().UpdatedAt) {
		at = before.Snapshot().UpdatedAt.Add(time.Microsecond)
	}
	after, err := before.Suspend(ownershipEpoch, at)
	if err != nil {
		return Session{}, err
	}
	event, err := service.newOutboxEvent(GameSessionSuspendedEventType, before.Snapshot().ID, before.Snapshot().State.StateVersion, at)
	if err != nil {
		return Session{}, err
	}
	commit, err := NewLifecycleCommit(before, after, []outbox.Event{event})
	if err != nil {
		return Session{}, err
	}
	return service.sessions.CommitLifecycle(ctx, commit)
}

func (service *Service) resumeWithModule(
	before Session,
	ownershipEpoch uint64,
	at time.Time,
	module game.ServerGameModule,
) (Session, error) {
	adjuster, ok := module.(game.ResumeAdjustingGameModule)
	if !ok {
		return before.Resume(ownershipEpoch, at)
	}
	return before.resumeWithAdjustment(ownershipEpoch, at, adjuster.AdjustResumed)
}

// loadRoomSession reads the authoritative room and session pair and fail-closes if they no longer point at each other.
func (service *Service) loadRoomSession(ctx context.Context, roomID, sessionID uuid.UUID) (roomDomain.Room, Session, error) {
	room, err := service.rooms.GetByID(ctx, roomID)
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	session, err := service.sessions.Get(ctx, sessionID)
	if err != nil {
		return roomDomain.Room{}, Session{}, err
	}
	if session.Snapshot().RoomID != roomID {
		return roomDomain.Room{}, Session{}, ErrGameSessionIntegrity
	}
	return room, session, nil
}

// governedLifecycleTime keeps room and runtime lifecycle rows on one strictly increasing shared timestamp.
func governedLifecycleTime(now, roomUpdatedAt, sessionUpdatedAt time.Time) time.Time {
	at := canonicalRuntimeTime(now)
	latest := roomUpdatedAt
	if sessionUpdatedAt.After(latest) {
		latest = sessionUpdatedAt
	}
	if !at.After(latest) {
		at = latest.Add(time.Microsecond)
	}
	return at
}

func (service *Service) newOutboxEvent(eventType outbox.EventType, sessionID uuid.UUID, stateVersion uint64, at time.Time) (outbox.Event, error) {
	eventID, err := service.generator.NewID()
	if err != nil {
		return outbox.Event{}, ErrInvalidSessionInput
	}
	payload, err := MarshalSessionNotification(SessionNotification{SessionID: sessionID, StateVersion: stateVersion})
	if err != nil {
		return outbox.Event{}, ErrInvalidSessionInput
	}
	return outbox.NewEvent(eventID, eventType, GameSessionAggregateType, sessionID, payload, at, at)
}

func mapFrozenParticipants(values []roomDomain.FrozenParticipant) ([]game.Participant, []Participant) {
	sdkParticipants := make([]game.Participant, len(values))
	runtimeParticipants := make([]Participant, len(values))
	for index, participant := range values {
		sdkParticipants[index] = game.Participant{UserID: game.Identifier(participant.UserID.String()), SeatIndex: participant.SeatIndex}
		runtimeParticipants[index] = Participant{UserID: participant.UserID, SeatIndex: participant.SeatIndex}
	}
	return sdkParticipants, runtimeParticipants
}

func trustedStartingSeat(hostUserID uuid.UUID, participants []roomDomain.FrozenParticipant) (uint32, bool) {
	var minimum uint32
	found := false
	for _, participant := range participants {
		if participant.UserID == hostUserID {
			return participant.SeatIndex, true
		}
		if !found || participant.SeatIndex < minimum {
			minimum, found = participant.SeatIndex, true
		}
	}
	return minimum, found
}

func optionalUserIDString(userID uuid.UUID) string {
	if userID == uuid.Nil {
		return ""
	}
	return userID.String()
}

func projectPlayer(module game.ServerGameModule, session Session, userID uuid.UUID) (game.Projection, error) {
	for _, participant := range session.Snapshot().Participants {
		if participant.UserID == userID {
			projection, err := module.Project(session.Snapshot().State, game.Viewer{
				Kind: game.ViewerPlayer, UserID: game.Identifier(userID.String()), SeatIndex: participant.SeatIndex,
			})
			if err != nil {
				return game.Projection{}, err
			}
			if !projection.Valid() {
				return game.Projection{}, ErrProjectionUnsafe
			}
			return projection, nil
		}
	}
	return game.Projection{}, ErrParticipantNotActive
}

func actionDigest(command ActionCommand) idempotency.Digest {
	hasher := sha256.New()
	writeDigestField(hasher, command.SessionID[:])
	writeDigestField(hasher, command.ActorUserID[:])
	writeDigestField(hasher, []byte(command.ActionID))
	writeDigestUint64(hasher, command.ExpectedStateVersion)
	writeVersionKey(hasher, command.VersionKey)
	writeMessage(hasher, command.Command)
	return digestFromHash(hasher)
}

func startDigest(command StartCommand) idempotency.Digest {
	hasher := sha256.New()
	writeDigestField(hasher, command.ActorUserID[:])
	writeDigestField(hasher, command.RoomID[:])
	writeDigestField(hasher, []byte(command.OperationID.Value()))
	writeDigestField(hasher, []byte(command.GameID))
	writeDigestUint64(hasher, command.Expected.Room)
	writeDigestUint64(hasher, command.Expected.Membership)
	writeDigestUint64(hasher, command.ConfigRevision)
	writeMessage(hasher, command.Config)
	return digestFromHash(hasher)
}

func systemDigest(command SystemCommand) idempotency.Digest {
	hasher := sha256.New()
	writeDigestField(hasher, command.SessionID[:])
	writeDigestField(hasher, []byte(command.OperationID.Value()))
	writeDigestField(hasher, []byte(command.Source.Kind))
	writeDigestField(hasher, command.Source.EventID[:])
	writeDigestField(hasher, command.Source.RequestedByUserID[:])
	writeVersionKey(hasher, command.VersionKey)
	if command.Source.Kind == SystemSourceHostAPI {
		// HostAPI requests are optimistic user commands; binding the expected version prevents
		// an old operation ID from being reused against a later state after a conflict.
		writeDigestUint64(hasher, command.ExpectedStateVersion)
	}
	writeMessage(hasher, command.Message)
	return digestFromHash(hasher)
}

// cancelDigest is the runtime-side authority for validating owner-coordinated cancel bindings.
func cancelDigest(command CancelCommand) idempotency.Digest {
	hasher := sha256.New()
	writeDigestField(hasher, command.RoomID[:])
	writeDigestField(hasher, command.SessionID[:])
	writeDigestUint64(hasher, command.ExpectedRoom.Room)
	writeDigestUint64(hasher, command.ExpectedRoom.Membership)
	writeDigestUint64(hasher, command.OwnershipEpoch)
	if command.OperationID.Valid() {
		writeDigestField(hasher, []byte(command.OperationID.Value()))
	} else {
		writeDigestField(hasher, nil)
	}
	if command.CloseRoom {
		writeDigestField(hasher, []byte{1})
	} else {
		writeDigestField(hasher, []byte{0})
	}
	writeDigestField(hasher, []byte(command.Reason))
	return digestFromHash(hasher)
}

func transitionResultDigest(sessionID uuid.UUID, stateVersion uint64, code ResultCode) idempotency.Digest {
	hasher := sha256.New()
	writeDigestField(hasher, sessionID[:])
	writeDigestUint64(hasher, stateVersion)
	writeDigestField(hasher, []byte(code))
	return digestFromHash(hasher)
}

func writeVersionKey(hasher hash.Hash, key game.VersionKey) {
	writeDigestField(hasher, []byte(key.GameID))
	writeDigestField(hasher, []byte(key.Engine))
	writeDigestField(hasher, []byte(key.Protocol))
	writeDigestField(hasher, []byte(key.Client))
}

func writeMessage(hasher hash.Hash, message game.Message) {
	writeDigestField(hasher, []byte(message.MessageType))
	writeDigestUint64(hasher, uint64(message.SchemaVersion))
	writeDigestField(hasher, message.Payload)
}

func writeDigestUint64(hasher hash.Hash, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	writeDigestField(hasher, encoded[:])
}

func writeDigestField(hasher hash.Hash, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = hasher.Write(length[:])
	_, _ = hasher.Write(value)
}

func digestFromHash(hasher hash.Hash) idempotency.Digest {
	digest, err := idempotency.NewDigest(hasher.Sum(nil))
	if err != nil {
		panic(err)
	}
	return digest
}

var _ Generator = SecureGenerator{}
