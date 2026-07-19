package checkpoint

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/persistence/objectstorage"
)

func TestDispatcherWritesVerifiedCheckpointBeforeAcknowledging(t *testing.T) {
	now := time.Date(2026, 7, 19, 4, 0, 0, 0, time.UTC)
	event := checkpointEvent(t, 1, now)
	store := newMemoryOutbox(t, now, event)
	sink := &recordingSink{}
	dispatcher := newTestDispatcher(t, store, sink, allowVerifier{}, clock.NewFake(now.Add(time.Second)))

	result, err := dispatcher.RunOnce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.Delivered != 1 || store.consumer.Snapshot().LastAckedSequence != 1 {
		t.Fatalf("result=%+v consumer=%+v", result, store.consumer.Snapshot())
	}
	if len(sink.objects) != 1 {
		t.Fatalf("written objects = %d, want 1", len(sink.objects))
	}
	checkpoint, err := audit.ParseCheckpoint(event.Snapshot().Payload)
	if err != nil {
		t.Fatal(err)
	}
	object := sink.objects[0]
	if object.Key().String() != checkpoint.Snapshot().ObjectKey() || !bytes.Equal(object.Content(), event.Snapshot().Payload) {
		t.Fatalf("object does not preserve checkpoint identity: key=%q", object.Key().String())
	}
	if store.consumer.Snapshot().LeaseOwner != "" {
		t.Fatal("successful pass retained the consumer lease")
	}
}

func TestDispatcherPersistsRetryAndDoesNotAckSinkFailure(t *testing.T) {
	now := time.Date(2026, 7, 19, 4, 0, 0, 0, time.UTC)
	store := newMemoryOutbox(t, now, checkpointEvent(t, 1, now))
	sink := &recordingSink{writeErr: objectstorage.ErrUnavailable}
	dispatcher := newTestDispatcher(t, store, sink, allowVerifier{}, clock.NewFake(now.Add(time.Second)))

	result, err := dispatcher.RunOnce(t.Context())
	if !errors.Is(err, ErrDeliveryFailed) {
		t.Fatalf("RunOnce() error = %v, want ErrDeliveryFailed", err)
	}
	if result.Delivered != 0 {
		t.Fatalf("delivered = %d, want 0", result.Delivered)
	}
	snapshot := store.consumer.Snapshot()
	if snapshot.LastAckedSequence != 0 || snapshot.RetryCount != 1 || snapshot.LastErrorCode != ErrorSinkUnavailable {
		t.Fatalf("unexpected retry state: %+v", snapshot)
	}
	if snapshot.LeaseOwner != "" || snapshot.NextAttemptAt.IsZero() {
		t.Fatalf("failed pass did not release a durable backoff state: %+v", snapshot)
	}
}

func TestDispatcherRetriesSameObjectWhenAcknowledgementResponseIsLost(t *testing.T) {
	now := time.Date(2026, 7, 19, 4, 0, 0, 0, time.UTC)
	store := newMemoryOutbox(t, now, checkpointEvent(t, 1, now))
	store.ackErrors = 1
	sink := &recordingSink{}
	clockSource := clock.NewFake(now.Add(time.Second))
	dispatcher := newTestDispatcher(t, store, sink, allowVerifier{}, clockSource)
	if _, err := dispatcher.RunOnce(t.Context()); !errors.Is(err, ErrDispatchUnavailable) {
		t.Fatalf("first RunOnce() error = %v, want acknowledgement failure", err)
	}
	if store.consumer.Snapshot().LastAckedSequence != 0 || len(sink.objects) != 1 {
		t.Fatalf("lost acknowledgement changed durable state: consumer=%+v writes=%d", store.consumer.Snapshot(), len(sink.objects))
	}
	second, err := dispatcher.RunOnce(t.Context())
	if err != nil || second.Delivered != 1 {
		t.Fatalf("retry result=%+v err=%v", second, err)
	}
	if len(sink.objects) != 2 || !bytes.Equal(sink.objects[0].Content(), sink.objects[1].Content()) ||
		sink.objects[0].Key().String() != sink.objects[1].Key().String() {
		t.Fatal("acknowledgement retry did not reuse the exact checkpoint object")
	}
}

func TestDispatcherRejectsUnverifiablePayloadWithoutWritingOrAcking(t *testing.T) {
	now := time.Date(2026, 7, 19, 4, 0, 0, 0, time.UTC)
	store := newMemoryOutbox(t, now, checkpointEvent(t, 1, now))
	sink := &recordingSink{}
	dispatcher := newTestDispatcher(t, store, sink, denyVerifier{}, clock.NewFake(now.Add(time.Second)))

	_, err := dispatcher.RunOnce(t.Context())
	if !errors.Is(err, ErrDeliveryFailed) {
		t.Fatalf("RunOnce() error = %v, want ErrDeliveryFailed", err)
	}
	if len(sink.objects) != 0 || store.consumer.Snapshot().LastAckedSequence != 0 {
		t.Fatal("unverifiable checkpoint was written or acknowledged")
	}
	if store.consumer.Snapshot().LastErrorCode != ErrorCheckpointIntegrity {
		t.Fatalf("error code = %q", store.consumer.Snapshot().LastErrorCode)
	}
}

func TestDispatcherReleasesLeaseWhenNoCheckpointIsAvailable(t *testing.T) {
	now := time.Date(2026, 7, 19, 4, 0, 0, 0, time.UTC)
	store := newMemoryOutbox(t, now)
	dispatcher := newTestDispatcher(t, store, &recordingSink{}, allowVerifier{}, clock.NewFake(now.Add(time.Second)))

	result, err := dispatcher.RunOnce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.Delivered != 0 || !result.Idle || store.consumer.Snapshot().LeaseOwner != "" {
		t.Fatalf("idle result=%+v consumer=%+v", result, store.consumer.Snapshot())
	}
}

func newTestDispatcher(
	t *testing.T,
	unitOfWork outbox.UnitOfWork,
	sink objectstorage.Sink,
	verifier CheckpointVerifier,
	source clock.Clock,
) *Dispatcher {
	t.Helper()
	dispatcher, err := NewDispatcher(Config{
		Owner: "worker-test", LeaseDuration: time.Minute, BatchSize: 10,
	}, unitOfWork, sink, verifier, source)
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

type allowVerifier struct{}

func (allowVerifier) VerifyCheckpoint(audit.Checkpoint) error { return nil }

type denyVerifier struct{}

func (denyVerifier) VerifyCheckpoint(audit.Checkpoint) error { return audit.ErrIntegrity }

type recordingSink struct {
	objects  []objectstorage.Object
	writeErr error
}

func (sink *recordingSink) Write(_ context.Context, object objectstorage.Object) error {
	if sink.writeErr != nil {
		return sink.writeErr
	}
	sink.objects = append(sink.objects, object)
	return nil
}

func (*recordingSink) CheckProductionReady(context.Context) error { return nil }

type memoryOutbox struct {
	consumer  outbox.Consumer
	events    []outbox.Event
	ackErrors int
}

func newMemoryOutbox(t *testing.T, createdAt time.Time, events ...outbox.Event) *memoryOutbox {
	t.Helper()
	subscription, err := outbox.NewSubscription(outbox.EventTypeAuditCheckpointPending)
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := outbox.NewConsumer(outbox.ConsumerIDAuditCheckpoint, subscription, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	return &memoryOutbox{consumer: consumer, events: events}
}

func (store *memoryOutbox) Run(ctx context.Context, work outbox.TransactionWork) error {
	return work(ctx, memoryTransaction{store: store})
}

type memoryTransaction struct{ store *memoryOutbox }

func (transaction memoryTransaction) Events() outbox.EventRepository {
	return memoryEventRepository{store: transaction.store}
}
func (transaction memoryTransaction) Consumers() outbox.ConsumerRepository {
	return memoryConsumerRepository{store: transaction.store}
}

type memoryEventRepository struct{ store *memoryOutbox }

func (memoryEventRepository) Insert(context.Context, outbox.Event) (outbox.Event, error) {
	return outbox.Event{}, errors.New("unexpected event insert")
}

type memoryConsumerRepository struct{ store *memoryOutbox }

func (repository memoryConsumerRepository) Insert(_ context.Context, requested outbox.Consumer) (outbox.Consumer, error) {
	return outbox.ResolveRegistration(repository.store.consumer, requested)
}

func (repository memoryConsumerRepository) Get(_ context.Context, id outbox.ConsumerID) (outbox.Consumer, error) {
	if id != outbox.ConsumerIDAuditCheckpoint {
		return outbox.Consumer{}, outbox.ErrNotFound
	}
	return repository.store.consumer, nil
}

func (repository memoryConsumerRepository) AcquireLeaseCAS(_ context.Context, transition outbox.ConsumerCAS) (outbox.Consumer, error) {
	return repository.store.apply(transition)
}

func (repository memoryConsumerRepository) RenewLeaseCAS(_ context.Context, transition outbox.ConsumerCAS) (outbox.Consumer, error) {
	return repository.store.apply(transition)
}

func (repository memoryConsumerRepository) ReleaseLeaseCAS(_ context.Context, transition outbox.ConsumerCAS) (outbox.Consumer, error) {
	return repository.store.apply(transition)
}

func (repository memoryConsumerRepository) AcknowledgeCAS(_ context.Context, transition outbox.ConsumerCAS) (outbox.Consumer, error) {
	if repository.store.ackErrors > 0 {
		repository.store.ackErrors--
		return outbox.Consumer{}, errors.New("acknowledgement response lost")
	}
	return repository.store.apply(transition)
}

func (repository memoryConsumerRepository) RecordRetryCAS(_ context.Context, transition outbox.ConsumerCAS) (outbox.Consumer, error) {
	return repository.store.apply(transition)
}

func (repository memoryConsumerRepository) ListAvailable(_ context.Context, batch outbox.EventBatch) ([]outbox.Event, error) {
	snapshot := repository.store.consumer.Snapshot()
	if snapshot.LeaseOwner != batch.LeaseOwner || !snapshot.LeaseUntil.After(batch.ReadAt) {
		return nil, outbox.ErrLeaseNotOwned
	}
	available := make([]outbox.Event, 0, batch.BatchSize)
	for _, event := range repository.store.events {
		eventSnapshot := event.Snapshot()
		if eventSnapshot.Sequence > snapshot.LastAckedSequence && eventSnapshot.AvailableAt.After(batch.ReadAt) == false {
			available = append(available, event)
		}
		if len(available) == int(batch.BatchSize) {
			break
		}
	}
	return available, nil
}

func (store *memoryOutbox) apply(transition outbox.ConsumerCAS) (outbox.Consumer, error) {
	if !reflect.DeepEqual(store.consumer.Snapshot(), transition.Expected()) {
		return outbox.Consumer{}, outbox.ErrConcurrentTransition
	}
	next, err := outbox.RestoreConsumer(transition.Next())
	if err != nil {
		return outbox.Consumer{}, err
	}
	store.consumer = next
	return next, nil
}

func checkpointEvent(t *testing.T, sequence int64, createdAt time.Time) outbox.Event {
	t.Helper()
	hash, err := audit.NewHash(bytes.Repeat([]byte{0x42}, audit.HashSize))
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := audit.RestoreCheckpoint(audit.CheckpointSnapshot{
		ChainID: audit.ChainAdmin, Sequence: uint64(sequence), ChainHash: hash,
		Signature: bytes.Repeat([]byte{0x24}, audit.SignatureSize), SigningKeyVersion: 3, CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	event, err := outbox.NewEvent(
		uuid.New(), outbox.EventTypeAuditCheckpointPending, outbox.AggregateTypeAuditChain,
		uuid.New(), checkpoint.Snapshot().CanonicalPayload(), createdAt, createdAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := event.Snapshot()
	position, err := outbox.NewSequence(sequence)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Sequence = position
	event, err = outbox.RestoreEvent(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return event
}
