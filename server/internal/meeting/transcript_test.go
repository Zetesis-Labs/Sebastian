package meeting

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func sample() Transcript {
	return Transcript{Diarized: true, Segments: []Segment{
		{Start: 0, End: 4.5, Speaker: "Hablante 1", Text: "Hola a todos."},
		{Start: 4.5, End: 10.25, Speaker: "Hablante 2", Text: "Empezamos."},
		{Start: 3700, End: 3702, Speaker: "Hablante 1", Text: "Cerramos."},
	}}
}

func TestRenamesApplyToEveryLineWithoutTouchingTheSegments(t *testing.T) {
	tr := sample().Rename(map[string]string{"Hablante 1": "Ana"})
	if tr.SpeakerName("Hablante 1") != "Ana" || tr.SpeakerName("Hablante 2") != "Hablante 2" {
		t.Fatalf("names: %+v", tr.Speakers)
	}
	if tr.Segments[0].Speaker != "Hablante 1" {
		t.Fatal("the segment keeps the id; only the alias changes (RM-31)")
	}
	if !strings.HasPrefix(tr.Text, "Ana: Hola a todos.\nHablante 2: Empezamos.\n") {
		t.Fatalf("text = %q", tr.Text)
	}
	back := tr.Rename(map[string]string{"Hablante 1": " "})
	if _, still := back.Speakers["Hablante 1"]; still {
		t.Fatal("an empty name drops the alias")
	}
	if got := tr.SpeakerIDs(); strings.Join(got, ",") != "Hablante 1,Hablante 2" {
		t.Fatalf("ids = %v", got)
	}
}

func TestTXTAndSRTDownloads(t *testing.T) {
	tr := sample().Rename(map[string]string{"Hablante 2": "Bea"})
	txt := tr.TXT()
	if !strings.Contains(txt, "[00:00] Hablante 1: Hola a todos.\n[00:04] Bea: Empezamos.\n[1:01:40] Hablante 1: Cerramos.\n") {
		t.Fatalf("txt = %q", txt)
	}
	srt := tr.SRT()
	if !strings.HasPrefix(srt, "1\n00:00:00,000 --> 00:00:04,500\nHablante 1: Hola a todos.\n\n2\n00:00:04,500 --> 00:00:10,250\nBea: Empezamos.\n\n") {
		t.Fatalf("srt = %q", srt)
	}
	plain := Transcript{Segments: []Segment{{Text: "solo texto"}}}
	if plain.TXT() != "[00:00] solo texto\n" || plain.PlainText() != "solo texto\n" {
		t.Fatalf("without diarization no speaker prefix: %q", plain.TXT())
	}
}

// T-D5: retention respects keep and never touches what is in progress.
func TestExpiredRespectsKeepAndProgress(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	old := now.Add(-91 * 24 * time.Hour)
	fresh := now.Add(-10 * 24 * time.Hour)
	mk := func(state State, ended time.Time, keep bool) Meeting {
		return Meeting{ID: uuid.New(), State: state, EndedAt: ended, RequestedAt: ended, Keep: keep}
	}
	kept := mk(StateReady, old, true)
	deleted := mk(StateReady, old, false)
	deleted.DeletedAt = now
	items := []Meeting{
		mk(StateReady, old, false),
		mk(StateNoTranscript, old.Add(-time.Hour), false),
		mk(StateCut, old, false),
		kept, deleted,
		mk(StateReady, fresh, false),
		mk(StateRecording, old, false),
		mk(StateTranscribing, old, false),
	}
	got := Expired(items, now, 90)
	if len(got) != 3 {
		t.Fatalf("expired = %d, want the three old finished ones", len(got))
	}
	if !got[0].EndedAt.Equal(old.Add(-time.Hour)) {
		t.Fatal("oldest first")
	}
	if Expired(items, now, 0) != nil {
		t.Fatal("retention 0 = disabled")
	}
}
