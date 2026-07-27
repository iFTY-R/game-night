// Package adminoverview adapts the authenticated overview RPC to bounded real domain sources.
package adminoverview

import (
	"context"
	"math"
	"strings"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1/adminv1connect"
	auditv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/audit/v1"
	domain "github.com/iFTY-R/game-night/platform/admin/operations"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Handler maps the generated request to the overview domain and never accepts actor fields from the wire.
type Handler struct {
	adminv1connect.UnimplementedAdminOverviewServiceHandler
	service *domain.OverviewService
}

// NewService requires the complete real-source overview aggregation service.
func NewService(service *domain.OverviewService) (*Handler, error) {
	if service == nil {
		return nil, domain.ErrInvalidInput
	}
	return &Handler{service: service}, nil
}

// GetOverview validates timestamp and granularity boundaries before executing domain reads.
func (handler *Handler) GetOverview(ctx context.Context, request *connect.Request[adminv1.GetOverviewRequest]) (*connect.Response[adminv1.GetOverviewResponse], error) {
	query, err := queryFromWire(request.Msg)
	if err != nil {
		return nil, err
	}
	snapshot, err := handler.service.GetOverview(ctx, query)
	if err != nil {
		return nil, err
	}
	metrics := make([]*adminv1.AdminOverviewMetricValue, 0, len(snapshot.Metrics))
	for _, metric := range snapshot.Metrics {
		wire, wireErr := metricToWire(metric)
		if wireErr != nil {
			return nil, wireErr
		}
		metrics = append(metrics, wire)
	}
	trends := make([]*adminv1.AdminOverviewTrendSeries, 0, len(snapshot.Trends))
	for _, series := range snapshot.Trends {
		wire, wireErr := trendToWire(series)
		if wireErr != nil {
			return nil, wireErr
		}
		trends = append(trends, wire)
	}
	dependencies := make([]*adminv1.AdminDependencyHealth, 0, len(snapshot.Dependencies))
	for _, dependency := range snapshot.Dependencies {
		dependencies = append(dependencies, dependencyToWire(dependency))
	}
	attention := make([]*adminv1.AdminOverviewAttentionItem, 0, len(snapshot.Attention))
	for _, item := range snapshot.Attention {
		attention = append(attention, attentionToWire(item))
	}
	highRiskOperations := make([]*adminv1.AdminOverviewRiskOperation, 0, len(snapshot.HighRiskOperations))
	for _, operation := range snapshot.HighRiskOperations {
		wire, wireErr := riskOperationToWire(operation)
		if wireErr != nil {
			return nil, wireErr
		}
		highRiskOperations = append(highRiskOperations, wire)
	}
	failedTasks := make([]*adminv1.AdminOverviewFailedTask, 0, len(snapshot.FailedTasks))
	for _, task := range snapshot.FailedTasks {
		failedTasks = append(failedTasks, failedTaskToWire(task))
	}
	return connect.NewResponse(&adminv1.GetOverviewResponse{
		Metrics: metrics, Trends: trends, Attention: attention, Dependencies: dependencies,
		HighRiskOperations: highRiskOperations, FailedTasks: failedTasks,
		WindowStart: timestampOrNil(snapshot.WindowStart), WindowEnd: timestampOrNil(snapshot.WindowEnd),
		SampledAt: timestampOrNil(snapshot.SampledAt), FreshUntil: timestampOrNil(snapshot.FreshUntil),
	}), nil
}

func attentionToWire(item domain.AttentionItem) *adminv1.AdminOverviewAttentionItem {
	kind := adminv1.AdminAttentionKind_ADMIN_ATTENTION_KIND_UNSPECIFIED
	if item.Kind == domain.AttentionRoom {
		kind = adminv1.AdminAttentionKind_ADMIN_ATTENTION_KIND_ROOM
	} else if item.Kind == domain.AttentionGame {
		kind = adminv1.AdminAttentionKind_ADMIN_ATTENTION_KIND_GAME
	}
	return &adminv1.AdminOverviewAttentionItem{
		Kind: kind, ResourceId: item.ResourceID.String(), RoomId: item.RoomID.String(), StatusCode: item.StatusCode,
		ReasonCodes: append([]string(nil), item.ReasonCodes...), ObservedAt: timestampOrNil(item.ObservedAt),
	}
}

func riskOperationToWire(operation domain.RiskOperation) (*adminv1.AdminOverviewRiskOperation, error) {
	actionName := auditv1.AuditAction(operation.Action).String()
	if actionName == "" || actionName == "AUDIT_ACTION_UNSPECIFIED" || !operation.Verified {
		return nil, domain.ErrIntegrity
	}
	return &adminv1.AdminOverviewRiskOperation{
		AuditEventId: operation.AuditEventID.String(), Action: strings.ToLower(strings.TrimPrefix(actionName, "AUDIT_ACTION_")),
		ActorAdminId: operation.ActorAdminID.String(), TargetId: operation.TargetID, Verified: true,
		OccurredAt: timestampOrNil(operation.OccurredAt),
	}, nil
}

func queryFromWire(request *adminv1.GetOverviewRequest) (domain.OverviewQuery, error) {
	if request == nil || request.GetWindowStart() == nil || request.GetWindowEnd() == nil ||
		request.GetWindowStart().CheckValid() != nil || request.GetWindowEnd().CheckValid() != nil {
		return domain.OverviewQuery{}, domain.ErrInvalidInput
	}
	width := domain.BucketWidth("")
	switch request.GetGranularity() {
	case adminv1.AdminOverviewGranularity_ADMIN_OVERVIEW_GRANULARITY_HOUR:
		width = domain.BucketHour
	case adminv1.AdminOverviewGranularity_ADMIN_OVERVIEW_GRANULARITY_DAY:
		width = domain.BucketDay
	default:
		return domain.OverviewQuery{}, domain.ErrInvalidInput
	}
	return domain.OverviewQuery{WindowStart: request.GetWindowStart().AsTime().UTC(), WindowEnd: request.GetWindowEnd().AsTime().UTC(), Width: width}, nil
}

func metricToWire(metric domain.MetricValue) (*adminv1.AdminOverviewMetricValue, error) {
	value, err := boundedWireCount(metric.Value)
	if err != nil {
		return nil, err
	}
	return &adminv1.AdminOverviewMetricValue{
		Metric: metricNameToWire(metric.Name), Value: value, UnavailableReason: availabilityToWire(metric.Unavailable),
		WindowStart: timestampOrNil(metric.WindowStart), WindowEnd: timestampOrNil(metric.WindowEnd),
		SampledAt: timestampOrNil(metric.SampledAt), FreshUntil: timestampOrNil(metric.FreshUntil),
	}, nil
}

func trendToWire(series domain.TrendSeries) (*adminv1.AdminOverviewTrendSeries, error) {
	points := make([]*adminv1.AdminOverviewTrendPoint, 0, len(series.Points))
	for _, point := range series.Points {
		value, err := boundedWireCount(point.Value)
		if err != nil {
			return nil, err
		}
		points = append(points, &adminv1.AdminOverviewTrendPoint{BucketStart: timestampOrNil(point.Start), BucketEnd: timestampOrNil(point.End), Value: value, SampledAt: timestampOrNil(point.SampledAt)})
	}
	granularity := adminv1.AdminOverviewGranularity_ADMIN_OVERVIEW_GRANULARITY_UNSPECIFIED
	if series.Width == domain.BucketHour {
		granularity = adminv1.AdminOverviewGranularity_ADMIN_OVERVIEW_GRANULARITY_HOUR
	} else if series.Width == domain.BucketDay {
		granularity = adminv1.AdminOverviewGranularity_ADMIN_OVERVIEW_GRANULARITY_DAY
	}
	return &adminv1.AdminOverviewTrendSeries{Metric: metricNameToWire(series.Name), Granularity: granularity, Points: points, UnavailableReason: availabilityToWire(series.Unavailable), FreshUntil: timestampOrNil(series.FreshUntil)}, nil
}

func dependencyToWire(dependency domain.DependencyHealth) *adminv1.AdminDependencyHealth {
	return &adminv1.AdminDependencyHealth{Kind: dependencyKindToWire(dependency.Kind), Status: healthToWire(dependency.Status), SampledAt: timestampOrNil(dependency.SampledAt), FreshUntil: timestampOrNil(dependency.FreshUntil)}
}

func failedTaskToWire(task domain.FailedTask) *adminv1.AdminOverviewFailedTask {
	kind := adminv1.AdminRetryTaskKind_ADMIN_RETRY_TASK_KIND_UNSPECIFIED
	if task.Kind == domain.RetryUserBatch {
		kind = adminv1.AdminRetryTaskKind_ADMIN_RETRY_TASK_KIND_USER_BATCH
	} else if task.Kind == domain.RetryUserErasure {
		kind = adminv1.AdminRetryTaskKind_ADMIN_RETRY_TASK_KIND_USER_ERASURE
	}
	state := adminv1.AdminJobState_ADMIN_JOB_STATE_UNSPECIFIED
	if task.State == "failed" {
		state = adminv1.AdminJobState_ADMIN_JOB_STATE_FAILED
	}
	return &adminv1.AdminOverviewFailedTask{TaskKind: kind, TaskId: task.ID.String(), State: state, StableErrorCode: task.StableErrorCode, Attempts: task.Attempts, TaskVersion: task.Version, UpdatedAt: timestampOrNil(task.UpdatedAt)}
}

func metricNameToWire(name domain.MetricName) adminv1.AdminOverviewMetric {
	return map[domain.MetricName]adminv1.AdminOverviewMetric{
		domain.MetricOnlineUsers:          adminv1.AdminOverviewMetric_ADMIN_OVERVIEW_METRIC_ONLINE_USERS,
		domain.MetricActiveRooms:          adminv1.AdminOverviewMetric_ADMIN_OVERVIEW_METRIC_ACTIVE_ROOMS,
		domain.MetricRunningGames:         adminv1.AdminOverviewMetric_ADMIN_OVERVIEW_METRIC_RUNNING_GAMES,
		domain.MetricNewUsers:             adminv1.AdminOverviewMetric_ADMIN_OVERVIEW_METRIC_NEW_USERS,
		domain.MetricSuspendedUsers:       adminv1.AdminOverviewMetric_ADMIN_OVERVIEW_METRIC_SUSPENDED_USERS,
		domain.MetricUnsuspendedUsers:     adminv1.AdminOverviewMetric_ADMIN_OVERVIEW_METRIC_UNSUSPENDED_USERS,
		domain.MetricAbnormalTerminations: adminv1.AdminOverviewMetric_ADMIN_OVERVIEW_METRIC_ABNORMAL_TERMINATIONS,
		domain.MetricEmergencyRepairs:     adminv1.AdminOverviewMetric_ADMIN_OVERVIEW_METRIC_EMERGENCY_REPAIRS,
	}[name]
}

func availabilityToWire(reason domain.AvailabilityReason) adminv1.AdminOverviewUnavailableReason {
	switch reason {
	case domain.AvailabilityNone:
		return adminv1.AdminOverviewUnavailableReason_ADMIN_OVERVIEW_UNAVAILABLE_REASON_NONE
	case domain.AvailabilitySourceUnavailable:
		return adminv1.AdminOverviewUnavailableReason_ADMIN_OVERVIEW_UNAVAILABLE_REASON_SOURCE_UNAVAILABLE
	case domain.AvailabilitySourceStale:
		return adminv1.AdminOverviewUnavailableReason_ADMIN_OVERVIEW_UNAVAILABLE_REASON_SOURCE_STALE
	case domain.AvailabilityWindowUnsupported:
		return adminv1.AdminOverviewUnavailableReason_ADMIN_OVERVIEW_UNAVAILABLE_REASON_WINDOW_UNSUPPORTED
	default:
		return adminv1.AdminOverviewUnavailableReason_ADMIN_OVERVIEW_UNAVAILABLE_REASON_UNSPECIFIED
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

// Protobuf uses int64 for counts, so reject impossible persisted values instead of wrapping them negative.
func boundedWireCount(value uint64) (int64, error) {
	if value > math.MaxInt64 {
		return 0, domain.ErrIntegrity
	}
	return int64(value), nil
}

func timestampOrNil(value time.Time) *timestamppb.Timestamp {
	if value.IsZero() {
		return nil
	}
	return timestamppb.New(value)
}
