package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/security"
)

func TestRecoveryCodeVerifyUsesRealAndDummyArgon2Paths(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	record := mustRecoveryCredential(t, validRecoverySnapshot(t, now))
	issuedHasher := &recordingRecoveryHasher{hash: testRecoveryPHC}
	issuer, err := NewRecoveryCodeService(issuedHasher)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := issuer.Issue(context.Background(), record.Snapshot().UserID, now)
	if err != nil {
		t.Fatal(err)
	}

	verifier := &recordingRecoveryHasher{verifyMatched: true}
	service, err := NewRecoveryCodeService(verifier)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.VerifyOrDummy(context.Background(), &issued.Credential, issued.Code); err != nil {
		t.Fatal(err)
	}
	if verifier.verifyHash != testRecoveryPHC || verifier.verifyCallCount != 1 || len(verifier.verifyInput) == 0 {
		t.Fatalf("real verification path was not used: hash=%q calls=%d input=%d", verifier.verifyHash, verifier.verifyCallCount, len(verifier.verifyInput))
	}

	verifier.verifyMatched = false
	if err := service.VerifyOrDummy(context.Background(), nil, issued.Code); !errors.Is(err, ErrRecoveryInvalid) {
		t.Fatalf("unknown selector must collapse to recovery invalid, got %v", err)
	}
	if verifier.verifyHash != "" || verifier.verifyCallCount != 2 || len(verifier.verifyInput) == 0 {
		t.Fatalf("dummy verification path was not used: hash=%q calls=%d input=%d", verifier.verifyHash, verifier.verifyCallCount, len(verifier.verifyInput))
	}
	if err := service.VerifyOrDummy(context.Background(), nil, "bad-token"); !errors.Is(err, ErrRecoveryInvalid) {
		t.Fatalf("malformed token must collapse to recovery invalid, got %v", err)
	}
	if verifier.verifyCallCount != 3 {
		t.Fatalf("malformed token skipped dummy Argon2: calls=%d", verifier.verifyCallCount)
	}
}

func TestRecoveryAttemptBindsChallengeOriginSourceAndDigest(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	serviceClock := clock.NewFake(now)
	keyring, err := security.LoadHMACKeyring[security.UserChallengeKeyPurpose](identityKeyringPath(t, now), now)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewRecoveryAttemptService(keyring, serviceClock)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := challenge.DigestOrigin("https://play.example")
	if err != nil {
		t.Fatal(err)
	}
	digest := idempotency.Digest{1, 2, 3}
	binding := RecoveryAttemptBinding{
		UserID: uuid.New(), ChallengeID: uuid.New(), Origin: origin,
		RecoveryCredentialID: uuid.New(), RecoveryCredentialVersion: 7,
	}
	prebound := binding
	prebound.RequestDigestSet = true
	prebound.RequestDigest = digest
	if _, err := service.Issue(prebound); !errors.Is(err, ErrInvalidRecoveryAttempt) {
		t.Fatalf("Begin accepted a digest before Complete: %v", err)
	}
	issued, err := service.Issue(binding)
	if err != nil {
		t.Fatal(err)
	}
	if issued.Attempt.Snapshot().GrantMAC.KeyVersion == 0 || issued.ExpiresAt != now.Add(RecoveryAttemptTTL) {
		t.Fatalf("unexpected issued attempt: %#v", issued.Attempt.Snapshot())
	}
	authorization, err := service.Authorize(issued.Attempt, issued.Grant, origin, digest)
	if err != nil || !authorization.AllowsFirstUse(issued.Attempt) {
		t.Fatalf("expected first-use authorization, auth=%#v err=%v", authorization, err)
	}

	wrongOrigin, _ := challenge.DigestOrigin("https://other.example")
	if _, err := service.Authorize(issued.Attempt, issued.Grant, wrongOrigin, digest); !errors.Is(err, ErrRecoveryInvalid) {
		t.Fatalf("wrong origin was accepted: %v", err)
	}
	otherAuthorization, err := service.Authorize(issued.Attempt, issued.Grant, origin, idempotency.Digest{9})
	if err != nil || !otherAuthorization.AllowsFirstUse(issued.Attempt) {
		t.Fatalf("Begin unexpectedly bound the first Complete digest: auth=%#v err=%v", otherAuthorization, err)
	}

	mutated := issued.Attempt.Snapshot()
	mutated.Binding.RecoveryCredentialVersion++
	restored, err := RestoreRecoveryAttempt(mutated)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authorize(restored, issued.Grant, origin, digest); !errors.Is(err, ErrRecoveryInvalid) {
		t.Fatalf("source-version substitution was accepted: %v", err)
	}
	consumed, err := issued.Attempt.Consume(authorization, uuid.New(), digest, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authorize(consumed, issued.Grant, origin, idempotency.Digest{9}); !errors.Is(err, idempotency.ErrConflict) {
		t.Fatalf("consumed attempt accepted a different request digest: %v", err)
	}
	invalidConsumed := consumed.Snapshot()
	invalidConsumed.Binding.RequestDigestSet = false
	if _, err := RestoreRecoveryAttempt(invalidConsumed); !errors.Is(err, ErrInvalidRecoveryAttempt) {
		t.Fatalf("consumed attempt restored without a bound digest: %v", err)
	}
}

func TestRecoveryAttemptAttemptLimitAndSingleConsumption(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	serviceClock := clock.NewFake(now)
	keyring, err := security.LoadHMACKeyring[security.UserChallengeKeyPurpose](identityKeyringPath(t, now), now)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewRecoveryAttemptService(keyring, serviceClock)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := challenge.DigestOrigin("https://play.example")
	binding := RecoveryAttemptBinding{
		UserID: uuid.New(), ChallengeID: uuid.New(), Origin: origin,
		AssistedGrantID: uuid.New(),
	}
	issued, err := service.Issue(binding)
	if err != nil {
		t.Fatal(err)
	}
	current := issued.Attempt
	for attempt := uint32(0); attempt < RecoveryAttemptMaxAttempts; attempt++ {
		current, err = current.RecordFailure(now.Add(time.Duration(attempt) * time.Microsecond))
		if err != nil {
			t.Fatal(err)
		}
	}
	digest := idempotency.Digest{4}
	if _, err := service.Authorize(current, issued.Grant, origin, digest); !errors.Is(err, ErrRecoveryInvalid) {
		t.Fatalf("attempt limit did not close grant: %v", err)
	}

	authorization, err := service.Authorize(issued.Attempt, issued.Grant, origin, digest)
	if err != nil {
		t.Fatal(err)
	}
	resultID := uuid.New()
	consumed, err := issued.Attempt.Consume(authorization, resultID, digest, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if consumed.Snapshot().Status != RecoveryAttemptConsumed || consumed.Snapshot().ResultID != resultID {
		t.Fatalf("attempt was not consumed: %#v", consumed.Snapshot())
	}
	if _, err := consumed.Consume(authorization, uuid.New(), digest, now.Add(2*time.Second)); !errors.Is(err, ErrRecoveryConcurrentTransition) {
		t.Fatalf("consumed attempt advanced twice: %v", err)
	}
}

func TestAssistedRecoveryGrantStaysActiveUntilComplete(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	selector, err := identifier.NewSelector(make([]byte, AssistedRecoverySelectorBytes))
	if err != nil {
		t.Fatal(err)
	}
	record, err := RestoreAssistedRecoveryGrant(AssistedRecoveryGrantSnapshot{
		ID: uuid.New(), UserID: uuid.New(), Selector: selector, SecretHash: testRecoveryPHC,
		Purpose: AssistedRecoveryPurpose, Status: AssistedRecoveryGrantActive, MaxAttempts: AssistedRecoveryMaxAttempts,
		CreatedByAdminID: uuid.New(), CreatedAt: now, ExpiresAt: now.Add(AssistedRecoveryTTL),
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.State(now.Add(time.Minute)) != AssistedRecoveryGrantActive {
		t.Fatal("Begin-equivalent read changed assisted grant state")
	}
	resultID := uuid.New()
	consumed, err := record.Consume(resultID, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if consumed.Snapshot().Status != AssistedRecoveryGrantConsumed || consumed.Snapshot().ResultID != resultID {
		t.Fatalf("assisted grant was not consumed by Complete: %#v", consumed.Snapshot())
	}
}

func mustRecoveryCredential(t testing.TB, snapshot RecoveryCredentialSnapshot) RecoveryCredential {
	t.Helper()
	record, err := RestoreRecoveryCredential(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return record
}
