package admin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iFTY-R/game-night/platform/security"
)

func TestTOTPReencryptPreservesEnrollmentAADContract(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	oldKeyring := loadTestTOTPKeyring(t, now, 1, []uint32{1, 2})
	activeKeyring := loadTestTOTPKeyring(t, now, 2, []uint32{1, 2})
	oldService, err := NewTOTPService(oldKeyring)
	if err != nil {
		t.Fatal(err)
	}
	activeService, err := NewTOTPService(activeKeyring)
	if err != nil {
		t.Fatal(err)
	}
	var adminID, enrollmentID [16]byte
	adminID[0], enrollmentID[0] = 7, 9
	secret, _, encrypted, err := oldService.NewEnrollmentSecret(adminID, enrollmentID, "Game Night", "admin")
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := activeService.ReencryptSeed(adminID, enrollmentID, encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.KeyVersion != 2 {
		t.Fatalf("rotation key version = %d", rotated.KeyVersion)
	}
	decrypted, err := activeService.DecryptSeed(adminID, enrollmentID, rotated)
	if err != nil || decrypted != secret {
		t.Fatalf("rotated seed mismatch: %v", err)
	}
	adminID[0]++
	if _, err := activeService.DecryptSeed(adminID, enrollmentID, rotated); !errors.Is(err, ErrTOTPInvalid) {
		t.Fatalf("wrong enrollment binding error = %v", err)
	}
}

type totpKeyringDocument struct {
	ActiveVersion uint32                `json:"active_version"`
	Keys          []totpKeyringKeyEntry `json:"keys"`
}

type totpKeyringKeyEntry struct {
	Version   uint32    `json:"version"`
	Key       string    `json:"key"`
	NotBefore time.Time `json:"not_before"`
}

func loadTestTOTPKeyring(t testing.TB, now time.Time, active uint32, versions []uint32) *security.AESKeyring[security.TOTPKeyPurpose] {
	t.Helper()
	document := totpKeyringDocument{ActiveVersion: active}
	for _, version := range versions {
		key := bytes.Repeat([]byte{byte(version)}, 32)
		document.Keys = append(document.Keys, totpKeyringKeyEntry{
			Version: version, Key: base64.StdEncoding.EncodeToString(key), NotBefore: now.Add(-time.Hour),
		})
	}
	contents, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "totp-keyring.json")
	if err := os.WriteFile(path, contents, 0o400); err != nil {
		t.Fatal(err)
	}
	keyring, err := security.LoadAESKeyring[security.TOTPKeyPurpose](path, now)
	if err != nil {
		t.Fatal(err)
	}
	return keyring
}
