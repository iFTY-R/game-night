package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/internal/integrationtest"
	admin "github.com/iFTY-R/game-night/platform/admin"
	adminuser "github.com/iFTY-R/game-night/platform/admin/user"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestAdminUserGovernanceRepositoryCommandPreviewReceiptAndDeviceGovernance(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	now := databaseIntegrationTime(t, ctx, fixture)
	adminID, _ := seedAdminUserCenterPrincipal(t, ctx, fixture, now)
	userID := uuid.New()
	createRoomTestUser(t, ctx, fixture, userID, "GovernedPreviewUser", now.Add(-time.Hour))
	insertDeviceCredential(t, ctx, fixture, userID, uuid.New(), "active-phone", now.Add(-time.Hour), false)
	insertDeviceCredential(t, ctx, fixture, userID, uuid.New(), "active-tablet", now.Add(-30*time.Minute), false)
	insertDeviceCredential(t, ctx, fixture, userID, uuid.New(), "expired-browser", now.Add(-400*24*time.Hour), true)
	exportID := insertQueuedUserExport(t, ctx, fixture, adminID, userID, now.Add(-time.Minute))

	repository := NewAdminUserGovernanceRepository(fixture.Pool)
	previewDigest := governanceDigest(0x11)
	previewID := uuid.New()
	preview, err := repository.CreateUserCommandPreview(ctx, adminuser.CreateUserCommandPreviewCommand{Preview: adminuser.UserCommandPreview{
		ID: previewID, ActorAdminID: adminID,
		Snapshot: adminuser.UserCommandSnapshot{
			SchemaVersion: adminuser.UserCommandSnapshotSchemaVersion,
			UserID:        userID, Command: adminuser.UserCommandRevokeAllDevices, ExpectedUserVersion: 1,
		},
		PreviewDigest: previewDigest, AffectedDevices: 2,
		Blockers:          []adminuser.GovernanceBlocker{{Type: adminuser.GovernanceBlockerPendingExport, ResourceID: userID.String(), MessageKey: "admin.user.pending_export"}},
		RequiredElevation: admin.ElevationScopeUsersRevokeDevices,
		SampledAt:         now, ExpiresAt: now.Add(adminuser.UserCommandPreviewTTL),
	}})
	if err != nil || preview.Version != 1 || preview.RequiredElevation != admin.ElevationScopeUsersRevokeDevices {
		t.Fatalf("create command preview: preview=%+v err=%v", preview, err)
	}
	loadedPreview, err := repository.GetUserCommandPreview(ctx, previewID, adminID)
	if err != nil || loadedPreview.ID != preview.ID || len(loadedPreview.Blockers) != 1 || loadedPreview.Blockers[0].Type != adminuser.GovernanceBlockerPendingExport {
		t.Fatalf("load command preview: preview=%+v err=%v", loadedPreview, err)
	}
	consumed, err := repository.ConsumeUserCommandPreview(ctx, previewID, adminID, preview.Version, now.Add(time.Minute))
	if err != nil || consumed.Version != preview.Version+1 || consumed.ConsumedAt.IsZero() {
		t.Fatalf("consume command preview: preview=%+v err=%v", consumed, err)
	}
	if _, err = repository.ConsumeUserCommandPreview(ctx, previewID, adminID, preview.Version, now.Add(2*time.Minute)); !errors.Is(err, adminuser.ErrConflict) {
		t.Fatalf("reconsume command preview error = %v", err)
	}

	auditEventID := insertAuditEvent(t, ctx, fixture, now.Add(2*time.Minute))
	operationID := mustOperationID(t, 0x41)
	receipt := adminuser.UserCommandReceipt{
		ActorAdminID: adminID, OperationID: operationID, RequestDigest: governanceDigest(0x12), PreviewID: previewID,
		UserID: userID, Command: adminuser.UserCommandRevokeAllDevices, Outcome: adminuser.UserCommandOutcomeExecuted,
		UserVersion: 2, RevokedDevices: 2, AuditEventID: auditEventID, CompletedAt: now.Add(2 * time.Minute),
	}
	storedReceipt, err := repository.SaveUserCommandReceipt(ctx, receipt)
	if err != nil || storedReceipt.OperationID.Value() != receipt.OperationID.Value() || storedReceipt.RevokedDevices != 2 {
		t.Fatalf("save command receipt: receipt=%+v err=%v", storedReceipt, err)
	}
	replayedReceipt, err := repository.SaveUserCommandReceipt(ctx, receipt)
	if err != nil || replayedReceipt.AuditEventID != storedReceipt.AuditEventID {
		t.Fatalf("replay command receipt: receipt=%+v err=%v", replayedReceipt, err)
	}
	conflictReceipt := receipt
	conflictReceipt.RequestDigest = governanceDigest(0x13)
	if _, err = repository.SaveUserCommandReceipt(ctx, conflictReceipt); !errors.Is(err, adminuser.ErrIdempotencyConflict) {
		t.Fatalf("conflicting command receipt error = %v", err)
	}
	loadedReceipt, err := repository.GetUserCommandReceipt(ctx, adminID, operationID)
	if err != nil || loadedReceipt.RequestDigest != receipt.RequestDigest || loadedReceipt.AuditEventID != auditEventID {
		t.Fatalf("load command receipt: receipt=%+v err=%v", loadedReceipt, err)
	}

	pending, err := repository.HasPendingExport(ctx, userID)
	if err != nil || !pending {
		t.Fatalf("pending export: pending=%v err=%v", pending, err)
	}
	if _, err = fixture.Pool.Exec(ctx, "DELETE FROM admin_export_jobs WHERE export_id = $1", exportID); err != nil {
		t.Fatal(err)
	}
	pending, err = repository.HasPendingExport(ctx, userID)
	if err != nil || pending {
		t.Fatalf("pending export after delete: pending=%v err=%v", pending, err)
	}

	activeDevices, err := repository.CountActiveDevices(ctx, userID, now)
	if err != nil || activeDevices != 2 {
		t.Fatalf("active devices: count=%d err=%v", activeDevices, err)
	}
	revokedDevices, err := repository.RevokeAllDevices(ctx, userID, now)
	if err != nil || revokedDevices != 2 {
		t.Fatalf("revoke devices: count=%d err=%v", revokedDevices, err)
	}
	activeDevices, err = repository.CountActiveDevices(ctx, userID, now)
	if err != nil || activeDevices != 0 {
		t.Fatalf("active devices after revoke: count=%d err=%v", activeDevices, err)
	}
}

func TestAdminUserGovernanceRepositoryDeleteUserAndEraseProfile(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	now := databaseIntegrationTime(t, ctx, fixture)
	adminID, _ := seedAdminUserCenterPrincipal(t, ctx, fixture, now)
	userID := uuid.New()
	createRoomTestUser(t, ctx, fixture, userID, "GovernedDeleteUser", now.Add(-time.Hour))
	insertDeviceCredential(t, ctx, fixture, userID, uuid.New(), "delete-phone", now.Add(-time.Hour), false)
	insertDeviceCredential(t, ctx, fixture, userID, uuid.New(), "delete-tablet", now.Add(-2*time.Hour), false)
	insertUserProfile(t, ctx, fixture, userID, adminID, now.Add(-30*time.Minute))

	repository := NewAdminUserGovernanceRepository(fixture.Pool)
	deleteOperationID := mustOperationID(t, 0x42)
	deleted, err := repository.DeleteUser(ctx, adminuser.DeleteUserCommand{
		ActorAdminID: adminID, OperationID: deleteOperationID, RequestDigest: governanceDigest(0x21),
		UserID: userID, ExpectedUserVersion: 1, Reason: "account lifecycle completed", ChangedAt: now,
	})
	if err != nil || deleted.User.Status != "deleted" || deleted.User.Version != 2 || deleted.RevokedDevices != 2 || deleted.ErasureJobID == uuid.Nil {
		t.Fatalf("delete user: result=%+v err=%v", deleted, err)
	}

	var status string
	var username, usernameKey pgtype.Text
	var accountVersion int64
	if err = fixture.Pool.QueryRow(ctx, `
        SELECT status, username, current_username_key, account_version
        FROM users
        WHERE user_id = $1
    `, userID).Scan(&status, &username, &usernameKey, &accountVersion); err != nil {
		t.Fatal(err)
	}
	if status != "deleted" || username.Valid || usernameKey.Valid || accountVersion != 2 {
		t.Fatalf("deleted user row: status=%s username=%+v username_key=%+v version=%d", status, username, usernameKey, accountVersion)
	}
	var claimCount int
	if err = fixture.Pool.QueryRow(ctx, "SELECT count(*) FROM username_claims WHERE owner_user_id = $1", userID).Scan(&claimCount); err != nil {
		t.Fatal(err)
	}
	if claimCount != 0 {
		t.Fatalf("username claims after delete = %d, want 0", claimCount)
	}
	var erasureState, erasureStep string
	if err = fixture.Pool.QueryRow(ctx, `
        SELECT state, step
        FROM admin_user_erasure_jobs
        WHERE erasure_job_id = $1
    `, deleted.ErasureJobID).Scan(&erasureState, &erasureStep); err != nil {
		t.Fatal(err)
	}
	if erasureState != "queued" || erasureStep != "queued" {
		t.Fatalf("erasure job state=%s step=%s", erasureState, erasureStep)
	}
	if err = repository.EraseUserProfile(ctx, userID); err != nil {
		t.Fatal(err)
	}
	var profileCount int
	if err = fixture.Pool.QueryRow(ctx, "SELECT count(*) FROM user_profiles WHERE user_id = $1", userID).Scan(&profileCount); err != nil {
		t.Fatal(err)
	}
	if profileCount != 0 {
		t.Fatalf("profile rows after erase = %d, want 0", profileCount)
	}
}

func governanceDigest(marker byte) [32]byte {
	var digest [32]byte
	for index := range digest {
		digest[index] = marker
	}
	return digest
}

func insertDeviceCredential(
	t testing.TB,
	ctx context.Context,
	fixture *integrationtest.PostgresSchema,
	userID, credentialID uuid.UUID,
	label string,
	createdAt time.Time,
	expired bool,
) {
	t.Helper()
	rotatedAt := createdAt
	lastSeenAt := createdAt
	if expired {
		// Expired fixtures still need to satisfy the new invariant: rotated <= last_seen < idle <= absolute.
		lastSeenAt = createdAt.Add(200 * 24 * time.Hour)
	}
	absoluteExpiresAt := createdAt.Add(365 * 24 * time.Hour)
	idleExpiresAt := lastSeenAt.Add(180 * 24 * time.Hour)
	if idleExpiresAt.After(absoluteExpiresAt) {
		idleExpiresAt = absoluteExpiresAt
	}
	if _, err := fixture.Pool.Exec(ctx, `
        INSERT INTO device_credentials (
            credential_id, user_id, secret_hash, secret_key_version, csrf_hash, generation, label,
            created_at, last_seen_at, rotated_at, idle_expires_at, absolute_expires_at
        ) VALUES ($1, $2, decode(repeat('01', 32), 'hex'), 1, decode(repeat('02', 32), 'hex'), 1, $3, $4, $5, $6, $7, $8)
    `, credentialID, userID, label, createdAt, lastSeenAt, rotatedAt, idleExpiresAt, absoluteExpiresAt); err != nil {
		t.Fatal(err)
	}
}

func insertUserProfile(
	t testing.TB,
	ctx context.Context,
	fixture *integrationtest.PostgresSchema,
	userID, adminID uuid.UUID,
	updatedAt time.Time,
) {
	t.Helper()
	if _, err := fixture.Pool.Exec(ctx, `
        INSERT INTO user_profiles (
            user_id, real_name_ciphertext, real_name_nonce, real_name_key_version,
            profile_version, real_name_updated_at, real_name_updated_by
        ) VALUES ($1, decode('010203', 'hex'), decode('0405', 'hex'), 1, 1, $2, $3)
    `, userID, updatedAt, adminID); err != nil {
		t.Fatal(err)
	}
}

func insertQueuedUserExport(
	t testing.TB,
	ctx context.Context,
	fixture *integrationtest.PostgresSchema,
	adminID, userID uuid.UUID,
	createdAt time.Time,
) uuid.UUID {
	t.Helper()
	exportID := uuid.New()
	filterSnapshot := fmt.Sprintf(`{"user_id":"%s"}`, userID.String())
	if _, err := fixture.Pool.Exec(ctx, `
        INSERT INTO admin_export_jobs (
            export_id, actor_admin_id, operation_id, request_digest, filter_schema_version, filter_snapshot, filter_digest,
            field_names, masking_policy, state, matched_users, exported_users, failed_users, result_schema_version,
            result_expires_at, version, created_at, updated_at
        ) VALUES (
            $1, $2, 'queued-export-op', decode(repeat('03', 32), 'hex'), 1, $3::jsonb, decode(repeat('04', 32), 'hex'),
            ARRAY['user_id'], 'redact_pii', 'queued', 0, 0, 0, 1, $4, 1, $5, $5
        )
    `, exportID, adminID, filterSnapshot, createdAt.Add(24*time.Hour), createdAt); err != nil {
		t.Fatal(err)
	}
	return exportID
}

func insertAuditEvent(t testing.TB, ctx context.Context, fixture *integrationtest.PostgresSchema, createdAt time.Time) uuid.UUID {
	t.Helper()
	var nextSequence int64
	if err := fixture.Pool.QueryRow(ctx, "SELECT COALESCE(max(sequence), 0) + 1 FROM audit_events WHERE chain_id = 'admin'").Scan(&nextSequence); err != nil {
		t.Fatal(err)
	}
	eventID := uuid.New()
	if _, err := fixture.Pool.Exec(ctx, `
        INSERT INTO audit_events (
            chain_id, sequence, event_id, previous_hash, canonical_event, event_hash, signature, signing_key_version, created_at
        ) VALUES (
            'admin', $1, $2, decode(repeat('00', 32), 'hex'), convert_to('{}', 'UTF8'), decode(repeat('00', 32), 'hex'),
            decode(repeat('00', 64), 'hex'), 1, $3
        )
    `, nextSequence, eventID, createdAt); err != nil {
		t.Fatal(err)
	}
	return eventID
}
