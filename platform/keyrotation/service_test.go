package keyrotation

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/profile"
	"github.com/iFTY-R/game-night/platform/security"
)

func TestServiceResumesProfileRotationAndAppendsLifecycleAudit(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	oldKeyring := testAESKeyring[security.PIIKeyPurpose](t, now, 1, []uint32{1, 2})
	activeKeyring := testAESKeyring[security.PIIKeyPurpose](t, now, 2, []uint32{1, 2})
	oldPII, _ := profile.NewDefaultPIIProtector(oldKeyring)
	activePII, _ := profile.NewDefaultPIIProtector(activeKeyring)
	totpKeyring := testAESKeyring[security.TOTPKeyPurpose](t, now, 2, []uint32{2})
	totpService, _ := admin.NewTOTPService(totpKeyring)
	auditService, auditRepository := newTestAuditService(t)
	checkpointPolicy, err := audit.NewCheckpointHealthPolicyWithThresholds(false,
		audit.SinkReadinessFunc(func(context.Context) bool { return true }), audit.CheckpointMaxEvents, audit.CheckpointMaxAge)
	if err != nil {
		t.Fatal(err)
	}
	userID := uuid.New()
	encrypted, err := oldPII.EncryptRealName(userID, "test-player")
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeRotationStore{profile: UserProfileCiphertext{UserID: userID, ProfileVersion: 1, Encrypted: encrypted}, audit: auditRepository}
	service, err := NewService(Config{Owner: "rotation-test", LeaseDuration: time.Minute, BatchSize: 10},
		store, activePII, totpService, auditService, checkpointPolicy, clock.NewFake(now))
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Processed != 1 || first.Conflicts != 0 || first.Completed {
		t.Fatalf("first pass = %+v", first)
	}
	if store.profile.Encrypted.KeyVersion != 2 || store.job.Cursor.Scope != ScopeUserProfiles {
		t.Fatalf("first pass did not rotate and advance: %+v", store.job)
	}
	var second Result
	for attempt := 0; attempt < 4 && !second.Completed; attempt++ {
		second, err = service.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
	}
	if !second.Completed || store.job.CompletedAt.IsZero() || auditRepository.head.Sequence() != 4 {
		t.Fatalf("resume did not complete lifecycle: result=%+v job=%+v audit-sequence=%d", second, store.job, auditRepository.head.Sequence())
	}
	plaintext, err := activePII.DecryptRealName(userID, store.profile.Encrypted)
	if err != nil || plaintext != "test-player" {
		t.Fatalf("rotated profile cannot decrypt: %v", err)
	}
	for _, event := range auditRepository.events {
		if string(event.Snapshot().CanonicalEvent) == "test-player" {
			t.Fatal("audit event contains profile plaintext")
		}
	}
}

func TestServiceRetriesAConcurrentProfileCASConflict(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	oldKeyring := testAESKeyring[security.PIIKeyPurpose](t, now, 1, []uint32{1, 2})
	activeKeyring := testAESKeyring[security.PIIKeyPurpose](t, now, 2, []uint32{1, 2})
	oldPII, _ := profile.NewDefaultPIIProtector(oldKeyring)
	activePII, _ := profile.NewDefaultPIIProtector(activeKeyring)
	totpService, _ := admin.NewTOTPService(testAESKeyring[security.TOTPKeyPurpose](t, now, 2, []uint32{2}))
	auditService, _ := newTestAuditService(t)
	checkpointPolicy, _ := audit.NewCheckpointHealthPolicyWithThresholds(false,
		audit.SinkReadinessFunc(func(context.Context) bool { return true }), audit.CheckpointMaxEvents, audit.CheckpointMaxAge)
	userID := uuid.New()
	encrypted, _ := oldPII.EncryptRealName(userID, "conflict")
	store := &fakeRotationStore{
		profile: UserProfileCiphertext{UserID: userID, ProfileVersion: 1, Encrypted: encrypted}, forceConflict: true,
	}
	service, err := NewService(Config{Owner: "rotation-test", LeaseDuration: time.Minute, BatchSize: 10},
		store, activePII, totpService, auditService, checkpointPolicy, clock.NewFake(now))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Conflicts != 1 || result.Processed != 0 {
		t.Fatalf("CAS conflict result = %+v", result)
	}
	store.forceConflict = false
	for attempt := 0; attempt < 5 && result.Processed == 0; attempt++ {
		result, err = service.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
	}
	if result.Processed != 1 {
		t.Fatalf("retry result = %+v", result)
	}
}

type testKeyringEntry struct {
	Version   uint32    `json:"version"`
	Key       string    `json:"key"`
	NotBefore time.Time `json:"not_before"`
}

type testKeyringDocument struct {
	ActiveVersion uint32             `json:"active_version"`
	Keys          []testKeyringEntry `json:"keys"`
}

func testAESKeyring[P security.AESKeyPurpose](t testing.TB, now time.Time, active uint32, versions []uint32) *security.AESKeyring[P] {
	t.Helper()
	document := testKeyringDocument{ActiveVersion: active}
	for _, version := range versions {
		key := make([]byte, 32)
		for index := range key {
			key[index] = byte(version)
		}
		document.Keys = append(document.Keys, testKeyringEntry{
			Version: version, Key: base64.StdEncoding.EncodeToString(key), NotBefore: now.Add(-time.Hour),
		})
	}
	contents, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "rotation-keyring.json")
	if err := os.WriteFile(path, contents, 0o400); err != nil {
		t.Fatal(err)
	}
	keyring, err := security.LoadAESKeyring[P](path, now)
	if err != nil {
		t.Fatal(err)
	}
	return keyring
}

type fakeRotationStore struct {
	profile       UserProfileCiphertext
	job           fakeStoredJob
	forceConflict bool
	audit         *testAuditRepository
}

type fakeStoredJob struct {
	Job
	LeaseOwner  outbox.LeaseOwner
	LeaseUntil  time.Time
	CompletedAt time.Time
	Status      string
}

func (store *fakeRotationStore) Run(ctx context.Context, work TransactionWork) error {
	if store.audit == nil {
		_, store.audit = newTestAuditService(nil)
	}
	return work(ctx, store)
}

func (store *fakeRotationStore) ListReferencedVersions(_ context.Context, purpose Purpose) ([]uint32, error) {
	if purpose == PurposePII && store.profile.Encrypted.KeyVersion == 1 {
		return []uint32{1, 2}, nil
	}
	return []uint32{2}, nil
}

func (store *fakeRotationStore) CreateJob(_ context.Context, request CreateRequest) (bool, error) {
	if store.job.ID != uuid.Nil && store.job.Status != "completed" {
		return false, nil
	}
	store.job = fakeStoredJob{Job: Job{ID: request.JobID, Purpose: request.Purpose, SourceVersion: request.SourceVersion,
		TargetVersion: request.TargetVersion, Cursor: Cursor{Scope: request.InitialScope}, StartedAt: time.Time{}}, Status: "pending"}
	return true, nil
}

func (store *fakeRotationStore) AcquireJob(_ context.Context, owner outbox.LeaseOwner, acquiredAt, leaseUntil time.Time) (*AcquiredJob, error) {
	if store.job.ID == uuid.Nil || store.job.Status == "completed" || store.job.LeaseOwner != "" && store.job.LeaseUntil.After(acquiredAt) {
		return nil, nil
	}
	started := store.job.Status == "pending"
	store.job.Status, store.job.LeaseOwner, store.job.LeaseUntil = "running", owner, leaseUntil
	if started {
		store.job.StartedAt = acquiredAt
	}
	return &AcquiredJob{Job: store.job.Job, StartedNow: started}, nil
}

func (store *fakeRotationStore) ListUserProfiles(_ context.Context, source uint32, after uuid.UUID, _ uint32) ([]UserProfileCiphertext, error) {
	if store.profile.Encrypted.KeyVersion != source || store.profile.UserID == after {
		return nil, nil
	}
	return []UserProfileCiphertext{store.profile}, nil
}

func (store *fakeRotationStore) RotateUserProfile(_ context.Context, row UserProfileCiphertext, rotated profile.EncryptedValue, source uint32) (bool, error) {
	if store.forceConflict || store.profile.UserID != row.UserID || store.profile.Encrypted.KeyVersion != source {
		return false, nil
	}
	store.profile.Encrypted = rotated
	return true, nil
}

func (store *fakeRotationStore) ListProfileExportItems(context.Context, uint32, uuid.UUID, int64, uint32) ([]ProfileExportCiphertext, error) {
	return nil, nil
}

func (store *fakeRotationStore) RotateProfileExportItem(context.Context, ProfileExportCiphertext, profile.EncryptedValue, uint32) (bool, error) {
	return false, errors.New("unexpected export rotation")
}

func (store *fakeRotationStore) ListTOTPEnrollments(context.Context, uint32, uuid.UUID, uint32) ([]TOTPEnrollmentCiphertext, error) {
	return nil, nil
}

func (store *fakeRotationStore) RotateTOTPEnrollment(context.Context, TOTPEnrollmentCiphertext, security.Encrypted[security.TOTPKeyPurpose], uint32) (bool, error) {
	return false, errors.New("unexpected totp rotation")
}

func (store *fakeRotationStore) AdvanceCursor(_ context.Context, request AdvanceRequest) error {
	if store.job.Cursor != request.ExpectedCursor {
		return ErrConcurrentTransition
	}
	store.job.Cursor = request.NextCursor
	store.job.ProcessedCount += request.ProcessedDelta
	store.job.ConflictCount += request.ConflictDelta
	return nil
}

func (store *fakeRotationStore) CountReferences(_ context.Context, purpose Purpose, _ uint32) (int64, error) {
	if purpose == PurposePII && store.profile.Encrypted.KeyVersion == 1 {
		return 1, nil
	}
	return 0, nil
}

func (store *fakeRotationStore) CompleteJob(_ context.Context, job Job, owner outbox.LeaseOwner, at time.Time) (Job, error) {
	if store.job.LeaseOwner != owner {
		return Job{}, ErrConcurrentTransition
	}
	store.job.Status, store.job.LeaseOwner, store.job.CompletedAt = "completed", "", at
	return store.job.Job, nil
}

func (store *fakeRotationStore) FailJob(context.Context, uuid.UUID, outbox.LeaseOwner, string, time.Time) error {
	store.job.Status = "failed"
	return nil
}

func (store *fakeRotationStore) ReleaseJob(_ context.Context, jobID uuid.UUID, owner outbox.LeaseOwner, _ time.Time) error {
	if store.job.ID != jobID || store.job.LeaseOwner != owner {
		return ErrConcurrentTransition
	}
	store.job.LeaseOwner = ""
	return nil
}

func (store *fakeRotationStore) Audit() audit.Repository                 { return store.audit }
func (store *fakeRotationStore) Checkpoints() audit.CheckpointRepository { return store.audit }

type testAuditKeys struct {
	private ed25519.PrivateKey
	public  ed25519.PublicKey
}

func (keys testAuditKeys) ActiveVersion() uint32 { return 1 }
func (keys testAuditKeys) Sign(payload []byte) (security.AuditSignature, error) {
	return security.AuditSignature{KeyVersion: 1, Value: ed25519.Sign(keys.private, payload)}, nil
}
func (keys testAuditKeys) Verify(payload []byte, signature security.AuditSignature) bool {
	return signature.KeyVersion == 1 && ed25519.Verify(keys.public, payload, signature.Value)
}

type testAuditRepository struct {
	head   audit.Head
	events []audit.SignedEvent
}

func (repository *testAuditRepository) ReadHead(context.Context, audit.ChainID) (audit.Head, error) {
	return repository.head, nil
}
func (repository *testAuditRepository) AppendEvent(_ context.Context, request audit.AppendRequest) (audit.Head, error) {
	if request.ExpectedHead.Sequence() != repository.head.Sequence() {
		return audit.Head{}, audit.ErrHeadConflict
	}
	next, err := request.Event.NextHead()
	if err != nil {
		return audit.Head{}, err
	}
	repository.events = append(repository.events, request.Event)
	repository.head = next
	return next, nil
}
func (repository *testAuditRepository) List(context.Context, audit.ListRequest) ([]audit.SignedEvent, error) {
	return repository.events, nil
}
func (repository *testAuditRepository) ReadCheckpointProgress(context.Context, audit.ChainID) (audit.CheckpointProgress, error) {
	return audit.CheckpointProgress{ChainID: audit.ChainAdmin, AcknowledgedSequence: repository.head.Sequence()}, nil
}
func (repository *testAuditRepository) AppendPendingCheckpoint(context.Context, audit.Checkpoint) error {
	return nil
}

func newTestAuditService(t testing.TB) (*audit.Service, *testAuditRepository) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	service, err := audit.NewService(testAuditKeys{private: private, public: public})
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	initial, err := audit.RestoreHead(audit.HeadSnapshot{ChainID: audit.ChainAdmin, Hash: audit.GenesisHash, UpdatedAt: time.Unix(1_699_999_999, 0).UTC()})
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	return service, &testAuditRepository{head: initial}
}

var _ UnitOfWork = (*fakeRotationStore)(nil)
var _ Transaction = (*fakeRotationStore)(nil)
var _ audit.Repository = (*testAuditRepository)(nil)
var _ audit.CheckpointRepository = (*testAuditRepository)(nil)
