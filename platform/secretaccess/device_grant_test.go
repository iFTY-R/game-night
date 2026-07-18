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

func TestDeviceGrantRejectsTamperedClaimsAndExpiryBoundary(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	keyring := deviceGrantTestKeyring(t, now)
	credentialID, actorID, resultID := uuid.New(), uuid.New(), uuid.New()
	grant, err := MintDeviceGrant(keyring, credentialID, 7, actorID, resultID, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyDeviceGrant(keyring, grant, resultID, actorID, now) ||
		VerifyDeviceGrant(keyring, grant, uuid.New(), actorID, now) ||
		VerifyDeviceGrant(keyring, grant, resultID, uuid.New(), now) ||
		VerifyDeviceGrant(keyring, grant, resultID, actorID, now.Add(time.Minute)) {
		t.Fatal("grant did not enforce exact result, actor, and half-open expiry")
	}
	tampered := grant
	tampered.generation++
	if VerifyDeviceGrant(keyring, tampered, resultID, actorID, now) {
		t.Fatal("grant accepted a generation change without a matching MAC")
	}
}

func deviceGrantTestKeyring(t testing.TB, now time.Time) *security.HMACKeyring[security.DeviceHMACKeyPurpose] {
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
	path := filepath.Join(t.TempDir(), "device-grant-keyring.json")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	keyring, err := security.LoadHMACKeyring[security.DeviceHMACKeyPurpose](path, now)
	if err != nil {
		t.Fatal(err)
	}
	return keyring
}
