package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/zetesis-labs/sebastian/server/internal/config"
	"github.com/zetesis-labs/sebastian/server/internal/database"
	"github.com/zetesis-labs/sebastian/server/internal/messaging"
	"github.com/zetesis-labs/sebastian/server/internal/outbox"
	postgresstore "github.com/zetesis-labs/sebastian/server/internal/postgres"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("outbox worker stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.LoadOutbox()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := database.Open(cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	publisher, err := messaging.NewPublisher(ctx, cfg.NATSURL, cfg.NATSStream, cfg.NATSSubjects, cfg.PublishTimeout)
	if err != nil {
		return err
	}
	defer publisher.Close()

	logger.Info("outbox worker started", "stream", cfg.NATSStream, "subjects", cfg.NATSSubjects)
	runner := outbox.NewRunner(
		postgresstore.NewStore(db),
		publisher,
		logger,
		cfg.BatchSize,
		cfg.PollInterval,
	)
	return runner.Run(ctx)
}
