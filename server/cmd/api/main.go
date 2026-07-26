package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	apihttp "github.com/zetesis-labs/sebastian/server/internal/api"
	"github.com/zetesis-labs/sebastian/server/internal/config"
	"github.com/zetesis-labs/sebastian/server/internal/database"
	"github.com/zetesis-labs/sebastian/server/internal/httpserver"
	livekitgateway "github.com/zetesis-labs/sebastian/server/internal/livekit"
	postgresstore "github.com/zetesis-labs/sebastian/server/internal/postgres"
	"github.com/zetesis-labs/sebastian/server/internal/recording"
	"github.com/zetesis-labs/sebastian/server/internal/session"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
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
	if err := db.PingContext(ctx); err != nil {
		return err
	}

	store := postgresstore.NewStore(db)
	livekit := livekitgateway.NewGateway(cfg.LiveKitURL, cfg.LiveKitAPIKey, cfg.LiveKitAPISecret)
	sessions := session.NewService(store, livekit, cfg.LiveKitURL, cfg.RoomPrefix, cfg.TokenTTL, cfg.LegacyDeviceID)
	recordings := recording.NewService(store)
	handler := apihttp.NewHandler(sessions, recordings, store, logger, cfg.LegacyTokenEnabled, cfg.DatabasePingTimeout)
	server, err := httpserver.New(cfg.Address, handler, logger, cfg.AdminSecret)
	if err != nil {
		return err
	}

	errorsCh := make(chan error, 1)
	go func() {
		logger.Info("sebastian server listening",
			"address", cfg.Address,
			"legacy_token_enabled", cfg.LegacyTokenEnabled,
		)
		errorsCh <- server.ListenAndServe()
	}()

	select {
	case err := <-errorsCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return err
	}
	return nil
}
