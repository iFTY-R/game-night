package fanout

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/clock"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/outbox"
	redisstore "github.com/iFTY-R/game-night/platform/persistence/redis"
)

func TestDispatcherPublishesBeforeAcknowledgingDurableCursor(t *testing.T) {
	now := time.Date(2026, time.July, 20, 21, 0, 0, 0, time.UTC)
	event := fanoutEvent(t, 1, now)
	store := newMemoryFanout(t, now, event)
	publisher := &recordingPublisher{}
	dispatcher := newTestDispatcher(t, store, publisher, clock.NewFake(now.Add(time.Second)))
	result, err := dispatcher.RunOnce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.Delivered != 1 || len(publisher.events) != 1 || store.consumer.Snapshot().LastAckedSequence != 1 ||
		store.consumer.Snapshot().LeaseOwner != "" {
		t.Fatalf("result=%+v events=%+v consumer=%+v", result, publisher.events, store.consumer.Snapshot())
	}
}

func TestDispatcherPersistsRetryWithoutAckOnRedisFailure(t *testing.T) {
	now := time.Date(2026, time.July, 20, 21, 30, 0, 0, time.UTC)
	store := newMemoryFanout(t, now, fanoutEvent(t, 1, now))
	dispatcher := newTestDispatcher(t, store, &recordingPublisher{err: redisstore.ErrCoordinationUnavailable}, clock.NewFake(now.Add(time.Second)))
	result, err := dispatcher.RunOnce(t.Context())
	if !errors.Is(err, ErrDeliveryFailed) || result.Delivered != 0 {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	snapshot := store.consumer.Snapshot()
	if snapshot.LastAckedSequence != 0 || snapshot.RetryCount != 1 || snapshot.LastErrorCode != ErrorRedisUnavailable ||
		snapshot.LeaseOwner != "" || snapshot.NextAttemptAt.IsZero() {
		t.Fatalf("consumer=%+v", snapshot)
	}
}

func TestDispatcherRetainsCorruptGameEventForOperationalRepair(t *testing.T) {
	now := time.Date(2026, time.July, 20, 22, 0, 0, 0, time.UTC)
	event := fanoutEvent(t, 1, now)
	snapshot := event.Snapshot()
	snapshot.Payload = []byte(`{"sessionId":"00000000-0000-0000-0000-000000000000","stateVersion":1}`)
	event, err := outbox.RestoreEvent(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	store := newMemoryFanout(t, now, event)
	publisher := &recordingPublisher{}
	dispatcher := newTestDispatcher(t, store, publisher, clock.NewFake(now.Add(time.Second)))
	_, err = dispatcher.RunOnce(t.Context())
	if !errors.Is(err, ErrDeliveryFailed) || len(publisher.events) != 0 ||
		store.consumer.Snapshot().LastErrorCode != ErrorIntegrity || store.consumer.Snapshot().LastAckedSequence != 0 {
		t.Fatalf("error=%v events=%+v consumer=%+v", err, publisher.events, store.consumer.Snapshot())
	}
}

func TestDispatcherRepeatsSafeWakeupAfterLostAcknowledgementResponse(t *testing.T) {
	now := time.Date(2026, time.July, 20, 22, 30, 0, 0, time.UTC)
	store := newMemoryFanout(t, now, fanoutEvent(t, 1, now))
	store.ackErrors = 1
	publisher := &recordingPublisher{}
	dispatcher := newTestDispatcher(t, store, publisher, clock.NewFake(now.Add(time.Second)))
	if _, err := dispatcher.RunOnce(t.Context()); !errors.Is(err, ErrDispatchUnavailable) {
		t.Fatalf("first error = %v", err)
	}
	second, err := dispatcher.RunOnce(t.Context())
	if err != nil || second.Delivered != 1 || len(publisher.events) != 2 || publisher.events[0] != publisher.events[1] {
		t.Fatalf("second=%+v error=%v events=%+v", second, err, publisher.events)
	}
}

func newTestDispatcher(t testing.TB, store outbox.UnitOfWork, publisher Publisher, source clock.Clock) *Dispatcher {
	t.Helper()
	dispatcher, err := NewDispatcher(Config{
		Owner: "realtime-test", LeaseDuration: time.Minute, PollInterval: time.Second, BatchSize: 10,
	}, store, publisher, source, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

type recordingPublisher struct {
	events []redisstore.SessionFanoutEvent
	err    error
}

func (publisher *recordingPublisher) PublishSessionFanout(_ context.Context, event redisstore.SessionFanoutEvent) error {
	if publisher.err != nil {
		return publisher.err
	}
	publisher.events = append(publisher.events, event)
	return nil
}

type memoryFanout struct {
	consumer  outbox.Consumer
	events    []outbox.Event
	ackErrors int
}

func newMemoryFanout(t testing.TB, createdAt time.Time, events ...outbox.Event) *memoryFanout {
	t.Helper()
	subscription, err := sessionSubscription()
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := outbox.NewConsumer(outbox.ConsumerIDGameSessionFanout, subscription, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	return &memoryFanout{consumer: consumer, events: events}
}

func (store *memoryFanout) Run(ctx context.Context, work outbox.TransactionWork) error {
	return work(ctx, memoryFanoutTransaction{store: store})
}

type memoryFanoutTransaction struct{ store *memoryFanout }

func (transaction memoryFanoutTransaction) Events() outbox.EventRepository {
	return memoryFanoutEventRepository{}
}

func (transaction memoryFanoutTransaction) Consumers() outbox.ConsumerRepository {
	return memoryFanoutConsumerRepository{store: transaction.store}
}

type memoryFanoutEventRepository struct{}

func (memoryFanoutEventRepository) Insert(context.Context, outbox.Event) (outbox.Event, error) {
	return outbox.Event{}, errors.New("unexpected event insert")
}

type memoryFanoutConsumerRepository struct{ store *memoryFanout }

func (repository memoryFanoutConsumerRepository) Insert(_ context.Context, requested outbox.Consumer) (outbox.Consumer, error) {
	return outbox.ResolveRegistration(repository.store.consumer, requested)
}

func (repository memoryFanoutConsumerRepository) Get(_ context.Context, id outbox.ConsumerID) (outbox.Consumer, error) {
	if id != outbox.ConsumerIDGameSessionFanout {
		return outbox.Consumer{}, outbox.ErrNotFound
	}
	return repository.store.consumer, nil
}

func (repository memoryFanoutConsumerRepository) AcquireLeaseCAS(_ context.Context, transition outbox.ConsumerCAS) (outbox.Consumer, error) {
	return repository.store.apply(transition)
}

func (repository memoryFanoutConsumerRepository) RenewLeaseCAS(_ context.Context, transition outbox.ConsumerCAS) (outbox.Consumer, error) {
	return repository.store.apply(transition)
}

func (repository memoryFanoutConsumerRepository) ReleaseLeaseCAS(_ context.Context, transition outbox.ConsumerCAS) (outbox.Consumer, error) {
	return repository.store.apply(transition)
}

func (repository memoryFanoutConsumerRepository) AcknowledgeCAS(_ context.Context, transition outbox.ConsumerCAS) (outbox.Consumer, error) {
	if repository.store.ackErrors > 0 {
		repository.store.ackErrors--
		return outbox.Consumer{}, errors.New("acknowledgement response lost")
	}
	return repository.store.apply(transition)
}

func (repository memoryFanoutConsumerRepository) RecordRetryCAS(_ context.Context, transition outbox.ConsumerCAS) (outbox.Consumer, error) {
	return repository.store.apply(transition)
}

func (repository memoryFanoutConsumerRepository) ListAvailable(_ context.Context, batch outbox.EventBatch) ([]outbox.Event, error) {
	snapshot := repository.store.consumer.Snapshot()
	if snapshot.LeaseOwner != batch.LeaseOwner || !snapshot.LeaseUntil.After(batch.ReadAt) {
		return nil, outbox.ErrLeaseNotOwned
	}
	available := make([]outbox.Event, 0, batch.BatchSize)
	for _, event := range repository.store.events {
		eventSnapshot := event.Snapshot()
		if eventSnapshot.Sequence > snapshot.LastAckedSequence && !eventSnapshot.AvailableAt.After(batch.ReadAt) {
			available = append(available, event)
		}
		if len(available) == int(batch.BatchSize) {
			break
		}
	}
	return available, nil
}

func (store *memoryFanout) apply(transition outbox.ConsumerCAS) (outbox.Consumer, error) {
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

func fanoutEvent(t testing.TB, sequence int64, createdAt time.Time) outbox.Event {
	t.Helper()
	sessionID := uuid.New()
	payload, err := gameruntime.MarshalSessionNotification(gameruntime.SessionNotification{
		SessionID: sessionID, StateVersion: uint64(sequence),
	})
	if err != nil {
		t.Fatal(err)
	}
	event, err := outbox.NewEvent(
		uuid.New(), gameruntime.GameSessionTransitionedEventType, gameruntime.GameSessionAggregateType,
		sessionID, payload, createdAt, createdAt,
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
