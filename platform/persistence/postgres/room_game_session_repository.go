package postgres

import (
	"context"
	"errors"
	"reflect"

	"github.com/google/uuid"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RoomGameSessionRepository publishes a PartyRoom start and its first GameSession in one transaction.
// The caller prepares both domain snapshots and the deterministic runtime creation commit before entering it.
type RoomGameSessionRepository struct {
	runner *TransactionRunner
}

// NewRoomGameSessionRepository binds cross-aggregate game starts to the supplied PostgreSQL pool.
func NewRoomGameSessionRepository(pool *pgxpool.Pool) *RoomGameSessionRepository {
	return &RoomGameSessionRepository{runner: NewTransactionRunner(pool)}
}

// GetStartReceipt checks PostgreSQL before module execution so ordinary network retries remain side-effect free.
func (repository *RoomGameSessionRepository) GetStartReceipt(
	ctx context.Context,
	key gameruntime.StartKey,
	requestDigest idempotency.Digest,
) (gameruntime.StartReceipt, error) {
	if repository == nil || repository.runner == nil || ctx == nil || !key.Valid() {
		return gameruntime.StartReceipt{}, gameruntime.ErrInvalidSessionInput
	}
	var receipt gameruntime.StartReceipt
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		row, err := queries.GetGameSessionStartReceipt(ctx, startReceiptKeyParams(key))
		if err != nil {
			return err
		}
		receipt, err = startReceiptFromRow(row)
		if err != nil {
			return err
		}
		_, err = receipt.Replay(requestDigest)
		return err
	})
	if err != nil {
		return gameruntime.StartReceipt{}, mapGameSessionRepositoryError(ctx, err, gameruntime.ErrStartReceiptNotFound)
	}
	return receipt, nil
}

// Start atomically locks the room, commits its CAS transition, creates the session children, and inserts outbox events.
// A failure after either aggregate write rolls back both aggregates and leaves the room in its previous state.
func (repository *RoomGameSessionRepository) Start(
	ctx context.Context,
	before roomDomain.Room,
	after roomDomain.Room,
	commit gameruntime.CreationCommit,
	receipt gameruntime.StartReceipt,
) (roomDomain.Room, gameruntime.Session, bool, error) {
	if repository == nil || repository.runner == nil || ctx == nil {
		return roomDomain.Room{}, gameruntime.Session{}, false, roomDomain.ErrInvalidRoomInput
	}
	if err := validateRoomTransition(before.Snapshot(), after.Snapshot()); err != nil {
		return roomDomain.Room{}, gameruntime.Session{}, false, err
	}
	if !commit.Valid() {
		return roomDomain.Room{}, gameruntime.Session{}, false, gameruntime.ErrInvalidSessionInput
	}
	if err := validateCreationWidths(commit); err != nil {
		return roomDomain.Room{}, gameruntime.Session{}, false, err
	}
	if err := validateRoomGameSessionStart(before, after, commit); err != nil {
		return roomDomain.Room{}, gameruntime.Session{}, false, err
	}
	receiptSnapshot := receipt.Snapshot()
	beforeSnapshot := before.Snapshot()
	if !receiptSnapshot.Valid() || receiptSnapshot.Key.RoomID != beforeSnapshot.ID ||
		receiptSnapshot.Key.ActorUserID != beforeSnapshot.HostUserID ||
		receiptSnapshot.SessionID != commit.Session.Snapshot().ID ||
		!receiptSnapshot.CommittedAt.Equal(commit.Session.Snapshot().StartedAt) {
		return roomDomain.Room{}, gameruntime.Session{}, false, gameruntime.ErrInvalidSessionInput
	}

	var storedRoom roomDomain.Room
	var storedSession gameruntime.Session
	var replayed bool
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		lockedRow, err := queries.GetPartyRoomForUpdate(ctx, sqlcgen.GetPartyRoomForUpdateParams{RoomID: uuidToPG(beforeSnapshot.ID)})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return roomDomain.ErrRoomNotFound
			}
			return err
		}
		lockedMembers, err := queries.ListRoomMembers(ctx, sqlcgen.ListRoomMembersParams{RoomID: uuidToPG(beforeSnapshot.ID)})
		if err != nil {
			return err
		}
		lockedRoom, err := roomFromRows(lockedRow, lockedMembers)
		if err != nil {
			return err
		}
		lockedSnapshot := lockedRoom.Snapshot()
		existingRow, receiptErr := queries.GetGameSessionStartReceiptForUpdate(ctx, startReceiptForUpdateParams(receiptSnapshot.Key))
		if receiptErr == nil {
			existing, parseErr := startReceiptFromRow(existingRow)
			if parseErr != nil {
				return parseErr
			}
			if _, replayErr := existing.Replay(receiptSnapshot.RequestDigest); replayErr != nil {
				return replayErr
			}
			storedSession, err = getGameSessionForUpdate(ctx, queries, existing.Snapshot().SessionID)
			if err != nil {
				return err
			}
			storedRoom, replayed = lockedRoom, true
			return nil
		}
		if !errors.Is(receiptErr, pgx.ErrNoRows) {
			return receiptErr
		}
		if !sameRoomSnapshot(lockedSnapshot, beforeSnapshot) {
			return roomDomain.ErrRoomVersionConflict
		}

		storedRoom, err = updateRoomAggregateCAS(ctx, queries, beforeSnapshot, after.Snapshot())
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return roomDomain.ErrRoomVersionConflict
			}
			return err
		}
		storedSession, err = createGameSessionAggregate(ctx, queries, commit)
		if err != nil {
			return err
		}
		createdReceipt, err := queries.CreateGameSessionStartReceipt(ctx, startReceiptCreateParams(receiptSnapshot))
		if err != nil {
			return err
		}
		persistedReceipt, err := startReceiptFromRow(createdReceipt)
		if err != nil {
			return err
		}
		if _, err := persistedReceipt.Replay(receiptSnapshot.RequestDigest); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return roomDomain.Room{}, gameruntime.Session{}, false, mapRoomGameSessionStartError(ctx, err)
	}
	return storedRoom, storedSession, replayed, nil
}

func startReceiptKeyParams(key gameruntime.StartKey) sqlcgen.GetGameSessionStartReceiptParams {
	return sqlcgen.GetGameSessionStartReceiptParams{
		ActorUserID: uuidToPG(key.ActorUserID), RoomID: uuidToPG(key.RoomID), OperationID: key.OperationID.Value(),
	}
}

func startReceiptForUpdateParams(key gameruntime.StartKey) sqlcgen.GetGameSessionStartReceiptForUpdateParams {
	return sqlcgen.GetGameSessionStartReceiptForUpdateParams{
		ActorUserID: uuidToPG(key.ActorUserID), RoomID: uuidToPG(key.RoomID), OperationID: key.OperationID.Value(),
	}
}

func startReceiptCreateParams(snapshot gameruntime.StartReceiptSnapshot) sqlcgen.CreateGameSessionStartReceiptParams {
	return sqlcgen.CreateGameSessionStartReceiptParams{
		ActorUserID: uuidToPG(snapshot.Key.ActorUserID), RoomID: uuidToPG(snapshot.Key.RoomID),
		OperationID: snapshot.Key.OperationID.Value(), RequestDigest: snapshot.RequestDigest.Bytes(),
		SessionID: uuidToPG(snapshot.SessionID), CommittedAt: timeToPG(snapshot.CommittedAt),
	}
}

func startReceiptFromRow(row sqlcgen.GameSessionStartReceipt) (gameruntime.StartReceipt, error) {
	if !row.ActorUserID.Valid || !row.RoomID.Valid || !row.SessionID.Valid || !row.CommittedAt.Valid {
		return gameruntime.StartReceipt{}, gameruntime.ErrGameSessionIntegrity
	}
	operationID, err := idempotency.ParseOperationID(row.OperationID)
	if err != nil {
		return gameruntime.StartReceipt{}, gameruntime.ErrGameSessionIntegrity
	}
	requestDigest, err := idempotency.NewDigest(row.RequestDigest)
	if err != nil || !row.CommittedAt.Valid {
		return gameruntime.StartReceipt{}, gameruntime.ErrGameSessionIntegrity
	}
	receipt, err := gameruntime.NewStartReceipt(gameruntime.StartReceiptSnapshot{
		Key: gameruntime.StartKey{
			ActorUserID: uuid.UUID(row.ActorUserID.Bytes), RoomID: uuid.UUID(row.RoomID.Bytes), OperationID: operationID,
		},
		RequestDigest: requestDigest, SessionID: uuid.UUID(row.SessionID.Bytes), CommittedAt: row.CommittedAt.Time.Round(0).UTC(),
	})
	if err != nil {
		return gameruntime.StartReceipt{}, gameruntime.ErrGameSessionIntegrity
	}
	return receipt, nil
}

// FinishAction atomically commits a naturally terminal player transition and returns the room to its post-game lobby.
func (repository *RoomGameSessionRepository) FinishAction(
	ctx context.Context,
	before roomDomain.Room,
	after roomDomain.Room,
	commit gameruntime.ActionCommit,
) (roomDomain.Room, gameruntime.ActionCommitResult, error) {
	if repository == nil || repository.runner == nil || ctx == nil || !commit.Valid() {
		return roomDomain.Room{}, gameruntime.ActionCommitResult{}, gameruntime.ErrInvalidActionCommit
	}
	if err := validateRoomGameSessionTermination(before, after, commit.Before(), commit.After(), gameruntime.StatusFinished); err != nil {
		return roomDomain.Room{}, gameruntime.ActionCommitResult{}, err
	}
	var storedRoom roomDomain.Room
	var result gameruntime.ActionCommitResult
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		locked, err := lockRoomForUpdate(ctx, queries, before.Snapshot().ID)
		if err != nil {
			return err
		}
		actorUserID := commit.Receipt().Snapshot().Key.ActorUserID
		role, err := queries.GetRoomMemberRole(ctx, sqlcgen.GetRoomMemberRoleParams{
			RoomID: uuidToPG(before.Snapshot().ID), UserID: uuidToPG(actorUserID),
		})
		if errors.Is(err, pgx.ErrNoRows) || err == nil && role != string(roomDomain.MemberRoleParticipant) {
			return gameruntime.ErrParticipantNotActive
		}
		if err != nil {
			return err
		}
		if !sameRoomSnapshot(locked.Snapshot(), before.Snapshot()) {
			result, err = replayFinishedActionAfterRoomChange(ctx, queries, commit)
			storedRoom = locked
			return err
		}
		result, err = commitActionAfterRoomLock(ctx, queries, commit)
		if err != nil {
			return err
		}
		if result.Replayed || result.Session.Snapshot().Status != gameruntime.StatusFinished {
			return gameruntime.ErrInvalidActionCommit
		}
		storedRoom, err = finishPartyRoomAggregateCAS(ctx, queries, before.Snapshot(), after.Snapshot())
		return err
	})
	if err != nil {
		return roomDomain.Room{}, gameruntime.ActionCommitResult{}, mapRoomGameSessionStartError(ctx, err)
	}
	return storedRoom, result, nil
}

// FinishTimer atomically commits a terminal due-timer transition and clears the matching room pointer.
func (repository *RoomGameSessionRepository) FinishTimer(
	ctx context.Context,
	before roomDomain.Room,
	after roomDomain.Room,
	commit gameruntime.TimerCommit,
) (roomDomain.Room, gameruntime.TimerCommitResult, error) {
	if repository == nil || repository.runner == nil || ctx == nil || !commit.Valid() {
		return roomDomain.Room{}, gameruntime.TimerCommitResult{}, gameruntime.ErrInvalidTimerCommit
	}
	if err := validateRoomGameSessionTermination(before, after, commit.Before(), commit.After(), gameruntime.StatusFinished); err != nil {
		return roomDomain.Room{}, gameruntime.TimerCommitResult{}, err
	}
	var storedRoom roomDomain.Room
	var result gameruntime.TimerCommitResult
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		locked, err := lockRoomForUpdate(ctx, queries, before.Snapshot().ID)
		if err != nil {
			return err
		}
		current, err := getGameSessionForUpdate(ctx, queries, commit.Before().Snapshot().ID)
		if err != nil {
			return err
		}
		if !sameRoomSnapshot(locked.Snapshot(), before.Snapshot()) {
			result, err = replayFinishedTimerAfterRoomChange(ctx, queries, current, commit)
			storedRoom = locked
			return err
		}
		result, err = commitTimerAfterSessionLock(ctx, queries, current, commit)
		if err != nil {
			return err
		}
		if result.Replayed || result.Session.Snapshot().Status != gameruntime.StatusFinished {
			return gameruntime.ErrInvalidTimerCommit
		}
		storedRoom, err = finishPartyRoomAggregateCAS(ctx, queries, before.Snapshot(), after.Snapshot())
		return err
	})
	if err != nil {
		return roomDomain.Room{}, gameruntime.TimerCommitResult{}, mapRoomGameSessionStartError(ctx, err)
	}
	return storedRoom, result, nil
}

// FinishSystem applies a current-host system transition and clears the room pointer in the same transaction.
func (repository *RoomGameSessionRepository) FinishSystem(
	ctx context.Context,
	before roomDomain.Room,
	after roomDomain.Room,
	actorUserID uuid.UUID,
	commit gameruntime.SystemCommit,
) (roomDomain.Room, gameruntime.SystemCommitResult, error) {
	if repository == nil || repository.runner == nil || ctx == nil || !commit.Valid() {
		return roomDomain.Room{}, gameruntime.SystemCommitResult{}, gameruntime.ErrInvalidSystemCommit
	}
	source := commit.Receipt().Snapshot().Key.Source
	if source.Kind == gameruntime.SystemSourceHostAPI &&
		(actorUserID == uuid.Nil || source.RequestedByUserID != actorUserID) {
		return roomDomain.Room{}, gameruntime.SystemCommitResult{}, roomDomain.ErrHostRequired
	}
	if source.Kind != gameruntime.SystemSourceHostAPI &&
		(actorUserID != uuid.Nil || source.RequestedByUserID != uuid.Nil) {
		return roomDomain.Room{}, gameruntime.SystemCommitResult{}, roomDomain.ErrHostRequired
	}
	if err := validateRoomGameSessionTermination(before, after, commit.Before(), commit.After(), gameruntime.StatusFinished); err != nil {
		return roomDomain.Room{}, gameruntime.SystemCommitResult{}, err
	}
	var storedRoom roomDomain.Room
	var result gameruntime.SystemCommitResult
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		locked, err := lockRoomForUpdate(ctx, queries, before.Snapshot().ID)
		if err != nil {
			return err
		}
		if source.Kind == gameruntime.SystemSourceHostAPI && locked.Snapshot().HostUserID != actorUserID {
			return roomDomain.ErrHostRequired
		}
		current, err := getGameSessionForUpdate(ctx, queries, commit.Before().Snapshot().ID)
		if err != nil {
			return err
		}
		if !sameRoomSnapshot(locked.Snapshot(), before.Snapshot()) {
			result, err = replayFinishedSystemAfterRoomChange(ctx, queries, current, commit)
			storedRoom = locked
			return err
		}
		result, err = commitSystemAfterSessionLock(ctx, queries, current, commit)
		if err != nil {
			return err
		}
		if result.Retry {
			storedRoom = locked
			return nil
		}
		if result.Replayed || result.Session.Snapshot().Status != gameruntime.StatusFinished {
			return gameruntime.ErrInvalidSystemCommit
		}
		storedRoom, err = finishPartyRoomAggregateCAS(ctx, queries, before.Snapshot(), after.Snapshot())
		return err
	})
	if err != nil {
		return roomDomain.Room{}, gameruntime.SystemCommitResult{}, mapRoomGameSessionStartError(ctx, err)
	}
	return storedRoom, result, nil
}

// Cancel atomically applies a runtime-native cancellation and clears the room pointer without a normal game result.
func (repository *RoomGameSessionRepository) Cancel(
	ctx context.Context,
	before roomDomain.Room,
	after roomDomain.Room,
	commit gameruntime.LifecycleCommit,
) (roomDomain.Room, gameruntime.Session, error) {
	if repository == nil || repository.runner == nil || ctx == nil || !commit.Valid() {
		return roomDomain.Room{}, gameruntime.Session{}, gameruntime.ErrInvalidLifecycleCommit
	}
	if err := validateRoomGameSessionTermination(before, after, commit.Before(), commit.After(), gameruntime.StatusCancelled); err != nil {
		return roomDomain.Room{}, gameruntime.Session{}, err
	}
	var storedRoom roomDomain.Room
	var storedSession gameruntime.Session
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		locked, err := lockRoomForUpdate(ctx, queries, before.Snapshot().ID)
		if err != nil {
			return err
		}
		current, err := getGameSessionForUpdate(ctx, queries, commit.Before().Snapshot().ID)
		if err != nil {
			return err
		}
		if !sameRoomSnapshot(locked.Snapshot(), before.Snapshot()) {
			if !samePersistedTerminalSession(current.Snapshot(), commit.After().Snapshot(), gameruntime.StatusCancelled) {
				return roomDomain.ErrRoomVersionConflict
			}
			storedRoom, storedSession = locked, current
			return nil
		}
		storedSession, err = persistLifecycleAfterSessionLock(ctx, queries, current, commit)
		if err != nil {
			return err
		}
		storedRoom, err = updateRoomAggregateCAS(ctx, queries, before.Snapshot(), after.Snapshot())
		return err
	})
	if err != nil {
		return roomDomain.Room{}, gameruntime.Session{}, mapRoomGameSessionStartError(ctx, err)
	}
	return storedRoom, storedSession, nil
}

func lockExactRoom(ctx context.Context, queries QueryHandle, before roomDomain.Room) (roomDomain.Room, error) {
	snapshot := before.Snapshot()
	locked, err := lockRoomForUpdate(ctx, queries, snapshot.ID)
	if err != nil {
		return roomDomain.Room{}, err
	}
	if !sameRoomSnapshot(locked.Snapshot(), snapshot) {
		return roomDomain.Room{}, roomDomain.ErrRoomVersionConflict
	}
	return locked, nil
}

// lockRoomForUpdate loads the current aggregate after taking the canonical room-first transaction lock.
func lockRoomForUpdate(ctx context.Context, queries QueryHandle, roomID uuid.UUID) (roomDomain.Room, error) {
	row, err := queries.GetPartyRoomForUpdate(ctx, sqlcgen.GetPartyRoomForUpdateParams{RoomID: uuidToPG(roomID)})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return roomDomain.Room{}, roomDomain.ErrRoomNotFound
		}
		return roomDomain.Room{}, err
	}
	members, err := queries.ListRoomMembers(ctx, sqlcgen.ListRoomMembersParams{RoomID: uuidToPG(roomID)})
	if err != nil {
		return roomDomain.Room{}, err
	}
	return roomFromRows(row, members)
}

// replayFinishedActionAfterRoomChange returns only an already-durable terminal receipt after the room pointer moved on.
func replayFinishedActionAfterRoomChange(
	ctx context.Context,
	queries QueryHandle,
	commit gameruntime.ActionCommit,
) (gameruntime.ActionCommitResult, error) {
	current, err := getGameSessionForUpdate(ctx, queries, commit.Before().Snapshot().ID)
	if err != nil {
		return gameruntime.ActionCommitResult{}, err
	}
	key := commit.Receipt().Snapshot().Key
	row, err := queries.GetGameActionReceipt(ctx, sqlcgen.GetGameActionReceiptParams{
		SessionID: uuidToPG(key.SessionID), ActorUserID: uuidToPG(key.ActorUserID), ActionID: key.ActionID.Value(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return gameruntime.ActionCommitResult{}, roomDomain.ErrRoomVersionConflict
	}
	if err != nil {
		return gameruntime.ActionCommitResult{}, err
	}
	receipt, err := actionReceiptFromRow(row)
	if err != nil {
		return gameruntime.ActionCommitResult{}, err
	}
	if _, err := receipt.Replay(commit.Receipt().Snapshot().RequestDigest); err != nil {
		return gameruntime.ActionCommitResult{}, err
	}
	if !samePersistedTerminalSession(current.Snapshot(), commit.After().Snapshot(), gameruntime.StatusFinished) ||
		receipt.Snapshot().StateVersion != current.Snapshot().State.StateVersion {
		return gameruntime.ActionCommitResult{}, gameruntime.ErrGameSessionIntegrity
	}
	return gameruntime.ActionCommitResult{Session: current, Receipt: receipt, Replayed: true}, nil
}

// replayFinishedTimerAfterRoomChange prevents a duplicate terminal wakeup from advancing either aggregate twice.
func replayFinishedTimerAfterRoomChange(
	ctx context.Context,
	queries QueryHandle,
	current gameruntime.Session,
	commit gameruntime.TimerCommit,
) (gameruntime.TimerCommitResult, error) {
	key := commit.Receipt().Snapshot().Key
	row, err := queries.GetGameTimerReceipt(ctx, sqlcgen.GetGameTimerReceiptParams{
		SessionID: uuidToPG(key.SessionID), TimerID: string(key.TimerID), ExpectedStateVersion: int64(key.ExpectedStateVersion),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return gameruntime.TimerCommitResult{}, roomDomain.ErrRoomVersionConflict
	}
	if err != nil {
		return gameruntime.TimerCommitResult{}, err
	}
	receipt, err := timerReceiptFromRow(row)
	if err != nil {
		return gameruntime.TimerCommitResult{}, err
	}
	if !samePersistedTerminalSession(current.Snapshot(), commit.After().Snapshot(), gameruntime.StatusFinished) ||
		receipt.Snapshot().StateVersion != current.Snapshot().State.StateVersion {
		return gameruntime.TimerCommitResult{}, gameruntime.ErrGameSessionIntegrity
	}
	return gameruntime.TimerCommitResult{Session: current, Receipt: receipt, Replayed: true}, nil
}

// replayFinishedSystemAfterRoomChange requires the original completed operation/source/digest binding.
func replayFinishedSystemAfterRoomChange(
	ctx context.Context,
	queries QueryHandle,
	current gameruntime.Session,
	commit gameruntime.SystemCommit,
) (gameruntime.SystemCommitResult, error) {
	receiptSnapshot := commit.Receipt().Snapshot()
	row, err := queries.GetGameSystemOperationForUpdate(ctx, sqlcgen.GetGameSystemOperationForUpdateParams{
		SessionID: uuidToPG(receiptSnapshot.Key.SessionID), OperationID: receiptSnapshot.Key.OperationID.Value(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return gameruntime.SystemCommitResult{}, roomDomain.ErrRoomVersionConflict
	}
	if err != nil {
		return gameruntime.SystemCommitResult{}, err
	}
	receipt, err := completedSystemReceiptFromRow(row, receiptSnapshot.Key, receiptSnapshot.RequestDigest)
	if errors.Is(err, gameruntime.ErrSystemOperationPending) {
		return gameruntime.SystemCommitResult{}, roomDomain.ErrRoomVersionConflict
	}
	if err != nil {
		return gameruntime.SystemCommitResult{}, err
	}
	if !samePersistedTerminalSession(current.Snapshot(), commit.After().Snapshot(), gameruntime.StatusFinished) ||
		receipt.Snapshot().StateVersion != current.Snapshot().State.StateVersion {
		return gameruntime.SystemCommitResult{}, gameruntime.ErrGameSessionIntegrity
	}
	return gameruntime.SystemCommitResult{Session: current, Receipt: receipt, Replayed: true}, nil
}

// samePersistedTerminalSession verifies the stable identity and terminal version needed by cross-aggregate replay.
func samePersistedTerminalSession(current, proposed gameruntime.SessionSnapshot, status gameruntime.Status) bool {
	return current.ID == proposed.ID && current.RoomID == proposed.RoomID && current.VersionKey == proposed.VersionKey &&
		current.OwnershipEpoch == proposed.OwnershipEpoch && current.State.StateVersion == proposed.State.StateVersion &&
		current.Status == status && current.EndedAt.Equal(current.UpdatedAt)
}

func finishPartyRoomAggregateCAS(
	ctx context.Context,
	queries QueryHandle,
	before roomDomain.RoomSnapshot,
	after roomDomain.RoomSnapshot,
) (roomDomain.Room, error) {
	row, err := queries.FinishPartyRoomCAS(ctx, sqlcgen.FinishPartyRoomCASParams{
		RoomVersion: int64(after.RoomVersion), UpdatedAt: timeToPG(after.UpdatedAt), RoomID: uuidToPG(before.ID),
		ActiveSessionID: uuidToPG(before.ActiveSessionID), ActiveGameID: pgtype.Text{String: before.ActiveGameID, Valid: true},
		ExpectedRoomVersion: int64(before.RoomVersion), ExpectedMembershipVersion: int64(before.MembershipVersion),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return roomDomain.Room{}, roomDomain.ErrRoomVersionConflict
		}
		return roomDomain.Room{}, err
	}
	members, err := queries.ListRoomMembers(ctx, sqlcgen.ListRoomMembersParams{RoomID: uuidToPG(before.ID)})
	if err != nil {
		return roomDomain.Room{}, err
	}
	stored, err := roomFromRows(row, members)
	if err != nil {
		return roomDomain.Room{}, err
	}
	if !sameRoomSnapshot(stored.Snapshot(), after) {
		return roomDomain.Room{}, roomDomain.ErrRoomIntegrity
	}
	return stored, nil
}

func validateRoomGameSessionTermination(
	before roomDomain.Room,
	after roomDomain.Room,
	beforeSession gameruntime.Session,
	afterSession gameruntime.Session,
	wantStatus gameruntime.Status,
) error {
	if err := validateRoomTransition(before.Snapshot(), after.Snapshot()); err != nil {
		return err
	}
	roomBefore, roomAfter := before.Snapshot(), after.Snapshot()
	sessionBefore, sessionAfter := beforeSession.Snapshot(), afterSession.Snapshot()
	expectedRoom := roomBefore
	expectedRoom.Status = roomDomain.RoomStatusPostGame
	expectedRoom.ParticipantAdmission = roomDomain.AdmissionClosed
	expectedRoom.ActiveSessionID = uuid.Nil
	expectedRoom.ActiveGameID = ""
	expectedRoom.LastFinishedSessionID = roomBefore.ActiveSessionID
	expectedRoom.LastFinishedGameID = roomBefore.ActiveGameID
	if wantStatus == gameruntime.StatusCancelled {
		expectedRoom.Status = roomDomain.RoomStatusLobby
		expectedRoom.LastFinishedSessionID = uuid.Nil
		expectedRoom.LastFinishedGameID = ""
	}
	expectedRoom.RoomVersion++
	expectedRoom.UpdatedAt = roomAfter.UpdatedAt
	if roomBefore.Status != roomDomain.RoomStatusPlaying || roomBefore.ActiveSessionID == uuid.Nil ||
		!sameRoomSnapshot(expectedRoom, roomAfter) || !sameTerminatingSessionIdentity(sessionBefore, sessionAfter) ||
		sessionBefore.ID != roomBefore.ActiveSessionID || sessionBefore.RoomID != roomBefore.ID ||
		string(sessionBefore.VersionKey.GameID) != roomBefore.ActiveGameID || sessionAfter.Status != wantStatus ||
		!sessionAfter.UpdatedAt.Equal(roomAfter.UpdatedAt) {
		return gameruntime.ErrInvalidSessionInput
	}
	if wantStatus == gameruntime.StatusCancelled && !reflect.DeepEqual(sessionBefore.State, sessionAfter.State) {
		return gameruntime.ErrInvalidLifecycleCommit
	}
	return nil
}

func sameTerminatingSessionIdentity(left, right gameruntime.SessionSnapshot) bool {
	return left.ID == right.ID && left.RoomID == right.RoomID && left.VersionKey == right.VersionKey &&
		left.OwnershipEpoch == right.OwnershipEpoch && left.StartedAt.Equal(right.StartedAt) &&
		reflect.DeepEqual(left.Participants, right.Participants)
}

// validateRoomGameSessionStart accepts only an idle-to-playing transition and its matching frozen runtime state.
func validateRoomGameSessionStart(before, after roomDomain.Room, commit gameruntime.CreationCommit) error {
	beforeSnapshot, afterSnapshot := before.Snapshot(), after.Snapshot()
	sessionSnapshot := commit.Session.Snapshot()
	if (beforeSnapshot.Status != roomDomain.RoomStatusLobby && beforeSnapshot.Status != roomDomain.RoomStatusPostGame) ||
		beforeSnapshot.ActiveSessionID != uuid.Nil || beforeSnapshot.ActiveGameID != "" ||
		afterSnapshot.Status != roomDomain.RoomStatusPlaying || afterSnapshot.ActiveSessionID == uuid.Nil || afterSnapshot.ActiveGameID == "" ||
		afterSnapshot.ParticipantAdmission != roomDomain.AdmissionClosed || afterSnapshot.LastFinishedSessionID != uuid.Nil || afterSnapshot.LastFinishedGameID != "" ||
		beforeSnapshot.ID != afterSnapshot.ID || beforeSnapshot.RoomCode != afterSnapshot.RoomCode ||
		beforeSnapshot.Visibility != afterSnapshot.Visibility || beforeSnapshot.HostUserID != afterSnapshot.HostUserID ||
		beforeSnapshot.ParticipantCapacity != afterSnapshot.ParticipantCapacity ||
		beforeSnapshot.SpectatorAdmission != afterSnapshot.SpectatorAdmission ||
		beforeSnapshot.MembershipVersion != afterSnapshot.MembershipVersion ||
		!beforeSnapshot.CreatedAt.Equal(afterSnapshot.CreatedAt) || !sameRoomMembers(beforeSnapshot.Members, afterSnapshot.Members) {
		return roomDomain.ErrInvalidRoomInput
	}
	if sessionSnapshot.ID != afterSnapshot.ActiveSessionID || sessionSnapshot.RoomID != afterSnapshot.ID ||
		string(sessionSnapshot.VersionKey.GameID) != afterSnapshot.ActiveGameID || sessionSnapshot.Status != gameruntime.StatusActive ||
		!sessionSnapshot.StartedAt.Equal(afterSnapshot.UpdatedAt) {
		return gameruntime.ErrInvalidSessionInput
	}
	roomParticipants := make(map[uuid.UUID]uint32)
	for _, member := range afterSnapshot.Members {
		if member.Role == roomDomain.MemberRoleParticipant {
			roomParticipants[member.UserID] = member.SeatIndex
		}
	}
	if len(roomParticipants) != len(sessionSnapshot.Participants) {
		return gameruntime.ErrInvalidSessionInput
	}
	for _, participant := range sessionSnapshot.Participants {
		if seat, ok := roomParticipants[participant.UserID]; !ok || seat != participant.SeatIndex {
			return gameruntime.ErrInvalidSessionInput
		}
	}
	return nil
}

// sameRoomSnapshot compares the exact optimistic snapshot without depending on database member ordering.
func sameRoomSnapshot(left, right roomDomain.RoomSnapshot) bool {
	return left.ID == right.ID && left.RoomCode == right.RoomCode && left.Visibility == right.Visibility && left.Status == right.Status &&
		left.HostUserID == right.HostUserID && left.ParticipantCapacity == right.ParticipantCapacity &&
		left.ParticipantAdmission == right.ParticipantAdmission && left.SpectatorAdmission == right.SpectatorAdmission &&
		left.ActiveSessionID == right.ActiveSessionID && left.ActiveGameID == right.ActiveGameID &&
		left.RoomVersion == right.RoomVersion && left.MembershipVersion == right.MembershipVersion &&
		left.CreatedAt.Equal(right.CreatedAt) && left.UpdatedAt.Equal(right.UpdatedAt) && sameRoomMembers(left.Members, right.Members)
}

// sameRoomMembers treats the aggregate slice as an identity-keyed set because SQL reads have a stable but different order.
func sameRoomMembers(left, right []roomDomain.MemberSnapshot) bool {
	if len(left) != len(right) {
		return false
	}
	members := make(map[uuid.UUID]roomDomain.MemberSnapshot, len(left))
	for _, member := range left {
		members[member.UserID] = member
	}
	for _, member := range right {
		current, ok := members[member.UserID]
		if !ok || current.Role != member.Role || current.RequestedRole != member.RequestedRole || current.SeatIndex != member.SeatIndex ||
			!current.JoinedAt.Equal(member.JoinedAt) || !current.LastSeenAt.Equal(member.LastSeenAt) {
			return false
		}
	}
	return true
}

// mapRoomGameSessionStartError preserves both aggregate contracts while hiding PostgreSQL diagnostics.
func mapRoomGameSessionStartError(ctx context.Context, err error) error {
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
		roomDomain.ErrInvalidRoomInput,
		roomDomain.ErrHostRequired,
		roomDomain.ErrRoomVersionConflict,
		roomDomain.ErrRoomNotFound,
		roomDomain.ErrRoomIntegrity,
		roomDomain.ErrRoomRepositoryUnavailable,
		gameruntime.ErrInvalidSessionInput,
		gameruntime.ErrSessionAlreadyExists,
		gameruntime.ErrStateVersionConflict,
		gameruntime.ErrOwnershipLost,
		gameruntime.ErrSessionSuspended,
		gameruntime.ErrSessionTerminal,
		gameruntime.ErrParticipantNotActive,
		gameruntime.ErrSystemOperationPending,
		gameruntime.ErrInvalidActionCommit,
		gameruntime.ErrInvalidTimerCommit,
		gameruntime.ErrInvalidSystemCommit,
		gameruntime.ErrInvalidLifecycleCommit,
		gameruntime.ErrGameSessionIntegrity,
		gameruntime.ErrGameSessionRepositoryUnavailable,
		idempotency.ErrConflict,
	} {
		if errors.Is(err, domainErr) {
			return domainErr
		}
	}
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		switch pgError.Code {
		case "23505":
			return gameruntime.ErrSessionAlreadyExists
		case "23502", "23503", "23514", "22P02":
			return gameruntime.ErrGameSessionIntegrity
		case "40001", "40P01":
			return roomDomain.ErrRoomVersionConflict
		}
	}
	return roomDomain.ErrRoomRepositoryUnavailable
}
