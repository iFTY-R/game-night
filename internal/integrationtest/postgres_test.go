package integrationtest

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOpenPostgresSchema(t *testing.T) {
	fixture := OpenPostgresSchema(t)

	var schema string
	if err := fixture.Pool.QueryRow(context.Background(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if schema != fixture.Name {
		t.Fatalf("expected current schema %q, got %q", fixture.Name, schema)
	}
}

func TestOpenPrivilegeDatabase(t *testing.T) {
	fixture := OpenPrivilegeDatabase(t)

	for name, databaseURL := range map[string]string{
		"migration": fixture.MigrationURL,
		"runtime":   fixture.RuntimeURL,
		"worker":    fixture.WorkerURL,
	} {
		t.Run(name, func(t *testing.T) {
			pool, err := pgxpool.New(context.Background(), databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			if err := pool.Ping(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
