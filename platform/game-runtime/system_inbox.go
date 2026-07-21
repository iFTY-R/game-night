package gameruntime

import (
	"context"
	"crypto/sha256"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/outbox"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

// SystemInboxStatus distinguishes durable work awaiting engine effects from an acknowledged receipt.
type SystemInboxStatus string

const (
	SystemInboxPending   SystemInboxStatus = "pending"
	SystemInboxCompleted SystemInboxStatus = "completed"
)

// SystemInboxKey binds one session to the globally unique room outbox event that must affect it.
type SystemInboxKey struct {
	SessionID     uuid.UUID
	SourceEventID uuid.UUID
}

// Valid rejects keys that cannot bind one frozen session and one durable source fact.
func (key SystemInboxKey) Valid() bool {
	return key.SessionID != uuid.Nil && key.SourceEventID != uuid.Nil
}

// SystemInboxSnapshot is the persistence-neutral state used by the dispatcher and PostgreSQL adapter.
type SystemInboxSnapshot struct {
	Key                   SystemInboxKey
	EventType             outbox.EventType
	PayloadDigest         idempotency.Digest
	Status                SystemInboxStatus
	CommittedStateVersion uint64
	CreatedAt             time.Time
	CompletedAt           time.Time
}

// SystemInboxRecord is immutable so retries cannot alter the source binding before acknowledgement.
type SystemInboxRecord struct {
	snapshot SystemInboxSnapshot
}

// RestoreSystemInboxRecord validates pending/completed shape and canonical timestamps from persistence.
func RestoreSystemInboxRecord(snapshot SystemInboxSnapshot) (SystemInboxRecord, error) {
	snapshot.CreatedAt = canonicalRuntimeTime(snapshot.CreatedAt)
	snapshot.CompletedAt = canonicalRuntimeTime(snapshot.CompletedAt)
	zeroDigest := idempotency.Digest{}
	if !snapshot.Key.Valid() || snapshot.EventType != roomDomain.ParticipantRevokedEventType ||
		snapshot.PayloadDigest == zeroDigest || snapshot.CreatedAt.IsZero() {
		return SystemInboxRecord{}, ErrInvalidSessionInput
	}
	switch snapshot.Status {
	case SystemInboxPending:
		if snapshot.CommittedStateVersion != 0 || !snapshot.CompletedAt.IsZero() {
			return SystemInboxRecord{}, ErrInvalidSessionInput
		}
	case SystemInboxCompleted:
		if snapshot.CommittedStateVersion == 0 || snapshot.CompletedAt.Before(snapshot.CreatedAt) {
			return SystemInboxRecord{}, ErrInvalidSessionInput
		}
	default:
		return SystemInboxRecord{}, ErrInvalidSessionInput
	}
	return SystemInboxRecord{snapshot: snapshot}, nil
}

// Snapshot returns the validated value used for CAS completion and retry decisions.
func (record SystemInboxRecord) Snapshot() SystemInboxSnapshot { return record.snapshot }

// SystemInboxStore persists source/digest bindings separately from game-owned command receipts.
type SystemInboxStore interface {
	Store
	GetSystemInbox(context.Context, SystemInboxKey, idempotency.Digest) (SystemInboxRecord, error)
	CompleteSystemInbox(context.Context, SystemInboxKey, idempotency.Digest, uint64, time.Time) (SystemInboxRecord, error)
}

// SystemInboxExecutor acquires the authoritative session lease before applying a prepared system command.
type SystemInboxExecutor interface {
	EnsureOwned(context.Context, uuid.UUID) (uint64, error)
	HandleSystem(context.Context, SystemCommand) (SystemCommitResult, error)
}

// SystemInboxConsumeResult tells the durable outbox dispatcher when it may safely advance its offset.
type SystemInboxConsumeResult struct {
	Record   SystemInboxRecord
	Replayed bool
}

// SystemInbox turns neutral room facts into exact-version module commands and completes them idempotently.
type SystemInbox struct {
	registry Registry
	store    SystemInboxStore
	executor SystemInboxExecutor
}

// NewSystemInbox requires one authoritative session/inbox store and an ownership-fenced executor.
func NewSystemInbox(registry Registry, store SystemInboxStore, executor SystemInboxExecutor) (*SystemInbox, error) {
	if registry == nil || store == nil || executor == nil {
		return nil, ErrInvalidSessionInput
	}
	return &SystemInbox{registry: registry, store: store, executor: executor}, nil
}

// Consume applies one registered participant revocation and returns only after its inbox row is completed.
func (inbox *SystemInbox) Consume(ctx context.Context, event outbox.Event) (SystemInboxConsumeResult, error) {
	if inbox == nil || ctx == nil {
		return SystemInboxConsumeResult{}, ErrInvalidSessionInput
	}
	fact, err := roomDomain.ParseParticipantRevokedEvent(event)
	if err != nil {
		return SystemInboxConsumeResult{}, ErrGameSessionIntegrity
	}
	payloadDigestBytes := sha256.Sum256(event.Snapshot().Payload)
	payloadDigest, err := idempotency.NewDigest(payloadDigestBytes[:])
	if err != nil {
		return SystemInboxConsumeResult{}, ErrGameSessionIntegrity
	}
	key := SystemInboxKey{SessionID: fact.SessionID, SourceEventID: fact.EventID}
	record, err := inbox.store.GetSystemInbox(ctx, key, payloadDigest)
	if err != nil {
		return SystemInboxConsumeResult{}, err
	}
	if record.Snapshot().Status == SystemInboxCompleted {
		return SystemInboxConsumeResult{Record: record, Replayed: true}, nil
	}
	session, err := inbox.store.Get(ctx, fact.SessionID)
	if err != nil {
		return SystemInboxConsumeResult{}, err
	}
	if session.Snapshot().RoomID != fact.RoomID || !sessionHasParticipant(session, fact.UserID) {
		return SystemInboxConsumeResult{}, ErrGameSessionIntegrity
	}
	module, err := inbox.registry.Resolve(session.Snapshot().VersionKey)
	if err != nil {
		return SystemInboxConsumeResult{}, err
	}
	encoder, ok := module.(game.ParticipantRevocationGameModule)
	if !ok {
		return SystemInboxConsumeResult{}, ErrModuleUnavailable
	}
	message, err := encoder.EncodeParticipantRevoked(game.ParticipantRevocationFact{UserID: game.Identifier(fact.UserID.String())})
	if err != nil || !message.Valid() {
		return SystemInboxConsumeResult{}, ErrInvalidSystemCommit
	}
	operationID, err := idempotency.NewOperationID(fact.EventID[:])
	if err != nil {
		return SystemInboxConsumeResult{}, ErrInvalidSessionInput
	}
	if !session.Snapshot().Status.Terminal() {
		if _, err := inbox.executor.EnsureOwned(ctx, fact.SessionID); err != nil {
			return SystemInboxConsumeResult{}, err
		}
	}
	result, err := inbox.executor.HandleSystem(ctx, SystemCommand{
		SessionID: fact.SessionID, OperationID: operationID,
		Source:               SystemSource{Kind: SystemSourceRoomOutbox, EventID: fact.EventID},
		ExpectedStateVersion: session.Snapshot().State.StateVersion,
		OwnershipEpoch:       session.Snapshot().OwnershipEpoch, VersionKey: session.Snapshot().VersionKey, Message: message,
	})
	if err != nil {
		return SystemInboxConsumeResult{}, err
	}
	receipt := result.Receipt.Snapshot()
	completed, err := inbox.store.CompleteSystemInbox(ctx, key, payloadDigest, receipt.StateVersion, receipt.CommittedAt)
	if err != nil {
		return SystemInboxConsumeResult{}, err
	}
	return SystemInboxConsumeResult{Record: completed, Replayed: result.Replayed}, nil
}

func sessionHasParticipant(session Session, userID uuid.UUID) bool {
	for _, participant := range session.Snapshot().Participants {
		if participant.UserID == userID {
			return true
		}
	}
	return false
}
