package operations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/clock"
)

type overviewRepositoryStub struct {
	counts       OverviewCounts
	buckets      []MetricBucket
	attention    []AttentionItem
	failedTasks  []FailedTask
	countsErr    error
	bucketsErr   error
	attentionErr error
	failedErr    error
}

func (repository *overviewRepositoryStub) GetOverviewCounts(context.Context, time.Time, time.Time, time.Time) (OverviewCounts, error) {
	return repository.counts, repository.countsErr
}

func (repository *overviewRepositoryStub) ListMetricBuckets(context.Context, MetricQuery) ([]MetricBucket, error) {
	return repository.buckets, repository.bucketsErr
}

func (repository *overviewRepositoryStub) ListFailedTasks(context.Context, uint32) ([]FailedTask, error) {
	return repository.failedTasks, repository.failedErr
}

func (repository *overviewRepositoryStub) ListAttentionItems(context.Context, uint32) ([]AttentionItem, error) {
	return repository.attention, repository.attentionErr
}

type overviewAuditReaderStub struct {
	operations []RiskOperation
	err        error
}

func (reader overviewAuditReaderStub) ListRecentHighRiskOperations(context.Context, time.Time, time.Time, uint32) ([]RiskOperation, error) {
	return reader.operations, reader.err
}

type presenceReaderStub struct {
	summary PresenceSummary
	err     error
}

func (reader presenceReaderStub) ReadPresenceSummary(context.Context) (PresenceSummary, error) {
	return reader.summary, reader.err
}

type operationsSnapshotReaderStub struct {
	snapshot OperationsSnapshot
	err      error
}

func (reader operationsSnapshotReaderStub) GetSnapshot(context.Context) (OperationsSnapshot, error) {
	return reader.snapshot, reader.err
}

func TestOverviewServiceAggregatesRealCountsBucketsAndPresence(t *testing.T) {
	start := time.Date(2026, time.July, 27, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	now := end.Add(time.Minute)
	repository := &overviewRepositoryStub{
		counts:      OverviewCounts{ActiveRooms: 4, RunningGames: 2, NewUsers: 9, SuspendedUsers: 1, UnsuspendedUsers: 3, AbnormalTerminations: 2, EmergencyRepairs: 1, WindowStart: start, WindowEnd: end, SampledAt: now},
		buckets:     []MetricBucket{{Name: MetricActiveRooms, Width: BucketHour, Start: start, Value: 3, SampledAt: now}},
		attention:   []AttentionItem{{Kind: AttentionRoom, ResourceID: uuid.New(), RoomID: uuid.New(), StatusCode: "playing", ReasonCodes: []string{"room.session_missing"}, ObservedAt: now}},
		failedTasks: []FailedTask{{Kind: RetryUserBatch, ID: uuid.New(), State: "failed", StableErrorCode: "admin.batch.failed", Version: 2, UpdatedAt: now}},
	}
	risk := RiskOperation{AuditEventID: uuid.New(), Action: audit.ActionAdminMaintenanceChanged, ActorAdminID: uuid.New(), TargetID: "user_mutations", Verified: true, OccurredAt: now}
	service, err := NewOverviewService(OverviewServiceConfig{
		Repository: repository,
		Presence:   presenceReaderStub{summary: PresenceSummary{Status: HealthHealthy, ActiveConnections: 7, OnlineUsers: 5, SampledAt: now, FreshUntil: now.Add(time.Minute)}},
		Operations: operationsSnapshotReaderStub{snapshot: OperationsSnapshot{Dependencies: []DependencyHealth{{Kind: DependencyPostgreSQL, Status: HealthHealthy}}}},
		Audit:      overviewAuditReaderStub{operations: []RiskOperation{risk}},
		Clock:      clock.NewFake(now),
	})
	if err != nil {
		t.Fatalf("NewOverviewService() error = %v", err)
	}

	snapshot, err := service.GetOverview(context.Background(), OverviewQuery{WindowStart: start, WindowEnd: end, Width: BucketHour})
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}
	assertMetricValue(t, snapshot.Metrics, MetricOnlineUsers, 5, AvailabilityNone)
	assertMetricValue(t, snapshot.Metrics, MetricActiveRooms, 4, AvailabilityNone)
	assertMetricValue(t, snapshot.Metrics, MetricRunningGames, 2, AvailabilityNone)
	assertMetricValue(t, snapshot.Metrics, MetricNewUsers, 9, AvailabilityNone)
	assertMetricValue(t, snapshot.Metrics, MetricSuspendedUsers, 1, AvailabilityNone)
	assertMetricValue(t, snapshot.Metrics, MetricUnsuspendedUsers, 3, AvailabilityNone)
	assertMetricValue(t, snapshot.Metrics, MetricAbnormalTerminations, 2, AvailabilityNone)
	assertMetricValue(t, snapshot.Metrics, MetricEmergencyRepairs, 1, AvailabilityNone)
	if len(snapshot.Trends) != len(overviewMetricOrder) || len(snapshot.Trends[1].Points) != 1 || snapshot.Trends[1].Points[0].Value != 3 {
		t.Fatalf("unexpected trend projection: %+v", snapshot.Trends)
	}
	if !snapshot.FailedTasksAvailable || len(snapshot.FailedTasks) != 1 || len(snapshot.Dependencies) != 1 || len(snapshot.Attention) != 1 || len(snapshot.HighRiskOperations) != 1 {
		t.Fatalf("unexpected attention sources: %+v", snapshot)
	}
}

func TestOverviewServiceKeepsPartialSourceFailuresExplicit(t *testing.T) {
	start := time.Date(2026, time.July, 27, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	now := end
	service, err := NewOverviewService(OverviewServiceConfig{
		Repository: &overviewRepositoryStub{countsErr: errors.New("counts"), bucketsErr: errors.New("buckets"), attentionErr: errors.New("attention"), failedErr: errors.New("tasks")},
		Presence:   presenceReaderStub{summary: PresenceSummary{Status: HealthHealthy, OnlineUsers: 8, SampledAt: now.Add(-time.Minute), FreshUntil: now.Add(-time.Second)}},
		Operations: operationsSnapshotReaderStub{err: errors.New("operations")},
		Audit:      overviewAuditReaderStub{err: errors.New("audit")},
		Clock:      clock.NewFake(now),
	})
	if err != nil {
		t.Fatalf("NewOverviewService() error = %v", err)
	}

	snapshot, err := service.GetOverview(context.Background(), OverviewQuery{WindowStart: start, WindowEnd: end, Width: BucketHour})
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}
	assertMetricValue(t, snapshot.Metrics, MetricOnlineUsers, 8, AvailabilitySourceStale)
	assertMetricValue(t, snapshot.Metrics, MetricActiveRooms, 0, AvailabilitySourceUnavailable)
	if snapshot.Trends[0].Unavailable != AvailabilitySourceUnavailable || snapshot.FailedTasksAvailable || snapshot.Dependencies != nil || snapshot.Attention != nil || snapshot.HighRiskOperations != nil {
		t.Fatalf("partial failures were hidden: %+v", snapshot)
	}
}

func TestOverviewServiceRejectsUnalignedOrOversizedWindows(t *testing.T) {
	start := time.Date(2026, time.July, 27, 0, 0, 0, 0, time.UTC)
	service, err := NewOverviewService(OverviewServiceConfig{
		Repository: &overviewRepositoryStub{}, Presence: presenceReaderStub{}, Operations: operationsSnapshotReaderStub{}, Audit: overviewAuditReaderStub{}, Clock: clock.NewFake(start),
	})
	if err != nil {
		t.Fatalf("NewOverviewService() error = %v", err)
	}
	for _, query := range []OverviewQuery{
		{WindowStart: start.Add(time.Minute), WindowEnd: start.Add(time.Hour), Width: BucketHour},
		{WindowStart: start, WindowEnd: start.Add(MaximumOverviewWindow + time.Hour), Width: BucketHour},
		{WindowStart: start, WindowEnd: start.Add(time.Hour), Width: BucketWidth("week")},
	} {
		if _, err := service.GetOverview(context.Background(), query); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("GetOverview(%+v) error = %v, want %v", query, err, ErrInvalidInput)
		}
	}
}

func assertMetricValue(t testing.TB, metrics []MetricValue, name MetricName, value uint64, availability AvailabilityReason) {
	t.Helper()
	for _, metric := range metrics {
		if metric.Name == name {
			if metric.Value != value || metric.Unavailable != availability {
				t.Fatalf("metric %q = (%d, %q), want (%d, %q)", name, metric.Value, metric.Unavailable, value, availability)
			}
			return
		}
	}
	t.Fatalf("metric %q was not returned", name)
}
