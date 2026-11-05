package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Address             string
	DatabaseURL         string
	LiveKitURL          string
	LiveKitAPIKey       string
	LiveKitAPISecret    string
	AdminSecret         string
	RoomPrefix          string
	TokenTTL            time.Duration
	ControlRoomName     string
	PublicAPIURL        string // what devices use to reach this control room
	SyslogIP            string
	SyslogPort          int
	OrgSecret           string
	AgentSecret         string        // shared with the agent for its server-side calls (meeting audio, credentials)
	AgentProfilesFile   string        // JSON with the Kubernetes-managed agent profiles; empty = none
	SecretsKey          string        // encrypts provider API keys at rest; empty = keys only from the agent's env
	MeetingsDir         string        // where meeting audio lives
	MeetingMaxDuration  time.Duration // RM-24 default
	MeetingRetention    int           // days (RM-45); 0 disables
	MeetingSummary      bool          // RM-32, per control room
	OpenAIAPIKey        string        // RM-34; empty = meetings end in no_transcript
	OpenAIBaseURL       string
	TranscribeModel     string
	SummaryModel        string
	FFmpeg              string
	DiscoveryEnabled    bool
	ShutdownTimeout     time.Duration
	DatabasePingTimeout time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		Address:             envOr("SEBASTIAN_SERVER_ADDRESS", ":8787"),
		DatabaseURL:         strings.TrimSpace(os.Getenv("DATABASE_URL")),
		LiveKitURL:          strings.TrimSpace(os.Getenv("LIVEKIT_URL")),
		LiveKitAPIKey:       strings.TrimSpace(os.Getenv("LIVEKIT_API_KEY")),
		LiveKitAPISecret:    strings.TrimSpace(os.Getenv("LIVEKIT_API_SECRET")),
		AdminSecret:         strings.TrimSpace(os.Getenv("SEBASTIAN_ADMIN_SECRET")),
		RoomPrefix:          envOr("SEBASTIAN_ROOM_PREFIX", "sebastian"),
		ControlRoomName:     strings.TrimSpace(os.Getenv("SEBASTIAN_CONTROL_ROOM_NAME")),
		PublicAPIURL:        strings.TrimRight(strings.TrimSpace(os.Getenv("SEBASTIAN_PUBLIC_API_URL")), "/"),
		SyslogIP:            strings.TrimSpace(os.Getenv("SEBASTIAN_SYSLOG_IP")),
		SyslogPort:          514,
		OrgSecret:           strings.TrimSpace(os.Getenv("SEBASTIAN_ORG_SECRET")),
		AgentSecret:         strings.TrimSpace(os.Getenv("SEBASTIAN_AGENT_SECRET")),
		AgentProfilesFile:   strings.TrimSpace(os.Getenv("SEBASTIAN_AGENT_PROFILES_FILE")),
		SecretsKey:          strings.TrimSpace(os.Getenv("SEBASTIAN_SECRETS_KEY")),
		MeetingsDir:         envOr("SEBASTIAN_MEETINGS_DIR", "meetings"),
		MeetingMaxDuration:  3 * time.Hour,
		MeetingRetention:    90,
		OpenAIAPIKey:        strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
		OpenAIBaseURL:       strings.TrimRight(envOr("OPENAI_BASE_URL", "https://api.openai.com/v1"), "/"),
		TranscribeModel:     envOr("SEBASTIAN_TRANSCRIBE_MODEL", "gpt-4o-transcribe-diarize"),
		SummaryModel:        envOr("SEBASTIAN_SUMMARY_MODEL", "gpt-5.4-mini"),
		FFmpeg:              envOr("SEBASTIAN_FFMPEG", "ffmpeg"),
		TokenTTL:            time.Hour,
		ShutdownTimeout:     10 * time.Second,
		DatabasePingTimeout: 2 * time.Second,
	}

	var err error
	if cfg.TokenTTL, err = duration("SEBASTIAN_TOKEN_TTL", cfg.TokenTTL); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownTimeout, err = duration("SEBASTIAN_SHUTDOWN_TIMEOUT", cfg.ShutdownTimeout); err != nil {
		return Config{}, err
	}
	if cfg.DatabasePingTimeout, err = duration("SEBASTIAN_DATABASE_PING_TIMEOUT", cfg.DatabasePingTimeout); err != nil {
		return Config{}, err
	}
	if cfg.MeetingMaxDuration, err = duration("SEBASTIAN_MEETING_MAX_DURATION", cfg.MeetingMaxDuration); err != nil {
		return Config{}, err
	}
	if cfg.DiscoveryEnabled, err = boolean("SEBASTIAN_DISCOVERY_ENABLED", true); err != nil {
		return Config{}, err
	}
	if cfg.MeetingSummary, err = boolean("SEBASTIAN_MEETING_SUMMARY", true); err != nil {
		return Config{}, err
	}
	if cfg.MeetingRetention, err = integer("SEBASTIAN_MEETING_RETENTION_DAYS", cfg.MeetingRetention); err != nil {
		return Config{}, err
	}
	if cfg.SyslogPort, err = integer("SEBASTIAN_SYSLOG_PORT", cfg.SyslogPort); err != nil {
		return Config{}, err
	}
	if cfg.ControlRoomName == "" {
		cfg.ControlRoomName = cfg.PublicAPIURL
	}

	var missing []string
	for name, value := range map[string]string{
		"DATABASE_URL":           cfg.DatabaseURL,
		"LIVEKIT_URL":            cfg.LiveKitURL,
		"LIVEKIT_API_KEY":        cfg.LiveKitAPIKey,
		"LIVEKIT_API_SECRET":     cfg.LiveKitAPISecret,
		"SEBASTIAN_ADMIN_SECRET": cfg.AdminSecret,
	} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}
	if cfg.TokenTTL <= 0 {
		return Config{}, errors.New("SEBASTIAN_TOKEN_TTL must be positive")
	}

	return cfg, nil
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func duration(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	return parsed, nil
}

func boolean(name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("parse %s: %w", name, err)
	}
	return parsed, nil
}

func integer(name string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	return parsed, nil
}
