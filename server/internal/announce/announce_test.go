package announce

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zetesis-labs/sebastian/server/internal/device"
)

type fakeUnits struct {
	detail  device.Detail
	err     error
	ip      string
	secret  string
	authErr error
}

func (f *fakeUnits) Get(context.Context, string) (device.Detail, error) { return f.detail, f.err }
func (f *fakeUnits) Authenticate(context.Context, string, string) error { return f.authErr }
func (f *fakeUnits) Address(string) string                              { return f.ip }
func (f *fakeUnits) ControlRoom() device.ControlRoom                    { return device.ControlRoom{OrgSecret: f.secret} }

type fakeCommander struct {
	mu   sync.Mutex
	sent []string
	done chan struct{}
}

func (f *fakeCommander) Command(_ context.Context, ip, cmd string, id uuid.UUID, _ string) error {
	f.mu.Lock()
	f.sent = append(f.sent, ip+" "+cmd+" "+id.String())
	f.mu.Unlock()
	if f.done != nil {
		f.done <- struct{}{}
	}
	return nil
}

type fakeConversations struct {
	open    bool
	said    []string
	sayErr  error
	meeting bool
}

func (f *fakeConversations) Say(_ context.Context, _ string, payload []byte) (bool, error) {
	if f.sayErr != nil {
		return false, f.sayErr
	}
	if !f.open {
		return false, nil
	}
	f.said = append(f.said, string(payload))
	return true, nil
}

func (f *fakeConversations) InMeeting(context.Context, string) (bool, error) { return f.meeting, nil }

func adoptedAgente() device.Detail {
	return device.Detail{
		Device: device.Device{ID: "e072a1f96ef0", Adopted: true, ReportedProfile: "agente", State: device.StateAdopted},
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newService(units *fakeUnits, cmd *fakeCommander, conv *fakeConversations) (*Service, *clock) {
	c := &clock{t: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	s := NewService(units, cmd, conv, nil)
	s.now = c.now
	return s, c
}

func TestAnIdleSpeakerGetsTheOrderOverTheLANAndKeepsItForThePoll(t *testing.T) {
	cmd := &fakeCommander{done: make(chan struct{}, 1)}
	s, _ := newService(&fakeUnits{detail: adoptedAgente(), ip: "10.0.120.50", secret: "org"}, cmd, &fakeConversations{})

	a, via, err := s.Create(context.Background(), "e072a1f96ef0", "  La lavadora ha terminado  ", true)
	if err != nil {
		t.Fatal(err)
	}
	if via != DeliveryLAN || a.Text != "La lavadora ha terminado" || !a.Reply {
		t.Fatalf("got %+v via %s", a, via)
	}
	<-cmd.done
	if len(cmd.sent) != 1 || cmd.sent[0] != "10.0.120.50 announce "+a.ID.String() {
		t.Fatalf("LAN order = %v", cmd.sent)
	}
	if got := s.PendingCommand(context.Background(), "e072a1f96ef0"); got != "start:"+a.ID.String() {
		t.Fatalf("poll header = %q", got)
	}
}

func TestWithoutALANAddressTheAnnouncementWaitsForThePoll(t *testing.T) {
	cmd := &fakeCommander{}
	s, _ := newService(&fakeUnits{detail: adoptedAgente(), secret: "org"}, cmd, &fakeConversations{})

	a, via, err := s.Create(context.Background(), "e072a1f96ef0", "Hola", false)
	if err != nil || via != DeliveryPoll {
		t.Fatalf("via %s err %v", via, err)
	}
	if got := s.PendingCommand(context.Background(), "e072a1f96ef0"); got != "start:"+a.ID.String() {
		t.Fatalf("poll header = %q", got)
	}
}

func TestAnOpenConversationSaysItThereAndNothingIsOrdered(t *testing.T) {
	cmd := &fakeCommander{}
	conv := &fakeConversations{open: true}
	s, _ := newService(&fakeUnits{detail: adoptedAgente(), ip: "10.0.120.50", secret: "org"}, cmd, conv)

	_, via, err := s.Create(context.Background(), "e072a1f96ef0", "Ya ha llegado el paquete", false)
	if err != nil || via != DeliveryConversation {
		t.Fatalf("via %s err %v", via, err)
	}
	if len(conv.said) != 1 || conv.said[0] != `{"text":"Ya ha llegado el paquete"}` {
		t.Fatalf("data payload = %v", conv.said)
	}
	if len(cmd.sent) != 0 || s.PendingCommand(context.Background(), "e072a1f96ef0") != "" {
		t.Fatal("an announcement said in the conversation must not also be ordered")
	}
}

func TestTheUnitClaimsItsAnnouncementOnceWhenItOpensTheSession(t *testing.T) {
	s, _ := newService(&fakeUnits{detail: adoptedAgente(), secret: "org"}, &fakeCommander{}, &fakeConversations{})
	a, _, _ := s.Create(context.Background(), "e072a1f96ef0", "Hola", true)

	got, err := s.Claim(context.Background(), "e072a1f96ef0", "secret", a.ID)
	if err != nil || got != a {
		t.Fatalf("claim = %+v, %v", got, err)
	}
	if _, err := s.Claim(context.Background(), "e072a1f96ef0", "secret", a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second claim = %v, want ErrNotFound", err)
	}
	if s.PendingCommand(context.Background(), "e072a1f96ef0") != "" {
		t.Fatal("a claimed announcement must leave the poll header")
	}
}

func TestOnlyTheRightUnitCanClaimIt(t *testing.T) {
	units := &fakeUnits{detail: adoptedAgente(), secret: "org"}
	s, _ := newService(units, &fakeCommander{}, &fakeConversations{})
	a, _, _ := s.Create(context.Background(), "e072a1f96ef0", "Hola", false)

	if _, err := s.Claim(context.Background(), "otra-placa", "secret", a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other unit claim = %v", err)
	}
	units.authErr = device.ErrUnauthorized
	if _, err := s.Claim(context.Background(), "e072a1f96ef0", "bad", a.ID); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad secret claim = %v", err)
	}
}

func TestAnnouncementsExpire(t *testing.T) {
	s, c := newService(&fakeUnits{detail: adoptedAgente(), secret: "org"}, &fakeCommander{}, &fakeConversations{})
	a, _, _ := s.Create(context.Background(), "e072a1f96ef0", "Hola", false)

	c.t = c.t.Add(TTL + time.Second)
	if s.PendingCommand(context.Background(), "e072a1f96ef0") != "" {
		t.Fatal("an expired announcement must not be ordered")
	}
	if _, err := s.Claim(context.Background(), "e072a1f96ef0", "secret", a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired claim = %v", err)
	}
}

func TestPendingAnnouncementsAreOrderedOldestFirst(t *testing.T) {
	s, c := newService(&fakeUnits{detail: adoptedAgente(), secret: "org"}, &fakeCommander{}, &fakeConversations{})
	first, _, _ := s.Create(context.Background(), "e072a1f96ef0", "Uno", false)
	c.t = c.t.Add(time.Second)
	second, _, _ := s.Create(context.Background(), "e072a1f96ef0", "Dos", false)

	if got := s.PendingCommand(context.Background(), "e072a1f96ef0"); got != "start:"+first.ID.String() {
		t.Fatalf("first = %q", got)
	}
	_, _ = s.Claim(context.Background(), "e072a1f96ef0", "secret", first.ID)
	if got := s.PendingCommand(context.Background(), "e072a1f96ef0"); got != "start:"+second.ID.String() {
		t.Fatalf("then = %q", got)
	}
}

func TestTheTextMustBeSayable(t *testing.T) {
	s, _ := newService(&fakeUnits{detail: adoptedAgente(), secret: "org"}, &fakeCommander{}, &fakeConversations{})
	for _, text := range []string{"", "   ", strings.Repeat("a", MaxText+1)} {
		if _, _, err := s.Create(context.Background(), "e072a1f96ef0", text, false); !errors.Is(err, ErrInvalid) {
			t.Errorf("text of %d runes: %v, want ErrInvalid", len([]rune(text)), err)
		}
	}
	if _, _, err := s.Create(context.Background(), "e072a1f96ef0", strings.Repeat("ñ", MaxText), false); err != nil {
		t.Errorf("%d multibyte runes must be accepted: %v", MaxText, err)
	}
}

func TestOnlyAnAdoptedContactingAgenteUnitThatIsNotRecordingCanAnnounce(t *testing.T) {
	cases := map[string]struct {
		detail  func(device.Detail) device.Detail
		meeting bool
		want    error
	}{
		"not adopted":    {detail: func(d device.Detail) device.Detail { d.Adopted = false; return d }, want: ErrNotAdopted},
		"usb-mic":        {detail: func(d device.Detail) device.Detail { d.ReportedProfile = "micro-usb"; return d }, want: ErrProfile},
		"not contacting": {detail: func(d device.Detail) device.Detail { d.State = device.StateAbsent; return d }, want: ErrAbsent},
		"recording":      {detail: func(d device.Detail) device.Detail { return d }, meeting: true, want: ErrBusy},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			conv := &fakeConversations{meeting: tc.meeting}
			s, _ := newService(&fakeUnits{detail: tc.detail(adoptedAgente()), secret: "org"}, &fakeCommander{}, conv)
			if _, _, err := s.Create(context.Background(), "e072a1f96ef0", "Hola", false); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestAnAnnouncementWhoseSessionFailedGoesBackForTheNextPoll(t *testing.T) {
	s, c := newService(&fakeUnits{detail: adoptedAgente(), secret: "org"}, &fakeCommander{}, &fakeConversations{})
	a, _, _ := s.Create(context.Background(), "e072a1f96ef0", "Hola", false)
	claimed, _ := s.Claim(context.Background(), "e072a1f96ef0", "secret", a.ID)

	s.Restore(claimed)
	if got := s.PendingCommand(context.Background(), "e072a1f96ef0"); got != "start:"+a.ID.String() {
		t.Fatalf("restored = %q", got)
	}
	s.Restore(claimed)
	if got, _ := s.Claim(context.Background(), "e072a1f96ef0", "secret", a.ID); got != a {
		t.Fatal("restoring twice must keep a single copy")
	}

	c.t = c.t.Add(TTL + time.Second)
	s.Restore(claimed)
	if s.PendingCommand(context.Background(), "e072a1f96ef0") != "" {
		t.Fatal("an expired announcement is not restored")
	}
}
