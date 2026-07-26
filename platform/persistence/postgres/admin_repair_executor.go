package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/google/uuid"
	adminroom "github.com/iFTY-R/game-night/platform/admin/room"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/outbox"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AdminEmergencyRepairExecutor applies fixed operational repairs through existing aggregate repositories.
type AdminEmergencyRepairExecutor struct {
	games        *GameSessionRepository
	rooms        *RoomRepository
	roomSessions *RoomGameSessionRepository
}

// NewAdminEmergencyRepairExecutor wires the admin repair side-effect boundary to PostgreSQL persistence.
func NewAdminEmergencyRepairExecutor(pool *pgxpool.Pool) *AdminEmergencyRepairExecutor {
	return &AdminEmergencyRepairExecutor{
		games:        NewGameSessionRepository(pool),
		rooms:        NewRoomRepository(pool),
		roomSessions: NewRoomGameSessionRepository(pool),
	}
}

// ExecuteEmergencyRepair rejects generic patches and dispatches only the reviewed fixed repair families.
func (executor *AdminEmergencyRepairExecutor) ExecuteEmergencyRepair(
	ctx context.Context,
	repair adminroom.RepairOperation,
	command adminroom.ExecuteEmergencyRepairCommand,
) ([]byte, error) {
	if executor == nil || ctx == nil || !command.OperationID.Valid() ||
		command.ExpectedRepairVersion == 0 || command.ExpectedRepairVersion != repair.Version ||
		command.ExecutedAt.IsZero() {
		return nil, adminroom.ErrInvalidInput
	}
	switch repair.RepairType {
	case adminroom.RepairTerminateUnrecoverable:
		return executor.terminateUnrecoverableGame(ctx, repair, command)
	case adminroom.RepairRepairRoomGameLink:
		return nil, adminroom.ErrInvalidInput
	default:
		return nil, adminroom.ErrInvalidInput
	}
}

func (executor *AdminEmergencyRepairExecutor) terminateUnrecoverableGame(
	ctx context.Context,
	repair adminroom.RepairOperation,
	command adminroom.ExecuteEmergencyRepairCommand,
) ([]byte, error) {
	if executor.games == nil || executor.rooms == nil || executor.roomSessions == nil ||
		repair.TargetID == uuid.Nil || repair.TargetKind != adminroom.RepairTargetKindGameSession ||
		repair.ExpectedStateVersion == 0 || repair.ExpectedOwnershipEpoch == 0 {
		return nil, adminroom.ErrInvalidInput
	}
	session, err := executor.games.Get(ctx, repair.TargetID)
	if err != nil {
		return nil, mapAdminRepairRuntimeError(ctx, err)
	}
	sessionSnapshot := session.Snapshot()
	// The dry-run digest and explicit version fences must still describe the current authoritative session.
	if sessionSnapshot.Status != gameruntime.StatusActive && sessionSnapshot.Status != gameruntime.StatusSuspended ||
		sessionSnapshot.State.StateVersion != repair.ExpectedStateVersion ||
		sessionSnapshot.OwnershipEpoch != repair.ExpectedOwnershipEpoch {
		return nil, adminroom.ErrConflict
	}
	beforeDigest := adminRepairStableDigest(
		"game", sessionSnapshot.ID.String(), sessionSnapshot.RoomID.String(), sessionSnapshot.Status,
		sessionSnapshot.State.StateVersion, sessionSnapshot.OwnershipEpoch,
	)
	if !adminRepairSameBytes(beforeDigest[:], repair.TargetDigest) {
		return nil, adminroom.ErrConflict
	}
	room, err := executor.rooms.GetByID(ctx, sessionSnapshot.RoomID)
	if err != nil {
		return nil, mapAdminRepairRoomError(ctx, err)
	}
	roomSnapshot := room.Snapshot()
	// The DB repair is intentionally narrow: it only cancels a room that still points at this exact session/game pair.
	if roomSnapshot.Status != roomDomain.RoomStatusPlaying ||
		roomSnapshot.ActiveSessionID != sessionSnapshot.ID ||
		roomSnapshot.ActiveGameID != string(sessionSnapshot.VersionKey.GameID) {
		return nil, adminroom.ErrConflict
	}
	cancelled, err := session.Cancel(sessionSnapshot.OwnershipEpoch, command.ExecutedAt, gameruntime.CancelReasonPlatformCancelled)
	if err != nil {
		return nil, mapAdminRepairRuntimeError(ctx, err)
	}
	nextRoom, err := room.CancelSession(sessionSnapshot.ID, room.Version(), command.ExecutedAt)
	if err != nil {
		return nil, mapAdminRepairRoomError(ctx, err)
	}
	commit, err := newAdminRepairLifecycleCommit(session, cancelled)
	if err != nil {
		return nil, mapAdminRepairRuntimeError(ctx, err)
	}
	storedRoom, storedSession, err := executor.roomSessions.Cancel(ctx, room, nextRoom, commit)
	if err != nil {
		return nil, mapAdminRepairCrossAggregateError(ctx, err)
	}
	storedRoomSnapshot, storedSessionSnapshot := storedRoom.Snapshot(), storedSession.Snapshot()
	afterDigest := adminRepairStableDigest(
		"game-cancelled", storedSessionSnapshot.ID.String(), storedSessionSnapshot.RoomID.String(),
		storedSessionSnapshot.Status, storedSessionSnapshot.State.StateVersion, storedSessionSnapshot.OwnershipEpoch,
		storedRoomSnapshot.Status, storedRoomSnapshot.RoomVersion,
	)
	return afterDigest[:], nil
}

func newAdminRepairLifecycleCommit(before, after gameruntime.Session) (gameruntime.LifecycleCommit, error) {
	snapshot := after.Snapshot()
	eventID, err := uuid.NewV7()
	if err != nil {
		return gameruntime.LifecycleCommit{}, err
	}
	event, err := outbox.NewEvent(
		eventID,
		gameruntime.GameSessionCancelledEventType,
		gameruntime.GameSessionAggregateType,
		snapshot.ID,
		[]byte("admin.repair."+string(adminroom.RepairTerminateUnrecoverable)),
		snapshot.UpdatedAt,
		snapshot.UpdatedAt,
	)
	if err != nil {
		return gameruntime.LifecycleCommit{}, err
	}
	return gameruntime.NewLifecycleCommit(before, after, []outbox.Event{event})
}

func adminRepairStableDigest(values ...any) [sha256.Size]byte {
	hasher := sha256.New()
	for _, value := range values {
		_, _ = fmt.Fprintf(hasher, "%d:%v;", len(fmt.Sprint(value)), value)
	}
	return sha256.Sum256(hasher.Sum(nil))
}

func adminRepairSameBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func mapAdminRepairCrossAggregateError(ctx context.Context, err error) error {
	if mapped := mapAdminRepairRuntimeError(ctx, err); mapped != adminroom.ErrRepositoryUnavailable {
		return mapped
	}
	return mapAdminRepairRoomError(ctx, err)
}

func mapAdminRepairRuntimeError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case ctx != nil && ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, gameruntime.ErrSessionNotFound):
		return adminroom.ErrNotFound
	case errors.Is(err, gameruntime.ErrInvalidSessionInput) || errors.Is(err, gameruntime.ErrInvalidLifecycleCommit):
		return adminroom.ErrInvalidInput
	case errors.Is(err, gameruntime.ErrOwnershipLost) || errors.Is(err, gameruntime.ErrStateVersionConflict) ||
		errors.Is(err, gameruntime.ErrSessionSuspended) || errors.Is(err, gameruntime.ErrSessionTerminal):
		return adminroom.ErrConflict
	case errors.Is(err, gameruntime.ErrGameSessionIntegrity):
		return adminroom.ErrIntegrity
	default:
		return adminroom.ErrRepositoryUnavailable
	}
}

func mapAdminRepairRoomError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case ctx != nil && ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, roomDomain.ErrRoomNotFound):
		return adminroom.ErrNotFound
	case errors.Is(err, roomDomain.ErrInvalidRoomInput):
		return adminroom.ErrInvalidInput
	case errors.Is(err, roomDomain.ErrRoomVersionConflict) || errors.Is(err, roomDomain.ErrRoomStatus) || errors.Is(err, roomDomain.ErrSessionNotFound):
		return adminroom.ErrConflict
	case errors.Is(err, roomDomain.ErrRoomIntegrity):
		return adminroom.ErrIntegrity
	default:
		return adminroom.ErrRepositoryUnavailable
	}
}

var _ adminroom.EmergencyRepairExecutor = (*AdminEmergencyRepairExecutor)(nil)
