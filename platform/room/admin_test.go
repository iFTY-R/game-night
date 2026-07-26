package room

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAdminRoomCommandsUseExactVersionsWithoutHostAuthority(t *testing.T) {
	now := time.Date(2026, time.July, 26, 10, 0, 0, 0, time.UTC)
	admin := AdminActor{ID: uuid.New()}
	host, participant := uuid.New(), uuid.New()
	room, err := New(uuid.New(), host, "ADMIN1", VisibilityPrivate, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	room, _, err = room.Join(participant, JoinIntentParticipant, room.Version(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	stale := room.Version()
	room, err = room.SetAdmissionByAdmin(admin, AdmissionApproval, AdmissionClosed, room.Version(), now.Add(2*time.Second))
	if err != nil || room.Snapshot().HostUserID != host || room.Snapshot().ParticipantAdmission != AdmissionApproval ||
		room.Snapshot().SpectatorAdmission != AdmissionClosed {
		t.Fatalf("admin admission: room=%+v err=%v", room.Snapshot(), err)
	}
	if _, err = room.SetAdmissionByAdmin(admin, AdmissionOpen, AdmissionOpen, stale, now.Add(3*time.Second)); !errors.Is(err, ErrRoomVersionConflict) {
		t.Fatalf("stale admin admission error=%v", err)
	}
	if _, err = room.SetAdmissionByAdmin(AdminActor{}, AdmissionOpen, AdmissionOpen, room.Version(), now.Add(3*time.Second)); !errors.Is(err, ErrInvalidRoomInput) {
		t.Fatalf("empty admin actor error=%v", err)
	}
}

func TestAdminRemoveMemberPreservesHostAndReportsActiveParticipantRevocation(t *testing.T) {
	now := time.Date(2026, time.July, 26, 10, 30, 0, 0, time.UTC)
	admin := AdminActor{ID: uuid.New()}
	host, participant, spectator := uuid.New(), uuid.New(), uuid.New()
	room, err := New(uuid.New(), host, "ADMIN2", VisibilityPrivate, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	room, _, err = room.Join(participant, JoinIntentParticipant, room.Version(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	room, _, err = room.Join(spectator, JoinIntentSpectator, room.Version(), now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	playing, started, err := room.StartSession(host, uuid.New(), "dice", 2, 9, room.Version(), now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = playing.RemoveMemberByAdmin(admin, host, playing.Version(), now.Add(4*time.Second)); !errors.Is(err, ErrCannotRemoveHost) {
		t.Fatalf("admin removed host error=%v", err)
	}
	removed, result, err := playing.RemoveMemberByAdmin(admin, participant, playing.Version(), now.Add(4*time.Second))
	if err != nil || !result.ParticipantRevoked || result.SessionID != started.SessionID || result.Removed.UserID != participant ||
		removed.Version().Membership != playing.Version().Membership+1 {
		t.Fatalf("admin remove participant: result=%+v room=%+v err=%v", result, removed.Snapshot(), err)
	}
	if _, ok := removed.Member(participant); ok {
		t.Fatal("admin-removed participant remains in room")
	}
	spectatorRemoved, spectatorResult, err := removed.RemoveMemberByAdmin(admin, spectator, removed.Version(), now.Add(5*time.Second))
	if err != nil || spectatorResult.ParticipantRevoked || spectatorRemoved.Version().Membership != removed.Version().Membership+1 {
		t.Fatalf("admin remove spectator: result=%+v room=%+v err=%v", spectatorResult, spectatorRemoved.Snapshot(), err)
	}
}

func TestAdminCloseWaitingRoomRejectsActiveGame(t *testing.T) {
	now := time.Date(2026, time.July, 26, 11, 0, 0, 0, time.UTC)
	admin := AdminActor{ID: uuid.New()}
	host, participant := uuid.New(), uuid.New()
	room, err := New(uuid.New(), host, "ADMIN3", VisibilityPrivate, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := room.CloseWaitingByAdmin(admin, room.Version(), now.Add(time.Second))
	if err != nil || closed.Snapshot().Status != RoomStatusClosed || closed.Snapshot().HostUserID != host {
		t.Fatalf("admin close lobby: room=%+v err=%v", closed.Snapshot(), err)
	}
	room, _, err = room.Join(participant, JoinIntentParticipant, room.Version(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	playing, _, err := room.StartSession(host, uuid.New(), "dice", 2, 9, room.Version(), now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = playing.CloseWaitingByAdmin(admin, playing.Version(), now.Add(3*time.Second)); !errors.Is(err, ErrSessionActive) {
		t.Fatalf("admin close active room error=%v", err)
	}
}
