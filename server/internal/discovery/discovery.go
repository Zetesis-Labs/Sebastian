// Package discovery watches the LAN for Sebastian devices announcing
// _sebastian._tcp over mDNS (firmware/main/announce.c) and keeps an in-memory
// "seen on the network" table. It is a live view, not persistence: an entry
// expires when the unit stops announcing.
package discovery

import (
	"context"
	"log/slog"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/brutella/dnssd"
)

const (
	serviceType = "_sebastian._tcp.local."
	// browseCycle restarts the browse so TXT updates (err, cfg, cr) are
	// re-read: dnssd reports an instance once, not on every change.
	browseCycle = 15 * time.Second
	// expireAfter drops a unit that stopped answering (spec RF-21: 60 s).
	expireAfter = 60 * time.Second
)

// Seen is one announced unit.
type Seen struct {
	ID            string
	IP            string
	Port          int
	Firmware      string
	Profile       string
	ControlRoom   string
	LastError     string
	ConfigVersion string
	SeenAt        time.Time
}

// Browser is what the fleet service consumes; the real one browses mDNS, tests
// provide a static one.
type Browser interface {
	Snapshot() []Seen
	Enabled() bool
}

// Static is a Browser for tests and for control rooms without LAN access.
type Static struct {
	Items []Seen
	On    bool
}

func (s Static) Snapshot() []Seen { return s.Items }
func (s Static) Enabled() bool    { return s.On }

// MDNS browses continuously once Run is called.
type MDNS struct {
	logger *slog.Logger
	now    func() time.Time

	mu   sync.Mutex
	seen map[string]Seen
}

func NewMDNS(logger *slog.Logger) *MDNS {
	return &MDNS{logger: logger, now: time.Now, seen: map[string]Seen{}}
}

func (m *MDNS) Enabled() bool { return true }

// Run blocks until ctx is done, restarting the browse every browseCycle.
func (m *MDNS) Run(ctx context.Context) {
	for ctx.Err() == nil {
		cycle, cancel := context.WithTimeout(ctx, browseCycle)
		err := dnssd.LookupType(cycle, serviceType, m.add, func(dnssd.BrowseEntry) {})
		cancel()
		if err != nil && ctx.Err() == nil {
			m.logger.Warn("mdns browse failed — LAN discovery off until it recovers", "error", err)
			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Second):
			}
		}
	}
}

func (m *MDNS) add(e dnssd.BrowseEntry) {
	id := e.Text["id"]
	if id == "" {
		return
	}
	entry := Seen{
		ID:            id,
		Port:          e.Port,
		Firmware:      e.Text["fw"],
		Profile:       e.Text["prof"],
		ControlRoom:   e.Text["cr"],
		LastError:     e.Text["err"],
		ConfigVersion: e.Text["cfg"],
		SeenAt:        m.now(),
	}
	for _, ip := range e.IPs {
		if v4 := ip.To4(); v4 != nil {
			entry.IP = v4.String()
			break
		}
	}
	if entry.IP == "" && len(e.IPs) > 0 {
		entry.IP = e.IPs[0].String()
	}
	m.mu.Lock()
	m.seen[id] = entry
	m.mu.Unlock()
}

// Snapshot returns the units seen within expireAfter, ordered by id.
func (m *MDNS) Snapshot() []Seen {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := m.now().Add(-expireAfter)
	out := make([]Seen, 0, len(m.seen))
	for id, s := range m.seen {
		if s.SeenAt.Before(cutoff) {
			delete(m.seen, id)
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Reachable reports whether ip parses as an address we could send a datagram to.
func Reachable(ip string) bool {
	parsed := net.ParseIP(ip)
	return parsed != nil && !parsed.IsUnspecified()
}
