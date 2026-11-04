// Package playback makes a speaker play audio that is not its agent: a source
// (a test script today, the AirPlay bridge later) publishes into a room the
// unit joins only to listen. The unit is ordered, like a meeting, over the
// LAN and by its next poll; it keeps the room open while it idles, leaves it
// to attend the wake word, an announcement or a meeting, and comes back while
// the playback is still on.
//
// Playbacks live in memory, like announcements: the server runs a single
// replica, and a playback lost in a restart is simply over — the unit stops
// hearing it in its poll and goes back to idle.
package playback

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/zetesis-labs/sebastian/server/internal/device"
)

const (
	// CmdStart and CmdStop are the LAN orders; the poll carries
	// "start:<id>" or "stop:<id>" in X-Playback.
	CmdStart = "play-start"
	CmdStop  = "play-stop"
	// StopTTL is how long the poll keeps telling the unit to leave a stopped
	// playback, for a unit that missed the LAN order.
	StopTTL = 2 * time.Minute
	// agenteProfile is the firmware personality with LiveKit sessions (the
	// micro-usb one has none).
	agenteProfile = "agente"
)

var (
	ErrNotAdopted   = errors.New("playback: the unit is not adopted here")
	ErrProfile      = errors.New("playback: the unit is not in the agente profile")
	ErrAbsent       = errors.New("playback: the unit is not contacting")
	ErrBusy         = errors.New("playback: the unit is recording a meeting")
	ErrNotFound     = errors.New("playback: no such playback on for this unit")
	ErrUnauthorized = device.ErrUnauthorized
)

// Delivery says how the order reaches the speaker.
type Delivery string

const (
	DeliveryLAN  Delivery = "lan"  // ordered over the LAN (the poll carries it too)
	DeliveryPoll Delivery = "poll" // waits for the unit's next poll
)

type Playback struct {
	ID        uuid.UUID
	DeviceID  string
	Room      string
	StartedAt time.Time
}

// Started is what the caller needs to feed the playback: where to publish.
type Started struct {
	Playback    Playback
	Delivery    Delivery
	ServerURL   string
	SourceToken string
}

type Units interface {
	Get(ctx context.Context, id string) (device.Detail, error)
	Authenticate(ctx context.Context, id, secret string) error
	Address(ctx context.Context, id string) string
	ControlRoom() device.ControlRoom
}

// Commander delivers a signed order over the LAN (adoption.Client).
type Commander interface {
	Command(ctx context.Context, ip, cmd string, id uuid.UUID, secret string) error
}

type Meetings interface {
	InMeeting(ctx context.Context, deviceID string) (bool, error)
}

// Tokens mints the source's grant: it may publish into the room, not listen.
type Tokens interface {
	MintSourceToken(room, identity string, ttl time.Duration) (string, error)
}

type Config struct {
	RoomPrefix string
	ServerURL  string // the LiveKit URL the source connects to
	TokenTTL   time.Duration
}

type stopped struct {
	id uuid.UUID
	at time.Time
}

type Service struct {
	units    Units
	cmd      Commander
	meetings Meetings
	tokens   Tokens
	cfg      Config
	logger   *slog.Logger
	now      func() time.Time

	mu      sync.Mutex
	active  map[string]Playback // per unit
	stopped map[string]stopped  // per unit, the last stop while it still travels in the poll
}

func NewService(units Units, cmd Commander, meetings Meetings, tokens Tokens, cfg Config, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		units: units, cmd: cmd, meetings: meetings, tokens: tokens, cfg: cfg, logger: logger, now: time.Now,
		active: map[string]Playback{}, stopped: map[string]stopped{},
	}
}

// Start puts a playback on the unit; one already on is kept, so a source
// that calls Start on every play does not open a room per track.
func (s *Service) Start(ctx context.Context, deviceID string) (Started, error) {
	detail, err := s.units.Get(ctx, deviceID)
	if err != nil {
		return Started{}, err
	}
	switch {
	case !detail.Adopted:
		return Started{}, ErrNotAdopted
	case detail.ReportedProfile != agenteProfile:
		return Started{}, ErrProfile
	case detail.State != device.StateAdopted:
		return Started{}, ErrAbsent
	}
	if recording, err := s.meetings.InMeeting(ctx, deviceID); err != nil {
		return Started{}, err
	} else if recording {
		return Started{}, ErrBusy
	}

	s.mu.Lock()
	p, on := s.active[deviceID]
	if !on {
		id := uuid.Must(uuid.NewV7())
		p = Playback{ID: id, DeviceID: deviceID, Room: fmt.Sprintf("%s-play-%s", s.cfg.RoomPrefix, id), StartedAt: s.now().UTC()}
	}
	s.mu.Unlock()

	token, err := s.tokens.MintSourceToken(p.Room, "source-"+p.ID.String(), s.cfg.TokenTTL)
	if err != nil {
		return Started{}, fmt.Errorf("playback: mint source token: %w", err)
	}

	s.mu.Lock()
	if current, raced := s.active[deviceID]; raced && current.ID != p.ID {
		s.mu.Unlock()
		return s.Start(ctx, deviceID)
	}
	s.active[deviceID] = p
	delete(s.stopped, deviceID)
	s.mu.Unlock()

	if !on {
		s.logger.InfoContext(ctx, "playback started", "playback", p.ID, "device", deviceID, "room", p.Room)
	}
	return Started{Playback: p, Delivery: s.deliver(ctx, deviceID, CmdStart, p.ID), ServerURL: s.cfg.ServerURL, SourceToken: token}, nil
}

// Stop ends the unit's playback; ErrNotFound when none is on.
func (s *Service) Stop(ctx context.Context, deviceID string) error {
	s.mu.Lock()
	p, on := s.active[deviceID]
	if on {
		delete(s.active, deviceID)
		s.stopped[deviceID] = stopped{id: p.ID, at: s.now()}
	}
	s.mu.Unlock()
	if !on {
		return ErrNotFound
	}
	s.logger.InfoContext(ctx, "playback stopped", "playback", p.ID, "device", deviceID)
	s.deliver(ctx, deviceID, CmdStop, p.ID)
	return nil
}

// Active is the playback on for the unit, if any.
func (s *Service) Active(_ context.Context, deviceID string) (Playback, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, on := s.active[deviceID]
	return p, on
}

// PendingCommand is the X-Playback header of the unit's poll: the playback to
// be in, or the one it must leave while that stop still travels.
func (s *Service) PendingCommand(_ context.Context, deviceID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, on := s.active[deviceID]; on {
		return "start:" + p.ID.String()
	}
	st, ok := s.stopped[deviceID]
	if !ok {
		return ""
	}
	if s.now().Sub(st.at) > StopTTL {
		delete(s.stopped, deviceID)
		return ""
	}
	return "stop:" + st.id.String()
}

// Join is the unit opening its listening session: only into the playback on.
func (s *Service) Join(ctx context.Context, deviceID, secret string, id uuid.UUID) (Playback, error) {
	if err := s.units.Authenticate(ctx, deviceID, secret); err != nil {
		return Playback{}, err
	}
	p, on := s.Active(ctx, deviceID)
	if !on || p.ID != id {
		return Playback{}, ErrNotFound
	}
	return p, nil
}

func (s *Service) deliver(ctx context.Context, deviceID, cmd string, id uuid.UUID) Delivery {
	ip := s.units.Address(ctx, deviceID)
	secret := s.units.ControlRoom().OrgSecret
	if ip == "" || secret == "" || s.cmd == nil {
		s.logger.InfoContext(ctx, "playback order deferred to the poll", "order", cmd, "playback", id, "device", deviceID, "lan", ip != "")
		return DeliveryPoll
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	go func() {
		defer cancel()
		if err := s.cmd.Command(cctx, ip, cmd, id, secret); err != nil {
			s.logger.WarnContext(cctx, "playback order not delivered over the LAN — the poll will carry it", "order", cmd, "playback", id, "device", deviceID, "error", err)
		}
	}()
	return DeliveryLAN
}
