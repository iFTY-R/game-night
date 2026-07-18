package identity

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/secretaccess"
	"github.com/iFTY-R/game-night/platform/security"
)

func TestDeviceCredentialIssueUsesVersionedHighEntropySecretsAndFixedExpiry(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	service := newDeviceServiceForTest(t, now)
	userID := uuid.New()

	first, err := service.Issue(userID, "  Alice's Phone  ")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Issue(userID, "Tablet")
	if err != nil {
		t.Fatal(err)
	}
	if first.secrets.token == second.secrets.token || first.secrets.csrfToken == second.secrets.csrfToken {
		t.Fatal("independent device issues reused secret material")
	}

	snapshot := first.Credential.Snapshot()
	if snapshot.CredentialID.Version() != 7 || snapshot.UserID != userID || snapshot.Generation != 1 {
		t.Fatalf("unexpected issued identity: id=%s version=%d user=%s generation=%d",
			snapshot.CredentialID, snapshot.CredentialID.Version(), snapshot.UserID, snapshot.Generation)
	}
	if snapshot.Label != "Alice's Phone" {
		t.Fatalf("label = %q", snapshot.Label)
	}
	if snapshot.SecretMAC.KeyVersion == 0 || len(snapshot.SecretMAC.Value) != 32 || len(snapshot.CSRFMAC) != 32 {
		t.Fatalf("invalid persisted MAC metadata: version=%d secret=%d csrf=%d",
			snapshot.SecretMAC.KeyVersion, len(snapshot.SecretMAC.Value), len(snapshot.CSRFMAC))
	}
	if !snapshot.IdleExpiresAt.Equal(now.Add(DeviceIdleTTL)) ||
		!snapshot.AbsoluteExpiresAt.Equal(now.Add(DeviceAbsoluteTTL)) {
		t.Fatalf("unexpected expiry: idle=%s absolute=%s", snapshot.IdleExpiresAt, snapshot.AbsoluteExpiresAt)
	}
	if !strings.HasPrefix(first.secrets.token, DeviceTokenVersion+"."+snapshot.CredentialID.String()+".") {
		t.Fatalf("device token has unexpected format")
	}
	parsed, err := security.ParseToken(first.secrets.token, security.TokenPolicy{
		Version: DeviceTokenVersion, MinSecretBytes: DeviceSecretBytes, MaxSecretBytes: DeviceSecretBytes,
	})
	if err != nil || len(parsed.Secret) != DeviceSecretBytes {
		t.Fatalf("parse issued token: secret=%d err=%v", len(parsed.Secret), err)
	}
	clear(parsed.Secret)
	if err := service.VerifyCSRF(first.Credential, first.secrets.csrfToken); err != nil {
		t.Fatalf("verify issued CSRF secret: %v", err)
	}
}

func TestDeviceRotationPreservesTwoMinutePreviousSecretWithoutCookieWriteAuthority(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	serviceClock := clock.NewFake(now)
	service := newDeviceServiceWithClockForTest(t, now, serviceClock)
	issued, err := service.Issue(uuid.New(), "Phone")
	if err != nil {
		t.Fatal(err)
	}
	currentAuthorization, err := service.Authenticate(issued.Credential, issued.secrets.token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Rotate(issued.Credential, currentAuthorization, issued.secrets.csrfToken); !errors.Is(err, ErrDeviceRotationNotDue) {
		t.Fatalf("early rotation error = %v", err)
	}

	if _, err := serviceClock.Advance(DeviceRotationInterval); err != nil {
		t.Fatal(err)
	}
	rotated, err := service.Rotate(issued.Credential, currentAuthorization, issued.secrets.csrfToken)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Credential.Snapshot().Generation != 2 || rotated.secrets.generation != 2 {
		t.Fatalf("rotated generation = %d/%d", rotated.Credential.Snapshot().Generation, rotated.secrets.generation)
	}
	if rotated.secrets.csrfToken != issued.secrets.csrfToken {
		t.Fatal("scheduled rotation replaced the CSRF secret needed by previous-generation continuations")
	}
	if currentAuthorization.AllowsCookieWrite(rotated.Credential) || currentAuthorization.AllowsSensitiveMutation(rotated.Credential) {
		t.Fatal("authorization from the old generation remained write-capable after rotation")
	}

	previousAuthorization, err := service.Authenticate(rotated.Credential, issued.secrets.token)
	if err != nil || previousAuthorization.SecretKind() != DeviceSecretPrevious {
		t.Fatalf("previous-secret authorization = %v, err=%v", previousAuthorization.SecretKind(), err)
	}
	if previousAuthorization.AllowsCookieWrite(rotated.Credential) || previousAuthorization.AllowsSensitiveMutation(rotated.Credential) {
		t.Fatal("previous secret received mutation or Cookie write authority")
	}
	currentAuthorization, err = service.Authenticate(rotated.Credential, rotated.secrets.token)
	if err != nil || !currentAuthorization.AllowsCookieWrite(rotated.Credential) ||
		!currentAuthorization.AllowsSensitiveMutation(rotated.Credential) {
		t.Fatalf("current rotated secret is not write-capable: auth=%+v err=%v", currentAuthorization, err)
	}

	if _, err := serviceClock.Advance(DevicePreviousSecretGrace); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(rotated.Credential, issued.secrets.token); !errors.Is(err, ErrDeviceAuthentication) {
		t.Fatalf("previous secret at grace boundary error = %v", err)
	}
}

func TestDeviceCookieWriteRequiresExactPersistedGenerationAndSecrets(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	serviceClock := clock.NewFake(now)
	service := newDeviceServiceWithClockForTest(t, now, serviceClock)
	issued, err := service.Issue(uuid.New(), "Phone")
	if err != nil {
		t.Fatal(err)
	}
	write, err := service.cookieWrite(issued.Credential, issued.secrets)
	if err != nil {
		t.Fatal(err)
	}
	if write.Token() != issued.secrets.token || write.CSRFToken() != issued.secrets.csrfToken || write.Generation() != 1 {
		t.Fatal("matching persisted credential produced incorrect Cookie authority")
	}

	if _, err := serviceClock.Advance(DeviceRotationInterval); err != nil {
		t.Fatal(err)
	}
	authorization, err := service.Authenticate(issued.Credential, issued.secrets.token)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := service.Rotate(issued.Credential, authorization, issued.secrets.csrfToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.cookieWrite(rotated.Credential, issued.secrets); !errors.Is(err, ErrDeviceConcurrentTransition) {
		t.Fatalf("G1 Cookie replay against G2 error = %v", err)
	}
	if _, err := service.cookieWrite(rotated.Credential, rotated.secrets); err != nil {
		t.Fatalf("matching G2 Cookie authority: %v", err)
	}

	casLoser := rotated.secrets
	casLoser.token = issued.secrets.token
	if _, err := service.cookieWrite(rotated.Credential, casLoser); !errors.Is(err, ErrDeviceConcurrentTransition) {
		t.Fatalf("CAS-loser token error = %v", err)
	}
	tamperedCSRF := rotated.secrets
	replacement := "A"
	if rotated.secrets.csrfToken[len(rotated.secrets.csrfToken)-1:] == replacement {
		replacement = "B"
	}
	tamperedCSRF.csrfToken = rotated.secrets.csrfToken[:len(rotated.secrets.csrfToken)-1] + replacement
	if _, err := service.cookieWrite(rotated.Credential, tamperedCSRF); !errors.Is(err, ErrDeviceConcurrentTransition) {
		t.Fatalf("mismatched CSRF error = %v", err)
	}
}

func TestDeviceIdleTouchIsCappedByAbsoluteExpiry(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	serviceClock := clock.NewFake(now)
	service := newDeviceServiceWithClockForTest(t, now, serviceClock)
	issued, err := service.Issue(uuid.New(), "Phone")
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := service.Authenticate(issued.Credential, issued.secrets.token)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := serviceClock.Advance(170 * 24 * time.Hour); err != nil {
		t.Fatal(err)
	}
	touched, err := issued.Credential.Touch(authorization, serviceClock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := serviceClock.Advance(170 * 24 * time.Hour); err != nil {
		t.Fatal(err)
	}
	authorization, err = service.Authenticate(touched, issued.secrets.token)
	if err != nil {
		t.Fatal(err)
	}
	touched, err = touched.Touch(authorization, serviceClock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !touched.Snapshot().IdleExpiresAt.Equal(issued.Credential.Snapshot().AbsoluteExpiresAt) {
		t.Fatalf("idle expiry was not capped at absolute expiry: %s", touched.Snapshot().IdleExpiresAt)
	}
	if touched.State(touched.Snapshot().AbsoluteExpiresAt) != DeviceStateExpired {
		t.Fatal("credential remained active at the absolute expiry boundary")
	}
}

func TestDeviceResultCapabilityBindsExactResultActorAndExpiry(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	service := newDeviceServiceForTest(t, now)
	issued, err := service.Issue(uuid.New(), "Phone")
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := service.Authenticate(issued.Credential, issued.secrets.token)
	if err != nil {
		t.Fatal(err)
	}
	resultID := uuid.New()
	capability, err := service.resultCapability(authorization, issued.Credential, resultID, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	actorID := issued.Credential.Snapshot().UserID
	if !secretaccess.VerifyDeviceGrant(service.keyring, capability, resultID, actorID, now) ||
		secretaccess.VerifyDeviceGrant(service.keyring, capability, uuid.New(), actorID, now) ||
		secretaccess.VerifyDeviceGrant(service.keyring, capability, resultID, uuid.New(), now) ||
		secretaccess.VerifyDeviceGrant(service.keyring, capability, resultID, actorID, now.Add(time.Minute)) {
		t.Fatal("device result capability did not enforce exact result, actor, and half-open expiry")
	}
}

func TestPreviousSecretCanOnlyContinueExactResultCommittedBeforeRotation(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	serviceClock := clock.NewFake(now)
	service := newDeviceServiceWithClockForTest(t, now, serviceClock)
	issued, err := service.Issue(uuid.New(), "Phone")
	if err != nil {
		t.Fatal(err)
	}
	currentAuthorization, err := service.Authenticate(issued.Credential, issued.secrets.token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := serviceClock.Advance(DeviceRotationInterval); err != nil {
		t.Fatal(err)
	}
	rotated, err := service.Rotate(issued.Credential, currentAuthorization, issued.secrets.csrfToken)
	if err != nil {
		t.Fatal(err)
	}
	previousAuthorization, err := service.Authenticate(rotated.Credential, issued.secrets.token)
	if err != nil {
		t.Fatal(err)
	}
	resultID := uuid.New()
	actorID := issued.Credential.Snapshot().UserID
	capability, err := service.resultContinuationCapability(
		previousAuthorization, rotated.Credential, resultID,
		serviceClock.Now().Add(-time.Minute), serviceClock.Now().Add(5*time.Minute),
	)
	if err != nil || !secretaccess.VerifyDeviceGrant(service.keyring, capability, resultID, actorID, serviceClock.Now()) {
		t.Fatalf("exact previous-generation continuation was rejected: %v", err)
	}
	if _, err := service.resultContinuationCapability(
		previousAuthorization, rotated.Credential, uuid.New(),
		serviceClock.Now(), serviceClock.Now().Add(5*time.Minute),
	); !errors.Is(err, ErrDeviceAuthentication) {
		t.Fatalf("at-rotation result continuation error = %v", err)
	}
	if _, err := service.resultContinuationCapability(
		previousAuthorization, rotated.Credential, uuid.New(),
		serviceClock.Now().Add(time.Microsecond), serviceClock.Now().Add(5*time.Minute),
	); !errors.Is(err, ErrDeviceAuthentication) {
		t.Fatalf("post-rotation result continuation error = %v", err)
	}
}

func TestDeviceRejectsClockRollbackAndImpossiblePersistedChronology(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	service := newDeviceServiceForTest(t, now)
	issued, err := service.Issue(uuid.New(), "Phone")
	if err != nil {
		t.Fatal(err)
	}
	if issued.Credential.State(now.Add(-time.Microsecond)) != DeviceStateExpired {
		t.Fatal("credential became active before its creation timestamp")
	}
	authorization, err := service.Authenticate(issued.Credential, issued.secrets.token)
	if err != nil {
		t.Fatal(err)
	}
	touched, err := issued.Credential.Touch(authorization, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if touched.State(now.Add(30*time.Second)) != DeviceStateExpired {
		t.Fatal("credential remained active while the clock was behind its last activity")
	}
	if _, err := service.Authenticate(touched, issued.secrets.token); !errors.Is(err, ErrDeviceUnavailable) {
		t.Fatalf("clock-rollback authentication error = %v", err)
	}
	if _, err := touched.Revoke(DeviceRevokeUserRequested, now.Add(30*time.Second)); !errors.Is(err, ErrDeviceConcurrentTransition) {
		t.Fatalf("clock rollback revoke error = %v", err)
	}
	revoked, err := issued.Credential.Revoke(DeviceRevokeAccountSuspended, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if revoked.State(now.Add(-time.Microsecond)) != DeviceStateExpired {
		t.Fatal("revoked credential disclosed state before its creation timestamp")
	}
	if revoked.State(now.Add(30*time.Second)) != DeviceStateExpired {
		t.Fatal("revoked credential disclosed state before its revocation timestamp")
	}
	invalid := issued.Credential.Snapshot()
	invalid.LastSeenAt = invalid.AbsoluteExpiresAt
	invalid.IdleExpiresAt = invalid.AbsoluteExpiresAt
	if _, err := RestoreDeviceCredential(invalid); !errors.Is(err, ErrInvalidDeviceInput) {
		t.Fatalf("impossible persisted chronology error = %v", err)
	}
	invalid = touched.Snapshot()
	invalid.RevokedAt = now.Add(30 * time.Second)
	invalid.RevokeReason = DeviceRevokeUserRequested
	if _, err := RestoreDeviceCredential(invalid); !errors.Is(err, ErrInvalidDeviceInput) {
		t.Fatalf("revocation before last activity error = %v", err)
	}
}

func TestRevokedDeviceOnlyDisclosesAccountStateAfterSecretVerification(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	service := newDeviceServiceForTest(t, now)
	issued, err := service.Issue(uuid.New(), "Phone")
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		reason      DeviceRevokeReason
		instruction AccountInstruction
	}{
		{reason: DeviceRevokeAccountSuspended, instruction: AccountInstructionSuspended},
		{reason: DeviceRevokeAccountDeleted, instruction: AccountInstructionDeleted},
		{reason: DeviceRevokeUserRequested, instruction: AccountInstructionNone},
	} {
		t.Run(string(test.reason), func(t *testing.T) {
			revoked, err := issued.Credential.Revoke(test.reason, now)
			if err != nil {
				t.Fatal(err)
			}
			verification, err := service.Verify(revoked, issued.secrets.token)
			if err != nil {
				t.Fatal(err)
			}
			if got := verification.AccountInstruction(); got != test.instruction {
				t.Fatalf("instruction = %v, want %v", got, test.instruction)
			}
			if _, err := service.Authenticate(revoked, issued.secrets.token); !errors.Is(err, ErrDeviceUnavailable) {
				t.Fatalf("ordinary authentication error = %v", err)
			}
		})
	}

	unknownToken := DeviceTokenVersion + "." + issued.Credential.Snapshot().CredentialID.String() + "." + strings.Repeat("A", 43)
	if _, err := service.Verify(issued.Credential, unknownToken); !errors.Is(err, ErrDeviceAuthentication) {
		t.Fatalf("wrong secret verification error = %v", err)
	}
}

func newDeviceServiceForTest(t testing.TB, keyringTime time.Time) *DeviceService {
	t.Helper()
	return newDeviceServiceWithClockForTest(t, keyringTime, clock.NewFake(keyringTime))
}

func newDeviceServiceWithClockForTest(t testing.TB, keyringTime time.Time, source clock.Clock) *DeviceService {
	t.Helper()
	keyring, err := security.LoadHMACKeyring[security.DeviceHMACKeyPurpose](identityKeyringPath(t, keyringTime), keyringTime)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewDeviceService(keyring, source)
	if err != nil {
		t.Fatal(err)
	}
	return service
}
