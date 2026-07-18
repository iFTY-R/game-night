package outbox

import (
	"context"
	"time"
)

const (
	// MaximumBatchSize bounds one dispatcher read and its in-memory payload retention.
	MaximumBatchSize = 1000
)

// EventRepository appends immutable events using caller-generated IDs for idempotency.
// Implementations using a global offset must keep sequence allocation serialized until producer commit.
type EventRepository interface {
	Insert(context.Context, Event) (Event, error)
}

// EventBatch selects available subscribed events while an owner holds the consumer lease.
type EventBatch struct {
	ConsumerID ConsumerID
	LeaseOwner LeaseOwner
	ReadAt     time.Time
	BatchSize  uint32
}

// NewEventBatch validates a bounded lease-authorized dispatcher read.
func NewEventBatch(consumerID ConsumerID, owner LeaseOwner, readAt time.Time, batchSize uint32) (EventBatch, error) {
	readAt = canonicalTime(readAt)
	if !consumerID.Valid() || !owner.Valid() || readAt.IsZero() || batchSize == 0 || batchSize > MaximumBatchSize {
		return EventBatch{}, ErrInvalidInput
	}
	return EventBatch{ConsumerID: consumerID, LeaseOwner: owner, ReadAt: readAt, BatchSize: batchSize}, nil
}

// ConsumerRepository persists independent offsets and applies only domain-produced CAS transitions.
type ConsumerRepository interface {
	// Insert atomically registers a consumer. Identical ID/subscriptions return the existing state;
	// the operation must never reset a progressed offset, lease, or retry state on an idempotent call.
	Insert(context.Context, Consumer) (Consumer, error)
	Get(context.Context, ConsumerID) (Consumer, error)
	AcquireLeaseCAS(context.Context, ConsumerCAS) (Consumer, error)
	RenewLeaseCAS(context.Context, ConsumerCAS) (Consumer, error)
	ReleaseLeaseCAS(context.Context, ConsumerCAS) (Consumer, error)
	ListAvailable(context.Context, EventBatch) ([]Event, error)
	AcknowledgeCAS(context.Context, ConsumerCAS) (Consumer, error)
	RecordRetryCAS(context.Context, ConsumerCAS) (Consumer, error)
}

// Transaction exposes outbox ports bound to the same authoritative database transaction.
type Transaction interface {
	Events() EventRepository
	Consumers() ConsumerRepository
}

// TransactionWork must not retain transaction-bound ports after returning.
type TransactionWork func(context.Context, Transaction) error

// UnitOfWork owns commit and rollback without exposing PostgreSQL handles to the domain.
type UnitOfWork interface {
	Run(context.Context, TransactionWork) error
}
