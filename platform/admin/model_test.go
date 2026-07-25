package admin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/security"
)

func TestAccountLifecycleKeepsMFAOutOfAccountState(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	account, err := NewBootstrapAccount(uuid.New(), now)
	if err != nil {
		t.Fatal(err)
	}
	account, err = account.WithPassword(testArgonHash, PasswordAlgorithmArgon2id, testPasswordParametersJSON, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := account.Snapshot().Status; got != AccountStatusSetupRequired {
		t.Fatalf("status = %q, want %q", got, AccountStatusSetupRequired)
	}
	account, err = account.Transition(AccountStatusActive, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := account.Snapshot()
	if snapshot.Status != AccountStatusActive {
		t.Fatalf("status = %q, want %q", snapshot.Status, AccountStatusActive)
	}
	if snapshot.PasswordVersion != 1 {
		t.Fatalf("password version = %d, want 1", snapshot.PasswordVersion)
	}
	if snapshot.AdminVersion != 3 {
		t.Fatalf("admin version = %d, want 3", snapshot.AdminVersion)
	}
}

func TestMFAChangeAdvancesOnlyAdminVersion(t *testing.T) {
	now := time.Date(2026, 7, 19, 13, 0, 0, 0, time.UTC)
	account, err := RestoreAccount(AccountSnapshot{
		ID:                 uuid.New(),
		Username:           "admin",
		Status:             AccountStatusActive,
		PasswordHash:       testArgonHash,
		PasswordAlgorithm:  PasswordAlgorithmArgon2id,
		PasswordParameters: testPasswordParametersJSON,
		PasswordVersion:    3,
		AdminVersion:       7,
		CreatedAt:          now.Add(-time.Hour),
		UpdatedAt:          now.Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := account.RecordMFAChange(now)
	if err != nil {
		t.Fatal(err)
	}
	before, after := account.Snapshot(), updated.Snapshot()
	if after.AdminVersion != before.AdminVersion+1 {
		t.Fatalf("admin version = %d, want %d", after.AdminVersion, before.AdminVersion+1)
	}
	if after.PasswordVersion != before.PasswordVersion || after.PasswordHash != before.PasswordHash ||
		after.PasswordAlgorithm != before.PasswordAlgorithm || after.PasswordParameters != before.PasswordParameters {
		t.Fatal("MFA changes must preserve the password and password version")
	}
}

func TestEnrollmentOwnsReplayFloorAndVersion(t *testing.T) {
	now := time.Date(2026, 7, 19, 14, 0, 0, 0, time.UTC)
	enrollment, err := RestoreEnrollment(EnrollmentSnapshot{
		ID:                uuid.New(),
		AdminID:           uuid.New(),
		Ciphertext:        []byte("ciphertext"),
		Nonce:             []byte("nonce"),
		KeyVersion:        1,
		Status:            EnrollmentStatusPending,
		AdminVersion:      7,
		EnrollmentVersion: 1,
		OperationID:       "op-enable-totp",
		CreatedAt:         now,
		ExpiresAt:         now.Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	activated, err := enrollment.Activate(42, 8, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got := activated.Snapshot().ReplayFloor; got == nil || *got != 42 {
		t.Fatalf("replay floor = %v, want 42", got)
	}
	if got := activated.Snapshot().EnrollmentVersion; got != 2 {
		t.Fatalf("version = %d, want 2", got)
	}
	if got := activated.Snapshot().AdminVersion; got != 8 {
		t.Fatalf("admin version = %d, want 8", got)
	}
	advanced, err := activated.AcceptTOTP(43, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got := advanced.Snapshot().ReplayFloor; got == nil || *got != 43 {
		t.Fatalf("replay floor = %v, want 43", got)
	}
	if got := advanced.Snapshot().EnrollmentVersion; got != 3 {
		t.Fatalf("version = %d, want 3", got)
	}
	if _, err := advanced.AcceptTOTP(43, now.Add(3*time.Minute)); !errors.Is(err, ErrConcurrentTransition) {
		t.Fatalf("expected replay rejection, got %v", err)
	}
	disabled, err := advanced.Disable(9, now.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	disabledSnapshot := disabled.Snapshot()
	if disabledSnapshot.Status != EnrollmentStatusDisabled {
		t.Fatalf("status = %q, want %q", disabledSnapshot.Status, EnrollmentStatusDisabled)
	}
	if len(disabledSnapshot.Ciphertext) != 0 || len(disabledSnapshot.Nonce) != 0 {
		t.Fatal("disabled enrollment must clear ciphertext and nonce")
	}
	if disabledSnapshot.EnrollmentVersion != 4 {
		t.Fatalf("version = %d, want 4", disabledSnapshot.EnrollmentVersion)
	}
	if disabledSnapshot.AdminVersion != 9 {
		t.Fatalf("admin version = %d, want 9", disabledSnapshot.AdminVersion)
	}
}

func TestSecuritySnapshotsDoNotExposeSecretBackingStorage(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 19, 14, 30, 0, 0, time.UTC)
	enrollment, err := RestoreEnrollment(EnrollmentSnapshot{
		ID:                uuid.New(),
		AdminID:           uuid.New(),
		Ciphertext:        []byte{1, 2, 3},
		Nonce:             []byte{4, 5, 6},
		KeyVersion:        1,
		Status:            EnrollmentStatusPending,
		AdminVersion:      3,
		EnrollmentVersion: 1,
		OperationID:       "op-snapshot-isolation",
		CreatedAt:         now,
		ExpiresAt:         now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	enrollmentSnapshot := enrollment.Snapshot()
	enrollmentSnapshot.Ciphertext[0] = 9
	enrollmentSnapshot.Nonce[0] = 9
	if current := enrollment.Snapshot(); current.Ciphertext[0] != 1 || current.Nonce[0] != 4 {
		t.Fatal("enrollment snapshot mutated aggregate secret storage")
	}

	session := mustRestoreTestSession(t, SessionSnapshot{
		ID:                uuid.New(),
		AdminID:           uuid.New(),
		Selector:          "AAAAAAAAAAAAAAAAAAAAAA",
		SecretMAC:         security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: 1, Value: make([]byte, 32)},
		CSRFHash:          security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: 1, Value: make([]byte, 32)},
		Kind:              SessionKindFull,
		AdminVersion:      3,
		PasswordVersion:   1,
		SessionVersion:    1,
		MaxAttempts:       5,
		CreatedAt:         now,
		LastSeenAt:        now,
		IdleExpiresAt:     now.Add(time.Minute),
		AbsoluteExpiresAt: now.Add(time.Hour),
	})
	sessionSnapshot := session.Snapshot()
	sessionSnapshot.SecretMAC.Value[0] = 9
	sessionSnapshot.CSRFHash.Value[0] = 9
	if current := session.Snapshot(); current.SecretMAC.Value[0] != 0 || current.CSRFHash.Value[0] != 0 {
		t.Fatal("session snapshot mutated aggregate proof storage")
	}
}

func TestRecoveryCodeIsOneTimeAndSelectorBound(t *testing.T) {
	service, err := NewRecoveryCodeService(testRecoveryHasher{})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.IssueSet(context.Background(), uuid.New(), 2, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(issued) != AdminRecoveryCodeCount || issued[0].Secret == issued[1].Secret {
		t.Fatal("recovery code set must contain independent secrets")
	}
	if err := service.Verify(context.Background(), issued[0].Code, issued[0].Secret); err != nil {
		t.Fatal(err)
	}
	if err := service.Verify(context.Background(), issued[0].Code, issued[1].Secret); !errors.Is(err, ErrRecoveryInvalid) {
		t.Fatalf("expected selector/secret binding failure, got %v", err)
	}
	consumed, err := issued[0].Code.Consume(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Verify(context.Background(), consumed, issued[0].Secret); !errors.Is(err, ErrRecoveryInvalid) {
		t.Fatalf("consumed code must not verify, got %v", err)
	}
}

func TestSessionLifecycleCarriesVersionAndClientMetadata(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	session, err := RestoreSession(SessionSnapshot{
		ID:                uuid.New(),
		AdminID:           uuid.New(),
		Selector:          "AAAAAAAAAAAAAAAAAAAAAA",
		SecretMAC:         security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: 1, Value: make([]byte, 32)},
		CSRFHash:          security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: 1, Value: make([]byte, 32)},
		Kind:              SessionKindFull,
		AdminVersion:      2,
		PasswordVersion:   1,
		SessionVersion:    1,
		ClientIP:          "203.0.113.10",
		UserAgent:         "GameNightAdmin/1.0",
		MaxAttempts:       5,
		CreatedAt:         now,
		LastSeenAt:        now,
		IdleExpiresAt:     now.Add(time.Minute),
		AbsoluteExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := session.Snapshot()
	if snapshot.SessionVersion != 1 {
		t.Fatalf("session version = %d, want 1", snapshot.SessionVersion)
	}
	if snapshot.ClientIP != "203.0.113.10" || snapshot.UserAgent != "GameNightAdmin/1.0" {
		t.Fatalf("unexpected client metadata: %+v", snapshot)
	}
	if _, err := RestoreSession(SessionSnapshot{
		ID:                uuid.New(),
		AdminID:           uuid.New(),
		Selector:          "AAAAAAAAAAAAAAAAAAAAAA",
		SecretMAC:         security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: 1, Value: make([]byte, 32)},
		CSRFHash:          security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: 1, Value: make([]byte, 32)},
		Kind:              SessionKindFull,
		AdminVersion:      2,
		PasswordVersion:   1,
		SessionVersion:    0,
		MaxAttempts:       5,
		CreatedAt:         now,
		LastSeenAt:        now,
		IdleExpiresAt:     now.Add(time.Minute),
		AbsoluteExpiresAt: now.Add(time.Hour),
	}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("expected missing session version integrity failure, got %v", err)
	}
}

func TestTOTPWindowReturnsMovingFactorForCAS(t *testing.T) {
	secret := "JBSWY3DPEHPK3PXP"
	now := time.Unix(1_700_000_000, 0).UTC()
	code, err := GenerateTOTPCode(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	step, err := VerifyTOTPCode(secret, code, now.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if step != now.Unix()/int64(TOTPPeriod) {
		t.Fatalf("accepted step %d does not identify previous window", step)
	}
	if _, err := VerifyTOTPCode(secret, code, now.Add(2*time.Minute)); !errors.Is(err, ErrTOTPInvalid) {
		t.Fatalf("expected expired TOTP, got %v", err)
	}
}

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

type testRecoveryHasher struct{}

const (
	testArgonHash              = "$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	testPasswordParametersJSON = `{"MemoryKiB":65536,"Iterations":3,"Parallelism":2,"SaltLength":16,"KeyLength":32}`
)

func (testRecoveryHasher) Hash(_ context.Context, _ []byte) (string, error) {
	return testArgonHash, nil
}

func (testRecoveryHasher) VerifyOrDummy(_ context.Context, encoded string, secret []byte) (bool, bool, error) {
	return encoded == testArgonHash && len(secret) > 0, false, nil
}
