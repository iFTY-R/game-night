package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/jackc/pgx/v5"
)

type fakeOutboxEventQueries struct {
	createRow sqlcgen.OutboxEvent
	createErr error
	getRow    sqlcgen.OutboxEvent
	getErr    error
}

func (fake *fakeOutboxEventQueries) CreateOutboxEvent(context.Context, sqlcgen.CreateOutboxEventParams) (sqlcgen.OutboxEvent, error) {
	return fake.createRow, fake.createErr
}

func (fake *fakeOutboxEventQueries) GetOutboxEventByID(context.Context, sqlcgen.GetOutboxEventByIDParams) (sqlcgen.OutboxEvent, error) {
	return fake.getRow, fake.getErr
}

type fakeOutboxConsumerQueries struct {
	acquireParams sqlcgen.AcquireOutboxConsumerLeaseCASParams
	acquireRow    sqlcgen.OutboxConsumer
	acquireErr    error
}

func (*fakeOutboxConsumerQueries) CreateOutboxConsumer(context.Context, sqlcgen.CreateOutboxConsumerParams) (sqlcgen.OutboxConsumer, error) {
	panic("unexpected CreateOutboxConsumer")
}

func (*fakeOutboxConsumerQueries) GetOutboxConsumer(context.Context, sqlcgen.GetOutboxConsumerParams) (sqlcgen.OutboxConsumer, error) {
	panic("unexpected GetOutboxConsumer")
}

func (fake *fakeOutboxConsumerQueries) AcquireOutboxConsumerLeaseCAS(_ context.Context, params sqlcgen.AcquireOutboxConsumerLeaseCASParams) (sqlcgen.OutboxConsumer, error) {
	fake.acquireParams = params
	return fake.acquireRow, fake.acquireErr
}

func (*fakeOutboxConsumerQueries) RenewOutboxConsumerLeaseCAS(context.Context, sqlcgen.RenewOutboxConsumerLeaseCASParams) (sqlcgen.OutboxConsumer, error) {
	panic("unexpected RenewOutboxConsumerLeaseCAS")
}

func (*fakeOutboxConsumerQueries) ReleaseOutboxConsumerLeaseCAS(context.Context, sqlcgen.ReleaseOutboxConsumerLeaseCASParams) (sqlcgen.OutboxConsumer, error) {
	panic("unexpected ReleaseOutboxConsumerLeaseCAS")
}

func (*fakeOutboxConsumerQueries) ListOutboxEventsForConsumer(context.Context, sqlcgen.ListOutboxEventsForConsumerParams) ([]sqlcgen.ListOutboxEventsForConsumerRow, error) {
	panic("unexpected ListOutboxEventsForConsumer")
}

func (*fakeOutboxConsumerQueries) AckOutboxConsumerOffsetCAS(context.Context, sqlcgen.AckOutboxConsumerOffsetCASParams) (sqlcgen.OutboxConsumer, error) {
	panic("unexpected AckOutboxConsumerOffsetCAS")
}

func (*fakeOutboxConsumerQueries) RecordOutboxConsumerRetryCAS(context.Context, sqlcgen.RecordOutboxConsumerRetryCASParams) (sqlcgen.OutboxConsumer, error) {
	panic("unexpected RecordOutboxConsumerRetryCAS")
}

func TestOutboxEventInsertClassifiesIdempotentReplay(t *testing.T) {
	event := repositoryOutboxEvent(t, "audit.checkpoint.pending")
	persisted := persistedOutboxRow(event, 7)
	repository := newOutboxEventRepository(&fakeOutboxEventQueries{createErr: pgx.ErrNoRows, getRow: persisted})

	result, err := repository.Insert(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot().Sequence != 7 {
		t.Fatalf("sequence = %d, want 7", result.Snapshot().Sequence)
	}
}

func TestOutboxEventInsertRejectsEventIDReuse(t *testing.T) {
	event := repositoryOutboxEvent(t, "audit.checkpoint.pending")
	persisted := persistedOutboxRow(event, 7)
	persisted.Payload = []byte("different")
	repository := newOutboxEventRepository(&fakeOutboxEventQueries{createErr: pgx.ErrNoRows, getRow: persisted})

	if _, err := repository.Insert(context.Background(), event); !errors.Is(err, outbox.ErrAlreadyExists) {
		t.Fatalf("insert error = %v, want already exists", err)
	}
}

func TestOutboxAcquireLeaseUsesCompleteExpectedSnapshot(t *testing.T) {
	createdAt := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	subscription, err := outbox.NewSubscription(outbox.EventTypeAuditCheckpointPending)
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := outbox.NewConsumer(outbox.ConsumerIDAuditCheckpoint, subscription, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := outbox.ParseLeaseOwner("worker-1")
	if err != nil {
		t.Fatal(err)
	}
	next, transition, err := consumer.AcquireLease(owner, createdAt.Add(time.Second), createdAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeOutboxConsumerQueries{acquireRow: outboxConsumerRow(next)}
	repository := newOutboxConsumerRepository(fake)

	result, err := repository.AcquireLeaseCAS(context.Background(), transition)
	if err != nil {
		t.Fatal(err)
	}
	if !sameConsumerSnapshot(result.Snapshot(), next.Snapshot()) {
		t.Fatal("persisted consumer differs from domain transition")
	}
	if fake.acquireParams.ExpectedLeaseOwner.Valid || fake.acquireParams.ExpectedLeaseUntil.Valid ||
		fake.acquireParams.ExpectedSequence != 0 || fake.acquireParams.ExpectedRetryCount != 0 ||
		!fake.acquireParams.ExpectedUpdatedAt.Time.Equal(createdAt) {
		t.Fatalf("incomplete expected CAS parameters: %+v", fake.acquireParams)
	}
}

func TestOutboxAcquireLeaseMapsLostCAS(t *testing.T) {
	createdAt := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	subscription, _ := outbox.NewSubscription(outbox.EventTypeAuditCheckpointPending)
	consumer, _ := outbox.NewConsumer(outbox.ConsumerIDAuditCheckpoint, subscription, createdAt)
	owner, _ := outbox.ParseLeaseOwner("worker-1")
	_, transition, _ := consumer.AcquireLease(owner, createdAt.Add(time.Second), createdAt.Add(time.Minute))
	repository := newOutboxConsumerRepository(&fakeOutboxConsumerQueries{acquireErr: pgx.ErrNoRows})

	if _, err := repository.AcquireLeaseCAS(context.Background(), transition); !errors.Is(err, outbox.ErrConcurrentTransition) {
		t.Fatalf("acquire error = %v, want concurrent transition", err)
	}
}

func repositoryOutboxEvent(t testing.TB, eventTypeValue string) outbox.Event {
	t.Helper()
	eventType, err := outbox.ParseEventType(eventTypeValue)
	if err != nil {
		t.Fatal(err)
	}
	aggregateType, err := outbox.ParseAggregateType("audit.chain")
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	event, err := outbox.NewEvent(uuid.New(), eventType, aggregateType, uuid.New(), []byte("payload"), createdAt, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func persistedOutboxRow(event outbox.Event, sequence int64) sqlcgen.OutboxEvent {
	snapshot := event.Snapshot()
	return sqlcgen.OutboxEvent{
		EventSequence: sequence, EventID: uuidToPG(snapshot.ID), EventType: snapshot.Type.Value(),
		AggregateType: snapshot.AggregateType.Value(), AggregateID: uuidToPG(snapshot.AggregateID), Payload: snapshot.Payload,
		CreatedAt: timeToPG(snapshot.CreatedAt), AvailableAt: timeToPG(snapshot.AvailableAt),
	}
}

func outboxConsumerRow(consumer outbox.Consumer) sqlcgen.OutboxConsumer {
	snapshot := consumer.Snapshot()
	sequence, _ := snapshot.LastAckedSequence.Int64()
	return sqlcgen.OutboxConsumer{
		ConsumerID: snapshot.ID.Value(), Subscriptions: eventTypeStrings(snapshot.Subscriptions),
		LastAckedSequence: sequence, LeaseOwner: textToPG(snapshot.LeaseOwner.Value()), LeaseUntil: timeToPG(snapshot.LeaseUntil),
		RetryCount: int32(snapshot.RetryCount), NextAttemptAt: timeToPG(snapshot.NextAttemptAt),
		LastErrorCode: textToPG(snapshot.LastErrorCode.Value()), CreatedAt: timeToPG(snapshot.CreatedAt), UpdatedAt: timeToPG(snapshot.UpdatedAt),
	}
}
