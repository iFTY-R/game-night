package checkpointstorage

import (
	"context"
	"errors"
	"sync"
	"time"

	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
	"github.com/iFTY-R/game-night/platform/persistence/objectstorage"
)

// Readiness caches the bounded external sink probe so API database health transactions never wait on S3 per request.
type Readiness struct {
	environment sharedconfig.Environment
	sink        objectstorage.Sink
	interval    time.Duration
	timeout     time.Duration

	mu        sync.Mutex
	ready     bool
	expiresAt time.Time
}

// NewReadiness binds deployment policy to a live sink without allowing a local sink to satisfy production health.
func NewReadiness(
	environment sharedconfig.Environment,
	sink objectstorage.Sink,
	interval time.Duration,
	timeout time.Duration,
) (*Readiness, error) {
	if sink == nil || interval <= 0 || timeout <= 0 || timeout > interval ||
		(environment != sharedconfig.EnvironmentDevelopment && environment != sharedconfig.EnvironmentTest &&
			environment != sharedconfig.EnvironmentProduction) {
		return nil, ErrInvalidConfig
	}
	return &Readiness{environment: environment, sink: sink, interval: interval, timeout: timeout}, nil
}

// Ready refreshes one serialized external probe after the cache expires and caches both success and failure.
func (readiness *Readiness) Ready(ctx context.Context) bool {
	if readiness == nil || ctx == nil {
		return false
	}
	readiness.mu.Lock()
	defer readiness.mu.Unlock()
	now := time.Now().UTC()
	if now.Before(readiness.expiresAt) {
		return readiness.ready
	}
	probeContext, cancel := context.WithTimeout(ctx, readiness.timeout)
	err := readiness.sink.CheckProductionReady(probeContext)
	cancel()
	readiness.ready = err == nil || readiness.environment != sharedconfig.EnvironmentProduction &&
		errors.Is(err, objectstorage.ErrNonProductionSink)
	readiness.expiresAt = now.Add(readiness.interval)
	return readiness.ready
}
