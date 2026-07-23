package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/iFTY-R/game-night/apps/edge/internal/config"
	edgeserver "github.com/iFTY-R/game-night/apps/edge/internal/server"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.LookupEnv, logger); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("edge process stopped", "error", err.Error())
		os.Exit(1)
	}
}

func run(ctx context.Context, lookup config.LookupEnv, logger *slog.Logger) error {
	cfg, err := config.Load(lookup)
	if err != nil {
		return err
	}
	return edgeserver.Run(ctx, cfg, logger)
}
