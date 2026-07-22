package room

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/clock"
)

func TestServiceListsMyRoomsWithHostAwareLookaheadCursor(t *testing.T) {
	now := time.Date(2026, time.July, 22, 20, 0, 0, 0, time.UTC)
	repository := newMemoryRoomRepository()
	for offset := range 3 {
		snapshot := myRoomCardFixture(now.Add(-time.Duration(offset) * time.Minute))
		snapshot.RoomID = uuid.New()
		snapshot.IsHost = offset < 2
		card, err := RestoreMyRoomCard(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		repository.myRooms = append(repository.myRooms, card)
	}
	service, err := NewService(repository, &sequenceRoomCodeGenerator{codes: []string{"UNUSED2"}}, clock.NewFake(now))
	if err != nil {
		t.Fatal(err)
	}

	page, err := service.ListMyRooms(t.Context(), ListMyRoomsCommand{ActorUserID: uuid.New(), PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rooms) != 2 || page.NextCursor.RoomID != page.Rooms[1].Snapshot().RoomID ||
		page.NextCursor.IsHost != page.Rooms[1].Snapshot().IsHost ||
		!page.NextCursor.UpdatedAt.Equal(page.Rooms[1].Snapshot().UpdatedAt) {
		t.Fatalf("page = %+v", page)
	}
	if repository.lastMyList.PageSize != 2 || repository.lastMyList.Limit != 3 {
		t.Fatalf("repository request = %+v", repository.lastMyList)
	}
}

func TestMyRoomProjectionRejectsInvalidMembershipAndCursor(t *testing.T) {
	actor := uuid.New()
	if _, err := NewMyRoomListRequest(actor, MyRoomPageCursor{RoomID: uuid.New()}, 20); !errors.Is(err, ErrInvalidRoomInput) {
		t.Fatalf("partial cursor error = %v", err)
	}
	request, err := NewMyRoomListRequest(actor, MyRoomPageCursor{}, 0)
	if err != nil || request.PageSize != DefaultPublicRoomPageSize || request.Limit != DefaultPublicRoomPageSize+1 {
		t.Fatalf("default request = %+v, err = %v", request, err)
	}

	snapshot := myRoomCardFixture(time.Date(2026, time.July, 22, 20, 0, 0, 0, time.UTC))
	snapshot.IsHost = true
	snapshot.ViewerRole = MemberRoleSpectator
	if _, err := RestoreMyRoomCard(snapshot); !errors.Is(err, ErrRoomIntegrity) {
		t.Fatalf("invalid host membership error = %v", err)
	}
}

func myRoomCardFixture(updatedAt time.Time) MyRoomCardSnapshot {
	return MyRoomCardSnapshot{
		RoomID: uuid.New(), RoomCode: "MYROOM", Visibility: VisibilityPrivate, HostUsername: "RoomHost",
		Status: RoomStatusLobby, ParticipantCapacity: 4, ParticipantCount: 1,
		ParticipantAdmission: AdmissionOpen, SpectatorAdmission: AdmissionOpen,
		ViewerRole: MemberRoleParticipant, UpdatedAt: updatedAt,
	}
}
