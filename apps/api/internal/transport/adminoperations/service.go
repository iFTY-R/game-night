package adminoperations

import (
	"context"
	"sort"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1/adminv1connect"
	"github.com/iFTY-R/game-night/platform/admin"
	domain "github.com/iFTY-R/game-night/platform/admin/operations"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SnapshotReader exposes the bounded domain snapshot without coupling this adapter to persistence.
type SnapshotReader interface {
	GetSnapshot(context.Context) (domain.OperationsSnapshot, error)
}

// MaintenanceReader exposes the authoritative singleton for the dedicated maintenance query.
type MaintenanceReader interface {
	GetMaintenanceState(context.Context) (domain.MaintenanceState, error)
}

// CommandExecutor owns authorization, preview binding, idempotency, and atomic operations writes.
type CommandExecutor interface {
	PreviewMaintenanceChange(context.Context, admin.ActorContext, domain.PreviewMaintenanceChangeInput) (domain.MaintenancePreview, error)
	ApplyMaintenanceChange(context.Context, admin.ActorContext, domain.ApplyMaintenanceChangeCommand) (domain.MaintenanceChangeResult, error)
	PreviewCacheRefresh(context.Context, admin.ActorContext, domain.PreviewCacheRefreshInput) (domain.CacheRefreshPreview, error)
	ApplyCacheRefresh(context.Context, admin.ActorContext, domain.ApplyCacheRefreshCommand) (domain.CacheRefreshResult, error)
	PreviewTaskRetry(context.Context, admin.ActorContext, domain.PreviewTaskRetryInput) (domain.TaskRetryPreview, error)
	ApplyTaskRetry(context.Context, admin.ActorContext, domain.ApplyTaskRetryCommand) (domain.TaskRetryResult, error)
}

// Handler maps authenticated operations reads to the generated Connect contract.
type Handler struct {
	adminv1connect.UnimplementedAdminOperationsServiceHandler

	snapshots   SnapshotReader
	maintenance MaintenanceReader
	commands    CommandExecutor
}

// NewService requires the aggregate reader, direct maintenance authority, and sensitive command service.
func NewService(snapshots SnapshotReader, maintenance MaintenanceReader, commands CommandExecutor) (*Handler, error) {
	if snapshots == nil || maintenance == nil || commands == nil {
		return nil, domain.ErrInvalidInput
	}
	return &Handler{snapshots: snapshots, maintenance: maintenance, commands: commands}, nil
}

// GetOperationsSnapshot returns real bounded evidence; individual dependency failures stay in-band as statuses.
func (handler *Handler) GetOperationsSnapshot(ctx context.Context, _ *connect.Request[adminv1.GetOperationsSnapshotRequest]) (*connect.Response[adminv1.GetOperationsSnapshotResponse], error) {
	snapshot, err := handler.snapshots.GetSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	services := make([]*adminv1.AdminServiceInstance, 0, len(snapshot.Services))
	for _, instance := range snapshot.Services {
		services = append(services, serviceInstanceToWire(instance))
	}
	dependencies := make([]*adminv1.AdminDependencyHealth, 0, len(snapshot.Dependencies))
	for _, dependency := range snapshot.Dependencies {
		dependencies = append(dependencies, dependencyToWire(dependency))
	}
	backlogs := make([]*adminv1.AdminBacklogSummary, 0, len(snapshot.Backlogs))
	for _, backlog := range snapshot.Backlogs {
		backlogs = append(backlogs, backlogToWire(backlog, snapshot.FreshUntil))
	}
	return connect.NewResponse(&adminv1.GetOperationsSnapshotResponse{
		Services:     services,
		Dependencies: dependencies,
		Backlogs:     backlogs,
		Maintenance:  maintenanceToWire(snapshot.Maintenance),
		SampledAt:    timestampOrNil(snapshot.SampledAt),
		FreshUntil:   timestampOrNil(snapshot.FreshUntil),
	}), nil
}

// GetMaintenanceState reads the singleton directly so a degraded optional probe does not hide mutation authority.
func (handler *Handler) GetMaintenanceState(ctx context.Context, _ *connect.Request[adminv1.GetMaintenanceStateRequest]) (*connect.Response[adminv1.GetMaintenanceStateResponse], error) {
	state, err := handler.maintenance.GetMaintenanceState(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.GetMaintenanceStateResponse{Maintenance: maintenanceToWire(state), SampledAt: timestampOrNil(state.ChangedAt)}), nil
}

func serviceInstanceToWire(instance domain.ServiceInstance) *adminv1.AdminServiceInstance {
	codes := make([]string, 0, len(instance.Components))
	for code := range instance.Components {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	components := make([]*adminv1.AdminServiceComponentStatus, 0, len(codes))
	for _, code := range codes {
		components = append(components, &adminv1.AdminServiceComponentStatus{ComponentCode: code, Status: healthToWire(instance.Components[code])})
	}
	return &adminv1.AdminServiceInstance{
		Kind: serviceKindToWire(instance.Kind), InstanceId: instance.InstanceID, BuildVersion: instance.BuildVersion,
		StartedAt: timestampOrNil(instance.StartedAt), LastHeartbeatAt: timestampOrNil(instance.LastHeartbeatAt),
		Status: healthToWire(instance.Status), MaintenanceVersion: instance.MaintenanceVersion, Components: components,
	}
}

func dependencyToWire(dependency domain.DependencyHealth) *adminv1.AdminDependencyHealth {
	return &adminv1.AdminDependencyHealth{
		Kind: dependencyKindToWire(dependency.Kind), Status: healthToWire(dependency.Status),
		SampledAt: timestampOrNil(dependency.SampledAt), FreshUntil: timestampOrNil(dependency.FreshUntil),
	}
}

func backlogToWire(backlog domain.BacklogSummary, freshUntil time.Time) *adminv1.AdminBacklogSummary {
	return &adminv1.AdminBacklogSummary{
		Kind: backlogKindToWire(backlog.Kind), Pending: backlog.Pending, Running: backlog.Running, Failed: backlog.Failed,
		OldestPendingAt: timestampOrNil(backlog.OldestPendingAt), SampledAt: timestampOrNil(backlog.SampledAt), FreshUntil: timestampOrNil(freshUntil),
	}
}

func maintenanceToWire(state domain.MaintenanceState) *adminv1.AdminMaintenanceState {
	return &adminv1.AdminMaintenanceState{
		Enabled: state.Enabled, Scope: adminv1.AdminMaintenanceScope_ADMIN_MAINTENANCE_SCOPE_USER_MUTATIONS,
		Reason: state.Reason, PlannedEndAt: timestampOrNil(state.PlannedEndAt), Version: state.Version,
		ChangedByAdminId: state.ChangedByAdminID.String(), ChangedAt: timestampOrNil(state.ChangedAt),
	}
}

func healthToWire(status domain.HealthStatus) adminv1.AdminHealthStatus {
	switch status {
	case domain.HealthHealthy:
		return adminv1.AdminHealthStatus_ADMIN_HEALTH_STATUS_HEALTHY
	case domain.HealthDegraded:
		return adminv1.AdminHealthStatus_ADMIN_HEALTH_STATUS_DEGRADED
	case domain.HealthUnavailable:
		return adminv1.AdminHealthStatus_ADMIN_HEALTH_STATUS_UNAVAILABLE
	case domain.HealthStale:
		return adminv1.AdminHealthStatus_ADMIN_HEALTH_STATUS_STALE
	default:
		return adminv1.AdminHealthStatus_ADMIN_HEALTH_STATUS_UNSPECIFIED
	}
}

func serviceKindToWire(kind domain.ServiceKind) adminv1.AdminServiceKind {
	switch kind {
	case domain.ServiceAPI:
		return adminv1.AdminServiceKind_ADMIN_SERVICE_KIND_API
	case domain.ServiceEdge:
		return adminv1.AdminServiceKind_ADMIN_SERVICE_KIND_EDGE
	case domain.ServiceRealtime:
		return adminv1.AdminServiceKind_ADMIN_SERVICE_KIND_REALTIME
	case domain.ServiceWorker:
		return adminv1.AdminServiceKind_ADMIN_SERVICE_KIND_WORKER
	default:
		return adminv1.AdminServiceKind_ADMIN_SERVICE_KIND_UNSPECIFIED
	}
}

func dependencyKindToWire(kind domain.DependencyKind) adminv1.AdminDependencyKind {
	switch kind {
	case domain.DependencyPostgreSQL:
		return adminv1.AdminDependencyKind_ADMIN_DEPENDENCY_KIND_POSTGRESQL
	case domain.DependencyRedis:
		return adminv1.AdminDependencyKind_ADMIN_DEPENDENCY_KIND_REDIS
	case domain.DependencyExportResultStore:
		return adminv1.AdminDependencyKind_ADMIN_DEPENDENCY_KIND_EXPORT_RESULT_STORE
	case domain.DependencyCheckpointSink:
		return adminv1.AdminDependencyKind_ADMIN_DEPENDENCY_KIND_CHECKPOINT_SINK
	case domain.DependencyCheckpointProgress:
		return adminv1.AdminDependencyKind_ADMIN_DEPENDENCY_KIND_CHECKPOINT_PROGRESS
	case domain.DependencyRealtimePresence:
		return adminv1.AdminDependencyKind_ADMIN_DEPENDENCY_KIND_REALTIME_PRESENCE
	case domain.DependencyRateLimiter:
		return adminv1.AdminDependencyKind_ADMIN_DEPENDENCY_KIND_RATE_LIMITER
	default:
		return adminv1.AdminDependencyKind_ADMIN_DEPENDENCY_KIND_UNSPECIFIED
	}
}

func backlogKindToWire(kind domain.BacklogKind) adminv1.AdminBacklogKind {
	switch kind {
	case domain.BacklogAuditOutbox:
		return adminv1.AdminBacklogKind_ADMIN_BACKLOG_KIND_AUDIT_OUTBOX
	case domain.BacklogRoomOutbox:
		return adminv1.AdminBacklogKind_ADMIN_BACKLOG_KIND_ROOM_OUTBOX
	case domain.BacklogRealtimeTimer:
		return adminv1.AdminBacklogKind_ADMIN_BACKLOG_KIND_REALTIME_TIMER
	case domain.BacklogUserBatch:
		return adminv1.AdminBacklogKind_ADMIN_BACKLOG_KIND_USER_BATCH
	case domain.BacklogUserErasure:
		return adminv1.AdminBacklogKind_ADMIN_BACKLOG_KIND_USER_ERASURE
	default:
		return adminv1.AdminBacklogKind_ADMIN_BACKLOG_KIND_UNSPECIFIED
	}
}

func timestampOrNil(value time.Time) *timestamppb.Timestamp {
	if value.IsZero() {
		return nil
	}
	return timestamppb.New(value)
}
