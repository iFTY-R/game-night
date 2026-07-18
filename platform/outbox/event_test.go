package outbox

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestEventSnapshotIsImmutable(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 18, 12, 0, 0, 123456789, time.FixedZone("test", 8*60*60))
	payload := []byte("signed checkpoint")
	event, err := NewEvent(
		uuid.New(), EventTypeAuditCheckpointPending, AggregateTypeAuditChain, uuid.New(), payload, now, now.Add(time.Second),
	)
	if err != nil {
		t.Fatalf("NewEvent() error = %v", err)
	}
	payload[0] = 'X'

	first := event.Snapshot()
	if first.Sequence != 0 {
		t.Fatalf("new event sequence = %d, want unpersisted zero", first.Sequence)
	}
	if !bytes.Equal(first.Payload, []byte("signed checkpoint")) {
		t.Fatalf("event payload = %q, constructor retained caller buffer", first.Payload)
	}
	if first.CreatedAt.Location() != time.UTC || first.CreatedAt.Nanosecond()%1000 != 0 {
		t.Fatalf("created time = %v, want canonical UTC microseconds", first.CreatedAt)
	}

	first.Payload[0] = 'Y'
	second := event.Snapshot()
	if !bytes.Equal(second.Payload, []byte("signed checkpoint")) {
		t.Fatalf("event payload = %q after snapshot mutation", second.Payload)
	}

	first.Sequence = 4
	persisted, err := RestoreEvent(first)
	if err != nil {
		t.Fatalf("RestoreEvent() error = %v", err)
	}
	if persisted.Snapshot().Sequence != 4 {
		t.Fatalf("restored sequence = %d, want 4", persisted.Snapshot().Sequence)
	}
}

func TestEventRejectsInvalidDurableState(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	_, err := NewEvent(uuid.New(), EventTypeAuditCheckpointPending, AggregateTypeAuditChain, uuid.New(), nil, now, now)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty payload error = %v, want ErrInvalidInput", err)
	}

	event, err := NewEvent(uuid.New(), EventTypeAuditCheckpointPending, AggregateTypeAuditChain, uuid.New(), []byte{1}, now, now)
	if err != nil {
		t.Fatalf("NewEvent() error = %v", err)
	}
	if _, err := RestoreEvent(event.Snapshot()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("persisted zero sequence error = %v, want ErrInvalidInput", err)
	}
}
