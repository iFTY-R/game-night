package gameruntime

import (
	"context"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/outbox"
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

// Store is the authoritative PostgreSQL boundary; Redis may only cache values returned after these calls commit.
type Store interface {
	Create(context.Context, CreationCommit) (Session, error)
	Get(context.Context, uuid.UUID) (Session, error)
	AcquireOwnershipCAS(context.Context, Session, Session) (Session, error)
	GetActionReceipt(context.Context, ActionKey, idempotency.Digest) (ActionReceipt, error)
	CommitAction(context.Context, ActionCommit) (ActionCommitResult, error)
}
