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

	wantVersions := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30}
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

func TestAdminUserCenterMigrationLocksStateAndLeastPrivilegeBoundaries(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join(migrationDirectory(t), "00030_admin_user_center.sql"))
	if err != nil {
		t.Fatal(err)
	}
	migration := string(contents)
	for _, fragment := range []string{
		"ADD COLUMN account_version bigint NOT NULL DEFAULT 1 CHECK (account_version > 0)",
		"CREATE TRIGGER admin_user_notes_append_only",
		"target_count bigint NOT NULL CHECK (target_count > 0)",
		"step IN ('queued', 'revoke_credentials', 'erase_profile', 'enqueue_room_cleanup', 'complete')",
		"CHECK ((state IN ('failed', 'skipped')) = (error_message_key IS NOT NULL))",
		"CHECK ((state = 'failed') = (error_message_key IS NOT NULL))",
		"expires_at <= created_at + interval '5 minutes'",
		"GRANT SELECT, UPDATE ON TABLE %I.admin_user_tag_catalog TO %I",
		"GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE %I.admin_user_tags TO %I",
		"GRANT SELECT, INSERT, DELETE ON TABLE %I.admin_user_tag_links TO %I",
		"GRANT SELECT, INSERT ON TABLE %I.admin_user_notes TO %I",
		"GRANT SELECT, UPDATE ON TABLE %I.admin_batch_jobs, %I.admin_batch_job_items, %I.admin_user_erasure_jobs, %I.admin_export_jobs TO %I",
		"REVOKE ALL ON FUNCTION %I.reject_admin_user_note_mutation() FROM PUBLIC",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("migration 00030 is missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE %I.%I TO %I",
		"GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE %I.admin_user_notes",
		"GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE %I.admin_export_download_grants",
	} {
		if strings.Contains(migration, forbidden) {
			t.Errorf("migration 00030 contains over-broad privilege %q", forbidden)
		}
	}
}

func TestGameSessionStartConfigMigrationFreezesSnapshotFieldsAsABundle(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join(migrationDirectory(t), "00025_game_session_start_config.sql"))
	if err != nil {
		t.Fatal(err)
	}
	migration := string(contents)
	constraintIndex := strings.Index(migration, "ADD CONSTRAINT game_sessions_start_snapshot_shape CHECK")
	downIndex := strings.Index(migration, "-- +goose Down")
	if constraintIndex < 0 || downIndex < 0 || constraintIndex > downIndex {
		t.Fatal("migration 00025 must define one bundled start snapshot constraint before the down section")
	}
	constraintBody := migration[constraintIndex:downIndex]

	requiredFragments := []string{
		"start_config_message_type IS NULL",
		"start_config_schema_version IS NULL",
		"start_config_payload IS NULL",
		"start_config_digest IS NULL",
		"start_config_revision IS NULL",
		"start_room_version IS NULL",
		"start_membership_version IS NULL",
		"start_ownership_epoch IS NULL",
		"start_config_message_type IS NOT NULL",
		"start_config_schema_version IS NOT NULL",
		"start_config_payload IS NOT NULL",
		"start_config_digest IS NOT NULL",
		"start_config_revision IS NOT NULL",
		"start_room_version IS NOT NULL",
		"start_membership_version IS NOT NULL",
		"start_ownership_epoch IS NOT NULL",
		"start_config_revision >= 0",
		"start_room_version > 0",
		"start_membership_version > 0",
		"start_ownership_epoch > 0",
	}
	for _, fragment := range requiredFragments {
		if !strings.Contains(constraintBody, fragment) {
			t.Errorf("migration 00025 bundled constraint is missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"ADD CONSTRAINT game_sessions_start_config_shape CHECK",
		"ADD CONSTRAINT game_sessions_start_fence_shape CHECK",
		"start_config_revision > 0",
		"(start_config_revision IS NULL OR start_config_revision > 0)",
	} {
		if strings.Contains(constraintBody, forbidden) {
			t.Errorf("migration 00025 bundled constraint still contains forbidden fragment %q", forbidden)
		}
	}
}

func TestGameSessionCancelReasonMigrationBackfillsLegacyRowsAndRequiresCancelledConsistency(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join(migrationDirectory(t), "00026_game_session_cancel_reason.sql"))
	if err != nil {
		t.Fatal(err)
	}
	migration := string(contents)
	for _, fragment := range []string{
		"ADD COLUMN cancel_reason text",
		"SET cancel_reason = 'legacy_cancelled'",
		"WHERE status = 'cancelled'",
		"ADD CONSTRAINT game_sessions_cancel_reason_shape CHECK",
		"status = 'cancelled'",
		"cancel_reason IS NOT NULL",
		"status <> 'cancelled'",
		"cancel_reason IS NULL",
		"DROP COLUMN IF EXISTS cancel_reason",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("migration 00026 is missing %q", fragment)
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

func TestAdminResetOutboxProtocolMigrationIsReversible(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join(migrationDirectory(t), "00008_admin_reset_outbox_protocol.sql"))
	if err != nil {
		t.Fatal(err)
	}
	migration := string(contents)
	downIndex := strings.Index(migration, "-- +goose Down")
	if downIndex < 0 || !strings.Contains(migration[:downIndex], "'audit.chain'") ||
		strings.Contains(migration[:downIndex], "'audit_chain'") || !strings.Contains(migration[downIndex:], "'audit_chain'") ||
		!strings.Contains(migration[:downIndex], "pg_advisory_xact_lock(1196314434, 1)") ||
		!strings.Contains(migration[:downIndex], "'9c26d493-92b3-59a5-a787-3a1a3df235aa'::uuid") {
		t.Fatal("migration 00008 must serialize the upgraded dotted checkpoint event and restore the legacy value on downgrade")
	}
}
