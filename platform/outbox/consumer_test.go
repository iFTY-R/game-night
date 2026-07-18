package outbox

import (
	"errors"
	"testing"
	"time"
)

func TestConsumersTrackOffsetsIndependently(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	subscription := mustSubscription(t, EventTypeAuditCheckpointPending)
	first := mustConsumer(t, "test.first", subscription, createdAt)
	second := mustConsumer(t, "test.second", subscription, createdAt)
	owner := mustLeaseOwner(t, "worker-1")

	first, _, err := first.AcquireLease(owner, createdAt.Add(time.Second), createdAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("first AcquireLease() error = %v", err)
	}
	second, _, err = second.AcquireLease(owner, createdAt.Add(time.Second), createdAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("second AcquireLease() error = %v", err)
	}
	first, transition, err := first.Acknowledge(owner, 0, 7, createdAt.Add(2*time.Second))
	if err != nil {
		t.Fatalf("first Acknowledge() error = %v", err)
	}

	if got := first.Snapshot().LastAckedSequence; got != 7 {
		t.Fatalf("first offset = %d, want 7", got)
	}
	if got := second.Snapshot().LastAckedSequence; got != 0 {
		t.Fatalf("second offset = %d, want independent zero", got)
	}
	if transition.Expected().LastAckedSequence != 0 || transition.Next().LastAckedSequence != 7 {
		t.Fatalf("CAS offsets = %d -> %d, want 0 -> 7", transition.Expected().LastAckedSequence, transition.Next().LastAckedSequence)
	}
}

func TestConsumerRejectsWrongOwnerAndExpiredLease(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	consumer := mustConsumer(t, "test.consumer", mustSubscription(t, EventTypeAuditCheckpointPending), createdAt)
	owner := mustLeaseOwner(t, "worker-1")
	other := mustLeaseOwner(t, "worker-2")
	leaseUntil := createdAt.Add(time.Minute)
	consumer, _, err := consumer.AcquireLease(owner, createdAt, leaseUntil)
	if err != nil {
		t.Fatalf("AcquireLease() error = %v", err)
	}

	if _, _, err := consumer.Acknowledge(other, 0, 1, createdAt.Add(time.Second)); !errors.Is(err, ErrLeaseNotOwned) {
		t.Fatalf("wrong-owner ack error = %v, want ErrLeaseNotOwned", err)
	}
	if _, _, err := consumer.RecordRetry(owner, "checkpoint.upload_failed", leaseUntil); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("expired retry error = %v, want ErrLeaseExpired", err)
	}
	if _, _, err := consumer.ReleaseLease(owner, leaseUntil); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("expired release error = %v, want ErrLeaseExpired", err)
	}
}

func TestConsumerAckRejectsStaleExpectedAndBackwardOffsets(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	consumer := mustConsumer(t, "test.consumer", mustSubscription(t, EventTypeAuditCheckpointPending), createdAt)
	owner := mustLeaseOwner(t, "worker-1")
	consumer, _, err := consumer.AcquireLease(owner, createdAt, createdAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("AcquireLease() error = %v", err)
	}

	// A global gap is valid: sequences 1-6 may belong to event types this consumer does not subscribe to.
	consumer, _, err = consumer.Acknowledge(owner, 0, 7, createdAt.Add(time.Second))
	if err != nil {
		t.Fatalf("gap Acknowledge() error = %v", err)
	}
	if _, _, err := consumer.Acknowledge(owner, 0, 8, createdAt.Add(2*time.Second)); !errors.Is(err, ErrConcurrentTransition) {
		t.Fatalf("stale expected offset error = %v, want ErrConcurrentTransition", err)
	}
	if _, _, err := consumer.Acknowledge(owner, 7, 6, createdAt.Add(2*time.Second)); !errors.Is(err, ErrInvalidAcknowledgement) {
		t.Fatalf("backward ack error = %v, want ErrInvalidAcknowledgement", err)
	}
	if _, _, err := consumer.Acknowledge(owner, 7, 7, createdAt.Add(2*time.Second)); !errors.Is(err, ErrInvalidAcknowledgement) {
		t.Fatalf("duplicate ack error = %v, want ErrInvalidAcknowledgement", err)
	}
}

func TestConsumerRetryBackoffResetsOnAckAndIsBounded(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	consumer := mustConsumer(t, "test.consumer", mustSubscription(t, EventTypeAuditCheckpointPending), createdAt)
	owner := mustLeaseOwner(t, "worker-1")
	consumer, _, err := consumer.AcquireLease(owner, createdAt, createdAt.Add(MaximumLeaseDuration))
	if err != nil {
		t.Fatalf("AcquireLease() error = %v", err)
	}
	consumer, _, err = consumer.RecordRetry(owner, "checkpoint.upload_failed", createdAt.Add(time.Second))
	if err != nil {
		t.Fatalf("RecordRetry() error = %v", err)
	}
	retried := consumer.Snapshot()
	if retried.RetryCount != 1 || !retried.NextAttemptAt.Equal(createdAt.Add(time.Second+InitialRetryBackoff)) {
		t.Fatalf("retry state = count %d, next %v", retried.RetryCount, retried.NextAttemptAt)
	}
	if _, _, err := consumer.RecordRetry(owner, "checkpoint.upload_failed", createdAt.Add(1500*time.Millisecond)); !errors.Is(err, ErrBackoffActive) {
		t.Fatalf("early retry error = %v, want ErrBackoffActive", err)
	}
	consumer, _, err = consumer.ReleaseLease(owner, createdAt.Add(1500*time.Millisecond))
	if err != nil {
		t.Fatalf("ReleaseLease() error = %v", err)
	}
	if _, _, err := consumer.AcquireLease(owner, createdAt.Add(1500*time.Millisecond), createdAt.Add(time.Minute)); !errors.Is(err, ErrBackoffActive) {
		t.Fatalf("early reacquire error = %v, want ErrBackoffActive", err)
	}
	consumer, _, err = consumer.AcquireLease(owner, retried.NextAttemptAt, retried.NextAttemptAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("AcquireLease(at retry deadline) error = %v", err)
	}

	consumer, _, err = consumer.Acknowledge(owner, 0, 3, createdAt.Add(3*time.Second))
	if err != nil {
		t.Fatalf("Acknowledge() error = %v", err)
	}
	acked := consumer.Snapshot()
	if acked.RetryCount != 0 || !acked.NextAttemptAt.IsZero() || acked.LastErrorCode != "" {
		t.Fatalf("ack did not reset retry state: %+v", acked)
	}

	exhausted := consumer.Snapshot()
	exhausted.RetryCount = MaximumRetryCount
	exhausted.NextAttemptAt = createdAt.Add(3 * time.Second)
	exhausted.LastErrorCode = "checkpoint.upload_failed"
	consumer, err = RestoreConsumer(exhausted)
	if err != nil {
		t.Fatalf("RestoreConsumer(exhausted) error = %v", err)
	}
	if _, _, err := consumer.RecordRetry(owner, "checkpoint.upload_failed", createdAt.Add(4*time.Second)); !errors.Is(err, ErrRetryExhausted) {
		t.Fatalf("exhausted retry error = %v, want ErrRetryExhausted", err)
	}

	nearCap := consumer.Snapshot()
	nearCap.RetryCount = 9
	nearCap.NextAttemptAt = createdAt.Add(3 * time.Second)
	nearCap.LastErrorCode = "checkpoint.upload_failed"
	consumer, err = RestoreConsumer(nearCap)
	if err != nil {
		t.Fatalf("RestoreConsumer(nearCap) error = %v", err)
	}
	consumer, _, err = consumer.RecordRetry(owner, "checkpoint.upload_failed", createdAt.Add(4*time.Second))
	if err != nil {
		t.Fatalf("capped RecordRetry() error = %v", err)
	}
	if got := consumer.Snapshot().NextAttemptAt.Sub(createdAt.Add(4 * time.Second)); got != MaximumRetryBackoff {
		t.Fatalf("capped retry delay = %v, want %v", got, MaximumRetryBackoff)
	}
}

func TestConsumerSnapshotIsImmutableAndCASIncludesLeaseExpiry(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	consumer := mustConsumer(t, "test.consumer", mustSubscription(t, EventTypeAuditCheckpointPending), createdAt)
	owner := mustLeaseOwner(t, "worker-1")
	leaseUntil := createdAt.Add(time.Minute)
	consumer, transition, err := consumer.AcquireLease(owner, createdAt, leaseUntil)
	if err != nil {
		t.Fatalf("AcquireLease() error = %v", err)
	}

	expected := transition.Expected()
	next := transition.Next()
	if !expected.LeaseUntil.IsZero() || !next.LeaseUntil.Equal(leaseUntil) {
		t.Fatalf("lease CAS expiry = %v -> %v", expected.LeaseUntil, next.LeaseUntil)
	}
	next.Subscriptions[0] = "identity.user.deleted"
	if !transition.Next().Subscriptions[0].Valid() || transition.Next().Subscriptions[0] != EventTypeAuditCheckpointPending {
		t.Fatal("ConsumerCAS returned mutable next subscriptions")
	}

	snapshot := consumer.Snapshot()
	snapshot.Subscriptions[0] = "identity.user.deleted"
	if consumer.Snapshot().Subscriptions[0] != EventTypeAuditCheckpointPending {
		t.Fatal("Consumer Snapshot returned mutable subscriptions")
	}
}

func TestResolveRegistrationIsIdempotentWithoutResettingState(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	subscription := mustSubscription(t, EventTypeAuditCheckpointPending)
	requested := mustConsumer(t, ConsumerIDAuditCheckpoint, subscription, createdAt)
	existing := requested
	owner := mustLeaseOwner(t, "worker-1")
	existing, _, err := existing.AcquireLease(owner, createdAt, createdAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("AcquireLease() error = %v", err)
	}
	existing, _, err = existing.Acknowledge(owner, 0, 8, createdAt.Add(time.Second))
	if err != nil {
		t.Fatalf("Acknowledge() error = %v", err)
	}

	resolved, err := ResolveRegistration(existing, requested)
	if err != nil {
		t.Fatalf("ResolveRegistration() error = %v", err)
	}
	if resolved.Snapshot().LastAckedSequence != 8 || resolved.Snapshot().LeaseOwner != owner {
		t.Fatalf("idempotent registration reset existing state: %+v", resolved.Snapshot())
	}

	conflicting := mustConsumer(t, ConsumerIDAuditCheckpoint, mustSubscription(t, "identity.user.deleted"), createdAt)
	if _, err := ResolveRegistration(existing, conflicting); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("conflicting registration error = %v, want ErrAlreadyExists", err)
	}
}

func mustSubscription(t *testing.T, eventTypes ...EventType) Subscription {
	t.Helper()
	subscription, err := NewSubscription(eventTypes...)
	if err != nil {
		t.Fatalf("NewSubscription() error = %v", err)
	}
	return subscription
}

func mustConsumer(t *testing.T, id ConsumerID, subscription Subscription, createdAt time.Time) Consumer {
	t.Helper()
	consumer, err := NewConsumer(id, subscription, createdAt)
	if err != nil {
		t.Fatalf("NewConsumer() error = %v", err)
	}
	return consumer
}

func mustLeaseOwner(t *testing.T, value string) LeaseOwner {
	t.Helper()
	owner, err := ParseLeaseOwner(value)
	if err != nil {
		t.Fatalf("ParseLeaseOwner() error = %v", err)
	}
	return owner
}
