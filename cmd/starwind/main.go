package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"spaceempire/back/internal/app"
	"spaceempire/back/internal/observ"
	"spaceempire/back/internal/pkg/config"
)

func main() {
	// Bootstrap logger for the config-load phase; replaced by the
	// config-driven logger (format/level/rotation) once cfg is loaded.
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		logger.Error("load config", "err", err)
		os.Exit(1)
	}
	logger = observ.NewLogger(cfg.Observability)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx, cfg, logger); err != nil {
		logger.Error("app run", "err", err)
		os.Exit(1)
	}
	logger.Info("shutdown complete")
}
