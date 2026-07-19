package room

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/clock"
)

func TestServiceCreatesRoomWithExplicitAdmissionAndRetriesCodeConflict(t *testing.T) {
	now := time.Date(2026, time.July, 19, 15, 0, 0, 0, time.UTC)
	host := uuid.New()
	repository := newMemoryRoomRepository()
	existing, err := New(uuid.New(), host, "TAKEN1", VisibilityPrivate, 4, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Create(t.Context(), existing); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(repository, &sequenceRoomCodeGenerator{codes: []string{"TAKEN1", "FRESH2"}}, testGameCatalog{}, clock.NewFake(now))
	if err != nil {
		t.Fatal(err)
	}

	created, err := service.CreateRoom(t.Context(), CreateRoomCommand{
		ActorUserID: host, Visibility: VisibilityPublic, ParticipantCapacity: 6,
		ParticipantAdmission: AdmissionApproval, SpectatorAdmission: AdmissionClosed,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := created.Snapshot()
	if snapshot.RoomCode != "FRESH2" || snapshot.ParticipantAdmission != AdmissionApproval ||
		snapshot.SpectatorAdmission != AdmissionClosed || snapshot.RoomVersion != 1 || snapshot.MembershipVersion != 1 {
		t.Fatalf("created room = %+v", snapshot)
	}
}

func TestServiceJoinsPrivateRoomByInviteCodeAndProtectsVisibility(t *testing.T) {
	now := time.Date(2026, time.July, 19, 16, 0, 0, 0, time.UTC)
	host, guest, outsider := uuid.New(), uuid.New(), uuid.New()
	repository := newMemoryRoomRepository()
	room, err := New(uuid.New(), host, "INVITE", VisibilityPrivate, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Create(t.Context(), room); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(repository, &sequenceRoomCodeGenerator{codes: []string{"SPARE1"}}, testGameCatalog{}, clock.NewFake(now.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.GetRoom(t.Context(), GetRoomCommand{ActorUserID: outsider, Selector: RoomSelector{ID: room.Snapshot().ID}}); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("private room outsider read error=%v", err)
	}
	joined, result, err := service.JoinRoom(t.Context(), JoinRoomCommand{
		ActorUserID: guest, Selector: RoomSelector{Code: "INVITE"}, Intent: JoinIntentParticipant,
	})
	if err != nil || !result.Created || result.Member.Role != MemberRoleParticipant {
		t.Fatalf("join by code: result=%+v err=%v", result, err)
	}
	if _, err := service.GetRoom(t.Context(), GetRoomCommand{ActorUserID: guest, Selector: RoomSelector{ID: joined.Snapshot().ID}}); err != nil {
		t.Fatalf("joined member read: %v", err)
	}
}

func TestServiceRequiresExactVersionForHostCommands(t *testing.T) {
	now := time.Date(2026, time.July, 19, 17, 0, 0, 0, time.UTC)
	host := uuid.New()
	repository := newMemoryRoomRepository()
	room, err := New(uuid.New(), host, "CAS123", VisibilityPrivate, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Create(t.Context(), room); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(repository, &sequenceRoomCodeGenerator{codes: []string{"SPARE2"}}, testGameCatalog{}, clock.NewFake(now.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.SetAdmission(t.Context(), SetAdmissionCommand{
		ActorUserID: host, RoomID: room.Snapshot().ID, Participant: AdmissionClosed, Spectator: AdmissionClosed,
	}); !errors.Is(err, ErrRoomVersionConflict) {
		t.Fatalf("missing version error=%v", err)
	}
	updated, err := service.SetAdmission(t.Context(), SetAdmissionCommand{
		ActorUserID: host, RoomID: room.Snapshot().ID, Participant: AdmissionClosed, Spectator: AdmissionClosed,
		Expected: room.Version(),
	})
	if err != nil || updated.Snapshot().ParticipantAdmission != AdmissionClosed {
		t.Fatalf("set admission: room=%+v err=%v", updated.Snapshot(), err)
	}
	if _, err := service.CloseRoom(t.Context(), CloseRoomCommand{
		ActorUserID: host, RoomID: room.Snapshot().ID, Expected: room.Version(),
	}); !errors.Is(err, ErrRoomVersionConflict) {
		t.Fatalf("stale close error=%v", err)
	}
}

type sequenceRoomCodeGenerator struct {
	mu    sync.Mutex
	codes []string
}

type testGameCatalog struct{}

func (testGameCatalog) MinimumParticipants(context.Context, string) (uint32, error) { return 2, nil }

func (generator *sequenceRoomCodeGenerator) Generate() (string, error) {
	generator.mu.Lock()
	defer generator.mu.Unlock()
	if len(generator.codes) == 0 {
		return "", ErrInvalidRoomInput
	}
	value := generator.codes[0]
	generator.codes = generator.codes[1:]
	return value, nil
}

type memoryRoomRepository struct {
	mu     sync.Mutex
	byID   map[uuid.UUID]Room
	byCode map[string]uuid.UUID
}

func newMemoryRoomRepository() *memoryRoomRepository {
	return &memoryRoomRepository{byID: make(map[uuid.UUID]Room), byCode: make(map[string]uuid.UUID)}
}

func (repository *memoryRoomRepository) Create(_ context.Context, room Room) (Room, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	snapshot := room.Snapshot()
	if _, exists := repository.byCode[snapshot.RoomCode]; exists {
		return Room{}, ErrRoomCodeUnavailable
	}
	repository.byID[snapshot.ID] = room
	repository.byCode[snapshot.RoomCode] = snapshot.ID
	return room, nil
}

func (repository *memoryRoomRepository) GetByID(_ context.Context, id uuid.UUID) (Room, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	room, exists := repository.byID[id]
	if !exists {
		return Room{}, ErrRoomNotFound
	}
	return room, nil
}

func (repository *memoryRoomRepository) GetByCode(ctx context.Context, code string) (Room, error) {
	repository.mu.Lock()
	id, exists := repository.byCode[code]
	repository.mu.Unlock()
	if !exists {
		return Room{}, ErrRoomNotFound
	}
	return repository.GetByID(ctx, id)
}

func (repository *memoryRoomRepository) UpdateCAS(_ context.Context, current, next Room) (Room, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	stored, exists := repository.byID[current.Snapshot().ID]
	if !exists {
		return Room{}, ErrRoomNotFound
	}
	if stored.Version() != current.Version() {
		return Room{}, ErrRoomVersionConflict
	}
	repository.byID[current.Snapshot().ID] = next
	return next, nil
}

var _ Repository = (*memoryRoomRepository)(nil)
