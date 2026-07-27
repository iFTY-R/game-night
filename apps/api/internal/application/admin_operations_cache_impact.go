package application

import (
	"context"
	"math"
	"time"

	adminoperations "github.com/iFTY-R/game-night/platform/admin/operations"
	"github.com/iFTY-R/game-night/platform/clock"
)

const adminOverviewImpactWindow = 30 * 24 * time.Hour

type adminOperationsMetricReader interface {
	ListMetricBuckets(context.Context, adminoperations.MetricQuery) ([]adminoperations.MetricBucket, error)
}

type adminOperationsSnapshotReader interface {
	GetSnapshot(context.Context) (adminoperations.OperationsSnapshot, error)
}

type adminOperationsPresenceReader interface {
	ReadPresenceSummary(context.Context) (adminoperations.PresenceSummary, error)
}

// adminCacheImpactReader reports real, bounded ownership estimates for the three closed projection namespaces.
type adminCacheImpactReader struct {
	metrics    adminOperationsMetricReader
	operations adminOperationsSnapshotReader
	presence   adminOperationsPresenceReader
	clock      clock.Clock
}

func newAdminCacheImpactReader(metrics adminOperationsMetricReader, operations adminOperationsSnapshotReader, presence adminOperationsPresenceReader, source clock.Clock) (*adminCacheImpactReader, error) {
	if metrics == nil || operations == nil || presence == nil || source == nil {
		return nil, adminoperations.ErrInvalidInput
	}
	return &adminCacheImpactReader{metrics: metrics, operations: operations, presence: presence, clock: source}, nil
}

// EstimateCacheEntries never accepts keys or patterns; each branch reads only its owned bounded source.
func (reader *adminCacheImpactReader) EstimateCacheEntries(ctx context.Context, namespace adminoperations.CacheNamespace) (uint64, error) {
	if reader == nil || ctx == nil || reader.clock == nil {
		return 0, adminoperations.ErrInvalidInput
	}
	switch namespace {
	case adminoperations.CacheOverviewProjection:
		end := reader.clock.Now().UTC().Truncate(time.Hour).Add(time.Hour)
		buckets, err := reader.metrics.ListMetricBuckets(ctx, adminoperations.MetricQuery{
			Names: []adminoperations.MetricName{
				adminoperations.MetricOnlineUsers, adminoperations.MetricActiveRooms, adminoperations.MetricRunningGames,
				adminoperations.MetricNewUsers, adminoperations.MetricSuspendedUsers, adminoperations.MetricUnsuspendedUsers,
				adminoperations.MetricAbnormalTerminations, adminoperations.MetricEmergencyRepairs,
			},
			Width: adminoperations.BucketHour, WindowStart: end.Add(-adminOverviewImpactWindow), WindowEnd: end,
			Limit: adminoperations.MaximumMetricBuckets,
		})
		if err != nil {
			return 0, err
		}
		return uint64(len(buckets)), nil
	case adminoperations.CacheOperationsProbes:
		snapshot, err := reader.operations.GetSnapshot(ctx)
		if err != nil {
			return 0, err
		}
		// The presence aggregate is one probe entry alongside service, dependency, and backlog rows.
		return uint64(len(snapshot.Services) + len(snapshot.Dependencies) + len(snapshot.Backlogs) + 1), nil
	case adminoperations.CacheRealtimePresence:
		presence, err := reader.presence.ReadPresenceSummary(ctx)
		if err != nil {
			return 0, err
		}
		if presence.ActiveConnections > math.MaxUint64-presence.OnlineUsers {
			return 0, adminoperations.ErrIntegrity
		}
		return presence.ActiveConnections + presence.OnlineUsers, nil
	default:
		return 0, adminoperations.ErrInvalidInput
	}
}

var _ adminoperations.CacheImpactReader = (*adminCacheImpactReader)(nil)
