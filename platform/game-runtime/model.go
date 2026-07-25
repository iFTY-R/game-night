package gameruntime

import (
	"bytes"
	"crypto/sha256"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/idempotency"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

// Status is the authoritative lifecycle of one game rather than its persistent PartyRoom.
type Status string

const (
	StatusActive    Status = "active"
	StatusSuspended Status = "suspended"
	StatusFinished  Status = "finished"
	StatusCancelled Status = "cancelled"
)

const (
	// CancelReasonPlatformCancelled is the normalized runtime-owned reason for ordinary platform/session cancellation.
	CancelReasonPlatformCancelled game.Identifier = "platform_cancelled"
	// CancelReasonLegacyCancelled preserves pre-v2 cancelled rows that had no persisted reason before migration 26.
	CancelReasonLegacyCancelled game.Identifier = "legacy_cancelled"
)

// Valid reports whether the status has defined recovery and terminal semantics.
func (status Status) Valid() bool {
	return status == StatusActive || status == StatusSuspended || status == StatusFinished || status == StatusCancelled
}

// Terminal reports whether state, timers, and ownership can no longer change.
func (status Status) Terminal() bool {
	return status == StatusFinished || status == StatusCancelled
}

// Participant is the immutable identity and stable PartyRoom seat frozen at game start.
type Participant struct {
	UserID    uuid.UUID
	SeatIndex uint32
}

// TimerSnapshot is one complete next-state timer bound to the state version that scheduled it.
type TimerSnapshot struct {
	TimerID              game.Identifier
	ExpectedStateVersion uint64
	DueAt                time.Time
	Message              game.Message
}

// FrozenStartConfig keeps the exact config envelope and the room fences that authorized one durable start.
type FrozenStartConfig struct {
	Config             game.Message
	ConfigDigest       idempotency.Digest
	ConfigRevision     uint64
	RoomVersion        uint64
	MembershipVersion  uint64
	RoomOwnershipEpoch uint64
}

// Valid reports whether the frozen config is complete enough to restore a new-style start or an old config-only start.
func (start FrozenStartConfig) Valid() bool {
	if !start.Config.Valid() || start.ConfigDigest == (idempotency.Digest{}) {
		return false
	}
	if start.RoomVersion == 0 && start.MembershipVersion == 0 && start.RoomOwnershipEpoch == 0 {
		return start.ConfigRevision == 0
	}
	return start.RoomVersion > 0 && start.MembershipVersion > 0 && start.RoomOwnershipEpoch > 0
}

// SessionSnapshot is the persistence-neutral authoritative state of one exact game release.
type SessionSnapshot struct {
	ID             uuid.UUID
	RoomID         uuid.UUID
	VersionKey     game.VersionKey
	OwnershipEpoch uint64
	Participants   []Participant
	Start          FrozenStartConfig
	State          game.Snapshot
	Timers         []TimerSnapshot
	NextDeadlineAt time.Time
	Status         Status
	StartedAt      time.Time
	UpdatedAt      time.Time
	// SuspendedAt anchors the remaining timer duration while a suspended session is not schedulable.
	SuspendedAt  time.Time
	EndedAt      time.Time
	CancelReason game.Identifier
}

// Session is immutable; every accepted command returns a new snapshot for one CAS commit.
type Session struct {
	snapshot SessionSnapshot
}

// CreateRequest binds the module's deterministic initial transition to its room, versions, and frozen seats.
type CreateRequest struct {
	SessionID    uuid.UUID
	RoomID       uuid.UUID
	VersionKey   game.VersionKey
	Participants []Participant
	Start        FrozenStartConfig
	BatchID      uuid.UUID
	Execution    game.DeterministicContext
	Input        game.Message
	Transition   game.Transition
}

// ActionTransitionRequest contains every durable input required to replay one pure engine action.
type ActionTransitionRequest struct {
	BatchID        uuid.UUID
	OwnershipEpoch uint64
	ActorUserID    uuid.UUID
	ActionID       idempotency.OperationID
	Execution      game.DeterministicContext
	Input          game.Message
	Transition     game.Transition
}

// TimerTransitionRequest binds one persisted timer firing to its exact scheduled state version and payload.
type TimerTransitionRequest struct {
	BatchID              uuid.UUID
	OwnershipEpoch       uint64
	TimerID              game.Identifier
	ExpectedStateVersion uint64
	Execution            game.DeterministicContext
	Input                game.Message
	Transition           game.Transition
}

// SystemTransitionRequest binds one external system fact to its durable idempotency identity.
type SystemTransitionRequest struct {
	BatchID              uuid.UUID
	OwnershipEpoch       uint64
	ExpectedStateVersion uint64
	SystemOperationID    idempotency.OperationID
	Source               SystemSource
	RequestDigest        idempotency.Digest
	Execution            game.DeterministicContext
	Input                game.Message
	Transition           game.Transition
}

// NewSession creates an initially unowned session and its state-version-one event batch.
func NewSession(request CreateRequest) (Session, EventBatch, error) {
	if request.SessionID == uuid.Nil || request.RoomID == uuid.Nil || request.BatchID == uuid.Nil || !request.VersionKey.Valid() ||
		!request.Execution.Valid() || !request.Input.Valid() || len(request.Participants) == 0 ||
		len(request.Participants) > int(game.MaximumParticipants) {
		return Session{}, EventBatch{}, ErrInvalidSessionInput
	}
	if err := request.Transition.Validate(0, request.Execution.Now); err != nil || request.Transition.Snapshot.StateVersion != 1 ||
		request.Transition.Finished && len(request.Transition.Timers) != 0 {
		return Session{}, EventBatch{}, ErrInvalidSessionInput
	}
	participants, err := canonicalParticipants(request.Participants)
	if err != nil {
		return Session{}, EventBatch{}, err
	}
	timers, deadline, err := timersFromIntents(request.Transition.Timers, 1)
	if err != nil {
		return Session{}, EventBatch{}, err
	}
	status, endedAt := StatusActive, time.Time{}
	if request.Transition.Finished {
		status, endedAt = StatusFinished, request.Execution.Now
	}
	start, err := canonicalFrozenStartConfig(request.Start, request.Input, request.VersionKey)
	if err != nil {
		return Session{}, EventBatch{}, err
	}
	session, err := RestoreSession(SessionSnapshot{
		ID: request.SessionID, RoomID: request.RoomID, VersionKey: request.VersionKey,
		Participants: participants, Start: start, State: request.Transition.Snapshot, Timers: timers,
		NextDeadlineAt: deadline, Status: status, StartedAt: request.Execution.Now,
		UpdatedAt: request.Execution.Now, EndedAt: endedAt,
	})
	if err != nil {
		return Session{}, EventBatch{}, err
	}
	batch, err := RestoreEventBatch(EventBatchSnapshot{
		ID: request.BatchID, SessionID: request.SessionID, StateVersion: 1,
		Cause: EventCauseCreated, Execution: request.Execution,
		Input: request.Input, Events: request.Transition.Events, CommittedAt: request.Execution.Now,
	})
	if err != nil {
		return Session{}, EventBatch{}, err
	}
	return session, batch, nil
}

// RestoreSession validates persisted rows and takes ownership of no caller-provided mutable bytes or slices.
func RestoreSession(snapshot SessionSnapshot) (Session, error) {
	snapshot.StartedAt = canonicalRuntimeTime(snapshot.StartedAt)
	snapshot.UpdatedAt = canonicalRuntimeTime(snapshot.UpdatedAt)
	snapshot.SuspendedAt = canonicalRuntimeTime(snapshot.SuspendedAt)
	snapshot.EndedAt = canonicalRuntimeTime(snapshot.EndedAt)
	snapshot.NextDeadlineAt = canonicalRuntimeTime(snapshot.NextDeadlineAt)
	var err error
	snapshot.CancelReason, err = canonicalCancelReason(snapshot.Status, snapshot.CancelReason)
	if err != nil {
		return Session{}, ErrInvalidSessionInput
	}
	if !frozenStartConfigZero(snapshot.Start) {
		start, err := canonicalFrozenStartConfig(snapshot.Start, snapshot.Start.Config, snapshot.VersionKey)
		if err != nil {
			return Session{}, ErrInvalidSessionInput
		}
		snapshot.Start = start
	}
	if snapshot.ID == uuid.Nil || snapshot.RoomID == uuid.Nil || !snapshot.VersionKey.Valid() || !snapshot.State.Valid() ||
		!snapshot.Status.Valid() || snapshot.StartedAt.IsZero() || snapshot.UpdatedAt.Before(snapshot.StartedAt) ||
		len(snapshot.Participants) == 0 || len(snapshot.Participants) > int(game.MaximumParticipants) ||
		len(snapshot.Timers) > game.MaximumTransitionTimers {
		return Session{}, ErrInvalidSessionInput
	}
	participants, err := canonicalParticipants(snapshot.Participants)
	if err != nil {
		return Session{}, err
	}
	timers, deadline, err := canonicalTimers(snapshot.Timers, snapshot.State.StateVersion)
	if err != nil || !deadline.Equal(snapshot.NextDeadlineAt) {
		return Session{}, ErrInvalidSessionInput
	}
	if snapshot.Status.Terminal() {
		if snapshot.EndedAt.IsZero() || !snapshot.EndedAt.Equal(snapshot.UpdatedAt) || len(timers) != 0 || !deadline.IsZero() {
			return Session{}, ErrInvalidSessionInput
		}
	} else if !snapshot.EndedAt.IsZero() || snapshot.CancelReason != "" {
		return Session{}, ErrInvalidSessionInput
	}
	if snapshot.Status == StatusSuspended {
		if snapshot.SuspendedAt.IsZero() || snapshot.SuspendedAt.Before(snapshot.StartedAt) || snapshot.SuspendedAt.After(snapshot.UpdatedAt) {
			return Session{}, ErrInvalidSessionInput
		}
	} else if !snapshot.SuspendedAt.IsZero() {
		return Session{}, ErrInvalidSessionInput
	}
	snapshot.Participants = participants
	snapshot.State = cloneGameSnapshot(snapshot.State)
	snapshot.Timers = timers
	return Session{snapshot: snapshot}, nil
}

// Snapshot returns a defensive copy suitable for persistence, module invocation, or projection.
func (session Session) Snapshot() SessionSnapshot {
	return cloneSessionSnapshot(session.snapshot)
}

// AcquireOwnership advances the PostgreSQL fencing token without changing the game state version.
func (session Session) AcquireOwnership(expectedEpoch uint64, at time.Time) (Session, error) {
	at = canonicalRuntimeTime(at)
	if session.snapshot.Status.Terminal() {
		return Session{}, ErrSessionTerminal
	}
	if expectedEpoch != session.snapshot.OwnershipEpoch {
		return Session{}, ErrOwnershipLost
	}
	if expectedEpoch == math.MaxUint64 || at.IsZero() || !at.After(session.snapshot.UpdatedAt) {
		return Session{}, ErrInvalidSessionInput
	}
	next := session.Snapshot()
	next.OwnershipEpoch++
	next.UpdatedAt = at
	return RestoreSession(next)
}

// ApplyAction accepts one exact N-to-N+1 engine result under the current non-zero ownership epoch.
func (session Session) ApplyAction(request ActionTransitionRequest) (Session, EventBatch, error) {
	if session.snapshot.Status.Terminal() {
		return Session{}, EventBatch{}, ErrSessionTerminal
	}
	if session.snapshot.Status != StatusActive {
		return Session{}, EventBatch{}, ErrSessionSuspended
	}
	if request.OwnershipEpoch == 0 || request.OwnershipEpoch != session.snapshot.OwnershipEpoch {
		return Session{}, EventBatch{}, ErrOwnershipLost
	}
	currentVersion := session.snapshot.State.StateVersion
	if currentVersion == math.MaxUint64 || request.Transition.Snapshot.StateVersion != currentVersion+1 {
		return Session{}, EventBatch{}, ErrStateVersionConflict
	}
	if request.BatchID == uuid.Nil || request.ActorUserID == uuid.Nil || !request.ActionID.Valid() ||
		!request.Execution.Valid() || request.Execution.Now.Before(session.snapshot.UpdatedAt) || !request.Input.Valid() ||
		!session.hasParticipant(request.ActorUserID) {
		return Session{}, EventBatch{}, ErrInvalidSessionInput
	}
	if err := request.Transition.Validate(currentVersion, request.Execution.Now); err != nil ||
		request.Transition.Finished && len(request.Transition.Timers) != 0 {
		return Session{}, EventBatch{}, ErrInvalidSessionInput
	}
	timers, deadline, err := timersFromIntents(request.Transition.Timers, currentVersion+1)
	if err != nil {
		return Session{}, EventBatch{}, err
	}
	nextSnapshot := session.Snapshot()
	nextSnapshot.State = request.Transition.Snapshot
	nextSnapshot.Timers = timers
	nextSnapshot.NextDeadlineAt = deadline
	nextSnapshot.UpdatedAt = request.Execution.Now
	if request.Transition.Finished {
		nextSnapshot.Status = StatusFinished
		nextSnapshot.EndedAt = request.Execution.Now
	}
	next, err := RestoreSession(nextSnapshot)
	if err != nil {
		return Session{}, EventBatch{}, err
	}
	batch, err := RestoreEventBatch(EventBatchSnapshot{
		ID: request.BatchID, SessionID: session.snapshot.ID, StateVersion: currentVersion + 1,
		OwnershipEpoch: request.OwnershipEpoch, Cause: EventCauseAction,
		ActorUserID: request.ActorUserID, ActionID: request.ActionID,
		Execution: request.Execution, Input: request.Input, Events: request.Transition.Events,
		CommittedAt: request.Execution.Now,
	})
	if err != nil {
		return Session{}, EventBatch{}, err
	}
	return next, batch, nil
}

// ApplyTimer accepts a due persisted timer exactly once for the state version that scheduled it.
func (session Session) ApplyTimer(request TimerTransitionRequest) (Session, EventBatch, error) {
	if session.snapshot.Status.Terminal() {
		return Session{}, EventBatch{}, ErrSessionTerminal
	}
	if session.snapshot.Status != StatusActive {
		return Session{}, EventBatch{}, ErrSessionSuspended
	}
	if request.OwnershipEpoch == 0 || request.OwnershipEpoch != session.snapshot.OwnershipEpoch {
		return Session{}, EventBatch{}, ErrOwnershipLost
	}
	currentVersion := session.snapshot.State.StateVersion
	if request.ExpectedStateVersion != currentVersion || currentVersion == math.MaxUint64 ||
		request.Transition.Snapshot.StateVersion != currentVersion+1 {
		return Session{}, EventBatch{}, ErrStateVersionConflict
	}
	if _, timerErr := game.ParseIdentifier(string(request.TimerID)); request.BatchID == uuid.Nil || timerErr != nil ||
		!request.Execution.Valid() || request.Execution.Now.Before(session.snapshot.UpdatedAt) || !request.Input.Valid() {
		return Session{}, EventBatch{}, ErrInvalidSessionInput
	}
	timer, found := session.timer(request.TimerID)
	if !found {
		return Session{}, EventBatch{}, ErrTimerNotFound
	}
	if timer.ExpectedStateVersion != request.ExpectedStateVersion || request.Execution.Now.Before(timer.DueAt) ||
		!sameGameMessage(timer.Message, request.Input) {
		return Session{}, EventBatch{}, ErrInvalidSessionInput
	}
	return session.applyTransition(
		request.BatchID,
		request.OwnershipEpoch,
		EventCauseTimer,
		request.Execution,
		request.Input,
		request.Transition,
		EventBatchSnapshot{TimerID: request.TimerID},
	)
}

// ApplySystem accepts one exact-version platform command without impersonating a participant action.
func (session Session) ApplySystem(request SystemTransitionRequest) (Session, EventBatch, error) {
	if session.snapshot.Status.Terminal() {
		return Session{}, EventBatch{}, ErrSessionTerminal
	}
	if session.snapshot.Status != StatusActive {
		return Session{}, EventBatch{}, ErrSessionSuspended
	}
	if request.OwnershipEpoch == 0 || request.OwnershipEpoch != session.snapshot.OwnershipEpoch {
		return Session{}, EventBatch{}, ErrOwnershipLost
	}
	currentVersion := session.snapshot.State.StateVersion
	if request.ExpectedStateVersion != currentVersion || currentVersion == math.MaxUint64 ||
		request.Transition.Snapshot.StateVersion != currentVersion+1 {
		return Session{}, EventBatch{}, ErrStateVersionConflict
	}
	if request.BatchID == uuid.Nil || !request.SystemOperationID.Valid() || !request.Source.Valid() ||
		!request.Execution.Valid() || request.Execution.Now.Before(session.snapshot.UpdatedAt) || !request.Input.Valid() {
		return Session{}, EventBatch{}, ErrInvalidSessionInput
	}
	return session.applyTransition(
		request.BatchID,
		request.OwnershipEpoch,
		EventCauseSystem,
		request.Execution,
		request.Input,
		request.Transition,
		EventBatchSnapshot{
			SystemOperationID: request.SystemOperationID,
			SystemSource:      request.Source,
			RequestDigest:     request.RequestDigest,
		},
	)
}

// Suspend pauses module-driven transitions while retaining exact state and timers for later recovery.
func (session Session) Suspend(expectedEpoch uint64, at time.Time) (Session, error) {
	if session.snapshot.Status.Terminal() {
		return Session{}, ErrSessionTerminal
	}
	if session.snapshot.Status != StatusActive {
		return Session{}, ErrSessionSuspended
	}
	if expectedEpoch == 0 || expectedEpoch != session.snapshot.OwnershipEpoch {
		return Session{}, ErrOwnershipLost
	}
	at = canonicalRuntimeTime(at)
	if at.IsZero() || !at.After(session.snapshot.UpdatedAt) {
		return Session{}, ErrInvalidSessionInput
	}
	next := session.Snapshot()
	next.Status = StatusSuspended
	next.UpdatedAt = at
	next.SuspendedAt = at
	return RestoreSession(next)
}

// Resume re-enables transitions and preserves every timer's remaining duration across the pause.
func (session Session) Resume(expectedEpoch uint64, at time.Time) (Session, error) {
	return session.resumeWithAdjustment(expectedEpoch, at, nil)
}

// resumeWithAdjustment reactivates a suspended session after shifting timers and
// optionally letting the module rewrite opaque deadline fields inside state and timer payloads.
func (session Session) resumeWithAdjustment(
	expectedEpoch uint64,
	at time.Time,
	adjust func(game.Snapshot, []game.TimerIntent) (game.Snapshot, []game.TimerIntent, error),
) (Session, error) {
	if session.snapshot.Status.Terminal() {
		return Session{}, ErrSessionTerminal
	}
	if session.snapshot.Status != StatusSuspended {
		return Session{}, ErrInvalidLifecycleCommit
	}
	if expectedEpoch == 0 || expectedEpoch != session.snapshot.OwnershipEpoch {
		return Session{}, ErrOwnershipLost
	}
	at = canonicalRuntimeTime(at)
	if at.IsZero() || !at.After(session.snapshot.UpdatedAt) {
		return Session{}, ErrInvalidSessionInput
	}
	pausedFor := at.Sub(session.snapshot.SuspendedAt)
	if session.snapshot.SuspendedAt.IsZero() || pausedFor <= 0 {
		return Session{}, ErrInvalidSessionInput
	}
	state := cloneGameSnapshot(session.snapshot.State)
	shiftedTimers, err := shiftedResumeTimers(session.snapshot.Timers, pausedFor)
	if err != nil {
		return Session{}, err
	}
	if adjust != nil {
		state, shiftedTimers, err = adjust(state, shiftedTimers)
		if err != nil {
			return Session{}, err
		}
	}
	if !resumeSnapshotEnvelopePreserved(session.snapshot.State, state) {
		return Session{}, ErrInvalidSessionInput
	}
	timers, deadline, err := timersFromIntents(shiftedTimers, session.snapshot.State.StateVersion)
	if err != nil {
		return Session{}, err
	}
	next := session.Snapshot()
	next.State = state
	next.Timers = timers
	next.NextDeadlineAt = deadline
	next.Status = StatusActive
	next.UpdatedAt = at
	next.SuspendedAt = time.Time{}
	return RestoreSession(next)
}

// Cancel terminates an active or suspended session without manufacturing an engine finish transition.
func (session Session) Cancel(expectedEpoch uint64, at time.Time, reason game.Identifier) (Session, error) {
	if session.snapshot.Status.Terminal() {
		return Session{}, ErrSessionTerminal
	}
	if expectedEpoch == 0 || expectedEpoch != session.snapshot.OwnershipEpoch {
		return Session{}, ErrOwnershipLost
	}
	at = canonicalRuntimeTime(at)
	if at.IsZero() || !at.After(session.snapshot.UpdatedAt) {
		return Session{}, ErrInvalidSessionInput
	}
	next := session.Snapshot()
	next.Status = StatusCancelled
	next.Timers = nil
	next.NextDeadlineAt = time.Time{}
	next.UpdatedAt = at
	next.SuspendedAt = time.Time{}
	next.EndedAt = at
	next.CancelReason = reason
	return RestoreSession(next)
}

// applyTransition centralizes the identical state, timer, and terminal invariants shared by timer and system inputs.
func (session Session) applyTransition(
	batchID uuid.UUID,
	ownershipEpoch uint64,
	cause EventCause,
	execution game.DeterministicContext,
	input game.Message,
	transition game.Transition,
	metadata EventBatchSnapshot,
) (Session, EventBatch, error) {
	currentVersion := session.snapshot.State.StateVersion
	if err := transition.Validate(currentVersion, execution.Now); err != nil || transition.Finished && len(transition.Timers) != 0 {
		return Session{}, EventBatch{}, ErrInvalidSessionInput
	}
	timers, deadline, err := timersFromIntents(transition.Timers, currentVersion+1)
	if err != nil {
		return Session{}, EventBatch{}, err
	}
	nextSnapshot := session.Snapshot()
	nextSnapshot.State = transition.Snapshot
	nextSnapshot.Timers = timers
	nextSnapshot.NextDeadlineAt = deadline
	nextSnapshot.UpdatedAt = execution.Now
	if transition.Finished {
		nextSnapshot.Status = StatusFinished
		nextSnapshot.EndedAt = execution.Now
	}
	next, err := RestoreSession(nextSnapshot)
	if err != nil {
		return Session{}, EventBatch{}, err
	}
	metadata.ID = batchID
	metadata.SessionID = session.snapshot.ID
	metadata.StateVersion = currentVersion + 1
	metadata.OwnershipEpoch = ownershipEpoch
	metadata.Cause = cause
	metadata.Execution = execution
	metadata.Input = input
	metadata.Events = transition.Events
	metadata.CommittedAt = execution.Now
	batch, err := RestoreEventBatch(metadata)
	if err != nil {
		return Session{}, EventBatch{}, err
	}
	return next, batch, nil
}

func (session Session) timer(timerID game.Identifier) (TimerSnapshot, bool) {
	for _, timer := range session.snapshot.Timers {
		if timer.TimerID == timerID {
			return timer, true
		}
	}
	return TimerSnapshot{}, false
}

func (session Session) hasParticipant(userID uuid.UUID) bool {
	for _, participant := range session.snapshot.Participants {
		if participant.UserID == userID {
			return true
		}
	}
	return false
}

func sameGameMessage(left, right game.Message) bool {
	return left.MessageType == right.MessageType && left.SchemaVersion == right.SchemaVersion && bytes.Equal(left.Payload, right.Payload)
}

func sameFrozenStartConfig(left, right FrozenStartConfig) bool {
	return sameGameMessage(left.Config, right.Config) && left.ConfigDigest == right.ConfigDigest &&
		left.ConfigRevision == right.ConfigRevision && left.RoomVersion == right.RoomVersion &&
		left.MembershipVersion == right.MembershipVersion && left.RoomOwnershipEpoch == right.RoomOwnershipEpoch
}

func canonicalParticipants(values []Participant) ([]Participant, error) {
	participants := append([]Participant(nil), values...)
	sort.Slice(participants, func(left, right int) bool {
		if participants[left].SeatIndex == participants[right].SeatIndex {
			return participants[left].UserID.String() < participants[right].UserID.String()
		}
		return participants[left].SeatIndex < participants[right].SeatIndex
	})
	users := make(map[uuid.UUID]struct{}, len(participants))
	seats := make(map[uint32]struct{}, len(participants))
	for _, participant := range participants {
		if participant.UserID == uuid.Nil {
			return nil, ErrInvalidSessionInput
		}
		if _, duplicate := users[participant.UserID]; duplicate {
			return nil, ErrInvalidSessionInput
		}
		if _, duplicate := seats[participant.SeatIndex]; duplicate {
			return nil, ErrInvalidSessionInput
		}
		users[participant.UserID] = struct{}{}
		seats[participant.SeatIndex] = struct{}{}
	}
	return participants, nil
}

func timersFromIntents(intents []game.TimerIntent, stateVersion uint64) ([]TimerSnapshot, time.Time, error) {
	timers := make([]TimerSnapshot, len(intents))
	for index, intent := range intents {
		timers[index] = TimerSnapshot{
			TimerID: intent.TimerID, ExpectedStateVersion: stateVersion,
			DueAt: intent.DueAt, Message: intent.Message,
		}
	}
	return canonicalTimers(timers, stateVersion)
}

func canonicalTimers(values []TimerSnapshot, stateVersion uint64) ([]TimerSnapshot, time.Time, error) {
	timers := make([]TimerSnapshot, len(values))
	seen := make(map[game.Identifier]struct{}, len(values))
	for index, timer := range values {
		timer.DueAt = canonicalRuntimeTime(timer.DueAt)
		timer.Message = timer.Message.Clone()
		if _, err := game.ParseIdentifier(string(timer.TimerID)); err != nil || timer.ExpectedStateVersion != stateVersion ||
			timer.DueAt.IsZero() || !timer.Message.Valid() {
			return nil, time.Time{}, ErrInvalidSessionInput
		}
		if _, duplicate := seen[timer.TimerID]; duplicate {
			return nil, time.Time{}, ErrInvalidSessionInput
		}
		seen[timer.TimerID] = struct{}{}
		timers[index] = timer
	}
	sort.Slice(timers, func(left, right int) bool { return timers[left].TimerID < timers[right].TimerID })
	deadline := time.Time{}
	for _, timer := range timers {
		if deadline.IsZero() || timer.DueAt.Before(deadline) {
			deadline = timer.DueAt
		}
	}
	return timers, deadline, nil
}

func cloneSessionSnapshot(snapshot SessionSnapshot) SessionSnapshot {
	snapshot.Participants = append([]Participant(nil), snapshot.Participants...)
	snapshot.Start = cloneFrozenStartConfig(snapshot.Start)
	snapshot.State = cloneGameSnapshot(snapshot.State)
	snapshot.Timers = cloneTimers(snapshot.Timers)
	return snapshot
}

func cloneFrozenStartConfig(start FrozenStartConfig) FrozenStartConfig {
	start.Config = start.Config.Clone()
	return start
}

func cloneGameSnapshot(snapshot game.Snapshot) game.Snapshot {
	snapshot.State = snapshot.State.Clone()
	return snapshot
}

func cloneTimers(values []TimerSnapshot) []TimerSnapshot {
	timers := make([]TimerSnapshot, len(values))
	for index, timer := range values {
		timer.Message = timer.Message.Clone()
		timers[index] = timer
	}
	return timers
}

func shiftedResumeTimers(values []TimerSnapshot, pausedFor time.Duration) ([]game.TimerIntent, error) {
	timers := make([]game.TimerIntent, len(values))
	for index, timer := range values {
		shifted := canonicalRuntimeTime(timer.DueAt.Add(pausedFor))
		if shifted.Before(timer.DueAt) {
			return nil, ErrInvalidSessionInput
		}
		timers[index] = game.TimerIntent{
			TimerID: timer.TimerID,
			DueAt:   shifted,
			Message: timer.Message.Clone(),
		}
	}
	return timers, nil
}

func resumeSnapshotEnvelopePreserved(before, after game.Snapshot) bool {
	return before.SnapshotVersion == after.SnapshotVersion &&
		before.StateVersion == after.StateVersion &&
		before.State.MessageType == after.State.MessageType &&
		before.State.SchemaVersion == after.State.SchemaVersion &&
		after.State.Valid()
}

func canonicalRuntimeTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	// PostgreSQL timestamptz and the shared outbox both retain microseconds.
	// Canonicalizing here keeps aggregate, receipt, batch, and outbox equality stable.
	return value.UTC().Truncate(time.Microsecond)
}

func canonicalCancelReason(status Status, reason game.Identifier) (game.Identifier, error) {
	if status != StatusCancelled {
		if reason != "" {
			return "", ErrInvalidSessionInput
		}
		return "", nil
	}
	if reason == "" {
		return CancelReasonPlatformCancelled, nil
	}
	parsed, err := game.ParseIdentifier(string(reason))
	if err != nil {
		return "", ErrInvalidSessionInput
	}
	return parsed, nil
}

func canonicalFrozenStartConfig(start FrozenStartConfig, config game.Message, versionKey game.VersionKey) (FrozenStartConfig, error) {
	config = config.Clone()
	if !config.Valid() || !versionKey.Valid() {
		return FrozenStartConfig{}, ErrInvalidSessionInput
	}
	if start.Config.Valid() && !sameGameMessage(start.Config, config) {
		return FrozenStartConfig{}, ErrInvalidSessionInput
	}
	start.Config = config
	if start.ConfigDigest == (idempotency.Digest{}) {
		start.ConfigDigest = startConfigDigest(versionKey, config)
	} else if start.ConfigDigest != startConfigDigest(versionKey, config) {
		return FrozenStartConfig{}, ErrInvalidSessionInput
	}
	if start.RoomVersion == 0 && start.MembershipVersion == 0 && start.RoomOwnershipEpoch == 0 {
		if start.ConfigRevision != 0 {
			return FrozenStartConfig{}, ErrInvalidSessionInput
		}
		return start, nil
	}
	if start.RoomVersion == 0 || start.MembershipVersion == 0 || start.RoomOwnershipEpoch == 0 {
		return FrozenStartConfig{}, ErrInvalidSessionInput
	}
	return start, nil
}

func frozenStartConfigZero(start FrozenStartConfig) bool {
	return !start.Config.Valid() && start.ConfigDigest == (idempotency.Digest{}) &&
		start.ConfigRevision == 0 && start.RoomVersion == 0 &&
		start.MembershipVersion == 0 && start.RoomOwnershipEpoch == 0
}

func startConfigDigest(versionKey game.VersionKey, message game.Message) idempotency.Digest {
	hasher := sha256.New()
	writeVersionKey(hasher, versionKey)
	writeMessage(hasher, message)
	return digestFromHash(hasher)
}
