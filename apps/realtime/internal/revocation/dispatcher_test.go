package revocation

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
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/outbox"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
)

func TestDispatcherCompletesInboxBeforeAcknowledgingRevocation(t *testing.T) {
	now := time.Date(2026, time.July, 21, 16, 0, 0, 0, time.UTC)
	event := revocationEvent(t, 1, now)
	store := newMemoryRevocations(t, now, event)
	inbox := &recordingInbox{}
	dispatcher := newTestDispatcher(t, store, inbox, clock.NewFake(now.Add(time.Second)))

	result, err := dispatcher.RunOnce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	consumer := store.consumer.Snapshot()
	if result.Delivered != 1 || len(inbox.events) != 1 || consumer.LastAckedSequence != 1 || consumer.LeaseOwner != "" {
		t.Fatalf("result=%+v calls=%d consumer=%+v", result, len(inbox.events), consumer)
	}
}

func TestDispatcherPersistsRetryWithoutAckOnInboxFailure(t *testing.T) {
	now := time.Date(2026, time.July, 21, 16, 30, 0, 0, time.UTC)
	store := newMemoryRevocations(t, now, revocationEvent(t, 1, now))
	inbox := &recordingInbox{err: gameruntime.ErrGameSessionRepositoryUnavailable}
	dispatcher := newTestDispatcher(t, store, inbox, clock.NewFake(now.Add(time.Second)))

	result, err := dispatcher.RunOnce(t.Context())
	if !errors.Is(err, ErrDeliveryFailed) || result.Delivered != 0 {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	consumer := store.consumer.Snapshot()
	if consumer.LastAckedSequence != 0 || consumer.RetryCount != 1 || consumer.LastErrorCode != ErrorUnavailable ||
		consumer.LeaseOwner != "" || consumer.NextAttemptAt.IsZero() {
		t.Fatalf("consumer=%+v", consumer)
	}
}

func TestDispatcherClassifiesBrokenInboxBindingAsIntegrity(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "missing atomic inbox", err: gameruntime.ErrSystemInboxNotFound},
		{name: "payload digest conflict", err: idempotency.ErrConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, time.July, 21, 16, 45, 0, 0, time.UTC)
			store := newMemoryRevocations(t, now, revocationEvent(t, 1, now))
			dispatcher := newTestDispatcher(t, store, &recordingInbox{err: test.err}, clock.NewFake(now.Add(time.Second)))

			if _, err := dispatcher.RunOnce(t.Context()); !errors.Is(err, ErrDeliveryFailed) {
				t.Fatalf("dispatch error=%v", err)
			}
			if snapshot := store.consumer.Snapshot(); snapshot.LastErrorCode != ErrorIntegrity || snapshot.LastAckedSequence != 0 {
				t.Fatalf("consumer=%+v", snapshot)
			}
		})
	}
}

func TestDispatcherRetainsInvalidRevocationForOperationalRepair(t *testing.T) {
	now := time.Date(2026, time.July, 21, 17, 0, 0, 0, time.UTC)
	event := revocationEvent(t, 1, now)
	snapshot := event.Snapshot()
	snapshot.AggregateType = gameruntime.GameSessionAggregateType
	event, err := outbox.RestoreEvent(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	store := newMemoryRevocations(t, now, event)
	inbox := &recordingInbox{}
	dispatcher := newTestDispatcher(t, store, inbox, clock.NewFake(now.Add(time.Second)))

	_, err = dispatcher.RunOnce(t.Context())
	consumer := store.consumer.Snapshot()
	if !errors.Is(err, ErrDeliveryFailed) || len(inbox.events) != 0 ||
		consumer.LastErrorCode != ErrorIntegrity || consumer.LastAckedSequence != 0 {
		t.Fatalf("error=%v calls=%d consumer=%+v", err, len(inbox.events), consumer)
	}
}

func TestDispatcherRedeliversCompletedInboxAfterLostAcknowledgement(t *testing.T) {
	now := time.Date(2026, time.July, 21, 17, 30, 0, 0, time.UTC)
	event := revocationEvent(t, 1, now)
	store := newMemoryRevocations(t, now, event)
	store.ackErrors = 1
	inbox := &recordingInbox{}
	dispatcher := newTestDispatcher(t, store, inbox, clock.NewFake(now.Add(time.Second)))

	if _, err := dispatcher.RunOnce(t.Context()); !errors.Is(err, ErrDispatchUnavailable) {
		t.Fatalf("first error=%v", err)
	}
	second, err := dispatcher.RunOnce(t.Context())
	if err != nil || second.Delivered != 1 || len(inbox.events) != 2 ||
		inbox.events[0].Snapshot().ID != inbox.events[1].Snapshot().ID || store.consumer.Snapshot().LastAckedSequence != 1 {
		t.Fatalf("second=%+v error=%v calls=%d consumer=%+v", second, err, len(inbox.events), store.consumer.Snapshot())
	}
}

func newTestDispatcher(t testing.TB, store outbox.UnitOfWork, inbox Inbox, source clock.Clock) *Dispatcher {
	t.Helper()
	dispatcher, err := NewDispatcher(Config{
		Owner: "realtime-revocation-test", LeaseDuration: time.Minute, PollInterval: time.Second, BatchSize: 10,
	}, store, inbox, source, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

type recordingInbox struct {
	events []outbox.Event
	err    error
}

func (inbox *recordingInbox) Consume(_ context.Context, event outbox.Event) (gameruntime.SystemInboxConsumeResult, error) {
	inbox.events = append(inbox.events, event)
	if inbox.err != nil {
		return gameruntime.SystemInboxConsumeResult{}, inbox.err
	}
	return gameruntime.SystemInboxConsumeResult{Replayed: len(inbox.events) > 1}, nil
}

type memoryRevocations struct {
	consumer  outbox.Consumer
	events    []outbox.Event
	ackErrors int
}

func newMemoryRevocations(t testing.TB, createdAt time.Time, events ...outbox.Event) *memoryRevocations {
	t.Helper()
	subscription, err := revocationSubscription()
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := outbox.NewConsumer(outbox.ConsumerIDRoomParticipantRevocation, subscription, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	return &memoryRevocations{consumer: consumer, events: events}
}

func (store *memoryRevocations) Run(ctx context.Context, work outbox.TransactionWork) error {
	return work(ctx, memoryRevocationTransaction{store: store})
}

type memoryRevocationTransaction struct{ store *memoryRevocations }

func (transaction memoryRevocationTransaction) Events() outbox.EventRepository {
	return memoryRevocationEventRepository{}
}

func (transaction memoryRevocationTransaction) Consumers() outbox.ConsumerRepository {
	return memoryRevocationConsumerRepository{store: transaction.store}
}

type memoryRevocationEventRepository struct{}

func (memoryRevocationEventRepository) Insert(context.Context, outbox.Event) (outbox.Event, error) {
	return outbox.Event{}, errors.New("unexpected event insert")
}

type memoryRevocationConsumerRepository struct{ store *memoryRevocations }

func (repository memoryRevocationConsumerRepository) Insert(_ context.Context, requested outbox.Consumer) (outbox.Consumer, error) {
	return outbox.ResolveRegistration(repository.store.consumer, requested)
}

func (repository memoryRevocationConsumerRepository) Get(_ context.Context, id outbox.ConsumerID) (outbox.Consumer, error) {
	if id != outbox.ConsumerIDRoomParticipantRevocation {
		return outbox.Consumer{}, outbox.ErrNotFound
	}
	return repository.store.consumer, nil
}

func (repository memoryRevocationConsumerRepository) AcquireLeaseCAS(_ context.Context, transition outbox.ConsumerCAS) (outbox.Consumer, error) {
	return repository.store.apply(transition)
}

func (repository memoryRevocationConsumerRepository) RenewLeaseCAS(_ context.Context, transition outbox.ConsumerCAS) (outbox.Consumer, error) {
	return repository.store.apply(transition)
}

func (repository memoryRevocationConsumerRepository) ReleaseLeaseCAS(_ context.Context, transition outbox.ConsumerCAS) (outbox.Consumer, error) {
	return repository.store.apply(transition)
}

func (repository memoryRevocationConsumerRepository) AcknowledgeCAS(_ context.Context, transition outbox.ConsumerCAS) (outbox.Consumer, error) {
	if repository.store.ackErrors > 0 {
		repository.store.ackErrors--
		return outbox.Consumer{}, errors.New("acknowledgement response lost")
	}
	return repository.store.apply(transition)
}

func (repository memoryRevocationConsumerRepository) RecordRetryCAS(_ context.Context, transition outbox.ConsumerCAS) (outbox.Consumer, error) {
	return repository.store.apply(transition)
}

func (repository memoryRevocationConsumerRepository) ListAvailable(_ context.Context, batch outbox.EventBatch) ([]outbox.Event, error) {
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

func (store *memoryRevocations) apply(transition outbox.ConsumerCAS) (outbox.Consumer, error) {
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

func revocationEvent(t testing.TB, sequence int64, createdAt time.Time) outbox.Event {
	t.Helper()
	event, err := roomDomain.NewParticipantRevokedEvent(roomDomain.ParticipantRevocationFact{
		EventID: uuid.New(), RoomID: uuid.New(), SessionID: uuid.New(), UserID: uuid.New(),
		ActorKind: roomDomain.RemovalActorHost, ActorID: uuid.New(), Reason: roomDomain.RemovalReasonHostRemoved,
		MembershipVersion: 2, OccurredAt: createdAt,
	})
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

var _ Inbox = (*recordingInbox)(nil)
var _ outbox.UnitOfWork = (*memoryRevocations)(nil)
