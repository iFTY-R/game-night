package postgres

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/jackc/pgx/v5"
)

var (
	// auditCheckpointEventNamespace makes retries for the same immutable anchor reuse one outbox event ID.
	auditCheckpointEventNamespace = uuid.MustParse("63a11e4e-8564-4be6-b0e8-a45f5e1743b1")
	// auditChainAggregateNamespace assigns each configured audit chain one stable aggregate UUID.
	auditChainAggregateNamespace = uuid.MustParse("842d3caf-8ce5-43f5-bce4-4e6bbd724861")
)

type auditCheckpointQueries interface {
	auditQueries
	outboxEventQueries
	outboxConsumerQueries
	checkpointProgressQueries
}

// AuditCheckpointRepository bridges signed checkpoint values to durable outbox progress.
type AuditCheckpointRepository struct {
	queries  auditCheckpointQueries
	verifier audit.IntegrityVerifier
}

func newAuditCheckpointRepository(queries auditCheckpointQueries, verifier audit.IntegrityVerifier) *AuditCheckpointRepository {
	return &AuditCheckpointRepository{queries: queries, verifier: verifier}
}

// AppendPendingCheckpoint inserts a deterministic checkpoint event in the caller's current transaction.
func (repository *AuditCheckpointRepository) AppendPendingCheckpoint(ctx context.Context, checkpoint audit.Checkpoint) error {
	snapshot := checkpoint.Snapshot()
	payload := snapshot.CanonicalPayload()
	parsed, err := audit.ParseCheckpoint(payload)
	if err != nil || parsed.Snapshot().ObjectKey() != snapshot.ObjectKey() {
		return audit.ErrInvalidInput
	}
	if repository.verifier == nil || repository.verifier.VerifyCheckpoint(checkpoint) != nil {
		return audit.ErrIntegrity
	}
	event, err := outbox.NewEvent(
		CheckpointEventID(snapshot),
		outbox.EventTypeAuditCheckpointPending,
		outbox.AggregateTypeAuditChain,
		auditChainAggregateID(snapshot.ChainID),
		payload,
		snapshot.CreatedAt,
		snapshot.CreatedAt,
	)
	if err != nil {
		return audit.ErrInvalidInput
	}
	if _, err := newOutboxEventRepository(repository.queries).Insert(ctx, event); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		if errors.Is(err, outbox.ErrAlreadyExists) || errors.Is(err, outbox.ErrIntegrity) {
			return audit.ErrIntegrity
		}
		return audit.ErrCheckpointUnavailable
	}
	return nil
}

// ReadCheckpointProgress reconstructs the last acknowledged audit anchor and the oldest unanchored event.
func (repository *AuditCheckpointRepository) ReadCheckpointProgress(ctx context.Context, chainID audit.ChainID) (audit.CheckpointProgress, error) {
	if !chainID.Valid() {
		return audit.CheckpointProgress{}, audit.ErrInvalidInput
	}
	ackedSequence, err := repository.queries.ReadCheckpointConsumerSequence(ctx)
	if err != nil {
		return audit.CheckpointProgress{}, mapCheckpointDependencyError(ctx, err)
	}
	if ackedSequence < 0 {
		return audit.CheckpointProgress{}, audit.ErrIntegrity
	}

	progress := audit.CheckpointProgress{ChainID: chainID}
	var acknowledged audit.Checkpoint
	if ackedSequence > 0 {
		checkpoint, checkpointErr := repository.latestAcknowledgedCheckpoint(ctx, chainID)
		if checkpointErr != nil {
			return audit.CheckpointProgress{}, checkpointErr
		}
		acknowledged = checkpoint
		checkpointSnapshot := checkpoint.Snapshot()
		progress.AcknowledgedSequence = checkpointSnapshot.Sequence
		// The consumer schema stores an offset rather than a separate ack timestamp; the signed anchor time is durable and immutable.
		progress.AcknowledgedAt = checkpointSnapshot.CreatedAt
	}
	// Read the head after the consumer offset so READ COMMITTED cannot observe a newer ack against an older head snapshot.
	head, err := newAuditRepository(repository.queries, repository.verifier).ReadHead(ctx, chainID)
	if err != nil {
		return audit.CheckpointProgress{}, err
	}
	if progress.AcknowledgedSequence > head.Sequence() {
		return audit.CheckpointProgress{}, audit.ErrIntegrity
	}
	if progress.AcknowledgedSequence > 0 {
		anchorHash, _, anchorErr := repository.readAnchor(ctx, chainID, progress.AcknowledgedSequence)
		if anchorErr != nil {
			return audit.CheckpointProgress{}, anchorErr
		}
		if anchorHash != acknowledged.Snapshot().ChainHash {
			return audit.CheckpointProgress{}, audit.ErrIntegrity
		}
	}
	if progress.AcknowledgedSequence < head.Sequence() {
		_, createdAt, firstErr := repository.readAnchor(ctx, chainID, progress.AcknowledgedSequence+1)
		if firstErr != nil {
			return audit.CheckpointProgress{}, firstErr
		}
		progress.UncheckpointedSince = createdAt
	}
	return progress, nil
}

func (repository *AuditCheckpointRepository) latestAcknowledgedCheckpoint(
	ctx context.Context,
	chainID audit.ChainID,
) (audit.Checkpoint, error) {
	row, err := repository.queries.GetLatestAckedOutboxEventByType(ctx, sqlcgen.GetLatestAckedOutboxEventByTypeParams{
		ConsumerID: outbox.ConsumerIDAuditCheckpoint.Value(), EventType: outbox.EventTypeAuditCheckpointPending.Value(),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return audit.Checkpoint{}, audit.ErrIntegrity
		}
		return audit.Checkpoint{}, mapCheckpointDependencyError(ctx, err)
	}
	event, err := outboxEventFromRow(row)
	if err != nil {
		return audit.Checkpoint{}, audit.ErrIntegrity
	}
	eventSnapshot := event.Snapshot()
	if eventSnapshot.Type != outbox.EventTypeAuditCheckpointPending || eventSnapshot.AggregateType != outbox.AggregateTypeAuditChain {
		return audit.Checkpoint{}, audit.ErrIntegrity
	}
	checkpoint, err := audit.ParseCheckpoint(eventSnapshot.Payload)
	if err != nil || checkpoint.Snapshot().ChainID != chainID || repository.verifier == nil ||
		repository.verifier.VerifyCheckpoint(checkpoint) != nil {
		return audit.Checkpoint{}, audit.ErrIntegrity
	}
	return checkpoint, nil
}

func (repository *AuditCheckpointRepository) readAnchor(
	ctx context.Context,
	chainID audit.ChainID,
	sequence uint64,
) (audit.Hash, time.Time, error) {
	if !chainID.Valid() || sequence == 0 || sequence > math.MaxInt64 {
		return audit.Hash{}, time.Time{}, audit.ErrInvalidInput
	}
	row, err := repository.queries.ReadAuditAnchor(ctx, sqlcgen.ReadAuditAnchorParams{
		ChainID: string(chainID), Sequence: int64(sequence),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return audit.Hash{}, time.Time{}, audit.ErrIntegrity
		}
		return audit.Hash{}, time.Time{}, mapCheckpointDependencyError(ctx, err)
	}
	if !row.CreatedAt.Valid {
		return audit.Hash{}, time.Time{}, audit.ErrIntegrity
	}
	hash, err := audit.NewHash(row.EventHash)
	if err != nil {
		return audit.Hash{}, time.Time{}, audit.ErrIntegrity
	}
	return hash, row.CreatedAt.Time.UTC().Truncate(time.Microsecond), nil
}

// CheckpointEventID derives the stable outbox event ID used for one immutable checkpoint payload.
func CheckpointEventID(snapshot audit.CheckpointSnapshot) uuid.UUID {
	return uuid.NewSHA1(auditCheckpointEventNamespace, []byte(snapshot.ObjectKey()))
}

func auditChainAggregateID(chainID audit.ChainID) uuid.UUID {
	return uuid.NewSHA1(auditChainAggregateNamespace, []byte(chainID))
}

func mapCheckpointDependencyError(ctx context.Context, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if errors.Is(err, audit.ErrIntegrity) || errors.Is(err, outbox.ErrIntegrity) {
		return audit.ErrIntegrity
	}
	return audit.ErrCheckpointUnavailable
}

var _ audit.CheckpointRepository = (*AuditCheckpointRepository)(nil)
