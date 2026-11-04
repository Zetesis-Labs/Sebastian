package playback

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
func (f *fakeUnits) Address(context.Context, string) string             { return f.ip }
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

type fakeMeetings struct{ recording bool }

func (f *fakeMeetings) InMeeting(context.Context, string) (bool, error) { return f.recording, nil }

type fakeTokens struct{ minted []string }

func (f *fakeTokens) MintSourceToken(room, identity string, _ time.Duration) (string, error) {
	f.minted = append(f.minted, room+" "+identity)
	return "source-jwt", nil
}

func adoptedAgente() device.Detail {
	return device.Detail{
		Device: device.Device{ID: "e072a1f96ef0", Adopted: true, ReportedProfile: "agente", State: device.StateAdopted},
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

type rig struct {
	svc    *Service
	clock  *clock
	cmd    *fakeCommander
	tokens *fakeTokens
}

func newRig(units *fakeUnits, meetings *fakeMeetings) rig {
	r := rig{
		clock:  &clock{t: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)},
		cmd:    &fakeCommander{done: make(chan struct{}, 4)},
		tokens: &fakeTokens{},
	}
	r.svc = NewService(units, r.cmd, meetings, r.tokens, Config{RoomPrefix: "sebastian", ServerURL: "ws://10.0.100.10:7880", TokenTTL: 10 * time.Minute}, nil)
	r.svc.now = r.clock.now
	return r
}

func reachable() *fakeUnits {
	return &fakeUnits{detail: adoptedAgente(), ip: "10.0.0.193", secret: "org"}
}

func TestStartingAPlaybackOrdersTheUnitAndHandsTheSourceAPublishToken(t *testing.T) {
	r := newRig(reachable(), &fakeMeetings{})

	got, err := r.svc.Start(context.Background(), "e072a1f96ef0")
	if err != nil {
		t.Fatal(err)
	}
	p := got.Playback
	if p.DeviceID != "e072a1f96ef0" || p.Room != "sebastian-play-"+p.ID.String() {
		t.Fatalf("playback = %+v", p)
	}
	if got.Delivery != DeliveryLAN || got.ServerURL != "ws://10.0.100.10:7880" || got.SourceToken != "source-jwt" {
		t.Fatalf("started = %+v", got)
	}
	if len(r.tokens.minted) != 1 || r.tokens.minted[0] != p.Room+" source-"+p.ID.String() {
		t.Fatalf("source token minted for %v", r.tokens.minted)
	}
	<-r.cmd.done
	if len(r.cmd.sent) != 1 || r.cmd.sent[0] != "10.0.0.193 play-start "+p.ID.String() {
		t.Fatalf("LAN order = %v", r.cmd.sent)
	}
	if h := r.svc.PendingCommand(context.Background(), "e072a1f96ef0"); h != "start:"+p.ID.String() {
		t.Fatalf("poll header = %q", h)
	}
}

func TestStartingAgainKeepsThePlaybackThatIsOn(t *testing.T) {
	r := newRig(reachable(), &fakeMeetings{})
	first, _ := r.svc.Start(context.Background(), "e072a1f96ef0")
	<-r.cmd.done

	again, err := r.svc.Start(context.Background(), "e072a1f96ef0")
	if err != nil {
		t.Fatal(err)
	}
	if again.Playback.ID != first.Playback.ID || again.Playback.Room != first.Playback.Room {
		t.Fatalf("a second start opened another playback: %v then %v", first.Playback.ID, again.Playback.ID)
	}
}

func TestWithoutALANAddressThePlaybackWaitsForThePoll(t *testing.T) {
	units := reachable()
	units.ip = ""
	r := newRig(units, &fakeMeetings{})

	got, err := r.svc.Start(context.Background(), "e072a1f96ef0")
	if err != nil || got.Delivery != DeliveryPoll {
		t.Fatalf("delivery %s err %v", got.Delivery, err)
	}
	if h := r.svc.PendingCommand(context.Background(), "e072a1f96ef0"); h != "start:"+got.Playback.ID.String() {
		t.Fatalf("poll header = %q", h)
	}
}

func TestOnlyAnAdoptedContactingAgenteUnitThatIsNotRecordingCanPlay(t *testing.T) {
	cases := map[string]struct {
		mutate   func(*fakeUnits)
		meetings *fakeMeetings
		want     error
	}{
		"not adopted here":  {mutate: func(u *fakeUnits) { u.detail.Adopted = false }, want: ErrNotAdopted},
		"micro-usb profile": {mutate: func(u *fakeUnits) { u.detail.ReportedProfile = "micro-usb" }, want: ErrProfile},
		"not contacting":    {mutate: func(u *fakeUnits) { u.detail.State = device.StateAbsent }, want: ErrAbsent},
		"recording":         {meetings: &fakeMeetings{recording: true}, want: ErrBusy},
		"unknown unit":      {mutate: func(u *fakeUnits) { u.err = device.ErrNotFound }, want: device.ErrNotFound},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			units := reachable()
			if tc.mutate != nil {
				tc.mutate(units)
			}
			meetings := tc.meetings
			if meetings == nil {
				meetings = &fakeMeetings{}
			}
			r := newRig(units, meetings)
			if _, err := r.svc.Start(context.Background(), "e072a1f96ef0"); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if h := r.svc.PendingCommand(context.Background(), "e072a1f96ef0"); h != "" {
				t.Fatalf("a refused playback must not reach the unit: %q", h)
			}
		})
	}
}

func TestStoppingOrdersTheUnitOutAndThePollRepeatsItForAWhile(t *testing.T) {
	r := newRig(reachable(), &fakeMeetings{})
	started, _ := r.svc.Start(context.Background(), "e072a1f96ef0")
	<-r.cmd.done
	id := started.Playback.ID

	if err := r.svc.Stop(context.Background(), "e072a1f96ef0"); err != nil {
		t.Fatal(err)
	}
	<-r.cmd.done
	if r.cmd.sent[1] != "10.0.0.193 play-stop "+id.String() {
		t.Fatalf("LAN orders = %v", r.cmd.sent)
	}
	if h := r.svc.PendingCommand(context.Background(), "e072a1f96ef0"); h != "stop:"+id.String() {
		t.Fatalf("poll header right after the stop = %q", h)
	}
	if _, on := r.svc.Active(context.Background(), "e072a1f96ef0"); on {
		t.Fatal("a stopped playback is still active")
	}
	r.clock.t = r.clock.t.Add(StopTTL + time.Second)
	if h := r.svc.PendingCommand(context.Background(), "e072a1f96ef0"); h != "" {
		t.Fatalf("the stop must not travel forever: %q", h)
	}
}

func TestStoppingWithNothingPlayingSaysSo(t *testing.T) {
	r := newRig(reachable(), &fakeMeetings{})
	if err := r.svc.Stop(context.Background(), "e072a1f96ef0"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestTheUnitJoinsOnlyThePlaybackThatIsOn(t *testing.T) {
	r := newRig(reachable(), &fakeMeetings{})
	started, _ := r.svc.Start(context.Background(), "e072a1f96ef0")
	<-r.cmd.done
	id := started.Playback.ID

	got, err := r.svc.Join(context.Background(), "e072a1f96ef0", "unit-secret", id)
	if err != nil || got.Room != started.Playback.Room {
		t.Fatalf("join = %+v, %v", got, err)
	}
	if _, err := r.svc.Join(context.Background(), "e072a1f96ef0", "unit-secret", uuid.Must(uuid.NewV7())); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another id: %v", err)
	}
	_ = r.svc.Stop(context.Background(), "e072a1f96ef0")
	<-r.cmd.done
	if _, err := r.svc.Join(context.Background(), "e072a1f96ef0", "unit-secret", id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a stopped playback: %v", err)
	}
}

func TestAWrongUnitSecretCannotJoin(t *testing.T) {
	units := reachable()
	r := newRig(units, &fakeMeetings{})
	started, _ := r.svc.Start(context.Background(), "e072a1f96ef0")
	<-r.cmd.done
	units.authErr = device.ErrUnauthorized

	if _, err := r.svc.Join(context.Background(), "e072a1f96ef0", "wrong", started.Playback.ID); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v", err)
	}
}

func TestTheSourceTokenNamesThePlaybackNotTheUnit(t *testing.T) {
	r := newRig(reachable(), &fakeMeetings{})
	if _, err := r.svc.Start(context.Background(), "e072a1f96ef0"); err != nil {
		t.Fatal(err)
	}
	<-r.cmd.done
	if strings.Contains(r.tokens.minted[0], "e072a1f96ef0") {
		t.Fatalf("the source must not take the unit's identity: %v", r.tokens.minted)
	}
}
