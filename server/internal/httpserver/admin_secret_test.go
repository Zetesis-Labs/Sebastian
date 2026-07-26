package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAdminSecretMiddleware(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := adminSecretMiddleware("correct-secret", next)

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/admin/recordings", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("missing secret status = %d, want 401", unauthorized.Code)
	}

	authorized := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/admin/recordings", nil)
	request.Header.Set("X-Admin-Secret", "correct-secret")
	handler.ServeHTTP(authorized, request)
	if authorized.Code != http.StatusNoContent {
		t.Fatalf("valid secret status = %d, want 204", authorized.Code)
	}
}

func TestAdminSecretMiddlewareDoesNotProtectPublicRoutes(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	recorder := httptest.NewRecorder()
	adminSecretMiddleware("secret", next).ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/healthz", nil),
	)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("public route status = %d, want 204", recorder.Code)
	}
}
