package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/internal/integrationtest"
	adminuser "github.com/iFTY-R/game-night/platform/admin/user"
)

func TestAdminJobRepositoryPreviewLeaseIdempotencyAndErasureState(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	now := databaseIntegrationTime(t, ctx, fixture)
	adminID, _ := seedAdminUserCenterPrincipal(t, ctx, fixture, now)
	userID := uuid.New()
	createRoomTestUser(t, ctx, fixture, userID, "BatchTarget", now.Add(-time.Minute))

	repository := NewAdminJobRepository(fixture.Pool)
	selectionDigest := digestMarker(0x11)
	previewDigest := digestMarker(0x12)
	preview, err := repository.CreateBatchPreview(ctx, adminuser.CreateBatchPreviewCommand{Preview: adminuser.BatchPreview{
		ID: uuid.New(), ActorAdminID: adminID, Command: "suspend", SelectionSchemaVersion: 1,
		SelectionSnapshot: []byte(`{"kind":"explicit"}`), SelectionDigest: selectionDigest, PreviewDigest: previewDigest,
		TargetCount: 1, ExecutableCount: 1, SampledAt: now.Add(-time.Second), ExpiresAt: now.Add(5 * time.Minute),
	}})
	if err != nil || preview.Version != 1 {
		t.Fatalf("create preview: preview=%+v err=%v", preview, err)
	}

	requestDigest := digestMarker(0x21)
	itemDigest := digestMarker(0x22)
	startCommand := adminuser.StartBatchJobCommand{
		BatchJobID: uuid.New(), ActorAdminID: adminID, OperationID: "batch-op-1", RequestDigest: requestDigest,
		PreviewID: preview.ID, PreviewDigest: previewDigest, ExpectedPreviewVersion: preview.Version,
		Reason: "reviewed batch suspension", CreatedAt: now,
		Targets: []adminuser.BatchTarget{{ItemID: uuid.New(), UserID: userID, ExpectedUserVersion: 1, RequestDigest: itemDigest}},
	}
	job, err := repository.StartBatchJob(ctx, startCommand)
	if err != nil || job.State != "queued" || job.QueuedCount != 1 {
		t.Fatalf("start batch job: job=%+v err=%v", job, err)
	}
	replayCommand := startCommand
	replayCommand.BatchJobID = uuid.New()
	replayCommand.Targets = append([]adminuser.BatchTarget(nil), startCommand.Targets...)
	replayCommand.Targets[0].ItemID = uuid.New()
	replayedJob, err := repository.StartBatchJob(ctx, replayCommand)
	if err != nil || replayedJob.ID != job.ID {
		t.Fatalf("batch replay: first=%+v replay=%+v err=%v", job, replayedJob, err)
	}
	conflictCommand := replayCommand
	conflictCommand.RequestDigest = digestMarker(0x23)
	if _, err = repository.StartBatchJob(ctx, conflictCommand); !errors.Is(err, adminuser.ErrIdempotencyConflict) {
		t.Fatalf("batch digest conflict error = %v", err)
	}
	var frozenItemCount int64
	if err = fixture.Pool.QueryRow(ctx, "SELECT count(*) FROM admin_batch_job_items WHERE batch_job_id = $1", job.ID).Scan(&frozenItemCount); err != nil {
		t.Fatal(err)
	}
	if frozenItemCount != 1 {
		t.Fatalf("batch replay created %d frozen items, want 1", frozenItemCount)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	items := make(chan adminuser.BatchItem, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(owner string) {
			defer wait.Done()
			<-start
			item, claimErr := repository.ClaimBatchItem(ctx, job.ID, owner, time.Minute)
			if claimErr == nil {
				items <- item
			}
			results <- claimErr
		}("worker-" + string(rune('A'+index)))
	}
	close(start)
	wait.Wait()
	close(results)
	close(items)

	winners := 0
	var claimed adminuser.BatchItem
	for claimErr := range results {
		if claimErr == nil {
			winners++
		} else if !errors.Is(claimErr, adminuser.ErrNotFound) {
			t.Fatalf("unexpected claim error: %v", claimErr)
		}
	}
	for item := range items {
		claimed = item
	}
	if winners != 1 || claimed.State != "running" || claimed.LeaseOwner == "" {
		t.Fatalf("claim winners=%d item=%+v", winners, claimed)
	}
	completedAt := databaseIntegrationTime(t, ctx, fixture)
	completed, err := repository.CompleteBatchItem(ctx, claimed, "succeeded", "", uuid.Nil, completedAt)
	if err != nil || completed.State != "succeeded" {
		t.Fatalf("complete item: item=%+v err=%v", completed, err)
	}
	var state string
	var succeeded, running int64
	if err = fixture.Pool.QueryRow(ctx, `
		SELECT state, succeeded_count, running_count
		FROM admin_batch_jobs
		WHERE batch_job_id = $1
	`, job.ID).Scan(&state, &succeeded, &running); err != nil {
		t.Fatal(err)
	}
	if state != "succeeded" || succeeded != 1 || running != 0 {
		t.Fatalf("batch aggregate state=%s succeeded=%d running=%d", state, succeeded, running)
	}

	secondUserID := uuid.New()
	createRoomTestUser(t, ctx, fixture, secondUserID, "CancelingTarget", now.Add(-time.Minute))
	cancelingPreviewDigest := digestMarker(0x24)
	cancelingPreview, err := repository.CreateBatchPreview(ctx, adminuser.CreateBatchPreviewCommand{Preview: adminuser.BatchPreview{
		ID: uuid.New(), ActorAdminID: adminID, Command: "suspend", SelectionSchemaVersion: 1,
		SelectionSnapshot: []byte(`{"kind":"explicit"}`), SelectionDigest: digestMarker(0x25), PreviewDigest: cancelingPreviewDigest,
		TargetCount: 2, ExecutableCount: 2, SampledAt: now.Add(-time.Second), ExpiresAt: now.Add(5 * time.Minute),
	}})
	if err != nil {
		t.Fatal(err)
	}
	cancelingJob, err := repository.StartBatchJob(ctx, adminuser.StartBatchJobCommand{
		BatchJobID: uuid.New(), ActorAdminID: adminID, OperationID: "batch-op-canceling", RequestDigest: digestMarker(0x26),
		PreviewID: cancelingPreview.ID, PreviewDigest: cancelingPreviewDigest, ExpectedPreviewVersion: cancelingPreview.Version,
		Reason: "cancel while one item is running", CreatedAt: now,
		Targets: []adminuser.BatchTarget{
			{ItemID: uuid.New(), UserID: userID, ExpectedUserVersion: 1, RequestDigest: digestMarker(0x27)},
			{ItemID: uuid.New(), UserID: secondUserID, ExpectedUserVersion: 1, RequestDigest: digestMarker(0x28)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelingItem, err := repository.ClaimBatchItem(ctx, cancelingJob.ID, "canceling-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Pool.Exec(ctx, "UPDATE admin_batch_jobs SET state = 'canceling' WHERE batch_job_id = $1", cancelingJob.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.CompleteBatchItem(ctx, cancelingItem, "succeeded", "", uuid.Nil, databaseIntegrationTime(t, ctx, fixture)); err != nil {
		t.Fatal(err)
	}
	var queued int64
	if err = fixture.Pool.QueryRow(ctx, `
		SELECT state, queued_count, running_count, succeeded_count
		FROM admin_batch_jobs
		WHERE batch_job_id = $1
	`, cancelingJob.ID).Scan(&state, &queued, &running, &succeeded); err != nil {
		t.Fatal(err)
	}
	if state != "canceling" || queued != 1 || running != 0 || succeeded != 1 {
		t.Fatalf("canceling batch reopened: state=%s queued=%d running=%d succeeded=%d", state, queued, running, succeeded)
	}

	erasureDigest := digestMarker(0x31)
	erasureInput := adminuser.ErasureJob{
		ID: uuid.New(), UserID: userID, ActorAdminID: adminID, OperationID: "erase-op-1",
		RequestDigest: erasureDigest, State: "queued", Step: "queued", Reason: "reviewed account deletion", CreatedAt: now,
	}
	erasure, err := repository.CreateErasureJob(ctx, adminuser.CreateErasureJobCommand{Job: erasureInput})
	if err != nil {
		t.Fatal(err)
	}
	replayInput := erasureInput
	replayInput.ID = uuid.New()
	replayed, err := repository.CreateErasureJob(ctx, adminuser.CreateErasureJobCommand{Job: replayInput})
	if err != nil || replayed.ID != erasure.ID {
		t.Fatalf("erasure replay: first=%+v replay=%+v err=%v", erasure, replayed, err)
	}
	conflictInput := erasureInput
	conflictInput.ID = uuid.New()
	conflictInput.RequestDigest = digestMarker(0x32)
	if _, err = repository.CreateErasureJob(ctx, adminuser.CreateErasureJobCommand{Job: conflictInput}); !errors.Is(err, adminuser.ErrIdempotencyConflict) {
		t.Fatalf("erasure digest conflict error = %v", err)
	}

	claimedErasure, err := repository.ClaimErasureJob(ctx, "erase-worker", time.Minute)
	if err != nil || claimedErasure.ID != erasure.ID || claimedErasure.State != "running" {
		t.Fatalf("claim erasure: job=%+v err=%v", claimedErasure, err)
	}
	for _, nextStep := range []string{"erase_profile", "enqueue_room_cleanup", "complete"} {
		changedAt := databaseIntegrationTime(t, ctx, fixture)
		claimedErasure, err = repository.AdvanceErasureJob(ctx, claimedErasure, nextStep, changedAt)
		if err != nil || claimedErasure.Step != nextStep {
			t.Fatalf("advance erasure to %s: job=%+v err=%v", nextStep, claimedErasure, err)
		}
	}
	if _, err = repository.AdvanceErasureJob(ctx, claimedErasure, "erase_profile", databaseIntegrationTime(t, ctx, fixture)); !errors.Is(err, adminuser.ErrInvalidInput) {
		t.Fatalf("backward erasure step error = %v", err)
	}
	erasureCompletedAt := databaseIntegrationTime(t, ctx, fixture)
	finishedErasure, err := repository.CompleteErasureJob(ctx, claimedErasure, "succeeded", "", erasureCompletedAt)
	if err != nil || finishedErasure.State != "succeeded" {
		t.Fatalf("complete erasure: job=%+v err=%v", finishedErasure, err)
	}
}

func TestAdminJobRepositoryRejectsExpiredPreview(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	now := databaseIntegrationTime(t, ctx, fixture)
	adminID, _ := seedAdminUserCenterPrincipal(t, ctx, fixture, now)
	userID := uuid.New()
	createRoomTestUser(t, ctx, fixture, userID, "ExpiredBatchTarget", now.Add(-time.Minute))
	repository := NewAdminJobRepository(fixture.Pool)
	previewDigest := digestMarker(0x41)
	preview, err := repository.CreateBatchPreview(ctx, adminuser.CreateBatchPreviewCommand{Preview: adminuser.BatchPreview{
		ID: uuid.New(), ActorAdminID: adminID, Command: "suspend", SelectionSchemaVersion: 1,
		SelectionSnapshot: []byte(`{"kind":"explicit"}`), SelectionDigest: digestMarker(0x42), PreviewDigest: previewDigest,
		TargetCount: 1, ExecutableCount: 1, SampledAt: now.Add(-2 * time.Minute), ExpiresAt: now.Add(-time.Minute),
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.StartBatchJob(ctx, adminuser.StartBatchJobCommand{
		BatchJobID: uuid.New(), ActorAdminID: adminID, OperationID: "expired-preview-op", RequestDigest: digestMarker(0x43),
		PreviewID: preview.ID, PreviewDigest: previewDigest, ExpectedPreviewVersion: preview.Version,
		Reason: "expired preview must fail", CreatedAt: now,
		Targets: []adminuser.BatchTarget{{ItemID: uuid.New(), UserID: userID, ExpectedUserVersion: 1, RequestDigest: digestMarker(0x44)}},
	})
	if !errors.Is(err, adminuser.ErrConflict) {
		t.Fatalf("expired preview error = %v", err)
	}
}

func digestMarker(marker byte) [32]byte {
	var digest [32]byte
	for index := range digest {
		digest[index] = marker
	}
	return digest
}
