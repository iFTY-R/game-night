package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/iFTY-R/game-night/internal/integrationtest"
)

func TestExpiryCleanupFunctionIsRepeatableOnRealPostgres(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	cleanup := NewExpiryCleanup(fixture.Pool)
	first, err := cleanup.RunReport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cleanup.RunReport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("empty cleanup was not repeatable: first=%+v second=%+v", first, second)
	}

	if _, err := fixture.Pool.Exec(ctx, "SELECT read_checkpoint_consumer_sequence()"); err != nil {
		t.Fatalf("checkpoint health reader function unavailable: %v", err)
	}
}
