package gameruntime

import (
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/outbox"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

const (
	// GameSessionCreatedEventType records the state-version-one creation batch without exposing private state.
	GameSessionCreatedEventType outbox.EventType = "game.session.created.v1"
	// GameSessionTransitionedEventType is the durable, secret-free notification emitted after an action commit.
	GameSessionTransitionedEventType outbox.EventType = "game.session.transitioned.v1"
	// GameSessionAggregateType identifies the GameSession aggregate in the shared durable outbox.
	GameSessionAggregateType outbox.AggregateType = "game.session"
)

// EventCause identifies which deterministic input produced a persisted engine batch.
type EventCause string

const (
	EventCauseCreated EventCause = "created"
	EventCauseAction  EventCause = "action"
	EventCauseTimer   EventCause = "timer"
	EventCauseSystem  EventCause = "system"
)

// Valid reports whether the cause has defined input and actor semantics.
func (cause EventCause) Valid() bool {
	return cause == EventCauseCreated || cause == EventCauseAction || cause == EventCauseTimer || cause == EventCauseSystem
}

// EventBatchSnapshot preserves ordered engine events and every non-state deterministic input used to produce them.
type EventBatchSnapshot struct {
	ID             uuid.UUID
	SessionID      uuid.UUID
	StateVersion   uint64
	OwnershipEpoch uint64
	Cause          EventCause
	ActorUserID    uuid.UUID
	ActionID       idempotency.OperationID
	Execution      game.DeterministicContext
	Input          game.Message
	Events         []game.Event
	CommittedAt    time.Time
}

// EventBatch is immutable so persistence and replay observe the exact engine event order.
type EventBatch struct {
	snapshot EventBatchSnapshot
}

// RestoreEventBatch validates a persisted batch and deep-copies all payload and execution slices.
func RestoreEventBatch(snapshot EventBatchSnapshot) (EventBatch, error) {
	snapshot.CommittedAt = canonicalRuntimeTime(snapshot.CommittedAt)
	if snapshot.ID == uuid.Nil || snapshot.SessionID == uuid.Nil || snapshot.StateVersion == 0 || !snapshot.Cause.Valid() ||
		!snapshot.Execution.Valid() || !snapshot.Execution.Now.Equal(snapshot.CommittedAt) || !snapshot.Input.Valid() ||
		len(snapshot.Events) == 0 || len(snapshot.Events) > game.MaximumTransitionEvents {
		return EventBatch{}, ErrInvalidSessionInput
	}
	switch snapshot.Cause {
	case EventCauseCreated:
		if snapshot.OwnershipEpoch != 0 || snapshot.ActorUserID != uuid.Nil || snapshot.ActionID.Valid() {
			return EventBatch{}, ErrInvalidSessionInput
		}
	case EventCauseAction:
		if snapshot.OwnershipEpoch == 0 || snapshot.ActorUserID == uuid.Nil || !snapshot.ActionID.Valid() {
			return EventBatch{}, ErrInvalidSessionInput
		}
	default:
		if snapshot.OwnershipEpoch == 0 || snapshot.ActorUserID != uuid.Nil || snapshot.ActionID.Valid() {
			return EventBatch{}, ErrInvalidSessionInput
		}
	}
	for _, event := range snapshot.Events {
		if !event.Valid() {
			return EventBatch{}, ErrInvalidSessionInput
		}
	}
	snapshot.Execution = snapshot.Execution.Clone()
	snapshot.Input = snapshot.Input.Clone()
	snapshot.Events = cloneGameEvents(snapshot.Events)
	return EventBatch{snapshot: snapshot}, nil
}

// Snapshot returns a deep copy for database mapping or deterministic replay.
func (batch EventBatch) Snapshot() EventBatchSnapshot {
	snapshot := batch.snapshot
	snapshot.Execution = snapshot.Execution.Clone()
	snapshot.Input = snapshot.Input.Clone()
	snapshot.Events = cloneGameEvents(snapshot.Events)
	return snapshot
}

// ActionKey scopes a caller-generated action ID to one authenticated actor and one session.
type ActionKey struct {
	SessionID   uuid.UUID
	ActorUserID uuid.UUID
	ActionID    idempotency.OperationID
}

// Valid reports whether the key can safely participate in the composite PostgreSQL uniqueness constraint.
func (key ActionKey) Valid() bool {
	return key.SessionID != uuid.Nil && key.ActorUserID != uuid.Nil && key.ActionID.Valid()
}

// ResultCode is a bounded stable machine result; it never carries raw engine or infrastructure diagnostics.
type ResultCode string

const (
	ResultCodeAccepted ResultCode = "accepted"
)

// Valid uses the SDK identifier grammar so codes remain safe in protocols, metrics, and indexes.
func (code ResultCode) Valid() bool {
	_, err := game.ParseIdentifier(string(code))
	return err == nil
}

// ActionReceiptSnapshot is the durable idempotent response for one scoped action key and request digest.
type ActionReceiptSnapshot struct {
	Key           ActionKey
	RequestDigest idempotency.Digest
	ResultCode    ResultCode
	ResultDigest  idempotency.Digest
	StateVersion  uint64
	CommittedAt   time.Time
}

// ActionReceipt is immutable and deliberately excludes viewer-specific or secret result payloads.
type ActionReceipt struct {
	snapshot ActionReceiptSnapshot
}

// NewActionReceipt validates a stable action result ready for the atomic commit.
func NewActionReceipt(snapshot ActionReceiptSnapshot) (ActionReceipt, error) {
	snapshot.CommittedAt = canonicalRuntimeTime(snapshot.CommittedAt)
	if !snapshot.Key.Valid() || !snapshot.ResultCode.Valid() || snapshot.StateVersion == 0 || snapshot.CommittedAt.IsZero() {
		return ActionReceipt{}, ErrInvalidSessionInput
	}
	return ActionReceipt{snapshot: snapshot}, nil
}

// Snapshot returns the persistence representation of an immutable receipt.
func (receipt ActionReceipt) Snapshot() ActionReceiptSnapshot {
	return receipt.snapshot
}

// Replay returns the original result only when the caller repeats the exact request digest.
func (receipt ActionReceipt) Replay(requestDigest idempotency.Digest) (ActionReceipt, error) {
	if receipt.snapshot.RequestDigest != requestDigest {
		return ActionReceipt{}, idempotency.ErrConflict
	}
	return receipt, nil
}

// ActionCommit is the only supported shape for committing state, timers, events, outbox, and a receipt together.
type ActionCommit struct {
	before       Session
	after        Session
	batch        EventBatch
	receipt      ActionReceipt
	outboxEvents []outbox.Event
}

// Valid reports whether the initial session, batch, and outbox notification describe one atomic creation.
func (commit CreationCommit) Valid() bool {
	session := commit.Session.Snapshot()
	batch := commit.Batch.Snapshot()
	if session.ID == uuid.Nil || session.State.StateVersion != 1 || session.OwnershipEpoch != 0 ||
		(session.Status != StatusActive && session.Status != StatusFinished) || batch.ID == uuid.Nil || batch.SessionID != session.ID ||
		batch.StateVersion != 1 || batch.Cause != EventCauseCreated || batch.OwnershipEpoch != 0 ||
		!batch.CommittedAt.Equal(session.StartedAt) || len(commit.OutboxEvents) == 0 {
		return false
	}
	for _, event := range commit.OutboxEvents {
		snapshot := event.Snapshot()
		if snapshot.Sequence != 0 || snapshot.Type != GameSessionCreatedEventType ||
			snapshot.AggregateType != GameSessionAggregateType || snapshot.AggregateID != session.ID ||
			!snapshot.CreatedAt.Equal(batch.CommittedAt) {
			return false
		}
	}
	return true
}

// NewActionCommit rejects cross-session or partially versioned data before a database transaction begins.
func NewActionCommit(
	before Session,
	after Session,
	batch EventBatch,
	receipt ActionReceipt,
	outboxEvents []outbox.Event,
) (ActionCommit, error) {
	beforeSnapshot, afterSnapshot := before.Snapshot(), after.Snapshot()
	batchSnapshot, receiptSnapshot := batch.Snapshot(), receipt.Snapshot()
	if !sameSessionIdentity(beforeSnapshot, afterSnapshot) || beforeSnapshot.Status != StatusActive ||
		(afterSnapshot.Status != StatusActive && afterSnapshot.Status != StatusFinished) ||
		beforeSnapshot.OwnershipEpoch == 0 || afterSnapshot.OwnershipEpoch != beforeSnapshot.OwnershipEpoch ||
		beforeSnapshot.State.StateVersion == ^uint64(0) || afterSnapshot.State.StateVersion != beforeSnapshot.State.StateVersion+1 ||
		batchSnapshot.SessionID != afterSnapshot.ID || batchSnapshot.StateVersion != afterSnapshot.State.StateVersion ||
		batchSnapshot.OwnershipEpoch != afterSnapshot.OwnershipEpoch || batchSnapshot.Cause != EventCauseAction ||
		!participantSnapshotContains(beforeSnapshot.Participants, batchSnapshot.ActorUserID) ||
		receiptSnapshot.Key.SessionID != afterSnapshot.ID || receiptSnapshot.Key.ActorUserID != batchSnapshot.ActorUserID ||
		receiptSnapshot.Key.ActionID.Value() != batchSnapshot.ActionID.Value() ||
		receiptSnapshot.StateVersion != afterSnapshot.State.StateVersion ||
		!receiptSnapshot.CommittedAt.Equal(batchSnapshot.CommittedAt) || !afterSnapshot.UpdatedAt.Equal(batchSnapshot.CommittedAt) ||
		afterSnapshot.UpdatedAt.Before(beforeSnapshot.UpdatedAt) ||
		len(outboxEvents) == 0 {
		return ActionCommit{}, ErrInvalidActionCommit
	}
	for _, event := range outboxEvents {
		snapshot := event.Snapshot()
		if snapshot.Sequence != 0 || snapshot.Type != GameSessionTransitionedEventType ||
			snapshot.AggregateType != GameSessionAggregateType || snapshot.AggregateID != afterSnapshot.ID ||
			!snapshot.CreatedAt.Equal(batchSnapshot.CommittedAt) {
			return ActionCommit{}, ErrInvalidActionCommit
		}
	}
	return ActionCommit{
		before: before, after: after, batch: batch, receipt: receipt,
		outboxEvents: append([]outbox.Event(nil), outboxEvents...),
	}, nil
}

// Valid reports whether the value was constructed as one coherent action transaction.
func (commit ActionCommit) Valid() bool {
	_, err := NewActionCommit(commit.before, commit.after, commit.batch, commit.receipt, commit.outboxEvents)
	return err == nil
}

// Before returns the exact state and ownership versions expected by the PostgreSQL CAS.
func (commit ActionCommit) Before() Session { return commit.before }

// After returns the authoritative snapshot that becomes visible only after the whole transaction commits.
func (commit ActionCommit) After() Session { return commit.after }

// Batch returns the ordered deterministic engine facts committed with the new state.
func (commit ActionCommit) Batch() EventBatch { return commit.batch }

// Receipt returns the stable idempotent response committed with the new state.
func (commit ActionCommit) Receipt() ActionReceipt { return commit.receipt }

// OutboxEvents returns an independent slice of immutable durable notification values.
func (commit ActionCommit) OutboxEvents() []outbox.Event {
	return append([]outbox.Event(nil), commit.outboxEvents...)
}

func sameSessionIdentity(left, right SessionSnapshot) bool {
	if left.ID != right.ID || left.RoomID != right.RoomID || left.VersionKey != right.VersionKey ||
		!left.StartedAt.Equal(right.StartedAt) || len(left.Participants) != len(right.Participants) {
		return false
	}
	for index := range left.Participants {
		if left.Participants[index] != right.Participants[index] {
			return false
		}
	}
	return true
}

func participantSnapshotContains(participants []Participant, userID uuid.UUID) bool {
	for _, participant := range participants {
		if participant.UserID == userID {
			return true
		}
	}
	return false
}

func cloneGameEvents(values []game.Event) []game.Event {
	events := make([]game.Event, len(values))
	for index, event := range values {
		event.Message = event.Message.Clone()
		events[index] = event
	}
	return events
}
