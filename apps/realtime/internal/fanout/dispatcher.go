// Package fanout republishes durable game-session outbox cursors to non-authoritative Redis wake-ups.
package fanout

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/iFTY-R/game-night/platform/clock"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/outbox"
	redisstore "github.com/iFTY-R/game-night/platform/persistence/redis"
)

const (
	// ErrorIntegrity retains a durable retry when event metadata and its secret-free payload disagree.
	ErrorIntegrity outbox.ErrorCode = "realtime.fanout_integrity"
	// ErrorRedisUnavailable distinguishes retryable wake-up delivery failure from a corrupt outbox event.
	ErrorRedisUnavailable outbox.ErrorCode = "realtime.fanout_redis_unavailable"
	// releaseTimeout bounds lease cleanup independently from a canceled process context.
	releaseTimeout = 5 * time.Second
)

var (
	ErrInvalidConfig       = errors.New("invalid realtime durable fanout configuration")
	ErrDeliveryFailed      = errors.New("realtime durable fanout delivery failed")
	ErrDispatchUnavailable = errors.New("realtime durable fanout dispatch unavailable")
)

// Publisher emits only a session ID and committed state version after the outbox event is validated.
type Publisher interface {
	PublishSessionFanout(context.Context, redisstore.SessionFanoutEvent) error
}

// Config bounds one consumer lease, polling cadence, and ordered database batch.
type Config struct {
	Owner         outbox.LeaseOwner
	LeaseDuration time.Duration
	PollInterval  time.Duration
	BatchSize     uint32
}

// Result reports durable offset progress from one bounded dispatcher pass.
type Result struct {
	Delivered uint32
	Idle      bool
}

// Dispatcher owns the realtime game-fanout consumer offset independently from all other outbox consumers.
type Dispatcher struct {
	config     Config
	unitOfWork outbox.UnitOfWork
	publisher  Publisher
	clock      clock.Clock
	logger     *slog.Logger
}

// NewDispatcher validates every dependency before registering or claiming the durable consumer.
func NewDispatcher(
	config Config,
	unitOfWork outbox.UnitOfWork,
	publisher Publisher,
	source clock.Clock,
	logger *slog.Logger,
) (*Dispatcher, error) {
	if !config.Owner.Valid() || config.LeaseDuration <= 0 || config.LeaseDuration > outbox.MaximumLeaseDuration ||
		config.PollInterval < 10*time.Millisecond || config.PollInterval > time.Minute ||
		config.BatchSize == 0 || config.BatchSize > outbox.MaximumBatchSize ||
		unitOfWork == nil || publisher == nil || source == nil || logger == nil {
		return nil, ErrInvalidConfig
	}
	return &Dispatcher{config: config, unitOfWork: unitOfWork, publisher: publisher, clock: source, logger: logger}, nil
}

// Run polls serially; durable backoff state prevents Redis outages from creating a hot retry loop.
func (dispatcher *Dispatcher) Run(ctx context.Context) error {
	if dispatcher == nil || ctx == nil {
		return ErrInvalidConfig
	}
	ticker := time.NewTicker(dispatcher.config.PollInterval)
	defer ticker.Stop()
	for {
		if _, err := dispatcher.RunOnce(ctx); err != nil && ctx.Err() == nil {
			dispatcher.logger.WarnContext(ctx, "dispatch durable game fanout", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// RunOnce publishes an ordered subscribed batch and acknowledges each event only after Redis accepts it.
func (dispatcher *Dispatcher) RunOnce(ctx context.Context) (Result, error) {
	if dispatcher == nil || ctx == nil {
		return Result{}, ErrInvalidConfig
	}
	consumer, err := dispatcher.acquire(ctx)
	if err != nil {
		if errors.Is(err, outbox.ErrLeaseUnavailable) || errors.Is(err, outbox.ErrBackoffActive) ||
			errors.Is(err, outbox.ErrConcurrentTransition) {
			return Result{Idle: true}, nil
		}
		return Result{}, ErrDispatchUnavailable
	}
	events, err := dispatcher.list(ctx)
	if err != nil {
		dispatcher.releaseAfterFailure(ctx, consumer)
		return Result{}, ErrDispatchUnavailable
	}
	if len(events) == 0 {
		if err := dispatcher.release(ctx, consumer); err != nil {
			return Result{}, ErrDispatchUnavailable
		}
		return Result{Idle: true}, nil
	}
	result := Result{}
	for _, event := range events {
		failureCode := dispatcher.deliver(ctx, event)
		if failureCode != "" {
			if ctx.Err() != nil {
				dispatcher.releaseAfterFailure(ctx, consumer)
				return result, ErrDispatchUnavailable
			}
			if err := dispatcher.recordRetryAndRelease(ctx, consumer, failureCode); err != nil {
				return result, ErrDispatchUnavailable
			}
			return result, ErrDeliveryFailed
		}
		acknowledged, ackErr := dispatcher.acknowledge(ctx, consumer, event)
		if ackErr != nil {
			// A lost response may hide a committed acknowledgement; never overwrite it with stale retry state.
			dispatcher.releaseAfterFailure(ctx, consumer)
			return result, ErrDispatchUnavailable
		}
		consumer = acknowledged
		result.Delivered++
	}
	if err := dispatcher.release(ctx, consumer); err != nil {
		return result, ErrDispatchUnavailable
	}
	return result, nil
}

func (dispatcher *Dispatcher) acquire(ctx context.Context) (outbox.Consumer, error) {
	var acquired outbox.Consumer
	now := dispatcher.clock.Now()
	subscription, err := sessionSubscription()
	if err != nil {
		return outbox.Consumer{}, err
	}
	requested, err := outbox.NewConsumer(outbox.ConsumerIDGameSessionFanout, subscription, now)
	if err != nil {
		return outbox.Consumer{}, err
	}
	err = dispatcher.unitOfWork.Run(ctx, func(ctx context.Context, transaction outbox.Transaction) error {
		current, insertErr := transaction.Consumers().Insert(ctx, requested)
		if insertErr != nil {
			return insertErr
		}
		next, transition, transitionErr := current.AcquireLease(
			dispatcher.config.Owner, now, now.Add(dispatcher.config.LeaseDuration),
		)
		if transitionErr != nil {
			return transitionErr
		}
		persisted, persistErr := transaction.Consumers().AcquireLeaseCAS(ctx, transition)
		if persistErr != nil {
			return persistErr
		}
		if persisted.Snapshot().LeaseOwner != next.Snapshot().LeaseOwner {
			return outbox.ErrIntegrity
		}
		acquired = persisted
		return nil
	})
	return acquired, err
}

func (dispatcher *Dispatcher) list(ctx context.Context) ([]outbox.Event, error) {
	var events []outbox.Event
	batch, err := outbox.NewEventBatch(
		outbox.ConsumerIDGameSessionFanout, dispatcher.config.Owner, dispatcher.clock.Now(), dispatcher.config.BatchSize,
	)
	if err != nil {
		return nil, err
	}
	err = dispatcher.unitOfWork.Run(ctx, func(ctx context.Context, transaction outbox.Transaction) error {
		listed, listErr := transaction.Consumers().ListAvailable(ctx, batch)
		events = listed
		return listErr
	})
	return events, err
}

func (dispatcher *Dispatcher) deliver(ctx context.Context, event outbox.Event) outbox.ErrorCode {
	snapshot := event.Snapshot()
	subscription, err := sessionSubscription()
	if err != nil || !subscription.Contains(snapshot.Type) || snapshot.AggregateType != gameruntime.GameSessionAggregateType {
		return ErrorIntegrity
	}
	notification, err := gameruntime.ParseSessionNotification(snapshot.Payload)
	if err != nil || notification.SessionID != snapshot.AggregateID {
		return ErrorIntegrity
	}
	if err := dispatcher.publisher.PublishSessionFanout(ctx, redisstore.SessionFanoutEvent{
		SessionID: notification.SessionID, StateVersion: notification.StateVersion,
	}); err != nil {
		return ErrorRedisUnavailable
	}
	return ""
}

func (dispatcher *Dispatcher) acknowledge(ctx context.Context, consumer outbox.Consumer, event outbox.Event) (outbox.Consumer, error) {
	var acknowledged outbox.Consumer
	now := dispatcher.clock.Now()
	next, transition, err := consumer.Acknowledge(
		dispatcher.config.Owner, consumer.Snapshot().LastAckedSequence, event.Snapshot().Sequence, now,
	)
	if err != nil {
		return outbox.Consumer{}, err
	}
	err = dispatcher.unitOfWork.Run(ctx, func(ctx context.Context, transaction outbox.Transaction) error {
		persisted, persistErr := transaction.Consumers().AcknowledgeCAS(ctx, transition)
		if persistErr != nil {
			return persistErr
		}
		if persisted.Snapshot().LastAckedSequence != next.Snapshot().LastAckedSequence {
			return outbox.ErrIntegrity
		}
		acknowledged = persisted
		return nil
	})
	return acknowledged, err
}

func (dispatcher *Dispatcher) recordRetryAndRelease(ctx context.Context, consumer outbox.Consumer, code outbox.ErrorCode) error {
	cleanupCtx, cancel := dispatcher.cleanupContext(ctx)
	defer cancel()
	retried, transition, err := consumer.RecordRetry(dispatcher.config.Owner, code, dispatcher.clock.Now())
	if err != nil {
		dispatcher.releaseAfterFailure(cleanupCtx, consumer)
		return err
	}
	err = dispatcher.unitOfWork.Run(cleanupCtx, func(ctx context.Context, transaction outbox.Transaction) error {
		persisted, persistErr := transaction.Consumers().RecordRetryCAS(ctx, transition)
		retried = persisted
		return persistErr
	})
	if err != nil {
		return err
	}
	return dispatcher.release(cleanupCtx, retried)
}

func (dispatcher *Dispatcher) release(ctx context.Context, consumer outbox.Consumer) error {
	_, transition, err := consumer.ReleaseLease(dispatcher.config.Owner, dispatcher.clock.Now())
	if err != nil {
		return err
	}
	return dispatcher.unitOfWork.Run(ctx, func(ctx context.Context, transaction outbox.Transaction) error {
		_, releaseErr := transaction.Consumers().ReleaseLeaseCAS(ctx, transition)
		return releaseErr
	})
}

func (dispatcher *Dispatcher) releaseAfterFailure(ctx context.Context, consumer outbox.Consumer) {
	cleanupCtx, cancel := dispatcher.cleanupContext(ctx)
	defer cancel()
	_ = dispatcher.release(cleanupCtx, consumer)
}

func (*Dispatcher) cleanupContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), releaseTimeout)
}

func sessionSubscription() (outbox.Subscription, error) {
	return outbox.NewSubscription(
		gameruntime.GameSessionCreatedEventType,
		gameruntime.GameSessionTransitionedEventType,
		gameruntime.GameSessionSuspendedEventType,
		gameruntime.GameSessionResumedEventType,
		gameruntime.GameSessionCancelledEventType,
	)
}
