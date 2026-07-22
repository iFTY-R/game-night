// Package loadtest exercises realtime capacity and dependency-failure boundaries without production credentials.
package loadtest

import (
	"errors"
	"math"
	"sort"
	"time"
)

const (
	// ReportSchemaVersion changes only when persisted CI evidence requires a reader migration.
	ReportSchemaVersion = 1
	// ScenarioName is stable so evidence from different commits can be compared without parsing command output.
	ScenarioName = "realtime-capacity-and-fault-gate"
	// maximumLoadUsers prevents a misconfigured CI invocation from exhausting the runner.
	maximumLoadUsers = 10_000
)

// ErrInvalidConfig rejects population or timing values that could make CI evidence unsafe or incomparable.
var ErrInvalidConfig = errors.New("invalid realtime load gate configuration")

// Targets defines bounded p95 gates for the in-process capacity fixture, not production network SLOs.
type Targets struct {
	FanoutP95           time.Duration
	HotSpectatorP95     time.Duration
	ReconnectP95        time.Duration
	RedisRecoveryP95    time.Duration
	PostgresRecoveryP95 time.Duration
	DrainingP95         time.Duration
	LeaseTransferP95    time.Duration
}

// Config controls the deterministic population and scaled reconciliation cadence used by the load gate.
type Config struct {
	Players           int
	Sessions          int
	HotSpectators     int
	ReconcileInterval time.Duration
	ProjectionTimeout time.Duration
	ScenarioTimeout   time.Duration
	Targets           Targets
}

// DefaultConfig represents the release gate required by Task 14.
func DefaultConfig() Config {
	return Config{
		Players:           1_000,
		Sessions:          125,
		HotSpectators:     500,
		ReconcileInterval: 20 * time.Millisecond,
		ProjectionTimeout: 2 * time.Second,
		ScenarioTimeout:   30 * time.Second,
		Targets: Targets{
			FanoutP95:           2 * time.Second,
			HotSpectatorP95:     2 * time.Second,
			ReconnectP95:        3 * time.Second,
			RedisRecoveryP95:    2 * time.Second,
			PostgresRecoveryP95: 3 * time.Second,
			DrainingP95:         2 * time.Second,
			LeaseTransferP95:    time.Second,
		},
	}
}

// Report is the secret-free JSON artifact retained by CI for release comparison.
type Report struct {
	SchemaVersion    int         `json:"schema_version"`
	Scenario         string      `json:"scenario"`
	Players          int         `json:"players"`
	Sessions         int         `json:"sessions"`
	HotSpectators    int         `json:"hot_spectators"`
	TotalSubscribers int         `json:"total_subscribers"`
	Metrics          Metrics     `json:"metrics"`
	Faults           FaultChecks `json:"faults"`
	Success          bool        `json:"success"`
}

// Metrics records latency distributions for each user-visible recovery boundary.
type Metrics struct {
	Fanout           LatencyMetric `json:"fanout"`
	HotSpectator     LatencyMetric `json:"hot_spectator"`
	Reconnect        LatencyMetric `json:"reconnect"`
	RedisRecovery    LatencyMetric `json:"redis_notification_loss_recovery"`
	PostgresRecovery LatencyMetric `json:"postgres_recovery"`
	Draining         LatencyMetric `json:"blue_green_draining"`
	LeaseTransfer    LatencyMetric `json:"lease_transfer"`
}

// LatencyMetric uses milliseconds so JSON evidence stays portable across Go duration encodings.
type LatencyMetric struct {
	Samples     int     `json:"samples"`
	P50Millis   float64 `json:"p50_ms"`
	P95Millis   float64 `json:"p95_ms"`
	P99Millis   float64 `json:"p99_ms"`
	MaxMillis   float64 `json:"max_ms"`
	TargetP95Ms float64 `json:"target_p95_ms"`
	Passed      bool    `json:"passed"`
}

// FaultChecks proves fail-closed behavior and recovery separately from latency thresholds.
type FaultChecks struct {
	RedisNotificationLossRecovered  bool `json:"redis_notification_loss_recovered"`
	RedisLeaseFailureClosed         bool `json:"redis_lease_failure_closed"`
	PostgresProjectionFailureClosed bool `json:"postgres_projection_failure_closed"`
	PostgresOwnershipFailureClosed  bool `json:"postgres_ownership_failure_closed"`
	LeaseTransferredWithNewEpoch    bool `json:"lease_transferred_with_new_epoch"`
	DrainingClosedAllSubscribers    bool `json:"draining_closed_all_subscribers"`
	UncommittedUpdates              int  `json:"uncommitted_updates"`
}

func (config Config) validate() error {
	if config.Players < 1 || config.Players > maximumLoadUsers || config.Sessions < 1 ||
		config.Sessions > config.Players || config.Players%config.Sessions != 0 ||
		config.HotSpectators < 1 || config.HotSpectators > maximumLoadUsers ||
		config.ReconcileInterval < 10*time.Millisecond || config.ReconcileInterval > time.Second ||
		config.ProjectionTimeout < 100*time.Millisecond || config.ProjectionTimeout > 10*time.Second ||
		config.ScenarioTimeout < time.Second || config.ScenarioTimeout > 5*time.Minute ||
		!config.Targets.valid() {
		return ErrInvalidConfig
	}
	return nil
}

func (targets Targets) valid() bool {
	values := [...]time.Duration{
		targets.FanoutP95,
		targets.HotSpectatorP95,
		targets.ReconnectP95,
		targets.RedisRecoveryP95,
		targets.PostgresRecoveryP95,
		targets.DrainingP95,
		targets.LeaseTransferP95,
	}
	for _, value := range values {
		if value <= 0 || value > time.Minute {
			return false
		}
	}
	return true
}

func (metrics Metrics) passed() bool {
	return metrics.Fanout.Passed && metrics.HotSpectator.Passed && metrics.Reconnect.Passed &&
		metrics.RedisRecovery.Passed && metrics.PostgresRecovery.Passed && metrics.Draining.Passed &&
		metrics.LeaseTransfer.Passed
}

func (faults FaultChecks) passed() bool {
	return faults.RedisNotificationLossRecovered && faults.RedisLeaseFailureClosed &&
		faults.PostgresProjectionFailureClosed && faults.PostgresOwnershipFailureClosed &&
		faults.LeaseTransferredWithNewEpoch && faults.DrainingClosedAllSubscribers &&
		faults.UncommittedUpdates == 0
}

func summarizeLatency(samples []time.Duration, target time.Duration) LatencyMetric {
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left] < sorted[right] })
	metric := LatencyMetric{Samples: len(sorted), TargetP95Ms: durationMillis(target)}
	if len(sorted) == 0 {
		return metric
	}
	metric.P50Millis = durationMillis(percentile(sorted, 0.50))
	metric.P95Millis = durationMillis(percentile(sorted, 0.95))
	metric.P99Millis = durationMillis(percentile(sorted, 0.99))
	metric.MaxMillis = durationMillis(sorted[len(sorted)-1])
	metric.Passed = percentile(sorted, 0.95) <= target
	return metric
}

func percentile(sorted []time.Duration, quantile float64) time.Duration {
	index := int(math.Ceil(quantile*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func durationMillis(value time.Duration) float64 {
	return math.Round(float64(value.Microseconds())) / 1_000
}
