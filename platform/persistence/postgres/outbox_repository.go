package postgres

import (
	"bytes"
	"context"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type outboxEventQueries interface {
	CreateOutboxEvent(context.Context, sqlcgen.CreateOutboxEventParams) (sqlcgen.OutboxEvent, error)
	GetOutboxEventByID(context.Context, sqlcgen.GetOutboxEventByIDParams) (sqlcgen.OutboxEvent, error)
}

type outboxConsumerQueries interface {
	CreateOutboxConsumer(context.Context, sqlcgen.CreateOutboxConsumerParams) (sqlcgen.OutboxConsumer, error)
	GetOutboxConsumer(context.Context, sqlcgen.GetOutboxConsumerParams) (sqlcgen.OutboxConsumer, error)
	AcquireOutboxConsumerLeaseCAS(context.Context, sqlcgen.AcquireOutboxConsumerLeaseCASParams) (sqlcgen.OutboxConsumer, error)
	RenewOutboxConsumerLeaseCAS(context.Context, sqlcgen.RenewOutboxConsumerLeaseCASParams) (sqlcgen.OutboxConsumer, error)
	ReleaseOutboxConsumerLeaseCAS(context.Context, sqlcgen.ReleaseOutboxConsumerLeaseCASParams) (sqlcgen.OutboxConsumer, error)
	ListOutboxEventsForConsumer(context.Context, sqlcgen.ListOutboxEventsForConsumerParams) ([]sqlcgen.ListOutboxEventsForConsumerRow, error)
	AckOutboxConsumerOffsetCAS(context.Context, sqlcgen.AckOutboxConsumerOffsetCASParams) (sqlcgen.OutboxConsumer, error)
	RecordOutboxConsumerRetryCAS(context.Context, sqlcgen.RecordOutboxConsumerRetryCASParams) (sqlcgen.OutboxConsumer, error)
}

type checkpointProgressQueries interface {
	GetLatestAckedOutboxEventByType(context.Context, sqlcgen.GetLatestAckedOutboxEventByTypeParams) (sqlcgen.OutboxEvent, error)
	ReadCheckpointConsumerSequence(context.Context) (int64, error)
}

// OutboxEventRepository appends immutable events and classifies event-ID replay without aborting a transaction.
type OutboxEventRepository struct {
	queries outboxEventQueries
}

func newOutboxEventRepository(queries outboxEventQueries) *OutboxEventRepository {
	return &OutboxEventRepository{queries: queries}
}

// Insert creates one event or returns the byte-identical existing event for an idempotent retry.
func (repository *OutboxEventRepository) Insert(ctx context.Context, event outbox.Event) (outbox.Event, error) {
	snapshot := event.Snapshot()
	if snapshot.Sequence != 0 {
		return outbox.Event{}, outbox.ErrInvalidInput
	}
	row, err := repository.queries.CreateOutboxEvent(ctx, sqlcgen.CreateOutboxEventParams{
		EventID:       uuidToPG(snapshot.ID),
		EventType:     snapshot.Type.Value(),
		AggregateType: snapshot.AggregateType.Value(),
		AggregateID:   uuidToPG(snapshot.AggregateID),
		Payload:       snapshot.Payload,
		CreatedAt:     timeToPG(snapshot.CreatedAt),
		AvailableAt:   timeToPG(snapshot.AvailableAt),
	})
	if err == nil {
		return outboxEventFromRow(row)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return outbox.Event{}, mapOutboxQueryError(ctx, err, outbox.ErrRepositoryUnavailable)
	}
	// ON CONFLICT DO NOTHING keeps the transaction usable so exact replay can be distinguished from ID reuse.
	existingRow, getErr := repository.queries.GetOutboxEventByID(ctx, sqlcgen.GetOutboxEventByIDParams{EventID: uuidToPG(snapshot.ID)})
	if getErr != nil {
		return outbox.Event{}, mapOutboxQueryError(ctx, getErr, outbox.ErrNotFound)
	}
	existing, parseErr := outboxEventFromRow(existingRow)
	if parseErr != nil {
		return outbox.Event{}, parseErr
	}
	if !sameUnsequencedEvent(existing.Snapshot(), snapshot) {
		return outbox.Event{}, outbox.ErrAlreadyExists
	}
	return existing, nil
}

// OutboxConsumerRepository owns durable registration, leases, offsets, and retry CAS transitions.
type OutboxConsumerRepository struct {
	queries outboxConsumerQueries
}

func newOutboxConsumerRepository(queries outboxConsumerQueries) *OutboxConsumerRepository {
	return &OutboxConsumerRepository{queries: queries}
}

// Insert registers a new consumer without resetting state when the same subscription is registered again.
func (repository *OutboxConsumerRepository) Insert(ctx context.Context, consumer outbox.Consumer) (outbox.Consumer, error) {
	snapshot := consumer.Snapshot()
	lastAcked, err := snapshot.LastAckedSequence.Int64()
	if err != nil || lastAcked != 0 || snapshot.LeaseOwner != "" || snapshot.RetryCount != 0 ||
		!snapshot.CreatedAt.Equal(snapshot.UpdatedAt) {
		return outbox.Consumer{}, outbox.ErrInvalidInput
	}
	row, err := repository.queries.CreateOutboxConsumer(ctx, sqlcgen.CreateOutboxConsumerParams{
		ConsumerID: snapshot.ID.Value(), Subscriptions: eventTypeStrings(snapshot.Subscriptions),
		LastAckedSequence: lastAcked, CreatedAt: timeToPG(snapshot.CreatedAt),
	})
	if err == nil {
		return outboxConsumerFromRow(row)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return outbox.Consumer{}, mapOutboxQueryError(ctx, err, outbox.ErrRepositoryUnavailable)
	}
	existing, getErr := repository.Get(ctx, snapshot.ID)
	if getErr != nil {
		return outbox.Consumer{}, getErr
	}
	return outbox.ResolveRegistration(existing, consumer)
}

// Get restores the complete consumer snapshot used to derive all subsequent CAS transitions.
func (repository *OutboxConsumerRepository) Get(ctx context.Context, consumerID outbox.ConsumerID) (outbox.Consumer, error) {
	if !consumerID.Valid() {
		return outbox.Consumer{}, outbox.ErrInvalidInput
	}
	row, err := repository.queries.GetOutboxConsumer(ctx, sqlcgen.GetOutboxConsumerParams{ConsumerID: consumerID.Value()})
	if err != nil {
		return outbox.Consumer{}, mapOutboxQueryError(ctx, err, outbox.ErrNotFound)
	}
	return outboxConsumerFromRow(row)
}

// AcquireLeaseCAS applies the complete expected snapshot so a stale worker cannot replace a newer lease or offset.
func (repository *OutboxConsumerRepository) AcquireLeaseCAS(ctx context.Context, transition outbox.ConsumerCAS) (outbox.Consumer, error) {
	values, err := validateConsumerCAS(transition, outbox.TransitionAcquireLease)
	if err != nil {
		return outbox.Consumer{}, err
	}
	row, err := repository.queries.AcquireOutboxConsumerLeaseCAS(ctx, sqlcgen.AcquireOutboxConsumerLeaseCASParams{
		NextLeaseOwner: textToPG(values.next.LeaseOwner.Value()), NextLeaseUntil: timeToPG(values.next.LeaseUntil),
		NextUpdatedAt: timeToPG(values.next.UpdatedAt), ConsumerID: values.expected.ID.Value(),
		ExpectedLeaseOwner: textToPG(values.expected.LeaseOwner.Value()), ExpectedLeaseUntil: timeToPG(values.expected.LeaseUntil),
		ExpectedSequence: values.expectedSequence, ExpectedRetryCount: int32(values.expected.RetryCount),
		ExpectedNextAttemptAt: timeToPG(values.expected.NextAttemptAt), ExpectedErrorCode: textToPG(values.expected.LastErrorCode.Value()),
		ExpectedUpdatedAt: timeToPG(values.expected.UpdatedAt),
	})
	return repository.finishCAS(ctx, row, err, values.next)
}

// RenewLeaseCAS extends only the exact lease snapshot from which the domain transition was derived.
func (repository *OutboxConsumerRepository) RenewLeaseCAS(ctx context.Context, transition outbox.ConsumerCAS) (outbox.Consumer, error) {
	values, err := validateConsumerCAS(transition, outbox.TransitionRenewLease)
	if err != nil {
		return outbox.Consumer{}, err
	}
	row, err := repository.queries.RenewOutboxConsumerLeaseCAS(ctx, sqlcgen.RenewOutboxConsumerLeaseCASParams{
		NextLeaseUntil: timeToPG(values.next.LeaseUntil), NextUpdatedAt: timeToPG(values.next.UpdatedAt),
		ConsumerID: values.expected.ID.Value(), ExpectedLeaseOwner: textToPG(values.expected.LeaseOwner.Value()),
		ExpectedLeaseUntil: timeToPG(values.expected.LeaseUntil), ExpectedSequence: values.expectedSequence,
		ExpectedRetryCount: int32(values.expected.RetryCount), ExpectedNextAttemptAt: timeToPG(values.expected.NextAttemptAt),
		ExpectedErrorCode: textToPG(values.expected.LastErrorCode.Value()), ExpectedUpdatedAt: timeToPG(values.expected.UpdatedAt),
	})
	return repository.finishCAS(ctx, row, err, values.next)
}

// ReleaseLeaseCAS clears ownership only when every concurrency-sensitive field still matches.
func (repository *OutboxConsumerRepository) ReleaseLeaseCAS(ctx context.Context, transition outbox.ConsumerCAS) (outbox.Consumer, error) {
	values, err := validateConsumerCAS(transition, outbox.TransitionReleaseLease)
	if err != nil {
		return outbox.Consumer{}, err
	}
	row, err := repository.queries.ReleaseOutboxConsumerLeaseCAS(ctx, sqlcgen.ReleaseOutboxConsumerLeaseCASParams{
		ReleasedAt: timeToPG(values.next.UpdatedAt), ConsumerID: values.expected.ID.Value(),
		ExpectedLeaseOwner: textToPG(values.expected.LeaseOwner.Value()), ExpectedLeaseUntil: timeToPG(values.expected.LeaseUntil),
		ExpectedSequence: values.expectedSequence, ExpectedRetryCount: int32(values.expected.RetryCount),
		ExpectedNextAttemptAt: timeToPG(values.expected.NextAttemptAt), ExpectedErrorCode: textToPG(values.expected.LastErrorCode.Value()),
		ExpectedUpdatedAt: timeToPG(values.expected.UpdatedAt),
	})
	return repository.finishCAS(ctx, row, err, values.next)
}

// ListAvailable returns only subscribed, available events while the supplied owner holds an active lease.
func (repository *OutboxConsumerRepository) ListAvailable(ctx context.Context, batch outbox.EventBatch) ([]outbox.Event, error) {
	validated, err := outbox.NewEventBatch(batch.ConsumerID, batch.LeaseOwner, batch.ReadAt, batch.BatchSize)
	if err != nil || validated.BatchSize > math.MaxInt32 {
		return nil, outbox.ErrInvalidInput
	}
	rows, err := repository.queries.ListOutboxEventsForConsumer(ctx, sqlcgen.ListOutboxEventsForConsumerParams{
		ConsumerID: validated.ConsumerID.Value(), LeaseOwner: textToPG(validated.LeaseOwner.Value()),
		ReadAt: timeToPG(validated.ReadAt), BatchSize: int32(validated.BatchSize),
	})
	if err != nil {
		return nil, mapOutboxQueryError(ctx, err, outbox.ErrRepositoryUnavailable)
	}
	events := make([]outbox.Event, 0, len(rows))
	for _, row := range rows {
		event, parseErr := outboxEventFromListRow(row)
		if parseErr != nil {
			return nil, parseErr
		}
		events = append(events, event)
	}
	return events, nil
}

func outboxEventFromListRow(row sqlcgen.ListOutboxEventsForConsumerRow) (outbox.Event, error) {
	return outboxEventFromRow(sqlcgen.OutboxEvent{
		EventSequence: row.EventSequence,
		EventID:       row.EventID,
		EventType:     row.EventType,
		AggregateType: row.AggregateType,
		AggregateID:   row.AggregateID,
		Payload:       row.Payload,
		CreatedAt:     row.CreatedAt,
		AvailableAt:   row.AvailableAt,
	})
}

// AcknowledgeCAS advances only the exact offset snapshot and relies on SQL to verify the subscribed event exists.
func (repository *OutboxConsumerRepository) AcknowledgeCAS(ctx context.Context, transition outbox.ConsumerCAS) (outbox.Consumer, error) {
	values, err := validateConsumerCAS(transition, outbox.TransitionAcknowledge)
	if err != nil {
		return outbox.Consumer{}, err
	}
	ackedSequence, err := values.next.LastAckedSequence.Int64()
	if err != nil {
		return outbox.Consumer{}, outbox.ErrInvalidInput
	}
	row, err := repository.queries.AckOutboxConsumerOffsetCAS(ctx, sqlcgen.AckOutboxConsumerOffsetCASParams{
		AckedSequence: ackedSequence, AckedAt: timeToPG(values.next.UpdatedAt), ConsumerID: values.expected.ID.Value(),
		ExpectedLeaseOwner: textToPG(values.expected.LeaseOwner.Value()), ExpectedLeaseUntil: timeToPG(values.expected.LeaseUntil),
		ExpectedSequence: values.expectedSequence, ExpectedRetryCount: int32(values.expected.RetryCount),
		ExpectedNextAttemptAt: timeToPG(values.expected.NextAttemptAt), ExpectedErrorCode: textToPG(values.expected.LastErrorCode.Value()),
		ExpectedUpdatedAt: timeToPG(values.expected.UpdatedAt),
	})
	return repository.finishCAS(ctx, row, err, values.next)
}

// RecordRetryCAS persists the domain-calculated backoff without allowing concurrent retry counters to be skipped.
func (repository *OutboxConsumerRepository) RecordRetryCAS(ctx context.Context, transition outbox.ConsumerCAS) (outbox.Consumer, error) {
	values, err := validateConsumerCAS(transition, outbox.TransitionRecordRetry)
	if err != nil {
		return outbox.Consumer{}, err
	}
	row, err := repository.queries.RecordOutboxConsumerRetryCAS(ctx, sqlcgen.RecordOutboxConsumerRetryCASParams{
		NextRetryCount: int32(values.next.RetryCount), NextAttemptAt: timeToPG(values.next.NextAttemptAt),
		ErrorCode: textToPG(values.next.LastErrorCode.Value()), FailedAt: timeToPG(values.next.UpdatedAt),
		ConsumerID: values.expected.ID.Value(), ExpectedLeaseOwner: textToPG(values.expected.LeaseOwner.Value()),
		ExpectedLeaseUntil: timeToPG(values.expected.LeaseUntil), ExpectedSequence: values.expectedSequence,
		ExpectedRetryCount: int32(values.expected.RetryCount), ExpectedNextAttemptAt: timeToPG(values.expected.NextAttemptAt),
		ExpectedErrorCode: textToPG(values.expected.LastErrorCode.Value()), ExpectedUpdatedAt: timeToPG(values.expected.UpdatedAt),
	})
	return repository.finishCAS(ctx, row, err, values.next)
}

func (repository *OutboxConsumerRepository) finishCAS(
	ctx context.Context,
	row sqlcgen.OutboxConsumer,
	queryErr error,
	expectedNext outbox.ConsumerSnapshot,
) (outbox.Consumer, error) {
	if queryErr != nil {
		return outbox.Consumer{}, mapOutboxQueryError(ctx, queryErr, outbox.ErrConcurrentTransition)
	}
	consumer, err := outboxConsumerFromRow(row)
	if err != nil {
		return outbox.Consumer{}, err
	}
	if !sameConsumerSnapshot(consumer.Snapshot(), expectedNext) {
		return outbox.Consumer{}, outbox.ErrIntegrity
	}
	return consumer, nil
}

type consumerCASValues struct {
	expected         outbox.ConsumerSnapshot
	next             outbox.ConsumerSnapshot
	expectedSequence int64
}

func validateConsumerCAS(transition outbox.ConsumerCAS, kind outbox.ConsumerTransitionKind) (consumerCASValues, error) {
	if transition.Kind() != kind {
		return consumerCASValues{}, outbox.ErrInvalidInput
	}
	expected := transition.Expected()
	next := transition.Next()
	expectedSequence, err := expected.LastAckedSequence.Int64()
	if err != nil || expected.RetryCount > math.MaxInt32 || next.RetryCount > math.MaxInt32 ||
		expected.ID != next.ID || !sameEventTypes(expected.Subscriptions, next.Subscriptions) ||
		!expected.CreatedAt.Equal(next.CreatedAt) {
		return consumerCASValues{}, outbox.ErrInvalidInput
	}
	return consumerCASValues{expected: expected, next: next, expectedSequence: expectedSequence}, nil
}

func outboxEventFromRow(row sqlcgen.OutboxEvent) (outbox.Event, error) {
	if row.EventSequence <= 0 || !row.EventID.Valid || !row.AggregateID.Valid || !row.CreatedAt.Valid || !row.AvailableAt.Valid {
		return outbox.Event{}, outbox.ErrIntegrity
	}
	sequence, err := outbox.NewSequence(row.EventSequence)
	if err != nil {
		return outbox.Event{}, outbox.ErrIntegrity
	}
	eventType, err := outbox.ParseEventType(row.EventType)
	if err != nil {
		return outbox.Event{}, outbox.ErrIntegrity
	}
	aggregateType, err := outbox.ParseAggregateType(row.AggregateType)
	if err != nil {
		return outbox.Event{}, outbox.ErrIntegrity
	}
	event, err := outbox.RestoreEvent(outbox.EventSnapshot{
		Sequence: sequence, ID: uuid.UUID(row.EventID.Bytes), Type: eventType, AggregateType: aggregateType,
		AggregateID: uuid.UUID(row.AggregateID.Bytes), Payload: row.Payload,
		CreatedAt: row.CreatedAt.Time, AvailableAt: row.AvailableAt.Time,
	})
	if err != nil {
		return outbox.Event{}, outbox.ErrIntegrity
	}
	return event, nil
}

func outboxConsumerFromRow(row sqlcgen.OutboxConsumer) (outbox.Consumer, error) {
	if !row.CreatedAt.Valid || !row.UpdatedAt.Valid || row.LastAckedSequence < 0 || row.RetryCount < 0 {
		return outbox.Consumer{}, outbox.ErrIntegrity
	}
	consumerID, err := outbox.ParseConsumerID(row.ConsumerID)
	if err != nil {
		return outbox.Consumer{}, outbox.ErrIntegrity
	}
	subscriptions := make([]outbox.EventType, 0, len(row.Subscriptions))
	for _, value := range row.Subscriptions {
		eventType, parseErr := outbox.ParseEventType(value)
		if parseErr != nil {
			return outbox.Consumer{}, outbox.ErrIntegrity
		}
		subscriptions = append(subscriptions, eventType)
	}
	sequence, err := outbox.NewSequence(row.LastAckedSequence)
	if err != nil {
		return outbox.Consumer{}, outbox.ErrIntegrity
	}
	leaseOwner, err := optionalLeaseOwner(row.LeaseOwner)
	if err != nil {
		return outbox.Consumer{}, outbox.ErrIntegrity
	}
	errorCode, err := optionalErrorCode(row.LastErrorCode)
	if err != nil {
		return outbox.Consumer{}, outbox.ErrIntegrity
	}
	consumer, err := outbox.RestoreConsumer(outbox.ConsumerSnapshot{
		ID: consumerID, Subscriptions: subscriptions, LastAckedSequence: sequence,
		LeaseOwner: leaseOwner, LeaseUntil: optionalTime(row.LeaseUntil), RetryCount: uint32(row.RetryCount),
		NextAttemptAt: optionalTime(row.NextAttemptAt), LastErrorCode: errorCode,
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	})
	if err != nil {
		return outbox.Consumer{}, outbox.ErrIntegrity
	}
	return consumer, nil
}

func optionalLeaseOwner(value pgtype.Text) (outbox.LeaseOwner, error) {
	if !value.Valid {
		return "", nil
	}
	return outbox.ParseLeaseOwner(value.String)
}

func optionalErrorCode(value pgtype.Text) (outbox.ErrorCode, error) {
	if !value.Valid {
		return "", nil
	}
	return outbox.ParseErrorCode(value.String)
}

func optionalTime(value pgtype.Timestamptz) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time
}

func textToPG(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

func eventTypeStrings(values []outbox.EventType) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = value.Value()
	}
	return result
}

func sameUnsequencedEvent(existing, requested outbox.EventSnapshot) bool {
	return existing.ID == requested.ID && existing.Type == requested.Type && existing.AggregateType == requested.AggregateType &&
		existing.AggregateID == requested.AggregateID && bytes.Equal(existing.Payload, requested.Payload) &&
		existing.CreatedAt.Equal(requested.CreatedAt) && existing.AvailableAt.Equal(requested.AvailableAt)
}

func sameConsumerSnapshot(left, right outbox.ConsumerSnapshot) bool {
	return left.ID == right.ID && sameEventTypes(left.Subscriptions, right.Subscriptions) &&
		left.LastAckedSequence == right.LastAckedSequence && left.LeaseOwner == right.LeaseOwner &&
		left.LeaseUntil.Equal(right.LeaseUntil) && left.RetryCount == right.RetryCount &&
		left.NextAttemptAt.Equal(right.NextAttemptAt) && left.LastErrorCode == right.LastErrorCode &&
		left.CreatedAt.Equal(right.CreatedAt) && left.UpdatedAt.Equal(right.UpdatedAt)
}

func sameEventTypes(left, right []outbox.EventType) bool {
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

func mapOutboxQueryError(ctx context.Context, err, noRowsError error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return noRowsError
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	return outbox.ErrRepositoryUnavailable
}

var _ outbox.EventRepository = (*OutboxEventRepository)(nil)
var _ outbox.ConsumerRepository = (*OutboxConsumerRepository)(nil)
