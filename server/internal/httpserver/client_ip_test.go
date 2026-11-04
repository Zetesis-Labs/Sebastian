package httpserver

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zetesis-labs/sebastian/server/internal/api"
)

func clientIPOf(request *http.Request) string {
	var seen string
	handler := Middleware(slog.New(slog.NewTextHandler(io.Discard, nil)), http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = api.ClientIP(r.Context())
	}))
	handler.ServeHTTP(httptest.NewRecorder(), request)
	return seen
}

func TestHandlersSeeTheAddressTheRequestCameFrom(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/devices/e072a1f96ef0/desired-profile", nil)
	request.RemoteAddr = "10.0.0.193:51234"

	if seen := clientIPOf(request); seen != "10.0.0.193" {
		t.Fatalf("client ip = %q", seen)
	}
}

// Behind an ingress the TCP peer is the proxy, not the unit; a forwarded
// header is not trusted either, so such a request tells nothing.
func TestARequestThatCameThroughAProxyHasNoClientAddress(t *testing.T) {
	for _, header := range []string{"X-Forwarded-For", "Forwarded", "X-Real-Ip"} {
		request := httptest.NewRequest(http.MethodGet, "/v1/devices/e072a1f96ef0/desired-profile", nil)
		request.RemoteAddr = "10.244.1.17:44312"
		request.Header.Set(header, "10.0.0.193")

		if seen := clientIPOf(request); seen != "" {
			t.Fatalf("%s: client ip = %q, want none", header, seen)
		}
	}
}
