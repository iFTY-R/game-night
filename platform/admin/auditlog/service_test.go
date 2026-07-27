package auditlog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	admin "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/security"
)

func TestServiceFiltersEventsAndBindsContinuationToFilter(t *testing.T) {
	now := time.Date(2026, time.July, 26, 14, 0, 0, 0, time.UTC)
	actorID := uuid.New()
	reader := &memoryReader{events: verifiedEvents(
		testSignedEvent(t, 1, now.Add(-3*time.Minute), actorID, audit.ActionUserSuspended, "admin.user.suspend"),
		testSignedEvent(t, 2, now.Add(-2*time.Minute), actorID, audit.ActionUserUnsuspended, "admin.user.unsuspend"),
		testSignedEvent(t, 3, now.Add(-time.Minute), actorID, audit.ActionUserSuspended, "admin.user.suspend"),
	)}
	reader.head = testHead(t, 3, now)
	service := newTestService(t, reader, now)
	actor := newTestActor(t, now, admin.PermissionAuditRead)

	first, err := service.ListEvents(context.Background(), actor, ListInput{Filter: Filter{Actions: []audit.Action{audit.ActionUserSuspended}}, PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 1 || first.Events[0].Sequence != 1 || first.NextPageToken == "" || first.ScannedEvents != 1 {
		t.Fatalf("first page = %+v", first)
	}

	second, err := service.ListEvents(context.Background(), actor, ListInput{
		Filter: Filter{Actions: []audit.Action{audit.ActionUserSuspended}}, PageSize: 1, PageToken: first.NextPageToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 1 || second.Events[0].Sequence != 3 || second.ScannedEvents != 2 || second.NextPageToken != "" {
		t.Fatalf("second page = %+v", second)
	}
	if _, err = service.ListEvents(context.Background(), actor, ListInput{
		Filter: Filter{Actions: []audit.Action{audit.ActionUserUnsuspended}}, PageSize: 1, PageToken: first.NextPageToken,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cross-filter cursor error = %v", err)
	}
}

// A historical event whose signature no longer validates (lost key or tampering) must be listed
// with Verified=false instead of failing the whole page and making the audit view unusable.
func TestServiceSurfacesUnverifiedEventsInsteadOfFailing(t *testing.T) {
	now := time.Date(2026, time.July, 26, 14, 0, 0, 0, time.UTC)
	actorID := uuid.New()
	reader := &memoryReader{events: []ReadEvent{
		{Event: testSignedEvent(t, 1, now.Add(-2*time.Minute), actorID, audit.ActionUserSuspended, "admin.user.suspend"), Verified: false},
		{Event: testSignedEvent(t, 2, now.Add(-time.Minute), actorID, audit.ActionUserUnsuspended, "admin.user.unsuspend"), Verified: true},
	}}
	reader.head = testHead(t, 2, now)
	service := newTestService(t, reader, now)
	actor := newTestActor(t, now, admin.PermissionAuditRead)

	page, err := service.ListEvents(context.Background(), actor, ListInput{PageSize: 10})
	if err != nil || len(page.Events) != 2 {
		t.Fatalf("mixed verification page = %+v err=%v", page, err)
	}
	if page.Events[0].Verified || !page.Events[1].Verified {
		t.Fatalf("verification flags = %v %v", page.Events[0].Verified, page.Events[1].Verified)
	}
}

func TestServiceRequiresAuditReadPermission(t *testing.T) {
	now := time.Date(2026, time.July, 26, 14, 30, 0, 0, time.UTC)
	reader := &memoryReader{head: testHead(t, 1, now)}
	service := newTestService(t, reader, now)
	if _, err := service.ListEvents(context.Background(), newTestActor(t, now, admin.PermissionUsersRead), ListInput{}); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("permission error = %v", err)
	}
}

func TestCursorCodecRejectsTamperingAndCrossFilter(t *testing.T) {
	now := time.Date(2026, time.July, 26, 15, 0, 0, 0, time.UTC)
	codec := newTestCursor(t, now)
	filter := sha256.Sum256([]byte("action=suspend"))
	token, err := codec.Encode(filter, 42)
	if err != nil {
		t.Fatal(err)
	}
	if sequence, decodeErr := codec.Decode(token, filter); decodeErr != nil || sequence != 42 {
		t.Fatalf("cursor round trip sequence=%d err=%v", sequence, decodeErr)
	}
	if _, err = codec.Decode(token, sha256.Sum256([]byte("action=unsuspend"))); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cross-filter error = %v", err)
	}
	tamperedSuffix := "A"
	if token[len(token)-1] == 'A' {
		tamperedSuffix = "B"
	}
	if _, err = codec.Decode(token[:len(token)-1]+tamperedSuffix, filter); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("tampered cursor error = %v", err)
	}
}

type memoryReader struct {
	head     audit.Head
	events   []ReadEvent
	requests []audit.ListRequest
}

func (reader *memoryReader) ReadHead(context.Context, audit.ChainID) (audit.Head, error) {
	return reader.head, nil
}

func (reader *memoryReader) List(_ context.Context, request audit.ListRequest) ([]ReadEvent, error) {
	reader.requests = append(reader.requests, request)
	result := make([]ReadEvent, 0, request.PageSize)
	for _, event := range reader.events {
		if event.Event.Snapshot().Event.Sequence <= request.AfterSequence {
			continue
		}
		result = append(result, event)
		if uint32(len(result)) == request.PageSize {
			break
		}
	}
	return result, nil
}

// verifiedEvents adapts signed fixtures to the reader contract with every signature verified.
func verifiedEvents(events ...audit.SignedEvent) []ReadEvent {
	result := make([]ReadEvent, 0, len(events))
	for _, event := range events {
		result = append(result, ReadEvent{Event: event, Verified: true})
	}
	return result
}

func newTestService(t testing.TB, reader Reader, now time.Time) *Service {
	t.Helper()
	service, err := NewService(Config{Reader: reader, Cursor: newTestCursor(t, now), Clock: clock.NewFake(now)})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func newTestCursor(t testing.TB, now time.Time) *CursorCodec {
	t.Helper()
	path := filepath.Join(t.TempDir(), "admin-audit-cursor.json")
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xA7}, 32))
	document := fmt.Sprintf(`{"active_version":1,"keys":[{"version":1,"key":%q,"not_before":"%s"}]}`,
		key, now.Add(-time.Hour).Format(time.RFC3339Nano))
	if err := os.WriteFile(path, []byte(document), 0o400); err != nil {
		t.Fatal(err)
	}
	keyring, err := security.LoadHMACKeyring[security.AdminCursorKeyPurpose](path, now)
	if err != nil {
		t.Fatal(err)
	}
	codec, err := NewCursorCodec(keyring)
	if err != nil {
		t.Fatal(err)
	}
	return codec
}

func newTestActor(t testing.TB, now time.Time, permissions ...admin.Permission) admin.ActorContext {
	t.Helper()
	adminID, sessionID := uuid.New(), uuid.New()
	session, err := admin.RestoreSession(admin.SessionSnapshot{
		ID: sessionID, AdminID: adminID, Selector: base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
		SecretMAC: security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: 1, Value: make([]byte, sha256.Size)},
		CSRFHash:  security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: 1, Value: make([]byte, sha256.Size)},
		Kind:      admin.SessionKindFull, AdminVersion: 1, PasswordVersion: 1, SessionVersion: 1, MaxAttempts: 5,
		ClientIP: "203.0.113.10", UserAgent: "admin-audit-test", CreatedAt: now.Add(-time.Minute), LastSeenAt: now,
		IdleExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	permissionSet, err := admin.NewPermissionSet(permissions...)
	if err != nil {
		t.Fatal(err)
	}
	elevations, err := admin.NewElevationSet()
	if err != nil {
		t.Fatal(err)
	}
	actor, err := admin.NewActorContext(adminID, sessionID, session, permissionSet, elevations, 0, "req-admin-audit", "http://127.0.0.1:4174", "203.0.113.10", "admin-audit-test")
	if err != nil {
		t.Fatal(err)
	}
	return actor
}

func testHead(t testing.TB, sequence uint64, updatedAt time.Time) audit.Head {
	t.Helper()
	hash, err := audit.NewHash(bytes.Repeat([]byte{byte(sequence + 1)}, audit.HashSize))
	if err != nil {
		t.Fatal(err)
	}
	head, err := audit.RestoreHead(audit.HeadSnapshot{ChainID: audit.ChainAdmin, Sequence: sequence, Hash: hash, UpdatedAt: updatedAt})
	if err != nil {
		t.Fatal(err)
	}
	return head
}

func testSignedEvent(t testing.TB, sequence uint64, occurredAt time.Time, actorID uuid.UUID, action audit.Action, reasonCode string) audit.SignedEvent {
	t.Helper()
	actor, err := audit.NewActor(audit.ActorAdmin, actorID.String())
	if err != nil {
		t.Fatal(err)
	}
	target, err := audit.NewTarget(audit.TargetUser, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	hash, err := audit.NewHash(bytes.Repeat([]byte{byte(sequence + 10)}, audit.HashSize))
	if err != nil {
		t.Fatal(err)
	}
	signed, err := audit.RestoreSignedEvent(audit.SignedEventSnapshot{
		Event: audit.EventSnapshot{
			SchemaVersion: audit.SchemaVersion, ChainID: audit.ChainAdmin, EventID: uuid.New(), Sequence: sequence,
			PreviousHash: audit.GenesisHash, RequestID: fmt.Sprintf("request-%d", sequence), OccurredAt: occurredAt,
			Actor: actor, Target: target, Action: action, ReasonCode: reasonCode, DetailDigest: bytes.Repeat([]byte{byte(sequence + 20)}, audit.HashSize), SigningKeyVersion: 1,
		},
		CanonicalEvent: []byte{byte(sequence)}, EventHash: hash, Signature: bytes.Repeat([]byte{0x7F}, audit.SignatureSize),
	})
	if err != nil {
		t.Fatal(err)
	}
	return signed
}
