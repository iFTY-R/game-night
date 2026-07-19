package audit

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/security"
)

func TestServiceProducesDeterministicCanonicalEvent(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 123456789, time.FixedZone("test", 8*60*60))
	service := mustService(t, newTestKeyring(t, 7, nil))
	head := mustHead(t, HeadSnapshot{ChainID: ChainAdmin, Sequence: 0, Hash: GenesisHash, UpdatedAt: now.Add(-time.Second)})
	input := validEventInput(t, now)

	first, err := service.Prepare(head, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Prepare(head, input)
	if err != nil {
		t.Fatal(err)
	}
	firstSnapshot := first.Snapshot()
	secondSnapshot := second.Snapshot()
	if !bytes.Equal(firstSnapshot.CanonicalEvent, secondSnapshot.CanonicalEvent) {
		t.Fatal("identical audit values produced different canonical protobuf")
	}
	if firstSnapshot.Event.OccurredAt.Location() != time.UTC || firstSnapshot.Event.OccurredAt.Nanosecond()%1_000 != 0 {
		t.Fatalf("occurred_at was not canonicalized to UTC microseconds: %v", firstSnapshot.Event.OccurredAt)
	}
	if firstSnapshot.Event.SigningKeyVersion != 7 || firstSnapshot.EventHash != secondSnapshot.EventHash {
		t.Fatalf("unexpected signing/hash metadata: %+v", firstSnapshot)
	}
	if err := service.Verify(first); err != nil {
		t.Fatalf("prepared event did not verify: %v", err)
	}
}

func TestVerifyDetectsCanonicalHashAndSignatureTampering(t *testing.T) {
	now := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	service := mustService(t, newTestKeyring(t, 3, nil))
	head := mustHead(t, HeadSnapshot{ChainID: ChainAdmin, Hash: GenesisHash, UpdatedAt: now.Add(-time.Second)})
	signed, err := service.Prepare(head, validEventInput(t, now))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*SignedEventSnapshot)
	}{
		{name: "canonical event", mutate: func(snapshot *SignedEventSnapshot) { snapshot.CanonicalEvent[0] ^= 0xff }},
		{name: "event hash", mutate: func(snapshot *SignedEventSnapshot) { snapshot.EventHash[0] ^= 0xff }},
		{name: "signature", mutate: func(snapshot *SignedEventSnapshot) { snapshot.Signature[0] ^= 0xff }},
		{name: "event value", mutate: func(snapshot *SignedEventSnapshot) { snapshot.Event.ReasonCode = "tampered" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := signed.Snapshot()
			test.mutate(&snapshot)
			tampered, err := RestoreSignedEvent(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if err := service.Verify(tampered); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("expected integrity error, got %v", err)
			}
		})
	}
}

func TestParseSignedEventRejectsUnknownAndStructuralTampering(t *testing.T) {
	now := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	service := mustService(t, newTestKeyring(t, 3, nil))
	head := mustHead(t, HeadSnapshot{ChainID: ChainAdmin, Hash: GenesisHash, UpdatedAt: now.Add(-time.Second)})
	signed, err := service.Prepare(head, validEventInput(t, now))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := signed.Snapshot()
	parsed, err := ParseSignedEvent(snapshot.CanonicalEvent, snapshot.EventHash.Bytes(), snapshot.Signature)
	if err != nil || service.Verify(parsed) != nil {
		t.Fatalf("canonical event did not round trip: %v", err)
	}

	unknownField := append(bytes.Clone(snapshot.CanonicalEvent), 0xa0, 0x06, 0x01)
	if _, err := ParseSignedEvent(unknownField, snapshot.EventHash.Bytes(), snapshot.Signature); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("expected unknown-field integrity error, got %v", err)
	}
	if _, err := ParseSignedEvent(snapshot.CanonicalEvent[:len(snapshot.CanonicalEvent)-1], snapshot.EventHash.Bytes(), snapshot.Signature); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("expected truncated event integrity error, got %v", err)
	}

	tamperedHash := snapshot.EventHash.Bytes()
	tamperedHash[0] ^= 0xff
	parsed, err = ParseSignedEvent(snapshot.CanonicalEvent, tamperedHash, snapshot.Signature)
	if err != nil {
		t.Fatalf("hash tamper must remain structurally parseable: %v", err)
	}
	if err := service.Verify(parsed); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("expected hash integrity error, got %v", err)
	}
}

func TestHistoricalKeyVerifiesAfterRotation(t *testing.T) {
	oldRing := newTestKeyring(t, 1, nil)
	rotatedRing := newTestKeyring(t, 2, oldRing)
	now := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	head := mustHead(t, HeadSnapshot{ChainID: ChainAdmin, Hash: GenesisHash, UpdatedAt: now.Add(-time.Second)})
	oldService := mustService(t, oldRing)
	oldEvent, err := oldService.Prepare(head, validEventInput(t, now))
	if err != nil {
		t.Fatal(err)
	}
	rotatedService := mustService(t, rotatedRing)
	if err := rotatedService.Verify(oldEvent); err != nil {
		t.Fatalf("historical event did not verify after rotation: %v", err)
	}
	newEvent, err := rotatedService.Prepare(head, validEventInput(t, now))
	if err != nil {
		t.Fatal(err)
	}
	if newEvent.Snapshot().Event.SigningKeyVersion != 2 {
		t.Fatal("rotated active key was not selected")
	}
}

func TestVerifyChainEnforcesSequenceAndPreviousHash(t *testing.T) {
	now := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	service := mustService(t, newTestKeyring(t, 1, nil))
	genesis := mustHead(t, HeadSnapshot{ChainID: ChainAdmin, Hash: GenesisHash, UpdatedAt: now.Add(-time.Second)})
	first, err := service.Prepare(genesis, validEventInput(t, now))
	if err != nil {
		t.Fatal(err)
	}
	firstHead, err := first.NextHead()
	if err != nil {
		t.Fatal(err)
	}
	secondInput := validEventInput(t, now.Add(time.Second))
	secondInput.EventID = uuid.New()
	second, err := service.Prepare(firstHead, secondInput)
	if err != nil {
		t.Fatal(err)
	}
	finalHead, err := service.VerifyChain(genesis, []SignedEvent{first, second})
	if err != nil || finalHead.Sequence() != 2 || finalHead.Hash() != second.Snapshot().EventHash {
		t.Fatalf("valid chain failed: head=%+v err=%v", finalHead.Snapshot(), err)
	}
	if _, err := service.VerifyChain(genesis, []SignedEvent{second}); !errors.Is(err, ErrChainDiscontinuity) {
		t.Fatalf("expected chain discontinuity, got %v", err)
	}
}

func TestCheckpointUsesSeparateSignaturePurposeAndVerifies(t *testing.T) {
	now := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	keys := newTestKeyring(t, 4, nil)
	service := mustService(t, keys)
	head := mustHead(t, HeadSnapshot{ChainID: ChainAdmin, Sequence: 100, Hash: mustHash(t, bytes.Repeat([]byte{0x42}, HashSize)), UpdatedAt: now})
	checkpoint, err := service.PrepareCheckpoint(head, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.VerifyCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	snapshot := checkpoint.Snapshot()
	if snapshot.SigningKeyVersion != 4 || snapshot.ObjectKey() != "audit/admin/00000000000000000100-"+snapshot.ChainHash.Hex()+".checkpoint" {
		t.Fatalf("unexpected checkpoint metadata: %+v", snapshot)
	}
	// Event and checkpoint signatures cannot be replayed across protocol purposes.
	if keys.Verify(eventSigningPayload(snapshot.CanonicalPayload()), security.AuditSignature{KeyVersion: snapshot.SigningKeyVersion, Value: snapshot.Signature}) {
		t.Fatal("checkpoint signature verified in the event signature domain")
	}
}

func TestParseCheckpointRejectsMalformedUnknownAndStructuralTampering(t *testing.T) {
	now := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	service := mustService(t, newTestKeyring(t, 4, nil))
	head := mustHead(t, HeadSnapshot{ChainID: ChainAdmin, Sequence: 12, Hash: mustHash(t, bytes.Repeat([]byte{0x42}, HashSize)), UpdatedAt: now})
	checkpoint, err := service.PrepareCheckpoint(head, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	payload := checkpoint.Snapshot().CanonicalPayload()
	parsed, err := ParseCheckpoint(payload)
	if err != nil || service.VerifyCheckpoint(parsed) != nil {
		t.Fatalf("canonical checkpoint did not round trip: %v", err)
	}

	unknownField := append(bytes.Clone(payload), 0xa0, 0x06, 0x01)
	if _, err := ParseCheckpoint(unknownField); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("expected unknown-field integrity error, got %v", err)
	}
	if _, err := ParseCheckpoint(payload[:len(payload)-1]); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("expected truncated payload integrity error, got %v", err)
	}
	structural := checkpoint.Snapshot()
	structural.Sequence = 0
	if _, err := ParseCheckpoint(structural.CanonicalPayload()); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("expected structural integrity error, got %v", err)
	}

	signatureTamper := checkpoint.Snapshot()
	signatureTamper.Signature[0] ^= 0xff
	tampered, err := ParseCheckpoint(signatureTamper.CanonicalPayload())
	if err != nil {
		t.Fatalf("signature tamper must remain structurally parseable: %v", err)
	}
	if err := service.VerifyCheckpoint(tampered); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("expected signature integrity error, got %v", err)
	}
}

func TestCheckpointHealthFailsClosedAtFixedThresholds(t *testing.T) {
	now := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		input CheckpointHealthInput
		state HealthState
	}{
		{name: "healthy below both thresholds", input: CheckpointHealthInput{HeadSequence: 99, AcknowledgedSequence: 0, UncheckpointedSince: now.Add(-CheckpointMaxAge + time.Nanosecond), Now: now, Production: true, SinkReady: true}, state: HealthHealthy},
		{name: "event threshold", input: CheckpointHealthInput{HeadSequence: 100, UncheckpointedSince: now, Now: now, Production: true, SinkReady: true}, state: HealthDegraded},
		{name: "time threshold", input: CheckpointHealthInput{HeadSequence: 1, UncheckpointedSince: now.Add(-CheckpointMaxAge), Now: now, Production: true, SinkReady: true}, state: HealthDegraded},
		{name: "production sink missing", input: CheckpointHealthInput{Now: now, Production: true, SinkReady: false}, state: HealthDegraded},
		{name: "development sink optional", input: CheckpointHealthInput{Now: now, Production: false, SinkReady: false}, state: HealthHealthy},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			health, err := EvaluateCheckpointHealth(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if health.State() != test.state || health.Ready() != (test.state == HealthHealthy) || health.AllowsSensitiveWrites() != (test.state == HealthHealthy) {
				t.Fatalf("unexpected health decision: state=%v ready=%t allowed=%t", health.State(), health.Ready(), health.AllowsSensitiveWrites())
			}
		})
	}
}

func TestCheckpointHealthPolicyUsesLiveSinkReadiness(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	sinkReady := false
	policy, err := NewCheckpointHealthPolicy(true, SinkReadinessFunc(func(context.Context) bool {
		return sinkReady
	}))
	if err != nil {
		t.Fatal(err)
	}
	progress := CheckpointProgress{ChainID: ChainAdmin}
	health, err := policy.Evaluate(t.Context(), 0, progress, now)
	if err != nil || health.Ready() {
		t.Fatalf("production policy ignored unavailable sink: health=%+v err=%v", health, err)
	}
	sinkReady = true
	health, err = policy.Evaluate(t.Context(), 0, progress, now)
	if err != nil || !health.Ready() {
		t.Fatalf("production policy ignored recovered sink: health=%+v err=%v", health, err)
	}
	if _, err = NewCheckpointHealthPolicy(false, nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil sink policy error = %v", err)
	}
}

func TestCheckpointHealthPolicyHonorsTighterConfiguredThresholds(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	policy, err := NewCheckpointHealthPolicyWithThresholds(
		false,
		SinkReadinessFunc(func(context.Context) bool { return true }),
		10,
		time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	health, err := policy.Evaluate(t.Context(), 10, CheckpointProgress{
		ChainID: ChainAdmin, UncheckpointedSince: now.Add(-time.Second),
	}, now)
	if err != nil || health.Ready() || !health.CheckpointDue() {
		t.Fatalf("tightened event threshold ignored: health=%+v err=%v", health, err)
	}
	if _, err := NewCheckpointHealthPolicyWithThresholds(
		false, SinkReadinessFunc(func(context.Context) bool { return true }), CheckpointMaxEvents+1, CheckpointMaxAge,
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("relaxed checkpoint threshold error = %v", err)
	}
}

func validEventInput(t *testing.T, at time.Time) EventInput {
	t.Helper()
	actor, err := NewActor(ActorAdmin, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	target, err := NewTarget(TargetUser, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	return EventInput{
		EventID:      uuid.New(),
		RequestID:    uuid.NewString(),
		OccurredAt:   at,
		Actor:        actor,
		Target:       target,
		Action:       ActionUserSuspended,
		ReasonCode:   "policy.violation",
		DetailDigest: bytes.Repeat([]byte{0x19}, HashSize),
	}
}

type testKeyring struct {
	active  uint32
	private map[uint32]ed25519.PrivateKey
	public  map[uint32]ed25519.PublicKey
}

func (keyring *testKeyring) ActiveVersion() uint32 { return keyring.active }

func newTestKeyring(t *testing.T, active uint32, previous *testKeyring) *testKeyring {
	t.Helper()
	result := &testKeyring{active: active, private: make(map[uint32]ed25519.PrivateKey), public: make(map[uint32]ed25519.PublicKey)}
	if previous != nil {
		for version, publicKey := range previous.public {
			result.public[version] = bytes.Clone(publicKey)
		}
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	result.public[active] = publicKey
	result.private[active] = privateKey
	return result
}

func (keyring *testKeyring) Sign(value []byte) (security.AuditSignature, error) {
	return security.AuditSignature{KeyVersion: keyring.active, Value: ed25519.Sign(keyring.private[keyring.active], value)}, nil
}

func (keyring *testKeyring) Verify(value []byte, signature security.AuditSignature) bool {
	return ed25519.Verify(keyring.public[signature.KeyVersion], value, signature.Value)
}

func mustService(t *testing.T, keys SigningKeyring) *Service {
	t.Helper()
	service, err := NewService(keys)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func mustHead(t *testing.T, snapshot HeadSnapshot) Head {
	t.Helper()
	head, err := RestoreHead(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return head
}

func mustHash(t *testing.T, value []byte) Hash {
	t.Helper()
	hash, err := NewHash(value)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}
