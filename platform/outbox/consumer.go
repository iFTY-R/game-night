package outbox

import "time"

const (
	// MaximumLeaseDuration bounds how long a crashed worker can block another dispatcher instance.
	MaximumLeaseDuration = 5 * time.Minute
	// InitialRetryBackoff is the first durable delay after a delivery failure.
	InitialRetryBackoff = time.Second
	// MaximumRetryBackoff caps exponential delay so checkpoint delivery continues to make progress.
	MaximumRetryBackoff = 5 * time.Minute
	// MaximumRetryCount bounds persisted counters; operational escalation handles exhaustion.
	MaximumRetryCount uint32 = 16
)

// ConsumerSnapshot is the persistence-neutral representation of an independent consumer offset.
type ConsumerSnapshot struct {
	ID                ConsumerID
	Subscriptions     []EventType
	LastAckedSequence Sequence
	LeaseOwner        LeaseOwner
	LeaseUntil        time.Time
	RetryCount        uint32
	NextAttemptAt     time.Time
	LastErrorCode     ErrorCode
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Consumer owns one subscription, offset, lease, and retry state independently from every other consumer.
type Consumer struct {
	snapshot ConsumerSnapshot
}

// ConsumerTransitionKind identifies the repository CAS statement required for a validated state change.
type ConsumerTransitionKind uint8

const (
	TransitionAcquireLease ConsumerTransitionKind = iota + 1
	TransitionRenewLease
	TransitionReleaseLease
	TransitionAcknowledge
	TransitionRecordRetry
)

// ConsumerCAS carries immutable expected and desired snapshots for one repository compare-and-swap.
// Expected lease expiry, owner, offset, and retry count prevent stale workers from overwriting newer state.
type ConsumerCAS struct {
	kind     ConsumerTransitionKind
	expected ConsumerSnapshot
	next     ConsumerSnapshot
}

// Kind selects the matching consumer repository operation.
func (transition ConsumerCAS) Kind() ConsumerTransitionKind { return transition.kind }

// Expected returns a deep copy of the state that must still be present in storage.
func (transition ConsumerCAS) Expected() ConsumerSnapshot {
	return cloneConsumerSnapshot(transition.expected)
}

// Next returns a deep copy of the validated state the CAS should persist.
func (transition ConsumerCAS) Next() ConsumerSnapshot {
	return cloneConsumerSnapshot(transition.next)
}

// NewConsumer registers a new independent consumer at offset zero without a lease or retry state.
func NewConsumer(id ConsumerID, subscription Subscription, createdAt time.Time) (Consumer, error) {
	return RestoreConsumer(ConsumerSnapshot{
		ID: id, Subscriptions: subscription.Types(), CreatedAt: createdAt, UpdatedAt: createdAt,
	})
}

// ResolveRegistration defines the atomic Insert conflict contract for repository adapters.
// An identical ID and subscription is idempotent and returns the existing state without resetting
// its offset, lease, or retry fields; reusing an ID for a different subscription is a conflict.
func ResolveRegistration(existing, requested Consumer) (Consumer, error) {
	if existing.snapshot.ID != requested.snapshot.ID || !equalEventTypes(existing.snapshot.Subscriptions, requested.snapshot.Subscriptions) {
		return Consumer{}, ErrAlreadyExists
	}
	if requested.snapshot.LastAckedSequence != 0 || requested.snapshot.LeaseOwner != "" || requested.snapshot.RetryCount != 0 {
		return Consumer{}, ErrInvalidInput
	}
	return existing, nil
}

// RestoreConsumer validates persisted state before it can authorize a lease-bound mutation.
func RestoreConsumer(snapshot ConsumerSnapshot) (Consumer, error) {
	snapshot = cloneConsumerSnapshot(snapshot)
	snapshot.LeaseUntil = canonicalOptionalTime(snapshot.LeaseUntil)
	snapshot.NextAttemptAt = canonicalOptionalTime(snapshot.NextAttemptAt)
	snapshot.CreatedAt = canonicalTime(snapshot.CreatedAt)
	snapshot.UpdatedAt = canonicalTime(snapshot.UpdatedAt)
	subscription, err := NewSubscription(snapshot.Subscriptions...)
	if err != nil || !snapshot.ID.Valid() || !snapshot.LastAckedSequence.Valid() || snapshot.CreatedAt.IsZero() ||
		snapshot.UpdatedAt.Before(snapshot.CreatedAt) || snapshot.RetryCount > MaximumRetryCount {
		return Consumer{}, ErrInvalidInput
	}
	snapshot.Subscriptions = subscription.Types()
	leaseOwnerSet := snapshot.LeaseOwner != ""
	leaseUntilSet := !snapshot.LeaseUntil.IsZero()
	if leaseOwnerSet != leaseUntilSet || (leaseOwnerSet && !snapshot.LeaseOwner.Valid()) {
		return Consumer{}, ErrInvalidInput
	}
	if snapshot.RetryCount == 0 {
		if !snapshot.NextAttemptAt.IsZero() || snapshot.LastErrorCode != "" {
			return Consumer{}, ErrInvalidInput
		}
	} else if snapshot.NextAttemptAt.IsZero() || !snapshot.LastErrorCode.Valid() {
		return Consumer{}, ErrInvalidInput
	}
	return Consumer{snapshot: snapshot}, nil
}

// Snapshot returns a deep copy so repository code cannot mutate subscriptions after validation.
func (consumer Consumer) Snapshot() ConsumerSnapshot {
	return cloneConsumerSnapshot(consumer.snapshot)
}

// AcquireLease claims an unleased or expired consumer after its durable retry deadline.
func (consumer Consumer) AcquireLease(owner LeaseOwner, acquiredAt, leaseUntil time.Time) (Consumer, ConsumerCAS, error) {
	acquiredAt = canonicalTime(acquiredAt)
	leaseUntil = canonicalTime(leaseUntil)
	if !owner.Valid() || !consumer.validTransitionTime(acquiredAt) || !validLeaseWindow(acquiredAt, leaseUntil) {
		return Consumer{}, ConsumerCAS{}, ErrInvalidInput
	}
	if consumer.snapshot.RetryCount > 0 && acquiredAt.Before(consumer.snapshot.NextAttemptAt) {
		return Consumer{}, ConsumerCAS{}, ErrBackoffActive
	}
	if consumer.snapshot.LeaseOwner != "" && consumer.snapshot.LeaseUntil.After(acquiredAt) {
		return Consumer{}, ConsumerCAS{}, ErrLeaseUnavailable
	}
	next := consumer.Snapshot()
	next.LeaseOwner = owner
	next.LeaseUntil = leaseUntil
	next.UpdatedAt = acquiredAt
	return consumer.transition(TransitionAcquireLease, next)
}

// RenewLease extends an active lease and includes the prior expiry in the resulting CAS input.
func (consumer Consumer) RenewLease(owner LeaseOwner, renewedAt, leaseUntil time.Time) (Consumer, ConsumerCAS, error) {
	renewedAt = canonicalTime(renewedAt)
	leaseUntil = canonicalTime(leaseUntil)
	if !owner.Valid() || !consumer.validTransitionTime(renewedAt) || !validLeaseWindow(renewedAt, leaseUntil) {
		return Consumer{}, ConsumerCAS{}, ErrInvalidInput
	}
	if err := consumer.requireActiveLease(owner, renewedAt); err != nil {
		return Consumer{}, ConsumerCAS{}, err
	}
	if !leaseUntil.After(consumer.snapshot.LeaseUntil) {
		return Consumer{}, ConsumerCAS{}, ErrInvalidInput
	}
	next := consumer.Snapshot()
	next.LeaseUntil = leaseUntil
	next.UpdatedAt = renewedAt
	return consumer.transition(TransitionRenewLease, next)
}

// ReleaseLease clears ownership only while the caller still owns an unexpired lease.
func (consumer Consumer) ReleaseLease(owner LeaseOwner, releasedAt time.Time) (Consumer, ConsumerCAS, error) {
	releasedAt = canonicalTime(releasedAt)
	if !owner.Valid() || !consumer.validTransitionTime(releasedAt) {
		return Consumer{}, ConsumerCAS{}, ErrInvalidInput
	}
	if err := consumer.requireActiveLease(owner, releasedAt); err != nil {
		return Consumer{}, ConsumerCAS{}, err
	}
	next := consumer.Snapshot()
	next.LeaseOwner = ""
	next.LeaseUntil = time.Time{}
	next.UpdatedAt = releasedAt
	return consumer.transition(TransitionReleaseLease, next)
}

// Acknowledge advances a consumer offset under an active lease and resets prior retry state.
// Global sequence gaps are valid because events for other subscriptions are intentionally filtered out.
func (consumer Consumer) Acknowledge(
	owner LeaseOwner,
	expectedSequence Sequence,
	ackedSequence Sequence,
	ackedAt time.Time,
) (Consumer, ConsumerCAS, error) {
	ackedAt = canonicalTime(ackedAt)
	if !owner.Valid() || !expectedSequence.Valid() || !ackedSequence.Valid() || !consumer.validTransitionTime(ackedAt) {
		return Consumer{}, ConsumerCAS{}, ErrInvalidInput
	}
	if err := consumer.requireActiveLease(owner, ackedAt); err != nil {
		return Consumer{}, ConsumerCAS{}, err
	}
	if expectedSequence != consumer.snapshot.LastAckedSequence {
		return Consumer{}, ConsumerCAS{}, ErrConcurrentTransition
	}
	if ackedSequence <= expectedSequence {
		return Consumer{}, ConsumerCAS{}, ErrInvalidAcknowledgement
	}
	next := consumer.Snapshot()
	next.LastAckedSequence = ackedSequence
	next.RetryCount = 0
	next.NextAttemptAt = time.Time{}
	next.LastErrorCode = ""
	next.UpdatedAt = ackedAt
	return consumer.transition(TransitionAcknowledge, next)
}

// RecordRetry records one bounded exponential-backoff step while the worker still owns the lease.
func (consumer Consumer) RecordRetry(owner LeaseOwner, code ErrorCode, failedAt time.Time) (Consumer, ConsumerCAS, error) {
	failedAt = canonicalTime(failedAt)
	if !owner.Valid() || !code.Valid() || !consumer.validTransitionTime(failedAt) {
		return Consumer{}, ConsumerCAS{}, ErrInvalidInput
	}
	if err := consumer.requireActiveLease(owner, failedAt); err != nil {
		return Consumer{}, ConsumerCAS{}, err
	}
	if consumer.snapshot.RetryCount > 0 && failedAt.Before(consumer.snapshot.NextAttemptAt) {
		return Consumer{}, ConsumerCAS{}, ErrBackoffActive
	}
	if consumer.snapshot.RetryCount >= MaximumRetryCount {
		return Consumer{}, ConsumerCAS{}, ErrRetryExhausted
	}
	next := consumer.Snapshot()
	next.RetryCount++
	next.NextAttemptAt = canonicalTime(failedAt.Add(retryBackoff(next.RetryCount)))
	next.LastErrorCode = code
	next.UpdatedAt = failedAt
	return consumer.transition(TransitionRecordRetry, next)
}

func (consumer Consumer) transition(kind ConsumerTransitionKind, next ConsumerSnapshot) (Consumer, ConsumerCAS, error) {
	nextConsumer, err := RestoreConsumer(next)
	if err != nil {
		return Consumer{}, ConsumerCAS{}, err
	}
	transition := ConsumerCAS{kind: kind, expected: consumer.Snapshot(), next: nextConsumer.Snapshot()}
	return nextConsumer, transition, nil
}

func (consumer Consumer) requireActiveLease(owner LeaseOwner, at time.Time) error {
	if consumer.snapshot.LeaseOwner == "" || consumer.snapshot.LeaseOwner != owner {
		return ErrLeaseNotOwned
	}
	if !consumer.snapshot.LeaseUntil.After(at) {
		return ErrLeaseExpired
	}
	return nil
}

func (consumer Consumer) validTransitionTime(at time.Time) bool {
	return !at.IsZero() && !at.Before(consumer.snapshot.UpdatedAt)
}

func validLeaseWindow(start, end time.Time) bool {
	return !start.IsZero() && end.After(start) && end.Sub(start) <= MaximumLeaseDuration
}

func retryBackoff(retryCount uint32) time.Duration {
	delay := InitialRetryBackoff
	for attempt := uint32(1); attempt < retryCount && delay < MaximumRetryBackoff; attempt++ {
		if delay > MaximumRetryBackoff/2 {
			return MaximumRetryBackoff
		}
		delay *= 2
	}
	if delay > MaximumRetryBackoff {
		return MaximumRetryBackoff
	}
	return delay
}

func cloneConsumerSnapshot(snapshot ConsumerSnapshot) ConsumerSnapshot {
	snapshot.Subscriptions = append([]EventType(nil), snapshot.Subscriptions...)
	return snapshot
}

func equalEventTypes(left, right []EventType) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func canonicalTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC().Truncate(time.Microsecond)
}

func canonicalOptionalTime(value time.Time) time.Time { return canonicalTime(value) }
