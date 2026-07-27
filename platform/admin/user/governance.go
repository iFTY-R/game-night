package user

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	admin "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/idempotency"
)

const (
	// UserCommandPreviewTTL bounds how long an administrator can act on the exact user and room state they reviewed.
	UserCommandPreviewTTL = 5 * time.Minute
	// UserCommandSnapshotSchemaVersion invalidates persisted previews if their immutable command shape changes.
	UserCommandSnapshotSchemaVersion uint32 = 1
)

// UserCommand is the closed set of synchronous, single-user governance actions.
type UserCommand string

const (
	UserCommandSuspend               UserCommand = "suspend"
	UserCommandUnsuspend             UserCommand = "unsuspend"
	UserCommandRevokeAllDevices      UserCommand = "revoke_all_devices"
	UserCommandRemoveFromCurrentRoom UserCommand = "remove_from_current_room"
	UserCommandDelete                UserCommand = "delete"
)

// UserCommandOutcome is persisted so retry responses report the original business result instead of recomputing it.
type UserCommandOutcome string

const (
	UserCommandOutcomeExecuted UserCommandOutcome = "executed"
	UserCommandOutcomeNoChange UserCommandOutcome = "no_change"
	UserCommandOutcomeRejected UserCommandOutcome = "rejected"
)

// UserCommandInput is the server-side representation of the reviewed wire command.
// Room versions are required only for a room removal and prevent an old drawer from removing a replacement member.
type UserCommandInput struct {
	Type                      UserCommand
	RoomID                    uuid.UUID
	ExpectedRoomVersion       uint64
	ExpectedMembershipVersion uint64
}

// UserCommandSnapshot freezes every version-bearing resource shown in a preview.
// It is deliberately independent of the incoming request so execution can reject client-side tampering.
type UserCommandSnapshot struct {
	SchemaVersion       uint32            `json:"schema_version"`
	UserID              uuid.UUID         `json:"user_id"`
	Command             UserCommand       `json:"command"`
	ExpectedUserVersion uint64            `json:"expected_user_version"`
	Room                *BatchRoomBinding `json:"room,omitempty"`
}

// UserCommandPreview is the durable, short-lived authorization context for one user command.
type UserCommandPreview struct {
	ID                uuid.UUID
	ActorAdminID      uuid.UUID
	Snapshot          UserCommandSnapshot
	PreviewDigest     [sha256.Size]byte
	AffectedDevices   int32
	AffectedRooms     int32
	Blockers          []GovernanceBlocker
	RequiredElevation admin.ElevationScope
	SampledAt         time.Time
	ExpiresAt         time.Time
	ConsumedAt        time.Time
	Version           uint64
}

// CreateUserCommandPreviewCommand persists the exact immutable command snapshot that was reviewed.
type CreateUserCommandPreviewCommand struct {
	Preview UserCommandPreview
}

// UserCommandReceipt is the idempotent result of a completed or rejected command.
type UserCommandReceipt struct {
	ActorAdminID   uuid.UUID
	OperationID    idempotency.OperationID
	RequestDigest  [sha256.Size]byte
	PreviewID      uuid.UUID
	UserID         uuid.UUID
	Command        UserCommand
	Outcome        UserCommandOutcome
	UserVersion    uint64
	RevokedDevices int32
	RemovedRooms   int32
	ErasureJobID   uuid.UUID
	AuditEventID   uuid.UUID
	CompletedAt    time.Time
}

// ExecuteUserCommandInput carries only data received from the transport. The service rebinds all meaningful state to the preview.
type ExecuteUserCommandInput struct {
	OperationID         idempotency.OperationID
	UserID              uuid.UUID
	Command             UserCommandInput
	PreviewID           uuid.UUID
	PreviewDigest       [sha256.Size]byte
	Reason              string
	ExpectedUserVersion uint64
}

// UserCommandExecutionResult is the transport-neutral response for both first execution and a receipt replay.
type UserCommandExecutionResult struct {
	Receipt        UserCommandReceipt
	User           UserRecord
	ErasureJobID   uuid.UUID
	RevokedDevices int32
	RemovedRooms   int32
}

// DeleteUserCommand keeps deletion, username-claim release, credential revocation, and erasure-job creation on one storage boundary.
type DeleteUserCommand struct {
	ActorAdminID        uuid.UUID
	OperationID         idempotency.OperationID
	RequestDigest       [sha256.Size]byte
	UserID              uuid.UUID
	ExpectedUserVersion uint64
	Reason              string
	ChangedAt           time.Time
}

// DeleteUserResult is the durable initial result of deleting a user before the asynchronous profile cleanup finishes.
type DeleteUserResult struct {
	User           GovernanceUserState
	RevokedDevices int32
	ErasureJobID   uuid.UUID
}

// UserCommandRepository owns preview consumption and durable command receipts.
// Keeping the two together makes an operation retry independent from mutable live user state.
type UserCommandRepository interface {
	CreateUserCommandPreview(context.Context, CreateUserCommandPreviewCommand) (UserCommandPreview, error)
	GetUserCommandPreview(context.Context, uuid.UUID, uuid.UUID) (UserCommandPreview, error)
	ConsumeUserCommandPreview(context.Context, uuid.UUID, uuid.UUID, uint64, time.Time) (UserCommandPreview, error)
	GetUserCommandReceipt(context.Context, uuid.UUID, idempotency.OperationID) (UserCommandReceipt, error)
	SaveUserCommandReceipt(context.Context, UserCommandReceipt) (UserCommandReceipt, error)
}

// SingleUserGovernanceExecutor adds the synchronous identity, device, and erasure primitives to the batch executor.
type SingleUserGovernanceExecutor interface {
	GovernanceExecutor
	CountActiveDevices(context.Context, uuid.UUID, time.Time) (int32, error)
	RevokeAllDevices(context.Context, uuid.UUID, time.Time) (int32, error)
	HasPendingExport(context.Context, uuid.UUID) (bool, error)
	DeleteUser(context.Context, DeleteUserCommand) (DeleteUserResult, error)
	EraseUserProfile(context.Context, uuid.UUID) error
}

// PreviewUserCommand records a fresh, server-derived impact snapshot. It never trusts a client-provided room or user version.
func (service *Service) PreviewUserCommand(
	ctx context.Context,
	actor admin.ActorContext,
	input PreviewUserCommandInput,
) (UserCommandPreview, error) {
	if service == nil || service.commandStore == nil || service.singleGovernance == nil || service.clock == nil || ctx == nil ||
		input.UserID == uuid.Nil || input.ExpectedUserVersion == 0 || !validUserCommandInput(input.Command) || !validReason(input.Reason) {
		return UserCommandPreview{}, ErrInvalidInput
	}
	if err := requireUserCommandPermission(actor, input.Command.Type); err != nil {
		return UserCommandPreview{}, err
	}
	now := service.clock.Now().Round(0).UTC()
	state, err := service.singleGovernance.GetUserState(ctx, input.UserID)
	if err != nil {
		return UserCommandPreview{}, err
	}

	snapshot := UserCommandSnapshot{
		SchemaVersion: UserCommandSnapshotSchemaVersion, UserID: input.UserID, Command: input.Command.Type,
		ExpectedUserVersion: state.Version,
	}
	preview := UserCommandPreview{
		ActorAdminID: actor.AdminID(), Snapshot: snapshot, RequiredElevation: requiredUserCommandElevation(input.Command.Type),
		SampledAt: now, ExpiresAt: now.Add(UserCommandPreviewTTL),
	}
	if state.Version != input.ExpectedUserVersion {
		preview.Blockers = append(preview.Blockers, GovernanceBlocker{
			Type: GovernanceBlockerVersionChanged, ResourceID: input.UserID.String(), MessageKey: "admin.user.version_changed",
		})
	} else if state.Status == "deleted" {
		preview.Blockers = append(preview.Blockers, GovernanceBlocker{
			Type: GovernanceBlockerAlreadyDeleted, ResourceID: input.UserID.String(), MessageKey: "admin.user.already_deleted",
		})
	} else if err := service.populateUserCommandImpact(ctx, &preview, state, input.Command, now); err != nil {
		return UserCommandPreview{}, err
	}

	preview.ID, err = uuid.NewV7()
	if err != nil {
		return UserCommandPreview{}, ErrInvalidInput
	}
	preview.PreviewDigest, err = userCommandPreviewDigest(actor.AdminID(), preview.Snapshot, input.Reason)
	if err != nil {
		return UserCommandPreview{}, ErrIntegrity
	}
	stored, err := service.commandStore.CreateUserCommandPreview(ctx, CreateUserCommandPreviewCommand{Preview: preview})
	if err != nil {
		return UserCommandPreview{}, err
	}
	return stored, nil
}

// PreviewUserCommandInput intentionally separates the preview's expected user version from the command's room versions.
type PreviewUserCommandInput struct {
	UserID              uuid.UUID
	Command             UserCommandInput
	Reason              string
	ExpectedUserVersion uint64
}

// ExecuteUserCommand revalidates the frozen snapshot, records the attempted command, and persists a replay-safe receipt.
func (service *Service) ExecuteUserCommand(
	ctx context.Context,
	actor admin.ActorContext,
	input ExecuteUserCommandInput,
) (UserCommandExecutionResult, error) {
	if service == nil || service.repository == nil || service.commandStore == nil || service.singleGovernance == nil || service.audit == nil || service.clock == nil || ctx == nil ||
		!input.OperationID.Valid() || input.UserID == uuid.Nil || input.PreviewID == uuid.Nil || input.ExpectedUserVersion == 0 ||
		!validUserCommandInput(input.Command) || !validReason(input.Reason) {
		return UserCommandExecutionResult{}, ErrInvalidInput
	}
	if err := requireUserCommandPermission(actor, input.Command.Type); err != nil {
		return UserCommandExecutionResult{}, err
	}
	if scope := requiredUserCommandElevation(input.Command.Type); scope != "" {
		if err := actor.RequireElevation(scope, service.clock.Now()); err != nil {
			return UserCommandExecutionResult{}, err
		}
	}
	requestDigest, err := userCommandRequestDigest(actor.AdminID(), input)
	if err != nil {
		return UserCommandExecutionResult{}, ErrInvalidInput
	}
	if existing, receiptErr := service.commandStore.GetUserCommandReceipt(ctx, actor.AdminID(), input.OperationID); receiptErr == nil {
		if existing.RequestDigest != requestDigest {
			return UserCommandExecutionResult{}, ErrIdempotencyConflict
		}
		return service.userCommandResultFromReceipt(ctx, existing)
	} else if !errors.Is(receiptErr, ErrNotFound) {
		return UserCommandExecutionResult{}, receiptErr
	}

	preview, err := service.commandStore.GetUserCommandPreview(ctx, input.PreviewID, actor.AdminID())
	if err != nil {
		return UserCommandExecutionResult{}, err
	}
	if err = validateUserCommandPreview(actor.AdminID(), preview, input); err != nil {
		return UserCommandExecutionResult{}, err
	}
	if !preview.ConsumedAt.IsZero() || !service.clock.Now().Before(preview.ExpiresAt) {
		return UserCommandExecutionResult{}, ErrConflict
	}
	preview, err = service.commandStore.ConsumeUserCommandPreview(ctx, preview.ID, actor.AdminID(), preview.Version, service.clock.Now())
	if err != nil {
		if existing, receiptErr := service.commandStore.GetUserCommandReceipt(ctx, actor.AdminID(), input.OperationID); receiptErr == nil && existing.RequestDigest == requestDigest {
			return service.userCommandResultFromReceipt(ctx, existing)
		}
		return UserCommandExecutionResult{}, err
	}

	state, err := service.singleGovernance.GetUserState(ctx, preview.Snapshot.UserID)
	if err != nil {
		return UserCommandExecutionResult{}, err
	}
	if state.Version != preview.Snapshot.ExpectedUserVersion {
		return UserCommandExecutionResult{}, ErrConflict
	}
	if len(preview.Blockers) > 0 {
		eventID, auditErr := service.recordUserCommandAudit(ctx, actor, input, state, UserCommandOutcomeRejected, 0, 0, uuid.Nil)
		if auditErr != nil {
			return UserCommandExecutionResult{}, auditErr
		}
		receipt, saveErr := service.saveUserCommandReceipt(ctx, actor, input, requestDigest, state, UserCommandOutcomeRejected, 0, 0, uuid.Nil, eventID)
		if saveErr != nil {
			return UserCommandExecutionResult{}, saveErr
		}
		return service.userCommandResultFromReceipt(ctx, receipt)
	}

	completedAt := service.clock.Now().Round(0).UTC()
	plannedOutcome, plannedRevokedDevices, plannedRemovedRooms, err := service.planUserCommandExecution(ctx, state, preview.Snapshot, input.Command, completedAt)
	if err != nil {
		return UserCommandExecutionResult{}, err
	}
	eventID, err := service.recordUserCommandAudit(ctx, actor, input, state, plannedOutcome, plannedRevokedDevices, plannedRemovedRooms, uuid.Nil)
	if err != nil {
		return UserCommandExecutionResult{}, err
	}

	var outcome UserCommandOutcome
	var revokedDevices, removedRooms int32
	var erasureJobID uuid.UUID
	switch input.Command.Type {
	case UserCommandSuspend:
		outcome = plannedOutcome
		if outcome == UserCommandOutcomeExecuted {
			if state, err = service.singleGovernance.TransitionUserStatus(ctx, input.UserID, state.Version, "suspended", completedAt); err != nil {
				return UserCommandExecutionResult{}, err
			}
		}
	case UserCommandUnsuspend:
		outcome = plannedOutcome
		if outcome == UserCommandOutcomeExecuted {
			if state, err = service.singleGovernance.TransitionUserStatus(ctx, input.UserID, state.Version, "active", completedAt); err != nil {
				return UserCommandExecutionResult{}, err
			}
		}
	case UserCommandRevokeAllDevices:
		revokedDevices = plannedRevokedDevices
		outcome = plannedOutcome
		if outcome == UserCommandOutcomeExecuted {
			revokedDevices, err = service.singleGovernance.RevokeAllDevices(ctx, input.UserID, completedAt)
			if err != nil {
				return UserCommandExecutionResult{}, err
			}
			if revokedDevices == 0 {
				return UserCommandExecutionResult{}, ErrConflict
			}
		}
	case UserCommandRemoveFromCurrentRoom:
		outcome, removedRooms = plannedOutcome, plannedRemovedRooms
		if outcome == UserCommandOutcomeExecuted {
			room := GovernanceRoomState{
				RoomID: preview.Snapshot.Room.RoomID, RoomStatus: preview.Snapshot.Room.RoomStatus,
				ExpectedRoomVersion: preview.Snapshot.Room.ExpectedRoomVersion, ExpectedMembershipVersion: preview.Snapshot.Room.ExpectedMembershipVersion,
			}
			if err = service.singleGovernance.RemoveUserFromRoom(ctx, actor.AdminID(), input.UserID, room, completedAt); err != nil {
				return UserCommandExecutionResult{}, err
			}
		}
	case UserCommandDelete:
		if plannedOutcome != UserCommandOutcomeExecuted {
			return UserCommandExecutionResult{}, ErrConflict
		}
		if binding := preview.Snapshot.Room; binding != nil {
			// Remove the reviewed waiting-room membership before deleting identity so a deleted user cannot remain eligible to start that room.
			room := GovernanceRoomState{
				RoomID: binding.RoomID, RoomStatus: binding.RoomStatus,
				ExpectedRoomVersion: binding.ExpectedRoomVersion, ExpectedMembershipVersion: binding.ExpectedMembershipVersion,
			}
			if err = service.singleGovernance.RemoveUserFromRoom(ctx, actor.AdminID(), input.UserID, room, completedAt); err != nil {
				return UserCommandExecutionResult{}, err
			}
			removedRooms = plannedRemovedRooms
		}
		deleted, deleteErr := service.singleGovernance.DeleteUser(ctx, DeleteUserCommand{
			ActorAdminID: actor.AdminID(), OperationID: input.OperationID, RequestDigest: requestDigest,
			UserID: input.UserID, ExpectedUserVersion: state.Version, Reason: input.Reason, ChangedAt: completedAt,
		})
		if deleteErr != nil {
			return UserCommandExecutionResult{}, deleteErr
		}
		state, revokedDevices, erasureJobID, outcome = deleted.User, deleted.RevokedDevices, deleted.ErasureJobID, UserCommandOutcomeExecuted
	default:
		return UserCommandExecutionResult{}, ErrInvalidInput
	}

	receipt, err := service.saveUserCommandReceipt(ctx, actor, input, requestDigest, state, outcome, revokedDevices, removedRooms, erasureJobID, eventID)
	if err != nil {
		return UserCommandExecutionResult{}, err
	}
	return service.userCommandResultFromReceipt(ctx, receipt)
}

// planUserCommandExecution repeats the resource checks immediately before the audit record is emitted.
// Once an audit event exists, every branch below either commits the intended side effect or returns a retryable conflict.
func (service *Service) planUserCommandExecution(
	ctx context.Context,
	state GovernanceUserState,
	snapshot UserCommandSnapshot,
	command UserCommandInput,
	at time.Time,
) (UserCommandOutcome, int32, int32, error) {
	switch command.Type {
	case UserCommandSuspend:
		if state.Status == "suspended" {
			return UserCommandOutcomeNoChange, 0, 0, nil
		}
		if state.Status != "active" {
			return UserCommandOutcomeRejected, 0, 0, nil
		}
		return UserCommandOutcomeExecuted, 0, 0, nil
	case UserCommandUnsuspend:
		if state.Status == "active" {
			return UserCommandOutcomeNoChange, 0, 0, nil
		}
		if state.Status != "suspended" {
			return UserCommandOutcomeRejected, 0, 0, nil
		}
		return UserCommandOutcomeExecuted, 0, 0, nil
	case UserCommandRevokeAllDevices:
		count, err := service.singleGovernance.CountActiveDevices(ctx, state.UserID, at)
		if err != nil {
			return "", 0, 0, err
		}
		if count == 0 {
			return UserCommandOutcomeNoChange, 0, 0, nil
		}
		return UserCommandOutcomeExecuted, count, 0, nil
	case UserCommandRemoveFromCurrentRoom:
		if snapshot.Room == nil {
			return "", 0, 0, ErrConflict
		}
		room, err := service.singleGovernance.GetCurrentRoom(ctx, state.UserID)
		if err != nil {
			return "", 0, 0, err
		}
		if !sameRoomBinding(snapshot.Room, room) || room.RoomStatus == "playing" {
			return "", 0, 0, ErrConflict
		}
		return UserCommandOutcomeExecuted, 0, 1, nil
	case UserCommandDelete:
		if state.Status != "active" && state.Status != "suspended" {
			return UserCommandOutcomeRejected, 0, 0, nil
		}
		pending, err := service.singleGovernance.HasPendingExport(ctx, state.UserID)
		if err != nil {
			return "", 0, 0, err
		}
		if pending {
			return UserCommandOutcomeRejected, 0, 0, nil
		}
		if snapshot.Room != nil {
			room, roomErr := service.singleGovernance.GetCurrentRoom(ctx, state.UserID)
			if roomErr != nil || !sameRoomBinding(snapshot.Room, room) || room.RoomStatus == "playing" {
				return "", 0, 0, ErrConflict
			}
		}
		count, err := service.singleGovernance.CountActiveDevices(ctx, state.UserID, at)
		if err != nil {
			return "", 0, 0, err
		}
		removedRooms := int32(0)
		if snapshot.Room != nil {
			removedRooms = 1
		}
		return UserCommandOutcomeExecuted, count, removedRooms, nil
	default:
		return "", 0, 0, ErrInvalidInput
	}
}

// populateUserCommandImpact translates live state into the exact snapshot and blockers shown before execution.
func (service *Service) populateUserCommandImpact(
	ctx context.Context,
	preview *UserCommandPreview,
	state GovernanceUserState,
	command UserCommandInput,
	at time.Time,
) error {
	if preview == nil {
		return ErrInvalidInput
	}
	switch command.Type {
	case UserCommandSuspend:
		if state.Status != "active" && state.Status != "suspended" {
			preview.Blockers = append(preview.Blockers, GovernanceBlocker{Type: GovernanceBlockerVersionChanged, ResourceID: state.UserID.String(), MessageKey: "admin.user.command.invalid_status"})
		}
	case UserCommandUnsuspend:
		if state.Status != "active" && state.Status != "suspended" {
			preview.Blockers = append(preview.Blockers, GovernanceBlocker{Type: GovernanceBlockerVersionChanged, ResourceID: state.UserID.String(), MessageKey: "admin.user.command.invalid_status"})
		}
	case UserCommandRevokeAllDevices:
		count, err := service.singleGovernance.CountActiveDevices(ctx, state.UserID, at)
		if err != nil {
			return err
		}
		preview.AffectedDevices = count
	case UserCommandRemoveFromCurrentRoom:
		room, err := service.singleGovernance.GetCurrentRoom(ctx, state.UserID)
		if errors.Is(err, ErrNotFound) {
			preview.Blockers = append(preview.Blockers, GovernanceBlocker{Type: GovernanceBlockerVersionChanged, ResourceID: state.UserID.String(), MessageKey: "admin.user.room.not_found"})
			return nil
		}
		if err != nil {
			return err
		}
		if room.RoomID != command.RoomID || room.ExpectedRoomVersion != command.ExpectedRoomVersion || room.ExpectedMembershipVersion != command.ExpectedMembershipVersion {
			preview.Blockers = append(preview.Blockers, GovernanceBlocker{Type: GovernanceBlockerVersionChanged, ResourceID: state.UserID.String(), MessageKey: "admin.user.version_changed"})
			return nil
		}
		if room.RoomStatus == "playing" {
			preview.Blockers = append(preview.Blockers, GovernanceBlocker{Type: GovernanceBlockerActiveGame, ResourceID: room.RoomID.String(), MessageKey: "admin.user.room.active_game"})
			return nil
		}
		preview.Snapshot.Room = &BatchRoomBinding{
			RoomID: room.RoomID, RoomStatus: room.RoomStatus,
			ExpectedRoomVersion: room.ExpectedRoomVersion, ExpectedMembershipVersion: room.ExpectedMembershipVersion,
		}
		preview.AffectedRooms = 1
	case UserCommandDelete:
		pending, err := service.singleGovernance.HasPendingExport(ctx, state.UserID)
		if err != nil {
			return err
		}
		if pending {
			preview.Blockers = append(preview.Blockers, GovernanceBlocker{Type: GovernanceBlockerPendingExport, ResourceID: state.UserID.String(), MessageKey: "admin.user.pending_export"})
		}
		count, err := service.singleGovernance.CountActiveDevices(ctx, state.UserID, at)
		if err != nil {
			return err
		}
		preview.AffectedDevices = count
		room, roomErr := service.singleGovernance.GetCurrentRoom(ctx, state.UserID)
		if roomErr == nil {
			if room.RoomStatus == "playing" {
				preview.Blockers = append(preview.Blockers, GovernanceBlocker{Type: GovernanceBlockerActiveGame, ResourceID: room.RoomID.String(), MessageKey: "admin.user.room.active_game"})
			} else {
				preview.Snapshot.Room = &BatchRoomBinding{
					RoomID: room.RoomID, RoomStatus: room.RoomStatus,
					ExpectedRoomVersion: room.ExpectedRoomVersion, ExpectedMembershipVersion: room.ExpectedMembershipVersion,
				}
				preview.AffectedRooms = 1
			}
		} else if !errors.Is(roomErr, ErrNotFound) {
			return roomErr
		}
	default:
		return ErrInvalidInput
	}
	return nil
}

// recordUserCommandAudit writes intent before a destructive side effect. A later retry can use the receipt to distinguish
// a completed command from an interrupted response without suppressing evidence that an administrator initiated it.
func (service *Service) recordUserCommandAudit(
	ctx context.Context,
	actor admin.ActorContext,
	input ExecuteUserCommandInput,
	state GovernanceUserState,
	outcome UserCommandOutcome,
	revokedDevices, removedRooms int32,
	erasureJobID uuid.UUID,
) (uuid.UUID, error) {
	eventID, err := service.audit.RecordAnnotationWrite(ctx, AnnotationAuditEvent{
		ActorAdminID: actor.AdminID(), UserID: input.UserID, OperationID: input.OperationID,
		Action: userCommandAuditAction(input.Command.Type, outcome), Reason: input.Reason,
		DetailDigest: digestStrings(input.UserID.String(), string(input.Command.Type), string(outcome), fmt.Sprintf("%d", state.Version), fmt.Sprintf("%d", revokedDevices), fmt.Sprintf("%d", removedRooms), erasureJobID.String()),
		RequestID:    fmt.Sprintf("admin-user-command:%s", input.OperationID.Value()), OccurredAt: service.clock.Now(),
	})
	if err != nil {
		return uuid.Nil, ErrAuditUnavailable
	}
	return eventID, nil
}

// saveUserCommandReceipt persists the exact response after the side effect has committed.
func (service *Service) saveUserCommandReceipt(
	ctx context.Context,
	actor admin.ActorContext,
	input ExecuteUserCommandInput,
	requestDigest [sha256.Size]byte,
	state GovernanceUserState,
	outcome UserCommandOutcome,
	revokedDevices, removedRooms int32,
	erasureJobID, eventID uuid.UUID,
) (UserCommandReceipt, error) {
	receipt, err := service.commandStore.SaveUserCommandReceipt(ctx, UserCommandReceipt{
		ActorAdminID: actor.AdminID(), OperationID: input.OperationID, RequestDigest: requestDigest, PreviewID: input.PreviewID,
		UserID: input.UserID, Command: input.Command.Type, Outcome: outcome, UserVersion: state.Version,
		RevokedDevices: revokedDevices, RemovedRooms: removedRooms, ErasureJobID: erasureJobID, AuditEventID: eventID,
		CompletedAt: service.clock.Now().Round(0).UTC(),
	})
	if err != nil {
		return UserCommandReceipt{}, err
	}
	return receipt, nil
}

func (service *Service) userCommandResultFromReceipt(ctx context.Context, receipt UserCommandReceipt) (UserCommandExecutionResult, error) {
	if receipt.UserID == uuid.Nil || receipt.UserVersion == 0 {
		return UserCommandExecutionResult{}, ErrIntegrity
	}
	rows, err := service.repository.ListUsers(ctx, UserListQuery{
		UserID: receipt.UserID, PageSize: 1, SampledAt: service.clock.Now(), SortField: UserSortUserID, Direction: SortAscending,
	})
	if err != nil {
		return UserCommandExecutionResult{}, err
	}
	if len(rows) != 1 || rows[0].Version != receipt.UserVersion {
		return UserCommandExecutionResult{}, ErrConflict
	}
	return UserCommandExecutionResult{
		Receipt: receipt, User: rows[0], ErasureJobID: receipt.ErasureJobID,
		RevokedDevices: receipt.RevokedDevices, RemovedRooms: receipt.RemovedRooms,
	}, nil
}

func validateUserCommandPreview(actorID uuid.UUID, preview UserCommandPreview, input ExecuteUserCommandInput) error {
	if preview.ActorAdminID != actorID || preview.Snapshot.SchemaVersion != UserCommandSnapshotSchemaVersion || preview.Snapshot.UserID != input.UserID ||
		preview.Snapshot.ExpectedUserVersion != input.ExpectedUserVersion || preview.Snapshot.Command != input.Command.Type || preview.PreviewDigest != input.PreviewDigest ||
		!sameUserCommandInput(preview.Snapshot, input.Command) {
		return ErrConflict
	}
	want, err := userCommandPreviewDigest(actorID, preview.Snapshot, input.Reason)
	if err != nil || want != preview.PreviewDigest {
		return ErrConflict
	}
	return nil
}

func requireUserCommandPermission(actor admin.ActorContext, command UserCommand) error {
	switch command {
	case UserCommandSuspend, UserCommandUnsuspend, UserCommandRevokeAllDevices, UserCommandDelete:
		if err := actor.Require(admin.PermissionUsersGovern); err != nil {
			return ErrPermissionDenied
		}
	case UserCommandRemoveFromCurrentRoom:
		if err := actor.Require(admin.PermissionRoomsControl); err != nil {
			return ErrPermissionDenied
		}
	default:
		return ErrInvalidInput
	}
	return nil
}

func requiredUserCommandElevation(command UserCommand) admin.ElevationScope {
	switch command {
	case UserCommandRevokeAllDevices:
		return admin.ElevationScopeUsersRevokeDevices
	case UserCommandDelete:
		return admin.ElevationScopeUsersDelete
	default:
		return ""
	}
}

func validUserCommandInput(command UserCommandInput) bool {
	switch command.Type {
	case UserCommandSuspend, UserCommandUnsuspend, UserCommandRevokeAllDevices, UserCommandDelete:
		return command.RoomID == uuid.Nil && command.ExpectedRoomVersion == 0 && command.ExpectedMembershipVersion == 0
	case UserCommandRemoveFromCurrentRoom:
		return command.RoomID != uuid.Nil && command.ExpectedRoomVersion > 0 && command.ExpectedMembershipVersion > 0
	default:
		return false
	}
}

func sameUserCommandInput(snapshot UserCommandSnapshot, command UserCommandInput) bool {
	if snapshot.Command != command.Type {
		return false
	}
	switch command.Type {
	case UserCommandSuspend, UserCommandUnsuspend, UserCommandRevokeAllDevices:
		return command.RoomID == uuid.Nil && command.ExpectedRoomVersion == 0 && command.ExpectedMembershipVersion == 0 && snapshot.Room == nil
	case UserCommandDelete:
		// The room binding belongs exclusively to the durable preview and is revalidated server-side before removal.
		return command.RoomID == uuid.Nil && command.ExpectedRoomVersion == 0 && command.ExpectedMembershipVersion == 0
	case UserCommandRemoveFromCurrentRoom:
		return snapshot.Room != nil && snapshot.Room.RoomID == command.RoomID && snapshot.Room.ExpectedRoomVersion == command.ExpectedRoomVersion &&
			snapshot.Room.ExpectedMembershipVersion == command.ExpectedMembershipVersion
	default:
		return false
	}
}

func sameRoomBinding(expected *BatchRoomBinding, actual GovernanceRoomState) bool {
	return expected != nil && expected.RoomID == actual.RoomID && expected.ExpectedRoomVersion == actual.ExpectedRoomVersion &&
		expected.ExpectedMembershipVersion == actual.ExpectedMembershipVersion && expected.RoomStatus == actual.RoomStatus
}

func userCommandPreviewDigest(actorID uuid.UUID, snapshot UserCommandSnapshot, reason string) ([sha256.Size]byte, error) {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	hash := sha256.New()
	hash.Write([]byte(actorID.String()))
	hash.Write([]byte{0})
	hash.Write(raw)
	hash.Write([]byte{0})
	hash.Write([]byte(strings.TrimSpace(reason)))
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}

func userCommandRequestDigest(actorID uuid.UUID, input ExecuteUserCommandInput) ([sha256.Size]byte, error) {
	if !input.OperationID.Valid() {
		return [sha256.Size]byte{}, ErrInvalidInput
	}
	body := struct {
		UserID              uuid.UUID         `json:"user_id"`
		Command             UserCommandInput  `json:"command"`
		PreviewID           uuid.UUID         `json:"preview_id"`
		PreviewDigest       [sha256.Size]byte `json:"preview_digest"`
		Reason              string            `json:"reason"`
		ExpectedUserVersion uint64            `json:"expected_user_version"`
	}{
		UserID: input.UserID, Command: input.Command, PreviewID: input.PreviewID, PreviewDigest: input.PreviewDigest,
		Reason: strings.TrimSpace(input.Reason), ExpectedUserVersion: input.ExpectedUserVersion,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	hash := sha256.New()
	hash.Write([]byte(actorID.String()))
	hash.Write([]byte{0})
	hash.Write([]byte(input.OperationID.Value()))
	hash.Write([]byte{0})
	hash.Write(raw)
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}

func userCommandAuditAction(command UserCommand, outcome UserCommandOutcome) string {
	return fmt.Sprintf("admin_user_%s_%s", command, outcome)
}
