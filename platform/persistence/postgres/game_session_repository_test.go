package postgres

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestFrozenStartConfigFromRowAllowsLegacyNullMetadata(t *testing.T) {
	start, err := frozenStartConfigFromRow(testGameSessionRow(t))
	if err != nil {
		t.Fatal(err)
	}
	if start.Config.Valid() || start.ConfigDigest != (idempotency.Digest{}) || start.ConfigRevision != 0 ||
		start.RoomVersion != 0 || start.MembershipVersion != 0 || start.RoomOwnershipEpoch != 0 {
		t.Fatalf("legacy start metadata = %+v", start)
	}
}

func TestSessionFromRowsRestoresFrozenStartConfig(t *testing.T) {
	row := testGameSessionRow(t)
	row.StartConfigMessageType = pgtype.Text{String: "round.config", Valid: true}
	row.StartConfigSchemaVersion = pgtype.Int4{Int32: 1, Valid: true}
	row.StartConfigPayload = []byte("config")
	row.StartConfigRevision = pgtype.Int8{Int64: 7, Valid: true}
	row.StartRoomVersion = pgtype.Int8{Int64: 11, Valid: true}
	row.StartMembershipVersion = pgtype.Int8{Int64: 13, Valid: true}
	row.StartOwnershipEpoch = pgtype.Int8{Int64: 17, Valid: true}
	versionKey := game.VersionKey{GameID: "dice", Engine: "1.2.3", Protocol: "2.3.4", Client: "3.4.5"}
	row.StartConfigDigest = gameruntimeDigest(t, versionKey, game.Message{
		MessageType: "round.config", SchemaVersion: 1, Payload: []byte("config"),
	}).Bytes()

	session, err := sessionFromRows(row, []sqlcgen.GameSessionParticipant{{
		SessionID: row.SessionID,
		UserID:    uuidToPG(uuid.New()),
		SeatIndex: 0,
	}}, []sqlcgen.GameSessionTimer{{
		SessionID:            row.SessionID,
		TimerID:              "turn.timeout",
		ExpectedStateVersion: 1,
		DueAt:                timeToPG(row.StartedAt.Time.Add(30 * time.Second)),
		MessageType:          "round.timer",
		SchemaVersion:        1,
		Payload:              []byte("timer"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	start := session.Snapshot().Start
	if start.Config.MessageType != "round.config" || start.Config.SchemaVersion != 1 ||
		!bytes.Equal(start.Config.Payload, []byte("config")) || start.ConfigDigest != gameruntimeDigest(t, versionKey, start.Config) ||
		start.ConfigRevision != 7 || start.RoomVersion != 11 ||
		start.MembershipVersion != 13 || start.RoomOwnershipEpoch != 17 {
		t.Fatalf("restored start metadata = %+v", start)
	}
}

func TestSessionFromRowsRestoresZeroStartConfigRevision(t *testing.T) {
	row := testGameSessionRow(t)
	versionKey := game.VersionKey{GameID: "dice", Engine: "1.2.3", Protocol: "2.3.4", Client: "3.4.5"}
	payload := []byte("config-zero")
	row.StartConfigMessageType = pgtype.Text{String: "round.config", Valid: true}
	row.StartConfigSchemaVersion = pgtype.Int4{Int32: 1, Valid: true}
	row.StartConfigPayload = payload
	row.StartConfigDigest = gameruntimeDigest(t, versionKey, game.Message{
		MessageType: "round.config", SchemaVersion: 1, Payload: payload,
	}).Bytes()
	row.StartConfigRevision = pgtype.Int8{Int64: 0, Valid: true}
	row.StartRoomVersion = pgtype.Int8{Int64: 11, Valid: true}
	row.StartMembershipVersion = pgtype.Int8{Int64: 13, Valid: true}
	row.StartOwnershipEpoch = pgtype.Int8{Int64: 17, Valid: true}

	session, err := sessionFromRows(row, []sqlcgen.GameSessionParticipant{{
		SessionID: row.SessionID,
		UserID:    uuidToPG(uuid.New()),
		SeatIndex: 0,
	}}, []sqlcgen.GameSessionTimer{{
		SessionID:            row.SessionID,
		TimerID:              "turn.timeout",
		ExpectedStateVersion: 1,
		DueAt:                timeToPG(row.StartedAt.Time.Add(30 * time.Second)),
		MessageType:          "round.timer",
		SchemaVersion:        1,
		Payload:              []byte("timer"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if session.Snapshot().Start.ConfigRevision != 0 {
		t.Fatalf("zero revision lost: %+v", session.Snapshot().Start)
	}
}

func TestFrozenStartConfigFromRowRejectsPartialStartMetadata(t *testing.T) {
	row := testGameSessionRow(t)
	versionKey := game.VersionKey{GameID: "dice", Engine: "1.2.3", Protocol: "2.3.4", Client: "3.4.5"}
	row.StartConfigMessageType = pgtype.Text{String: "round.config", Valid: true}
	row.StartConfigSchemaVersion = pgtype.Int4{Int32: 1, Valid: true}
	row.StartConfigPayload = []byte("config")
	row.StartConfigDigest = gameruntimeDigest(t, versionKey, game.Message{
		MessageType: "round.config", SchemaVersion: 1, Payload: []byte("config"),
	}).Bytes()
	row.StartRoomVersion = pgtype.Int8{Int64: 11, Valid: true}
	row.StartMembershipVersion = pgtype.Int8{Int64: 13, Valid: true}
	row.StartOwnershipEpoch = pgtype.Int8{Int64: 17, Valid: true}

	if _, err := frozenStartConfigFromRow(row); !errors.Is(err, gameruntime.ErrGameSessionIntegrity) {
		t.Fatalf("partial start metadata error = %v", err)
	}
}

func TestCreateGameSessionParamsKeepsZeroStartConfigRevisionPresent(t *testing.T) {
	versionKey := game.VersionKey{GameID: "dice", Engine: "1.2.3", Protocol: "2.3.4", Client: "3.4.5"}
	payload := []byte("config-zero")
	snapshot := gameruntime.SessionSnapshot{
		ID: uuid.New(), RoomID: uuid.New(), VersionKey: versionKey, OwnershipEpoch: 1,
		Participants: []gameruntime.Participant{{UserID: uuid.New(), SeatIndex: 0}},
		Start: gameruntime.FrozenStartConfig{
			Config:             game.Message{MessageType: "round.config", SchemaVersion: 1, Payload: payload},
			ConfigDigest:       gameruntimeDigest(t, versionKey, game.Message{MessageType: "round.config", SchemaVersion: 1, Payload: payload}),
			ConfigRevision:     0,
			RoomVersion:        11,
			MembershipVersion:  13,
			RoomOwnershipEpoch: 17,
		},
		State: game.Snapshot{
			SnapshotVersion: 1, StateVersion: 1,
			State: game.Message{MessageType: "round.state", SchemaVersion: 1, Payload: []byte("state")},
		},
		Status:    gameruntime.StatusActive,
		StartedAt: time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC),
	}

	params := createGameSessionParams(snapshot)
	if !params.StartConfigRevision.Valid || params.StartConfigRevision.Int64 != 0 {
		t.Fatalf("start config revision params = %+v", params.StartConfigRevision)
	}
}

func TestSessionFromRowsRestoresCancelledReason(t *testing.T) {
	row := testGameSessionRow(t)
	now := row.UpdatedAt.Time.Add(time.Microsecond)
	row.Status = string(gameruntime.StatusCancelled)
	row.NextDeadlineAt = pgtype.Timestamptz{}
	row.UpdatedAt = timeToPG(now)
	row.EndedAt = timeToPG(now)
	row.CancelReason = pgtype.Text{String: string(gameruntime.CancelReasonLegacyCancelled), Valid: true}
	session, err := sessionFromRows(row, []sqlcgen.GameSessionParticipant{{
		SessionID: row.SessionID,
		UserID:    uuidToPG(uuid.New()),
		SeatIndex: 0,
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if session.Snapshot().CancelReason != gameruntime.CancelReasonLegacyCancelled {
		t.Fatalf("cancel reason = %q", session.Snapshot().CancelReason)
	}
}

func testGameSessionRow(t testing.TB) sqlcgen.GameSession {
	t.Helper()
	sessionID := uuid.New()
	roomID := uuid.New()
	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	return sqlcgen.GameSession{
		SessionID:          uuidToPG(sessionID),
		RoomID:             uuidToPG(roomID),
		GameID:             "dice",
		EngineVersion:      "1.2.3",
		ProtocolVersion:    "2.3.4",
		ClientVersion:      "3.4.5",
		StateVersion:       1,
		OwnershipEpoch:     1,
		SnapshotVersion:    1,
		StateMessageType:   "round.state",
		StateSchemaVersion: 1,
		StatePayload:       []byte("state"),
		NextDeadlineAt:     timeToPG(now.Add(30 * time.Second)),
		Status:             string(gameruntime.StatusActive),
		StartedAt:          timeToPG(now),
		UpdatedAt:          timeToPG(now),
	}
}

func gameruntimeDigest(t testing.TB, versionKey game.VersionKey, message game.Message) idempotency.Digest {
	t.Helper()
	hasher := sha256.New()
	var length [8]byte
	for _, field := range [][]byte{
		[]byte(versionKey.GameID),
		[]byte(versionKey.Engine),
		[]byte(versionKey.Protocol),
		[]byte(versionKey.Client),
		[]byte(message.MessageType),
	} {
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		_, _ = hasher.Write(length[:])
		_, _ = hasher.Write(field)
	}
	var version [8]byte
	binary.BigEndian.PutUint64(version[:], uint64(message.SchemaVersion))
	binary.BigEndian.PutUint64(length[:], uint64(len(version)))
	_, _ = hasher.Write(length[:])
	_, _ = hasher.Write(version[:])
	binary.BigEndian.PutUint64(length[:], uint64(len(message.Payload)))
	_, _ = hasher.Write(length[:])
	_, _ = hasher.Write(message.Payload)
	digest, err := idempotency.NewDigest(hasher.Sum(nil))
	if err != nil {
		t.Fatal(err)
	}
	if digest == (idempotency.Digest{}) {
		t.Fatal("zero digest")
	}
	return digest
}
