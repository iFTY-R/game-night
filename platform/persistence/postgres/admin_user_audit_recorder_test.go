package postgres

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/google/uuid"
	adminuser "github.com/iFTY-R/game-night/platform/admin/user"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/outbox"
)

// recorderFakeUnitOfWork runs the transactional closure against in-memory repositories so the
// append/health orchestration can be tested without PostgreSQL.
type recorderFakeUnitOfWork struct{ transaction *recorderFakeTransaction }

func (unitOfWork *recorderFakeUnitOfWork) Run(ctx context.Context, work audit.TransactionWork) error {
	return work(ctx, unitOfWork.transaction)
}

type recorderFakeTransaction struct {
	auditRepository *recorderFakeAuditRepository
	checkpoints     *recorderFakeCheckpointRepository
}

func (transaction *recorderFakeTransaction) Audit() audit.Repository {
	return transaction.auditRepository
}
func (transaction *recorderFakeTransaction) Checkpoints() audit.CheckpointRepository {
	return transaction.checkpoints
}
func (transaction *recorderFakeTransaction) OutboxEvents() outbox.EventRepository { return nil }

type recorderFakeAuditRepository struct{ head audit.Head }

func (repository *recorderFakeAuditRepository) ReadHead(context.Context, audit.ChainID) (audit.Head, error) {
	return repository.head, nil
}

func (repository *recorderFakeAuditRepository) AppendEvent(_ context.Context, request audit.AppendRequest) (audit.Head, error) {
	snapshot := request.ExpectedHead.Snapshot()
	return audit.RestoreHead(audit.HeadSnapshot{
		ChainID: snapshot.ChainID, Sequence: snapshot.Sequence + 1, Hash: snapshot.Hash,
		UpdatedAt: snapshot.UpdatedAt.Add(time.Second),
	})
}

func (repository *recorderFakeAuditRepository) List(context.Context, audit.ListRequest) ([]audit.SignedEvent, error) {
	return nil, nil
}

// recorderFakeCheckpointRepository serves scripted progress reads: index 0 is the pre-append
// snapshot and index 1 is the post-append derivation that sees the just-inserted event.
type recorderFakeCheckpointRepository struct {
	progressReads []audit.CheckpointProgress
	readCount     int
	pending       []audit.Checkpoint
}

func (repository *recorderFakeCheckpointRepository) ReadCheckpointProgress(context.Context, audit.ChainID) (audit.CheckpointProgress, error) {
	index := repository.readCount
	if index >= len(repository.progressReads) {
		index = len(repository.progressReads) - 1
	}
	repository.readCount++
	return repository.progressReads[index], nil
}

func (repository *recorderFakeCheckpointRepository) AppendPendingCheckpoint(_ context.Context, checkpoint audit.Checkpoint) error {
	repository.pending = append(repository.pending, checkpoint)
	return nil
}

func newRecorderFixture(t *testing.T, now time.Time, policy *audit.CheckpointHealthPolicy, progressReads []audit.CheckpointProgress) (*AdminUserAuditRecorder, *recorderFakeCheckpointRepository) {
	t.Helper()
	service, err := audit.NewService(newRepositoryAuditKeyring())
	if err != nil {
		t.Fatalf("audit service: %v", err)
	}
	hash, err := audit.NewHash(func() []byte { digest := sha256.Sum256([]byte("recorder-test-head")); return digest[:] }())
	if err != nil {
		t.Fatalf("head hash: %v", err)
	}
	head, err := audit.RestoreHead(audit.HeadSnapshot{
		ChainID: audit.ChainAdmin, Sequence: 5, Hash: hash, UpdatedAt: now.Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	checkpoints := &recorderFakeCheckpointRepository{progressReads: progressReads}
	recorder := &AdminUserAuditRecorder{
		service: service,
		unitOfWork: &recorderFakeUnitOfWork{transaction: &recorderFakeTransaction{
			auditRepository: &recorderFakeAuditRepository{head: head}, checkpoints: checkpoints,
		}},
		checkpointHealth: policy,
		clock:            clock.NewFake(now),
	}
	return recorder, checkpoints
}

func annotationEvent(now time.Time) adminuser.AnnotationAuditEvent {
	return adminuser.AnnotationAuditEvent{
		ActorAdminID: uuid.New(), Action: "create_user_tag", Reason: "regression test",
		DetailDigest: sha256.Sum256([]byte("detail")), RequestID: "recorder-regression", OccurredAt: now,
	}
}

// A fully checkpointed chain (acknowledged == head, zero UncheckpointedSince) is the healthiest
// possible state; a sensitive write must succeed there. The old single-read implementation fed the
// stale pre-append snapshot into the post-append evaluation and deadlocked every sensitive write
// until unrelated traffic recreated a checkpoint backlog.
func TestAdminUserAuditRecorderAppendsOnFullyCheckpointedChain(t *testing.T) {
	now := time.Date(2026, 7, 26, 15, 0, 0, 0, time.UTC)
	policy, err := audit.NewCheckpointHealthPolicy(false, audit.SinkReadinessFunc(func(context.Context) bool { return true }))
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	recorder, checkpoints := newRecorderFixture(t, now, policy, []audit.CheckpointProgress{
		{ChainID: audit.ChainAdmin, AcknowledgedSequence: 5, AcknowledgedAt: now.Add(-time.Minute)},
		{ChainID: audit.ChainAdmin, AcknowledgedSequence: 5, AcknowledgedAt: now.Add(-time.Minute), UncheckpointedSince: now},
	})

	eventID, err := recorder.RecordAnnotationWrite(context.Background(), annotationEvent(now))
	if err != nil || eventID == uuid.Nil {
		t.Fatalf("append on fully checkpointed chain = %v (event %s)", err, eventID)
	}
	if checkpoints.readCount != 2 {
		t.Fatalf("checkpoint progress must be re-read after the append, reads=%d", checkpoints.readCount)
	}
	if len(checkpoints.pending) != 0 {
		t.Fatalf("a fresh single-event lag must not enqueue a checkpoint, pending=%d", len(checkpoints.pending))
	}
}

// When the post-append lag crosses the policy threshold, the same transaction must enqueue a
// pending checkpoint for the new head so the write both succeeds and schedules its own anchor.
func TestAdminUserAuditRecorderEnqueuesCheckpointWhenDue(t *testing.T) {
	now := time.Date(2026, 7, 26, 15, 0, 0, 0, time.UTC)
	policy, err := audit.NewCheckpointHealthPolicyWithThresholds(
		false, audit.SinkReadinessFunc(func(context.Context) bool { return true }), 1, audit.CheckpointMaxAge,
	)
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	recorder, checkpoints := newRecorderFixture(t, now, policy, []audit.CheckpointProgress{
		{ChainID: audit.ChainAdmin, AcknowledgedSequence: 5, AcknowledgedAt: now.Add(-time.Minute)},
		{ChainID: audit.ChainAdmin, AcknowledgedSequence: 5, AcknowledgedAt: now.Add(-time.Minute), UncheckpointedSince: now},
	})

	eventID, err := recorder.RecordAnnotationWrite(context.Background(), annotationEvent(now))
	if err != nil || eventID == uuid.Nil {
		t.Fatalf("append with due checkpoint = %v (event %s)", err, eventID)
	}
	if len(checkpoints.pending) != 1 || checkpoints.pending[0].Snapshot().Sequence != 6 {
		t.Fatalf("due lag must enqueue a checkpoint for the new head, pending=%+v", checkpoints.pending)
	}
}
