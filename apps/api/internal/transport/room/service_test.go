package room

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/cookies"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/csrf"
	transporterrors "github.com/iFTY-R/game-night/apps/api/internal/transport/errors"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/origin"
	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
	gamev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1"
	roomv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/room/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/room/v1/roomv1connect"
	"github.com/iFTY-R/game-night/platform/clock"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/outbox"
	redisstore "github.com/iFTY-R/game-night/platform/persistence/redis"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
	gameSDK "github.com/iFTY-R/game-night/sdk/go/game"
)

const roomTransportOrigin = "https://play.example.test"

func TestRoomConnectFlowCreatesJoinsStartsAndFinishes(t *testing.T) {
	host, guest, newcomer := uuid.New(), uuid.New(), uuid.New()
	client := newRoomTransportClient(t, map[string]uuid.UUID{
		"host-device": host, "guest-device": guest, "newcomer-device": newcomer,
	})

	createRequest := connect.NewRequest(&roomv1.CreateRoomRequest{
		Visibility: roomv1.RoomVisibility_ROOM_VISIBILITY_PRIVATE, ParticipantCapacity: 3,
		ParticipantAdmission: roomv1.AdmissionMode_ADMISSION_MODE_OPEN,
		SpectatorAdmission:   roomv1.AdmissionMode_ADMISSION_MODE_OPEN,
	})
	authorizeRoomWrite(createRequest, "host-device")
	created, err := client.CreateRoom(t.Context(), createRequest)
	if err != nil || created.Msg.GetRoom().GetRoomCode() != "TEST01" {
		t.Fatalf("create room: room=%+v err=%v", created, err)
	}

	joinRequest := connect.NewRequest(&roomv1.JoinRoomRequest{
		RoomCode: created.Msg.GetRoom().GetRoomCode(), Intent: roomv1.JoinIntent_JOIN_INTENT_PARTICIPANT,
	})
	authorizeRoomWrite(joinRequest, "guest-device")
	joined, err := client.JoinRoom(t.Context(), joinRequest)
	if err != nil || !joined.Msg.GetCreated() || len(joined.Msg.GetRoom().GetMembers()) != 2 {
		t.Fatalf("join room: response=%+v err=%v", joined, err)
	}
	hostReadRequest := connect.NewRequest(&roomv1.GetRoomRequest{RoomId: joined.Msg.GetRoom().GetRoomId()})
	authorizeRoomRead(hostReadRequest, "host-device")
	hostView, err := client.GetRoom(t.Context(), hostReadRequest)
	if err != nil {
		t.Fatal(err)
	}
	guestReadRequest := connect.NewRequest(&roomv1.GetRoomRequest{RoomId: joined.Msg.GetRoom().GetRoomId()})
	authorizeRoomRead(guestReadRequest, "guest-device")
	guestView, err := client.GetRoom(t.Context(), guestReadRequest)
	if err != nil {
		t.Fatal(err)
	}
	if hostNames, guestNames := roomMemberUsernames(hostView.Msg.GetRoom()), roomMemberUsernames(guestView.Msg.GetRoom()); len(hostNames) != 2 || !maps.Equal(hostNames, guestNames) || hostNames[host.String()] != "测试房主" || hostNames[guest.String()] != "测试玩家" {
		t.Fatalf("member usernames diverged: host=%v guest=%v", hostNames, guestNames)
	}
	heartbeatRequest := connect.NewRequest(&roomv1.HeartbeatRoomRequest{RoomId: joined.Msg.GetRoom().GetRoomId()})
	authorizeRoomWrite(heartbeatRequest, "guest-device")
	heartbeat, err := client.HeartbeatRoom(t.Context(), heartbeatRequest)
	if err != nil || heartbeat.Msg.GetObservedAt() == nil {
		t.Fatalf("heartbeat room: response=%+v err=%v", heartbeat, err)
	}

	startRequest := connect.NewRequest(&roomv1.StartGameRequest{
		RoomId: joined.Msg.GetRoom().GetRoomId(), GameId: "liars-dice",
		ExpectedVersion: joined.Msg.GetRoom().GetVersion(),
		Config: &gamev1.GameConfig{
			GameId: "liars-dice", SchemaVersion: 1, MessageType: "session.config", Payload: []byte("configured"),
		},
		OperationId: roomTransportOperationID(t, 1).Value(), RequestDigest: roomTransportDigest("start")[:],
	})
	authorizeRoomWrite(startRequest, "host-device")
	started, err := client.StartGame(t.Context(), startRequest)
	if err != nil || started.Msg.GetSessionId() == "" || len(started.Msg.GetParticipants()) != 2 ||
		started.Msg.GetRoom().GetParticipantAdmission() != roomv1.AdmissionMode_ADMISSION_MODE_CLOSED {
		t.Fatalf("start game: response=%+v err=%v", started, err)
	}
	waitingJoin := connect.NewRequest(&roomv1.JoinRoomRequest{
		RoomCode: started.Msg.GetRoom().GetRoomCode(), Intent: roomv1.JoinIntent_JOIN_INTENT_PARTICIPANT,
		ExpectedVersion: started.Msg.GetRoom().GetVersion(),
	})
	authorizeRoomWrite(waitingJoin, "newcomer-device")
	waiting, err := client.JoinRoom(t.Context(), waitingJoin)
	if err != nil || !waiting.Msg.GetCreated() || waiting.Msg.GetMember().GetRole() != roomv1.MemberRole_MEMBER_ROLE_WAITING ||
		waiting.Msg.GetMember().GetRequestedRole() != roomv1.MemberRole_MEMBER_ROLE_PARTICIPANT {
		t.Fatalf("join while playing: response=%+v err=%v", waiting, err)
	}

	finishRequest := connect.NewRequest(&roomv1.FinishGameRequest{
		RoomId: started.Msg.GetRoom().GetRoomId(), SessionId: started.Msg.GetSessionId(),
		ExpectedVersion: waiting.Msg.GetRoom().GetVersion(),
		OperationId:     roomTransportOperationID(t, 2).Value(), SourceEventId: uuid.NewString(), ExpectedStateVersion: 1,
		Command: &gamev1.GameEnvelope{
			GameId: "liars-dice", Version: &gamev1.VersionTuple{Engine: "1.0.0", Protocol: "1.0.0", Client: "1.0.0"},
			SchemaVersion: 1, MessageType: string(finishAction), Payload: []byte("finished"),
		},
		RequestDigest: roomTransportDigest("finish")[:],
	})
	authorizeRoomWrite(finishRequest, "host-device")
	finished, err := client.FinishGame(t.Context(), finishRequest)
	if err != nil || finished.Msg.GetRoom().GetStatus() != roomv1.RoomStatus_ROOM_STATUS_POST_GAME ||
		finished.Msg.GetRoom().GetParticipantAdmission() != roomv1.AdmissionMode_ADMISSION_MODE_CLOSED ||
		finished.Msg.GetRoom().GetLastFinishedSessionId() != started.Msg.GetSessionId() ||
		finished.Msg.GetRoom().GetLastFinishedGameId() != "liars-dice" {
		t.Fatalf("finish game: response=%+v err=%v", finished, err)
	}
	reopenRequest := connect.NewRequest(&roomv1.SetAdmissionRequest{
		RoomId: finished.Msg.GetRoom().GetRoomId(), ParticipantAdmission: roomv1.AdmissionMode_ADMISSION_MODE_OPEN,
		SpectatorAdmission: roomv1.AdmissionMode_ADMISSION_MODE_OPEN, ExpectedVersion: finished.Msg.GetRoom().GetVersion(),
	})
	authorizeRoomWrite(reopenRequest, "host-device")
	reopened, err := client.SetAdmission(t.Context(), reopenRequest)
	if err != nil || reopened.Msg.GetRoom().GetParticipantAdmission() != roomv1.AdmissionMode_ADMISSION_MODE_OPEN {
		t.Fatalf("reopen room: response=%+v err=%v", reopened, err)
	}
	approveRequest := connect.NewRequest(&roomv1.ApproveMemberRequest{
		RoomId: reopened.Msg.GetRoom().GetRoomId(), UserId: newcomer.String(), ExpectedVersion: reopened.Msg.GetRoom().GetVersion(),
	})
	authorizeRoomWrite(approveRequest, "host-device")
	approved, err := client.ApproveMember(t.Context(), approveRequest)
	if err != nil || approved.Msg.GetMember().GetRole() != roomv1.MemberRole_MEMBER_ROLE_PARTICIPANT ||
		len(approved.Msg.GetRoom().GetMembers()) != 3 {
		t.Fatalf("approve after reopen: response=%+v err=%v", approved, err)
	}
	restartRequest := connect.NewRequest(&roomv1.StartGameRequest{
		RoomId: approved.Msg.GetRoom().GetRoomId(), GameId: "dice-789", ExpectedVersion: approved.Msg.GetRoom().GetVersion(),
		Config: &gamev1.GameConfig{
			GameId: "dice-789", SchemaVersion: 1, MessageType: "session.config", Payload: []byte("configured"),
		},
		OperationId: roomTransportOperationID(t, 3).Value(), RequestDigest: roomTransportDigest("restart")[:],
	})
	authorizeRoomWrite(restartRequest, "host-device")
	restarted, err := client.StartGame(t.Context(), restartRequest)
	if err != nil || restarted.Msg.GetRoom().GetLastFinishedSessionId() != "" ||
		restarted.Msg.GetRoom().GetLastFinishedGameId() != "" {
		t.Fatalf("restart game: response=%+v err=%v", restarted, err)
	}
}

func TestRoomConnectListsPublicCardsWithoutWriteHeaders(t *testing.T) {
	host, guest := uuid.New(), uuid.New()
	client := newRoomTransportClient(t, map[string]uuid.UUID{"host-device": host, "guest-device": guest})
	createRequest := connect.NewRequest(&roomv1.CreateRoomRequest{
		Visibility: roomv1.RoomVisibility_ROOM_VISIBILITY_PUBLIC, ParticipantCapacity: 4,
		ParticipantAdmission: roomv1.AdmissionMode_ADMISSION_MODE_OPEN,
		SpectatorAdmission:   roomv1.AdmissionMode_ADMISSION_MODE_CLOSED,
	})
	authorizeRoomWrite(createRequest, "host-device")
	created, err := client.CreateRoom(t.Context(), createRequest)
	if err != nil {
		t.Fatal(err)
	}

	listRequest := connect.NewRequest(&roomv1.ListPublicRoomsRequest{})
	authorizeRoomRead(listRequest, "guest-device")
	listed, err := client.ListPublicRooms(t.Context(), listRequest)
	if err != nil || len(listed.Msg.GetRooms()) != 1 {
		t.Fatalf("list public rooms: response=%+v err=%v", listed, err)
	}
	card := listed.Msg.GetRooms()[0]
	if card.GetRoomId() != created.Msg.GetRoom().GetRoomId() || card.GetHostUsername() != "TestHost" ||
		card.GetParticipantCount() != 1 || card.GetPrimaryAction() != roomv1.PublicRoomPrimaryAction_PUBLIC_ROOM_PRIMARY_ACTION_JOIN ||
		listed.Msg.GetPage().GetNextPageToken() != "" {
		t.Fatalf("public card = %+v", card)
	}
}

func TestRoomConnectListsPrivateMemberRoomsWithoutWriteHeaders(t *testing.T) {
	host, guest := uuid.New(), uuid.New()
	client := newRoomTransportClient(t, map[string]uuid.UUID{"host-device": host, "guest-device": guest})
	createRequest := connect.NewRequest(&roomv1.CreateRoomRequest{
		Visibility: roomv1.RoomVisibility_ROOM_VISIBILITY_PRIVATE, ParticipantCapacity: 4,
		ParticipantAdmission: roomv1.AdmissionMode_ADMISSION_MODE_OPEN,
		SpectatorAdmission:   roomv1.AdmissionMode_ADMISSION_MODE_CLOSED,
	})
	authorizeRoomWrite(createRequest, "host-device")
	created, err := client.CreateRoom(t.Context(), createRequest)
	if err != nil {
		t.Fatal(err)
	}
	joinRequest := connect.NewRequest(&roomv1.JoinRoomRequest{
		RoomCode: created.Msg.GetRoom().GetRoomCode(), Intent: roomv1.JoinIntent_JOIN_INTENT_PARTICIPANT,
	})
	authorizeRoomWrite(joinRequest, "guest-device")
	if _, err := client.JoinRoom(t.Context(), joinRequest); err != nil {
		t.Fatal(err)
	}

	hostRequest := connect.NewRequest(&roomv1.ListMyRoomsRequest{})
	authorizeRoomRead(hostRequest, "host-device")
	hostRooms, err := client.ListMyRooms(t.Context(), hostRequest)
	if err != nil || len(hostRooms.Msg.GetRooms()) != 1 {
		t.Fatalf("host rooms: response=%+v err=%v", hostRooms, err)
	}
	hostCard := hostRooms.Msg.GetRooms()[0]
	if hostCard.GetRoomCode() != created.Msg.GetRoom().GetRoomCode() || !hostCard.GetIsHost() ||
		hostCard.GetVisibility() != roomv1.RoomVisibility_ROOM_VISIBILITY_PRIVATE || hostCard.GetParticipantCount() != 2 {
		t.Fatalf("host card = %+v", hostCard)
	}

	guestRequest := connect.NewRequest(&roomv1.ListMyRoomsRequest{})
	authorizeRoomRead(guestRequest, "guest-device")
	guestRooms, err := client.ListMyRooms(t.Context(), guestRequest)
	if err != nil || len(guestRooms.Msg.GetRooms()) != 1 || guestRooms.Msg.GetRooms()[0].GetIsHost() ||
		guestRooms.Msg.GetRooms()[0].GetViewerRole() != roomv1.MemberRole_MEMBER_ROLE_PARTICIPANT {
		t.Fatalf("guest rooms: response=%+v err=%v", guestRooms, err)
	}
}

func TestRoomConnectRemovalReturnsDurableSourceForPlayingParticipant(t *testing.T) {
	host, guest := uuid.New(), uuid.New()
	client := newRoomTransportClient(t, map[string]uuid.UUID{"host-device": host, "guest-device": guest})
	createRequest := connect.NewRequest(&roomv1.CreateRoomRequest{
		Visibility: roomv1.RoomVisibility_ROOM_VISIBILITY_PRIVATE, ParticipantCapacity: 3,
		ParticipantAdmission: roomv1.AdmissionMode_ADMISSION_MODE_OPEN,
		SpectatorAdmission:   roomv1.AdmissionMode_ADMISSION_MODE_OPEN,
	})
	authorizeRoomWrite(createRequest, "host-device")
	created, err := client.CreateRoom(t.Context(), createRequest)
	if err != nil {
		t.Fatal(err)
	}
	joinRequest := connect.NewRequest(&roomv1.JoinRoomRequest{
		RoomCode: created.Msg.GetRoom().GetRoomCode(), Intent: roomv1.JoinIntent_JOIN_INTENT_PARTICIPANT,
	})
	authorizeRoomWrite(joinRequest, "guest-device")
	joined, err := client.JoinRoom(t.Context(), joinRequest)
	if err != nil {
		t.Fatal(err)
	}
	startRequest := connect.NewRequest(&roomv1.StartGameRequest{
		RoomId: joined.Msg.GetRoom().GetRoomId(), GameId: "liars-dice", ExpectedVersion: joined.Msg.GetRoom().GetVersion(),
		Config: &gamev1.GameConfig{
			GameId: "liars-dice", SchemaVersion: 1, MessageType: "session.config", Payload: []byte("configured"),
		},
		OperationId: roomTransportOperationID(t, 11).Value(), RequestDigest: roomTransportDigest("remove-start")[:],
	})
	authorizeRoomWrite(startRequest, "host-device")
	started, err := client.StartGame(t.Context(), startRequest)
	if err != nil {
		t.Fatal(err)
	}
	removeRequest := connect.NewRequest(&roomv1.RemoveMemberRequest{
		RoomId: started.Msg.GetRoom().GetRoomId(), UserId: guest.String(), ExpectedVersion: started.Msg.GetRoom().GetVersion(),
	})
	authorizeRoomWrite(removeRequest, "host-device")

	removed, err := client.RemoveMember(t.Context(), removeRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !removed.Msg.GetParticipantRevoked() || removed.Msg.GetActiveSessionId() != started.Msg.GetSessionId() ||
		len(removed.Msg.GetRoom().GetMembers()) != 1 {
		t.Fatalf("remove response=%+v", removed.Msg)
	}
	if sourceEventID, err := uuid.Parse(removed.Msg.GetSourceEventId()); err != nil || sourceEventID == uuid.Nil {
		t.Fatalf("source event id=%q error=%v", removed.Msg.GetSourceEventId(), err)
	}
}

func TestRoomConnectHostCancelsActiveGameAndClosesRoom(t *testing.T) {
	host, guest := uuid.New(), uuid.New()
	fixture := newRoomTransportFixture(t, map[string]uuid.UUID{"host-device": host, "guest-device": guest})
	createRequest := connect.NewRequest(&roomv1.CreateRoomRequest{
		Visibility: roomv1.RoomVisibility_ROOM_VISIBILITY_PRIVATE, ParticipantCapacity: 3,
		ParticipantAdmission: roomv1.AdmissionMode_ADMISSION_MODE_OPEN,
		SpectatorAdmission:   roomv1.AdmissionMode_ADMISSION_MODE_OPEN,
	})
	authorizeRoomWrite(createRequest, "host-device")
	created, err := fixture.client.CreateRoom(t.Context(), createRequest)
	if err != nil {
		t.Fatal(err)
	}
	joinRequest := connect.NewRequest(&roomv1.JoinRoomRequest{
		RoomCode: created.Msg.GetRoom().GetRoomCode(), Intent: roomv1.JoinIntent_JOIN_INTENT_PARTICIPANT,
	})
	authorizeRoomWrite(joinRequest, "guest-device")
	joined, err := fixture.client.JoinRoom(t.Context(), joinRequest)
	if err != nil {
		t.Fatal(err)
	}
	startRequest := connect.NewRequest(&roomv1.StartGameRequest{
		RoomId: joined.Msg.GetRoom().GetRoomId(), GameId: "liars-dice", ExpectedVersion: joined.Msg.GetRoom().GetVersion(),
		Config: &gamev1.GameConfig{
			GameId: "liars-dice", SchemaVersion: 1, MessageType: "session.config", Payload: []byte("configured"),
		},
		OperationId: roomTransportOperationID(t, 12).Value(), RequestDigest: roomTransportDigest("close-start")[:],
	})
	authorizeRoomWrite(startRequest, "host-device")
	started, err := fixture.client.StartGame(t.Context(), startRequest)
	if err != nil {
		t.Fatal(err)
	}
	closeRequest := connect.NewRequest(&roomv1.CloseRoomRequest{
		RoomId: started.Msg.GetRoom().GetRoomId(), ExpectedVersion: started.Msg.GetRoom().GetVersion(),
	})
	authorizeRoomWrite(closeRequest, "guest-device")
	if _, err := fixture.client.CloseRoom(t.Context(), closeRequest); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("non-host close error=%v code=%v", err, connect.CodeOf(err))
	}
	closeRequest = connect.NewRequest(&roomv1.CloseRoomRequest{
		RoomId: started.Msg.GetRoom().GetRoomId(), ExpectedVersion: started.Msg.GetRoom().GetVersion(),
	})
	authorizeRoomWrite(closeRequest, "host-device")
	closed, err := fixture.client.CloseRoom(t.Context(), closeRequest)
	if err != nil {
		t.Fatal(err)
	}
	closedRoom := closed.Msg.GetRoom()
	if closedRoom.GetStatus() != roomv1.RoomStatus_ROOM_STATUS_CLOSED || closedRoom.GetActiveSessionId() != "" ||
		closedRoom.GetActiveGameId() != "" || closedRoom.GetParticipantAdmission() != roomv1.AdmissionMode_ADMISSION_MODE_CLOSED ||
		closedRoom.GetSpectatorAdmission() != roomv1.AdmissionMode_ADMISSION_MODE_CLOSED {
		t.Fatalf("closed room=%+v", closedRoom)
	}
	sessionID, err := uuid.Parse(started.Msg.GetSessionId())
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := fixture.runtime.Get(t.Context(), sessionID)
	if err != nil || cancelled.Snapshot().Status != gameruntime.StatusCancelled {
		t.Fatalf("cancelled session=%+v err=%v", cancelled.Snapshot(), err)
	}
	fixture.fanout.mu.Lock()
	events := append([]redisstore.SessionFanoutEvent(nil), fixture.fanout.events...)
	fixture.fanout.mu.Unlock()
	if len(events) != 2 || events[1].SessionID != sessionID || events[1].StateVersion != cancelled.Snapshot().State.StateVersion {
		t.Fatalf("fanout events=%+v", events)
	}
}

func TestPublicRoomCursorRoundTripsAndRejectsNonCanonicalInput(t *testing.T) {
	want := roomDomain.PublicRoomPageCursor{
		UpdatedAt: time.Date(2026, time.July, 19, 22, 0, 0, 123456000, time.UTC), RoomID: uuid.New(),
	}
	token, err := encodePublicRoomCursor(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodePublicRoomCursor(token)
	if err != nil || got.RoomID != want.RoomID || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("cursor = %+v, want %+v, err = %v", got, want, err)
	}
	if _, err := decodePublicRoomCursor(token + "A"); !errors.Is(err, roomDomain.ErrInvalidRoomInput) {
		t.Fatalf("non-canonical cursor error = %v", err)
	}
}

func TestMyRoomCursorRoundTripsHostPriorityAndRejectsNonCanonicalInput(t *testing.T) {
	want := roomDomain.MyRoomPageCursor{
		IsHost: true, UpdatedAt: time.Date(2026, time.July, 22, 22, 0, 0, 654321000, time.UTC), RoomID: uuid.New(),
	}
	token, err := encodeMyRoomCursor(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeMyRoomCursor(token)
	if err != nil || got != want {
		t.Fatalf("cursor = %+v, want %+v, err = %v", got, want, err)
	}
	if _, err := decodeMyRoomCursor(token + "A"); !errors.Is(err, roomDomain.ErrInvalidRoomInput) {
		t.Fatalf("non-canonical cursor error = %v", err)
	}
}

func TestEveryRoomRPCIsImplemented(t *testing.T) {
	client := newRoomTransportClient(t, map[string]uuid.UUID{"host-device": uuid.New()})
	calls := []func() error{
		func() error {
			_, err := client.CreateRoom(t.Context(), connect.NewRequest(&roomv1.CreateRoomRequest{}))
			return err
		},
		func() error {
			_, err := client.GetRoom(t.Context(), connect.NewRequest(&roomv1.GetRoomRequest{}))
			return err
		},
		func() error {
			_, err := client.ListMyRooms(t.Context(), connect.NewRequest(&roomv1.ListMyRoomsRequest{}))
			return err
		},
		func() error {
			_, err := client.ListPublicRooms(t.Context(), connect.NewRequest(&roomv1.ListPublicRoomsRequest{}))
			return err
		},
		func() error {
			_, err := client.JoinRoom(t.Context(), connect.NewRequest(&roomv1.JoinRoomRequest{}))
			return err
		},
		func() error {
			_, err := client.ApproveMember(t.Context(), connect.NewRequest(&roomv1.ApproveMemberRequest{}))
			return err
		},
		func() error {
			_, err := client.SetAdmission(t.Context(), connect.NewRequest(&roomv1.SetAdmissionRequest{}))
			return err
		},
		func() error {
			_, err := client.StartGame(t.Context(), connect.NewRequest(&roomv1.StartGameRequest{}))
			return err
		},
		func() error {
			_, err := client.FinishGame(t.Context(), connect.NewRequest(&roomv1.FinishGameRequest{}))
			return err
		},
		func() error {
			_, err := client.RemoveMember(t.Context(), connect.NewRequest(&roomv1.RemoveMemberRequest{}))
			return err
		},
		func() error {
			_, err := client.CloseRoom(t.Context(), connect.NewRequest(&roomv1.CloseRoomRequest{}))
			return err
		},
	}
	for index, call := range calls {
		if err := call(); err == nil || connect.CodeOf(err) == connect.CodeUnimplemented {
			t.Fatalf("room RPC %d error=%v", index, err)
		}
	}
}

type roomTransportFixture struct {
	client  roomv1connect.RoomServiceClient
	runtime *transportGameRuntime
	fanout  *transportFanout
}

func newRoomTransportClient(t testing.TB, actors map[string]uuid.UUID) roomv1connect.RoomServiceClient {
	return newRoomTransportFixture(t, actors).client
}

func newRoomTransportFixture(t testing.TB, actors map[string]uuid.UUID) roomTransportFixture {
	t.Helper()
	repository := newTransportRoomRepository(actors)
	source := clock.NewFake(time.Date(2026, time.July, 19, 18, 0, 0, 0, time.UTC))
	domainService, err := roomDomain.NewService(repository, &transportCodeGenerator{}, source)
	if err != nil {
		t.Fatal(err)
	}
	origins, err := origin.NewUserValidator(sharedconfig.OriginAllowlist{roomTransportOrigin})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newTransportGameRuntime(repository)
	fanout := &transportFanout{}
	service, err := NewService(
		domainService, transportGameCatalog{}, runtime, runtime, repository, fanout,
		&transportAuthenticator{actors: actors}, origins, csrf.NewUserValidator(),
	)
	if err != nil {
		t.Fatal(err)
	}
	path, handler := roomv1connect.NewRoomServiceHandler(service, connect.WithInterceptors(transporterrors.Interceptor()))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return roomTransportFixture{
		client: roomv1connect.NewRoomServiceClient(server.Client(), server.URL), runtime: runtime, fanout: fanout,
	}
}

func authorizeRoomWrite[T any](request *connect.Request[T], deviceToken string) {
	csrfToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, csrf.TokenBytes))
	request.Header().Set("Origin", roomTransportOrigin)
	request.Header().Set(csrf.HeaderName, csrfToken)
	request.Header().Set("Cookie", cookies.UserDeviceCookieName+"="+deviceToken+"; "+cookies.UserCSRFCookieName+"="+csrfToken)
}

func authorizeRoomRead[T any](request *connect.Request[T], deviceToken string) {
	csrfToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, csrf.TokenBytes))
	request.Header().Set("Cookie", cookies.UserDeviceCookieName+"="+deviceToken+"; "+cookies.UserCSRFCookieName+"="+csrfToken)
}

func roomMemberUsernames(room *roomv1.Room) map[string]string {
	usernames := make(map[string]string, len(room.GetMembers()))
	for _, member := range room.GetMembers() {
		usernames[member.GetUserId()] = member.GetUsername()
	}
	return usernames
}

type transportAuthenticator struct{ actors map[string]uuid.UUID }

func (authenticator *transportAuthenticator) Authenticate(_ context.Context, deviceToken, csrfToken string) (uuid.UUID, error) {
	if csrfToken == "" {
		return uuid.Nil, errors.New("missing test csrf")
	}
	actor, ok := authenticator.actors[deviceToken]
	if !ok {
		return uuid.Nil, errors.New("unknown test device")
	}
	return actor, nil
}

type transportCodeGenerator struct{}

func (*transportCodeGenerator) Generate() (string, error) { return "TEST01", nil }

type transportGameCatalog struct{}

func (transportGameCatalog) ParticipantLimits(context.Context, string) (gameSDK.ParticipantLimits, error) {
	return gameSDK.ParticipantLimits{Minimum: 2, Maximum: 9}, nil
}

// transportGameRuntime models the room/session atomic boundary without replacing production module tests.
type transportGameRuntime struct {
	mu       sync.Mutex
	rooms    *transportRoomRepository
	sessions map[uuid.UUID]gameruntime.Session
}

func newTransportGameRuntime(rooms *transportRoomRepository) *transportGameRuntime {
	return &transportGameRuntime{rooms: rooms, sessions: make(map[uuid.UUID]gameruntime.Session)}
}

func (runtime *transportGameRuntime) Start(
	ctx context.Context,
	command gameruntime.StartCommand,
) (roomDomain.Room, gameruntime.Session, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	before, err := runtime.rooms.GetByID(ctx, command.RoomID)
	if err != nil {
		return roomDomain.Room{}, gameruntime.Session{}, err
	}
	sessionID := uuid.New()
	startedAt := before.Snapshot().UpdatedAt.Add(time.Microsecond)
	after, start, err := before.StartSession(
		command.ActorUserID, sessionID, string(command.GameID), 2, 8, command.Expected, startedAt,
	)
	if err != nil {
		return roomDomain.Room{}, gameruntime.Session{}, err
	}
	storedRoom, err := runtime.rooms.UpdateCAS(ctx, before, after)
	if err != nil {
		return roomDomain.Room{}, gameruntime.Session{}, err
	}
	participants := make([]gameruntime.Participant, len(start.Participants))
	for index, participant := range start.Participants {
		participants[index] = gameruntime.Participant{UserID: participant.UserID, SeatIndex: participant.SeatIndex}
	}
	key := gameSDK.VersionKey{GameID: command.GameID, Engine: "1.0.0", Protocol: "1.0.0", Client: "1.0.0"}
	session, err := gameruntime.RestoreSession(gameruntime.SessionSnapshot{
		ID: sessionID, RoomID: command.RoomID, VersionKey: key, OwnershipEpoch: 1, Participants: participants,
		State: gameSDK.Snapshot{
			SnapshotVersion: 1, StateVersion: 1,
			State: gameSDK.Message{MessageType: "test.state", SchemaVersion: 1, Payload: []byte("active")},
		},
		Status: gameruntime.StatusActive, StartedAt: startedAt, UpdatedAt: startedAt,
	})
	if err != nil {
		return roomDomain.Room{}, gameruntime.Session{}, err
	}
	runtime.sessions[sessionID] = session
	return storedRoom, session, nil
}

func (runtime *transportGameRuntime) Cancel(
	ctx context.Context,
	command gameruntime.CancelCommand,
) (roomDomain.Room, gameruntime.Session, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	beforeSession, exists := runtime.sessions[command.SessionID]
	if !exists {
		return roomDomain.Room{}, gameruntime.Session{}, gameruntime.ErrSessionNotFound
	}
	beforeRoom, err := runtime.rooms.GetByID(ctx, command.RoomID)
	if err != nil {
		return roomDomain.Room{}, gameruntime.Session{}, err
	}
	if beforeRoom.Version() != command.ExpectedRoom || beforeRoom.Snapshot().ActiveSessionID != command.SessionID {
		return roomDomain.Room{}, gameruntime.Session{}, roomDomain.ErrRoomVersionConflict
	}
	cancelledAt := beforeSession.Snapshot().UpdatedAt.Add(time.Microsecond)
	afterSession, err := beforeSession.Cancel(command.OwnershipEpoch, cancelledAt)
	if err != nil {
		return roomDomain.Room{}, gameruntime.Session{}, err
	}
	var afterRoom roomDomain.Room
	if command.CloseRoom {
		afterRoom, err = beforeRoom.CancelSessionAndClose(beforeRoom.Snapshot().HostUserID, command.SessionID, beforeRoom.Version(), cancelledAt)
	} else {
		afterRoom, err = beforeRoom.CancelSession(command.SessionID, beforeRoom.Version(), cancelledAt)
	}
	if err != nil {
		return roomDomain.Room{}, gameruntime.Session{}, err
	}
	storedRoom, err := runtime.rooms.UpdateCAS(ctx, beforeRoom, afterRoom)
	if err != nil {
		return roomDomain.Room{}, gameruntime.Session{}, err
	}
	runtime.sessions[command.SessionID] = afterSession
	return storedRoom, afterSession, nil
}

func (runtime *transportGameRuntime) HandleSystem(
	ctx context.Context,
	command gameruntime.SystemCommand,
) (gameruntime.SystemCommitResult, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	before, exists := runtime.sessions[command.SessionID]
	if !exists {
		return gameruntime.SystemCommitResult{}, gameruntime.ErrSessionNotFound
	}
	beforeSnapshot := before.Snapshot()
	if beforeSnapshot.Status.Terminal() {
		return gameruntime.SystemCommitResult{Session: before, Replayed: true}, nil
	}
	if command.Source.Kind != gameruntime.SystemSourceHostAPI || command.Message.MessageType != finishAction ||
		command.VersionKey != beforeSnapshot.VersionKey || command.ExpectedStateVersion != beforeSnapshot.State.StateVersion {
		return gameruntime.SystemCommitResult{}, gameruntime.ErrInvalidSystemCommit
	}
	roomBefore, err := runtime.rooms.GetByID(ctx, beforeSnapshot.RoomID)
	if err != nil {
		return gameruntime.SystemCommitResult{}, err
	}
	finishedAt := beforeSnapshot.UpdatedAt.Add(time.Microsecond)
	roomAfter, err := roomBefore.FinishSession(command.SessionID, roomBefore.Version(), finishedAt)
	if err != nil {
		return gameruntime.SystemCommitResult{}, err
	}
	if _, err := runtime.rooms.UpdateCAS(ctx, roomBefore, roomAfter); err != nil {
		return gameruntime.SystemCommitResult{}, err
	}
	finished, err := gameruntime.RestoreSession(gameruntime.SessionSnapshot{
		ID: beforeSnapshot.ID, RoomID: beforeSnapshot.RoomID, VersionKey: beforeSnapshot.VersionKey,
		OwnershipEpoch: beforeSnapshot.OwnershipEpoch, Participants: beforeSnapshot.Participants,
		State: gameSDK.Snapshot{
			SnapshotVersion: 1, StateVersion: beforeSnapshot.State.StateVersion + 1,
			State: gameSDK.Message{MessageType: "test.state", SchemaVersion: 1, Payload: []byte("finished")},
		},
		Status: gameruntime.StatusFinished, StartedAt: beforeSnapshot.StartedAt,
		UpdatedAt: finishedAt, EndedAt: finishedAt,
	})
	if err != nil {
		return gameruntime.SystemCommitResult{}, err
	}
	runtime.sessions[command.SessionID] = finished
	return gameruntime.SystemCommitResult{Session: finished}, nil
}

func (runtime *transportGameRuntime) Get(_ context.Context, sessionID uuid.UUID) (gameruntime.Session, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	session, exists := runtime.sessions[sessionID]
	if !exists {
		return gameruntime.Session{}, gameruntime.ErrSessionNotFound
	}
	return session, nil
}

type transportFanout struct {
	mu     sync.Mutex
	events []redisstore.SessionFanoutEvent
}

func (fanout *transportFanout) PublishSessionFanout(_ context.Context, event redisstore.SessionFanoutEvent) error {
	fanout.mu.Lock()
	defer fanout.mu.Unlock()
	fanout.events = append(fanout.events, event)
	return nil
}

func roomTransportOperationID(t testing.TB, marker byte) idempotency.OperationID {
	t.Helper()
	operationID, err := idempotency.NewOperationID(bytes.Repeat([]byte{marker}, 16))
	if err != nil {
		t.Fatal(err)
	}
	return operationID
}

func roomTransportDigest(value string) []byte {
	digest := sha256.Sum256([]byte(value))
	return digest[:]
}

type transportRoomRepository struct {
	mu        sync.Mutex
	byID      map[uuid.UUID]roomDomain.Room
	byCode    map[string]uuid.UUID
	usernames map[uuid.UUID]string
}

func newTransportRoomRepository(actors map[string]uuid.UUID) *transportRoomRepository {
	usernames := make(map[uuid.UUID]string, len(actors))
	for deviceToken, userID := range actors {
		switch deviceToken {
		case "host-device":
			usernames[userID] = "测试房主"
		case "guest-device":
			usernames[userID] = "测试玩家"
		case "newcomer-device":
			usernames[userID] = "测试候场"
		default:
			usernames[userID] = "测试成员"
		}
	}
	return &transportRoomRepository{
		byID: make(map[uuid.UUID]roomDomain.Room), byCode: make(map[string]uuid.UUID), usernames: usernames,
	}
}

func (repository *transportRoomRepository) Create(_ context.Context, room roomDomain.Room) (roomDomain.Room, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	snapshot := room.Snapshot()
	if _, exists := repository.byCode[snapshot.RoomCode]; exists {
		return roomDomain.Room{}, roomDomain.ErrRoomCodeUnavailable
	}
	repository.byID[snapshot.ID], repository.byCode[snapshot.RoomCode] = room, snapshot.ID
	return room, nil
}

func (repository *transportRoomRepository) GetByID(_ context.Context, id uuid.UUID) (roomDomain.Room, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	room, exists := repository.byID[id]
	if !exists {
		return roomDomain.Room{}, roomDomain.ErrRoomNotFound
	}
	return room, nil
}

func (repository *transportRoomRepository) GetByCode(ctx context.Context, code string) (roomDomain.Room, error) {
	repository.mu.Lock()
	id, exists := repository.byCode[code]
	repository.mu.Unlock()
	if !exists {
		return roomDomain.Room{}, roomDomain.ErrRoomNotFound
	}
	return repository.GetByID(ctx, id)
}

// RecordRoomPresence mirrors the persistence authorization boundary without mutating the aggregate version.
func (repository *transportRoomRepository) RecordRoomPresence(
	_ context.Context,
	roomID uuid.UUID,
	userID uuid.UUID,
) (time.Time, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	room, exists := repository.byID[roomID]
	if !exists {
		return time.Time{}, roomDomain.ErrRoomNotFound
	}
	snapshot := room.Snapshot()
	if snapshot.Status == roomDomain.RoomStatusClosed {
		return time.Time{}, roomDomain.ErrRoomClosed
	}
	if _, member := room.Member(userID); !member {
		return time.Time{}, roomDomain.ErrMemberNotFound
	}
	return snapshot.UpdatedAt.Add(time.Millisecond), nil
}

func (repository *transportRoomRepository) ListRoomMemberUsernames(_ context.Context, roomID uuid.UUID) (map[uuid.UUID]string, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	room, exists := repository.byID[roomID]
	if !exists {
		return nil, roomDomain.ErrRoomNotFound
	}
	usernames := make(map[uuid.UUID]string, len(room.Snapshot().Members))
	for _, member := range room.Snapshot().Members {
		usernames[member.UserID] = repository.usernames[member.UserID]
	}
	return usernames, nil
}

func (repository *transportRoomRepository) UpdateCAS(_ context.Context, current, next roomDomain.Room) (roomDomain.Room, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	stored, exists := repository.byID[current.Snapshot().ID]
	if !exists || stored.Version() != current.Version() {
		return roomDomain.Room{}, roomDomain.ErrRoomVersionConflict
	}
	repository.byID[current.Snapshot().ID] = next
	return next, nil
}

func (repository *transportRoomRepository) CommitRemoval(
	ctx context.Context, current, next roomDomain.Room, _ outbox.Event,
) (roomDomain.Room, error) {
	return repository.UpdateCAS(ctx, current, next)
}

func (repository *transportRoomRepository) ListPublicRooms(_ context.Context, request roomDomain.PublicRoomListRequest) ([]roomDomain.PublicRoomCard, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if !request.Valid() {
		return nil, roomDomain.ErrInvalidRoomInput
	}
	cards := make([]roomDomain.PublicRoomCard, 0, len(repository.byID))
	for _, candidate := range repository.byID {
		snapshot := candidate.Snapshot()
		if snapshot.Visibility != roomDomain.VisibilityPublic || snapshot.Status == roomDomain.RoomStatusClosed ||
			!transportRoomMatchesFilter(snapshot, request.Filter) || !transportRoomAfterCursor(snapshot, request.After) {
			continue
		}
		participantCount, spectatorCount, waitingCount := transportRoomMemberCounts(snapshot)
		viewerRole, viewerRequestedRole := roomDomain.MemberRole(""), roomDomain.MemberRole("")
		for _, member := range snapshot.Members {
			if member.UserID == request.ActorUserID {
				viewerRole, viewerRequestedRole = member.Role, member.RequestedRole
			}
		}
		card, err := roomDomain.RestorePublicRoomCard(roomDomain.PublicRoomCardSnapshot{
			RoomID: snapshot.ID, HostUsername: "TestHost", Status: snapshot.Status,
			ParticipantCapacity: snapshot.ParticipantCapacity, ParticipantCount: participantCount,
			SpectatorCount: spectatorCount, WaitingCount: waitingCount,
			ParticipantAdmission: snapshot.ParticipantAdmission, SpectatorAdmission: snapshot.SpectatorAdmission,
			ActiveGameID: snapshot.ActiveGameID, ViewerRole: viewerRole, ViewerRequestedRole: viewerRequestedRole,
			UpdatedAt: snapshot.UpdatedAt,
		})
		if err != nil {
			return nil, err
		}
		cards = append(cards, card)
	}
	sort.Slice(cards, func(left, right int) bool {
		leftSnapshot, rightSnapshot := cards[left].Snapshot(), cards[right].Snapshot()
		if !leftSnapshot.UpdatedAt.Equal(rightSnapshot.UpdatedAt) {
			return leftSnapshot.UpdatedAt.After(rightSnapshot.UpdatedAt)
		}
		return leftSnapshot.RoomID.String() > rightSnapshot.RoomID.String()
	})
	return append([]roomDomain.PublicRoomCard(nil), cards[:min(len(cards), int(request.Limit))]...), nil
}

func (repository *transportRoomRepository) ListMyRooms(_ context.Context, request roomDomain.MyRoomListRequest) ([]roomDomain.MyRoomCard, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if !request.Valid() {
		return nil, roomDomain.ErrInvalidRoomInput
	}
	cards := make([]roomDomain.MyRoomCard, 0, len(repository.byID))
	for _, candidate := range repository.byID {
		snapshot := candidate.Snapshot()
		viewer, member := candidate.Member(request.ActorUserID)
		if !member || snapshot.Status == roomDomain.RoomStatusClosed {
			continue
		}
		isHost := snapshot.HostUserID == request.ActorUserID
		if !transportMyRoomAfterCursor(isHost, snapshot, request.After) {
			continue
		}
		participantCount, spectatorCount, waitingCount := transportRoomMemberCounts(snapshot)
		card, err := roomDomain.RestoreMyRoomCard(roomDomain.MyRoomCardSnapshot{
			RoomID: snapshot.ID, RoomCode: snapshot.RoomCode, Visibility: snapshot.Visibility, HostUsername: "TestHost",
			Status: snapshot.Status, IsHost: isHost, ParticipantCapacity: snapshot.ParticipantCapacity,
			ParticipantCount: participantCount, SpectatorCount: spectatorCount, WaitingCount: waitingCount,
			ParticipantAdmission: snapshot.ParticipantAdmission, SpectatorAdmission: snapshot.SpectatorAdmission,
			ActiveGameID: snapshot.ActiveGameID, LastFinishedGameID: snapshot.LastFinishedGameID,
			ViewerRole: viewer.Role, ViewerRequestedRole: viewer.RequestedRole, UpdatedAt: snapshot.UpdatedAt,
		})
		if err != nil {
			return nil, err
		}
		cards = append(cards, card)
	}
	sort.Slice(cards, func(left, right int) bool {
		leftSnapshot, rightSnapshot := cards[left].Snapshot(), cards[right].Snapshot()
		if leftSnapshot.IsHost != rightSnapshot.IsHost {
			return leftSnapshot.IsHost
		}
		if !leftSnapshot.UpdatedAt.Equal(rightSnapshot.UpdatedAt) {
			return leftSnapshot.UpdatedAt.After(rightSnapshot.UpdatedAt)
		}
		return leftSnapshot.RoomID.String() > rightSnapshot.RoomID.String()
	})
	return append([]roomDomain.MyRoomCard(nil), cards[:min(len(cards), int(request.Limit))]...), nil
}

func transportRoomMemberCounts(snapshot roomDomain.RoomSnapshot) (uint32, uint32, uint32) {
	participantCount, spectatorCount, waitingCount := uint32(0), uint32(0), uint32(0)
	for _, member := range snapshot.Members {
		switch member.Role {
		case roomDomain.MemberRoleParticipant:
			participantCount++
		case roomDomain.MemberRoleSpectator:
			spectatorCount++
		case roomDomain.MemberRoleWaiting:
			waitingCount++
		}
	}
	return participantCount, spectatorCount, waitingCount
}

func transportMyRoomAfterCursor(isHost bool, snapshot roomDomain.RoomSnapshot, after roomDomain.MyRoomPageCursor) bool {
	if after.UpdatedAt.IsZero() {
		return true
	}
	if isHost != after.IsHost {
		return !isHost && after.IsHost
	}
	return snapshot.UpdatedAt.Before(after.UpdatedAt) ||
		(snapshot.UpdatedAt.Equal(after.UpdatedAt) && snapshot.ID.String() < after.RoomID.String())
}

func transportRoomMatchesFilter(snapshot roomDomain.RoomSnapshot, filter roomDomain.PublicRoomFilter) bool {
	if len(filter.Statuses) > 0 {
		matched := false
		for _, status := range filter.Statuses {
			matched = matched || snapshot.Status == status
		}
		if !matched {
			return false
		}
	}
	if filter.GameID != "" && snapshot.ActiveGameID != filter.GameID {
		return false
	}
	if !filter.ParticipantJoinableOnly {
		return true
	}
	participants := uint32(0)
	for _, member := range snapshot.Members {
		if member.Role == roomDomain.MemberRoleParticipant {
			participants++
		}
	}
	return (snapshot.Status == roomDomain.RoomStatusLobby || snapshot.Status == roomDomain.RoomStatusPostGame) &&
		snapshot.ParticipantAdmission != roomDomain.AdmissionClosed &&
		participants < snapshot.ParticipantCapacity
}

func transportRoomAfterCursor(snapshot roomDomain.RoomSnapshot, after roomDomain.PublicRoomPageCursor) bool {
	if after.UpdatedAt.IsZero() {
		return true
	}
	return snapshot.UpdatedAt.Before(after.UpdatedAt) ||
		(snapshot.UpdatedAt.Equal(after.UpdatedAt) && snapshot.ID.String() < after.RoomID.String())
}

var _ roomDomain.Repository = (*transportRoomRepository)(nil)
var _ roomDomain.Store = (*transportRoomRepository)(nil)
