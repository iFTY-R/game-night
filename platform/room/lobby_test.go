package room

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/clock"
)

func TestPublicRoomPrimaryActionFollowsMembershipAdmissionAndCapacity(t *testing.T) {
	now := time.Date(2026, time.July, 19, 20, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*PublicRoomCardSnapshot)
		want   PublicRoomPrimaryAction
	}{
		{name: "member enters", mutate: func(card *PublicRoomCardSnapshot) { card.ViewerRole = MemberRoleParticipant }, want: PublicRoomPrimaryActionEnterRoom},
		{name: "open lobby joins", mutate: func(*PublicRoomCardSnapshot) {}, want: PublicRoomPrimaryActionJoin},
		{name: "approval lobby requests", mutate: func(card *PublicRoomCardSnapshot) { card.ParticipantAdmission = AdmissionApproval }, want: PublicRoomPrimaryActionRequestJoin},
		{name: "full lobby spectates", mutate: func(card *PublicRoomCardSnapshot) { card.ParticipantCount = card.ParticipantCapacity }, want: PublicRoomPrimaryActionSpectate},
		{name: "full closed lobby reports full", mutate: func(card *PublicRoomCardSnapshot) {
			card.ParticipantCount = card.ParticipantCapacity
			card.SpectatorAdmission = AdmissionClosed
		}, want: PublicRoomPrimaryActionFull},
		{name: "postgame waits for host", mutate: func(card *PublicRoomCardSnapshot) {
			card.Status = RoomStatusPostGame
			card.ParticipantAdmission = AdmissionClosed
			card.SpectatorAdmission = AdmissionClosed
		}, want: PublicRoomPrimaryActionWaitForHost},
		{name: "playing room spectates", mutate: func(card *PublicRoomCardSnapshot) {
			card.Status = RoomStatusPlaying
			card.ParticipantAdmission = AdmissionClosed
			card.ActiveGameID = "dice"
		}, want: PublicRoomPrimaryActionSpectate},
		{name: "playing approval requests spectate", mutate: func(card *PublicRoomCardSnapshot) {
			card.Status = RoomStatusPlaying
			card.ParticipantAdmission = AdmissionClosed
			card.SpectatorAdmission = AdmissionApproval
			card.ActiveGameID = "dice"
		}, want: PublicRoomPrimaryActionRequestSpectate},
		{name: "closed spectating reports in progress", mutate: func(card *PublicRoomCardSnapshot) {
			card.Status = RoomStatusPlaying
			card.ParticipantAdmission = AdmissionClosed
			card.SpectatorAdmission = AdmissionClosed
			card.ActiveGameID = "dice"
		}, want: PublicRoomPrimaryActionInProgress},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := publicRoomCardFixture(now)
			test.mutate(&snapshot)
			card, err := RestorePublicRoomCard(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if got := card.PrimaryAction(); got != test.want {
				t.Fatalf("primary action = %q, want %q", got, test.want)
			}
		})
	}
}

func TestServiceListsPublicRoomsWithLookaheadCursor(t *testing.T) {
	now := time.Date(2026, time.July, 19, 21, 0, 0, 0, time.UTC)
	repository := newMemoryRoomRepository()
	for offset := range 3 {
		snapshot := publicRoomCardFixture(now.Add(-time.Duration(offset) * time.Minute))
		snapshot.RoomID = uuid.New()
		card, err := RestorePublicRoomCard(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		repository.publicRooms = append(repository.publicRooms, card)
	}
	service, err := NewService(repository, &sequenceRoomCodeGenerator{codes: []string{"UNUSED1"}}, clock.NewFake(now))
	if err != nil {
		t.Fatal(err)
	}

	page, err := service.ListPublicRooms(t.Context(), ListPublicRoomsCommand{
		ActorUserID: uuid.New(), Filter: PublicRoomFilter{Statuses: []RoomStatus{RoomStatusLobby}}, PageSize: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rooms) != 2 || page.NextCursor.RoomID != page.Rooms[1].Snapshot().RoomID ||
		!page.NextCursor.UpdatedAt.Equal(page.Rooms[1].Snapshot().UpdatedAt) {
		t.Fatalf("page = %+v", page)
	}
	if repository.lastList.PageSize != 2 || repository.lastList.Limit != 3 {
		t.Fatalf("repository request = %+v", repository.lastList)
	}
}

func TestPublicRoomListRequestRejectsInvalidFiltersAndCursor(t *testing.T) {
	actor := uuid.New()
	if _, err := NewPublicRoomListRequest(actor, PublicRoomFilter{Statuses: []RoomStatus{RoomStatusClosed}}, PublicRoomPageCursor{}, 20); !errors.Is(err, ErrInvalidRoomInput) {
		t.Fatalf("closed status error = %v", err)
	}
	if _, err := NewPublicRoomListRequest(actor, PublicRoomFilter{}, PublicRoomPageCursor{RoomID: uuid.New()}, 20); !errors.Is(err, ErrInvalidRoomInput) {
		t.Fatalf("partial cursor error = %v", err)
	}
	request, err := NewPublicRoomListRequest(actor, PublicRoomFilter{GameID: " dice "}, PublicRoomPageCursor{}, 0)
	if err != nil || request.Filter.GameID != "dice" || request.PageSize != DefaultPublicRoomPageSize {
		t.Fatalf("normalized request = %+v, err = %v", request, err)
	}
}

func publicRoomCardFixture(updatedAt time.Time) PublicRoomCardSnapshot {
	return PublicRoomCardSnapshot{
		RoomID: uuid.New(), HostUsername: "RoomHost", Status: RoomStatusLobby,
		ParticipantCapacity: 4, ParticipantCount: 2, ParticipantAdmission: AdmissionOpen,
		SpectatorAdmission: AdmissionOpen, UpdatedAt: updatedAt,
	}
}
