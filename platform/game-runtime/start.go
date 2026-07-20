package gameruntime

import (
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/idempotency"
)

// StartKey scopes a client operation to the authenticated host and PartyRoom it can mutate.
type StartKey struct {
	ActorUserID uuid.UUID
	RoomID      uuid.UUID
	OperationID idempotency.OperationID
}

// Valid rejects unscoped operation IDs before they can become durable replay keys.
func (key StartKey) Valid() bool {
	return key.ActorUserID != uuid.Nil && key.RoomID != uuid.Nil && key.OperationID.Valid()
}

// StartReceiptSnapshot is the immutable durable result of one atomic room/session start.
type StartReceiptSnapshot struct {
	Key           StartKey
	RequestDigest idempotency.Digest
	SessionID     uuid.UUID
	CommittedAt   time.Time
}

// Valid ensures a receipt can identify one exact committed session without carrying game secrets.
func (snapshot StartReceiptSnapshot) Valid() bool {
	return snapshot.Key.Valid() && snapshot.SessionID != uuid.Nil &&
		!snapshot.CommittedAt.IsZero() && snapshot.CommittedAt.Location() == time.UTC
}

// StartReceipt protects the original request binding from accidental mutation during retries.
type StartReceipt struct {
	snapshot StartReceiptSnapshot
}

// NewStartReceipt creates the durable replay result written in the same transaction as the session.
func NewStartReceipt(snapshot StartReceiptSnapshot) (StartReceipt, error) {
	snapshot.CommittedAt = snapshot.CommittedAt.Round(0)
	if !snapshot.Valid() {
		return StartReceipt{}, ErrInvalidSessionInput
	}
	return StartReceipt{snapshot: snapshot}, nil
}

// Snapshot returns the immutable operation binding and committed session identity.
func (receipt StartReceipt) Snapshot() StartReceiptSnapshot { return receipt.snapshot }

// Replay verifies the request digest before returning the original receipt.
func (receipt StartReceipt) Replay(requestDigest idempotency.Digest) (StartReceipt, error) {
	if !receipt.snapshot.Valid() {
		return StartReceipt{}, ErrInvalidSessionInput
	}
	if receipt.snapshot.RequestDigest != requestDigest {
		return StartReceipt{}, idempotency.ErrConflict
	}
	return receipt, nil
}
