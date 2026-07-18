package security

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestAESKeyringRotationAndAssociatedData(t *testing.T) {
	now := time.Now().UTC()
	firstKey := randomTestBytes(t, 32)
	secondKey := randomTestBytes(t, 32)
	firstPath := writeSymmetricKeyring(t, keyringDocument{
		ActiveVersion: 1,
		Keys:          []keyDocument{testKeyDocument(1, firstKey, now.Add(-time.Hour), time.Time{})},
	})
	rotatedPath := writeSymmetricKeyring(t, keyringDocument{
		ActiveVersion: 2,
		Keys: []keyDocument{
			testKeyDocument(1, firstKey, now.Add(-time.Hour), now.Add(time.Hour)),
			testKeyDocument(2, secondKey, now.Add(-time.Minute), time.Time{}),
		},
	})

	first, err := LoadAESKeyring[PIIKeyPurpose](firstPath, now)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := first.Encrypt([]byte("private name"), []byte("user-1:real_name:v1"))
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := LoadAESKeyring[PIIKeyPurpose](rotatedPath, now)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := rotated.Decrypt(payload, []byte("user-1:real_name:v1"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plaintext, []byte("private name")) {
		t.Fatalf("unexpected plaintext %q", plaintext)
	}
	newPayload, err := rotated.Encrypt([]byte("new value"), []byte("user-1:real_name:v1"))
	if err != nil {
		t.Fatal(err)
	}
	if newPayload.KeyVersion != 2 {
		t.Fatalf("expected active version 2, got %d", newPayload.KeyVersion)
	}
	if _, err := rotated.Decrypt(payload, []byte("user-2:real_name:v1")); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("expected AAD authentication failure, got %v", err)
	}
}

func TestAESKeyringRejectsUnknownOrWrongKey(t *testing.T) {
	now := time.Now().UTC()
	firstPath := writeSymmetricKeyring(t, keyringDocument{
		ActiveVersion: 1,
		Keys:          []keyDocument{testKeyDocument(1, randomTestBytes(t, 32), now.Add(-time.Hour), time.Time{})},
	})
	secondPath := writeSymmetricKeyring(t, keyringDocument{
		ActiveVersion: 1,
		Keys:          []keyDocument{testKeyDocument(1, randomTestBytes(t, 32), now.Add(-time.Hour), time.Time{})},
	})
	first, _ := LoadAESKeyring[ResultEnvelopeKeyPurpose](firstPath, now)
	second, _ := LoadAESKeyring[ResultEnvelopeKeyPurpose](secondPath, now)
	payload, err := first.Encrypt([]byte("secret"), []byte("scope"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Decrypt(payload, []byte("scope")); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("expected wrong-key authentication failure, got %v", err)
	}
	payload.KeyVersion = 99
	if _, err := first.Decrypt(payload, []byte("scope")); !errors.Is(err, ErrUnknownKeyVersion) {
		t.Fatalf("expected unknown version, got %v", err)
	}
}

func TestAESKeyringReportsImmutableActiveVersion(t *testing.T) {
	now := time.Now().UTC()
	path := writeSymmetricKeyring(t, keyringDocument{
		ActiveVersion: 9,
		Keys:          []keyDocument{testKeyDocument(9, randomTestBytes(t, 32), now.Add(-time.Hour), time.Time{})},
	})
	keyring, err := LoadAESKeyring[ResultEnvelopeKeyPurpose](path, now)
	if err != nil {
		t.Fatal(err)
	}
	if keyring.ActiveVersion() != 9 {
		t.Fatalf("active version = %d, want 9", keyring.ActiveVersion())
	}
}

func TestHMACKeyringVersionedDigest(t *testing.T) {
	now := time.Now().UTC()
	historicalKey := randomTestBytes(t, 32)
	path := writeSymmetricKeyring(t, keyringDocument{
		ActiveVersion: 8,
		Keys: []keyDocument{
			testKeyDocument(7, historicalKey, now.Add(-2*time.Hour), time.Time{}),
			testKeyDocument(8, randomTestBytes(t, 32), now.Add(-time.Hour), time.Time{}),
		},
	})
	ring, err := LoadHMACKeyring[DeviceHMACKeyPurpose](path, now)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := ring.Sum([]byte("device secret"))
	if err != nil {
		t.Fatal(err)
	}
	if digest.KeyVersion != 8 || len(digest.Value) != 32 {
		t.Fatalf("unexpected digest metadata: version=%d length=%d", digest.KeyVersion, len(digest.Value))
	}
	matched, err := ring.Verify([]byte("device secret"), digest)
	if err != nil || !matched {
		t.Fatalf("expected digest match, matched=%t err=%v", matched, err)
	}
	matched, err = ring.Verify([]byte("other secret"), digest)
	if err != nil || matched {
		t.Fatalf("expected digest mismatch, matched=%t err=%v", matched, err)
	}
	historical := MAC[DeviceHMACKeyPurpose]{KeyVersion: 7, Value: sumHMAC(historicalKey, []byte("old device secret"))}
	matched, err = ring.Verify([]byte("old device secret"), historical)
	if err != nil || !matched {
		t.Fatalf("historical HMAC did not verify, matched=%t err=%v", matched, err)
	}
}

func TestSymmetricKeyringRejectsInvalidDocuments(t *testing.T) {
	now := time.Now().UTC()
	validKey := randomTestBytes(t, 32)
	tests := []struct {
		name     string
		document keyringDocument
	}{
		{
			name: "duplicate version",
			document: keyringDocument{ActiveVersion: 1, Keys: []keyDocument{
				testKeyDocument(1, validKey, now.Add(-time.Hour), time.Time{}),
				testKeyDocument(1, validKey, now.Add(-time.Hour), time.Time{}),
			}},
		},
		{
			name: "short key",
			document: keyringDocument{ActiveVersion: 1, Keys: []keyDocument{
				testKeyDocument(1, []byte("short"), now.Add(-time.Hour), time.Time{}),
			}},
		},
		{
			name: "future active key",
			document: keyringDocument{ActiveVersion: 1, Keys: []keyDocument{
				testKeyDocument(1, validKey, now.Add(time.Hour), time.Time{}),
			}},
		},
		{
			name: "retired active key",
			document: keyringDocument{ActiveVersion: 1, Keys: []keyDocument{
				testKeyDocument(1, validKey, now.Add(-2*time.Hour), now.Add(-time.Hour)),
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeSymmetricKeyring(t, test.document)
			if _, err := LoadHMACKeyring[RateLimitHMACKeyPurpose](path, now); !errors.Is(err, ErrInvalidKeyring) {
				t.Fatalf("expected invalid keyring, got %v", err)
			}
		})
	}
}

func TestKeyringLoaderRejectsWritableSecretFile(t *testing.T) {
	now := time.Now().UTC()
	path := writeSymmetricKeyring(t, keyringDocument{
		ActiveVersion: 1,
		Keys:          []keyDocument{testKeyDocument(1, randomTestBytes(t, 32), now.Add(-time.Hour), time.Time{})},
	})
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAESKeyring[TOTPKeyPurpose](path, now); !errors.Is(err, ErrInvalidKeyring) {
		t.Fatalf("expected writable keyring rejection, got %v", err)
	}
}

func TestKeyringLoaderRejectsGroupOrWorldReadableSecretFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows os.FileMode does not expose read ACL ownership")
	}
	now := time.Now().UTC()
	path := writeSymmetricKeyring(t, keyringDocument{
		ActiveVersion: 1,
		Keys:          []keyDocument{testKeyDocument(1, randomTestBytes(t, 32), now.Add(-time.Hour), time.Time{})},
	})
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAESKeyring[TOTPKeyPurpose](path, now); !errors.Is(err, ErrInvalidKeyring) {
		t.Fatalf("expected broadly readable keyring rejection, got %v", err)
	}
}

func TestKeyringLoaderRejectsSymbolicLink(t *testing.T) {
	now := time.Now().UTC()
	target := writeSymmetricKeyring(t, keyringDocument{
		ActiveVersion: 1,
		Keys:          []keyDocument{testKeyDocument(1, randomTestBytes(t, 32), now.Add(-time.Hour), time.Time{})},
	})
	link := filepath.Join(t.TempDir(), "keyring-link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if _, err := LoadAESKeyring[TOTPKeyPurpose](link, now); !errors.Is(err, ErrInvalidKeyring) {
		t.Fatalf("expected symbolic link rejection, got %v", err)
	}
}

func TestAuditKeyringSignsAndVerifiesHistoricalKeys(t *testing.T) {
	now := time.Now().UTC()
	firstPublic, firstPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secondPublic, secondPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	path := writeAuditKeyring(t, auditKeyringDocument{
		ActiveVersion: 2,
		Keys: []auditKeyDocument{
			{Version: 1, PublicKey: base64.StdEncoding.EncodeToString(firstPublic), NotBefore: now.Add(-2 * time.Hour)},
			{Version: 2, PublicKey: base64.StdEncoding.EncodeToString(secondPublic), PrivateKey: base64.StdEncoding.EncodeToString(secondPrivate), NotBefore: now.Add(-time.Hour)},
		},
	})
	ring, err := LoadAuditKeyring(path, now)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := ring.Sign([]byte("canonical audit event"))
	if err != nil {
		t.Fatal(err)
	}
	if ring.ActiveVersion() != 2 || signature.KeyVersion != 2 || !ring.Verify([]byte("canonical audit event"), signature) {
		t.Fatal("active audit signature did not verify")
	}
	historical := AuditSignature{KeyVersion: 1, Value: ed25519.Sign(firstPrivate, []byte("old event"))}
	if !ring.Verify([]byte("old event"), historical) {
		t.Fatal("historical public key did not verify")
	}
}

func TestAuditKeyringRejectsPrivateKeyNotDerivedFromSeed(t *testing.T) {
	now := time.Now().UTC()
	_, firstPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secondPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tamperedPrivate := bytes.Clone(firstPrivate)
	copy(tamperedPrivate[ed25519.SeedSize:], secondPublic)

	tests := []struct {
		name       string
		publicKey  ed25519.PublicKey
		privateKey ed25519.PrivateKey
	}{
		{name: "seed and suffix disagree", publicKey: secondPublic, privateKey: tamperedPrivate},
		{name: "private suffix and document disagree", publicKey: secondPublic, privateKey: firstPrivate},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeAuditKeyring(t, auditKeyringDocument{
				ActiveVersion: 1,
				Keys: []auditKeyDocument{{
					Version:    1,
					PublicKey:  base64.StdEncoding.EncodeToString(test.publicKey),
					PrivateKey: base64.StdEncoding.EncodeToString(test.privateKey),
					NotBefore:  now.Add(-time.Hour),
				}},
			})
			if _, err := LoadAuditKeyring(path, now); !errors.Is(err, ErrInvalidKeyring) {
				t.Fatalf("expected invalid private key rejection, got %v", err)
			}
		})
	}
}

func TestAuditKeyringRejectsHistoricalPrivateKeys(t *testing.T) {
	now := time.Now().UTC()
	firstPublic, firstPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secondPublic, secondPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	path := writeAuditKeyring(t, auditKeyringDocument{
		ActiveVersion: 2,
		Keys: []auditKeyDocument{
			{Version: 1, PublicKey: base64.StdEncoding.EncodeToString(firstPublic), PrivateKey: base64.StdEncoding.EncodeToString(firstPrivate), NotBefore: now.Add(-2 * time.Hour)},
			{Version: 2, PublicKey: base64.StdEncoding.EncodeToString(secondPublic), PrivateKey: base64.StdEncoding.EncodeToString(secondPrivate), NotBefore: now.Add(-time.Hour)},
		},
	})
	if _, err := LoadAuditKeyring(path, now); !errors.Is(err, ErrInvalidKeyring) {
		t.Fatalf("expected historical private key rejection, got %v", err)
	}
}

func TestLoadKeyringsRejectsMaterialReusedAcrossPurposes(t *testing.T) {
	now := time.Now().UTC()
	sharedKey := randomTestBytes(t, 32)
	paths := testKeyringPaths(t, now, sharedKey, sharedKey)
	if _, err := LoadKeyrings(paths, now); !errors.Is(err, ErrInvalidKeyring) {
		t.Fatalf("expected cross-purpose key reuse rejection, got %v", err)
	}
}

func TestLoadKeyringsReturnsPurposeTypedBundle(t *testing.T) {
	now := time.Now().UTC()
	paths := testKeyringPaths(t, now, randomTestBytes(t, 32), randomTestBytes(t, 32))
	keyrings, err := LoadKeyrings(paths, now)
	if err != nil {
		t.Fatal(err)
	}
	if keyrings.PII == nil || keyrings.TOTP == nil || keyrings.ResultEnvelope == nil ||
		keyrings.Device == nil || keyrings.RateLimit == nil || keyrings.UserChallenge == nil ||
		keyrings.AdminChallenge == nil || keyrings.Audit == nil {
		t.Fatal("loaded keyring bundle is incomplete")
	}
}

func testKeyringPaths(t testing.TB, now time.Time, piiKey, totpKey []byte) KeyringPaths {
	t.Helper()
	writeSymmetric := func(key []byte) string {
		return writeSymmetricKeyring(t, keyringDocument{
			ActiveVersion: 1,
			Keys:          []keyDocument{testKeyDocument(1, key, now.Add(-time.Hour), time.Time{})},
		})
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auditPath := writeAuditKeyring(t, auditKeyringDocument{
		ActiveVersion: 1,
		Keys: []auditKeyDocument{{
			Version:    1,
			PublicKey:  base64.StdEncoding.EncodeToString(publicKey),
			PrivateKey: base64.StdEncoding.EncodeToString(privateKey),
			NotBefore:  now.Add(-time.Hour),
		}},
	})
	return KeyringPaths{
		PII:            writeSymmetric(piiKey),
		TOTP:           writeSymmetric(totpKey),
		ResultEnvelope: writeSymmetric(randomTestBytes(t, 32)),
		Device:         writeSymmetric(randomTestBytes(t, 32)),
		RateLimit:      writeSymmetric(randomTestBytes(t, 32)),
		UserChallenge:  writeSymmetric(randomTestBytes(t, 32)),
		AdminChallenge: writeSymmetric(randomTestBytes(t, 32)),
		Audit:          auditPath,
	}
}

func randomTestBytes(t testing.TB, length int) []byte {
	t.Helper()
	value := make([]byte, length)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	return value
}

func testKeyDocument(version uint32, key []byte, notBefore, retireAfter time.Time) keyDocument {
	return keyDocument{
		Version:     version,
		Key:         base64.StdEncoding.EncodeToString(key),
		NotBefore:   notBefore,
		RetireAfter: retireAfter,
	}
}

func writeSymmetricKeyring(t testing.TB, document keyringDocument) string {
	t.Helper()
	return writeReadOnlyJSON(t, "keyring.json", document)
}

func writeAuditKeyring(t testing.TB, document auditKeyringDocument) string {
	t.Helper()
	return writeReadOnlyJSON(t, "audit-keyring.json", document)
}

func writeReadOnlyJSON(t testing.TB, name string, value any) string {
	t.Helper()
	contents, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	return path
}
