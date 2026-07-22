package gameruntime

import (
	"bytes"
	"reflect"
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
	// GameSessionSuspendedEventType records a runtime pause without manufacturing a game-owned transition.
	GameSessionSuspendedEventType outbox.EventType = "game.session.suspended.v1"
	// GameSessionResumedEventType records that the exact retained module became executable again.
	GameSessionResumedEventType outbox.EventType = "game.session.resumed.v1"
	// GameSessionCancelledEventType records an administrative terminal state without a normal game result.
	GameSessionCancelledEventType outbox.EventType = "game.session.cancelled.v1"
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

// SystemSource identifies the durable platform fact that caused one system transition.
type SystemSource struct {
	Kind              game.Identifier
	EventID           uuid.UUID
	RequestedByUserID uuid.UUID
}

const (
	SystemSourceHostAPI    game.Identifier = "host_api"
	SystemSourceRoomOutbox game.Identifier = "room_outbox"
	SystemSourcePlatform   game.Identifier = "platform"
)

// Valid requires a typed, globally unique source event suitable for durable inbox deduplication.
func (source SystemSource) Valid() bool {
	kindValid := source.Kind == SystemSourceHostAPI || source.Kind == SystemSourceRoomOutbox || source.Kind == SystemSourcePlatform
	requesterValid := source.Kind == SystemSourceHostAPI && source.RequestedByUserID != uuid.Nil ||
		source.Kind != SystemSourceHostAPI && source.RequestedByUserID == uuid.Nil
	return kindValid && requesterValid && source.EventID != uuid.Nil
}

func (source SystemSource) zero() bool {
	return source.Kind == "" && source.EventID == uuid.Nil && source.RequestedByUserID == uuid.Nil
}

// EventBatchSnapshot preserves ordered engine events and every non-state deterministic input used to produce them.
type EventBatchSnapshot struct {
	ID                uuid.UUID
	SessionID         uuid.UUID
	StateVersion      uint64
	OwnershipEpoch    uint64
	Cause             EventCause
	ActorUserID       uuid.UUID
	ActionID          idempotency.OperationID
	TimerID           game.Identifier
	SystemOperationID idempotency.OperationID
	SystemSource      SystemSource
	RequestDigest     idempotency.Digest
	Execution         game.DeterministicContext
	Input             game.Message
	Events            []game.Event
	CommittedAt       time.Time
}

// EventBatch is immutable so persistence and replay observe the exact engine event order.
type EventBatch struct {
	snapshot EventBatchSnapshot
}

// RestoreEventBatch validates a persisted batch and deep-copies all payload and execution slices.
func RestoreEventBatch(snapshot EventBatchSnapshot) (EventBatch, error) {
	snapshot.CommittedAt = canonicalRuntimeTime(snapshot.CommittedAt)
	// PostgreSQL may decode timestamptz values in the connection's local zone; replay state always uses canonical UTC.
	snapshot.Execution = snapshot.Execution.Clone()
	snapshot.Execution.Now = canonicalRuntimeTime(snapshot.Execution.Now)
	zeroDigest := idempotency.Digest{}
	if snapshot.ID == uuid.Nil || snapshot.SessionID == uuid.Nil || snapshot.StateVersion == 0 || !snapshot.Cause.Valid() ||
		!snapshot.Execution.Valid() || !snapshot.Execution.Now.Equal(snapshot.CommittedAt) || !snapshot.Input.Valid() ||
		len(snapshot.Events) == 0 || len(snapshot.Events) > game.MaximumTransitionEvents {
		return EventBatch{}, ErrInvalidSessionInput
	}
	switch snapshot.Cause {
	case EventCauseCreated:
		if snapshot.OwnershipEpoch != 0 || snapshot.ActorUserID != uuid.Nil || snapshot.ActionID.Valid() ||
			snapshot.TimerID != "" || snapshot.SystemOperationID.Valid() || !snapshot.SystemSource.zero() ||
			snapshot.RequestDigest != zeroDigest {
			return EventBatch{}, ErrInvalidSessionInput
		}
	case EventCauseAction:
		if snapshot.OwnershipEpoch == 0 || snapshot.ActorUserID == uuid.Nil || !snapshot.ActionID.Valid() ||
			snapshot.TimerID != "" || snapshot.SystemOperationID.Valid() || !snapshot.SystemSource.zero() ||
			snapshot.RequestDigest != zeroDigest {
			return EventBatch{}, ErrInvalidSessionInput
		}
	case EventCauseTimer:
		if _, err := game.ParseIdentifier(string(snapshot.TimerID)); snapshot.OwnershipEpoch == 0 ||
			snapshot.ActorUserID != uuid.Nil || snapshot.ActionID.Valid() || err != nil ||
			snapshot.SystemOperationID.Valid() || !snapshot.SystemSource.zero() || snapshot.RequestDigest != zeroDigest {
			return EventBatch{}, ErrInvalidSessionInput
		}
	case EventCauseSystem:
		if snapshot.OwnershipEpoch == 0 || snapshot.ActorUserID != uuid.Nil || snapshot.ActionID.Valid() ||
			snapshot.TimerID != "" || !snapshot.SystemOperationID.Valid() || !snapshot.SystemSource.Valid() {
			return EventBatch{}, ErrInvalidSessionInput
		}
	}
	for _, event := range snapshot.Events {
		if !event.Valid() {
			return EventBatch{}, ErrInvalidSessionInput
		}
	}
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
	ResultCodeAccepted     ResultCode = "accepted"
	ResultCodeNoopTerminal ResultCode = "noop_terminal"
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

// TimerKey scopes a persisted timer to the state version that created it.
type TimerKey struct {
	SessionID            uuid.UUID
	TimerID              game.Identifier
	ExpectedStateVersion uint64
}

// Valid reports whether a timer firing can be uniquely deduplicated after retries or ownership changes.
func (key TimerKey) Valid() bool {
	_, err := game.ParseIdentifier(string(key.TimerID))
	return key.SessionID != uuid.Nil && err == nil && key.ExpectedStateVersion > 0
}

// TimerReceiptSnapshot is the durable result for one exact persisted timer firing.
type TimerReceiptSnapshot struct {
	Key          TimerKey
	ResultCode   ResultCode
	ResultDigest idempotency.Digest
	StateVersion uint64
	CommittedAt  time.Time
}

// TimerReceipt is immutable and carries no participant or viewer-specific payload.
type TimerReceipt struct {
	snapshot TimerReceiptSnapshot
}

// NewTimerReceipt validates a timer result before it participates in an atomic commit.
func NewTimerReceipt(snapshot TimerReceiptSnapshot) (TimerReceipt, error) {
	snapshot.CommittedAt = canonicalRuntimeTime(snapshot.CommittedAt)
	if !snapshot.Key.Valid() || !snapshot.ResultCode.Valid() || snapshot.StateVersion == 0 || snapshot.CommittedAt.IsZero() {
		return TimerReceipt{}, ErrInvalidSessionInput
	}
	return TimerReceipt{snapshot: snapshot}, nil
}

// Snapshot returns the immutable timer receipt persistence representation.
func (receipt TimerReceipt) Snapshot() TimerReceiptSnapshot {
	return receipt.snapshot
}

// SystemKey scopes an external system operation to its durable source fact and target session.
type SystemKey struct {
	SessionID   uuid.UUID
	OperationID idempotency.OperationID
	Source      SystemSource
}

// Valid reports whether the complete system idempotency identity can be persisted safely.
func (key SystemKey) Valid() bool {
	return key.SessionID != uuid.Nil && key.OperationID.Valid() && key.Source.Valid()
}

// SystemReceiptSnapshot preserves the original result for one operation/source/digest binding.
type SystemReceiptSnapshot struct {
	Key           SystemKey
	RequestDigest idempotency.Digest
	ResultCode    ResultCode
	ResultDigest  idempotency.Digest
	StateVersion  uint64
	CommittedAt   time.Time
}

// SystemReceipt is immutable so retries cannot rewrite the original system result or committed version.
type SystemReceipt struct {
	snapshot SystemReceiptSnapshot
}

// NewSystemReceipt validates a durable system result ready for an atomic commit.
func NewSystemReceipt(snapshot SystemReceiptSnapshot) (SystemReceipt, error) {
	snapshot.CommittedAt = canonicalRuntimeTime(snapshot.CommittedAt)
	if !snapshot.Key.Valid() || !snapshot.ResultCode.Valid() || snapshot.StateVersion == 0 || snapshot.CommittedAt.IsZero() {
		return SystemReceipt{}, ErrInvalidSessionInput
	}
	return SystemReceipt{snapshot: snapshot}, nil
}

// Snapshot returns the immutable system receipt persistence representation.
func (receipt SystemReceipt) Snapshot() SystemReceiptSnapshot {
	return receipt.snapshot
}

// Replay returns the original result only for the request digest bound to this operation and source.
func (receipt SystemReceipt) Replay(requestDigest idempotency.Digest) (SystemReceipt, error) {
	if receipt.snapshot.RequestDigest != requestDigest {
		return SystemReceipt{}, idempotency.ErrConflict
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

// TimerCommit keeps a due timer's state, events, replacement timer set, receipt, and outbox atomically aligned.
type TimerCommit struct {
	before       Session
	after        Session
	batch        EventBatch
	receipt      TimerReceipt
	outboxEvents []outbox.Event
}

// SystemCommit keeps a system command's state transition and durable idempotency receipt inseparable.
type SystemCommit struct {
	before       Session
	after        Session
	batch        EventBatch
	receipt      SystemReceipt
	outboxEvents []outbox.Event
}

// LifecycleCommit changes only runtime-owned status, timestamps, and timers; it never creates an engine batch.
type LifecycleCommit struct {
	before       Session
	after        Session
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

// NewTimerCommit rejects a timer batch or receipt that does not describe the exact scheduled transition.
func NewTimerCommit(
	before Session,
	after Session,
	batch EventBatch,
	receipt TimerReceipt,
	outboxEvents []outbox.Event,
) (TimerCommit, error) {
	beforeSnapshot, afterSnapshot := before.Snapshot(), after.Snapshot()
	batchSnapshot, receiptSnapshot := batch.Snapshot(), receipt.Snapshot()
	if !validRuntimeTransition(beforeSnapshot, afterSnapshot, batchSnapshot, EventCauseTimer) ||
		receiptSnapshot.Key.SessionID != afterSnapshot.ID || receiptSnapshot.Key.TimerID != batchSnapshot.TimerID ||
		receiptSnapshot.Key.ExpectedStateVersion != beforeSnapshot.State.StateVersion ||
		receiptSnapshot.StateVersion != afterSnapshot.State.StateVersion ||
		!receiptSnapshot.CommittedAt.Equal(batchSnapshot.CommittedAt) ||
		!sessionSnapshotContainsTimer(beforeSnapshot, receiptSnapshot.Key) ||
		!validTransitionOutbox(afterSnapshot, batchSnapshot, outboxEvents) {
		return TimerCommit{}, ErrInvalidTimerCommit
	}
	return TimerCommit{
		before: before, after: after, batch: batch, receipt: receipt,
		outboxEvents: append([]outbox.Event(nil), outboxEvents...),
	}, nil
}

// Valid reports whether the value was constructed as one coherent timer transaction.
func (commit TimerCommit) Valid() bool {
	_, err := NewTimerCommit(commit.before, commit.after, commit.batch, commit.receipt, commit.outboxEvents)
	return err == nil
}

// Before returns the session version expected by the timer CAS.
func (commit TimerCommit) Before() Session { return commit.before }

// After returns the authoritative session produced by the timer.
func (commit TimerCommit) After() Session { return commit.after }

// Batch returns the timer-caused engine event batch.
func (commit TimerCommit) Batch() EventBatch { return commit.batch }

// Receipt returns the timer's durable deduplication result.
func (commit TimerCommit) Receipt() TimerReceipt { return commit.receipt }

// OutboxEvents returns an independent copy of timer transition notifications.
func (commit TimerCommit) OutboxEvents() []outbox.Event {
	return append([]outbox.Event(nil), commit.outboxEvents...)
}

// NewSystemCommit enforces operation/source/digest receipt identity across one system transition.
func NewSystemCommit(
	before Session,
	after Session,
	batch EventBatch,
	receipt SystemReceipt,
	outboxEvents []outbox.Event,
) (SystemCommit, error) {
	beforeSnapshot, afterSnapshot := before.Snapshot(), after.Snapshot()
	batchSnapshot, receiptSnapshot := batch.Snapshot(), receipt.Snapshot()
	if !validRuntimeTransition(beforeSnapshot, afterSnapshot, batchSnapshot, EventCauseSystem) ||
		receiptSnapshot.Key.SessionID != afterSnapshot.ID ||
		receiptSnapshot.Key.OperationID.Value() != batchSnapshot.SystemOperationID.Value() ||
		receiptSnapshot.Key.Source != batchSnapshot.SystemSource ||
		receiptSnapshot.RequestDigest != batchSnapshot.RequestDigest ||
		receiptSnapshot.StateVersion != afterSnapshot.State.StateVersion ||
		!receiptSnapshot.CommittedAt.Equal(batchSnapshot.CommittedAt) ||
		!validTransitionOutbox(afterSnapshot, batchSnapshot, outboxEvents) {
		return SystemCommit{}, ErrInvalidSystemCommit
	}
	return SystemCommit{
		before: before, after: after, batch: batch, receipt: receipt,
		outboxEvents: append([]outbox.Event(nil), outboxEvents...),
	}, nil
}

// Valid reports whether the value was constructed as one coherent system transaction.
func (commit SystemCommit) Valid() bool {
	_, err := NewSystemCommit(commit.before, commit.after, commit.batch, commit.receipt, commit.outboxEvents)
	return err == nil
}

// Before returns the session version expected by the system CAS.
func (commit SystemCommit) Before() Session { return commit.before }

// After returns the authoritative session produced by the system command.
func (commit SystemCommit) After() Session { return commit.after }

// Batch returns the system-caused engine event batch.
func (commit SystemCommit) Batch() EventBatch { return commit.batch }

// Receipt returns the operation/source/digest-bound system result.
func (commit SystemCommit) Receipt() SystemReceipt { return commit.receipt }

// OutboxEvents returns an independent copy of system transition notifications.
func (commit SystemCommit) OutboxEvents() []outbox.Event {
	return append([]outbox.Event(nil), commit.outboxEvents...)
}

// NewLifecycleCommit accepts only suspend, resume, or cancel transitions with unchanged game state and ownership.
func NewLifecycleCommit(before, after Session, outboxEvents []outbox.Event) (LifecycleCommit, error) {
	beforeSnapshot, afterSnapshot := before.Snapshot(), after.Snapshot()
	expectedEventType := GameSessionSuspendedEventType
	validStatus := beforeSnapshot.Status == StatusActive && afterSnapshot.Status == StatusSuspended &&
		reflect.DeepEqual(beforeSnapshot.Timers, afterSnapshot.Timers) &&
		beforeSnapshot.NextDeadlineAt.Equal(afterSnapshot.NextDeadlineAt) && afterSnapshot.EndedAt.IsZero()
	if beforeSnapshot.Status == StatusSuspended && afterSnapshot.Status == StatusActive {
		expectedEventType = GameSessionResumedEventType
		validStatus = reflect.DeepEqual(beforeSnapshot.Timers, afterSnapshot.Timers) &&
			beforeSnapshot.NextDeadlineAt.Equal(afterSnapshot.NextDeadlineAt) && afterSnapshot.EndedAt.IsZero()
	}
	if afterSnapshot.Status == StatusCancelled {
		expectedEventType = GameSessionCancelledEventType
		validStatus = (beforeSnapshot.Status == StatusActive || beforeSnapshot.Status == StatusSuspended) &&
			len(afterSnapshot.Timers) == 0 && afterSnapshot.NextDeadlineAt.IsZero() &&
			!afterSnapshot.EndedAt.IsZero() && afterSnapshot.EndedAt.Equal(afterSnapshot.UpdatedAt)
	}
	if !validStatus || !sameSessionIdentity(beforeSnapshot, afterSnapshot) ||
		beforeSnapshot.OwnershipEpoch == 0 || beforeSnapshot.OwnershipEpoch != afterSnapshot.OwnershipEpoch ||
		!sameSnapshotState(beforeSnapshot.State, afterSnapshot.State) ||
		!afterSnapshot.UpdatedAt.After(beforeSnapshot.UpdatedAt) || len(outboxEvents) == 0 {
		return LifecycleCommit{}, ErrInvalidLifecycleCommit
	}
	for _, event := range outboxEvents {
		snapshot := event.Snapshot()
		if snapshot.Sequence != 0 || snapshot.Type != expectedEventType ||
			snapshot.AggregateType != GameSessionAggregateType || snapshot.AggregateID != afterSnapshot.ID ||
			!snapshot.CreatedAt.Equal(afterSnapshot.UpdatedAt) {
			return LifecycleCommit{}, ErrInvalidLifecycleCommit
		}
	}
	return LifecycleCommit{before: before, after: after, outboxEvents: append([]outbox.Event(nil), outboxEvents...)}, nil
}

// Valid reports whether the lifecycle value still satisfies its constructor invariants.
func (commit LifecycleCommit) Valid() bool {
	_, err := NewLifecycleCommit(commit.before, commit.after, commit.outboxEvents)
	return err == nil
}

// Before returns the exact session version expected by the lifecycle CAS.
func (commit LifecycleCommit) Before() Session { return commit.before }

// After returns the suspended, resumed, or cancelled runtime state.
func (commit LifecycleCommit) After() Session { return commit.after }

// OutboxEvents returns independent runtime lifecycle notifications.
func (commit LifecycleCommit) OutboxEvents() []outbox.Event {
	return append([]outbox.Event(nil), commit.outboxEvents...)
}

func validRuntimeTransition(before, after SessionSnapshot, batch EventBatchSnapshot, cause EventCause) bool {
	return sameSessionIdentity(before, after) && before.Status == StatusActive &&
		(after.Status == StatusActive || after.Status == StatusFinished) && before.OwnershipEpoch > 0 &&
		after.OwnershipEpoch == before.OwnershipEpoch && before.State.StateVersion != ^uint64(0) &&
		after.State.StateVersion == before.State.StateVersion+1 && batch.SessionID == after.ID &&
		batch.StateVersion == after.State.StateVersion && batch.OwnershipEpoch == after.OwnershipEpoch &&
		batch.Cause == cause && batch.ActorUserID == uuid.Nil && !batch.ActionID.Valid() &&
		after.UpdatedAt.Equal(batch.CommittedAt) && !after.UpdatedAt.Before(before.UpdatedAt)
}

func validTransitionOutbox(after SessionSnapshot, batch EventBatchSnapshot, events []outbox.Event) bool {
	if len(events) == 0 {
		return false
	}
	for _, event := range events {
		snapshot := event.Snapshot()
		if snapshot.Sequence != 0 || snapshot.Type != GameSessionTransitionedEventType ||
			snapshot.AggregateType != GameSessionAggregateType || snapshot.AggregateID != after.ID ||
			!snapshot.CreatedAt.Equal(batch.CommittedAt) {
			return false
		}
	}
	return true
}

func sessionSnapshotContainsTimer(snapshot SessionSnapshot, key TimerKey) bool {
	for _, timer := range snapshot.Timers {
		if timer.TimerID == key.TimerID && timer.ExpectedStateVersion == key.ExpectedStateVersion {
			return true
		}
	}
	return false
}

func sameSnapshotState(left, right game.Snapshot) bool {
	return left.SnapshotVersion == right.SnapshotVersion && left.StateVersion == right.StateVersion &&
		left.State.MessageType == right.State.MessageType && left.State.SchemaVersion == right.State.SchemaVersion &&
		bytes.Equal(left.State.Payload, right.State.Payload)
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
