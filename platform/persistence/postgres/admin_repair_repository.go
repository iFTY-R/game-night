package postgres

import (
	"context"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	adminroom "github.com/iFTY-R/game-night/platform/admin/room"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AdminRepairRepository struct {
	queries *sqlcgen.Queries
}

func NewAdminRepairRepository(pool *pgxpool.Pool) *AdminRepairRepository {
	return &AdminRepairRepository{queries: sqlcgen.New(pool)}
}

func (repository *AdminRepairRepository) CreateRepairOperation(
	ctx context.Context,
	command adminroom.CreateRepairOperationCommand,
) (adminroom.RepairOperation, error) {
	if repository == nil || repository.queries == nil || ctx == nil || !validCreateRepairOperation(command.RepairOperation) {
		return adminroom.RepairOperation{}, adminroom.ErrInvalidInput
	}
	row, err := repository.queries.AdminCreateRepairOperation(ctx, sqlcgen.AdminCreateRepairOperationParams{
		RepairID: uuidToPG(command.RepairID), RepairType: string(command.RepairType), TargetID: uuidToPG(command.TargetID),
		TargetKind: command.TargetKind, TargetDigest: cloneBytes(command.TargetDigest), PreviewDigest: cloneBytes(command.PreviewDigest),
		CommandVersion: int64(command.CommandVersion), ExpectedRoomVersion: uint64ToOptionalInt8(command.ExpectedRoomVersion),
		ExpectedMembershipVersion: uint64ToOptionalInt8(command.ExpectedMembershipVersion),
		ExpectedStateVersion:      uint64ToOptionalInt8(command.ExpectedStateVersion),
		ExpectedOwnershipEpoch:    uint64ToOptionalInt8(command.ExpectedOwnershipEpoch),
		Summary:                   strings.TrimSpace(command.Summary), IrreversibleEffects: append([]string(nil), command.IrreversibleEffects...),
		BeforeSnapshotDigest: cloneBytes(command.BeforeSnapshotDigest), AfterSnapshotDigest: cloneBytes(command.AfterSnapshotDigest),
		RequestedByAdminID: uuidToPG(command.RequestedByAdminID), Reason: strings.TrimSpace(command.Reason),
		CreatedAt: timeToPG(command.CreatedAt), ExpiresAt: timeToPG(command.ExpiresAt),
	})
	if err != nil {
		return adminroom.RepairOperation{}, mapAdminRoomQueryError(ctx, err, adminroom.ErrConflict)
	}
	return adminRepairOperationFromRow(row)
}

func (repository *AdminRepairRepository) GetRepairOperation(ctx context.Context, repairID uuid.UUID) (adminroom.RepairOperation, error) {
	if repository == nil || repository.queries == nil || ctx == nil || repairID == uuid.Nil {
		return adminroom.RepairOperation{}, adminroom.ErrInvalidInput
	}
	row, err := repository.queries.AdminGetRepairOperation(ctx, sqlcgen.AdminGetRepairOperationParams{RepairID: uuidToPG(repairID)})
	if err != nil {
		return adminroom.RepairOperation{}, mapAdminRoomQueryError(ctx, err, adminroom.ErrNotFound)
	}
	return adminRepairOperationFromRow(row)
}

func (repository *AdminRepairRepository) ExpireRepairOperation(
	ctx context.Context,
	repairID uuid.UUID,
	expectedVersion uint64,
) (adminroom.RepairOperation, error) {
	if repository == nil || repository.queries == nil || ctx == nil || repairID == uuid.Nil || expectedVersion == 0 || expectedVersion > math.MaxInt64 {
		return adminroom.RepairOperation{}, adminroom.ErrInvalidInput
	}
	row, err := repository.queries.AdminExpireRepairOperationCAS(ctx, sqlcgen.AdminExpireRepairOperationCASParams{
		RepairID: uuidToPG(repairID), ExpectedVersion: int64(expectedVersion), ExpiredAt: timeToPG(time.Now().UTC()),
	})
	if err != nil {
		return adminroom.RepairOperation{}, mapAdminRoomQueryError(ctx, err, adminroom.ErrConflict)
	}
	return adminRepairOperationFromRow(row)
}

func (repository *AdminRepairRepository) CompleteRepairOperation(
	ctx context.Context,
	command adminroom.CompleteRepairOperationCommand,
) (adminroom.RepairOperation, error) {
	if repository == nil || repository.queries == nil || ctx == nil || !validCompleteRepairOperation(command) {
		return adminroom.RepairOperation{}, adminroom.ErrInvalidInput
	}
	row, err := repository.queries.AdminExecuteRepairOperationCAS(ctx, sqlcgen.AdminExecuteRepairOperationCASParams{
		State: command.State, OperationID: pgtype.Text{String: command.OperationID, Valid: true}, RequestDigest: cloneBytes(command.RequestDigest),
		AuditEventID: uuidToPG(command.AuditEventID), AfterSnapshotDigest: cloneBytes(command.AfterSnapshotDigest),
		Reason: strings.TrimSpace(command.Reason), ExecutedAt: timeToPG(command.ExecutedAt), RepairID: uuidToPG(command.RepairID),
		ExpectedVersion: int64(command.ExpectedVersion),
	})
	if err != nil {
		return adminroom.RepairOperation{}, mapAdminRoomQueryError(ctx, err, adminroom.ErrConflict)
	}
	return adminRepairOperationFromRow(row)
}

func validCreateRepairOperation(operation adminroom.RepairOperation) bool {
	return operation.RepairID != uuid.Nil && operation.TargetID != uuid.Nil && operation.RequestedByAdminID != uuid.Nil &&
		validRepairType(operation.RepairType) && validRepairTargetKind(operation.TargetKind) && operation.CommandVersion > 0 &&
		operation.CommandVersion <= math.MaxInt64 && validDigest(operation.TargetDigest) && validDigest(operation.PreviewDigest) &&
		validDigest(operation.BeforeSnapshotDigest) && validOptionalDigest(operation.AfterSnapshotDigest) &&
		validRepairVersions(operation) && validRepairSummary(operation.Summary) && validRepairEffects(operation.IrreversibleEffects) &&
		validAdminReason(operation.Reason) && !operation.CreatedAt.IsZero() && operation.ExpiresAt.After(operation.CreatedAt)
}

func validCompleteRepairOperation(command adminroom.CompleteRepairOperationCommand) bool {
	return command.RepairID != uuid.Nil && command.AuditEventID != uuid.Nil && validOperationID(command.OperationID) &&
		validDigest(command.RequestDigest) && validDigest(command.AfterSnapshotDigest) && validAdminReason(command.Reason) &&
		(command.State == adminroom.RepairStateExecuted || command.State == adminroom.RepairStateRejected) &&
		command.ExpectedVersion > 0 && command.ExpectedVersion <= math.MaxInt64 && !command.ExecutedAt.IsZero()
}

func validRepairType(value adminroom.RepairType) bool {
	switch value {
	case adminroom.RepairClearStaleOwnerLease, adminroom.RepairTerminateUnrecoverable, adminroom.RepairRepairRoomGameLink:
		return true
	default:
		return false
	}
}

func validRepairTargetKind(value string) bool {
	return value == adminroom.RepairTargetKindRoom || value == adminroom.RepairTargetKindGameSession
}

func validRepairVersions(operation adminroom.RepairOperation) bool {
	return operation.ExpectedRoomVersion <= math.MaxInt64 && operation.ExpectedMembershipVersion <= math.MaxInt64 &&
		operation.ExpectedStateVersion <= math.MaxInt64 && operation.ExpectedOwnershipEpoch <= math.MaxInt64
}

func validRepairSummary(value string) bool {
	trimmed := strings.TrimSpace(value)
	length := utf8.RuneCountInString(trimmed)
	return utf8.ValidString(trimmed) && length >= 1 && length <= 2000
}

func validRepairEffects(values []string) bool {
	if len(values) > 16 {
		return false
	}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		length := utf8.RuneCountInString(trimmed)
		if !utf8.ValidString(trimmed) || length == 0 || length > 256 {
			return false
		}
	}
	return true
}

func validOperationID(value string) bool {
	length := len(value)
	if length < 22 || length > 86 {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func validDigest(value []byte) bool {
	return len(value) == 32
}

func validOptionalDigest(value []byte) bool {
	return len(value) == 0 || len(value) == 32
}

func cloneBytes(value []byte) []byte {
	if len(value) == 0 {
		return nil
	}
	return append([]byte(nil), value...)
}

func adminRepairOperationFromRow(row sqlcgen.AdminRepairOperation) (adminroom.RepairOperation, error) {
	if !row.RepairID.Valid || row.RepairID.Bytes == uuid.Nil || !row.TargetID.Valid || row.TargetID.Bytes == uuid.Nil ||
		!row.RequestedByAdminID.Valid || row.RequestedByAdminID.Bytes == uuid.Nil || row.CommandVersion <= 0 ||
		row.Version <= 0 || !row.CreatedAt.Valid || !row.ExpiresAt.Valid || !validDigest(row.TargetDigest) ||
		!validDigest(row.PreviewDigest) || !validDigest(row.BeforeSnapshotDigest) || !validOptionalDigest(row.AfterSnapshotDigest) {
		return adminroom.RepairOperation{}, adminroom.ErrIntegrity
	}
	operation := adminroom.RepairOperation{
		RepairID: uuid.UUID(row.RepairID.Bytes), RepairType: adminroom.RepairType(row.RepairType), State: row.State,
		TargetID: uuid.UUID(row.TargetID.Bytes), TargetKind: row.TargetKind, TargetDigest: cloneBytes(row.TargetDigest),
		PreviewDigest: cloneBytes(row.PreviewDigest), CommandVersion: uint64(row.CommandVersion),
		ExpectedRoomVersion:       optionalInt8ToUint64(row.ExpectedRoomVersion),
		ExpectedMembershipVersion: optionalInt8ToUint64(row.ExpectedMembershipVersion),
		ExpectedStateVersion:      optionalInt8ToUint64(row.ExpectedStateVersion),
		ExpectedOwnershipEpoch:    optionalInt8ToUint64(row.ExpectedOwnershipEpoch),
		Summary:                   row.Summary, IrreversibleEffects: append([]string(nil), row.IrreversibleEffects...),
		BeforeSnapshotDigest: cloneBytes(row.BeforeSnapshotDigest), AfterSnapshotDigest: cloneBytes(row.AfterSnapshotDigest),
		RequestedByAdminID: uuid.UUID(row.RequestedByAdminID.Bytes), OperationID: optionalTextFromPG(row.OperationID),
		RequestDigest: cloneBytes(row.RequestDigest), AuditEventID: optionalUUIDFromPG(row.AuditEventID),
		Reason: row.Reason, Version: uint64(row.Version), CreatedAt: canonicalPostgresTime(row.CreatedAt),
		ExpiresAt: canonicalPostgresTime(row.ExpiresAt), ExecutedAt: optionalTimeFromPG(row.ExecutedAt),
	}
	if !validRepairType(operation.RepairType) || !validRepairTargetKind(operation.TargetKind) {
		return adminroom.RepairOperation{}, adminroom.ErrIntegrity
	}
	return operation, nil
}

func optionalInt8ToUint64(value pgtype.Int8) uint64 {
	if !value.Valid {
		return 0
	}
	return uint64(value.Int64)
}

var _ adminroom.RepairRepository = (*AdminRepairRepository)(nil)
