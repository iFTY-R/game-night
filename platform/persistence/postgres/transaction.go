package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// transactionCleanupTimeout bounds rollback after the request context has failed or been canceled.
const transactionCleanupTimeout = 5 * time.Second

// transactionCommitOperation identifies the only lifecycle phase where deferred constraints can surface.
const transactionCommitOperation = "commit PostgreSQL transaction"

// QueryHandle exposes generated statements while keeping pgx.Tx and transaction lifecycle private.
type QueryHandle = sqlcgen.Querier

// TransactionWork receives queries bound to exactly one transaction for the callback lifetime.
type TransactionWork func(context.Context, QueryHandle) error

type transactionHandle interface {
	sqlcgen.DBTX
	Commit(context.Context) error
	Rollback(context.Context) error
}

type beginTransaction func(context.Context, pgx.TxOptions) (transactionHandle, error)

// transactionLifecycleError marks failures owned by the transaction runner so domain adapters can
// hide driver diagnostics without treating callback business errors as infrastructure failures.
type transactionLifecycleError struct {
	operation string
	cause     error
}

func (failure *transactionLifecycleError) Error() string {
	return failure.operation + ": " + failure.cause.Error()
}

func (failure *transactionLifecycleError) Unwrap() error {
	return failure.cause
}

func newTransactionLifecycleError(operation string, cause error) error {
	return &transactionLifecycleError{operation: operation, cause: cause}
}

// mapUnitOfWorkError prevents transaction lifecycle diagnostics from crossing a domain boundary.
// Callback errors remain distinguishable: context termination and known domain sentinels are
// normalized, while unexpected callback errors remain available to their owning application layer.
func mapUnitOfWorkError(err, repositoryUnavailable error, domainErrors ...error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var lifecycleFailure *transactionLifecycleError
	if errors.As(err, &lifecycleFailure) {
		return repositoryUnavailable
	}
	for _, domainErr := range domainErrors {
		if errors.Is(err, domainErr) {
			return domainErr
		}
	}
	return err
}

// TransactionRunner owns begin, commit, rollback, and panic cleanup for PostgreSQL units of work.
type TransactionRunner struct {
	begin beginTransaction
}

// NewTransactionRunner binds transaction creation to the supplied runtime pool.
func NewTransactionRunner(pool *pgxpool.Pool) *TransactionRunner {
	if pool == nil {
		panic("postgres transaction runner requires a pool")
	}
	return newTransactionRunner(func(ctx context.Context, options pgx.TxOptions) (transactionHandle, error) {
		return pool.BeginTx(ctx, options)
	})
}

func newTransactionRunner(begin beginTransaction) *TransactionRunner {
	return &TransactionRunner{begin: begin}
}

// Run executes work with PostgreSQL's default transaction options.
func (runner *TransactionRunner) Run(ctx context.Context, work TransactionWork) error {
	return runner.RunWithOptions(ctx, pgx.TxOptions{}, work)
}

// RunWithOptions commits only after work succeeds and preserves callback, commit, and rollback failures.
func (runner *TransactionRunner) RunWithOptions(ctx context.Context, options pgx.TxOptions, work TransactionWork) (err error) {
	if work == nil {
		return errors.New("PostgreSQL transaction work is required")
	}
	transaction, err := runner.begin(ctx, options)
	if err != nil {
		return newTransactionLifecycleError("begin PostgreSQL transaction", err)
	}
	// finished prevents a successful commit from being followed by the deferred rollback path.
	finished := false
	defer func() {
		recovered := recover()
		if !finished {
			rollbackErr := rollbackTransaction(ctx, transaction)
			if recovered == nil && rollbackErr != nil {
				err = errors.Join(err, newTransactionLifecycleError("rollback PostgreSQL transaction", rollbackErr))
			}
		}
		if recovered != nil {
			panic(recovered)
		}
	}()

	queries := sqlcgen.New(transaction)
	if workErr := work(ctx, queries); workErr != nil {
		return fmt.Errorf("run PostgreSQL transaction work: %w", workErr)
	}
	if commitErr := transaction.Commit(ctx); commitErr != nil {
		return newTransactionLifecycleError(transactionCommitOperation, commitErr)
	}
	finished = true
	return nil
}

func rollbackTransaction(ctx context.Context, transaction transactionHandle) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), transactionCleanupTimeout)
	defer cancel()
	if err := transaction.Rollback(cleanupCtx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		return err
	}
	return nil
}
