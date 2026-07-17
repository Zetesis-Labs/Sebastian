package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/sebastian")
	t.Setenv("LIVEKIT_URL", "ws://localhost:7880")
	t.Setenv("LIVEKIT_API_KEY", "key")
	t.Setenv("LIVEKIT_API_SECRET", "secret")
	t.Setenv("SEBASTIAN_ADMIN_SECRET", "admin-secret")
	t.Setenv("SEBASTIAN_TOKEN_TTL", "30m")
	t.Setenv("SEBASTIAN_LEGACY_TOKEN_ENABLED", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.TokenTTL != 30*time.Minute {
		t.Fatalf("TokenTTL = %s, want 30m", cfg.TokenTTL)
	}
	if !cfg.LegacyTokenEnabled {
		t.Fatal("LegacyTokenEnabled = false, want true")
	}
}

func TestLoadReportsMissingRequiredValues(t *testing.T) {
	for _, name := range []string{"DATABASE_URL", "LIVEKIT_URL", "LIVEKIT_API_KEY", "LIVEKIT_API_SECRET", "SEBASTIAN_ADMIN_SECRET"} {
		t.Setenv(name, "")
	}

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("Load() error = %v, want missing variables", err)
	}
}

func TestLoadOutbox(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/sebastian")
	t.Setenv("NATS_URL", "nats://localhost:4222")
	t.Setenv("SEBASTIAN_OUTBOX_BATCH_SIZE", "25")
	t.Setenv("SEBASTIAN_OUTBOX_POLL_INTERVAL", "250ms")

	cfg, err := LoadOutbox()
	if err != nil {
		t.Fatalf("LoadOutbox() error = %v", err)
	}
	if cfg.BatchSize != 25 || cfg.PollInterval != 250*time.Millisecond {
		t.Fatalf("unexpected outbox config: %#v", cfg)
	}
}

func TestLoadOutboxRejectsInvalidBatchSize(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/sebastian")
	t.Setenv("NATS_URL", "nats://localhost:4222")
	t.Setenv("SEBASTIAN_OUTBOX_BATCH_SIZE", "0")

	_, err := LoadOutbox()
	if err == nil || !strings.Contains(err.Error(), "BATCH_SIZE") {
		t.Fatalf("LoadOutbox() error = %v, want batch size validation", err)
	}
}
