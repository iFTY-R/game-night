package metrics

import (
	"fmt"
	"testing"
	"time"

	"github.com/iFTY-R/game-night/apps/api/internal/transport/proxy"
	"github.com/iFTY-R/game-night/platform/ratelimit"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRegistryRecordsOnlyBoundedLabels(t *testing.T) {
	prometheusRegistry := prometheus.NewRegistry()
	registry, err := New(prometheusRegistry, "/platform.identity.v1.IdentityService/GetCurrentIdentity")
	if err != nil {
		t.Fatal(err)
	}
	registry.ObserveRateLimit(ratelimit.OperationIdentityBootstrap, ratelimit.DimensionIP, ResultAllowed)
	registry.ObserveRPC("/platform.identity.v1.IdentityService/GetCurrentIdentity", "ok", 250*time.Millisecond)
	registry.ObserveProxyAnomaly(proxy.AnomalyConflictingHeaders)

	if got := testutil.ToFloat64(registry.rateLimit.WithLabelValues("identity.bootstrap", "allowed", "ip")); got != 1 {
		t.Fatalf("rate-limit metric = %v, want 1", got)
	}
	if got := testutil.ToFloat64(registry.rpc.WithLabelValues("/platform.identity.v1.IdentityService/GetCurrentIdentity", "ok")); got != 1 {
		t.Fatalf("RPC metric = %v, want 1", got)
	}
	if got := testutil.CollectAndCount(registry.rpcDuration); got != 1 {
		t.Fatalf("RPC duration series = %v, want 1", got)
	}
	if got := testutil.ToFloat64(registry.proxy.WithLabelValues("conflicting_headers")); got != 1 {
		t.Fatalf("proxy metric = %v, want 1", got)
	}
	assertAllowedLabelNames(t, prometheusRegistry)
}

func TestRegistryFoldsUntrustedValuesIntoOneSeries(t *testing.T) {
	registry, err := New(prometheus.NewRegistry(), "known.operation")
	if err != nil {
		t.Fatal(err)
	}
	for index := range 100 {
		registry.ObserveRPC(fmt.Sprintf("attacker-operation-%d", index), fmt.Sprintf("attacker-result-%d", index), -time.Second)
	}
	if got := testutil.ToFloat64(registry.rpc.WithLabelValues("unknown", "unknown")); got != 100 {
		t.Fatalf("folded RPC metric = %v, want 100", got)
	}
	registry.ObserveRateLimit(ratelimit.Operation("invalid"), ratelimit.Dimension("invalid"), "invalid")
	if got := testutil.ToFloat64(registry.rateLimit.WithLabelValues("unknown", "unknown", "unknown")); got != 1 {
		t.Fatalf("folded rate-limit metric = %v, want 1", got)
	}
}

func assertAllowedLabelNames(t testing.TB, registry *prometheus.Registry) {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]struct{}{"operation": {}, "result": {}, "dimension": {}}
	for _, family := range families {
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				if _, ok := allowed[label.GetName()]; !ok {
					t.Fatalf("metric %s uses forbidden label %q", family.GetName(), label.GetName())
				}
			}
		}
	}
}
