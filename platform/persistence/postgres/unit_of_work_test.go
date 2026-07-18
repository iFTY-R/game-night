package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	adminDomain "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/challenge"
	identityDomain "github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/secretresult"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

func TestIdentityUnitOfWorkRejectsNilWorkWithIdentityError(t *testing.T) {
	if err := (&IdentityUnitOfWork{}).Run(context.Background(), nil); err != identityDomain.ErrInvalidIdentityRequest {
		t.Fatalf("identity nil-work error = %v", err)
	}
	if err := (&IdentityUnitOfWork{}).RunIdentity(context.Background(), nil); err != identityDomain.ErrInvalidIdentityRequest {
		t.Fatalf("full identity nil-work error = %v", err)
	}
}

func TestCompleteIdentityTransactionInjectsRecoveryAuditAndOutboxRepositories(t *testing.T) {
	verifier, err := audit.NewService(newRepositoryAuditKeyring())
	if err != nil {
		t.Fatal(err)
	}
	transaction := newCompleteIdentityTransaction(nil, verifier)
	if _, ok := transaction.RecoveryAttempts().(*identityRecoveryAttemptRepository); !ok {
		t.Fatalf("recovery attempts repository = %T", transaction.RecoveryAttempts())
	}
	if _, ok := transaction.AssistedRecoveryGrants().(*identityAssistedRecoveryRepository); !ok {
		t.Fatalf("assisted recovery repository = %T", transaction.AssistedRecoveryGrants())
	}
	auditRepository, ok := transaction.Audit().(*AuditRepository)
	if !ok {
		t.Fatalf("audit repository = %T", transaction.Audit())
	}
	if auditRepository.verifier != verifier {
		t.Fatalf("audit verifier = %T, want %T", auditRepository.verifier, verifier)
	}
	checkpointRepository, ok := transaction.AuditCheckpoints().(*AuditCheckpointRepository)
	if !ok {
		t.Fatalf("checkpoint repository = %T", transaction.AuditCheckpoints())
	}
	if checkpointRepository.verifier != verifier {
		t.Fatalf("checkpoint verifier = %T, want %T", checkpointRepository.verifier, verifier)
	}
	if _, ok := transaction.OutboxEvents().(*OutboxEventRepository); !ok {
		t.Fatalf("outbox repository = %T", transaction.OutboxEvents())
	}
}

func TestIdentityUnitOfWorkWithAuditRejectsNilVerifier(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("nil audit verifier did not panic")
		}
	}()
	NewIdentityUnitOfWorkWithAudit(nil, nil)
}

func TestIdentityUnitOfWorkMapsCommitSQLStates(t *testing.T) {
	for _, test := range []struct {
		code string
		want error
	}{
		{code: "23503", want: identityDomain.ErrIdentityIntegrity},
		{code: "23514", want: identityDomain.ErrIdentityIntegrity},
		{code: "23505", want: identityDomain.ErrIdentityConcurrentTransition},
		{code: "40001", want: identityDomain.ErrIdentityConcurrentTransition},
		{code: "40P01", want: identityDomain.ErrIdentityConcurrentTransition},
		{code: "XX000", want: identityDomain.ErrIdentityRepositoryUnavailable},
	} {
		t.Run(test.code, func(t *testing.T) {
			unitOfWork := &IdentityUnitOfWork{runner: newTestTransactionRunner(&fakeTransaction{
				commitError: &pgconn.PgError{Code: test.code},
			})}
			err := unitOfWork.RunIdentity(context.Background(), func(context.Context, identityDomain.IdentityTransaction) error {
				return nil
			})
			if err != test.want {
				t.Fatalf("commit SQLSTATE %s error = %v, want %v", test.code, err, test.want)
			}
		})
	}

	challengeUnitOfWork := &IdentityChallengeUnitOfWork{runner: newTestTransactionRunner(&fakeTransaction{
		commitError: &pgconn.PgError{Code: "23514"},
	})}
	err := challengeUnitOfWork.Run(context.Background(), func(context.Context, identityDomain.ChallengeTransaction) error {
		return nil
	})
	if err != challenge.ErrRepositoryUnavailable {
		t.Fatalf("challenge commit constraint error = %v", err)
	}

	beginFailure := newTransactionRunner(func(context.Context, pgx.TxOptions) (transactionHandle, error) {
		return nil, &pgconn.PgError{Code: "23514"}
	})
	err = (&IdentityUnitOfWork{runner: beginFailure}).RunIdentity(
		context.Background(), func(context.Context, identityDomain.IdentityTransaction) error { return nil },
	)
	if err != identityDomain.ErrIdentityRepositoryUnavailable {
		t.Fatalf("begin constraint-shaped error = %v", err)
	}
}

func TestAuditAndOutboxUnitOfWorksRejectNilWork(t *testing.T) {
	if err := (&AuditOutboxUnitOfWork{}).Run(context.Background(), nil); err != audit.ErrInvalidInput {
		t.Fatalf("audit nil-work error = %v", err)
	}
	if err := (&OutboxUnitOfWork{}).Run(context.Background(), nil); err != outbox.ErrInvalidInput {
		t.Fatalf("outbox nil-work error = %v", err)
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
			name:                  "identity",
			repositoryUnavailable: identityDomain.ErrIdentityRepositoryUnavailable,
			domainErrors:          identityTransactionDomainErrors,
			run: func(ctx context.Context, runner *TransactionRunner, callbackErr error) error {
				unitOfWork := &IdentityUnitOfWork{runner: runner}
				return unitOfWork.RunIdentity(ctx, func(context.Context, identityDomain.IdentityTransaction) error {
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
		{
			name:                  "audit_outbox",
			repositoryUnavailable: audit.ErrRepositoryUnavailable,
			domainErrors:          auditOutboxDomainErrors,
			run: func(ctx context.Context, runner *TransactionRunner, callbackErr error) error {
				verifier, err := audit.NewService(newRepositoryAuditKeyring())
				if err != nil {
					return err
				}
				unitOfWork := &AuditOutboxUnitOfWork{runner: runner, verifier: verifier}
				return unitOfWork.Run(ctx, func(context.Context, audit.Transaction) error {
					return callbackErr
				})
			},
		},
		{
			name:                  "outbox",
			repositoryUnavailable: outbox.ErrRepositoryUnavailable,
			domainErrors:          outboxDomainErrors,
			run: func(ctx context.Context, runner *TransactionRunner, callbackErr error) error {
				unitOfWork := &OutboxUnitOfWork{runner: runner}
				return unitOfWork.Run(ctx, func(context.Context, outbox.Transaction) error {
					return callbackErr
				})
			},
		},
	}
}
