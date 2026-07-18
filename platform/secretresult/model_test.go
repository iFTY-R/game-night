package secretresult

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestResultStateMachineErasesSecretsAndRetainsBindings(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 123456789, time.FixedZone("test", 8*60*60))
	binding := testBinding(t)
	available := testAvailableResult(t, binding, now)

	resolution, err := available.Resolve(binding, now)
	if err != nil || resolution.Kind != ReplayAvailable {
		t.Fatalf("available resolution = %+v, err=%v", resolution, err)
	}
	confirmed, err := available.Confirm(now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	confirmedSnapshot := confirmed.Snapshot()
	if confirmedSnapshot.Status != StatusConfirmed || !confirmedSnapshot.Payload.Empty() || confirmedSnapshot.ConfirmedAt.IsZero() {
		t.Fatalf("invalid confirmed tombstone: %+v", confirmedSnapshot)
	}
	confirmedAgain, err := confirmed.Confirm(now.Add(2 * time.Minute))
	if err != nil || confirmedAgain.Snapshot().ID != confirmedSnapshot.ID {
		t.Fatalf("repeated confirm must be idempotent: result=%+v err=%v", confirmedAgain.Snapshot(), err)
	}
	resolution, err = confirmed.Resolve(binding, now.Add(2*time.Minute))
	if err != nil || resolution.Kind != ReplayUnavailable {
		t.Fatalf("confirmed resolution = %+v, err=%v", resolution, err)
	}

	expired, err := available.Expire(now.Add(MaximumSecretTTL))
	if err != nil {
		t.Fatal(err)
	}
	expiredSnapshot := expired.Snapshot()
	if expiredSnapshot.Status != StatusExpired || !expiredSnapshot.Payload.Empty() || expiredSnapshot.Binding != binding {
		t.Fatalf("invalid expired tombstone: %+v", expiredSnapshot)
	}
	if _, err := available.Expire(now.Add(time.Minute)); !errors.Is(err, ErrConcurrentTransition) {
		t.Fatalf("early expiry error = %v", err)
	}
}

func TestResultResolutionSeparatesConflictAndUnauthorizedReplay(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	binding := testBinding(t)
	result := testAvailableResult(t, binding, now)

	differentDigest := binding
	differentDigest.RequestDigest[0] ^= 0xff
	if _, err := result.Resolve(differentDigest, now); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("digest mismatch error = %v", err)
	}
	for name, mutate := range map[string]func(*Binding){
		"actor": func(candidate *Binding) { candidate.Key.ActorID = uuid.New() },
		"identity scope and type": func(candidate *Binding) {
			candidate.Key.Scope = ScopeIdentityOnboarding
			candidate.ResultType = ResultTypeIdentityRecoveryCode
		},
		"admin scope and type": func(candidate *Binding) {
			candidate.Key.Scope = ScopeAdminTOTPEnrollment
			candidate.ResultType = ResultTypeAdminTOTPEnrollment
		},
		"version": func(candidate *Binding) {
			candidate.ResultVersion++
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := binding
			mutate(&candidate)
			if _, err := result.Resolve(candidate, now); !errors.Is(err, ErrReplayUnauthorized) {
				t.Fatalf("binding mismatch error = %v", err)
			}
		})
	}
}

func TestBindingAllowsOnlyReviewedScopeAndResultTypePairs(t *testing.T) {
	binding := testBinding(t)
	allowed := []struct {
		scope      Scope
		resultType ResultType
	}{
		{ScopeIdentityBootstrap, ResultTypeIdentityDeviceCredential},
		{ScopeIdentityOnboarding, ResultTypeIdentityRecoveryCode},
		{ScopeIdentityRecovery, ResultTypeIdentityRecoveryBundle},
		{ScopeIdentityRecoveryCodeRotation, ResultTypeIdentityRecoveryCode},
		{ScopeAdminTOTPEnrollment, ResultTypeAdminTOTPEnrollment},
		{ScopeAdminInitialRecoveryCodes, ResultTypeAdminRecoveryCodes},
		{ScopeAdminTOTPRebind, ResultTypeAdminTOTPEnrollment},
		{ScopeAdminTOTPRebind, ResultTypeAdminRecoveryCodes},
		{ScopeAdminRegenerateRecoveryCodes, ResultTypeAdminRecoveryCodes},
		{ScopeAdminAssistedRecoveryGrant, ResultTypeAdminAssistedRecoveryGrant},
	}
	for _, pair := range allowed {
		candidate := binding
		candidate.Key.Scope = pair.scope
		candidate.ResultType = pair.resultType
		if err := candidate.Validate(); err != nil {
			t.Fatalf("allowed pair %q/%q error = %v", pair.scope, pair.resultType, err)
		}
	}

	for _, pair := range []struct {
		scope      Scope
		resultType ResultType
	}{
		{ScopeIdentityBootstrap, ResultTypeAdminRecoveryCodes},
		{ScopeAdminTOTPEnrollment, ResultTypeIdentityDeviceCredential},
		{ScopeIdentityRecovery, ResultTypeIdentityRecoveryCode},
	} {
		candidate := binding
		candidate.Key.Scope = pair.scope
		candidate.ResultType = pair.resultType
		if err := candidate.Validate(); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("forbidden pair %q/%q error = %v", pair.scope, pair.resultType, err)
		}
	}
}

func TestResultSnapshotDoesNotExposeMutableCiphertext(t *testing.T) {
	result := testAvailableResult(t, testBinding(t), time.Now().UTC())
	first := result.Snapshot()
	first.Payload.Ciphertext[0] ^= 0xff
	second := result.Snapshot()
	if bytes.Equal(first.Payload.Ciphertext, second.Payload.Ciphertext) {
		t.Fatal("snapshot mutation changed immutable result state")
	}
}

func testBinding(t testing.TB) Binding {
	t.Helper()
	operationID, err := NewOperationID(bytes.Repeat([]byte{0x42}, 16))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := NewDigest(bytes.Repeat([]byte{0x24}, DigestSize))
	if err != nil {
		t.Fatal(err)
	}
	return Binding{
		Key:           Key{Scope: ScopeIdentityBootstrap, ActorID: uuid.MustParse("46cb6236-543f-4a30-83ce-8b533ba3d3b5"), OperationID: operationID},
		RequestDigest: digest, ResultType: ResultTypeIdentityDeviceCredential, ResultVersion: 1,
	}
}

func testAvailableResult(t testing.TB, binding Binding, completedAt time.Time) Result {
	t.Helper()
	completedAt = completedAt.UTC().Truncate(time.Microsecond)
	expiresAt := completedAt.Add(MaximumSecretTTL)
	result, err := NewAvailable(
		uuid.MustParse("61ffad2e-9088-43ca-80c8-080eb59a68a0"),
		binding,
		EncryptedPayload{Ciphertext: []byte("ciphertext"), Nonce: []byte("nonce"), WrappedDataKey: []byte("wrapped"), KeyVersion: 1},
		completedAt,
		expiresAt,
		expiresAt.Add(MinimumTombstoneRetention),
	)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
