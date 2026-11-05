package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zetesis-labs/sebastian/server/internal/playback"
)

func agentStop(t *testing.T, p *stubPlayback, secret, body string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/playback/stop", strings.NewReader(body))
	req.Header.Set("X-Agent-Secret", secret)
	rec := httptest.NewRecorder()
	PlaybackAgentStop(p, "agent-secret").ServeHTTP(rec, req)
	return rec.Code
}

func TestTheAgentStopsTheMusicOfItsUnit(t *testing.T) {
	stub := &stubPlayback{}
	if code := agentStop(t, stub, "agent-secret", `{"deviceId":"e072a1f96ef0"}`); code != http.StatusNoContent {
		t.Fatalf("status = %d", code)
	}
	if len(stub.stopped) != 1 || stub.stopped[0] != "e072a1f96ef0" {
		t.Fatalf("stopped = %v", stub.stopped)
	}
}

func TestTheAgentIsToldWhenNothingIsPlaying(t *testing.T) {
	if code := agentStop(t, &stubPlayback{stopErr: playback.ErrNotFound}, "agent-secret", `{"deviceId":"e072a1f96ef0"}`); code != http.StatusNotFound {
		t.Fatalf("status = %d", code)
	}
}

func TestOnlyTheAgentCanStopTheMusicThisWay(t *testing.T) {
	stub := &stubPlayback{}
	if code := agentStop(t, stub, "wrong", `{"deviceId":"e072a1f96ef0"}`); code != http.StatusUnauthorized {
		t.Fatalf("status = %d", code)
	}
	if code := agentStop(t, stub, "agent-secret", `{}`); code != http.StatusBadRequest {
		t.Fatalf("no unit: status = %d", code)
	}
	if len(stub.stopped) != 0 {
		t.Fatalf("stopped = %v", stub.stopped)
	}
}
