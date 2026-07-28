package integrationtest

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
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

	for name, connection := range map[string]struct {
		databaseURL string
		role        string
	}{
		"migration": {databaseURL: fixture.MigrationURL, role: fixture.MigrationRole},
		"runtime":   {databaseURL: fixture.RuntimeURL, role: fixture.RuntimeRole},
		"worker":    {databaseURL: fixture.WorkerURL, role: fixture.WorkerRole},
	} {
		t.Run(name, func(t *testing.T) {
			pool, err := pgxpool.New(context.Background(), connection.databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			if err := pool.Ping(context.Background()); err != nil {
				t.Fatal(err)
			}
			var database, searchPath, role, databaseOwner string
			if err := pool.QueryRow(context.Background(), `
				SELECT current_database(), current_setting('search_path'), current_user, pg_catalog.pg_get_userbyid(database.datdba)
				FROM pg_catalog.pg_database AS database
				WHERE database.datname = current_database()
			`).Scan(&database, &searchPath, &role, &databaseOwner); err != nil {
				t.Fatal(err)
			}
			wantSearchPath := pgx.Identifier{fixture.Schema}.Sanitize() + ",pg_catalog"
			if database != fixture.DatabaseName || databaseOwner != fixture.OwnerRole || searchPath != wantSearchPath || role != connection.role {
				t.Fatalf("unexpected privilege connection: database=%q owner=%q search_path=%q role=%q", database, databaseOwner, searchPath, role)
			}
		})
	}
}
