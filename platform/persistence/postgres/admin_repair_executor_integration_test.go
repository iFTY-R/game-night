package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	adminroom "github.com/iFTY-R/game-night/platform/admin/room"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/idempotency"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
)

func TestAdminEmergencyRepairExecutorTerminatesUnrecoverableGame(t *testing.T) {
	fixture, repository, session, now := openGameSessionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
	defer cancel()
	owned, err := session.AcquireOwnership(0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	owned, err = repository.AcquireOwnershipCAS(ctx, session, owned)
	if err != nil {
		t.Fatal(err)
	}
	repair := newTerminateRepairForSession(t, owned)
	command := adminroom.ExecuteEmergencyRepairCommand{
		OperationID:           adminRepairOperationID(t, 1),
		ExpectedRepairVersion: repair.Version,
		Reason:                "operator reviewed unrecoverable game state",
		ExecutedAt:            now.Add(2 * time.Second),
	}

	afterDigest, err := NewAdminEmergencyRepairExecutor(fixture.Pool).ExecuteEmergencyRepair(ctx, repair, command)
	if err != nil {
		t.Fatal(err)
	}
	storedSession, err := repository.Get(ctx, owned.Snapshot().ID)
	if err != nil {
		t.Fatal(err)
	}
	storedRoom, err := NewRoomRepository(fixture.Pool).GetByID(ctx, owned.Snapshot().RoomID)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterDigest) != idempotency.DigestSize ||
		storedSession.Snapshot().Status != gameruntime.StatusCancelled ||
		storedSession.Snapshot().CancelReason != gameruntime.CancelReasonPlatformCancelled ||
		storedRoom.Snapshot().Status != roomDomain.RoomStatusLobby ||
		storedRoom.Snapshot().ActiveSessionID != uuid.Nil ||
		storedRoom.Snapshot().ActiveGameID != "" {
		t.Fatalf("digest=%x room=%+v session=%+v", afterDigest, storedRoom.Snapshot(), storedSession.Snapshot())
	}
}

func TestAdminEmergencyRepairExecutorRejectsStaleTerminatePreview(t *testing.T) {
	fixture, repository, session, now := openGameSessionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), gameSessionRepositoryIntegrationTimeout)
	defer cancel()
	owned, err := session.AcquireOwnership(0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	owned, err = repository.AcquireOwnershipCAS(ctx, session, owned)
	if err != nil {
		t.Fatal(err)
	}
	repair := newTerminateRepairForSession(t, owned)
	repair.ExpectedOwnershipEpoch++

	_, err = NewAdminEmergencyRepairExecutor(fixture.Pool).ExecuteEmergencyRepair(ctx, repair, adminroom.ExecuteEmergencyRepairCommand{
		OperationID:           adminRepairOperationID(t, 2),
		ExpectedRepairVersion: repair.Version,
		Reason:                "operator reviewed stale unrecoverable game state",
		ExecutedAt:            now.Add(2 * time.Second),
	})
	if !errors.Is(err, adminroom.ErrConflict) {
		t.Fatalf("stale preview error = %v", err)
	}
}

func newTerminateRepairForSession(t testing.TB, session gameruntime.Session) adminroom.RepairOperation {
	t.Helper()
	snapshot := session.Snapshot()
	targetDigest := adminRepairStableDigest(
		"game", snapshot.ID.String(), snapshot.RoomID.String(), snapshot.Status,
		snapshot.State.StateVersion, snapshot.OwnershipEpoch,
	)
	previewDigest := adminRepairStableDigest("preview", snapshot.ID.String())
	return adminroom.RepairOperation{
		RepairID:               uuid.New(),
		RepairType:             adminroom.RepairTerminateUnrecoverable,
		State:                  adminroom.RepairStatePreviewed,
		TargetID:               snapshot.ID,
		TargetKind:             adminroom.RepairTargetKindGameSession,
		TargetDigest:           append([]byte(nil), targetDigest[:]...),
		PreviewDigest:          append([]byte(nil), previewDigest[:]...),
		CommandVersion:         adminroom.RepairCommandVersion,
		ExpectedStateVersion:   snapshot.State.StateVersion,
		ExpectedOwnershipEpoch: snapshot.OwnershipEpoch,
		Summary:                "terminate unrecoverable active game session",
		IrreversibleEffects:    []string{"commit a reviewed terminal cancellation through the repair executor"},
		BeforeSnapshotDigest:   append([]byte(nil), targetDigest[:]...),
		Version:                1,
		CreatedAt:              snapshot.UpdatedAt,
		ExpiresAt:              snapshot.UpdatedAt.Add(adminroom.DefaultRepairPreviewTTL),
	}
}

func adminRepairOperationID(t testing.TB, marker byte) idempotency.OperationID {
	t.Helper()
	operationID, err := idempotency.NewOperationID([]byte{marker, marker, marker, marker, marker, marker, marker, marker, marker, marker, marker, marker, marker, marker, marker, marker})
	if err != nil {
		t.Fatal(err)
	}
	return operationID
}
