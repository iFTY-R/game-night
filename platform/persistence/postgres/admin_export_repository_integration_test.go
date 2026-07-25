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

func TestAdminExportRepositoryResultTTLAndSingleUseGrant(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	now := databaseIntegrationTime(t, ctx, fixture)
	adminID, sessionID := seedAdminUserCenterPrincipal(t, ctx, fixture, now)
	repository := NewAdminExportRepository(fixture.Pool)

	job := createReadyExportJob(t, ctx, fixture, repository, adminID, "export-op-1", now, now.Add(time.Hour))
	exportReplayInput := adminuser.ExportJob{
		ID: uuid.New(), ActorAdminID: adminID, OperationID: "export-op-1", RequestDigest: digestMarker(0x51),
		FilterSchemaVersion: 1, FilterSnapshot: []byte(`{"statuses":["active"]}`), FilterDigest: digestMarker(0x52),
		Fields: []string{"user_id", "username"}, MaskingPolicy: "redact_pii", State: "queued",
		ResultSchemaVersion: 1, ResultExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}
	replayedExport, err := repository.CreateExportJob(ctx, adminuser.CreateExportJobCommand{Job: exportReplayInput})
	if err != nil || replayedExport.ID != job.ID {
		t.Fatalf("export replay: first=%+v replay=%+v err=%v", job, replayedExport, err)
	}
	exportConflictInput := exportReplayInput
	exportConflictInput.ID = uuid.New()
	exportConflictInput.RequestDigest = digestMarker(0x54)
	if _, err = repository.CreateExportJob(ctx, adminuser.CreateExportJobCommand{Job: exportConflictInput}); !errors.Is(err, adminuser.ErrIdempotencyConflict) {
		t.Fatalf("export digest conflict error = %v", err)
	}
	var exportCount int64
	if err = fixture.Pool.QueryRow(ctx, "SELECT count(*) FROM admin_export_jobs WHERE actor_admin_id = $1 AND operation_id = $2", adminID, "export-op-1").Scan(&exportCount); err != nil {
		t.Fatal(err)
	}
	if exportCount != 1 {
		t.Fatalf("export replay created %d jobs, want 1", exportCount)
	}
	grantCreatedAt := databaseIntegrationTime(t, ctx, fixture)
	grantInput := adminuser.DownloadGrant{
		ID: uuid.New(), ExportID: job.ID, ActorAdminID: adminID, SessionID: sessionID,
		OperationID: "grant-op-1", RequestDigest: digestMarker(0x61), TokenDigest: digestMarker(0x62), TokenKeyVersion: 1,
		ExpectedExportVersion: job.Version, MaskingPolicy: job.MaskingPolicy,
		CreatedAt: grantCreatedAt, ExpiresAt: grantCreatedAt.Add(5 * time.Minute),
	}
	grant, err := repository.CreateDownloadGrant(ctx, grantInput)
	if err != nil || grant.State != "active" {
		t.Fatalf("create grant: grant=%+v err=%v", grant, err)
	}
	grantReplayInput := grantInput
	grantReplayInput.ID = uuid.New()
	replayedGrant, err := repository.CreateDownloadGrant(ctx, grantReplayInput)
	if err != nil || replayedGrant.ID != grant.ID {
		t.Fatalf("grant replay: first=%+v replay=%+v err=%v", grant, replayedGrant, err)
	}
	grantConflictInput := grantReplayInput
	grantConflictInput.ID = uuid.New()
	grantConflictInput.RequestDigest = digestMarker(0x63)
	if _, err = repository.CreateDownloadGrant(ctx, grantConflictInput); !errors.Is(err, adminuser.ErrIdempotencyConflict) {
		t.Fatalf("grant digest conflict error = %v", err)
	}
	var grantCount int64
	if err = fixture.Pool.QueryRow(ctx, "SELECT count(*) FROM admin_export_download_grants WHERE actor_admin_id = $1 AND operation_id = $2", adminID, "grant-op-1").Scan(&grantCount); err != nil {
		t.Fatal(err)
	}
	if grantCount != 1 {
		t.Fatalf("grant replay created %d grants, want 1", grantCount)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	consumed := make(chan adminuser.ConsumedExport, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, consumeErr := repository.ConsumeDownloadGrant(ctx, grant.TokenKeyVersion, grant.TokenDigest, adminID, sessionID)
			if consumeErr == nil {
				consumed <- result
			}
			results <- consumeErr
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(consumed)

	winners := 0
	for consumeErr := range results {
		if consumeErr == nil {
			winners++
		} else if !errors.Is(consumeErr, adminuser.ErrConflict) {
			t.Fatalf("unexpected grant consumption error: %v", consumeErr)
		}
	}
	for result := range consumed {
		if result.ResultObjectKey != "admin-exports/export-op-1.enc" || result.Grant.State != "consumed" || result.ExportVersion != job.Version {
			t.Fatalf("unexpected consumed export: %+v", result)
		}
	}
	if winners != 1 {
		t.Fatalf("download grant winners = %d, want 1", winners)
	}

	var expiresAt time.Time
	if err = fixture.Pool.QueryRow(ctx, "SELECT result_expires_at FROM admin_export_jobs WHERE export_id = $1", job.ID).Scan(&expiresAt); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.ConsumeDownloadGrant(ctx, grant.TokenKeyVersion, grant.TokenDigest, adminID, sessionID); !errors.Is(err, adminuser.ErrConflict) {
		t.Fatalf("replayed grant error = %v", err)
	}
	var expiresAfterReplay time.Time
	if err = fixture.Pool.QueryRow(ctx, "SELECT result_expires_at FROM admin_export_jobs WHERE export_id = $1", job.ID).Scan(&expiresAfterReplay); err != nil {
		t.Fatal(err)
	}
	if !expiresAfterReplay.Equal(expiresAt) {
		t.Fatalf("failed download extended result ttl: before=%v after=%v", expiresAt, expiresAfterReplay)
	}
	deletedAt := databaseIntegrationTime(t, ctx, fixture)
	deleted, err := repository.DeleteExportResult(ctx, adminuser.DeleteExportResultCommand{
		ExportID: job.ID, ExpectedVersion: job.Version, DeletedAt: deletedAt,
	})
	if err != nil || deleted.State != "deleted" || deleted.ResultObjectKey != "" || deleted.Version != job.Version+1 {
		t.Fatalf("delete export result: job=%+v err=%v", deleted, err)
	}
	if _, err = repository.DeleteExportResult(ctx, adminuser.DeleteExportResultCommand{
		ExportID: job.ID, ExpectedVersion: job.Version, DeletedAt: deletedAt,
	}); !errors.Is(err, adminuser.ErrConflict) {
		t.Fatalf("stale export delete error = %v", err)
	}

	failedInput := adminuser.ExportJob{
		ID: uuid.New(), ActorAdminID: adminID, OperationID: "export-op-failed", RequestDigest: digestMarker(0x65),
		FilterSchemaVersion: 1, FilterSnapshot: []byte(`{"statuses":["active"]}`), FilterDigest: digestMarker(0x66),
		Fields: []string{"user_id"}, MaskingPolicy: "redact_pii", State: "queued",
		ResultSchemaVersion: 1, ResultExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}
	createdFailure, err := repository.CreateExportJob(ctx, adminuser.CreateExportJobCommand{Job: failedInput})
	if err != nil {
		t.Fatal(err)
	}
	claimedFailure, err := repository.ClaimExportJob(ctx, "export-worker", time.Minute)
	if err != nil || claimedFailure.ID != createdFailure.ID {
		t.Fatalf("claim failed export: job=%+v err=%v", claimedFailure, err)
	}
	failed, err := repository.FailExportJob(ctx, adminuser.FailExportJobCommand{
		Job: claimedFailure, MatchedUsers: 2, ExportedUsers: 1, FailedUsers: 1,
		ErrorMessageKey: "admin.export.object_write_failed", CompletedAt: databaseIntegrationTime(t, ctx, fixture),
	})
	if err != nil || failed.State != "failed" || failed.ErrorMessageKey != "admin.export.object_write_failed" {
		t.Fatalf("fail export: job=%+v err=%v", failed, err)
	}
	loadedFailure, err := repository.GetExportJob(ctx, failed.ID)
	if err != nil || loadedFailure.ErrorMessageKey != failed.ErrorMessageKey || loadedFailure.Version != failed.Version {
		t.Fatalf("load failed export: job=%+v err=%v", loadedFailure, err)
	}

	expiring := createReadyExportJob(t, ctx, fixture, repository, adminID, "export-op-expiring", now, now.Add(time.Minute))
	if _, err = repository.CreateDownloadGrant(ctx, adminuser.DownloadGrant{
		ID: uuid.New(), ExportID: expiring.ID, ActorAdminID: adminID, SessionID: sessionID,
		OperationID: "grant-op-after-result-expiry", RequestDigest: digestMarker(0x63), TokenDigest: digestMarker(0x64), TokenKeyVersion: 1,
		ExpectedExportVersion: expiring.Version, MaskingPolicy: expiring.MaskingPolicy,
		CreatedAt: grantCreatedAt, ExpiresAt: grantCreatedAt.Add(5 * time.Minute),
	}); !errors.Is(err, adminuser.ErrConflict) {
		t.Fatalf("grant beyond result expiry error = %v", err)
	}
	expiredIDs, err := repository.ExpireExportResults(ctx, now.Add(2*time.Minute))
	if err != nil || len(expiredIDs) != 1 || expiredIDs[0] != expiring.ID {
		t.Fatalf("expire results: ids=%v err=%v", expiredIDs, err)
	}
	var state string
	var objectKey *string
	if err = fixture.Pool.QueryRow(ctx, "SELECT state, result_object_key FROM admin_export_jobs WHERE export_id = $1", expiring.ID).Scan(&state, &objectKey); err != nil {
		t.Fatal(err)
	}
	if state != "expired" || objectKey != nil {
		t.Fatalf("expired export state=%s object_key=%v", state, objectKey)
	}
}

func createReadyExportJob(
	t testing.TB,
	ctx context.Context,
	fixture *integrationtest.PostgresSchema,
	repository *AdminExportRepository,
	adminID uuid.UUID,
	operationID string,
	createdAt, expiresAt time.Time,
) adminuser.ExportJob {
	t.Helper()
	created, err := repository.CreateExportJob(ctx, adminuser.CreateExportJobCommand{Job: adminuser.ExportJob{
		ID: uuid.New(), ActorAdminID: adminID, OperationID: operationID, RequestDigest: digestMarker(0x51),
		FilterSchemaVersion: 1, FilterSnapshot: []byte(`{"statuses":["active"]}`), FilterDigest: digestMarker(0x52),
		Fields: []string{"user_id", "username"}, MaskingPolicy: "redact_pii", State: "queued",
		ResultSchemaVersion: 1, ResultExpiresAt: expiresAt, CreatedAt: createdAt,
	}})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := repository.ClaimExportJob(ctx, "export-worker", time.Minute)
	if err != nil || claimed.ID != created.ID {
		t.Fatalf("claim export: created=%+v claimed=%+v err=%v", created, claimed, err)
	}
	completedAt := databaseIntegrationTime(t, ctx, fixture)
	completed, err := repository.CompleteExportJob(ctx, adminuser.CompleteExportJobCommand{
		Job: claimed, NextState: "succeeded", MatchedUsers: 2, ExportedUsers: 2,
		ResultObjectKey: "admin-exports/" + operationID + ".enc", ResultDigest: digestMarker(0x53),
		ResultKeyVersion: 1, CompletedAt: completedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return completed
}
