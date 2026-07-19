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
