package operations

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	admin "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/security"
)

type commandRepositoryFake struct {
	CommandRepository
	maintenance  MaintenanceState
	cache        map[CacheNamespace]CacheGeneration
	previews     map[[sha256.Size]byte]CommandPreview
	receipts     map[string]CommandReceipt
	retryTasks   map[uuid.UUID]RetryTask
	retries      map[uuid.UUID]uint32
	retryReceipt map[string]RetryReceipt
}

func newCommandRepositoryFake(now time.Time) *commandRepositoryFake {
	return &commandRepositoryFake{
		maintenance: MaintenanceState{Scope: MaintenanceUserMutations, Version: 1, ChangedAt: now.Add(-time.Hour)},
		cache:       map[CacheNamespace]CacheGeneration{CacheOverviewProjection: {Namespace: CacheOverviewProjection, Generation: 1, UpdatedAt: now.Add(-time.Hour)}},
		previews:    make(map[[sha256.Size]byte]CommandPreview), receipts: make(map[string]CommandReceipt),
		retryTasks: make(map[uuid.UUID]RetryTask), retries: make(map[uuid.UUID]uint32), retryReceipt: make(map[string]RetryReceipt),
	}
}

func (repository *commandRepositoryFake) GetMaintenanceState(context.Context) (MaintenanceState, error) {
	return repository.maintenance, nil
}
func (repository *commandRepositoryFake) UpdateMaintenanceState(_ context.Context, change MaintenanceChange) (MaintenanceState, error) {
	if repository.maintenance.Version != change.ExpectedVersion {
		return MaintenanceState{}, ErrConflict
	}
	repository.maintenance = MaintenanceState{Enabled: change.Enabled, Scope: change.Scope, Reason: change.Reason, PlannedEndAt: change.PlannedEndAt, Version: change.ExpectedVersion + 1, ChangedByAdminID: change.ChangedByAdminID, ChangedAt: change.ChangedAt}
	return repository.maintenance, nil
}
func (*commandRepositoryFake) GetOverviewCounts(_ context.Context, start, end, sampled time.Time) (OverviewCounts, error) {
	return OverviewCounts{ActiveRooms: 4, RunningGames: 2, WindowStart: start, WindowEnd: end, SampledAt: sampled}, nil
}
func (repository *commandRepositoryFake) GetCacheGeneration(_ context.Context, namespace CacheNamespace) (CacheGeneration, error) {
	value, ok := repository.cache[namespace]
	if !ok {
		return CacheGeneration{}, ErrNotFound
	}
	return value, nil
}
func (repository *commandRepositoryFake) AdvanceCacheGeneration(_ context.Context, namespace CacheNamespace, expected uint64, actor uuid.UUID, at time.Time) (CacheGeneration, error) {
	current := repository.cache[namespace]
	if current.Generation != expected {
		return CacheGeneration{}, ErrConflict
	}
	current.Generation++
	current.UpdatedByAdminID, current.UpdatedAt = actor, at
	repository.cache[namespace] = current
	return current, nil
}
func (repository *commandRepositoryFake) CreateCommandPreview(_ context.Context, preview CommandPreview) (CommandPreview, error) {
	repository.previews[preview.Digest] = preview
	return preview, nil
}
func (repository *commandRepositoryFake) GetCommandPreview(_ context.Context, actor uuid.UUID, digest [sha256.Size]byte) (CommandPreview, error) {
	preview, ok := repository.previews[digest]
	if !ok || preview.ActorAdminID != actor {
		return CommandPreview{}, ErrNotFound
	}
	return preview, nil
}
func (repository *commandRepositoryFake) ConsumeCommandPreview(_ context.Context, actor uuid.UUID, digest [sha256.Size]byte, version uint64, at time.Time) (CommandPreview, error) {
	preview, err := repository.GetCommandPreview(context.Background(), actor, digest)
	if err != nil || preview.Version != version || !preview.ConsumedAt.IsZero() || !at.Before(preview.ExpiresAt) {
		return CommandPreview{}, ErrPreviewExpired
	}
	preview.ConsumedAt, preview.Version = at, preview.Version+1
	repository.previews[digest] = preview
	return preview, nil
}
func (repository *commandRepositoryFake) GetCommandReceipt(_ context.Context, actor uuid.UUID, operation string) (CommandReceipt, error) {
	receipt, ok := repository.receipts[actor.String()+operation]
	if !ok {
		return CommandReceipt{}, ErrNotFound
	}
	return receipt, nil
}
func (repository *commandRepositoryFake) CreateCommandReceipt(_ context.Context, receipt CommandReceipt) (CommandReceipt, error) {
	repository.receipts[receipt.ActorAdminID.String()+receipt.OperationID] = receipt
	return receipt, nil
}
func (repository *commandRepositoryFake) GetRetryTask(_ context.Context, kind RetryTaskKind, id uuid.UUID) (RetryTask, error) {
	value, ok := repository.retryTasks[id]
	if !ok || value.Kind != kind {
		return RetryTask{}, ErrNotFound
	}
	return value, nil
}
func (repository *commandRepositoryFake) GetRetryTaskForUpdate(ctx context.Context, kind RetryTaskKind, id uuid.UUID) (RetryTask, error) {
	return repository.GetRetryTask(ctx, kind, id)
}
func (repository *commandRepositoryFake) CountTaskRetries(_ context.Context, _ RetryTaskKind, id uuid.UUID) (uint32, error) {
	return repository.retries[id], nil
}
func (repository *commandRepositoryFake) RetryTask(_ context.Context, kind RetryTaskKind, id uuid.UUID, version uint64, at time.Time) (RetryTask, error) {
	value, err := repository.GetRetryTask(context.Background(), kind, id)
	if err != nil || value.Version != version || value.State != "failed" {
		return RetryTask{}, ErrConflict
	}
	value.State, value.Version, value.UpdatedAt = "queued", value.Version+1, at
	repository.retryTasks[id] = value
	return value, nil
}
func (repository *commandRepositoryFake) GetRetryReceipt(_ context.Context, actor uuid.UUID, operation string) (RetryReceipt, error) {
	value, ok := repository.retryReceipt[actor.String()+operation]
	if !ok {
		return RetryReceipt{}, ErrNotFound
	}
	return value, nil
}
func (repository *commandRepositoryFake) CreateRetryReceipt(_ context.Context, receipt RetryReceipt) (RetryReceipt, error) {
	repository.retryReceipt[receipt.ActorAdminID.String()+receipt.OperationID] = receipt
	repository.retries[receipt.TaskID] = receipt.ManualRetryCount
	return receipt, nil
}

type commandUnitOfWorkFake struct{ transaction CommandTransaction }

func (unit *commandUnitOfWorkFake) Run(ctx context.Context, work CommandTransactionWork) error {
	return work(ctx, unit.transaction)
}

type commandTransactionFake struct {
	repository      CommandRepository
	auditRepository *commandAuditRepositoryFake
	checkpoints     *commandCheckpointRepositoryFake
	outbox          *commandOutboxFake
}

func (transaction *commandTransactionFake) Operations() CommandRepository {
	return transaction.repository
}
func (transaction *commandTransactionFake) Audit() audit.Repository {
	return transaction.auditRepository
}
func (transaction *commandTransactionFake) Checkpoints() audit.CheckpointRepository {
	return transaction.checkpoints
}
func (transaction *commandTransactionFake) OutboxEvents() outbox.EventRepository {
	return transaction.outbox
}

type commandAuditRepositoryFake struct {
	head   audit.Head
	events []audit.SignedEvent
	fail   bool
}

func (repository *commandAuditRepositoryFake) ReadHead(context.Context, audit.ChainID) (audit.Head, error) {
	if repository.fail {
		return audit.Head{}, errors.New("audit unavailable")
	}
	return repository.head, nil
}
func (repository *commandAuditRepositoryFake) AppendEvent(_ context.Context, request audit.AppendRequest) (audit.Head, error) {
	next, err := request.Event.NextHead()
	if err == nil {
		repository.head = next
		repository.events = append(repository.events, request.Event)
	}
	return next, err
}
func (*commandAuditRepositoryFake) List(context.Context, audit.ListRequest) ([]audit.SignedEvent, error) {
	return nil, nil
}

type commandCheckpointRepositoryFake struct{ progress audit.CheckpointProgress }

func (repository *commandCheckpointRepositoryFake) ReadCheckpointProgress(context.Context, audit.ChainID) (audit.CheckpointProgress, error) {
	return repository.progress, nil
}
func (*commandCheckpointRepositoryFake) AppendPendingCheckpoint(context.Context, audit.Checkpoint) error {
	return nil
}

type commandOutboxFake struct{ events []outbox.Event }

func (repository *commandOutboxFake) Insert(_ context.Context, event outbox.Event) (outbox.Event, error) {
	repository.events = append(repository.events, event)
	return event, nil
}

type commandCacheImpactFake uint64

func (value commandCacheImpactFake) EstimateCacheEntries(context.Context, CacheNamespace) (uint64, error) {
	return uint64(value), nil
}

type commandAuditKeyring struct{ private ed25519.PrivateKey }

func newCommandAuditKeyring() *commandAuditKeyring {
	seed := sha256.Sum256([]byte("operations-command-test-key"))
	return &commandAuditKeyring{private: ed25519.NewKeyFromSeed(seed[:])}
}
func (*commandAuditKeyring) ActiveVersion() uint32 { return 1 }
func (keyring *commandAuditKeyring) Sign(payload []byte) (security.AuditSignature, error) {
	return security.AuditSignature{KeyVersion: 1, Value: ed25519.Sign(keyring.private, payload)}, nil
}
func (keyring *commandAuditKeyring) Verify(payload []byte, signature security.AuditSignature) bool {
	return signature.KeyVersion == 1 && ed25519.Verify(keyring.private.Public().(ed25519.PublicKey), payload, signature.Value)
}

func newCommandTestFixture(t *testing.T, now time.Time) (*CommandService, admin.ActorContext, *commandRepositoryFake, *commandTransactionFake) {
	t.Helper()
	repository := newCommandRepositoryFake(now)
	hash, _ := audit.NewHash(sha256.New().Sum(make([]byte, 0)))
	head, err := audit.RestoreHead(audit.HeadSnapshot{ChainID: audit.ChainAdmin, Sequence: 1, Hash: hash, UpdatedAt: now.Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	transaction := &commandTransactionFake{repository: repository, auditRepository: &commandAuditRepositoryFake{head: head}, checkpoints: &commandCheckpointRepositoryFake{progress: audit.CheckpointProgress{ChainID: audit.ChainAdmin, AcknowledgedSequence: 0, UncheckpointedSince: now.Add(-time.Minute)}}, outbox: &commandOutboxFake{}}
	auditService, _ := audit.NewService(newCommandAuditKeyring())
	policy, _ := audit.NewCheckpointHealthPolicy(false, audit.SinkReadinessFunc(func(context.Context) bool { return true }))
	service, err := NewCommandService(CommandServiceConfig{Repository: repository, UnitOfWork: &commandUnitOfWorkFake{transaction: transaction}, Audit: auditService, CheckpointHealth: policy, CacheImpact: commandCacheImpactFake(7), Clock: clock.NewFake(now)})
	if err != nil {
		t.Fatal(err)
	}
	actor := newOperationsActor(t, now, true)
	return service, actor, repository, transaction
}

func newOperationsActor(t *testing.T, now time.Time, authorized bool) admin.ActorContext {
	t.Helper()
	adminID, sessionID := uuid.New(), uuid.New()
	session, err := admin.RestoreSession(admin.SessionSnapshot{ID: sessionID, AdminID: adminID, Selector: "AAAAAAAAAAAAAAAAAAAAAA", SecretMAC: security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: 1, Value: make([]byte, 32)}, CSRFHash: security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: 1, Value: make([]byte, 32)}, Kind: admin.SessionKindFull, AdminVersion: 1, PasswordVersion: 1, SessionVersion: 1, MaxAttempts: 5, CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(2 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	permissions, _ := admin.NewPermissionSet(admin.PermissionOperationsMaintain)
	if !authorized {
		permissions, _ = admin.NewPermissionSet(admin.PermissionOperationsRead)
	}
	elevations, _ := admin.NewElevationSet()
	if authorized {
		elevation, grantErr := admin.NewElevation(session, 1, admin.ElevationScopeOperationsMaintenance, now, now.Add(4*time.Minute))
		if grantErr != nil {
			t.Fatal(grantErr)
		}
		elevations, _ = admin.NewElevationSet(elevation)
	}
	actor, err := admin.NewActorContext(adminID, sessionID, session, permissions, elevations, 1, "operations-test", "https://admin.example.test", "203.0.113.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	return actor
}

func testOperationID(t *testing.T, marker byte) idempotency.OperationID {
	t.Helper()
	value, err := idempotency.NewOperationID([]byte{marker, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestMaintenanceCommandRequiresAuthorizationAndReplaysReceipt(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	service, actor, repository, transaction := newCommandTestFixture(t, now)
	if _, err := service.PreviewMaintenanceChange(context.Background(), newOperationsActor(t, now, false), PreviewMaintenanceChangeInput{Enabled: true, Scope: MaintenanceUserMutations, Reason: "release maintenance", PlannedEndAt: now.Add(time.Hour)}); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("permission error = %v", err)
	}
	preview, err := service.PreviewMaintenanceChange(context.Background(), actor, PreviewMaintenanceChangeInput{Enabled: true, Scope: MaintenanceUserMutations, Reason: "release maintenance", PlannedEndAt: now.Add(time.Hour)})
	if err != nil || preview.ActiveRooms != 4 || preview.ActiveGames != 2 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	command := ApplyMaintenanceChangeCommand{OperationID: testOperationID(t, 1), Enabled: true, Scope: MaintenanceUserMutations, Reason: "release maintenance", PlannedEndAt: now.Add(time.Hour), ExpectedVersion: 1, PreviewDigest: preview.PreviewDigest}
	first, err := service.ApplyMaintenanceChange(context.Background(), actor, command)
	if err != nil || first.Outcome != CommandOutcomeApplied || first.Maintenance.Version != 2 || len(transaction.auditRepository.events) != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := service.ApplyMaintenanceChange(context.Background(), actor, command)
	if err != nil || second.Receipt.AuditEventID != first.Receipt.AuditEventID || repository.maintenance.Version != 2 || len(transaction.auditRepository.events) != 1 {
		t.Fatalf("replay=%+v err=%v", second, err)
	}
}
