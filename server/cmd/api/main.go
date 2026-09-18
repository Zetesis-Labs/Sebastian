package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zetesis-labs/sebastian/server/internal/adoption"
	apihttp "github.com/zetesis-labs/sebastian/server/internal/api"
	"github.com/zetesis-labs/sebastian/server/internal/config"
	"github.com/zetesis-labs/sebastian/server/internal/database"
	"github.com/zetesis-labs/sebastian/server/internal/device"
	"github.com/zetesis-labs/sebastian/server/internal/discovery"
	"github.com/zetesis-labs/sebastian/server/internal/httpserver"
	livekitgateway "github.com/zetesis-labs/sebastian/server/internal/livekit"
	"github.com/zetesis-labs/sebastian/server/internal/meeting"
	postgresstore "github.com/zetesis-labs/sebastian/server/internal/postgres"
	"github.com/zetesis-labs/sebastian/server/internal/recording"
	"github.com/zetesis-labs/sebastian/server/internal/session"
	"github.com/zetesis-labs/sebastian/server/internal/transcribe"
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
	sessions := session.NewService(store, livekit, cfg.LiveKitURL, cfg.RoomPrefix, cfg.TokenTTL)
	recordings := recording.NewService(store)
	var lan discovery.Browser = discovery.Static{}
	if cfg.DiscoveryEnabled {
		browser := discovery.NewMDNS(logger)
		go browser.Run(ctx)
		lan = browser
	}
	room := device.ControlRoom{
		Name:       cfg.ControlRoomName,
		APIURL:     cfg.PublicAPIURL,
		SyslogIP:   cfg.SyslogIP,
		SyslogPort: cfg.SyslogPort,
		OrgSecret:  cfg.OrgSecret,
		AdoptPort:  adoption.Port,
	}
	lanClient := adoption.NewClient()
	devices := device.NewService(store, lan, lanClient, room, logger)
	meetings := meeting.NewService(store.Meetings(), devices, lanClient, cfg.MeetingsDir, meeting.Limits{MaxDuration: cfg.MeetingMaxDuration}, logger)
	var provider transcribe.Provider
	if cfg.OpenAIAPIKey != "" {
		openai := transcribe.NewOpenAI(cfg.OpenAIAPIKey)
		openai.BaseURL, openai.TranscribeModel, openai.SummaryModel = cfg.OpenAIBaseURL, cfg.TranscribeModel, cfg.SummaryModel
		provider = openai
	} else {
		logger.Warn("OPENAI_API_KEY not set — meetings will record but not transcribe")
	}
	transcriber := transcribe.NewJob(meetings, provider, transcribe.FFmpeg{Path: cfg.FFmpeg}, cfg.MeetingSummary, logger)
	meetings.OnTranscribe, meetings.OnSummarize = transcriber.Enqueue, transcriber.EnqueueSummary
	go meetings.Run(ctx, 5*time.Second)
	go transcriber.Run(ctx)
	go meetings.RunRetention(ctx, cfg.MeetingRetention, 24*time.Hour)
	handler := apihttp.NewHandler(sessions, recordings, devices, meetings, store, logger, cfg.DatabasePingTimeout)
	server, err := httpserver.New(cfg.Address, handler, logger, cfg.AdminSecret,
		httpserver.Raw{Pattern: "PUT /v1/meetings/{id}/audio", Handler: apihttp.MeetingAudioUpload(meetings, cfg.AgentSecret)},
		httpserver.Raw{Pattern: "HEAD /v1/meetings/{id}/audio", Handler: apihttp.MeetingAudioOffset(meetings)(cfg.AgentSecret)},
		httpserver.Raw{Pattern: "POST /v1/meetings", Handler: apihttp.MeetingAgentStart(meetings, cfg.AgentSecret)},
		httpserver.Raw{Pattern: "POST /v1/meetings/{id}/stop", Handler: apihttp.MeetingAgentStop(meetings, cfg.AgentSecret)},
		httpserver.Raw{Pattern: "POST /v1/meetings/{id}/warn", Handler: apihttp.MeetingAgentWarn(meetings, cfg.AgentSecret)},
		httpserver.Raw{Pattern: "GET /v1/admin/meetings/{id}/audio", Handler: apihttp.MeetingAudioDownload(meetings)},
	)
	if err != nil {
		return err
	}

	errorsCh := make(chan error, 1)
	go func() {
		logger.Info("sebastian server listening",
			"address", cfg.Address,
			"control_room", cfg.ControlRoomName,
			"public_api_url", cfg.PublicAPIURL,
			"discovery", cfg.DiscoveryEnabled,
			"org_secret_configured", cfg.OrgSecret != "",
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
