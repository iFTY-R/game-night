package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/iFTY-R/game-night/internal/integrationtest"
)

// databaseIntegrationTime keeps time-sensitive integration transitions aligned with PostgreSQL's authoritative clock.
func databaseIntegrationTime(t testing.TB, ctx context.Context, fixture *integrationtest.PostgresSchema) time.Time {
	t.Helper()
	var now time.Time
	if err := fixture.Pool.QueryRow(ctx, "SELECT pg_catalog.clock_timestamp()").Scan(&now); err != nil {
		t.Fatal(err)
	}
	return now.UTC().Truncate(time.Microsecond)
}
