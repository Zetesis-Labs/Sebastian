package meeting

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Transcript is what block D produces (design §2): timed segments with a
// speaker id each. Speaker ids are "Hablante N"; Speakers holds the names
// the operator gave them (RM-31), so a rename never rewrites the segments.
type Transcript struct {
	Language string            `json:"language,omitempty"`
	Text     string            `json:"text"`
	Segments []Segment         `json:"segments"`
	Speakers map[string]string `json:"speakers,omitempty"`
	Model    string            `json:"model,omitempty"`
	Diarized bool              `json:"diarized"`
}

type Segment struct {
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Speaker string  `json:"speaker"`
	Text    string  `json:"text"`
}

// Summary is the automatic digest (RM-32), marked with the model that wrote it.
type Summary struct {
	Language    string    `json:"language,omitempty"`
	Text        string    `json:"text"`
	Agreements  []string  `json:"agreements"`
	Actions     []string  `json:"actions"`
	Model       string    `json:"model"`
	GeneratedAt time.Time `json:"generatedAt"`
}

// SpeakerName is what the reader sees for a speaker id (RM-31).
func (t Transcript) SpeakerName(id string) string {
	if name, ok := t.Speakers[id]; ok && strings.TrimSpace(name) != "" {
		return name
	}
	return id
}

// SpeakerIDs lists the speakers in order of first appearance.
func (t Transcript) SpeakerIDs() []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range t.Segments {
		if !seen[s.Speaker] {
			seen[s.Speaker] = true
			out = append(out, s.Speaker)
		}
	}
	return out
}

// PlainText joins the segments as "Name: text" lines (the stored `text`).
func (t Transcript) PlainText() string {
	var b strings.Builder
	for _, s := range t.Segments {
		if !t.Diarized {
			b.WriteString(strings.TrimSpace(s.Text))
		} else {
			b.WriteString(t.SpeakerName(s.Speaker))
			b.WriteString(": ")
			b.WriteString(strings.TrimSpace(s.Text))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// TXT is the download with timestamps (RM-41).
func (t Transcript) TXT() string {
	var b strings.Builder
	for _, s := range t.Segments {
		fmt.Fprintf(&b, "[%s] ", clock(s.Start))
		if t.Diarized {
			b.WriteString(t.SpeakerName(s.Speaker))
			b.WriteString(": ")
		}
		b.WriteString(strings.TrimSpace(s.Text))
		b.WriteString("\n")
	}
	return b.String()
}

// SRT is the subtitle download (RM-41).
func (t Transcript) SRT() string {
	var b strings.Builder
	for i, s := range t.Segments {
		fmt.Fprintf(&b, "%d\n%s --> %s\n", i+1, srtClock(s.Start), srtClock(s.End))
		if t.Diarized {
			b.WriteString(t.SpeakerName(s.Speaker))
			b.WriteString(": ")
		}
		b.WriteString(strings.TrimSpace(s.Text))
		b.WriteString("\n\n")
	}
	return b.String()
}

func clock(seconds float64) string {
	d := time.Duration(seconds * float64(time.Second))
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

func srtClock(seconds float64) string {
	d := time.Duration(seconds * float64(time.Second))
	return fmt.Sprintf("%02d:%02d:%02d,%03d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60, int(d.Milliseconds())%1000)
}

// Rename applies the operator's names (RM-31); an empty name drops the alias.
func (t Transcript) Rename(names map[string]string) Transcript {
	out := t
	out.Speakers = map[string]string{}
	for k, v := range t.Speakers {
		out.Speakers[k] = v
	}
	for id, name := range names {
		if strings.TrimSpace(name) == "" {
			delete(out.Speakers, id)
		} else {
			out.Speakers[id] = strings.TrimSpace(name)
		}
	}
	out.Text = out.PlainText()
	return out
}

// Expired lists the meetings retention must delete at `now` (RM-45): finished,
// not kept, not deleted, ended more than `days` ago. Pure.
func Expired(items []Meeting, now time.Time, days int) []Meeting {
	if days <= 0 {
		return nil
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	var out []Meeting
	for _, m := range items {
		if !Final(m.State) || m.Keep || !m.DeletedAt.IsZero() {
			continue
		}
		ended := m.EndedAt
		if ended.IsZero() {
			ended = m.RequestedAt
		}
		if ended.Before(cutoff) {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EndedAt.Before(out[j].EndedAt) })
	return out
}
