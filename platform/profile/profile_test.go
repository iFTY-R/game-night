package profile

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/security"
)

func TestPIIAADBindsUserFieldAndSchema(t *testing.T) {
	userID := uuid.New()
	base, err := PIIAssociatedData(userID, FieldRealName, ProfileSchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	for name, candidate := range map[string][]byte{
		"other user":   mustAAD(t, uuid.New(), FieldRealName, ProfileSchemaVersion),
		"other field":  mustAAD(t, userID, Field("other"), ProfileSchemaVersion),
		"other schema": mustAAD(t, userID, FieldRealName, ProfileSchemaVersion+1),
	} {
		if bytes.Equal(base, candidate) {
			t.Fatalf("AAD for %s unexpectedly matched", name)
		}
	}
	if _, err := PIIAssociatedData(uuid.Nil, FieldRealName, ProfileSchemaVersion); !errors.Is(err, ErrInvalidProfileInput) {
		t.Fatalf("nil user was accepted: %v", err)
	}
}

func TestPIIProtectorRejectsWrongUserAndField(t *testing.T) {
	now := time.Now().UTC()
	keyring := loadTestAESKeyring(t, now, 1, []uint32{1})
	protector, err := NewDefaultPIIProtector(keyring)
	if err != nil {
		t.Fatal(err)
	}
	userID := uuid.New()
	payload, err := protector.EncryptRealName(userID, "张三")
	if err != nil {
		t.Fatal(err)
	}
	name, err := protector.DecryptRealName(userID, payload)
	if err != nil || name != "张三" {
		t.Fatalf("decrypt name = %q, err = %v", name, err)
	}
	if _, err := protector.Decrypt(uuid.New(), FieldRealName, payload); !errors.Is(err, ErrPIIAuthentication) {
		t.Fatalf("wrong user error = %v", err)
	}
	if _, err := protector.Decrypt(userID, Field("other"), payload); !errors.Is(err, ErrPIIAuthentication) {
		t.Fatalf("wrong field error = %v", err)
	}
	if _, err := protector.EncryptRealName(userID, "\x00bad"); !errors.Is(err, ErrInvalidProfileInput) {
		t.Fatalf("control character was accepted: %v", err)
	}
}

func TestPIIKeyRotationDecryptsHistoricalAndReencryptsActive(t *testing.T) {
	now := time.Now().UTC()
	first := loadTestAESKeyring(t, now, 1, []uint32{1})
	rotated := loadTestAESKeyring(t, now, 2, []uint32{1, 2})
	oldProtector, _ := NewDefaultPIIProtector(first)
	newProtector, _ := NewDefaultPIIProtector(rotated)
	userID := uuid.New()
	payload, err := oldProtector.EncryptRealName(userID, "李四")
	if err != nil {
		t.Fatal(err)
	}
	if !newProtector.RequiresRotation(payload) {
		t.Fatal("historical payload was not marked for rotation")
	}
	reencrypted, err := newProtector.Reencrypt(userID, FieldRealName, payload)
	if err != nil {
		t.Fatal(err)
	}
	if reencrypted.KeyVersion != 2 {
		t.Fatalf("reencrypted version = %d, want 2", reencrypted.KeyVersion)
	}
	name, err := newProtector.DecryptRealName(userID, reencrypted)
	if err != nil || name != "李四" {
		t.Fatalf("rotated decrypt = %q, err = %v", name, err)
	}
}

func TestUserProfileVersionedUpdateAndClone(t *testing.T) {
	now := time.Date(2026, 7, 19, 1, 2, 3, 456789000, time.FixedZone("CST", 8*60*60))
	value := EncryptedValue{KeyVersion: 1, Nonce: bytes.Repeat([]byte{1}, ProfileAESNonceSize), Ciphertext: bytes.Repeat([]byte{2}, ProfileAESOverhead)}
	profile, err := NewUserProfile(uuid.New(), value, now, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := profile.Snapshot()
	snapshot.RealNameNonce[0] = 9
	if profile.Snapshot().RealNameNonce[0] == 9 {
		t.Fatal("profile snapshot exposed mutable nonce")
	}
	updated, err := profile.UpdateEncrypted(1, value, now.Add(time.Second), uuid.New())
	if err != nil || updated.ProfileVersion() != 2 {
		t.Fatalf("update version = %d, err = %v", updated.ProfileVersion(), err)
	}
	if _, err := profile.UpdateEncrypted(2, value, now.Add(time.Second), uuid.New()); !errors.Is(err, ErrProfileConcurrentTransition) {
		t.Fatalf("stale version error = %v", err)
	}
}

func mustAAD(t *testing.T, userID uuid.UUID, field Field, schemaVersion uint32) []byte {
	t.Helper()
	value, err := PIIAssociatedData(userID, field, schemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func loadTestAESKeyring(t *testing.T, now time.Time, active uint32, versions []uint32) *security.AESKeyring[security.PIIKeyPurpose] {
	t.Helper()
	type keyDocument struct {
		Version   uint32    `json:"version"`
		Key       string    `json:"key"`
		NotBefore time.Time `json:"not_before"`
	}
	document := struct {
		ActiveVersion uint32        `json:"active_version"`
		Keys          []keyDocument `json:"keys"`
	}{ActiveVersion: active}
	for _, version := range versions {
		key := bytes.Repeat([]byte{byte(version)}, 32)
		document.Keys = append(document.Keys, keyDocument{Version: version, Key: base64.StdEncoding.EncodeToString(key), NotBefore: now.Add(-time.Hour)})
	}
	contents, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "pii-keyring.json")
	if err := os.WriteFile(path, contents, 0o400); err != nil {
		t.Fatal(err)
	}
	keyring, err := security.LoadAESKeyring[security.PIIKeyPurpose](path, now)
	if err != nil {
		t.Fatal(err)
	}
	return keyring
}
