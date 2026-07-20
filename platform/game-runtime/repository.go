package gameruntime

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/outbox"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

// CreationCommit keeps the initial state, deterministic event batch, and durable notification inseparable.
type CreationCommit struct {
	Session      Session
	Batch        EventBatch
	OutboxEvents []outbox.Event
}

// ActionCommitResult distinguishes a newly committed transition from a durable idempotent replay.
type ActionCommitResult struct {
	Session  Session
	Receipt  ActionReceipt
	Replayed bool
}

// TimerCommitResult distinguishes a newly committed timer transition from a durable retry result.
type TimerCommitResult struct {
	Session  Session
	Receipt  TimerReceipt
	Replayed bool
}

// SystemCommitResult distinguishes a new system transition from an operation/source/digest replay.
type SystemCommitResult struct {
	Session  Session
	Receipt  SystemReceipt
	Replayed bool
	// Retry means the pending operation was preserved but the caller must recompute from Session.
	Retry bool
}

// DueTimer is a non-locking scheduling candidate; CommitTimer rechecks every field under row locks.
type DueTimer struct {
	SessionID            uuid.UUID
	TimerID              game.Identifier
	ExpectedStateVersion uint64
	DueAt                time.Time
	Message              game.Message
}

// Store is the authoritative PostgreSQL boundary; Redis may only cache values returned after these calls commit.
type Store interface {
	Create(context.Context, CreationCommit) (Session, error)
	Get(context.Context, uuid.UUID) (Session, error)
	AcquireOwnershipCAS(context.Context, Session, Session) (Session, error)
	GetActionReceipt(context.Context, ActionKey, idempotency.Digest) (ActionReceipt, error)
	CommitAction(context.Context, ActionCommit) (ActionCommitResult, error)
	GetTimerReceipt(context.Context, TimerKey) (TimerReceipt, error)
	CommitTimer(context.Context, TimerCommit) (TimerCommitResult, error)
	GetSystemReceipt(context.Context, SystemKey, idempotency.Digest) (SystemReceipt, error)
	CommitSystem(context.Context, SystemCommit) (SystemCommitResult, error)
	CompleteSystemNoop(context.Context, SystemKey, idempotency.Digest, time.Time) (SystemCommitResult, error)
	CommitLifecycle(context.Context, LifecycleCommit) (Session, error)
	ListDueTimers(context.Context, time.Time, uint32) ([]DueTimer, error)
	ReadEventBatches(context.Context, uuid.UUID, uint64, uint32) ([]EventBatch, error)
}
