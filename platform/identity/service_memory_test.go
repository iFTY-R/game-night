package identity

import (
	"context"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/secretresult"
)

type memoryIdentityStorage struct {
	mu           sync.Mutex
	users        map[uuid.UUID]User
	claims       map[string]UsernameClaim
	devices      map[uuid.UUID]DeviceCredential
	recoveries   map[uuid.UUID]RecoveryCredential
	attempts     map[string]RecoveryAttempt
	assisted     map[uuid.UUID]AssistedRecoveryGrant
	challenges   map[string]Challenge
	results      map[secretresult.Key]secretresult.Result
	auditHead    audit.Head
	auditEvents  []audit.SignedEvent
	checkpoints  []audit.Checkpoint
	outboxEvents []outbox.Event
	failOutbox   error
	writeCount   int
}

func newMemoryIdentityStorage() *memoryIdentityStorage {
	genesis, err := audit.RestoreHead(audit.HeadSnapshot{
		ChainID: audit.ChainAdmin, Hash: audit.GenesisHash, UpdatedAt: time.Unix(0, 0).UTC(),
	})
	if err != nil {
		panic(err)
	}
	return &memoryIdentityStorage{
		users: make(map[uuid.UUID]User), claims: make(map[string]UsernameClaim),
		devices: make(map[uuid.UUID]DeviceCredential), recoveries: make(map[uuid.UUID]RecoveryCredential),
		attempts: make(map[string]RecoveryAttempt), assisted: make(map[uuid.UUID]AssistedRecoveryGrant),
		challenges: make(map[string]Challenge), results: make(map[secretresult.Key]secretresult.Result),
		auditHead: genesis,
	}
}

type memoryIdentityUnitOfWork struct{ storage *memoryIdentityStorage }

func (unitOfWork *memoryIdentityUnitOfWork) Run(ctx context.Context, work ChallengeTransactionWork) error {
	unitOfWork.storage.mu.Lock()
	defer unitOfWork.storage.mu.Unlock()
	working := unitOfWork.storage.clone()
	if err := work(ctx, memoryIdentityTransaction{storage: working}); err != nil {
		return err
	}
	unitOfWork.storage.commit(working)
	return nil
}

func (unitOfWork *memoryIdentityUnitOfWork) RunIdentity(ctx context.Context, work IdentityTransactionWork) error {
	unitOfWork.storage.mu.Lock()
	defer unitOfWork.storage.mu.Unlock()
	working := unitOfWork.storage.clone()
	if err := work(ctx, memoryIdentityTransaction{storage: working}); err != nil {
		return err
	}
	unitOfWork.storage.commit(working)
	return nil
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
func (transaction memoryIdentityTransaction) RecoveryAttempts() RecoveryAttemptRepository {
	return memoryRecoveryAttemptRepository{storage: transaction.storage}
}
func (transaction memoryIdentityTransaction) AssistedRecoveryGrants() AssistedRecoveryGrantRepository {
	return memoryAssistedRecoveryRepository{storage: transaction.storage}
}
func (transaction memoryIdentityTransaction) Audit() audit.Repository {
	return memoryAuditRepository{storage: transaction.storage}
}
func (transaction memoryIdentityTransaction) AuditCheckpoints() audit.CheckpointRepository {
	return memoryAuditCheckpointRepository{storage: transaction.storage}
}
func (transaction memoryIdentityTransaction) OutboxEvents() outbox.EventRepository {
	return memoryOutboxEventRepository{storage: transaction.storage}
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

func (repository memoryResultRepository) GetByIDForUpdate(_ context.Context, resultID uuid.UUID) (secretresult.Result, error) {
	for _, result := range repository.storage.results {
		if result.Snapshot().ID == resultID {
			return result, nil
		}
	}
	return secretresult.Result{}, secretresult.ErrNotFound
}

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
func (repository memoryDeviceRepository) GetForUpdate(_ context.Context, id uuid.UUID) (DeviceCredential, error) {
	device, exists := repository.storage.devices[id]
	if !exists {
		return DeviceCredential{}, ErrDeviceAuthentication
	}
	return device, nil
}
func (repository memoryDeviceRepository) List(_ context.Context, request DeviceListRequest) ([]DeviceSummary, error) {
	devices := make([]DeviceSummary, 0)
	for _, device := range repository.storage.devices {
		snapshot := device.Snapshot()
		if snapshot.UserID != request.UserID || (!request.IncludeRevoked && !snapshot.RevokedAt.IsZero()) ||
			snapshot.CreatedAt.Before(request.After.CreatedAt) ||
			(snapshot.CreatedAt.Equal(request.After.CreatedAt) && snapshot.CredentialID.String() <= request.After.CredentialID.String()) {
			continue
		}
		summary, err := RestoreDeviceSummary(DeviceSummarySnapshot{
			CredentialID: snapshot.CredentialID, UserID: snapshot.UserID, Label: snapshot.Label,
			CreatedAt: snapshot.CreatedAt, LastSeenAt: snapshot.LastSeenAt,
			IdleExpiresAt: snapshot.IdleExpiresAt, AbsoluteExpiresAt: snapshot.AbsoluteExpiresAt,
			RevokedAt: snapshot.RevokedAt,
		}, request.ListedAt)
		if err != nil {
			return nil, err
		}
		devices = append(devices, summary)
	}
	sort.Slice(devices, func(left, right int) bool {
		if devices[left].CreatedAt.Equal(devices[right].CreatedAt) {
			return devices[left].CredentialID.String() < devices[right].CredentialID.String()
		}
		return devices[left].CreatedAt.Before(devices[right].CreatedAt)
	})
	if len(devices) > int(request.PageSize) {
		devices = devices[:request.PageSize]
	}
	return devices, nil
}
func (repository memoryDeviceRepository) TouchCAS(_ context.Context, current, next DeviceCredential) (DeviceCredential, error) {
	return repository.replace(current, next)
}
func (repository memoryDeviceRepository) RotateCAS(_ context.Context, current, next DeviceCredential) (DeviceCredential, error) {
	return repository.replace(current, next)
}
func (repository memoryDeviceRepository) RevokeCAS(_ context.Context, current, next DeviceCredential) (DeviceCredential, error) {
	return repository.replace(current, next)
}
func (repository memoryDeviceRepository) RevokeOtherActiveForRecovery(
	_ context.Context,
	userID, keepID uuid.UUID,
	at time.Time,
) ([]DeviceSummary, error) {
	ids := make([]uuid.UUID, 0)
	for id, device := range repository.storage.devices {
		if id != keepID && device.Snapshot().UserID == userID && device.State(at) == DeviceStateActive {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left].String() < ids[right].String() })
	revoked := make([]DeviceSummary, 0, len(ids))
	for _, id := range ids {
		current := repository.storage.devices[id]
		next, err := current.Revoke(DeviceRevokeRecovery, at)
		if err != nil {
			return nil, err
		}
		repository.storage.devices[id] = next
		repository.storage.writeCount++
		snapshot := next.Snapshot()
		summary, err := RestoreDeviceSummary(DeviceSummarySnapshot{
			CredentialID: snapshot.CredentialID, UserID: snapshot.UserID, Label: snapshot.Label,
			CreatedAt: snapshot.CreatedAt, LastSeenAt: snapshot.LastSeenAt,
			IdleExpiresAt: snapshot.IdleExpiresAt, AbsoluteExpiresAt: snapshot.AbsoluteExpiresAt,
			RevokedAt: snapshot.RevokedAt,
		}, at)
		if err != nil {
			return nil, err
		}
		revoked = append(revoked, summary)
	}
	return revoked, nil
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

func (repository memoryRecoveryRepository) GetBySelector(_ context.Context, selector identifier.Selector) (RecoveryCredential, error) {
	for _, credential := range repository.storage.recoveries {
		if credential.Snapshot().Selector == selector {
			return credential, nil
		}
	}
	return RecoveryCredential{}, ErrRecoveryInvalid
}
func (repository memoryRecoveryRepository) GetForUpdate(
	_ context.Context,
	id, userID uuid.UUID,
	version uint64,
) (RecoveryCredential, error) {
	credential, exists := repository.storage.recoveries[id]
	if !exists || credential.Snapshot().UserID != userID || credential.Snapshot().Version != version {
		return RecoveryCredential{}, ErrRecoveryInvalid
	}
	return credential, nil
}
func (repository memoryRecoveryRepository) GetActiveForUserForUpdate(_ context.Context, userID uuid.UUID) (RecoveryCredential, error) {
	for _, credential := range repository.storage.recoveries {
		if credential.Snapshot().UserID == userID && credential.Snapshot().Status == RecoveryCredentialActive {
			return credential, nil
		}
	}
	return RecoveryCredential{}, ErrRecoveryInvalid
}
func (repository memoryRecoveryRepository) ConsumeCAS(_ context.Context, current, next RecoveryCredential) (RecoveryCredential, error) {
	return repository.replace(current, next)
}
func (repository memoryRecoveryRepository) RevokeCAS(_ context.Context, current, next RecoveryCredential) (RecoveryCredential, error) {
	return repository.replace(current, next)
}
func (repository memoryRecoveryRepository) replace(current, next RecoveryCredential) (RecoveryCredential, error) {
	id := current.Snapshot().ID
	stored, exists := repository.storage.recoveries[id]
	if !exists || stored.Snapshot() != current.Snapshot() {
		return RecoveryCredential{}, ErrRecoveryConcurrentTransition
	}
	repository.storage.recoveries[id] = next
	repository.storage.writeCount++
	return next, nil
}

type memoryRecoveryAttemptRepository struct{ storage *memoryIdentityStorage }

func (repository memoryRecoveryAttemptRepository) Insert(_ context.Context, attempt RecoveryAttempt) (RecoveryAttempt, error) {
	key := attempt.Snapshot().Selector.Value()
	if _, exists := repository.storage.attempts[key]; exists {
		return RecoveryAttempt{}, ErrRecoveryConcurrentTransition
	}
	repository.storage.attempts[key] = attempt
	repository.storage.writeCount++
	return attempt, nil
}
func (repository memoryRecoveryAttemptRepository) GetBySelector(_ context.Context, selector identifier.Selector) (RecoveryAttempt, error) {
	attempt, exists := repository.storage.attempts[selector.Value()]
	if !exists {
		return RecoveryAttempt{}, ErrRecoveryInvalid
	}
	return attempt, nil
}
func (repository memoryRecoveryAttemptRepository) GetForUpdate(ctx context.Context, selector identifier.Selector) (RecoveryAttempt, error) {
	return repository.GetBySelector(ctx, selector)
}
func (repository memoryRecoveryAttemptRepository) RecordFailureCAS(_ context.Context, current, next RecoveryAttempt) (RecoveryAttempt, error) {
	return repository.replace(current, next)
}
func (repository memoryRecoveryAttemptRepository) ConsumeCAS(_ context.Context, current, next RecoveryAttempt) (RecoveryAttempt, error) {
	return repository.replace(current, next)
}
func (repository memoryRecoveryAttemptRepository) RevokeCAS(_ context.Context, current, next RecoveryAttempt) (RecoveryAttempt, error) {
	return repository.replace(current, next)
}
func (repository memoryRecoveryAttemptRepository) replace(current, next RecoveryAttempt) (RecoveryAttempt, error) {
	key := current.Snapshot().Selector.Value()
	stored, exists := repository.storage.attempts[key]
	if !exists || !reflect.DeepEqual(stored.Snapshot(), current.Snapshot()) {
		return RecoveryAttempt{}, ErrRecoveryConcurrentTransition
	}
	repository.storage.attempts[key] = next
	repository.storage.writeCount++
	return next, nil
}

type memoryAssistedRecoveryRepository struct{ storage *memoryIdentityStorage }

func (repository memoryAssistedRecoveryRepository) GetBySelector(_ context.Context, selector identifier.Selector) (AssistedRecoveryGrant, error) {
	for _, grant := range repository.storage.assisted {
		if grant.Snapshot().Selector == selector {
			return grant, nil
		}
	}
	return AssistedRecoveryGrant{}, ErrRecoveryInvalid
}
func (repository memoryAssistedRecoveryRepository) GetForUpdate(_ context.Context, id, userID uuid.UUID) (AssistedRecoveryGrant, error) {
	grant, exists := repository.storage.assisted[id]
	if !exists || grant.Snapshot().UserID != userID {
		return AssistedRecoveryGrant{}, ErrRecoveryInvalid
	}
	return grant, nil
}
func (repository memoryAssistedRecoveryRepository) RecordFailureCAS(_ context.Context, current, next AssistedRecoveryGrant) (AssistedRecoveryGrant, error) {
	return repository.replace(current, next)
}
func (repository memoryAssistedRecoveryRepository) ConsumeCAS(_ context.Context, current, next AssistedRecoveryGrant) (AssistedRecoveryGrant, error) {
	return repository.replace(current, next)
}
func (repository memoryAssistedRecoveryRepository) replace(current, next AssistedRecoveryGrant) (AssistedRecoveryGrant, error) {
	id := current.Snapshot().ID
	stored, exists := repository.storage.assisted[id]
	if !exists || !reflect.DeepEqual(stored.Snapshot(), current.Snapshot()) {
		return AssistedRecoveryGrant{}, ErrRecoveryConcurrentTransition
	}
	repository.storage.assisted[id] = next
	repository.storage.writeCount++
	return next, nil
}
func (repository memoryAssistedRecoveryRepository) RevokeActiveForUser(
	_ context.Context,
	userID, exceptID uuid.UUID,
	at time.Time,
) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, 0)
	for id, grant := range repository.storage.assisted {
		if id != exceptID && grant.Snapshot().UserID == userID && grant.State(at) == AssistedRecoveryGrantActive {
			next, err := grant.Revoke(at)
			if err != nil {
				return nil, err
			}
			repository.storage.assisted[id] = next
			repository.storage.writeCount++
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left].String() < ids[right].String() })
	return ids, nil
}

type memoryAuditRepository struct{ storage *memoryIdentityStorage }

func (repository memoryAuditRepository) ReadHead(_ context.Context, chainID audit.ChainID) (audit.Head, error) {
	if chainID != repository.storage.auditHead.ChainID() {
		return audit.Head{}, audit.ErrNotFound
	}
	return repository.storage.auditHead, nil
}
func (repository memoryAuditRepository) AppendEvent(_ context.Context, request audit.AppendRequest) (audit.Head, error) {
	if request.ExpectedHead.Snapshot() != repository.storage.auditHead.Snapshot() {
		return audit.Head{}, audit.ErrHeadConflict
	}
	next, err := request.Event.NextHead()
	if err != nil {
		return audit.Head{}, err
	}
	repository.storage.auditEvents = append(repository.storage.auditEvents, request.Event)
	repository.storage.auditHead = next
	repository.storage.writeCount++
	return next, nil
}
func (repository memoryAuditRepository) List(_ context.Context, request audit.ListRequest) ([]audit.SignedEvent, error) {
	result := make([]audit.SignedEvent, 0)
	for _, event := range repository.storage.auditEvents {
		snapshot := event.Snapshot().Event
		if snapshot.ChainID == request.ChainID && snapshot.Sequence > request.AfterSequence {
			result = append(result, event)
		}
		if len(result) == int(request.PageSize) {
			break
		}
	}
	return result, nil
}

type memoryAuditCheckpointRepository struct{ storage *memoryIdentityStorage }

func (repository memoryAuditCheckpointRepository) AppendPendingCheckpoint(_ context.Context, checkpoint audit.Checkpoint) error {
	repository.storage.checkpoints = append(repository.storage.checkpoints, checkpoint)
	repository.storage.writeCount++
	return nil
}
func (repository memoryAuditCheckpointRepository) ReadCheckpointProgress(_ context.Context, chainID audit.ChainID) (audit.CheckpointProgress, error) {
	if chainID != audit.ChainAdmin {
		return audit.CheckpointProgress{}, audit.ErrNotFound
	}
	progress := audit.CheckpointProgress{ChainID: chainID}
	if len(repository.storage.checkpoints) > 0 {
		last := repository.storage.checkpoints[len(repository.storage.checkpoints)-1].Snapshot()
		progress.AcknowledgedSequence = last.Sequence
		progress.AcknowledgedAt = last.CreatedAt
	}
	if progress.AcknowledgedSequence < repository.storage.auditHead.Sequence() && len(repository.storage.auditEvents) > 0 {
		progress.UncheckpointedSince = repository.storage.auditEvents[progress.AcknowledgedSequence].Snapshot().Event.OccurredAt
	}
	return progress, nil
}

type memoryOutboxEventRepository struct{ storage *memoryIdentityStorage }

func (repository memoryOutboxEventRepository) Insert(_ context.Context, event outbox.Event) (outbox.Event, error) {
	if repository.storage.failOutbox != nil {
		return outbox.Event{}, repository.storage.failOutbox
	}
	snapshot := event.Snapshot()
	for _, existing := range repository.storage.outboxEvents {
		if existing.Snapshot().ID == snapshot.ID {
			return outbox.Event{}, outbox.ErrAlreadyExists
		}
	}
	snapshot.Sequence = outbox.Sequence(len(repository.storage.outboxEvents) + 1)
	stored, err := outbox.RestoreEvent(snapshot)
	if err != nil {
		return outbox.Event{}, err
	}
	repository.storage.outboxEvents = append(repository.storage.outboxEvents, stored)
	repository.storage.writeCount++
	return stored, nil
}

func (storage *memoryIdentityStorage) clone() *memoryIdentityStorage {
	copyStorage := &memoryIdentityStorage{
		users: make(map[uuid.UUID]User, len(storage.users)), claims: make(map[string]UsernameClaim, len(storage.claims)),
		devices: make(map[uuid.UUID]DeviceCredential, len(storage.devices)), recoveries: make(map[uuid.UUID]RecoveryCredential, len(storage.recoveries)),
		attempts: make(map[string]RecoveryAttempt, len(storage.attempts)), assisted: make(map[uuid.UUID]AssistedRecoveryGrant, len(storage.assisted)),
		challenges: make(map[string]Challenge, len(storage.challenges)), results: make(map[secretresult.Key]secretresult.Result, len(storage.results)),
		auditHead: storage.auditHead, auditEvents: append([]audit.SignedEvent(nil), storage.auditEvents...),
		checkpoints: append([]audit.Checkpoint(nil), storage.checkpoints...), outboxEvents: append([]outbox.Event(nil), storage.outboxEvents...),
		failOutbox: storage.failOutbox, writeCount: storage.writeCount,
	}
	for key, value := range storage.users {
		copyStorage.users[key] = value
	}
	for key, value := range storage.claims {
		copyStorage.claims[key] = value
	}
	for key, value := range storage.devices {
		copyStorage.devices[key] = value
	}
	for key, value := range storage.recoveries {
		copyStorage.recoveries[key] = value
	}
	for key, value := range storage.attempts {
		copyStorage.attempts[key] = value
	}
	for key, value := range storage.assisted {
		copyStorage.assisted[key] = value
	}
	for key, value := range storage.challenges {
		copyStorage.challenges[key] = value
	}
	for key, value := range storage.results {
		copyStorage.results[key] = value
	}
	return copyStorage
}

func (storage *memoryIdentityStorage) commit(working *memoryIdentityStorage) {
	storage.users, storage.claims = working.users, working.claims
	storage.devices, storage.recoveries = working.devices, working.recoveries
	storage.attempts, storage.assisted = working.attempts, working.assisted
	storage.challenges, storage.results = working.challenges, working.results
	storage.auditHead, storage.auditEvents = working.auditHead, working.auditEvents
	storage.checkpoints, storage.outboxEvents = working.checkpoints, working.outboxEvents
	storage.failOutbox = working.failOutbox
	storage.writeCount = working.writeCount
}
