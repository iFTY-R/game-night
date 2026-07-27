package operations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCacheRefreshUsesClosedNamespaceAndDurableReplay(t *testing.T) {
	now := time.Date(2026, 7, 27, 13, 0, 0, 0, time.UTC)
	service, actor, repository, transaction := newCommandTestFixture(t, now)
	if _, err := service.PreviewCacheRefresh(context.Background(), actor, PreviewCacheRefreshInput{Namespace: CacheNamespace("redis:*"), Reason: "refresh projection"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("arbitrary namespace error = %v", err)
	}
	preview, err := service.PreviewCacheRefresh(context.Background(), actor, PreviewCacheRefreshInput{Namespace: CacheOverviewProjection, Reason: "refresh projection"})
	if err != nil || preview.EstimatedEntries != 7 || preview.CurrentGeneration != 1 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	command := ApplyCacheRefreshCommand{OperationID: testOperationID(t, 2), Namespace: CacheOverviewProjection, Reason: "refresh projection", ExpectedGeneration: 1, PreviewDigest: preview.PreviewDigest}
	first, err := service.ApplyCacheRefresh(context.Background(), actor, command)
	if err != nil || first.CurrentGeneration != 2 || len(transaction.outbox.events) != 1 {
		t.Fatalf("first=%+v events=%d err=%v", first, len(transaction.outbox.events), err)
	}
	second, err := service.ApplyCacheRefresh(context.Background(), actor, command)
	if err != nil || second.Receipt.AuditEventID != first.Receipt.AuditEventID || repository.cache[CacheOverviewProjection].Generation != 2 || len(transaction.outbox.events) != 1 {
		t.Fatalf("replay=%+v err=%v", second, err)
	}
	tampered := command
	tampered.Reason = "different reason"
	if _, err = service.ApplyCacheRefresh(context.Background(), actor, tampered); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("idempotency error = %v", err)
	}
}

func TestTaskRetryAllowsOnlyFailedReviewedKindsAndEnforcesLimit(t *testing.T) {
	now := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	service, actor, repository, transaction := newCommandTestFixture(t, now)
	taskID := uuid.New()
	repository.retryTasks[taskID] = RetryTask{Kind: RetryUserErasure, ID: taskID, State: "failed", StableErrorCode: "admin.erasure.failed", Attempts: 2, Version: 5, UpdatedAt: now.Add(-time.Minute)}
	preview, err := service.PreviewTaskRetry(context.Background(), actor, PreviewTaskRetryInput{TaskKind: RetryUserErasure, TaskID: taskID, Reason: "retry reviewed task"})
	if err != nil || !preview.RetryAllowed {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	result, err := service.ApplyTaskRetry(context.Background(), actor, ApplyTaskRetryCommand{OperationID: testOperationID(t, 3), TaskKind: RetryUserErasure, TaskID: taskID, Reason: "retry reviewed task", ExpectedTaskVersion: 5, PreviewDigest: preview.PreviewDigest})
	if err != nil || result.Task.State != "queued" || result.Task.Version != 6 || result.Receipt.OriginalErrorCode != "admin.erasure.failed" || len(transaction.outbox.events) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}

	limitedID := uuid.New()
	repository.retryTasks[limitedID] = RetryTask{Kind: RetryUserBatch, ID: limitedID, State: "failed", StableErrorCode: "admin.batch.failed", Version: 9, UpdatedAt: now}
	repository.retries[limitedID] = MaximumManualTaskRetries
	limited, err := service.PreviewTaskRetry(context.Background(), actor, PreviewTaskRetryInput{TaskKind: RetryUserBatch, TaskID: limitedID, Reason: "retry capped task"})
	if err != nil || limited.RetryAllowed {
		t.Fatalf("limited=%+v err=%v", limited, err)
	}
	if _, err = service.ApplyTaskRetry(context.Background(), actor, ApplyTaskRetryCommand{OperationID: testOperationID(t, 4), TaskKind: RetryUserBatch, TaskID: limitedID, Reason: "retry capped task", ExpectedTaskVersion: 9, PreviewDigest: limited.PreviewDigest}); !errors.Is(err, ErrRetryLimit) {
		t.Fatalf("retry limit error = %v", err)
	}
}
