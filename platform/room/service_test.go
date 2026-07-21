package room

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/outbox"
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
	service, err := NewService(repository, &sequenceRoomCodeGenerator{codes: []string{"TAKEN1", "FRESH2"}}, clock.NewFake(now))
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
	service, err := NewService(repository, &sequenceRoomCodeGenerator{codes: []string{"SPARE1"}}, clock.NewFake(now.Add(time.Second)))
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
	service, err := NewService(repository, &sequenceRoomCodeGenerator{codes: []string{"SPARE2"}}, clock.NewFake(now.Add(time.Second)))
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

func TestServicePersistsSpectatorRequestingParticipantWhilePlaying(t *testing.T) {
	now := time.Date(2026, time.July, 19, 17, 30, 0, 0, time.UTC)
	host, participant, spectator := uuid.New(), uuid.New(), uuid.New()
	repository := newMemoryRoomRepository()
	playing, err := New(uuid.New(), host, "QUEUE2", VisibilityPrivate, 4, now)
	if err != nil {
		t.Fatal(err)
	}
	playing, _, err = playing.Join(participant, JoinIntentParticipant, playing.Version(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	playing, _, err = playing.Join(spectator, JoinIntentSpectator, playing.Version(), now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	playing, _, err = playing.StartSession(host, uuid.New(), "dice", 2, 9, playing.Version(), now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Create(t.Context(), playing); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(repository, &sequenceRoomCodeGenerator{codes: []string{"SPARE3"}}, clock.NewFake(now.Add(4*time.Second)))
	if err != nil {
		t.Fatal(err)
	}

	updated, result, err := service.JoinRoom(t.Context(), JoinRoomCommand{
		ActorUserID: spectator, Selector: RoomSelector{ID: playing.Snapshot().ID}, Intent: JoinIntentParticipant,
		Expected: playing.Version(),
	})
	if err != nil || result.Created || result.Member.Role != MemberRoleWaiting ||
		updated.Version().Membership != playing.Version().Membership+1 {
		t.Fatalf("participant request: result=%+v room=%+v err=%v", result, updated.Snapshot(), err)
	}
	persisted, err := repository.GetByID(t.Context(), playing.Snapshot().ID)
	if err != nil {
		t.Fatal(err)
	}
	member, present := persisted.Member(spectator)
	if !present || member.Role != MemberRoleWaiting || member.RequestedRole != MemberRoleParticipant {
		t.Fatalf("persisted member=%+v present=%v", member, present)
	}
}

func TestServiceCommitsDurableRevocationForPlayingParticipant(t *testing.T) {
	now := time.Date(2026, time.July, 21, 14, 0, 0, 0, time.UTC)
	host, participant := uuid.New(), uuid.New()
	repository := newMemoryRoomRepository()
	playing, err := New(uuid.New(), host, "REVOKE", VisibilityPrivate, 4, now)
	if err != nil {
		t.Fatal(err)
	}
	playing, _, err = playing.Join(participant, JoinIntentParticipant, playing.Version(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	sessionID := uuid.New()
	playing, _, err = playing.StartSession(host, sessionID, "liars-dice", 2, 9, playing.Version(), now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Create(t.Context(), playing); err != nil {
		t.Fatal(err)
	}
	removedAt := now.Add(3 * time.Second)
	service, err := NewService(repository, &sequenceRoomCodeGenerator{codes: []string{"SPARE4"}}, clock.NewFake(removedAt))
	if err != nil {
		t.Fatal(err)
	}

	stored, result, err := service.RemoveMember(t.Context(), RemoveMemberCommand{
		ActorUserID: host, RoomID: playing.Snapshot().ID, UserID: participant,
		Reason: RemovalReasonHostRemoved, Expected: playing.Version(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.ParticipantRevoked || result.SourceEventID == uuid.Nil || result.SessionID != sessionID || len(repository.removals) != 1 {
		t.Fatalf("result=%+v removals=%d", result, len(repository.removals))
	}
	if _, found := stored.Member(participant); found {
		t.Fatalf("removed participant remains in room: %+v", stored.Snapshot())
	}
	fact, err := ParseParticipantRevokedEvent(repository.removals[0])
	if err != nil {
		t.Fatal(err)
	}
	if fact.EventID != result.SourceEventID || fact.RoomID != playing.Snapshot().ID || fact.SessionID != sessionID ||
		fact.UserID != participant || fact.ActorKind != RemovalActorHost || fact.ActorID != host ||
		fact.Reason != RemovalReasonHostRemoved || fact.MembershipVersion != stored.Version().Membership || !fact.OccurredAt.Equal(removedAt) {
		t.Fatalf("revocation fact=%+v", fact)
	}
}

type sequenceRoomCodeGenerator struct {
	mu    sync.Mutex
	codes []string
}

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
	mu          sync.Mutex
	byID        map[uuid.UUID]Room
	byCode      map[string]uuid.UUID
	publicRooms []PublicRoomCard
	lastList    PublicRoomListRequest
	removals    []outbox.Event
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

func (repository *memoryRoomRepository) CommitRemoval(_ context.Context, current, next Room, event outbox.Event) (Room, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	stored, exists := repository.byID[current.Snapshot().ID]
	if !exists || stored.Version() != current.Version() {
		return Room{}, ErrRoomVersionConflict
	}
	repository.byID[current.Snapshot().ID] = next
	repository.removals = append(repository.removals, event)
	return next, nil
}

func (repository *memoryRoomRepository) ListPublicRooms(_ context.Context, request PublicRoomListRequest) ([]PublicRoomCard, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.lastList = request
	limit := min(len(repository.publicRooms), int(request.Limit))
	return append([]PublicRoomCard(nil), repository.publicRooms[:limit]...), nil
}

var _ Repository = (*memoryRoomRepository)(nil)
var _ Store = (*memoryRoomRepository)(nil)
