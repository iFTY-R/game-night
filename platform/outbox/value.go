package outbox

import (
	"errors"
	"math"
	"regexp"
	"sort"
)

const (
	// MaximumNameLength bounds every persisted protocol name before it reaches database indexes or logs.
	MaximumNameLength = 128
	// MaximumSubscriptions prevents a malformed consumer row from creating unbounded filtering work.
	MaximumSubscriptions = 64
)

var (
	protocolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)+$`)
	leaseOwnerPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]*$`)

	// ErrInvalidInput rejects malformed commands and persisted state without exposing their values.
	ErrInvalidInput = errors.New("invalid outbox input")
	// ErrNotFound is the stable absence result shared by event and consumer repositories.
	ErrNotFound = errors.New("outbox record not found")
	// ErrAlreadyExists reports an event ID or consumer ID uniqueness conflict.
	ErrAlreadyExists = errors.New("outbox record already exists")
	// ErrLeaseUnavailable means another unexpired owner currently controls the consumer.
	ErrLeaseUnavailable = errors.New("outbox consumer lease unavailable")
	// ErrLeaseNotOwned rejects a transition attempted by anyone except the recorded owner.
	ErrLeaseNotOwned = errors.New("outbox consumer lease not owned")
	// ErrLeaseExpired prevents stale workers from acknowledging or mutating retry state.
	ErrLeaseExpired = errors.New("outbox consumer lease expired")
	// ErrBackoffActive prevents reacquisition before the durable retry deadline.
	ErrBackoffActive = errors.New("outbox consumer retry backoff active")
	// ErrInvalidAcknowledgement rejects zero, decreasing, or otherwise invalid offsets.
	ErrInvalidAcknowledgement = errors.New("invalid outbox acknowledgement")
	// ErrRetryExhausted prevents persisted retry counters from growing without bound.
	ErrRetryExhausted = errors.New("outbox consumer retry limit reached")
	// ErrConcurrentTransition reports a stale expected snapshot or lost repository CAS.
	ErrConcurrentTransition = errors.New("outbox consumer transition lost concurrency race")
	// ErrRepositoryUnavailable hides database and host details from domain callers.
	ErrRepositoryUnavailable = errors.New("outbox repository unavailable")
	// ErrIntegrity reports persisted data that violates outbox invariants.
	ErrIntegrity = errors.New("outbox integrity failure")
)

// EventType is a versionable dotted protocol name used for subscription filtering.
type EventType string

const (
	// EventTypeAuditCheckpointPending asks the checkpoint consumer to persist one signed audit checkpoint.
	EventTypeAuditCheckpointPending EventType = "audit.checkpoint.pending"
	// EventTypeIdentityRecoveryCompleted records one committed user recovery without carrying recovery secrets.
	EventTypeIdentityRecoveryCompleted EventType = "identity.recovery.completed.v1"
	// EventTypeIdentityDeviceRevoked tells downstream modules to recheck one device's authoritative state.
	EventTypeIdentityDeviceRevoked EventType = "identity.device.revoked.v1"
	// EventTypeIdentityUserSuspended invalidates room/realtime authority derived from an active user.
	EventTypeIdentityUserSuspended EventType = "identity.user.suspended.v1"
	// EventTypeIdentityUserUnsuspended announces restored account authority without restoring old credentials.
	EventTypeIdentityUserUnsuspended EventType = "identity.user.unsuspended.v1"
	// EventTypeIdentityUserDeleted announces terminal account deletion to downstream modules.
	EventTypeIdentityUserDeleted EventType = "identity.user.deleted.v1"
)

// ParseEventType validates a durable event protocol name.
func ParseEventType(value string) (EventType, error) {
	eventType := EventType(value)
	if !eventType.Valid() {
		return "", ErrInvalidInput
	}
	return eventType, nil
}

// Valid reports whether an event type is bounded, canonical, and namespace-qualified.
func (eventType EventType) Valid() bool {
	return validProtocolName(string(eventType))
}

// Value returns the canonical database and wire representation.
func (eventType EventType) Value() string { return string(eventType) }

// AggregateType identifies the authoritative aggregate that emitted an event.
type AggregateType string

const (
	// AggregateTypeAuditChain identifies the append-only audit chain checkpointed by the production consumer.
	AggregateTypeAuditChain AggregateType = "audit.chain"
	// AggregateTypeIdentityUser identifies durable events emitted by one user identity aggregate.
	AggregateTypeIdentityUser AggregateType = "identity.user"
	// AggregateTypeIdentityDevice identifies durable events emitted for one device credential.
	AggregateTypeIdentityDevice AggregateType = "identity.device"
)

// ParseAggregateType validates a namespace-qualified aggregate type.
func ParseAggregateType(value string) (AggregateType, error) {
	aggregateType := AggregateType(value)
	if !aggregateType.Valid() {
		return "", ErrInvalidInput
	}
	return aggregateType, nil
}

// Valid reports whether an aggregate type is bounded and canonical.
func (aggregateType AggregateType) Valid() bool {
	return validProtocolName(string(aggregateType))
}

// Value returns the canonical database and wire representation.
func (aggregateType AggregateType) Value() string { return string(aggregateType) }

// ConsumerID names one independent subscription and offset.
type ConsumerID string

const (
	// ConsumerIDAuditCheckpoint owns WORM delivery of signed audit checkpoints.
	ConsumerIDAuditCheckpoint ConsumerID = "audit.checkpoint"
	// ConsumerIDGameSessionFanout republishes committed game cursors to non-authoritative Redis wake-up channels.
	ConsumerIDGameSessionFanout ConsumerID = "realtime.game_fanout"
)

// ParseConsumerID validates a stable namespace-qualified consumer identity.
func ParseConsumerID(value string) (ConsumerID, error) {
	consumerID := ConsumerID(value)
	if !consumerID.Valid() {
		return "", ErrInvalidInput
	}
	return consumerID, nil
}

// Valid reports whether a consumer ID is bounded and canonical.
func (consumerID ConsumerID) Valid() bool { return validProtocolName(string(consumerID)) }

// Value returns the canonical database representation.
func (consumerID ConsumerID) Value() string { return string(consumerID) }

// LeaseOwner identifies one worker process or instance attempting a CAS transition.
type LeaseOwner string

// ParseLeaseOwner rejects whitespace, control characters, and unbounded process identities.
func ParseLeaseOwner(value string) (LeaseOwner, error) {
	owner := LeaseOwner(value)
	if !owner.Valid() {
		return "", ErrInvalidInput
	}
	return owner, nil
}

// Valid reports whether a lease owner is safe for persistence and structured logging.
func (owner LeaseOwner) Valid() bool {
	return len(owner) > 0 && len(owner) <= MaximumNameLength && leaseOwnerPattern.MatchString(string(owner))
}

// Value returns the canonical database representation.
func (owner LeaseOwner) Value() string { return string(owner) }

// ErrorCode is a stable machine-readable failure category; raw infrastructure messages are never persisted here.
type ErrorCode string

// ParseErrorCode validates a namespace-qualified retry error category.
func ParseErrorCode(value string) (ErrorCode, error) {
	code := ErrorCode(value)
	if !code.Valid() {
		return "", ErrInvalidInput
	}
	return code, nil
}

// Valid reports whether an error code is bounded and canonical.
func (code ErrorCode) Valid() bool { return validProtocolName(string(code)) }

// Value returns the stable database representation.
func (code ErrorCode) Value() string { return string(code) }

// Sequence is a non-negative PostgreSQL bigint outbox position.
type Sequence uint64

// NewSequence converts a database bigint without permitting negative offsets.
func NewSequence(value int64) (Sequence, error) {
	if value < 0 {
		return 0, ErrInvalidInput
	}
	return Sequence(value), nil
}

// Valid reports whether a sequence round-trips through PostgreSQL bigint.
func (sequence Sequence) Valid() bool { return sequence <= Sequence(math.MaxInt64) }

// Int64 returns the persistence representation after domain validation.
func (sequence Sequence) Int64() (int64, error) {
	if !sequence.Valid() {
		return 0, ErrInvalidInput
	}
	return int64(sequence), nil
}

// Subscription is an immutable, canonical set of event types for one consumer.
type Subscription struct {
	eventTypes []EventType
}

// NewSubscription validates, de-duplicates, and sorts event types into a canonical set.
func NewSubscription(eventTypes ...EventType) (Subscription, error) {
	if len(eventTypes) == 0 || len(eventTypes) > MaximumSubscriptions {
		return Subscription{}, ErrInvalidInput
	}
	types := make([]EventType, len(eventTypes))
	seen := make(map[EventType]struct{}, len(eventTypes))
	for index, eventType := range eventTypes {
		if !eventType.Valid() {
			return Subscription{}, ErrInvalidInput
		}
		if _, exists := seen[eventType]; exists {
			return Subscription{}, ErrInvalidInput
		}
		seen[eventType] = struct{}{}
		types[index] = eventType
	}
	sort.Slice(types, func(left, right int) bool { return types[left] < types[right] })
	return Subscription{eventTypes: types}, nil
}

// Types returns a copy so callers cannot change a consumer's subscription after validation.
func (subscription Subscription) Types() []EventType {
	return append([]EventType(nil), subscription.eventTypes...)
}

// Contains reports whether an event belongs to this independent consumer stream.
func (subscription Subscription) Contains(eventType EventType) bool {
	for _, subscribed := range subscription.eventTypes {
		if subscribed == eventType {
			return true
		}
	}
	return false
}

// Valid reports whether the subscription was constructed with unique valid event types.
func (subscription Subscription) Valid() bool {
	_, err := NewSubscription(subscription.eventTypes...)
	return err == nil
}

func validProtocolName(value string) bool {
	return len(value) > 0 && len(value) <= MaximumNameLength && protocolNamePattern.MatchString(value)
}
