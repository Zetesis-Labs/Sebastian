package transcribe

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zetesis-labs/sebastian/server/internal/meeting"
)

type fakeMeetings struct {
	rows  map[uuid.UUID]meeting.Meeting
	audio map[uuid.UUID]string
	log   []string
}

func (f *fakeMeetings) Get(_ context.Context, id uuid.UUID) (meeting.Meeting, error) {
	m, ok := f.rows[id]
	if !ok {
		return meeting.Meeting{}, meeting.ErrNotFound
	}
	return m, nil
}
func (f *fakeMeetings) AudioFile(_ context.Context, id uuid.UUID) (string, error) {
	p, ok := f.audio[id]
	if !ok {
		return "", meeting.ErrNotFound
	}
	return p, nil
}
func (f *fakeMeetings) Transcribing(context.Context) ([]meeting.Meeting, error) {
	var out []meeting.Meeting
	for _, m := range f.rows {
		if m.State == meeting.StateTranscribing {
			out = append(out, m)
		}
	}
	return out, nil
}
func (f *fakeMeetings) Transcribed(_ context.Context, m meeting.Meeting, t meeting.Transcript) (meeting.Meeting, error) {
	m.State, m.Transcript, m.TranscriptError = meeting.StateReady, &t, ""
	f.rows[m.ID] = m
	f.log = append(f.log, "transcribed")
	return m, nil
}
func (f *fakeMeetings) TranscriptFailed(_ context.Context, m meeting.Meeting, reason string) (meeting.Meeting, error) {
	m.State, m.TranscriptError = meeting.StateNoTranscript, reason
	f.rows[m.ID] = m
	f.log = append(f.log, "failed:"+reason)
	return m, nil
}
func (f *fakeMeetings) Summarized(_ context.Context, m meeting.Meeting, s meeting.Summary) (meeting.Meeting, error) {
	m.Summary = &s
	f.rows[m.ID] = m
	f.log = append(f.log, "summarized")
	return m, nil
}

type fakeProvider struct {
	calls   [][]Reference
	sizes   []int
	fail    error
	noSum   bool
	summary meeting.Summary
}

func (p *fakeProvider) Transcribe(_ context.Context, audio []byte, known []Reference) (Result, error) {
	p.calls = append(p.calls, known)
	p.sizes = append(p.sizes, len(audio))
	if p.fail != nil {
		return Result{}, p.fail
	}
	n := len(p.calls)
	// A sentence past the 2 s overlap, so every piece contributes one segment.
	return Result{Diarized: true, Model: "diarize", Segments: []meeting.Segment{{Start: 3, End: 6, Speaker: "A", Text: fmt.Sprintf("pieza %d", n)}}}, nil
}
func (p *fakeProvider) Summarize(_ context.Context, text string) (meeting.Summary, error) {
	if p.noSum {
		return meeting.Summary{}, errors.New("summary must not be called")
	}
	p.summary.Text = "resumen de: " + strings.TrimSpace(text)
	return p.summary, nil
}

type fakeCutter struct{ cuts []Piece }

func (c *fakeCutter) Cut(_ context.Context, _ string, p Piece) ([]byte, error) {
	c.cuts = append(c.cuts, p)
	return []byte(strings.Repeat("x", int(p.Length.Seconds()))), nil
}
func (c *fakeCutter) Clip(_ context.Context, _ string, start, length float64) (string, error) {
	return fmt.Sprintf("data:audio/wav;base64,%v-%v", start, length), nil
}

func fixture(t *testing.T, size int, durationMs int64) (*fakeMeetings, meeting.Meeting) {
	t.Helper()
	dir := t.TempDir()
	id := uuid.New()
	path := filepath.Join(dir, id.String()+".ogg")
	if err := os.WriteFile(path, []byte(strings.Repeat("o", size)), 0o600); err != nil {
		t.Fatal(err)
	}
	m := meeting.Meeting{ID: id, State: meeting.StateTranscribing, DurationMs: durationMs}
	return &fakeMeetings{rows: map[uuid.UUID]meeting.Meeting{id: m}, audio: map[uuid.UUID]string{id: path}}, m
}

func TestAShortMeetingGoesWholeToTheProviderThenTheSummary(t *testing.T) {
	store, m := fixture(t, 1000, 60_000)
	provider := &fakeProvider{summary: meeting.Summary{Language: "es", Model: "mini"}}
	cutter := &fakeCutter{}
	job := NewJob(store, provider, cutter, true, nil)
	if err := job.Process(context.Background(), m.ID); err != nil {
		t.Fatal(err)
	}
	if len(provider.calls) != 1 || provider.sizes[0] != 1000 || len(cutter.cuts) != 0 {
		t.Fatalf("one piece = the file itself, no ffmpeg: calls=%d sizes=%v cuts=%d", len(provider.calls), provider.sizes, len(cutter.cuts))
	}
	got := store.rows[m.ID]
	if got.State != meeting.StateReady || got.Transcript == nil || got.Transcript.Segments[0].Speaker != "Hablante 1" || got.Transcript.Model != "diarize" {
		t.Fatalf("meeting = %+v", got)
	}
	if got.Summary == nil || got.Summary.Text != "resumen de: Hablante 1: pieza 1" {
		t.Fatalf("the summary must follow the transcript: %+v", got.Summary)
	}
	if strings.Join(store.log, ",") != "transcribed,summarized" {
		t.Fatalf("log = %v", store.log)
	}
}

func TestALongMeetingIsCutAndSpeakersTravelBetweenPieces(t *testing.T) {
	store, m := fixture(t, 3000, 3_000_000) // 3000 s at 1 byte/s
	provider := &fakeProvider{noSum: true}
	cutter := &fakeCutter{}
	job := NewJob(store, provider, cutter, false, nil)
	job.maxBytes = 1000
	if err := job.Process(context.Background(), m.ID); err != nil {
		t.Fatal(err)
	}
	if len(cutter.cuts) < 3 || len(provider.calls) != len(cutter.cuts) {
		t.Fatalf("cuts=%d calls=%d", len(cutter.cuts), len(provider.calls))
	}
	if len(provider.calls[0]) != 0 {
		t.Fatal("the first piece knows nobody")
	}
	if len(provider.calls[1]) != 1 || provider.calls[1][0].Name != "Hablante 1" || !strings.HasPrefix(provider.calls[1][0].DataURL, "data:audio/wav;base64,") {
		t.Fatalf("the second piece carries the speakers heard so far: %+v", provider.calls[1])
	}
	got := store.rows[m.ID]
	if got.State != meeting.StateReady || len(got.Transcript.Segments) != len(cutter.cuts) || got.Summary != nil {
		t.Fatalf("meeting = %+v", got)
	}
	if strings.Join(store.log, ",") != "transcribed" {
		t.Fatalf("summary disabled by the control room → not called (RM-32): %v", store.log)
	}
}

func TestProviderRejectionLeavesNoTranscriptWithTheReason(t *testing.T) {
	store, m := fixture(t, 100, 10_000)
	provider := &fakeProvider{fail: fmt.Errorf("%w: 413 too big", ErrRejected)}
	job := NewJob(store, provider, &fakeCutter{}, true, nil)
	if err := job.Process(context.Background(), m.ID); err != nil {
		t.Fatal(err)
	}
	got := store.rows[m.ID]
	if got.State != meeting.StateNoTranscript || got.TranscriptError != "413 too big" {
		t.Fatalf("RM-33: %+v", got)
	}
	// Without a provider (no key) the reason says so.
	store2, m2 := fixture(t, 100, 10_000)
	if err := NewJob(store2, nil, &fakeCutter{}, true, nil).Process(context.Background(), m2.ID); err != nil {
		t.Fatal(err)
	}
	if got := store2.rows[m2.ID]; got.State != meeting.StateNoTranscript || !strings.Contains(got.TranscriptError, "OPENAI_API_KEY") {
		t.Fatalf("no key: %+v", got)
	}
	// A meeting that is not transcribing is left alone (a stale queue entry).
	store3, m3 := fixture(t, 100, 10_000)
	m3.State = meeting.StateReady
	store3.rows[m3.ID] = m3
	provider3 := &fakeProvider{}
	_ = NewJob(store3, provider3, &fakeCutter{}, true, nil).Process(context.Background(), m3.ID)
	if len(provider3.calls) != 0 {
		t.Fatal("only transcribing meetings are processed")
	}
}

func TestRunRecoversWhatWasLeftTranscribing(t *testing.T) {
	store, m := fixture(t, 100, 10_000)
	provider := &fakeProvider{noSum: true}
	job := NewJob(store, provider, &fakeCutter{}, false, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go job.Run(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && store.rows[m.ID].State != meeting.StateReady {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if store.rows[m.ID].State != meeting.StateReady {
		t.Fatal("a meeting left transcribing by a previous run is picked up at start")
	}
}

func TestFFmpegCutsAndClipsARealFile(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "tone.ogg")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=6", "-c:a", "libopus", "-b:a", "48k", src).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg fixture: %v %s", err, out)
	}
	f := FFmpeg{}
	piece, err := f.Cut(context.Background(), src, Piece{Start: 2 * time.Second, Length: 2 * time.Second})
	if err != nil || len(piece) < 1000 || string(piece[:4]) != "OggS" {
		t.Fatalf("cut: %v len=%d", err, len(piece))
	}
	clip, err := f.Clip(context.Background(), src, 1, 2)
	if err != nil || !strings.HasPrefix(clip, "data:audio/wav;base64,") || len(clip) < 2*16000*2*4/3 {
		t.Fatalf("clip: %v len=%d", err, len(clip))
	}
}
