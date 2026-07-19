// Package metrics exposes bounded Prometheus observations for API security decisions.
package metrics

import (
	"errors"
	"sync"
	"time"

	"github.com/iFTY-R/game-night/apps/api/internal/transport/proxy"
	"github.com/iFTY-R/game-night/platform/ratelimit"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// ResultAllowed identifies a rate-limit token consumed successfully.
	ResultAllowed = "allowed"
	// ResultRejected identifies an authoritative rate-limit capacity denial.
	ResultRejected = "rejected"
	// ResultUnavailable identifies a fail-closed rate-limit dependency failure.
	ResultUnavailable = "unavailable"
	// ResultUnknown folds unreviewed values into one bounded metric and log label.
	ResultUnknown = "unknown"
)

// Observer is the minimal interface consumed by transport decorators and proxy resolution.
type Observer interface {
	ObserveRateLimit(ratelimit.Operation, ratelimit.Dimension, string)
	ObserveRPC(string, string, time.Duration)
	ObserveProxyAnomaly(proxy.Anomaly)
}

// Registry owns process-local observations and an allowlist for RPC operation labels.
type Registry struct {
	rateLimit   *prometheus.CounterVec
	rpc         *prometheus.CounterVec
	rpcDuration *prometheus.HistogramVec
	proxy       *prometheus.CounterVec
	rpcOps      map[string]struct{}
	mu          sync.RWMutex
}

// New registers bounded counters under the supplied registry and rejects duplicate metric ownership.
func New(registerer prometheus.Registerer, rpcOperations ...string) (*Registry, error) {
	if registerer == nil || len(rpcOperations) == 0 {
		return nil, errors.New("invalid metrics configuration")
	}
	ops := make(map[string]struct{}, len(rpcOperations))
	for _, operation := range rpcOperations {
		if !validLabelValue(operation) {
			return nil, errors.New("invalid metrics configuration")
		}
		if _, exists := ops[operation]; exists {
			return nil, errors.New("invalid metrics configuration")
		}
		ops[operation] = struct{}{}
	}
	registry := &Registry{
		rateLimit: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "game_night", Subsystem: "ratelimit", Name: "decisions_total",
			Help: "Rate-limit decisions by reviewed operation and bucket dimension.",
		}, []string{"operation", "result", "dimension"}),
		rpc: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "game_night", Subsystem: "rpc", Name: "requests_total",
			Help: "Connect RPC outcomes by configured operation.",
		}, []string{"operation", "result"}),
		rpcDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "game_night", Subsystem: "rpc", Name: "duration_seconds",
			Help: "Connect RPC duration by configured operation and outcome.",
		}, []string{"operation", "result"}),
		proxy: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "game_night", Subsystem: "proxy", Name: "anomalies_total",
			Help: "Ignored forwarding metadata by bounded security reason.",
		}, []string{"result"}),
		rpcOps: ops,
	}
	for _, collector := range []prometheus.Collector{registry.rateLimit, registry.rpc, registry.rpcDuration, registry.proxy} {
		if err := registerer.Register(collector); err != nil {
			return nil, errors.New("invalid metrics registration")
		}
	}
	return registry, nil
}

// ObserveRateLimit records only closed domain labels; malformed values are folded into unknown.
func (registry *Registry) ObserveRateLimit(operation ratelimit.Operation, dimension ratelimit.Dimension, result string) {
	if registry == nil {
		return
	}
	if !operation.Valid() {
		operation = ratelimit.Operation("unknown")
	}
	if !dimension.Valid() {
		dimension = ratelimit.Dimension("unknown")
	}
	result = boundedResult(result)
	registry.rateLimit.WithLabelValues(operation.String(), result, dimension.String()).Inc()
}

// ObserveRPC records a configured RPC operation, bounded outcome, and non-negative duration.
func (registry *Registry) ObserveRPC(operation, result string, duration time.Duration) {
	if registry == nil {
		return
	}
	registry.mu.RLock()
	_, known := registry.rpcOps[operation]
	registry.mu.RUnlock()
	if !known {
		operation = "unknown"
	}
	result = boundedRPCResult(result)
	if duration < 0 {
		duration = 0
	}
	registry.rpc.WithLabelValues(operation, result).Inc()
	registry.rpcDuration.WithLabelValues(operation, result).Observe(duration.Seconds())
}

// ObserveProxyAnomaly records a fixed forwarding-header reason without retaining network addresses.
func (registry *Registry) ObserveProxyAnomaly(anomaly proxy.Anomaly) {
	if registry == nil {
		return
	}
	result := ResultUnknown
	if anomaly.Valid() {
		result = anomaly.String()
	}
	registry.proxy.WithLabelValues(result).Inc()
}

func boundedResult(value string) string {
	switch value {
	case ResultAllowed, ResultRejected, ResultUnavailable:
		return value
	default:
		return ResultUnknown
	}
}

func boundedRPCResult(value string) string {
	switch value {
	case "ok", "canceled", "unknown", "invalid_argument", "deadline_exceeded", "not_found",
		"already_exists", "permission_denied", "resource_exhausted", "failed_precondition", "aborted",
		"out_of_range", "unimplemented", "internal", "unavailable", "data_loss", "unauthenticated":
		return value
	default:
		return ResultUnknown
	}
}

func validLabelValue(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

var _ Observer = (*Registry)(nil)
