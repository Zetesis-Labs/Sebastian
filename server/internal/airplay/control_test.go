package airplay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func serveControlRoom(t *testing.T, status int, body string) (*ControlRoom, *string) {
	t.Helper()
	var method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/admin/devices/e072/playback" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("X-Admin-Secret") != "s3" {
			t.Errorf("secret = %q", r.Header.Get("X-Admin-Secret"))
		}
		method = r.Method
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewControlRoom(srv.URL, "e072", "s3"), &method
}

func TestStart(t *testing.T) {
	c, method := serveControlRoom(t, http.StatusCreated, `{"id":"p1","room":"r","startedAt":"2026-11-07T00:00:00Z","serverUrl":"ws://lk","sourceToken":"tok","delivery":"lan"}`)
	got, err := c.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if *method != http.MethodPost {
		t.Errorf("method = %s", *method)
	}
	if got.ID != "p1" || got.ServerURL != "ws://lk" || got.SourceToken != "tok" || got.Delivery != "lan" {
		t.Fatalf("got %+v", got)
	}
}

func TestStartRefused(t *testing.T) {
	c, _ := serveControlRoom(t, http.StatusConflict, `{"detail":"recording a meeting"}`)
	if _, err := c.Start(context.Background()); err == nil {
		t.Fatal("no error for a unit in a meeting")
	}
}

func TestStartOfAnUnknownUnitIsAnError(t *testing.T) {
	c, _ := serveControlRoom(t, http.StatusNotFound, `{"detail":"no such unit"}`)
	if _, err := c.Start(context.Background()); err == nil {
		t.Fatal("no error for an unknown unit")
	}
}

func TestActive(t *testing.T) {
	c, _ := serveControlRoom(t, http.StatusOK, `{"id":"p1","startedAt":"2026-11-07T00:00:00Z"}`)
	id, err := c.Active(context.Background())
	if err != nil || id != "p1" {
		t.Fatalf("id = %q, err = %v", id, err)
	}
}

func TestActiveWhenNothingIsOn(t *testing.T) {
	c, _ := serveControlRoom(t, http.StatusNotFound, `{"detail":"nothing"}`)
	id, err := c.Active(context.Background())
	if err != nil || id != "" {
		t.Fatalf("id = %q, err = %v", id, err)
	}
}

func TestActiveFailureIsNotNothingOn(t *testing.T) {
	c, _ := serveControlRoom(t, http.StatusInternalServerError, `oops`)
	if _, err := c.Active(context.Background()); err == nil {
		t.Fatal("a server error read as nothing on: it would silence the speaker")
	}
}

func TestStop(t *testing.T) {
	c, method := serveControlRoom(t, http.StatusNoContent, ``)
	if err := c.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *method != http.MethodDelete {
		t.Errorf("method = %s", *method)
	}
}

func TestStopOfWhatIsAlreadyOver(t *testing.T) {
	c, _ := serveControlRoom(t, http.StatusNotFound, `{"detail":"nothing"}`)
	if err := c.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
