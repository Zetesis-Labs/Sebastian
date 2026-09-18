// Package adoption speaks the LAN adoption protocol with a Sebastian device
// (firmware/main/adopt.c; docs/implementation/11-fleet-adoption-control-room.md §3).
//
//	→ {"t":"hello"}                                   ← {"t":"nonce","n":"<64 hex>"}
//	→ {"t":"adopt","n":"<nonce>","cfg":"<json>","mac":"<64 hex>"}
//	← {"t":"wait","why":"consent"} | {"t":"queued"}     (progress)
//	← {"t":"ok"} | {"t":"err","why":"..."}
//
// mac = HMAC-SHA256(secret, nonce + "." + cfg). The device accepts its
// organization secret or its own device secret.
package adoption

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

const (
	// Port is the device's UDP adoption listener (SEBASTIAN_ADOPT_PORT).
	Port = 5688

	helloTimeout   = 4 * time.Second
	helloAttempts  = 3
	consentTimeout = 35 * time.Second  // device waits 30 s for the button
	queuedTimeout  = 125 * time.Second // device waits up to 120 s for the conversation
	replyTimeout   = 8 * time.Second
)

// Phase is a progress notification while an adoption runs.
type Phase string

const (
	PhaseHello          Phase = "hello"
	PhaseWaitingConsent Phase = "waiting_consent"
	PhaseQueued         Phase = "queued"
)

// Sign computes the message authentication code the device verifies.
func Sign(secret, nonce, cfg string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(nonce))
	mac.Write([]byte("."))
	mac.Write([]byte(cfg))
	return hex.EncodeToString(mac.Sum(nil))
}

// Error kinds the device (or the transport) can produce.
var (
	ErrNoReply        = errors.New("no reply from the device")
	ErrDenied         = errors.New("the device rejected the secret")
	ErrNonce          = errors.New("the device rejected the nonce")
	ErrConsentTimeout = errors.New("the MUTE button was not pressed in time")
	ErrRejected       = errors.New("the device rejected the config")
)

type message struct {
	T   string `json:"t"`
	N   string `json:"n,omitempty"`
	Cfg string `json:"cfg,omitempty"`
	Mac string `json:"mac,omitempty"`
	Why string `json:"why,omitempty"`
}

// Dialer opens the UDP conversation; swapped in tests.
type Dialer func(ctx context.Context, addr string) (net.Conn, error)

func defaultDialer(ctx context.Context, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "udp", addr)
}

// Client runs adoptions against devices.
type Client struct {
	dial Dialer
}

func NewClient() *Client { return &Client{dial: defaultDialer} }

// NewClientWithDialer is for tests.
func NewClientWithDialer(dial Dialer) *Client { return &Client{dial: dial} }

// Adopt sends cfg (a sebastian.config.v1 document, as the exact JSON text the
// signature covers) to the device at ip, signed with secret. progress may be
// nil. Returns nil when the device stored the config and is restarting.
func (c *Client) Adopt(ctx context.Context, ip string, cfg, secret string, progress func(Phase)) error {
	notify := func(p Phase) {
		if progress != nil {
			progress(p)
		}
	}
	conn, err := c.dial(ctx, net.JoinHostPort(ip, fmt.Sprint(Port)))
	if err != nil {
		return fmt.Errorf("dial %s: %w", ip, err)
	}
	defer conn.Close()

	notify(PhaseHello)
	nonce, err := hello(ctx, conn)
	if err != nil {
		return err
	}
	adopt := message{T: "adopt", N: nonce, Cfg: cfg, Mac: Sign(secret, nonce, cfg)}
	if err := send(conn, adopt); err != nil {
		return err
	}

	deadline := replyTimeout
	for {
		reply, err := receive(ctx, conn, deadline)
		if err != nil {
			return err
		}
		switch reply.T {
		case "ok":
			return nil
		case "wait":
			notify(PhaseWaitingConsent)
			deadline = consentTimeout
		case "queued":
			notify(PhaseQueued)
			deadline = queuedTimeout
		case "err":
			return errorFor(reply.Why)
		default:
			// a stale nonce reply or noise: keep waiting for the real answer
		}
	}
}

func hello(ctx context.Context, conn net.Conn) (string, error) {
	for range helloAttempts {
		if err := send(conn, message{T: "hello"}); err != nil {
			return "", err
		}
		reply, err := receive(ctx, conn, helloTimeout)
		if errors.Is(err, ErrNoReply) {
			continue
		}
		if err != nil {
			return "", err
		}
		if reply.T == "nonce" && len(reply.N) == 64 {
			return reply.N, nil
		}
	}
	return "", ErrNoReply
}

func send(conn net.Conn, m message) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = conn.Write(body)
	return err
}

func receive(ctx context.Context, conn net.Conn, timeout time.Duration) (message, error) {
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetReadDeadline(deadline)
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		if ctx.Err() != nil {
			return message{}, ctx.Err()
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return message{}, ErrNoReply
		}
		return message{}, fmt.Errorf("read: %w", err)
	}
	var m message
	if err := json.Unmarshal(buf[:n], &m); err != nil {
		return message{}, fmt.Errorf("malformed reply %q", string(buf[:n]))
	}
	return m, nil
}

func errorFor(why string) error {
	switch why {
	case "auth":
		return ErrDenied
	case "nonce":
		return ErrNonce
	case "consent-timeout":
		return ErrConsentTimeout
	default:
		return fmt.Errorf("%w: %s", ErrRejected, why)
	}
}
