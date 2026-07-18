package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/internal/integrationtest"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
)

// auditOutboxIntegrationTimeout covers isolated migrations and serialized audit-function checks on CI.
const auditOutboxIntegrationTimeout = 90 * time.Second

func TestAuditOutboxRepositoriesIntegration(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), auditOutboxIntegrationTimeout)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	service, err := audit.NewService(newRepositoryAuditKeyring())
	if err != nil {
		t.Fatal(err)
	}

	t.Run("head conflict maps to retryable domain error", func(t *testing.T) {
		repository := newAuditRepository(sqlcgen.New(fixture.Pool), service)
		head, err := repository.ReadHead(ctx, audit.ChainAdmin)
		if err != nil {
			t.Fatal(err)
		}
		first := prepareRepositoryAuditEvent(t, service, head, "conflict-first")
		second := prepareRepositoryAuditEvent(t, service, head, "conflict-second")
		if _, err := repository.AppendEvent(ctx, audit.AppendRequest{ExpectedHead: head, Event: first}); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.AppendEvent(ctx, audit.AppendRequest{ExpectedHead: head, Event: second}); !errors.Is(err, audit.ErrHeadConflict) {
			t.Fatalf("second append error = %v, want head conflict", err)
		}
	})

	var committedEventID uuid.UUID
	var committedCheckpoint audit.Checkpoint
	t.Run("audit append and checkpoint outbox commit atomically", func(t *testing.T) {
		unitOfWork := NewAuditOutboxUnitOfWork(fixture.Pool, service)
		err := unitOfWork.Run(ctx, func(ctx context.Context, transaction audit.Transaction) error {
			head, err := transaction.Audit().ReadHead(ctx, audit.ChainAdmin)
			if err != nil {
				return err
			}
			event := prepareRepositoryAuditEvent(t, service, head, "atomic-commit")
			committedEventID = event.Snapshot().Event.EventID
			next, err := transaction.Audit().AppendEvent(ctx, audit.AppendRequest{ExpectedHead: head, Event: event})
			if err != nil {
				return err
			}
			checkpoint, err := service.PrepareCheckpoint(next, next.UpdatedAt().Add(time.Microsecond))
			if err != nil {
				return err
			}
			committedCheckpoint = checkpoint
			return transaction.Checkpoints().AppendPendingCheckpoint(ctx, checkpoint)
		})
		if err != nil {
			t.Fatal(err)
		}
		if countRowsByUUID(t, ctx, fixture, "audit_events", "event_id", committedEventID) != 1 {
			t.Fatal("committed audit event is missing")
		}
		if countRowsByBytes(t, ctx, fixture, "outbox_events", "payload", committedCheckpoint.Snapshot().CanonicalPayload()) != 1 {
			t.Fatal("checkpoint outbox event did not commit with audit append")
		}
	})

	t.Run("checkpoint progress follows durable consumer acknowledgement", func(t *testing.T) {
		consumerRepository := newOutboxConsumerRepository(sqlcgen.New(fixture.Pool))
		consumer, err := consumerRepository.Get(ctx, outbox.ConsumerIDAuditCheckpoint)
		if err != nil {
			t.Fatal(err)
		}
		owner, _ := outbox.ParseLeaseOwner("checkpoint-worker")
		acquiredAt := time.Now().UTC()
		leased, transition, err := consumer.AcquireLease(owner, acquiredAt, acquiredAt.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		leased, err = consumerRepository.AcquireLeaseCAS(ctx, transition)
		if err != nil {
			t.Fatal(err)
		}
		batch, err := outbox.NewEventBatch(outbox.ConsumerIDAuditCheckpoint, owner, acquiredAt, 100)
		if err != nil {
			t.Fatal(err)
		}
		events, err := consumerRepository.ListAvailable(ctx, batch)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 {
			t.Fatalf("available checkpoints = %d, want 1", len(events))
		}
		_, ackTransition, err := leased.Acknowledge(owner, leased.Snapshot().LastAckedSequence, events[0].Snapshot().Sequence, acquiredAt.Add(time.Microsecond))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := consumerRepository.AcknowledgeCAS(ctx, ackTransition); err != nil {
			t.Fatal(err)
		}
		progress, err := newAuditCheckpointRepository(sqlcgen.New(fixture.Pool), service).ReadCheckpointProgress(ctx, audit.ChainAdmin)
		if err != nil {
			t.Fatal(err)
		}
		if progress.AcknowledgedSequence != committedCheckpoint.Snapshot().Sequence || !progress.UncheckpointedSince.IsZero() {
			t.Fatalf("checkpoint progress = %+v", progress)
		}
	})

	t.Run("callback failure rolls back business audit and outbox writes", func(t *testing.T) {
		userID := uuid.New()
		var eventID uuid.UUID
		var checkpointPayload []byte
		workError := errors.New("reject authoritative mutation")
		runner := NewTransactionRunner(fixture.Pool)
		err := runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
			if _, err := queries.CreateUser(ctx, createUserParams(userID)); err != nil {
				return err
			}
			participants := newAuditOutboxTransaction(queries, service)
			head, err := participants.Audit().ReadHead(ctx, audit.ChainAdmin)
			if err != nil {
				return err
			}
			event := prepareRepositoryAuditEvent(t, service, head, "atomic-rollback")
			eventID = event.Snapshot().Event.EventID
			next, err := participants.Audit().AppendEvent(ctx, audit.AppendRequest{ExpectedHead: head, Event: event})
			if err != nil {
				return err
			}
			checkpoint, err := service.PrepareCheckpoint(next, next.UpdatedAt().Add(time.Microsecond))
			if err != nil {
				return err
			}
			checkpointPayload = checkpoint.Snapshot().CanonicalPayload()
			if err := participants.Checkpoints().AppendPendingCheckpoint(ctx, checkpoint); err != nil {
				return err
			}
			return workError
		})
		if !errors.Is(err, workError) {
			t.Fatalf("transaction error = %v, want callback error", err)
		}
		if countUsers(t, ctx, fixture, userID) != 0 ||
			countRowsByUUID(t, ctx, fixture, "audit_events", "event_id", eventID) != 0 ||
			countRowsByBytes(t, ctx, fixture, "outbox_events", "payload", checkpointPayload) != 0 {
			t.Fatal("callback failure left a partially committed business, audit, or outbox write")
		}
	})

	t.Run("consumer offsets and leases remain independent", func(t *testing.T) {
		eventTypeA, _ := outbox.ParseEventType("test.event_a")
		eventTypeB, _ := outbox.ParseEventType("test.event_b")
		aggregateType, _ := outbox.ParseAggregateType("test.aggregate")
		createdAt := time.Now().UTC()
		eventA, _ := outbox.NewEvent(uuid.New(), eventTypeA, aggregateType, uuid.New(), []byte("a"), createdAt, createdAt)
		eventB, _ := outbox.NewEvent(uuid.New(), eventTypeB, aggregateType, uuid.New(), []byte("b"), createdAt, createdAt)
		eventRepository := newOutboxEventRepository(sqlcgen.New(fixture.Pool))
		persistedA, err := eventRepository.Insert(ctx, eventA)
		if err != nil {
			t.Fatal(err)
		}
		persistedB, err := eventRepository.Insert(ctx, eventB)
		if err != nil {
			t.Fatal(err)
		}

		consumerRepository := newOutboxConsumerRepository(sqlcgen.New(fixture.Pool))
		consumerA := registerIntegrationConsumer(t, ctx, consumerRepository, "test.consumer_a", eventTypeA, createdAt)
		consumerB := registerIntegrationConsumer(t, ctx, consumerRepository, "test.consumer_b", eventTypeB, createdAt)
		ackedA := leaseAndAckIntegrationConsumer(t, ctx, consumerRepository, consumerA, "worker-a", persistedA)
		beforeB, err := consumerRepository.Get(ctx, consumerB.Snapshot().ID)
		if err != nil {
			t.Fatal(err)
		}
		if beforeB.Snapshot().LastAckedSequence != 0 || beforeB.Snapshot().LeaseOwner != "" {
			t.Fatal("consumer A transition changed consumer B state")
		}
		ackedB := leaseAndAckIntegrationConsumer(t, ctx, consumerRepository, beforeB, "worker-b", persistedB)
		if ackedA.Snapshot().LastAckedSequence == ackedB.Snapshot().LastAckedSequence {
			t.Fatal("independent consumers unexpectedly share an offset")
		}
	})

	t.Run("consumer cannot skip an available subscribed event", func(t *testing.T) {
		eventType, _ := outbox.ParseEventType("test.ordered")
		aggregateType, _ := outbox.ParseAggregateType("test.aggregate")
		createdAt := time.Now().UTC()
		eventRepository := newOutboxEventRepository(sqlcgen.New(fixture.Pool))
		first, _ := outbox.NewEvent(uuid.New(), eventType, aggregateType, uuid.New(), []byte("first"), createdAt, createdAt)
		second, _ := outbox.NewEvent(uuid.New(), eventType, aggregateType, uuid.New(), []byte("second"), createdAt, createdAt)
		persistedFirst, err := eventRepository.Insert(ctx, first)
		if err != nil {
			t.Fatal(err)
		}
		persistedSecond, err := eventRepository.Insert(ctx, second)
		if err != nil {
			t.Fatal(err)
		}

		consumerRepository := newOutboxConsumerRepository(sqlcgen.New(fixture.Pool))
		consumer := registerIntegrationConsumer(t, ctx, consumerRepository, "test.ordered_consumer", eventType, createdAt)
		owner, _ := outbox.ParseLeaseOwner("ordered-worker")
		acquiredAt := time.Now().UTC()
		leased, transition, err := consumer.AcquireLease(owner, acquiredAt, acquiredAt.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		leased, err = consumerRepository.AcquireLeaseCAS(ctx, transition)
		if err != nil {
			t.Fatal(err)
		}
		_, skipTransition, err := leased.Acknowledge(
			owner, leased.Snapshot().LastAckedSequence, persistedSecond.Snapshot().Sequence, acquiredAt.Add(time.Microsecond),
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := consumerRepository.AcknowledgeCAS(ctx, skipTransition); !errors.Is(err, outbox.ErrConcurrentTransition) {
			t.Fatalf("skip error = %v, want concurrent transition", err)
		}
		_, firstTransition, err := leased.Acknowledge(
			owner, leased.Snapshot().LastAckedSequence, persistedFirst.Snapshot().Sequence, acquiredAt.Add(2*time.Microsecond),
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := consumerRepository.AcknowledgeCAS(ctx, firstTransition); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("outbox sequence allocation is serialized until producer commit", func(t *testing.T) {
		eventType, _ := outbox.ParseEventType("test.commit_order")
		aggregateType, _ := outbox.ParseAggregateType("test.aggregate")
		createdAt := time.Now().UTC()
		first, _ := outbox.NewEvent(uuid.New(), eventType, aggregateType, uuid.New(), []byte("first"), createdAt, createdAt)
		second, _ := outbox.NewEvent(uuid.New(), eventType, aggregateType, uuid.New(), []byte("second"), createdAt, createdAt)

		firstTransaction, err := fixture.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer firstTransaction.Rollback(ctx)
		persistedFirst, err := newOutboxEventRepository(sqlcgen.New(firstTransaction)).Insert(ctx, first)
		if err != nil {
			t.Fatal(err)
		}

		type insertionResult struct {
			event outbox.Event
			err   error
		}
		result := make(chan insertionResult, 1)
		go func() {
			secondTransaction, beginErr := fixture.Pool.Begin(ctx)
			if beginErr != nil {
				result <- insertionResult{err: beginErr}
				return
			}
			persisted, insertErr := newOutboxEventRepository(sqlcgen.New(secondTransaction)).Insert(ctx, second)
			if insertErr == nil {
				insertErr = secondTransaction.Commit(ctx)
			} else {
				_ = secondTransaction.Rollback(ctx)
			}
			result <- insertionResult{event: persisted, err: insertErr}
		}()

		select {
		case early := <-result:
			t.Fatalf("second producer committed before lower sequence transaction: %v", early.err)
		case <-time.After(100 * time.Millisecond):
		}
		if err := firstTransaction.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		select {
		case completed := <-result:
			if completed.err != nil {
				t.Fatal(completed.err)
			}
			if completed.event.Snapshot().Sequence <= persistedFirst.Snapshot().Sequence {
				t.Fatal("serialized producer did not receive a later sequence")
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	})

	t.Run("delayed subscribed event blocks later offset", func(t *testing.T) {
		eventType, _ := outbox.ParseEventType("test.delayed")
		aggregateType, _ := outbox.ParseAggregateType("test.aggregate")
		createdAt := time.Now().UTC()
		first, _ := outbox.NewEvent(uuid.New(), eventType, aggregateType, uuid.New(), []byte("delayed"), createdAt, createdAt.Add(time.Minute))
		second, _ := outbox.NewEvent(uuid.New(), eventType, aggregateType, uuid.New(), []byte("ready"), createdAt, createdAt)
		eventRepository := newOutboxEventRepository(sqlcgen.New(fixture.Pool))
		_, err := eventRepository.Insert(ctx, first)
		if err != nil {
			t.Fatal(err)
		}
		persistedSecond, err := eventRepository.Insert(ctx, second)
		if err != nil {
			t.Fatal(err)
		}

		consumerRepository := newOutboxConsumerRepository(sqlcgen.New(fixture.Pool))
		consumer := registerIntegrationConsumer(t, ctx, consumerRepository, "test.delayed_consumer", eventType, createdAt)
		owner, _ := outbox.ParseLeaseOwner("delayed-worker")
		acquiredAt := time.Now().UTC()
		leased, transition, _ := consumer.AcquireLease(owner, acquiredAt, acquiredAt.Add(time.Minute))
		leased, err = consumerRepository.AcquireLeaseCAS(ctx, transition)
		if err != nil {
			t.Fatal(err)
		}
		batch, _ := outbox.NewEventBatch(leased.Snapshot().ID, owner, acquiredAt, 10)
		available, err := consumerRepository.ListAvailable(ctx, batch)
		if err != nil {
			t.Fatal(err)
		}
		if len(available) != 0 {
			t.Fatal("later event bypassed an unavailable subscribed event")
		}
		_, ackTransition, _ := leased.Acknowledge(
			owner, leased.Snapshot().LastAckedSequence, persistedSecond.Snapshot().Sequence, acquiredAt.Add(time.Microsecond),
		)
		if _, err := consumerRepository.AcknowledgeCAS(ctx, ackTransition); !errors.Is(err, outbox.ErrConcurrentTransition) {
			t.Fatalf("delayed skip error = %v, want concurrent transition", err)
		}
	})

	t.Run("retry backoff blocks list while lease remains owned", func(t *testing.T) {
		eventType, _ := outbox.ParseEventType("test.retry")
		aggregateType, _ := outbox.ParseAggregateType("test.aggregate")
		createdAt := time.Now().UTC()
		event, _ := outbox.NewEvent(uuid.New(), eventType, aggregateType, uuid.New(), []byte("retry"), createdAt, createdAt)
		if _, err := newOutboxEventRepository(sqlcgen.New(fixture.Pool)).Insert(ctx, event); err != nil {
			t.Fatal(err)
		}
		consumerRepository := newOutboxConsumerRepository(sqlcgen.New(fixture.Pool))
		consumer := registerIntegrationConsumer(t, ctx, consumerRepository, "test.retry_consumer", eventType, createdAt)
		owner, _ := outbox.ParseLeaseOwner("retry-worker")
		acquiredAt := time.Now().UTC()
		leased, transition, _ := consumer.AcquireLease(owner, acquiredAt, acquiredAt.Add(time.Minute))
		leased, err := consumerRepository.AcquireLeaseCAS(ctx, transition)
		if err != nil {
			t.Fatal(err)
		}
		errorCode, _ := outbox.ParseErrorCode("test.delivery_failure")
		retrying, retryTransition, _ := leased.RecordRetry(owner, errorCode, acquiredAt.Add(time.Microsecond))
		retrying, err = consumerRepository.RecordRetryCAS(ctx, retryTransition)
		if err != nil {
			t.Fatal(err)
		}
		batch, _ := outbox.NewEventBatch(retrying.Snapshot().ID, owner, time.Now().UTC(), 10)
		available, err := consumerRepository.ListAvailable(ctx, batch)
		if err != nil {
			t.Fatal(err)
		}
		if len(available) != 0 {
			t.Fatal("durable retry backoff was bypassed by the existing lease owner")
		}
	})

	t.Run("expired lease rejects a preconstructed acknowledgement", func(t *testing.T) {
		eventType, _ := outbox.ParseEventType("test.expired_lease")
		aggregateType, _ := outbox.ParseAggregateType("test.aggregate")
		createdAt := time.Now().UTC()
		event, _ := outbox.NewEvent(uuid.New(), eventType, aggregateType, uuid.New(), []byte("expired"), createdAt, createdAt)
		persisted, err := newOutboxEventRepository(sqlcgen.New(fixture.Pool)).Insert(ctx, event)
		if err != nil {
			t.Fatal(err)
		}
		consumerRepository := newOutboxConsumerRepository(sqlcgen.New(fixture.Pool))
		consumer := registerIntegrationConsumer(t, ctx, consumerRepository, "test.expired_consumer", eventType, createdAt)
		owner, _ := outbox.ParseLeaseOwner("expired-worker")
		acquiredAt := time.Now().UTC()
		leased, transition, _ := consumer.AcquireLease(owner, acquiredAt, acquiredAt.Add(50*time.Millisecond))
		leased, err = consumerRepository.AcquireLeaseCAS(ctx, transition)
		if err != nil {
			t.Fatal(err)
		}
		_, ackTransition, _ := leased.Acknowledge(
			owner, leased.Snapshot().LastAckedSequence, persisted.Snapshot().Sequence, acquiredAt.Add(time.Microsecond),
		)
		time.Sleep(75 * time.Millisecond)
		if _, err := consumerRepository.AcknowledgeCAS(ctx, ackTransition); !errors.Is(err, outbox.ErrConcurrentTransition) {
			t.Fatalf("expired ack error = %v, want concurrent transition", err)
		}
	})
}

func prepareRepositoryAuditEvent(t testing.TB, service *audit.Service, head audit.Head, requestID string) audit.SignedEvent {
	t.Helper()
	actor, err := audit.NewActor(audit.ActorAdmin, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	target, err := audit.NewTarget(audit.TargetUser, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	occurredAt := time.Now().UTC().Truncate(time.Microsecond)
	if occurredAt.Before(head.UpdatedAt()) {
		occurredAt = head.UpdatedAt().Add(time.Microsecond)
	}
	event, err := service.Prepare(head, audit.EventInput{
		EventID: uuid.New(), RequestID: requestID, OccurredAt: occurredAt,
		Actor: actor, Target: target, Action: audit.ActionUserSuspended, ReasonCode: "policy",
	})
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func registerIntegrationConsumer(
	t testing.TB,
	ctx context.Context,
	repository *OutboxConsumerRepository,
	idValue string,
	eventType outbox.EventType,
	createdAt time.Time,
) outbox.Consumer {
	t.Helper()
	id, err := outbox.ParseConsumerID(idValue)
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := outbox.NewSubscription(eventType)
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := outbox.NewConsumer(id, subscription, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := repository.Insert(ctx, consumer)
	if err != nil {
		t.Fatal(err)
	}
	return persisted
}

func leaseAndAckIntegrationConsumer(
	t testing.TB,
	ctx context.Context,
	repository *OutboxConsumerRepository,
	consumer outbox.Consumer,
	ownerValue string,
	event outbox.Event,
) outbox.Consumer {
	t.Helper()
	owner, err := outbox.ParseLeaseOwner(ownerValue)
	if err != nil {
		t.Fatal(err)
	}
	acquiredAt := time.Now().UTC()
	leased, transition, err := consumer.AcquireLease(owner, acquiredAt, acquiredAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	leased, err = repository.AcquireLeaseCAS(ctx, transition)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := outbox.NewEventBatch(leased.Snapshot().ID, owner, acquiredAt, 10)
	if err != nil {
		t.Fatal(err)
	}
	available, err := repository.ListAvailable(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(available) != 1 || available[0].Snapshot().ID != event.Snapshot().ID {
		t.Fatalf("available events = %+v", available)
	}
	acked, transition, err := leased.Acknowledge(owner, leased.Snapshot().LastAckedSequence, event.Snapshot().Sequence, acquiredAt.Add(time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := repository.AcknowledgeCAS(ctx, transition)
	if err != nil {
		t.Fatal(err)
	}
	if !sameConsumerSnapshot(persisted.Snapshot(), acked.Snapshot()) {
		t.Fatal("acknowledged consumer differs from domain transition")
	}
	return persisted
}

func countRowsByUUID(
	t testing.TB,
	ctx context.Context,
	fixture *integrationtest.PostgresSchema,
	table string,
	column string,
	value uuid.UUID,
) int {
	t.Helper()
	var count int
	query := "SELECT count(*) FROM " + table + " WHERE " + column + " = $1"
	if err := fixture.Pool.QueryRow(ctx, query, value).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func countRowsByBytes(
	t testing.TB,
	ctx context.Context,
	fixture *integrationtest.PostgresSchema,
	table string,
	column string,
	value []byte,
) int {
	t.Helper()
	var count int
	query := "SELECT count(*) FROM " + table + " WHERE " + column + " = $1"
	if err := fixture.Pool.QueryRow(ctx, query, value).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
