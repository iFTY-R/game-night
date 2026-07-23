package postgres

import (
	"bytes"
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	ruleOperationDraftUpdate       = "draft_update"
	ruleOperationPresetSave        = "preset_save"
	ruleOperationPresetDelete      = "preset_delete"
	ruleOperationPendingStartBegin = "pending_start_begin"
)

// RuleRepository persists room-scoped game rule drafts, user presets, and pending starts.
// Idempotent write methods record their original result snapshot so retries do not observe later edits.
type RuleRepository struct {
	runner *TransactionRunner
}

// NewRuleRepository binds durable rule persistence to the supplied PostgreSQL pool.
func NewRuleRepository(pool *pgxpool.Pool) *RuleRepository {
	return &RuleRepository{runner: NewTransactionRunner(pool)}
}

// ListDrafts returns the current draft for each game edited in a room.
func (repository *RuleRepository) ListDrafts(ctx context.Context, roomID uuid.UUID) ([]roomDomain.RuleDraft, error) {
	if err := validateRuleRepositoryContext(ctx, roomID); err != nil {
		return nil, err
	}
	var rows []sqlcgen.RoomGameConfigDraft
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		var err error
		rows, err = queries.ListRoomGameConfigDrafts(ctx, sqlcgen.ListRoomGameConfigDraftsParams{RoomID: uuidToPG(roomID)})
		return err
	})
	if err != nil {
		return nil, mapRuleRepositoryError(ctx, err, roomDomain.ErrRuleNotFound)
	}
	drafts := make([]roomDomain.RuleDraft, 0, len(rows))
	for _, row := range rows {
		draft, mapErr := ruleDraftFromRow(row)
		if mapErr != nil {
			return nil, mapErr
		}
		drafts = append(drafts, draft)
	}
	return drafts, nil
}

// GetDraft returns one room/game draft by primary key.
func (repository *RuleRepository) GetDraft(ctx context.Context, roomID uuid.UUID, gameID string) (roomDomain.RuleDraft, error) {
	if err := validateRuleRepositoryContext(ctx, roomID); err != nil {
		return roomDomain.RuleDraft{}, err
	}
	gameID = strings.TrimSpace(gameID)
	if gameID == "" {
		return roomDomain.RuleDraft{}, roomDomain.ErrInvalidRoomInput
	}
	var row sqlcgen.RoomGameConfigDraft
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		var err error
		row, err = queries.GetRoomGameConfigDraft(ctx, sqlcgen.GetRoomGameConfigDraftParams{RoomID: uuidToPG(roomID), GameID: gameID})
		return err
	})
	if err != nil {
		return roomDomain.RuleDraft{}, mapRuleRepositoryError(ctx, err, roomDomain.ErrRuleNotFound)
	}
	return ruleDraftFromRow(row)
}

// UpdateDraft applies one host edit after checking room version, membership version, ownership epoch, and draft revision.
func (repository *RuleRepository) UpdateDraft(ctx context.Context, update roomDomain.RuleDraftUpdate) (roomDomain.RuleDraft, error) {
	if err := validateRuleRepositoryContext(ctx, update.RoomID); err != nil {
		return roomDomain.RuleDraft{}, err
	}
	update.GameID = strings.TrimSpace(update.GameID)
	if update.RequestDigest == ([32]byte{}) {
		update.RequestDigest = update.Config.Digest()
	}
	if update.ActorUserID == uuid.Nil || update.OwnershipEpoch == 0 || update.OperationID == "" || update.GameID == "" ||
		update.Config.GameID != update.GameID || update.Expected.Room == 0 || update.Expected.Membership == 0 || update.At.IsZero() ||
		!update.Config.Valid() || !ruleConfigFitsPostgres(update.Config) || update.ExpectedRevision > math.MaxInt64 {
		return roomDomain.RuleDraft{}, roomDomain.ErrInvalidRoomInput
	}
	var stored roomDomain.RuleDraft
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		if err := lockRuleOperation(ctx, queries, ruleOperationDraftUpdate, update.OperationID); err != nil {
			return err
		}
		if operation, ok, err := getRuleOperation(ctx, queries, ruleOperationDraftUpdate, update.OperationID); err != nil || ok {
			if err != nil {
				return err
			}
			replayed, replayErr := draftFromOperation(operation, update.RequestDigest)
			if replayErr != nil {
				return replayErr
			}
			stored = replayed
			return nil
		}
		roomRow, err := queries.GetPartyRoomForUpdate(ctx, sqlcgen.GetPartyRoomForUpdateParams{RoomID: uuidToPG(update.RoomID)})
		if err != nil {
			return mapNoRows(err, roomDomain.ErrRuleNotFound)
		}
		if err := validateRuleRoomFence(roomRow, update.ActorUserID, update.Expected, update.OwnershipEpoch); err != nil {
			return err
		}
		current, err := queries.GetRoomGameConfigDraftForUpdate(ctx, sqlcgen.GetRoomGameConfigDraftForUpdateParams{
			RoomID: uuidToPG(update.RoomID), GameID: update.GameID,
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if errors.Is(err, pgx.ErrNoRows) {
			if update.ExpectedRevision != 0 {
				return roomDomain.ErrRuleRevisionConflict
			}
			row, createErr := queries.CreateRoomGameConfigDraft(ctx, createRuleDraftParams(update, 1))
			if createErr != nil {
				return createErr
			}
			draft, mapErr := ruleDraftFromRow(row)
			if mapErr != nil {
				return mapErr
			}
			stored = draft
		} else {
			if uint64(current.Revision) != update.ExpectedRevision {
				return roomDomain.ErrRuleRevisionConflict
			}
			row, updateErr := queries.UpdateRoomGameConfigDraft(ctx, updateRuleDraftParams(update, update.ExpectedRevision+1))
			if updateErr != nil {
				return updateErr
			}
			draft, mapErr := ruleDraftFromRow(row)
			if mapErr != nil {
				return mapErr
			}
			stored = draft
		}
		_, err = queries.CreateRoomRuleOperationRecord(ctx, operationParamsForDraft(update.OperationID, update.RequestDigest, stored, update.At))
		return err
	})
	if err != nil {
		return roomDomain.RuleDraft{}, mapRuleRepositoryError(ctx, err, roomDomain.ErrRuleNotFound,
			roomDomain.ErrInvalidRoomInput, roomDomain.ErrRuleRevisionConflict, roomDomain.ErrRulePermission,
			roomDomain.ErrRuleOperationConflict, roomDomain.ErrRoomVersionConflict, roomDomain.ErrRoomIntegrity)
	}
	return stored, nil
}

// ListPresets returns presets owned by one user, optionally narrowed to one game.
func (repository *RuleRepository) ListPresets(ctx context.Context, ownerID uuid.UUID, gameID string) ([]roomDomain.RulePreset, error) {
	if err := validateRuleRepositoryContext(ctx, ownerID); err != nil {
		return nil, err
	}
	gameID = strings.TrimSpace(gameID)
	var rows []sqlcgen.GameRulePreset
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		var err error
		rows, err = queries.ListGameRulePresets(ctx, sqlcgen.ListGameRulePresetsParams{OwnerUserID: uuidToPG(ownerID), GameID: gameID})
		return err
	})
	if err != nil {
		return nil, mapRuleRepositoryError(ctx, err, roomDomain.ErrRuleNotFound)
	}
	presets := make([]roomDomain.RulePreset, 0, len(rows))
	for _, row := range rows {
		preset, mapErr := rulePresetFromRow(row)
		if mapErr != nil {
			return nil, mapErr
		}
		presets = append(presets, preset)
	}
	return presets, nil
}

// SavePreset creates, updates, or copies an owner-scoped reusable rule preset.
func (repository *RuleRepository) SavePreset(ctx context.Context, write roomDomain.RulePresetWrite) (roomDomain.RulePreset, error) {
	if err := validateRuleRepositoryContext(ctx, write.OwnerUserID); err != nil {
		return roomDomain.RulePreset{}, err
	}
	write.GameID, write.Name = strings.TrimSpace(write.GameID), strings.TrimSpace(write.Name)
	if write.OwnerUserID == uuid.Nil || write.GameID == "" || write.Name == "" || write.OperationID == "" ||
		write.Config.GameID != write.GameID || write.At.IsZero() || !write.Config.Valid() ||
		!ruleConfigFitsPostgres(write.Config) || write.ExpectedRevision > math.MaxInt64 {
		return roomDomain.RulePreset{}, roomDomain.ErrInvalidRoomInput
	}
	if write.Copy {
		write.PresetID = uuid.New()
		write.ExpectedRevision = 0
	} else if write.PresetID == uuid.Nil {
		write.PresetID = uuid.New()
	}
	var stored roomDomain.RulePreset
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		if err := lockRuleOperation(ctx, queries, ruleOperationPresetSave, write.OperationID); err != nil {
			return err
		}
		if operation, ok, err := getRuleOperation(ctx, queries, ruleOperationPresetSave, write.OperationID); err != nil || ok {
			if err != nil {
				return err
			}
			replayed, replayErr := presetFromOperation(operation, write.RequestDigest)
			if replayErr != nil {
				return replayErr
			}
			stored = replayed
			return nil
		}
		current, err := queries.GetGameRulePresetForUpdate(ctx, sqlcgen.GetGameRulePresetForUpdateParams{PresetID: uuidToPG(write.PresetID)})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if errors.Is(err, pgx.ErrNoRows) {
			if write.ExpectedRevision != 0 {
				return roomDomain.ErrRuleRevisionConflict
			}
			row, createErr := queries.CreateGameRulePreset(ctx, createRulePresetParams(write, 1, write.At))
			if createErr != nil {
				return createErr
			}
			preset, mapErr := rulePresetFromRow(row)
			if mapErr != nil {
				return mapErr
			}
			stored = preset
		} else {
			if uuid.UUID(current.OwnerUserID.Bytes) != write.OwnerUserID || current.GameID != write.GameID {
				return roomDomain.ErrRulePermission
			}
			if uint64(current.Revision) != write.ExpectedRevision {
				return roomDomain.ErrRuleRevisionConflict
			}
			row, updateErr := queries.UpdateGameRulePreset(ctx, updateRulePresetParams(write, write.ExpectedRevision+1))
			if updateErr != nil {
				return updateErr
			}
			preset, mapErr := rulePresetFromRow(row)
			if mapErr != nil {
				return mapErr
			}
			stored = preset
		}
		_, err = queries.CreateRoomRuleOperationRecord(ctx, operationParamsForPreset(write.OperationID, write.RequestDigest, stored, write.At))
		return err
	})
	if err != nil {
		return roomDomain.RulePreset{}, mapRuleRepositoryError(ctx, err, roomDomain.ErrRuleNotFound,
			roomDomain.ErrInvalidRoomInput, roomDomain.ErrRuleRevisionConflict, roomDomain.ErrRulePermission,
			roomDomain.ErrRuleOperationConflict, roomDomain.ErrRoomIntegrity)
	}
	return stored, nil
}

// DeletePreset removes one owner preset with optimistic revision and operation-id replay protection.
func (repository *RuleRepository) DeletePreset(ctx context.Context, ownerID, presetID uuid.UUID, expectedRevision uint64, operationID string, digest [32]byte) error {
	if err := validateRuleRepositoryContext(ctx, ownerID); err != nil {
		return err
	}
	if presetID == uuid.Nil || operationID == "" || expectedRevision == 0 || expectedRevision > math.MaxInt64 {
		return roomDomain.ErrInvalidRoomInput
	}
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		if err := lockRuleOperation(ctx, queries, ruleOperationPresetDelete, operationID); err != nil {
			return err
		}
		if operation, ok, err := getRuleOperation(ctx, queries, ruleOperationPresetDelete, operationID); err != nil || ok {
			if err != nil {
				return err
			}
			return validateOperationReplay(operation, ruleOperationPresetDelete, digest)
		}
		current, err := queries.GetGameRulePresetForUpdate(ctx, sqlcgen.GetGameRulePresetForUpdateParams{PresetID: uuidToPG(presetID)})
		if err != nil {
			return mapNoRows(err, roomDomain.ErrRuleNotFound)
		}
		if uuid.UUID(current.OwnerUserID.Bytes) != ownerID {
			return roomDomain.ErrRulePermission
		}
		if uint64(current.Revision) != expectedRevision {
			return roomDomain.ErrRuleRevisionConflict
		}
		if err := queries.DeleteGameRulePreset(ctx, sqlcgen.DeleteGameRulePresetParams{
			PresetID: uuidToPG(presetID), OwnerUserID: uuidToPG(ownerID), ExpectedRevision: int64(expectedRevision),
		}); err != nil {
			return err
		}
		_, err = queries.CreateRoomRuleOperationRecord(ctx, operationParamsForPresetDelete(operationID, digest, ownerID, presetID, time.Now().UTC()))
		return err
	})
	if err != nil {
		return mapRuleRepositoryError(ctx, err, roomDomain.ErrRuleNotFound,
			roomDomain.ErrInvalidRoomInput, roomDomain.ErrRuleRevisionConflict, roomDomain.ErrRulePermission,
			roomDomain.ErrRuleOperationConflict)
	}
	return nil
}

// BeginPendingStart creates one durable countdown for a host-owned room.
func (repository *RuleRepository) BeginPendingStart(ctx context.Context, create roomDomain.PendingStartCreate) (roomDomain.PendingStart, error) {
	if err := validateRuleRepositoryContext(ctx, create.RoomID); err != nil {
		return roomDomain.PendingStart{}, err
	}
	create.GameID = strings.TrimSpace(create.GameID)
	if create.ActorUserID == uuid.Nil || create.GameID == "" || create.ConfigRevision == 0 || create.Expected.Room == 0 ||
		create.Expected.Membership == 0 || create.OwnershipEpoch == 0 || create.OperationID == "" || create.Deadline.Before(create.At) ||
		create.At.IsZero() || create.ConfigRevision > math.MaxInt64 || create.Expected.Room > math.MaxInt64 ||
		create.Expected.Membership > math.MaxInt64 || create.OwnershipEpoch > math.MaxInt64 {
		return roomDomain.PendingStart{}, roomDomain.ErrInvalidRoomInput
	}
	var stored roomDomain.PendingStart
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		if err := lockRuleOperation(ctx, queries, ruleOperationPendingStartBegin, create.OperationID); err != nil {
			return err
		}
		if operation, ok, err := getRuleOperation(ctx, queries, ruleOperationPendingStartBegin, create.OperationID); err != nil || ok {
			if err != nil {
				return err
			}
			replayed, replayErr := pendingStartFromOperation(operation, create.RequestDigest)
			if replayErr != nil {
				return replayErr
			}
			stored = replayed
			return nil
		}
		roomRow, err := queries.GetPartyRoomForUpdate(ctx, sqlcgen.GetPartyRoomForUpdateParams{RoomID: uuidToPG(create.RoomID)})
		if err != nil {
			return mapNoRows(err, roomDomain.ErrRuleNotFound)
		}
		if err := validateRuleRoomFence(roomRow, create.ActorUserID, create.Expected, create.OwnershipEpoch); err != nil {
			return err
		}
		if err := queries.ExpireRoomPendingStarts(ctx, sqlcgen.ExpireRoomPendingStartsParams{
			RoomID: uuidToPG(create.RoomID), CancelledAt: timeToPG(create.At),
		}); err != nil {
			return err
		}
		row, err := queries.CreateRoomPendingStart(ctx, sqlcgen.CreateRoomPendingStartParams{
			PendingStartID:            uuidToPG(uuid.New()),
			RoomID:                    uuidToPG(create.RoomID),
			CancelToken:               uuid.NewString(),
			GameID:                    create.GameID,
			ConfigRevision:            int64(create.ConfigRevision),
			ExpectedRoomVersion:       int64(create.Expected.Room),
			ExpectedMembershipVersion: int64(create.Expected.Membership),
			OwnershipEpoch:            int64(create.OwnershipEpoch),
			OperationID:               create.OperationID,
			RequestDigest:             digestToBytes(create.RequestDigest),
			DeadlineAt:                timeToPG(create.Deadline),
			CreatedAt:                 timeToPG(create.At),
		})
		if err != nil {
			return err
		}
		start, mapErr := pendingStartFromRow(row)
		if mapErr != nil {
			return mapErr
		}
		stored = start
		_, err = queries.CreateRoomRuleOperationRecord(ctx, operationParamsForPendingStart(create.OperationID, create.RequestDigest, stored, create.At))
		return err
	})
	if err != nil {
		return roomDomain.PendingStart{}, mapRuleRepositoryError(ctx, err, roomDomain.ErrRuleNotFound,
			roomDomain.ErrInvalidRoomInput, roomDomain.ErrRulePermission, roomDomain.ErrRuleOperationConflict,
			roomDomain.ErrPendingStartInvalid, roomDomain.ErrRoomVersionConflict, roomDomain.ErrRoomIntegrity)
	}
	return stored, nil
}

// GetPendingStart returns the latest countdown row for the room.
func (repository *RuleRepository) GetPendingStart(ctx context.Context, roomID uuid.UUID) (roomDomain.PendingStart, error) {
	if err := validateRuleRepositoryContext(ctx, roomID); err != nil {
		return roomDomain.PendingStart{}, err
	}
	var row sqlcgen.RoomPendingStart
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		var err error
		row, err = queries.GetLatestRoomPendingStart(ctx, sqlcgen.GetLatestRoomPendingStartParams{RoomID: uuidToPG(roomID)})
		return err
	})
	if err != nil {
		return roomDomain.PendingStart{}, mapRuleRepositoryError(ctx, err, roomDomain.ErrRuleNotFound)
	}
	return pendingStartFromRow(row)
}

// CancelPendingStart marks a countdown cancelled when token, epoch, and deadline still match.
func (repository *RuleRepository) CancelPendingStart(ctx context.Context, roomID, pendingID uuid.UUID, cancelToken string, ownershipEpoch uint64, _ [32]byte, at time.Time) error {
	if err := validateRuleRepositoryContext(ctx, roomID); err != nil {
		return err
	}
	if pendingID == uuid.Nil || cancelToken == "" || ownershipEpoch == 0 || ownershipEpoch > math.MaxInt64 || at.IsZero() {
		return roomDomain.ErrInvalidRoomInput
	}
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		_, err := queries.CancelRoomPendingStart(ctx, sqlcgen.CancelRoomPendingStartParams{
			RoomID: uuidToPG(roomID), PendingStartID: uuidToPG(pendingID), CancelToken: cancelToken,
			OwnershipEpoch: int64(ownershipEpoch), CancelledAt: timeToPG(at),
		})
		return mapNoRows(err, roomDomain.ErrPendingStartInvalid)
	})
	if err != nil {
		return mapRuleRepositoryError(ctx, err, roomDomain.ErrPendingStartInvalid,
			roomDomain.ErrInvalidRoomInput, roomDomain.ErrPendingStartInvalid)
	}
	return nil
}

// ConsumePendingStart marks a matured countdown consumed and returns the stored start snapshot.
func (repository *RuleRepository) ConsumePendingStart(ctx context.Context, roomID, pendingID uuid.UUID, cancelToken, operationID string, digest [32]byte, at time.Time) (roomDomain.PendingStart, error) {
	if err := validateRuleRepositoryContext(ctx, roomID); err != nil {
		return roomDomain.PendingStart{}, err
	}
	if pendingID == uuid.Nil || cancelToken == "" || operationID == "" || at.IsZero() {
		return roomDomain.PendingStart{}, roomDomain.ErrInvalidRoomInput
	}
	var stored roomDomain.PendingStart
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		row, err := queries.ConsumeRoomPendingStart(ctx, sqlcgen.ConsumeRoomPendingStartParams{
			RoomID: uuidToPG(roomID), PendingStartID: uuidToPG(pendingID), CancelToken: cancelToken,
			OperationID: operationID, RequestDigest: digestToBytes(digest), ConsumedAt: timeToPG(at),
		})
		if err != nil {
			return mapNoRows(err, roomDomain.ErrPendingStartInvalid)
		}
		start, mapErr := pendingStartFromRow(row)
		if mapErr != nil {
			return mapErr
		}
		stored = start
		return nil
	})
	if err != nil {
		return roomDomain.PendingStart{}, mapRuleRepositoryError(ctx, err, roomDomain.ErrPendingStartInvalid,
			roomDomain.ErrInvalidRoomInput, roomDomain.ErrPendingStartInvalid, roomDomain.ErrRoomIntegrity)
	}
	return stored, nil
}

func validateRuleRepositoryContext(ctx context.Context, id uuid.UUID) error {
	if ctx == nil || id == uuid.Nil {
		return roomDomain.ErrInvalidRoomInput
	}
	return ctx.Err()
}

func lockRuleOperation(ctx context.Context, queries QueryHandle, operationKind, operationID string) error {
	return queries.LockRoomRuleOperationKey(ctx, sqlcgen.LockRoomRuleOperationKeyParams{OperationKind: operationKind, OperationID: operationID})
}

func getRuleOperation(ctx context.Context, queries QueryHandle, operationKind, operationID string) (sqlcgen.RoomRuleOperationRecord, bool, error) {
	record, err := queries.GetRoomRuleOperationRecord(ctx, sqlcgen.GetRoomRuleOperationRecordParams{OperationKind: operationKind, OperationID: operationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlcgen.RoomRuleOperationRecord{}, false, nil
	}
	return record, err == nil, err
}

func validateRuleRoomFence(row sqlcgen.PartyRoom, actorUserID uuid.UUID, expected roomDomain.Version, ownershipEpoch uint64) error {
	if !row.RoomID.Valid || !row.HostUserID.Valid || row.RoomVersion <= 0 || row.MembershipVersion <= 0 || row.OwnershipEpoch <= 0 {
		return roomDomain.ErrRoomIntegrity
	}
	if uuid.UUID(row.HostUserID.Bytes) != actorUserID {
		return roomDomain.ErrRulePermission
	}
	if uint64(row.RoomVersion) != expected.Room || uint64(row.MembershipVersion) != expected.Membership ||
		uint64(row.OwnershipEpoch) != ownershipEpoch {
		return roomDomain.ErrRoomVersionConflict
	}
	return nil
}

func createRuleDraftParams(update roomDomain.RuleDraftUpdate, revision uint64) sqlcgen.CreateRoomGameConfigDraftParams {
	return sqlcgen.CreateRoomGameConfigDraftParams{
		RoomID: uuidToPG(update.RoomID), GameID: update.GameID, EngineVersion: update.Config.EngineVersion,
		ProtocolVersion: update.Config.ProtocolVersion, ClientVersion: update.Config.ClientVersion,
		ConfigSchemaVersion: int32(update.Config.SchemaVersion), ConfigMessageType: update.Config.MessageType,
		ConfigPayload: append([]byte(nil), update.Config.Payload...), Revision: int64(revision),
		UpdatedBy: uuidToPG(update.ActorUserID), UpdatedAt: timeToPG(update.At),
	}
}

func updateRuleDraftParams(update roomDomain.RuleDraftUpdate, revision uint64) sqlcgen.UpdateRoomGameConfigDraftParams {
	return sqlcgen.UpdateRoomGameConfigDraftParams{
		EngineVersion: update.Config.EngineVersion, ProtocolVersion: update.Config.ProtocolVersion,
		ClientVersion: update.Config.ClientVersion, ConfigSchemaVersion: int32(update.Config.SchemaVersion),
		ConfigMessageType: update.Config.MessageType, ConfigPayload: append([]byte(nil), update.Config.Payload...),
		Revision: int64(revision), UpdatedBy: uuidToPG(update.ActorUserID), UpdatedAt: timeToPG(update.At),
		RoomID: uuidToPG(update.RoomID), GameID: update.GameID, ExpectedRevision: int64(update.ExpectedRevision),
	}
}

func createRulePresetParams(write roomDomain.RulePresetWrite, revision uint64, createdAt time.Time) sqlcgen.CreateGameRulePresetParams {
	return sqlcgen.CreateGameRulePresetParams{
		PresetID: uuidToPG(write.PresetID), OwnerUserID: uuidToPG(write.OwnerUserID), GameID: write.GameID,
		Name: write.Name, EngineVersion: write.Config.EngineVersion, ProtocolVersion: write.Config.ProtocolVersion,
		ClientVersion: write.Config.ClientVersion, ConfigSchemaVersion: int32(write.Config.SchemaVersion),
		ConfigMessageType: write.Config.MessageType, ConfigPayload: append([]byte(nil), write.Config.Payload...),
		Revision: int64(revision), Compatible: true, CreatedAt: timeToPG(createdAt), UpdatedAt: timeToPG(write.At),
		LastUsedAt: timeToPG(write.At),
	}
}

func updateRulePresetParams(write roomDomain.RulePresetWrite, revision uint64) sqlcgen.UpdateGameRulePresetParams {
	return sqlcgen.UpdateGameRulePresetParams{
		PresetID: uuidToPG(write.PresetID), Name: write.Name, EngineVersion: write.Config.EngineVersion,
		ProtocolVersion: write.Config.ProtocolVersion, ClientVersion: write.Config.ClientVersion,
		ConfigSchemaVersion: int32(write.Config.SchemaVersion), ConfigMessageType: write.Config.MessageType,
		ConfigPayload: append([]byte(nil), write.Config.Payload...), Revision: int64(revision), Compatible: true,
		UpdatedAt: timeToPG(write.At), LastUsedAt: timeToPG(write.At), ExpectedRevision: int64(write.ExpectedRevision),
	}
}

func ruleDraftFromRow(row sqlcgen.RoomGameConfigDraft) (roomDomain.RuleDraft, error) {
	if !row.RoomID.Valid || !row.UpdatedBy.Valid || row.Revision <= 0 || row.ConfigSchemaVersion <= 0 || !row.UpdatedAt.Valid {
		return roomDomain.RuleDraft{}, roomDomain.ErrRoomIntegrity
	}
	draft := roomDomain.RuleDraft{
		RoomID: uuid.UUID(row.RoomID.Bytes), GameID: row.GameID, Revision: uint64(row.Revision),
		UpdatedBy: uuid.UUID(row.UpdatedBy.Bytes), UpdatedAt: row.UpdatedAt.Time,
		Config: ruleConfigFromParts(row.GameID, row.EngineVersion, row.ProtocolVersion, row.ClientVersion,
			uint32(row.ConfigSchemaVersion), row.ConfigMessageType, row.ConfigPayload),
	}
	if !draft.Config.Valid() {
		return roomDomain.RuleDraft{}, roomDomain.ErrRoomIntegrity
	}
	return draft, nil
}

func rulePresetFromRow(row sqlcgen.GameRulePreset) (roomDomain.RulePreset, error) {
	if !row.PresetID.Valid || !row.OwnerUserID.Valid || row.Revision <= 0 || row.ConfigSchemaVersion <= 0 ||
		!row.CreatedAt.Valid || !row.UpdatedAt.Valid || !row.LastUsedAt.Valid {
		return roomDomain.RulePreset{}, roomDomain.ErrRoomIntegrity
	}
	preset := roomDomain.RulePreset{
		ID: uuid.UUID(row.PresetID.Bytes), OwnerUserID: uuid.UUID(row.OwnerUserID.Bytes),
		GameID: row.GameID, Name: row.Name, Revision: uint64(row.Revision), CreatedAt: row.CreatedAt.Time,
		UpdatedAt: row.UpdatedAt.Time, LastUsedAt: row.LastUsedAt.Time, Compatible: row.Compatible,
		Config: ruleConfigFromParts(row.GameID, row.EngineVersion, row.ProtocolVersion, row.ClientVersion,
			uint32(row.ConfigSchemaVersion), row.ConfigMessageType, row.ConfigPayload),
	}
	if !preset.Config.Valid() {
		return roomDomain.RulePreset{}, roomDomain.ErrRoomIntegrity
	}
	return preset, nil
}

func pendingStartFromRow(row sqlcgen.RoomPendingStart) (roomDomain.PendingStart, error) {
	if !row.PendingStartID.Valid || !row.RoomID.Valid || row.ConfigRevision <= 0 || row.ExpectedRoomVersion <= 0 ||
		row.ExpectedMembershipVersion <= 0 || row.OwnershipEpoch <= 0 || len(row.RequestDigest) != 32 ||
		!row.DeadlineAt.Valid || !row.CreatedAt.Valid {
		return roomDomain.PendingStart{}, roomDomain.ErrRoomIntegrity
	}
	digest, err := digestFromBytes(row.RequestDigest)
	if err != nil {
		return roomDomain.PendingStart{}, err
	}
	return roomDomain.PendingStart{
		ID: uuid.UUID(row.PendingStartID.Bytes), RoomID: uuid.UUID(row.RoomID.Bytes), CancelToken: row.CancelToken,
		Deadline: row.DeadlineAt.Time, GameID: row.GameID, ConfigRevision: uint64(row.ConfigRevision),
		Expected:       roomDomain.Version{Room: uint64(row.ExpectedRoomVersion), Membership: uint64(row.ExpectedMembershipVersion)},
		OwnershipEpoch: uint64(row.OwnershipEpoch), OperationID: row.OperationID, RequestDigest: digest,
		Cancelled: row.CancelledAt.Valid, Consumed: row.ConsumedAt.Valid,
	}, nil
}

func ruleConfigFromParts(gameID, engineVersion, protocolVersion, clientVersion string, schemaVersion uint32, messageType string, payload []byte) roomDomain.ConfigEnvelope {
	return roomDomain.ConfigEnvelope{
		GameID: gameID, EngineVersion: engineVersion, ProtocolVersion: protocolVersion,
		ClientVersion: clientVersion, SchemaVersion: schemaVersion, MessageType: messageType,
		Payload: append([]byte(nil), payload...),
	}
}

func operationParamsForDraft(operationID string, digest [32]byte, draft roomDomain.RuleDraft, at time.Time) sqlcgen.CreateRoomRuleOperationRecordParams {
	params := baseRuleOperationParams(operationID, ruleOperationDraftUpdate, digest, at)
	params.RoomID, params.GameID = uuidToPG(draft.RoomID), textToPG(draft.GameID)
	params.ResultRevision, params.ResultUpdatedBy = int8ToPG(draft.Revision), uuidToPG(draft.UpdatedBy)
	applyConfigToOperationParams(&params, draft.Config)
	params.ResultUpdatedAt = timeToPG(draft.UpdatedAt)
	return params
}

func operationParamsForPreset(operationID string, digest [32]byte, preset roomDomain.RulePreset, at time.Time) sqlcgen.CreateRoomRuleOperationRecordParams {
	params := baseRuleOperationParams(operationID, ruleOperationPresetSave, digest, at)
	params.OwnerUserID, params.PresetID, params.GameID = uuidToPG(preset.OwnerUserID), uuidToPG(preset.ID), textToPG(preset.GameID)
	params.ResultRevision, params.ResultName = int8ToPG(preset.Revision), textToPG(preset.Name)
	params.ResultCreatedAt, params.ResultUpdatedAt = timeToPG(preset.CreatedAt), timeToPG(preset.UpdatedAt)
	params.ResultLastUsedAt, params.ResultCompatible = timeToPG(preset.LastUsedAt), pgtype.Bool{Bool: preset.Compatible, Valid: true}
	applyConfigToOperationParams(&params, preset.Config)
	return params
}

func operationParamsForPresetDelete(operationID string, digest [32]byte, ownerID, presetID uuid.UUID, at time.Time) sqlcgen.CreateRoomRuleOperationRecordParams {
	params := baseRuleOperationParams(operationID, ruleOperationPresetDelete, digest, at)
	params.OwnerUserID, params.PresetID = uuidToPG(ownerID), uuidToPG(presetID)
	return params
}

func operationParamsForPendingStart(operationID string, digest [32]byte, start roomDomain.PendingStart, at time.Time) sqlcgen.CreateRoomRuleOperationRecordParams {
	params := baseRuleOperationParams(operationID, ruleOperationPendingStartBegin, digest, at)
	params.RoomID, params.PendingStartID, params.GameID = uuidToPG(start.RoomID), uuidToPG(start.ID), textToPG(start.GameID)
	params.CancelToken, params.DeadlineAt = textToPG(start.CancelToken), timeToPG(start.Deadline)
	params.ConfigRevision, params.ExpectedRoomVersion = int8ToPG(start.ConfigRevision), int8ToPG(start.Expected.Room)
	params.ExpectedMembershipVersion, params.OwnershipEpoch = int8ToPG(start.Expected.Membership), int8ToPG(start.OwnershipEpoch)
	return params
}

func baseRuleOperationParams(operationID, kind string, digest [32]byte, at time.Time) sqlcgen.CreateRoomRuleOperationRecordParams {
	return sqlcgen.CreateRoomRuleOperationRecordParams{
		OperationID: operationID, OperationKind: kind, RequestDigest: digestToBytes(digest), CreatedAt: timeToPG(at),
	}
}

func applyConfigToOperationParams(params *sqlcgen.CreateRoomRuleOperationRecordParams, config roomDomain.ConfigEnvelope) {
	params.EngineVersion, params.ProtocolVersion = textToPG(config.EngineVersion), textToPG(config.ProtocolVersion)
	params.ClientVersion, params.ConfigMessageType = textToPG(config.ClientVersion), textToPG(config.MessageType)
	params.ConfigSchemaVersion = pgtype.Int4{Int32: int32(config.SchemaVersion), Valid: true}
	params.ConfigPayload = append([]byte(nil), config.Payload...)
}

func draftFromOperation(record sqlcgen.RoomRuleOperationRecord, digest [32]byte) (roomDomain.RuleDraft, error) {
	if err := validateOperationReplay(record, ruleOperationDraftUpdate, digest); err != nil {
		return roomDomain.RuleDraft{}, err
	}
	if !record.RoomID.Valid || !record.ResultUpdatedBy.Valid || !record.ResultRevision.Valid || !record.ResultUpdatedAt.Valid ||
		!record.GameID.Valid || !operationConfigValid(record) {
		return roomDomain.RuleDraft{}, roomDomain.ErrRoomIntegrity
	}
	return roomDomain.RuleDraft{
		RoomID: uuid.UUID(record.RoomID.Bytes), GameID: record.GameID.String, Revision: uint64(record.ResultRevision.Int64),
		UpdatedBy: uuid.UUID(record.ResultUpdatedBy.Bytes), UpdatedAt: record.ResultUpdatedAt.Time,
		Config: operationConfig(record),
	}, nil
}

func presetFromOperation(record sqlcgen.RoomRuleOperationRecord, digest [32]byte) (roomDomain.RulePreset, error) {
	if err := validateOperationReplay(record, ruleOperationPresetSave, digest); err != nil {
		return roomDomain.RulePreset{}, err
	}
	if !record.PresetID.Valid || !record.OwnerUserID.Valid || !record.ResultRevision.Valid || !record.ResultName.Valid ||
		!record.ResultCreatedAt.Valid || !record.ResultUpdatedAt.Valid || !record.ResultLastUsedAt.Valid ||
		!record.ResultCompatible.Valid || !record.GameID.Valid || !operationConfigValid(record) {
		return roomDomain.RulePreset{}, roomDomain.ErrRoomIntegrity
	}
	return roomDomain.RulePreset{
		ID: uuid.UUID(record.PresetID.Bytes), OwnerUserID: uuid.UUID(record.OwnerUserID.Bytes),
		GameID: record.GameID.String, Name: record.ResultName.String, Revision: uint64(record.ResultRevision.Int64),
		CreatedAt: record.ResultCreatedAt.Time, UpdatedAt: record.ResultUpdatedAt.Time,
		LastUsedAt: record.ResultLastUsedAt.Time, Compatible: record.ResultCompatible.Bool, Config: operationConfig(record),
	}, nil
}

func pendingStartFromOperation(record sqlcgen.RoomRuleOperationRecord, digest [32]byte) (roomDomain.PendingStart, error) {
	if err := validateOperationReplay(record, ruleOperationPendingStartBegin, digest); err != nil {
		return roomDomain.PendingStart{}, err
	}
	if !record.PendingStartID.Valid || !record.RoomID.Valid || !record.GameID.Valid || !record.CancelToken.Valid ||
		!record.DeadlineAt.Valid || !record.ConfigRevision.Valid || !record.ExpectedRoomVersion.Valid ||
		!record.ExpectedMembershipVersion.Valid || !record.OwnershipEpoch.Valid {
		return roomDomain.PendingStart{}, roomDomain.ErrRoomIntegrity
	}
	requestDigest, err := digestFromBytes(record.RequestDigest)
	if err != nil {
		return roomDomain.PendingStart{}, err
	}
	return roomDomain.PendingStart{
		ID: uuid.UUID(record.PendingStartID.Bytes), RoomID: uuid.UUID(record.RoomID.Bytes), CancelToken: record.CancelToken.String,
		Deadline: record.DeadlineAt.Time, GameID: record.GameID.String, ConfigRevision: uint64(record.ConfigRevision.Int64),
		Expected:       roomDomain.Version{Room: uint64(record.ExpectedRoomVersion.Int64), Membership: uint64(record.ExpectedMembershipVersion.Int64)},
		OwnershipEpoch: uint64(record.OwnershipEpoch.Int64), OperationID: record.OperationID, RequestDigest: requestDigest,
	}, nil
}

func validateOperationReplay(record sqlcgen.RoomRuleOperationRecord, kind string, digest [32]byte) error {
	if record.OperationKind != kind || !bytes.Equal(record.RequestDigest, digestToBytes(digest)) {
		return roomDomain.ErrRuleOperationConflict
	}
	return nil
}

func operationConfigValid(record sqlcgen.RoomRuleOperationRecord) bool {
	return record.EngineVersion.Valid && record.ProtocolVersion.Valid && record.ClientVersion.Valid &&
		record.ConfigSchemaVersion.Valid && record.ConfigMessageType.Valid && record.ConfigSchemaVersion.Int32 > 0
}

func operationConfig(record sqlcgen.RoomRuleOperationRecord) roomDomain.ConfigEnvelope {
	return ruleConfigFromParts(record.GameID.String, record.EngineVersion.String, record.ProtocolVersion.String,
		record.ClientVersion.String, uint32(record.ConfigSchemaVersion.Int32), record.ConfigMessageType.String, record.ConfigPayload)
}

func ruleConfigFitsPostgres(config roomDomain.ConfigEnvelope) bool {
	return config.SchemaVersion <= math.MaxInt32
}

func digestToBytes(digest [32]byte) []byte {
	value := make([]byte, len(digest))
	copy(value, digest[:])
	return value
}

func digestFromBytes(value []byte) ([32]byte, error) {
	if len(value) != 32 {
		return [32]byte{}, roomDomain.ErrRoomIntegrity
	}
	var digest [32]byte
	copy(digest[:], value)
	return digest, nil
}

func int8ToPG(value uint64) pgtype.Int8 {
	return pgtype.Int8{Int64: int64(value), Valid: value > 0 && value <= math.MaxInt64}
}

func mapNoRows(err, noRowsError error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return noRowsError
	}
	return err
}

func mapRuleRepositoryError(ctx context.Context, err, noRowsError error, domainErrors ...error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return noRowsError
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	for _, domainErr := range domainErrors {
		if errors.Is(err, domainErr) {
			return domainErr
		}
	}
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		switch pgError.Code {
		case "23505":
			switch pgError.ConstraintName {
			case "room_pending_starts_active_unique_idx":
				return roomDomain.ErrPendingStartInvalid
			case "room_rule_operation_records_kind_operation_unique", "room_pending_starts_operation_unique_idx":
				return roomDomain.ErrRuleOperationConflict
			default:
				return roomDomain.ErrRuleRevisionConflict
			}
		case "23503", "23514", "22001":
			return roomDomain.ErrRoomIntegrity
		case "40001", "40P01":
			return roomDomain.ErrRoomVersionConflict
		}
	}
	return mapUnitOfWorkError(err, roomDomain.ErrRoomRepositoryUnavailable, append(domainErrors,
		noRowsError, roomDomain.ErrRoomIntegrity, roomDomain.ErrRuleNotFound, roomDomain.ErrPendingStartInvalid)...)
}

var _ roomDomain.RuleRepository = (*RuleRepository)(nil)
