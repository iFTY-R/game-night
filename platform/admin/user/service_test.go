package user

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	admin "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/profile"
	"github.com/iFTY-R/game-night/platform/security"
)

func TestServiceListUsersBindsCursorToNormalizedFilter(t *testing.T) {
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	repository := &memoryRepository{users: []UserRecord{
		{ID: uuid.MustParse("10000000-0000-0000-0000-000000000001"), Status: "active", Username: "alice", CurrentUsernameKey: "alice", Version: 1, CreatedAt: now.Add(-3 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour), LastActivityAt: now.Add(-2 * time.Hour)},
		{ID: uuid.MustParse("10000000-0000-0000-0000-000000000002"), Status: "active", Username: "bob", CurrentUsernameKey: "bob", Version: 1, CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-time.Hour), LastActivityAt: now.Add(-time.Hour)},
	}}
	service := newTestService(t, repository, nil, nil, nil, now)
	actor := newTestActor(t, now, admin.PermissionUsersRead)

	first, err := service.ListUsers(context.Background(), actor, ListUsersInput{Statuses: []string{"active"}, PageSize: 1})
	if err != nil || len(first.Users) != 1 || first.NextPageToken == "" {
		t.Fatalf("first page = %+v err=%v", first, err)
	}
	if _, err = service.ListUsers(context.Background(), actor, ListUsersInput{Statuses: []string{"suspended"}, PageSize: 1, PageToken: first.NextPageToken}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cross-filter cursor error = %v", err)
	}
	if len(repository.queries) != 1 || repository.queries[0].SampledAt.IsZero() {
		t.Fatalf("unexpected repository queries: %+v", repository.queries)
	}
}

func TestServiceGetUserNeverReturnsPIIPlaintext(t *testing.T) {
	now := time.Date(2026, 7, 26, 11, 0, 0, 0, time.UTC)
	userID := uuid.New()
	repository := &memoryRepository{
		users: []UserRecord{{ID: userID, Status: "active", Username: "alice", CurrentUsernameKey: "alice", Version: 7, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute), LastActivityAt: now.Add(-time.Minute)}},
		notes: []Note{{ID: uuid.New(), UserID: userID, AuthorAdminID: uuid.New(), Body: "review note", Reason: "case review", Version: 1, CreatedAt: now.Add(-time.Minute)}},
	}
	profiles := &memoryProfiles{profiles: map[uuid.UUID]profile.UserProfile{userID: newEncryptedProfile(t, userID, "Alice Secret", now)}}
	service := newTestService(t, repository, profiles, nil, nil, now)
	actor := newTestActor(t, now, admin.PermissionUsersRead)

	detail, err := service.GetUser(context.Background(), actor, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.PIIAvailability) != 1 || !detail.PIIAvailability[0].Available || detail.PIIAvailability[0].Field != profile.FieldRealName {
		t.Fatalf("PII availability missing: %+v", detail.PIIAvailability)
	}
	if detail.Summary.Username == "Alice Secret" || detail.RecentNotes[0].Body == "Alice Secret" {
		t.Fatal("GetUser leaked PII plaintext")
	}
}

func TestServiceGetUserPIIRequiresPermissionReasonAndAudit(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	userID := uuid.New()
	protector := newTestPIIProtector(t, now)
	encrypted, err := protector.EncryptRealName(userID, "Alice Secret")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := profile.NewUserProfile(userID, encrypted, now.Add(-time.Minute), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	repository := &memoryRepository{}
	profiles := &memoryProfiles{profiles: map[uuid.UUID]profile.UserProfile{userID: loaded}}
	audit := &memoryAudit{}
	service := newTestService(t, repository, profiles, protector, audit, now)

	withoutPII := newTestActor(t, now, admin.PermissionUsersRead)
	if _, err = service.GetUserPII(context.Background(), withoutPII, GetUserPIIRequest{UserID: userID, Fields: []profile.Field{profile.FieldRealName}, Reason: "support review"}); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("missing permission error = %v", err)
	}
	withPII := newTestActor(t, now, admin.PermissionUsersReadPII)
	if _, err = service.GetUserPII(context.Background(), withPII, GetUserPIIRequest{UserID: userID, Fields: []profile.Field{profile.FieldRealName}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("missing reason error = %v", err)
	}
	audit.failPII = true
	if _, err = service.GetUserPII(context.Background(), withPII, GetUserPIIRequest{UserID: userID, Fields: []profile.Field{profile.FieldRealName}, Reason: "support review"}); !errors.Is(err, ErrAuditUnavailable) {
		t.Fatalf("audit failure error = %v", err)
	}
	audit.failPII = false
	result, err := service.GetUserPII(context.Background(), withPII, GetUserPIIRequest{UserID: userID, Fields: []profile.Field{profile.FieldRealName}, Reason: "support review"})
	if err != nil || len(result.Values) != 1 || result.Values[0].Value != "Alice Secret" || result.AccessAuditEventID == uuid.Nil {
		t.Fatalf("PII result = %+v err=%v", result, err)
	}
	if len(audit.piiEvents) != 2 || audit.piiEvents[1].ReasonDigest == ([sha256.Size]byte{}) {
		t.Fatalf("PII audit not recorded correctly: %+v", audit.piiEvents)
	}
}

func TestServiceAnnotationsRequirePermissionAuditAndVersion(t *testing.T) {
	now := time.Date(2026, 7, 26, 13, 0, 0, 0, time.UTC)
	userID := uuid.New()
	repository := &memoryRepository{userVersion: 4}
	audit := &memoryAudit{}
	service := newTestService(t, repository, nil, nil, audit, now)
	operationID := newOperationID(t)

	reader := newTestActor(t, now, admin.PermissionUsersRead)
	_, err := service.SetUserTags(context.Background(), reader, SetUserTagsRequest{OperationID: operationID, UserID: userID, TagIDs: []uuid.UUID{uuid.New()}, Reason: "review tag", ExpectedVersion: 4})
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("missing annotate permission error = %v", err)
	}
	annotator := newTestActor(t, now, admin.PermissionUsersAnnotate)
	audit.failAnnotation = true
	_, err = service.AppendUserNote(context.Background(), annotator, AppendUserNoteRequest{OperationID: operationID, UserID: userID, Body: "manual review", Reason: "review note", ExpectedVersion: 4})
	if !errors.Is(err, ErrAuditUnavailable) {
		t.Fatalf("annotation audit failure error = %v", err)
	}
	audit.failAnnotation = false
	note, err := service.AppendUserNote(context.Background(), annotator, AppendUserNoteRequest{OperationID: operationID, UserID: userID, Body: "manual review", Reason: "review note", ExpectedVersion: 4})
	if err != nil || note.Body != "manual review" || repository.userVersion != 5 {
		t.Fatalf("append note = %+v version=%d err=%v", note, repository.userVersion, err)
	}
}

// Operators type hex colors in either case; the service must fold them to the uppercase form the
// PostgreSQL CHECK constraint enforces, and must reject malformed colors before any audit write.
func TestServiceTagColorCanonicalization(t *testing.T) {
	now := time.Date(2026, 7, 26, 13, 0, 0, 0, time.UTC)
	repository := &memoryRepository{}
	audit := &memoryAudit{}
	service := newTestService(t, repository, nil, nil, audit, now)
	annotator := newTestActor(t, now, admin.PermissionUsersAnnotate)
	operationID := newOperationID(t)

	created, err := service.CreateTag(context.Background(), annotator,
		CreateTagCommand{Name: "回归验证标签", Color: " #15803d ", Reason: "统一颜色大小写", ExpectedCatalogVersion: 1}, operationID)
	if err != nil || created.Tag.Color != "#15803D" {
		t.Fatalf("create tag canonical color = %q err=%v", created.Tag.Color, err)
	}
	if len(audit.annotation) != 1 || audit.annotation[0].DetailDigest != digestStrings("回归验证标签", "#15803D") {
		t.Fatalf("create audit digest must bind the canonical color, events=%d", len(audit.annotation))
	}

	updated, err := service.UpdateTag(context.Background(), annotator,
		UpdateTagCommand{TagID: uuid.New(), Name: "回归验证标签", Color: "#2563eb", Reason: "调整颜色", ExpectedVersion: 4}, operationID)
	if err != nil || updated.Color != "#2563EB" {
		t.Fatalf("update tag canonical color = %q err=%v", updated.Color, err)
	}

	auditEvents := len(audit.annotation)
	for _, invalid := range []string{"", "#15803", "15803D", "#15803G", "#15803DFF", "rgb(21,128,61)"} {
		if _, err := service.CreateTag(context.Background(), annotator,
			CreateTagCommand{Name: "x", Color: invalid, Reason: "invalid color", ExpectedCatalogVersion: 1}, operationID); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("color %q must be rejected, err=%v", invalid, err)
		}
	}
	// Version 0 can never match the 1-based catalog or tag versions; it must fail as invalid input
	// before any audit event is produced.
	if _, err := service.CreateTag(context.Background(), annotator,
		CreateTagCommand{Name: "x", Color: "#15803D", Reason: "zero version"}, operationID); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("zero catalog version must be rejected, err=%v", err)
	}
	if _, err := service.UpdateTag(context.Background(), annotator,
		UpdateTagCommand{TagID: uuid.New(), Name: "x", Color: "#15803D", Reason: "zero version"}, operationID); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("zero tag version must be rejected, err=%v", err)
	}
	if _, err := service.DeleteTag(context.Background(), annotator,
		DeleteTagCommand{TagID: uuid.New()}, operationID, "zero version"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("zero delete version must be rejected, err=%v", err)
	}
	if len(audit.annotation) != auditEvents {
		t.Fatalf("invalid colors and zero versions must fail before audit writes, events=%d", len(audit.annotation))
	}
}

func TestServiceStartBatchOperationReplaysAfterPreviewVersionAdvance(t *testing.T) {
	now := time.Date(2026, 7, 26, 14, 0, 0, 0, time.UTC)
	actor := newElevatedTestActor(t, now, admin.ElevationScopeUsersBulkGovernance, admin.PermissionUsersGovern)
	target := BatchExecutableTarget{ItemID: uuid.New(), UserID: uuid.New(), ExpectedUserVersion: 3}
	snapshot, err := marshalBatchSelectionSnapshot(BatchCommandSuspend, now, []BatchExecutableTarget{target})
	if err != nil {
		t.Fatal(err)
	}
	previewDigest := sha256.Sum256([]byte("batch-preview"))
	jobs := &replayBatchRepository{
		preview: BatchPreview{
			ID: uuid.New(), ActorAdminID: actor.AdminID(), Command: string(BatchCommandSuspend),
			SelectionSnapshot: snapshot, SelectionDigest: sha256.Sum256(snapshot), PreviewDigest: previewDigest,
			Version: 2, // The original request already consumed version 1 before this network retry arrives.
		},
		job: BatchJob{ID: uuid.New()},
	}
	service, err := NewService(Config{Repository: &memoryRepository{}, Jobs: jobs, Clock: clock.NewFake(now)})
	if err != nil {
		t.Fatal(err)
	}

	job, err := service.StartBatchUserOperation(context.Background(), actor, StartBatchCommand{
		OperationID: newOperationID(t), PreviewID: jobs.preview.ID, PreviewDigest: previewDigest,
		ExpectedVersion: 1, Reason: "retry after an interrupted response",
	})
	if err != nil || job.ID != jobs.job.ID {
		t.Fatalf("replayed start = %+v err=%v", job, err)
	}
	if len(jobs.starts) != 1 || jobs.starts[0].ExpectedPreviewVersion != 1 {
		t.Fatalf("repository did not receive the original CAS version: %+v", jobs.starts)
	}
}

func TestServiceProcessNextBatchItemCancelsExpiredWorkAfterBatchCancellation(t *testing.T) {
	now := time.Date(2026, 7, 26, 15, 0, 0, 0, time.UTC)
	item := BatchItem{
		ID: uuid.New(), BatchJobID: uuid.New(), UserID: uuid.New(), ExpectedUserVersion: 2,
		State: "running", LeaseOwner: "worker:test", Version: 3,
	}
	jobs := &cancelingBatchRepository{
		item: item,
		job:  BatchJob{ID: item.BatchJobID, State: "canceling"},
	}
	governance := &countingGovernance{}
	service, err := NewService(Config{
		Repository: &memoryRepository{}, Jobs: jobs, Governance: governance, Clock: clock.NewFake(now),
	})
	if err != nil {
		t.Fatal(err)
	}

	processed, err := service.ProcessNextBatchItem(context.Background(), "test")
	if err != nil || !processed {
		t.Fatalf("process canceled item processed=%t err=%v", processed, err)
	}
	if governance.calls != 0 {
		t.Fatalf("canceled batch invoked governance %d times", governance.calls)
	}
	if len(jobs.completions) != 1 || jobs.completions[0].state != "canceled" || jobs.completions[0].errorMessageKey != "" {
		t.Fatalf("unexpected cancellation completion: %+v", jobs.completions)
	}
}

type memoryRepository struct {
	users       []UserRecord
	notes       []Note
	userVersion uint64
	queries     []UserListQuery
}

// replayBatchRepository exposes the operation-ID-first repository behavior without reproducing persistence details in a service test.
type replayBatchRepository struct {
	BatchRepository
	preview BatchPreview
	job     BatchJob
	starts  []StartBatchJobCommand
}

func (repository *replayBatchRepository) GetBatchPreview(_ context.Context, previewID, actorAdminID uuid.UUID) (BatchPreview, error) {
	if previewID != repository.preview.ID || actorAdminID != repository.preview.ActorAdminID {
		return BatchPreview{}, ErrNotFound
	}
	return repository.preview, nil
}

func (repository *replayBatchRepository) StartBatchJob(_ context.Context, command StartBatchJobCommand) (BatchJob, error) {
	repository.starts = append(repository.starts, command)
	return repository.job, nil
}

type batchCompletion struct {
	state           string
	errorMessageKey string
}

type cancelingBatchRepository struct {
	BatchRepository
	item        BatchItem
	job         BatchJob
	completions []batchCompletion
}

func (repository *cancelingBatchRepository) ClaimNextBatchItem(_ context.Context, _ string, _ time.Duration) (BatchItem, error) {
	return repository.item, nil
}

func (repository *cancelingBatchRepository) GetBatchJob(_ context.Context, batchJobID uuid.UUID) (BatchJob, error) {
	if batchJobID != repository.job.ID {
		return BatchJob{}, ErrNotFound
	}
	return repository.job, nil
}

func (repository *cancelingBatchRepository) CompleteBatchItem(
	_ context.Context,
	item BatchItem,
	nextState, errorMessageKey string,
	_ uuid.UUID,
	_ time.Time,
) (BatchItem, error) {
	if item.ID != repository.item.ID {
		return BatchItem{}, ErrNotFound
	}
	repository.completions = append(repository.completions, batchCompletion{state: nextState, errorMessageKey: errorMessageKey})
	return item, nil
}

type countingGovernance struct{ calls int }

func (governance *countingGovernance) GetUserState(context.Context, uuid.UUID) (GovernanceUserState, error) {
	governance.calls++
	return GovernanceUserState{}, errors.New("governance should not execute")
}

func (governance *countingGovernance) GetCurrentRoom(context.Context, uuid.UUID) (GovernanceRoomState, error) {
	governance.calls++
	return GovernanceRoomState{}, errors.New("governance should not execute")
}

func (governance *countingGovernance) TransitionUserStatus(context.Context, uuid.UUID, uint64, string, time.Time) (GovernanceUserState, error) {
	governance.calls++
	return GovernanceUserState{}, errors.New("governance should not execute")
}

func (governance *countingGovernance) RemoveUserFromRoom(context.Context, uuid.UUID, uuid.UUID, GovernanceRoomState, time.Time) error {
	governance.calls++
	return errors.New("governance should not execute")
}

func (repository *memoryRepository) CreateTag(_ context.Context, command CreateTagCommand) (TagMutation, error) {
	// Echo the persisted shape so tests can assert the service handed persistence canonical values.
	return TagMutation{Tag: Tag{Name: command.Name, Color: command.Color, Reason: command.Reason}}, nil
}

func (repository *memoryRepository) UpdateTag(_ context.Context, command UpdateTagCommand) (Tag, error) {
	return Tag{ID: command.TagID, Name: command.Name, Color: command.Color, Reason: command.Reason}, nil
}
func (repository *memoryRepository) DeleteTag(context.Context, DeleteTagCommand) (uint64, error) {
	return 0, nil
}
func (repository *memoryRepository) ListTags(context.Context, TagPageQuery) (TagPage, error) {
	return TagPage{}, nil
}

func (repository *memoryRepository) SetTags(_ context.Context, command SetTagsCommand) (uint64, error) {
	if command.ExpectedVersion != repository.userVersion {
		return 0, ErrConflict
	}
	repository.userVersion++
	return repository.userVersion, nil
}

func (repository *memoryRepository) AppendNote(_ context.Context, command AppendNoteCommand) (Note, error) {
	if command.ExpectedVersion != repository.userVersion {
		return Note{}, ErrConflict
	}
	repository.userVersion++
	note := Note{ID: command.NoteID, UserID: command.UserID, AuthorAdminID: command.AuthorAdminID, Body: command.Body, Reason: command.Reason, Version: 1, CreatedAt: command.CreatedAt}
	repository.notes = append([]Note{note}, repository.notes...)
	return note, nil
}

func (repository *memoryRepository) ListNotes(_ context.Context, query NotePageQuery) ([]Note, error) {
	result := make([]Note, 0, len(repository.notes))
	for _, note := range repository.notes {
		if note.UserID == query.UserID {
			result = append(result, note)
		}
	}
	if len(result) > int(query.PageSize) {
		result = result[:query.PageSize]
	}
	return result, nil
}

func (repository *memoryRepository) ListUsers(_ context.Context, query UserListQuery) ([]UserRecord, error) {
	repository.queries = append(repository.queries, query)
	result := make([]UserRecord, 0, len(repository.users))
	for _, user := range repository.users {
		if query.UserID != uuid.Nil && user.ID != query.UserID {
			continue
		}
		if len(query.Statuses) > 0 && user.Status != query.Statuses[0] {
			continue
		}
		result = append(result, user)
	}
	if query.After.UserID != uuid.Nil && len(result) > 0 {
		result = result[1:]
	}
	if len(result) > int(query.PageSize) {
		result = result[:query.PageSize]
	}
	return result, nil
}

type memoryProfiles struct {
	profiles map[uuid.UUID]profile.UserProfile
}

func (profiles *memoryProfiles) GetByID(_ context.Context, userID uuid.UUID) (profile.UserProfile, error) {
	value, ok := profiles.profiles[userID]
	if !ok {
		return profile.UserProfile{}, profile.ErrProfileNotFound
	}
	return value, nil
}

type memoryAudit struct {
	failPII        bool
	failAnnotation bool
	piiEvents      []PIIAuditEvent
	annotation     []AnnotationAuditEvent
}

func (audit *memoryAudit) RecordPIIRead(_ context.Context, event PIIAuditEvent) (uuid.UUID, error) {
	audit.piiEvents = append(audit.piiEvents, event)
	if audit.failPII {
		return uuid.UUID{}, errors.New("audit down")
	}
	return uuid.New(), nil
}

func (audit *memoryAudit) RecordAnnotationWrite(_ context.Context, event AnnotationAuditEvent) (uuid.UUID, error) {
	audit.annotation = append(audit.annotation, event)
	if audit.failAnnotation {
		return uuid.UUID{}, errors.New("audit down")
	}
	return uuid.New(), nil
}

func newTestService(t testing.TB, repository Repository, profiles ProfileReader, protector *profile.PIIProtector, audit AuditRecorder, now time.Time) *Service {
	t.Helper()
	cursor, err := NewCursorCodec(newTestHMACKeyring(t, now))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Config{Repository: repository, Profiles: profiles, Protector: protector, Audit: audit, Cursor: cursor, Clock: clock.NewFake(now)})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func newTestActor(t testing.TB, now time.Time, permissions ...admin.Permission) admin.ActorContext {
	t.Helper()
	adminID, sessionID := uuid.New(), uuid.New()
	session, err := admin.RestoreSession(admin.SessionSnapshot{
		ID: sessionID, AdminID: adminID, Selector: base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
		SecretMAC: security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: 1, Value: make([]byte, 32)},
		CSRFHash:  security.MAC[security.AdminSessionKeyPurpose]{KeyVersion: 1, Value: make([]byte, 32)},
		Kind:      admin.SessionKindFull, AdminVersion: 1, PasswordVersion: 1, SessionVersion: 1,
		ClientIP: "203.0.113.10", UserAgent: "admin-user-test", MaxAttempts: 5,
		CreatedAt: now.Add(-time.Minute), LastSeenAt: now, IdleExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(2 * time.Hour),
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
	actor, err := admin.NewActorContext(adminID, sessionID, session, permissionSet, elevations, 0, "req-admin-user", "http://127.0.0.1:4174", "203.0.113.10", "admin-user-test")
	if err != nil {
		t.Fatal(err)
	}
	return actor
}

func newElevatedTestActor(t testing.TB, now time.Time, scope admin.ElevationScope, permissions ...admin.Permission) admin.ActorContext {
	t.Helper()
	base := newTestActor(t, now, permissions...)
	elevation, err := admin.NewElevation(base.Session(), base.EnrollmentVersion(), scope, now, now.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	permissionSet, err := admin.NewPermissionSet(base.Permissions()...)
	if err != nil {
		t.Fatal(err)
	}
	elevations, err := admin.NewElevationSet(elevation)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := admin.NewActorContext(
		base.AdminID(), base.SessionID(), base.Session(), permissionSet, elevations, base.EnrollmentVersion(),
		base.RequestID(), base.Origin(), base.ClientIP(), base.UserAgent(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return actor
}

func newEncryptedProfile(t testing.TB, userID uuid.UUID, realName string, now time.Time) profile.UserProfile {
	t.Helper()
	protector := newTestPIIProtector(t, now)
	encrypted, err := protector.EncryptRealName(userID, realName)
	if err != nil {
		t.Fatal(err)
	}
	value, err := profile.NewUserProfile(userID, encrypted, now, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func newTestPIIProtector(t testing.TB, now time.Time) *profile.PIIProtector {
	t.Helper()
	keyring, err := security.LoadAESKeyring[security.PIIKeyPurpose](writeSymmetricKeyring(t, now), now)
	if err != nil {
		t.Fatal(err)
	}
	protector, err := profile.NewDefaultPIIProtector(keyring)
	if err != nil {
		t.Fatal(err)
	}
	return protector
}

func newTestHMACKeyring(t testing.TB, now time.Time) *security.HMACKeyring[security.AdminCursorKeyPurpose] {
	t.Helper()
	keyring, err := security.LoadHMACKeyring[security.AdminCursorKeyPurpose](writeSymmetricKeyring(t, now), now)
	if err != nil {
		t.Fatal(err)
	}
	return keyring
}

func writeSymmetricKeyring(t testing.TB, now time.Time) string {
	t.Helper()
	document := map[string]any{
		"active_version": 1,
		"keys": []map[string]any{{
			"version":    1,
			"key":        base64.StdEncoding.EncodeToString(bytesOf(0x42, 32)),
			"not_before": now.Add(-time.Hour),
		}},
	}
	contents, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "keyring.json")
	if err = os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	return path
}

func bytesOf(value byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return result
}

func newOperationID(t testing.TB) idempotency.OperationID {
	t.Helper()
	operationID, err := idempotency.NewOperationID(bytesOf(0xA5, 16))
	if err != nil {
		t.Fatal(err)
	}
	return operationID
}
