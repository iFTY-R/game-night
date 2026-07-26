package room

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	admin "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/security"
)

func TestQueryServiceEnrichesRoomOwnersAndFiltersAnomalies(t *testing.T) {
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	freshSession := uuid.MustParse("018f3f7c-296f-7a4e-8e16-0bba70f3f4aa")
	staleSession := uuid.MustParse("018f3f7c-296f-7a4e-8e16-0bba70f3f4ab")
	missingSession := uuid.MustParse("018f3f7c-296f-7a4e-8e16-0bba70f3f4ac")
	repository := &memoryRoomQueryRepository{rooms: []RoomSummary{
		{RoomID: uuid.New(), RoomCode: "FRESH", ActiveSessionID: freshSession, OwnershipEpoch: 7, RoomVersion: 1, MembershipVersion: 1},
		{RoomID: uuid.New(), RoomCode: "STALE", ActiveSessionID: staleSession, OwnershipEpoch: 8, RoomVersion: 1, MembershipVersion: 1},
		{RoomID: uuid.New(), RoomCode: "MISSING", ActiveSessionID: missingSession, OwnershipEpoch: 9, RoomVersion: 1, MembershipVersion: 1},
	}}
	owners := &memoryOwnerReader{owners: map[uuid.UUID]OwnerLeaseSummary{
		freshSession: {SessionID: freshSession, OwnerInstance: "rt-1", OwnerAddress: "10.0.0.1:9000", OwnershipEpoch: 7, Freshness: OwnerFreshnessFresh},
		staleSession: {SessionID: staleSession, OwnerInstance: "rt-2", OwnerAddress: "10.0.0.2:9000", OwnershipEpoch: 3, Freshness: OwnerFreshnessFresh},
	}}
	service := newRoomQueryService(t, repository, owners, now)

	page, err := service.ListRooms(context.Background(), newRoomTestActor(t, now, admin.PermissionRoomsRead), RoomListQuery{PageSize: 10, AnomaliesOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rooms) != 2 || page.Rooms[0].RoomCode != "STALE" || page.Rooms[1].RoomCode != "MISSING" {
		t.Fatalf("filtered anomalous rooms = %+v", page.Rooms)
	}
	if page.Rooms[0].Owner.Freshness != OwnerFreshnessStale || page.Rooms[0].Anomalies[0] != RoomAnomalyOwnerStale {
		t.Fatalf("stale owner not marked: %+v", page.Rooms[0])
	}
	if page.Rooms[1].Owner.Freshness != OwnerFreshnessMissing || page.Rooms[1].Anomalies[0] != RoomAnomalyOwnerMissing {
		t.Fatalf("missing owner not marked: %+v", page.Rooms[1])
	}
	if repository.roomsQueries[0].AnomaliesOnly {
		t.Fatal("service delegated anomalies_only to PostgreSQL before Redis anomalies were known")
	}
}

func TestQueryServiceMarksUnknownOwnerWhenReaderUnavailable(t *testing.T) {
	now := time.Date(2026, 7, 26, 11, 0, 0, 0, time.UTC)
	sessionID := uuid.New()
	repository := &memoryRoomQueryRepository{games: []GameSummary{{
		SessionID: sessionID, RoomID: uuid.New(), GameID: "liars-dice", Status: "active", OwnershipEpoch: 4,
		StateVersion: 3, LastProgressAt: now.Add(-time.Minute),
	}}}
	service := newRoomQueryService(t, repository, &memoryOwnerReader{err: ErrRepositoryUnavailable}, now)

	page, err := service.ListGames(context.Background(), newRoomTestActor(t, now, admin.PermissionGamesRead), GameListQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Games) != 1 || page.Games[0].Owner.Freshness != OwnerFreshnessUnknown || page.Games[0].Anomalies[0] != GameAnomalyOwnerStale {
		t.Fatalf("unknown owner not fail-closed: %+v", page.Games)
	}
	if page.PageSize != DefaultGamePageSize {
		t.Fatalf("default game page size = %d", page.PageSize)
	}
}

func TestQueryServiceDetailsMarkOfflineAndSlowProgress(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	roomID := uuid.New()
	sessionID := uuid.New()
	repository := &memoryRoomQueryRepository{roomDetail: RoomDetail{
		Summary: RoomSummary{RoomID: roomID, ActiveSessionID: sessionID, OwnershipEpoch: 2, RoomVersion: 1, MembershipVersion: 3},
		Members: []RoomMemberSummary{
			{UserID: uuid.New(), Username: "alice", Online: false},
			{UserID: uuid.New(), Username: "bob", Online: false},
		},
		ActiveGames: []GameSummary{{
			SessionID: sessionID, RoomID: roomID, GameID: "liars-dice", Status: "active", StateVersion: 5,
			OwnershipEpoch: 2, LastProgressAt: now.Add(-DefaultGameProgressWindow - time.Second),
		}},
		SampledAt: now,
	}}
	service := newRoomQueryService(t, repository, &memoryOwnerReader{owners: map[uuid.UUID]OwnerLeaseSummary{
		sessionID: {SessionID: sessionID, OwnerInstance: "rt-1", OwnerAddress: "10.0.0.1:9000", OwnershipEpoch: 2, Freshness: OwnerFreshnessFresh},
	}}, now)

	detail, err := service.GetRoom(context.Background(), newRoomTestActor(t, now, admin.PermissionRoomsRead), roomID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Summary.Anomalies[0] != RoomAnomalyAllPlayersOffline {
		t.Fatalf("offline room anomaly missing: %+v", detail.Summary.Anomalies)
	}
	if detail.ActiveGames[0].Anomalies[0] != GameAnomalyNoRecentProgress {
		t.Fatalf("slow progress anomaly missing: %+v", detail.ActiveGames[0].Anomalies)
	}
}

func TestQueryServiceRequiresSpecificReadPermissions(t *testing.T) {
	now := time.Date(2026, 7, 26, 13, 0, 0, 0, time.UTC)
	service := newRoomQueryService(t, &memoryRoomQueryRepository{}, nil, now)
	actor := newRoomTestActor(t, now, admin.PermissionRoomsRead)
	if _, err := service.ListGames(context.Background(), actor, GameListQuery{}); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("ListGames permission error = %v", err)
	}
	if _, err := service.ListRooms(context.Background(), actor, RoomListQuery{}); err != nil {
		t.Fatalf("ListRooms with rooms.read error = %v", err)
	}
}

type memoryRoomQueryRepository struct {
	rooms        []RoomSummary
	games        []GameSummary
	roomDetail   RoomDetail
	gameDetail   GameDetail
	roomsQueries []RoomListQuery
	gamesQueries []GameListQuery
}

func (repository *memoryRoomQueryRepository) ListRooms(_ context.Context, query RoomListQuery) ([]RoomSummary, error) {
	repository.roomsQueries = append(repository.roomsQueries, query)
	return append([]RoomSummary(nil), repository.rooms...), nil
}

func (repository *memoryRoomQueryRepository) GetRoom(context.Context, uuid.UUID) (RoomDetail, error) {
	return repository.roomDetail, nil
}

func (repository *memoryRoomQueryRepository) ListGames(_ context.Context, query GameListQuery) ([]GameSummary, error) {
	repository.gamesQueries = append(repository.gamesQueries, query)
	return append([]GameSummary(nil), repository.games...), nil
}

func (repository *memoryRoomQueryRepository) GetGame(context.Context, uuid.UUID) (GameDetail, error) {
	return repository.gameDetail, nil
}

type memoryOwnerReader struct {
	owners map[uuid.UUID]OwnerLeaseSummary
	err    error
}

func (reader *memoryOwnerReader) ReadOwners(_ context.Context, ids []uuid.UUID, observedAt time.Time) (map[uuid.UUID]OwnerLeaseSummary, error) {
	if reader.err != nil {
		return nil, reader.err
	}
	result := make(map[uuid.UUID]OwnerLeaseSummary, len(ids))
	for _, id := range ids {
		if owner, ok := reader.owners[id]; ok {
			owner.ObservedAt = observedAt
			result[id] = owner
		}
	}
	return result, nil
}

func newRoomQueryService(t testing.TB, repository QueryRepository, owners OwnerReader, now time.Time) *Service {
	t.Helper()
	service, err := NewService(Config{Repository: repository, Owners: owners, Clock: clock.NewFake(now)})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func newRoomTestActor(t testing.TB, now time.Time, permissions ...admin.Permission) admin.ActorContext {
	t.Helper()
	adminID, sessionID := uuid.New(), uuid.New()
	session, err := admin.RestoreSession(admin.SessionSnapshot{
		ID: sessionID, AdminID: adminID, Selector: base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
		SecretMAC: security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: 1, Value: make([]byte, 32)},
		CSRFHash:  security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: 1, Value: make([]byte, 32)},
		Kind:      admin.SessionKindFull, AdminVersion: 1, PasswordVersion: 1, SessionVersion: 1,
		ClientIP: "203.0.113.10", UserAgent: "admin-room-test", MaxAttempts: 5,
		CreatedAt: now.Add(-time.Minute), LastSeenAt: now, IdleExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	permissionSet, err := admin.NewPermissionSet(permissions...)
	if err != nil {
		t.Fatal(err)
	}
	elevations, err := admin.NewElevationSet()
	if err != nil {
		t.Fatal(err)
	}
	actor, err := admin.NewActorContext(adminID, sessionID, session, permissionSet, elevations, 0, "req-admin-room", "http://127.0.0.1:4174", "203.0.113.10", "admin-room-test")
	if err != nil {
		t.Fatal(err)
	}
	return actor
}
