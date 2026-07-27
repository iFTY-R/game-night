package adminuser

import (
	"testing"
	"time"

	"github.com/google/uuid"
	adminv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1"
	"github.com/iFTY-R/game-night/platform/admin"
	domain "github.com/iFTY-R/game-night/platform/admin/user"
	"github.com/iFTY-R/game-night/platform/idempotency"
)

func TestUserCommandFromWireRemoveFromCurrentRoom(t *testing.T) {
	roomID := uuid.New()
	command, err := userCommandFromWire(&adminv1.AdminUserCommand{
		Type:                      adminv1.AdminUserCommandType_ADMIN_USER_COMMAND_TYPE_REMOVE_FROM_CURRENT_ROOM,
		RoomId:                    roomID.String(),
		ExpectedRoomVersion:       11,
		ExpectedMembershipVersion: 7,
	})
	if err != nil {
		t.Fatalf("userCommandFromWire error = %v", err)
	}
	if command.Type != domain.UserCommandRemoveFromCurrentRoom || command.RoomID != roomID ||
		command.ExpectedRoomVersion != 11 || command.ExpectedMembershipVersion != 7 {
		t.Fatalf("userCommandFromWire = %+v", command)
	}
}

func TestUserCommandFromWireRejectsUnexpectedRoomBinding(t *testing.T) {
	_, err := userCommandFromWire(&adminv1.AdminUserCommand{
		Type:   adminv1.AdminUserCommandType_ADMIN_USER_COMMAND_TYPE_SUSPEND,
		RoomId: uuid.NewString(),
	})
	if err == nil {
		t.Fatal("userCommandFromWire unexpectedly accepted room binding for suspend")
	}
}

func TestPreviewUserCommandResponseMapsExactPreview(t *testing.T) {
	now := time.Date(2026, time.July, 26, 9, 30, 0, 0, time.UTC)
	preview := domain.UserCommandPreview{
		ID: uuid.New(),
		Snapshot: domain.UserCommandSnapshot{
			UserID: uuid.New(), Command: domain.UserCommandDelete, ExpectedUserVersion: 23,
		},
		PreviewDigest:     digest("preview", "delete"),
		AffectedDevices:   4,
		AffectedRooms:     1,
		Blockers:          []domain.GovernanceBlocker{{Type: domain.GovernanceBlockerPendingExport, ResourceID: "user-1", MessageKey: "admin.user.pending_export"}},
		RequiredElevation: admin.ElevationScopeUsersDelete,
		SampledAt:         now,
		ExpiresAt:         now.Add(5 * time.Minute),
		Version:           2,
	}

	response := previewUserCommandResponse(preview)
	if response.GetPreviewId() != preview.ID.String() || response.GetPreviewDigest() != digestString(preview.PreviewDigest) {
		t.Fatalf("preview identity = %+v", response)
	}
	if response.GetUserId() != preview.Snapshot.UserID.String() || response.GetExpectedUserVersion() != preview.Snapshot.ExpectedUserVersion {
		t.Fatalf("preview target = %+v", response)
	}
	if response.GetAffectedDevices() != preview.AffectedDevices || response.GetAffectedRooms() != preview.AffectedRooms {
		t.Fatalf("preview impact = %+v", response)
	}
	if response.GetRequiredElevation() != adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_USERS_DELETE {
		t.Fatalf("preview required elevation = %s", response.GetRequiredElevation())
	}
	if len(response.GetBlockers()) != 1 || response.GetBlockers()[0].GetType() != adminv1.AdminUserCommandBlockerType_ADMIN_USER_COMMAND_BLOCKER_TYPE_PENDING_EXPORT {
		t.Fatalf("preview blockers = %+v", response.GetBlockers())
	}
	if !response.GetExpiresAt().AsTime().Equal(preview.ExpiresAt) || !response.GetSampledAt().AsTime().Equal(preview.SampledAt) {
		t.Fatalf("preview timestamps = %+v", response)
	}
}

func TestExecuteUserCommandResponseMapsReceiptOutcomeAndErasure(t *testing.T) {
	operationID, err := idempotency.NewOperationID([]byte("0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewOperationID error = %v", err)
	}
	completedAt := time.Date(2026, time.July, 26, 10, 45, 0, 0, time.UTC)
	auditEventID := uuid.New()
	erasureJobID := uuid.New()
	result := domain.UserCommandExecutionResult{
		Receipt: domain.UserCommandReceipt{
			OperationID: operationID, AuditEventID: auditEventID, Outcome: domain.UserCommandOutcomeExecuted, CompletedAt: completedAt,
		},
		User: domain.UserRecord{
			ID: uuid.New(), Username: "target-user", Status: "deleted", Version: 29,
			CreatedAt: completedAt.Add(-time.Hour), UpdatedAt: completedAt, LastActivityAt: completedAt.Add(-time.Minute),
		},
		RevokedDevices: 6,
		RemovedRooms:   1,
		ErasureJobID:   erasureJobID,
	}

	response := executeUserCommandResponse(result)
	if response.GetReceipt().GetOperationId() != operationID.Value() || response.GetReceipt().GetAuditEventId() != auditEventID.String() {
		t.Fatalf("receipt = %+v", response.GetReceipt())
	}
	if response.GetOutcome() != adminv1.AdminUserCommandOutcome_ADMIN_USER_COMMAND_OUTCOME_EXECUTED {
		t.Fatalf("outcome = %s", response.GetOutcome())
	}
	if response.GetUser().GetUserId() != result.User.ID.String() || response.GetUser().GetVersion() != result.User.Version {
		t.Fatalf("user = %+v", response.GetUser())
	}
	if response.GetRevokedDevices() != result.RevokedDevices || response.GetRemovedRooms() != result.RemovedRooms {
		t.Fatalf("counts = %+v", response)
	}
	if response.GetErasureJobId() != erasureJobID.String() {
		t.Fatalf("erasure_job_id = %q", response.GetErasureJobId())
	}
	if !response.GetReceipt().GetCompletedAt().AsTime().Equal(completedAt) {
		t.Fatalf("completed_at = %v", response.GetReceipt().GetCompletedAt().AsTime())
	}
}
