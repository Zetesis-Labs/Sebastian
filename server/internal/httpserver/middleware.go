package httpserver

import (
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/google/uuid"

	"github.com/zetesis-labs/sebastian/server/internal/api"
)

func Middleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := r.Header.Get("X-Request-ID")
		if len(requestID) == 0 || len(requestID) > 128 {
			requestID = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")

		if !proxied(r) {
			if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
				r = r.WithContext(api.WithClientIP(r.Context(), host))
			} else {
				logger.WarnContext(r.Context(), "unparseable remote address", "remote_addr", r.RemoteAddr, "error", err)
			}
		}

		response := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.ErrorContext(r.Context(), "panic recovered",
					"request_id", requestID,
					"panic", recovered,
					"stack", string(debug.Stack()),
				)
				if !response.wroteHeader {
					http.Error(response, "internal server error", http.StatusInternalServerError)
				}
			}
			logger.InfoContext(r.Context(), "http request",
				"request_id", requestID,
				"method", r.Method,
				"path", r.URL.Path,
				"status", response.status,
				"duration", time.Since(started),
			)
		}()
		next.ServeHTTP(response, r)
	})
}

// proxied: the TCP peer is a proxy, not the client, and what it forwards is
// not trusted, so the request tells nothing about where the client is.
func proxied(r *http.Request) bool {
	return r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("Forwarded") != "" || r.Header.Get("X-Real-Ip") != ""
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
