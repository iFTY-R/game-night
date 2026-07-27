package operations

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestClosedOperationsEnumsRejectUnknownValues(t *testing.T) {
	if HealthStatus("unknown").Valid() || ServiceKind("scheduler").Valid() || CacheNamespace("redis:* ").Valid() || RetryTaskKind("user_export").Valid() || BacklogKind("shell").Valid() || MetricName("arbitrary").Valid() {
		t.Fatal("an unknown operations value was accepted")
	}
}

func TestMaintenanceChangeRequiresReasonCASAndFuturePlan(t *testing.T) {
	now := time.Date(2026, time.July, 27, 8, 0, 0, 0, time.UTC)
	valid := MaintenanceChange{
		Enabled: true, Scope: MaintenanceUserMutations, Reason: "reviewed release window", PlannedEndAt: now.Add(time.Hour),
		ExpectedVersion: 1, ChangedByAdminID: uuid.New(), ChangedAt: now,
	}
	if !ValidMaintenanceChange(valid) {
		t.Fatal("valid maintenance change was rejected")
	}
	valid.Reason = ""
	if ValidMaintenanceChange(valid) {
		t.Fatal("enabled maintenance accepted an empty reason")
	}
	valid.Reason = "reviewed release window"
	valid.PlannedEndAt = now
	if ValidMaintenanceChange(valid) {
		t.Fatal("maintenance accepted a non-future planned end")
	}
}

func TestMetricBucketRequiresUTCAlignmentAndKnownMetric(t *testing.T) {
	start := time.Date(2026, time.July, 27, 8, 0, 0, 0, time.UTC)
	bucket := MetricBucket{Name: MetricActiveRooms, Width: BucketHour, Start: start, Value: 4, SampledAt: start.Add(5 * time.Minute), SourceWatermark: 8}
	if !ValidMetricBucket(bucket) {
		t.Fatal("aligned metric bucket was rejected")
	}
	bucket.Start = start.Add(time.Minute)
	if ValidMetricBucket(bucket) {
		t.Fatal("misaligned metric bucket was accepted")
	}
}

func TestServiceInstanceRejectsStaleOrUnboundedComponents(t *testing.T) {
	now := time.Date(2026, time.July, 27, 8, 0, 0, 0, time.UTC)
	instance := ServiceInstance{
		Kind: ServiceAPI, InstanceID: "api-local-1", BuildVersion: "development", StartedAt: now,
		LastHeartbeatAt: now.Add(time.Second), Status: HealthHealthy, Components: map[string]HealthStatus{"postgresql": HealthHealthy}, MaintenanceVersion: 1,
	}
	if !ValidServiceInstance(instance) {
		t.Fatal("valid service heartbeat was rejected")
	}
	instance.Components["raw error text"] = HealthHealthy
	if ValidServiceInstance(instance) {
		t.Fatal("unbounded component code was accepted")
	}
	delete(instance.Components, "raw error text")
	instance.Status = HealthStale
	if ValidServiceInstance(instance) {
		t.Fatal("reporter-supplied stale status was accepted")
	}
}
