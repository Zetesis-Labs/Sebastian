package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/zetesis-labs/sebastian/server/internal/meeting"
)

const (
	DefaultBaseURL         = "https://api.openai.com/v1"
	DefaultTranscribeModel = "gpt-4o-transcribe-diarize"
	// LanguageAuto means: say nothing and let the provider decide.
	LanguageAuto  = "auto"
	FallbackModel = "whisper-1"
	// DefaultSummaryModel is the economy model in force when block D was
	// implemented (GET /v1/models, 2026-09-19).
	DefaultSummaryModel = "gpt-5.4-mini"
)

// ErrRejected is a provider answer that will not change on retry (4xx):
// the meeting ends in no_transcript with the reason (RM-33).
var ErrRejected = errors.New("transcription rejected by the provider")

// Reference is a known speaker for the next piece: its id and a short clip.
type Reference struct {
	Name    string
	DataURL string
}

type Result struct {
	Text     string
	Language string
	Segments []meeting.Segment
	Diarized bool
	Model    string
}

// OpenAI is the provider client (RM-34). It sends audio and the model
// parameters, nothing else (RM-35): no meeting, unit or control room data.
type OpenAI struct {
	BaseURL         string
	APIKey          string
	TranscribeModel string
	SummaryModel    string
	HTTP            *http.Client
	Retries         int
	Backoff         time.Duration
}

func NewOpenAI(apiKey string) *OpenAI {
	return &OpenAI{BaseURL: DefaultBaseURL, APIKey: apiKey, TranscribeModel: DefaultTranscribeModel, SummaryModel: DefaultSummaryModel,
		HTTP: &http.Client{Timeout: 10 * time.Minute}, Retries: 3, Backoff: 2 * time.Second}
}

// Transcribe sends one piece. The diarizing model first; if the provider
// does not know it, whisper-1 without speakers (RM-34). An empty language (or
// "auto") leaves the guess to the provider.
func (c *OpenAI) Transcribe(ctx context.Context, audio []byte, known []Reference, language string) (Result, error) {
	res, err := c.transcribeWith(ctx, c.TranscribeModel, audio, known, language)
	if errors.Is(err, errModelUnavailable) && c.TranscribeModel != FallbackModel {
		return c.transcribeWith(ctx, FallbackModel, audio, nil, language)
	}
	return res, err
}

var errModelUnavailable = errors.New("model unavailable")

func (c *OpenAI) transcribeWith(ctx context.Context, model string, audio []byte, known []Reference, language string) (Result, error) {
	diarize := model == DefaultTranscribeModel || strings.Contains(model, "diarize")
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "audio.ogg")
	if err != nil {
		return Result{}, err
	}
	if _, err := part.Write(audio); err != nil {
		return Result{}, err
	}
	fields := map[string]string{"model": model}
	// Without this the provider guesses per PIECE, so a long meeting could even
	// come back with its pieces in different languages.
	if language != "" && language != LanguageAuto {
		fields["language"] = language
	}
	if diarize {
		fields["response_format"] = "diarized_json"
		fields["chunking_strategy"] = "auto"
	} else {
		fields["response_format"] = "verbose_json"
	}
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return Result{}, err
		}
	}
	if diarize {
		for _, k := range known {
			_ = w.WriteField("known_speaker_names[]", k.Name)
			_ = w.WriteField("known_speaker_references[]", k.DataURL)
		}
	}
	if err := w.Close(); err != nil {
		return Result{}, err
	}
	raw, status, err := c.do(ctx, "/audio/transcriptions", w.FormDataContentType(), body.Bytes())
	if err != nil {
		return Result{}, err
	}
	if status == 404 || (status == 400 && strings.Contains(string(raw), "model")) {
		return Result{}, fmt.Errorf("%w: %s (%d)", errModelUnavailable, model, status)
	}
	if status >= 400 {
		return Result{}, fmt.Errorf("%w: %s", ErrRejected, problemText(raw, status))
	}
	var parsed struct {
		Text     string `json:"text"`
		Language string `json:"language"`
		Segments []struct {
			Start   float64 `json:"start"`
			End     float64 `json:"end"`
			Speaker string  `json:"speaker"`
			Text    string  `json:"text"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Result{}, fmt.Errorf("%w: unreadable answer", ErrRejected)
	}
	out := Result{Text: parsed.Text, Language: parsed.Language, Diarized: diarize, Model: model}
	for _, s := range parsed.Segments {
		out.Segments = append(out.Segments, meeting.Segment{Start: s.Start, End: s.End, Speaker: s.Speaker, Text: s.Text})
	}
	if len(out.Segments) == 0 && strings.TrimSpace(parsed.Text) != "" {
		out.Segments = []meeting.Segment{{Text: parsed.Text}}
	}
	return out, nil
}

// Summarize writes the digest in the meeting's own language (RM-32) and
// reports that language, which the diarizing model does not.
func (c *OpenAI) Summarize(ctx context.Context, transcript string) (meeting.Summary, error) {
	schema := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"language", "summary", "agreements", "actions"},
		"properties": map[string]any{
			"language":   map[string]any{"type": "string", "description": "ISO-639-1 code of the transcript's language"},
			"summary":    map[string]any{"type": "string"},
			"agreements": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"actions":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
	}
	req := map[string]any{
		"model": c.SummaryModel,
		"messages": []map[string]string{
			{"role": "system", "content": "Eres el secretario de una reunión. Resume la transcripción en el MISMO idioma en que está escrita: un resumen breve (5-8 frases), la lista de acuerdos tomados y la lista de acciones con responsable si se menciona. Sin inventar nada que no esté en el texto. Indica el idioma de la transcripción como código ISO-639-1."},
			{"role": "user", "content": transcript},
		},
		"response_format": map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "meeting_summary", "strict": true, "schema": schema}},
	}
	payload, _ := json.Marshal(req)
	raw, status, err := c.do(ctx, "/chat/completions", "application/json", payload)
	if err != nil {
		return meeting.Summary{}, err
	}
	if status >= 400 {
		return meeting.Summary{}, fmt.Errorf("%w: %s", ErrRejected, problemText(raw, status))
	}
	var reply struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil || len(reply.Choices) == 0 {
		return meeting.Summary{}, fmt.Errorf("%w: unreadable answer", ErrRejected)
	}
	var digest struct {
		Language   string   `json:"language"`
		Summary    string   `json:"summary"`
		Agreements []string `json:"agreements"`
		Actions    []string `json:"actions"`
	}
	if err := json.Unmarshal([]byte(reply.Choices[0].Message.Content), &digest); err != nil {
		return meeting.Summary{}, fmt.Errorf("%w: the summary is not the JSON asked for", ErrRejected)
	}
	return meeting.Summary{Language: digest.Language, Text: digest.Summary, Agreements: orEmpty(digest.Agreements), Actions: orEmpty(digest.Actions), Model: c.SummaryModel, GeneratedAt: time.Now().UTC()}, nil
}

// do posts with retries on transport errors and 5xx (RM-33); 4xx returns as is.
func (c *OpenAI) do(ctx context.Context, path, contentType string, payload []byte) ([]byte, int, error) {
	var last error
	for attempt := 0; attempt <= c.Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			case <-time.After(c.Backoff * time.Duration(1<<(attempt-1))):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(payload))
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
		req.Header.Set("Content-Type", contentType)
		res, err := c.HTTP.Do(req)
		if err != nil {
			last = err
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
		res.Body.Close()
		if err != nil {
			last = err
			continue
		}
		if res.StatusCode >= 500 || res.StatusCode == 429 {
			last = fmt.Errorf("provider %d: %s", res.StatusCode, problemText(raw, res.StatusCode))
			continue
		}
		return raw, res.StatusCode, nil
	}
	return nil, 0, fmt.Errorf("provider unreachable after %d attempts: %w", c.Retries+1, last)
}

func problemText(raw []byte, status int) string {
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &body) == nil && body.Error.Message != "" {
		return fmt.Sprintf("%d %s", status, body.Error.Message)
	}
	return fmt.Sprintf("%d %s", status, strings.TrimSpace(string(bytes.ToValidUTF8(raw[:min(len(raw), 200)], nil))))
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
