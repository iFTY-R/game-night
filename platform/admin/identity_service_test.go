package admin

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/profile"
)

func TestAuditPageTokenIsBoundToCanonicalFilters(t *testing.T) {
	command := ListAuditEventsCommand{
		ActorAdminID: uuid.New(), TargetUserID: uuid.New(),
		Actions:   []audit.Action{audit.ActionUserDeleted, audit.ActionUserSuspended},
		StartedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		EndedAt:   time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
	}
	digest := auditFilterDigest(command)
	token := encodeAuditPageToken(42, digest)
	sequence, err := decodeAuditPageToken(token, digest)
	if err != nil || sequence != 42 {
		t.Fatalf("token round trip failed: sequence=%d err=%v", sequence, err)
	}

	reordered := command
	reordered.Actions = []audit.Action{audit.ActionUserSuspended, audit.ActionUserDeleted}
	if !bytes.Equal(digest, auditFilterDigest(reordered)) {
		t.Fatal("action order changed the canonical filter digest")
	}
	changed := command
	changed.TargetUserID = uuid.New()
	if _, err := decodeAuditPageToken(token, auditFilterDigest(changed)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("token was accepted under a different filter: %v", err)
	}
}

func TestProfileFilterDigestIsOrderIndependentAndScopeSensitive(t *testing.T) {
	first, second := uuid.New(), uuid.New()
	left := ProfileExportFilter{UserIDs: []uuid.UUID{first, second}, Statuses: []identity.UserStatus{identity.UserStatusSuspended, identity.UserStatusActive}}
	right := ProfileExportFilter{UserIDs: []uuid.UUID{second, first}, Statuses: []identity.UserStatus{identity.UserStatusActive, identity.UserStatusSuspended}}
	leftDigest := profileFilterDigest(left, []profile.Field{profile.FieldRealName})
	rightDigest := profileFilterDigest(right, []profile.Field{profile.FieldRealName})
	if !bytes.Equal(leftDigest, rightDigest) {
		t.Fatal("filter ordering changed the export scope digest")
	}
	right.Statuses = []identity.UserStatus{identity.UserStatusActive}
	if bytes.Equal(leftDigest, profileFilterDigest(right, []profile.Field{profile.FieldRealName})) {
		t.Fatal("different export scope reused the same digest")
	}
}

func TestAdminReasonRejectsControlsAndKeepsOperatorTextOutOfReasonCode(t *testing.T) {
	if _, err := normalizeAdminReason("  "); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("blank reason accepted: %v", err)
	}
	if _, err := normalizeAdminReason("line\nbreak"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("control character accepted: %v", err)
	}
	value, err := normalizeAdminReason("用户申诉核验")
	if err != nil || value != "用户申诉核验" {
		t.Fatalf("valid operator reason rejected: value=%q err=%v", value, err)
	}
}
