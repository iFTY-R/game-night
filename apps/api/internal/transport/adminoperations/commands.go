package adminoperations

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/adminauth"
	adminv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1"
	"github.com/iFTY-R/game-night/platform/admin"
	domain "github.com/iFTY-R/game-night/platform/admin/operations"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// PreviewMaintenanceChange persists the exact authority version and impact reviewed by the authenticated administrator.
func (handler *Handler) PreviewMaintenanceChange(ctx context.Context, request *connect.Request[adminv1.PreviewMaintenanceChangeRequest]) (*connect.Response[adminv1.PreviewMaintenanceChangeResponse], error) {
	actor, err := commandActor(ctx)
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil {
		return nil, domain.ErrInvalidInput
	}
	plannedEndAt, err := optionalTime(request.Msg.GetPlannedEndAt())
	if err != nil {
		return nil, err
	}
	scope, err := maintenanceScopeFromWire(request.Msg.GetScope())
	if err != nil {
		return nil, err
	}
	preview, err := handler.commands.PreviewMaintenanceChange(ctx, actor, domain.PreviewMaintenanceChangeInput{
		Enabled: request.Msg.GetEnabled(), Scope: scope, Reason: request.Msg.GetReason(), PlannedEndAt: plannedEndAt,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.PreviewMaintenanceChangeResponse{
		Current: previewMaintenanceStateToWire(preview.Current), Target: previewMaintenanceStateToWire(preview.Target),
		ActiveRooms: preview.ActiveRooms, ActiveGames: preview.ActiveGames,
		RejectedProcedureCodes: append([]string(nil), preview.RejectedProcedures...),
		PreviewDigest:          digestToWire(preview.PreviewDigest), SampledAt: timestampOrNil(preview.SampledAt), ExpiresAt: timestampOrNil(preview.ExpiresAt),
	}), nil
}

// ApplyMaintenanceChange executes only the server preview bound to the request's expected authority version.
func (handler *Handler) ApplyMaintenanceChange(ctx context.Context, request *connect.Request[adminv1.ApplyMaintenanceChangeRequest]) (*connect.Response[adminv1.ApplyMaintenanceChangeResponse], error) {
	actor, err := commandActor(ctx)
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil {
		return nil, domain.ErrInvalidInput
	}
	command, err := maintenanceCommandFromWire(request.Msg)
	if err != nil {
		return nil, err
	}
	result, err := handler.commands.ApplyMaintenanceChange(ctx, actor, command)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.ApplyMaintenanceChangeResponse{
		Receipt: commandReceiptToWire(result.Receipt), Outcome: commandOutcomeToWire(result.Outcome),
		Maintenance: previewMaintenanceStateToWire(result.Maintenance),
	}), nil
}

// PreviewCacheRefresh obtains a real owned-entry estimate for one fixed rebuildable projection.
func (handler *Handler) PreviewCacheRefresh(ctx context.Context, request *connect.Request[adminv1.PreviewCacheRefreshRequest]) (*connect.Response[adminv1.PreviewCacheRefreshResponse], error) {
	actor, err := commandActor(ctx)
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil {
		return nil, domain.ErrInvalidInput
	}
	namespace, err := cacheNamespaceFromWire(request.Msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	preview, err := handler.commands.PreviewCacheRefresh(ctx, actor, domain.PreviewCacheRefreshInput{Namespace: namespace, Reason: request.Msg.GetReason()})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.PreviewCacheRefreshResponse{
		Namespace: cacheNamespaceToWire(preview.Namespace), CurrentGeneration: preview.CurrentGeneration,
		EstimatedEntries: preview.EstimatedEntries, PreviewDigest: digestToWire(preview.PreviewDigest),
		SampledAt: timestampOrNil(preview.SampledAt), ExpiresAt: timestampOrNil(preview.ExpiresAt),
	}), nil
}

// ApplyCacheRefresh advances a PostgreSQL generation; Redis mutation remains an asynchronous owned projection concern.
func (handler *Handler) ApplyCacheRefresh(ctx context.Context, request *connect.Request[adminv1.ApplyCacheRefreshRequest]) (*connect.Response[adminv1.ApplyCacheRefreshResponse], error) {
	actor, err := commandActor(ctx)
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil {
		return nil, domain.ErrInvalidInput
	}
	operationID, err := parseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, err
	}
	namespace, err := cacheNamespaceFromWire(request.Msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	digest, err := digestFromWire(request.Msg.GetPreviewDigest())
	if err != nil {
		return nil, err
	}
	result, err := handler.commands.ApplyCacheRefresh(ctx, actor, domain.ApplyCacheRefreshCommand{
		OperationID: operationID, Namespace: namespace, Reason: request.Msg.GetReason(),
		ExpectedGeneration: request.Msg.GetExpectedGeneration(), PreviewDigest: digest,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.ApplyCacheRefreshResponse{
		Receipt: commandReceiptToWire(result.Receipt), Outcome: commandOutcomeToWire(result.Outcome),
		Namespace: cacheNamespaceToWire(result.Namespace), PreviousGeneration: result.PreviousGeneration, CurrentGeneration: result.CurrentGeneration,
	}), nil
}

// PreviewTaskRetry reads one fixed durable task family without exposing payloads or raw error text.
func (handler *Handler) PreviewTaskRetry(ctx context.Context, request *connect.Request[adminv1.PreviewTaskRetryRequest]) (*connect.Response[adminv1.PreviewTaskRetryResponse], error) {
	actor, err := commandActor(ctx)
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil {
		return nil, domain.ErrInvalidInput
	}
	taskKind, err := retryTaskKindFromWire(request.Msg.GetTaskKind())
	if err != nil {
		return nil, err
	}
	taskID, err := strictUUID(request.Msg.GetTaskId())
	if err != nil {
		return nil, err
	}
	preview, err := handler.commands.PreviewTaskRetry(ctx, actor, domain.PreviewTaskRetryInput{TaskKind: taskKind, TaskID: taskID, Reason: request.Msg.GetReason()})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.PreviewTaskRetryResponse{
		TaskKind: retryTaskKindToWire(preview.Task.Kind), TaskId: preview.Task.ID.String(), CurrentState: jobStateToWire(preview.Task.State),
		TaskVersion: preview.Task.Version, ManualRetryCount: preview.ManualRetryCount, RetryAllowed: preview.RetryAllowed,
		StableErrorCode: preview.Task.StableErrorCode, PreviewDigest: digestToWire(preview.PreviewDigest),
		SampledAt: timestampOrNil(preview.SampledAt), ExpiresAt: timestampOrNil(preview.ExpiresAt),
	}), nil
}

// ApplyTaskRetry requeues the original task under its existing lease/CAS state machine.
func (handler *Handler) ApplyTaskRetry(ctx context.Context, request *connect.Request[adminv1.ApplyTaskRetryRequest]) (*connect.Response[adminv1.ApplyTaskRetryResponse], error) {
	actor, err := commandActor(ctx)
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil {
		return nil, domain.ErrInvalidInput
	}
	operationID, err := parseOperationID(request.Msg.GetOperationId())
	if err != nil {
		return nil, err
	}
	taskKind, err := retryTaskKindFromWire(request.Msg.GetTaskKind())
	if err != nil {
		return nil, err
	}
	taskID, err := strictUUID(request.Msg.GetTaskId())
	if err != nil {
		return nil, err
	}
	digest, err := digestFromWire(request.Msg.GetPreviewDigest())
	if err != nil {
		return nil, err
	}
	result, err := handler.commands.ApplyTaskRetry(ctx, actor, domain.ApplyTaskRetryCommand{
		OperationID: operationID, TaskKind: taskKind, TaskID: taskID, Reason: request.Msg.GetReason(),
		ExpectedTaskVersion: request.Msg.GetExpectedTaskVersion(), PreviewDigest: digest,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.ApplyTaskRetryResponse{
		Receipt: commandRetryReceiptToWire(result.Receipt), Outcome: commandOutcomeToWire(result.Outcome),
		TaskKind: retryTaskKindToWire(result.Task.Kind), TaskId: result.Task.ID.String(), TaskState: jobStateToWire(result.Task.State),
		TaskVersion: result.Task.Version, ManualRetryCount: result.ManualRetryCount,
	}), nil
}

func maintenanceCommandFromWire(message *adminv1.ApplyMaintenanceChangeRequest) (domain.ApplyMaintenanceChangeCommand, error) {
	operationID, err := parseOperationID(message.GetOperationId())
	if err != nil {
		return domain.ApplyMaintenanceChangeCommand{}, err
	}
	scope, err := maintenanceScopeFromWire(message.GetScope())
	if err != nil {
		return domain.ApplyMaintenanceChangeCommand{}, err
	}
	plannedEndAt, err := optionalTime(message.GetPlannedEndAt())
	if err != nil {
		return domain.ApplyMaintenanceChangeCommand{}, err
	}
	digest, err := digestFromWire(message.GetPreviewDigest())
	if err != nil {
		return domain.ApplyMaintenanceChangeCommand{}, err
	}
	return domain.ApplyMaintenanceChangeCommand{
		OperationID: operationID, Enabled: message.GetEnabled(), Scope: scope, Reason: message.GetReason(), PlannedEndAt: plannedEndAt,
		ExpectedVersion: message.GetExpectedVersion(), PreviewDigest: digest,
	}, nil
}

func commandActor(ctx context.Context) (admin.ActorContext, error) {
	actor, ok := adminauth.ActorFromContext(ctx)
	if !ok {
		return admin.ActorContext{}, admin.ErrAuthentication
	}
	return actor, nil
}

func maintenanceScopeFromWire(value adminv1.AdminMaintenanceScope) (domain.MaintenanceScope, error) {
	if value != adminv1.AdminMaintenanceScope_ADMIN_MAINTENANCE_SCOPE_USER_MUTATIONS {
		return "", domain.ErrInvalidInput
	}
	return domain.MaintenanceUserMutations, nil
}

func cacheNamespaceFromWire(value adminv1.AdminCacheNamespace) (domain.CacheNamespace, error) {
	switch value {
	case adminv1.AdminCacheNamespace_ADMIN_CACHE_NAMESPACE_OVERVIEW_PROJECTION:
		return domain.CacheOverviewProjection, nil
	case adminv1.AdminCacheNamespace_ADMIN_CACHE_NAMESPACE_OPERATIONS_PROBES:
		return domain.CacheOperationsProbes, nil
	case adminv1.AdminCacheNamespace_ADMIN_CACHE_NAMESPACE_REALTIME_PRESENCE_PROJECTION:
		return domain.CacheRealtimePresence, nil
	default:
		return "", domain.ErrInvalidInput
	}
}

func cacheNamespaceToWire(value domain.CacheNamespace) adminv1.AdminCacheNamespace {
	switch value {
	case domain.CacheOverviewProjection:
		return adminv1.AdminCacheNamespace_ADMIN_CACHE_NAMESPACE_OVERVIEW_PROJECTION
	case domain.CacheOperationsProbes:
		return adminv1.AdminCacheNamespace_ADMIN_CACHE_NAMESPACE_OPERATIONS_PROBES
	case domain.CacheRealtimePresence:
		return adminv1.AdminCacheNamespace_ADMIN_CACHE_NAMESPACE_REALTIME_PRESENCE_PROJECTION
	default:
		return adminv1.AdminCacheNamespace_ADMIN_CACHE_NAMESPACE_UNSPECIFIED
	}
}

func retryTaskKindFromWire(value adminv1.AdminRetryTaskKind) (domain.RetryTaskKind, error) {
	switch value {
	case adminv1.AdminRetryTaskKind_ADMIN_RETRY_TASK_KIND_USER_BATCH:
		return domain.RetryUserBatch, nil
	case adminv1.AdminRetryTaskKind_ADMIN_RETRY_TASK_KIND_USER_ERASURE:
		return domain.RetryUserErasure, nil
	default:
		return "", domain.ErrInvalidInput
	}
}

func retryTaskKindToWire(value domain.RetryTaskKind) adminv1.AdminRetryTaskKind {
	switch value {
	case domain.RetryUserBatch:
		return adminv1.AdminRetryTaskKind_ADMIN_RETRY_TASK_KIND_USER_BATCH
	case domain.RetryUserErasure:
		return adminv1.AdminRetryTaskKind_ADMIN_RETRY_TASK_KIND_USER_ERASURE
	default:
		return adminv1.AdminRetryTaskKind_ADMIN_RETRY_TASK_KIND_UNSPECIFIED
	}
}

func jobStateToWire(value string) adminv1.AdminJobState {
	switch value {
	case "pending", "queued":
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
	case "expired":
		return adminv1.AdminJobState_ADMIN_JOB_STATE_EXPIRED
	case "deleted":
		return adminv1.AdminJobState_ADMIN_JOB_STATE_DELETED
	default:
		return adminv1.AdminJobState_ADMIN_JOB_STATE_UNSPECIFIED
	}
}

func commandOutcomeToWire(value domain.CommandOutcome) adminv1.AdminOperationsCommandOutcome {
	switch value {
	case domain.CommandOutcomeApplied:
		return adminv1.AdminOperationsCommandOutcome_ADMIN_OPERATIONS_COMMAND_OUTCOME_APPLIED
	case domain.CommandOutcomeNoChange:
		return adminv1.AdminOperationsCommandOutcome_ADMIN_OPERATIONS_COMMAND_OUTCOME_NO_CHANGE
	case domain.CommandOutcomeRejected:
		return adminv1.AdminOperationsCommandOutcome_ADMIN_OPERATIONS_COMMAND_OUTCOME_REJECTED
	default:
		return adminv1.AdminOperationsCommandOutcome_ADMIN_OPERATIONS_COMMAND_OUTCOME_UNSPECIFIED
	}
}

func commandReceiptToWire(receipt domain.CommandReceipt) *adminv1.AdminOperationReceipt {
	return operationReceiptToWire(receipt.OperationID, receipt.AuditEventID, receipt.CompletedAt)
}

func commandRetryReceiptToWire(receipt domain.RetryReceipt) *adminv1.AdminOperationReceipt {
	return operationReceiptToWire(receipt.OperationID, receipt.AuditEventID, receipt.CompletedAt)
}

func operationReceiptToWire(operationID string, auditEventID uuid.UUID, completedAt time.Time) *adminv1.AdminOperationReceipt {
	auditID := ""
	if auditEventID != uuid.Nil {
		auditID = auditEventID.String()
	}
	return &adminv1.AdminOperationReceipt{OperationId: operationID, AuditEventId: auditID, CompletedAt: timestampOrNil(completedAt)}
}

func previewMaintenanceStateToWire(state domain.MaintenanceState) *adminv1.AdminMaintenanceState {
	return maintenanceToWire(state)
}

func digestToWire(value [sha256.Size]byte) string {
	return base64.RawURLEncoding.EncodeToString(value[:])
}

func digestFromWire(value string) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	trimmed := strings.TrimSpace(value)
	raw, err := base64.RawURLEncoding.Strict().DecodeString(trimmed)
	if err != nil || len(raw) != len(digest) || base64.RawURLEncoding.EncodeToString(raw) != trimmed {
		return digest, domain.ErrInvalidInput
	}
	copy(digest[:], raw)
	return digest, nil
}

func parseOperationID(value string) (idempotency.OperationID, error) {
	operationID, err := idempotency.ParseOperationID(strings.TrimSpace(value))
	if err != nil {
		return idempotency.OperationID{}, domain.ErrInvalidInput
	}
	return operationID, nil
}

func strictUUID(value string) (uuid.UUID, error) {
	trimmed := strings.TrimSpace(value)
	parsed, err := uuid.Parse(trimmed)
	if err != nil || parsed == uuid.Nil || parsed.String() != trimmed {
		return uuid.Nil, domain.ErrInvalidInput
	}
	return parsed, nil
}

func optionalTime(value *timestamppb.Timestamp) (time.Time, error) {
	if value == nil {
		return time.Time{}, nil
	}
	if err := value.CheckValid(); err != nil {
		return time.Time{}, domain.ErrInvalidInput
	}
	return value.AsTime().Round(0).UTC(), nil
}
