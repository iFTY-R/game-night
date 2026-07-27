package user

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
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
	// BatchPreviewSelectionSchemaVersion invalidates stored preview payloads when the frozen target shape changes.
	BatchPreviewSelectionSchemaVersion uint32 = 1
	// DefaultBatchPageSize keeps job history queries bounded without requiring clients to send a page size.
	DefaultBatchPageSize uint32 = 20
	// MaximumBatchPageSize caps batch history reads and matches the user-center list bounds.
	MaximumBatchPageSize uint32 = 200
	// maximumBatchPreviewTargets bounds a single preview so one approval cannot accidentally materialize an unbounded job.
	maximumBatchPreviewTargets = 1000
	// batchExecutionLease prevents a crashed worker from holding one target indefinitely before another worker can reclaim it.
	batchExecutionLease = time.Minute
)

type BatchCommand string

const (
	BatchCommandSuspend               BatchCommand = "suspend"
	BatchCommandUnsuspend             BatchCommand = "unsuspend"
	BatchCommandRemoveFromCurrentRoom BatchCommand = "remove_from_current_room"
)

type GovernanceBlockerType string

const (
	GovernanceBlockerActiveGame     GovernanceBlockerType = "active_game"
	GovernanceBlockerPendingExport  GovernanceBlockerType = "pending_export"
	GovernanceBlockerVersionChanged GovernanceBlockerType = "version_changed"
	GovernanceBlockerAlreadyDeleted GovernanceBlockerType = "already_deleted"
)

type BatchSelection struct {
	Filter          *ListUsersInput
	ExplicitTargets []VersionedUserTarget
}

type VersionedUserTarget struct {
	UserID              uuid.UUID
	ExpectedUserVersion uint64
}

type GovernanceBlocker struct {
	Type       GovernanceBlockerType
	ResourceID string
	MessageKey string
}

// BatchRoomBinding freezes the exact room membership reviewed during preview for later room-removal retries.
type BatchRoomBinding struct {
	RoomID                    uuid.UUID `json:"room_id"`
	RoomStatus                string    `json:"room_status"`
	ExpectedRoomVersion       uint64    `json:"expected_room_version"`
	ExpectedMembershipVersion uint64    `json:"expected_membership_version"`
}

// BatchExecutableTarget is the preview-owned immutable target set later frozen into job items.
type BatchExecutableTarget struct {
	ItemID              uuid.UUID         `json:"item_id"`
	UserID              uuid.UUID         `json:"user_id"`
	ExpectedUserVersion uint64            `json:"expected_user_version"`
	Room                *BatchRoomBinding `json:"room,omitempty"`
}

type batchSelectionSnapshot struct {
	SchemaVersion uint32                  `json:"schema_version"`
	Command       BatchCommand            `json:"command"`
	SampledAtUnix int64                   `json:"sampled_at_unix"`
	Targets       []BatchExecutableTarget `json:"targets"`
}

type BatchPreviewResult struct {
	Preview           BatchPreview
	SampledBlockers   []GovernanceBlocker
	RequiredElevation admin.ElevationScope
	SampledAt         time.Time
}

type PreviewBatchCommand struct {
	Selection BatchSelection
	Command   BatchCommand
	Reason    string
}

type StartBatchCommand struct {
	OperationID     idempotency.OperationID
	PreviewID       uuid.UUID
	PreviewDigest   [sha256.Size]byte
	ExpectedVersion uint64
	Reason          string
}

type BatchJobListQuery struct {
	States      []string
	Commands    []BatchCommand
	CreatedFrom time.Time
	CreatedTo   time.Time
	SortField   BatchJobSortField
	Direction   SortDirection
	PageSize    uint32
	PageToken   string
	After       BatchJobListPosition
}

type BatchItemListQuery struct {
	BatchJobID uuid.UUID
	States     []string
	PageSize   uint32
	PageToken  string
	After      BatchItemListPosition
}

type BatchJobSortField string

const (
	BatchJobSortCreatedAt BatchJobSortField = "created_at"
	BatchJobSortUpdatedAt BatchJobSortField = "updated_at"
	BatchJobSortID        BatchJobSortField = "batch_job_id"
)

type CancelBatchCommand struct {
	OperationID     idempotency.OperationID
	BatchJobID      uuid.UUID
	ExpectedVersion uint64
	Reason          string
}

type RetryBatchCommand struct {
	OperationID     idempotency.OperationID
	BatchJobID      uuid.UUID
	ItemIDs         []uuid.UUID
	ExpectedVersion uint64
	Reason          string
}

type RetryBatchResult struct {
	BatchJob      BatchJob
	RequeuedItems int64
}

// GovernanceExecutor owns the version-checked side effects performed by the durable batch worker.
type GovernanceExecutor interface {
	GetUserState(context.Context, uuid.UUID) (GovernanceUserState, error)
	GetCurrentRoom(context.Context, uuid.UUID) (GovernanceRoomState, error)
	TransitionUserStatus(context.Context, uuid.UUID, uint64, string, time.Time) (GovernanceUserState, error)
	RemoveUserFromRoom(context.Context, uuid.UUID, uuid.UUID, GovernanceRoomState, time.Time) error
}

type GovernanceUserState struct {
	UserID  uuid.UUID
	Status  string
	Version uint64
}

type GovernanceRoomState struct {
	RoomID                    uuid.UUID
	RoomStatus                string
	ExpectedRoomVersion       uint64
	ExpectedMembershipVersion uint64
}

func (service *Service) PreviewBatchUserOperation(ctx context.Context, actor admin.ActorContext, command PreviewBatchCommand) (BatchPreviewResult, error) {
	if service == nil || service.repository == nil || service.jobs == nil || service.governance == nil || service.clock == nil || ctx == nil ||
		!validBatchCommand(command.Command) || !validReason(command.Reason) {
		return BatchPreviewResult{}, ErrInvalidInput
	}
	if err := actor.Require(admin.PermissionUsersGovern); err != nil {
		return BatchPreviewResult{}, ErrPermissionDenied
	}
	now := service.clock.Now()
	targets, blockers, sampledAt, err := service.resolveBatchTargets(ctx, command.Selection, command.Command, now)
	if err != nil {
		return BatchPreviewResult{}, err
	}
	snapshot, err := marshalBatchSelectionSnapshot(command.Command, sampledAt, targets)
	if err != nil {
		return BatchPreviewResult{}, err
	}
	selectionDigest := sha256.Sum256(snapshot)
	previewDigest := batchPreviewDigest(actor.AdminID(), command.Command, command.Reason, snapshot)
	previewID, err := uuid.NewV7()
	if err != nil {
		return BatchPreviewResult{}, ErrInvalidInput
	}
	expiresAt := now.Add(5 * time.Minute)
	preview, err := service.jobs.CreateBatchPreview(ctx, CreateBatchPreviewCommand{Preview: BatchPreview{
		ID: previewID, ActorAdminID: actor.AdminID(), Command: string(command.Command),
		SelectionSchemaVersion: BatchPreviewSelectionSchemaVersion, SelectionSnapshot: snapshot,
		SelectionDigest: selectionDigest, PreviewDigest: previewDigest,
		TargetCount: int64(len(targets) + len(blockers)), ExecutableCount: int64(len(targets)), BlockedCount: int64(len(blockers)),
		SampledAt: sampledAt, ExpiresAt: expiresAt,
	}})
	if err != nil {
		return BatchPreviewResult{}, err
	}
	return BatchPreviewResult{
		Preview:           preview,
		SampledBlockers:   blockers,
		RequiredElevation: admin.ElevationScopeUsersBulkGovernance,
		SampledAt:         sampledAt,
	}, nil
}

func (service *Service) StartBatchUserOperation(ctx context.Context, actor admin.ActorContext, command StartBatchCommand) (BatchJob, error) {
	if service == nil || service.jobs == nil || service.clock == nil || ctx == nil ||
		command.PreviewID == uuid.Nil || !command.OperationID.Valid() || command.ExpectedVersion == 0 || !validReason(command.Reason) {
		return BatchJob{}, ErrInvalidInput
	}
	if err := actor.Require(admin.PermissionUsersGovern); err != nil {
		return BatchJob{}, ErrPermissionDenied
	}
	if err := actor.RequireElevation(admin.ElevationScopeUsersBulkGovernance, service.clock.Now()); err != nil {
		return BatchJob{}, err
	}
	preview, err := service.loadBatchPreview(ctx, actor.AdminID(), command.PreviewID, command.PreviewDigest)
	if err != nil {
		return BatchJob{}, err
	}
	frozenTargets, err := decodeBatchSelectionTargets(preview.SelectionSnapshot)
	if err != nil || len(frozenTargets) == 0 {
		return BatchJob{}, ErrIntegrity
	}
	batchJobID, err := uuid.NewV7()
	if err != nil {
		return BatchJob{}, ErrInvalidInput
	}
	startedAt := service.clock.Now()
	jobTargets := make([]BatchTarget, 0, len(frozenTargets))
	for _, target := range frozenTargets {
		jobTargets = append(jobTargets, BatchTarget{
			ItemID: target.ItemID, UserID: target.UserID, ExpectedUserVersion: target.ExpectedUserVersion,
			RequestDigest: batchItemDigest(command.OperationID.Value(), target),
		})
	}
	job, err := service.jobs.StartBatchJob(ctx, StartBatchJobCommand{
		BatchJobID: batchJobID, ActorAdminID: actor.AdminID(), OperationID: command.OperationID.Value(),
		RequestDigest: batchJobDigest(actor.AdminID(), command.OperationID.Value(), command.Reason, preview.SelectionDigest),
		PreviewID:     command.PreviewID, PreviewDigest: command.PreviewDigest, ExpectedPreviewVersion: command.ExpectedVersion,
		Reason: command.Reason, Targets: jobTargets, CreatedAt: startedAt,
	})
	if err != nil {
		return BatchJob{}, err
	}
	// Execution is intentionally deferred to the lease-owning worker. HTTP only creates the durable task.
	return job, nil
}

func (service *Service) GetBatchUserOperation(ctx context.Context, actor admin.ActorContext, batchJobID uuid.UUID) (BatchJob, error) {
	return service.getBatchJob(ctx, actor, batchJobID)
}

func (service *Service) ListBatchUserOperations(ctx context.Context, actor admin.ActorContext, query BatchJobListQuery) ([]BatchJob, string, time.Time, error) {
	if service == nil || service.jobs == nil || service.clock == nil || ctx == nil {
		return nil, "", time.Time{}, ErrInvalidInput
	}
	if err := actor.Require(admin.PermissionUsersGovern); err != nil {
		return nil, "", time.Time{}, ErrPermissionDenied
	}
	if query.PageSize == 0 {
		query.PageSize = DefaultBatchPageSize
	}
	if query.PageSize > MaximumBatchPageSize || !validBatchJobSort(query.SortField, query.Direction) || !validBatchStateFilter(query.States) || !validBatchCommandFilter(query.Commands) ||
		(query.CreatedFrom.After(query.CreatedTo) && !query.CreatedTo.IsZero()) {
		return nil, "", time.Time{}, ErrInvalidInput
	}
	if query.SortField == "" {
		query.SortField = BatchJobSortCreatedAt
	}
	if query.Direction == "" {
		query.Direction = SortDescending
	}
	filterDigest := batchJobQueryDigest(query)
	if query.PageToken != "" {
		if service.cursor == nil {
			return nil, "", time.Time{}, ErrInvalidInput
		}
		after, err := service.cursor.DecodeBatchJob(query.PageToken, filterDigest, query.SortField, query.Direction)
		if err != nil {
			return nil, "", time.Time{}, err
		}
		query.After = after
	}
	rows, err := service.jobs.ListBatchJobs(ctx, query)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	nextToken := ""
	if len(rows) == int(query.PageSize) && service.cursor != nil {
		position, positionErr := batchJobListPosition(rows[len(rows)-1], query.SortField)
		if positionErr != nil {
			return nil, "", time.Time{}, positionErr
		}
		nextToken, err = service.cursor.EncodeBatchJob(filterDigest, query.SortField, query.Direction, position)
		if err != nil {
			return nil, "", time.Time{}, err
		}
	}
	return rows, nextToken, service.clock.Now(), nil
}

func (service *Service) ListBatchUserOperationItems(ctx context.Context, actor admin.ActorContext, query BatchItemListQuery) ([]BatchItem, string, time.Time, error) {
	if service == nil || service.jobs == nil || service.clock == nil || ctx == nil || query.BatchJobID == uuid.Nil {
		return nil, "", time.Time{}, ErrInvalidInput
	}
	if err := actor.Require(admin.PermissionUsersGovern); err != nil {
		return nil, "", time.Time{}, ErrPermissionDenied
	}
	if query.PageSize == 0 {
		query.PageSize = DefaultBatchPageSize
	}
	if query.PageSize > MaximumBatchPageSize || !validBatchItemStateFilter(query.States) {
		return nil, "", time.Time{}, ErrInvalidInput
	}
	filterDigest := batchItemQueryDigest(query.BatchJobID, query.States)
	if query.PageToken != "" {
		if service.cursor == nil {
			return nil, "", time.Time{}, ErrInvalidInput
		}
		after, err := service.cursor.DecodeBatchItem(query.PageToken, filterDigest, query.BatchJobID)
		if err != nil {
			return nil, "", time.Time{}, err
		}
		query.After = after
	}
	rows, err := service.jobs.ListBatchItems(ctx, query)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	nextToken := ""
	if len(rows) == int(query.PageSize) && service.cursor != nil {
		position, positionErr := batchItemListPosition(rows[len(rows)-1])
		if positionErr != nil {
			return nil, "", time.Time{}, positionErr
		}
		nextToken, err = service.cursor.EncodeBatchItem(filterDigest, query.BatchJobID, position)
		if err != nil {
			return nil, "", time.Time{}, err
		}
	}
	return rows, nextToken, service.clock.Now(), nil
}

func (service *Service) CancelBatchUserOperation(ctx context.Context, actor admin.ActorContext, command CancelBatchCommand) (BatchJob, error) {
	if service == nil || service.jobs == nil || service.clock == nil || ctx == nil || command.BatchJobID == uuid.Nil ||
		!command.OperationID.Valid() || command.ExpectedVersion == 0 || !validReason(command.Reason) {
		return BatchJob{}, ErrInvalidInput
	}
	if err := actor.Require(admin.PermissionUsersGovern); err != nil {
		return BatchJob{}, ErrPermissionDenied
	}
	if err := actor.RequireElevation(admin.ElevationScopeUsersBulkGovernance, service.clock.Now()); err != nil {
		return BatchJob{}, err
	}
	job, err := service.getBatchJob(ctx, actor, command.BatchJobID)
	if err != nil {
		return BatchJob{}, err
	}
	if job.Version != command.ExpectedVersion {
		return BatchJob{}, ErrConflict
	}
	if job.State == "canceled" || job.State == "succeeded" || job.State == "failed" || job.State == "partially_succeeded" {
		return job, nil
	}
	updated, err := service.jobs.CancelBatchJob(ctx, command.BatchJobID, command.ExpectedVersion, service.clock.Now())
	if err != nil {
		return BatchJob{}, err
	}
	return updated, nil
}

func (service *Service) RetryBatchUserOperation(ctx context.Context, actor admin.ActorContext, command RetryBatchCommand) (RetryBatchResult, error) {
	if service == nil || service.jobs == nil || service.clock == nil || ctx == nil ||
		command.BatchJobID == uuid.Nil || !command.OperationID.Valid() || command.ExpectedVersion == 0 || !validReason(command.Reason) {
		return RetryBatchResult{}, ErrInvalidInput
	}
	if err := actor.Require(admin.PermissionUsersGovern); err != nil {
		return RetryBatchResult{}, ErrPermissionDenied
	}
	if err := actor.RequireElevation(admin.ElevationScopeUsersBulkGovernance, service.clock.Now()); err != nil {
		return RetryBatchResult{}, err
	}
	updated, requeued, err := service.jobs.RetryBatchJob(ctx, command.BatchJobID, command.ItemIDs, command.ExpectedVersion, service.clock.Now())
	if err != nil {
		return RetryBatchResult{}, err
	}
	return RetryBatchResult{BatchJob: updated, RequeuedItems: requeued}, nil
}

func (service *Service) getBatchJob(ctx context.Context, actor admin.ActorContext, batchJobID uuid.UUID) (BatchJob, error) {
	if service == nil || service.jobs == nil || ctx == nil || batchJobID == uuid.Nil {
		return BatchJob{}, ErrInvalidInput
	}
	if err := actor.Require(admin.PermissionUsersGovern); err != nil {
		return BatchJob{}, ErrPermissionDenied
	}
	job, err := service.jobs.GetBatchJob(ctx, batchJobID)
	if err != nil {
		return BatchJob{}, err
	}
	return job, nil
}

// loadBatchPreview reconstructs the frozen targets for both a first submission and an idempotent replay.
// The repository owns expected-version enforcement after its operation-ID lookup, so a retry can return an
// already-created job even though the original preview has since been consumed and versioned forward.
func (service *Service) loadBatchPreview(
	ctx context.Context,
	adminID uuid.UUID,
	previewID uuid.UUID,
	expectedDigest [sha256.Size]byte,
) (BatchPreview, error) {
	preview, err := service.jobs.GetBatchPreview(ctx, previewID, adminID)
	if err != nil {
		return BatchPreview{}, err
	}
	if preview.PreviewDigest != expectedDigest {
		return BatchPreview{}, ErrConflict
	}
	return preview, nil
}

func (service *Service) resolveBatchTargets(
	ctx context.Context,
	selection BatchSelection,
	command BatchCommand,
	sampledAt time.Time,
) ([]BatchExecutableTarget, []GovernanceBlocker, time.Time, error) {
	targets := make([]BatchExecutableTarget, 0)
	blockers := make([]GovernanceBlocker, 0)
	appendResolved := func(userID uuid.UUID, expectedVersion uint64, explicit bool) error {
		user, err := service.governance.GetUserState(ctx, userID)
		if err != nil {
			return err
		}
		if explicit && expectedVersion > 0 && user.Version != expectedVersion {
			blockers = append(blockers, GovernanceBlocker{Type: GovernanceBlockerVersionChanged, ResourceID: userID.String(), MessageKey: "admin.user.version_changed"})
			return nil
		}
		target, blocker, err := service.previewBatchTarget(ctx, command, user)
		if err != nil {
			return err
		}
		if blocker != nil {
			blockers = append(blockers, *blocker)
			return nil
		}
		targets = append(targets, target)
		return nil
	}
	switch {
	case selection.Filter != nil:
		input := *selection.Filter
		input.PageToken = ""
		input.PageSize = MaximumUserPageSize
		input.SortField = UserSortUserID
		input.Direction = SortAscending
		normalized, _, err := normalizeListUsersInput(input, sampledAt)
		if err != nil {
			return nil, nil, time.Time{}, err
		}
		for {
			rows, err := service.repository.ListUsers(ctx, normalized)
			if err != nil {
				return nil, nil, time.Time{}, err
			}
			for _, row := range rows {
				if len(targets)+len(blockers) >= maximumBatchPreviewTargets {
					return nil, nil, time.Time{}, ErrInvalidInput
				}
				if err := appendResolved(row.ID, row.Version, false); err != nil {
					return nil, nil, time.Time{}, err
				}
			}
			if len(rows) < int(normalized.PageSize) {
				break
			}
			normalized.After = UserListPosition{UserID: rows[len(rows)-1].ID}
		}
	case len(selection.ExplicitTargets) > 0:
		seen := make(map[uuid.UUID]struct{}, len(selection.ExplicitTargets))
		for _, target := range selection.ExplicitTargets {
			if target.UserID == uuid.Nil || target.ExpectedUserVersion == 0 {
				return nil, nil, time.Time{}, ErrInvalidInput
			}
			if _, exists := seen[target.UserID]; exists {
				return nil, nil, time.Time{}, ErrInvalidInput
			}
			seen[target.UserID] = struct{}{}
			if err := appendResolved(target.UserID, target.ExpectedUserVersion, true); err != nil {
				return nil, nil, time.Time{}, err
			}
		}
	default:
		return nil, nil, time.Time{}, ErrInvalidInput
	}
	return targets, sampleBlockers(blockers), sampledAt.Round(0).UTC(), nil
}

func (service *Service) previewBatchTarget(ctx context.Context, command BatchCommand, user GovernanceUserState) (BatchExecutableTarget, *GovernanceBlocker, error) {
	if user.Status == "deleted" {
		blocker := GovernanceBlocker{Type: GovernanceBlockerAlreadyDeleted, ResourceID: user.UserID.String(), MessageKey: "admin.user.already_deleted"}
		return BatchExecutableTarget{}, &blocker, nil
	}
	target := BatchExecutableTarget{UserID: user.UserID, ExpectedUserVersion: user.Version}
	itemID, err := uuid.NewV7()
	if err != nil {
		return BatchExecutableTarget{}, nil, ErrInvalidInput
	}
	target.ItemID = itemID
	if command != BatchCommandRemoveFromCurrentRoom {
		return target, nil, nil
	}
	room, err := service.governance.GetCurrentRoom(ctx, user.UserID)
	if errors.Is(err, ErrNotFound) {
		blocker := GovernanceBlocker{Type: GovernanceBlockerVersionChanged, ResourceID: user.UserID.String(), MessageKey: "admin.user.room.not_found"}
		return BatchExecutableTarget{}, &blocker, nil
	}
	if err != nil {
		return BatchExecutableTarget{}, nil, err
	}
	if room.RoomStatus == "playing" {
		blocker := GovernanceBlocker{Type: GovernanceBlockerActiveGame, ResourceID: room.RoomID.String(), MessageKey: "admin.user.room.active_game"}
		return BatchExecutableTarget{}, &blocker, nil
	}
	target.Room = &BatchRoomBinding{
		RoomID: room.RoomID, RoomStatus: room.RoomStatus, ExpectedRoomVersion: room.ExpectedRoomVersion, ExpectedMembershipVersion: room.ExpectedMembershipVersion,
	}
	return target, nil, nil
}

// ProcessNextBatchItem claims exactly one durable item. The worker calls this repeatedly, while lease ownership
// makes concurrent workers and crash recovery safe without making the HTTP request a long-running executor.
func (service *Service) ProcessNextBatchItem(ctx context.Context, workerID string) (bool, error) {
	if service == nil || service.jobs == nil || service.governance == nil || service.clock == nil || ctx == nil || !validBatchWorkerID(workerID) {
		return false, ErrInvalidInput
	}
	item, err := service.jobs.ClaimNextBatchItem(ctx, batchLeaseOwner(workerID, uuid.Nil), batchExecutionLease)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	job, err := service.jobs.GetBatchJob(ctx, item.BatchJobID)
	if err != nil {
		return false, err
	}
	// A cancellation can race a crashed worker's expired lease. Claim the item only to close its accounting;
	// never restart a side effect after the administrator has requested that the batch stop.
	if job.State == "canceling" {
		if _, err = service.jobs.CompleteBatchItem(ctx, item, "canceled", "", uuid.Nil, service.clock.Now()); err != nil {
			return false, err
		}
		return true, nil
	}
	snapshot, err := decodeBatchSelectionSnapshot(job.SelectionSnapshot)
	if err != nil || string(snapshot.Command) != job.Command {
		return false, ErrIntegrity
	}
	targets := make(map[uuid.UUID]BatchExecutableTarget, len(snapshot.Targets))
	for _, target := range snapshot.Targets {
		targets[target.ItemID] = target
	}
	nextState, errorKey, auditEventID := service.executeBatchItem(ctx, job, snapshot.Command, item, targets[item.ID])
	if _, err = service.jobs.CompleteBatchItem(ctx, item, nextState, errorKey, auditEventID, service.clock.Now()); err != nil {
		return false, err
	}
	return true, nil
}

func (service *Service) executeBatchItem(
	ctx context.Context,
	job BatchJob,
	command BatchCommand,
	item BatchItem,
	target BatchExecutableTarget,
) (string, string, uuid.UUID) {
	if target.UserID == uuid.Nil || target.UserID != item.UserID {
		return "failed", "admin.user.batch.target_missing", uuid.Nil
	}
	user, err := service.governance.GetUserState(ctx, item.UserID)
	if err != nil {
		return "failed", "admin.user.load_failed", uuid.Nil
	}
	if user.Version != item.ExpectedUserVersion {
		return "skipped", "admin.user.version_changed", uuid.Nil
	}
	switch command {
	case BatchCommandSuspend:
		if user.Status == "suspended" {
			return "skipped", "admin.user.command.no_change", uuid.Nil
		}
		auditEventID, auditErr := service.recordBatchAudit(ctx, job, item, "batch_suspend_user", targetDigestStrings(target)...)
		if auditErr != nil {
			return "failed", "audit.write.unavailable", uuid.Nil
		}
		if _, err = service.governance.TransitionUserStatus(ctx, item.UserID, item.ExpectedUserVersion, "suspended", service.clock.Now()); err != nil {
			state, key, _ := batchExecutionError(err)
			return state, key, auditEventID
		}
		return "succeeded", "", auditEventID
	case BatchCommandUnsuspend:
		if user.Status == "active" {
			return "skipped", "admin.user.command.no_change", uuid.Nil
		}
		auditEventID, auditErr := service.recordBatchAudit(ctx, job, item, "batch_unsuspend_user", targetDigestStrings(target)...)
		if auditErr != nil {
			return "failed", "audit.write.unavailable", uuid.Nil
		}
		if _, err = service.governance.TransitionUserStatus(ctx, item.UserID, item.ExpectedUserVersion, "active", service.clock.Now()); err != nil {
			state, key, _ := batchExecutionError(err)
			return state, key, auditEventID
		}
		return "succeeded", "", auditEventID
	case BatchCommandRemoveFromCurrentRoom:
		if target.Room == nil {
			return "failed", "admin.user.batch.room_target_missing", uuid.Nil
		}
		currentRoom, roomErr := service.governance.GetCurrentRoom(ctx, item.UserID)
		if roomErr != nil {
			if errors.Is(roomErr, ErrNotFound) {
				return "skipped", "admin.user.room.not_found", uuid.Nil
			}
			return "failed", "admin.user.room.load_failed", uuid.Nil
		}
		if currentRoom.RoomID != target.Room.RoomID || currentRoom.ExpectedRoomVersion != target.Room.ExpectedRoomVersion ||
			currentRoom.ExpectedMembershipVersion != target.Room.ExpectedMembershipVersion {
			return "skipped", "admin.user.version_changed", uuid.Nil
		}
		if currentRoom.RoomStatus == "playing" {
			return "skipped", "admin.user.room.active_game", uuid.Nil
		}
		auditEventID, auditErr := service.recordBatchAudit(ctx, job, item, "batch_remove_user_from_room", targetDigestStrings(target)...)
		if auditErr != nil {
			return "failed", "audit.write.unavailable", uuid.Nil
		}
		if roomErr = service.governance.RemoveUserFromRoom(ctx, job.ActorAdminID, item.UserID, currentRoom, service.clock.Now()); roomErr != nil {
			state, key, _ := batchExecutionError(roomErr)
			return state, key, auditEventID
		}
		return "succeeded", "", auditEventID
	default:
		return "failed", "admin.user.batch.command_invalid", uuid.Nil
	}
}

func (service *Service) recordBatchAudit(ctx context.Context, job BatchJob, item BatchItem, action string, detail ...string) (uuid.UUID, error) {
	if service.audit == nil {
		return uuid.Nil, ErrAuditUnavailable
	}
	operationID, err := idempotency.ParseOperationID(job.OperationID)
	if err != nil {
		return uuid.Nil, ErrIntegrity
	}
	eventID, err := service.audit.RecordAnnotationWrite(ctx, AnnotationAuditEvent{
		ActorAdminID: job.ActorAdminID, UserID: item.UserID, OperationID: operationID,
		Action: action, Reason: job.Reason, DetailDigest: digestStrings(detail...),
		RequestID: batchAuditRequestID(job.ID, item.ID, item.AttemptCount), OccurredAt: service.clock.Now(),
	})
	if err != nil {
		return uuid.Nil, ErrAuditUnavailable
	}
	return eventID, nil
}

func batchExecutionError(err error) (string, string, uuid.UUID) {
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
		return "skipped", "admin.user.version_changed", uuid.Nil
	}
	return "failed", "admin.user.execution_failed", uuid.Nil
}

func marshalBatchSelectionSnapshot(command BatchCommand, sampledAt time.Time, targets []BatchExecutableTarget) ([]byte, error) {
	body, err := json.Marshal(batchSelectionSnapshot{
		SchemaVersion: BatchPreviewSelectionSchemaVersion, Command: command, SampledAtUnix: sampledAt.UTC().UnixNano(), Targets: targets,
	})
	if err != nil {
		return nil, err
	}
	return body, nil
}

func decodeBatchSelectionSnapshot(raw []byte) (batchSelectionSnapshot, error) {
	var snapshot batchSelectionSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return batchSelectionSnapshot{}, err
	}
	if snapshot.SchemaVersion != BatchPreviewSelectionSchemaVersion || !validBatchCommand(snapshot.Command) || snapshot.SampledAtUnix <= 0 {
		return batchSelectionSnapshot{}, ErrInvalidInput
	}
	return snapshot, nil
}

func decodeBatchSelectionTargets(raw []byte) ([]BatchExecutableTarget, error) {
	snapshot, err := decodeBatchSelectionSnapshot(raw)
	if err != nil {
		return nil, err
	}
	if len(snapshot.Targets) == 0 {
		return nil, ErrInvalidInput
	}
	return snapshot.Targets, nil
}

func batchPreviewDigest(adminID uuid.UUID, command BatchCommand, reason string, selectionSnapshot []byte) [sha256.Size]byte {
	hash := sha256.New()
	hash.Write([]byte(adminID.String()))
	hash.Write([]byte{0})
	hash.Write([]byte(command))
	hash.Write([]byte{0})
	hash.Write([]byte(strings.TrimSpace(reason)))
	hash.Write([]byte{0})
	hash.Write(selectionSnapshot)
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

func batchJobDigest(adminID uuid.UUID, operationID string, reason string, selectionDigest [sha256.Size]byte) [sha256.Size]byte {
	hash := sha256.New()
	hash.Write([]byte(adminID.String()))
	hash.Write([]byte{0})
	hash.Write([]byte(operationID))
	hash.Write([]byte{0})
	hash.Write([]byte(strings.TrimSpace(reason)))
	hash.Write([]byte{0})
	hash.Write(selectionDigest[:])
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

func batchItemDigest(operationID string, target BatchExecutableTarget) [sha256.Size]byte {
	hash := sha256.New()
	hash.Write([]byte(operationID))
	hash.Write([]byte{0})
	hash.Write([]byte(target.UserID.String()))
	hash.Write([]byte{0})
	hash.Write([]byte(fmt.Sprintf("%d", target.ExpectedUserVersion)))
	if target.Room != nil {
		hash.Write([]byte{0})
		hash.Write([]byte(target.Room.RoomID.String()))
		hash.Write([]byte{0})
		hash.Write([]byte(fmt.Sprintf("%d:%d", target.Room.ExpectedRoomVersion, target.Room.ExpectedMembershipVersion)))
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

func batchLeaseOwner(workerID string, batchJobID uuid.UUID) string {
	value := fmt.Sprintf("worker:%s", strings.TrimSpace(workerID))
	if batchJobID != uuid.Nil {
		value = fmt.Sprintf("%s:%s", value, batchJobID.String())
	}
	if len(value) <= 128 {
		return value
	}
	digest := sha256.Sum256([]byte(value))
	return "worker:" + base64.RawURLEncoding.EncodeToString(digest[:])
}

func batchAuditRequestID(batchJobID, itemID uuid.UUID, attempt uint32) string {
	return fmt.Sprintf("batch:%s:%s:%d", batchJobID.String(), itemID.String(), attempt)
}

func validBatchWorkerID(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 96
}

func sampleBlockers(blockers []GovernanceBlocker) []GovernanceBlocker {
	if len(blockers) <= 20 {
		return blockers
	}
	return append([]GovernanceBlocker(nil), blockers[:20]...)
}

func validBatchStateFilter(states []string) bool {
	seen := make(map[string]struct{}, len(states))
	for _, state := range states {
		switch state {
		case "queued", "running", "succeeded", "partially_succeeded", "failed", "canceling", "canceled":
		default:
			return false
		}
		if _, exists := seen[state]; exists {
			return false
		}
		seen[state] = struct{}{}
	}
	return true
}

func validBatchCommand(command BatchCommand) bool {
	return command == BatchCommandSuspend || command == BatchCommandUnsuspend || command == BatchCommandRemoveFromCurrentRoom
}

func validBatchCommandFilter(commands []BatchCommand) bool {
	seen := make(map[BatchCommand]struct{}, len(commands))
	for _, command := range commands {
		if !validBatchCommand(command) {
			return false
		}
		if _, exists := seen[command]; exists {
			return false
		}
		seen[command] = struct{}{}
	}
	return true
}

func validBatchItemStateFilter(states []string) bool {
	seen := make(map[string]struct{}, len(states))
	for _, state := range states {
		switch state {
		case "queued", "running", "succeeded", "failed", "skipped", "canceled":
		default:
			return false
		}
		if _, exists := seen[state]; exists {
			return false
		}
		seen[state] = struct{}{}
	}
	return true
}

func validBatchJobSort(field BatchJobSortField, direction SortDirection) bool {
	if field == "" {
		field = BatchJobSortCreatedAt
	}
	if direction == "" {
		direction = SortDescending
	}
	validField := field == BatchJobSortCreatedAt || field == BatchJobSortUpdatedAt || field == BatchJobSortID
	return validField && (direction == SortAscending || direction == SortDescending)
}

func targetDigestStrings(target BatchExecutableTarget) []string {
	values := []string{target.UserID.String(), fmt.Sprintf("%d", target.ExpectedUserVersion)}
	if target.Room != nil {
		values = append(values, target.Room.RoomID.String(), fmt.Sprintf("%d", target.Room.ExpectedRoomVersion), fmt.Sprintf("%d", target.Room.ExpectedMembershipVersion))
	}
	return values
}
