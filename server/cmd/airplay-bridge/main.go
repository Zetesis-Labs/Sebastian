// Command airplay-bridge exposes one speaker as an AirPlay receiver.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	protologger "github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"

	"github.com/zetesis-labs/sebastian/server/internal/airplay"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("bridge stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := airplay.LoadConfig()
	if err != nil {
		return err
	}
	protologger.InitFromConfig(&protologger.Config{JSON: true, Level: "warn"}, "airplay-bridge")
	lksdk.SetLogger(protologger.GetLogger())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	frames, err := airplay.StartReceiver(ctx, cfg, logger)
	if err != nil {
		return err
	}
	control := airplay.NewControlRoom(cfg.APIURL, cfg.DeviceID, cfg.AdminSecret)
	err = airplay.NewBridge(control, airplay.JoinLiveKit(logger), logger).Run(ctx, frames)
	if ctx.Err() != nil {
		return nil
	}
	return err
}
