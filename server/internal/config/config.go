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
	LegacyTokenEnabled  bool
	LegacyDeviceID      string
	ShutdownTimeout     time.Duration
	DatabasePingTimeout time.Duration
}

type OutboxConfig struct {
	DatabaseURL    string
	NATSURL        string
	NATSStream     string
	NATSSubjects   string
	PollInterval   time.Duration
	PublishTimeout time.Duration
	BatchSize      int
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
		LegacyDeviceID:      envOr("SEBASTIAN_LEGACY_DEVICE_ID", "esp32-respeaker"),
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
	if cfg.LegacyTokenEnabled, err = boolean("SEBASTIAN_LEGACY_TOKEN_ENABLED", false); err != nil {
		return Config{}, err
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

func LoadOutbox() (OutboxConfig, error) {
	cfg := OutboxConfig{
		DatabaseURL:    strings.TrimSpace(os.Getenv("DATABASE_URL")),
		NATSURL:        strings.TrimSpace(os.Getenv("NATS_URL")),
		NATSStream:     envOr("SEBASTIAN_NATS_STREAM", "SEBASTIAN_EVENTS"),
		NATSSubjects:   envOr("SEBASTIAN_NATS_SUBJECTS", "evt.sebastian.v1.>"),
		PollInterval:   time.Second,
		PublishTimeout: 5 * time.Second,
		BatchSize:      50,
	}

	var err error
	if cfg.PollInterval, err = duration("SEBASTIAN_OUTBOX_POLL_INTERVAL", cfg.PollInterval); err != nil {
		return OutboxConfig{}, err
	}
	if cfg.PublishTimeout, err = duration("SEBASTIAN_OUTBOX_PUBLISH_TIMEOUT", cfg.PublishTimeout); err != nil {
		return OutboxConfig{}, err
	}
	if cfg.BatchSize, err = integer("SEBASTIAN_OUTBOX_BATCH_SIZE", cfg.BatchSize); err != nil {
		return OutboxConfig{}, err
	}

	var missing []string
	for name, value := range map[string]string{
		"DATABASE_URL": cfg.DatabaseURL,
		"NATS_URL":     cfg.NATSURL,
	} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return OutboxConfig{}, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}
	if cfg.PollInterval <= 0 || cfg.PublishTimeout <= 0 {
		return OutboxConfig{}, errors.New("outbox durations must be positive")
	}
	if cfg.BatchSize <= 0 || cfg.BatchSize > 1000 {
		return OutboxConfig{}, errors.New("SEBASTIAN_OUTBOX_BATCH_SIZE must be between 1 and 1000")
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
