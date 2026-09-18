package meeting

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zetesis-labs/sebastian/server/internal/device"
)

// ── doubles ────────────────────────────────────────────────────────────────

type fakeStore struct {
	mu     sync.Mutex
	rows   map[uuid.UUID]Meeting
	events []string
}

func newFakeStore() *fakeStore { return &fakeStore{rows: map[uuid.UUID]Meeting{}} }

func (f *fakeStore) Insert(_ context.Context, m Meeting) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[m.ID] = m
	f.events = append(f.events, "meeting.requested")
	return nil
}

func (f *fakeStore) Update(_ context.Context, m Meeting, event string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[m.ID] = m
	if event != "" {
		f.events = append(f.events, event)
	}
	return nil
}

func (f *fakeStore) Get(_ context.Context, id uuid.UUID) (Meeting, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.rows[id]
	if !ok {
		return Meeting{}, ErrNotFound
	}
	return m, nil
}

func (f *fakeStore) Active(_ context.Context, deviceID string) (Meeting, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.rows {
		if m.DeviceID == deviceID && InProgress(m.State) {
			return m, true, nil
		}
	}
	return Meeting{}, false, nil
}

func (f *fakeStore) InProgress(context.Context) ([]Meeting, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Meeting
	for _, m := range f.rows {
		if InProgress(m.State) {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *fakeStore) List(_ context.Context, flt Filter) ([]Meeting, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Meeting
	for _, m := range f.rows {
		if (flt.DeviceID == "" || m.DeviceID == flt.DeviceID) && (flt.State == "" || m.State == flt.State) {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *fakeStore) EndedBefore(_ context.Context, before time.Time, _ int) ([]Meeting, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Meeting
	for _, m := range f.rows {
		if Final(m.State) && !m.Keep && m.DeletedAt.IsZero() && m.EndedAt.Before(before) {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *fakeStore) Drop(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rows, id)
	f.events = append(f.events, "dropped")
	return nil
}

func (f *fakeStore) Delete(_ context.Context, id uuid.UUID, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.rows[id]
	m.DeletedAt, m.AudioPath = at, ""
	f.rows[id] = m
	f.events = append(f.events, "meeting.deleted")
	return nil
}

type fakeUnits struct {
	detail device.Detail
	ip     string
	secret string
	org    string
}

func (u *fakeUnits) Get(_ context.Context, id string) (device.Detail, error) {
	if id != u.detail.ID {
		return device.Detail{}, device.ErrNotFound
	}
	return u.detail, nil
}

func (u *fakeUnits) Authenticate(_ context.Context, id, secret string) error {
	if id != u.detail.ID || secret != u.secret {
		return device.ErrUnauthorized
	}
	return nil
}
func (u *fakeUnits) Address(string) string           { return u.ip }
func (u *fakeUnits) ControlRoom() device.ControlRoom { return device.ControlRoom{OrgSecret: u.org} }

type sent struct {
	ip, cmd, secret string
	id              uuid.UUID
}

type fakeCommander struct {
	mu   sync.Mutex
	got  []sent
	err  error
	done chan struct{}
}

func (c *fakeCommander) Command(_ context.Context, ip, cmd string, id uuid.UUID, secret string) error {
	c.mu.Lock()
	c.got = append(c.got, sent{ip, cmd, secret, id})
	c.mu.Unlock()
	if c.done != nil {
		c.done <- struct{}{}
	}
	return c.err
}

func (c *fakeCommander) wait(t *testing.T) sent {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
		t.Fatal("no command reached the unit")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.got[len(c.got)-1]
}

type harness struct {
	s     *Service
	store *fakeStore
	units *fakeUnits
	cmd   *fakeCommander
	clock time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{store: newFakeStore(), cmd: &fakeCommander{done: make(chan struct{}, 8)}, clock: t0}
	h.units = &fakeUnits{
		detail: device.Detail{Device: device.Device{ID: "68ee", Adopted: true, ReportedProfile: "agente", State: device.StateAdopted}},
		ip:     "10.0.0.125", secret: "unit-secret", org: "org-secret",
	}
	h.s = NewService(h.store, h.units, h.cmd, filepath.Join(t.TempDir(), "meetings"), Limits{MaxDuration: 3 * time.Hour}, nil)
	h.s.now = func() time.Time { return h.clock }
	return h
}

func (h *harness) advance(d time.Duration) { h.clock = h.clock.Add(d) }

func (h *harness) started(t *testing.T) Meeting {
	t.Helper()
	m, err := h.s.Start(context.Background(), "68ee", OriginDashboard)
	if err != nil {
		t.Fatal(err)
	}
	h.cmd.wait(t)
	h.advance(2 * time.Second)
	if err := h.s.Report(context.Background(), "68ee", "unit-secret", m.ID, "recording", ""); err != nil {
		t.Fatal(err)
	}
	m, _ = h.s.Get(context.Background(), m.ID)
	return m
}

// ── T-A2: who may start ────────────────────────────────────────────────────

func TestStartRefusesTheWrongUnitAndASecondMeeting(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.units.detail.ReportedProfile = "micro-usb"
	if _, err := h.s.Start(ctx, "68ee", OriginDashboard); !errors.Is(err, ErrProfile) {
		t.Fatalf("micro-usb must be refused (RM-44): %v", err)
	}
	h.units.detail.ReportedProfile = "agente"
	h.units.detail.Adopted = false
	if _, err := h.s.Start(ctx, "68ee", OriginDashboard); !errors.Is(err, ErrNotAdopted) {
		t.Fatalf("a unit not adopted here must be refused (RM-07): %v", err)
	}
	h.units.detail.Adopted = true
	h.units.detail.State = device.StateAbsent
	if _, err := h.s.Start(ctx, "68ee", OriginDashboard); !errors.Is(err, ErrAbsent) {
		t.Fatalf("a unit that is not contacting cannot be asked (RM-03): %v", err)
	}
	h.units.detail.State = device.StateAdopted
	if _, err := h.s.StartFromUnit(ctx, "68ee", "wrong"); !errors.Is(err, device.ErrUnauthorized) {
		t.Fatalf("a gesture start needs the unit's secret: %v", err)
	}
	first, err := h.s.StartFromUnit(ctx, "68ee", "unit-secret")
	if err != nil || first.State != StateRequested || first.RequestedBy != OriginGesture {
		t.Fatalf("start: %+v %v", first, err)
	}
	if len(h.cmd.got) != 0 {
		t.Fatal("a gesture start is not ordered back to the unit")
	}
	if _, err := h.s.Start(ctx, "68ee", OriginDashboard); !errors.Is(err, ErrBusy) {
		t.Fatalf("one meeting per unit (RM-05): %v", err)
	}
	if h.store.events[0] != "meeting.requested" {
		t.Fatalf("events = %v", h.store.events)
	}
}

// ── T-A3/T-A4: the order reaches the unit over the LAN, or waits in the poll ─

func TestStartOrdersTheUnitOverTheLANSignedWithTheOrgSecret(t *testing.T) {
	h := newHarness(t)
	m, err := h.s.Start(context.Background(), "68ee", OriginDashboard)
	if err != nil {
		t.Fatal(err)
	}
	got := h.cmd.wait(t)
	if got.ip != "10.0.0.125" || got.cmd != CmdStart || got.secret != "org-secret" || got.id != m.ID {
		t.Fatalf("command = %+v", got)
	}
	// While the unit has not confirmed, the poll carries the same order (RM-06).
	if p := h.s.PendingCommand(context.Background(), "68ee"); p != "start:"+m.ID.String() {
		t.Fatalf("pending = %q", p)
	}
}

func TestWithoutLANTheOrderRidesThePoll(t *testing.T) {
	h := newHarness(t)
	h.units.ip = ""
	m, err := h.s.Start(context.Background(), "68ee", OriginDashboard)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.cmd.got) != 0 {
		t.Fatal("nothing must be sent over the LAN without an address")
	}
	if p := h.s.PendingCommand(context.Background(), "68ee"); p != "start:"+m.ID.String() {
		t.Fatalf("pending = %q", p)
	}
	if err := h.s.Report(context.Background(), "68ee", "unit-secret", m.ID, "recording", ""); err != nil {
		t.Fatal(err)
	}
	if p := h.s.PendingCommand(context.Background(), "68ee"); p != "" {
		t.Fatalf("confirmed: nothing pending, got %q", p)
	}
}

func TestStopFromTheDashboardOrdersTheUnitAndAGestureStopDoesNot(t *testing.T) {
	h := newHarness(t)
	m := h.started(t)
	h.advance(time.Minute)
	stopped, err := h.s.Stop(context.Background(), m.ID, EndDashboard)
	if err != nil || stopped.State != StateClosing || stopped.DurationMs != 60_000 {
		t.Fatalf("stop: %+v %v", stopped, err)
	}
	if got := h.cmd.wait(t); got.cmd != CmdStop {
		t.Fatalf("the unit must be told to stop (RM-22): %+v", got)
	}
	if p := h.s.PendingCommand(context.Background(), "68ee"); p != "stop:"+m.ID.String() {
		t.Fatalf("pending = %q", p)
	}

	h2 := newHarness(t)
	m2 := h2.started(t)
	if err := h2.s.Report(context.Background(), "68ee", "unit-secret", m2.ID, "stopped", EndGesture); err != nil {
		t.Fatal(err)
	}
	got, _ := h2.s.Get(context.Background(), m2.ID)
	if got.State != StateClosing || got.EndReason != EndGesture || len(h2.cmd.got) != 1 {
		t.Fatalf("a gesture stop needs no order back: %+v cmds=%d", got, len(h2.cmd.got))
	}
	if strings.Join(h2.store.events, ",") != "meeting.requested,meeting.started,meeting.ended" {
		t.Fatalf("events = %v", h2.store.events)
	}
}

// ── T-A5: the unit's report needs its secret ───────────────────────────────

func TestReportNeedsTheUnitSecretAndItsOwnMeeting(t *testing.T) {
	h := newHarness(t)
	m, _ := h.s.Start(context.Background(), "68ee", OriginDashboard)
	h.cmd.wait(t)
	if err := h.s.Report(context.Background(), "68ee", "wrong", m.ID, "recording", ""); !errors.Is(err, device.ErrUnauthorized) {
		t.Fatalf("wrong secret: %v", err)
	}
	if err := h.s.Report(context.Background(), "68ee", "unit-secret", uuid.New(), "recording", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown meeting: %v", err)
	}
	if err := h.s.Report(context.Background(), "68ee", "unit-secret", m.ID, "dancing", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown state: %v", err)
	}
	if err := h.s.Report(context.Background(), "68ee", "unit-secret", m.ID, "recording", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := h.s.Get(context.Background(), m.ID)
	if got.State != StateRecording || !got.StartedAt.Equal(h.clock) {
		t.Fatalf("recording: %+v", got)
	}
}

func TestARequestTheUnitNeverConfirmsIsDropped(t *testing.T) {
	h := newHarness(t)
	m, _ := h.s.Start(context.Background(), "68ee", OriginDashboard)
	h.cmd.wait(t)
	h.advance(ConfirmWindow)
	h.s.Tick(context.Background())
	if _, err := h.s.Get(context.Background(), m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("must be dropped after %s (RM-03): %v", ConfirmWindow, err)
	}
	if _, err := h.s.Start(context.Background(), "68ee", OriginDashboard); err != nil {
		t.Fatalf("the unit is free again: %v", err)
	}
}

// ── T-A6: audio streams into the file; a cut upload keeps what arrived ─────

type brokenReader struct {
	data []byte
	err  error
	sent bool
}

func (b *brokenReader) Read(p []byte) (int, error) {
	if b.sent {
		return 0, b.err
	}
	b.sent = true
	return copy(p, b.data), nil
}

func TestAudioIsAppendedAsItArrivesAndACutKeepsIt(t *testing.T) {
	h := newHarness(t)
	m := h.started(t)
	ctx := context.Background()
	n, err := h.s.Audio(ctx, m.ID, 0, &brokenReader{data: []byte("OggS-part-1"), err: io.ErrUnexpectedEOF})
	if !errors.Is(err, io.ErrUnexpectedEOF) || n != 11 {
		t.Fatalf("aborted upload: n=%d err=%v", n, err)
	}
	got, _ := h.s.Get(ctx, m.ID)
	if got.State != StateRecording || got.AudioBytes != 11 || got.AudioPath == "" {
		t.Fatalf("what arrived must count (RM-15): %+v", got)
	}
	// Resume from the wrong offset is refused with the real size (design §3.3).
	if n, err := h.s.Audio(ctx, m.ID, 0, strings.NewReader("x")); !errors.Is(err, ErrOffset) || n != 11 {
		t.Fatalf("offset check: n=%d err=%v", n, err)
	}
	// An empty body at the right offset neither closes nor advances anything.
	if n, err := h.s.Audio(ctx, m.ID, 11, strings.NewReader("")); err != nil || n != 11 {
		t.Fatalf("empty upload: n=%d err=%v", n, err)
	}
	if got, _ := h.s.Get(ctx, m.ID); got.State != StateRecording {
		t.Fatalf("an empty upload must not close the recording: %s", got.State)
	}
	// Resume from the right offset, clean end of stream → closing → transcribing.
	h.advance(10 * time.Second)
	if _, err := h.s.Stop(ctx, m.ID, EndDashboard); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.Audio(ctx, m.ID, 11, strings.NewReader("-part-2")); err != nil {
		t.Fatalf("resume: %v", err)
	}
	got, _ = h.s.Get(ctx, m.ID)
	if got.State != StateTranscribing || got.AudioBytes != 18 {
		t.Fatalf("after a clean end: %+v", got)
	}
	data, _ := os.ReadFile(filepath.Join(h.s.dir, m.ID.String()+".ogg"))
	if string(data) != "OggS-part-1-part-2" {
		t.Fatalf("file = %q", data)
	}
	if _, err := h.s.Audio(ctx, m.ID, 18, strings.NewReader("late")); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("a finished meeting takes no audio: %v", err)
	}
}

func TestThirtySecondsWithoutAudioCutsTheMeeting(t *testing.T) {
	h := newHarness(t)
	m := h.started(t)
	ctx := context.Background()
	var queued []Meeting
	h.s.OnTranscribe = func(m Meeting) { queued = append(queued, m) }
	if _, err := h.s.Audio(ctx, m.ID, 0, &brokenReader{data: []byte("OggS"), err: io.ErrUnexpectedEOF}); err == nil {
		t.Fatal("expected the abort error")
	}
	h.advance(AudioSilenceWindow - time.Second)
	h.s.Tick(ctx)
	if got, _ := h.s.Get(ctx, m.ID); got.State != StateRecording {
		t.Fatalf("too early: %s", got.State)
	}
	h.advance(time.Second)
	h.s.Tick(ctx)
	got, _ := h.s.Get(ctx, m.ID)
	if got.State != StateCut || got.EndReason != EndDeviceLost || got.AudioBytes != 4 {
		t.Fatalf("cut (RM-25): %+v", got)
	}
	if len(queued) != 1 || queued[0].ID != m.ID {
		t.Fatalf("a cut meeting goes to transcription: %v", queued)
	}
	if got := h.cmd.wait(t); got.cmd != CmdStop {
		t.Fatalf("the unit is told to stop in case it is alive: %+v", got)
	}
}

func TestMaxDurationStopsTheMeeting(t *testing.T) {
	h := newHarness(t)
	h.s.limits = Limits{MaxDuration: 2 * time.Hour}
	m := h.started(t)
	ctx := context.Background()
	h.advance(2 * time.Hour)
	// keep the audio clock fresh so only the limit fires
	h.store.mu.Lock()
	row := h.store.rows[m.ID]
	row.LastAudioAt = h.clock
	h.store.rows[m.ID] = row
	h.store.mu.Unlock()
	h.s.Tick(ctx)
	got, _ := h.s.Get(ctx, m.ID)
	if got.State != StateClosing || got.EndReason != EndMaxDuration {
		t.Fatalf("limit (RM-24): %+v", got)
	}
}

// ── T-A8: what the list shows ──────────────────────────────────────────────

func TestLiveDurationAndDeleteKeepTheTrace(t *testing.T) {
	h := newHarness(t)
	m := h.started(t)
	ctx := context.Background()
	if d := Live(m, h.clock.Add(90*time.Second)); d != 90*time.Second {
		t.Fatalf("live = %s", d)
	}
	if err := h.s.Delete(ctx, m.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("a meeting in progress cannot be deleted: %v", err)
	}
	if _, err := h.s.Audio(ctx, m.ID, 0, &brokenReader{data: []byte("OggS"), err: io.ErrUnexpectedEOF}); err == nil {
		t.Fatal("expected the abort error")
	}
	h.advance(time.Minute)
	if _, err := h.s.Stop(ctx, m.ID, EndVoice); err != nil {
		t.Fatal(err)
	}
	h.cmd.wait(t)
	h.advance(CloseWindow)
	h.s.Tick(ctx)
	path, err := h.s.AudioFile(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.s.Delete(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the audio must be gone (RM-46)")
	}
	got, _ := h.s.Get(ctx, m.ID)
	if got.DeletedAt.IsZero() || got.AudioPath != "" || got.DurationMs != 60_000 {
		t.Fatalf("the trace stays without content: %+v", got)
	}
	if _, err := h.s.AudioFile(ctx, m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("no audio after delete")
	}
	if !strings.Contains(strings.Join(h.store.events, ","), "meeting.deleted") {
		t.Fatalf("events = %v", h.store.events)
	}
}

// ── block C: the agent's silence net (RM-23) ──────────────────────────────

func TestWarnReachesTheUnitOnlyWhileRecording(t *testing.T) {
	h := newHarness(t)
	m := h.started(t)
	if err := h.s.Warn(context.Background(), m.ID); err != nil {
		t.Fatal(err)
	}
	if got := h.cmd.wait(t); got.cmd != CmdWarn || got.id != m.ID {
		t.Fatalf("the unit must blink its warning (RM-13): %+v", got)
	}
	if _, err := h.s.Stop(context.Background(), m.ID, EndSilence); err != nil {
		t.Fatal(err)
	}
	h.cmd.wait(t)
	if err := h.s.Warn(context.Background(), m.ID); !errors.Is(err, ErrNotRecording) {
		t.Fatalf("a warning after the stop is meaningless: %v", err)
	}
	if err := h.s.Warn(context.Background(), uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown meeting: %v", err)
	}
}

// The agent's upload is one long request: a stop that lands while it is open
// must not be undone by the upload's own bookkeeping (field bug, block C).
func TestAStopDuringTheUploadIsNotUndoneByIt(t *testing.T) {
	h := newHarness(t)
	m := h.started(t)
	ctx := context.Background()
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, err := h.s.Audio(ctx, m.ID, 0, pr)
		done <- err
	}()
	if _, err := pw.Write([]byte("OggS-part-1")); err != nil {
		t.Fatal(err)
	}
	h.advance(6 * time.Second)
	if _, err := pw.Write([]byte("-more")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.Stop(ctx, m.ID, EndDashboard); err != nil {
		t.Fatal(err)
	}
	h.cmd.wait(t)
	h.advance(6 * time.Second)
	if _, err := pw.Write([]byte("-tail")); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	if err := <-done; err != nil {
		t.Fatalf("upload: %v", err)
	}
	got, _ := h.s.Get(ctx, m.ID)
	if got.State != StateTranscribing || got.EndReason != EndDashboard || got.AudioBytes != 21 {
		t.Fatalf("the stop must survive the upload: %+v", got)
	}
}

// Block D at the service edge: transcript, digest, renames, keep, retention.
func TestTranscriptDigestRenameAndRetention(t *testing.T) {
	h := newHarness(t)
	m := h.started(t)
	ctx := context.Background()
	h.advance(time.Minute)
	if _, err := h.s.Stop(ctx, m.ID, EndDashboard); err != nil {
		t.Fatal(err)
	}
	h.cmd.wait(t)
	if _, err := h.s.Audio(ctx, m.ID, 0, strings.NewReader("OggS")); err != nil {
		t.Fatal(err)
	}
	m, _ = h.s.Get(ctx, m.ID)
	if m.State != StateTranscribing {
		t.Fatalf("state = %s", m.State)
	}
	tr := Transcript{Diarized: true, Segments: []Segment{{Start: 0, End: 2, Speaker: "Hablante 1", Text: "Hola"}}}
	tr.Text = tr.PlainText()
	if _, err := h.s.Transcribed(ctx, m, tr); err != nil {
		t.Fatal(err)
	}
	got, _ := h.s.Get(ctx, m.ID)
	if got.State != StateReady || got.Transcript == nil || got.Transcript.Text != "Hablante 1: Hola\n" {
		t.Fatalf("transcribed: %+v", got)
	}
	if _, err := h.s.Summarized(ctx, got, Summary{Language: "es", Text: "resumen"}); err != nil {
		t.Fatal(err)
	}
	got, _ = h.s.Get(ctx, m.ID)
	if got.Summary == nil || got.Summary.Text != "resumen" || got.Transcript.Language != "es" {
		t.Fatalf("summarized: %+v %+v", got.Summary, got.Transcript)
	}
	keep := true
	got, err := h.s.Patch(ctx, m.ID, map[string]string{"Hablante 1": "Ana"}, &keep)
	if err != nil || !got.Keep || got.Transcript.SpeakerName("Hablante 1") != "Ana" || got.Transcript.Text != "Ana: Hola\n" {
		t.Fatalf("patch: %+v %v", got, err)
	}
	if strings.Join(h.store.events, ",") != "meeting.requested,meeting.started,meeting.ended,meeting.transcribed" {
		t.Fatalf("events = %v", h.store.events)
	}
	var summaries []Meeting
	h.s.OnSummarize = func(m Meeting) { summaries = append(summaries, m) }
	if _, err := h.s.Resummarize(ctx, m.ID); err != nil || len(summaries) != 1 {
		t.Fatalf("resummarize: %v %d", err, len(summaries))
	}
	// Retention: kept → survives; unkept → gone after 90 days (RM-45).
	h.advance(91 * 24 * time.Hour)
	if n := h.s.Retain(ctx, 90); n != 0 {
		t.Fatalf("a kept meeting survives retention: deleted %d", n)
	}
	keep = false
	if _, err := h.s.Patch(ctx, m.ID, nil, &keep); err != nil {
		t.Fatal(err)
	}
	if n := h.s.Retain(ctx, 90); n != 1 {
		t.Fatalf("retention must delete it: %d", n)
	}
	got, _ = h.s.Get(ctx, m.ID)
	if got.DeletedAt.IsZero() {
		t.Fatal("deleted keeps the trace (RM-46)")
	}
}
