package transcribe

import (
	"testing"
	"time"

	"github.com/zetesis-labs/sebastian/server/internal/meeting"
)

// T-D1: cuts stay under the limit, overlap by 2 s and cover the whole recording.
func TestPiecesCoverTheRecordingUnderTheLimit(t *testing.T) {
	rate := int64(48_000 / 8) // 48 kbit/s
	forty := 40 * time.Minute
	if got := Pieces(forty, rate*int64(forty.Seconds()), MaxPieceBytes); len(got) != 1 || got[0].Start != 0 || got[0].Length != forty {
		t.Fatalf("40 min at 48 kbit/s (14 MB) is one piece: %+v", got)
	}
	three := 3 * time.Hour
	size := rate * int64(three.Seconds())
	got := Pieces(three, size, MaxPieceBytes)
	if len(got) < 4 {
		t.Fatalf("3 h (65 MB) needs several pieces: %d", len(got))
	}
	for i, p := range got {
		if bytes := int64(p.Length.Seconds()) * rate; bytes > MaxPieceBytes {
			t.Fatalf("piece %d is %d bytes, over the limit", i, bytes)
		}
		if i > 0 && got[i-1].End()-p.Start != Overlap {
			t.Fatalf("pieces %d/%d overlap by %s, want %s", i-1, i, got[i-1].End()-p.Start, Overlap)
		}
	}
	if got[0].Start != 0 || got[len(got)-1].End() != three {
		t.Fatalf("the pieces must cover 0..3h: %+v", got)
	}
	if Pieces(0, 0, MaxPieceBytes) != nil {
		t.Fatal("no duration, no pieces")
	}
}

// T-D2: merged pieces keep one clock, drop the overlap and number speakers.
func TestMergeShiftsDeduplicatesAndNumbersSpeakers(t *testing.T) {
	p0 := PieceResult{Piece: Piece{Index: 0, Start: 0, Length: 62 * time.Second}, Diarized: true, Segments: []meeting.Segment{
		{Start: 0, End: 5, Speaker: "A", Text: "Buenos días."},
		{Start: 5, End: 9, Speaker: "B", Text: "Hola."},
		{Start: 59, End: 61.5, Speaker: "A", Text: "Seguimos."},
	}}
	// The first piece ran to 62 s; the second starts at 60 s, so its first
	// 2 s repeat the previous tail. A and B were passed as known speakers
	// "Hablante 1"/"Hablante 2"; a newcomer gets a fresh letter.
	p1 := PieceResult{Piece: Piece{Index: 1, Start: 60 * time.Second, Length: 60 * time.Second}, Diarized: true, Segments: []meeting.Segment{
		{Start: 0, End: 1.5, Speaker: "Hablante 1", Text: "Seguimos."},
		{Start: 2, End: 6, Speaker: "A", Text: "Yo soy nueva."},
		{Start: 6, End: 10, Speaker: "Hablante 2", Text: "Bienvenida."},
	}}
	tr := Merge([]PieceResult{p1, p0})
	want := []meeting.Segment{
		{Start: 0, End: 5, Speaker: "Hablante 1", Text: "Buenos días."},
		{Start: 5, End: 9, Speaker: "Hablante 2", Text: "Hola."},
		{Start: 59, End: 61.5, Speaker: "Hablante 1", Text: "Seguimos."},
		{Start: 62, End: 66, Speaker: "Hablante 3", Text: "Yo soy nueva."},
		{Start: 66, End: 70, Speaker: "Hablante 2", Text: "Bienvenida."},
	}
	if len(tr.Segments) != len(want) {
		t.Fatalf("segments = %+v", tr.Segments)
	}
	for i := range want {
		if tr.Segments[i] != want[i] {
			t.Fatalf("segment %d = %+v, want %+v", i, tr.Segments[i], want[i])
		}
	}
	if !tr.Diarized || tr.Text != "Hablante 1: Buenos días.\nHablante 2: Hola.\nHablante 1: Seguimos.\nHablante 3: Yo soy nueva.\nHablante 2: Bienvenida.\n" {
		t.Fatalf("text = %q", tr.Text)
	}
	known := KnownSpeakers(tr.Segments)
	if len(known) != 3 || known[0].ID != "Hablante 2" || known[1].ID != "Hablante 1" || known[1].Len != 5 {
		t.Fatalf("known = %+v (most talkative first, the longest clip ≥ 2 s)", known)
	}
}

func TestMergeWithoutDiarizationHasNoSpeakers(t *testing.T) {
	tr := Merge([]PieceResult{{Piece: Piece{Index: 0, Length: time.Minute}, Diarized: false, Segments: []meeting.Segment{{Start: 0, End: 3, Text: "texto plano"}}}})
	if tr.Diarized || tr.Segments[0].Speaker != "" || tr.Text != "texto plano\n" {
		t.Fatalf("fallback: %+v", tr)
	}
	if len(KnownSpeakers(tr.Segments)) != 0 {
		t.Fatal("no speakers to carry")
	}
}
