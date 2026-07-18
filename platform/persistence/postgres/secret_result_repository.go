package postgres

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/iFTY-R/game-night/platform/secretresult"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type secretResultQueries interface {
	CreateSecretOperationResult(context.Context, sqlcgen.CreateSecretOperationResultParams) (sqlcgen.SecretOperationResult, error)
	GetSecretOperationResultByIDForUpdate(context.Context, sqlcgen.GetSecretOperationResultByIDForUpdateParams) (sqlcgen.SecretOperationResult, error)
	GetSecretOperationResultByOperationForUpdate(context.Context, sqlcgen.GetSecretOperationResultByOperationForUpdateParams) (sqlcgen.SecretOperationResult, error)
	ConfirmSecretOperationResultCAS(context.Context, sqlcgen.ConfirmSecretOperationResultCASParams) (sqlcgen.ConfirmSecretOperationResultCASRow, error)
	ExpireSecretOperationResultCAS(context.Context, sqlcgen.ExpireSecretOperationResultCASParams) (sqlcgen.ExpireSecretOperationResultCASRow, error)
}

// SecretResultRepository maps generated queries to persistence-neutral result values.
// It must be used inside a UnitOfWork because replay authorization relies on row locks surviving until commit.
type SecretResultRepository struct {
	queries secretResultQueries
}

func newSecretResultRepository(queries secretResultQueries) *SecretResultRepository {
	return &SecretResultRepository{queries: queries}
}

// GetByIDForUpdate locks the authorization-selected result before callers trust its persisted operation binding.
func (repository *SecretResultRepository) GetByIDForUpdate(ctx context.Context, resultID uuid.UUID) (secretresult.Result, error) {
	if resultID == uuid.Nil {
		return secretresult.Result{}, secretresult.ErrInvalidInput
	}
	row, err := repository.queries.GetSecretOperationResultByIDForUpdate(
		ctx, sqlcgen.GetSecretOperationResultByIDForUpdateParams{ResultID: uuidToPG(resultID)},
	)
	if err != nil {
		return secretresult.Result{}, mapSecretResultQueryError(ctx, err, secretresult.ErrNotFound)
	}
	return secretResultFromRow(row)
}

// GetByOperationForUpdate locks the composite operation row for retry, confirm, or cleanup arbitration.
func (repository *SecretResultRepository) GetByOperationForUpdate(ctx context.Context, key secretresult.Key) (secretresult.Result, error) {
	if key.Validate() != nil {
		return secretresult.Result{}, secretresult.ErrInvalidInput
	}
	row, err := repository.queries.GetSecretOperationResultByOperationForUpdate(ctx, sqlcgen.GetSecretOperationResultByOperationForUpdateParams{
		OperationScope:     string(key.Scope),
		ActorOrChallengeID: uuidToPG(key.ActorID),
		OperationID:        key.OperationID.Value(),
	})
	if err != nil {
		return secretresult.Result{}, mapSecretResultQueryError(ctx, err, secretresult.ErrNotFound)
	}
	return secretResultFromRow(row)
}

// InsertAvailable inserts once, then classifies a unique-key miss by rereading the committed operation.
func (repository *SecretResultRepository) InsertAvailable(ctx context.Context, result secretresult.Result) (secretresult.Result, error) {
	snapshot := result.Snapshot()
	params, err := createSecretResultParams(snapshot)
	if err != nil {
		return secretresult.Result{}, err
	}
	row, err := repository.queries.CreateSecretOperationResult(ctx, params)
	if err == nil {
		return secretResultFromRow(row)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return secretresult.Result{}, mapSecretResultQueryError(ctx, err, secretresult.ErrRepositoryUnavailable)
	}
	// ON CONFLICT DO NOTHING intentionally leaves the transaction usable for exact-replay classification.
	existing, getErr := repository.GetByOperationForUpdate(ctx, snapshot.Binding.Key)
	if getErr != nil {
		return secretresult.Result{}, getErr
	}
	if _, resolveErr := existing.Resolve(snapshot.Binding, snapshot.CompletedAt); resolveErr != nil {
		return secretresult.Result{}, resolveErr
	}
	return existing, nil
}

// ConfirmCAS erases all secret columns only when the complete binding still matches an available row.
func (repository *SecretResultRepository) ConfirmCAS(ctx context.Context, confirmation secretresult.Confirmation) (secretresult.Result, error) {
	if confirmation.ResultID == uuid.Nil || confirmation.Binding.Validate() != nil || confirmation.ConfirmedAt.IsZero() ||
		confirmation.Binding.ResultVersion > math.MaxInt32 {
		return secretresult.Result{}, secretresult.ErrInvalidInput
	}
	_, err := repository.queries.ConfirmSecretOperationResultCAS(ctx, sqlcgen.ConfirmSecretOperationResultCASParams{
		ConfirmedAt:        timeToPG(confirmation.ConfirmedAt),
		ResultID:           uuidToPG(confirmation.ResultID),
		OperationScope:     string(confirmation.Binding.Key.Scope),
		ActorOrChallengeID: uuidToPG(confirmation.Binding.Key.ActorID),
		OperationID:        confirmation.Binding.Key.OperationID.Value(),
		RequestDigest:      confirmation.Binding.RequestDigest.Bytes(),
		ResultType:         string(confirmation.Binding.ResultType),
		ResultVersion:      int32(confirmation.Binding.ResultVersion),
	})
	if err != nil {
		return secretresult.Result{}, mapSecretResultQueryError(ctx, err, secretresult.ErrConcurrentTransition)
	}
	return repository.GetByOperationForUpdate(ctx, confirmation.Binding.Key)
}

// ExpireCAS performs TTL erasure and derives the terminal domain value from the row already locked by the caller.
func (repository *SecretResultRepository) ExpireCAS(ctx context.Context, current secretresult.Result, expiredAt time.Time) (secretresult.Result, error) {
	snapshot := current.Snapshot()
	row, err := repository.queries.ExpireSecretOperationResultCAS(ctx, sqlcgen.ExpireSecretOperationResultCASParams{
		ResultID: uuidToPG(snapshot.ID), ExpiredAt: timeToPG(expiredAt),
	})
	if err != nil {
		return secretresult.Result{}, mapSecretResultQueryError(ctx, err, secretresult.ErrConcurrentTransition)
	}
	if row.Status != string(secretresult.StatusExpired) {
		return secretresult.Result{}, secretresult.ErrIntegrity
	}
	return current.Expire(expiredAt)
}

// SecretResultUnitOfWork binds repository row locks and CAS transitions to one PostgreSQL transaction.
type SecretResultUnitOfWork struct {
	runner *TransactionRunner
}

// NewSecretResultUnitOfWork builds a standalone result workflow over the supplied runtime pool.
func NewSecretResultUnitOfWork(pool *pgxpool.Pool) *SecretResultUnitOfWork {
	return &SecretResultUnitOfWork{runner: NewTransactionRunner(pool)}
}

// Run implements secretresult.UnitOfWork without exposing generated query or pgx transaction types.
func (unitOfWork *SecretResultUnitOfWork) Run(ctx context.Context, work secretresult.TransactionWork) error {
	if work == nil {
		return secretresult.ErrInvalidInput
	}
	err := unitOfWork.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		return work(ctx, newSecretResultRepository(queries))
	})
	return mapUnitOfWorkError(err, secretresult.ErrRepositoryUnavailable, secretResultDomainErrors...)
}

// secretResultDomainErrors is the closed set that a PostgreSQL unit of work may return unchanged.
var secretResultDomainErrors = []error{
	secretresult.ErrInvalidInput,
	secretresult.ErrIdempotencyConflict,
	secretresult.ErrReplayUnauthorized,
	secretresult.ErrSecretNoLongerAvailable,
	secretresult.ErrConcurrentTransition,
	secretresult.ErrNotFound,
	secretresult.ErrRepositoryUnavailable,
	secretresult.ErrIntegrity,
	secretresult.ErrEnvelopeAuthentication,
}

func createSecretResultParams(snapshot secretresult.Snapshot) (sqlcgen.CreateSecretOperationResultParams, error) {
	if snapshot.ID == uuid.Nil || snapshot.Binding.Validate() != nil || snapshot.Status != secretresult.StatusAvailable ||
		snapshot.Payload.KeyVersion == 0 || snapshot.Payload.KeyVersion > math.MaxInt32 || snapshot.Binding.ResultVersion > math.MaxInt32 {
		return sqlcgen.CreateSecretOperationResultParams{}, secretresult.ErrInvalidInput
	}
	return sqlcgen.CreateSecretOperationResultParams{
		ResultID:           uuidToPG(snapshot.ID),
		OperationScope:     string(snapshot.Binding.Key.Scope),
		ActorOrChallengeID: uuidToPG(snapshot.Binding.Key.ActorID),
		OperationID:        snapshot.Binding.Key.OperationID.Value(),
		RequestDigest:      snapshot.Binding.RequestDigest.Bytes(),
		ResultType:         string(snapshot.Binding.ResultType),
		ResultVersion:      int32(snapshot.Binding.ResultVersion),
		Ciphertext:         snapshot.Payload.Ciphertext,
		Nonce:              snapshot.Payload.Nonce,
		WrappedDataKey:     snapshot.Payload.WrappedDataKey,
		KeyVersion:         int32(snapshot.Payload.KeyVersion),
		SecretExpiresAt:    timeToPG(snapshot.SecretExpiresAt),
		CompletedAt:        timeToPG(snapshot.CompletedAt),
		TombstoneExpiresAt: timeToPG(snapshot.TombstoneExpiresAt),
	}, nil
}

func secretResultFromRow(row sqlcgen.SecretOperationResult) (secretresult.Result, error) {
	if !row.ResultID.Valid || !row.ActorOrChallengeID.Valid || !row.SecretExpiresAt.Valid || !row.CompletedAt.Valid ||
		!row.TombstoneExpiresAt.Valid || row.ResultVersion <= 0 || row.KeyVersion <= 0 {
		return secretresult.Result{}, secretresult.ErrIntegrity
	}
	operationID, err := secretresult.ParseOperationID(row.OperationID)
	if err != nil {
		return secretresult.Result{}, secretresult.ErrIntegrity
	}
	digest, err := secretresult.NewDigest(row.RequestDigest)
	if err != nil {
		return secretresult.Result{}, secretresult.ErrIntegrity
	}
	snapshot := secretresult.Snapshot{
		ID: uuid.UUID(row.ResultID.Bytes),
		Binding: secretresult.Binding{
			Key: secretresult.Key{
				Scope: secretresult.Scope(row.OperationScope), ActorID: uuid.UUID(row.ActorOrChallengeID.Bytes), OperationID: operationID,
			},
			RequestDigest: digest, ResultType: secretresult.ResultType(row.ResultType), ResultVersion: uint32(row.ResultVersion),
		},
		Payload: secretresult.EncryptedPayload{
			Ciphertext: row.Ciphertext, Nonce: row.Nonce, WrappedDataKey: row.WrappedDataKey, KeyVersion: uint32(row.KeyVersion),
		},
		Status:             secretresult.Status(row.Status),
		SecretExpiresAt:    row.SecretExpiresAt.Time,
		CompletedAt:        row.CompletedAt.Time,
		TombstoneExpiresAt: row.TombstoneExpiresAt.Time,
	}
	if row.ConfirmedAt.Valid {
		snapshot.ConfirmedAt = row.ConfirmedAt.Time
	}
	result, err := secretresult.Restore(snapshot)
	if err != nil {
		return secretresult.Result{}, secretresult.ErrIntegrity
	}
	return result, nil
}

func uuidToPG(value uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: value, Valid: true}
}

func timeToPG(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC().Truncate(time.Microsecond), Valid: !value.IsZero()}
}

func mapSecretResultQueryError(ctx context.Context, err, noRowsError error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return noRowsError
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	return secretresult.ErrRepositoryUnavailable
}

var _ secretresult.Repository = (*SecretResultRepository)(nil)
var _ secretresult.UnitOfWork = (*SecretResultUnitOfWork)(nil)
