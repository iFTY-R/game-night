package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/iFTY-R/game-night/apps/realtime/internal/application"
	"github.com/iFTY-R/game-night/apps/realtime/internal/config"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(logger); err != nil {
		logger.Error("realtime process stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	if logger == nil {
		return errors.New("realtime logger is required")
	}
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app, err := application.New(ctx, cfg, application.Options{Logger: logger})
	if err != nil {
		return err
	}
	return app.ListenAndServe(ctx)
}
