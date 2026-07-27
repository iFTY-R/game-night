// Package operations owns bounded service health, maintenance, cache, retry, and overview models.
package operations

import (
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/audit"
)

const (
	// MaximumServiceInstances bounds one snapshot independently of table growth.
	MaximumServiceInstances = 200
	// MaximumMetricBuckets bounds one trend query and therefore response memory.
	MaximumMetricBuckets = 2048
	// MaximumOverviewFailedTasks bounds attention rows on the overview page.
	MaximumOverviewFailedTasks = 50
	// MaximumOverviewAttentionItems bounds actionable room and game anomaly summaries.
	MaximumOverviewAttentionItems = 50
	// MaximumOverviewRiskOperations bounds recent verified high-risk audit metadata.
	MaximumOverviewRiskOperations = 20
)

// HealthStatus is the only status vocabulary that can cross the admin boundary.
type HealthStatus string

const (
	HealthHealthy     HealthStatus = "healthy"
	HealthDegraded    HealthStatus = "degraded"
	HealthUnavailable HealthStatus = "unavailable"
	HealthStale       HealthStatus = "stale"
)

// Valid reports whether a status is safe to persist or derive for display.
func (status HealthStatus) Valid() bool {
	return status == HealthHealthy || status == HealthDegraded || status == HealthUnavailable || status == HealthStale
}

// ServiceKind identifies one deployable process family.
type ServiceKind string

const (
	ServiceAPI      ServiceKind = "api"
	ServiceEdge     ServiceKind = "edge"
	ServiceRealtime ServiceKind = "realtime"
	ServiceWorker   ServiceKind = "worker"
)

// Valid reports whether the process family is part of the supported topology.
func (kind ServiceKind) Valid() bool {
	return kind == ServiceAPI || kind == ServiceEdge || kind == ServiceRealtime || kind == ServiceWorker
}

// MaintenanceScope is intentionally closed to user-facing mutations.
type MaintenanceScope string

const MaintenanceUserMutations MaintenanceScope = "user_mutations"

// CacheNamespace identifies one safe-to-rebuild projection.
type CacheNamespace string

const (
	CacheOverviewProjection CacheNamespace = "admin_overview_projection"
	CacheOperationsProbes   CacheNamespace = "admin_operations_probes"
	CacheRealtimePresence   CacheNamespace = "realtime_presence_projection"
)

// Valid reports whether a namespace can be refreshed without accepting a Redis key or pattern.
func (namespace CacheNamespace) Valid() bool {
	return namespace == CacheOverviewProjection || namespace == CacheOperationsProbes || namespace == CacheRealtimePresence
}

// RetryTaskKind identifies the two durable job families eligible for manual retry.
type RetryTaskKind string

const (
	RetryUserBatch   RetryTaskKind = "user_batch"
	RetryUserErasure RetryTaskKind = "user_erasure"
)

// Valid reports whether a task kind has a reviewed retry state machine.
func (kind RetryTaskKind) Valid() bool { return kind == RetryUserBatch || kind == RetryUserErasure }

// BacklogKind identifies one bounded queue summary.
type BacklogKind string

const (
	BacklogAuditOutbox   BacklogKind = "audit_outbox"
	BacklogRoomOutbox    BacklogKind = "room_outbox"
	BacklogRealtimeTimer BacklogKind = "realtime_timer"
	BacklogUserBatch     BacklogKind = "user_batch"
	BacklogUserErasure   BacklogKind = "user_erasure"
)

// Valid reports whether the backlog summary belongs to a known durable mechanism.
func (kind BacklogKind) Valid() bool {
	return kind == BacklogAuditOutbox || kind == BacklogRoomOutbox || kind == BacklogRealtimeTimer || kind == BacklogUserBatch || kind == BacklogUserErasure
}

// DependencyKind identifies one reviewed infrastructure dependency.
type DependencyKind string

const (
	DependencyPostgreSQL         DependencyKind = "postgresql"
	DependencyRedis              DependencyKind = "redis"
	DependencyExportResultStore  DependencyKind = "export_result_store"
	DependencyCheckpointSink     DependencyKind = "checkpoint_sink"
	DependencyCheckpointProgress DependencyKind = "checkpoint_progress"
	DependencyRealtimePresence   DependencyKind = "realtime_presence"
	DependencyRateLimiter        DependencyKind = "rate_limiter"
)

// Valid reports whether the dependency has a bounded administrator-facing meaning.
func (kind DependencyKind) Valid() bool {
	switch kind {
	case DependencyPostgreSQL, DependencyRedis, DependencyExportResultStore, DependencyCheckpointSink,
		DependencyCheckpointProgress, DependencyRealtimePresence, DependencyRateLimiter:
		return true
	default:
		return false
	}
}

// MetricName identifies a persisted operational metric.
type MetricName string

const (
	MetricOnlineUsers          MetricName = "online_users"
	MetricActiveRooms          MetricName = "active_rooms"
	MetricRunningGames         MetricName = "running_games"
	MetricNewUsers             MetricName = "new_users"
	MetricSuspendedUsers       MetricName = "suspended_users"
	MetricUnsuspendedUsers     MetricName = "unsuspended_users"
	MetricAbnormalTerminations MetricName = "abnormal_terminations"
	MetricEmergencyRepairs     MetricName = "emergency_repairs"
)

// Valid reports whether the metric has a stable meaning and collector.
func (name MetricName) Valid() bool {
	switch name {
	case MetricOnlineUsers, MetricActiveRooms, MetricRunningGames, MetricNewUsers,
		MetricSuspendedUsers, MetricUnsuspendedUsers, MetricAbnormalTerminations, MetricEmergencyRepairs:
		return true
	default:
		return false
	}
}

// BucketWidth is one UTC-aligned aggregation width.
type BucketWidth string

const (
	BucketHour BucketWidth = "hour"
	BucketDay  BucketWidth = "day"
)

// Duration returns the exact UTC alignment unit for this bucket.
func (width BucketWidth) Duration() (time.Duration, bool) {
	switch width {
	case BucketHour:
		return time.Hour, true
	case BucketDay:
		return 24 * time.Hour, true
	default:
		return 0, false
	}
}

// MaintenanceState is the current versioned mutation-admission authority.
type MaintenanceState struct {
	Enabled          bool
	Scope            MaintenanceScope
	Reason           string
	PlannedEndAt     time.Time
	Version          uint64
	ChangedByAdminID uuid.UUID
	ChangedAt        time.Time
}

// MaintenanceChange carries one CAS-protected state transition.
type MaintenanceChange struct {
	Enabled          bool
	Scope            MaintenanceScope
	Reason           string
	PlannedEndAt     time.Time
	ExpectedVersion  uint64
	ChangedByAdminID uuid.UUID
	ChangedAt        time.Time
}

// ServiceInstance is the last bounded heartbeat for one concrete process.
type ServiceInstance struct {
	Kind               ServiceKind
	InstanceID         string
	BuildVersion       string
	StartedAt          time.Time
	LastHeartbeatAt    time.Time
	Status             HealthStatus
	Components         map[string]HealthStatus
	MaintenanceVersion uint64
}

// MetricBucket is an idempotently recomputable UTC metric interval.
type MetricBucket struct {
	Name            MetricName
	Width           BucketWidth
	Start           time.Time
	Value           uint64
	SampledAt       time.Time
	SourceWatermark uint64
}

// MetricQuery bounds one ordered metric-window read.
type MetricQuery struct {
	Names       []MetricName
	Width       BucketWidth
	WindowStart time.Time
	WindowEnd   time.Time
	Limit       uint32
}

// CacheGeneration is the durable monotonic invalidation marker for one projection.
type CacheGeneration struct {
	Namespace        CacheNamespace
	Generation       uint64
	UpdatedByAdminID uuid.UUID
	UpdatedAt        time.Time
}

// RetryReceipt preserves the first committed result for an idempotent manual retry.
type RetryReceipt struct {
	ActorAdminID        uuid.UUID
	OperationID         string
	RequestDigest       [32]byte
	TaskKind            RetryTaskKind
	TaskID              uuid.UUID
	ExpectedTaskVersion uint64
	Outcome             string
	TaskVersion         uint64
	ManualRetryCount    uint32
	TaskState           string
	OriginalErrorCode   string
	AuditEventID        uuid.UUID
	CompletedAt         time.Time
}

// BacklogSummary contains aggregate counts only and never a task payload.
type BacklogSummary struct {
	Kind            BacklogKind
	Pending         uint64
	Running         uint64
	Failed          uint64
	OldestPendingAt time.Time
	SampledAt       time.Time
}

// DependencyHealth reports one sampled dependency without retaining its underlying error.
type DependencyHealth struct {
	Kind       DependencyKind
	Status     HealthStatus
	SampledAt  time.Time
	FreshUntil time.Time
}

// PresenceSummary is an aggregate Redis projection and never acts as an identity authority.
type PresenceSummary struct {
	Status            HealthStatus
	ActiveConnections uint64
	OnlineUsers       uint64
	SampledAt         time.Time
	FreshUntil        time.Time
}

// OperationsSnapshot is one bounded, partially degradable view of current process and queue state.
type OperationsSnapshot struct {
	Services     []ServiceInstance
	Dependencies []DependencyHealth
	Presence     PresenceSummary
	Backlogs     []BacklogSummary
	Maintenance  MaintenanceState
	SampledAt    time.Time
	FreshUntil   time.Time
}

// AvailabilityReason distinguishes a real zero from evidence that could not be read safely.
type AvailabilityReason string

const (
	AvailabilityNone              AvailabilityReason = "none"
	AvailabilitySourceUnavailable AvailabilityReason = "source_unavailable"
	AvailabilitySourceStale       AvailabilityReason = "source_stale"
	AvailabilityWindowUnsupported AvailabilityReason = "window_unsupported"
)

// MetricValue is one current or windowed overview value with explicit source freshness.
type MetricValue struct {
	Name        MetricName
	Value       uint64
	Unavailable AvailabilityReason
	WindowStart time.Time
	WindowEnd   time.Time
	SampledAt   time.Time
	FreshUntil  time.Time
}

// TrendPoint is one persisted bucket; bucket end is derived from the reviewed width.
type TrendPoint struct {
	Start     time.Time
	End       time.Time
	Value     uint64
	SampledAt time.Time
}

// TrendSeries keeps one metric and one bucket width separate from unavailable source state.
type TrendSeries struct {
	Name        MetricName
	Width       BucketWidth
	Points      []TrendPoint
	Unavailable AvailabilityReason
	FreshUntil  time.Time
}

// OverviewQuery selects one bounded UTC window and one pre-aggregated granularity.
type OverviewQuery struct {
	WindowStart time.Time
	WindowEnd   time.Time
	Width       BucketWidth
}

// OverviewSnapshot contains only real source evidence and explicit partial-source status.
type OverviewSnapshot struct {
	Metrics              []MetricValue
	Trends               []TrendSeries
	Attention            []AttentionItem
	Dependencies         []DependencyHealth
	HighRiskOperations   []RiskOperation
	FailedTasks          []FailedTask
	FailedTasksAvailable bool
	WindowStart          time.Time
	WindowEnd            time.Time
	SampledAt            time.Time
	FreshUntil           time.Time
}

// OverviewCounts contains authoritative PostgreSQL snapshot values for one window.
type OverviewCounts struct {
	ActiveRooms          uint64
	RunningGames         uint64
	NewUsers             uint64
	SuspendedUsers       uint64
	UnsuspendedUsers     uint64
	AbnormalTerminations uint64
	EmergencyRepairs     uint64
	WindowStart          time.Time
	WindowEnd            time.Time
	SampledAt            time.Time
}

// AttentionKind identifies an anomalous room or running game that an operator can inspect.
type AttentionKind string

const (
	AttentionRoom AttentionKind = "room"
	AttentionGame AttentionKind = "game"
)

// Valid reports whether an attention item links to a supported management detail view.
func (kind AttentionKind) Valid() bool { return kind == AttentionRoom || kind == AttentionGame }

// AttentionItem is bounded, non-sensitive anomaly metadata derived from authoritative room/game links.
type AttentionItem struct {
	Kind        AttentionKind
	ResourceID  uuid.UUID
	RoomID      uuid.UUID
	StatusCode  string
	ReasonCodes []string
	ObservedAt  time.Time
}

// RiskOperation contains only verified, redacted audit metadata suitable for the overview page.
type RiskOperation struct {
	AuditEventID uuid.UUID
	Action       audit.Action
	ActorAdminID uuid.UUID
	TargetID     string
	Verified     bool
	OccurredAt   time.Time
}

// FailedTask is redacted durable job metadata suitable for an operator attention list.
type FailedTask struct {
	Kind            RetryTaskKind
	ID              uuid.UUID
	State           string
	StableErrorCode string
	Attempts        uint32
	Version         uint64
	UpdatedAt       time.Time
}

var componentCodePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)

// ValidServiceInstance validates heartbeat content before it can overwrite a newer row.
func ValidServiceInstance(instance ServiceInstance) bool {
	if !instance.Kind.Valid() || strings.TrimSpace(instance.InstanceID) != instance.InstanceID || instance.InstanceID == "" || len(instance.InstanceID) > 128 ||
		strings.TrimSpace(instance.BuildVersion) != instance.BuildVersion || instance.BuildVersion == "" || len(instance.BuildVersion) > 128 ||
		instance.StartedAt.IsZero() || instance.LastHeartbeatAt.Before(instance.StartedAt) || !instance.Status.Valid() || instance.Status == HealthStale ||
		instance.MaintenanceVersion == 0 || len(instance.Components) > 32 {
		return false
	}
	for code, status := range instance.Components {
		if !componentCodePattern.MatchString(code) || len(code) > 64 || !status.Valid() || status == HealthStale {
			return false
		}
	}
	return true
}

// ValidMaintenanceChange validates business constraints before a CAS update reaches PostgreSQL.
func ValidMaintenanceChange(change MaintenanceChange) bool {
	reason := strings.TrimSpace(change.Reason)
	return change.Scope == MaintenanceUserMutations && change.ExpectedVersion > 0 && change.ChangedByAdminID != uuid.Nil && !change.ChangedAt.IsZero() &&
		len(change.Reason) <= 512 && (!change.Enabled || (reason == change.Reason && reason != "")) &&
		(change.PlannedEndAt.IsZero() || change.PlannedEndAt.After(change.ChangedAt))
}

// ValidMetricBucket protects UTC alignment and monotonic watermark semantics.
func ValidMetricBucket(bucket MetricBucket) bool {
	width, ok := bucket.Width.Duration()
	return bucket.Name.Valid() && ok && !bucket.Start.IsZero() && bucket.Start.Equal(bucket.Start.UTC()) &&
		bucket.Start.Unix()%int64(width/time.Second) == 0 && !bucket.SampledAt.Before(bucket.Start)
}
