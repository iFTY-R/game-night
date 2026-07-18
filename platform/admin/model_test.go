package admin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/security"
)

func TestAccountLifecycleAndGeneration(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	account, err := NewBootstrapAccount(uuid.New(), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := account.Transition(AccountStatusActive, now); !errors.Is(err, ErrConcurrentTransition) {
		t.Fatalf("expected guarded bootstrap transition, got %v", err)
	}
	account, err = account.WithPassword("$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", PasswordAlgorithmArgon2id, `{"MemoryKiB":65536,"Iterations":3,"Parallelism":2,"SaltLength":16,"KeyLength":32}`, now)
	if err != nil {
		t.Fatal(err)
	}
	if account.Snapshot().Status != AccountStatusSetupRequired || account.Snapshot().AdminVersion != 2 || account.Snapshot().PasswordVersion != 1 {
		t.Fatalf("unexpected bootstrap account generations: %+v", account.Snapshot())
	}
	account, err = account.Transition(AccountStatusActive, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if account.Snapshot().Status != AccountStatusActive || account.Snapshot().AdminVersion != 3 {
		t.Fatalf("unexpected setup state: %+v", account.Snapshot())
	}
}

func TestPasswordPolicyRejectsWeakAndUsernamePasswords(t *testing.T) {
	policy := DefaultPasswordPolicy()
	for _, candidate := range []string{"short", "admin", "password123"} {
		if !errors.Is(policy.Validate("admin", candidate), ErrPasswordPolicy) {
			t.Fatalf("expected policy rejection for %q", candidate)
		}
	}
	if err := policy.Validate("admin", "correct horse battery staple"); err != nil {
		t.Fatal(err)
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

type testRecoveryHasher struct{}

const testRecoveryHash = "$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func (testRecoveryHasher) Hash(_ context.Context, _ []byte) (string, error) {
	return testRecoveryHash, nil
}
func (testRecoveryHasher) VerifyOrDummy(_ context.Context, encoded string, secret []byte) (bool, bool, error) {
	return encoded == testRecoveryHash && len(secret) > 0, false, nil
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

func TestSessionLifecycleRejectsMalformedGeneration(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	session, err := RestoreSession(SessionSnapshot{
		ID: uuid.New(), AdminID: uuid.New(), Selector: "AAAAAAAAAAAAAAAAAAAAAA", SecretMAC: security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: 1, Value: make([]byte, 32)},
		CSRFHash: security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: 1, Value: make([]byte, 32)}, Kind: SessionKindFull, AdminVersion: 2, PasswordVersion: 1,
		MaxAttempts: 5, CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(time.Minute), AbsoluteExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !session.Active(now) || session.Active(now.Add(time.Minute)) {
		t.Fatal("session expiry boundary is not enforced")
	}
	if _, err := session.Revoke("logout", now); err != nil {
		t.Fatal(err)
	}
}
