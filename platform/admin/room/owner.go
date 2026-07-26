package room

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// OwnerReader samples realtime owner leases without exposing mutation tokens to the admin domain.
type OwnerReader interface {
	ReadOwners(context.Context, []uuid.UUID, time.Time) (map[uuid.UUID]OwnerLeaseSummary, error)
}

// OwnerRepairer mutates only one reviewed realtime lease after the service revalidates the dry-run digest.
type OwnerRepairer interface {
	ClearStaleOwnerLease(context.Context, OwnerLeaseSummary) (bool, error)
}

// ownerMissing is used when a concrete session has no Redis lease at the sampled instant.
func ownerMissing(sessionID uuid.UUID, observedAt time.Time) OwnerLeaseSummary {
	return OwnerLeaseSummary{SessionID: sessionID, Freshness: OwnerFreshnessMissing, ObservedAt: observedAt}
}

// ownerUnknown is used when Redis cannot be sampled; callers surface it as stale so controls fail closed.
func ownerUnknown(sessionID uuid.UUID, observedAt time.Time) OwnerLeaseSummary {
	return OwnerLeaseSummary{SessionID: sessionID, Freshness: OwnerFreshnessUnknown, ObservedAt: observedAt}
}

// evaluateOwner compares non-authoritative Redis liveness with the PostgreSQL fencing epoch for one session.
func evaluateOwner(sessionID uuid.UUID, expectedEpoch uint64, owners map[uuid.UUID]OwnerLeaseSummary, observedAt time.Time) OwnerLeaseSummary {
	if sessionID == uuid.Nil {
		return OwnerLeaseSummary{}
	}
	owner, ok := owners[sessionID]
	if !ok {
		return ownerMissing(sessionID, observedAt)
	}
	if owner.SessionID == uuid.Nil {
		owner.SessionID = sessionID
	}
	if owner.ObservedAt.IsZero() {
		owner.ObservedAt = observedAt
	}
	if owner.Freshness == "" {
		owner.Freshness = OwnerFreshnessUnknown
	}
	if owner.Freshness == OwnerFreshnessFresh && owner.OwnershipEpoch != expectedEpoch {
		owner.Freshness = OwnerFreshnessStale
	}
	return owner
}
