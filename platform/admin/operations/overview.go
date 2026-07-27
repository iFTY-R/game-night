package operations

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/iFTY-R/game-night/platform/clock"
)

const (
	// MaximumOverviewWindow bounds PostgreSQL bucket and event-range scans initiated by one browser request.
	MaximumOverviewWindow = 31 * 24 * time.Hour
	// OverviewFreshness tells the browser when current counts should be sampled again.
	OverviewFreshness = 30 * time.Second
	// defaultOverviewTimeout bounds all independent overview sources together.
	defaultOverviewTimeout = 2 * time.Second
)

var overviewMetricOrder = [...]MetricName{
	MetricOnlineUsers,
	MetricActiveRooms,
	MetricRunningGames,
	MetricNewUsers,
	MetricSuspendedUsers,
	MetricUnsuspendedUsers,
	MetricAbnormalTerminations,
	MetricEmergencyRepairs,
}

// OverviewRepository exposes only bounded, indexed overview reads.
type OverviewRepository interface {
	GetOverviewCounts(context.Context, time.Time, time.Time, time.Time) (OverviewCounts, error)
	ListMetricBuckets(context.Context, MetricQuery) ([]MetricBucket, error)
	ListAttentionItems(context.Context, uint32) ([]AttentionItem, error)
	ListFailedTasks(context.Context, uint32) ([]FailedTask, error)
}

// OverviewAuditReader returns only recent, signature-verified high-risk audit metadata.
type OverviewAuditReader interface {
	ListRecentHighRiskOperations(context.Context, time.Time, time.Time, uint32) ([]RiskOperation, error)
}

// PresenceReader supplies aggregate online state from a dedicated expiring projection.
type PresenceReader interface {
	ReadPresenceSummary(context.Context) (PresenceSummary, error)
}

// OperationsSnapshotReader supplies dependency status without exposing probe implementation details.
type OperationsSnapshotReader interface {
	GetSnapshot(context.Context) (OperationsSnapshot, error)
}

// OverviewServiceConfig freezes the three real data sources and request deadline.
type OverviewServiceConfig struct {
	Repository OverviewRepository
	Presence   PresenceReader
	Operations OperationsSnapshotReader
	Audit      OverviewAuditReader
	Clock      clock.Clock
	Timeout    time.Duration
}

// OverviewService aggregates partial real evidence while preserving zero versus unavailable semantics.
type OverviewService struct {
	repository OverviewRepository
	presence   PresenceReader
	operations OperationsSnapshotReader
	audit      OverviewAuditReader
	clock      clock.Clock
	timeout    time.Duration
}

// NewOverviewService validates the full read graph before it can serve the admin landing page.
func NewOverviewService(config OverviewServiceConfig) (*OverviewService, error) {
	if config.Repository == nil || config.Presence == nil || config.Operations == nil || config.Audit == nil || config.Clock == nil {
		return nil, ErrInvalidInput
	}
	timeout := config.Timeout
	if timeout == 0 {
		timeout = defaultOverviewTimeout
	}
	if timeout <= 0 || timeout > 10*time.Second {
		return nil, ErrInvalidInput
	}
	return &OverviewService{repository: config.Repository, presence: config.Presence, operations: config.Operations, audit: config.Audit, clock: config.Clock, timeout: timeout}, nil
}

// GetOverview reads each source concurrently so one slow dependency cannot consume the other source budgets.
func (service *OverviewService) GetOverview(ctx context.Context, query OverviewQuery) (OverviewSnapshot, error) {
	if service == nil || ctx == nil || !validOverviewQuery(query) {
		return OverviewSnapshot{}, ErrInvalidInput
	}
	requestContext, cancel := context.WithTimeout(ctx, service.timeout)
	defer cancel()
	sampledAt := service.clock.Now()

	var result overviewSourceResults
	var waitGroup sync.WaitGroup
	waitGroup.Add(7)
	go func() {
		defer waitGroup.Done()
		result.counts, result.countsErr = service.repository.GetOverviewCounts(requestContext, query.WindowStart, query.WindowEnd, sampledAt)
	}()
	go func() {
		defer waitGroup.Done()
		result.presence, result.presenceErr = service.presence.ReadPresenceSummary(requestContext)
	}()
	go func() {
		defer waitGroup.Done()
		result.buckets, result.bucketsErr = service.repository.ListMetricBuckets(requestContext, MetricQuery{
			Names: append([]MetricName(nil), overviewMetricOrder[:]...), Width: query.Width,
			WindowStart: query.WindowStart, WindowEnd: query.WindowEnd, Limit: overviewBucketLimit(query),
		})
	}()
	go func() {
		defer waitGroup.Done()
		result.failedTasks, result.failedTasksErr = service.repository.ListFailedTasks(requestContext, MaximumOverviewFailedTasks)
	}()
	go func() {
		defer waitGroup.Done()
		result.attention, result.attentionErr = service.repository.ListAttentionItems(requestContext, MaximumOverviewAttentionItems)
	}()
	go func() {
		defer waitGroup.Done()
		result.riskOperations, result.riskOperationsErr = service.audit.ListRecentHighRiskOperations(requestContext, query.WindowStart, query.WindowEnd, MaximumOverviewRiskOperations)
	}()
	go func() {
		defer waitGroup.Done()
		result.operations, result.operationsErr = service.operations.GetSnapshot(requestContext)
	}()
	waitGroup.Wait()

	return OverviewSnapshot{
		Metrics:              overviewMetrics(query, sampledAt, result),
		Trends:               overviewTrends(query, sampledAt, result.buckets, result.bucketsErr),
		Attention:            cloneAttention(result.attention, result.attentionErr),
		Dependencies:         cloneDependencies(result.operations.Dependencies, result.operationsErr),
		HighRiskOperations:   cloneRiskOperations(result.riskOperations, result.riskOperationsErr),
		FailedTasks:          cloneFailedTasks(result.failedTasks, result.failedTasksErr),
		FailedTasksAvailable: result.failedTasksErr == nil,
		WindowStart:          query.WindowStart, WindowEnd: query.WindowEnd,
		SampledAt: sampledAt, FreshUntil: sampledAt.Add(OverviewFreshness),
	}, nil
}

type overviewSourceResults struct {
	counts            OverviewCounts
	countsErr         error
	presence          PresenceSummary
	presenceErr       error
	buckets           []MetricBucket
	bucketsErr        error
	failedTasks       []FailedTask
	failedTasksErr    error
	attention         []AttentionItem
	attentionErr      error
	riskOperations    []RiskOperation
	riskOperationsErr error
	operations        OperationsSnapshot
	operationsErr     error
}

func validOverviewQuery(query OverviewQuery) bool {
	width, ok := query.Width.Duration()
	return ok && !query.WindowStart.IsZero() && query.WindowStart.Equal(query.WindowStart.UTC()) &&
		query.WindowEnd.Equal(query.WindowEnd.UTC()) && query.WindowEnd.After(query.WindowStart) &&
		query.WindowEnd.Sub(query.WindowStart) <= MaximumOverviewWindow &&
		query.WindowStart.Unix()%int64(width/time.Second) == 0 && query.WindowEnd.Unix()%int64(width/time.Second) == 0
}

func overviewBucketLimit(query OverviewQuery) uint32 {
	width, _ := query.Width.Duration()
	count := uint64(query.WindowEnd.Sub(query.WindowStart)/width) * uint64(len(overviewMetricOrder))
	if count == 0 {
		return uint32(len(overviewMetricOrder))
	}
	if count > MaximumMetricBuckets {
		return MaximumMetricBuckets
	}
	return uint32(count)
}

func overviewMetrics(query OverviewQuery, sampledAt time.Time, result overviewSourceResults) []MetricValue {
	metrics := make([]MetricValue, 0, len(overviewMetricOrder))
	for _, name := range overviewMetricOrder {
		metric := MetricValue{Name: name, WindowStart: query.WindowStart, WindowEnd: query.WindowEnd, SampledAt: sampledAt, FreshUntil: sampledAt.Add(OverviewFreshness)}
		switch name {
		case MetricOnlineUsers:
			metric.Unavailable = availabilityFor(result.presenceErr, result.presence.FreshUntil, sampledAt)
			metric.Value = result.presence.OnlineUsers
			if result.presenceErr == nil {
				metric.SampledAt, metric.FreshUntil = result.presence.SampledAt, result.presence.FreshUntil
			}
		case MetricActiveRooms:
			metric.Unavailable, metric.Value = availabilityFor(result.countsErr, time.Time{}, sampledAt), result.counts.ActiveRooms
		case MetricRunningGames:
			metric.Unavailable, metric.Value = availabilityFor(result.countsErr, time.Time{}, sampledAt), result.counts.RunningGames
		case MetricNewUsers:
			metric.Unavailable, metric.Value = availabilityFor(result.countsErr, time.Time{}, sampledAt), result.counts.NewUsers
		case MetricSuspendedUsers:
			metric.Unavailable, metric.Value = availabilityFor(result.countsErr, time.Time{}, sampledAt), result.counts.SuspendedUsers
		case MetricUnsuspendedUsers:
			metric.Unavailable, metric.Value = availabilityFor(result.countsErr, time.Time{}, sampledAt), result.counts.UnsuspendedUsers
		case MetricAbnormalTerminations:
			metric.Unavailable, metric.Value = availabilityFor(result.countsErr, time.Time{}, sampledAt), result.counts.AbnormalTerminations
		case MetricEmergencyRepairs:
			metric.Unavailable, metric.Value = availabilityFor(result.countsErr, time.Time{}, sampledAt), result.counts.EmergencyRepairs
		default:
			metric.Unavailable = AvailabilitySourceUnavailable
		}
		metrics = append(metrics, metric)
	}
	return metrics
}

func overviewTrends(query OverviewQuery, sampledAt time.Time, buckets []MetricBucket, err error) []TrendSeries {
	seriesByName := make(map[MetricName][]TrendPoint, len(overviewMetricOrder))
	width, _ := query.Width.Duration()
	if err == nil {
		for _, bucket := range buckets {
			seriesByName[bucket.Name] = append(seriesByName[bucket.Name], TrendPoint{Start: bucket.Start, End: bucket.Start.Add(width), Value: bucket.Value, SampledAt: bucket.SampledAt})
		}
	}
	series := make([]TrendSeries, 0, len(overviewMetricOrder))
	for _, name := range overviewMetricOrder {
		points := seriesByName[name]
		sort.Slice(points, func(left, right int) bool { return points[left].Start.Before(points[right].Start) })
		unavailable := AvailabilityNone
		if err != nil {
			unavailable = AvailabilitySourceUnavailable
		}
		series = append(series, TrendSeries{Name: name, Width: query.Width, Points: append([]TrendPoint(nil), points...), Unavailable: unavailable, FreshUntil: sampledAt.Add(OverviewFreshness)})
	}
	return series
}

func availabilityFor(err error, freshUntil, sampledAt time.Time) AvailabilityReason {
	if err != nil {
		return AvailabilitySourceUnavailable
	}
	if !freshUntil.IsZero() && freshUntil.Before(sampledAt) {
		return AvailabilitySourceStale
	}
	return AvailabilityNone
}

func cloneDependencies(source []DependencyHealth, err error) []DependencyHealth {
	if err != nil {
		return nil
	}
	return append([]DependencyHealth(nil), source...)
}

func cloneFailedTasks(source []FailedTask, err error) []FailedTask {
	if err != nil {
		return nil
	}
	return append([]FailedTask(nil), source...)
}

func cloneAttention(source []AttentionItem, err error) []AttentionItem {
	if err != nil {
		return nil
	}
	result := make([]AttentionItem, len(source))
	for index, item := range source {
		result[index] = item
		result[index].ReasonCodes = append([]string(nil), item.ReasonCodes...)
	}
	return result
}

func cloneRiskOperations(source []RiskOperation, err error) []RiskOperation {
	if err != nil {
		return nil
	}
	return append([]RiskOperation(nil), source...)
}
