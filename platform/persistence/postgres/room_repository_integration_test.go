package postgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/internal/integrationtest"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
)

// roomRepositoryIntegrationTimeout covers schema migration and transactional CAS checks on a shared PostgreSQL service.
const roomRepositoryIntegrationTimeout = 90 * time.Second

func TestRoomRepositoryPersistsMembershipAndRejectsStaleVersions(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), roomRepositoryIntegrationTimeout)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)

	now := databaseIntegrationTime(t, ctx, fixture)
	hostID, participantID, waitingID := uuid.New(), uuid.New(), uuid.New()
	createRoomTestUser(t, ctx, fixture, hostID, "RoomHost1", now)
	createRoomTestUser(t, ctx, fixture, participantID, "RoomPlayer2", now)
	createRoomTestUser(t, ctx, fixture, waitingID, "RoomWait3", now)

	repository := NewRoomRepository(fixture.Pool)
	created, err := roomDomain.New(uuid.New(), hostID, "PGROOM1", roomDomain.VisibilityPublic, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := repository.Create(ctx, created)
	if err != nil || !reflect.DeepEqual(stored.Snapshot(), created.Snapshot()) {
		t.Fatalf("create room: expected=%+v stored=%+v err=%v", created.Snapshot(), stored.Snapshot(), err)
	}

	withParticipant, _, err := stored.Join(participantID, roomDomain.JoinIntentParticipant, stored.Version(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	withParticipant, err = repository.UpdateCAS(ctx, stored, withParticipant)
	if err != nil {
		t.Fatal(err)
	}
	withAdmission, err := withParticipant.SetAdmission(hostID, roomDomain.AdmissionApproval, roomDomain.AdmissionOpen, withParticipant.Version(), now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	withAdmission, err = repository.UpdateCAS(ctx, withParticipant, withAdmission)
	if err != nil {
		t.Fatal(err)
	}
	withWaiting, _, err := withAdmission.Join(waitingID, roomDomain.JoinIntentParticipant, withAdmission.Version(), now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	updated, err := repository.UpdateCAS(ctx, withAdmission, withWaiting)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.GetByID(ctx, created.Snapshot().ID)
	if err != nil || !reflect.DeepEqual(loaded.Snapshot(), updated.Snapshot()) {
		t.Fatalf("load updated room: loaded=%+v err=%v", loaded.Snapshot(), err)
	}
	byCode, err := repository.GetByCode(ctx, "PGROOM1")
	if err != nil || !reflect.DeepEqual(byCode.Snapshot(), updated.Snapshot()) {
		t.Fatalf("load room by code: loaded=%+v err=%v", byCode.Snapshot(), err)
	}

	first, err := updated.SetAdmission(hostID, roomDomain.AdmissionOpen, roomDomain.AdmissionOpen, updated.Version(), now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	second, err := updated.SetAdmission(hostID, roomDomain.AdmissionClosed, roomDomain.AdmissionClosed, updated.Version(), now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpdateCAS(ctx, updated, first); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpdateCAS(ctx, updated, second); !errors.Is(err, roomDomain.ErrRoomVersionConflict) {
		t.Fatalf("stale room update error=%v", err)
	}
}

func TestRoomRepositoryListsFilteredPublicCardsWithStableKeyset(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), roomRepositoryIntegrationTimeout)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)

	now := databaseIntegrationTime(t, ctx, fixture).Truncate(time.Second)
	actorID, participantID := uuid.New(), uuid.New()
	hostIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	createRoomTestUser(t, ctx, fixture, actorID, "LobbyViewer", now)
	createRoomTestUser(t, ctx, fixture, participantID, "LobbyPlayer", now)
	for index, hostID := range hostIDs {
		createRoomTestUser(t, ctx, fixture, hostID, "LobbyHost"+string(rune('A'+index)), now)
	}

	repository := NewRoomRepository(fixture.Pool)
	stableIDs := []uuid.UUID{
		uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		uuid.MustParse("00000000-0000-0000-0000-000000000002"),
		uuid.MustParse("00000000-0000-0000-0000-000000000003"),
	}
	openRoom, err := roomDomain.New(stableIDs[0], hostIDs[0], "LOBBY01", roomDomain.VisibilityPublic, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	approvalRoom, err := roomDomain.NewWithAdmission(
		stableIDs[1], hostIDs[1], "LOBBY02", roomDomain.VisibilityPublic, 3,
		roomDomain.AdmissionApproval, roomDomain.AdmissionOpen, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	fullRoom, err := roomDomain.NewWithAdmission(
		stableIDs[2], hostIDs[2], "LOBBY03", roomDomain.VisibilityPublic, 1,
		roomDomain.AdmissionOpen, roomDomain.AdmissionClosed, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []roomDomain.Room{openRoom, approvalRoom, fullRoom} {
		if _, err := repository.Create(ctx, candidate); err != nil {
			t.Fatal(err)
		}
	}

	viewerRoom, err := roomDomain.New(uuid.New(), hostIDs[3], "LOBBY04", roomDomain.VisibilityPublic, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	storedViewerRoom, err := repository.Create(ctx, viewerRoom)
	if err != nil {
		t.Fatal(err)
	}
	withViewer, _, err := storedViewerRoom.Join(actorID, roomDomain.JoinIntentSpectator, storedViewerRoom.Version(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	viewerRoom, err = repository.UpdateCAS(ctx, storedViewerRoom, withViewer)
	if err != nil {
		t.Fatal(err)
	}

	playingRoom, err := roomDomain.New(uuid.New(), hostIDs[4], "LOBBY05", roomDomain.VisibilityPublic, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	storedPlayingRoom, err := repository.Create(ctx, playingRoom)
	if err != nil {
		t.Fatal(err)
	}
	withParticipant, _, err := storedPlayingRoom.Join(
		participantID, roomDomain.JoinIntentParticipant, storedPlayingRoom.Version(), now.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	withParticipant, err = repository.UpdateCAS(ctx, storedPlayingRoom, withParticipant)
	if err != nil {
		t.Fatal(err)
	}
	playingRoom, _, err = withParticipant.StartSession(
		hostIDs[4], uuid.New(), "dice", 2, withParticipant.Version(), now.Add(2*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	playingRoom, err = repository.UpdateCAS(ctx, withParticipant, playingRoom)
	if err != nil {
		t.Fatal(err)
	}

	privateRoom, err := roomDomain.New(uuid.New(), hostIDs[5], "LOBBY06", roomDomain.VisibilityPrivate, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Create(ctx, privateRoom); err != nil {
		t.Fatal(err)
	}
	closedRoom, err := roomDomain.New(uuid.New(), hostIDs[6], "LOBBY07", roomDomain.VisibilityPublic, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	storedClosedRoom, err := repository.Create(ctx, closedRoom)
	if err != nil {
		t.Fatal(err)
	}
	closedRoom, err = storedClosedRoom.Close(hostIDs[6], storedClosedRoom.Version(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpdateCAS(ctx, storedClosedRoom, closedRoom); err != nil {
		t.Fatal(err)
	}
	suspendedRoom, err := roomDomain.New(uuid.New(), hostIDs[7], "LOBBY08", roomDomain.VisibilityPublic, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Create(ctx, suspendedRoom); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Pool.Exec(ctx, "UPDATE users SET status = 'suspended', updated_at = $2 WHERE user_id = $1", hostIDs[7], now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	firstRequest, err := roomDomain.NewPublicRoomListRequest(actorID, roomDomain.PublicRoomFilter{}, roomDomain.PublicRoomPageCursor{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	firstRows, err := repository.ListPublicRooms(ctx, firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstRows) != 3 || firstRows[0].Snapshot().RoomID != playingRoom.Snapshot().ID ||
		firstRows[0].Snapshot().ParticipantCount != 2 || firstRows[0].Snapshot().ActiveGameID != "dice" ||
		firstRows[0].PrimaryAction() != roomDomain.PublicRoomPrimaryActionSpectate ||
		firstRows[1].Snapshot().RoomID != viewerRoom.Snapshot().ID || firstRows[1].Snapshot().ViewerRole != roomDomain.MemberRoleSpectator ||
		firstRows[1].PrimaryAction() != roomDomain.PublicRoomPrimaryActionEnterRoom {
		t.Fatalf("first public rows = %+v", publicRoomSnapshots(firstRows))
	}

	after := firstRows[1].Snapshot()
	secondRequest, err := roomDomain.NewPublicRoomListRequest(
		actorID, roomDomain.PublicRoomFilter{}, roomDomain.PublicRoomPageCursor{UpdatedAt: after.UpdatedAt, RoomID: after.RoomID}, 10,
	)
	if err != nil {
		t.Fatal(err)
	}
	secondRows, err := repository.ListPublicRooms(ctx, secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	gotStableIDs := make([]uuid.UUID, 0, len(secondRows))
	for _, card := range secondRows {
		gotStableIDs = append(gotStableIDs, card.Snapshot().RoomID)
	}
	wantStableIDs := []uuid.UUID{stableIDs[2], stableIDs[1], stableIDs[0]}
	if !reflect.DeepEqual(gotStableIDs, wantStableIDs) {
		t.Fatalf("stable IDs = %v, want %v", gotStableIDs, wantStableIDs)
	}

	gameRequest, err := roomDomain.NewPublicRoomListRequest(
		actorID, roomDomain.PublicRoomFilter{GameID: "dice"}, roomDomain.PublicRoomPageCursor{}, 10,
	)
	if err != nil {
		t.Fatal(err)
	}
	gameRows, err := repository.ListPublicRooms(ctx, gameRequest)
	if err != nil || len(gameRows) != 1 || gameRows[0].Snapshot().RoomID != playingRoom.Snapshot().ID {
		t.Fatalf("game rows = %+v, err = %v", publicRoomSnapshots(gameRows), err)
	}

	joinableRequest, err := roomDomain.NewPublicRoomListRequest(
		actorID, roomDomain.PublicRoomFilter{ParticipantJoinableOnly: true}, roomDomain.PublicRoomPageCursor{}, 10,
	)
	if err != nil {
		t.Fatal(err)
	}
	joinableRows, err := repository.ListPublicRooms(ctx, joinableRequest)
	if err != nil || len(joinableRows) != 3 {
		t.Fatalf("joinable rows = %+v, err = %v", publicRoomSnapshots(joinableRows), err)
	}
}

func TestRoomRepositoryEnforcesCodeAndHostMembershipIntegrity(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), roomRepositoryIntegrationTimeout)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	now := databaseIntegrationTime(t, ctx, fixture)
	hostID := uuid.New()
	createRoomTestUser(t, ctx, fixture, hostID, "RoomHost4", now)

	repository := NewRoomRepository(fixture.Pool)
	first, err := roomDomain.New(uuid.New(), hostID, "UNIQUE7", roomDomain.VisibilityPrivate, 2, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Create(ctx, first); err != nil {
		t.Fatal(err)
	}
	second, err := roomDomain.New(uuid.New(), hostID, "UNIQUE7", roomDomain.VisibilityPrivate, 2, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Create(ctx, second); !errors.Is(err, roomDomain.ErrRoomCodeUnavailable) {
		t.Fatalf("duplicate code error=%v", err)
	}

	// The host membership FK is deferred so an atomic room+member insert works, but commit rejects missing ownership.
	transaction, err := fixture.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, execErr := transaction.Exec(ctx, `
		INSERT INTO party_rooms (
			room_id, room_code, visibility, status, host_user_id, participant_capacity,
			participant_admission, spectator_admission, room_version, membership_version, created_at, updated_at
		) VALUES ($1, 'NOHOST8', 'private', 'lobby', $2, 2, 'open', 'open', 1, 1, $3, $3)
	`, uuid.New(), hostID, now)
	if execErr != nil {
		_ = transaction.Rollback(ctx)
		t.Fatal(execErr)
	}
	if err := transaction.Commit(ctx); err == nil {
		t.Fatal("party room without host membership committed")
	}
}

func createRoomTestUser(
	t testing.TB,
	ctx context.Context,
	fixture *integrationtest.PostgresSchema,
	userID uuid.UUID,
	username string,
	at time.Time,
) {
	t.Helper()
	transaction, err := fixture.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	usernameKey := strings.ToLower(username)
	if _, err := transaction.Exec(ctx, `
		INSERT INTO users (user_id, status, username, current_username_key, username_changed_at, created_at, updated_at)
		VALUES ($1, 'active', $2, $3, $4, $4, $4)
	`, userID, username, usernameKey, at); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.Exec(ctx, `
		INSERT INTO username_claims (username_key, display_username, status, owner_user_id, created_at, updated_at)
		VALUES ($1, $2, 'active', $3, $4, $4)
	`, usernameKey, username, userID, at); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func publicRoomSnapshots(cards []roomDomain.PublicRoomCard) []roomDomain.PublicRoomCardSnapshot {
	snapshots := make([]roomDomain.PublicRoomCardSnapshot, 0, len(cards))
	for _, card := range cards {
		snapshots = append(snapshots, card.Snapshot())
	}
	return snapshots
}
