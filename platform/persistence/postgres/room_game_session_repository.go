package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// Start atomically locks the room, commits its CAS transition, creates the session children, and inserts outbox events.
// A failure after either aggregate write rolls back both aggregates and leaves the room in its previous state.
func (repository *RoomGameSessionRepository) Start(
	ctx context.Context,
	before roomDomain.Room,
	after roomDomain.Room,
	commit gameruntime.CreationCommit,
) (roomDomain.Room, gameruntime.Session, error) {
	if repository == nil || repository.runner == nil || ctx == nil {
		return roomDomain.Room{}, gameruntime.Session{}, roomDomain.ErrInvalidRoomInput
	}
	if err := validateRoomTransition(before.Snapshot(), after.Snapshot()); err != nil {
		return roomDomain.Room{}, gameruntime.Session{}, err
	}
	if !commit.Valid() {
		return roomDomain.Room{}, gameruntime.Session{}, gameruntime.ErrInvalidSessionInput
	}
	if err := validateCreationWidths(commit); err != nil {
		return roomDomain.Room{}, gameruntime.Session{}, err
	}
	if err := validateRoomGameSessionStart(before, after, commit); err != nil {
		return roomDomain.Room{}, gameruntime.Session{}, err
	}

	var storedRoom roomDomain.Room
	var storedSession gameruntime.Session
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		beforeSnapshot := before.Snapshot()
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
		return err
	})
	if err != nil {
		return roomDomain.Room{}, gameruntime.Session{}, mapRoomGameSessionStartError(ctx, err)
	}
	return storedRoom, storedSession, nil
}

// validateRoomGameSessionStart accepts only the exact lobby-to-playing transition and its matching frozen runtime state.
func validateRoomGameSessionStart(before, after roomDomain.Room, commit gameruntime.CreationCommit) error {
	beforeSnapshot, afterSnapshot := before.Snapshot(), after.Snapshot()
	sessionSnapshot := commit.Session.Snapshot()
	if beforeSnapshot.Status != roomDomain.RoomStatusLobby || beforeSnapshot.ActiveSessionID != uuid.Nil || beforeSnapshot.ActiveGameID != "" ||
		afterSnapshot.Status != roomDomain.RoomStatusPlaying || afterSnapshot.ActiveSessionID == uuid.Nil || afterSnapshot.ActiveGameID == "" ||
		afterSnapshot.ParticipantAdmission != roomDomain.AdmissionClosed ||
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
		roomDomain.ErrRoomVersionConflict,
		roomDomain.ErrRoomNotFound,
		roomDomain.ErrRoomIntegrity,
		roomDomain.ErrRoomRepositoryUnavailable,
		gameruntime.ErrInvalidSessionInput,
		gameruntime.ErrSessionAlreadyExists,
		gameruntime.ErrGameSessionIntegrity,
		gameruntime.ErrGameSessionRepositoryUnavailable,
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
		case "23503", "23514", "22P02":
			return gameruntime.ErrGameSessionIntegrity
		case "40001", "40P01":
			return roomDomain.ErrRoomVersionConflict
		}
	}
	return roomDomain.ErrRoomRepositoryUnavailable
}
