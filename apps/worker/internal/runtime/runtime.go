// Package runtime owns the worker polling lifecycle and cancellation boundary.
package runtime

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/iFTY-R/game-night/apps/worker/internal/checkpoint"
	"github.com/iFTY-R/game-night/platform/admin/operations"
	"github.com/iFTY-R/game-night/platform/keyrotation"
)

var ErrInvalidConfig = errors.New("invalid worker runtime configuration")

type dispatcher interface {
	RunOnce(context.Context) (checkpoint.Result, error)
}

type checkpointScheduler interface {
	RunOnce(context.Context) (checkpoint.ScheduleResult, error)
}

type maintenance interface {
	RunOnce(context.Context) error
}

type rotation interface {
	RunOnce(context.Context) (keyrotation.Result, error)
}

// Runtime executes one pass immediately and then on a fixed bounded interval until cancellation.
type Runtime struct {
	scheduler    checkpointScheduler
	dispatcher   dispatcher
	rotation     rotation
	maintenance  maintenance
	adminJobs    maintenance
	pollInterval time.Duration
	logger       *slog.Logger
	healthMu     sync.RWMutex
	health       map[string]operations.HealthStatus
}

// New validates the loop owner and observer before any goroutine is started.
func New(dispatcher dispatcher, pollInterval time.Duration, logger *slog.Logger) (*Runtime, error) {
	return NewWithMaintenance(dispatcher, nil, pollInterval, logger)
}

// NewWithMaintenance adds an optional database-time cleanup pass without widening the dispatcher authority.
func NewWithMaintenance(dispatcher dispatcher, cleanup maintenance, pollInterval time.Duration, logger *slog.Logger) (*Runtime, error) {
	return NewWithOperations(dispatcher, nil, cleanup, pollInterval, logger)
}

// NewWithOperations adds serial key rotation and cleanup passes without overlapping worker authorities.
func NewWithOperations(dispatcher dispatcher, keyRotation rotation, cleanup maintenance, pollInterval time.Duration, logger *slog.Logger) (*Runtime, error) {
	return NewWithCheckpointScheduler(dispatcher, nil, keyRotation, cleanup, pollInterval, logger)
}

// NewWithCheckpointScheduler runs due-checkpoint creation before delivery in the same serial worker pass.
func NewWithCheckpointScheduler(
	dispatcher dispatcher,
	scheduler checkpointScheduler,
	keyRotation rotation,
	cleanup maintenance,
	pollInterval time.Duration,
	logger *slog.Logger,
) (*Runtime, error) {
	return NewWithAdminJobs(dispatcher, scheduler, keyRotation, cleanup, nil, pollInterval, logger)
}

// NewWithAdminJobs adds durable admin-governance processing after checkpoint, rotation, and cleanup work.
// The job pass remains bounded by its own dispatcher and shares this runtime's serial cancellation boundary.
func NewWithAdminJobs(
	dispatcher dispatcher,
	scheduler checkpointScheduler,
	keyRotation rotation,
	cleanup maintenance,
	adminJobs maintenance,
	pollInterval time.Duration,
	logger *slog.Logger,
) (*Runtime, error) {
	if dispatcher == nil || pollInterval <= 0 || logger == nil {
		return nil, ErrInvalidConfig
	}
	runtime := &Runtime{
		scheduler: scheduler, dispatcher: dispatcher, rotation: keyRotation, maintenance: cleanup, adminJobs: adminJobs,
		pollInterval: pollInterval, logger: logger, health: map[string]operations.HealthStatus{"checkpoint_dispatch": operations.HealthUnavailable},
	}
	for code, enabled := range map[string]bool{
		"checkpoint_scheduler": scheduler != nil,
		"key_rotation":         keyRotation != nil,
		"cleanup":              cleanup != nil,
		"admin_jobs":           adminJobs != nil,
	} {
		if enabled {
			runtime.health[code] = operations.HealthUnavailable
		}
	}
	return runtime, nil
}

// HealthComponents returns the most recent bounded pass status for heartbeat reporting.
func (runtime *Runtime) HealthComponents() map[string]operations.HealthStatus {
	if runtime == nil {
		return map[string]operations.HealthStatus{}
	}
	runtime.healthMu.RLock()
	defer runtime.healthMu.RUnlock()
	result := make(map[string]operations.HealthStatus, len(runtime.health))
	for code, status := range runtime.health {
		result[code] = status
	}
	return result
}

func (runtime *Runtime) setHealth(code string, err error) {
	status := operations.HealthHealthy
	if err != nil {
		status = operations.HealthUnavailable
	}
	runtime.healthMu.Lock()
	runtime.health[code] = status
	runtime.healthMu.Unlock()
}

// Run serially executes passes so one process never overlaps its own lease-bound delivery work.
func (runtime *Runtime) Run(ctx context.Context) error {
	if runtime == nil || ctx == nil {
		return ErrInvalidConfig
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			if runtime.scheduler != nil {
				scheduleResult, scheduleErr := runtime.scheduler.RunOnce(ctx)
				runtime.setHealth("checkpoint_scheduler", scheduleErr)
				if scheduleErr != nil && ctx.Err() == nil {
					runtime.logger.Warn("checkpoint scheduler pass failed", slog.String("error", scheduleErr.Error()))
				}
				if scheduleResult.Enqueued {
					runtime.logger.Info("checkpoint scheduler advanced")
				}
			}
			result, err := runtime.dispatcher.RunOnce(ctx)
			runtime.setHealth("checkpoint_dispatch", err)
			if err != nil && ctx.Err() == nil {
				// Dispatcher errors are stable categories; payloads and sink responses never enter logs.
				runtime.logger.Warn("checkpoint worker pass failed", slog.String("error", err.Error()))
			}
			if result.Delivered > 0 {
				runtime.logger.Info("checkpoint worker advanced", slog.Uint64("delivered", uint64(result.Delivered)))
			}
			if runtime.rotation != nil {
				rotationResult, rotationErr := runtime.rotation.RunOnce(ctx)
				runtime.setHealth("key_rotation", rotationErr)
				if rotationErr != nil && ctx.Err() == nil {
					runtime.logger.Warn("key rotation worker pass failed", slog.String("error", rotationErr.Error()))
				}
				if rotationResult.Processed > 0 || rotationResult.Conflicts > 0 || rotationResult.Completed {
					runtime.logger.Info("key rotation worker advanced",
						slog.Uint64("processed", uint64(rotationResult.Processed)),
						slog.Uint64("conflicts", uint64(rotationResult.Conflicts)),
						slog.Bool("completed", rotationResult.Completed))
				}
			}
			if runtime.maintenance != nil {
				cleanupErr := runtime.maintenance.RunOnce(ctx)
				runtime.setHealth("cleanup", cleanupErr)
				if cleanupErr != nil && ctx.Err() == nil {
					runtime.logger.Warn("worker cleanup pass failed", slog.String("error", "maintenance unavailable"))
				}
			}
			// Admin governance queues are independent durable work. They must run even when deployments omit expiry cleanup.
			if runtime.adminJobs != nil {
				adminJobErr := runtime.adminJobs.RunOnce(ctx)
				runtime.setHealth("admin_jobs", adminJobErr)
				if adminJobErr != nil && ctx.Err() == nil {
					runtime.logger.Warn("admin governance worker pass failed", slog.String("error", "admin governance unavailable"))
				}
			}
			timer.Reset(runtime.pollInterval)
		}
	}
}
