package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/iFTY-R/game-night/apps/api/internal/application"
	apiConfig "github.com/iFTY-R/game-night/apps/api/internal/config"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/logging"
	"github.com/iFTY-R/game-night/apps/internal/checkpointstorage"
	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
	"github.com/iFTY-R/game-night/platform/audit"
	gameregistry "github.com/iFTY-R/game-night/tooling/game-registry"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	errLoadConfiguration = errors.New("load API configuration")
	errBuildApplication  = errors.New("build API application")
	errServeApplication  = errors.New("serve API application")
	errStopApplication   = errors.New("stop API application")
	// The probe cache prevents readiness traffic from turning into unbounded object-store traffic.
	checkpointProbeInterval = 15 * time.Second
	checkpointProbeTimeout  = 2 * time.Second
)

func main() {
	logger := slog.New(logging.NewJSONHandler(os.Stderr, slog.LevelInfo))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.LookupEnv, logger); err != nil {
		logger.Error("api process stopped", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run(ctx context.Context, lookup sharedconfig.LookupEnv, logger *slog.Logger) error {
	if ctx == nil || lookup == nil || logger == nil {
		return errLoadConfiguration
	}
	config, err := apiConfig.Load(lookup)
	if err != nil {
		return errLoadConfiguration
	}
	checkpointSink, err := buildCheckpointReadiness(ctx, config)
	if err != nil {
		return errBuildApplication
	}
	metricsRegistry := prometheus.NewRegistry()
	registry, err := gameregistry.New()
	if err != nil {
		return errBuildApplication
	}
	app, err := application.New(ctx, config, application.Options{
		Logger: logger, Metrics: metricsRegistry, CheckpointSink: checkpointSink, Registry: registry,
	})
	if err != nil {
		return errBuildApplication
	}
	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- app.ListenAndServe()
	}()

	select {
	case serveErr := <-serveErrors:
		_ = app.Shutdown(ctx)
		if serveErr != nil {
			return errServeApplication
		}
		return nil
	case <-ctx.Done():
		if err := app.Shutdown(ctx); err != nil {
			return errStopApplication
		}
		if serveErr := <-serveErrors; serveErr != nil {
			return errServeApplication
		}
		return nil
	}
}

func buildCheckpointReadiness(ctx context.Context, config apiConfig.Config) (audit.SinkReadiness, error) {
	sink, err := checkpointstorage.Build(ctx, config.CheckpointStorage)
	if err != nil {
		return nil, err
	}
	return checkpointstorage.NewReadiness(
		config.Shared.Environment, sink, checkpointProbeInterval, checkpointProbeTimeout,
	)
}
