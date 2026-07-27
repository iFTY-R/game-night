package auditlog

import (
	"context"
	"crypto/sha256"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/audit"
)

const (
	// DefaultPageSize keeps the initial audit view useful while bounding signature verification work.
	DefaultPageSize uint32 = 50
	// MaximumPageSize protects the read path from unbounded redacted event materialization.
	MaximumPageSize uint32 = 100
	// MaximumScanEvents bounds server-side filter scans when actor/action metadata is only available in signed payloads.
	MaximumScanEvents uint32 = 1000
)

// Reader is the minimal read-only audit-chain port. Implementations must attempt signature
// verification for every returned event and report the outcome per event; structural corruption
// (unparseable or column-inconsistent rows) must still fail the read outright.
type Reader interface {
	ReadHead(context.Context, audit.ChainID) (audit.Head, error)
	List(context.Context, audit.ListRequest) ([]ReadEvent, error)
}

// ReadEvent pairs one structurally valid chain event with its signature verification outcome.
// Verified=false is an integrity alarm (tampering or a lost historical signing key); the event is
// still surfaced so operators can inspect the affected range instead of losing the entire view.
type ReadEvent struct {
	Event    audit.SignedEvent
	Verified bool
}

// Filter describes the redacted facts an operator may use to locate audit events.
type Filter struct {
	EventID      uuid.UUID
	Actions      []audit.Action
	ActorTypes   []audit.ActorType
	ActorID      string
	TargetTypes  []audit.TargetType
	TargetID     string
	RequestID    string
	ReasonCode   string
	OccurredFrom time.Time
	OccurredTo   time.Time
}

// Event contains only signed, redacted audit metadata. Canonical payload and signature bytes remain internal.
type Event struct {
	EventID           uuid.UUID
	Sequence          uint64
	PreviousHash      audit.Hash
	EventHash         audit.Hash
	RequestID         string
	OccurredAt        time.Time
	ActorType         audit.ActorType
	ActorID           string
	TargetType        audit.TargetType
	TargetID          string
	Action            audit.Action
	ReasonCode        string
	DetailDigest      [sha256.Size]byte
	SigningKeyVersion uint32
	// Verified is false when the stored signature does not validate against the current keyring;
	// consumers must render such events as untrusted rather than hiding them.
	Verified bool
}

// Page is one HMAC-bound redacted audit page plus the trusted chain head observed by the repository.
type Page struct {
	Events        []Event
	NextPageToken string
	SampledAt     time.Time
	ChainHead     audit.Head
	ScannedEvents uint32
}
