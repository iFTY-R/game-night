package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
	"github.com/iFTY-R/game-night/apps/worker/internal/application"
	workerconfig "github.com/iFTY-R/game-night/apps/worker/internal/config"
)

var (
	errLoadConfiguration = errors.New("load worker configuration")
	errBuildApplication  = errors.New("build worker application")
	errRunApplication    = errors.New("run worker application")
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.LookupEnv, logger); err != nil {
		logger.Error("worker process stopped", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run(ctx context.Context, lookup sharedconfig.LookupEnv, logger *slog.Logger) error {
	if ctx == nil || lookup == nil || logger == nil {
		return errLoadConfiguration
	}
	config, err := workerconfig.Load(lookup)
	if err != nil {
		return errLoadConfiguration
	}
	app, err := application.New(ctx, config, logger)
	if err != nil {
		return errBuildApplication
	}
	defer app.Close()
	if err := app.Run(ctx); err != nil {
		return errRunApplication
	}
	return nil
}
