package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/internal/integrationtest"
	adminroom "github.com/iFTY-R/game-night/platform/admin/room"
)

// The fixture keeps more event batches than the detail limit, so its aggregate version must cover every seeded batch.
const adminRoomQueryFixtureStateVersion = 64

func TestAdminRoomQueryRepositoryListsDetailsAndLimitsEvents(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	now := databaseIntegrationTime(t, ctx, fixture).Truncate(time.Second)
	roomID, sessionID, hostID, playerID := seedAdminRoomQueryFixture(t, ctx, fixture, now)

	repository := NewAdminRoomQueryRepository(fixture.Pool)
	rooms, err := repository.ListRooms(ctx, adminroom.RoomListQuery{PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 1 || rooms[0].RoomID != roomID || rooms[0].HostUserID != hostID ||
		rooms[0].ParticipantCount != 2 || rooms[0].ActiveSessionID != sessionID || rooms[0].RoomVersion != 3 {
		t.Fatalf("room list mismatch: %+v", rooms)
	}
	tail, err := repository.ListRooms(ctx, adminroom.RoomListQuery{
		PageSize: 10, After: adminroom.RoomCursor{RoomID: rooms[0].RoomID, SortTime: rooms[0].UpdatedAt},
	})
	if err != nil || len(tail) != 0 {
		t.Fatalf("room cursor tail: rooms=%+v err=%v", tail, err)
	}

	room, err := repository.GetRoom(ctx, roomID)
	if err != nil {
		t.Fatal(err)
	}
	if room.Summary.RoomID != roomID || len(room.Members) != 2 || len(room.ActiveGames) != 1 || len(room.RecentEvents) != int(adminroom.DefaultRoomDetailEventLimit) {
		t.Fatalf("room detail mismatch: %+v", room)
	}
	if room.Members[0].MembershipVersion != room.Summary.MembershipVersion {
		t.Fatalf("member version not stamped from room summary: %+v", room.Members[0])
	}

	games, err := repository.ListGames(ctx, adminroom.GameListQuery{RoomID: roomID, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 1 || games[0].SessionID != sessionID || games[0].StateVersion != adminRoomQueryFixtureStateVersion || games[0].OwnershipEpoch != 1 {
		t.Fatalf("game list mismatch: %+v", games)
	}
	game, err := repository.GetGame(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if game.Summary.SessionID != sessionID || len(game.Participants) != 2 || game.Participants[1].UserID != playerID ||
		len(game.RecentEvents) != int(adminroom.DefaultGameDetailEventLimit) {
		t.Fatalf("game detail mismatch: %+v", game)
	}
}

func seedAdminRoomQueryFixture(
	t testing.TB,
	ctx context.Context,
	fixture *integrationtest.PostgresSchema,
	now time.Time,
) (uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	hostID := uuid.MustParse("20000000-0000-0000-0000-000000000001")
	playerID := uuid.MustParse("20000000-0000-0000-0000-000000000002")
	roomID := uuid.MustParse("30000000-0000-0000-0000-000000000001")
	sessionID := uuid.MustParse("40000000-0000-0000-0000-000000000001")
	createRoomTestUser(t, ctx, fixture, hostID, "AdminRoomHost", now.Add(-10*time.Minute))
	createRoomTestUser(t, ctx, fixture, playerID, "AdminRoomPlayer", now.Add(-10*time.Minute))

	tx, err := fixture.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, "SET CONSTRAINTS ALL DEFERRED"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO party_rooms (
			room_id, room_code, visibility, status, host_user_id, participant_capacity,
			participant_admission, spectator_admission, active_session_id, active_game_id,
			last_finished_session_id, last_finished_game_id, selected_game_id, ownership_epoch,
			room_version, membership_version, created_at, updated_at
		) VALUES ($1, 'AR01', 'private', 'playing', $2, 4, 'closed', 'open', $3, 'dice',
			NULL, NULL, 'dice', 1, 3, 2, $4, $5)
	`, roomID, hostID, sessionID, now.Add(-9*time.Minute), now); err != nil {
		t.Fatal(err)
	}
	for index, member := range []struct {
		id       uuid.UUID
		username string
		seenAt   time.Time
	}{{hostID, "AdminRoomHost", now.Add(-30 * time.Second)}, {playerID, "AdminRoomPlayer", now.Add(-3 * time.Minute)}} {
		if _, err = tx.Exec(ctx, `
			INSERT INTO room_members (
				room_id, user_id, role, requested_role, seat_index, joined_at, last_seen_at, display_username, username_key
			) VALUES ($1, $2, 'participant', NULL, $3, $4, $5, $6, $7)
		`, roomID, member.id, index, now.Add(-8*time.Minute), member.seenAt, member.username, "admin-room-"+member.username); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO game_sessions (
			session_id, room_id, game_id, engine_version, protocol_version, client_version,
			state_version, ownership_epoch, snapshot_version, state_message_type, state_schema_version, state_payload,
			start_config_message_type, start_config_schema_version, start_config_payload, start_config_digest,
			start_config_revision, start_room_version, start_membership_version, start_ownership_epoch,
			cancel_reason, next_deadline_at, suspended_at, status, started_at, updated_at, ended_at
		) VALUES (
			$1, $2, 'dice', '1.0.0', '1.0.0', '1.0.0',
			$4, 1, 1, 'game.state', 1, $3,
			NULL, NULL, NULL, NULL,
			NULL, NULL, NULL, NULL,
			NULL, NULL, NULL, 'active', $5, $6, NULL
		)
	`, sessionID, roomID, []byte{1, 2, 3}, adminRoomQueryFixtureStateVersion, now.Add(-7*time.Minute), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	for index, userID := range []uuid.UUID{hostID, playerID} {
		if _, err = tx.Exec(ctx, `
			INSERT INTO game_session_participants (session_id, user_id, seat_index)
			VALUES ($1, $2, $3)
		`, sessionID, userID, index); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= int(adminroom.DefaultGameDetailEventLimit)+3; i++ {
		batchID := uuid.New()
		if _, err = tx.Exec(ctx, `
			INSERT INTO game_session_event_batches (
				batch_id, session_id, state_version, ownership_epoch, cause, actor_user_id, action_id,
				timer_id, system_operation_id, system_source_kind, system_source_event_id,
				system_requested_by_user_id, system_request_digest, executed_at, random_seed, allocated_ids,
				input_message_type, input_schema_version, input_payload, event_count, committed_at
			) VALUES (
				$1, $2, $3, 0, 'created', NULL, NULL,
				NULL, NULL, NULL, NULL,
				NULL, NULL, $4, $5, ARRAY[]::text[],
				'game.input', 1, $6, 1, $4
			)
		`, batchID, sessionID, i, now.Add(time.Duration(i)*time.Second), repeatedByte(byte(i), 32), []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `
			INSERT INTO game_session_events (batch_id, event_ordinal, message_type, schema_version, payload)
			VALUES ($1, 0, 'game.event', 1, $2)
		`, batchID, []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return roomID, sessionID, hostID, playerID
}

func repeatedByte(value byte, size int) []byte {
	result := make([]byte, size)
	for i := range result {
		result[i] = value
	}
	return result
}
