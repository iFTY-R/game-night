package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/iFTY-R/game-night/internal/integrationtest"
	adminDomain "github.com/iFTY-R/game-night/platform/admin"
)

func TestAdminUnitOfWorkReadsSingletonAndPreservesBootstrapCAS(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	unitOfWork := NewAdminUnitOfWork(fixture.Pool)
	if err := unitOfWork.Run(ctx, func(ctx context.Context, transaction adminDomain.Transaction) error {
		account, err := transaction.Accounts().GetForUpdate(ctx)
		if err != nil {
			return err
		}
		if account.Snapshot().Status != adminDomain.AccountStatusBootstrapPending || account.Snapshot().PasswordVersion != 0 {
			t.Fatalf("unexpected bootstrap account state: %+v", account.Snapshot())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
