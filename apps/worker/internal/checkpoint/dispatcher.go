// Package checkpoint owns delivery of signed audit checkpoint outbox events to append-only storage.
package checkpoint

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/persistence/objectstorage"
)

const (
	// ErrorCheckpointIntegrity persists a bounded diagnostic when an event or signature is not trustworthy.
	ErrorCheckpointIntegrity outbox.ErrorCode = "checkpoint.integrity"
	// ErrorSinkUnavailable distinguishes retryable object-store outages from payload integrity failures.
	ErrorSinkUnavailable outbox.ErrorCode = "checkpoint.sink_unavailable"
	// ErrorSinkIntegrity prevents a deterministic key collision or failed read-back from being acknowledged.
	ErrorSinkIntegrity outbox.ErrorCode = "checkpoint.sink_integrity"
	// releaseTimeout bounds shutdown cleanup independently from the canceled worker loop context.
	releaseTimeout = 5 * time.Second
)

var (
	// ErrInvalidConfig rejects a dispatcher that cannot safely own a bounded consumer lease.
	ErrInvalidConfig = errors.New("invalid checkpoint dispatcher configuration")
	// ErrDeliveryFailed reports a durably retried checkpoint without exposing object-store details.
	ErrDeliveryFailed = errors.New("checkpoint delivery failed")
	// ErrDispatchUnavailable reports a lease, database, or acknowledgement failure outside delivery retry handling.
	ErrDispatchUnavailable = errors.New("checkpoint dispatch unavailable")
)

// CheckpointVerifier authenticates historical checkpoint signatures without exposing broader audit mutation APIs.
type CheckpointVerifier interface {
	VerifyCheckpoint(audit.Checkpoint) error
}

// Config bounds one dispatcher pass. The worker instance identity is persisted only as lease ownership metadata.
type Config struct {
	Owner         outbox.LeaseOwner
	LeaseDuration time.Duration
	BatchSize     uint32
}

// Result describes committed progress from one bounded pass.
type Result struct {
	Delivered uint32
	Idle      bool
}

// Dispatcher serializes the claim, verify, create-if-absent, acknowledge, and retry protocol.
type Dispatcher struct {
	config     Config
	unitOfWork outbox.UnitOfWork
	sink       objectstorage.Sink
	verifier   CheckpointVerifier
	clock      clock.Clock
}

// NewDispatcher validates all process-owned dependencies before the worker begins claiming leases.
func NewDispatcher(
	config Config,
	unitOfWork outbox.UnitOfWork,
	sink objectstorage.Sink,
	verifier CheckpointVerifier,
	source clock.Clock,
) (*Dispatcher, error) {
	if !config.Owner.Valid() || config.LeaseDuration <= 0 || config.LeaseDuration > outbox.MaximumLeaseDuration ||
		config.BatchSize == 0 || config.BatchSize > outbox.MaximumBatchSize || unitOfWork == nil || sink == nil ||
		verifier == nil || source == nil {
		return nil, ErrInvalidConfig
	}
	return &Dispatcher{config: config, unitOfWork: unitOfWork, sink: sink, verifier: verifier, clock: source}, nil
}

// RunOnce claims the checkpoint consumer, delivers one bounded ordered batch, and releases ownership.
// A successful object write followed by a lost acknowledgement is safe because the next owner writes the same key and bytes.
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
			if err := dispatcher.recordRetryAndRelease(ctx, consumer, failureCode); err != nil {
				return result, ErrDispatchUnavailable
			}
			return result, ErrDeliveryFailed
		}
		acknowledged, ackErr := dispatcher.acknowledge(ctx, consumer, event)
		if ackErr != nil {
			// The acknowledgement may have committed despite a lost response. Do not write a stale retry state.
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
	subscription, err := outbox.NewSubscription(outbox.EventTypeAuditCheckpointPending)
	if err != nil {
		return outbox.Consumer{}, ErrInvalidConfig
	}
	requested, err := outbox.NewConsumer(outbox.ConsumerIDAuditCheckpoint, subscription, now)
	if err != nil {
		return outbox.Consumer{}, ErrInvalidConfig
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
	readAt := dispatcher.clock.Now()
	batch, err := outbox.NewEventBatch(
		outbox.ConsumerIDAuditCheckpoint, dispatcher.config.Owner, readAt, dispatcher.config.BatchSize,
	)
	if err != nil {
		return nil, err
	}
	err = dispatcher.unitOfWork.Run(ctx, func(ctx context.Context, transaction outbox.Transaction) error {
		listed, listErr := transaction.Consumers().ListAvailable(ctx, batch)
		if listErr != nil {
			return listErr
		}
		events = listed
		return nil
	})
	return events, err
}

func (dispatcher *Dispatcher) deliver(ctx context.Context, event outbox.Event) outbox.ErrorCode {
	eventSnapshot := event.Snapshot()
	if eventSnapshot.Type != outbox.EventTypeAuditCheckpointPending ||
		eventSnapshot.AggregateType != outbox.AggregateTypeAuditChain {
		return ErrorCheckpointIntegrity
	}
	checkpoint, err := audit.ParseCheckpoint(eventSnapshot.Payload)
	if err != nil || dispatcher.verifier.VerifyCheckpoint(checkpoint) != nil {
		return ErrorCheckpointIntegrity
	}
	checkpointSnapshot := checkpoint.Snapshot()
	key, err := objectstorage.NewKey(checkpointSnapshot.ObjectKey())
	if err != nil {
		return ErrorCheckpointIntegrity
	}
	metadata, err := objectstorage.NewMetadata(map[string]string{
		"chain-id":            string(checkpointSnapshot.ChainID),
		"chain-sequence":      strconv.FormatUint(checkpointSnapshot.Sequence, 10),
		"outbox-sequence":     strconv.FormatUint(uint64(eventSnapshot.Sequence), 10),
		"signing-key-version": strconv.FormatUint(uint64(checkpointSnapshot.SigningKeyVersion), 10),
	})
	if err != nil {
		return ErrorCheckpointIntegrity
	}
	object, err := objectstorage.NewObject(key, eventSnapshot.Payload, metadata)
	if err != nil {
		return ErrorCheckpointIntegrity
	}
	if err := dispatcher.sink.Write(ctx, object); err != nil {
		if errors.Is(err, objectstorage.ErrIntegrity) || errors.Is(err, objectstorage.ErrInvalidInput) {
			return ErrorSinkIntegrity
		}
		return ErrorSinkUnavailable
	}
	return ""
}

func (dispatcher *Dispatcher) acknowledge(
	ctx context.Context,
	consumer outbox.Consumer,
	event outbox.Event,
) (outbox.Consumer, error) {
	var acknowledged outbox.Consumer
	eventSequence := event.Snapshot().Sequence
	now := dispatcher.clock.Now()
	next, transition, err := consumer.Acknowledge(
		dispatcher.config.Owner, consumer.Snapshot().LastAckedSequence, eventSequence, now,
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

func (dispatcher *Dispatcher) recordRetryAndRelease(
	ctx context.Context,
	consumer outbox.Consumer,
	code outbox.ErrorCode,
) error {
	cleanupCtx, cancel := dispatcher.cleanupContext(ctx)
	defer cancel()
	now := dispatcher.clock.Now()
	retried, transition, err := consumer.RecordRetry(dispatcher.config.Owner, code, now)
	if err != nil {
		dispatcher.releaseAfterFailure(cleanupCtx, consumer)
		return err
	}
	err = dispatcher.unitOfWork.Run(cleanupCtx, func(ctx context.Context, transaction outbox.Transaction) error {
		persisted, persistErr := transaction.Consumers().RecordRetryCAS(ctx, transition)
		if persistErr != nil {
			return persistErr
		}
		retried = persisted
		return nil
	})
	if err != nil {
		return err
	}
	return dispatcher.release(cleanupCtx, retried)
}

func (dispatcher *Dispatcher) release(ctx context.Context, consumer outbox.Consumer) error {
	now := dispatcher.clock.Now()
	_, transition, err := consumer.ReleaseLease(dispatcher.config.Owner, now)
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
