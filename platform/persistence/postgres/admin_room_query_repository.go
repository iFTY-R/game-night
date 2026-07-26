package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	adminroom "github.com/iFTY-R/game-night/platform/admin/room"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const adminRoomMaxPageSize uint32 = 200

type AdminRoomQueryRepository struct {
	queries *sqlcgen.Queries
	runner  *TransactionRunner
}

func NewAdminRoomQueryRepository(pool *pgxpool.Pool) *AdminRoomQueryRepository {
	return &AdminRoomQueryRepository{queries: sqlcgen.New(pool), runner: NewTransactionRunner(pool)}
}

func (repository *AdminRoomQueryRepository) ListRooms(ctx context.Context, query adminroom.RoomListQuery) ([]adminroom.RoomSummary, error) {
	if repository == nil || repository.queries == nil || ctx == nil || !validRoomListQuery(query) {
		return nil, adminroom.ErrInvalidInput
	}
	rows, err := repository.queries.AdminListRooms(ctx, adminListRoomsParams(query))
	if err != nil {
		return nil, mapAdminRoomQueryError(ctx, err, adminroom.ErrRepositoryUnavailable)
	}
	result := make([]adminroom.RoomSummary, 0, len(rows))
	for _, row := range rows {
		summary, mapErr := adminRoomSummaryFromListRow(row)
		if mapErr != nil {
			return nil, mapErr
		}
		result = append(result, summary)
	}
	return result, nil
}

func (repository *AdminRoomQueryRepository) GetRoom(ctx context.Context, roomID uuid.UUID) (adminroom.RoomDetail, error) {
	if repository == nil || repository.runner == nil || ctx == nil || roomID == uuid.Nil {
		return adminroom.RoomDetail{}, adminroom.ErrInvalidInput
	}
	sampledAt := time.Now().UTC()
	onlineCutoff := sampledAt.Add(-adminroom.DefaultRoomMemberOnlineWindow)
	var row sqlcgen.AdminGetRoomRow
	var memberRows []sqlcgen.AdminListRoomMembersRow
	var gameRows []sqlcgen.AdminListRoomActiveGamesRow
	var eventRows []sqlcgen.AdminListRoomRecentEventsRow
	err := repository.runner.RunWithOptions(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(ctx context.Context, queries QueryHandle) error {
		var err error
		row, err = queries.AdminGetRoom(ctx, sqlcgen.AdminGetRoomParams{RoomID: uuidToPG(roomID)})
		if err != nil {
			return err
		}
		memberRows, err = queries.AdminListRoomMembers(ctx, sqlcgen.AdminListRoomMembersParams{
			OnlineCutoff: timeToPG(onlineCutoff), RoomID: uuidToPG(roomID),
		})
		if err != nil {
			return err
		}
		gameRows, err = queries.AdminListRoomActiveGames(ctx, sqlcgen.AdminListRoomActiveGamesParams{
			RoomID: uuidToPG(roomID), PageSize: int32(adminroom.DefaultRoomActiveGameLimit),
		})
		if err != nil {
			return err
		}
		eventRows, err = queries.AdminListRoomRecentEvents(ctx, sqlcgen.AdminListRoomRecentEventsParams{
			RoomID: uuidToPG(roomID), PageSize: int32(adminroom.DefaultRoomDetailEventLimit),
		})
		return err
	})
	if err != nil {
		return adminroom.RoomDetail{}, mapAdminRoomQueryError(ctx, err, adminroom.ErrNotFound)
	}
	summary, err := adminRoomSummaryFromGetRow(row)
	if err != nil {
		return adminroom.RoomDetail{}, err
	}
	members, err := adminRoomMembersFromRows(memberRows, summary.MembershipVersion)
	if err != nil {
		return adminroom.RoomDetail{}, err
	}
	games, err := adminRoomGamesFromRows(gameRows)
	if err != nil {
		return adminroom.RoomDetail{}, err
	}
	events, err := adminRoomEventsFromRows(eventRows)
	if err != nil {
		return adminroom.RoomDetail{}, err
	}
	return adminroom.RoomDetail{Summary: summary, Members: members, ActiveGames: games, RecentEvents: events, SampledAt: sampledAt}, nil
}

func (repository *AdminRoomQueryRepository) ListGames(ctx context.Context, query adminroom.GameListQuery) ([]adminroom.GameSummary, error) {
	if repository == nil || repository.queries == nil || ctx == nil || !validGameListQuery(query) {
		return nil, adminroom.ErrInvalidInput
	}
	rows, err := repository.queries.AdminListGames(ctx, adminListGamesParams(query))
	if err != nil {
		return nil, mapAdminRoomQueryError(ctx, err, adminroom.ErrRepositoryUnavailable)
	}
	result := make([]adminroom.GameSummary, 0, len(rows))
	for _, row := range rows {
		summary, mapErr := adminGameSummaryFromListRow(row)
		if mapErr != nil {
			return nil, mapErr
		}
		result = append(result, summary)
	}
	return result, nil
}

func (repository *AdminRoomQueryRepository) GetGame(ctx context.Context, sessionID uuid.UUID) (adminroom.GameDetail, error) {
	if repository == nil || repository.runner == nil || ctx == nil || sessionID == uuid.Nil {
		return adminroom.GameDetail{}, adminroom.ErrInvalidInput
	}
	sampledAt := time.Now().UTC()
	var row sqlcgen.AdminGetGameRow
	var participantRows []sqlcgen.AdminListGameParticipantsRow
	var eventRows []sqlcgen.AdminListGameRecentEventsRow
	err := repository.runner.RunWithOptions(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(ctx context.Context, queries QueryHandle) error {
		var err error
		row, err = queries.AdminGetGame(ctx, sqlcgen.AdminGetGameParams{SessionID: uuidToPG(sessionID)})
		if err != nil {
			return err
		}
		participantRows, err = queries.AdminListGameParticipants(ctx, sqlcgen.AdminListGameParticipantsParams{SessionID: uuidToPG(sessionID)})
		if err != nil {
			return err
		}
		eventRows, err = queries.AdminListGameRecentEvents(ctx, sqlcgen.AdminListGameRecentEventsParams{
			SessionID: uuidToPG(sessionID), PageSize: int32(adminroom.DefaultGameDetailEventLimit),
		})
		return err
	})
	if err != nil {
		return adminroom.GameDetail{}, mapAdminRoomQueryError(ctx, err, adminroom.ErrNotFound)
	}
	summary, err := adminGameSummaryFromGetRow(row)
	if err != nil {
		return adminroom.GameDetail{}, err
	}
	participants, err := adminGameParticipantsFromRows(participantRows)
	if err != nil {
		return adminroom.GameDetail{}, err
	}
	events, err := adminGameEventsFromRows(eventRows)
	if err != nil {
		return adminroom.GameDetail{}, err
	}
	return adminroom.GameDetail{Summary: summary, Participants: participants, RecentEvents: events, SampledAt: sampledAt}, nil
}

func validRoomListQuery(query adminroom.RoomListQuery) bool {
	if query.PageSize == 0 || query.PageSize > adminRoomMaxPageSize || len(query.Statuses) > 16 || len(query.GameIDs) > 32 ||
		!validAdminRoomRange(query.CreatedFrom, query.CreatedTo) || !validAdminRoomRange(query.UpdatedFrom, query.UpdatedTo) {
		return false
	}
	sortField, direction := adminRoomSortDefaults(query.SortField, query.Direction)
	if sortField != adminroom.RoomSortUpdatedAt && sortField != adminroom.RoomSortCreatedAt &&
		sortField != adminroom.RoomSortLastActivityAt && sortField != adminroom.RoomSortRoomCode {
		return false
	}
	if direction != adminroom.SortAscending && direction != adminroom.SortDescending {
		return false
	}
	if query.After.RoomID == uuid.Nil {
		return query.After.SortTime.IsZero() && query.After.SortText == ""
	}
	if sortField == adminroom.RoomSortRoomCode {
		return query.After.SortTime.IsZero() && query.After.SortText != ""
	}
	return !query.After.SortTime.IsZero() && query.After.SortText == ""
}

func validGameListQuery(query adminroom.GameListQuery) bool {
	if query.PageSize == 0 || query.PageSize > adminRoomMaxPageSize || len(query.Statuses) > 16 || len(query.GameIDs) > 32 ||
		!validAdminRoomRange(query.StartedFrom, query.StartedTo) || !validAdminRoomRange(query.UpdatedFrom, query.UpdatedTo) {
		return false
	}
	sortField, direction := adminGameSortDefaults(query.SortField, query.Direction)
	if sortField != adminroom.GameSortUpdatedAt && sortField != adminroom.GameSortStartedAt &&
		sortField != adminroom.GameSortLastProgressAt && sortField != adminroom.GameSortSessionID {
		return false
	}
	if direction != adminroom.SortAscending && direction != adminroom.SortDescending {
		return false
	}
	if query.After.SessionID == uuid.Nil {
		return query.After.SortTime.IsZero()
	}
	if sortField == adminroom.GameSortSessionID {
		return query.After.SortTime.IsZero()
	}
	return !query.After.SortTime.IsZero()
}

func adminListRoomsParams(query adminroom.RoomListQuery) sqlcgen.AdminListRoomsParams {
	sortField, direction := adminRoomSortDefaults(query.SortField, query.Direction)
	return sqlcgen.AdminListRoomsParams{
		AnomaliesOnly: query.AnomaliesOnly, AfterRoomID: optionalUUIDToPG(query.After.RoomID), SortField: string(sortField),
		SortDirection: string(direction), AfterSortTime: optionalTimeToPG(query.After.SortTime),
		AfterSortText: textToPG(query.After.SortText), PageSize: int32(query.PageSize), RoomID: optionalUUIDToPG(query.RoomID),
		RoomCode: strings.TrimSpace(query.RoomCode), Statuses: dedupeStrings(query.Statuses), GameIds: dedupeStrings(query.GameIDs),
		HostUserID: optionalUUIDToPG(query.HostUserID), MemberUserID: optionalUUIDToPG(query.MemberUserID),
		CreatedFrom: optionalTimeToPG(query.CreatedFrom), CreatedTo: optionalTimeToPG(query.CreatedTo),
		UpdatedFrom: optionalTimeToPG(query.UpdatedFrom), UpdatedTo: optionalTimeToPG(query.UpdatedTo),
	}
}

func adminListGamesParams(query adminroom.GameListQuery) sqlcgen.AdminListGamesParams {
	sortField, direction := adminGameSortDefaults(query.SortField, query.Direction)
	return sqlcgen.AdminListGamesParams{
		AnomaliesOnly: query.AnomaliesOnly, AfterSessionID: optionalUUIDToPG(query.After.SessionID),
		SortField: string(sortField), SortDirection: string(direction), AfterSortTime: optionalTimeToPG(query.After.SortTime),
		PageSize: int32(query.PageSize), SessionID: optionalUUIDToPG(query.SessionID), RoomID: optionalUUIDToPG(query.RoomID),
		GameIds: dedupeStrings(query.GameIDs), Statuses: dedupeStrings(query.Statuses), StartedFrom: optionalTimeToPG(query.StartedFrom),
		StartedTo: optionalTimeToPG(query.StartedTo), UpdatedFrom: optionalTimeToPG(query.UpdatedFrom), UpdatedTo: optionalTimeToPG(query.UpdatedTo),
	}
}

func adminRoomSummaryFromListRow(row sqlcgen.AdminListRoomsRow) (adminroom.RoomSummary, error) {
	return adminRoomSummaryFromValues(row.RoomID, row.RoomCode, row.Status, row.ActiveGameID, row.ActiveSessionID, row.HostUserID,
		row.HostUsername, row.ParticipantCount, row.SpectatorCount, row.ParticipantAdmission, row.SpectatorAdmission,
		row.RoomVersion, row.MembershipVersion, row.OwnershipEpoch, row.CreatedAt, row.UpdatedAt, row.LastActivityAt, row.RoomGameLinkMismatch)
}

func adminRoomSummaryFromGetRow(row sqlcgen.AdminGetRoomRow) (adminroom.RoomSummary, error) {
	return adminRoomSummaryFromValues(row.RoomID, row.RoomCode, row.Status, row.ActiveGameID, row.ActiveSessionID, row.HostUserID,
		row.HostUsername, row.ParticipantCount, row.SpectatorCount, row.ParticipantAdmission, row.SpectatorAdmission,
		row.RoomVersion, row.MembershipVersion, row.OwnershipEpoch, row.CreatedAt, row.UpdatedAt, row.LastActivityAt, row.RoomGameLinkMismatch)
}

func adminRoomSummaryFromValues(
	roomID pgtype.UUID,
	roomCode, status string,
	activeGameID pgtype.Text,
	activeSessionID, hostUserID pgtype.UUID,
	hostUsername pgtype.Text,
	participantCount, spectatorCount int32,
	participantAdmission, spectatorAdmission string,
	roomVersion, membershipVersion, ownershipEpoch int64,
	createdAt, updatedAt, lastActivityAt pgtype.Timestamptz,
	linkMismatch pgtype.Bool,
) (adminroom.RoomSummary, error) {
	if !roomID.Valid || roomID.Bytes == uuid.Nil || !hostUserID.Valid || hostUserID.Bytes == uuid.Nil ||
		participantCount < 0 || spectatorCount < 0 || roomVersion <= 0 || membershipVersion <= 0 || ownershipEpoch < 0 ||
		!createdAt.Valid || !updatedAt.Valid || !lastActivityAt.Valid {
		return adminroom.RoomSummary{}, adminroom.ErrIntegrity
	}
	anomalies := make([]adminroom.RoomAnomalyFlag, 0, 1)
	if linkMismatch.Valid && linkMismatch.Bool {
		anomalies = append(anomalies, adminroom.RoomAnomalyGameLinkMismatch)
	}
	return adminroom.RoomSummary{
		RoomID: uuid.UUID(roomID.Bytes), RoomCode: roomCode, Status: status, ActiveGameID: optionalTextFromPG(activeGameID),
		ActiveSessionID: optionalUUIDFromPG(activeSessionID), HostUserID: uuid.UUID(hostUserID.Bytes), HostUsername: optionalTextFromPG(hostUsername),
		ParticipantCount: uint32(participantCount), SpectatorCount: uint32(spectatorCount), ParticipantAdmission: participantAdmission,
		SpectatorAdmission: spectatorAdmission, RoomVersion: uint64(roomVersion), MembershipVersion: uint64(membershipVersion),
		OwnershipEpoch: uint64(ownershipEpoch), CreatedAt: canonicalPostgresTime(createdAt), UpdatedAt: canonicalPostgresTime(updatedAt),
		LastActivityAt: canonicalPostgresTime(lastActivityAt), Anomalies: anomalies,
	}, nil
}

func adminRoomMembersFromRows(rows []sqlcgen.AdminListRoomMembersRow, membershipVersion uint64) ([]adminroom.RoomMemberSummary, error) {
	result := make([]adminroom.RoomMemberSummary, 0, len(rows))
	for _, row := range rows {
		if !row.UserID.Valid || row.UserID.Bytes == uuid.Nil || !row.JoinedAt.Valid {
			return nil, adminroom.ErrIntegrity
		}
		result = append(result, adminroom.RoomMemberSummary{
			UserID: uuid.UUID(row.UserID.Bytes), Username: row.Username, Role: row.Role, RequestedRole: optionalTextFromPG(row.RequestedRole),
			MembershipVersion: membershipVersion, JoinedAt: canonicalPostgresTime(row.JoinedAt), Online: row.Online,
		})
	}
	return result, nil
}

func adminRoomGamesFromRows(rows []sqlcgen.AdminListRoomActiveGamesRow) ([]adminroom.GameSummary, error) {
	result := make([]adminroom.GameSummary, 0, len(rows))
	for _, row := range rows {
		summary, err := adminGameSummaryFromValues(row.SessionID, row.RoomID, row.RoomCode, row.GameID, row.EngineVersion, row.ProtocolVersion,
			row.ClientVersion, row.Status, row.StateVersion, row.OwnershipEpoch, row.StartedAt, row.UpdatedAt, row.LastProgressAt, row.RoomLinkMismatch)
		if err != nil {
			return nil, err
		}
		result = append(result, summary)
	}
	return result, nil
}

func adminGameSummaryFromListRow(row sqlcgen.AdminListGamesRow) (adminroom.GameSummary, error) {
	return adminGameSummaryFromValues(row.SessionID, row.RoomID, row.RoomCode, row.GameID, row.EngineVersion, row.ProtocolVersion,
		row.ClientVersion, row.Status, row.StateVersion, row.OwnershipEpoch, row.StartedAt, row.UpdatedAt, row.LastProgressAt, row.RoomLinkMismatch)
}

func adminGameSummaryFromGetRow(row sqlcgen.AdminGetGameRow) (adminroom.GameSummary, error) {
	return adminGameSummaryFromValues(row.SessionID, row.RoomID, row.RoomCode, row.GameID, row.EngineVersion, row.ProtocolVersion,
		row.ClientVersion, row.Status, row.StateVersion, row.OwnershipEpoch, row.StartedAt, row.UpdatedAt, row.LastProgressAt, row.RoomLinkMismatch)
}

func adminGameSummaryFromValues(
	sessionID, roomID pgtype.UUID,
	roomCode pgtype.Text,
	gameID, engineVersion, protocolVersion, clientVersion, status string,
	stateVersion, ownershipEpoch int64,
	startedAt, updatedAt, lastProgressAt pgtype.Timestamptz,
	linkMismatch pgtype.Bool,
) (adminroom.GameSummary, error) {
	if !sessionID.Valid || sessionID.Bytes == uuid.Nil || !roomID.Valid || roomID.Bytes == uuid.Nil ||
		stateVersion <= 0 || ownershipEpoch < 0 || !startedAt.Valid || !updatedAt.Valid || !lastProgressAt.Valid {
		return adminroom.GameSummary{}, adminroom.ErrIntegrity
	}
	anomalies := make([]adminroom.GameAnomalyFlag, 0, 1)
	if linkMismatch.Valid && linkMismatch.Bool {
		anomalies = append(anomalies, adminroom.GameAnomalyRoomLinkMismatch)
	}
	return adminroom.GameSummary{
		SessionID: uuid.UUID(sessionID.Bytes), RoomID: uuid.UUID(roomID.Bytes), RoomCode: optionalTextFromPG(roomCode), GameID: gameID,
		GameVersion: fmt.Sprintf("%s/%s/%s", engineVersion, protocolVersion, clientVersion), Status: status, StateVersion: uint64(stateVersion),
		OwnershipEpoch: uint64(ownershipEpoch), StartedAt: canonicalPostgresTime(startedAt), UpdatedAt: canonicalPostgresTime(updatedAt),
		LastProgressAt: canonicalPostgresTime(lastProgressAt), Anomalies: anomalies,
	}, nil
}

func adminGameParticipantsFromRows(rows []sqlcgen.AdminListGameParticipantsRow) ([]adminroom.GameParticipantSummary, error) {
	result := make([]adminroom.GameParticipantSummary, 0, len(rows))
	for _, row := range rows {
		if !row.UserID.Valid || row.UserID.Bytes == uuid.Nil {
			return nil, adminroom.ErrIntegrity
		}
		result = append(result, adminroom.GameParticipantSummary{
			UserID: uuid.UUID(row.UserID.Bytes), Username: optionalTextFromPG(row.Username),
			RoomRole: optionalTextFromPG(row.RoomRole), Active: row.Active,
		})
	}
	return result, nil
}

func adminRoomEventsFromRows(rows []sqlcgen.AdminListRoomRecentEventsRow) ([]adminroom.RoomEventSummary, error) {
	result := make([]adminroom.RoomEventSummary, 0, len(rows))
	for _, row := range rows {
		if !row.BatchID.Valid || row.BatchID.Bytes == uuid.Nil || !row.CommittedAt.Valid || row.StateVersion <= 0 {
			return nil, adminroom.ErrIntegrity
		}
		result = append(result, adminroom.RoomEventSummary{
			EventID: fmt.Sprintf("%s:%d", uuid.UUID(row.BatchID.Bytes), row.StateVersion), EventType: adminEventType(row.Cause, row.SystemOperationID),
			ActorUserID: optionalUUIDFromPG(row.ActorUserID), Digest: row.Digest, OccurredAt: canonicalPostgresTime(row.CommittedAt),
		})
	}
	return result, nil
}

func adminGameEventsFromRows(rows []sqlcgen.AdminListGameRecentEventsRow) ([]adminroom.GameEventSummary, error) {
	result := make([]adminroom.GameEventSummary, 0, len(rows))
	for _, row := range rows {
		if !row.BatchID.Valid || row.BatchID.Bytes == uuid.Nil || !row.CommittedAt.Valid || row.StateVersion <= 0 {
			return nil, adminroom.ErrIntegrity
		}
		result = append(result, adminroom.GameEventSummary{
			EventID: fmt.Sprintf("%s:%d", uuid.UUID(row.BatchID.Bytes), row.StateVersion), EventType: adminEventType(row.Cause, row.SystemOperationID),
			StateVersion: uint64(row.StateVersion), ActorUserID: optionalUUIDFromPG(row.ActorUserID),
			Digest: row.Digest, OccurredAt: canonicalPostgresTime(row.CommittedAt),
		})
	}
	return result, nil
}

func adminEventType(cause string, systemOperationID pgtype.Text) string {
	if cause == "system" && systemOperationID.Valid && systemOperationID.String != "" {
		return "system:" + systemOperationID.String
	}
	return cause
}

func adminRoomSortDefaults(field adminroom.RoomSortField, direction adminroom.SortDirection) (adminroom.RoomSortField, adminroom.SortDirection) {
	if field == "" {
		field = adminroom.RoomSortUpdatedAt
	}
	if direction == "" {
		direction = adminroom.SortDescending
	}
	return field, direction
}

func adminGameSortDefaults(field adminroom.GameSortField, direction adminroom.SortDirection) (adminroom.GameSortField, adminroom.SortDirection) {
	if field == "" {
		field = adminroom.GameSortUpdatedAt
	}
	if direction == "" {
		direction = adminroom.SortDescending
	}
	return field, direction
}

func validAdminRoomRange(from, to time.Time) bool {
	return from.IsZero() || to.IsZero() || !from.After(to)
}

func dedupeStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func uint64ToOptionalInt8(value uint64) pgtype.Int8 {
	if value == 0 {
		return pgtype.Int8{}
	}
	if value > math.MaxInt64 {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: int64(value), Valid: true}
}

func mapAdminRoomQueryError(ctx context.Context, err, noRowsError error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return noRowsError
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		switch pgError.Code {
		case "23503", "23514", "22000", "22023":
			return adminroom.ErrInvalidInput
		case "40001", "40P01":
			return adminroom.ErrConflict
		}
	}
	if errors.Is(err, adminroom.ErrInvalidInput) || errors.Is(err, adminroom.ErrIntegrity) || errors.Is(err, adminroom.ErrConflict) {
		return err
	}
	return adminroom.ErrRepositoryUnavailable
}

var _ adminroom.QueryRepository = (*AdminRoomQueryRepository)(nil)
