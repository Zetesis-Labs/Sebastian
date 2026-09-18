package adoption

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeDevice implements adopt.c's side of the protocol on a loopback UDP port.
type fakeDevice struct {
	mu         sync.Mutex // the serve goroutine and the test share the fields below
	t          *testing.T
	conn       net.PacketConn
	orgSecret  string
	devSecret  string
	factory    bool // no secrets: needs consent
	pressMute  bool // consent granted
	busy       bool // conversation in progress: queued first
	nonce      string
	gotCfg     string
	rejectWith string
}

func newFakeDevice(t *testing.T) *fakeDevice {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDevice{t: t, conn: conn, orgSecret: "org-secret", devSecret: "dev-secret"}
	go d.serve()
	t.Cleanup(func() { conn.Close() })
	return d
}

func (d *fakeDevice) addr() string { return d.conn.LocalAddr().String() }

func (d *fakeDevice) set(fn func(*fakeDevice)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	fn(d)
}

func (d *fakeDevice) storedCfg() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.gotCfg
}

func (d *fakeDevice) reply(to net.Addr, m message) {
	body, _ := json.Marshal(m)
	_, _ = d.conn.WriteTo(body, to)
}

func (d *fakeDevice) serve() {
	buf := make([]byte, 4096)
	for {
		n, from, err := d.conn.ReadFrom(buf)
		if err != nil {
			return
		}
		var m message
		if err := json.Unmarshal(buf[:n], &m); err != nil {
			continue
		}
		d.mu.Lock()
		d.handle(from, m)
		d.mu.Unlock()
	}
}

func (d *fakeDevice) handle(from net.Addr, m message) {
	{
		switch m.T {
		case "hello":
			d.nonce = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
			d.reply(from, message{T: "nonce", N: d.nonce})
		case "adopt":
			if m.N != d.nonce {
				d.reply(from, message{T: "err", Why: "nonce"})
				return
			}
			if d.factory {
				d.reply(from, message{T: "wait", Why: "consent"})
				if !d.pressMute {
					time.Sleep(50 * time.Millisecond)
					d.reply(from, message{T: "err", Why: "consent-timeout"})
					return
				}
			} else if m.Mac != Sign(d.orgSecret, m.N, m.Cfg) && m.Mac != Sign(d.devSecret, m.N, m.Cfg) {
				d.reply(from, message{T: "err", Why: "auth"})
				return
			}
			if d.busy {
				d.reply(from, message{T: "queued"})
				time.Sleep(30 * time.Millisecond)
			}
			if d.rejectWith != "" {
				d.reply(from, message{T: "err", Why: d.rejectWith})
				return
			}
			d.gotCfg = m.Cfg
			d.reply(from, message{T: "ok"})
		}
	}
}

// dialTo ignores the ip the client computed and talks to the fake device.
func dialTo(addr string) Dialer {
	return func(ctx context.Context, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "udp", addr)
	}
}

func TestSignIsDeterministicHMACOverNonceDotConfig(t *testing.T) {
	a := Sign("s", "n", `{"schema":"sebastian.config.v1"}`)
	b := Sign("s", "n", `{"schema":"sebastian.config.v1"}`)
	if a != b || len(a) != 64 {
		t.Fatalf("unexpected signature %q / %q", a, b)
	}
	if Sign("s", "n", `{}`) == a || Sign("other", "n", `{"schema":"sebastian.config.v1"}`) == a {
		t.Fatal("signature must depend on config and secret")
	}
}

func TestAdoptWithOrganizationSecret(t *testing.T) {
	dev := newFakeDevice(t)
	client := NewClientWithDialer(dialTo(dev.addr()))
	var phases []Phase
	cfg := `{"schema":"sebastian.config.v1","livekit":{"tokenServerUrl":"http://10.0.0.188:8787/token"}}`
	if err := client.Adopt(context.Background(), "10.0.0.125", cfg, "org-secret", func(p Phase) { phases = append(phases, p) }); err != nil {
		t.Fatal(err)
	}
	if dev.storedCfg() != cfg {
		t.Fatalf("device stored %q", dev.storedCfg())
	}
	if len(phases) != 1 || phases[0] != PhaseHello {
		t.Fatalf("phases %v", phases)
	}
}

func TestAdoptWithDeviceSecretAndQueuedConversation(t *testing.T) {
	dev := newFakeDevice(t)
	dev.set(func(d *fakeDevice) { d.busy = true })
	client := NewClientWithDialer(dialTo(dev.addr()))
	var phases []Phase
	if err := client.Adopt(context.Background(), "x", `{"schema":"sebastian.config.v1"}`, "dev-secret", func(p Phase) { phases = append(phases, p) }); err != nil {
		t.Fatal(err)
	}
	if len(phases) != 2 || phases[1] != PhaseQueued {
		t.Fatalf("phases %v", phases)
	}
}

func TestAdoptDeniedWithWrongSecret(t *testing.T) {
	dev := newFakeDevice(t)
	client := NewClientWithDialer(dialTo(dev.addr()))
	err := client.Adopt(context.Background(), "x", `{"schema":"sebastian.config.v1"}`, "intruder", nil)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("expected ErrDenied, got %v", err)
	}
	if dev.storedCfg() != "" {
		t.Fatal("device must not store a config it rejected")
	}
}

func TestFactoryUnitNeedsConsent(t *testing.T) {
	dev := newFakeDevice(t)
	dev.set(func(d *fakeDevice) { d.factory = true })
	client := NewClientWithDialer(dialTo(dev.addr()))
	var phases []Phase
	err := client.Adopt(context.Background(), "x", `{"schema":"sebastian.config.v1"}`, "anything", func(p Phase) { phases = append(phases, p) })
	if !errors.Is(err, ErrConsentTimeout) {
		t.Fatalf("expected ErrConsentTimeout, got %v", err)
	}
	if len(phases) != 2 || phases[1] != PhaseWaitingConsent {
		t.Fatalf("phases %v", phases)
	}

	dev.set(func(d *fakeDevice) { d.pressMute = true })
	if err := client.Adopt(context.Background(), "x", `{"schema":"sebastian.config.v1"}`, "anything", nil); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptReportsStoreRejection(t *testing.T) {
	dev := newFakeDevice(t)
	dev.set(func(d *fakeDevice) { d.rejectWith = "wifi" })
	client := NewClientWithDialer(dialTo(dev.addr()))
	err := client.Adopt(context.Background(), "x", `{"schema":"sebastian.config.v1"}`, "org-secret", nil)
	if !errors.Is(err, ErrRejected) || err.Error() != "the device rejected the config: wifi" {
		t.Fatalf("got %v", err)
	}
}

func TestAdoptNoReply(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	client := NewClientWithDialer(dialTo(conn.LocalAddr().String()))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err = client.Adopt(ctx, "x", `{}`, "s", nil)
	if err == nil {
		t.Fatal("expected an error")
	}
}
