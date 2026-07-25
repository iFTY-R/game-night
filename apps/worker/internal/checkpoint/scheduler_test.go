package checkpoint

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/outbox"
)

func TestSchedulerEnqueuesCheckpointForSilentUncheckpointedChain(t *testing.T) {
	now := time.Date(2026, 7, 26, 4, 30, 0, 0, time.UTC)
	transaction := newSchedulerTransaction(t, now, now.Add(-audit.CheckpointMaxAge))
	scheduler := newTestScheduler(t, transaction, now)

	result, err := scheduler.RunOnce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Enqueued || result.Idle {
		t.Fatalf("result = %+v, want one enqueued checkpoint", result)
	}
	if len(transaction.checkpoints.pending) != 1 {
		t.Fatalf("pending checkpoints = %d, want 1", len(transaction.checkpoints.pending))
	}
	if snapshot := transaction.checkpoints.pending[0].Snapshot(); snapshot.ChainID != audit.ChainAdmin || snapshot.Sequence != transaction.head.Sequence() {
		t.Fatalf("checkpoint snapshot = %+v, want current admin head", snapshot)
	}
}

func TestSchedulerLeavesRecentUncheckpointedChainAlone(t *testing.T) {
	now := time.Date(2026, 7, 26, 4, 30, 0, 0, time.UTC)
	transaction := newSchedulerTransaction(t, now, now.Add(-time.Minute))
	scheduler := newTestScheduler(t, transaction, now)

	result, err := scheduler.RunOnce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Idle || result.Enqueued || len(transaction.checkpoints.pending) != 0 {
		t.Fatalf("result=%+v pending=%d, want idle", result, len(transaction.checkpoints.pending))
	}
}

func newTestScheduler(t *testing.T, transaction *schedulerTransaction, now time.Time) *Scheduler {
	t.Helper()
	policy, err := audit.NewCheckpointHealthPolicy(false, audit.SinkReadinessFunc(func(context.Context) bool { return true }))
	if err != nil {
		t.Fatal(err)
	}
	scheduler, err := NewScheduler(
		schedulerUnitOfWork{transaction: transaction},
		fakeCheckpointPreparer{},
		policy,
		clock.NewFake(now),
	)
	if err != nil {
		t.Fatal(err)
	}
	return scheduler
}

func newSchedulerTransaction(t *testing.T, now, uncheckpointedSince time.Time) *schedulerTransaction {
	t.Helper()
	hash, err := audit.NewHash(bytes.Repeat([]byte{0x42}, audit.HashSize))
	if err != nil {
		t.Fatal(err)
	}
	head, err := audit.RestoreHead(audit.HeadSnapshot{
		ChainID: audit.ChainAdmin, Sequence: 2, Hash: hash, UpdatedAt: uncheckpointedSince,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &schedulerTransaction{
		head: head,
		checkpoints: &schedulerCheckpointRepository{progress: audit.CheckpointProgress{
			ChainID: audit.ChainAdmin, AcknowledgedSequence: 1, UncheckpointedSince: uncheckpointedSince,
		}},
	}
}

type schedulerUnitOfWork struct{ transaction *schedulerTransaction }

func (unitOfWork schedulerUnitOfWork) Run(ctx context.Context, work audit.TransactionWork) error {
	return work(ctx, unitOfWork.transaction)
}

type schedulerTransaction struct {
	head        audit.Head
	checkpoints *schedulerCheckpointRepository
}

func (transaction *schedulerTransaction) Audit() audit.Repository {
	return schedulerAuditRepository{head: transaction.head}
}
func (transaction *schedulerTransaction) Checkpoints() audit.CheckpointRepository {
	return transaction.checkpoints
}
func (*schedulerTransaction) OutboxEvents() outbox.EventRepository { return nil }

type schedulerAuditRepository struct{ head audit.Head }

func (repository schedulerAuditRepository) ReadHead(context.Context, audit.ChainID) (audit.Head, error) {
	return repository.head, nil
}
func (schedulerAuditRepository) AppendEvent(context.Context, audit.AppendRequest) (audit.Head, error) {
	return audit.Head{}, audit.ErrInvalidInput
}
func (schedulerAuditRepository) List(context.Context, audit.ListRequest) ([]audit.SignedEvent, error) {
	return nil, audit.ErrInvalidInput
}

type schedulerCheckpointRepository struct {
	progress audit.CheckpointProgress
	pending  []audit.Checkpoint
}

func (repository *schedulerCheckpointRepository) AppendPendingCheckpoint(_ context.Context, checkpoint audit.Checkpoint) error {
	repository.pending = append(repository.pending, checkpoint)
	return nil
}
func (repository *schedulerCheckpointRepository) ReadCheckpointProgress(context.Context, audit.ChainID) (audit.CheckpointProgress, error) {
	return repository.progress, nil
}

type fakeCheckpointPreparer struct{}

func (fakeCheckpointPreparer) PrepareCheckpoint(head audit.Head, createdAt time.Time) (audit.Checkpoint, error) {
	return audit.RestoreCheckpoint(audit.CheckpointSnapshot{
		ChainID: head.ChainID(), Sequence: head.Sequence(), ChainHash: head.Hash(),
		Signature: bytes.Repeat([]byte{0x24}, audit.SignatureSize), SigningKeyVersion: 1, CreatedAt: createdAt,
	})
}
