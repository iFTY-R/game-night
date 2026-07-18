package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestTransactionRunnerCommitsSuccessfulWork(t *testing.T) {
	transaction := &fakeTransaction{}
	runner := newTransactionRunner(func(context.Context, pgx.TxOptions) (transactionHandle, error) {
		return transaction, nil
	})

	if err := runner.Run(context.Background(), func(context.Context, QueryHandle) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if transaction.commitCalls != 1 || transaction.rollbackCalls != 0 {
		t.Fatalf("unexpected transaction cleanup: commits=%d rollbacks=%d", transaction.commitCalls, transaction.rollbackCalls)
	}
}

func TestTransactionRunnerRollsBackCallbackError(t *testing.T) {
	workError := errors.New("work failed")
	transaction := &fakeTransaction{}
	runner := newTestTransactionRunner(transaction)

	err := runner.Run(context.Background(), func(context.Context, QueryHandle) error {
		return workError
	})
	if !errors.Is(err, workError) {
		t.Fatalf("expected callback error, got %v", err)
	}
	if transaction.commitCalls != 0 || transaction.rollbackCalls != 1 {
		t.Fatalf("unexpected transaction cleanup: commits=%d rollbacks=%d", transaction.commitCalls, transaction.rollbackCalls)
	}
}

func TestTransactionRunnerPreservesCommitAndRollbackErrors(t *testing.T) {
	commitError := context.Canceled
	rollbackError := errors.New("rollback failed")
	transaction := &fakeTransaction{commitError: commitError, rollbackError: rollbackError}
	runner := newTestTransactionRunner(transaction)

	err := runner.Run(context.Background(), func(context.Context, QueryHandle) error { return nil })
	if !errors.Is(err, commitError) || !errors.Is(err, rollbackError) {
		t.Fatalf("expected commit and rollback errors, got %v", err)
	}
	if transaction.commitCalls != 1 || transaction.rollbackCalls != 1 {
		t.Fatalf("unexpected transaction cleanup: commits=%d rollbacks=%d", transaction.commitCalls, transaction.rollbackCalls)
	}
}

func TestTransactionRunnerRollsBackAndRepanics(t *testing.T) {
	transaction := &fakeTransaction{}
	runner := newTestTransactionRunner(transaction)
	panicValue := "transaction panic"

	func() {
		defer func() {
			if recovered := recover(); recovered != panicValue {
				t.Fatalf("expected panic %q, got %#v", panicValue, recovered)
			}
		}()
		_ = runner.Run(context.Background(), func(context.Context, QueryHandle) error {
			panic(panicValue)
		})
	}()
	if transaction.commitCalls != 0 || transaction.rollbackCalls != 1 {
		t.Fatalf("unexpected transaction cleanup: commits=%d rollbacks=%d", transaction.commitCalls, transaction.rollbackCalls)
	}
}

func TestTransactionRunnerReturnsBeginError(t *testing.T) {
	beginError := errors.New("begin failed")
	runner := newTransactionRunner(func(context.Context, pgx.TxOptions) (transactionHandle, error) {
		return nil, beginError
	})

	err := runner.Run(context.Background(), func(context.Context, QueryHandle) error { return nil })
	if !errors.Is(err, beginError) {
		t.Fatalf("expected begin error, got %v", err)
	}
}

func newTestTransactionRunner(transaction transactionHandle) *TransactionRunner {
	return newTransactionRunner(func(context.Context, pgx.TxOptions) (transactionHandle, error) {
		return transaction, nil
	})
}

type fakeTransaction struct {
	commitError   error
	rollbackError error
	commitCalls   int
	rollbackCalls int
}

func (transaction *fakeTransaction) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (transaction *fakeTransaction) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected query")
}

func (transaction *fakeTransaction) QueryRow(context.Context, string, ...any) pgx.Row {
	panic("unexpected query row")
}

func (transaction *fakeTransaction) Commit(context.Context) error {
	transaction.commitCalls++
	return transaction.commitError
}

func (transaction *fakeTransaction) Rollback(context.Context) error {
	transaction.rollbackCalls++
	return transaction.rollbackError
}
