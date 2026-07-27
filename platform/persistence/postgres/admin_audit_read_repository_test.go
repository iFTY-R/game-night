package postgres

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	adminaudit "github.com/iFTY-R/game-night/platform/admin/auditlog"
	"github.com/iFTY-R/game-night/platform/audit"
)

func TestRecentHighRiskOperationsReturnsOnlyVerifiedAdminEventsInsideWindow(t *testing.T) {
	now := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	adminID := uuid.New()
	userID := uuid.New()
	events := []adminaudit.ReadEvent{
		{Event: overviewAuditEvent(t, 1, now.Add(-2*time.Hour), audit.ActorAdmin, adminID, audit.ActionAdminMaintenanceChanged), Verified: true},
		{Event: overviewAuditEvent(t, 2, now.Add(-30*time.Minute), audit.ActorAdmin, adminID, audit.ActionAdminLoginSucceeded), Verified: true},
		{Event: overviewAuditEvent(t, 3, now.Add(-20*time.Minute), audit.ActorUser, userID, audit.ActionUserSuspended), Verified: true},
		{Event: overviewAuditEvent(t, 4, now.Add(-10*time.Minute), audit.ActorAdmin, adminID, audit.ActionUserDeleted), Verified: false},
		{Event: overviewAuditEvent(t, 5, now.Add(-5*time.Minute), audit.ActorAdmin, adminID, audit.ActionAdminCacheRefreshed), Verified: true},
	}

	result, err := recentHighRiskOperations(events, now.Add(-time.Hour), now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].Action != audit.ActionAdminCacheRefreshed || result[0].ActorAdminID != adminID || !result[0].Verified {
		t.Fatalf("recent high-risk operations = %+v", result)
	}
}

func TestRecentHighRiskOperationsUsesNewestFirstLimit(t *testing.T) {
	now := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	adminID := uuid.New()
	events := []adminaudit.ReadEvent{
		{Event: overviewAuditEvent(t, 1, now.Add(-2*time.Minute), audit.ActorAdmin, adminID, audit.ActionUserSuspended), Verified: true},
		{Event: overviewAuditEvent(t, 2, now.Add(-time.Minute), audit.ActorAdmin, adminID, audit.ActionUserUnsuspended), Verified: true},
	}
	result, err := recentHighRiskOperations(events, now.Add(-time.Hour), now, 1)
	if err != nil || len(result) != 1 || result[0].Action != audit.ActionUserUnsuspended {
		t.Fatalf("limited high-risk operations = %+v, %v", result, err)
	}
}

func overviewAuditEvent(t testing.TB, sequence uint64, occurredAt time.Time, actorType audit.ActorType, actorID uuid.UUID, action audit.Action) audit.SignedEvent {
	t.Helper()
	actor, err := audit.NewActor(actorType, actorID.String())
	if err != nil {
		t.Fatal(err)
	}
	target, err := audit.NewTarget(audit.TargetSystem, "overview-test-target")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := audit.NewHash(bytes.Repeat([]byte{byte(sequence + 1)}, audit.HashSize))
	if err != nil {
		t.Fatal(err)
	}
	event, err := audit.RestoreSignedEvent(audit.SignedEventSnapshot{
		Event: audit.EventSnapshot{
			SchemaVersion: audit.SchemaVersion, ChainID: audit.ChainAdmin, EventID: uuid.New(), Sequence: sequence,
			PreviousHash: audit.GenesisHash, RequestID: fmt.Sprintf("overview-%d", sequence), OccurredAt: occurredAt,
			Actor: actor, Target: target, Action: action, ReasonCode: "overview.test",
			DetailDigest: bytes.Repeat([]byte{byte(sequence + 2)}, audit.HashSize), SigningKeyVersion: 1,
		},
		CanonicalEvent: []byte{byte(sequence)}, EventHash: hash, Signature: bytes.Repeat([]byte{0x4A}, audit.SignatureSize),
	})
	if err != nil {
		t.Fatal(err)
	}
	return event
}
