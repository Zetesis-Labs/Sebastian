package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zetesis-labs/sebastian/server/internal/device"
	"github.com/zetesis-labs/sebastian/server/internal/meeting"
)

type stubMeetings struct {
	err      error
	m        meeting.Meeting
	pending  string
	audio    string
	got      string
	offset   int64
	reported []string
	warned   int
}

func (s *stubMeetings) Start(_ context.Context, deviceID string, origin meeting.Origin) (meeting.Meeting, error) {
	s.m.DeviceID, s.m.RequestedBy = deviceID, origin
	return s.m, s.err
}

func (s *stubMeetings) StartFromUnit(_ context.Context, deviceID, secret string) (meeting.Meeting, error) {
	s.reported = append(s.reported, "start|"+deviceID+"|"+secret)
	s.m.DeviceID, s.m.RequestedBy = deviceID, meeting.OriginGesture
	return s.m, s.err
}

func (s *stubMeetings) Stop(_ context.Context, _ uuid.UUID, reason meeting.EndReason) (meeting.Meeting, error) {
	s.m.EndReason = reason
	return s.m, s.err
}

func (s *stubMeetings) Report(_ context.Context, deviceID, secret string, id uuid.UUID, state string, reason meeting.EndReason) error {
	s.reported = append(s.reported, strings.Join([]string{deviceID, secret, id.String(), state, string(reason)}, "|"))
	return s.err
}
func (s *stubMeetings) Warn(context.Context, uuid.UUID) error         { s.warned++; return s.err }
func (s *stubMeetings) PendingCommand(context.Context, string) string { return s.pending }
func (s *stubMeetings) Audio(_ context.Context, _ uuid.UUID, offset int64, r io.Reader) (int64, error) {
	s.offset = offset
	b, _ := io.ReadAll(r)
	s.got = string(b)
	return offset + int64(len(b)), s.err
}
func (s *stubMeetings) AudioFile(context.Context, uuid.UUID) (string, error) { return s.audio, s.err }
func (s *stubMeetings) Get(context.Context, uuid.UUID) (meeting.Meeting, error) {
	return s.m, s.err
}
func (s *stubMeetings) List(context.Context, meeting.Filter) ([]meeting.Meeting, error) {
	return []meeting.Meeting{s.m}, s.err
}
func (s *stubMeetings) Delete(context.Context, uuid.UUID) error { return s.err }
func (s *stubMeetings) Patch(_ context.Context, _ uuid.UUID, speakers map[string]string, keep *bool) (meeting.Meeting, error) {
	if s.err != nil {
		return meeting.Meeting{}, s.err
	}
	if len(speakers) > 0 {
		if s.m.Transcript == nil {
			return meeting.Meeting{}, meeting.ErrInvalid
		}
		t := s.m.Transcript.Rename(speakers)
		s.m.Transcript = &t
	}
	if keep != nil {
		s.m.Keep = *keep
	}
	return s.m, nil
}
func (s *stubMeetings) Retranscribe(context.Context, uuid.UUID) (meeting.Meeting, error) {
	s.reported = append(s.reported, "retranscribe")
	return s.m, s.err
}
func (s *stubMeetings) Resummarize(context.Context, uuid.UUID) (meeting.Meeting, error) {
	s.reported = append(s.reported, "resummarize")
	return s.m, s.err
}

func meetingsHandler(m *stubMeetings) *Server {
	return NewHandler(nil, nil, &stubDevices{}, m, stubReadiness{}, testLogger(), time.Second)
}

// T-A2 at the edge: the service's refusals become the spec's HTTP answers.
func TestStartMeetingMapsRefusals(t *testing.T) {
	id := uuid.New()
	cases := []struct {
		err  error
		want string
	}{
		{meeting.ErrBusy, "*api.StartMeeting409ApplicationProblemPlusJSONResponse"},
		{meeting.ErrProfile, "*api.StartMeeting422ApplicationProblemPlusJSONResponse"},
		{meeting.ErrNotAdopted, "*api.StartMeeting422ApplicationProblemPlusJSONResponse"},
		{meeting.ErrAbsent, "*api.StartMeeting422ApplicationProblemPlusJSONResponse"},
		{device.ErrNotFound, "*api.StartMeeting404ApplicationProblemPlusJSONResponse"},
	}
	for _, c := range cases {
		h := meetingsHandler(&stubMeetings{err: c.err, m: meeting.Meeting{ID: id}})
		got, _ := h.StartMeeting(context.Background(), StartMeetingRequestObject{DeviceId: "68ee"})
		if name := typeName(got); name != strings.TrimPrefix(c.want, "*") {
			t.Fatalf("%v → %s, want %s", c.err, name, c.want)
		}
	}
	stub := &stubMeetings{m: meeting.Meeting{ID: id, State: meeting.StateRequested, RequestedAt: time.Now()}}
	h := meetingsHandler(stub)
	by := MeetingStartRequestedBy("voice")
	got, _ := h.StartMeeting(context.Background(), StartMeetingRequestObject{DeviceId: "68ee", Body: &MeetingStart{RequestedBy: &by}})
	ok, isOK := got.(StartMeeting202JSONResponse)
	if !isOK || ok.Id != id || ok.State != "requested" || stub.m.RequestedBy != meeting.OriginVoice {
		t.Fatalf("202: %#v", got)
	}
}

func TestLiveDurationTravelsWhileRecording(t *testing.T) {
	start := time.Now().Add(-90 * time.Second)
	h := meetingsHandler(&stubMeetings{m: meeting.Meeting{ID: uuid.New(), State: meeting.StateRecording, StartedAt: start, AudioPath: "x.ogg"}})
	got, _ := h.GetMeeting(context.Background(), GetMeetingRequestObject{MeetingId: uuid.New()})
	m := got.(GetMeeting200JSONResponse)
	if m.DurationMs < 89_000 || m.DurationMs > 92_000 || !m.HasAudio {
		t.Fatalf("live duration (RM-14): %+v", m)
	}
}

// T-A5 at the edge: the unit's report carries its secret and the meeting.
func TestReportMeetingForwardsTheSecretAndMapsAuth(t *testing.T) {
	id := uuid.New()
	stub := &stubMeetings{}
	h := meetingsHandler(stub)
	reason := MeetingReportReason("gesture")
	got, _ := h.ReportMeeting(context.Background(), ReportMeetingRequestObject{
		DeviceId: "68ee", Params: ReportMeetingParams{XDeviceSecret: "s3cret"},
		Body: &MeetingReport{MeetingId: id, State: "stopped", Reason: &reason},
	})
	if _, ok := got.(ReportMeeting204Response); !ok || stub.reported[0] != "68ee|s3cret|"+id.String()+"|stopped|gesture" {
		t.Fatalf("report: %#v %v", got, stub.reported)
	}
	h = meetingsHandler(&stubMeetings{err: device.ErrUnauthorized})
	got, _ = h.ReportMeeting(context.Background(), ReportMeetingRequestObject{DeviceId: "68ee", Body: &MeetingReport{MeetingId: id, State: "recording"}})
	if _, ok := got.(ReportMeeting401ApplicationProblemPlusJSONResponse); !ok {
		t.Fatalf("wrong secret must be 401: %#v", got)
	}
}

// T-A4 at the edge: the poll carries the pending order.
func TestPollCarriesThePendingMeetingOrder(t *testing.T) {
	h := NewHandler(nil, nil, &stubDevices{desired: "agente"}, &stubMeetings{pending: "start:abc"}, stubReadiness{}, testLogger(), time.Second)
	got, _ := h.GetDesiredProfile(context.Background(), GetDesiredProfileRequestObject{DeviceId: "68ee"})
	text := got.(GetDesiredProfile200TextResponse)
	if text.Headers.XMeeting == nil || *text.Headers.XMeeting != "start:abc" {
		t.Fatalf("X-Meeting = %v", text.Headers.XMeeting)
	}
	h = NewHandler(nil, nil, &stubDevices{desired: "agente"}, &stubMeetings{}, stubReadiness{}, testLogger(), time.Second)
	got, _ = h.GetDesiredProfile(context.Background(), GetDesiredProfileRequestObject{DeviceId: "68ee"})
	if text := got.(GetDesiredProfile200TextResponse); text.Headers.XMeeting != nil {
		t.Fatal("no order, no header")
	}
}

// T-A6 at the edge: the raw upload needs the agent secret and resumes by offset.
func TestMeetingAudioUploadAuthenticatesAndResumes(t *testing.T) {
	stub := &stubMeetings{}
	mux := http.NewServeMux()
	mux.Handle("PUT /v1/meetings/{id}/audio", MeetingAudioUpload(stub, "agent-secret"))
	mux.Handle("HEAD /v1/meetings/{id}/audio", MeetingAudioOffset(stub)("agent-secret"))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	id := uuid.New()
	stub.m = meeting.Meeting{ID: id, State: meeting.StateRecording, AudioBytes: 11}
	head, _ := http.NewRequest(http.MethodHead, srv.URL+"/v1/meetings/"+id.String()+"/audio", nil)
	head.Header.Set("X-Agent-Secret", "agent-secret")
	if res, err := http.DefaultClient.Do(head); err != nil || res.StatusCode != 204 || res.Header.Get("X-Audio-Bytes") != "11" || res.Header.Get("X-Meeting-State") != "recording" {
		t.Fatalf("HEAD tells the agent where to resume: %v %+v", err, res)
	}
	put := func(secret, contentRange, body string) *http.Response {
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/v1/meetings/"+id.String()+"/audio", strings.NewReader(body))
		req.Header.Set("X-Agent-Secret", secret)
		if contentRange != "" {
			req.Header.Set("Content-Range", contentRange)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	if res := put("wrong", "", "x"); res.StatusCode != 401 {
		t.Fatalf("wrong agent secret: %d", res.StatusCode)
	}
	if res := put("agent-secret", "bytes=0-", "x"); res.StatusCode != 400 {
		t.Fatalf("bad content-range: %d", res.StatusCode)
	}
	res := put("agent-secret", "bytes 11-*/*", "OggS-part-2")
	if res.StatusCode != 204 || stub.offset != 11 || stub.got != "OggS-part-2" || res.Header.Get("X-Audio-Bytes") != "22" {
		t.Fatalf("resume: %d offset=%d got=%q bytes=%s", res.StatusCode, stub.offset, stub.got, res.Header.Get("X-Audio-Bytes"))
	}
	stub.err = meeting.ErrOffset
	if res := put("agent-secret", "", "x"); res.StatusCode != 409 || res.Header.Get("X-Audio-Bytes") == "" {
		t.Fatalf("offset mismatch tells the real size: %d %q", res.StatusCode, res.Header.Get("X-Audio-Bytes"))
	}
}

func TestMeetingAudioDownloadServesRanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.ogg")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /v1/admin/meetings/{id}/audio", MeetingAudioDownload(&stubMeetings{audio: path}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/admin/meetings/"+uuid.NewString()+"/audio", nil)
	req.Header.Set("Range", "bytes=2-4")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 206 || string(body) != "234" || res.Header.Get("Content-Type") != "audio/ogg" {
		t.Fatalf("range: %d %q %s", res.StatusCode, body, res.Header.Get("Content-Type"))
	}
}

func typeName(v any) string {
	return strings.TrimPrefix(fmt.Sprintf("%T", v), "*")
}

// Block C: the agent stops by silence or voice and warns 30 s before (RM-21/23).
func TestAgentStopAndWarnRoutes(t *testing.T) {
	stub := &stubMeetings{m: meeting.Meeting{ID: uuid.New(), State: meeting.StateRecording}}
	mux := http.NewServeMux()
	mux.Handle("POST /v1/meetings/{id}/stop", MeetingAgentStop(stub, "agent-secret"))
	mux.Handle("POST /v1/meetings/{id}/warn", MeetingAgentWarn(stub, "agent-secret"))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	post := func(path, secret, body string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/meetings/"+stub.m.ID.String()+path, strings.NewReader(body))
		req.Header.Set("X-Agent-Secret", secret)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	if res := post("/stop", "wrong", `{"reason":"silence"}`); res.StatusCode != 401 {
		t.Fatalf("wrong secret: %d", res.StatusCode)
	}
	if res := post("/stop", "agent-secret", `{"reason":"dashboard"}`); res.StatusCode != 400 {
		t.Fatalf("the agent may only stop by silence or voice: %d", res.StatusCode)
	}
	if res := post("/stop", "agent-secret", `{"reason":"silence"}`); res.StatusCode != 202 || stub.m.EndReason != meeting.EndSilence {
		t.Fatalf("stop by silence: %d reason=%q", res.StatusCode, stub.m.EndReason)
	}
	if res := post("/warn", "agent-secret", ""); res.StatusCode != 204 || stub.warned != 1 {
		t.Fatalf("warn: %d warned=%d", res.StatusCode, stub.warned)
	}
	stub.err = meeting.ErrNotRecording
	if res := post("/warn", "agent-secret", ""); res.StatusCode != 409 {
		t.Fatalf("warn after the stop: %d", res.StatusCode)
	}
	if res := post("/stop", "agent-secret", `{"reason":"voice"}`); res.StatusCode != 409 {
		t.Fatalf("stop after the stop: %d", res.StatusCode)
	}
	stub.err = meeting.ErrNotFound
	if res := post("/warn", "agent-secret", ""); res.StatusCode != 404 {
		t.Fatalf("unknown meeting: %d", res.StatusCode)
	}
}

// Block D at the edge: the transcript travels, downloads render, renames and
// retries map to the spec's answers.
func TestTranscriptDownloadsRenamesAndRetries(t *testing.T) {
	id := uuid.New()
	tr := meeting.Transcript{Diarized: true, Segments: []meeting.Segment{{Start: 0, End: 2.5, Speaker: "Hablante 1", Text: "Hola"}}}
	tr.Text = tr.PlainText()
	stub := &stubMeetings{m: meeting.Meeting{ID: id, State: meeting.StateReady, Transcript: &tr, Summary: &meeting.Summary{Text: "resumen", Model: "mini"}}}
	h := meetingsHandler(stub)
	ctx := context.Background()

	got, _ := h.GetMeeting(ctx, GetMeetingRequestObject{MeetingId: id})
	body := got.(GetMeeting200JSONResponse)
	if body.Transcript == nil || body.Transcript.Segments[0].Speaker != "Hablante 1" || body.Summary == nil || body.Summary.Text != "resumen" || len(body.Summary.Agreements) != 0 {
		t.Fatalf("detail: %+v", body)
	}
	srt := Srt
	txt, _ := h.GetMeetingTranscript(ctx, GetMeetingTranscriptRequestObject{MeetingId: id})
	if string(txt.(GetMeetingTranscript200TextResponse)) != "[00:00] Hablante 1: Hola\n" {
		t.Fatalf("txt = %q", txt)
	}
	sub, _ := h.GetMeetingTranscript(ctx, GetMeetingTranscriptRequestObject{MeetingId: id, Params: GetMeetingTranscriptParams{Format: &srt}})
	if !strings.HasPrefix(string(sub.(GetMeetingTranscript200TextResponse)), "1\n00:00:00,000 --> 00:00:02,500\nHablante 1: Hola") {
		t.Fatalf("srt = %q", sub)
	}
	keep := true
	names := map[string]string{"Hablante 1": "Ana"}
	upd, _ := h.UpdateMeeting(ctx, UpdateMeetingRequestObject{MeetingId: id, Body: &MeetingPatch{Speakers: &names, Keep: &keep}})
	u := upd.(UpdateMeeting200JSONResponse)
	if !u.Keep || (*u.Transcript.Speakers)["Hablante 1"] != "Ana" || u.Transcript.Text != "Ana: Hola\n" {
		t.Fatalf("patch: %+v", u)
	}
	stub.m.Transcript = nil
	if res, _ := h.UpdateMeeting(ctx, UpdateMeetingRequestObject{MeetingId: id, Body: &MeetingPatch{Speakers: &names}}); fmt.Sprintf("%T", res) != "api.UpdateMeeting409ApplicationProblemPlusJSONResponse" {
		t.Fatalf("rename without transcript: %T", res)
	}
	if res, _ := h.GetMeetingTranscript(ctx, GetMeetingTranscriptRequestObject{MeetingId: id}); fmt.Sprintf("%T", res) != "api.GetMeetingTranscript404ApplicationProblemPlusJSONResponse" {
		t.Fatalf("download without transcript: %T", res)
	}
	if res, _ := h.TranscribeMeeting(ctx, TranscribeMeetingRequestObject{MeetingId: id}); fmt.Sprintf("%T", res) != "api.TranscribeMeeting202JSONResponse" {
		t.Fatalf("retranscribe: %T", res)
	}
	stub.err = meeting.ErrInvalid
	if res, _ := h.TranscribeMeeting(ctx, TranscribeMeetingRequestObject{MeetingId: id}); fmt.Sprintf("%T", res) != "api.TranscribeMeeting409ApplicationProblemPlusJSONResponse" {
		t.Fatalf("retranscribe while busy: %T", res)
	}
	if res, _ := h.SummarizeMeeting(ctx, SummarizeMeetingRequestObject{MeetingId: id}); fmt.Sprintf("%T", res) != "api.SummarizeMeeting409ApplicationProblemPlusJSONResponse" {
		t.Fatalf("summarize without transcript: %T", res)
	}
	stub.err = nil
	q := "hola"
	if _, err := h.ListMeetings(ctx, ListMeetingsRequestObject{Params: ListMeetingsParams{Q: &q}}); err != nil {
		t.Fatal(err)
	}
}
