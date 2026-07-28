package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/internal/integrationtest"
	adminroom "github.com/iFTY-R/game-night/platform/admin/room"
)

func TestAdminRepairRepositoryPreviewExpiryAndCompletionCAS(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	now := databaseIntegrationTime(t, ctx, fixture)
	adminID, _ := seedAdminUserCenterPrincipal(t, ctx, fixture, now)

	repository := NewAdminRepairRepository(fixture.Pool)
	repairID := uuid.New()
	created, err := repository.CreateRepairOperation(ctx, adminroom.CreateRepairOperationCommand{RepairOperation: adminroom.RepairOperation{
		RepairID: repairID, RepairType: adminroom.RepairRepairRoomGameLink, State: adminroom.RepairStatePreviewed,
		TargetID: uuid.New(), TargetKind: adminroom.RepairTargetKindRoom, TargetDigest: repeatedByte(1, 32),
		PreviewDigest: repeatedByte(2, 32), CommandVersion: 1, ExpectedRoomVersion: 3, ExpectedMembershipVersion: 2,
		Summary: "repair active room and game session pointer", IrreversibleEffects: []string{"room pointer may be changed"},
		BeforeSnapshotDigest: repeatedByte(3, 32), RequestedByAdminID: adminID, Reason: "investigated room link mismatch",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}})
	if err != nil || created.Version != 1 || created.State != adminroom.RepairStatePreviewed {
		t.Fatalf("create repair: repair=%+v err=%v", created, err)
	}
	loaded, err := repository.GetRepairOperation(ctx, repairID)
	if err != nil || loaded.RepairID != repairID || loaded.ExpectedRoomVersion != 3 {
		t.Fatalf("load repair: repair=%+v err=%v", loaded, err)
	}
	if _, err = repository.ExpireRepairOperation(ctx, repairID, created.Version); !errors.Is(err, adminroom.ErrConflict) {
		t.Fatalf("unexpired repair expire error = %v", err)
	}

	operationID := "AdminRoomRepairOperation0001"
	completed, err := repository.CompleteRepairOperation(ctx, adminroom.CompleteRepairOperationCommand{
		RepairID: repairID, OperationID: operationID, RequestDigest: repeatedByte(4, 32), AuditEventID: uuid.New(),
		AfterSnapshotDigest: repeatedByte(5, 32), Reason: "execute reviewed link repair", State: adminroom.RepairStateExecuted,
		ExpectedVersion: created.Version, ExecutedAt: now.Add(time.Minute),
	})
	if err != nil || completed.State != adminroom.RepairStateExecuted || completed.Version != 2 || completed.OperationID != operationID {
		t.Fatalf("complete repair: repair=%+v err=%v", completed, err)
	}
	if _, err = repository.CompleteRepairOperation(ctx, adminroom.CompleteRepairOperationCommand{
		RepairID: repairID, OperationID: "AdminRoomRepairOperation0002", RequestDigest: repeatedByte(6, 32), AuditEventID: uuid.New(),
		AfterSnapshotDigest: repeatedByte(7, 32), Reason: "repeat stale repair", State: adminroom.RepairStateExecuted,
		ExpectedVersion: created.Version, ExecutedAt: now.Add(2 * time.Minute),
	}); !errors.Is(err, adminroom.ErrConflict) {
		t.Fatalf("stale repair completion error = %v", err)
	}

	expiredID := uuid.New()
	expired, err := repository.CreateRepairOperation(ctx, adminroom.CreateRepairOperationCommand{RepairOperation: adminroom.RepairOperation{
		RepairID: expiredID, RepairType: adminroom.RepairClearStaleOwnerLease, State: adminroom.RepairStatePreviewed,
		TargetID: uuid.New(), TargetKind: adminroom.RepairTargetKindGameSession, TargetDigest: repeatedByte(8, 32),
		PreviewDigest: repeatedByte(9, 32), CommandVersion: 1, ExpectedStateVersion: 10, ExpectedOwnershipEpoch: 1,
		Summary: "clear stale owner lease", IrreversibleEffects: []string{"owner lease will be removed"},
		BeforeSnapshotDigest: repeatedByte(10, 32), RequestedByAdminID: adminID, Reason: "owner lease expired",
		CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour),
	}})
	if err != nil {
		t.Fatal(err)
	}
	expired, err = repository.ExpireRepairOperation(ctx, expiredID, expired.Version)
	if err != nil || expired.State != adminroom.RepairStateExpired || expired.Version != 2 {
		t.Fatalf("expire repair: repair=%+v err=%v", expired, err)
	}
}
