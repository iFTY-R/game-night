package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/iFTY-R/game-night/apps/api/internal/application"
	apiConfig "github.com/iFTY-R/game-night/apps/api/internal/config"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/logging"
	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	errLoadConfiguration = errors.New("load API configuration")
	errBuildApplication  = errors.New("build API application")
	errServeApplication  = errors.New("serve API application")
	errStopApplication   = errors.New("stop API application")
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
	metricsRegistry := prometheus.NewRegistry()
	app, err := application.New(ctx, config, application.Options{
		Logger: logger, Metrics: metricsRegistry, CheckpointSink: defaultCheckpointSink(config.Shared.Environment),
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

func defaultCheckpointSink(environment sharedconfig.Environment) audit.SinkReadiness {
	// Task 15 replaces this with the live WORM worker probe. Production remains fail closed until then.
	ready := environment != sharedconfig.EnvironmentProduction
	return audit.SinkReadinessFunc(func(context.Context) bool { return ready })
}
