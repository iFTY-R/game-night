package user

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	admin "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/idempotency"
)

func TestServicePreviewUserCommandBindsActorReasonAndVersions(t *testing.T) {
	now := time.Date(2026, 7, 26, 16, 0, 0, 0, time.UTC)
	userID := uuid.New()
	repository := governanceUserRepository(userID, now, 4, "active")
	governance := &memorySingleUserGovernance{
		state: GovernanceUserState{UserID: userID, Status: "active", Version: 4}, repository: repository,
	}
	store := newMemoryUserCommandStore()
	service := newGovernanceTestService(t, repository, store, governance, &memoryAudit{}, now)
	actor := newTestActor(t, now, admin.PermissionUsersGovern)

	preview, err := service.PreviewUserCommand(context.Background(), actor, PreviewUserCommandInput{
		UserID: userID, Command: UserCommandInput{Type: UserCommandSuspend}, Reason: "违反房间规则", ExpectedUserVersion: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Snapshot.UserID != userID || preview.Snapshot.ExpectedUserVersion != 4 || preview.Snapshot.Command != UserCommandSuspend || preview.PreviewDigest == ([sha256.Size]byte{}) {
		t.Fatalf("unexpected preview: %+v", preview)
	}

	_, err = service.ExecuteUserCommand(context.Background(), actor, ExecuteUserCommandInput{
		OperationID: newOperationID(t), UserID: userID, Command: UserCommandInput{Type: UserCommandSuspend},
		PreviewID: preview.ID, PreviewDigest: preview.PreviewDigest, Reason: "changed reason", ExpectedUserVersion: 4,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("changed reason error = %v", err)
	}
	if governance.state.Status != "active" || !store.previews[preview.ID].ConsumedAt.IsZero() {
		t.Fatalf("tampered execution changed governance state=%+v preview=%+v", governance.state, store.previews[preview.ID])
	}
}

func TestServiceExecuteUserCommandRequiresElevationAuditsBeforeMutationAndReplays(t *testing.T) {
	now := time.Date(2026, 7, 26, 16, 10, 0, 0, time.UTC)
	userID := uuid.New()
	repository := governanceUserRepository(userID, now, 7, "active")
	governance := &memorySingleUserGovernance{
		state: GovernanceUserState{UserID: userID, Status: "active", Version: 7}, activeDevices: 2, repository: repository,
	}
	store := newMemoryUserCommandStore()
	audit := &memoryAudit{}
	service := newGovernanceTestService(t, repository, store, governance, audit, now)
	actor := newTestActor(t, now, admin.PermissionUsersGovern)
	preview, err := service.PreviewUserCommand(context.Background(), actor, PreviewUserCommandInput{
		UserID: userID, Command: UserCommandInput{Type: UserCommandRevokeAllDevices}, Reason: "设备风险处置", ExpectedUserVersion: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := ExecuteUserCommandInput{
		OperationID: newOperationID(t), UserID: userID, Command: UserCommandInput{Type: UserCommandRevokeAllDevices},
		PreviewID: preview.ID, PreviewDigest: preview.PreviewDigest, Reason: "设备风险处置", ExpectedUserVersion: 7,
	}
	if _, err = service.ExecuteUserCommand(context.Background(), actor, input); !errors.Is(err, admin.ErrElevationDenied) {
		t.Fatalf("missing elevation error = %v", err)
	}
	if governance.activeDevices != 2 || !store.previews[preview.ID].ConsumedAt.IsZero() {
		t.Fatalf("missing elevation consumed preview or revoked devices: %+v", store.previews[preview.ID])
	}

	elevated := elevateGovernanceActor(t, actor, now, admin.ElevationScopeUsersRevokeDevices)
	audit.failAnnotation = true
	if _, err = service.ExecuteUserCommand(context.Background(), elevated, input); !errors.Is(err, ErrAuditUnavailable) {
		t.Fatalf("audit failure error = %v", err)
	}
	if governance.activeDevices != 2 {
		t.Fatalf("audit failure mutated devices: %d", governance.activeDevices)
	}

	// Audit failure consumes the preview, so the operator must create a fresh review before retrying the command.
	audit.failAnnotation = false
	preview, err = service.PreviewUserCommand(context.Background(), elevated, PreviewUserCommandInput{
		UserID: userID, Command: UserCommandInput{Type: UserCommandRevokeAllDevices}, Reason: "设备风险处置", ExpectedUserVersion: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	input.PreviewID, input.PreviewDigest = preview.ID, preview.PreviewDigest
	result, err := service.ExecuteUserCommand(context.Background(), elevated, input)
	if err != nil || result.Receipt.Outcome != UserCommandOutcomeExecuted || result.RevokedDevices != 2 || governance.activeDevices != 0 {
		t.Fatalf("execute result=%+v devices=%d err=%v", result, governance.activeDevices, err)
	}
	auditCount := len(audit.annotation)
	replayed, err := service.ExecuteUserCommand(context.Background(), elevated, input)
	if err != nil || replayed.Receipt.AuditEventID != result.Receipt.AuditEventID || len(audit.annotation) != auditCount || governance.revokeCalls != 1 {
		t.Fatalf("replayed result=%+v audit=%d revokeCalls=%d err=%v", replayed, len(audit.annotation), governance.revokeCalls, err)
	}
}

func TestServicePreviewUserCommandBlocksDeletionDuringActiveGameAndExecutesRoomRemoval(t *testing.T) {
	now := time.Date(2026, 7, 26, 16, 20, 0, 0, time.UTC)
	userID, roomID := uuid.New(), uuid.New()
	repository := governanceUserRepository(userID, now, 3, "active")
	governance := &memorySingleUserGovernance{
		state: GovernanceUserState{UserID: userID, Status: "active", Version: 3}, repository: repository,
		room: &GovernanceRoomState{RoomID: roomID, RoomStatus: "playing", ExpectedRoomVersion: 5, ExpectedMembershipVersion: 8},
	}
	store := newMemoryUserCommandStore()
	service := newGovernanceTestService(t, repository, store, governance, &memoryAudit{}, now)
	actor := newTestActor(t, now, admin.PermissionUsersGovern, admin.PermissionRoomsControl)

	deletePreview, err := service.PreviewUserCommand(context.Background(), actor, PreviewUserCommandInput{
		UserID: userID, Command: UserCommandInput{Type: UserCommandDelete}, Reason: "合规删除", ExpectedUserVersion: 3,
	})
	if err != nil || len(deletePreview.Blockers) != 1 || deletePreview.Blockers[0].Type != GovernanceBlockerActiveGame {
		t.Fatalf("delete preview=%+v err=%v", deletePreview, err)
	}

	governance.room.RoomStatus = "waiting"
	removePreview, err := service.PreviewUserCommand(context.Background(), actor, PreviewUserCommandInput{
		UserID:  userID,
		Command: UserCommandInput{Type: UserCommandRemoveFromCurrentRoom, RoomID: roomID, ExpectedRoomVersion: 5, ExpectedMembershipVersion: 8},
		Reason:  "用户请求离开", ExpectedUserVersion: 3,
	})
	if err != nil || removePreview.AffectedRooms != 1 || removePreview.Snapshot.Room == nil {
		t.Fatalf("room preview=%+v err=%v", removePreview, err)
	}
	result, err := service.ExecuteUserCommand(context.Background(), actor, ExecuteUserCommandInput{
		OperationID: newOperationID(t), UserID: userID,
		Command:   UserCommandInput{Type: UserCommandRemoveFromCurrentRoom, RoomID: roomID, ExpectedRoomVersion: 5, ExpectedMembershipVersion: 8},
		PreviewID: removePreview.ID, PreviewDigest: removePreview.PreviewDigest, Reason: "用户请求离开", ExpectedUserVersion: 3,
	})
	if err != nil || result.RemovedRooms != 1 || governance.room != nil {
		t.Fatalf("room removal result=%+v room=%+v err=%v", result, governance.room, err)
	}
}

func TestServiceExecuteUserCommandClearsWaitingMembershipBeforeDeletingUser(t *testing.T) {
	now := time.Date(2026, 7, 26, 16, 25, 0, 0, time.UTC)
	userID, roomID := uuid.New(), uuid.New()
	repository := governanceUserRepository(userID, now, 3, "active")
	governance := &memorySingleUserGovernance{
		state: GovernanceUserState{UserID: userID, Status: "active", Version: 3}, activeDevices: 1, repository: repository,
		room: &GovernanceRoomState{RoomID: roomID, RoomStatus: "waiting", ExpectedRoomVersion: 5, ExpectedMembershipVersion: 8},
	}
	store := newMemoryUserCommandStore()
	service := newGovernanceTestService(t, repository, store, governance, &memoryAudit{}, now)
	actor := elevateGovernanceActor(t, newTestActor(t, now, admin.PermissionUsersGovern), now, admin.ElevationScopeUsersDelete)

	preview, err := service.PreviewUserCommand(context.Background(), actor, PreviewUserCommandInput{
		UserID: userID, Command: UserCommandInput{Type: UserCommandDelete}, Reason: "合规删除", ExpectedUserVersion: 3,
	})
	if err != nil || preview.Snapshot.Room == nil || preview.AffectedRooms != 1 {
		t.Fatalf("delete preview=%+v err=%v", preview, err)
	}
	result, err := service.ExecuteUserCommand(context.Background(), actor, ExecuteUserCommandInput{
		OperationID: newOperationID(t), UserID: userID, Command: UserCommandInput{Type: UserCommandDelete},
		PreviewID: preview.ID, PreviewDigest: preview.PreviewDigest, Reason: "合规删除", ExpectedUserVersion: 3,
	})
	if err != nil || result.Receipt.Outcome != UserCommandOutcomeExecuted || result.RemovedRooms != 1 ||
		governance.room != nil || governance.state.Status != "deleted" || governance.activeDevices != 0 {
		t.Fatalf("delete result=%+v governance=%+v err=%v", result, governance, err)
	}
}

func TestServiceExecuteUserCommandDoesNotDeleteWhenWaitingMembershipRemovalConflicts(t *testing.T) {
	now := time.Date(2026, 7, 26, 16, 30, 0, 0, time.UTC)
	userID, roomID := uuid.New(), uuid.New()
	repository := governanceUserRepository(userID, now, 3, "active")
	governance := &memorySingleUserGovernance{
		state: GovernanceUserState{UserID: userID, Status: "active", Version: 3}, activeDevices: 1, repository: repository,
		room:      &GovernanceRoomState{RoomID: roomID, RoomStatus: "waiting", ExpectedRoomVersion: 5, ExpectedMembershipVersion: 8},
		removeErr: ErrConflict,
	}
	store := newMemoryUserCommandStore()
	service := newGovernanceTestService(t, repository, store, governance, &memoryAudit{}, now)
	actor := elevateGovernanceActor(t, newTestActor(t, now, admin.PermissionUsersGovern), now, admin.ElevationScopeUsersDelete)

	preview, err := service.PreviewUserCommand(context.Background(), actor, PreviewUserCommandInput{
		UserID: userID, Command: UserCommandInput{Type: UserCommandDelete}, Reason: "合规删除", ExpectedUserVersion: 3,
	})
	if err != nil || preview.Snapshot.Room == nil {
		t.Fatalf("delete preview=%+v err=%v", preview, err)
	}
	_, err = service.ExecuteUserCommand(context.Background(), actor, ExecuteUserCommandInput{
		OperationID: newOperationID(t), UserID: userID, Command: UserCommandInput{Type: UserCommandDelete},
		PreviewID: preview.ID, PreviewDigest: preview.PreviewDigest, Reason: "合规删除", ExpectedUserVersion: 3,
	})
	if !errors.Is(err, ErrConflict) || governance.state.Status != "active" || governance.activeDevices != 1 || governance.room == nil {
		t.Fatalf("delete after room conflict: state=%+v devices=%d room=%+v err=%v", governance.state, governance.activeDevices, governance.room, err)
	}
}

func governanceUserRepository(userID uuid.UUID, now time.Time, version uint64, status string) *memoryRepository {
	return &memoryRepository{users: []UserRecord{{
		ID: userID, Username: "governed-user", CurrentUsernameKey: "governed-user", Status: status, Version: version,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute), LastActivityAt: now.Add(-time.Minute),
	}}}
}

func newGovernanceTestService(
	t testing.TB,
	repository Repository,
	store UserCommandRepository,
	governance SingleUserGovernanceExecutor,
	audit AuditRecorder,
	now time.Time,
) *Service {
	t.Helper()
	service, err := NewService(Config{
		Repository: repository, UserCommands: store, SingleGovernance: governance, Audit: audit, Clock: clock.NewFake(now),
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func elevateGovernanceActor(t testing.TB, actor admin.ActorContext, now time.Time, scope admin.ElevationScope) admin.ActorContext {
	t.Helper()
	elevation, err := admin.NewElevation(actor.Session(), actor.EnrollmentVersion(), scope, now, now.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	permissions, err := admin.NewPermissionSet(actor.Permissions()...)
	if err != nil {
		t.Fatal(err)
	}
	elevations, err := admin.NewElevationSet(elevation)
	if err != nil {
		t.Fatal(err)
	}
	elevated, err := admin.NewActorContext(
		actor.AdminID(), actor.SessionID(), actor.Session(), permissions, elevations, actor.EnrollmentVersion(),
		actor.RequestID(), actor.Origin(), actor.ClientIP(), actor.UserAgent(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return elevated
}

type memoryUserCommandStore struct {
	previews map[uuid.UUID]UserCommandPreview
	receipts map[string]UserCommandReceipt
}

func newMemoryUserCommandStore() *memoryUserCommandStore {
	return &memoryUserCommandStore{previews: make(map[uuid.UUID]UserCommandPreview), receipts: make(map[string]UserCommandReceipt)}
}

func (store *memoryUserCommandStore) CreateUserCommandPreview(_ context.Context, command CreateUserCommandPreviewCommand) (UserCommandPreview, error) {
	preview := command.Preview
	preview.Version = 1
	store.previews[preview.ID] = preview
	return preview, nil
}

func (store *memoryUserCommandStore) GetUserCommandPreview(_ context.Context, previewID, actorID uuid.UUID) (UserCommandPreview, error) {
	preview, ok := store.previews[previewID]
	if !ok || preview.ActorAdminID != actorID {
		return UserCommandPreview{}, ErrNotFound
	}
	return preview, nil
}

func (store *memoryUserCommandStore) ConsumeUserCommandPreview(_ context.Context, previewID, actorID uuid.UUID, expectedVersion uint64, consumedAt time.Time) (UserCommandPreview, error) {
	preview, ok := store.previews[previewID]
	if !ok || preview.ActorAdminID != actorID || preview.Version != expectedVersion || !preview.ConsumedAt.IsZero() || !consumedAt.Before(preview.ExpiresAt) {
		return UserCommandPreview{}, ErrConflict
	}
	preview.ConsumedAt, preview.Version = consumedAt, preview.Version+1
	store.previews[previewID] = preview
	return preview, nil
}

func (store *memoryUserCommandStore) GetUserCommandReceipt(_ context.Context, actorID uuid.UUID, operationID idempotency.OperationID) (UserCommandReceipt, error) {
	receipt, ok := store.receipts[commandReceiptKey(actorID, operationID)]
	if !ok {
		return UserCommandReceipt{}, ErrNotFound
	}
	return receipt, nil
}

func (store *memoryUserCommandStore) SaveUserCommandReceipt(_ context.Context, input UserCommandReceipt) (UserCommandReceipt, error) {
	key := commandReceiptKey(input.ActorAdminID, input.OperationID)
	if existing, exists := store.receipts[key]; exists {
		if existing.RequestDigest != input.RequestDigest {
			return UserCommandReceipt{}, ErrIdempotencyConflict
		}
		return existing, nil
	}
	store.receipts[key] = input
	return input, nil
}

func commandReceiptKey(actorID uuid.UUID, operationID idempotency.OperationID) string {
	return actorID.String() + ":" + operationID.Value()
}

type memorySingleUserGovernance struct {
	state         GovernanceUserState
	activeDevices int32
	pendingExport bool
	room          *GovernanceRoomState
	repository    *memoryRepository
	revokeCalls   int
	removeErr     error
}

func (governance *memorySingleUserGovernance) GetUserState(_ context.Context, userID uuid.UUID) (GovernanceUserState, error) {
	if userID != governance.state.UserID {
		return GovernanceUserState{}, ErrNotFound
	}
	return governance.state, nil
}

func (governance *memorySingleUserGovernance) GetCurrentRoom(_ context.Context, userID uuid.UUID) (GovernanceRoomState, error) {
	if userID != governance.state.UserID || governance.room == nil {
		return GovernanceRoomState{}, ErrNotFound
	}
	return *governance.room, nil
}

func (governance *memorySingleUserGovernance) TransitionUserStatus(_ context.Context, userID uuid.UUID, expectedVersion uint64, nextStatus string, _ time.Time) (GovernanceUserState, error) {
	if userID != governance.state.UserID || governance.state.Version != expectedVersion {
		return GovernanceUserState{}, ErrConflict
	}
	governance.state.Status, governance.state.Version = nextStatus, governance.state.Version+1
	governance.updateRecord()
	return governance.state, nil
}

func (governance *memorySingleUserGovernance) RemoveUserFromRoom(_ context.Context, _ uuid.UUID, userID uuid.UUID, room GovernanceRoomState, _ time.Time) error {
	if governance.removeErr != nil {
		return governance.removeErr
	}
	if userID != governance.state.UserID || governance.room == nil || !sameRoomState(*governance.room, room) {
		return ErrConflict
	}
	governance.room = nil
	return nil
}

func (governance *memorySingleUserGovernance) CountActiveDevices(_ context.Context, userID uuid.UUID, _ time.Time) (int32, error) {
	if userID != governance.state.UserID {
		return 0, ErrNotFound
	}
	return governance.activeDevices, nil
}

func (governance *memorySingleUserGovernance) RevokeAllDevices(_ context.Context, userID uuid.UUID, _ time.Time) (int32, error) {
	if userID != governance.state.UserID {
		return 0, ErrNotFound
	}
	revoked := governance.activeDevices
	governance.activeDevices = 0
	governance.revokeCalls++
	return revoked, nil
}

func (governance *memorySingleUserGovernance) HasPendingExport(_ context.Context, userID uuid.UUID) (bool, error) {
	if userID != governance.state.UserID {
		return false, ErrNotFound
	}
	return governance.pendingExport, nil
}

func (governance *memorySingleUserGovernance) DeleteUser(_ context.Context, command DeleteUserCommand) (DeleteUserResult, error) {
	if command.UserID != governance.state.UserID || command.ExpectedUserVersion != governance.state.Version {
		return DeleteUserResult{}, ErrConflict
	}
	governance.state.Status, governance.state.Version = "deleted", governance.state.Version+1
	revoked := governance.activeDevices
	governance.activeDevices = 0
	governance.updateRecord()
	return DeleteUserResult{User: governance.state, RevokedDevices: revoked, ErasureJobID: uuid.New()}, nil
}

func (governance *memorySingleUserGovernance) EraseUserProfile(_ context.Context, userID uuid.UUID) error {
	if userID != governance.state.UserID {
		return ErrNotFound
	}
	return nil
}

func (governance *memorySingleUserGovernance) updateRecord() {
	if governance.repository == nil {
		return
	}
	for index := range governance.repository.users {
		if governance.repository.users[index].ID == governance.state.UserID {
			governance.repository.users[index].Status = governance.state.Status
			governance.repository.users[index].Version = governance.state.Version
			if governance.state.Status == "deleted" {
				governance.repository.users[index].Username = ""
				governance.repository.users[index].CurrentUsernameKey = ""
			}
		}
	}
}

func sameRoomState(left, right GovernanceRoomState) bool {
	return left.RoomID == right.RoomID && left.RoomStatus == right.RoomStatus && left.ExpectedRoomVersion == right.ExpectedRoomVersion &&
		left.ExpectedMembershipVersion == right.ExpectedMembershipVersion
}

var _ UserCommandRepository = (*memoryUserCommandStore)(nil)
var _ SingleUserGovernanceExecutor = (*memorySingleUserGovernance)(nil)
