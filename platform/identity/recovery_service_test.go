package identity

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/ratelimit"
	"github.com/iFTY-R/game-night/platform/secretresult"
	"github.com/iFTY-R/game-night/platform/security"
)

func TestServiceRecoveryRoundTripReplaysAndConsumesInOrder(t *testing.T) {
	fixture := newIdentityServiceFixture(t)
	ctx := context.Background()
	bootstrap, onboarding := fixture.onboard(t, ctx, "recover_me")
	flowID := challenge.RequestFlowID("flow_recovery_round_trip")
	beginChallenge, err := fixture.service.BeginRecoveryChallenge(ctx, BeginRecoveryChallengeCommand{
		CanonicalOrigin: "https://play.example", RequestFlowID: flowID,
	})
	if err != nil {
		t.Fatal(err)
	}
	beforeLimits := len(fixture.limiter.Consumptions())
	begin, err := fixture.service.BeginRecovery(ctx, BeginRecoveryCommand{
		CanonicalOrigin: "https://play.example", RequestFlowID: flowID,
		ChallengeCredentials: beginChallenge.Credentials, RecoveryCode: onboarding.RecoveryCode,
		ClientIP: "203.0.113.50",
	})
	if err != nil {
		t.Fatal(err)
	}
	consumptions := fixture.limiter.Consumptions()[beforeLimits:]
	wantDimensions := []ratelimit.Dimension{
		ratelimit.DimensionIP, ratelimit.DimensionRecoverySelector, ratelimit.DimensionUser,
	}
	if len(consumptions) != len(wantDimensions) {
		t.Fatalf("unexpected recovery limiter calls: %v", consumptions)
	}
	for index, expected := range wantDimensions {
		if consumptions[index].Bucket().Dimension() != expected {
			t.Fatalf("limiter call %d used %s, want %s", index, consumptions[index].Bucket().Dimension(), expected)
		}
	}
	if fixture.storage.recoveries[firstRecoveryID(fixture.storage)].Snapshot().Status != RecoveryCredentialActive {
		t.Fatal("BeginRecovery consumed the long-lived recovery code")
	}

	command := CompleteRecoveryCommand{
		CanonicalOrigin: "https://play.example", RecoveryGrant: begin.RecoveryGrant,
		OperationID: testOperationID(t, 90), DeviceLabel: "Recovered Phone",
		DevicePolicy: RecoveryDevicePolicyKeepOtherDevices, RequestID: "req-recovery-1",
	}
	first, err := fixture.service.CompleteRecovery(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.service.CompleteRecovery(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Operation.Replayed || first.RecoveryCode != second.RecoveryCode ||
		first.DeviceSecrets.Token() != second.DeviceSecrets.Token() || first.Operation.ResultID != second.Operation.ResultID {
		t.Fatalf("response-loss retry did not replay exact recovery result: first=%#v second=%#v", first.Operation, second.Operation)
	}
	if first.User.Snapshot().ID != bootstrap.User.Snapshot().ID || len(fixture.storage.auditEvents) != 1 || len(fixture.storage.outboxEvents) != 1 {
		t.Fatalf("unexpected committed recovery effects: audits=%d outbox=%d", len(fixture.storage.auditEvents), len(fixture.storage.outboxEvents))
	}
	if _, err := fixture.service.CompleteRecovery(ctx, CompleteRecoveryCommand{
		CanonicalOrigin: command.CanonicalOrigin, RecoveryGrant: command.RecoveryGrant,
		OperationID: command.OperationID, DeviceLabel: "Different", DevicePolicy: command.DevicePolicy,
		RequestID: command.RequestID,
	}); !errors.Is(err, idempotency.ErrConflict) {
		t.Fatalf("changed recovery request did not conflict: %v", err)
	}
	confirmed, err := fixture.service.ConfirmSecretReceipt(ctx, ConfirmSecretReceiptCommand{
		DeviceToken: first.DeviceSecrets.Token(), CSRFToken: first.DeviceSecrets.CSRFToken(),
		Operation: IdentitySecretOperationRecovery, OperationID: first.Operation.OperationID,
		ResultID: first.Operation.ResultID,
	})
	if err != nil || !confirmed.Confirmed {
		t.Fatalf("recovery receipt was not confirmed: result=%#v err=%v", confirmed, err)
	}
	storedResult := fixture.storage.results[secretResultKeyForUser(
		fixture.storage, first.User.Snapshot().ID, first.Operation.OperationID,
	)]
	if storedResult.Snapshot().Status != secretresult.StatusConfirmed {
		t.Fatal("recovery confirmation did not erase the result envelope")
	}
	if _, err := fixture.service.CompleteRecovery(ctx, command); !errors.Is(err, secretresult.ErrSecretNoLongerAvailable) {
		t.Fatalf("confirmed recovery result became available again: %v", err)
	}
}

func TestServiceRotatesConfirmsListsAndRevokesDevices(t *testing.T) {
	fixture := newIdentityServiceFixture(t)
	ctx := context.Background()
	bootstrap, _ := fixture.onboard(t, ctx, "device_owner")
	rotation, err := fixture.service.RotateRecoveryCode(ctx, RotateRecoveryCodeCommand{
		DeviceToken: bootstrap.DeviceSecrets.Token(), CSRFToken: bootstrap.DeviceSecrets.CSRFToken(),
		OperationID: testOperationID(t, 91), RequestID: "req-rotate-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := fixture.service.ConfirmSecretReceipt(ctx, ConfirmSecretReceiptCommand{
		DeviceToken: bootstrap.DeviceSecrets.Token(), CSRFToken: bootstrap.DeviceSecrets.CSRFToken(),
		Operation:   IdentitySecretOperationRecoveryCodeRotation,
		OperationID: rotation.Operation.OperationID, ResultID: rotation.Operation.ResultID,
	})
	if err != nil || !confirmed.Confirmed {
		t.Fatalf("recovery-code receipt was not confirmed: result=%#v err=%v", confirmed, err)
	}
	storedResult := fixture.storage.results[secretResultKeyForUser(
		fixture.storage, bootstrap.User.Snapshot().ID, rotation.Operation.OperationID,
	)]
	if storedResult.Snapshot().Status != secretresult.StatusConfirmed {
		t.Fatal("confirmation did not erase the result envelope")
	}

	list, err := fixture.service.ListDevices(ctx, ListDevicesCommand{
		DeviceToken: bootstrap.DeviceSecrets.Token(), PageSize: 20,
	})
	if err != nil || len(list.Devices) != 1 || !list.Devices[0].CurrentDevice {
		t.Fatalf("unexpected device list: result=%#v err=%v", list, err)
	}
	revoked, err := fixture.service.RevokeDevice(ctx, RevokeDeviceCommand{
		DeviceToken: bootstrap.DeviceSecrets.Token(), CSRFToken: bootstrap.DeviceSecrets.CSRFToken(),
		CredentialID: bootstrap.Device.Snapshot().CredentialID, Reason: "lost phone", RequestID: "req-revoke-1",
	})
	if err != nil || !revoked.CurrentDeviceRevoked || revoked.CredentialInstruction != CredentialInstructionClear {
		t.Fatalf("current device revocation did not clear authority: result=%#v err=%v", revoked, err)
	}
	if len(fixture.storage.auditEvents) != 2 || len(fixture.storage.outboxEvents) != 1 {
		t.Fatalf("rotation/revoke event counts differ: audit=%d outbox=%d", len(fixture.storage.auditEvents), len(fixture.storage.outboxEvents))
	}
}

func TestServiceAssistedRecoveryConsumesOnlyAtComplete(t *testing.T) {
	fixture := newIdentityServiceFixture(t)
	ctx := context.Background()
	bootstrap, _ := fixture.onboard(t, ctx, "assisted_user")
	now := fixture.clock.Now()
	for id, credential := range fixture.storage.recoveries {
		revoked, err := credential.Revoke(RecoveryRevokeAssisted, now)
		if err != nil {
			t.Fatal(err)
		}
		fixture.storage.recoveries[id] = revoked
	}
	selector, err := identifier.NewSelector(bytesOf(AssistedRecoverySelectorBytes, 7))
	if err != nil {
		t.Fatal(err)
	}
	grant, err := RestoreAssistedRecoveryGrant(AssistedRecoveryGrantSnapshot{
		ID: uuid.New(), UserID: bootstrap.User.Snapshot().ID, Selector: selector, SecretHash: testRecoveryPHC,
		Purpose: AssistedRecoveryPurpose, Status: AssistedRecoveryGrantActive,
		MaxAttempts: AssistedRecoveryMaxAttempts, CreatedByAdminID: uuid.New(),
		CreatedAt: now, ExpiresAt: now.Add(AssistedRecoveryTTL),
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.storage.assisted[grant.Snapshot().ID] = grant
	code, err := security.FormatToken(AssistedRecoveryCodeVersion, selector.Value(), bytesOf(AssistedRecoverySecretBytes, 9))
	if err != nil {
		t.Fatal(err)
	}
	flowID := challenge.RequestFlowID("flow_assisted_recovery")
	beginChallenge, err := fixture.service.BeginRecoveryChallenge(ctx, BeginRecoveryChallengeCommand{
		CanonicalOrigin: "https://play.example", RequestFlowID: flowID,
	})
	if err != nil {
		t.Fatal(err)
	}
	begin, err := fixture.service.BeginRecovery(ctx, BeginRecoveryCommand{
		CanonicalOrigin: "https://play.example", RequestFlowID: flowID,
		ChallengeCredentials: beginChallenge.Credentials, RecoveryCode: code, ClientIP: "203.0.113.51",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fixture.storage.assisted[grant.Snapshot().ID].Snapshot().Status != AssistedRecoveryGrantActive {
		t.Fatal("BeginRecovery consumed the assisted grant")
	}
	result, err := fixture.service.CompleteRecovery(ctx, CompleteRecoveryCommand{
		CanonicalOrigin: "https://play.example", RecoveryGrant: begin.RecoveryGrant,
		OperationID: testOperationID(t, 92), DeviceLabel: "Assisted Phone",
		DevicePolicy: RecoveryDevicePolicyRevokeOtherDevices, RequestID: "req-assisted-1",
	})
	if err != nil || result.RecoveryCode == "" {
		t.Fatalf("assisted recovery failed: result=%#v err=%v", result.Operation, err)
	}
	storedGrant := fixture.storage.assisted[grant.Snapshot().ID]
	if storedGrant.Snapshot().Status != AssistedRecoveryGrantConsumed || storedGrant.Snapshot().ResultID != result.Operation.ResultID {
		t.Fatalf("assisted grant was not consumed with its result: %#v", storedGrant.Snapshot())
	}
	if fixture.storage.devices[bootstrap.Device.Snapshot().CredentialID].State(fixture.clock.Now()) != DeviceStateRevoked {
		t.Fatal("revoke-other policy left the old device active")
	}
	if len(fixture.storage.outboxEvents) != 2 {
		t.Fatalf("expected recovery plus device-revoked events, got %d", len(fixture.storage.outboxEvents))
	}
}

func TestCompleteRecoveryRollsBackWhenOutboxFails(t *testing.T) {
	fixture := newIdentityServiceFixture(t)
	ctx := context.Background()
	_, onboarding := fixture.onboard(t, ctx, "rollback_user")
	flowID := challenge.RequestFlowID("flow_recovery_rollback")
	beginChallenge, err := fixture.service.BeginRecoveryChallenge(ctx, BeginRecoveryChallengeCommand{
		CanonicalOrigin: "https://play.example", RequestFlowID: flowID,
	})
	if err != nil {
		t.Fatal(err)
	}
	begin, err := fixture.service.BeginRecovery(ctx, BeginRecoveryCommand{
		CanonicalOrigin: "https://play.example", RequestFlowID: flowID,
		ChallengeCredentials: beginChallenge.Credentials, RecoveryCode: onboarding.RecoveryCode,
		ClientIP: "203.0.113.52",
	})
	if err != nil {
		t.Fatal(err)
	}
	devicesBefore, recoveriesBefore, resultsBefore := len(fixture.storage.devices), len(fixture.storage.recoveries), len(fixture.storage.results)
	fixture.storage.failOutbox = outbox.ErrRepositoryUnavailable
	command := CompleteRecoveryCommand{
		CanonicalOrigin: "https://play.example", RecoveryGrant: begin.RecoveryGrant,
		OperationID: testOperationID(t, 93), DeviceLabel: "Rollback Phone",
		DevicePolicy: RecoveryDevicePolicyKeepOtherDevices, RequestID: "req-rollback-1",
	}
	if _, err = fixture.service.CompleteRecovery(ctx, command); !errors.Is(err, outbox.ErrRepositoryUnavailable) {
		t.Fatalf("expected outbox failure, got %v", err)
	}
	if len(fixture.storage.devices) != devicesBefore || len(fixture.storage.recoveries) != recoveriesBefore ||
		len(fixture.storage.results) != resultsBefore || len(fixture.storage.auditEvents) != 0 {
		t.Fatalf("failed transaction leaked state: devices=%d recoveries=%d results=%d audit=%d",
			len(fixture.storage.devices), len(fixture.storage.recoveries), len(fixture.storage.results), len(fixture.storage.auditEvents))
	}
	fixture.storage.failOutbox = nil
	if _, err = fixture.service.CompleteRecovery(ctx, command); err != nil {
		t.Fatalf("rolled-back grant was not retryable: %v", err)
	}
}

func TestCompleteRecoveryFailsClosedWhenAuditCheckpointIsStale(t *testing.T) {
	fixture := newIdentityServiceFixture(t)
	ctx := context.Background()
	_, onboarding := fixture.onboard(t, ctx, "health_gate")
	flowID := challenge.RequestFlowID("flow_recovery_audit_blocked")
	beginChallenge, err := fixture.service.BeginRecoveryChallenge(ctx, BeginRecoveryChallengeCommand{
		CanonicalOrigin: "https://play.example", RequestFlowID: flowID,
	})
	if err != nil {
		t.Fatal(err)
	}
	begin, err := fixture.service.BeginRecovery(ctx, BeginRecoveryCommand{
		CanonicalOrigin: "https://play.example", RequestFlowID: flowID,
		ChallengeCredentials: beginChallenge.Credentials, RecoveryCode: onboarding.RecoveryCode,
		ClientIP: "203.0.113.53",
	})
	if err != nil {
		t.Fatal(err)
	}
	userID := onboarding.User.Snapshot().ID
	oldEvent, err := fixture.service.audit.Prepare(fixture.storage.auditHead, audit.EventInput{
		EventID: uuid.New(), RequestID: "req-stale-audit", OccurredAt: fixture.clock.Now().Add(-6 * time.Minute),
		Actor: mustAuditActor(audit.ActorUser, userID), Target: mustAuditTarget(audit.TargetUser, userID),
		Action: audit.ActionRecoveryCodeRotated, ReasonCode: "user_requested",
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.storage.auditEvents = append(fixture.storage.auditEvents, oldEvent)
	fixture.storage.auditHead, err = oldEvent.NextHead()
	if err != nil {
		t.Fatal(err)
	}
	devicesBefore, recoveriesBefore := len(fixture.storage.devices), len(fixture.storage.recoveries)
	_, err = fixture.service.CompleteRecovery(ctx, CompleteRecoveryCommand{
		CanonicalOrigin: "https://play.example", RecoveryGrant: begin.RecoveryGrant,
		OperationID: testOperationID(t, 94), DeviceLabel: "Blocked Phone",
		DevicePolicy: RecoveryDevicePolicyKeepOtherDevices, RequestID: "req-blocked-1",
	})
	if !errors.Is(err, audit.ErrSensitiveWriteBlocked) {
		t.Fatalf("stale checkpoint did not block recovery: %v", err)
	}
	if len(fixture.storage.devices) != devicesBefore || len(fixture.storage.recoveries) != recoveriesBefore {
		t.Fatal("audit health rejection changed authoritative recovery state")
	}
}

func (fixture *identityServiceFixture) onboard(t testing.TB, ctx context.Context, username string) (BootstrapIdentityResult, CompleteOnboardingResult) {
	t.Helper()
	bootstrap := fixture.bootstrap(t, ctx)
	result, err := fixture.service.CompleteOnboarding(ctx, CompleteOnboardingCommand{
		DeviceToken: bootstrap.DeviceSecrets.Token(), CSRFToken: bootstrap.DeviceSecrets.CSRFToken(),
		ClientIP: "203.0.113.20", Username: username, OperationID: testOperationID(t, byte(len(fixture.storage.users)+30)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return bootstrap, result
}

func firstRecoveryID(storage *memoryIdentityStorage) uuid.UUID {
	for id := range storage.recoveries {
		return id
	}
	return uuid.Nil
}

func bytesOf(length int, marker byte) []byte {
	value := make([]byte, length)
	for index := range value {
		value[index] = marker
	}
	return value
}

func secretResultKeyForUser(storage *memoryIdentityStorage, userID uuid.UUID, operationID idempotency.OperationID) secretresult.Key {
	for key := range storage.results {
		if key.ActorID == userID && key.OperationID == operationID {
			return key
		}
	}
	return secretresult.Key{}
}

type identityAuditTestKeyring struct {
	private ed25519.PrivateKey
	public  ed25519.PublicKey
}

func newIdentityAuditService(t testing.TB) *audit.Service {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service, err := audit.NewService(&identityAuditTestKeyring{private: private, public: public})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func (keyring *identityAuditTestKeyring) ActiveVersion() uint32 { return 1 }
func (keyring *identityAuditTestKeyring) Sign(value []byte) (security.AuditSignature, error) {
	return security.AuditSignature{KeyVersion: 1, Value: ed25519.Sign(keyring.private, value)}, nil
}
func (keyring *identityAuditTestKeyring) Verify(value []byte, signature security.AuditSignature) bool {
	return signature.KeyVersion == 1 && ed25519.Verify(keyring.public, value, signature.Value)
}
