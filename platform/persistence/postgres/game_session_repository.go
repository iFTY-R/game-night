package postgres

import (
	"bytes"
	"context"
	"errors"
	"math"
	"reflect"
	"time"

	"github.com/google/uuid"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
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
		after := commit.After().Snapshot()
		row, err := queries.GetGameSessionForUpdate(ctx, sqlcgen.GetGameSessionForUpdateParams{SessionID: uuidToPG(before.ID)})
		if err != nil {
			return err
		}
		participants, err := queries.ListGameSessionParticipants(ctx, sqlcgen.ListGameSessionParticipantsParams{SessionID: uuidToPG(before.ID)})
		if err != nil {
			return err
		}
		timers, err := queries.ListGameSessionTimers(ctx, sqlcgen.ListGameSessionTimersParams{SessionID: uuidToPG(before.ID)})
		if err != nil {
			return err
		}
		current, err := sessionFromRows(row, participants, timers)
		if err != nil {
			return err
		}
		receiptRow, receiptErr := queries.GetGameActionReceipt(ctx, sqlcgen.GetGameActionReceiptParams{
			SessionID: uuidToPG(before.ID), ActorUserID: uuidToPG(commit.Receipt().Snapshot().Key.ActorUserID),
			ActionID: commit.Receipt().Snapshot().Key.ActionID.Value(),
		})
		if receiptErr == nil {
			receipt, err := actionReceiptFromRow(receiptRow)
			if err != nil {
				return err
			}
			if _, err := receipt.Replay(commit.Receipt().Snapshot().RequestDigest); err != nil {
				return err
			}
			result = gameruntime.ActionCommitResult{Session: current, Receipt: receipt, Replayed: true}
			return nil
		}
		if !errors.Is(receiptErr, pgx.ErrNoRows) {
			return receiptErr
		}
		if current.Snapshot().OwnershipEpoch != before.OwnershipEpoch {
			return gameruntime.ErrOwnershipLost
		}
		if current.Snapshot().State.StateVersion != before.State.StateVersion {
			return gameruntime.ErrStateVersionConflict
		}
		if current.Snapshot().Status == gameruntime.StatusSuspended {
			return gameruntime.ErrSessionSuspended
		}
		if current.Snapshot().Status.Terminal() {
			return gameruntime.ErrSessionTerminal
		}
		updatedRow, err := queries.UpdateGameSessionStateCAS(ctx, updateGameSessionStateParams(before, after))
		if err != nil {
			return err
		}
		if err := replaceGameSessionTimers(ctx, queries, after.ID, after.Timers); err != nil {
			return err
		}
		if err := insertGameSessionBatch(ctx, queries, commit.Batch()); err != nil {
			return err
		}
		if err := insertGameActionReceipt(ctx, queries, commit.Receipt()); err != nil {
			return err
		}
		if err := insertGameSessionOutbox(ctx, queries, commit.OutboxEvents()); err != nil {
			return err
		}
		storedTimers, err := queries.ListGameSessionTimers(ctx, sqlcgen.ListGameSessionTimersParams{SessionID: uuidToPG(after.ID)})
		if err != nil {
			return err
		}
		storedParticipants, err := queries.ListGameSessionParticipants(ctx, sqlcgen.ListGameSessionParticipantsParams{SessionID: uuidToPG(after.ID)})
		if err != nil {
			return err
		}
		stored, err := sessionFromRows(updatedRow, storedParticipants, storedTimers)
		if err != nil {
			return err
		}
		result = gameruntime.ActionCommitResult{Session: stored, Receipt: commit.Receipt()}
		return nil
	})
	if err != nil {
		return gameruntime.ActionCommitResult{}, mapGameSessionRepositoryError(ctx, err, gameruntime.ErrSessionNotFound)
	}
	return result, nil
}

func createGameSessionParams(snapshot gameruntime.SessionSnapshot) sqlcgen.CreateGameSessionParams {
	return sqlcgen.CreateGameSessionParams{
		SessionID: uuidToPG(snapshot.ID), RoomID: uuidToPG(snapshot.RoomID), GameID: string(snapshot.VersionKey.GameID),
		EngineVersion: string(snapshot.VersionKey.Engine), ProtocolVersion: string(snapshot.VersionKey.Protocol), ClientVersion: string(snapshot.VersionKey.Client),
		StateVersion: int64(snapshot.State.StateVersion), OwnershipEpoch: int64(snapshot.OwnershipEpoch), SnapshotVersion: int32(snapshot.State.SnapshotVersion),
		StateMessageType: string(snapshot.State.State.MessageType), StateSchemaVersion: int32(snapshot.State.State.SchemaVersion), StatePayload: snapshot.State.State.Payload,
		NextDeadlineAt: optionalTimeToPG(snapshot.NextDeadlineAt), Status: string(snapshot.Status), StartedAt: timeToPG(snapshot.StartedAt),
		UpdatedAt: timeToPG(snapshot.UpdatedAt), EndedAt: optionalTimeToPG(snapshot.EndedAt),
	}
}

func updateGameSessionStateParams(before, after gameruntime.SessionSnapshot) sqlcgen.UpdateGameSessionStateCASParams {
	return sqlcgen.UpdateGameSessionStateCASParams{
		StateVersion: int64(after.State.StateVersion), SnapshotVersion: int32(after.State.SnapshotVersion),
		StateMessageType: string(after.State.State.MessageType), StateSchemaVersion: int32(after.State.State.SchemaVersion), StatePayload: after.State.State.Payload,
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
			DueAt: timeToPG(timer.DueAt), MessageType: string(timer.Message.MessageType), SchemaVersion: int32(timer.Message.SchemaVersion), Payload: timer.Message.Payload,
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
		Cause: string(snapshot.Cause), ActorUserID: optionalUUIDToPG(snapshot.ActorUserID), ActionID: optionalOperationIDToPG(snapshot.ActionID), ExecutedAt: timeToPG(snapshot.Execution.Now),
		RandomSeed: snapshot.Execution.RandomSeed[:], AllocatedIds: identifierStrings(snapshot.Execution.AllocatedIDs), InputMessageType: string(snapshot.Input.MessageType),
		InputSchemaVersion: int32(snapshot.Input.SchemaVersion), InputPayload: snapshot.Input.Payload, EventCount: int32(len(snapshot.Events)), CommittedAt: timeToPG(snapshot.CommittedAt),
	}); err != nil {
		return err
	}
	for index, event := range snapshot.Events {
		if err := queries.CreateGameSessionEvent(ctx, sqlcgen.CreateGameSessionEventParams{
			BatchID: uuidToPG(snapshot.ID), EventOrdinal: int32(index), MessageType: string(event.Message.MessageType),
			SchemaVersion: int32(event.Message.SchemaVersion), Payload: event.Message.Payload,
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
		gameruntime.ErrSessionTerminal, gameruntime.ErrActionReceiptNotFound, gameruntime.ErrInvalidActionCommit,
		gameruntime.ErrGameSessionIntegrity, idempotency.ErrConflict,
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
		case "23503", "23514", "22P02":
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
