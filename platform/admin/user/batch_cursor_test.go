package user

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	admin "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/clock"
)

func TestBatchJobCursorBindsFilterAndOrdering(t *testing.T) {
	now := time.Date(2026, 7, 26, 16, 0, 0, 0, time.UTC)
	codec, err := NewCursorCodec(newTestHMACKeyring(t, now))
	if err != nil {
		t.Fatal(err)
	}
	query := BatchJobListQuery{
		States: []string{"failed", "queued"}, Commands: []BatchCommand{BatchCommandSuspend},
		SortField: BatchJobSortUpdatedAt, Direction: SortDescending,
	}
	if batchJobQueryDigest(BatchJobListQuery{}) != batchJobQueryDigest(BatchJobListQuery{States: []string{}, Commands: []BatchCommand{}}) {
		t.Fatal("empty task filters must have one canonical cursor digest")
	}
	digest := batchJobQueryDigest(query)
	position := BatchJobListPosition{SortTime: now.Add(-time.Minute), BatchJobID: uuid.New()}
	token, err := codec.EncodeBatchJob(digest, query.SortField, query.Direction, position)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := codec.DecodeBatchJob(token, digest, query.SortField, query.Direction)
	if err != nil || decoded.BatchJobID != position.BatchJobID || !decoded.SortTime.Equal(position.SortTime) {
		t.Fatalf("decoded batch job cursor = %+v err=%v", decoded, err)
	}
	if _, err = codec.DecodeBatchJob(token, sha256.Sum256([]byte("other-filter")), query.SortField, query.Direction); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cross-filter cursor error = %v", err)
	}
	if _, err = codec.DecodeBatchJob(token, digest, query.SortField, SortAscending); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cross-sort cursor error = %v", err)
	}
}

func TestServiceListBatchJobsUsesVerifiedKeysetPosition(t *testing.T) {
	now := time.Date(2026, 7, 26, 16, 30, 0, 0, time.UTC)
	firstJob := BatchJob{ID: uuid.New(), CreatedAt: now.Add(-2 * time.Minute), UpdatedAt: now.Add(-time.Minute)}
	secondJob := BatchJob{ID: uuid.New(), CreatedAt: now.Add(-3 * time.Minute), UpdatedAt: now.Add(-2 * time.Minute)}
	jobs := &pagedBatchRepository{firstJobs: []BatchJob{firstJob}, laterJobs: []BatchJob{secondJob}}
	codec, err := NewCursorCodec(newTestHMACKeyring(t, now))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Config{Repository: &memoryRepository{}, Jobs: jobs, Cursor: codec, Clock: clock.NewFake(now)})
	if err != nil {
		t.Fatal(err)
	}
	actor := newTestActor(t, now, admin.PermissionUsersGovern)
	query := BatchJobListQuery{PageSize: 1, SortField: BatchJobSortCreatedAt, Direction: SortDescending}

	first, token, _, err := service.ListBatchUserOperations(context.Background(), actor, query)
	if err != nil || len(first) != 1 || first[0].ID != firstJob.ID || token == "" {
		t.Fatalf("first batch page = %+v token=%q err=%v", first, token, err)
	}
	second, _, _, err := service.ListBatchUserOperations(context.Background(), actor, BatchJobListQuery{
		PageSize: 1, SortField: BatchJobSortCreatedAt, Direction: SortDescending, PageToken: token,
	})
	if err != nil || len(second) != 1 || second[0].ID != secondJob.ID {
		t.Fatalf("second batch page = %+v err=%v", second, err)
	}
	if len(jobs.jobQueries) != 2 || jobs.jobQueries[1].After.BatchJobID != firstJob.ID || !jobs.jobQueries[1].After.SortTime.Equal(firstJob.CreatedAt) {
		t.Fatalf("repository did not receive decoded position: %+v", jobs.jobQueries)
	}
	if _, _, _, err = service.ListBatchUserOperations(context.Background(), actor, BatchJobListQuery{
		States: []string{"failed"}, PageSize: 1, SortField: BatchJobSortCreatedAt, Direction: SortDescending, PageToken: token,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cross-filter task page token error = %v", err)
	}
}

type pagedBatchRepository struct {
	BatchRepository
	firstJobs  []BatchJob
	laterJobs  []BatchJob
	jobQueries []BatchJobListQuery
}

func (repository *pagedBatchRepository) ListBatchJobs(_ context.Context, query BatchJobListQuery) ([]BatchJob, error) {
	repository.jobQueries = append(repository.jobQueries, query)
	if query.After.BatchJobID == uuid.Nil {
		return repository.firstJobs, nil
	}
	return repository.laterJobs, nil
}
