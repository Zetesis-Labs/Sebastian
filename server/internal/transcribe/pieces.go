// Package transcribe turns a stored meeting into a transcript and a summary
// (design 14 block D). This file is pure: how to cut the audio into pieces
// the provider accepts (25 MB per request) and how to merge what comes back.
package transcribe

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/zetesis-labs/sebastian/server/internal/meeting"
)

const (
	// MaxPieceBytes stays under the provider's 25 MB limit.
	MaxPieceBytes int64 = 20 << 20
	// Overlap keeps a sentence that straddles a cut in both pieces.
	Overlap = 2 * time.Second
)

type Piece struct {
	Index  int
	Start  time.Duration
	Length time.Duration
}

func (p Piece) End() time.Duration { return p.Start + p.Length }

// Pieces cuts a recording of `duration` and `size` bytes into pieces of at
// most maxBytes, overlapping by Overlap. T-D1.
func Pieces(duration time.Duration, size, maxBytes int64) []Piece {
	if duration <= 0 {
		return nil
	}
	if size <= maxBytes {
		return []Piece{{Index: 0, Start: 0, Length: duration}}
	}
	bytesPerSecond := float64(size) / duration.Seconds()
	// 5 % headroom: the container overhead is not spread evenly.
	pieceSeconds := float64(maxBytes)/bytesPerSecond*0.95 - Overlap.Seconds()
	step := time.Duration(pieceSeconds * float64(time.Second))
	n := int(math.Ceil(duration.Seconds() / pieceSeconds))
	out := make([]Piece, 0, n)
	for i := 0; i < n; i++ {
		start := time.Duration(i) * step
		end := start + step + Overlap
		if i == n-1 || end > duration {
			end = duration
		}
		out = append(out, Piece{Index: i, Start: start, Length: end - start})
	}
	return out
}

// PieceResult is one piece transcribed: segment times relative to the piece,
// speakers as the provider labelled them (a known name we passed, or a letter).
type PieceResult struct {
	Piece    Piece
	Segments []meeting.Segment
	Diarized bool
}

// Merge shifts every piece to the recording's clock, drops what the overlap
// duplicated and gives speakers stable ids: "Hablante N" in order of first
// appearance; a label we passed as a known speaker keeps its id. T-D2.
func Merge(results []PieceResult) meeting.Transcript {
	sort.Slice(results, func(i, j int) bool { return results[i].Piece.Index < results[j].Piece.Index })
	names := map[string]string{}
	next := 1
	speakerID := func(label string) string {
		if strings.HasPrefix(label, "Hablante ") {
			return label
		}
		if id, ok := names[label]; ok {
			return id
		}
		id := "Hablante " + itoa(next)
		next++
		names[label] = id
		return id
	}
	out := meeting.Transcript{Diarized: true}
	for i, r := range results {
		if !r.Diarized {
			out.Diarized = false
		}
		// Provider labels are per request: forget the letters between pieces.
		names = map[string]string{}
		offset := r.Piece.Start.Seconds()
		// Everything before this piece's start was already covered by the
		// previous one, whose tail overlaps us by Overlap.
		for _, s := range r.Segments {
			start := offset + s.Start
			if i > 0 && start < offset+Overlap.Seconds() {
				continue
			}
			seg := meeting.Segment{Start: round(start), End: round(offset + s.End), Text: strings.TrimSpace(s.Text)}
			if r.Diarized {
				seg.Speaker = speakerID(s.Speaker)
			}
			if seg.Text != "" {
				out.Segments = append(out.Segments, seg)
			}
		}
	}
	if !out.Diarized {
		for i := range out.Segments {
			out.Segments[i].Speaker = ""
		}
	}
	out.Text = out.PlainText()
	return out
}

// Known picks the speakers to carry into the next piece: the ones with a
// segment long enough to serve as a reference (the provider wants 2–10 s),
// most talkative first, at most four (the provider's limit).
type Known struct {
	ID    string
	Start float64
	Len   float64
}

func KnownSpeakers(segments []meeting.Segment) []Known {
	talk := map[string]float64{}
	best := map[string]Known{}
	for _, s := range segments {
		if s.Speaker == "" {
			continue
		}
		l := s.End - s.Start
		talk[s.Speaker] += l
		if l >= 2 && l > best[s.Speaker].Len {
			best[s.Speaker] = Known{ID: s.Speaker, Start: s.Start, Len: math.Min(l, 8)}
		}
	}
	out := make([]Known, 0, len(best))
	for _, k := range best {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return talk[out[i].ID] > talk[out[j].ID] })
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}

func round(f float64) float64 { return math.Round(f*100) / 100 }

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}
