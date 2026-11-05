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

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.TokenTTL != 30*time.Minute {
		t.Fatalf("TokenTTL = %s, want 30m", cfg.TokenTTL)
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
