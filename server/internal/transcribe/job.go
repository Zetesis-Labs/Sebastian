package transcribe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zetesis-labs/sebastian/server/internal/meeting"
)

// Provider is what the job needs from OpenAI (RM-34).
type Provider interface {
	Transcribe(ctx context.Context, audio []byte, known []Reference, language string) (Result, error)
	Summarize(ctx context.Context, transcript string) (meeting.Summary, error)
}

// Cutter is ffmpeg: a piece of the stored Ogg, or a short WAV clip of a
// speaker as a data URL for the provider's speaker references.
type Cutter interface {
	Cut(ctx context.Context, src string, p Piece) ([]byte, error)
	Clip(ctx context.Context, src string, start, length float64) (string, error)
}

// Meetings is the slice of meeting.Service the job drives.
type Meetings interface {
	Get(ctx context.Context, id uuid.UUID) (meeting.Meeting, error)
	AudioFile(ctx context.Context, id uuid.UUID) (string, error)
	Transcribing(ctx context.Context) ([]meeting.Meeting, error)
	Transcribed(ctx context.Context, m meeting.Meeting, t meeting.Transcript) (meeting.Meeting, error)
	TranscriptFailed(ctx context.Context, m meeting.Meeting, reason string) (meeting.Meeting, error)
	Summarized(ctx context.Context, m meeting.Meeting, s meeting.Summary) (meeting.Meeting, error)
}

type task struct {
	id          uuid.UUID
	summaryOnly bool
}

// Job transcribes and summarizes meetings one at a time, in the background
// (RM-30/32/33). Enqueue never blocks the caller.
// LanguageFor answers what language this unit's recordings are in. Read at
// transcription time, not frozen when the meeting was recorded: changing the
// unit's setting and retrying is how a meeting that came back in the wrong
// language gets fixed. An empty answer means let the provider guess.
type LanguageFor func(ctx context.Context, deviceID string) string

type Job struct {
	meetings    Meetings
	provider    Provider
	cutter      Cutter
	summary     bool
	logger      *slog.Logger
	queue       chan task
	maxBytes    int64
	languageFor LanguageFor
}

func NewJob(meetings Meetings, provider Provider, cutter Cutter, summary bool, languageFor LanguageFor, logger *slog.Logger) *Job {
	if logger == nil {
		logger = slog.Default()
	}
	return &Job{meetings: meetings, provider: provider, cutter: cutter, summary: summary, languageFor: languageFor, logger: logger, queue: make(chan task, 256), maxBytes: MaxPieceBytes}
}

// Enqueue is meeting.Service's OnTranscribe hook.
func (j *Job) Enqueue(m meeting.Meeting) { j.push(task{id: m.ID}) }

// EnqueueSummary regenerates the digest of a transcribed meeting (RM-32).
func (j *Job) EnqueueSummary(m meeting.Meeting) { j.push(task{id: m.ID, summaryOnly: true}) }

func (j *Job) push(t task) {
	select {
	case j.queue <- t:
	default:
		j.logger.Error("transcription queue full — the meeting stays transcribing until the next restart", "meeting", t.id)
	}
}

// Run drains the queue until ctx ends; first it picks up whatever was left
// transcribing by a previous run (the job survives restarts, decision 3).
func (j *Job) Run(ctx context.Context) {
	if left, err := j.meetings.Transcribing(ctx); err == nil {
		for _, m := range left {
			j.Enqueue(m)
		}
	} else {
		j.logger.Warn("transcription recovery: list failed", "error", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-j.queue:
			var err error
			if t.summaryOnly {
				err = j.Summarize(ctx, t.id)
			} else {
				err = j.Process(ctx, t.id)
			}
			if err != nil {
				j.logger.Warn("transcription job failed", "meeting", t.id, "summary_only", t.summaryOnly, "error", err)
			}
		}
	}
}

// Process transcribes one meeting: pieces → provider → merge → summary.
func (j *Job) Process(ctx context.Context, id uuid.UUID) error {
	m, err := j.meetings.Get(ctx, id)
	if err != nil {
		return err
	}
	if m.State != meeting.StateTranscribing {
		return nil
	}
	path, err := j.meetings.AudioFile(ctx, id)
	if err != nil {
		_, err = j.meetings.TranscriptFailed(ctx, m, "no audio stored")
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		_, err = j.meetings.TranscriptFailed(ctx, m, "audio file missing: "+err.Error())
		return err
	}
	if j.provider == nil {
		_, err = j.meetings.TranscriptFailed(ctx, m, "OPENAI_API_KEY is not configured")
		return err
	}
	language := ""
	if j.languageFor != nil {
		language = j.languageFor(ctx, m.DeviceID)
	}
	t, err := j.transcribe(ctx, path, time.Duration(m.DurationMs)*time.Millisecond, info.Size(), language)
	if err != nil {
		j.logger.Warn("transcription failed", "meeting", id, "error", err)
		_, err = j.meetings.TranscriptFailed(ctx, m, strings.TrimPrefix(err.Error(), ErrRejected.Error()+": "))
		return err
	}
	m, err = j.meetings.Transcribed(ctx, m, t)
	if err != nil {
		return err
	}
	j.logger.Info("meeting transcribed", "meeting", id, "segments", len(t.Segments), "diarized", t.Diarized, "model", t.Model)
	if !j.summary {
		return nil
	}
	return j.summarize(ctx, m)
}

func (j *Job) transcribe(ctx context.Context, path string, duration time.Duration, size int64, language string) (meeting.Transcript, error) {
	if duration <= 0 {
		duration = time.Second
	}
	pieces := Pieces(duration, size, j.maxBytes)
	var results []PieceResult
	var known []Reference
	var detected, model string
	for _, p := range pieces {
		var audio []byte
		var err error
		if len(pieces) == 1 {
			audio, err = os.ReadFile(path)
		} else {
			audio, err = j.cutter.Cut(ctx, path, p)
		}
		if err != nil {
			return meeting.Transcript{}, fmt.Errorf("cut piece %d: %w", p.Index, err)
		}
		res, err := j.provider.Transcribe(ctx, audio, known, language)
		if err != nil {
			return meeting.Transcript{}, err
		}
		results = append(results, PieceResult{Piece: p, Segments: res.Segments, Diarized: res.Diarized})
		if res.Language != "" {
			detected = res.Language
		}
		model = res.Model
		if p.Index < len(pieces)-1 && res.Diarized {
			known = j.references(ctx, path, Merge(results).Segments)
		}
	}
	t := Merge(results)
	t.Language, t.Model = detected, model
	return t, nil
}

// references clips each known speaker so the next piece keeps their ids.
func (j *Job) references(ctx context.Context, path string, segments []meeting.Segment) []Reference {
	var out []Reference
	for _, k := range KnownSpeakers(segments) {
		clip, err := j.cutter.Clip(ctx, path, k.Start, k.Len)
		if err != nil {
			j.logger.Warn("speaker reference clip failed", "speaker", k.ID, "error", err)
			continue
		}
		out = append(out, Reference{Name: k.ID, DataURL: clip})
	}
	return out
}

// Summarize (re)generates the digest of a transcribed meeting (RM-32).
func (j *Job) Summarize(ctx context.Context, id uuid.UUID) error {
	m, err := j.meetings.Get(ctx, id)
	if err != nil {
		return err
	}
	return j.summarize(ctx, m)
}

func (j *Job) summarize(ctx context.Context, m meeting.Meeting) error {
	if m.Transcript == nil || strings.TrimSpace(m.Transcript.Text) == "" {
		return errors.New("nothing to summarize")
	}
	if j.provider == nil {
		return errors.New("OPENAI_API_KEY is not configured")
	}
	s, err := j.provider.Summarize(ctx, m.Transcript.Text)
	if err != nil {
		return err
	}
	_, err = j.meetings.Summarized(ctx, m, s)
	return err
}
