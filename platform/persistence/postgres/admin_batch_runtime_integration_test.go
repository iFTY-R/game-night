package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/internal/integrationtest"
	adminuser "github.com/iFTY-R/game-night/platform/admin/user"
	roomdomain "github.com/iFTY-R/game-night/platform/room"
)

func TestAdminJobRepositorySupportsGetListCancelAndRetry(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	now := databaseIntegrationTime(t, ctx, fixture)
	adminID, _ := seedAdminUserCenterPrincipal(t, ctx, fixture, now)
	firstUserID, secondUserID := uuid.New(), uuid.New()
	createRoomTestUser(t, ctx, fixture, firstUserID, "BatchOne", now.Add(-time.Minute))
	createRoomTestUser(t, ctx, fixture, secondUserID, "BatchTwo", now.Add(-time.Minute))

	repository := NewAdminJobRepository(fixture.Pool)
	previewDigest := digestMarker(0x61)
	preview, err := repository.CreateBatchPreview(ctx, adminuser.CreateBatchPreviewCommand{Preview: adminuser.BatchPreview{
		ID: uuid.New(), ActorAdminID: adminID, Command: "suspend", SelectionSchemaVersion: 1,
		SelectionSnapshot: []byte(`{"schema_version":1}`), SelectionDigest: digestMarker(0x62), PreviewDigest: previewDigest,
		TargetCount: 1, ExecutableCount: 1, BlockedCount: 0, SampledAt: now, ExpiresAt: now.Add(5 * time.Minute),
	}})
	if err != nil {
		t.Fatal(err)
	}
	loadedPreview, err := repository.GetBatchPreview(ctx, preview.ID, adminID)
	if err != nil || loadedPreview.ID != preview.ID || loadedPreview.Version != preview.Version {
		t.Fatalf("get preview = %+v err=%v", loadedPreview, err)
	}

	job, err := repository.StartBatchJob(ctx, adminuser.StartBatchJobCommand{
		BatchJobID: uuid.New(), ActorAdminID: adminID, OperationID: "batch-runtime-1", RequestDigest: digestMarker(0x63),
		PreviewID: preview.ID, PreviewDigest: previewDigest, ExpectedPreviewVersion: preview.Version,
		Reason: "runtime batch repository coverage", CreatedAt: now,
		Targets: []adminuser.BatchTarget{{ItemID: uuid.New(), UserID: firstUserID, ExpectedUserVersion: 1, RequestDigest: digestMarker(0x64)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	loadedJob, err := repository.GetBatchJob(ctx, job.ID)
	if err != nil || loadedJob.ID != job.ID || loadedJob.State != "queued" {
		t.Fatalf("get job = %+v err=%v", loadedJob, err)
	}
	jobs, err := repository.ListBatchJobs(ctx, adminuser.BatchJobListQuery{
		Commands: []adminuser.BatchCommand{adminuser.BatchCommandSuspend}, SortField: adminuser.BatchJobSortCreatedAt, Direction: adminuser.SortDescending, PageSize: 10,
	})
	if err != nil || len(jobs) == 0 || jobs[0].ID != job.ID {
		t.Fatalf("list jobs = %+v err=%v", jobs, err)
	}
	// The worker does not know a job ID in advance; it must atomically claim the next runnable item globally.
	claimed, err := repository.ClaimNextBatchItem(ctx, "runtime-test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.BatchJobID != job.ID {
		t.Fatalf("global claim selected job %s, want %s", claimed.BatchJobID, job.ID)
	}
	failedAt := databaseIntegrationTime(t, ctx, fixture)
	if _, err = repository.CompleteBatchItem(ctx, claimed, "failed", "admin.user.synthetic_failure", uuid.Nil, failedAt); err != nil {
		t.Fatal(err)
	}
	items, err := repository.ListBatchItems(ctx, adminuser.BatchItemListQuery{BatchJobID: job.ID, States: []string{"failed"}, PageSize: 10})
	if err != nil || len(items) != 1 || items[0].State != "failed" {
		t.Fatalf("list failed items = %+v err=%v", items, err)
	}
	retriedJob, requeued, err := repository.RetryBatchJob(ctx, job.ID, []uuid.UUID{items[0].ID}, loadedJob.Version+2, databaseIntegrationTime(t, ctx, fixture))
	if err != nil || requeued != 1 || retriedJob.QueuedCount != 1 || retriedJob.State != "running" {
		t.Fatalf("retry job = %+v requeued=%d err=%v", retriedJob, requeued, err)
	}

	cancelPreviewDigest := digestMarker(0x65)
	cancelPreview, err := repository.CreateBatchPreview(ctx, adminuser.CreateBatchPreviewCommand{Preview: adminuser.BatchPreview{
		ID: uuid.New(), ActorAdminID: adminID, Command: "unsuspend", SelectionSchemaVersion: 1,
		SelectionSnapshot: []byte(`{"schema_version":1}`), SelectionDigest: digestMarker(0x66), PreviewDigest: cancelPreviewDigest,
		TargetCount: 2, ExecutableCount: 2, BlockedCount: 0, SampledAt: now, ExpiresAt: now.Add(5 * time.Minute),
	}})
	if err != nil {
		t.Fatal(err)
	}
	cancelJob, err := repository.StartBatchJob(ctx, adminuser.StartBatchJobCommand{
		BatchJobID: uuid.New(), ActorAdminID: adminID, OperationID: "batch-runtime-cancel", RequestDigest: digestMarker(0x67),
		PreviewID: cancelPreview.ID, PreviewDigest: cancelPreviewDigest, ExpectedPreviewVersion: cancelPreview.Version,
		Reason: "cancel queued batch", CreatedAt: now,
		Targets: []adminuser.BatchTarget{
			{ItemID: uuid.New(), UserID: firstUserID, ExpectedUserVersion: 1, RequestDigest: digestMarker(0x68)},
			{ItemID: uuid.New(), UserID: secondUserID, ExpectedUserVersion: 1, RequestDigest: digestMarker(0x69)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	canceledJob, err := repository.CancelBatchJob(ctx, cancelJob.ID, cancelJob.Version, databaseIntegrationTime(t, ctx, fixture))
	if err != nil || canceledJob.State != "canceled" || canceledJob.QueuedCount != 0 || canceledJob.CanceledCount != 2 {
		t.Fatalf("cancel job = %+v err=%v", canceledJob, err)
	}
}

func TestAdminUserGovernanceRepositoryTransitionsStatusAndRemovesRoomMember(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	now := databaseIntegrationTime(t, ctx, fixture)
	adminID, _ := seedAdminUserCenterPrincipal(t, ctx, fixture, now)
	hostID, userID := uuid.New(), uuid.New()
	createRoomTestUser(t, ctx, fixture, hostID, "GovHost", now.Add(-time.Minute))
	createRoomTestUser(t, ctx, fixture, userID, "GovUser", now.Add(-time.Minute))

	roomRepository := NewRoomRepository(fixture.Pool)
	room, err := roomdomain.New(uuid.New(), hostID, "GOVRM1", roomdomain.VisibilityPrivate, 4, now)
	if err != nil {
		t.Fatal(err)
	}
	room, err = roomRepository.Create(ctx, room)
	if err != nil {
		t.Fatal(err)
	}
	joined, _, err := room.Join(userID, roomdomain.JoinIntentParticipant, room.Version(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	room, err = roomRepository.UpdateCAS(ctx, room, joined)
	if err != nil {
		t.Fatal(err)
	}

	governance := NewAdminUserGovernanceRepository(fixture.Pool)
	state, err := governance.GetUserState(ctx, userID)
	if err != nil || state.Status != "active" || state.Version != 1 {
		t.Fatalf("get user state = %+v err=%v", state, err)
	}
	suspended, err := governance.TransitionUserStatus(ctx, userID, state.Version, "suspended", now.Add(2*time.Second))
	if err != nil || suspended.Status != "suspended" || suspended.Version != 2 {
		t.Fatalf("suspend user = %+v err=%v", suspended, err)
	}
	roomState, err := governance.GetCurrentRoom(ctx, userID)
	if err != nil || roomState.RoomID != room.Snapshot().ID || roomState.ExpectedMembershipVersion != room.Version().Membership {
		t.Fatalf("get current room = %+v err=%v", roomState, err)
	}
	if err = governance.RemoveUserFromRoom(ctx, adminID, userID, roomState, now.Add(3*time.Second)); err != nil {
		t.Fatalf("remove user from room err=%v", err)
	}
	updatedRoom, err := roomRepository.GetByID(ctx, room.Snapshot().ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := updatedRoom.Member(userID); exists {
		t.Fatalf("user %s still present in room after governance removal", userID)
	}
}
