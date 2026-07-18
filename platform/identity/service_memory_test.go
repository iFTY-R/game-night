package identity

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/secretresult"
)

type memoryIdentityStorage struct {
	mu         sync.Mutex
	users      map[uuid.UUID]User
	claims     map[string]UsernameClaim
	devices    map[uuid.UUID]DeviceCredential
	recoveries map[uuid.UUID]RecoveryCredential
	challenges map[string]Challenge
	results    map[secretresult.Key]secretresult.Result
	writeCount int
}

func newMemoryIdentityStorage() *memoryIdentityStorage {
	return &memoryIdentityStorage{
		users: make(map[uuid.UUID]User), claims: make(map[string]UsernameClaim),
		devices: make(map[uuid.UUID]DeviceCredential), recoveries: make(map[uuid.UUID]RecoveryCredential),
		challenges: make(map[string]Challenge), results: make(map[secretresult.Key]secretresult.Result),
	}
}

type memoryIdentityUnitOfWork struct{ storage *memoryIdentityStorage }

func (unitOfWork *memoryIdentityUnitOfWork) Run(ctx context.Context, work ChallengeTransactionWork) error {
	unitOfWork.storage.mu.Lock()
	defer unitOfWork.storage.mu.Unlock()
	transaction := memoryIdentityTransaction{storage: unitOfWork.storage}
	return work(ctx, transaction)
}

type memoryIdentityTransaction struct{ storage *memoryIdentityStorage }

func (transaction memoryIdentityTransaction) Challenges() ChallengeRepository {
	return memoryChallengeRepository{storage: transaction.storage}
}
func (transaction memoryIdentityTransaction) SecretResults() secretresult.Repository {
	return memoryResultRepository{storage: transaction.storage}
}
func (transaction memoryIdentityTransaction) Users() UserRepository {
	return memoryUserRepository{storage: transaction.storage}
}
func (transaction memoryIdentityTransaction) UsernameClaims() UsernameClaimRepository {
	return memoryClaimRepository{storage: transaction.storage}
}
func (transaction memoryIdentityTransaction) Devices() DeviceRepository {
	return memoryDeviceRepository{storage: transaction.storage}
}
func (transaction memoryIdentityTransaction) RecoveryCredentials() RecoveryCredentialRepository {
	return memoryRecoveryRepository{storage: transaction.storage}
}

type memoryChallengeRepository struct{ storage *memoryIdentityStorage }

func (repository memoryChallengeRepository) Insert(_ context.Context, record Challenge) error {
	key := record.Snapshot().Selector.Value()
	if _, exists := repository.storage.challenges[key]; exists {
		return challenge.ErrRepositoryUnavailable
	}
	repository.storage.challenges[key] = record
	repository.storage.writeCount++
	return nil
}
func (repository memoryChallengeRepository) GetForUpdate(_ context.Context, selector identifier.Selector) (Challenge, error) {
	record, exists := repository.storage.challenges[selector.Value()]
	if !exists {
		return Challenge{}, challenge.ErrNotFound
	}
	return record, nil
}
func (repository memoryChallengeRepository) RecordFailureCAS(_ context.Context, record Challenge, at time.Time) (Challenge, error) {
	updated, err := record.RecordFailure(at)
	if err == nil {
		repository.storage.challenges[record.Snapshot().Selector.Value()] = updated
		repository.storage.writeCount++
	}
	return updated, err
}
func (repository memoryChallengeRepository) ConsumeCAS(_ context.Context, record Challenge) (Challenge, error) {
	repository.storage.challenges[record.Snapshot().Selector.Value()] = record
	repository.storage.writeCount++
	return record, nil
}

type memoryResultRepository struct{ storage *memoryIdentityStorage }

func (repository memoryResultRepository) GetByOperationForUpdate(_ context.Context, key secretresult.Key) (secretresult.Result, error) {
	result, exists := repository.storage.results[key]
	if !exists {
		return secretresult.Result{}, secretresult.ErrNotFound
	}
	return result, nil
}
func (repository memoryResultRepository) InsertAvailable(_ context.Context, result secretresult.Result) (secretresult.Result, error) {
	key := result.Snapshot().Binding.Key
	if existing, exists := repository.storage.results[key]; exists {
		_, err := existing.Resolve(result.Snapshot().Binding, result.Snapshot().CompletedAt)
		return existing, err
	}
	repository.storage.results[key] = result
	repository.storage.writeCount++
	return result, nil
}
func (repository memoryResultRepository) ConfirmCAS(_ context.Context, confirmation secretresult.Confirmation) (secretresult.Result, error) {
	current, exists := repository.storage.results[confirmation.Binding.Key]
	if !exists {
		return secretresult.Result{}, secretresult.ErrNotFound
	}
	updated, err := current.Confirm(confirmation.ConfirmedAt)
	if err == nil {
		repository.storage.results[confirmation.Binding.Key] = updated
		repository.storage.writeCount++
	}
	return updated, err
}
func (repository memoryResultRepository) ExpireCAS(_ context.Context, result secretresult.Result, at time.Time) (secretresult.Result, error) {
	updated, err := result.Expire(at)
	if err == nil {
		repository.storage.results[result.Snapshot().Binding.Key] = updated
		repository.storage.writeCount++
	}
	return updated, err
}

type memoryUserRepository struct{ storage *memoryIdentityStorage }

func (repository memoryUserRepository) Insert(_ context.Context, user User) (User, error) {
	id := user.Snapshot().ID
	if _, exists := repository.storage.users[id]; exists {
		return User{}, ErrIdentityConcurrentTransition
	}
	repository.storage.users[id] = user
	repository.storage.writeCount++
	return user, nil
}
func (repository memoryUserRepository) GetByID(_ context.Context, id uuid.UUID) (User, error) {
	user, exists := repository.storage.users[id]
	if !exists {
		return User{}, ErrUserNotFound
	}
	return user, nil
}
func (repository memoryUserRepository) GetForUpdate(ctx context.Context, id uuid.UUID) (User, error) {
	return repository.GetByID(ctx, id)
}
func (repository memoryUserRepository) CompleteOnboardingCAS(_ context.Context, current, next User) (User, error) {
	stored := repository.storage.users[current.Snapshot().ID]
	username, err := identifier.ParseUsername(next.Snapshot().Username)
	if err != nil {
		return User{}, ErrInvalidUserInput
	}
	planned, err := current.CompleteOnboarding(username, next.Snapshot().UsernameChangedAt)
	if err != nil {
		return User{}, err
	}
	if stored.Snapshot() != current.Snapshot() || planned.Snapshot() != next.Snapshot() {
		return User{}, ErrIdentityConcurrentTransition
	}
	repository.storage.users[next.Snapshot().ID] = next
	repository.storage.writeCount++
	return next, nil
}
func (repository memoryUserRepository) ChangeUsernameCAS(_ context.Context, current, next User) (User, error) {
	stored := repository.storage.users[current.Snapshot().ID]
	username, err := identifier.ParseUsername(next.Snapshot().Username)
	if err != nil {
		return User{}, ErrInvalidUserInput
	}
	plan, err := current.PlanUsernameChange(username, next.Snapshot().UsernameChangedAt)
	if err != nil {
		return User{}, err
	}
	if stored.Snapshot() != current.Snapshot() || plan.Next.Snapshot() != next.Snapshot() {
		return User{}, ErrIdentityConcurrentTransition
	}
	repository.storage.users[next.Snapshot().ID] = next
	repository.storage.writeCount++
	return next, nil
}

type memoryClaimRepository struct{ storage *memoryIdentityStorage }

func (repository memoryClaimRepository) Claim(_ context.Context, claim UsernameClaim, at time.Time) (UsernameClaim, error) {
	key := claim.Snapshot().UsernameKey
	if existing, exists := repository.storage.claims[key]; exists && !existing.AvailableAt(at) {
		return UsernameClaim{}, ErrUsernameUnavailable
	}
	repository.storage.claims[key] = claim
	repository.storage.writeCount++
	return claim, nil
}
func (repository memoryClaimRepository) GetForUpdate(_ context.Context, key string) (UsernameClaim, error) {
	claim, exists := repository.storage.claims[key]
	if !exists {
		return UsernameClaim{}, ErrIdentityIntegrity
	}
	return claim, nil
}
func (repository memoryClaimRepository) ReserveCAS(_ context.Context, current, next UsernameClaim) (UsernameClaim, error) {
	stored, exists := repository.storage.claims[current.Snapshot().UsernameKey]
	if !exists || stored.Snapshot() != current.Snapshot() {
		return UsernameClaim{}, ErrIdentityConcurrentTransition
	}
	repository.storage.claims[next.Snapshot().UsernameKey] = next
	repository.storage.writeCount++
	return next, nil
}

type memoryDeviceRepository struct{ storage *memoryIdentityStorage }

func (repository memoryDeviceRepository) Insert(_ context.Context, device DeviceCredential) (DeviceCredential, error) {
	id := device.Snapshot().CredentialID
	if _, exists := repository.storage.devices[id]; exists {
		return DeviceCredential{}, ErrIdentityConcurrentTransition
	}
	repository.storage.devices[id] = device
	repository.storage.writeCount++
	return device, nil
}
func (repository memoryDeviceRepository) GetIdentityForUpdate(_ context.Context, id uuid.UUID) (User, DeviceCredential, error) {
	device, exists := repository.storage.devices[id]
	if !exists {
		return User{}, DeviceCredential{}, ErrDeviceAuthentication
	}
	user, exists := repository.storage.users[device.Snapshot().UserID]
	if !exists {
		return User{}, DeviceCredential{}, ErrIdentityIntegrity
	}
	return user, device, nil
}
func (repository memoryDeviceRepository) TouchCAS(_ context.Context, current, next DeviceCredential) (DeviceCredential, error) {
	return repository.replace(current, next)
}
func (repository memoryDeviceRepository) RotateCAS(_ context.Context, current, next DeviceCredential) (DeviceCredential, error) {
	return repository.replace(current, next)
}
func (repository memoryDeviceRepository) replace(current, next DeviceCredential) (DeviceCredential, error) {
	id := current.Snapshot().CredentialID
	stored, exists := repository.storage.devices[id]
	if !exists || stored.Snapshot().Generation != current.Snapshot().Generation {
		return DeviceCredential{}, ErrIdentityConcurrentTransition
	}
	repository.storage.devices[id] = next
	repository.storage.writeCount++
	return next, nil
}

type memoryRecoveryRepository struct{ storage *memoryIdentityStorage }

func (repository memoryRecoveryRepository) Insert(_ context.Context, credential RecoveryCredential) (RecoveryCredential, error) {
	for _, existing := range repository.storage.recoveries {
		if existing.Snapshot().UserID == credential.Snapshot().UserID && existing.Snapshot().Status == RecoveryCredentialActive {
			return RecoveryCredential{}, ErrIdentityConcurrentTransition
		}
	}
	repository.storage.recoveries[credential.Snapshot().ID] = credential
	repository.storage.writeCount++
	return credential, nil
}
