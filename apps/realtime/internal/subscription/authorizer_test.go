package subscription

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	gamev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1"
	"github.com/iFTY-R/game-night/platform/clock"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	roomdomain "github.com/iFTY-R/game-night/platform/room"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const subscriptionOrigin = "https://play.game-night.test"

func TestAuthorizerAcceptsExactOneTimeGrantAndCurrentPlayerSeat(t *testing.T) {
	fixture := newSubscriptionFixture(t)
	grant := fixture.grant(t, fixture.hostID, gamev1.ViewerKind_VIEWER_KIND_PLAYER, 2, 1, fixture.now.Add(30*time.Second))
	authorization, err := fixture.authorizer.Accept(t.Context(), subscriptionOrigin, []byte(fixture.ticket), grant)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.tickets.calls != 1 || !bytes.Equal(fixture.tickets.grant, grant) || authorization.UserID != fixture.hostID ||
		authorization.Viewer.Kind != game.ViewerPlayer || authorization.Viewer.SeatIndex != 2 || !authorization.Host ||
		authorization.Cursor != 1 || authorization.CurrentVersion != 1 {
		t.Fatalf("tickets=%+v authorization=%+v", fixture.tickets, authorization)
	}
}

func TestAuthorizerRejectsHandshakeBeforeTicketConsumption(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		origin string
		mutate func(*subscriptionFixture, []byte) []byte
		error  error
	}{
		{name: "origin mismatch", origin: "https://other.game-night.test", error: ErrInvalidHandshake},
		{name: "expired", origin: subscriptionOrigin, error: ErrGrantExpired, mutate: func(fixture *subscriptionFixture, _ []byte) []byte {
			return fixture.grant(t, fixture.hostID, gamev1.ViewerKind_VIEWER_KIND_PLAYER, 2, 1, fixture.now)
		}},
		{name: "future lifetime", origin: subscriptionOrigin, error: ErrInvalidHandshake, mutate: func(fixture *subscriptionFixture, _ []byte) []byte {
			return fixture.grant(t, fixture.hostID, gamev1.ViewerKind_VIEWER_KIND_PLAYER, 2, 1, fixture.now.Add(time.Minute))
		}},
		{name: "unknown protobuf field", origin: subscriptionOrigin, error: ErrInvalidHandshake, mutate: func(_ *subscriptionFixture, grant []byte) []byte {
			return append(grant, 0xa0, 0x06, 0x01)
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newSubscriptionFixture(t)
			grant := fixture.grant(t, fixture.hostID, gamev1.ViewerKind_VIEWER_KIND_PLAYER, 2, 1, fixture.now.Add(30*time.Second))
			if testCase.mutate != nil {
				grant = testCase.mutate(&fixture, grant)
			}
			_, err := fixture.authorizer.Accept(t.Context(), testCase.origin, []byte(fixture.ticket), grant)
			if !errors.Is(err, testCase.error) || fixture.tickets.calls != 0 {
				t.Fatalf("error=%v ticket calls=%d", err, fixture.tickets.calls)
			}
		})
	}
}

func TestAuthorizerRejectsConsumedOrStaleAuthority(t *testing.T) {
	t.Run("ticket mismatch", func(t *testing.T) {
		fixture := newSubscriptionFixture(t)
		fixture.tickets.consume = false
		grant := fixture.grant(t, fixture.hostID, gamev1.ViewerKind_VIEWER_KIND_PLAYER, 2, 1, fixture.now.Add(30*time.Second))
		_, err := fixture.authorizer.Accept(t.Context(), subscriptionOrigin, []byte(fixture.ticket), grant)
		if !errors.Is(err, ErrTicketRejected) {
			t.Fatalf("error = %v", err)
		}
	})

	for _, testCase := range []struct {
		name   string
		seat   uint32
		cursor uint64
		error  error
	}{
		{name: "seat changed", seat: 3, cursor: 1, error: ErrAuthorizationChanged},
		{name: "future cursor", seat: 2, cursor: 2, error: ErrUnauthorized},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newSubscriptionFixture(t)
			grant := fixture.grant(t, fixture.hostID, gamev1.ViewerKind_VIEWER_KIND_PLAYER, testCase.seat, testCase.cursor, fixture.now.Add(30*time.Second))
			_, err := fixture.authorizer.Accept(t.Context(), subscriptionOrigin, []byte(fixture.ticket), grant)
			if !errors.Is(err, testCase.error) || fixture.tickets.calls != 1 {
				t.Fatalf("error=%v ticket calls=%d", err, fixture.tickets.calls)
			}
		})
	}
}

func TestAuthorizerRefreshRevokesRoleChangesAndForcesSnapshotOnHostChange(t *testing.T) {
	fixture := newSubscriptionFixture(t)
	grant := fixture.grant(t, fixture.hostID, gamev1.ViewerKind_VIEWER_KIND_PLAYER, 2, 1, fixture.now.Add(30*time.Second))
	previous, err := fixture.authorizer.Accept(t.Context(), subscriptionOrigin, []byte(fixture.ticket), grant)
	if err != nil {
		t.Fatal(err)
	}

	roomSnapshot := fixture.rooms.room.Snapshot()
	roomSnapshot.HostUserID = fixture.otherID
	roomSnapshot.RoomVersion++
	fixture.rooms.room, err = roomdomain.Restore(roomSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := fixture.authorizer.Refresh(t.Context(), previous)
	if err != nil || !refreshed.SnapshotRequired || refreshed.Authorization.Host {
		t.Fatalf("refreshed=%+v error=%v", refreshed, err)
	}

	roomSnapshot = fixture.rooms.room.Snapshot()
	for index := range roomSnapshot.Members {
		if roomSnapshot.Members[index].UserID == fixture.hostID {
			roomSnapshot.Members[index].Role = roomdomain.MemberRoleSpectator
			roomSnapshot.Members[index].SeatIndex = 0
		}
	}
	roomSnapshot.MembershipVersion++
	fixture.rooms.room, err = roomdomain.Restore(roomSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.authorizer.Refresh(t.Context(), refreshed.Authorization); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("role change error = %v", err)
	}
}

type subscriptionFixture struct {
	authorizer *Authorizer
	tickets    *fakeSubscriptionTickets
	rooms      *fakeSubscriptionRooms
	sessions   *fakeSubscriptionSessions
	now        time.Time
	ticket     string
	hostID     uuid.UUID
	otherID    uuid.UUID
}

func newSubscriptionFixture(t testing.TB) subscriptionFixture {
	t.Helper()
	now := time.Date(2026, time.July, 20, 19, 0, 0, 0, time.UTC)
	hostID, otherID, roomID, sessionID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	room, err := roomdomain.Restore(roomdomain.RoomSnapshot{
		ID: roomID, RoomCode: "SUB123", Visibility: roomdomain.VisibilityPrivate, Status: roomdomain.RoomStatusPlaying,
		HostUserID: hostID, ParticipantCapacity: 8, ParticipantAdmission: roomdomain.AdmissionClosed,
		SpectatorAdmission: roomdomain.AdmissionOpen,
		Members: []roomdomain.MemberSnapshot{
			{UserID: hostID, Role: roomdomain.MemberRoleParticipant, SeatIndex: 2, JoinedAt: now, LastSeenAt: now},
			{UserID: otherID, Role: roomdomain.MemberRoleParticipant, SeatIndex: 4, JoinedAt: now, LastSeenAt: now},
		},
		ActiveSessionID: sessionID, ActiveGameID: "liars-dice", RoomVersion: 2, MembershipVersion: 2,
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := gameruntime.RestoreSession(gameruntime.SessionSnapshot{
		ID: sessionID, RoomID: roomID,
		VersionKey:     game.VersionKey{GameID: "liars-dice", Engine: "1.0.0", Protocol: "1.0.0", Client: "1.0.0"},
		OwnershipEpoch: 1,
		Participants:   []gameruntime.Participant{{UserID: hostID, SeatIndex: 2}, {UserID: otherID, SeatIndex: 4}},
		State: game.Snapshot{
			SnapshotVersion: 1, StateVersion: 1,
			State: game.Message{MessageType: "round.state", SchemaVersion: 1},
		},
		Status: gameruntime.StatusActive, StartedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	tickets := &fakeSubscriptionTickets{consume: true}
	rooms := &fakeSubscriptionRooms{room: room}
	sessions := &fakeSubscriptionSessions{session: session}
	authorizer, err := NewAuthorizer(tickets, rooms, sessions, clock.NewFake(now), Config{MaximumGrantLifetime: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return subscriptionFixture{
		authorizer: authorizer, tickets: tickets, rooms: rooms, sessions: sessions, now: now,
		ticket: string(bytes.Repeat([]byte{'t'}, 43)), hostID: hostID, otherID: otherID,
	}
}

func (fixture subscriptionFixture) grant(
	t testing.TB,
	userID uuid.UUID,
	kind gamev1.ViewerKind,
	seat uint32,
	cursor uint64,
	expiresAt time.Time,
) []byte {
	t.Helper()
	value := &gamev1.SubscriptionGrant{
		UserId: userID.String(), RoomId: fixture.rooms.room.Snapshot().ID.String(),
		SessionId: fixture.sessions.session.Snapshot().ID.String(), ViewerKind: kind, SeatIndex: seat,
		LastStateVersion: cursor, Origin: subscriptionOrigin, ExpiresAt: timestamppb.New(expiresAt),
	}
	raw, err := (proto.MarshalOptions{Deterministic: true}).Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type fakeSubscriptionTickets struct {
	consume bool
	calls   int
	ticket  string
	grant   []byte
}

func (tickets *fakeSubscriptionTickets) ConsumeConnectionTicket(_ context.Context, ticket string, grant []byte) (bool, error) {
	tickets.calls++
	tickets.ticket = ticket
	tickets.grant = append([]byte(nil), grant...)
	return tickets.consume, nil
}

type fakeSubscriptionRooms struct{ room roomdomain.Room }

func (rooms *fakeSubscriptionRooms) GetByID(context.Context, uuid.UUID) (roomdomain.Room, error) {
	return rooms.room, nil
}

type fakeSubscriptionSessions struct{ session gameruntime.Session }

func (sessions *fakeSubscriptionSessions) Get(context.Context, uuid.UUID) (gameruntime.Session, error) {
	return sessions.session, nil
}
