package postgres

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/internal/integrationtest"
	"github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/keyrotation"
	"github.com/iFTY-R/game-night/platform/profile"
	"github.com/iFTY-R/game-night/platform/security"
)

func TestKeyRotationUnitOfWorkRotatesProfileAndAppendsAuditOnRealPostgres(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)
	// Keep the test event after the migration transaction even when the remote database clock is a little ahead.
	now := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	keyOne, keyTwo := randomAESKey(t), randomAESKey(t)
	oldPIIKeyring, err := loadRotationAESKeyring[security.PIIKeyPurpose](t, now, 1, keyOne, keyTwo, "pii-old.json")
	if err != nil {
		t.Fatal(err)
	}
	activePIIKeyring, err := loadRotationAESKeyring[security.PIIKeyPurpose](t, now, 2, keyOne, keyTwo, "pii-active.json")
	if err != nil {
		t.Fatal(err)
	}
	activeTOTPKeyring, err := loadRotationAESKeyring[security.TOTPKeyPurpose](t, now, 2, randomAESKey(t), randomAESKey(t), "totp.json")
	if err != nil {
		t.Fatal(err)
	}
	auditKeyring, err := loadRotationAuditKeyring(t, now)
	if err != nil {
		t.Fatal(err)
	}
	auditService, err := audit.NewService(auditKeyring)
	if err != nil {
		t.Fatal(err)
	}
	piiOld, _ := profile.NewDefaultPIIProtector(oldPIIKeyring)
	piiActive, _ := profile.NewDefaultPIIProtector(activePIIKeyring)
	totpService, _ := admin.NewTOTPService(activeTOTPKeyring)
	checkpointPolicy, err := audit.NewCheckpointHealthPolicyWithThresholds(false,
		audit.SinkReadinessFunc(func(context.Context) bool { return true }), audit.CheckpointMaxEvents, audit.CheckpointMaxAge)
	if err != nil {
		t.Fatal(err)
	}
	adminID := integrationAdminID(t, ctx, fixture)
	userID := uuid.New()
	if _, err := fixture.Pool.Exec(ctx, `
INSERT INTO users (user_id, status, created_at, updated_at)
VALUES ($1, 'onboarding', $2, $2)`, userID, now); err != nil {
		t.Fatal(err)
	}
	encrypted, err := piiOld.EncryptRealName(userID, "rotation-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Pool.Exec(ctx, `
INSERT INTO user_profiles (
    user_id, real_name_ciphertext, real_name_nonce, real_name_key_version,
    profile_version, real_name_updated_at, real_name_updated_by
) VALUES ($1, $2, $3, $4, 1, $5, $6)`, userID, encrypted.Ciphertext, encrypted.Nonce,
		encrypted.KeyVersion, now, adminID); err != nil {
		t.Fatal(err)
	}
	service, err := keyrotation.NewService(keyrotation.Config{Owner: "integration-rotation", LeaseDuration: time.Minute, BatchSize: 10},
		NewKeyRotationUnitOfWork(fixture.Pool, auditService), piiActive, totpService, auditService, checkpointPolicy, clock.NewFake(now))
	if err != nil {
		t.Fatal(err)
	}
	completed := false
	for attempt := 0; attempt < 6; attempt++ {
		result, runErr := service.RunOnce(ctx)
		if runErr != nil {
			t.Fatal(runErr)
		}
		if result.Completed {
			completed = true
			break
		}
	}
	if !completed {
		t.Fatal("rotation did not complete within resumable passes")
	}
	var keyVersion int32
	if err := fixture.Pool.QueryRow(ctx, "SELECT real_name_key_version FROM user_profiles WHERE user_id = $1", userID).Scan(&keyVersion); err != nil {
		t.Fatal(err)
	}
	if keyVersion != 2 {
		t.Fatalf("profile key version = %d", keyVersion)
	}
	var oldReferences int64
	if err := fixture.Pool.QueryRow(ctx, "SELECT count(*) FROM user_profiles WHERE real_name_key_version = 1").Scan(&oldReferences); err != nil {
		t.Fatal(err)
	}
	if oldReferences != 0 {
		t.Fatalf("old PII references remain: %d", oldReferences)
	}
	var jobStatus string
	if err := fixture.Pool.QueryRow(ctx, "SELECT status FROM key_rotation_jobs WHERE purpose = 'pii' ORDER BY created_at DESC LIMIT 1").Scan(&jobStatus); err != nil {
		t.Fatal(err)
	}
	if jobStatus != "completed" {
		t.Fatalf("rotation job status = %s", jobStatus)
	}
	var auditSequence int64
	if err := fixture.Pool.QueryRow(ctx, "SELECT sequence FROM audit_chain_head WHERE chain_id = 'admin'").Scan(&auditSequence); err != nil {
		t.Fatal(err)
	}
	if auditSequence < 4 {
		t.Fatalf("rotation audit sequence = %d", auditSequence)
	}
}

func integrationAdminID(t testing.TB, ctx context.Context, fixture *integrationtest.PostgresSchema) uuid.UUID {
	t.Helper()
	var adminID uuid.UUID
	if err := fixture.Pool.QueryRow(ctx, "SELECT admin_id FROM admin_accounts LIMIT 1").Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	return adminID
}

func randomAESKey(t testing.TB) []byte {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

func loadRotationAESKeyring[P security.AESKeyPurpose](
	t testing.TB,
	now time.Time,
	active uint32,
	keyOne, keyTwo []byte,
	filename string,
) (*security.AESKeyring[P], error) {
	t.Helper()
	document := map[string]any{
		"active_version": active,
		"keys": []map[string]any{
			{"version": 1, "key": base64.StdEncoding.EncodeToString(keyOne), "not_before": now.Add(-time.Hour)},
			{"version": 2, "key": base64.StdEncoding.EncodeToString(keyTwo), "not_before": now.Add(-time.Hour)},
		},
	}
	contents, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(t.TempDir(), filename)
	if err := os.WriteFile(path, contents, 0o400); err != nil {
		return nil, err
	}
	return security.LoadAESKeyring[P](path, now)
}

func loadRotationAuditKeyring(t testing.TB, now time.Time) (*security.AuditKeyring, error) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	document := map[string]any{
		"active_version": 1,
		"keys": []map[string]any{{
			"version": 1, "public_key": base64.StdEncoding.EncodeToString(public),
			"private_key": base64.StdEncoding.EncodeToString(private), "not_before": now.Add(-time.Hour),
		}},
	}
	contents, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(t.TempDir(), "audit-rotation.json")
	if err := os.WriteFile(path, contents, 0o400); err != nil {
		return nil, err
	}
	return security.LoadAuditKeyring(path, now)
}
