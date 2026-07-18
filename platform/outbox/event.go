package outbox

import (
	"bytes"
	"time"

	"github.com/google/uuid"
)

const (
	// MaximumPayloadBytes bounds transaction size while leaving enough room for signed checkpoint envelopes.
	MaximumPayloadBytes = 1 << 20
)

// EventSnapshot is the persistence-neutral representation of one immutable durable event.
type EventSnapshot struct {
	Sequence      Sequence
	ID            uuid.UUID
	Type          EventType
	AggregateType AggregateType
	AggregateID   uuid.UUID
	Payload       []byte
	CreatedAt     time.Time
	AvailableAt   time.Time
}

// Event is immutable after construction so retries always deliver identical bytes and metadata.
type Event struct {
	snapshot EventSnapshot
}

// NewEvent creates an unpersisted durable event; the repository assigns its monotonic sequence.
func NewEvent(
	id uuid.UUID,
	eventType EventType,
	aggregateType AggregateType,
	aggregateID uuid.UUID,
	payload []byte,
	createdAt time.Time,
	availableAt time.Time,
) (Event, error) {
	return restoreEvent(EventSnapshot{
		ID: id, Type: eventType, AggregateType: aggregateType, AggregateID: aggregateID,
		Payload: payload, CreatedAt: createdAt, AvailableAt: availableAt,
	}, false)
}

// RestoreEvent validates a persisted event and requires its database-assigned sequence.
func RestoreEvent(snapshot EventSnapshot) (Event, error) {
	return restoreEvent(snapshot, true)
}

// Snapshot returns a deep copy suitable for an adapter without granting mutable access to payload bytes.
func (event Event) Snapshot() EventSnapshot {
	return cloneEventSnapshot(event.snapshot)
}

func restoreEvent(snapshot EventSnapshot, persisted bool) (Event, error) {
	snapshot = cloneEventSnapshot(snapshot)
	snapshot.CreatedAt = canonicalTime(snapshot.CreatedAt)
	snapshot.AvailableAt = canonicalTime(snapshot.AvailableAt)
	if !snapshot.Sequence.Valid() || (persisted && snapshot.Sequence == 0) || (!persisted && snapshot.Sequence != 0) ||
		snapshot.ID == uuid.Nil || !snapshot.Type.Valid() || !snapshot.AggregateType.Valid() ||
		snapshot.AggregateID == uuid.Nil || len(snapshot.Payload) == 0 || len(snapshot.Payload) > MaximumPayloadBytes ||
		snapshot.CreatedAt.IsZero() || snapshot.AvailableAt.Before(snapshot.CreatedAt) {
		return Event{}, ErrInvalidInput
	}
	return Event{snapshot: snapshot}, nil
}

func cloneEventSnapshot(snapshot EventSnapshot) EventSnapshot {
	snapshot.Payload = bytes.Clone(snapshot.Payload)
	return snapshot
}
