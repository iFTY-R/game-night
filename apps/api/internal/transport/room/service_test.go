package room

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
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
	roomv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/room/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/room/v1/roomv1connect"
	"github.com/iFTY-R/game-night/platform/clock"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
	gameSDK "github.com/iFTY-R/game-night/sdk/go/game"
)

const roomTransportOrigin = "https://play.example.test"

func TestRoomConnectFlowCreatesJoinsStartsAndFinishes(t *testing.T) {
	host, guest := uuid.New(), uuid.New()
	client := newRoomTransportClient(t, map[string]uuid.UUID{"host-device": host, "guest-device": guest})

	createRequest := connect.NewRequest(&roomv1.CreateRoomRequest{
		Visibility: roomv1.RoomVisibility_ROOM_VISIBILITY_PRIVATE, ParticipantCapacity: 2,
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

	startRequest := connect.NewRequest(&roomv1.StartGameRequest{
		RoomId: joined.Msg.GetRoom().GetRoomId(), GameId: "dice",
		ExpectedVersion: joined.Msg.GetRoom().GetVersion(),
	})
	authorizeRoomWrite(startRequest, "host-device")
	started, err := client.StartGame(t.Context(), startRequest)
	if err != nil || started.Msg.GetSessionId() == "" || len(started.Msg.GetParticipants()) != 2 ||
		started.Msg.GetRoom().GetParticipantAdmission() != roomv1.AdmissionMode_ADMISSION_MODE_CLOSED {
		t.Fatalf("start game: response=%+v err=%v", started, err)
	}

	finishRequest := connect.NewRequest(&roomv1.FinishGameRequest{
		RoomId: started.Msg.GetRoom().GetRoomId(), SessionId: started.Msg.GetSessionId(),
		ExpectedVersion: started.Msg.GetRoom().GetVersion(),
	})
	authorizeRoomWrite(finishRequest, "host-device")
	finished, err := client.FinishGame(t.Context(), finishRequest)
	if err != nil || finished.Msg.GetRoom().GetStatus() != roomv1.RoomStatus_ROOM_STATUS_LOBBY ||
		finished.Msg.GetRoom().GetParticipantAdmission() != roomv1.AdmissionMode_ADMISSION_MODE_CLOSED {
		t.Fatalf("finish game: response=%+v err=%v", finished, err)
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

func newRoomTransportClient(t testing.TB, actors map[string]uuid.UUID) roomv1connect.RoomServiceClient {
	t.Helper()
	repository := newTransportRoomRepository()
	source := clock.NewFake(time.Date(2026, time.July, 19, 18, 0, 0, 0, time.UTC))
	domainService, err := roomDomain.NewService(repository, &transportCodeGenerator{}, transportGameCatalog{}, source)
	if err != nil {
		t.Fatal(err)
	}
	origins, err := origin.NewUserValidator(sharedconfig.OriginAllowlist{roomTransportOrigin})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(domainService, &transportAuthenticator{actors: actors}, origins, csrf.NewUserValidator())
	if err != nil {
		t.Fatal(err)
	}
	path, handler := roomv1connect.NewRoomServiceHandler(service, connect.WithInterceptors(transporterrors.Interceptor()))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return roomv1connect.NewRoomServiceClient(server.Client(), server.URL)
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

type transportRoomRepository struct {
	mu     sync.Mutex
	byID   map[uuid.UUID]roomDomain.Room
	byCode map[string]uuid.UUID
}

func newTransportRoomRepository() *transportRoomRepository {
	return &transportRoomRepository{byID: make(map[uuid.UUID]roomDomain.Room), byCode: make(map[string]uuid.UUID)}
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
		participantCount, spectatorCount, waitingCount := uint32(0), uint32(0), uint32(0)
		viewerRole, viewerRequestedRole := roomDomain.MemberRole(""), roomDomain.MemberRole("")
		for _, member := range snapshot.Members {
			switch member.Role {
			case roomDomain.MemberRoleParticipant:
				participantCount++
			case roomDomain.MemberRoleSpectator:
				spectatorCount++
			case roomDomain.MemberRoleWaiting:
				waitingCount++
			}
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
	return snapshot.Status == roomDomain.RoomStatusLobby && snapshot.ParticipantAdmission != roomDomain.AdmissionClosed &&
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
