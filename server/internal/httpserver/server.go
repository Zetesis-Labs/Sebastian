package httpserver

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/zetesis-labs/sebastian/server/internal/api"
)

// Raw routes live outside the generated router: streaming uploads and Range
// downloads, which the strict OpenAPI handlers cannot express.
type Raw struct {
	Pattern string // e.g. "PUT /v1/meetings/{id}/audio"
	Handler http.Handler
}

func New(address string, handler api.StrictServerInterface, logger *slog.Logger, adminSecret string, raw ...Raw) (*http.Server, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /openapi.json", openAPISpecHandler(logger))
	for _, r := range raw {
		mux.Handle(r.Pattern, r.Handler)
	}
	strict := api.NewStrictHandlerWithOptions(handler, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  problemErrorHandler(logger, http.StatusBadRequest),
		ResponseErrorHandlerFunc: problemErrorHandler(logger, http.StatusInternalServerError),
	})
	router := api.HandlerWithOptions(strict, api.StdHTTPServerOptions{
		BaseRouter:       mux,
		ErrorHandlerFunc: problemErrorHandler(logger, http.StatusBadRequest),
	})
	// Kept while the existing Helm chart transitions from /health to /healthz.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})

	return &http.Server{
		Addr:              address,
		Handler:           Middleware(logger, adminSecretMiddleware(adminSecret, router)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}, nil
}

func adminSecretMiddleware(secret string, next http.Handler) http.Handler {
	expected := sha256.Sum256([]byte(secret))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/admin/") {
			next.ServeHTTP(w, r)
			return
		}
		provided := sha256.Sum256([]byte(r.Header.Get("X-Admin-Secret")))
		if subtle.ConstantTimeCompare(expected[:], provided[:]) != 1 {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(api.Problem{
				Type: "about:blank", Title: "Unauthorized", Status: http.StatusUnauthorized,
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func openAPISpecHandler(logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		spec, err := api.GetSwagger()
		if err != nil {
			logger.ErrorContext(r.Context(), "load embedded OpenAPI document", "error", err)
			problemErrorHandler(logger, http.StatusInternalServerError)(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(spec); err != nil {
			logger.ErrorContext(r.Context(), "write OpenAPI document", "error", err)
		}
	}
}

func problemErrorHandler(logger *slog.Logger, status int) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		logger.ErrorContext(r.Context(), "http contract error", "status", status, "error", err)
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(api.Problem{
			Type:   "about:blank",
			Title:  http.StatusText(status),
			Status: status,
		})
	}
}
