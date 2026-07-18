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
		return fmt.Errorf("begin PostgreSQL transaction: %w", err)
	}
	// finished prevents a successful commit from being followed by the deferred rollback path.
	finished := false
	defer func() {
		recovered := recover()
		if !finished {
			rollbackErr := rollbackTransaction(ctx, transaction)
			if recovered == nil && rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("rollback PostgreSQL transaction: %w", rollbackErr))
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
		return fmt.Errorf("commit PostgreSQL transaction: %w", commitErr)
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
