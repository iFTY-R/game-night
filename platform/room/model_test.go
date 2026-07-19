package room

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRoomLifecycleKeepsPostGameAdmissionClosed(t *testing.T) {
	now := time.Date(2026, time.July, 19, 10, 0, 0, 0, time.UTC)
	host, first, late := uuid.New(), uuid.New(), uuid.New()
	room, err := New(uuid.New(), host, "ABCD12", VisibilityPrivate, 3, now)
	if err != nil {
		t.Fatal(err)
	}

	room, joined, err := room.Join(first, JoinIntentParticipant, room.Version(), now.Add(time.Second))
	if err != nil || joined.Member.SeatIndex != 1 {
		t.Fatalf("join first: member=%+v err=%v", joined.Member, err)
	}
	room, started, err := room.StartSession(host, uuid.New(), "dice", 2, 9, room.Version(), now.Add(2*time.Second))
	if err != nil || len(started.Participants) != 2 || room.Snapshot().ParticipantAdmission != AdmissionClosed {
		t.Fatalf("start session: started=%+v room=%+v err=%v", started, room.Snapshot(), err)
	}
	if _, _, err := room.Join(late, JoinIntentParticipant, room.Version(), now.Add(3*time.Second)); !errors.Is(err, ErrAdmissionClosed) {
		t.Fatalf("new participant during game error=%v", err)
	}
	room, _, err = room.Join(late, JoinIntentSpectator, room.Version(), now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	room, err = room.FinishSession(started.SessionID, room.Version(), now.Add(4*time.Second))
	if err != nil || room.Snapshot().Status != RoomStatusLobby || room.Snapshot().ParticipantAdmission != AdmissionClosed {
		t.Fatalf("finish session: snapshot=%+v err=%v", room.Snapshot(), err)
	}
	if _, _, err := room.Join(uuid.New(), JoinIntentParticipant, room.Version(), now.Add(5*time.Second)); !errors.Is(err, ErrAdmissionClosed) {
		t.Fatalf("post-game participant admission error=%v", err)
	}
	room, err = room.SetAdmission(host, AdmissionOpen, AdmissionOpen, room.Version(), now.Add(6*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := room.Join(uuid.New(), JoinIntentParticipant, room.Version(), now.Add(7*time.Second)); err != nil {
		t.Fatalf("reopened participant admission: %v", err)
	}
}

func TestRoomApprovalPromotesWaitingMemberToLowestStableSeat(t *testing.T) {
	now := time.Date(2026, time.July, 19, 11, 0, 0, 0, time.UTC)
	host, participant, waiting := uuid.New(), uuid.New(), uuid.New()
	room, err := New(uuid.New(), host, "ROOM99", VisibilityPublic, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	room, _, err = room.Join(participant, JoinIntentParticipant, room.Version(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	room, err = room.SetAdmission(host, AdmissionApproval, AdmissionOpen, room.Version(), now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	room, joined, err := room.Join(waiting, JoinIntentParticipant, room.Version(), now.Add(3*time.Second))
	if err != nil || joined.Member.Role != MemberRoleWaiting || joined.Member.RequestedRole != MemberRoleParticipant {
		t.Fatalf("waiting join: member=%+v err=%v", joined.Member, err)
	}
	approved, result, err := room.ApproveWaiting(host, waiting, room.Version(), now.Add(4*time.Second))
	if err != nil || result.Member.Role != MemberRoleParticipant || result.Member.SeatIndex != 2 || approved.Version() != result.Version {
		t.Fatalf("approve waiting: member=%+v result=%+v err=%v", result.Member, result, err)
	}
	if _, _, err := approved.ApproveWaiting(host, waiting, approved.Version(), now.Add(5*time.Second)); !errors.Is(err, ErrWaitingNotFound) {
		t.Fatalf("second approval error=%v", err)
	}
}

func TestRoomVersionsAndHostPermissionsAreCheckedBeforeMutation(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	host, other := uuid.New(), uuid.New()
	room, err := New(uuid.New(), host, "VERS01", VisibilityPrivate, 2, now)
	if err != nil {
		t.Fatal(err)
	}
	stale := room.Version()
	room, _, err = room.Join(other, JoinIntentParticipant, stale, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := room.SetAdmission(other, AdmissionClosed, AdmissionClosed, room.Version(), now.Add(2*time.Second)); !errors.Is(err, ErrHostRequired) {
		t.Fatalf("non-host error=%v", err)
	}
	if _, err := room.SetAdmission(host, AdmissionClosed, AdmissionClosed, stale, now.Add(2*time.Second)); !errors.Is(err, ErrRoomVersionConflict) {
		t.Fatalf("stale version error=%v", err)
	}
	if _, _, err := room.Join(uuid.New(), JoinIntentParticipant, stale, now.Add(3*time.Second)); !errors.Is(err, ErrRoomVersionConflict) {
		t.Fatalf("stale join error=%v", err)
	}
}

func TestRemovingActiveParticipantReportsRuntimeRevocation(t *testing.T) {
	now := time.Date(2026, time.July, 19, 13, 0, 0, 0, time.UTC)
	host, participant := uuid.New(), uuid.New()
	room, err := New(uuid.New(), host, "KICK77", VisibilityPrivate, 2, now)
	if err != nil {
		t.Fatal(err)
	}
	room, _, err = room.Join(participant, JoinIntentParticipant, room.Version(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	room, started, err := room.StartSession(host, uuid.New(), "poker", 2, 9, room.Version(), now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	room, result, err := room.RemoveMember(host, participant, room.Version(), now.Add(3*time.Second))
	if err != nil || !result.ParticipantRevoked || result.SessionID != started.SessionID || result.Removed.UserID != participant {
		t.Fatalf("remove active participant: result=%+v err=%v", result, err)
	}
	if _, ok := room.Member(participant); ok {
		t.Fatal("removed participant remains in room")
	}
	if _, _, err := room.RemoveMember(host, host, room.Version(), now.Add(4*time.Second)); !errors.Is(err, ErrCannotRemoveHost) {
		t.Fatalf("remove host error=%v", err)
	}
}

func TestStartSessionRejectsMoreParticipantsThanGameManifestSupports(t *testing.T) {
	now := time.Date(2026, time.July, 19, 13, 30, 0, 0, time.UTC)
	host := uuid.New()
	room, err := New(uuid.New(), host, "LIMIT9", VisibilityPrivate, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	for offset := 1; offset <= 2; offset++ {
		room, _, err = room.Join(uuid.New(), JoinIntentParticipant, room.Version(), now.Add(time.Duration(offset)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := room.StartSession(
		host, uuid.New(), "two-seat-game", 2, 2, room.Version(), now.Add(3*time.Second),
	); !errors.Is(err, ErrParticipantLimitExceeded) {
		t.Fatalf("participant limit error = %v", err)
	}
}

func TestRestoreRejectsPlayingRoomWithoutClosedParticipantAdmission(t *testing.T) {
	now := time.Date(2026, time.July, 19, 14, 0, 0, 0, time.UTC)
	host := uuid.New()
	room, err := New(uuid.New(), host, "BAD123", VisibilityPrivate, 2, now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := room.Snapshot()
	snapshot.Status = RoomStatusPlaying
	snapshot.ActiveSessionID = uuid.New()
	snapshot.ActiveGameID = "dice"
	if _, err := Restore(snapshot); !errors.Is(err, ErrInvalidRoomInput) {
		t.Fatalf("invalid playing snapshot error=%v", err)
	}
	snapshot.Status = RoomStatusClosed
	snapshot.ActiveSessionID = uuid.Nil
	snapshot.ActiveGameID = ""
	if _, err := Restore(snapshot); !errors.Is(err, ErrInvalidRoomInput) {
		t.Fatalf("open closed-room snapshot error=%v", err)
	}
}
