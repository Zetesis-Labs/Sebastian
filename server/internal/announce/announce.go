// Package announce makes a speaker say something on behalf of the control
// room. An idle speaker is ordered, like a meeting, over the LAN and by its
// next poll, to open an announce session; the agent says the text in the
// voice of the speaker's profile and either closes or stays to listen. A
// speaker already in a conversation gets the text in that room instead.
//
// Announcements live in memory for TTL: the unit claims one within seconds
// of the order, and one lost in a restart is lost on purpose (the server runs
// a single replica and the caller is told it was only queued).
package announce

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/zetesis-labs/sebastian/server/internal/device"
)

const (
	// CmdStart is the LAN order; the poll carries "start:<id>" in X-Announce.
	CmdStart = "announce"
	// Topic is the data channel the agent of an open conversation listens on.
	Topic   = "sebastian.announce"
	MaxText = 500
	TTL     = 2 * time.Minute
	// agenteProfile is the firmware personality that talks (the micro-usb one
	// has no agent).
	agenteProfile = "agente"
)

var (
	ErrInvalid      = errors.New("announce: the text is empty or longer than 500 characters")
	ErrNotAdopted   = errors.New("announce: the unit is not adopted here")
	ErrProfile      = errors.New("announce: the unit is not in the agente profile")
	ErrAbsent       = errors.New("announce: the unit is not contacting")
	ErrBusy         = errors.New("announce: the unit is recording a meeting")
	ErrNotFound     = errors.New("announce: no such pending announcement for this unit")
	ErrUnauthorized = device.ErrUnauthorized
)

// Delivery says how the announcement reaches the speaker.
type Delivery string

const (
	DeliveryConversation Delivery = "conversation" // said in the open conversation
	DeliveryLAN          Delivery = "lan"          // ordered over the LAN (the poll carries it too)
	DeliveryPoll         Delivery = "poll"         // waits for the unit's next poll
)

type Announcement struct {
	ID        uuid.UUID
	DeviceID  string
	Text      string
	Reply     bool // the agent stays listening after saying it
	CreatedAt time.Time
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

// Conversations knows whether the speaker is busy with a session.
type Conversations interface {
	// Say hands the payload to the agent of the speaker's open conversation;
	// false when the speaker has none.
	Say(ctx context.Context, deviceID string, payload []byte) (bool, error)
	InMeeting(ctx context.Context, deviceID string) (bool, error)
}

type Service struct {
	units  Units
	cmd    Commander
	conv   Conversations
	logger *slog.Logger
	now    func() time.Time

	mu      sync.Mutex
	pending map[string][]Announcement // per unit, oldest first
}

func NewService(units Units, cmd Commander, conv Conversations, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{units: units, cmd: cmd, conv: conv, logger: logger, now: time.Now, pending: map[string][]Announcement{}}
}

// Normalize trims the text and checks it can be said.
func Normalize(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > MaxText {
		return "", ErrInvalid
	}
	return text, nil
}

func (s *Service) Create(ctx context.Context, deviceID, text string, reply bool) (Announcement, Delivery, error) {
	text, err := Normalize(text)
	if err != nil {
		return Announcement{}, "", err
	}
	detail, err := s.units.Get(ctx, deviceID)
	if err != nil {
		return Announcement{}, "", err
	}
	switch {
	case !detail.Adopted:
		return Announcement{}, "", ErrNotAdopted
	case detail.ReportedProfile != agenteProfile:
		return Announcement{}, "", ErrProfile
	case detail.State != device.StateAdopted:
		return Announcement{}, "", ErrAbsent
	}
	if recording, err := s.conv.InMeeting(ctx, deviceID); err != nil {
		return Announcement{}, "", err
	} else if recording {
		return Announcement{}, "", ErrBusy
	}
	a := Announcement{ID: uuid.Must(uuid.NewV7()), DeviceID: deviceID, Text: text, Reply: reply, CreatedAt: s.now().UTC()}

	payload, _ := json.Marshal(map[string]string{"text": text})
	said, err := s.conv.Say(ctx, deviceID, payload)
	if err != nil {
		s.logger.WarnContext(ctx, "announce: could not check the speaker's conversation, ordering a session", "device", deviceID, "error", err)
	}
	if said {
		s.logger.InfoContext(ctx, "announce said in the open conversation", "announcement", a.ID, "device", deviceID)
		return a, DeliveryConversation, nil
	}

	s.mu.Lock()
	s.pending[deviceID] = append(s.live(deviceID), a)
	s.mu.Unlock()
	return a, s.deliver(ctx, a), nil
}

// PendingCommand is the X-Announce header of the unit's poll: the oldest
// announcement it has not claimed yet.
func (s *Service) PendingCommand(_ context.Context, deviceID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	live := s.live(deviceID)
	if len(live) == 0 {
		return ""
	}
	return "start:" + live[0].ID.String()
}

// Claim hands the unit its announcement when it opens the announce session.
func (s *Service) Claim(ctx context.Context, deviceID, secret string, id uuid.UUID) (Announcement, error) {
	if err := s.units.Authenticate(ctx, deviceID, secret); err != nil {
		return Announcement{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	live := s.live(deviceID)
	for i, a := range live {
		if a.ID == id {
			s.pending[deviceID] = append(live[:i:i], live[i+1:]...)
			return a, nil
		}
	}
	return Announcement{}, ErrNotFound
}

// Restore puts back an announcement whose session could not be created, so
// the unit's next poll retries it while it has not expired.
func (s *Service) Restore(a Announcement) {
	s.mu.Lock()
	defer s.mu.Unlock()
	live := s.live(a.DeviceID)
	if !a.CreatedAt.After(s.now().Add(-TTL)) {
		return
	}
	for _, p := range live {
		if p.ID == a.ID {
			return
		}
	}
	s.pending[a.DeviceID] = append([]Announcement{a}, live...)
}

// live drops the expired announcements of the unit; s.mu must be held.
func (s *Service) live(deviceID string) []Announcement {
	cutoff := s.now().Add(-TTL)
	kept := s.pending[deviceID][:0]
	for _, a := range s.pending[deviceID] {
		if a.CreatedAt.After(cutoff) {
			kept = append(kept, a)
			continue
		}
		s.logger.Warn("announcement expired before the unit claimed it — never said", "announcement", a.ID, "device", deviceID, "text", a.Text)
	}
	if len(kept) == 0 {
		delete(s.pending, deviceID)
		return nil
	}
	s.pending[deviceID] = kept
	return kept
}

func (s *Service) deliver(ctx context.Context, a Announcement) Delivery {
	ip := s.units.Address(ctx, a.DeviceID)
	secret := s.units.ControlRoom().OrgSecret
	if ip == "" || secret == "" || s.cmd == nil {
		s.logger.InfoContext(ctx, "announce order deferred to the poll", "announcement", a.ID, "device", a.DeviceID, "lan", ip != "")
		return DeliveryPoll
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	go func() {
		defer cancel()
		if err := s.cmd.Command(cctx, ip, CmdStart, a.ID, secret); err != nil {
			s.logger.WarnContext(cctx, "announce order not delivered over the LAN — the poll will carry it", "announcement", a.ID, "device", a.DeviceID, "error", err)
		}
	}()
	return DeliveryLAN
}
