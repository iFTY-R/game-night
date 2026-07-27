package operations

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Repository persists bounded operational state and reads real overview evidence.
type Repository interface {
	GetMaintenanceState(context.Context) (MaintenanceState, error)
	UpdateMaintenanceState(context.Context, MaintenanceChange) (MaintenanceState, error)
	UpsertServiceInstance(context.Context, ServiceInstance) (ServiceInstance, error)
	ListServiceInstances(context.Context, uint32) ([]ServiceInstance, error)
	UpsertMetricBucket(context.Context, MetricBucket) (MetricBucket, error)
	ListMetricBuckets(context.Context, MetricQuery) ([]MetricBucket, error)
	GetCacheGeneration(context.Context, CacheNamespace) (CacheGeneration, error)
	AdvanceCacheGeneration(context.Context, CacheNamespace, uint64, uuid.UUID, time.Time) (CacheGeneration, error)
	GetRetryReceipt(context.Context, uuid.UUID, string) (RetryReceipt, error)
	CreateRetryReceipt(context.Context, RetryReceipt) (RetryReceipt, error)
	ListBacklogs(context.Context, time.Time) ([]BacklogSummary, error)
	GetOverviewCounts(context.Context, time.Time, time.Time, time.Time) (OverviewCounts, error)
	ListFailedTasks(context.Context, uint32) ([]FailedTask, error)
}
