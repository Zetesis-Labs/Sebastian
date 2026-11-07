package airplay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Started is where to publish a playback.
type Started struct {
	ID          string `json:"id"`
	ServerURL   string `json:"serverUrl"`
	SourceToken string `json:"sourceToken"`
	Delivery    string `json:"delivery"`
}

// ControlRoom is the server's playback API for this bridge's speaker.
type ControlRoom struct {
	url    string
	secret string
	client *http.Client
}

func NewControlRoom(apiURL, deviceID, adminSecret string) *ControlRoom {
	return &ControlRoom{
		url:    fmt.Sprintf("%s/v1/admin/devices/%s/playback", apiURL, deviceID),
		secret: adminSecret,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Start puts a playback on the speaker, or returns the one already on.
func (c *ControlRoom) Start(ctx context.Context) (Started, error) {
	var started Started
	_, err := c.do(ctx, http.MethodPost, &started)
	return started, err
}

// Active is the id of the playback on the speaker; empty when none is on.
func (c *ControlRoom) Active(ctx context.Context) (string, error) {
	var on struct {
		ID string `json:"id"`
	}
	notFound, err := c.do(ctx, http.MethodGet, &on)
	if notFound {
		return "", nil
	}
	return on.ID, err
}

// Stop ends the speaker's playback; one already over is not an error.
func (c *ControlRoom) Stop(ctx context.Context) error {
	notFound, err := c.do(ctx, http.MethodDelete, nil)
	if notFound {
		return nil
	}
	return err
}

func (c *ControlRoom) do(ctx context.Context, method string, out any) (notFound bool, err error) {
	req, err := http.NewRequestWithContext(ctx, method, c.url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("X-Admin-Secret", c.secret)
	res, err := c.client.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return false, err
	}
	if res.StatusCode == http.StatusNotFound {
		return true, fmt.Errorf("%s playback: not found: %s", method, body)
	}
	if res.StatusCode >= http.StatusMultipleChoices {
		return false, fmt.Errorf("%s playback: HTTP %d: %s", method, res.StatusCode, body)
	}
	if out == nil {
		return false, nil
	}
	return false, json.Unmarshal(body, out)
}
