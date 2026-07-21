package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"reflect"
	"time"

	"github.com/google/uuid"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GameSessionRepository persists authoritative session state and binds every child write to one transaction.
type GameSessionRepository struct {
	runner *TransactionRunner
}

// NewGameSessionRepository binds session persistence to the supplied runtime PostgreSQL pool.
func NewGameSessionRepository(pool *pgxpool.Pool) *GameSessionRepository {
	return &GameSessionRepository{runner: NewTransactionRunner(pool)}
}

// Create commits the initial state, frozen participants, timers, event batch, and outbox notification together.
func (repository *GameSessionRepository) Create(ctx context.Context, commit gameruntime.CreationCommit) (gameruntime.Session, error) {
	if repository == nil || repository.runner == nil || ctx == nil || !commit.Valid() {
		return gameruntime.Session{}, gameruntime.ErrInvalidSessionInput
	}
	if err := validateCreationWidths(commit); err != nil {
		return gameruntime.Session{}, err
	}
	var stored gameruntime.Session
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		var err error
		stored, err = createGameSessionAggregate(ctx, queries, commit)
		return err
	})
	if err != nil {
		return gameruntime.Session{}, mapGameSessionRepositoryError(ctx, err, gameruntime.ErrSessionAlreadyExists)
	}
	return stored, nil
}

// createGameSessionAggregate writes the session and all immutable creation children through one query handle.
// The surrounding transaction must also own any parent aggregate pointer that references this session.
func createGameSessionAggregate(ctx context.Context, queries QueryHandle, commit gameruntime.CreationCommit) (gameruntime.Session, error) {
	snapshot := commit.Session.Snapshot()
	row, err := queries.CreateGameSession(ctx, createGameSessionParams(snapshot))
	if err != nil {
		return gameruntime.Session{}, err
	}
	for _, participant := range snapshot.Participants {
		if err := queries.CreateGameSessionParticipant(ctx, sqlcgen.CreateGameSessionParticipantParams{
			SessionID: uuidToPG(snapshot.ID), UserID: uuidToPG(participant.UserID), SeatIndex: int32(participant.SeatIndex),
		}); err != nil {
			return gameruntime.Session{}, err
		}
	}
	if err := replaceGameSessionTimers(ctx, queries, snapshot.ID, snapshot.Timers); err != nil {
		return gameruntime.Session{}, err
	}
	if err := insertGameSessionBatch(ctx, queries, commit.Batch); err != nil {
		return gameruntime.Session{}, err
	}
	if err := insertGameSessionOutbox(ctx, queries, commit.OutboxEvents); err != nil {
		return gameruntime.Session{}, err
	}
	participants, err := queries.ListGameSessionParticipants(ctx, sqlcgen.ListGameSessionParticipantsParams{SessionID: uuidToPG(snapshot.ID)})
	if err != nil {
		return gameruntime.Session{}, err
	}
	timers, err := queries.ListGameSessionTimers(ctx, sqlcgen.ListGameSessionTimersParams{SessionID: uuidToPG(snapshot.ID)})
	if err != nil {
		return gameruntime.Session{}, err
	}
	return sessionFromRows(row, participants, timers)
}

// Get loads one session and its immutable children in a consistent PostgreSQL transaction snapshot.
func (repository *GameSessionRepository) Get(ctx context.Context, sessionID uuid.UUID) (gameruntime.Session, error) {
	if repository == nil || repository.runner == nil || ctx == nil || sessionID == uuid.Nil {
		return gameruntime.Session{}, gameruntime.ErrInvalidSessionInput
	}
	var loaded gameruntime.Session
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		row, err := queries.GetGameSessionForShare(ctx, sqlcgen.GetGameSessionForShareParams{SessionID: uuidToPG(sessionID)})
		if err != nil {
			return err
		}
		participants, err := queries.ListGameSessionParticipants(ctx, sqlcgen.ListGameSessionParticipantsParams{SessionID: uuidToPG(sessionID)})
		if err != nil {
			return err
		}
		timers, err := queries.ListGameSessionTimers(ctx, sqlcgen.ListGameSessionTimersParams{SessionID: uuidToPG(sessionID)})
		if err != nil {
			return err
		}
		loaded, err = sessionFromRows(row, participants, timers)
		return err
	})
	if err != nil {
		return gameruntime.Session{}, mapGameSessionRepositoryError(ctx, err, gameruntime.ErrSessionNotFound)
	}
	return loaded, nil
}

// AcquireOwnershipCAS increments the epoch only when the caller still owns the expected state snapshot.
func (repository *GameSessionRepository) AcquireOwnershipCAS(
	ctx context.Context,
	before gameruntime.Session,
	next gameruntime.Session,
) (gameruntime.Session, error) {
	if repository == nil || repository.runner == nil || ctx == nil {
		return gameruntime.Session{}, gameruntime.ErrInvalidSessionInput
	}
	beforeSnapshot, nextSnapshot := before.Snapshot(), next.Snapshot()
	if err := validateOwnershipTransition(beforeSnapshot, nextSnapshot); err != nil {
		return gameruntime.Session{}, err
	}
	var stored gameruntime.Session
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		row, err := queries.AcquireGameSessionOwnershipCAS(ctx, sqlcgen.AcquireGameSessionOwnershipCASParams{
			UpdatedAt: timeToPG(nextSnapshot.UpdatedAt), SessionID: uuidToPG(beforeSnapshot.ID),
			ExpectedStateVersion: int64(beforeSnapshot.State.StateVersion), ExpectedOwnershipEpoch: int64(beforeSnapshot.OwnershipEpoch),
		})
		if err != nil {
			return err
		}
		participants, err := queries.ListGameSessionParticipants(ctx, sqlcgen.ListGameSessionParticipantsParams{SessionID: uuidToPG(beforeSnapshot.ID)})
		if err != nil {
			return err
		}
		timers, err := queries.ListGameSessionTimers(ctx, sqlcgen.ListGameSessionTimersParams{SessionID: uuidToPG(beforeSnapshot.ID)})
		if err != nil {
			return err
		}
		stored, err = sessionFromRows(row, participants, timers)
		return err
	})
	if err != nil {
		return gameruntime.Session{}, mapGameSessionRepositoryError(ctx, err, gameruntime.ErrOwnershipLost)
	}
	return stored, nil
}

// GetActionReceipt reads the PostgreSQL authority before any Redis cache can be considered.
func (repository *GameSessionRepository) GetActionReceipt(
	ctx context.Context,
	key gameruntime.ActionKey,
	requestDigest idempotency.Digest,
) (gameruntime.ActionReceipt, error) {
	if repository == nil || repository.runner == nil || ctx == nil || !key.Valid() {
		return gameruntime.ActionReceipt{}, gameruntime.ErrInvalidSessionInput
	}
	var receipt gameruntime.ActionReceipt
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		if err := lockActionReceiptFence(ctx, queries, key); err != nil {
			return err
		}
		row, err := queries.GetGameActionReceipt(ctx, sqlcgen.GetGameActionReceiptParams{
			SessionID: uuidToPG(key.SessionID), ActorUserID: uuidToPG(key.ActorUserID), ActionID: key.ActionID.Value(),
		})
		if err != nil {
			return err
		}
		receipt, err = actionReceiptFromRow(row)
		if err != nil {
			return err
		}
		_, err = receipt.Replay(requestDigest)
		return err
	})
	if err != nil {
		return gameruntime.ActionReceipt{}, mapGameSessionRepositoryError(ctx, err, gameruntime.ErrActionReceiptNotFound)
	}
	return receipt, nil
}

func lockActionReceiptFence(ctx context.Context, queries QueryHandle, key gameruntime.ActionKey) error {
	roomID, err := queries.GetGameSessionRoomID(ctx, sqlcgen.GetGameSessionRoomIDParams{
		SessionID: uuidToPG(key.SessionID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return gameruntime.ErrSessionNotFound
	}
	if err != nil || !roomID.Valid {
		return err
	}
	_, err = queries.GetPartyRoomForUpdate(ctx, sqlcgen.GetPartyRoomForUpdateParams{RoomID: roomID})
	if err != nil {
		return err
	}
	role, err := queries.GetRoomMemberRole(ctx, sqlcgen.GetRoomMemberRoleParams{RoomID: roomID, UserID: uuidToPG(key.ActorUserID)})
	if errors.Is(err, pgx.ErrNoRows) || err == nil && role != string(roomDomain.MemberRoleParticipant) {
		return gameruntime.ErrParticipantNotActive
	}
	return err
}

// CommitAction serializes one session row, resolves idempotent retries, and atomically writes every durable child.
func (repository *GameSessionRepository) CommitAction(
	ctx context.Context,
	commit gameruntime.ActionCommit,
) (gameruntime.ActionCommitResult, error) {
	if repository == nil || repository.runner == nil || ctx == nil || !commit.Valid() {
		return gameruntime.ActionCommitResult{}, gameruntime.ErrInvalidActionCommit
	}
	if err := validateActionWidths(commit); err != nil {
		return gameruntime.ActionCommitResult{}, err
	}
	var result gameruntime.ActionCommitResult
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		before := commit.Before().Snapshot()
		// Every participant action joins the same room->session lock order used by remove and finish.
		// Reading membership only after the room lock ensures a concurrent removal cannot leave a stale authorization snapshot.
		if err := lockActionParticipantFence(ctx, queries, before, commit.Receipt().Snapshot().Key.ActorUserID); err != nil {
			return err
		}
		var err error
		result, err = commitActionAfterRoomLock(ctx, queries, commit)
		return err
	})
	if err != nil {
		return gameruntime.ActionCommitResult{}, mapGameSessionRepositoryError(ctx, err, gameruntime.ErrSessionNotFound)
	}
	return result, nil
}

func commitActionAfterRoomLock(
	ctx context.Context,
	queries QueryHandle,
	commit gameruntime.ActionCommit,
) (gameruntime.ActionCommitResult, error) {
	before, after := commit.Before().Snapshot(), commit.After().Snapshot()
	current, err := getGameSessionForUpdate(ctx, queries, before.ID)
	if err != nil {
		return gameruntime.ActionCommitResult{}, err
	}
	receiptRow, receiptErr := queries.GetGameActionReceipt(ctx, sqlcgen.GetGameActionReceiptParams{
		SessionID: uuidToPG(before.ID), ActorUserID: uuidToPG(commit.Receipt().Snapshot().Key.ActorUserID),
		ActionID: commit.Receipt().Snapshot().Key.ActionID.Value(),
	})
	if receiptErr == nil {
		receipt, err := actionReceiptFromRow(receiptRow)
		if err != nil {
			return gameruntime.ActionCommitResult{}, err
		}
		if _, err := receipt.Replay(commit.Receipt().Snapshot().RequestDigest); err != nil {
			return gameruntime.ActionCommitResult{}, err
		}
		return gameruntime.ActionCommitResult{Session: current, Receipt: receipt, Replayed: true}, nil
	}
	if !errors.Is(receiptErr, pgx.ErrNoRows) {
		return gameruntime.ActionCommitResult{}, receiptErr
	}
	if err := validateCurrentTransition(current, before); err != nil {
		return gameruntime.ActionCommitResult{}, err
	}
	if err := rejectTerminalWithPendingSystemInbox(ctx, queries, commit.After().Snapshot(), uuid.Nil); err != nil {
		return gameruntime.ActionCommitResult{}, err
	}
	updatedRow, err := queries.UpdateGameSessionStateCAS(ctx, updateGameSessionStateParams(before, after))
	if err != nil {
		return gameruntime.ActionCommitResult{}, err
	}
	if err := replaceGameSessionTimers(ctx, queries, after.ID, after.Timers); err != nil {
		return gameruntime.ActionCommitResult{}, err
	}
	if err := insertGameSessionBatch(ctx, queries, commit.Batch()); err != nil {
		return gameruntime.ActionCommitResult{}, err
	}
	if err := insertGameActionReceipt(ctx, queries, commit.Receipt()); err != nil {
		return gameruntime.ActionCommitResult{}, err
	}
	if err := insertGameSessionOutbox(ctx, queries, commit.OutboxEvents()); err != nil {
		return gameruntime.ActionCommitResult{}, err
	}
	stored, err := sessionFromUpdatedRow(ctx, queries, updatedRow)
	if err != nil {
		return gameruntime.ActionCommitResult{}, err
	}
	return gameruntime.ActionCommitResult{Session: stored, Receipt: commit.Receipt()}, nil
}

// GetTimerReceipt returns the original durable result for one exact scheduled timer firing.
func (repository *GameSessionRepository) GetTimerReceipt(ctx context.Context, key gameruntime.TimerKey) (gameruntime.TimerReceipt, error) {
	if repository == nil || repository.runner == nil || ctx == nil || !key.Valid() {
		return gameruntime.TimerReceipt{}, gameruntime.ErrInvalidSessionInput
	}
	var receipt gameruntime.TimerReceipt
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		row, err := queries.GetGameTimerReceipt(ctx, sqlcgen.GetGameTimerReceiptParams{
			SessionID: uuidToPG(key.SessionID), TimerID: string(key.TimerID), ExpectedStateVersion: int64(key.ExpectedStateVersion),
		})
		if err != nil {
			return err
		}
		receipt, err = timerReceiptFromRow(row)
		return err
	})
	if err != nil {
		return gameruntime.TimerReceipt{}, mapGameSessionRepositoryError(ctx, err, gameruntime.ErrTimerReceiptNotFound)
	}
	return receipt, nil
}

// CommitTimer rechecks the persisted timer under the session lock and commits one firing or its durable replay.
func (repository *GameSessionRepository) CommitTimer(
	ctx context.Context,
	commit gameruntime.TimerCommit,
) (gameruntime.TimerCommitResult, error) {
	if repository == nil || repository.runner == nil || ctx == nil || !commit.Valid() {
		return gameruntime.TimerCommitResult{}, gameruntime.ErrInvalidTimerCommit
	}
	if err := validateTimerCommitWidths(commit); err != nil {
		return gameruntime.TimerCommitResult{}, err
	}
	var result gameruntime.TimerCommitResult
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		before := commit.Before().Snapshot()
		current, err := getGameSessionForUpdate(ctx, queries, before.ID)
		if err != nil {
			return err
		}
		result, err = commitTimerAfterSessionLock(ctx, queries, current, commit)
		return err
	})
	if err != nil {
		return gameruntime.TimerCommitResult{}, mapGameSessionRepositoryError(ctx, err, gameruntime.ErrSessionNotFound)
	}
	return result, nil
}

func commitTimerAfterSessionLock(
	ctx context.Context,
	queries QueryHandle,
	current gameruntime.Session,
	commit gameruntime.TimerCommit,
) (gameruntime.TimerCommitResult, error) {
	before := commit.Before().Snapshot()
	key := commit.Receipt().Snapshot().Key
	receiptRow, receiptErr := queries.GetGameTimerReceipt(ctx, sqlcgen.GetGameTimerReceiptParams{
		SessionID: uuidToPG(key.SessionID), TimerID: string(key.TimerID), ExpectedStateVersion: int64(key.ExpectedStateVersion),
	})
	if receiptErr == nil {
		receipt, err := timerReceiptFromRow(receiptRow)
		if err != nil {
			return gameruntime.TimerCommitResult{}, err
		}
		return gameruntime.TimerCommitResult{Session: current, Receipt: receipt, Replayed: true}, nil
	}
	if !errors.Is(receiptErr, pgx.ErrNoRows) {
		return gameruntime.TimerCommitResult{}, receiptErr
	}
	if err := validateCurrentTransition(current, before); err != nil {
		return gameruntime.TimerCommitResult{}, err
	}
	if err := rejectTerminalWithPendingSystemInbox(ctx, queries, commit.After().Snapshot(), uuid.Nil); err != nil {
		return gameruntime.TimerCommitResult{}, err
	}
	timerRow, err := queries.GetGameSessionTimerForUpdate(ctx, sqlcgen.GetGameSessionTimerForUpdateParams{
		SessionID: uuidToPG(before.ID), TimerID: string(key.TimerID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return gameruntime.TimerCommitResult{}, gameruntime.ErrTimerNotFound
	}
	if err != nil {
		return gameruntime.TimerCommitResult{}, err
	}
	batch := commit.Batch().Snapshot()
	if timerRow.ExpectedStateVersion != int64(key.ExpectedStateVersion) || !timerRow.DueAt.Valid ||
		batch.CommittedAt.Before(timerRow.DueAt.Time) || timerRow.MessageType != string(batch.Input.MessageType) ||
		timerRow.SchemaVersion != int32(batch.Input.SchemaVersion) || !bytes.Equal(timerRow.Payload, batch.Input.Payload) {
		return gameruntime.TimerCommitResult{}, gameruntime.ErrTimerNotDue
	}
	stored, err := persistTimerTransition(ctx, queries, commit)
	if err != nil {
		return gameruntime.TimerCommitResult{}, err
	}
	return gameruntime.TimerCommitResult{Session: stored, Receipt: commit.Receipt()}, nil
}

// GetSystemReceipt returns only completed operation/source/digest bindings; pending work remains retryable.
func (repository *GameSessionRepository) GetSystemReceipt(
	ctx context.Context,
	key gameruntime.SystemKey,
	requestDigest idempotency.Digest,
) (gameruntime.SystemReceipt, error) {
	if repository == nil || repository.runner == nil || ctx == nil || !key.Valid() {
		return gameruntime.SystemReceipt{}, gameruntime.ErrInvalidSessionInput
	}
	var receipt gameruntime.SystemReceipt
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		row, err := queries.GetGameSystemOperationForUpdate(ctx, sqlcgen.GetGameSystemOperationForUpdateParams{
			SessionID: uuidToPG(key.SessionID), OperationID: key.OperationID.Value(),
		})
		if err != nil {
			return err
		}
		receipt, err = completedSystemReceiptFromRow(row, key, requestDigest)
		return err
	})
	if err != nil {
		return gameruntime.SystemReceipt{}, mapGameSessionRepositoryError(ctx, err, gameruntime.ErrSystemReceiptNotFound)
	}
	return receipt, nil
}

// CommitSystem persists a pending logical operation before applying its exact-version module transition.
func (repository *GameSessionRepository) CommitSystem(
	ctx context.Context,
	commit gameruntime.SystemCommit,
) (gameruntime.SystemCommitResult, error) {
	if repository == nil || repository.runner == nil || ctx == nil || !commit.Valid() {
		return gameruntime.SystemCommitResult{}, gameruntime.ErrInvalidSystemCommit
	}
	if err := validateSystemCommitWidths(commit); err != nil {
		return gameruntime.SystemCommitResult{}, err
	}
	var result gameruntime.SystemCommitResult
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		before := commit.Before().Snapshot()
		current, err := getGameSessionForUpdate(ctx, queries, before.ID)
		if err != nil {
			return err
		}
		result, err = commitSystemAfterSessionLock(ctx, queries, current, commit)
		return err
	})
	if err != nil {
		return gameruntime.SystemCommitResult{}, mapGameSessionRepositoryError(ctx, err, gameruntime.ErrSessionNotFound)
	}
	return result, nil
}

func commitSystemAfterSessionLock(
	ctx context.Context,
	queries QueryHandle,
	current gameruntime.Session,
	commit gameruntime.SystemCommit,
) (gameruntime.SystemCommitResult, error) {
	before := commit.Before().Snapshot()
	receiptSnapshot := commit.Receipt().Snapshot()
	operation, err := lockOrCreateSystemOperation(ctx, queries, receiptSnapshot)
	if err != nil {
		return gameruntime.SystemCommitResult{}, err
	}
	if operation.Status == "completed" {
		receipt, err := completedSystemReceiptFromRow(operation, receiptSnapshot.Key, receiptSnapshot.RequestDigest)
		if err != nil {
			return gameruntime.SystemCommitResult{}, err
		}
		return gameruntime.SystemCommitResult{Session: current, Receipt: receipt, Replayed: true}, nil
	}
	if current.Snapshot().Status.Terminal() {
		completedAt := terminalSystemCompletionTime(receiptSnapshot.CommittedAt, current.Snapshot().UpdatedAt)
		receipt, err := completeTerminalSystemOperation(
			ctx, queries, receiptSnapshot.Key, receiptSnapshot.RequestDigest, current, completedAt,
		)
		if err != nil {
			return gameruntime.SystemCommitResult{}, err
		}
		return gameruntime.SystemCommitResult{Session: current, Receipt: receipt}, nil
	}
	if current.Snapshot().OwnershipEpoch != before.OwnershipEpoch ||
		current.Snapshot().State.StateVersion != before.State.StateVersion {
		// Returning nil commits the pending operation; the service reloads this snapshot and recomputes without changing the logical digest.
		return gameruntime.SystemCommitResult{Session: current, Retry: true}, nil
	}
	if current.Snapshot().Status == gameruntime.StatusSuspended {
		return gameruntime.SystemCommitResult{}, gameruntime.ErrSessionSuspended
	}
	if err := rejectTerminalWithPendingSystemInbox(
		ctx, queries, commit.After().Snapshot(), receiptSnapshot.Key.Source.EventID,
	); err != nil {
		return gameruntime.SystemCommitResult{}, err
	}
	stored, err := persistSystemTransition(ctx, queries, commit)
	if err != nil {
		return gameruntime.SystemCommitResult{}, err
	}
	return gameruntime.SystemCommitResult{Session: stored, Receipt: commit.Receipt()}, nil
}

// CompleteSystemNoop durably acknowledges a late system command after the session is already terminal.
func (repository *GameSessionRepository) CompleteSystemNoop(
	ctx context.Context,
	key gameruntime.SystemKey,
	digest idempotency.Digest,
	completedAt time.Time,
) (gameruntime.SystemCommitResult, error) {
	completedAt = completedAt.Round(0).UTC()
	if repository == nil || repository.runner == nil || ctx == nil || !key.Valid() || completedAt.IsZero() {
		return gameruntime.SystemCommitResult{}, gameruntime.ErrInvalidSessionInput
	}
	var result gameruntime.SystemCommitResult
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		current, err := getGameSessionForUpdate(ctx, queries, key.SessionID)
		if err != nil {
			return err
		}
		if !current.Snapshot().Status.Terminal() {
			return gameruntime.ErrSystemOperationPending
		}
		completedAt = terminalSystemCompletionTime(completedAt, current.Snapshot().UpdatedAt)
		candidate, err := newTerminalSystemReceipt(key, digest, current, completedAt)
		if err != nil {
			return err
		}
		operation, err := lockOrCreateSystemOperation(ctx, queries, candidate.Snapshot())
		if err != nil {
			return err
		}
		if operation.Status == "completed" {
			receipt, err := completedSystemReceiptFromRow(operation, key, digest)
			if err != nil {
				return err
			}
			result = gameruntime.SystemCommitResult{Session: current, Receipt: receipt, Replayed: true}
			return nil
		}
		receipt, err := completeTerminalSystemOperation(ctx, queries, key, digest, current, completedAt)
		if err != nil {
			return err
		}
		result = gameruntime.SystemCommitResult{Session: current, Receipt: receipt}
		return nil
	})
	if err != nil {
		return gameruntime.SystemCommitResult{}, mapGameSessionRepositoryError(ctx, err, gameruntime.ErrSessionNotFound)
	}
	return result, nil
}

// CommitLifecycle persists a runtime-owned suspend or cancel CAS without creating an engine event batch.
func (repository *GameSessionRepository) CommitLifecycle(ctx context.Context, commit gameruntime.LifecycleCommit) (gameruntime.Session, error) {
	if repository == nil || repository.runner == nil || ctx == nil || !commit.Valid() {
		return gameruntime.Session{}, gameruntime.ErrInvalidLifecycleCommit
	}
	var stored gameruntime.Session
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		before := commit.Before().Snapshot()
		current, err := getGameSessionForUpdate(ctx, queries, before.ID)
		if err != nil {
			return err
		}
		stored, err = persistLifecycleAfterSessionLock(ctx, queries, current, commit)
		return err
	})
	if err != nil {
		return gameruntime.Session{}, mapGameSessionRepositoryError(ctx, err, gameruntime.ErrSessionNotFound)
	}
	return stored, nil
}

func persistLifecycleAfterSessionLock(
	ctx context.Context,
	queries QueryHandle,
	current gameruntime.Session,
	commit gameruntime.LifecycleCommit,
) (gameruntime.Session, error) {
	before, after := commit.Before().Snapshot(), commit.After().Snapshot()
	if err := validateCurrentLifecycleTransition(current, before); err != nil {
		return gameruntime.Session{}, err
	}
	row, err := queries.UpdateGameSessionLifecycleCAS(ctx, updateGameSessionLifecycleParams(before, after))
	if err != nil {
		return gameruntime.Session{}, err
	}
	if err := replaceGameSessionTimers(ctx, queries, after.ID, after.Timers); err != nil {
		return gameruntime.Session{}, err
	}
	if err := insertGameSessionOutbox(ctx, queries, commit.OutboxEvents()); err != nil {
		return gameruntime.Session{}, err
	}
	return sessionFromUpdatedRow(ctx, queries, row)
}

// validateCurrentLifecycleTransition permits exact suspended-state resume/cancel while retaining epoch and version fencing.
func validateCurrentLifecycleTransition(current gameruntime.Session, before gameruntime.SessionSnapshot) error {
	snapshot := current.Snapshot()
	if snapshot.OwnershipEpoch != before.OwnershipEpoch {
		return gameruntime.ErrOwnershipLost
	}
	if snapshot.State.StateVersion != before.State.StateVersion {
		return gameruntime.ErrStateVersionConflict
	}
	if snapshot.Status != before.Status {
		if snapshot.Status == gameruntime.StatusSuspended {
			return gameruntime.ErrSessionSuspended
		}
		if snapshot.Status.Terminal() {
			return gameruntime.ErrSessionTerminal
		}
		return gameruntime.ErrInvalidLifecycleCommit
	}
	if snapshot.Status.Terminal() {
		return gameruntime.ErrSessionTerminal
	}
	return nil
}

// ListDueTimers returns bounded scheduling candidates without claiming ownership; CommitTimer performs authoritative checks.
func (repository *GameSessionRepository) ListDueTimers(ctx context.Context, dueAt time.Time, limit uint32) ([]gameruntime.DueTimer, error) {
	dueAt = dueAt.Round(0).UTC()
	if repository == nil || repository.runner == nil || ctx == nil || dueAt.IsZero() || limit == 0 || limit > 1024 {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	var result []gameruntime.DueTimer
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		rows, err := queries.ListDueGameSessionTimerCandidates(ctx, sqlcgen.ListDueGameSessionTimerCandidatesParams{
			DueAt: timeToPG(dueAt), BatchLimit: int32(limit),
		})
		if err != nil {
			return err
		}
		result = make([]gameruntime.DueTimer, len(rows))
		for index, row := range rows {
			if !row.SessionID.Valid || row.ExpectedStateVersion <= 0 || !row.DueAt.Valid || row.SchemaVersion <= 0 {
				return gameruntime.ErrGameSessionIntegrity
			}
			result[index] = gameruntime.DueTimer{
				SessionID: uuid.UUID(row.SessionID.Bytes), TimerID: game.Identifier(row.TimerID),
				ExpectedStateVersion: uint64(row.ExpectedStateVersion), DueAt: row.DueAt.Time,
				Message: game.Message{MessageType: game.Identifier(row.MessageType), SchemaVersion: uint32(row.SchemaVersion), Payload: bytes.Clone(row.Payload)},
			}
			if _, err := game.ParseIdentifier(row.TimerID); err != nil || !result[index].Message.Valid() {
				return gameruntime.ErrGameSessionIntegrity
			}
		}
		return nil
	})
	if err != nil {
		return nil, mapGameSessionRepositoryError(ctx, err, gameruntime.ErrSessionNotFound)
	}
	return result, nil
}

// ReadEventBatches restores deterministic replay input and ordered raw events for runtime-internal projection only.
func (repository *GameSessionRepository) ReadEventBatches(
	ctx context.Context,
	sessionID uuid.UUID,
	afterStateVersion uint64,
	limit uint32,
) ([]gameruntime.EventBatch, error) {
	if repository == nil || repository.runner == nil || ctx == nil || sessionID == uuid.Nil ||
		afterStateVersion > math.MaxInt64 || limit == 0 || limit > 1024 {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	var result []gameruntime.EventBatch
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		rows, err := queries.ListGameSessionEventBatchesAfter(ctx, sqlcgen.ListGameSessionEventBatchesAfterParams{
			SessionID: uuidToPG(sessionID), AfterStateVersion: int64(afterStateVersion), BatchLimit: int32(limit),
		})
		if err != nil {
			return err
		}
		result = make([]gameruntime.EventBatch, len(rows))
		for index, row := range rows {
			events, err := queries.ListGameSessionEvents(ctx, sqlcgen.ListGameSessionEventsParams{BatchID: row.BatchID})
			if err != nil {
				return err
			}
			result[index], err = eventBatchFromRows(row, events)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, mapGameSessionRepositoryError(ctx, err, gameruntime.ErrSessionNotFound)
	}
	return result, nil
}

func getGameSessionForUpdate(ctx context.Context, queries QueryHandle, sessionID uuid.UUID) (gameruntime.Session, error) {
	row, err := queries.GetGameSessionForUpdate(ctx, sqlcgen.GetGameSessionForUpdateParams{SessionID: uuidToPG(sessionID)})
	if err != nil {
		return gameruntime.Session{}, err
	}
	return sessionFromUpdatedRow(ctx, queries, row)
}

func sessionFromUpdatedRow(ctx context.Context, queries QueryHandle, row sqlcgen.GameSession) (gameruntime.Session, error) {
	participants, err := queries.ListGameSessionParticipants(ctx, sqlcgen.ListGameSessionParticipantsParams{SessionID: row.SessionID})
	if err != nil {
		return gameruntime.Session{}, err
	}
	timers, err := queries.ListGameSessionTimers(ctx, sqlcgen.ListGameSessionTimersParams{SessionID: row.SessionID})
	if err != nil {
		return gameruntime.Session{}, err
	}
	return sessionFromRows(row, participants, timers)
}

func validateCurrentTransition(current gameruntime.Session, before gameruntime.SessionSnapshot) error {
	snapshot := current.Snapshot()
	if snapshot.OwnershipEpoch != before.OwnershipEpoch {
		return gameruntime.ErrOwnershipLost
	}
	if snapshot.State.StateVersion != before.State.StateVersion {
		return gameruntime.ErrStateVersionConflict
	}
	if snapshot.Status == gameruntime.StatusSuspended {
		return gameruntime.ErrSessionSuspended
	}
	if snapshot.Status.Terminal() {
		return gameruntime.ErrSessionTerminal
	}
	return nil
}

func persistTimerTransition(ctx context.Context, queries QueryHandle, commit gameruntime.TimerCommit) (gameruntime.Session, error) {
	before, after := commit.Before().Snapshot(), commit.After().Snapshot()
	updatedRow, err := queries.UpdateGameSessionStateCAS(ctx, updateGameSessionStateParams(before, after))
	if err != nil {
		return gameruntime.Session{}, err
	}
	if err := replaceGameSessionTimers(ctx, queries, after.ID, after.Timers); err != nil {
		return gameruntime.Session{}, err
	}
	if err := insertGameSessionBatch(ctx, queries, commit.Batch()); err != nil {
		return gameruntime.Session{}, err
	}
	if err := insertGameTimerReceipt(ctx, queries, commit.Receipt(), commit.Batch().Snapshot().ID); err != nil {
		return gameruntime.Session{}, err
	}
	if err := insertGameSessionOutbox(ctx, queries, commit.OutboxEvents()); err != nil {
		return gameruntime.Session{}, err
	}
	return sessionFromUpdatedRow(ctx, queries, updatedRow)
}

func persistSystemTransition(ctx context.Context, queries QueryHandle, commit gameruntime.SystemCommit) (gameruntime.Session, error) {
	before, after := commit.Before().Snapshot(), commit.After().Snapshot()
	updatedRow, err := queries.UpdateGameSessionStateCAS(ctx, updateGameSessionStateParams(before, after))
	if err != nil {
		return gameruntime.Session{}, err
	}
	if err := replaceGameSessionTimers(ctx, queries, after.ID, after.Timers); err != nil {
		return gameruntime.Session{}, err
	}
	if err := insertGameSessionBatch(ctx, queries, commit.Batch()); err != nil {
		return gameruntime.Session{}, err
	}
	if err := insertGameSessionOutbox(ctx, queries, commit.OutboxEvents()); err != nil {
		return gameruntime.Session{}, err
	}
	if _, err := completeGameSystemOperation(ctx, queries, commit.Receipt(), commit.Batch().Snapshot().ID); err != nil {
		return gameruntime.Session{}, err
	}
	return sessionFromUpdatedRow(ctx, queries, updatedRow)
}

func insertGameTimerReceipt(ctx context.Context, queries QueryHandle, receipt gameruntime.TimerReceipt, batchID uuid.UUID) error {
	snapshot := receipt.Snapshot()
	_, err := queries.CreateGameTimerReceipt(ctx, sqlcgen.CreateGameTimerReceiptParams{
		SessionID: uuidToPG(snapshot.Key.SessionID), TimerID: string(snapshot.Key.TimerID),
		ExpectedStateVersion: int64(snapshot.Key.ExpectedStateVersion), ResultCode: string(snapshot.ResultCode),
		ResultDigest: snapshot.ResultDigest.Bytes(), CommittedStateVersion: int64(snapshot.StateVersion),
		BatchID: uuidToPG(batchID), CommittedAt: timeToPG(snapshot.CommittedAt),
	})
	return err
}

func timerReceiptFromRow(row sqlcgen.GameTimerReceipt) (gameruntime.TimerReceipt, error) {
	if !row.SessionID.Valid || row.ExpectedStateVersion <= 0 || row.CommittedStateVersion <= 0 || !row.CommittedAt.Valid || !row.BatchID.Valid {
		return gameruntime.TimerReceipt{}, gameruntime.ErrGameSessionIntegrity
	}
	resultDigest, err := idempotency.NewDigest(row.ResultDigest)
	if err != nil {
		return gameruntime.TimerReceipt{}, gameruntime.ErrGameSessionIntegrity
	}
	receipt, err := gameruntime.NewTimerReceipt(gameruntime.TimerReceiptSnapshot{
		Key: gameruntime.TimerKey{
			SessionID: uuid.UUID(row.SessionID.Bytes), TimerID: game.Identifier(row.TimerID),
			ExpectedStateVersion: uint64(row.ExpectedStateVersion),
		},
		ResultCode: gameruntime.ResultCode(row.ResultCode), ResultDigest: resultDigest,
		StateVersion: uint64(row.CommittedStateVersion), CommittedAt: row.CommittedAt.Time,
	})
	if err != nil {
		return gameruntime.TimerReceipt{}, gameruntime.ErrGameSessionIntegrity
	}
	return receipt, nil
}

func lockOrCreateSystemOperation(
	ctx context.Context,
	queries QueryHandle,
	receipt gameruntime.SystemReceiptSnapshot,
) (sqlcgen.GameSystemOperation, error) {
	params := sqlcgen.GetGameSystemOperationForUpdateParams{
		SessionID: uuidToPG(receipt.Key.SessionID), OperationID: receipt.Key.OperationID.Value(),
	}
	operation, err := queries.GetGameSystemOperationForUpdate(ctx, params)
	if err == nil {
		return validateSystemOperationBinding(operation, receipt.Key, receipt.RequestDigest)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return sqlcgen.GameSystemOperation{}, err
	}
	bySource, sourceErr := queries.GetGameSystemOperationBySourceForUpdate(ctx, sqlcgen.GetGameSystemOperationBySourceForUpdateParams{
		SessionID: uuidToPG(receipt.Key.SessionID), SourceEventID: uuidToPG(receipt.Key.Source.EventID),
	})
	if sourceErr == nil {
		if bySource.OperationID != receipt.Key.OperationID.Value() {
			return sqlcgen.GameSystemOperation{}, idempotency.ErrConflict
		}
		return validateSystemOperationBinding(bySource, receipt.Key, receipt.RequestDigest)
	}
	if !errors.Is(sourceErr, pgx.ErrNoRows) {
		return sqlcgen.GameSystemOperation{}, sourceErr
	}
	operation, err = queries.InsertGameSystemOperationPending(ctx, sqlcgen.InsertGameSystemOperationPendingParams{
		SessionID: uuidToPG(receipt.Key.SessionID), OperationID: receipt.Key.OperationID.Value(),
		SourceKind: string(receipt.Key.Source.Kind), SourceEventID: uuidToPG(receipt.Key.Source.EventID),
		RequestedByUserID: optionalUUIDToPG(receipt.Key.Source.RequestedByUserID),
		LogicalDigest:     receipt.RequestDigest.Bytes(), CreatedAt: timeToPG(receipt.CommittedAt),
	})
	if err != nil {
		return sqlcgen.GameSystemOperation{}, err
	}
	return validateSystemOperationBinding(operation, receipt.Key, receipt.RequestDigest)
}

func validateSystemOperationBinding(
	row sqlcgen.GameSystemOperation,
	key gameruntime.SystemKey,
	digest idempotency.Digest,
) (sqlcgen.GameSystemOperation, error) {
	if !row.SessionID.Valid || uuid.UUID(row.SessionID.Bytes) != key.SessionID || row.OperationID != key.OperationID.Value() ||
		row.SourceKind != string(key.Source.Kind) || !row.SourceEventID.Valid || uuid.UUID(row.SourceEventID.Bytes) != key.Source.EventID ||
		row.RequestedByUserID.Valid != (key.Source.RequestedByUserID != uuid.Nil) ||
		row.RequestedByUserID.Valid && uuid.UUID(row.RequestedByUserID.Bytes) != key.Source.RequestedByUserID {
		return sqlcgen.GameSystemOperation{}, idempotency.ErrConflict
	}
	storedDigest, err := idempotency.NewDigest(row.LogicalDigest)
	if err != nil {
		return sqlcgen.GameSystemOperation{}, gameruntime.ErrGameSessionIntegrity
	}
	if storedDigest != digest {
		return sqlcgen.GameSystemOperation{}, idempotency.ErrConflict
	}
	if row.Status != "pending" && row.Status != "completed" {
		return sqlcgen.GameSystemOperation{}, gameruntime.ErrGameSessionIntegrity
	}
	return row, nil
}

func completedSystemReceiptFromRow(
	row sqlcgen.GameSystemOperation,
	key gameruntime.SystemKey,
	digest idempotency.Digest,
) (gameruntime.SystemReceipt, error) {
	row, err := validateSystemOperationBinding(row, key, digest)
	if err != nil {
		return gameruntime.SystemReceipt{}, err
	}
	if row.Status != "completed" {
		return gameruntime.SystemReceipt{}, gameruntime.ErrSystemOperationPending
	}
	if !row.ResultCode.Valid || !row.CommittedStateVersion.Valid || row.CommittedStateVersion.Int64 <= 0 || !row.CompletedAt.Valid {
		return gameruntime.SystemReceipt{}, gameruntime.ErrGameSessionIntegrity
	}
	resultDigest, err := idempotency.NewDigest(row.ResultDigest)
	if err != nil {
		return gameruntime.SystemReceipt{}, gameruntime.ErrGameSessionIntegrity
	}
	receipt, err := gameruntime.NewSystemReceipt(gameruntime.SystemReceiptSnapshot{
		Key: key, RequestDigest: digest, ResultCode: gameruntime.ResultCode(row.ResultCode.String), ResultDigest: resultDigest,
		StateVersion: uint64(row.CommittedStateVersion.Int64), CommittedAt: row.CompletedAt.Time,
	})
	if err != nil {
		return gameruntime.SystemReceipt{}, gameruntime.ErrGameSessionIntegrity
	}
	return receipt, nil
}

func completeGameSystemOperation(
	ctx context.Context,
	queries QueryHandle,
	receipt gameruntime.SystemReceipt,
	batchID uuid.UUID,
) (sqlcgen.GameSystemOperation, error) {
	snapshot := receipt.Snapshot()
	return queries.CompleteGameSystemOperationCAS(ctx, sqlcgen.CompleteGameSystemOperationCASParams{
		ResultCode: pgtype.Text{String: string(snapshot.ResultCode), Valid: true}, ResultDigest: snapshot.ResultDigest.Bytes(),
		CommittedStateVersion: pgtype.Int8{Int64: int64(snapshot.StateVersion), Valid: true},
		BatchID:               optionalUUIDToPG(batchID), CompletedAt: timeToPG(snapshot.CommittedAt),
		SessionID: uuidToPG(snapshot.Key.SessionID), OperationID: snapshot.Key.OperationID.Value(),
	})
}

func completeTerminalSystemOperation(
	ctx context.Context,
	queries QueryHandle,
	key gameruntime.SystemKey,
	digest idempotency.Digest,
	session gameruntime.Session,
	completedAt time.Time,
) (gameruntime.SystemReceipt, error) {
	receipt, err := newTerminalSystemReceipt(key, digest, session, completedAt)
	if err != nil {
		return gameruntime.SystemReceipt{}, err
	}
	row, err := completeGameSystemOperation(ctx, queries, receipt, uuid.Nil)
	if err != nil {
		return gameruntime.SystemReceipt{}, err
	}
	return completedSystemReceiptFromRow(row, key, digest)
}

func newTerminalSystemReceipt(
	key gameruntime.SystemKey,
	digest idempotency.Digest,
	session gameruntime.Session,
	completedAt time.Time,
) (gameruntime.SystemReceipt, error) {
	snapshot := session.Snapshot()
	var versionBytes [8]byte
	binary.BigEndian.PutUint64(versionBytes[:], snapshot.State.StateVersion)
	hash := sha256.New()
	_, _ = hash.Write([]byte(gameruntime.ResultCodeNoopTerminal))
	_, _ = hash.Write(snapshot.ID[:])
	_, _ = hash.Write(versionBytes[:])
	resultDigest, err := idempotency.NewDigest(hash.Sum(nil))
	if err != nil {
		return gameruntime.SystemReceipt{}, err
	}
	receipt, err := gameruntime.NewSystemReceipt(gameruntime.SystemReceiptSnapshot{
		Key: key, RequestDigest: digest, ResultCode: gameruntime.ResultCodeNoopTerminal,
		ResultDigest: resultDigest, StateVersion: snapshot.State.StateVersion, CommittedAt: completedAt,
	})
	return receipt, err
}

// terminalSystemCompletionTime keeps a late no-op receipt chronologically after the transition that ended the session.
func terminalSystemCompletionTime(proposed, sessionUpdatedAt time.Time) time.Time {
	if !proposed.After(sessionUpdatedAt) {
		return sessionUpdatedAt.Add(time.Microsecond)
	}
	return proposed
}

func updateGameSessionLifecycleParams(before, after gameruntime.SessionSnapshot) sqlcgen.UpdateGameSessionLifecycleCASParams {
	return sqlcgen.UpdateGameSessionLifecycleCASParams{
		NextDeadlineAt: optionalTimeToPG(after.NextDeadlineAt), Status: string(after.Status),
		UpdatedAt: timeToPG(after.UpdatedAt), EndedAt: optionalTimeToPG(after.EndedAt), SessionID: uuidToPG(before.ID),
		ExpectedStateVersion: int64(before.State.StateVersion), ExpectedOwnershipEpoch: int64(before.OwnershipEpoch),
	}
}

func eventBatchFromRows(
	row sqlcgen.ListGameSessionEventBatchesAfterRow,
	eventRows []sqlcgen.GameSessionEvent,
) (gameruntime.EventBatch, error) {
	if !row.BatchID.Valid || !row.SessionID.Valid || row.StateVersion <= 0 || row.OwnershipEpoch < 0 ||
		!row.ExecutedAt.Valid || !row.CommittedAt.Valid || len(row.RandomSeed) != game.RandomSeedBytes ||
		row.InputSchemaVersion <= 0 || row.EventCount != int32(len(eventRows)) {
		return gameruntime.EventBatch{}, gameruntime.ErrGameSessionIntegrity
	}
	var seed [game.RandomSeedBytes]byte
	copy(seed[:], row.RandomSeed)
	allocated := make([]game.Identifier, len(row.AllocatedIds))
	for index, value := range row.AllocatedIds {
		identifier, err := game.ParseIdentifier(value)
		if err != nil {
			return gameruntime.EventBatch{}, gameruntime.ErrGameSessionIntegrity
		}
		allocated[index] = identifier
	}
	events := make([]game.Event, len(eventRows))
	for index, eventRow := range eventRows {
		if !eventRow.BatchID.Valid || uuid.UUID(eventRow.BatchID.Bytes) != uuid.UUID(row.BatchID.Bytes) ||
			eventRow.EventOrdinal != int32(index) || eventRow.SchemaVersion <= 0 {
			return gameruntime.EventBatch{}, gameruntime.ErrGameSessionIntegrity
		}
		events[index] = game.Event{Message: game.Message{
			MessageType: game.Identifier(eventRow.MessageType), SchemaVersion: uint32(eventRow.SchemaVersion), Payload: bytes.Clone(eventRow.Payload),
		}}
	}
	snapshot := gameruntime.EventBatchSnapshot{
		ID: uuid.UUID(row.BatchID.Bytes), SessionID: uuid.UUID(row.SessionID.Bytes),
		StateVersion: uint64(row.StateVersion), OwnershipEpoch: uint64(row.OwnershipEpoch),
		Cause: gameruntime.EventCause(row.Cause), ActorUserID: optionalUUIDFromPG(row.ActorUserID),
		Execution: game.DeterministicContext{Now: row.ExecutedAt.Time, RandomSeed: seed, AllocatedIDs: allocated},
		Input:     game.Message{MessageType: game.Identifier(row.InputMessageType), SchemaVersion: uint32(row.InputSchemaVersion), Payload: bytes.Clone(row.InputPayload)},
		Events:    events, CommittedAt: row.CommittedAt.Time,
	}
	if row.ActionID.Valid {
		snapshot.ActionID, _ = idempotency.ParseOperationID(row.ActionID.String)
	}
	if row.TimerID.Valid {
		snapshot.TimerID = game.Identifier(row.TimerID.String)
	}
	if row.SystemOperationID.Valid {
		snapshot.SystemOperationID, _ = idempotency.ParseOperationID(row.SystemOperationID.String)
	}
	if row.SystemSourceKind.Valid || row.SystemSourceEventID.Valid {
		snapshot.SystemSource = gameruntime.SystemSource{
			Kind: game.Identifier(row.SystemSourceKind.String), EventID: optionalUUIDFromPG(row.SystemSourceEventID),
			RequestedByUserID: optionalUUIDFromPG(row.SystemRequestedByUserID),
		}
	}
	if len(row.SystemRequestDigest) > 0 {
		snapshot.RequestDigest, _ = idempotency.NewDigest(row.SystemRequestDigest)
	}
	batch, err := gameruntime.RestoreEventBatch(snapshot)
	if err != nil {
		return gameruntime.EventBatch{}, gameruntime.ErrGameSessionIntegrity
	}
	return batch, nil
}

func lockActionParticipantFence(
	ctx context.Context,
	queries QueryHandle,
	session gameruntime.SessionSnapshot,
	actorUserID uuid.UUID,
) error {
	roomRow, err := queries.GetPartyRoomForUpdate(ctx, sqlcgen.GetPartyRoomForUpdateParams{RoomID: uuidToPG(session.RoomID)})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gameruntime.ErrSessionNotFound
		}
		return err
	}
	if !roomRow.RoomID.Valid || uuid.UUID(roomRow.RoomID.Bytes) != session.RoomID {
		return gameruntime.ErrGameSessionIntegrity
	}
	role, err := queries.GetRoomMemberRole(ctx, sqlcgen.GetRoomMemberRoleParams{
		RoomID: uuidToPG(session.RoomID), UserID: uuidToPG(actorUserID),
	})
	if errors.Is(err, pgx.ErrNoRows) || err == nil && role != string(roomDomain.MemberRoleParticipant) {
		return gameruntime.ErrParticipantNotActive
	}
	return err
}

// rejectTerminalWithPendingSystemInbox prevents normal finish from overtaking a committed membership revocation.
func rejectTerminalWithPendingSystemInbox(
	ctx context.Context,
	queries QueryHandle,
	after gameruntime.SessionSnapshot,
	excludedSourceEventID uuid.UUID,
) error {
	if !after.Status.Terminal() {
		return nil
	}
	pending, err := queries.HasPendingGameSystemInbox(ctx, sqlcgen.HasPendingGameSystemInboxParams{
		SessionID: uuidToPG(after.ID), ExcludedSourceEventID: optionalUUIDToPG(excludedSourceEventID),
	})
	if err != nil {
		return err
	}
	if pending {
		return gameruntime.ErrSystemOperationPending
	}
	return nil
}

func createGameSessionParams(snapshot gameruntime.SessionSnapshot) sqlcgen.CreateGameSessionParams {
	return sqlcgen.CreateGameSessionParams{
		SessionID: uuidToPG(snapshot.ID), RoomID: uuidToPG(snapshot.RoomID), GameID: string(snapshot.VersionKey.GameID),
		EngineVersion: string(snapshot.VersionKey.Engine), ProtocolVersion: string(snapshot.VersionKey.Protocol), ClientVersion: string(snapshot.VersionKey.Client),
		StateVersion: int64(snapshot.State.StateVersion), OwnershipEpoch: int64(snapshot.OwnershipEpoch), SnapshotVersion: int32(snapshot.State.SnapshotVersion),
		StateMessageType: string(snapshot.State.State.MessageType), StateSchemaVersion: int32(snapshot.State.State.SchemaVersion), StatePayload: requiredBytea(snapshot.State.State.Payload),
		NextDeadlineAt: optionalTimeToPG(snapshot.NextDeadlineAt), Status: string(snapshot.Status), StartedAt: timeToPG(snapshot.StartedAt),
		UpdatedAt: timeToPG(snapshot.UpdatedAt), EndedAt: optionalTimeToPG(snapshot.EndedAt),
	}
}

func updateGameSessionStateParams(before, after gameruntime.SessionSnapshot) sqlcgen.UpdateGameSessionStateCASParams {
	return sqlcgen.UpdateGameSessionStateCASParams{
		StateVersion: int64(after.State.StateVersion), SnapshotVersion: int32(after.State.SnapshotVersion),
		StateMessageType: string(after.State.State.MessageType), StateSchemaVersion: int32(after.State.State.SchemaVersion), StatePayload: requiredBytea(after.State.State.Payload),
		NextDeadlineAt: optionalTimeToPG(after.NextDeadlineAt), Status: string(after.Status), UpdatedAt: timeToPG(after.UpdatedAt), EndedAt: optionalTimeToPG(after.EndedAt),
		SessionID: uuidToPG(before.ID), ExpectedStateVersion: int64(before.State.StateVersion), ExpectedOwnershipEpoch: int64(before.OwnershipEpoch),
	}
}

func replaceGameSessionTimers(ctx context.Context, queries QueryHandle, sessionID uuid.UUID, timers []gameruntime.TimerSnapshot) error {
	if err := queries.DeleteGameSessionTimers(ctx, sqlcgen.DeleteGameSessionTimersParams{SessionID: uuidToPG(sessionID)}); err != nil {
		return err
	}
	for _, timer := range timers {
		if err := queries.CreateGameSessionTimer(ctx, sqlcgen.CreateGameSessionTimerParams{
			SessionID: uuidToPG(sessionID), TimerID: string(timer.TimerID), ExpectedStateVersion: int64(timer.ExpectedStateVersion),
			DueAt: timeToPG(timer.DueAt), MessageType: string(timer.Message.MessageType), SchemaVersion: int32(timer.Message.SchemaVersion), Payload: requiredBytea(timer.Message.Payload),
		}); err != nil {
			return err
		}
	}
	return nil
}

func insertGameSessionBatch(ctx context.Context, queries QueryHandle, batch gameruntime.EventBatch) error {
	snapshot := batch.Snapshot()
	if _, err := queries.CreateGameSessionEventBatch(ctx, sqlcgen.CreateGameSessionEventBatchParams{
		BatchID: uuidToPG(snapshot.ID), SessionID: uuidToPG(snapshot.SessionID), StateVersion: int64(snapshot.StateVersion), OwnershipEpoch: int64(snapshot.OwnershipEpoch),
		Cause: string(snapshot.Cause), ActorUserID: optionalUUIDToPG(snapshot.ActorUserID), ActionID: optionalOperationIDToPG(snapshot.ActionID),
		TimerID: optionalIdentifierToPG(snapshot.TimerID), SystemOperationID: optionalOperationIDToPG(snapshot.SystemOperationID),
		SystemSourceKind: optionalIdentifierToPG(snapshot.SystemSource.Kind), SystemSourceEventID: optionalUUIDToPG(snapshot.SystemSource.EventID),
		SystemRequestedByUserID: optionalUUIDToPG(snapshot.SystemSource.RequestedByUserID),
		SystemRequestDigest:     optionalDigestBytes(snapshot.RequestDigest, snapshot.Cause == gameruntime.EventCauseSystem),
		ExecutedAt:              timeToPG(snapshot.Execution.Now),
		RandomSeed:              snapshot.Execution.RandomSeed[:], AllocatedIds: identifierStrings(snapshot.Execution.AllocatedIDs), InputMessageType: string(snapshot.Input.MessageType),
		InputSchemaVersion: int32(snapshot.Input.SchemaVersion), InputPayload: requiredBytea(snapshot.Input.Payload), EventCount: int32(len(snapshot.Events)), CommittedAt: timeToPG(snapshot.CommittedAt),
	}); err != nil {
		return err
	}
	for index, event := range snapshot.Events {
		if err := queries.CreateGameSessionEvent(ctx, sqlcgen.CreateGameSessionEventParams{
			BatchID: uuidToPG(snapshot.ID), EventOrdinal: int32(index), MessageType: string(event.Message.MessageType),
			SchemaVersion: int32(event.Message.SchemaVersion), Payload: requiredBytea(event.Message.Payload),
		}); err != nil {
			return err
		}
	}
	return nil
}

func insertGameActionReceipt(ctx context.Context, queries QueryHandle, receipt gameruntime.ActionReceipt) error {
	snapshot := receipt.Snapshot()
	_, err := queries.CreateGameActionReceipt(ctx, sqlcgen.CreateGameActionReceiptParams{
		SessionID: uuidToPG(snapshot.Key.SessionID), ActorUserID: uuidToPG(snapshot.Key.ActorUserID), ActionID: snapshot.Key.ActionID.Value(),
		RequestDigest: snapshot.RequestDigest.Bytes(), ResultCode: string(snapshot.ResultCode), ResultDigest: snapshot.ResultDigest.Bytes(),
		CommittedStateVersion: int64(snapshot.StateVersion), CommittedAt: timeToPG(snapshot.CommittedAt),
	})
	return err
}

func insertGameSessionOutbox(ctx context.Context, queries QueryHandle, events []outbox.Event) error {
	repository := newOutboxEventRepository(queries)
	for _, event := range events {
		if _, err := repository.Insert(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

func sessionFromRows(row sqlcgen.GameSession, participants []sqlcgen.GameSessionParticipant, timers []sqlcgen.GameSessionTimer) (gameruntime.Session, error) {
	if !row.SessionID.Valid || !row.RoomID.Valid || row.StateVersion <= 0 || row.OwnershipEpoch < 0 || row.SnapshotVersion <= 0 ||
		row.StateSchemaVersion <= 0 || row.StateVersion > math.MaxInt64 || row.OwnershipEpoch > math.MaxInt64 ||
		row.SnapshotVersion > math.MaxInt32 || row.StateSchemaVersion > math.MaxInt32 || !row.StartedAt.Valid || !row.UpdatedAt.Valid {
		return gameruntime.Session{}, gameruntime.ErrGameSessionIntegrity
	}
	if len(participants) == 0 || len(participants) > int(game.MaximumParticipants) {
		return gameruntime.Session{}, gameruntime.ErrGameSessionIntegrity
	}
	mappedParticipants := make([]gameruntime.Participant, len(participants))
	for index, participant := range participants {
		if !participant.SessionID.Valid || uuid.UUID(participant.SessionID.Bytes) != uuid.UUID(row.SessionID.Bytes) || !participant.UserID.Valid || participant.SeatIndex < 0 {
			return gameruntime.Session{}, gameruntime.ErrGameSessionIntegrity
		}
		mappedParticipants[index] = gameruntime.Participant{UserID: uuid.UUID(participant.UserID.Bytes), SeatIndex: uint32(participant.SeatIndex)}
	}
	mappedTimers := make([]gameruntime.TimerSnapshot, len(timers))
	for index, timer := range timers {
		if !timer.SessionID.Valid || uuid.UUID(timer.SessionID.Bytes) != uuid.UUID(row.SessionID.Bytes) || timer.ExpectedStateVersion <= 0 ||
			timer.ExpectedStateVersion > math.MaxInt64 || timer.SchemaVersion <= 0 || timer.SchemaVersion > math.MaxInt32 || !timer.DueAt.Valid {
			return gameruntime.Session{}, gameruntime.ErrGameSessionIntegrity
		}
		mappedTimers[index] = gameruntime.TimerSnapshot{
			TimerID: game.Identifier(timer.TimerID), ExpectedStateVersion: uint64(timer.ExpectedStateVersion), DueAt: timer.DueAt.Time,
			Message: game.Message{MessageType: game.Identifier(timer.MessageType), SchemaVersion: uint32(timer.SchemaVersion), Payload: timer.Payload},
		}
	}
	return gameruntime.RestoreSession(gameruntime.SessionSnapshot{
		ID: uuid.UUID(row.SessionID.Bytes), RoomID: uuid.UUID(row.RoomID.Bytes),
		VersionKey:     game.VersionKey{GameID: game.GameID(row.GameID), Engine: game.Version(row.EngineVersion), Protocol: game.Version(row.ProtocolVersion), Client: game.Version(row.ClientVersion)},
		OwnershipEpoch: uint64(row.OwnershipEpoch), Participants: mappedParticipants,
		State:  game.Snapshot{SnapshotVersion: uint32(row.SnapshotVersion), StateVersion: uint64(row.StateVersion), State: game.Message{MessageType: game.Identifier(row.StateMessageType), SchemaVersion: uint32(row.StateSchemaVersion), Payload: row.StatePayload}},
		Timers: mappedTimers, NextDeadlineAt: optionalTimeFromPG(row.NextDeadlineAt), Status: gameruntime.Status(row.Status),
		StartedAt: row.StartedAt.Time, UpdatedAt: row.UpdatedAt.Time, EndedAt: optionalTimeFromPG(row.EndedAt),
	})
}

func actionReceiptFromRow(row sqlcgen.GameActionReceipt) (gameruntime.ActionReceipt, error) {
	if !row.SessionID.Valid || !row.ActorUserID.Valid || row.CommittedStateVersion <= 0 || !row.CommittedAt.Valid {
		return gameruntime.ActionReceipt{}, gameruntime.ErrGameSessionIntegrity
	}
	actionID, err := idempotency.ParseOperationID(row.ActionID)
	if err != nil {
		return gameruntime.ActionReceipt{}, gameruntime.ErrGameSessionIntegrity
	}
	requestDigest, err := idempotency.NewDigest(row.RequestDigest)
	if err != nil {
		return gameruntime.ActionReceipt{}, gameruntime.ErrGameSessionIntegrity
	}
	resultDigest, err := idempotency.NewDigest(row.ResultDigest)
	if err != nil {
		return gameruntime.ActionReceipt{}, gameruntime.ErrGameSessionIntegrity
	}
	receipt, err := gameruntime.NewActionReceipt(gameruntime.ActionReceiptSnapshot{
		Key:           gameruntime.ActionKey{SessionID: uuid.UUID(row.SessionID.Bytes), ActorUserID: uuid.UUID(row.ActorUserID.Bytes), ActionID: actionID},
		RequestDigest: requestDigest, ResultCode: gameruntime.ResultCode(row.ResultCode), ResultDigest: resultDigest,
		StateVersion: uint64(row.CommittedStateVersion), CommittedAt: row.CommittedAt.Time,
	})
	if err != nil {
		return gameruntime.ActionReceipt{}, gameruntime.ErrGameSessionIntegrity
	}
	return receipt, nil
}

func validateCreationWidths(commit gameruntime.CreationCommit) error {
	snapshot := commit.Session.Snapshot()
	if snapshot.State.StateVersion > math.MaxInt64 || snapshot.OwnershipEpoch > math.MaxInt64 || snapshot.State.SnapshotVersion > math.MaxInt32 || snapshot.State.State.SchemaVersion > math.MaxInt32 {
		return gameruntime.ErrInvalidSessionInput
	}
	for _, participant := range snapshot.Participants {
		if participant.SeatIndex > math.MaxInt32 {
			return gameruntime.ErrInvalidSessionInput
		}
	}
	for _, timer := range snapshot.Timers {
		if timer.ExpectedStateVersion > math.MaxInt64 || timer.Message.SchemaVersion > math.MaxInt32 {
			return gameruntime.ErrInvalidSessionInput
		}
	}
	return nil
}

func validateActionWidths(commit gameruntime.ActionCommit) error {
	for _, snapshot := range []gameruntime.SessionSnapshot{commit.Before().Snapshot(), commit.After().Snapshot()} {
		if snapshot.State.StateVersion > math.MaxInt64 || snapshot.OwnershipEpoch > math.MaxInt64 ||
			snapshot.State.SnapshotVersion > math.MaxInt32 || snapshot.State.State.SchemaVersion > math.MaxInt32 {
			return gameruntime.ErrInvalidActionCommit
		}
		for _, timer := range snapshot.Timers {
			if timer.ExpectedStateVersion > math.MaxInt64 || timer.Message.SchemaVersion > math.MaxInt32 {
				return gameruntime.ErrInvalidActionCommit
			}
		}
	}
	batch := commit.Batch().Snapshot()
	if batch.StateVersion > math.MaxInt64 {
		return gameruntime.ErrInvalidActionCommit
	}
	for _, event := range batch.Events {
		if event.Message.SchemaVersion > math.MaxInt32 {
			return gameruntime.ErrInvalidActionCommit
		}
	}
	if commit.Receipt().Snapshot().StateVersion > math.MaxInt64 {
		return gameruntime.ErrInvalidActionCommit
	}
	return nil
}

func validateTimerCommitWidths(commit gameruntime.TimerCommit) error {
	if err := validateTransitionWidths(commit.Before().Snapshot(), commit.After().Snapshot(), commit.Batch().Snapshot()); err != nil {
		return gameruntime.ErrInvalidTimerCommit
	}
	receipt := commit.Receipt().Snapshot()
	if receipt.Key.ExpectedStateVersion > math.MaxInt64 || receipt.StateVersion > math.MaxInt64 {
		return gameruntime.ErrInvalidTimerCommit
	}
	return nil
}

func validateSystemCommitWidths(commit gameruntime.SystemCommit) error {
	if err := validateTransitionWidths(commit.Before().Snapshot(), commit.After().Snapshot(), commit.Batch().Snapshot()); err != nil {
		return gameruntime.ErrInvalidSystemCommit
	}
	if commit.Receipt().Snapshot().StateVersion > math.MaxInt64 {
		return gameruntime.ErrInvalidSystemCommit
	}
	return nil
}

func validateTransitionWidths(before, after gameruntime.SessionSnapshot, batch gameruntime.EventBatchSnapshot) error {
	for _, snapshot := range []gameruntime.SessionSnapshot{before, after} {
		if snapshot.State.StateVersion > math.MaxInt64 || snapshot.OwnershipEpoch > math.MaxInt64 ||
			snapshot.State.SnapshotVersion > math.MaxInt32 || snapshot.State.State.SchemaVersion > math.MaxInt32 {
			return gameruntime.ErrInvalidSessionInput
		}
		for _, timer := range snapshot.Timers {
			if timer.ExpectedStateVersion > math.MaxInt64 || timer.Message.SchemaVersion > math.MaxInt32 {
				return gameruntime.ErrInvalidSessionInput
			}
		}
	}
	if batch.StateVersion > math.MaxInt64 || batch.Input.SchemaVersion > math.MaxInt32 {
		return gameruntime.ErrInvalidSessionInput
	}
	for _, event := range batch.Events {
		if event.Message.SchemaVersion > math.MaxInt32 {
			return gameruntime.ErrInvalidSessionInput
		}
	}
	return nil
}

func validateOwnershipTransition(before, next gameruntime.SessionSnapshot) error {
	expected := before
	expected.OwnershipEpoch++
	expected.UpdatedAt = next.UpdatedAt
	if before.ID == uuid.Nil || before.ID != next.ID || before.State.StateVersion != next.State.StateVersion ||
		before.State.StateVersion > math.MaxInt64 || before.OwnershipEpoch > math.MaxInt64 || next.OwnershipEpoch > math.MaxInt64 ||
		before.OwnershipEpoch == math.MaxUint64 || next.OwnershipEpoch != before.OwnershipEpoch+1 ||
		!before.StartedAt.Equal(next.StartedAt) || !sameGameSnapshotState(before.State, next.State) ||
		!next.UpdatedAt.After(before.UpdatedAt) || !reflect.DeepEqual(expected, next) {
		return gameruntime.ErrInvalidSessionInput
	}
	return nil
}

func mapGameSessionRepositoryError(ctx context.Context, err, noRowsError error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	for _, domainErr := range []error{
		gameruntime.ErrInvalidSessionInput, gameruntime.ErrSessionNotFound, gameruntime.ErrSessionAlreadyExists,
		gameruntime.ErrStateVersionConflict, gameruntime.ErrOwnershipLost, gameruntime.ErrSessionSuspended,
		gameruntime.ErrSessionTerminal, gameruntime.ErrParticipantNotActive, gameruntime.ErrTimerNotDue,
		gameruntime.ErrTimerNotFound, gameruntime.ErrActionReceiptNotFound, gameruntime.ErrTimerReceiptNotFound,
		gameruntime.ErrSystemReceiptNotFound, gameruntime.ErrSystemOperationPending,
		gameruntime.ErrInvalidActionCommit, gameruntime.ErrInvalidTimerCommit, gameruntime.ErrInvalidSystemCommit,
		gameruntime.ErrInvalidLifecycleCommit, gameruntime.ErrGameSessionIntegrity, idempotency.ErrConflict,
	} {
		if errors.Is(err, domainErr) {
			return domainErr
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return noRowsError
	}
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		switch pgError.Code {
		case "23505":
			return gameruntime.ErrSessionAlreadyExists
		case "23502", "23503", "23514", "22P02":
			return gameruntime.ErrGameSessionIntegrity
		case "40001", "40P01":
			return gameruntime.ErrStateVersionConflict
		}
	}
	return gameruntime.ErrGameSessionRepositoryUnavailable
}

func optionalOperationIDToPG(value idempotency.OperationID) pgtype.Text {
	if !value.Valid() {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value.Value(), Valid: true}
}

func optionalIdentifierToPG(value game.Identifier) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: string(value), Valid: true}
}

func optionalDigestBytes(value idempotency.Digest, present bool) []byte {
	if !present {
		return nil
	}
	return value.Bytes()
}

// requiredBytea preserves the protocol's empty-byte-string value instead of letting pgx encode a nil slice as SQL NULL.
func requiredBytea(value []byte) []byte {
	if value == nil {
		return []byte{}
	}
	return value
}

func optionalTimeToPG(value time.Time) pgtype.Timestamptz {
	if value.IsZero() {
		return pgtype.Timestamptz{}
	}
	return timeToPG(value)
}

func sameGameSnapshotState(left, right game.Snapshot) bool {
	return left.SnapshotVersion == right.SnapshotVersion && left.StateVersion == right.StateVersion &&
		left.State.MessageType == right.State.MessageType && left.State.SchemaVersion == right.State.SchemaVersion &&
		bytes.Equal(left.State.Payload, right.State.Payload)
}

func optionalTimeFromPG(value pgtype.Timestamptz) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time
}

func identifierStrings(values []game.Identifier) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}

var _ gameruntime.Store = (*GameSessionRepository)(nil)
