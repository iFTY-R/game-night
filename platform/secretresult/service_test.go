package secretresult

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/clock"
)

func TestServiceResolveReplayOpenAndConfirm(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	fakeClock := clock.NewFake(now)
	cipher := testEnvelopeCipher(t, now)
	service, err := NewService(cipher, fakeClock)
	if err != nil {
		t.Fatal(err)
	}
	binding := testBinding(t)
	prepared, err := service.PrepareAvailable(uuid.New(), binding, []byte("one-time-secret"), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	repository := newMemoryRepository()
	if _, err := repository.InsertAvailable(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	unitOfWork := memoryUnitOfWork{repository: repository}
	emptyResolution, err := service.Resolve(context.Background(), memoryUnitOfWork{repository: newMemoryRepository()}, binding)
	if err != nil || emptyResolution.Kind != ExecuteNew {
		t.Fatalf("empty resolution = %+v, err=%v", emptyResolution, err)
	}

	resolution, err := service.Resolve(context.Background(), unitOfWork, binding)
	if err != nil || resolution.Kind != ReplayAvailable {
		t.Fatalf("resolution = %+v, err=%v", resolution, err)
	}
	if _, err := service.Open(context.Background(), unitOfWork, prepared.Snapshot().ID, binding, nil); !errors.Is(err, ErrReplayUnauthorized) {
		t.Fatalf("open without replay capability error = %v", err)
	}
	capability := testReplayCapability(t, now, prepared)
	wrongBinding := binding
	wrongBinding.Key.ActorID = uuid.New()
	if _, err := service.Open(
		context.Background(), unitOfWork, prepared.Snapshot().ID, wrongBinding, capability,
	); !errors.Is(err, ErrReplayUnauthorized) {
		t.Fatalf("open with wrong operation binding error = %v", err)
	}
	plaintext, err := service.Open(context.Background(), unitOfWork, prepared.Snapshot().ID, binding, capability)
	if err != nil || string(plaintext) != "one-time-secret" {
		t.Fatalf("plaintext = %q, err=%v", plaintext, err)
	}
	if _, err := service.Confirm(
		context.Background(), unitOfWork, prepared.Snapshot().ID, binding, nil,
	); !errors.Is(err, ErrReplayUnauthorized) {
		t.Fatalf("confirm without replay capability error = %v", err)
	}
	confirmed, err := service.Confirm(context.Background(), unitOfWork, prepared.Snapshot().ID, binding, capability)
	if err != nil || confirmed.Snapshot().Status != StatusConfirmed {
		t.Fatalf("confirmed = %+v, err=%v", confirmed.Snapshot(), err)
	}
	if _, err := service.Confirm(context.Background(), unitOfWork, prepared.Snapshot().ID, binding, capability); err != nil {
		t.Fatalf("repeated confirm must succeed: %v", err)
	}
	resolution, err = service.Resolve(context.Background(), unitOfWork, binding)
	if err != nil || resolution.Kind != ReplayUnavailable {
		t.Fatalf("terminal resolution = %+v, err=%v", resolution, err)
	}
	if _, err := service.Open(context.Background(), unitOfWork, prepared.Snapshot().ID, binding, capability); !errors.Is(err, ErrSecretNoLongerAvailable) {
		t.Fatalf("terminal open error = %v", err)
	}
	conflictingBinding := binding
	conflictingBinding.RequestDigest[0] ^= 0xff
	if _, err := service.Resolve(context.Background(), unitOfWork, conflictingBinding); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("confirmed tombstone digest conflict error = %v", err)
	}
}

func TestServiceExpiresDueResultWithoutLosingTombstone(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	fakeClock := clock.NewFake(now)
	service, err := NewService(testEnvelopeCipher(t, now), fakeClock)
	if err != nil {
		t.Fatal(err)
	}
	binding := testBinding(t)
	prepared, err := service.PrepareAvailable(uuid.New(), binding, []byte("secret"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	repository := newMemoryRepository()
	if _, err := repository.InsertAvailable(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := fakeClock.Advance(time.Minute); err != nil {
		t.Fatal(err)
	}
	expired, err := service.Expire(context.Background(), memoryUnitOfWork{repository: repository}, binding.Key)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := expired.Snapshot()
	if snapshot.Status != StatusExpired || !snapshot.Payload.Empty() || snapshot.Payload.KeyVersion == 0 {
		t.Fatalf("invalid expired tombstone: %+v", snapshot)
	}
	resolution, err := service.Resolve(context.Background(), memoryUnitOfWork{repository: repository}, binding)
	if err != nil || resolution.Kind != ReplayUnavailable {
		t.Fatalf("expired resolution = %+v, err=%v", resolution, err)
	}
}

type memoryRepository struct {
	mu      sync.Mutex
	results map[Key]Result
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{results: make(map[Key]Result)}
}

func (repository *memoryRepository) GetByOperationForUpdate(_ context.Context, key Key) (Result, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	result, exists := repository.results[key]
	if !exists {
		return Result{}, ErrNotFound
	}
	return result, nil
}

type memoryUnitOfWork struct {
	repository Repository
}

func (unitOfWork memoryUnitOfWork) Run(ctx context.Context, work TransactionWork) error {
	return work(ctx, unitOfWork.repository)
}

func (repository *memoryRepository) InsertAvailable(_ context.Context, result Result) (Result, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	key := result.Snapshot().Binding.Key
	if existing, exists := repository.results[key]; exists {
		return existing, nil
	}
	repository.results[key] = result
	return result, nil
}

func (repository *memoryRepository) ConfirmCAS(_ context.Context, confirmation Confirmation) (Result, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	current, exists := repository.results[confirmation.Binding.Key]
	if !exists || current.Snapshot().ID != confirmation.ResultID {
		return Result{}, ErrConcurrentTransition
	}
	confirmed, err := current.Confirm(confirmation.ConfirmedAt)
	if err != nil {
		return Result{}, err
	}
	repository.results[confirmation.Binding.Key] = confirmed
	return confirmed, nil
}

func (repository *memoryRepository) ExpireCAS(_ context.Context, result Result, expiredAt time.Time) (Result, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	resultID := result.Snapshot().ID
	for key, current := range repository.results {
		if current.Snapshot().ID != resultID {
			continue
		}
		expired, err := current.Expire(expiredAt)
		if err != nil {
			return Result{}, err
		}
		repository.results[key] = expired
		return expired, nil
	}
	return Result{}, ErrNotFound
}
