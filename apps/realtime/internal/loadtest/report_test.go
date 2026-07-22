package loadtest

import (
	"context"
	"testing"
	"time"
)

func TestRunSmallScenarioCoversCapacityAndFaultMatrix(t *testing.T) {
	config := DefaultConfig()
	config.Players = 16
	config.Sessions = 2
	config.HotSpectators = 8
	config.ScenarioTimeout = 10 * time.Second

	report, err := Run(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Success || report.TotalSubscribers != 24 || !report.Metrics.passed() || !report.Faults.passed() {
		t.Fatalf("unexpected load report: %+v", report)
	}
	if report.Metrics.Fanout.Samples != 24 || report.Metrics.HotSpectator.Samples != 8 ||
		report.Metrics.Reconnect.Samples != 24 || report.Metrics.RedisRecovery.Samples != 24 ||
		report.Metrics.PostgresRecovery.Samples != 24 || report.Metrics.Draining.Samples != 24 ||
		report.Metrics.LeaseTransfer.Samples != 1 {
		t.Fatalf("unexpected metric sample counts: %+v", report.Metrics)
	}
}

func TestConfigRejectsUnboundedOrUnevenPopulation(t *testing.T) {
	config := DefaultConfig()
	config.Players = 17
	config.Sessions = 2
	if _, err := Run(context.Background(), config); err != ErrInvalidConfig {
		t.Fatalf("Run() error = %v, want ErrInvalidConfig", err)
	}

	config = DefaultConfig()
	config.HotSpectators = maximumLoadUsers + 1
	if _, err := Run(context.Background(), config); err != ErrInvalidConfig {
		t.Fatalf("Run() error = %v, want ErrInvalidConfig", err)
	}
}

func TestSummarizeLatencyUsesNearestRankP95(t *testing.T) {
	samples := make([]time.Duration, 100)
	for index := range samples {
		samples[index] = time.Duration(index+1) * time.Millisecond
	}
	metric := summarizeLatency(samples, 95*time.Millisecond)
	if metric.P50Millis != 50 || metric.P95Millis != 95 || metric.P99Millis != 99 || metric.MaxMillis != 100 || !metric.Passed {
		t.Fatalf("unexpected latency metric: %+v", metric)
	}
}
