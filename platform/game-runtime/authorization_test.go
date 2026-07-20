package gameruntime

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	roomdomain "github.com/iFTY-R/game-night/platform/room"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

func TestAuthorizeLiveViewerBindsCurrentRoleToFrozenSeat(t *testing.T) {
	room, session, hostID, spectatorID := liveAuthorizationFixture(t)
	player, err := AuthorizeLiveViewer(room, session, hostID, game.ViewerPlayer)
	if err != nil || player.Viewer.SeatIndex != 3 || !player.Host {
		t.Fatalf("player=%+v error=%v", player, err)
	}
	spectator, err := AuthorizeLiveViewer(room, session, spectatorID, game.ViewerSpectator)
	if err != nil || spectator.Viewer.SeatIndex != 0 || spectator.Host {
		t.Fatalf("spectator=%+v error=%v", spectator, err)
	}
}

func TestAuthorizeLiveViewerRejectsStaleOrMismatchedAuthority(t *testing.T) {
	room, session, hostID, spectatorID := liveAuthorizationFixture(t)
	tests := []struct {
		name     string
		room     roomdomain.Room
		session  Session
		userID   uuid.UUID
		viewer   game.ViewerKind
		expected error
	}{
		{name: "role mismatch", room: room, session: session, userID: spectatorID, viewer: game.ViewerPlayer, expected: ErrParticipantNotActive},
		{name: "missing member", room: room, session: session, userID: uuid.New(), viewer: game.ViewerPlayer, expected: ErrParticipantNotActive},
		{name: "invalid viewer", room: room, session: session, userID: hostID, viewer: game.ViewerReplay, expected: ErrInvalidSessionInput},
	}
	otherRoomSnapshot := room.Snapshot()
	otherRoomSnapshot.ID = uuid.New()
	otherRoomSnapshot.ActiveSessionID = session.Snapshot().ID
	otherRoom, err := roomdomain.Restore(otherRoomSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	tests = append(tests, struct {
		name     string
		room     roomdomain.Room
		session  Session
		userID   uuid.UUID
		viewer   game.ViewerKind
		expected error
	}{name: "cross room", room: otherRoom, session: session, userID: hostID, viewer: game.ViewerPlayer, expected: ErrParticipantNotActive})
	terminalSnapshot := session.Snapshot()
	terminalSnapshot.Status = StatusFinished
	terminalSnapshot.EndedAt = terminalSnapshot.UpdatedAt
	terminal, err := RestoreSession(terminalSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	tests = append(tests, struct {
		name     string
		room     roomdomain.Room
		session  Session
		userID   uuid.UUID
		viewer   game.ViewerKind
		expected error
	}{name: "terminal session", room: room, session: terminal, userID: hostID, viewer: game.ViewerPlayer, expected: ErrParticipantNotActive})

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := AuthorizeLiveViewer(testCase.room, testCase.session, testCase.userID, testCase.viewer)
			if !errors.Is(err, testCase.expected) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func liveAuthorizationFixture(t testing.TB) (roomdomain.Room, Session, uuid.UUID, uuid.UUID) {
	t.Helper()
	now := time.Date(2026, time.July, 20, 18, 0, 0, 0, time.UTC)
	hostID, spectatorID, sessionID, roomID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	room, err := roomdomain.Restore(roomdomain.RoomSnapshot{
		ID: roomID, RoomCode: "AUTH123", Visibility: roomdomain.VisibilityPrivate, Status: roomdomain.RoomStatusPlaying,
		HostUserID: hostID, ParticipantCapacity: 8, ParticipantAdmission: roomdomain.AdmissionClosed,
		SpectatorAdmission: roomdomain.AdmissionOpen,
		Members: []roomdomain.MemberSnapshot{
			{UserID: hostID, Role: roomdomain.MemberRoleParticipant, SeatIndex: 3, JoinedAt: now, LastSeenAt: now},
			{UserID: spectatorID, Role: roomdomain.MemberRoleSpectator, JoinedAt: now, LastSeenAt: now},
		},
		ActiveSessionID: sessionID, ActiveGameID: "liars-dice", RoomVersion: 2, MembershipVersion: 2,
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := RestoreSession(SessionSnapshot{
		ID: sessionID, RoomID: roomID,
		VersionKey:     game.VersionKey{GameID: "liars-dice", Engine: "1.0.0", Protocol: "1.0.0", Client: "1.0.0"},
		OwnershipEpoch: 1, Participants: []Participant{{UserID: hostID, SeatIndex: 3}},
		State:  game.Snapshot{SnapshotVersion: 1, StateVersion: 1, State: game.Message{MessageType: "round.state", SchemaVersion: 1}},
		Status: StatusActive, StartedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return room, session, hostID, spectatorID
}
