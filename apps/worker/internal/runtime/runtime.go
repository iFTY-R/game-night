// Package runtime owns the worker polling lifecycle and cancellation boundary.
package runtime

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/iFTY-R/game-night/apps/worker/internal/checkpoint"
	"github.com/iFTY-R/game-night/platform/keyrotation"
)

var ErrInvalidConfig = errors.New("invalid worker runtime configuration")

type dispatcher interface {
	RunOnce(context.Context) (checkpoint.Result, error)
}

type maintenance interface {
	RunOnce(context.Context) error
}

type rotation interface {
	RunOnce(context.Context) (keyrotation.Result, error)
}

// Runtime executes one pass immediately and then on a fixed bounded interval until cancellation.
type Runtime struct {
	dispatcher   dispatcher
	rotation     rotation
	maintenance  maintenance
	pollInterval time.Duration
	logger       *slog.Logger
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
	if dispatcher == nil || pollInterval <= 0 || logger == nil {
		return nil, ErrInvalidConfig
	}
	return &Runtime{dispatcher: dispatcher, rotation: keyRotation, maintenance: cleanup, pollInterval: pollInterval, logger: logger}, nil
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
			result, err := runtime.dispatcher.RunOnce(ctx)
			if err != nil && ctx.Err() == nil {
				// Dispatcher errors are stable categories; payloads and sink responses never enter logs.
				runtime.logger.Warn("checkpoint worker pass failed", slog.String("error", err.Error()))
			}
			if result.Delivered > 0 {
				runtime.logger.Info("checkpoint worker advanced", slog.Uint64("delivered", uint64(result.Delivered)))
			}
			if runtime.rotation != nil {
				rotationResult, rotationErr := runtime.rotation.RunOnce(ctx)
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
				if cleanupErr := runtime.maintenance.RunOnce(ctx); cleanupErr != nil && ctx.Err() == nil {
					runtime.logger.Warn("worker cleanup pass failed", slog.String("error", "maintenance unavailable"))
				}
			}
			timer.Reset(runtime.pollInterval)
		}
	}
}
