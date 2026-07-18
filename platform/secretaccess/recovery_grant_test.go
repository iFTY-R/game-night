package secretaccess

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/security"
)

func TestRecoveryGrantRejectsTamperedClaimsAndExpiryBoundary(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	keyring := recoveryGrantTestKeyring(t, now)
	attemptID, actorID, resultID := uuid.New(), uuid.New(), uuid.New()
	grant, err := MintRecoveryGrant(keyring, attemptID, actorID, resultID, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyRecoveryGrant(keyring, grant, resultID, actorID, now) ||
		VerifyRecoveryGrant(keyring, grant, uuid.New(), actorID, now) ||
		VerifyRecoveryGrant(keyring, grant, resultID, uuid.New(), now) ||
		VerifyRecoveryGrant(keyring, grant, resultID, actorID, now.Add(time.Minute)) {
		t.Fatal("grant did not enforce exact result, actor, and half-open expiry")
	}
	tampered := grant
	tampered.attemptID = uuid.New()
	if VerifyRecoveryGrant(keyring, tampered, resultID, actorID, now) {
		t.Fatal("grant accepted an attempt change without a matching MAC")
	}
}

func recoveryGrantTestKeyring(t testing.TB, now time.Time) *security.HMACKeyring[security.UserChallengeKeyPurpose] {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	document := map[string]any{
		"active_version": 1,
		"keys": []map[string]any{{
			"version": 1, "key": base64.StdEncoding.EncodeToString(key), "not_before": now.Add(-time.Hour),
		}},
	}
	contents, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "recovery-grant-keyring.json")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	keyring, err := security.LoadHMACKeyring[security.UserChallengeKeyPurpose](path, now)
	if err != nil {
		t.Fatal(err)
	}
	return keyring
}
