package adminuser

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/adminauth"
	adminv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1/adminv1connect"
	"github.com/iFTY-R/game-night/platform/admin"
	adminuser "github.com/iFTY-R/game-night/platform/admin/user"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/profile"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Handler adapts server-authenticated admin user-center requests to the domain service.
type Handler struct {
	adminv1connect.UnimplementedAdminUserServiceHandler

	service *adminuser.Service
	clock   clock.Clock
}

// NewService keeps the transport thin: request identity comes from adminauth, never from client JSON.
func NewService(service *adminuser.Service, source clock.Clock) (*Handler, error) {
	if service == nil || source == nil {
		return nil, adminuser.ErrInvalidInput
	}
	return &Handler{service: service, clock: source}, nil
}

func (handler *Handler) ListUsers(ctx context.Context, request *connect.Request[adminv1.ListUsersRequest]) (*connect.Response[adminv1.ListUsersResponse], error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return nil, err
	}
	input, err := listUsersInput(request.Msg)
	if err != nil {
		return nil, err
	}
	page, err := handler.service.ListUsers(ctx, actor, input)
	if err != nil {
		return nil, err
	}
	users := make([]*adminv1.AdminUserSummary, 0, len(page.Users))
	for _, row := range page.Users {
		users = append(users, userSummary(row))
	}
	return connect.NewResponse(&adminv1.ListUsersResponse{
		Users: users,
		Page:  &adminv1.AdminPageInfo{NextPageToken: page.NextPageToken, SampledAt: timestamppb.New(page.SampledAt)},
	}), nil
}

func (handler *Handler) GetUser(ctx context.Context, request *connect.Request[adminv1.GetUserRequest]) (*connect.Response[adminv1.GetUserResponse], error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return nil, err
	}
	userID, err := parseUUID(request.Msg.GetUserId())
	if err != nil {
		return nil, err
	}
	detail, err := handler.service.GetUser(ctx, actor, userID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.GetUserResponse{User: userDetail(detail), SampledAt: timestamppb.New(detail.SampledAt)}), nil
}

func (handler *Handler) GetUserPII(ctx context.Context, request *connect.Request[adminv1.GetUserPIIRequest]) (*connect.Response[adminv1.GetUserPIIResponse], error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return nil, err
	}
	userID, err := parseUUID(request.Msg.GetUserId())
	if err != nil {
		return nil, err
	}
	fields, err := piiFields(request.Msg.GetFields())
	if err != nil {
		return nil, err
	}
	result, err := handler.service.GetUserPII(ctx, actor, adminuser.GetUserPIIRequest{
		UserID: userID,
		Fields: fields,
		Reason: request.Msg.GetReason(),
	})
	if err != nil {
		return nil, err
	}
	values := make([]*adminv1.AdminUserPIIValue, 0, len(result.Values))
	for _, value := range result.Values {
		values = append(values, &adminv1.AdminUserPIIValue{
			Field: piiField(value.Field), Value: value.Value, Version: value.Version, UpdatedAt: timestampOrNil(value.UpdatedAt),
		})
	}
	return connect.NewResponse(&adminv1.GetUserPIIResponse{
		UserId: result.UserID.String(), Values: values, AccessAuditEventId: result.AccessAuditEventID.String(),
		AccessedAt: timestamppb.New(result.AccessedAt),
	}), nil
}

func (handler *Handler) ListUserTags(ctx context.Context, request *connect.Request[adminv1.ListUserTagsRequest]) (*connect.Response[adminv1.ListUserTagsResponse], error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return nil, err
	}
	pageSize, err := pageSize(request.Msg.GetPageSize(), adminuser.DefaultUserPageSize)
	if err != nil || request.Msg.GetPageToken() != "" {
		return nil, adminuser.ErrInvalidInput
	}
	page, err := handler.service.ListTags(ctx, actor, adminuser.TagPageQuery{NamePrefix: request.Msg.GetNamePrefix(), PageSize: pageSize})
	if err != nil {
		return nil, err
	}
	tags := make([]*adminv1.AdminUserTag, 0, len(page.Tags))
	for _, tag := range page.Tags {
		tags = append(tags, userTag(tag))
	}
	return connect.NewResponse(&adminv1.ListUserTagsResponse{
		Tags: tags, CatalogVersion: page.CatalogVersion,
		Page: &adminv1.AdminPageInfo{SampledAt: timestamppb.New(handler.clock.Now())},
	}), nil
}

func (handler *Handler) CreateUserTag(ctx context.Context, request *connect.Request[adminv1.CreateUserTagRequest]) (*connect.Response[adminv1.CreateUserTagResponse], error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return nil, err
	}
	operationID, err := parseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, err
	}
	tagID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	result, err := handler.service.CreateTag(ctx, actor, adminuser.CreateTagCommand{
		TagID: tagID, Name: request.Msg.GetName(), Color: request.Msg.GetColor(), Reason: request.Msg.GetReason(),
		ExpectedCatalogVersion: request.Msg.GetExpectedVersion(),
	}, operationID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.CreateUserTagResponse{
		Receipt: receipt(operationID, uuid.Nil, result.Tag.CreatedAt), Tag: userTag(result.Tag), CatalogVersion: result.CatalogVersion,
	}), nil
}

func (handler *Handler) UpdateUserTag(ctx context.Context, request *connect.Request[adminv1.UpdateUserTagRequest]) (*connect.Response[adminv1.UpdateUserTagResponse], error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return nil, err
	}
	operationID, err := parseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, err
	}
	tagID, err := parseUUID(request.Msg.GetTagId())
	if err != nil {
		return nil, err
	}
	tag, err := handler.service.UpdateTag(ctx, actor, adminuser.UpdateTagCommand{
		TagID: tagID, Name: request.Msg.GetName(), Color: request.Msg.GetColor(), Reason: request.Msg.GetReason(),
		ExpectedVersion: request.Msg.GetExpectedVersion(),
	}, operationID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.UpdateUserTagResponse{Receipt: receipt(operationID, uuid.Nil, tag.UpdatedAt), Tag: userTag(tag)}), nil
}

func (handler *Handler) DeleteUserTag(ctx context.Context, request *connect.Request[adminv1.DeleteUserTagRequest]) (*connect.Response[adminv1.DeleteUserTagResponse], error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return nil, err
	}
	operationID, err := parseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, err
	}
	tagID, err := parseUUID(request.Msg.GetTagId())
	if err != nil {
		return nil, err
	}
	catalogVersion, err := handler.service.DeleteTag(ctx, actor, adminuser.DeleteTagCommand{
		TagID: tagID, ExpectedVersion: request.Msg.GetExpectedVersion(),
	}, operationID, request.Msg.GetReason())
	if err != nil {
		return nil, err
	}
	now := handler.clock.Now()
	return connect.NewResponse(&adminv1.DeleteUserTagResponse{
		Receipt: receipt(operationID, uuid.Nil, now), TagId: tagID.String(), CatalogVersion: catalogVersion, RemovedAssignments: 0,
	}), nil
}

func (handler *Handler) SetUserTags(ctx context.Context, request *connect.Request[adminv1.SetUserTagsRequest]) (*connect.Response[adminv1.SetUserTagsResponse], error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return nil, err
	}
	operationID, err := parseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, err
	}
	userID, err := parseUUID(request.Msg.GetUserId())
	if err != nil {
		return nil, err
	}
	tagIDs, err := parseUUIDs(request.Msg.GetTagIds())
	if err != nil {
		return nil, err
	}
	if _, err = handler.service.SetUserTags(ctx, actor, adminuser.SetUserTagsRequest{
		OperationID: operationID, UserID: userID, TagIDs: tagIDs, Reason: request.Msg.GetReason(),
		ExpectedVersion: request.Msg.GetExpectedVersion(),
	}); err != nil {
		return nil, err
	}
	detail, err := handler.service.GetUser(ctx, actor, userID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.SetUserTagsResponse{
		Receipt: receipt(operationID, uuid.Nil, handler.clock.Now()), User: userSummary(detail.Summary),
	}), nil
}

func (handler *Handler) ListUserNotes(ctx context.Context, request *connect.Request[adminv1.ListUserNotesRequest]) (*connect.Response[adminv1.ListUserNotesResponse], error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return nil, err
	}
	userID, err := parseUUID(request.Msg.GetUserId())
	if err != nil {
		return nil, err
	}
	pageSize, err := pageSize(request.Msg.GetPageSize(), adminuser.DefaultDetailNoteLimit)
	if err != nil || request.Msg.GetPageToken() != "" {
		return nil, adminuser.ErrInvalidInput
	}
	notes, err := handler.service.ListUserNotes(ctx, actor, adminuser.NotePageQuery{UserID: userID, PageSize: pageSize})
	if err != nil {
		return nil, err
	}
	wireNotes := make([]*adminv1.AdminUserNote, 0, len(notes))
	for _, note := range notes {
		wireNotes = append(wireNotes, userNote(note))
	}
	return connect.NewResponse(&adminv1.ListUserNotesResponse{Notes: wireNotes, Page: &adminv1.AdminPageInfo{SampledAt: timestamppb.New(handler.clock.Now())}}), nil
}

func (handler *Handler) AppendUserNote(ctx context.Context, request *connect.Request[adminv1.AppendUserNoteRequest]) (*connect.Response[adminv1.AppendUserNoteResponse], error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return nil, err
	}
	operationID, err := parseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, err
	}
	userID, err := parseUUID(request.Msg.GetUserId())
	if err != nil {
		return nil, err
	}
	note, err := handler.service.AppendUserNote(ctx, actor, adminuser.AppendUserNoteRequest{
		OperationID: operationID, UserID: userID, Body: request.Msg.GetBody(), Reason: request.Msg.GetReason(),
		ExpectedVersion: request.Msg.GetExpectedVersion(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.AppendUserNoteResponse{
		Receipt: receipt(operationID, uuid.Nil, note.CreatedAt), Note: userNote(note), UserVersion: note.UserVersion,
	}), nil
}

func (handler *Handler) PreviewUserCommand(ctx context.Context, request *connect.Request[adminv1.PreviewUserCommandRequest]) (*connect.Response[adminv1.PreviewUserCommandResponse], error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return nil, err
	}
	userID, err := parseUUID(request.Msg.GetUserId())
	if err != nil {
		return nil, err
	}
	command, err := userCommandFromWire(request.Msg.GetCommand())
	if err != nil {
		return nil, err
	}
	preview, err := handler.service.PreviewUserCommand(ctx, actor, adminuser.PreviewUserCommandInput{
		UserID: userID, Command: command, Reason: request.Msg.GetReason(), ExpectedUserVersion: request.Msg.GetExpectedUserVersion(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(previewUserCommandResponse(preview)), nil
}

func (handler *Handler) ExecuteUserCommand(ctx context.Context, request *connect.Request[adminv1.ExecuteUserCommandRequest]) (*connect.Response[adminv1.ExecuteUserCommandResponse], error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return nil, err
	}
	operationID, err := parseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, err
	}
	userID, err := parseUUID(request.Msg.GetUserId())
	if err != nil {
		return nil, err
	}
	command, err := userCommandFromWire(request.Msg.GetCommand())
	if err != nil {
		return nil, err
	}
	previewID, err := parseUUID(request.Msg.GetPreviewId())
	if err != nil {
		return nil, err
	}
	previewDigest, err := parseDigest(request.Msg.GetPreviewDigest())
	if err != nil {
		return nil, err
	}
	result, err := handler.service.ExecuteUserCommand(ctx, actor, adminuser.ExecuteUserCommandInput{
		OperationID: operationID, UserID: userID, Command: command, PreviewID: previewID,
		PreviewDigest: previewDigest, Reason: request.Msg.GetReason(), ExpectedUserVersion: request.Msg.GetExpectedUserVersion(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(executeUserCommandResponse(result)), nil
}

func (handler *Handler) PreviewBatchUserOperation(ctx context.Context, request *connect.Request[adminv1.PreviewBatchUserOperationRequest]) (*connect.Response[adminv1.PreviewBatchUserOperationResponse], error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return nil, err
	}
	selection, err := batchSelectionFromWire(request.Msg.GetSelection())
	if err != nil {
		return nil, err
	}
	command, err := batchCommandFromWire(request.Msg.GetCommand())
	if err != nil {
		return nil, err
	}
	result, err := handler.service.PreviewBatchUserOperation(ctx, actor, adminuser.PreviewBatchCommand{
		Selection: selection, Command: command, Reason: request.Msg.GetReason(),
	})
	if err != nil {
		return nil, err
	}
	blockers := make([]*adminv1.AdminUserCommandBlocker, 0, len(result.SampledBlockers))
	for _, blocker := range result.SampledBlockers {
		blockers = append(blockers, batchBlockerToWire(blocker))
	}
	requiredElevation, _ := elevationScopeWire(result.RequiredElevation)
	return connect.NewResponse(&adminv1.PreviewBatchUserOperationResponse{
		PreviewId: result.Preview.ID.String(), PreviewDigest: digestString(result.Preview.PreviewDigest), PreviewVersion: result.Preview.Version,
		ExpiresAt: timestamppb.New(result.Preview.ExpiresAt), TargetCount: result.Preview.TargetCount, ExecutableCount: result.Preview.ExecutableCount,
		BlockedCount: result.Preview.BlockedCount, SampledBlockers: blockers, RequiredElevation: requiredElevation,
		SampledAt: timestamppb.New(result.SampledAt),
	}), nil
}

func (handler *Handler) StartBatchUserOperation(ctx context.Context, request *connect.Request[adminv1.StartBatchUserOperationRequest]) (*connect.Response[adminv1.StartBatchUserOperationResponse], error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return nil, err
	}
	operationID, err := parseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, err
	}
	previewID, err := parseUUID(request.Msg.GetPreviewId())
	if err != nil {
		return nil, err
	}
	previewDigest, err := parseDigest(request.Msg.GetPreviewDigest())
	if err != nil {
		return nil, err
	}
	job, err := handler.service.StartBatchUserOperation(ctx, actor, adminuser.StartBatchCommand{
		OperationID: operationID, PreviewID: previewID, PreviewDigest: previewDigest,
		ExpectedVersion: request.Msg.GetExpectedVersion(), Reason: request.Msg.GetReason(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.StartBatchUserOperationResponse{
		Receipt: receipt(operationID, uuid.Nil, handler.clock.Now()), BatchOperation: batchJobToWire(job),
	}), nil
}

func (handler *Handler) GetBatchUserOperation(ctx context.Context, request *connect.Request[adminv1.GetBatchUserOperationRequest]) (*connect.Response[adminv1.GetBatchUserOperationResponse], error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return nil, err
	}
	batchJobID, err := parseUUID(request.Msg.GetBatchOperationId())
	if err != nil {
		return nil, err
	}
	job, err := handler.service.GetBatchUserOperation(ctx, actor, batchJobID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.GetBatchUserOperationResponse{
		BatchOperation: batchJobToWire(job), SampledAt: timestamppb.New(handler.clock.Now()),
	}), nil
}

func (handler *Handler) ListBatchUserOperations(ctx context.Context, request *connect.Request[adminv1.ListBatchUserOperationsRequest]) (*connect.Response[adminv1.ListBatchUserOperationsResponse], error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return nil, err
	}
	states, err := batchJobStatesFromWire(request.Msg.GetStates())
	if err != nil {
		return nil, err
	}
	commands, err := batchCommandsFromWire(request.Msg.GetCommands())
	if err != nil {
		return nil, err
	}
	sortField, direction, err := batchJobSortFromWire(request.Msg.GetSortField(), request.Msg.GetSortDirection())
	if err != nil {
		return nil, err
	}
	pageSize, err := pageSize(request.Msg.GetPageSize(), adminuser.DefaultBatchPageSize)
	if err != nil {
		return nil, err
	}
	jobs, nextPageToken, sampledAt, err := handler.service.ListBatchUserOperations(ctx, actor, adminuser.BatchJobListQuery{
		States: states, Commands: commands, CreatedFrom: timestampTime(request.Msg.GetCreatedFrom()), CreatedTo: timestampTime(request.Msg.GetCreatedTo()),
		SortField: sortField, Direction: direction, PageSize: pageSize, PageToken: request.Msg.GetPageToken(),
	})
	if err != nil {
		return nil, err
	}
	wireJobs := make([]*adminv1.AdminBatchUserOperation, 0, len(jobs))
	for _, job := range jobs {
		wireJobs = append(wireJobs, batchJobToWire(job))
	}
	return connect.NewResponse(&adminv1.ListBatchUserOperationsResponse{
		BatchOperations: wireJobs, Page: &adminv1.AdminPageInfo{NextPageToken: nextPageToken, SampledAt: timestamppb.New(sampledAt)},
	}), nil
}

func (handler *Handler) ListBatchUserOperationItems(ctx context.Context, request *connect.Request[adminv1.ListBatchUserOperationItemsRequest]) (*connect.Response[adminv1.ListBatchUserOperationItemsResponse], error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return nil, err
	}
	batchJobID, err := parseUUID(request.Msg.GetBatchOperationId())
	if err != nil {
		return nil, err
	}
	states, err := batchItemStatesFromWire(request.Msg.GetStates())
	if err != nil {
		return nil, err
	}
	pageSize, err := pageSize(request.Msg.GetPageSize(), adminuser.DefaultBatchPageSize)
	if err != nil {
		return nil, err
	}
	items, nextPageToken, sampledAt, err := handler.service.ListBatchUserOperationItems(ctx, actor, adminuser.BatchItemListQuery{
		BatchJobID: batchJobID, States: states, PageSize: pageSize, PageToken: request.Msg.GetPageToken(),
	})
	if err != nil {
		return nil, err
	}
	wireItems := make([]*adminv1.AdminBatchUserOperationItem, 0, len(items))
	for _, item := range items {
		wireItems = append(wireItems, batchItemToWire(item))
	}
	return connect.NewResponse(&adminv1.ListBatchUserOperationItemsResponse{
		Items: wireItems, Page: &adminv1.AdminPageInfo{NextPageToken: nextPageToken, SampledAt: timestamppb.New(sampledAt)},
	}), nil
}

func (handler *Handler) CancelBatchUserOperation(ctx context.Context, request *connect.Request[adminv1.CancelBatchUserOperationRequest]) (*connect.Response[adminv1.CancelBatchUserOperationResponse], error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return nil, err
	}
	operationID, err := parseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, err
	}
	batchJobID, err := parseUUID(request.Msg.GetBatchOperationId())
	if err != nil {
		return nil, err
	}
	job, err := handler.service.CancelBatchUserOperation(ctx, actor, adminuser.CancelBatchCommand{
		OperationID: operationID, BatchJobID: batchJobID, ExpectedVersion: request.Msg.GetExpectedVersion(), Reason: request.Msg.GetReason(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.CancelBatchUserOperationResponse{
		Receipt: receipt(operationID, uuid.Nil, handler.clock.Now()), BatchOperation: batchJobToWire(job),
	}), nil
}

func (handler *Handler) RetryBatchUserOperation(ctx context.Context, request *connect.Request[adminv1.RetryBatchUserOperationRequest]) (*connect.Response[adminv1.RetryBatchUserOperationResponse], error) {
	actor, err := requestActor(ctx)
	if err != nil {
		return nil, err
	}
	operationID, err := parseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, err
	}
	batchJobID, err := parseUUID(request.Msg.GetBatchOperationId())
	if err != nil {
		return nil, err
	}
	itemIDs, err := parseUUIDs(request.Msg.GetItemIds())
	if err != nil {
		return nil, err
	}
	result, err := handler.service.RetryBatchUserOperation(ctx, actor, adminuser.RetryBatchCommand{
		OperationID: operationID, BatchJobID: batchJobID, ItemIDs: itemIDs, ExpectedVersion: request.Msg.GetExpectedVersion(), Reason: request.Msg.GetReason(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.RetryBatchUserOperationResponse{
		Receipt: receipt(operationID, uuid.Nil, handler.clock.Now()), BatchOperation: batchJobToWire(result.BatchJob), RequeuedItems: result.RequeuedItems,
	}), nil
}

// AuditRecorder durably appends user-center sensitive access and annotation events.
type AuditRecorder struct {
	service          *audit.Service
	unitOfWork       audit.UnitOfWork
	checkpointHealth *audit.CheckpointHealthPolicy
	clock            clock.Clock
}

func NewAuditRecorder(service *audit.Service, unitOfWork audit.UnitOfWork, checkpointHealth *audit.CheckpointHealthPolicy, source clock.Clock) *AuditRecorder {
	return &AuditRecorder{service: service, unitOfWork: unitOfWork, checkpointHealth: checkpointHealth, clock: source}
}

func (recorder *AuditRecorder) RecordPIIRead(ctx context.Context, event adminuser.PIIAuditEvent) (uuid.UUID, error) {
	return recorder.append(ctx, event.ActorAdminID, event.UserID, event.RequestID, audit.ActionRealNameRead, "admin_user_pii_read", event.ReasonDigest[:], event.OccurredAt)
}

func (recorder *AuditRecorder) RecordAnnotationWrite(ctx context.Context, event adminuser.AnnotationAuditEvent) (uuid.UUID, error) {
	action := audit.ActionRealNameUpdated
	targetID := event.UserID
	if targetID == uuid.Nil {
		targetID = event.ActorAdminID
		action = audit.ActionAuditEventsRead
	}
	return recorder.append(ctx, event.ActorAdminID, targetID, event.RequestID, action, event.Action, event.DetailDigest[:], event.OccurredAt)
}

func (recorder *AuditRecorder) append(ctx context.Context, adminID, targetID uuid.UUID, requestID string, action audit.Action, reasonCode string, detailDigest []byte, occurredAt time.Time) (uuid.UUID, error) {
	if recorder == nil || recorder.service == nil || recorder.unitOfWork == nil || recorder.checkpointHealth == nil || recorder.clock == nil ||
		adminID == uuid.Nil || targetID == uuid.Nil {
		return uuid.Nil, audit.ErrInvalidInput
	}
	if occurredAt.IsZero() {
		occurredAt = recorder.clock.Now()
	}
	var eventID uuid.UUID
	err := recorder.unitOfWork.Run(ctx, func(ctx context.Context, transaction audit.Transaction) error {
		head, err := transaction.Audit().ReadHead(ctx, audit.ChainAdmin)
		if err != nil {
			return err
		}
		progress, err := transaction.Checkpoints().ReadCheckpointProgress(ctx, audit.ChainAdmin)
		if err != nil {
			return err
		}
		health, err := recorder.checkpointHealth.Evaluate(ctx, head.Sequence(), progress, recorder.clock.Now())
		if err != nil {
			return err
		}
		if !health.AllowsSensitiveWrites() {
			return audit.ErrSensitiveWriteBlocked
		}
		actor, err := audit.NewActor(audit.ActorAdmin, adminID.String())
		if err != nil {
			return err
		}
		target, err := audit.NewTarget(audit.TargetUser, targetID.String())
		if err != nil {
			return err
		}
		if strings.TrimSpace(requestID) == "" {
			requestID = fmt.Sprintf("admin-user:%d:%s", action, adminID.String())
		}
		eventID, err = uuid.NewV7()
		if err != nil {
			return err
		}
		signed, err := recorder.service.Prepare(head, audit.EventInput{
			EventID: eventID, RequestID: requestID, OccurredAt: occurredAt,
			Actor: actor, Target: target, Action: action, ReasonCode: reasonCode, DetailDigest: detailDigest,
		})
		if err != nil {
			return err
		}
		next, err := transaction.Audit().AppendEvent(ctx, audit.AppendRequest{ExpectedHead: head, Event: signed})
		if err != nil {
			return err
		}
		// Checkpoint scheduling must re-read progress after the append. Reusing the pre-append
		// snapshot is inconsistent with the new head when the chain was fully checkpointed at
		// transaction start (positive lag with a zero UncheckpointedSince), which fails health
		// evaluation and would block every sensitive write on an otherwise healthy chain.
		progress, err = transaction.Checkpoints().ReadCheckpointProgress(ctx, audit.ChainAdmin)
		if err != nil {
			return err
		}
		health, err = recorder.checkpointHealth.Evaluate(ctx, next.Sequence(), progress, recorder.clock.Now())
		if err != nil || !health.CheckpointDue() {
			return err
		}
		checkpoint, err := recorder.service.PrepareCheckpoint(next, recorder.clock.Now())
		if err != nil {
			return err
		}
		return transaction.Checkpoints().AppendPendingCheckpoint(ctx, checkpoint)
	})
	if err != nil {
		return uuid.Nil, err
	}
	return eventID, nil
}

func requestActor(ctx context.Context) (admin.ActorContext, error) {
	actor, ok := adminauth.ActorFromContext(ctx)
	if !ok {
		return admin.ActorContext{}, admin.ErrAuthentication
	}
	return actor, nil
}

func userCommandFromWire(command *adminv1.AdminUserCommand) (adminuser.UserCommandInput, error) {
	if command == nil {
		return adminuser.UserCommandInput{}, adminuser.ErrInvalidInput
	}
	switch command.GetType() {
	case adminv1.AdminUserCommandType_ADMIN_USER_COMMAND_TYPE_SUSPEND:
		if command.GetRoomId() != "" || command.GetExpectedRoomVersion() != 0 || command.GetExpectedMembershipVersion() != 0 {
			return adminuser.UserCommandInput{}, adminuser.ErrInvalidInput
		}
		return adminuser.UserCommandInput{Type: adminuser.UserCommandSuspend}, nil
	case adminv1.AdminUserCommandType_ADMIN_USER_COMMAND_TYPE_UNSUSPEND:
		if command.GetRoomId() != "" || command.GetExpectedRoomVersion() != 0 || command.GetExpectedMembershipVersion() != 0 {
			return adminuser.UserCommandInput{}, adminuser.ErrInvalidInput
		}
		return adminuser.UserCommandInput{Type: adminuser.UserCommandUnsuspend}, nil
	case adminv1.AdminUserCommandType_ADMIN_USER_COMMAND_TYPE_REVOKE_ALL_DEVICES:
		if command.GetRoomId() != "" || command.GetExpectedRoomVersion() != 0 || command.GetExpectedMembershipVersion() != 0 {
			return adminuser.UserCommandInput{}, adminuser.ErrInvalidInput
		}
		return adminuser.UserCommandInput{Type: adminuser.UserCommandRevokeAllDevices}, nil
	case adminv1.AdminUserCommandType_ADMIN_USER_COMMAND_TYPE_REMOVE_FROM_CURRENT_ROOM:
		roomID, err := parseUUID(command.GetRoomId())
		if err != nil || command.GetExpectedRoomVersion() == 0 || command.GetExpectedMembershipVersion() == 0 {
			return adminuser.UserCommandInput{}, adminuser.ErrInvalidInput
		}
		return adminuser.UserCommandInput{
			Type: adminuser.UserCommandRemoveFromCurrentRoom, RoomID: roomID,
			ExpectedRoomVersion: command.GetExpectedRoomVersion(), ExpectedMembershipVersion: command.GetExpectedMembershipVersion(),
		}, nil
	case adminv1.AdminUserCommandType_ADMIN_USER_COMMAND_TYPE_DELETE:
		if command.GetRoomId() != "" || command.GetExpectedRoomVersion() != 0 || command.GetExpectedMembershipVersion() != 0 {
			return adminuser.UserCommandInput{}, adminuser.ErrInvalidInput
		}
		return adminuser.UserCommandInput{Type: adminuser.UserCommandDelete}, nil
	default:
		return adminuser.UserCommandInput{}, adminuser.ErrInvalidInput
	}
}

func listUsersInput(request *adminv1.ListUsersRequest) (adminuser.ListUsersInput, error) {
	if request == nil {
		return adminuser.ListUsersInput{}, adminuser.ErrInvalidInput
	}
	filter := request.GetFilter()
	userID, err := optionalUUID(filter.GetUserId())
	if err != nil {
		return adminuser.ListUsersInput{}, err
	}
	tagIDs, err := parseUUIDs(filter.GetTagIds())
	if err != nil {
		return adminuser.ListUsersInput{}, err
	}
	statuses, err := statuses(filter.GetStatuses())
	if err != nil {
		return adminuser.ListUsersInput{}, err
	}
	if filter.GetPresence() == adminv1.AdminUserPresenceFilter_ADMIN_USER_PRESENCE_FILTER_ONLINE {
		return adminuser.ListUsersInput{}, adminuser.ErrInvalidInput
	}
	pageSize, err := pageSize(request.GetPageSize(), adminuser.DefaultUserPageSize)
	if err != nil {
		return adminuser.ListUsersInput{}, err
	}
	sortField, direction, err := sort(request.GetSort())
	if err != nil {
		return adminuser.ListUsersInput{}, err
	}
	return adminuser.ListUsersInput{
		UserID: userID, Statuses: statuses, UsernamePrefix: filter.GetUsername(), TagIDs: tagIDs,
		CreatedFrom: timestampTime(filter.GetCreatedFrom()), CreatedTo: timestampTime(filter.GetCreatedTo()),
		LastActivityFrom: timestampTime(filter.GetLastActivityFrom()), LastActivityTo: timestampTime(filter.GetLastActivityTo()),
		PageSize: pageSize, PageToken: request.GetPageToken(), SortField: sortField, Direction: direction,
	}, nil
}

func sort(value *adminv1.AdminUserSort) (adminuser.UserSortField, adminuser.SortDirection, error) {
	var field adminuser.UserSortField
	switch value.GetField() {
	case adminv1.AdminUserSortField_ADMIN_USER_SORT_FIELD_UNSPECIFIED, adminv1.AdminUserSortField_ADMIN_USER_SORT_FIELD_CREATED_AT:
		field = adminuser.UserSortCreatedAt
	case adminv1.AdminUserSortField_ADMIN_USER_SORT_FIELD_LAST_ACTIVITY_AT:
		field = adminuser.UserSortLastActivityAt
	case adminv1.AdminUserSortField_ADMIN_USER_SORT_FIELD_USERNAME:
		field = adminuser.UserSortUsername
	case adminv1.AdminUserSortField_ADMIN_USER_SORT_FIELD_USER_ID:
		field = adminuser.UserSortUserID
	default:
		return "", "", adminuser.ErrInvalidInput
	}
	var direction adminuser.SortDirection
	switch value.GetDirection() {
	case adminv1.AdminSortDirection_ADMIN_SORT_DIRECTION_UNSPECIFIED, adminv1.AdminSortDirection_ADMIN_SORT_DIRECTION_DESCENDING:
		direction = adminuser.SortDescending
	case adminv1.AdminSortDirection_ADMIN_SORT_DIRECTION_ASCENDING:
		direction = adminuser.SortAscending
	default:
		return "", "", adminuser.ErrInvalidInput
	}
	return field, direction, nil
}

func statuses(values []adminv1.AdminUserStatus) ([]string, error) {
	result := make([]string, 0, len(values))
	for _, value := range values {
		switch value {
		case adminv1.AdminUserStatus_ADMIN_USER_STATUS_ONBOARDING:
			result = append(result, "onboarding")
		case adminv1.AdminUserStatus_ADMIN_USER_STATUS_ACTIVE:
			result = append(result, "active")
		case adminv1.AdminUserStatus_ADMIN_USER_STATUS_SUSPENDED:
			result = append(result, "suspended")
		case adminv1.AdminUserStatus_ADMIN_USER_STATUS_DELETED:
			result = append(result, "deleted")
		default:
			return nil, adminuser.ErrInvalidInput
		}
	}
	return result, nil
}

func userStatus(value string) adminv1.AdminUserStatus {
	switch value {
	case "onboarding":
		return adminv1.AdminUserStatus_ADMIN_USER_STATUS_ONBOARDING
	case "active":
		return adminv1.AdminUserStatus_ADMIN_USER_STATUS_ACTIVE
	case "suspended":
		return adminv1.AdminUserStatus_ADMIN_USER_STATUS_SUSPENDED
	case "deleted":
		return adminv1.AdminUserStatus_ADMIN_USER_STATUS_DELETED
	default:
		return adminv1.AdminUserStatus_ADMIN_USER_STATUS_UNSPECIFIED
	}
}

func piiFields(values []adminv1.AdminUserPIIField) ([]profile.Field, error) {
	result := make([]profile.Field, 0, len(values))
	for _, value := range values {
		switch value {
		case adminv1.AdminUserPIIField_ADMIN_USER_PII_FIELD_REAL_NAME:
			result = append(result, profile.FieldRealName)
		default:
			return nil, adminuser.ErrInvalidInput
		}
	}
	return result, nil
}

func piiField(value profile.Field) adminv1.AdminUserPIIField {
	if value == profile.FieldRealName {
		return adminv1.AdminUserPIIField_ADMIN_USER_PII_FIELD_REAL_NAME
	}
	return adminv1.AdminUserPIIField_ADMIN_USER_PII_FIELD_UNSPECIFIED
}

func userDetail(detail adminuser.UserDetail) *adminv1.AdminUserDetail {
	availability := make([]*adminv1.AdminUserPIIAvailability, 0, len(detail.PIIAvailability))
	for _, item := range detail.PIIAvailability {
		availability = append(availability, &adminv1.AdminUserPIIAvailability{
			Field: piiField(item.Field), Available: item.Available, Version: item.Version, UpdatedAt: timestampOrNil(item.UpdatedAt),
		})
	}
	notes := make([]*adminv1.AdminUserNote, 0, len(detail.RecentNotes))
	for _, note := range detail.RecentNotes {
		notes = append(notes, userNote(note))
	}
	return &adminv1.AdminUserDetail{Summary: userSummary(detail.Summary), PiiAvailability: availability, RecentNotes: notes}
}

func userSummary(row adminuser.UserRecord) *adminv1.AdminUserSummary {
	tags := make([]*adminv1.AdminUserTag, 0, len(row.Tags))
	for _, tag := range row.Tags {
		tags = append(tags, userTag(tag))
	}
	return &adminv1.AdminUserSummary{
		UserId: row.ID.String(), Username: row.Username, Status: userStatus(row.Status), Tags: tags, Version: row.Version,
		CreatedAt: timestamppb.New(row.CreatedAt), UpdatedAt: timestamppb.New(row.UpdatedAt), LastActivityAt: timestamppb.New(row.LastActivityAt),
	}
}

func userTag(tag adminuser.Tag) *adminv1.AdminUserTag {
	return &adminv1.AdminUserTag{
		TagId: tag.ID.String(), Name: tag.Name, Color: tag.Color, Version: tag.Version,
		CreatedAt: timestamppb.New(tag.CreatedAt), UpdatedAt: timestamppb.New(tag.UpdatedAt),
	}
}

func userNote(note adminuser.Note) *adminv1.AdminUserNote {
	return &adminv1.AdminUserNote{
		NoteId: note.ID.String(), UserId: note.UserID.String(), AuthorAdminId: note.AuthorAdminID.String(),
		Body: note.Body, Reason: note.Reason, Version: note.Version, CreatedAt: timestamppb.New(note.CreatedAt),
	}
}

func previewUserCommandResponse(preview adminuser.UserCommandPreview) *adminv1.PreviewUserCommandResponse {
	blockers := make([]*adminv1.AdminUserCommandBlocker, 0, len(preview.Blockers))
	for _, blocker := range preview.Blockers {
		blockers = append(blockers, batchBlockerToWire(blocker))
	}
	requiredElevation, _ := elevationScopeWire(preview.RequiredElevation)
	return &adminv1.PreviewUserCommandResponse{
		PreviewId: preview.ID.String(), PreviewDigest: digestString(preview.PreviewDigest), PreviewVersion: preview.Version,
		ExpiresAt: timestamppb.New(preview.ExpiresAt), UserId: preview.Snapshot.UserID.String(),
		ExpectedUserVersion: preview.Snapshot.ExpectedUserVersion, AffectedDevices: preview.AffectedDevices,
		AffectedRooms: preview.AffectedRooms, Blockers: blockers, RequiredElevation: requiredElevation,
		SampledAt: timestamppb.New(preview.SampledAt),
	}
}

func executeUserCommandResponse(result adminuser.UserCommandExecutionResult) *adminv1.ExecuteUserCommandResponse {
	erasureJobID := ""
	if result.ErasureJobID != uuid.Nil {
		erasureJobID = result.ErasureJobID.String()
	}
	return &adminv1.ExecuteUserCommandResponse{
		Receipt: receipt(result.Receipt.OperationID, result.Receipt.AuditEventID, result.Receipt.CompletedAt),
		Outcome: userCommandOutcomeToWire(result.Receipt.Outcome), User: userSummary(result.User),
		RevokedDevices: result.RevokedDevices, RemovedRooms: result.RemovedRooms, ErasureJobId: erasureJobID,
	}
}

func receipt(operationID idempotency.OperationID, auditEventID uuid.UUID, completedAt time.Time) *adminv1.AdminOperationReceipt {
	auditID := ""
	if auditEventID != uuid.Nil {
		auditID = auditEventID.String()
	}
	return &adminv1.AdminOperationReceipt{OperationId: operationID.Value(), AuditEventId: auditID, CompletedAt: timestamppb.New(completedAt)}
}

func parseOperationID(value string) (idempotency.OperationID, error) {
	operationID, err := idempotency.ParseOperationID(strings.TrimSpace(value))
	if err != nil {
		return idempotency.OperationID{}, adminuser.ErrInvalidInput
	}
	return operationID, nil
}

func optionalUUID(value string) (uuid.UUID, error) {
	if strings.TrimSpace(value) == "" {
		return uuid.Nil, nil
	}
	return parseUUID(value)
}

func parseUUID(value string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil || parsed == uuid.Nil || parsed.String() != strings.TrimSpace(value) {
		return uuid.Nil, adminuser.ErrInvalidInput
	}
	return parsed, nil
}

func parseUUIDs(values []string) ([]uuid.UUID, error) {
	result := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		parsed, err := parseUUID(value)
		if err != nil {
			return nil, err
		}
		result = append(result, parsed)
	}
	return result, nil
}

func pageSize(value int32, fallback uint32) (uint32, error) {
	if value < 0 || value > int32(adminuser.MaximumUserPageSize) {
		return 0, adminuser.ErrInvalidInput
	}
	if value == 0 {
		return fallback, nil
	}
	return uint32(value), nil
}

func timestampTime(value *timestamppb.Timestamp) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.AsTime()
}

func timestampOrNil(value time.Time) *timestamppb.Timestamp {
	if value.IsZero() {
		return nil
	}
	return timestamppb.New(value)
}

func digest(values ...string) [sha256.Size]byte {
	hash := sha256.New()
	for _, value := range values {
		hash.Write([]byte(strings.TrimSpace(value)))
		hash.Write([]byte{0})
	}
	var result [sha256.Size]byte
	copy(result[:], hash.Sum(nil))
	return result
}

func digestString(value [sha256.Size]byte) string {
	return base64.RawURLEncoding.EncodeToString(value[:])
}

func parseDigest(value string) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	raw, err := base64.RawURLEncoding.Strict().DecodeString(strings.TrimSpace(value))
	if err != nil || len(raw) != len(digest) || base64.RawURLEncoding.EncodeToString(raw) != strings.TrimSpace(value) {
		return digest, adminuser.ErrInvalidInput
	}
	copy(digest[:], raw)
	return digest, nil
}

func batchSelectionFromWire(selection *adminv1.AdminUserSelection) (adminuser.BatchSelection, error) {
	if selection == nil {
		return adminuser.BatchSelection{}, adminuser.ErrInvalidInput
	}
	switch value := selection.Selection.(type) {
	case *adminv1.AdminUserSelection_Filter:
		filter := value.Filter
		if filter == nil || filter.GetPresence() == adminv1.AdminUserPresenceFilter_ADMIN_USER_PRESENCE_FILTER_ONLINE ||
			filter.GetPresence() == adminv1.AdminUserPresenceFilter_ADMIN_USER_PRESENCE_FILTER_OFFLINE {
			return adminuser.BatchSelection{}, adminuser.ErrInvalidInput
		}
		userID, err := optionalUUID(filter.GetUserId())
		if err != nil {
			return adminuser.BatchSelection{}, err
		}
		tagIDs, err := parseUUIDs(filter.GetTagIds())
		if err != nil {
			return adminuser.BatchSelection{}, err
		}
		statusValues, err := statuses(filter.GetStatuses())
		if err != nil {
			return adminuser.BatchSelection{}, err
		}
		return adminuser.BatchSelection{Filter: &adminuser.ListUsersInput{
			UserID: userID, Statuses: statusValues, UsernamePrefix: filter.GetUsername(), TagIDs: tagIDs,
			CreatedFrom: timestampTime(filter.GetCreatedFrom()), CreatedTo: timestampTime(filter.GetCreatedTo()),
			LastActivityFrom: timestampTime(filter.GetLastActivityFrom()), LastActivityTo: timestampTime(filter.GetLastActivityTo()),
		}}, nil
	case *adminv1.AdminUserSelection_ExplicitTargets:
		values := value.ExplicitTargets
		if values == nil || len(values.GetUsers()) == 0 {
			return adminuser.BatchSelection{}, adminuser.ErrInvalidInput
		}
		targets := make([]adminuser.VersionedUserTarget, 0, len(values.GetUsers()))
		for _, target := range values.GetUsers() {
			userID, err := parseUUID(target.GetUserId())
			if err != nil || target.GetExpectedUserVersion() == 0 {
				return adminuser.BatchSelection{}, adminuser.ErrInvalidInput
			}
			targets = append(targets, adminuser.VersionedUserTarget{UserID: userID, ExpectedUserVersion: target.GetExpectedUserVersion()})
		}
		return adminuser.BatchSelection{ExplicitTargets: targets}, nil
	default:
		return adminuser.BatchSelection{}, adminuser.ErrInvalidInput
	}
}

func batchCommandFromWire(command adminv1.AdminBatchUserCommandType) (adminuser.BatchCommand, error) {
	switch command {
	case adminv1.AdminBatchUserCommandType_ADMIN_BATCH_USER_COMMAND_TYPE_SUSPEND:
		return adminuser.BatchCommandSuspend, nil
	case adminv1.AdminBatchUserCommandType_ADMIN_BATCH_USER_COMMAND_TYPE_UNSUSPEND:
		return adminuser.BatchCommandUnsuspend, nil
	case adminv1.AdminBatchUserCommandType_ADMIN_BATCH_USER_COMMAND_TYPE_REMOVE_FROM_CURRENT_ROOM:
		return adminuser.BatchCommandRemoveFromCurrentRoom, nil
	default:
		return "", adminuser.ErrInvalidInput
	}
}

func batchCommandsFromWire(values []adminv1.AdminBatchUserCommandType) ([]adminuser.BatchCommand, error) {
	result := make([]adminuser.BatchCommand, 0, len(values))
	for _, value := range values {
		command, err := batchCommandFromWire(value)
		if err != nil {
			return nil, err
		}
		result = append(result, command)
	}
	return result, nil
}

func batchJobStatesFromWire(values []adminv1.AdminJobState) ([]string, error) {
	result := make([]string, 0, len(values))
	for _, value := range values {
		state, ok := batchJobStateString(value)
		if !ok {
			return nil, adminuser.ErrInvalidInput
		}
		result = append(result, state)
	}
	return result, nil
}

func batchItemStatesFromWire(values []adminv1.AdminBatchUserItemState) ([]string, error) {
	result := make([]string, 0, len(values))
	for _, value := range values {
		state, ok := batchItemStateString(value)
		if !ok {
			return nil, adminuser.ErrInvalidInput
		}
		result = append(result, state)
	}
	return result, nil
}

func batchJobSortFromWire(field adminv1.AdminBatchUserOperationSortField, direction adminv1.AdminSortDirection) (adminuser.BatchJobSortField, adminuser.SortDirection, error) {
	var sortField adminuser.BatchJobSortField
	switch field {
	case adminv1.AdminBatchUserOperationSortField_ADMIN_BATCH_USER_OPERATION_SORT_FIELD_UNSPECIFIED, adminv1.AdminBatchUserOperationSortField_ADMIN_BATCH_USER_OPERATION_SORT_FIELD_CREATED_AT:
		sortField = adminuser.BatchJobSortCreatedAt
	case adminv1.AdminBatchUserOperationSortField_ADMIN_BATCH_USER_OPERATION_SORT_FIELD_UPDATED_AT:
		sortField = adminuser.BatchJobSortUpdatedAt
	case adminv1.AdminBatchUserOperationSortField_ADMIN_BATCH_USER_OPERATION_SORT_FIELD_BATCH_OPERATION_ID:
		sortField = adminuser.BatchJobSortID
	default:
		return "", "", adminuser.ErrInvalidInput
	}
	var sortDirection adminuser.SortDirection
	switch direction {
	case adminv1.AdminSortDirection_ADMIN_SORT_DIRECTION_UNSPECIFIED, adminv1.AdminSortDirection_ADMIN_SORT_DIRECTION_DESCENDING:
		sortDirection = adminuser.SortDescending
	case adminv1.AdminSortDirection_ADMIN_SORT_DIRECTION_ASCENDING:
		sortDirection = adminuser.SortAscending
	default:
		return "", "", adminuser.ErrInvalidInput
	}
	return sortField, sortDirection, nil
}

func batchBlockerToWire(blocker adminuser.GovernanceBlocker) *adminv1.AdminUserCommandBlocker {
	return &adminv1.AdminUserCommandBlocker{
		Type: batchBlockerTypeToWire(blocker.Type), ResourceId: blocker.ResourceID, MessageKey: blocker.MessageKey,
	}
}

func batchBlockerTypeToWire(value adminuser.GovernanceBlockerType) adminv1.AdminUserCommandBlockerType {
	switch value {
	case adminuser.GovernanceBlockerActiveGame:
		return adminv1.AdminUserCommandBlockerType_ADMIN_USER_COMMAND_BLOCKER_TYPE_ACTIVE_GAME
	case adminuser.GovernanceBlockerPendingExport:
		return adminv1.AdminUserCommandBlockerType_ADMIN_USER_COMMAND_BLOCKER_TYPE_PENDING_EXPORT
	case adminuser.GovernanceBlockerVersionChanged:
		return adminv1.AdminUserCommandBlockerType_ADMIN_USER_COMMAND_BLOCKER_TYPE_VERSION_CHANGED
	case adminuser.GovernanceBlockerAlreadyDeleted:
		return adminv1.AdminUserCommandBlockerType_ADMIN_USER_COMMAND_BLOCKER_TYPE_ALREADY_DELETED
	default:
		return adminv1.AdminUserCommandBlockerType_ADMIN_USER_COMMAND_BLOCKER_TYPE_UNSPECIFIED
	}
}

func batchJobToWire(job adminuser.BatchJob) *adminv1.AdminBatchUserOperation {
	return &adminv1.AdminBatchUserOperation{
		BatchOperationId: job.ID.String(), Command: batchCommandToWire(adminuser.BatchCommand(job.Command)), State: batchJobStateToWire(job.State),
		TargetCount: job.TargetCount, QueuedCount: job.QueuedCount, RunningCount: job.RunningCount, SucceededCount: job.SucceededCount,
		FailedCount: job.FailedCount, SkippedCount: job.SkippedCount, CanceledCount: job.CanceledCount,
		RequestedByAdminId: job.ActorAdminID.String(), Reason: job.Reason, Version: job.Version,
		CreatedAt: timestamppb.New(job.CreatedAt), StartedAt: timestampOrNil(job.StartedAt), CompletedAt: timestampOrNil(job.CompletedAt),
		UpdatedAt: timestamppb.New(job.UpdatedAt), ErrorMessageKey: job.ErrorMessageKey,
	}
}

func batchItemToWire(item adminuser.BatchItem) *adminv1.AdminBatchUserOperationItem {
	auditEventID := ""
	if item.AuditEventID != uuid.Nil {
		auditEventID = item.AuditEventID.String()
	}
	return &adminv1.AdminBatchUserOperationItem{
		ItemId: item.ID.String(), BatchOperationId: item.BatchJobID.String(), UserId: item.UserID.String(), ExpectedUserVersion: item.ExpectedUserVersion,
		State: batchItemStateToWire(item.State), AttemptCount: int32(item.AttemptCount), ErrorMessageKey: item.ErrorMessageKey, AuditEventId: auditEventID,
		StartedAt: timestampOrNil(item.StartedAt), CompletedAt: timestampOrNil(item.CompletedAt), Version: item.Version,
	}
}

func batchCommandToWire(command adminuser.BatchCommand) adminv1.AdminBatchUserCommandType {
	switch command {
	case adminuser.BatchCommandSuspend:
		return adminv1.AdminBatchUserCommandType_ADMIN_BATCH_USER_COMMAND_TYPE_SUSPEND
	case adminuser.BatchCommandUnsuspend:
		return adminv1.AdminBatchUserCommandType_ADMIN_BATCH_USER_COMMAND_TYPE_UNSUSPEND
	case adminuser.BatchCommandRemoveFromCurrentRoom:
		return adminv1.AdminBatchUserCommandType_ADMIN_BATCH_USER_COMMAND_TYPE_REMOVE_FROM_CURRENT_ROOM
	default:
		return adminv1.AdminBatchUserCommandType_ADMIN_BATCH_USER_COMMAND_TYPE_UNSPECIFIED
	}
}

func userCommandOutcomeToWire(outcome adminuser.UserCommandOutcome) adminv1.AdminUserCommandOutcome {
	switch outcome {
	case adminuser.UserCommandOutcomeExecuted:
		return adminv1.AdminUserCommandOutcome_ADMIN_USER_COMMAND_OUTCOME_EXECUTED
	case adminuser.UserCommandOutcomeNoChange:
		return adminv1.AdminUserCommandOutcome_ADMIN_USER_COMMAND_OUTCOME_NO_CHANGE
	case adminuser.UserCommandOutcomeRejected:
		return adminv1.AdminUserCommandOutcome_ADMIN_USER_COMMAND_OUTCOME_REJECTED
	default:
		return adminv1.AdminUserCommandOutcome_ADMIN_USER_COMMAND_OUTCOME_UNSPECIFIED
	}
}

func batchJobStateToWire(state string) adminv1.AdminJobState {
	switch state {
	case "queued":
		return adminv1.AdminJobState_ADMIN_JOB_STATE_QUEUED
	case "running":
		return adminv1.AdminJobState_ADMIN_JOB_STATE_RUNNING
	case "succeeded":
		return adminv1.AdminJobState_ADMIN_JOB_STATE_SUCCEEDED
	case "partially_succeeded":
		return adminv1.AdminJobState_ADMIN_JOB_STATE_PARTIALLY_SUCCEEDED
	case "failed":
		return adminv1.AdminJobState_ADMIN_JOB_STATE_FAILED
	case "canceling":
		return adminv1.AdminJobState_ADMIN_JOB_STATE_CANCELING
	case "canceled":
		return adminv1.AdminJobState_ADMIN_JOB_STATE_CANCELED
	default:
		return adminv1.AdminJobState_ADMIN_JOB_STATE_UNSPECIFIED
	}
}

func batchJobStateString(value adminv1.AdminJobState) (string, bool) {
	switch value {
	case adminv1.AdminJobState_ADMIN_JOB_STATE_QUEUED:
		return "queued", true
	case adminv1.AdminJobState_ADMIN_JOB_STATE_RUNNING:
		return "running", true
	case adminv1.AdminJobState_ADMIN_JOB_STATE_SUCCEEDED:
		return "succeeded", true
	case adminv1.AdminJobState_ADMIN_JOB_STATE_PARTIALLY_SUCCEEDED:
		return "partially_succeeded", true
	case adminv1.AdminJobState_ADMIN_JOB_STATE_FAILED:
		return "failed", true
	case adminv1.AdminJobState_ADMIN_JOB_STATE_CANCELING:
		return "canceling", true
	case adminv1.AdminJobState_ADMIN_JOB_STATE_CANCELED:
		return "canceled", true
	default:
		return "", false
	}
}

func batchItemStateToWire(state string) adminv1.AdminBatchUserItemState {
	switch state {
	case "queued":
		return adminv1.AdminBatchUserItemState_ADMIN_BATCH_USER_ITEM_STATE_QUEUED
	case "running":
		return adminv1.AdminBatchUserItemState_ADMIN_BATCH_USER_ITEM_STATE_RUNNING
	case "succeeded":
		return adminv1.AdminBatchUserItemState_ADMIN_BATCH_USER_ITEM_STATE_SUCCEEDED
	case "failed":
		return adminv1.AdminBatchUserItemState_ADMIN_BATCH_USER_ITEM_STATE_FAILED
	case "skipped":
		return adminv1.AdminBatchUserItemState_ADMIN_BATCH_USER_ITEM_STATE_SKIPPED
	case "canceled":
		return adminv1.AdminBatchUserItemState_ADMIN_BATCH_USER_ITEM_STATE_CANCELED
	default:
		return adminv1.AdminBatchUserItemState_ADMIN_BATCH_USER_ITEM_STATE_UNSPECIFIED
	}
}

func batchItemStateString(value adminv1.AdminBatchUserItemState) (string, bool) {
	switch value {
	case adminv1.AdminBatchUserItemState_ADMIN_BATCH_USER_ITEM_STATE_QUEUED:
		return "queued", true
	case adminv1.AdminBatchUserItemState_ADMIN_BATCH_USER_ITEM_STATE_RUNNING:
		return "running", true
	case adminv1.AdminBatchUserItemState_ADMIN_BATCH_USER_ITEM_STATE_SUCCEEDED:
		return "succeeded", true
	case adminv1.AdminBatchUserItemState_ADMIN_BATCH_USER_ITEM_STATE_FAILED:
		return "failed", true
	case adminv1.AdminBatchUserItemState_ADMIN_BATCH_USER_ITEM_STATE_SKIPPED:
		return "skipped", true
	case adminv1.AdminBatchUserItemState_ADMIN_BATCH_USER_ITEM_STATE_CANCELED:
		return "canceled", true
	default:
		return "", false
	}
}

func elevationScopeWire(scope admin.ElevationScope) (adminv1.AdminElevationScope, bool) {
	switch scope {
	case admin.ElevationScopeUsersBulkGovernance:
		return adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_USERS_BULK_GOVERNANCE, true
	case admin.ElevationScopeUsersRevokeDevices:
		return adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_USERS_REVOKE_DEVICES, true
	case admin.ElevationScopeUsersDelete:
		return adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_USERS_DELETE, true
	case admin.ElevationScopeRoomsForceClose:
		return adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_ROOMS_FORCE_CLOSE, true
	case admin.ElevationScopeGamesForceTerminate:
		return adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_GAMES_FORCE_TERMINATE, true
	case admin.ElevationScopeGamesEmergencyRepair:
		return adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_GAMES_EMERGENCY_REPAIR, true
	case admin.ElevationScopeOperationsMaintenance:
		return adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_OPERATIONS_MAINTENANCE, true
	case admin.ElevationScopeSecurityDisableMFA:
		return adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_SECURITY_DISABLE_MFA, true
	case admin.ElevationScopeSecurityRegenerateRecoveryCodes:
		return adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_SECURITY_REGENERATE_RECOVERY_CODES, true
	case admin.ElevationScopeSecurityRevokeSessions:
		return adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_SECURITY_REVOKE_SESSIONS, true
	case admin.ElevationScopeAuditExportSensitive:
		return adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_AUDIT_EXPORT_SENSITIVE, true
	default:
		return adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_UNSPECIFIED, false
	}
}

var _ adminv1connect.AdminUserServiceHandler = (*Handler)(nil)
var _ adminuser.AuditRecorder = (*AuditRecorder)(nil)

func init() {
	_ = errors.Is
	_ = digest
}
