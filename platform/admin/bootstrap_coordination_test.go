package admin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/clock"
)

const bootstrapTestHash = "$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestBootstrapPasswordCoordinatesCompetingInstancesAndRejectsStaleMount(t *testing.T) {
	now := time.Date(2026, 7, 19, 16, 0, 0, 0, time.UTC)
	account, err := NewBootstrapAccount(uuid.New(), now)
	if err != nil {
		t.Fatal(err)
	}
	repository := &bootstrapAccountRepository{account: account}
	hasher := &bootstrapHasher{expected: "correct-bootstrap-secret"}
	service := &Service{
		passwords: hasher, passwordPolicy: DefaultPasswordPolicy(), clock: clock.NewFake(now),
		unitOfWork: bootstrapUnitOfWork{accounts: repository},
	}

	if err := service.BootstrapPassword(t.Context(), hasher.expected); err != nil {
		t.Fatalf("winning bootstrap: %v", err)
	}
	if repository.account.Snapshot().Status != AccountStatusSetupRequired {
		t.Fatalf("bootstrap status = %s", repository.account.Snapshot().Status)
	}
	if err := service.BootstrapPassword(t.Context(), hasher.expected); err != nil {
		t.Fatalf("same-secret losing instance: %v", err)
	}
	if err := service.BootstrapPassword(t.Context(), "different-bootstrap-secret"); !errors.Is(err, ErrBootstrapSecretMismatch) {
		t.Fatalf("different-secret losing instance error = %v", err)
	}
	active, err := repository.account.Transition(AccountStatusActive, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	repository.account = active
	if err := service.BootstrapPassword(t.Context(), hasher.expected); !errors.Is(err, ErrBootstrapSecretMismatch) {
		t.Fatalf("active account accepted mounted bootstrap secret: %v", err)
	}
	if err := service.BootstrapReadyWithoutSecret(t.Context()); err != nil {
		t.Fatalf("active account rejected removed bootstrap secret: %v", err)
	}
}

func TestBootstrapReadyWithoutSecretRejectsPendingAccount(t *testing.T) {
	account, err := NewBootstrapAccount(uuid.New(), time.Date(2026, 7, 19, 16, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{unitOfWork: bootstrapUnitOfWork{accounts: &bootstrapAccountRepository{account: account}}}
	if err := service.BootstrapReadyWithoutSecret(t.Context()); !errors.Is(err, ErrBootstrapSecretMismatch) {
		t.Fatalf("pending account readiness error = %v", err)
	}
}

type bootstrapHasher struct{ expected string }

func (*bootstrapHasher) Hash(context.Context, []byte) (string, error) { return bootstrapTestHash, nil }

func (hasher *bootstrapHasher) VerifyOrDummy(_ context.Context, _ string, secret []byte) (bool, bool, error) {
	return string(secret) == hasher.expected, false, nil
}

type bootstrapUnitOfWork struct{ accounts *bootstrapAccountRepository }

func (unitOfWork bootstrapUnitOfWork) Run(ctx context.Context, work TransactionWork) error {
	return work(ctx, bootstrapTransaction{accounts: unitOfWork.accounts})
}

type bootstrapTransaction struct {
	Transaction
	accounts *bootstrapAccountRepository
}

func (transaction bootstrapTransaction) Accounts() AccountRepository { return transaction.accounts }

type bootstrapAccountRepository struct {
	AccountRepository
	account Account
}

func (repository *bootstrapAccountRepository) GetForUpdate(context.Context) (Account, error) {
	return repository.account, nil
}

func (repository *bootstrapAccountRepository) BootstrapPasswordCAS(
	_ context.Context,
	current Account,
	hash, algorithm, parameters string,
	at time.Time,
) (Account, error) {
	if current.Snapshot().AdminVersion != repository.account.Snapshot().AdminVersion {
		return Account{}, ErrConcurrentTransition
	}
	updated, err := current.WithPassword(hash, algorithm, parameters, at)
	if err != nil {
		return Account{}, err
	}
	repository.account = updated
	return updated, nil
}

var _ PasswordHasher = (*bootstrapHasher)(nil)
