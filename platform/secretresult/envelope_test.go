package secretresult

import (
	"bytes"
	"crypto/rand"
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

func TestEnvelopeCipherRoundTripAndAADIsolation(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 987654321, time.UTC)
	cipher := testEnvelopeCipher(t, now)
	binding := testBinding(t)
	expiresAt := now.Add(5 * time.Minute)
	plaintext := []byte("device-token-and-recovery-code")
	payload, err := cipher.Seal(plaintext, binding, expiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if payload.KeyVersion != 7 || len(payload.Ciphertext) == 0 || len(payload.Nonce) == 0 || len(payload.WrappedDataKey) == 0 {
		t.Fatalf("incomplete envelope: %+v", payload)
	}
	opened, err := cipher.open(payload, binding, expiresAt)
	if err != nil || !bytes.Equal(opened, plaintext) {
		t.Fatalf("open = %q, err=%v", opened, err)
	}

	tests := map[string]func(*EncryptedPayload, *Binding, *time.Time){
		"actor": func(_ *EncryptedPayload, candidate *Binding, _ *time.Time) { candidate.Key.ActorID = uuid.New() },
		"scope": func(_ *EncryptedPayload, candidate *Binding, _ *time.Time) {
			candidate.Key.Scope = ScopeAdminTOTPEnrollment
		},
		"digest": func(_ *EncryptedPayload, candidate *Binding, _ *time.Time) {
			candidate.RequestDigest[0] ^= 0xff
		},
		"type": func(_ *EncryptedPayload, candidate *Binding, _ *time.Time) {
			candidate.ResultType = ResultTypeIdentityRecoveryCode
		},
		"version": func(_ *EncryptedPayload, candidate *Binding, _ *time.Time) {
			candidate.ResultVersion++
		},
		"key version": func(candidate *EncryptedPayload, _ *Binding, _ *time.Time) { candidate.KeyVersion++ },
		"expiry": func(_ *EncryptedPayload, _ *Binding, candidate *time.Time) {
			*candidate = candidate.Add(time.Microsecond)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidatePayload := payload
			candidateBinding := binding
			candidateExpiry := expiresAt
			mutate(&candidatePayload, &candidateBinding, &candidateExpiry)
			if _, err := cipher.open(candidatePayload, candidateBinding, candidateExpiry); !errors.Is(err, ErrEnvelopeAuthentication) {
				t.Fatalf("tampered envelope error = %v", err)
			}
		})
	}
}

func TestEnvelopeCipherUsesPostgreSQLStableMicrosecondTime(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 123456789, time.UTC)
	cipher := testEnvelopeCipher(t, now)
	binding := testBinding(t)
	payload, err := cipher.Seal([]byte("secret"), binding, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cipher.open(payload, binding, now.Truncate(time.Microsecond)); err != nil {
		t.Fatalf("PostgreSQL timestamptz precision changed AAD: %v", err)
	}
}

func testEnvelopeCipher(t testing.TB, now time.Time) *EnvelopeCipher {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	document := struct {
		ActiveVersion uint32 `json:"active_version"`
		Keys          []struct {
			Version   uint32    `json:"version"`
			Key       string    `json:"key"`
			NotBefore time.Time `json:"not_before"`
		} `json:"keys"`
	}{ActiveVersion: 7}
	document.Keys = append(document.Keys, struct {
		Version   uint32    `json:"version"`
		Key       string    `json:"key"`
		NotBefore time.Time `json:"not_before"`
	}{Version: 7, Key: base64.StdEncoding.EncodeToString(key), NotBefore: now.Add(-time.Hour)})
	contents, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "result-envelope-keyring.json")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	keyring, err := security.LoadAESKeyring[security.ResultEnvelopeKeyPurpose](path, now)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := NewEnvelopeCipher(keyring)
	if err != nil {
		t.Fatal(err)
	}
	return cipher
}
