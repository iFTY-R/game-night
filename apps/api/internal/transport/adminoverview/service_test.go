package adminoverview

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	adminv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1"
	domain "github.com/iFTY-R/game-night/platform/admin/operations"
	"github.com/iFTY-R/game-night/platform/audit"
)

func TestBoundedWireCountRejectsSignedOverflow(t *testing.T) {
	if value, err := boundedWireCount(math.MaxInt64); err != nil || value != math.MaxInt64 {
		t.Fatalf("boundedWireCount(max) = %d, %v", value, err)
	}
	if _, err := boundedWireCount(uint64(math.MaxInt64) + 1); !errors.Is(err, domain.ErrIntegrity) {
		t.Fatalf("boundedWireCount(overflow) error = %v, want %v", err, domain.ErrIntegrity)
	}
}

func TestRiskOperationToWireRequiresVerifiedKnownAction(t *testing.T) {
	now := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	operation := domain.RiskOperation{
		AuditEventID: uuid.New(), Action: audit.ActionAdminMaintenanceChanged, ActorAdminID: uuid.New(),
		TargetID: "user_mutations", Verified: true, OccurredAt: now,
	}
	wire, err := riskOperationToWire(operation)
	if err != nil || wire.GetAction() != "admin_maintenance_changed" || !wire.GetVerified() {
		t.Fatalf("riskOperationToWire() = %+v, %v", wire, err)
	}
	operation.Verified = false
	if _, err = riskOperationToWire(operation); !errors.Is(err, domain.ErrIntegrity) {
		t.Fatalf("unverified risk operation error = %v", err)
	}
}

func TestDependencyAndHealthMappingsAreExplicit(t *testing.T) {
	dependencies := map[domain.DependencyKind]adminv1.AdminDependencyKind{
		domain.DependencyPostgreSQL:         adminv1.AdminDependencyKind_ADMIN_DEPENDENCY_KIND_POSTGRESQL,
		domain.DependencyRedis:              adminv1.AdminDependencyKind_ADMIN_DEPENDENCY_KIND_REDIS,
		domain.DependencyExportResultStore:  adminv1.AdminDependencyKind_ADMIN_DEPENDENCY_KIND_EXPORT_RESULT_STORE,
		domain.DependencyCheckpointSink:     adminv1.AdminDependencyKind_ADMIN_DEPENDENCY_KIND_CHECKPOINT_SINK,
		domain.DependencyCheckpointProgress: adminv1.AdminDependencyKind_ADMIN_DEPENDENCY_KIND_CHECKPOINT_PROGRESS,
		domain.DependencyRealtimePresence:   adminv1.AdminDependencyKind_ADMIN_DEPENDENCY_KIND_REALTIME_PRESENCE,
		domain.DependencyRateLimiter:        adminv1.AdminDependencyKind_ADMIN_DEPENDENCY_KIND_RATE_LIMITER,
	}
	for input, expected := range dependencies {
		if actual := dependencyKindToWire(input); actual != expected {
			t.Fatalf("dependencyKindToWire(%q) = %v, want %v", input, actual, expected)
		}
	}
	if actual := dependencyKindToWire(domain.DependencyKind("future")); actual != adminv1.AdminDependencyKind_ADMIN_DEPENDENCY_KIND_UNSPECIFIED {
		t.Fatalf("unknown dependency = %v", actual)
	}

	health := map[domain.HealthStatus]adminv1.AdminHealthStatus{
		domain.HealthHealthy:     adminv1.AdminHealthStatus_ADMIN_HEALTH_STATUS_HEALTHY,
		domain.HealthDegraded:    adminv1.AdminHealthStatus_ADMIN_HEALTH_STATUS_DEGRADED,
		domain.HealthUnavailable: adminv1.AdminHealthStatus_ADMIN_HEALTH_STATUS_UNAVAILABLE,
		domain.HealthStale:       adminv1.AdminHealthStatus_ADMIN_HEALTH_STATUS_STALE,
	}
	for input, expected := range health {
		if actual := healthToWire(input); actual != expected {
			t.Fatalf("healthToWire(%q) = %v, want %v", input, actual, expected)
		}
	}
}
