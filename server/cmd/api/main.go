package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zetesis-labs/sebastian/server/internal/adoption"
	"github.com/zetesis-labs/sebastian/server/internal/agentprofile"
	"github.com/zetesis-labs/sebastian/server/internal/announce"
	apihttp "github.com/zetesis-labs/sebastian/server/internal/api"
	"github.com/zetesis-labs/sebastian/server/internal/config"
	"github.com/zetesis-labs/sebastian/server/internal/database"
	"github.com/zetesis-labs/sebastian/server/internal/device"
	"github.com/zetesis-labs/sebastian/server/internal/discovery"
	"github.com/zetesis-labs/sebastian/server/internal/httpserver"
	livekitgateway "github.com/zetesis-labs/sebastian/server/internal/livekit"
	"github.com/zetesis-labs/sebastian/server/internal/meeting"
	"github.com/zetesis-labs/sebastian/server/internal/playback"
	postgresstore "github.com/zetesis-labs/sebastian/server/internal/postgres"
	"github.com/zetesis-labs/sebastian/server/internal/secretbox"
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
	agents, err := agentProfiles(ctx, cfg, store, logger)
	if err != nil {
		return err
	}
	announcements := announce.NewService(devices, lanClient, busyUnits{livekit: livekit, meetings: meetings}, logger)
	playbacks := playback.NewService(devices, lanClient, meetings, livekit,
		playback.Config{RoomPrefix: cfg.RoomPrefix, ServerURL: cfg.LiveKitURL, TokenTTL: cfg.TokenTTL}, logger)
	handler := apihttp.NewHandler(sessions, devices, meetings, store, logger, cfg.DatabasePingTimeout).
		WithAgentProfiles(agents).
		WithAnnouncements(announcements).
		WithPlayback(playbacks)
	server, err := httpserver.New(cfg.Address, handler, logger, cfg.AdminSecret,
		httpserver.Raw{Pattern: "PUT /v1/meetings/{id}/audio", Handler: apihttp.MeetingAudioUpload(meetings, cfg.AgentSecret)},
		httpserver.Raw{Pattern: "HEAD /v1/meetings/{id}/audio", Handler: apihttp.MeetingAudioOffset(meetings)(cfg.AgentSecret)},
		httpserver.Raw{Pattern: "POST /v1/meetings", Handler: apihttp.MeetingAgentStart(meetings, cfg.AgentSecret)},
		httpserver.Raw{Pattern: "POST /v1/meetings/{id}/stop", Handler: apihttp.MeetingAgentStop(meetings, cfg.AgentSecret)},
		httpserver.Raw{Pattern: "POST /v1/playback/stop", Handler: apihttp.PlaybackAgentStop(playbacks, cfg.AgentSecret)},
		httpserver.Raw{Pattern: "POST /v1/meetings/{id}/warn", Handler: apihttp.MeetingAgentWarn(meetings, cfg.AgentSecret)},
		httpserver.Raw{Pattern: "GET /v1/admin/meetings/{id}/audio", Handler: apihttp.MeetingAudioDownload(meetings)},
		httpserver.Raw{Pattern: "GET /v1/agent-profiles/{id}/credentials", Handler: apihttp.AgentProfileCredentials(agents, cfg.AgentSecret, logger)},
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
			"secrets_key_configured", agents.SecretsKeyConfigured(),
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

// agentProfiles syncs the Kubernetes-managed profiles from their file before
// serving: a broken file stops the server instead of serving stale agents.
func agentProfiles(ctx context.Context, cfg config.Config, store *postgresstore.Store, logger *slog.Logger) (*agentprofile.Service, error) {
	var sealer agentprofile.Sealer
	if cfg.SecretsKey != "" {
		key, err := secretbox.ParseKey(cfg.SecretsKey)
		if err != nil {
			return nil, err
		}
		box, err := secretbox.New(key)
		if err != nil {
			return nil, err
		}
		sealer = box
	} else {
		logger.Warn("SEBASTIAN_SECRETS_KEY not set — API keys cannot be stored from the panel; the agent uses its environment keys")
	}
	agents := agentprofile.NewService(store, sealer, logger)
	if cfg.AgentProfilesFile == "" {
		return agents, nil
	}
	data, err := os.ReadFile(cfg.AgentProfilesFile)
	if err != nil {
		return nil, fmt.Errorf("read agent profiles file: %w", err)
	}
	manifest, err := agentprofile.ParseManifest(data)
	if err != nil {
		return nil, fmt.Errorf("agent profiles file %s: %w", cfg.AgentProfilesFile, err)
	}
	plan, err := agents.Sync(ctx, manifest)
	if err != nil {
		return nil, err
	}
	logger.Info("agent profiles synced from file", "file", cfg.AgentProfilesFile, "declared", len(manifest),
		"created", len(plan.Create), "updated", len(plan.Update), "deleted", len(plan.Delete), "released", len(plan.Release))
	return agents, nil
}

// busyUnits tells the announce service what a unit is doing: talking with an
// agent (LiveKit, where it joins with its device id) or recording a meeting.
type busyUnits struct {
	livekit  *livekitgateway.Gateway
	meetings *meeting.Service
}

func (b busyUnits) Say(ctx context.Context, deviceID string, payload []byte) (bool, error) {
	return b.livekit.SendToAgentWith(ctx, deviceID, announce.Topic, payload)
}

func (b busyUnits) InMeeting(ctx context.Context, deviceID string) (bool, error) {
	return b.meetings.InMeeting(ctx, deviceID)
}
