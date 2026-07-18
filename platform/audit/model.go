// Package audit defines the immutable, signed audit chain shared by platform domains.
package audit

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"time"

	"github.com/google/uuid"
	auditv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/audit/v1"
	"google.golang.org/protobuf/proto"
)

const (
	// SchemaVersion identifies the canonical protobuf schema covered by signatures.
	SchemaVersion uint32 = 1
	// HashSize is the fixed SHA-256 chain-link and detail-digest length.
	HashSize = sha256.Size
	// SignatureSize is the fixed Ed25519 signature length persisted by the audit schema.
	SignatureSize = ed25519.SignatureSize
	// MaxRequestIDBytes bounds request correlation data before protobuf allocation.
	MaxRequestIDBytes = 128
	// MaxReasonCodeBytes bounds non-sensitive, machine-readable audit reasons.
	MaxReasonCodeBytes = 64
	// MaxCanonicalEventBytes bounds persisted protobuf parsing independently of query/page limits.
	MaxCanonicalEventBytes = 16 << 10
)

var (
	// ErrInvalidInput rejects values outside the versioned audit protocol.
	ErrInvalidInput = errors.New("invalid audit input")
	// ErrIntegrity reports canonical, hash, signature, or persisted-value tampering.
	ErrIntegrity = errors.New("audit integrity failure")
	// ErrChainDiscontinuity reports an event whose chain, sequence, or previous hash does not follow the supplied head.
	ErrChainDiscontinuity = errors.New("audit chain discontinuity")
	// ErrHeadConflict is the stable retryable result for an expected-head compare-and-swap loss.
	ErrHeadConflict = errors.New("audit head conflict")
	// ErrNotFound reports an unknown chain or checkpoint without leaking storage details.
	ErrNotFound = errors.New("audit value not found")
	// ErrRepositoryUnavailable hides infrastructure lifecycle and query failures.
	ErrRepositoryUnavailable = errors.New("audit repository unavailable")
	// ErrCheckpointUnavailable reports checkpoint persistence or sink integrity failure.
	ErrCheckpointUnavailable = errors.New("audit checkpoint unavailable")
	// ErrSensitiveWriteBlocked is returned by application services when checkpoint health is degraded.
	ErrSensitiveWriteBlocked = errors.New("sensitive write blocked by audit health")

	requestIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
	reasonCodePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	systemIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
)

// ChainID selects one independently serialized integrity chain.
type ChainID string

const (
	// ChainAdmin is the platform administration and security audit chain.
	ChainAdmin ChainID = "admin"
)

// Valid rejects unprovisioned chains so callers cannot create an unsigned namespace by typo.
func (chainID ChainID) Valid() bool { return chainID == ChainAdmin }

// Hash is an immutable SHA-256 value used for chain heads and event digests.
type Hash [HashSize]byte

// GenesisHash is the database-provisioned previous hash for sequence zero.
var GenesisHash Hash

// NewHash copies an exact SHA-256 value into its immutable representation.
func NewHash(value []byte) (Hash, error) {
	if len(value) != HashSize {
		return Hash{}, ErrInvalidInput
	}
	var result Hash
	copy(result[:], value)
	return result, nil
}

// Bytes returns a copy suitable for persistence adapters.
func (hash Hash) Bytes() []byte { return bytes.Clone(hash[:]) }

// Hex returns the stable lowercase form used by checkpoint object keys.
func (hash Hash) Hex() string { return hex.EncodeToString(hash[:]) }

// HeadSnapshot is the persistence-neutral audit chain head representation.
type HeadSnapshot struct {
	ChainID   ChainID
	Sequence  uint64
	Hash      Hash
	UpdatedAt time.Time
}

// Head is a validated immutable chain position.
type Head struct{ snapshot HeadSnapshot }

// RestoreHead validates a chain position read through the restricted head port.
func RestoreHead(snapshot HeadSnapshot) (Head, error) {
	snapshot.UpdatedAt = canonicalTime(snapshot.UpdatedAt)
	if !snapshot.ChainID.Valid() || snapshot.UpdatedAt.IsZero() || (snapshot.Sequence == 0 && snapshot.Hash != GenesisHash) {
		return Head{}, ErrInvalidInput
	}
	return Head{snapshot: snapshot}, nil
}

// Snapshot returns a value copy for adapters.
func (head Head) Snapshot() HeadSnapshot { return head.snapshot }

// ChainID returns the chain namespace.
func (head Head) ChainID() ChainID { return head.snapshot.ChainID }

// Sequence returns the last committed event sequence.
func (head Head) Sequence() uint64 { return head.snapshot.Sequence }

// Hash returns the last committed event hash.
func (head Head) Hash() Hash { return head.snapshot.Hash }

// UpdatedAt returns the canonical commit timestamp of the head.
func (head Head) UpdatedAt() time.Time { return head.snapshot.UpdatedAt }

// ActorType is a closed set of authorities capable of causing an audit event.
type ActorType int32

const (
	ActorUser   ActorType = ActorType(auditv1.AuditActorType_AUDIT_ACTOR_TYPE_USER)
	ActorAdmin  ActorType = ActorType(auditv1.AuditActorType_AUDIT_ACTOR_TYPE_ADMIN)
	ActorSystem ActorType = ActorType(auditv1.AuditActorType_AUDIT_ACTOR_TYPE_SYSTEM)
)

func (actorType ActorType) valid() bool {
	switch actorType {
	case ActorUser, ActorAdmin, ActorSystem:
		return true
	default:
		return false
	}
}

// Actor is an immutable identity reference; it never contains display names or other PII.
type Actor struct {
	actorType ActorType
	id        string
}

// NewActor validates UUID-backed user/admin actors and bounded system identifiers.
func NewActor(actorType ActorType, id string) (Actor, error) {
	if !actorType.valid() || !validEntityID(actorType == ActorSystem, id) {
		return Actor{}, ErrInvalidInput
	}
	return Actor{actorType: actorType, id: id}, nil
}

func (actor Actor) valid() bool {
	return actor.actorType.valid() && validEntityID(actor.actorType == ActorSystem, actor.id)
}

// Type returns the closed actor category encoded in canonical protobuf.
func (actor Actor) Type() ActorType { return actor.actorType }

// ID returns the non-PII stable actor identifier.
func (actor Actor) ID() string { return actor.id }

// TargetType is the closed set of redacted resources referenced by audit events.
type TargetType int32

const (
	TargetUser          TargetType = TargetType(auditv1.AuditTargetType_AUDIT_TARGET_TYPE_USER)
	TargetDevice        TargetType = TargetType(auditv1.AuditTargetType_AUDIT_TARGET_TYPE_DEVICE)
	TargetProfileExport TargetType = TargetType(auditv1.AuditTargetType_AUDIT_TARGET_TYPE_PROFILE_EXPORT)
	TargetAdmin         TargetType = TargetType(auditv1.AuditTargetType_AUDIT_TARGET_TYPE_ADMIN)
	TargetSystem        TargetType = TargetType(auditv1.AuditTargetType_AUDIT_TARGET_TYPE_SYSTEM)
)

func (targetType TargetType) valid() bool {
	switch targetType {
	case TargetUser, TargetDevice, TargetProfileExport, TargetAdmin, TargetSystem:
		return true
	default:
		return false
	}
}

// Target is an immutable resource reference containing only a stable ID.
type Target struct {
	targetType TargetType
	id         string
}

// NewTarget validates UUID-backed resources and bounded system identifiers.
func NewTarget(targetType TargetType, id string) (Target, error) {
	if !targetType.valid() || !validEntityID(targetType == TargetSystem, id) {
		return Target{}, ErrInvalidInput
	}
	return Target{targetType: targetType, id: id}, nil
}

func (target Target) valid() bool {
	return target.targetType.valid() && validEntityID(target.targetType == TargetSystem, target.id)
}

// Type returns the closed resource category encoded in canonical protobuf.
func (target Target) Type() TargetType { return target.targetType }

// ID returns the non-PII stable target identifier.
func (target Target) ID() string { return target.id }

// Action is the versioned closed set mirrored by platform.audit.v1.AuditAction.
type Action int32

const (
	ActionIdentityOnboarded         Action = 1
	ActionIdentityRecovered         Action = 2
	ActionRecoveryCodeRotated       Action = 3
	ActionDeviceRevoked             Action = 4
	ActionUsernameChanged           Action = 5
	ActionUsernameForceChanged      Action = 6
	ActionUserSuspended             Action = 7
	ActionUserUnsuspended           Action = 8
	ActionUserDeleted               Action = 9
	ActionAssistedRecoveryCreated   Action = 10
	ActionRealNameRead              Action = 11
	ActionRealNameUpdated           Action = 12
	ActionProfileExportCreated      Action = 13
	ActionProfileExportPageRead     Action = 14
	ActionProfileExportCompleted    Action = 15
	ActionProfileExportAborted      Action = 16
	ActionProfileExportExpired      Action = 17
	ActionAdminSetupCompleted       Action = 18
	ActionAdminPasswordChanged      Action = 19
	ActionAdminTOTPRebound          Action = 20
	ActionAdminRecoveryUsed         Action = 21
	ActionAdminSessionsRevoked      Action = 22
	ActionAdminOfflineReset         Action = 23
	ActionAuditEventsRead           Action = 24
	ActionKeyRotationStarted        Action = 25
	ActionKeyRotationBatchCompleted Action = 26
	ActionKeyRotationCompleted      Action = 27
)

// Valid prevents unspecified or future wire values from entering the current canonical schema.
func (action Action) Valid() bool {
	switch action {
	case ActionIdentityOnboarded, ActionIdentityRecovered, ActionRecoveryCodeRotated, ActionDeviceRevoked,
		ActionUsernameChanged, ActionUsernameForceChanged, ActionUserSuspended, ActionUserUnsuspended,
		ActionUserDeleted, ActionAssistedRecoveryCreated, ActionRealNameRead, ActionRealNameUpdated,
		ActionProfileExportCreated, ActionProfileExportPageRead, ActionProfileExportCompleted,
		ActionProfileExportAborted, ActionProfileExportExpired, ActionAdminSetupCompleted,
		ActionAdminPasswordChanged, ActionAdminTOTPRebound, ActionAdminRecoveryUsed,
		ActionAdminSessionsRevoked, ActionAdminOfflineReset, ActionAuditEventsRead,
		ActionKeyRotationStarted, ActionKeyRotationBatchCompleted, ActionKeyRotationCompleted:
		return true
	default:
		return false
	}
}

// EventInput contains the caller-owned facts; chain position and signing version are assigned by Service.
type EventInput struct {
	EventID      uuid.UUID
	RequestID    string
	OccurredAt   time.Time
	Actor        Actor
	Target       Target
	Action       Action
	ReasonCode   string
	DetailDigest []byte
}

func (input EventInput) validate() error {
	if input.EventID == uuid.Nil || !validRequestID(input.RequestID) || input.OccurredAt.IsZero() ||
		!input.Actor.valid() || !input.Target.valid() || !input.Action.Valid() || !validReasonCode(input.ReasonCode) ||
		(len(input.DetailDigest) != 0 && len(input.DetailDigest) != HashSize) {
		return ErrInvalidInput
	}
	return nil
}

// EventSnapshot is the persistence-neutral, versioned canonical event value.
type EventSnapshot struct {
	SchemaVersion     uint32
	ChainID           ChainID
	EventID           uuid.UUID
	Sequence          uint64
	PreviousHash      Hash
	RequestID         string
	OccurredAt        time.Time
	Actor             Actor
	Target            Target
	Action            Action
	ReasonCode        string
	DetailDigest      []byte
	SigningKeyVersion uint32
}

// Event is an immutable validated canonical event.
type Event struct{ snapshot EventSnapshot }

// RestoreEvent validates values read from persistence before verification.
func RestoreEvent(snapshot EventSnapshot) (Event, error) {
	snapshot.OccurredAt = canonicalTime(snapshot.OccurredAt)
	snapshot.DetailDigest = bytes.Clone(snapshot.DetailDigest)
	input := EventInput{EventID: snapshot.EventID, RequestID: snapshot.RequestID, OccurredAt: snapshot.OccurredAt,
		Actor: snapshot.Actor, Target: snapshot.Target, Action: snapshot.Action, ReasonCode: snapshot.ReasonCode,
		DetailDigest: snapshot.DetailDigest}
	if snapshot.SchemaVersion != SchemaVersion || !snapshot.ChainID.Valid() || snapshot.Sequence == 0 ||
		snapshot.SigningKeyVersion == 0 || input.validate() != nil {
		return Event{}, ErrInvalidInput
	}
	return Event{snapshot: snapshot}, nil
}

// Snapshot returns a deep copy so adapters cannot mutate signed content.
func (event Event) Snapshot() EventSnapshot {
	snapshot := event.snapshot
	snapshot.DetailDigest = bytes.Clone(snapshot.DetailDigest)
	return snapshot
}

// SignedEventSnapshot contains the exact bytes persisted by append_audit_event.
type SignedEventSnapshot struct {
	Event          EventSnapshot
	CanonicalEvent []byte
	EventHash      Hash
	Signature      []byte
}

// SignedEvent is an immutable event envelope whose integrity is checked by Service.Verify.
type SignedEvent struct{ snapshot SignedEventSnapshot }

// RestoreSignedEvent accepts structurally valid persisted bytes; Verify performs canonical and cryptographic checks.
func RestoreSignedEvent(snapshot SignedEventSnapshot) (SignedEvent, error) {
	event, err := RestoreEvent(snapshot.Event)
	if err != nil || len(snapshot.CanonicalEvent) == 0 || len(snapshot.Signature) != SignatureSize {
		return SignedEvent{}, ErrInvalidInput
	}
	snapshot.Event = event.Snapshot()
	snapshot.CanonicalEvent = bytes.Clone(snapshot.CanonicalEvent)
	snapshot.Signature = bytes.Clone(snapshot.Signature)
	return SignedEvent{snapshot: snapshot}, nil
}

// ParseSignedEvent restores strict deterministic canonical bytes and their separately persisted integrity metadata.
// Callers must additionally compare redundant database columns, then call Service.Verify for hash/signature authenticity.
func ParseSignedEvent(canonical, eventHash, signature []byte) (SignedEvent, error) {
	if len(canonical) == 0 || len(canonical) > MaxCanonicalEventBytes || len(eventHash) != HashSize || len(signature) != SignatureSize {
		return SignedEvent{}, ErrIntegrity
	}
	message := &auditv1.AuditEvent{}
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(canonical, message); err != nil ||
		len(message.ProtoReflect().GetUnknown()) != 0 || message.OccurredAt == nil || message.OccurredAt.CheckValid() != nil ||
		message.Actor == nil || message.Target == nil {
		return SignedEvent{}, ErrIntegrity
	}
	eventID, err := uuid.Parse(message.EventId)
	if err != nil || eventID.String() != message.EventId {
		return SignedEvent{}, ErrIntegrity
	}
	previousHash, err := NewHash(message.PreviousHash)
	if err != nil {
		return SignedEvent{}, ErrIntegrity
	}
	parsedEventHash, err := NewHash(eventHash)
	if err != nil {
		return SignedEvent{}, ErrIntegrity
	}
	actor, err := NewActor(ActorType(message.Actor.Type), message.Actor.ActorId)
	if err != nil {
		return SignedEvent{}, ErrIntegrity
	}
	target, err := NewTarget(TargetType(message.Target.Type), message.Target.TargetId)
	if err != nil {
		return SignedEvent{}, ErrIntegrity
	}
	event, err := RestoreEvent(EventSnapshot{
		SchemaVersion: message.SchemaVersion, ChainID: ChainID(message.ChainId), EventID: eventID,
		Sequence: message.Sequence, PreviousHash: previousHash, RequestID: message.RequestId,
		OccurredAt: message.OccurredAt.AsTime(), Actor: actor, Target: target, Action: Action(message.Action),
		ReasonCode: message.ReasonCode, DetailDigest: message.DetailDigest, SigningKeyVersion: message.SigningKeyVersion,
	})
	if err != nil {
		return SignedEvent{}, ErrIntegrity
	}
	remarshaled, err := canonicalEvent(event.Snapshot())
	if err != nil || !bytes.Equal(canonical, remarshaled) {
		return SignedEvent{}, ErrIntegrity
	}
	parsed, err := RestoreSignedEvent(SignedEventSnapshot{Event: event.Snapshot(), CanonicalEvent: canonical,
		EventHash: parsedEventHash, Signature: signature})
	if err != nil {
		return SignedEvent{}, ErrIntegrity
	}
	return parsed, nil
}

// Snapshot returns a deep copy for restricted persistence adapters.
func (event SignedEvent) Snapshot() SignedEventSnapshot {
	snapshot := event.snapshot
	snapshot.Event.DetailDigest = bytes.Clone(snapshot.Event.DetailDigest)
	snapshot.CanonicalEvent = bytes.Clone(snapshot.CanonicalEvent)
	snapshot.Signature = bytes.Clone(snapshot.Signature)
	return snapshot
}

// NextHead derives the position returned by a successful append.
func (event SignedEvent) NextHead() (Head, error) {
	snapshot := event.snapshot
	return RestoreHead(HeadSnapshot{ChainID: snapshot.Event.ChainID, Sequence: snapshot.Event.Sequence,
		Hash: snapshot.EventHash, UpdatedAt: snapshot.Event.OccurredAt})
}

func validEntityID(system bool, value string) bool {
	if system {
		return len(value) > 0 && len(value) <= 128 && systemIDPattern.MatchString(value)
	}
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func validRequestID(value string) bool {
	return len(value) > 0 && len(value) <= MaxRequestIDBytes && requestIDPattern.MatchString(value)
}

func validReasonCode(value string) bool {
	return value == "" || len(value) <= MaxReasonCodeBytes && reasonCodePattern.MatchString(value)
}

func canonicalTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC().Truncate(time.Microsecond)
}
