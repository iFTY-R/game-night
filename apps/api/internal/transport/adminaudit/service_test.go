package adminaudit

import (
	"bytes"
	"testing"
	"time"

	"github.com/google/uuid"
	adminv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1"
	auditv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/audit/v1"
	domain "github.com/iFTY-R/game-night/platform/admin/auditlog"
	"github.com/iFTY-R/game-night/platform/audit"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestFilterFromWireMapsEverySafeAuditField(t *testing.T) {
	eventID := uuid.New()
	from := time.Date(2026, time.July, 26, 8, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	filter, err := filterFromWire(&adminv1.AdminAuditFilter{
		EventId: eventID.String(), Actions: []auditv1.AuditAction{auditv1.AuditAction_AUDIT_ACTION_USER_SUSPENDED},
		ActorTypes: []auditv1.AuditActorType{auditv1.AuditActorType_AUDIT_ACTOR_TYPE_ADMIN}, ActorId: uuid.NewString(),
		TargetTypes: []auditv1.AuditTargetType{auditv1.AuditTargetType_AUDIT_TARGET_TYPE_USER}, TargetId: uuid.NewString(),
		RequestId: "request-123", ReasonCode: "admin.user.suspend", OccurredFrom: timestamppb.New(from), OccurredTo: timestamppb.New(to),
	})
	if err != nil {
		t.Fatal(err)
	}
	if filter.EventID != eventID || len(filter.Actions) != 1 || filter.Actions[0] != audit.ActionUserSuspended ||
		len(filter.ActorTypes) != 1 || filter.ActorTypes[0] != audit.ActorAdmin || len(filter.TargetTypes) != 1 || filter.TargetTypes[0] != audit.TargetUser ||
		!filter.OccurredFrom.Equal(from) || !filter.OccurredTo.Equal(to) {
		t.Fatalf("filter = %+v", filter)
	}
}

func TestFilterFromWireRejectsUnknownAuditEnum(t *testing.T) {
	_, err := filterFromWire(&adminv1.AdminAuditFilter{Actions: []auditv1.AuditAction{auditv1.AuditAction(999)}})
	if err == nil {
		t.Fatal("filterFromWire unexpectedly accepted unknown action")
	}
}

func TestAuditEventToWireMapsOnlyRedactedMetadata(t *testing.T) {
	previous, err := audit.NewHash(bytes.Repeat([]byte{0x11}, audit.HashSize))
	if err != nil {
		t.Fatal(err)
	}
	eventHash, err := audit.NewHash(bytes.Repeat([]byte{0x22}, audit.HashSize))
	if err != nil {
		t.Fatal(err)
	}
	row := domain.Event{
		EventID: uuid.New(), Sequence: 42, PreviousHash: previous, EventHash: eventHash, RequestID: "request-42",
		OccurredAt: time.Date(2026, time.July, 26, 11, 0, 0, 0, time.UTC), ActorType: audit.ActorAdmin, ActorID: uuid.NewString(),
		TargetType: audit.TargetUser, TargetID: uuid.NewString(), Action: audit.ActionUserSuspended, ReasonCode: "admin.user.suspend",
		SigningKeyVersion: 3, Verified: true,
	}
	copy(row.DetailDigest[:], bytes.Repeat([]byte{0x33}, len(row.DetailDigest)))

	wire := auditEventToWire(row)
	if wire.GetEventId() != row.EventID.String() || wire.GetSequence() != row.Sequence || wire.GetPreviousHash() != previous.Hex() || wire.GetEventHash() != eventHash.Hex() {
		t.Fatalf("wire identity = %+v", wire)
	}
	if wire.GetActor().GetType() != auditv1.AuditActorType_AUDIT_ACTOR_TYPE_ADMIN || wire.GetTarget().GetType() != auditv1.AuditTargetType_AUDIT_TARGET_TYPE_USER ||
		wire.GetAction() != auditv1.AuditAction_AUDIT_ACTION_USER_SUSPENDED || wire.GetDetailDigest() == "" || !wire.GetVerified() {
		t.Fatalf("wire redacted fields = %+v", wire)
	}
}
