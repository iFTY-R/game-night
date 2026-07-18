package challenge

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/security"
)

func TestIssueUsesStrictCookieAndVersionedProof(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 123456789, time.UTC)
	service := newUserChallengeService(t, now, nil)
	binding := testBinding(t, "bootstrap", "identity_api", "https://play.example", "flow_1", SubjectBinding{})

	issued, err := service.Issue(binding, DefaultMaxAttempts)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := issued.Challenge.Snapshot()
	if snapshot.Selector.ByteLength() != SelectorBytes || len(snapshot.SecretMAC.Value) != sha256.Size {
		t.Fatalf("unexpected persisted credentials: selector=%d digest=%d", snapshot.Selector.ByteLength(), len(snapshot.SecretMAC.Value))
	}
	if !snapshot.CreatedAt.Equal(now.UTC().Truncate(time.Microsecond)) || !snapshot.ExpiresAt.Equal(snapshot.CreatedAt.Add(TTL)) {
		t.Fatalf("unexpected challenge times: created=%s expires=%s", snapshot.CreatedAt, snapshot.ExpiresAt)
	}

	cookie, err := security.ParseToken(issued.Credentials.CookieToken, security.TokenPolicy{
		Version: "v1", MinSecretBytes: SecretBytes, MaxSecretBytes: SecretBytes,
	})
	if err != nil || cookie.Selector != snapshot.Selector.Value() || len(cookie.Secret) != SecretBytes {
		t.Fatalf("unexpected cookie token: selector=%q secret=%d err=%v", cookie.Selector, len(cookie.Secret), err)
	}
	proof, err := security.ParseToken(issued.Credentials.BodyProof, security.TokenPolicy{
		Version: "v1", MinSecretBytes: proofMACBytes, MaxSecretBytes: proofMACBytes,
	})
	if err != nil || proof.Selector != "1" || len(proof.Secret) != proofMACBytes {
		t.Fatalf("unexpected proof: version=%q digest=%d err=%v", proof.Selector, len(proof.Secret), err)
	}

	operationID := testOperationID(t, 1)
	authorization, err := service.Authorize(issued.Challenge, binding, issued.Credentials, operationID, testDigest(1))
	if err != nil || authorization.Kind() != AuthorizeFirstUse || authorization.ResultID() != uuid.Nil {
		t.Fatalf("first authorization = %+v, err=%v", authorization, err)
	}
}

func TestVerifyRejectsCredentialAndClaimsSubstitution(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	service := newUserChallengeService(t, now, nil)
	binding := testBinding(t, "bootstrap", "identity_api", "https://play.example", "flow_1", SubjectBinding{})
	issued, err := service.Issue(binding, 3)
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.Issue(binding, 3)
	if err != nil {
		t.Fatal(err)
	}

	wrongPurpose := binding
	wrongPurpose.Purpose = "recovery"
	wrongAudience := binding
	wrongAudience.Audience = "admin_auth_api"
	wrongOrigin := binding
	wrongOrigin.Origin, _ = DigestOrigin("https://other.example")
	wrongFlow := binding
	wrongFlow.RequestFlowID = "flow_2"
	tests := []struct {
		name        string
		expected    Binding
		credentials Credentials
	}{
		{name: "cookie from another challenge", expected: binding, credentials: Credentials{CookieToken: other.Credentials.CookieToken, BodyProof: issued.Credentials.BodyProof}},
		{name: "proof from another challenge", expected: binding, credentials: Credentials{CookieToken: issued.Credentials.CookieToken, BodyProof: other.Credentials.BodyProof}},
		{name: "wrong purpose", expected: wrongPurpose, credentials: issued.Credentials},
		{name: "wrong audience", expected: wrongAudience, credentials: issued.Credentials},
		{name: "wrong origin", expected: wrongOrigin, credentials: issued.Credentials},
		{name: "wrong flow", expected: wrongFlow, credentials: issued.Credentials},
		{name: "missing cookie", expected: binding, credentials: Credentials{BodyProof: issued.Credentials.BodyProof}},
		{name: "missing proof", expected: binding, credentials: Credentials{CookieToken: issued.Credentials.CookieToken}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := service.Verify(issued.Challenge, test.expected, test.credentials); !errors.Is(err, ErrAuthentication) {
				t.Fatalf("verify error = %v, want ErrAuthentication", err)
			}
		})
	}

	wrongKeyService := newUserChallengeService(t, now, nil)
	if err := wrongKeyService.Verify(issued.Challenge, binding, issued.Credentials); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("cross-keyring verify error = %v, want ErrAuthentication", err)
	}
}

func TestChallengeTTLAndAttemptStates(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	service := newUserChallengeService(t, now, nil)
	binding := testBinding(t, "bootstrap", "identity_api", "https://play.example", "flow_1", SubjectBinding{})
	issued, err := service.Issue(binding, 3)
	if err != nil {
		t.Fatal(err)
	}
	if state := issued.Challenge.State(now.Add(TTL - time.Microsecond)); state != StateActive {
		t.Fatalf("state before expiry = %v, want active", state)
	}
	if state := issued.Challenge.State(now.Add(TTL)); state != StateExpired {
		t.Fatalf("state at expiry = %v, want expired", state)
	}

	current := issued.Challenge
	for attempt := uint32(1); attempt <= 3; attempt++ {
		current, err = current.RecordFailure(now.Add(time.Duration(attempt) * time.Second))
		if err != nil {
			t.Fatalf("record attempt %d: %v", attempt, err)
		}
		if got := current.Snapshot().AttemptCount; got != attempt {
			t.Fatalf("attempt count = %d, want %d", got, attempt)
		}
	}
	if state := current.State(now.Add(4 * time.Second)); state != StateExhausted {
		t.Fatalf("state after max attempts = %v, want exhausted", state)
	}
	if _, err := current.RecordFailure(now.Add(5 * time.Second)); !errors.Is(err, ErrConcurrentTransition) {
		t.Fatalf("extra failure error = %v, want ErrConcurrentTransition", err)
	}
	if state := current.State(now.Add(TTL)); state != StateExhausted {
		t.Fatalf("terminal exhausted state after TTL = %v, want exhausted", state)
	}
}

func TestRestorePreservesPersistedExpiredTerminalState(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	service := newUserChallengeService(t, now, nil)
	binding := testBinding(t, "bootstrap", "identity_api", "https://play.example", "flow_1", SubjectBinding{})
	issued, err := service.Issue(binding, 3)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := issued.Challenge.Snapshot()
	snapshot.PersistedState = StateExpired
	restored, err := Restore(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if state := restored.State(now); state != StateExpired {
		t.Fatalf("persisted state = %v, want expired", state)
	}
	if err := service.Verify(restored, binding, issued.Credentials); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("verify persisted expired challenge error = %v", err)
	}

	snapshot.PersistedState = StateRevoked
	if _, err := Restore(snapshot); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unsupported persisted state error = %v", err)
	}
}

func TestConsumeAllowsOnlyExactReplayWithinWindow(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	fakeClock := clock.NewFake(now)
	service := newUserChallengeServiceWithClock(t, fakeClock, now, nil)
	binding := testBinding(t, "recovery", "identity_api", "https://play.example", "flow_recovery", SubjectBinding{})
	issued, err := service.Issue(binding, 3)
	if err != nil {
		t.Fatal(err)
	}
	operationID := testOperationID(t, 7)
	digest := testDigest(7)
	resultID := uuid.New()
	consumedAt, _ := fakeClock.Advance(time.Minute)
	consumed, err := issued.Challenge.Consume(consumedAt, ReplayAuthorization{
		OperationID: operationID, RequestDigest: digest, ResultID: resultID,
		ReplayUntil: consumedAt.Add(8 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if issued.Challenge.State(consumedAt) != StateActive || consumed.State(consumedAt) != StateConsumed {
		t.Fatal("consume must return a new aggregate without mutating the prior value")
	}
	if _, err := consumed.Consume(consumedAt, *consumed.Snapshot().Replay); !errors.Is(err, ErrConcurrentTransition) {
		t.Fatalf("second consume error = %v, want ErrConcurrentTransition", err)
	}

	authorization, err := service.Authorize(consumed, binding, issued.Credentials, operationID, digest)
	if err != nil || authorization.Kind() != AuthorizeExactReplay || authorization.ResultID() != resultID ||
		!AuthorizesReplay(authorization, resultID, consumedAt) || AuthorizesReplay(authorization, uuid.New(), consumedAt) ||
		AuthorizesReplay(authorization, resultID, consumedAt.Add(8*time.Minute)) {
		t.Fatalf("exact replay = %+v, err=%v", authorization, err)
	}
	if _, err := service.Authorize(consumed, binding, issued.Credentials, operationID, testDigest(8)); !errors.Is(err, idempotency.ErrConflict) {
		t.Fatalf("digest conflict error = %v, want ErrIdempotencyConflict", err)
	}
	if _, err := service.Authorize(consumed, binding, issued.Credentials, testOperationID(t, 8), digest); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("operation mismatch error = %v, want ErrUnavailable", err)
	}

	_, _ = fakeClock.Advance(8 * time.Minute)
	if _, err := service.Authorize(consumed, binding, issued.Credentials, operationID, digest); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("replay at window boundary error = %v, want ErrUnavailable", err)
	}
}

func TestConsumeWithoutReplayCannotAuthorizeRetry(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	service := newUserChallengeService(t, now, nil)
	binding := testBinding(t, "recovery", "identity_api", "https://play.example", "flow_recovery", SubjectBinding{})
	issued, err := service.Issue(binding, 3)
	if err != nil {
		t.Fatal(err)
	}
	consumed, err := issued.Challenge.ConsumeWithoutReplay(now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := consumed.Snapshot()
	if consumed.State(now.Add(time.Minute)) != StateConsumed || snapshot.Replay != nil {
		t.Fatalf("unexpected no-result consumption: state=%v replay=%+v", consumed.State(now.Add(time.Minute)), snapshot.Replay)
	}
	if _, err := Restore(snapshot); err != nil {
		t.Fatalf("restore consumed challenge without replay: %v", err)
	}
	if _, err := service.Authorize(
		consumed, binding, issued.Credentials, testOperationID(t, 9), testDigest(9),
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("no-result retry error = %v, want ErrUnavailable", err)
	}
}

func TestAdminSubjectClaimsAreAuthenticated(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	service := newAdminChallengeService(t, now, nil)
	adminID := uuid.New()
	binding := testBinding(t, "mfa", "admin_auth_api", "https://admin.example", "flow_mfa", SubjectBinding{
		ID: adminID, Version: 4, CredentialVersion: 2,
	})
	issued, err := service.Issue(binding, 3)
	if err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*Binding){
		"admin id":         func(value *Binding) { value.Subject.ID = uuid.New() },
		"admin version":    func(value *Binding) { value.Subject.Version++ },
		"password version": func(value *Binding) { value.Subject.CredentialVersion++ },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := binding
			mutate(&candidate)
			if err := service.Verify(issued.Challenge, candidate, issued.Credentials); !errors.Is(err, ErrAuthentication) {
				t.Fatalf("verify error = %v, want ErrAuthentication", err)
			}
		})
	}
}

func newUserChallengeService(t testing.TB, now time.Time, key []byte) *Service[security.UserChallengeKeyPurpose] {
	t.Helper()
	return newUserChallengeServiceWithClock(t, clock.NewFake(now), now, key)
}

func newUserChallengeServiceWithClock(
	t testing.TB,
	source clock.Clock,
	now time.Time,
	key []byte,
) *Service[security.UserChallengeKeyPurpose] {
	t.Helper()
	if key == nil {
		key = randomBytes(t, 32)
	}
	keyring, err := security.LoadHMACKeyring[security.UserChallengeKeyPurpose](writeKeyring(t, now, key), now)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(keyring, source)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func newAdminChallengeService(t testing.TB, now time.Time, key []byte) *Service[security.AdminChallengeKeyPurpose] {
	t.Helper()
	if key == nil {
		key = randomBytes(t, 32)
	}
	keyring, err := security.LoadHMACKeyring[security.AdminChallengeKeyPurpose](writeKeyring(t, now, key), now)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(keyring, clock.NewFake(now))
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func testBinding(
	t testing.TB,
	purpose Purpose,
	audience Audience,
	origin string,
	flow RequestFlowID,
	subject SubjectBinding,
) Binding {
	t.Helper()
	originDigest, err := DigestOrigin(origin)
	if err != nil {
		t.Fatal(err)
	}
	return Binding{Purpose: purpose, Audience: audience, Origin: originDigest, RequestFlowID: flow, Subject: subject}
}

func testOperationID(t testing.TB, marker byte) idempotency.OperationID {
	t.Helper()
	value := make([]byte, 16)
	for index := range value {
		value[index] = marker
	}
	operationID, err := idempotency.NewOperationID(value)
	if err != nil {
		t.Fatal(err)
	}
	return operationID
}

func testDigest(marker byte) idempotency.Digest {
	return sha256.Sum256([]byte{marker})
}

func randomBytes(t testing.TB, length int) []byte {
	t.Helper()
	value := make([]byte, length)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	return value
}

func writeKeyring(t testing.TB, now time.Time, key []byte) string {
	t.Helper()
	document := struct {
		ActiveVersion uint32 `json:"active_version"`
		Keys          []struct {
			Version   uint32    `json:"version"`
			Key       string    `json:"key"`
			NotBefore time.Time `json:"not_before"`
		} `json:"keys"`
	}{ActiveVersion: 1}
	document.Keys = append(document.Keys, struct {
		Version   uint32    `json:"version"`
		Key       string    `json:"key"`
		NotBefore time.Time `json:"not_before"`
	}{Version: 1, Key: base64.StdEncoding.EncodeToString(key), NotBefore: now.Add(-time.Hour)})
	contents, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "challenge-keyring.json")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	return path
}
