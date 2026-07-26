package room

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	admin "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/outbox"
	roomdomain "github.com/iFTY-R/game-night/platform/room"
)

func TestControlServiceSetAdmissionUsesAggregateAndCAS(t *testing.T) {
	now := time.Date(2026, 7, 26, 14, 0, 0, 0, time.UTC)
	room := newControlRoom(t, now)
	store := &memoryRoomStore{rooms: map[uuid.UUID]roomdomain.Room{room.Snapshot().ID: room}}
	service := newControlService(t, store, nil, now)
	actor := newRoomTestActor(t, now, admin.PermissionRoomsControl, admin.PermissionRoomsRead)

	result, err := service.SetRoomAdmission(context.Background(), actor, SetAdmissionCommand{
		RoomID: room.Snapshot().ID, ParticipantAdmission: roomdomain.AdmissionApproval,
		SpectatorAdmission: roomdomain.AdmissionClosed, ExpectedRoomVersion: room.Version().Room,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CommandOutcomeExecuted || result.Room.ParticipantAdmission != string(roomdomain.AdmissionApproval) ||
		result.Room.SpectatorAdmission != string(roomdomain.AdmissionClosed) {
		t.Fatalf("admission result = %+v", result)
	}

	conflict, err := service.SetRoomAdmission(context.Background(), actor, SetAdmissionCommand{
		RoomID: room.Snapshot().ID, ParticipantAdmission: roomdomain.AdmissionOpen,
		SpectatorAdmission: roomdomain.AdmissionOpen, ExpectedRoomVersion: room.Version().Room,
	})
	if err != nil {
		t.Fatal(err)
	}
	if conflict.Outcome != CommandOutcomeVersionConflict || conflict.CurrentRoomVersion == 0 {
		t.Fatalf("conflict result = %+v", conflict)
	}
}

func TestControlServiceRemoveMemberEmitsAdminRevocationEvent(t *testing.T) {
	now := time.Date(2026, 7, 26, 15, 0, 0, 0, time.UTC)
	room := newPlayingControlRoom(t, now)
	memberID := room.Snapshot().Members[1].UserID
	store := &memoryRoomStore{rooms: map[uuid.UUID]roomdomain.Room{room.Snapshot().ID: room}}
	service := newControlService(t, store, nil, now)
	actor := newRoomTestActor(t, now, admin.PermissionRoomsControl, admin.PermissionRoomsRead)

	result, err := service.RemoveRoomMember(context.Background(), actor, RemoveMemberCommand{
		RoomID: room.Snapshot().ID, UserID: memberID,
		ExpectedRoomVersion: room.Version().Room, ExpectedMembershipVersion: room.Version().Membership,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CommandOutcomeExecuted || result.RevokedConnections != 1 || len(store.events) != 1 {
		t.Fatalf("remove result=%+v events=%d", result, len(store.events))
	}
	fact, err := roomdomain.ParseParticipantRevokedEvent(store.events[0])
	if err != nil {
		t.Fatal(err)
	}
	if fact.ActorKind != roomdomain.RemovalActorAdmin || fact.ActorID != actor.AdminID() || fact.UserID != memberID {
		t.Fatalf("revocation fact = %+v", fact)
	}
}

func TestControlServiceForceCloseRejectsPlayingRoom(t *testing.T) {
	now := time.Date(2026, 7, 26, 16, 0, 0, 0, time.UTC)
	room := newPlayingControlRoom(t, now)
	store := &memoryRoomStore{rooms: map[uuid.UUID]roomdomain.Room{room.Snapshot().ID: room}}
	service := newControlService(t, store, nil, now)
	actor := newRoomTestActor(t, now, admin.PermissionRoomsControl, admin.PermissionRoomsRead)

	_, err := service.ForceCloseRoom(context.Background(), actor, ForceCloseRoomCommand{RoomID: room.Snapshot().ID, ExpectedRoomVersion: room.Version().Room})
	if !errors.Is(err, roomdomain.ErrSessionActive) {
		t.Fatalf("force close playing error = %v", err)
	}
}

func TestControlServiceForceTerminateDoesNotFallbackWhenOwnerUnavailable(t *testing.T) {
	now := time.Date(2026, 7, 26, 17, 0, 0, 0, time.UTC)
	controller := &memoryGameController{err: ErrRepositoryUnavailable}
	service := newControlService(t, &memoryRoomStore{}, controller, now)
	actor := newRoomTestActor(t, now, admin.PermissionGamesControl)

	result, err := service.ForceTerminateGame(context.Background(), actor, ForceTerminateGameCommand{
		SessionID: uuid.New(), ExpectedStateVersion: 3, ExpectedOwnershipEpoch: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CommandOutcomeOwnerUnreachable || !result.RepairRequired || controller.calls != 1 {
		t.Fatalf("terminate result=%+v calls=%d", result, controller.calls)
	}
}

type memoryRoomStore struct {
	rooms  map[uuid.UUID]roomdomain.Room
	events []outbox.Event
}

func (store *memoryRoomStore) GetByID(_ context.Context, roomID uuid.UUID) (roomdomain.Room, error) {
	room, ok := store.rooms[roomID]
	if !ok {
		return roomdomain.Room{}, roomdomain.ErrRoomNotFound
	}
	return room, nil
}

func (store *memoryRoomStore) UpdateCAS(_ context.Context, before, next roomdomain.Room) (roomdomain.Room, error) {
	current, ok := store.rooms[before.Snapshot().ID]
	if !ok {
		return roomdomain.Room{}, roomdomain.ErrRoomNotFound
	}
	if current.Version() != before.Version() {
		return roomdomain.Room{}, roomdomain.ErrRoomVersionConflict
	}
	store.rooms[before.Snapshot().ID] = next
	return next, nil
}

func (store *memoryRoomStore) CommitRemoval(ctx context.Context, before, next roomdomain.Room, event outbox.Event) (roomdomain.Room, error) {
	stored, err := store.UpdateCAS(ctx, before, next)
	if err != nil {
		return roomdomain.Room{}, err
	}
	store.events = append(store.events, event)
	return stored, nil
}

type controlQueryRepository struct {
	store *memoryRoomStore
}

func (repository controlQueryRepository) ListRooms(context.Context, RoomListQuery) ([]RoomSummary, error) {
	return nil, nil
}

func (repository controlQueryRepository) GetRoom(_ context.Context, roomID uuid.UUID) (RoomDetail, error) {
	room, err := repository.store.GetByID(context.Background(), roomID)
	if err != nil {
		return RoomDetail{}, err
	}
	return RoomDetail{Summary: roomSummaryFromDomain(room), SampledAt: room.Snapshot().UpdatedAt}, nil
}

func (repository controlQueryRepository) ListGames(context.Context, GameListQuery) ([]GameSummary, error) {
	return nil, nil
}

func (repository controlQueryRepository) GetGame(context.Context, uuid.UUID) (GameDetail, error) {
	return GameDetail{}, ErrNotFound
}

type memoryGameController struct {
	calls int
	err   error
}

func (controller *memoryGameController) TerminateGame(context.Context, ForceTerminateGameCommand) (GameSummary, bool, error) {
	controller.calls++
	if controller.err != nil {
		return GameSummary{}, false, controller.err
	}
	return GameSummary{SessionID: uuid.New(), Status: "cancelled", StateVersion: 4}, false, nil
}

func newControlService(t testing.TB, store *memoryRoomStore, controller GameController, now time.Time) *Service {
	t.Helper()
	service, err := NewService(Config{
		Repository: controlQueryRepository{store: store}, Rooms: store, Games: controller, Clock: clock.NewFake(now),
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func newControlRoom(t testing.TB, now time.Time) roomdomain.Room {
	t.Helper()
	room, err := roomdomain.New(uuid.New(), uuid.New(), "ABC123", roomdomain.VisibilityPrivate, 4, now)
	if err != nil {
		t.Fatal(err)
	}
	return room
}

func newPlayingControlRoom(t testing.TB, now time.Time) roomdomain.Room {
	t.Helper()
	hostID, memberID := uuid.New(), uuid.New()
	sessionID := uuid.New()
	room, err := roomdomain.Restore(roomdomain.RoomSnapshot{
		ID: uuid.New(), RoomCode: "ABC123", Visibility: roomdomain.VisibilityPrivate, Status: roomdomain.RoomStatusPlaying,
		HostUserID: hostID, ParticipantCapacity: 4, ParticipantAdmission: roomdomain.AdmissionClosed,
		SpectatorAdmission: roomdomain.AdmissionOpen, ActiveSessionID: sessionID, ActiveGameID: "liars-dice",
		Members: []roomdomain.MemberSnapshot{
			{UserID: hostID, Role: roomdomain.MemberRoleParticipant, SeatIndex: 0, JoinedAt: now, LastSeenAt: now},
			{UserID: memberID, Role: roomdomain.MemberRoleParticipant, SeatIndex: 1, JoinedAt: now, LastSeenAt: now},
		},
		RoomVersion: 3, MembershipVersion: 2, OwnershipEpoch: 1, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return room
}

func roomSummaryFromDomain(room roomdomain.Room) RoomSummary {
	snapshot := room.Snapshot()
	return RoomSummary{
		RoomID: snapshot.ID, RoomCode: snapshot.RoomCode, Status: string(snapshot.Status), ActiveGameID: snapshot.ActiveGameID,
		ActiveSessionID: snapshot.ActiveSessionID, HostUserID: snapshot.HostUserID, ParticipantAdmission: string(snapshot.ParticipantAdmission),
		SpectatorAdmission: string(snapshot.SpectatorAdmission), RoomVersion: snapshot.RoomVersion,
		MembershipVersion: snapshot.MembershipVersion, OwnershipEpoch: snapshot.OwnershipEpoch, CreatedAt: snapshot.CreatedAt,
		UpdatedAt: snapshot.UpdatedAt, LastActivityAt: snapshot.UpdatedAt,
	}
}
