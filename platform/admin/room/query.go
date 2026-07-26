package room

import (
	"context"
	"time"

	"github.com/google/uuid"
	admin "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/clock"
)

type Service struct {
	repository QueryRepository
	rooms      RoomStore
	games      GameController
	owners     OwnerReader
	ownerFixes OwnerRepairer
	repairs    RepairRepository
	executor   EmergencyRepairExecutor
	clock      clock.Clock
}

// Config makes PostgreSQL reads, Redis owner reads, and sampled time explicit for deterministic tests.
type Config struct {
	Repository QueryRepository
	Rooms      RoomStore
	Games      GameController
	Owners     OwnerReader
	OwnerFixes OwnerRepairer
	Repairs    RepairRepository
	Executor   EmergencyRepairExecutor
	Clock      clock.Clock
}

// NewService constructs the read-only admin room/game query service.
func NewService(config Config) (*Service, error) {
	if config.Repository == nil || config.Clock == nil {
		return nil, ErrInvalidInput
	}
	return &Service{
		repository: config.Repository, rooms: config.Rooms, games: config.Games, owners: config.Owners,
		ownerFixes: config.OwnerFixes, repairs: config.Repairs, executor: config.Executor, clock: config.Clock,
	}, nil
}

// ListRooms returns a sampled room page with Redis owner freshness merged before anomaly filtering.
func (service *Service) ListRooms(ctx context.Context, actor admin.ActorContext, query RoomListQuery) (RoomPage, error) {
	if service == nil || service.repository == nil || service.clock == nil || ctx == nil {
		return RoomPage{}, ErrInvalidInput
	}
	if err := actor.Require(admin.PermissionRoomsRead); err != nil {
		return RoomPage{}, ErrPermissionDenied
	}
	if query.PageSize == 0 {
		query.PageSize = DefaultRoomPageSize
	}
	anomaliesOnly := query.AnomaliesOnly
	query.AnomaliesOnly = false
	sampledAt := service.clock.Now()
	rows, err := service.repository.ListRooms(ctx, query)
	if err != nil {
		return RoomPage{}, err
	}
	rows = service.enrichRooms(ctx, rows, sampledAt)
	if anomaliesOnly {
		rows = filterAnomalousRooms(rows)
	}
	return RoomPage{Rooms: rows, PageSize: query.PageSize, SampledAt: sampledAt}, nil
}

// GetRoom returns one room detail without full replay payloads and annotates runtime-only anomalies.
func (service *Service) GetRoom(ctx context.Context, actor admin.ActorContext, roomID uuid.UUID) (RoomDetail, error) {
	if service == nil || service.repository == nil || service.clock == nil || ctx == nil || roomID == uuid.Nil {
		return RoomDetail{}, ErrInvalidInput
	}
	if err := actor.Require(admin.PermissionRoomsRead); err != nil {
		return RoomDetail{}, ErrPermissionDenied
	}
	detail, err := service.repository.GetRoom(ctx, roomID)
	if err != nil {
		return RoomDetail{}, err
	}
	sampledAt := detail.SampledAt
	if sampledAt.IsZero() {
		sampledAt = service.clock.Now()
	}
	rooms := service.enrichRooms(ctx, []RoomSummary{detail.Summary}, sampledAt)
	detail.Summary = rooms[0]
	detail.ActiveGames = service.enrichGames(ctx, detail.ActiveGames, sampledAt)
	if allRoomMembersOffline(detail.Members) {
		detail.Summary.Anomalies = appendRoomAnomaly(detail.Summary.Anomalies, RoomAnomalyAllPlayersOffline)
	}
	detail.SampledAt = sampledAt
	return detail, nil
}

// ListGames returns a sampled game page with owner lease and progress anomalies merged in process memory.
func (service *Service) ListGames(ctx context.Context, actor admin.ActorContext, query GameListQuery) (GamePage, error) {
	if service == nil || service.repository == nil || service.clock == nil || ctx == nil {
		return GamePage{}, ErrInvalidInput
	}
	if err := actor.Require(admin.PermissionGamesRead); err != nil {
		return GamePage{}, ErrPermissionDenied
	}
	if query.PageSize == 0 {
		query.PageSize = DefaultGamePageSize
	}
	anomaliesOnly := query.AnomaliesOnly
	query.AnomaliesOnly = false
	sampledAt := service.clock.Now()
	rows, err := service.repository.ListGames(ctx, query)
	if err != nil {
		return GamePage{}, err
	}
	rows = service.enrichGames(ctx, rows, sampledAt)
	if anomaliesOnly {
		rows = filterAnomalousGames(rows)
	}
	return GamePage{Games: rows, PageSize: query.PageSize, SampledAt: sampledAt}, nil
}

// GetGame returns one game detail plus the current Redis owner view without loading full state/replay payloads.
func (service *Service) GetGame(ctx context.Context, actor admin.ActorContext, sessionID uuid.UUID) (GameDetail, error) {
	if service == nil || service.repository == nil || service.clock == nil || ctx == nil || sessionID == uuid.Nil {
		return GameDetail{}, ErrInvalidInput
	}
	if err := actor.Require(admin.PermissionGamesRead); err != nil {
		return GameDetail{}, ErrPermissionDenied
	}
	detail, err := service.repository.GetGame(ctx, sessionID)
	if err != nil {
		return GameDetail{}, err
	}
	sampledAt := detail.SampledAt
	if sampledAt.IsZero() {
		sampledAt = service.clock.Now()
	}
	games := service.enrichGames(ctx, []GameSummary{detail.Summary}, sampledAt)
	detail.Summary = games[0]
	detail.SampledAt = sampledAt
	return detail, nil
}

func (service *Service) enrichRooms(ctx context.Context, rows []RoomSummary, sampledAt time.Time) []RoomSummary {
	owners := service.readOwners(ctx, roomSessionIDs(rows), sampledAt)
	for index := range rows {
		if rows[index].ActiveSessionID == uuid.Nil {
			continue
		}
		rows[index].Owner = evaluateOwner(rows[index].ActiveSessionID, rows[index].OwnershipEpoch, owners, sampledAt)
		rows[index].Anomalies = appendRoomOwnerAnomaly(rows[index].Anomalies, rows[index].Owner.Freshness)
	}
	return rows
}

func (service *Service) enrichGames(ctx context.Context, rows []GameSummary, sampledAt time.Time) []GameSummary {
	owners := service.readOwners(ctx, gameSessionIDs(rows), sampledAt)
	for index := range rows {
		rows[index].Owner = evaluateOwner(rows[index].SessionID, rows[index].OwnershipEpoch, owners, sampledAt)
		rows[index].Anomalies = appendGameOwnerAnomaly(rows[index].Anomalies, rows[index].Owner.Freshness)
		if staleGameProgress(rows[index], sampledAt) {
			rows[index].Anomalies = appendGameAnomaly(rows[index].Anomalies, GameAnomalyNoRecentProgress)
		}
	}
	return rows
}

func (service *Service) readOwners(ctx context.Context, sessionIDs []uuid.UUID, sampledAt time.Time) map[uuid.UUID]OwnerLeaseSummary {
	if len(sessionIDs) == 0 {
		return nil
	}
	if service.owners == nil {
		return unknownOwners(sessionIDs, sampledAt)
	}
	owners, err := service.owners.ReadOwners(ctx, sessionIDs, sampledAt)
	if err != nil {
		return unknownOwners(sessionIDs, sampledAt)
	}
	return owners
}

func roomSessionIDs(rows []RoomSummary) []uuid.UUID {
	values := make([]uuid.UUID, 0, len(rows))
	seen := make(map[uuid.UUID]struct{}, len(rows))
	for _, row := range rows {
		if row.ActiveSessionID == uuid.Nil {
			continue
		}
		if _, exists := seen[row.ActiveSessionID]; exists {
			continue
		}
		seen[row.ActiveSessionID] = struct{}{}
		values = append(values, row.ActiveSessionID)
	}
	return values
}

func gameSessionIDs(rows []GameSummary) []uuid.UUID {
	values := make([]uuid.UUID, 0, len(rows))
	seen := make(map[uuid.UUID]struct{}, len(rows))
	for _, row := range rows {
		if row.SessionID == uuid.Nil {
			continue
		}
		if _, exists := seen[row.SessionID]; exists {
			continue
		}
		seen[row.SessionID] = struct{}{}
		values = append(values, row.SessionID)
	}
	return values
}

func unknownOwners(sessionIDs []uuid.UUID, observedAt time.Time) map[uuid.UUID]OwnerLeaseSummary {
	result := make(map[uuid.UUID]OwnerLeaseSummary, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		result[sessionID] = ownerUnknown(sessionID, observedAt)
	}
	return result
}

func appendRoomOwnerAnomaly(anomalies []RoomAnomalyFlag, freshness OwnerFreshness) []RoomAnomalyFlag {
	switch freshness {
	case OwnerFreshnessMissing, OwnerFreshnessExpired:
		return appendRoomAnomaly(anomalies, RoomAnomalyOwnerMissing)
	case OwnerFreshnessStale, OwnerFreshnessUnknown:
		return appendRoomAnomaly(anomalies, RoomAnomalyOwnerStale)
	default:
		return anomalies
	}
}

func appendGameOwnerAnomaly(anomalies []GameAnomalyFlag, freshness OwnerFreshness) []GameAnomalyFlag {
	switch freshness {
	case OwnerFreshnessMissing, OwnerFreshnessExpired:
		return appendGameAnomaly(anomalies, GameAnomalyOwnerMissing)
	case OwnerFreshnessStale, OwnerFreshnessUnknown:
		return appendGameAnomaly(anomalies, GameAnomalyOwnerStale)
	default:
		return anomalies
	}
}

func appendRoomAnomaly(anomalies []RoomAnomalyFlag, flag RoomAnomalyFlag) []RoomAnomalyFlag {
	for _, existing := range anomalies {
		if existing == flag {
			return anomalies
		}
	}
	return append(anomalies, flag)
}

func appendGameAnomaly(anomalies []GameAnomalyFlag, flag GameAnomalyFlag) []GameAnomalyFlag {
	for _, existing := range anomalies {
		if existing == flag {
			return anomalies
		}
	}
	return append(anomalies, flag)
}

func allRoomMembersOffline(members []RoomMemberSummary) bool {
	if len(members) == 0 {
		return false
	}
	for _, member := range members {
		if member.Online {
			return false
		}
	}
	return true
}

func staleGameProgress(summary GameSummary, sampledAt time.Time) bool {
	return summary.Status == "active" && !summary.LastProgressAt.IsZero() && sampledAt.Sub(summary.LastProgressAt) > DefaultGameProgressWindow
}

func filterAnomalousRooms(rows []RoomSummary) []RoomSummary {
	result := rows[:0]
	for _, row := range rows {
		if len(row.Anomalies) > 0 {
			result = append(result, row)
		}
	}
	return result
}

func filterAnomalousGames(rows []GameSummary) []GameSummary {
	result := rows[:0]
	for _, row := range rows {
		if len(row.Anomalies) > 0 {
			result = append(result, row)
		}
	}
	return result
}
