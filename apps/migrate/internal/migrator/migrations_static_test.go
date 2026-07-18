package migrator

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestMigrationFilesAreContiguousAndReversible(t *testing.T) {
	migrations, err := goose.CollectMigrations(migrationDirectory(t), 0, math.MaxInt64)
	if err != nil {
		t.Fatalf("collect migrations: %v", err)
	}

	wantVersions := []int64{1, 2, 3, 4, 5}
	if len(migrations) != len(wantVersions) {
		t.Fatalf("expected %d migrations, got %d", len(wantVersions), len(migrations))
	}
	for index, migration := range migrations {
		if migration.Version != wantVersions[index] {
			t.Fatalf("migration %d has version %d, want %d", index, migration.Version, wantVersions[index])
		}

		contents, err := os.ReadFile(migration.Source)
		if err != nil {
			t.Fatalf("read migration %s: %v", filepath.Base(migration.Source), err)
		}
		for _, marker := range []string{"-- +goose Up", "-- +goose Down"} {
			if !strings.Contains(string(contents), marker) {
				t.Errorf("migration %s is missing %q", filepath.Base(migration.Source), marker)
			}
		}
	}
}
