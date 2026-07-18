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
	"github.com/iFTY-R/game-night/platform/ratelimit"
	"github.com/iFTY-R/game-night/platform/ratelimit/ratelimittest"
	"github.com/iFTY-R/game-night/platform/secretresult"
	"github.com/iFTY-R/game-night/platform/security"
)

func TestServiceBootstrapReplaysSameDeviceSecretsAfterResponseLoss(t *testing.T) {
	fixture := newIdentityServiceFixture(t)
	ctx := context.Background()
	begin, err := fixture.service.BeginIdentityBootstrap(ctx, BeginIdentityBootstrapCommand{
		CanonicalOrigin: "https://play.example", RequestFlowID: "flow_bootstrap", ClientIP: "203.0.113.10",
	})
	if err != nil {
		t.Fatal(err)
	}
	operationID := testOperationID(t, 0x31)
	command := BootstrapIdentityCommand{
		CanonicalOrigin: "https://play.example", RequestFlowID: "flow_bootstrap",
		ChallengeCredentials: begin.Credentials, OperationID: operationID,
		ClientIP: "203.0.113.10", DeviceLabel: "Phone",
	}
	first, err := fixture.service.BootstrapIdentity(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.service.BootstrapIdentity(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if first.Operation.ResultID != second.Operation.ResultID || first.DeviceSecrets == nil || second.DeviceSecrets == nil ||
		first.DeviceSecrets.Token() != second.DeviceSecrets.Token() || first.DeviceSecrets.CSRFToken() != second.DeviceSecrets.CSRFToken() ||
		!second.Operation.Replayed {
		t.Fatalf("bootstrap replay changed result or secrets: first=%+v second=%+v", first, second)
	}
	if len(fixture.storage.users) != 1 || len(fixture.storage.devices) != 1 || len(fixture.storage.results) != 1 {
		t.Fatalf("bootstrap duplicated state: users=%d devices=%d results=%d",
			len(fixture.storage.users), len(fixture.storage.devices), len(fixture.storage.results))
	}

	conflict := command
	conflict.DeviceLabel = "Other Phone"
	if _, err := fixture.service.BootstrapIdentity(ctx, conflict); !errors.Is(err, idempotency.ErrConflict) {
		t.Fatalf("conflicting replay error = %v", err)
	}
	resultKey := secretresult.Key{
		Scope: secretresult.ScopeIdentityBootstrap, ActorID: first.User.Snapshot().ID, OperationID: operationID,
	}
	if _, exists := fixture.storage.results[resultKey]; !exists {
		t.Fatal("bootstrap result is not owned by the newly created user")
	}
	confirmed, err := fixture.service.ConfirmSecretReceipt(ctx, ConfirmSecretReceiptCommand{
		DeviceToken: first.DeviceSecrets.Token(), CSRFToken: first.DeviceSecrets.CSRFToken(),
		Operation: IdentitySecretOperationBootstrap, OperationID: operationID, ResultID: first.Operation.ResultID,
	})
	if err != nil || !confirmed.Confirmed || fixture.storage.results[resultKey].Snapshot().Status != secretresult.StatusConfirmed {
		t.Fatalf("bootstrap receipt was not confirmed: result=%#v err=%v", confirmed, err)
	}
}

func TestServiceScheduledRotationReplaysSameGenerationAfterResponseLoss(t *testing.T) {
	fixture := newIdentityServiceFixture(t)
	ctx := context.Background()
	bootstrap := fixture.bootstrap(t, ctx)
	if _, err := fixture.clock.Advance(DeviceRotationInterval); err != nil {
		t.Fatal(err)
	}
	flowID := challenge.RequestFlowID("flow_rotation")
	begin, err := fixture.service.BeginIdentityBootstrap(ctx, BeginIdentityBootstrapCommand{
		CanonicalOrigin: "https://play.example", RequestFlowID: flowID, ClientIP: "203.0.113.21",
	})
	if err != nil {
		t.Fatal(err)
	}
	command := BootstrapIdentityCommand{
		CanonicalOrigin: "https://play.example", RequestFlowID: flowID,
		ChallengeCredentials: begin.Credentials, OperationID: testOperationID(t, 0x35),
		DeviceToken: bootstrap.DeviceSecrets.Token(), CSRFToken: bootstrap.DeviceSecrets.CSRFToken(),
	}
	first, err := fixture.service.BootstrapIdentity(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	wrongSecret := make([]byte, DeviceSecretBytes)
	wrongToken, err := security.FormatToken(
		DeviceTokenVersion, bootstrap.Device.Snapshot().CredentialID.String(), wrongSecret,
	)
	if err != nil {
		t.Fatal(err)
	}
	wrongProof := command
	wrongProof.DeviceToken = wrongToken
	if _, err := fixture.service.BootstrapIdentity(ctx, wrongProof); !errors.Is(err, ErrDeviceAuthentication) {
		t.Fatalf("rotation replay with substituted device token error = %v", err)
	}
	otherDevice, err := fixture.service.devices.Issue(uuid.New(), "Other Phone")
	if err != nil {
		t.Fatal(err)
	}
	wrongProof = command
	wrongProof.CSRFToken = otherDevice.secrets.csrfToken
	if _, err := fixture.service.BootstrapIdentity(ctx, wrongProof); !errors.Is(err, ErrDeviceAuthentication) {
		t.Fatalf("rotation replay with substituted CSRF error = %v", err)
	}
	second, err := fixture.service.BootstrapIdentity(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if first.DeviceSecrets == nil || second.DeviceSecrets == nil || first.DeviceSecrets.Generation() != 2 ||
		first.DeviceSecrets.Token() != second.DeviceSecrets.Token() || first.DeviceSecrets.CSRFToken() != second.DeviceSecrets.CSRFToken() ||
		first.Operation.ResultID != second.Operation.ResultID || !second.Operation.Replayed {
		t.Fatalf("scheduled rotation was not reliably replayed: first=%+v second=%+v", first, second)
	}
	resultKey := secretresult.Key{
		Scope: secretresult.ScopeIdentityBootstrap, ActorID: bootstrap.User.Snapshot().ID, OperationID: command.OperationID,
	}
	if _, exists := fixture.storage.results[resultKey]; !exists {
		t.Fatal("scheduled rotation result is not owned by the authenticated user")
	}
	confirmed, err := fixture.service.ConfirmSecretReceipt(ctx, ConfirmSecretReceiptCommand{
		DeviceToken: second.DeviceSecrets.Token(), CSRFToken: second.DeviceSecrets.CSRFToken(),
		Operation: IdentitySecretOperationBootstrap, OperationID: command.OperationID, ResultID: second.Operation.ResultID,
	})
	if err != nil || !confirmed.Confirmed || fixture.storage.results[resultKey].Snapshot().Status != secretresult.StatusConfirmed {
		t.Fatalf("scheduled rotation receipt was not confirmed: result=%#v err=%v", confirmed, err)
	}
	verification, err := fixture.service.devices.Authenticate(second.Device, bootstrap.DeviceSecrets.Token())
	if err != nil || verification.SecretKind() != DeviceSecretPrevious {
		t.Fatalf("old token did not enter previous-secret grace: kind=%v err=%v", verification.SecretKind(), err)
	}
}

func TestServiceCompleteOnboardingReplaysRecoveryCodeAndLimitsInOrder(t *testing.T) {
	fixture := newIdentityServiceFixture(t)
	ctx := context.Background()
	bootstrap := fixture.bootstrap(t, ctx)
	fixture.limiter = ratelimittest.New()
	fixture.service.limiter = fixture.limiter
	command := CompleteOnboardingCommand{
		DeviceToken: bootstrap.DeviceSecrets.Token(), CSRFToken: bootstrap.DeviceSecrets.CSRFToken(),
		ClientIP: "203.0.113.11", Username: "  Ａlice9  ", OperationID: testOperationID(t, 0x41),
	}
	first, err := fixture.service.CompleteOnboarding(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.service.CompleteOnboarding(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if first.RecoveryCode == "" || first.RecoveryCode != second.RecoveryCode ||
		first.Operation.ResultID != second.Operation.ResultID || !second.Operation.Replayed {
		t.Fatalf("onboarding replay changed one-time result: first=%+v second=%+v", first, second)
	}
	if first.User.Snapshot().Username != "Alice9" || len(fixture.storage.claims) != 1 ||
		len(fixture.storage.recoveries) != 1 || len(fixture.storage.results) != 2 {
		t.Fatalf("unexpected onboarding state: user=%+v claims=%d recovery=%d results=%d",
			first.User.Snapshot(), len(fixture.storage.claims), len(fixture.storage.recoveries), len(fixture.storage.results))
	}
	consumptions := fixture.limiter.Consumptions()
	if len(consumptions) != 6 {
		t.Fatalf("username limiter consumptions = %d, want 6", len(consumptions))
	}
	want := []ratelimit.Dimension{ratelimit.DimensionIP, ratelimit.DimensionDevice, ratelimit.DimensionUsername}
	for index, request := range consumptions {
		if got := request.Bucket().Dimension(); got != want[index%len(want)] {
			t.Fatalf("limiter dimension %d = %s, want %s", index, got, want[index%len(want)])
		}
	}

	conflict := command
	conflict.Username = "Bob9"
	if _, err := fixture.service.CompleteOnboarding(ctx, conflict); !errors.Is(err, idempotency.ErrConflict) {
		t.Fatalf("onboarding digest conflict error = %v", err)
	}
}

func TestServiceLimiterFailureStopsBeforeIdentityWrites(t *testing.T) {
	fixture := newIdentityServiceFixture(t)
	ctx := context.Background()
	policy, _ := ratelimit.PolicyFor(ratelimit.OperationIdentityBootstrap)
	ipKey := testBucketKey(t, ratelimit.DimensionIP, "203.0.113.12")
	requests, _ := policy.Requests(ipKey)
	if err := fixture.limiter.Fail(requests[0], errors.New("redis unavailable")); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.service.BeginIdentityBootstrap(ctx, BeginIdentityBootstrapCommand{
		CanonicalOrigin: "https://play.example", RequestFlowID: "flow_limited", ClientIP: "203.0.113.12",
	})
	if !errors.Is(err, ratelimit.ErrUnavailable) {
		t.Fatalf("bootstrap limiter error = %v", err)
	}
	if fixture.storage.writeCount != 0 || len(fixture.storage.challenges) != 0 || len(fixture.storage.users) != 0 {
		t.Fatalf("limiter failure wrote identity state: writes=%d challenges=%d users=%d",
			fixture.storage.writeCount, len(fixture.storage.challenges), len(fixture.storage.users))
	}
}

func TestServiceUsernameLimiterRejectsOrFailsClosedBeforeClaim(t *testing.T) {
	for _, test := range []struct {
		name      string
		dimension ratelimit.Dimension
		fail      bool
		want      error
	}{
		{name: "device rejected", dimension: ratelimit.DimensionDevice, want: ratelimit.ErrRejected},
		{name: "username dependency unavailable", dimension: ratelimit.DimensionUsername, fail: true, want: ratelimit.ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIdentityServiceFixture(t)
			ctx := context.Background()
			bootstrap := fixture.bootstrap(t, ctx)
			fixture.limiter = ratelimittest.New()
			fixture.service.limiter = fixture.limiter
			credentialID := bootstrap.Device.Snapshot().CredentialID
			policy, _ := ratelimit.PolicyFor(ratelimit.OperationUsernameClaim)
			requests, _ := policy.Requests(
				testBucketKey(t, ratelimit.DimensionIP, "203.0.113.14"),
				testBucketKey(t, ratelimit.DimensionDevice, credentialID.String()),
				testBucketKey(t, ratelimit.DimensionUsername, "alice9"),
			)
			for _, request := range requests {
				if request.Bucket().Dimension() != test.dimension {
					continue
				}
				var configureErr error
				if test.fail {
					configureErr = fixture.limiter.Fail(request, errors.New("redis unavailable"))
				} else {
					configureErr = fixture.limiter.Reject(request, time.Second)
				}
				if configureErr != nil {
					t.Fatal(configureErr)
				}
			}
			writesBefore := fixture.storage.writeCount
			_, err := fixture.service.CompleteOnboarding(ctx, CompleteOnboardingCommand{
				DeviceToken: bootstrap.DeviceSecrets.Token(), CSRFToken: bootstrap.DeviceSecrets.CSRFToken(),
				ClientIP: "203.0.113.14", Username: "Alice9", OperationID: testOperationID(t, 0x55),
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("limiter error = %v, want %v", err, test.want)
			}
			if fixture.storage.writeCount != writesBefore || len(fixture.storage.claims) != 0 || len(fixture.storage.recoveries) != 0 {
				t.Fatalf("username limiter failure wrote state: writes=%d before=%d claims=%d recoveries=%d",
					fixture.storage.writeCount, writesBefore, len(fixture.storage.claims), len(fixture.storage.recoveries))
			}
		})
	}
}

func TestServiceRevokedCredentialStatusInstructionRequiresVerifiedSecret(t *testing.T) {
	for _, test := range []struct {
		name        string
		status      UserStatus
		reason      DeviceRevokeReason
		instruction AccountInstruction
	}{
		{name: "suspended", status: UserStatusSuspended, reason: DeviceRevokeAccountSuspended, instruction: AccountInstructionSuspended},
		{name: "deleted", status: UserStatusDeleted, reason: DeviceRevokeAccountDeleted, instruction: AccountInstructionDeleted},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIdentityServiceFixture(t)
			ctx := context.Background()
			bootstrap := fixture.bootstrap(t, ctx)
			onboard, err := fixture.service.CompleteOnboarding(ctx, CompleteOnboardingCommand{
				DeviceToken: bootstrap.DeviceSecrets.Token(), CSRFToken: bootstrap.DeviceSecrets.CSRFToken(),
				ClientIP: "203.0.113.15", Username: "Alice9", OperationID: testOperationID(t, 0x56),
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.clock.Advance(time.Minute); err != nil {
				t.Fatal(err)
			}
			userSnapshot := onboard.User.Snapshot()
			userSnapshot.Status = test.status
			userSnapshot.UpdatedAt = fixture.clock.Now()
			if test.status == UserStatusDeleted {
				userSnapshot.Username = ""
				userSnapshot.CurrentUsernameKey = ""
			}
			user, err := RestoreUser(userSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			device, err := bootstrap.Device.Revoke(test.reason, fixture.clock.Now())
			if err != nil {
				t.Fatal(err)
			}
			fixture.storage.users[user.Snapshot().ID] = user
			fixture.storage.devices[device.Snapshot().CredentialID] = device

			result, err := fixture.service.GetCurrentIdentity(ctx, GetCurrentIdentityCommand{DeviceToken: bootstrap.DeviceSecrets.Token()})
			if err != nil {
				t.Fatal(err)
			}
			if result.AccountInstruction != test.instruction || result.CredentialInstruction != CredentialInstructionClear {
				t.Fatalf("status result = %+v", result)
			}
			replacement := "A"
			if token := bootstrap.DeviceSecrets.Token(); token[len(token)-1:] == replacement {
				replacement = "B"
			}
			token := bootstrap.DeviceSecrets.Token()
			wrongToken := token[:len(token)-1] + replacement
			if _, err := fixture.service.GetCurrentIdentity(ctx, GetCurrentIdentityCommand{DeviceToken: wrongToken}); !errors.Is(err, ErrDeviceAuthentication) {
				t.Fatalf("wrong secret status inspection error = %v", err)
			}
		})
	}
}

func TestServiceChangeUsernameEnforcesCooldownAndReservesOldClaim(t *testing.T) {
	fixture := newIdentityServiceFixture(t)
	ctx := context.Background()
	bootstrap := fixture.bootstrap(t, ctx)
	onboard, err := fixture.service.CompleteOnboarding(ctx, CompleteOnboardingCommand{
		DeviceToken: bootstrap.DeviceSecrets.Token(), CSRFToken: bootstrap.DeviceSecrets.CSRFToken(),
		ClientIP: "203.0.113.13", Username: "Alice9", OperationID: testOperationID(t, 0x61),
	})
	if err != nil {
		t.Fatal(err)
	}
	command := ChangeUsernameCommand{
		DeviceToken: bootstrap.DeviceSecrets.Token(), CSRFToken: bootstrap.DeviceSecrets.CSRFToken(),
		ClientIP: "203.0.113.13", Username: "Bob9",
	}
	if _, err := fixture.service.ChangeUsername(ctx, command); !errors.Is(err, ErrUsernameChangeCooldown) {
		t.Fatalf("early rename error = %v", err)
	}
	if _, err := fixture.clock.Advance(UsernameChangeCooldown); err != nil {
		t.Fatal(err)
	}
	changed, err := fixture.service.ChangeUsername(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if changed.User.Snapshot().Username != "Bob9" {
		t.Fatalf("changed username = %q", changed.User.Snapshot().Username)
	}
	oldKey := onboard.User.Snapshot().CurrentUsernameKey
	oldClaim := fixture.storage.claims[oldKey].Snapshot()
	if oldClaim.Status != UsernameClaimReserved || !oldClaim.ReservedUntil.Equal(fixture.clock.Now().Add(UsernameReservationTTL)) {
		t.Fatalf("old claim was not reserved: %+v", oldClaim)
	}
}

type identityServiceFixture struct {
	service *Service
	clock   *clock.Fake
	limiter *ratelimittest.Fake
	storage *memoryIdentityStorage
	hasher  *recordingRecoveryHasher
}

func newIdentityServiceFixture(t testing.TB) *identityServiceFixture {
	t.Helper()
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	serviceClock := clock.NewFake(now)
	deviceService := newDeviceServiceWithClockForTest(t, now, serviceClock)
	challengeKeyring, err := security.LoadHMACKeyring[security.UserChallengeKeyPurpose](identityKeyringPath(t, now), now)
	if err != nil {
		t.Fatal(err)
	}
	challengeService, err := NewChallengeService(challengeKeyring, serviceClock)
	if err != nil {
		t.Fatal(err)
	}
	resultKeyring, err := security.LoadAESKeyring[security.ResultEnvelopeKeyPurpose](identityKeyringPath(t, now), now)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secretresult.NewEnvelopeCipher(resultKeyring)
	if err != nil {
		t.Fatal(err)
	}
	resultService, err := secretresult.NewServiceWithIdentityAccess(cipher, serviceClock, deviceService.keyring, challengeKeyring)
	if err != nil {
		t.Fatal(err)
	}
	hasher := &recordingRecoveryHasher{hash: testRecoveryPHC, verifyMatched: true}
	recoveryService, err := NewRecoveryCodeService(hasher)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := identifier.NewUsernameValidator(nil, []string{"blocked"})
	if err != nil {
		t.Fatal(err)
	}
	storage := newMemoryIdentityStorage()
	unitOfWork := &memoryIdentityUnitOfWork{storage: storage}
	limiter := ratelimittest.New()
	attemptService, err := NewRecoveryAttemptService(challengeKeyring, serviceClock)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewServiceWithRecovery(
		challengeService, deviceService, recoveryService, attemptService, resultService,
		unitOfWork, limiter, validator, serviceClock, newIdentityAuditService(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	return &identityServiceFixture{service: service, clock: serviceClock, limiter: limiter, storage: storage, hasher: hasher}
}

func (fixture *identityServiceFixture) bootstrap(t testing.TB, ctx context.Context) BootstrapIdentityResult {
	t.Helper()
	flowID := challenge.RequestFlowID("flow_" + uuid.NewString())
	begin, err := fixture.service.BeginIdentityBootstrap(ctx, BeginIdentityBootstrapCommand{
		CanonicalOrigin: "https://play.example", RequestFlowID: flowID, ClientIP: "203.0.113.20",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := fixture.service.BootstrapIdentity(ctx, BootstrapIdentityCommand{
		CanonicalOrigin: "https://play.example", RequestFlowID: flowID,
		ChallengeCredentials: begin.Credentials, OperationID: testOperationID(t, byte(len(fixture.storage.users)+1)),
		ClientIP: "203.0.113.20", DeviceLabel: "Phone",
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func testOperationID(t testing.TB, marker byte) idempotency.OperationID {
	t.Helper()
	value := make([]byte, 16)
	value[len(value)-1] = marker
	operationID, err := idempotency.NewOperationID(value)
	if err != nil {
		t.Fatal(err)
	}
	return operationID
}

func testBucketKey(t testing.TB, dimension ratelimit.Dimension, raw string) ratelimit.BucketKey {
	t.Helper()
	value, err := ratelimit.NewBucketValue(raw)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ratelimit.NewBucketKey(dimension, value)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
