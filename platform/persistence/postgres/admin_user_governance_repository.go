package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	admin "github.com/iFTY-R/game-night/platform/admin"
	adminuser "github.com/iFTY-R/game-night/platform/admin/user"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	roomdomain "github.com/iFTY-R/game-night/platform/room"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// adminUserCommandSnapshotLimit keeps durable preview payloads bounded to reviewed governance state only.
	adminUserCommandSnapshotLimit = 64 * 1024
	// adminUserCommandBlockersLimit prevents one preview from storing an unbounded blocker list in PostgreSQL.
	adminUserCommandBlockersLimit = 16 * 1024
	// adminUserDeviceRevokeReasonAdminRequested matches the identity revoke invariant for operator-forced device sign-out.
	adminUserDeviceRevokeReasonAdminRequested = "admin_requested"
	// adminUserDeviceRevokeReasonAccountDeleted matches the identity revoke invariant for account deletion cleanup.
	adminUserDeviceRevokeReasonAccountDeleted = "account_deleted"
)

// AdminUserGovernanceRepository executes the narrow single-user governance mutations needed by the user center.
type AdminUserGovernanceRepository struct {
	queries *sqlcgen.Queries
	runner  *TransactionRunner
	rooms   *RoomRepository
}

// NewAdminUserGovernanceRepository wires durable single-user governance to one PostgreSQL pool.
func NewAdminUserGovernanceRepository(pool *pgxpool.Pool) *AdminUserGovernanceRepository {
	return &AdminUserGovernanceRepository{
		queries: sqlcgen.New(pool),
		runner:  NewTransactionRunner(pool),
		rooms:   NewRoomRepository(pool),
	}
}

// CreateUserCommandPreview persists one reviewed command snapshot so execution never trusts mutable client state.
func (repository *AdminUserGovernanceRepository) CreateUserCommandPreview(
	ctx context.Context,
	command adminuser.CreateUserCommandPreviewCommand,
) (adminuser.UserCommandPreview, error) {
	preview := command.Preview
	if repository == nil || repository.queries == nil || ctx == nil || !validUserCommandPreview(preview) {
		return adminuser.UserCommandPreview{}, adminuser.ErrInvalidInput
	}
	snapshot, blockers, err := marshalUserCommandPreview(preview)
	if err != nil {
		return adminuser.UserCommandPreview{}, adminuser.ErrInvalidInput
	}
	row, err := repository.queries.CreateAdminUserCommandPreview(ctx, sqlcgen.CreateAdminUserCommandPreviewParams{
		PreviewID: uuidToPG(preview.ID), ActorAdminID: uuidToPG(preview.ActorAdminID), UserID: uuidToPG(preview.Snapshot.UserID),
		Command: string(preview.Snapshot.Command), SnapshotSchemaVersion: int32(preview.Snapshot.SchemaVersion), Snapshot: snapshot,
		PreviewDigest: preview.PreviewDigest[:], AffectedDevices: preview.AffectedDevices, AffectedRooms: preview.AffectedRooms,
		Blockers: blockers, RequiredElevation: optionalText(string(preview.RequiredElevation)),
		SampledAt: timeToPG(preview.SampledAt), ExpiresAt: timeToPG(preview.ExpiresAt),
	})
	if err != nil {
		return adminuser.UserCommandPreview{}, mapAdminUserQueryError(ctx, err, adminuser.ErrConflict)
	}
	return adminUserCommandPreviewFromRow(row)
}

// GetUserCommandPreview reloads one durable preview for validation and execution.
func (repository *AdminUserGovernanceRepository) GetUserCommandPreview(
	ctx context.Context,
	previewID, actorAdminID uuid.UUID,
) (adminuser.UserCommandPreview, error) {
	if repository == nil || repository.queries == nil || ctx == nil || previewID == uuid.Nil || actorAdminID == uuid.Nil {
		return adminuser.UserCommandPreview{}, adminuser.ErrInvalidInput
	}
	row, err := repository.queries.GetAdminUserCommandPreview(ctx, sqlcgen.GetAdminUserCommandPreviewParams{
		PreviewID: uuidToPG(previewID), ActorAdminID: uuidToPG(actorAdminID),
	})
	if err != nil {
		return adminuser.UserCommandPreview{}, mapAdminUserQueryError(ctx, err, adminuser.ErrNotFound)
	}
	return adminUserCommandPreviewFromRow(row)
}

// ConsumeUserCommandPreview marks one preview as spent only while its reviewed snapshot is still live.
func (repository *AdminUserGovernanceRepository) ConsumeUserCommandPreview(
	ctx context.Context,
	previewID, actorAdminID uuid.UUID,
	expectedVersion uint64,
	consumedAt time.Time,
) (adminuser.UserCommandPreview, error) {
	if repository == nil || repository.queries == nil || ctx == nil || previewID == uuid.Nil || actorAdminID == uuid.Nil ||
		expectedVersion == 0 || consumedAt.IsZero() {
		return adminuser.UserCommandPreview{}, adminuser.ErrInvalidInput
	}
	row, err := repository.queries.ConsumeAdminUserCommandPreviewCAS(ctx, sqlcgen.ConsumeAdminUserCommandPreviewCASParams{
		ConsumedAt: timeToPG(consumedAt), PreviewID: uuidToPG(previewID), ActorAdminID: uuidToPG(actorAdminID), ExpectedVersion: int64(expectedVersion),
	})
	if err != nil {
		return adminuser.UserCommandPreview{}, mapAdminUserQueryError(ctx, err, adminuser.ErrConflict)
	}
	return adminUserCommandPreviewFromRow(row)
}

// GetUserCommandReceipt returns the stored business result for an already-finished operation ID.
func (repository *AdminUserGovernanceRepository) GetUserCommandReceipt(
	ctx context.Context,
	actorAdminID uuid.UUID,
	operationID idempotency.OperationID,
) (adminuser.UserCommandReceipt, error) {
	if repository == nil || repository.queries == nil || ctx == nil || actorAdminID == uuid.Nil || !operationID.Valid() {
		return adminuser.UserCommandReceipt{}, adminuser.ErrInvalidInput
	}
	row, err := repository.queries.GetAdminUserCommandReceipt(ctx, sqlcgen.GetAdminUserCommandReceiptParams{
		ActorAdminID: uuidToPG(actorAdminID), OperationID: operationID.Value(),
	})
	if err != nil {
		return adminuser.UserCommandReceipt{}, mapAdminUserQueryError(ctx, err, adminuser.ErrNotFound)
	}
	return adminUserCommandReceiptFromRow(row)
}

// SaveUserCommandReceipt stores the exact completed outcome and replays the original row on same-digest retries.
func (repository *AdminUserGovernanceRepository) SaveUserCommandReceipt(
	ctx context.Context,
	receipt adminuser.UserCommandReceipt,
) (adminuser.UserCommandReceipt, error) {
	if repository == nil || repository.queries == nil || ctx == nil || !validUserCommandReceipt(receipt) {
		return adminuser.UserCommandReceipt{}, adminuser.ErrInvalidInput
	}
	row, err := repository.queries.CreateAdminUserCommandReceipt(ctx, sqlcgen.CreateAdminUserCommandReceiptParams{
		ActorAdminID: uuidToPG(receipt.ActorAdminID), OperationID: receipt.OperationID.Value(), RequestDigest: receipt.RequestDigest[:],
		PreviewID: uuidToPG(receipt.PreviewID), UserID: uuidToPG(receipt.UserID), Command: string(receipt.Command), Outcome: string(receipt.Outcome),
		UserVersion: int64(receipt.UserVersion), RevokedDevices: receipt.RevokedDevices, RemovedRooms: receipt.RemovedRooms,
		ErasureJobID: optionalUUID(receipt.ErasureJobID), AuditEventID: uuidToPG(receipt.AuditEventID), CompletedAt: timeToPG(receipt.CompletedAt),
	})
	if err == nil {
		return adminUserCommandReceiptFromRow(row)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return adminuser.UserCommandReceipt{}, adminuser.ErrIdempotencyConflict
	}
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) && pgError.Code == "23505" {
		return adminuser.UserCommandReceipt{}, adminuser.ErrIdempotencyConflict
	}
	return adminuser.UserCommandReceipt{}, mapAdminUserQueryError(ctx, err, adminuser.ErrConflict)
}

// GetUserState loads the current user governance status and version used by both previews and execution CAS checks.
func (repository *AdminUserGovernanceRepository) GetUserState(ctx context.Context, userID uuid.UUID) (adminuser.GovernanceUserState, error) {
	if repository == nil || repository.queries == nil || ctx == nil || userID == uuid.Nil {
		return adminuser.GovernanceUserState{}, adminuser.ErrInvalidInput
	}
	row, err := repository.queries.GetAdminUserGovernanceState(ctx, sqlcgen.GetAdminUserGovernanceStateParams{UserID: uuidToPG(userID)})
	if err != nil {
		return adminuser.GovernanceUserState{}, mapAdminUserQueryError(ctx, err, adminuser.ErrNotFound)
	}
	if !row.UserID.Valid || row.UserID.Bytes == uuid.Nil || row.AccountVersion <= 0 {
		return adminuser.GovernanceUserState{}, adminuser.ErrIntegrity
	}
	return adminuser.GovernanceUserState{UserID: row.UserID.Bytes, Status: row.Status, Version: uint64(row.AccountVersion)}, nil
}

// GetCurrentRoom returns the latest room binding that a room-removal preview or execution must still match.
func (repository *AdminUserGovernanceRepository) GetCurrentRoom(ctx context.Context, userID uuid.UUID) (adminuser.GovernanceRoomState, error) {
	if repository == nil || repository.queries == nil || ctx == nil || userID == uuid.Nil {
		return adminuser.GovernanceRoomState{}, adminuser.ErrInvalidInput
	}
	row, err := repository.queries.GetAdminCurrentRoomForUser(ctx, sqlcgen.GetAdminCurrentRoomForUserParams{UserID: uuidToPG(userID)})
	if err != nil {
		return adminuser.GovernanceRoomState{}, mapAdminUserQueryError(ctx, err, adminuser.ErrNotFound)
	}
	if !row.RoomID.Valid || row.RoomID.Bytes == uuid.Nil || row.RoomVersion <= 0 || row.MembershipVersion <= 0 {
		return adminuser.GovernanceRoomState{}, adminuser.ErrIntegrity
	}
	return adminuser.GovernanceRoomState{
		RoomID: row.RoomID.Bytes, RoomStatus: row.Status,
		ExpectedRoomVersion: uint64(row.RoomVersion), ExpectedMembershipVersion: uint64(row.MembershipVersion),
	}, nil
}

// TransitionUserStatus applies the exact reviewed active<->suspended transition using account_version as the CAS token.
func (repository *AdminUserGovernanceRepository) TransitionUserStatus(
	ctx context.Context,
	userID uuid.UUID,
	expectedVersion uint64,
	nextStatus string,
	changedAt time.Time,
) (adminuser.GovernanceUserState, error) {
	if repository == nil || repository.queries == nil || ctx == nil || userID == uuid.Nil || expectedVersion == 0 || changedAt.IsZero() {
		return adminuser.GovernanceUserState{}, adminuser.ErrInvalidInput
	}
	current, err := repository.GetUserState(ctx, userID)
	if err != nil {
		return adminuser.GovernanceUserState{}, err
	}
	if current.Version != expectedVersion || !validGovernanceTransition(current.Status, nextStatus) {
		return adminuser.GovernanceUserState{}, adminuser.ErrConflict
	}
	row, err := repository.queries.TransitionAdminUserStatusCAS(ctx, sqlcgen.TransitionAdminUserStatusCASParams{
		NextStatus: nextStatus, ChangedAt: timeToPG(changedAt), UserID: uuidToPG(userID),
		ExpectedVersion: int64(expectedVersion), ExpectedStatus: current.Status,
	})
	if err != nil {
		return adminuser.GovernanceUserState{}, mapAdminUserQueryError(ctx, err, adminuser.ErrConflict)
	}
	if !row.UserID.Valid || row.UserID.Bytes == uuid.Nil || row.AccountVersion <= int64(expectedVersion) {
		return adminuser.GovernanceUserState{}, adminuser.ErrIntegrity
	}
	return adminuser.GovernanceUserState{UserID: row.UserID.Bytes, Status: row.Status, Version: uint64(row.AccountVersion)}, nil
}

// RemoveUserFromRoom replays the reviewed membership removal against the current aggregate and records participant revocation if needed.
func (repository *AdminUserGovernanceRepository) RemoveUserFromRoom(
	ctx context.Context,
	adminID uuid.UUID,
	userID uuid.UUID,
	room adminuser.GovernanceRoomState,
	changedAt time.Time,
) error {
	if repository == nil || repository.runner == nil || repository.rooms == nil || ctx == nil || adminID == uuid.Nil ||
		userID == uuid.Nil || room.RoomID == uuid.Nil || room.ExpectedRoomVersion == 0 || room.ExpectedMembershipVersion == 0 || changedAt.IsZero() {
		return adminuser.ErrInvalidInput
	}
	before, err := repository.rooms.GetByID(ctx, room.RoomID)
	if err != nil {
		return mapAdminUserRoomError(err)
	}
	if before.Snapshot().MembershipVersion != room.ExpectedMembershipVersion || before.Snapshot().Status != roomdomain.RoomStatus(room.RoomStatus) {
		return adminuser.ErrConflict
	}
	next, removal, err := before.RemoveMemberByAdmin(
		roomdomain.AdminActor{ID: adminID}, userID,
		roomdomain.Version{Room: room.ExpectedRoomVersion, Membership: room.ExpectedMembershipVersion}, changedAt,
	)
	if err != nil {
		if errors.Is(err, roomdomain.ErrRoomVersionConflict) {
			return adminuser.ErrConflict
		}
		return mapAdminUserRoomError(err)
	}
	if room.RoomStatus == "playing" {
		return adminuser.ErrConflict
	}
	if removal.ParticipantRevoked {
		eventID, idErr := uuid.NewV7()
		if idErr != nil {
			return adminuser.ErrInvalidInput
		}
		event, eventErr := roomdomain.NewParticipantRevokedEvent(roomdomain.ParticipantRevocationFact{
			EventID: eventID, RoomID: room.RoomID, SessionID: removal.SessionID, UserID: userID,
			ActorKind: roomdomain.RemovalActorAdmin, ActorID: adminID, Reason: roomdomain.RemovalReasonAdminRemoved,
			MembershipVersion: removal.Version.Membership, OccurredAt: changedAt,
		})
		if eventErr != nil {
			return mapAdminUserRoomError(eventErr)
		}
		_, err = repository.rooms.CommitRemoval(ctx, before, next, event)
	} else {
		_, err = repository.rooms.UpdateCAS(ctx, before, next)
	}
	if err != nil {
		if errors.Is(err, roomdomain.ErrRoomVersionConflict) {
			return adminuser.ErrConflict
		}
		return mapAdminUserRoomError(err)
	}
	return nil
}

// CountActiveDevices counts only credentials that are both unrevoked and still usable at the reviewed timestamp.
func (repository *AdminUserGovernanceRepository) CountActiveDevices(ctx context.Context, userID uuid.UUID, at time.Time) (int32, error) {
	if repository == nil || repository.queries == nil || ctx == nil || userID == uuid.Nil || at.IsZero() {
		return 0, adminuser.ErrInvalidInput
	}
	if _, err := repository.GetUserState(ctx, userID); err != nil {
		return 0, err
	}
	count, err := repository.queries.CountActiveAdminUserDevices(ctx, sqlcgen.CountActiveAdminUserDevicesParams{
		UserID: uuidToPG(userID), ActiveAt: timeToPG(at),
	})
	if err != nil {
		return 0, mapAdminUserQueryError(ctx, err, adminuser.ErrRepositoryUnavailable)
	}
	if count < 0 {
		return 0, adminuser.ErrIntegrity
	}
	return count, nil
}

// RevokeAllDevices revokes exactly the credentials that CountActiveDevices would have counted at the same timestamp.
func (repository *AdminUserGovernanceRepository) RevokeAllDevices(ctx context.Context, userID uuid.UUID, at time.Time) (int32, error) {
	if repository == nil || repository.queries == nil || ctx == nil || userID == uuid.Nil || at.IsZero() {
		return 0, adminuser.ErrInvalidInput
	}
	if _, err := repository.GetUserState(ctx, userID); err != nil {
		return 0, err
	}
	count, err := repository.queries.RevokeActiveAdminUserDevices(ctx, sqlcgen.RevokeActiveAdminUserDevicesParams{
		RevokedAt: timeToPG(at), RevokeReason: pgtype.Text{String: adminUserDeviceRevokeReasonAdminRequested, Valid: true},
		UserID: uuidToPG(userID), ActiveAt: timeToPG(at),
	})
	if err != nil {
		return 0, mapAdminUserQueryError(ctx, err, adminuser.ErrConflict)
	}
	if count < 0 || count > int64(^uint32(0)>>1) {
		return 0, adminuser.ErrIntegrity
	}
	return int32(count), nil
}

// HasPendingExport evaluates queued and running export filters against the current user snapshot because export execution is deferred.
func (repository *AdminUserGovernanceRepository) HasPendingExport(ctx context.Context, userID uuid.UUID) (bool, error) {
	if repository == nil || repository.queries == nil || ctx == nil || userID == uuid.Nil {
		return false, adminuser.ErrInvalidInput
	}
	if _, err := repository.GetUserState(ctx, userID); err != nil {
		return false, err
	}
	pending, err := repository.queries.HasPendingAdminExportForUser(ctx, sqlcgen.HasPendingAdminExportForUserParams{UserID: uuidToPG(userID)})
	if err != nil {
		return false, mapAdminUserQueryError(ctx, err, adminuser.ErrRepositoryUnavailable)
	}
	return pending, nil
}

// DeleteUser clears the username claim, revokes active devices, marks the user deleted, and creates the erasure job in one transaction.
func (repository *AdminUserGovernanceRepository) DeleteUser(
	ctx context.Context,
	command adminuser.DeleteUserCommand,
) (adminuser.DeleteUserResult, error) {
	if repository == nil || repository.runner == nil || ctx == nil || command.ActorAdminID == uuid.Nil || command.UserID == uuid.Nil ||
		!command.OperationID.Valid() || command.ExpectedUserVersion == 0 || command.ChangedAt.IsZero() || !validAdminReason(command.Reason) {
		return adminuser.DeleteUserResult{}, adminuser.ErrInvalidInput
	}
	var result adminuser.DeleteUserResult
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		locked, err := queries.GetUserForUpdate(ctx, sqlcgen.GetUserForUpdateParams{UserID: uuidToPG(command.UserID)})
		if err != nil {
			return err
		}
		if !locked.UserID.Valid || locked.UserID.Bytes == uuid.Nil || locked.AccountVersion <= 0 {
			return adminuser.ErrIntegrity
		}
		if locked.AccountVersion != int64(command.ExpectedUserVersion) || (locked.Status != "active" && locked.Status != "suspended") {
			return adminuser.ErrConflict
		}
		updated, err := queries.DeleteAdminUserCAS(ctx, sqlcgen.DeleteAdminUserCASParams{
			ChangedAt: timeToPG(command.ChangedAt), UserID: uuidToPG(command.UserID), ExpectedVersion: int64(command.ExpectedUserVersion),
		})
		if err != nil {
			return err
		}
		revokedDevices, err := queries.RevokeActiveAdminUserDevices(ctx, sqlcgen.RevokeActiveAdminUserDevicesParams{
			RevokedAt: timeToPG(command.ChangedAt), RevokeReason: pgtype.Text{String: adminUserDeviceRevokeReasonAccountDeleted, Valid: true},
			UserID: uuidToPG(command.UserID), ActiveAt: timeToPG(command.ChangedAt),
		})
		if err != nil {
			return err
		}
		if locked.CurrentUsernameKey.Valid && locked.CurrentUsernameKey.String != "" {
			deletedClaims, deleteErr := queries.DeleteAdminUsernameClaimForUser(ctx, sqlcgen.DeleteAdminUsernameClaimForUserParams{
				UsernameKey: locked.CurrentUsernameKey.String, OwnerUserID: uuidToPG(command.UserID),
			})
			if deleteErr != nil {
				return deleteErr
			}
			// Active and suspended users must own the current claim they expose; a missing row means integrity drift.
			if deletedClaims != 1 {
				return adminuser.ErrIntegrity
			}
		}
		erasureJobID, idErr := uuid.NewV7()
		if idErr != nil {
			return adminuser.ErrInvalidInput
		}
		erasure, err := queries.CreateAdminUserErasureJob(ctx, sqlcgen.CreateAdminUserErasureJobParams{
			ErasureJobID: uuidToPG(erasureJobID), UserID: uuidToPG(command.UserID), ActorAdminID: uuidToPG(command.ActorAdminID),
			OperationID: command.OperationID.Value(), RequestDigest: command.RequestDigest[:],
			Reason: strings.TrimSpace(command.Reason), CreatedAt: timeToPG(command.ChangedAt),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return adminuser.ErrIdempotencyConflict
		}
		if err != nil {
			return err
		}
		if !updated.UserID.Valid || updated.UserID.Bytes == uuid.Nil || updated.AccountVersion <= int64(command.ExpectedUserVersion) ||
			!erasure.ErasureJobID.Valid || erasure.ErasureJobID.Bytes == uuid.Nil || revokedDevices < 0 || revokedDevices > int64(^uint32(0)>>1) {
			return adminuser.ErrIntegrity
		}
		result = adminuser.DeleteUserResult{
			User:           adminuser.GovernanceUserState{UserID: updated.UserID.Bytes, Status: updated.Status, Version: uint64(updated.AccountVersion)},
			RevokedDevices: int32(revokedDevices),
			ErasureJobID:   erasure.ErasureJobID.Bytes,
		}
		return nil
	})
	if err != nil {
		return adminuser.DeleteUserResult{}, mapAdminUserQueryError(ctx, err, adminuser.ErrConflict)
	}
	return result, nil
}

// EraseUserProfile removes the encrypted profile row entirely so subsequent PII reads observe that the deleted user has no remaining profile.
func (repository *AdminUserGovernanceRepository) EraseUserProfile(ctx context.Context, userID uuid.UUID) error {
	if repository == nil || repository.queries == nil || ctx == nil || userID == uuid.Nil {
		return adminuser.ErrInvalidInput
	}
	if _, err := repository.GetUserState(ctx, userID); err != nil {
		return err
	}
	if _, err := repository.queries.DeleteAdminUserProfile(ctx, sqlcgen.DeleteAdminUserProfileParams{UserID: uuidToPG(userID)}); err != nil {
		return mapAdminUserQueryError(ctx, err, adminuser.ErrRepositoryUnavailable)
	}
	return nil
}

func validGovernanceTransition(currentStatus, nextStatus string) bool {
	return currentStatus == "active" && nextStatus == "suspended" ||
		currentStatus == "suspended" && nextStatus == "active"
}

func validUserCommand(command adminuser.UserCommand) bool {
	return command == adminuser.UserCommandSuspend ||
		command == adminuser.UserCommandUnsuspend ||
		command == adminuser.UserCommandRevokeAllDevices ||
		command == adminuser.UserCommandRemoveFromCurrentRoom ||
		command == adminuser.UserCommandDelete
}

func validUserCommandOutcome(outcome adminuser.UserCommandOutcome) bool {
	return outcome == adminuser.UserCommandOutcomeExecuted ||
		outcome == adminuser.UserCommandOutcomeNoChange ||
		outcome == adminuser.UserCommandOutcomeRejected
}

func validUserCommandPreview(preview adminuser.UserCommandPreview) bool {
	if preview.ID == uuid.Nil || preview.ActorAdminID == uuid.Nil || preview.Snapshot.UserID == uuid.Nil || preview.Snapshot.SchemaVersion == 0 ||
		!validUserCommand(preview.Snapshot.Command) || preview.Snapshot.ExpectedUserVersion == 0 ||
		preview.AffectedDevices < 0 || preview.AffectedRooms < 0 || preview.SampledAt.IsZero() || !preview.ExpiresAt.After(preview.SampledAt) {
		return false
	}
	if preview.RequiredElevation != "" && !preview.RequiredElevation.Valid() {
		return false
	}
	for _, blocker := range preview.Blockers {
		if blocker.Type == "" || strings.TrimSpace(blocker.ResourceID) == "" || strings.TrimSpace(blocker.MessageKey) == "" {
			return false
		}
	}
	if preview.Snapshot.Room != nil {
		if preview.Snapshot.Room.RoomID == uuid.Nil || preview.Snapshot.Room.ExpectedRoomVersion == 0 || preview.Snapshot.Room.ExpectedMembershipVersion == 0 {
			return false
		}
	}
	return true
}

func validUserCommandReceipt(receipt adminuser.UserCommandReceipt) bool {
	return receipt.ActorAdminID != uuid.Nil && receipt.PreviewID != uuid.Nil && receipt.UserID != uuid.Nil &&
		receipt.AuditEventID != uuid.Nil && receipt.UserVersion > 0 && receipt.CompletedAt.Round(0).UTC() == receipt.CompletedAt &&
		!receipt.CompletedAt.IsZero() && receipt.OperationID.Valid() && validUserCommand(receipt.Command) &&
		validUserCommandOutcome(receipt.Outcome) && receipt.RevokedDevices >= 0 && receipt.RemovedRooms >= 0
}

func marshalUserCommandPreview(preview adminuser.UserCommandPreview) ([]byte, []byte, error) {
	snapshot, err := json.Marshal(preview.Snapshot)
	if err != nil || len(snapshot) == 0 || len(snapshot) > adminUserCommandSnapshotLimit {
		return nil, nil, adminuser.ErrInvalidInput
	}
	blockersPayload := preview.Blockers
	if blockersPayload == nil {
		blockersPayload = make([]adminuser.GovernanceBlocker, 0)
	}
	blockers, err := json.Marshal(blockersPayload)
	if err != nil || len(blockers) > adminUserCommandBlockersLimit {
		return nil, nil, adminuser.ErrInvalidInput
	}
	return snapshot, blockers, nil
}

func adminUserCommandPreviewFromRow(row sqlcgen.AdminUserCommandPreview) (adminuser.UserCommandPreview, error) {
	digest, ok := bytesToDigest(row.PreviewDigest)
	if !ok || !row.PreviewID.Valid || row.PreviewID.Bytes == uuid.Nil || !row.ActorAdminID.Valid || row.ActorAdminID.Bytes == uuid.Nil ||
		!row.UserID.Valid || row.UserID.Bytes == uuid.Nil || row.SnapshotSchemaVersion <= 0 || !json.Valid(row.Snapshot) || !json.Valid(row.Blockers) ||
		row.AffectedDevices < 0 || row.AffectedRooms < 0 || row.Version <= 0 || !row.SampledAt.Valid || !row.ExpiresAt.Valid {
		return adminuser.UserCommandPreview{}, adminuser.ErrIntegrity
	}
	var snapshot adminuser.UserCommandSnapshot
	if err := json.Unmarshal(row.Snapshot, &snapshot); err != nil || snapshot.UserID != row.UserID.Bytes || snapshot.SchemaVersion != uint32(row.SnapshotSchemaVersion) ||
		!validUserCommand(snapshot.Command) || snapshot.ExpectedUserVersion == 0 || string(snapshot.Command) != row.Command {
		return adminuser.UserCommandPreview{}, adminuser.ErrIntegrity
	}
	var blockers []adminuser.GovernanceBlocker
	if err := json.Unmarshal(row.Blockers, &blockers); err != nil {
		return adminuser.UserCommandPreview{}, adminuser.ErrIntegrity
	}
	requiredElevation := admin.ElevationScope(row.RequiredElevation.String)
	if row.RequiredElevation.Valid && !requiredElevation.Valid() {
		return adminuser.UserCommandPreview{}, adminuser.ErrIntegrity
	}
	preview := adminuser.UserCommandPreview{
		ID: row.PreviewID.Bytes, ActorAdminID: row.ActorAdminID.Bytes, Snapshot: snapshot, PreviewDigest: digest,
		AffectedDevices: row.AffectedDevices, AffectedRooms: row.AffectedRooms, Blockers: blockers,
		RequiredElevation: requiredElevation, SampledAt: canonicalPostgresTime(row.SampledAt), ExpiresAt: canonicalPostgresTime(row.ExpiresAt),
		ConsumedAt: canonicalPostgresTime(row.ConsumedAt), Version: uint64(row.Version),
	}
	return preview, nil
}

func adminUserCommandReceiptFromRow(row sqlcgen.AdminUserCommandReceipt) (adminuser.UserCommandReceipt, error) {
	digest, ok := bytesToDigest(row.RequestDigest)
	if !ok || !row.ActorAdminID.Valid || row.ActorAdminID.Bytes == uuid.Nil || !row.PreviewID.Valid || row.PreviewID.Bytes == uuid.Nil ||
		!row.UserID.Valid || row.UserID.Bytes == uuid.Nil || row.UserVersion <= 0 || row.RevokedDevices < 0 || row.RemovedRooms < 0 ||
		!row.AuditEventID.Valid || row.AuditEventID.Bytes == uuid.Nil || !row.CompletedAt.Valid {
		return adminuser.UserCommandReceipt{}, adminuser.ErrIntegrity
	}
	operationID, err := idempotency.ParseOperationID(row.OperationID)
	if err != nil {
		return adminuser.UserCommandReceipt{}, adminuser.ErrIntegrity
	}
	command := adminuser.UserCommand(row.Command)
	outcome := adminuser.UserCommandOutcome(row.Outcome)
	if !validUserCommand(command) || !validUserCommandOutcome(outcome) {
		return adminuser.UserCommandReceipt{}, adminuser.ErrIntegrity
	}
	return adminuser.UserCommandReceipt{
		ActorAdminID: row.ActorAdminID.Bytes, OperationID: operationID, RequestDigest: digest, PreviewID: row.PreviewID.Bytes,
		UserID: row.UserID.Bytes, Command: command, Outcome: outcome, UserVersion: uint64(row.UserVersion),
		RevokedDevices: row.RevokedDevices, RemovedRooms: row.RemovedRooms, ErasureJobID: row.ErasureJobID.Bytes,
		AuditEventID: row.AuditEventID.Bytes, CompletedAt: canonicalPostgresTime(row.CompletedAt),
	}, nil
}

func mapAdminUserRoomError(err error) error {
	switch {
	case errors.Is(err, roomdomain.ErrInvalidRoomInput):
		return adminuser.ErrInvalidInput
	case errors.Is(err, roomdomain.ErrRoomNotFound), errors.Is(err, roomdomain.ErrMemberNotFound):
		return adminuser.ErrNotFound
	case errors.Is(err, roomdomain.ErrRoomVersionConflict), errors.Is(err, roomdomain.ErrSessionActive):
		return adminuser.ErrConflict
	case errors.Is(err, roomdomain.ErrRoomRepositoryUnavailable):
		return adminuser.ErrRepositoryUnavailable
	default:
		return err
	}
}

var _ adminuser.UserCommandRepository = (*AdminUserGovernanceRepository)(nil)
var _ adminuser.SingleUserGovernanceExecutor = (*AdminUserGovernanceRepository)(nil)
