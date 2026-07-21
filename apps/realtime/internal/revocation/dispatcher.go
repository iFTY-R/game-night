// Package revocation delivers durable room membership facts into the authoritative game runtime.
package revocation

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/iFTY-R/game-night/platform/clock"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/outbox"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
)

const (
	ErrorIntegrity   outbox.ErrorCode = "realtime.revocation_integrity"
	ErrorUnavailable outbox.ErrorCode = "realtime.revocation_unavailable"
	releaseTimeout                    = 5 * time.Second
)

var (
	ErrInvalidConfig       = errors.New("invalid participant revocation dispatcher configuration")
	ErrDeliveryFailed      = errors.New("participant revocation delivery failed")
	ErrDispatchUnavailable = errors.New("participant revocation dispatch unavailable")
)

// Inbox completes one neutral source event only after the module transition has a durable receipt.
type Inbox interface {
	Consume(context.Context, outbox.Event) (gameruntime.SystemInboxConsumeResult, error)
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

// Dispatcher owns an offset independent from game projection fanout so a revoke retry cannot block snapshots.
type Dispatcher struct {
	config     Config
	unitOfWork outbox.UnitOfWork
	inbox      Inbox
	clock      clock.Clock
	logger     *slog.Logger
}

// NewDispatcher validates every dependency before registering or claiming the durable consumer.
func NewDispatcher(
	config Config,
	unitOfWork outbox.UnitOfWork,
	inbox Inbox,
	source clock.Clock,
	logger *slog.Logger,
) (*Dispatcher, error) {
	if !config.Owner.Valid() || config.LeaseDuration <= 0 || config.LeaseDuration > outbox.MaximumLeaseDuration ||
		config.PollInterval < 10*time.Millisecond || config.PollInterval > time.Minute ||
		config.BatchSize == 0 || config.BatchSize > outbox.MaximumBatchSize ||
		unitOfWork == nil || inbox == nil || source == nil || logger == nil {
		return nil, ErrInvalidConfig
	}
	return &Dispatcher{config: config, unitOfWork: unitOfWork, inbox: inbox, clock: source, logger: logger}, nil
}

// Run polls serially; durable backoff state prevents ownership or database outages from creating a hot loop.
func (dispatcher *Dispatcher) Run(ctx context.Context) error {
	if dispatcher == nil || ctx == nil {
		return ErrInvalidConfig
	}
	ticker := time.NewTicker(dispatcher.config.PollInterval)
	defer ticker.Stop()
	for {
		if _, err := dispatcher.RunOnce(ctx); err != nil && ctx.Err() == nil {
			dispatcher.logger.WarnContext(ctx, "dispatch durable participant revocation", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// RunOnce applies and acknowledges an ordered batch; any failure leaves the current event retryable.
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
	subscription, err := revocationSubscription()
	if err != nil {
		return outbox.Consumer{}, err
	}
	requested, err := outbox.NewConsumer(outbox.ConsumerIDRoomParticipantRevocation, subscription, now)
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
		outbox.ConsumerIDRoomParticipantRevocation, dispatcher.config.Owner, dispatcher.clock.Now(), dispatcher.config.BatchSize,
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
	if snapshot.Type != roomDomain.ParticipantRevokedEventType || snapshot.AggregateType != roomDomain.RoomSessionAggregateType {
		return ErrorIntegrity
	}
	if _, err := dispatcher.inbox.Consume(ctx, event); err != nil {
		if errors.Is(err, gameruntime.ErrGameSessionIntegrity) || errors.Is(err, gameruntime.ErrInvalidSystemCommit) ||
			errors.Is(err, gameruntime.ErrSystemInboxNotFound) || errors.Is(err, idempotency.ErrConflict) {
			return ErrorIntegrity
		}
		return ErrorUnavailable
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
	if err := dispatcher.release(cleanupCtx, consumer); err != nil && !errors.Is(err, outbox.ErrConcurrentTransition) {
		dispatcher.logger.WarnContext(cleanupCtx, "release participant revocation consumer lease", "error", err)
	}
}

func (*Dispatcher) cleanupContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), releaseTimeout)
}

func revocationSubscription() (outbox.Subscription, error) {
	return outbox.NewSubscription(roomDomain.ParticipantRevokedEventType)
}
