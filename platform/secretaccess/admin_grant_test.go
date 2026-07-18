package secretaccess

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/security"
)

func TestAdminGrantBindsSessionActorResultAndExpiry(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	keyring := adminGrantTestKeyring(t, now)
	sessionID, actorID, resultID := uuid.New(), uuid.New(), uuid.New()
	grant, err := MintAdminGrant(keyring, sessionID, actorID, resultID, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyAdminGrant(keyring, grant, resultID, actorID, now) {
		t.Fatal("expected exact admin grant verification")
	}
	if VerifyAdminGrant(keyring, grant, uuid.New(), actorID, now) || VerifyAdminGrant(keyring, grant, resultID, actorID, now.Add(time.Minute)) {
		t.Fatal("admin grant must reject another result and the expiry boundary")
	}
}

func adminGrantTestKeyring(t testing.TB, now time.Time) *security.HMACKeyring[security.AdminSessionKeyPurpose] {
	t.Helper()
	document := map[string]any{"active_version": 1, "keys": []map[string]any{{"version": 1, "key": base64.StdEncoding.EncodeToString(make([]byte, 32)), "not_before": now.Add(-time.Hour)}}}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "admin-grant-keyring.json")
	if err := os.WriteFile(path, encoded, 0o400); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if err := os.Chmod(path, 0o444); err != nil {
			t.Fatal(err)
		}
	}
	keyring, err := security.LoadHMACKeyring[security.AdminSessionKeyPurpose](path, now)
	if err != nil {
		t.Fatal(err)
	}
	return keyring
}
