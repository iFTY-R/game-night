package audit

import (
	"context"
	"math"
	"time"

	"github.com/iFTY-R/game-night/platform/outbox"
)

const (
	// MaximumPageSize bounds one audit read so canonical payloads cannot cause unbounded retention.
	MaximumPageSize uint32 = 1000
)

// HeadReader reads the trusted chain position through the restricted database function.
type HeadReader interface {
	ReadHead(context.Context, ChainID) (Head, error)
}

// AppendRequest binds an event to the exact head observed by the surrounding business transaction.
type AppendRequest struct {
	ExpectedHead Head
	Event        SignedEvent
}

// EventAppender atomically compares the expected hash, appends one event, and advances the chain head.
type EventAppender interface {
	AppendEvent(context.Context, AppendRequest) (Head, error)
}

// ListRequest selects the events after one trusted sequence in ascending chain order.
type ListRequest struct {
	ChainID       ChainID
	AfterSequence uint64
	PageSize      uint32
}

// NewListRequest validates PostgreSQL-compatible pagination before a repository allocates a result page.
func NewListRequest(chainID ChainID, afterSequence uint64, pageSize uint32) (ListRequest, error) {
	if !chainID.Valid() || afterSequence > math.MaxInt64 || pageSize == 0 || pageSize > MaximumPageSize {
		return ListRequest{}, ErrInvalidInput
	}
	return ListRequest{ChainID: chainID, AfterSequence: afterSequence, PageSize: pageSize}, nil
}

// EventReader returns structurally validated signed envelopes; callers still verify signatures with Service.
type EventReader interface {
	List(context.Context, ListRequest) ([]SignedEvent, error)
}

// Repository is the minimum transaction-bound port required to read and append the audit chain.
type Repository interface {
	HeadReader
	EventAppender
	EventReader
}

// IntegrityVerifier authenticates restored events and checkpoints with historical audit public keys.
type IntegrityVerifier interface {
	Verify(SignedEvent) error
	VerifyCheckpoint(Checkpoint) error
}

// CheckpointProgress is durable acknowledgement state used to calculate health after restarts.
type CheckpointProgress struct {
	ChainID              ChainID
	AcknowledgedSequence uint64
	AcknowledgedAt       time.Time
	UncheckpointedSince  time.Time
}

// CheckpointRepository exposes pending anchors and durable consumer progress without object-storage types.
type CheckpointRepository interface {
	AppendPendingCheckpoint(context.Context, Checkpoint) error
	ReadCheckpointProgress(context.Context, ChainID) (CheckpointProgress, error)
}

// Transaction exposes audit and outbox participants bound to one authoritative database transaction.
// Callers append audit before their first outbox event so producer serialization has one global lock order.
type Transaction interface {
	Audit() Repository
	Checkpoints() CheckpointRepository
	OutboxEvents() outbox.EventRepository
}

// TransactionWork must not retain transaction-bound repositories after returning.
type TransactionWork func(context.Context, Transaction) error

// UnitOfWork commits authoritative writes, audit append, and durable outbox insertion atomically.
type UnitOfWork interface {
	Run(context.Context, TransactionWork) error
}
