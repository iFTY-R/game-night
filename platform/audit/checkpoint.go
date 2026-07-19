package audit

import (
	"bytes"
	"context"
	"fmt"
	"time"

	auditv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/audit/v1"
	"github.com/iFTY-R/game-night/platform/security"
	"google.golang.org/protobuf/proto"
)

const (
	// CheckpointMaxEvents is the fixed maximum number of events allowed past the durable checkpoint.
	CheckpointMaxEvents uint64 = 100
	// CheckpointMaxAge is the fixed maximum time the oldest uncheckpointed event may remain unanchored.
	CheckpointMaxAge = 5 * time.Minute
	// MaxCheckpointPayloadBytes bounds object/outbox parsing independently of transport limits.
	MaxCheckpointPayloadBytes = 4 << 10
)

// CheckpointSnapshot is the immutable WORM anchor payload and signature metadata.
type CheckpointSnapshot struct {
	ChainID           ChainID
	Sequence          uint64
	ChainHash         Hash
	Signature         []byte
	SigningKeyVersion uint32
	CreatedAt         time.Time
}

// ObjectKey returns the deterministic create-if-absent WORM object path.
func (snapshot CheckpointSnapshot) ObjectKey() string {
	return fmt.Sprintf("audit/%s/%020d-%s.checkpoint", snapshot.ChainID, snapshot.Sequence, snapshot.ChainHash.Hex())
}

// CanonicalPayload returns deterministic protobuf bytes including the checkpoint signature for WORM storage.
func (snapshot CheckpointSnapshot) CanonicalPayload() []byte {
	encoded, err := deterministicMarshal.Marshal(checkpointProto(snapshot, snapshot.Signature))
	if err != nil {
		return nil
	}
	return encoded
}

// Checkpoint is an immutable signed chain anchor.
type Checkpoint struct{ snapshot CheckpointSnapshot }

// RestoreCheckpoint validates persisted checkpoint shape before cryptographic verification.
func RestoreCheckpoint(snapshot CheckpointSnapshot) (Checkpoint, error) {
	snapshot.CreatedAt = canonicalTime(snapshot.CreatedAt)
	snapshot.Signature = bytes.Clone(snapshot.Signature)
	if !snapshot.ChainID.Valid() || snapshot.Sequence == 0 || snapshot.SigningKeyVersion == 0 ||
		snapshot.CreatedAt.IsZero() || len(snapshot.Signature) != SignatureSize {
		return Checkpoint{}, ErrInvalidInput
	}
	return Checkpoint{snapshot: snapshot}, nil
}

// ParseCheckpoint restores deterministic WORM/outbox bytes while rejecting unknown fields and structural tampering.
// Cryptographic authenticity remains the responsibility of Service.VerifyCheckpoint.
func ParseCheckpoint(encoded []byte) (Checkpoint, error) {
	if len(encoded) == 0 || len(encoded) > MaxCheckpointPayloadBytes {
		return Checkpoint{}, ErrIntegrity
	}
	var message auditv1.AuditCheckpoint
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(encoded, &message); err != nil ||
		len(message.ProtoReflect().GetUnknown()) != 0 || message.CreatedAt == nil || message.CreatedAt.CheckValid() != nil {
		return Checkpoint{}, ErrIntegrity
	}
	chainHash, err := NewHash(message.ChainHash)
	if err != nil {
		return Checkpoint{}, ErrIntegrity
	}
	checkpoint, err := RestoreCheckpoint(CheckpointSnapshot{
		ChainID: ChainID(message.ChainId), Sequence: message.Sequence, ChainHash: chainHash,
		Signature: message.CheckpointSignature, SigningKeyVersion: message.SigningKeyVersion,
		CreatedAt: message.CreatedAt.AsTime(),
	})
	if err != nil {
		return Checkpoint{}, ErrIntegrity
	}
	// Byte equality rejects duplicate/default encodings even when protobuf would decode them to the same value.
	if !bytes.Equal(encoded, checkpoint.Snapshot().CanonicalPayload()) {
		return Checkpoint{}, ErrIntegrity
	}
	return checkpoint, nil
}

// Snapshot returns a deep copy for outbox and object-storage adapters.
func (checkpoint Checkpoint) Snapshot() CheckpointSnapshot {
	snapshot := checkpoint.snapshot
	snapshot.Signature = bytes.Clone(snapshot.Signature)
	return snapshot
}

// PrepareCheckpoint signs the supplied committed head in the checkpoint-specific signature domain.
func (service *Service) PrepareCheckpoint(head Head, createdAt time.Time) (Checkpoint, error) {
	createdAt = canonicalTime(createdAt)
	if head.Sequence() == 0 || createdAt.IsZero() || createdAt.Before(head.UpdatedAt()) {
		return Checkpoint{}, ErrInvalidInput
	}
	keyVersion := service.keys.ActiveVersion()
	if keyVersion == 0 {
		return Checkpoint{}, ErrIntegrity
	}
	snapshot := CheckpointSnapshot{ChainID: head.ChainID(), Sequence: head.Sequence(), ChainHash: head.Hash(),
		SigningKeyVersion: keyVersion, CreatedAt: createdAt}
	canonical, err := canonicalUnsignedCheckpoint(snapshot)
	if err != nil {
		return Checkpoint{}, err
	}
	signature, err := service.keys.Sign(checkpointSigningPayload(canonical))
	if err != nil || signature.KeyVersion != keyVersion || len(signature.Value) != SignatureSize {
		return Checkpoint{}, ErrIntegrity
	}
	snapshot.Signature = signature.Value
	return RestoreCheckpoint(snapshot)
}

// VerifyCheckpoint verifies the unsigned canonical checkpoint with the recorded historical public key.
func (service *Service) VerifyCheckpoint(checkpoint Checkpoint) error {
	snapshot := checkpoint.Snapshot()
	canonical, err := canonicalUnsignedCheckpoint(snapshot)
	if err != nil || !service.keys.Verify(checkpointSigningPayload(canonical), security.AuditSignature{
		KeyVersion: snapshot.SigningKeyVersion,
		Value:      snapshot.Signature,
	}) {
		return ErrIntegrity
	}
	return nil
}

// HealthState is the readiness and sensitive-write gate derived from durable checkpoint progress.
type HealthState uint8

const (
	HealthHealthy HealthState = iota + 1
	HealthDegraded
)

// CheckpointHealthInput contains durable progress only; transient in-memory upload attempts cannot make it healthy.
type CheckpointHealthInput struct {
	HeadSequence         uint64
	AcknowledgedSequence uint64
	UncheckpointedSince  time.Time
	Now                  time.Time
	Production           bool
	SinkReady            bool
}

// CheckpointHealth is the pure fail-closed decision consumed by readiness and sensitive command guards.
type CheckpointHealth struct {
	state                HealthState
	uncheckpointedEvents uint64
	uncheckpointedAge    time.Duration
	maxEvents            uint64
	maxAge               time.Duration
}

// SinkReadiness reports whether the configured durable checkpoint sink can accept new anchors.
type SinkReadiness interface {
	Ready(context.Context) bool
}

// SinkReadinessFunc adapts a process-owned sink probe to the domain health policy.
type SinkReadinessFunc func(context.Context) bool

// Ready invokes the sink probe and treats an absent function as unavailable.
func (check SinkReadinessFunc) Ready(ctx context.Context) bool {
	return check != nil && check(ctx)
}

// CheckpointHealthPolicy combines deployment mode with a live sink probe so domains and readiness use one decision.
type CheckpointHealthPolicy struct {
	production bool
	sink       SinkReadiness
	maxEvents  uint64
	maxAge     time.Duration
}

// NewCheckpointHealthPolicy requires an explicit sink probe even outside production to prevent hidden healthy defaults.
func NewCheckpointHealthPolicy(production bool, sink SinkReadiness) (*CheckpointHealthPolicy, error) {
	return NewCheckpointHealthPolicyWithThresholds(production, sink, CheckpointMaxEvents, CheckpointMaxAge)
}

// NewCheckpointHealthPolicyWithThresholds permits deployments to tighten, but never relax, the fixed ceilings.
func NewCheckpointHealthPolicyWithThresholds(
	production bool,
	sink SinkReadiness,
	maxEvents uint64,
	maxAge time.Duration,
) (*CheckpointHealthPolicy, error) {
	if sink == nil {
		return nil, ErrInvalidInput
	}
	if maxEvents == 0 || maxEvents > CheckpointMaxEvents || maxAge <= 0 || maxAge > CheckpointMaxAge {
		return nil, ErrInvalidInput
	}
	return &CheckpointHealthPolicy{
		production: production, sink: sink, maxEvents: maxEvents, maxAge: maxAge,
	}, nil
}

// Evaluate derives health from durable database progress and the current sink state.
func (policy *CheckpointHealthPolicy) Evaluate(
	ctx context.Context,
	headSequence uint64,
	progress CheckpointProgress,
	now time.Time,
) (CheckpointHealth, error) {
	if policy == nil || policy.sink == nil || ctx == nil || !progress.ChainID.Valid() {
		return CheckpointHealth{}, ErrInvalidInput
	}
	return evaluateCheckpointHealth(CheckpointHealthInput{
		HeadSequence:         headSequence,
		AcknowledgedSequence: progress.AcknowledgedSequence,
		UncheckpointedSince:  progress.UncheckpointedSince,
		Now:                  now,
		Production:           policy.production,
		SinkReady:            policy.sink.Ready(ctx),
	}, policy.maxEvents, policy.maxAge)
}

// EvaluateCheckpointHealth applies the fixed 100-event/5-minute thresholds and production sink requirement.
func EvaluateCheckpointHealth(input CheckpointHealthInput) (CheckpointHealth, error) {
	return evaluateCheckpointHealth(input, CheckpointMaxEvents, CheckpointMaxAge)
}

func evaluateCheckpointHealth(input CheckpointHealthInput, maxEvents uint64, maxAge time.Duration) (CheckpointHealth, error) {
	if input.Now.IsZero() || input.AcknowledgedSequence > input.HeadSequence {
		return CheckpointHealth{}, ErrInvalidInput
	}
	input.Now = input.Now.UTC()
	uncheckpointed := input.HeadSequence - input.AcknowledgedSequence
	age := time.Duration(0)
	if uncheckpointed > 0 {
		if input.UncheckpointedSince.IsZero() || input.UncheckpointedSince.After(input.Now) {
			return CheckpointHealth{}, ErrInvalidInput
		}
		age = input.Now.Sub(input.UncheckpointedSince.UTC())
	} else if !input.UncheckpointedSince.IsZero() {
		return CheckpointHealth{}, ErrInvalidInput
	}
	state := HealthHealthy
	if input.Production && !input.SinkReady || uncheckpointed >= maxEvents || age >= maxAge {
		state = HealthDegraded
	}
	return CheckpointHealth{
		state: state, uncheckpointedEvents: uncheckpointed, uncheckpointedAge: age,
		maxEvents: maxEvents, maxAge: maxAge,
	}, nil
}

// State returns the closed readiness state.
func (health CheckpointHealth) State() HealthState { return health.state }

// Ready reports whether the process may advertise readiness.
func (health CheckpointHealth) Ready() bool { return health.state == HealthHealthy }

// AllowsSensitiveWrites applies the same durable health boundary to protected commands.
func (health CheckpointHealth) AllowsSensitiveWrites() bool { return health.state == HealthHealthy }

// CheckpointDue reports whether the append transaction must enqueue a new checkpoint.
func (health CheckpointHealth) CheckpointDue() bool {
	return health.uncheckpointedEvents >= health.maxEvents || health.uncheckpointedAge >= health.maxAge
}

// UncheckpointedEvents returns the durable event lag used for metrics.
func (health CheckpointHealth) UncheckpointedEvents() uint64 { return health.uncheckpointedEvents }

// UncheckpointedAge returns the age used for metrics and alerting.
func (health CheckpointHealth) UncheckpointedAge() time.Duration { return health.uncheckpointedAge }
