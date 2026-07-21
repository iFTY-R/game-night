package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/iFTY-R/game-night/platform/replay"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ReplayAccessRepository authorizes terminal replay resources without exposing event or snapshot payloads.
type ReplayAccessRepository struct {
	runner *TransactionRunner
}

// NewReplayAccessRepository binds replay ACL reads and host-controlled CAS updates to PostgreSQL.
func NewReplayAccessRepository(pool *pgxpool.Pool) *ReplayAccessRepository {
	return &ReplayAccessRepository{runner: NewTransactionRunner(pool)}
}

// Authorize resolves one authenticated viewer to the narrowest allowed projection policy.
func (repository *ReplayAccessRepository) Authorize(ctx context.Context, actorID, roomID, sessionID uuid.UUID) (game.ReplayAccessPolicy, error) {
	row, err := repository.accessRow(ctx, actorID, roomID, sessionID)
	if err != nil {
		return "", err
	}
	if row.SessionStatus != "finished" {
		return "", replay.ErrPolicyUnavailable
	}
	if row.ActorParticipated {
		return game.ReplayAccessParticipant, nil
	}
	// Only post-migration terminal transactions have a trustworthy room-member snapshot.
	if !row.MemberSnapshotCompletedAt.Valid {
		return "", replay.ErrAccessDenied
	}
	switch replay.Policy(row.Policy) {
	case replay.PolicyRoomMember:
		if row.ActorWasRoomMember {
			return game.ReplayAccessRoomMember, nil
		}
	case replay.PolicyPublic:
		if row.RoomVisibility == "public" {
			return game.ReplayAccessPublic, nil
		}
	}
	return "", replay.ErrAccessDenied
}

// Get returns policy metadata only to the current PartyRoom host.
func (repository *ReplayAccessRepository) Get(ctx context.Context, actorID, roomID, sessionID uuid.UUID) (replay.Access, error) {
	row, err := repository.accessRow(ctx, actorID, roomID, sessionID)
	if err != nil {
		return replay.Access{}, err
	}
	if uuid.UUID(row.HostUserID.Bytes) != actorID {
		return replay.Access{}, replay.ErrAccessDenied
	}
	return replayAccessFromValues(
		row.SessionID, row.RoomID, row.Policy, row.PolicyVersion, row.MemberSnapshotCompletedAt, row.CreatedAt, row.UpdatedAt,
	)
}

// SetPolicy updates one finished-session policy only under current-host authority and exact policy version.
func (repository *ReplayAccessRepository) SetPolicy(ctx context.Context, command replay.SetPolicyCommand) (replay.Access, error) {
	if repository == nil || repository.runner == nil || ctx == nil || !command.Valid() {
		return replay.Access{}, replay.ErrInvalidInput
	}
	var access replay.Access
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		state, err := queries.GetGameSessionReplayAccess(ctx, sqlcgen.GetGameSessionReplayAccessParams{
			ActorUserID: uuidToPG(command.ActorUserID), SessionID: uuidToPG(command.SessionID), RoomID: uuidToPG(command.RoomID),
		})
		if err != nil {
			return err
		}
		if uuid.UUID(state.HostUserID.Bytes) != command.ActorUserID {
			return replay.ErrAccessDenied
		}
		if state.SessionStatus != "finished" || !state.MemberSnapshotCompletedAt.Valid ||
			command.Policy == replay.PolicyPublic && state.RoomVisibility != "public" {
			return replay.ErrPolicyUnavailable
		}
		row, err := queries.SetGameSessionReplayPolicyCAS(ctx, sqlcgen.SetGameSessionReplayPolicyCASParams{
			Policy: string(command.Policy), UpdatedAt: timeToPG(command.UpdatedAt), SessionID: uuidToPG(command.SessionID),
			RoomID: uuidToPG(command.RoomID), ExpectedPolicyVersion: int64(command.ExpectedVersion), ActorUserID: uuidToPG(command.ActorUserID),
		})
		if err != nil {
			return err
		}
		access, err = replayAccessFromValues(
			row.SessionID, row.RoomID, row.Policy, row.PolicyVersion, row.MemberSnapshotCompletedAt, row.CreatedAt, row.UpdatedAt,
		)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return replay.Access{}, replay.ErrPolicyConflict
	}
	if err != nil {
		return replay.Access{}, err
	}
	return access, nil
}

func (repository *ReplayAccessRepository) accessRow(
	ctx context.Context, actorID, roomID, sessionID uuid.UUID,
) (sqlcgen.GetGameSessionReplayAccessRow, error) {
	if repository == nil || repository.runner == nil || ctx == nil || actorID == uuid.Nil || roomID == uuid.Nil || sessionID == uuid.Nil {
		return sqlcgen.GetGameSessionReplayAccessRow{}, replay.ErrInvalidInput
	}
	var row sqlcgen.GetGameSessionReplayAccessRow
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		var err error
		row, err = queries.GetGameSessionReplayAccess(ctx, sqlcgen.GetGameSessionReplayAccessParams{
			ActorUserID: uuidToPG(actorID), SessionID: uuidToPG(sessionID), RoomID: uuidToPG(roomID),
		})
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlcgen.GetGameSessionReplayAccessRow{}, replay.ErrPolicyUnavailable
	}
	return row, err
}

func replayAccessFromValues(
	sessionID, roomID pgtype.UUID, policy string, version int64, snapshotAt, createdAt, updatedAt pgtype.Timestamptz,
) (replay.Access, error) {
	if version <= 0 || !sessionID.Valid || !roomID.Valid || !createdAt.Valid || !updatedAt.Valid {
		return replay.Access{}, replay.ErrInvalidInput
	}
	access := replay.Access{
		SessionID: uuid.UUID(sessionID.Bytes), RoomID: uuid.UUID(roomID.Bytes), Policy: replay.Policy(policy), Version: uint64(version),
		CreatedAt: createdAt.Time.Round(0).UTC(), UpdatedAt: updatedAt.Time.Round(0).UTC(),
	}
	if snapshotAt.Valid {
		access.MemberSnapshotCompletedAt = snapshotAt.Time.Round(0).UTC()
	}
	if !access.Valid() {
		return replay.Access{}, replay.ErrInvalidInput
	}
	return access, nil
}
