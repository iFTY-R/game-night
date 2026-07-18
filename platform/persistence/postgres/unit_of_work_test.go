package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	adminDomain "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/challenge"
	identityDomain "github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/secretresult"
	"github.com/jackc/pgx/v5"
)

type unitOfWorkTestCase struct {
	name                  string
	repositoryUnavailable error
	domainErrors          []error
	run                   func(context.Context, *TransactionRunner, error) error
}

func TestUnitOfWorksHideTransactionLifecycleErrors(t *testing.T) {
	for _, unitOfWork := range unitOfWorkTestCases() {
		unitOfWork := unitOfWork
		t.Run(unitOfWork.name, func(t *testing.T) {
			for _, scenario := range []struct {
				name        string
				newRunner   func(error) *TransactionRunner
				callbackErr error
			}{
				{
					name: "begin",
					newRunner: func(injected error) *TransactionRunner {
						return newTransactionRunner(func(context.Context, pgx.TxOptions) (transactionHandle, error) {
							return nil, injected
						})
					},
				},
				{
					name: "commit",
					newRunner: func(injected error) *TransactionRunner {
						return newTestTransactionRunner(&fakeTransaction{commitError: injected})
					},
				},
				{
					name: "rollback",
					newRunner: func(injected error) *TransactionRunner {
						return newTestTransactionRunner(&fakeTransaction{rollbackError: injected})
					},
					callbackErr: unitOfWork.domainErrors[0],
				},
			} {
				scenario := scenario
				t.Run(scenario.name, func(t *testing.T) {
					injected := errors.New("private database host and constraint details")
					err := unitOfWork.run(context.Background(), scenario.newRunner(injected), scenario.callbackErr)
					if err != unitOfWork.repositoryUnavailable {
						t.Fatalf("error = %v, want %v", err, unitOfWork.repositoryUnavailable)
					}
					if strings.Contains(err.Error(), injected.Error()) {
						t.Fatalf("unit of work leaked lifecycle diagnostics: %v", err)
					}
				})
			}
		})
	}
}

func TestUnitOfWorksPreserveContextErrors(t *testing.T) {
	for _, unitOfWork := range unitOfWorkTestCases() {
		unitOfWork := unitOfWork
		t.Run(unitOfWork.name, func(t *testing.T) {
			for _, contextErr := range []error{context.Canceled, context.DeadlineExceeded} {
				t.Run(contextErr.Error(), func(t *testing.T) {
					beginFailureRunner := newTransactionRunner(func(context.Context, pgx.TxOptions) (transactionHandle, error) {
						return nil, contextErr
					})
					if err := unitOfWork.run(context.Background(), beginFailureRunner, nil); err != contextErr {
						t.Fatalf("begin error = %v, want %v", err, contextErr)
					}
					if err := unitOfWork.run(context.Background(), newTestTransactionRunner(&fakeTransaction{}), contextErr); err != contextErr {
						t.Fatalf("callback error = %v, want %v", err, contextErr)
					}
				})
			}
		})
	}
}

func TestUnitOfWorksPreserveKnownDomainErrors(t *testing.T) {
	for _, unitOfWork := range unitOfWorkTestCases() {
		unitOfWork := unitOfWork
		t.Run(unitOfWork.name, func(t *testing.T) {
			for _, domainErr := range unitOfWork.domainErrors {
				err := unitOfWork.run(context.Background(), newTestTransactionRunner(&fakeTransaction{}), domainErr)
				if err != domainErr {
					t.Fatalf("error = %v, want %v", err, domainErr)
				}
			}
		})
	}
}

func TestChallengeUnitOfWorksRejectNilWorkWithChallengeError(t *testing.T) {
	identityUnitOfWork := &IdentityChallengeUnitOfWork{}
	if err := identityUnitOfWork.Run(context.Background(), nil); err != challenge.ErrInvalidInput {
		t.Fatalf("identity nil-work error = %v", err)
	}
	adminUnitOfWork := &AdminChallengeUnitOfWork{}
	if err := adminUnitOfWork.Run(context.Background(), nil); err != challenge.ErrInvalidInput {
		t.Fatalf("admin nil-work error = %v", err)
	}
}

func unitOfWorkTestCases() []unitOfWorkTestCase {
	return []unitOfWorkTestCase{
		{
			name:                  "secret_result",
			repositoryUnavailable: secretresult.ErrRepositoryUnavailable,
			domainErrors:          secretResultDomainErrors,
			run: func(ctx context.Context, runner *TransactionRunner, callbackErr error) error {
				unitOfWork := &SecretResultUnitOfWork{runner: runner}
				return unitOfWork.Run(ctx, func(context.Context, secretresult.Repository) error {
					return callbackErr
				})
			},
		},
		{
			name:                  "identity_challenge",
			repositoryUnavailable: challenge.ErrRepositoryUnavailable,
			domainErrors:          challengeTransactionDomainErrors,
			run: func(ctx context.Context, runner *TransactionRunner, callbackErr error) error {
				unitOfWork := &IdentityChallengeUnitOfWork{runner: runner}
				return unitOfWork.Run(ctx, func(context.Context, identityDomain.ChallengeTransaction) error {
					return callbackErr
				})
			},
		},
		{
			name:                  "admin_challenge",
			repositoryUnavailable: challenge.ErrRepositoryUnavailable,
			domainErrors:          challengeTransactionDomainErrors,
			run: func(ctx context.Context, runner *TransactionRunner, callbackErr error) error {
				unitOfWork := &AdminChallengeUnitOfWork{runner: runner}
				return unitOfWork.Run(ctx, func(context.Context, adminDomain.ChallengeTransaction) error {
					return callbackErr
				})
			},
		},
	}
}
