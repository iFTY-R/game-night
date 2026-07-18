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

	wantVersions := []int64{1, 2, 3, 4, 5, 6}
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

func TestSecretResultWorkflowDownCleansUnrepresentableChallengesBeforeRestoringConstraint(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join(migrationDirectory(t), "00006_secret_result_workflows.sql"))
	if err != nil {
		t.Fatal(err)
	}
	migration := string(contents)
	downIndex := strings.Index(migration, "-- +goose Down")
	recoveryDeleteIndex := strings.Index(migration, "DELETE FROM user_recovery_attempts")
	challengeDeleteIndex := strings.Index(migration, "DELETE FROM anonymous_challenges")
	restoredConstraintIndex := strings.LastIndex(migration, "ADD CONSTRAINT anonymous_challenges_consumption_shape_check")
	if downIndex < 0 || recoveryDeleteIndex < downIndex || challengeDeleteIndex < recoveryDeleteIndex ||
		restoredConstraintIndex < challengeDeleteIndex {
		t.Fatalf("migration 00006 must delete dependent recovery attempts and unrepresentable challenges before restoring its old constraint")
	}
	for _, condition := range []string{
		"consumed_at IS NOT NULL",
		"replay_until IS NULL",
		"result_id IS NULL",
	} {
		if strings.Count(migration[recoveryDeleteIndex:restoredConstraintIndex], condition) < 2 {
			t.Errorf("migration 00006 downgrade cleanup is missing repeated guard %q", condition)
		}
	}
}
