package transcribe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type capture struct {
	fields   map[string][]string
	fileName string
	fileSize int
	headers  http.Header
	body     string
}

func fakeServer(t *testing.T, handler func(c capture, calls int32, w http.ResponseWriter)) (*httptest.Server, *[]capture) {
	t.Helper()
	var calls int32
	var seen []capture
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := capture{headers: r.Header.Clone(), fields: map[string][]string{}}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			if err := r.ParseMultipartForm(64 << 20); err != nil {
				t.Errorf("multipart: %v", err)
			}
			c.fields = r.MultipartForm.Value
			f, hdr, err := r.FormFile("file")
			if err == nil {
				b, _ := io.ReadAll(f)
				c.fileName, c.fileSize = hdr.Filename, len(b)
			}
		} else {
			b, _ := io.ReadAll(r.Body)
			c.body = string(b)
		}
		seen = append(seen, c)
		handler(c, atomic.AddInt32(&calls, 1), w)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func client(srv *httptest.Server) *OpenAI {
	c := NewOpenAI("sk-test")
	c.BaseURL, c.Backoff = srv.URL, time.Millisecond
	return c
}

// T-D3 + T-D6: the multipart is what the diarizing model wants and carries
// nothing about the unit or the control room.
func TestTranscribeSendsAudioAndSpeakersOnly(t *testing.T) {
	srv, seen := fakeServer(t, func(c capture, _ int32, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"task":"transcribe","duration":9.1,"text":"Hablante 1: Hola.\nA: Buenas.","segments":[{"start":0,"end":4,"speaker":"Hablante 1","text":"Hola."},{"start":4.2,"end":9.1,"speaker":"A","text":"Buenas."}]}`))
	})
	res, err := client(srv).Transcribe(context.Background(), []byte("OggS...."), []Reference{{Name: "Hablante 1", DataURL: "data:audio/wav;base64,AAAA"}})
	if err != nil {
		t.Fatal(err)
	}
	c := (*seen)[0]
	if c.headers.Get("Authorization") != "Bearer sk-test" || c.fileName != "audio.ogg" || c.fileSize != 8 {
		t.Fatalf("request: %+v", c)
	}
	want := map[string]string{"model": "gpt-4o-transcribe-diarize", "response_format": "diarized_json", "chunking_strategy": "auto", "known_speaker_names[]": "Hablante 1", "known_speaker_references[]": "data:audio/wav;base64,AAAA"}
	for k, v := range want {
		if got := c.fields[k]; len(got) != 1 || got[0] != v {
			t.Fatalf("field %s = %v, want %q", k, got, v)
		}
	}
	for k := range c.fields {
		if _, ok := want[k]; !ok {
			t.Fatalf("unexpected field %q: only audio and model parameters may travel (RM-35)", k)
		}
	}
	if !res.Diarized || len(res.Segments) != 2 || res.Segments[1].Speaker != "A" || res.Segments[1].End != 9.1 || res.Model != DefaultTranscribeModel {
		t.Fatalf("result = %+v", res)
	}
}

func TestTranscribeFallsBackToWhisperWhenTheModelIsMissing(t *testing.T) {
	srv, seen := fakeServer(t, func(c capture, _ int32, w http.ResponseWriter) {
		if c.fields["model"][0] != "whisper-1" {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"message":"The model gpt-4o-transcribe-diarize does not exist"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"text":"todo seguido","language":"spanish","segments":[{"start":0,"end":3,"text":"todo seguido"}]}`))
	})
	res, err := client(srv).Transcribe(context.Background(), []byte("x"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 2 || (*seen)[1].fields["response_format"][0] != "verbose_json" || len((*seen)[1].fields["known_speaker_names[]"]) != 0 {
		t.Fatalf("fallback request: %+v", (*seen)[1].fields)
	}
	if res.Diarized || res.Language != "spanish" || res.Model != "whisper-1" || res.Segments[0].Speaker != "" {
		t.Fatalf("result = %+v", res)
	}
}

func TestProviderErrorsAreFinalOrRetried(t *testing.T) {
	srv, seen := fakeServer(t, func(_ capture, _ int32, w http.ResponseWriter) {
		w.WriteHeader(413)
		_, _ = w.Write([]byte(`{"error":{"message":"Maximum content size limit exceeded"}}`))
	})
	_, err := client(srv).Transcribe(context.Background(), []byte("x"), nil)
	if !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), "413 Maximum content size") || len(*seen) != 1 {
		t.Fatalf("4xx is final with the reason (RM-33): %v calls=%d", err, len(*seen))
	}
	srv2, seen2 := fakeServer(t, func(_ capture, calls int32, w http.ResponseWriter) {
		if calls < 3 {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(`{"text":"ok","segments":[{"start":0,"end":1,"speaker":"A","text":"ok"}]}`))
	})
	res, err := client(srv2).Transcribe(context.Background(), []byte("x"), nil)
	if err != nil || res.Text != "ok" || len(*seen2) != 3 {
		t.Fatalf("5xx retries with backoff: %v calls=%d", err, len(*seen2))
	}
	srv3, seen3 := fakeServer(t, func(_ capture, _ int32, w http.ResponseWriter) { w.WriteHeader(500) })
	if _, err := client(srv3).Transcribe(context.Background(), []byte("x"), nil); err == nil || errors.Is(err, ErrRejected) || len(*seen3) != 4 {
		t.Fatalf("exhausted retries are not final: %v calls=%d", err, len(*seen3))
	}
}

// T-D4: the summary asks for the meeting's own language and structured output.
func TestSummarizeAsksForStructuredDigestInTheMeetingLanguage(t *testing.T) {
	srv, seen := fakeServer(t, func(_ capture, _ int32, w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"language\":\"es\",\"summary\":\"Se acordó el plan.\",\"agreements\":[\"Plan aprobado\"],\"actions\":[\"Ana envía el acta\"]}"}}]}`))
	})
	c := client(srv)
	s, err := c.Summarize(context.Background(), "Hablante 1: Aprobamos el plan.\nHablante 2: Ana envía el acta.\n")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]any
	if err := json.Unmarshal([]byte((*seen)[0].body), &req); err != nil {
		t.Fatal(err)
	}
	if req["model"] != DefaultSummaryModel {
		t.Fatalf("model = %v", req["model"])
	}
	rf := req["response_format"].(map[string]any)
	if rf["type"] != "json_schema" {
		t.Fatalf("response_format = %v", rf)
	}
	msgs := req["messages"].([]any)
	system := msgs[0].(map[string]any)["content"].(string)
	if !strings.Contains(system, "MISMO idioma") || !strings.Contains(system, "ISO-639-1") {
		t.Fatalf("the prompt must demand the transcript's language: %q", system)
	}
	if strings.Contains((*seen)[0].body, "68ee") || strings.Contains((*seen)[0].body, "control room") {
		t.Fatal("no unit or control room data leaves (RM-35)")
	}
	if s.Language != "es" || s.Text != "Se acordó el plan." || s.Agreements[0] != "Plan aprobado" || s.Actions[0] != "Ana envía el acta" || s.Model != DefaultSummaryModel || s.GeneratedAt.IsZero() {
		t.Fatalf("summary = %+v", s)
	}
}
