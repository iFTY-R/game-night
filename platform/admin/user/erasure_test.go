package user

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/clock"
)

func TestServiceProcessNextErasureJobAdvancesThroughCompletion(t *testing.T) {
	now := time.Date(2026, 7, 26, 17, 0, 0, 0, time.UTC)
	claimed := ErasureJob{
		ID: uuid.New(), UserID: uuid.New(), ActorAdminID: uuid.New(), OperationID: "erase-op-1",
		State: "running", Step: erasureStepRevokeCredentials, LeaseOwner: "worker:test", Version: 3,
	}
	repository := &erasureBatchRepository{claimed: claimed}
	governance := &erasureSingleGovernance{}
	service, err := NewService(Config{
		Repository: &memoryRepository{}, Jobs: repository, SingleGovernance: governance, Clock: clock.NewFake(now),
	})
	if err != nil {
		t.Fatal(err)
	}

	processed, err := service.ProcessNextErasureJob(context.Background(), "test")
	if err != nil || !processed {
		t.Fatalf("processed=%t err=%v", processed, err)
	}
	if len(governance.erasedUsers) != 1 || governance.erasedUsers[0] != claimed.UserID {
		t.Fatalf("erased users = %+v", governance.erasedUsers)
	}
	if len(repository.advances) != 3 ||
		repository.advances[0] != erasureStepEraseProfile ||
		repository.advances[1] != erasureStepEnqueueRoomCleanup ||
		repository.advances[2] != erasureStepComplete {
		t.Fatalf("advance sequence = %+v", repository.advances)
	}
	if len(repository.completions) != 1 || repository.completions[0].state != "succeeded" || repository.completions[0].errorMessageKey != "" {
		t.Fatalf("completion = %+v", repository.completions)
	}
}

func TestServiceProcessNextErasureJobMarksStableFailureWhenProfileEraseFails(t *testing.T) {
	now := time.Date(2026, 7, 26, 17, 10, 0, 0, time.UTC)
	claimed := ErasureJob{
		ID: uuid.New(), UserID: uuid.New(), ActorAdminID: uuid.New(), OperationID: "erase-op-2",
		State: "running", Step: erasureStepEraseProfile, LeaseOwner: "worker:test", Version: 4,
	}
	repository := &erasureBatchRepository{claimed: claimed}
	governance := &erasureSingleGovernance{eraseErr: ErrRepositoryUnavailable}
	service, err := NewService(Config{
		Repository: &memoryRepository{}, Jobs: repository, SingleGovernance: governance, Clock: clock.NewFake(now),
	})
	if err != nil {
		t.Fatal(err)
	}

	processed, err := service.ProcessNextErasureJob(context.Background(), "test")
	if err != nil || !processed {
		t.Fatalf("processed=%t err=%v", processed, err)
	}
	if len(repository.advances) != 0 {
		t.Fatalf("unexpected advances = %+v", repository.advances)
	}
	if len(repository.completions) != 1 || repository.completions[0].state != "failed" ||
		repository.completions[0].errorMessageKey != erasureExecutionFailedMessageKey {
		t.Fatalf("completion = %+v", repository.completions)
	}
}

type erasureBatchRepository struct {
	BatchRepository
	claimed     ErasureJob
	claimErr    error
	advances    []string
	completions []batchCompletion
}

func (repository *erasureBatchRepository) ClaimErasureJob(_ context.Context, _ string, _ time.Duration) (ErasureJob, error) {
	if repository.claimErr != nil {
		return ErasureJob{}, repository.claimErr
	}
	return repository.claimed, nil
}

func (repository *erasureBatchRepository) AdvanceErasureJob(
	_ context.Context,
	job ErasureJob,
	nextStep string,
	_ time.Time,
) (ErasureJob, error) {
	if job.ID != repository.claimed.ID {
		return ErasureJob{}, ErrNotFound
	}
	repository.advances = append(repository.advances, nextStep)
	repository.claimed.Step = nextStep
	repository.claimed.Version++
	return repository.claimed, nil
}

func (repository *erasureBatchRepository) CompleteErasureJob(
	_ context.Context,
	job ErasureJob,
	nextState, errorMessageKey string,
	_ time.Time,
) (ErasureJob, error) {
	if job.ID != repository.claimed.ID {
		return ErasureJob{}, ErrNotFound
	}
	repository.completions = append(repository.completions, batchCompletion{state: nextState, errorMessageKey: errorMessageKey})
	repository.claimed.State = nextState
	repository.claimed.ErrorMessageKey = errorMessageKey
	return repository.claimed, nil
}

type erasureSingleGovernance struct {
	SingleUserGovernanceExecutor
	erasedUsers []uuid.UUID
	eraseErr    error
}

func (governance *erasureSingleGovernance) EraseUserProfile(_ context.Context, userID uuid.UUID) error {
	if governance.eraseErr != nil {
		return governance.eraseErr
	}
	governance.erasedUsers = append(governance.erasedUsers, userID)
	return nil
}
