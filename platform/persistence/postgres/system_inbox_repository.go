package postgres

import (
	"bytes"
	"context"
	"math"
	"time"

	"github.com/google/uuid"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/jackc/pgx/v5/pgtype"
)

// GetSystemInbox locks one neutral source binding while validating the caller's canonical payload digest.
func (repository *GameSessionRepository) GetSystemInbox(
	ctx context.Context,
	key gameruntime.SystemInboxKey,
	digest idempotency.Digest,
) (gameruntime.SystemInboxRecord, error) {
	if repository == nil || repository.runner == nil || ctx == nil || !key.Valid() {
		return gameruntime.SystemInboxRecord{}, gameruntime.ErrInvalidSessionInput
	}
	var record gameruntime.SystemInboxRecord
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		row, err := queries.GetGameSystemInboxForUpdate(ctx, sqlcgen.GetGameSystemInboxForUpdateParams{
			SessionID: uuidToPG(key.SessionID), SourceEventID: uuidToPG(key.SourceEventID),
		})
		if err != nil {
			return err
		}
		record, err = systemInboxFromRow(row, key, digest)
		return err
	})
	if err != nil {
		return gameruntime.SystemInboxRecord{}, mapGameSessionRepositoryError(ctx, err, gameruntime.ErrSystemInboxNotFound)
	}
	return record, nil
}

// CompleteSystemInbox marks one source fact acknowledged after HandleSystem returns a durable receipt.
func (repository *GameSessionRepository) CompleteSystemInbox(
	ctx context.Context,
	key gameruntime.SystemInboxKey,
	digest idempotency.Digest,
	stateVersion uint64,
	completedAt time.Time,
) (gameruntime.SystemInboxRecord, error) {
	completedAt = completedAt.Round(0).UTC()
	if repository == nil || repository.runner == nil || ctx == nil || !key.Valid() || stateVersion == 0 ||
		stateVersion > math.MaxInt64 || completedAt.IsZero() {
		return gameruntime.SystemInboxRecord{}, gameruntime.ErrInvalidSessionInput
	}
	var record gameruntime.SystemInboxRecord
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		row, err := queries.GetGameSystemInboxForUpdate(ctx, sqlcgen.GetGameSystemInboxForUpdateParams{
			SessionID: uuidToPG(key.SessionID), SourceEventID: uuidToPG(key.SourceEventID),
		})
		if err != nil {
			return err
		}
		current, err := systemInboxFromRow(row, key, digest)
		if err != nil {
			return err
		}
		if current.Snapshot().Status == gameruntime.SystemInboxCompleted {
			if current.Snapshot().CommittedStateVersion != stateVersion {
				return gameruntime.ErrGameSessionIntegrity
			}
			record = current
			return nil
		}
		row, err = queries.CompleteGameSystemInboxCAS(ctx, sqlcgen.CompleteGameSystemInboxCASParams{
			CommittedStateVersion: pgtype.Int8{Int64: int64(stateVersion), Valid: true}, CompletedAt: timeToPG(completedAt),
			SessionID: uuidToPG(key.SessionID), SourceEventID: uuidToPG(key.SourceEventID), PayloadDigest: digest.Bytes(),
		})
		if err != nil {
			return err
		}
		record, err = systemInboxFromRow(row, key, digest)
		return err
	})
	if err != nil {
		return gameruntime.SystemInboxRecord{}, mapGameSessionRepositoryError(ctx, err, gameruntime.ErrSystemInboxNotFound)
	}
	return record, nil
}

// systemInboxFromRow treats a source or digest mismatch as integrity failure so retries cannot reinterpret durable work.
func systemInboxFromRow(
	row sqlcgen.GameSystemInbox,
	key gameruntime.SystemInboxKey,
	digest idempotency.Digest,
) (gameruntime.SystemInboxRecord, error) {
	if !row.SessionID.Valid || !row.SourceEventID.Valid || !row.CreatedAt.Valid ||
		uuid.UUID(row.SessionID.Bytes) != key.SessionID || uuid.UUID(row.SourceEventID.Bytes) != key.SourceEventID {
		return gameruntime.SystemInboxRecord{}, gameruntime.ErrGameSessionIntegrity
	}
	if !bytes.Equal(row.PayloadDigest, digest.Bytes()) {
		return gameruntime.SystemInboxRecord{}, idempotency.ErrConflict
	}
	stateVersion := uint64(0)
	if row.CommittedStateVersion.Valid {
		if row.CommittedStateVersion.Int64 <= 0 {
			return gameruntime.SystemInboxRecord{}, gameruntime.ErrGameSessionIntegrity
		}
		stateVersion = uint64(row.CommittedStateVersion.Int64)
	}
	record, err := gameruntime.RestoreSystemInboxRecord(gameruntime.SystemInboxSnapshot{
		Key: key, EventType: outbox.EventType(row.EventType), PayloadDigest: digest,
		Status: gameruntime.SystemInboxStatus(row.Status), CommittedStateVersion: stateVersion,
		CreatedAt: row.CreatedAt.Time, CompletedAt: optionalTimeFromPG(row.CompletedAt),
	})
	if err != nil {
		return gameruntime.SystemInboxRecord{}, gameruntime.ErrGameSessionIntegrity
	}
	return record, nil
}

var _ gameruntime.SystemInboxStore = (*GameSessionRepository)(nil)
