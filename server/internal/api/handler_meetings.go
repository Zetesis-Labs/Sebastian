package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zetesis-labs/sebastian/server/internal/device"
	"github.com/zetesis-labs/sebastian/server/internal/meeting"
)

// Meeting recordings (docs/implementation/14-meeting-recordings-technical-design.md §3).

type MeetingService interface {
	Start(ctx context.Context, deviceID string, origin meeting.Origin) (meeting.Meeting, error)
	StartFromUnit(ctx context.Context, deviceID, secret string) (meeting.Meeting, error)
	Stop(ctx context.Context, id uuid.UUID, reason meeting.EndReason) (meeting.Meeting, error)
	Report(ctx context.Context, deviceID, secret string, id uuid.UUID, state string, reason meeting.EndReason) error
	Warn(ctx context.Context, id uuid.UUID) error
	PendingCommand(ctx context.Context, deviceID string) string
	Audio(ctx context.Context, id uuid.UUID, offset int64, r io.Reader) (int64, error)
	AudioFile(ctx context.Context, id uuid.UUID) (string, error)
	Get(ctx context.Context, id uuid.UUID) (meeting.Meeting, error)
	List(ctx context.Context, f meeting.Filter) ([]meeting.Meeting, error)
	Delete(ctx context.Context, id uuid.UUID) error
	Patch(ctx context.Context, id uuid.UUID, speakers map[string]string, keep *bool) (meeting.Meeting, error)
	Retranscribe(ctx context.Context, id uuid.UUID) (meeting.Meeting, error)
	Resummarize(ctx context.Context, id uuid.UUID) (meeting.Meeting, error)
}

func (h *Server) StartMeeting(ctx context.Context, request StartMeetingRequestObject) (StartMeetingResponseObject, error) {
	origin := meeting.OriginDashboard
	if request.Body != nil && request.Body.RequestedBy != nil {
		origin = meeting.Origin(*request.Body.RequestedBy)
	}
	m, err := h.meetings.Start(ctx, request.DeviceId, origin)
	switch {
	case errors.Is(err, device.ErrNotFound):
		return StartMeeting404ApplicationProblemPlusJSONResponse{DeviceNotFoundApplicationProblemPlusJSONResponse: notFound()}, nil
	case errors.Is(err, meeting.ErrBusy):
		return StartMeeting409ApplicationProblemPlusJSONResponse(problem(409, "Already recording", "A meeting is already in progress on this unit.")), nil
	case errors.Is(err, meeting.ErrProfile):
		return StartMeeting422ApplicationProblemPlusJSONResponse(problem(422, "Profile without recording", "In the micro-usb profile the unit is the computer's microphone; record from the computer or switch it to agente.")), nil
	case errors.Is(err, meeting.ErrNotAdopted):
		return StartMeeting422ApplicationProblemPlusJSONResponse(problem(422, "Not adopted here", "Only the control room that adopted the unit can make it record.")), nil
	case errors.Is(err, meeting.ErrAbsent):
		return StartMeeting422ApplicationProblemPlusJSONResponse(problem(422, "Unit not contacting", "The unit has not contacted this control room lately; it cannot be asked to record.")), nil
	case err != nil:
		return StartMeeting503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "start meeting failed", err, "device_id", request.DeviceId)}, nil
	}
	return StartMeeting202JSONResponse(meetingResponse(m, h.now())), nil
}

// RequestMeeting is the gesture path (RM-02): the unit asks with its secret.
func (h *Server) RequestMeeting(ctx context.Context, request RequestMeetingRequestObject) (RequestMeetingResponseObject, error) {
	m, err := h.meetings.StartFromUnit(ctx, request.DeviceId, request.Params.XDeviceSecret)
	switch {
	case errors.Is(err, device.ErrUnauthorized):
		return RequestMeeting401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse: UnauthorizedApplicationProblemPlusJSONResponse(problem(401, "Unauthorized", "Invalid device credentials."))}, nil
	case errors.Is(err, meeting.ErrBusy):
		return RequestMeeting409ApplicationProblemPlusJSONResponse(problem(409, "Already recording", "A meeting is already in progress on this unit.")), nil
	case errors.Is(err, meeting.ErrProfile), errors.Is(err, meeting.ErrNotAdopted), errors.Is(err, meeting.ErrAbsent):
		return RequestMeeting422ApplicationProblemPlusJSONResponse(problem(422, "Cannot record", err.Error())), nil
	case err != nil:
		return RequestMeeting503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "request meeting failed", err, "device_id", request.DeviceId)}, nil
	}
	return RequestMeeting202JSONResponse(meetingResponse(m, h.now())), nil
}

func (h *Server) StopMeeting(ctx context.Context, request StopMeetingRequestObject) (StopMeetingResponseObject, error) {
	reason := meeting.EndDashboard
	if request.Body != nil && request.Body.Reason != nil {
		reason = meeting.EndReason(*request.Body.Reason)
	}
	m, err := h.meetings.Stop(ctx, request.MeetingId, reason)
	switch {
	case errors.Is(err, meeting.ErrNotFound):
		return StopMeeting404ApplicationProblemPlusJSONResponse{MeetingNotFoundApplicationProblemPlusJSONResponse: meetingNotFound()}, nil
	case errors.Is(err, meeting.ErrNotRecording):
		return StopMeeting409ApplicationProblemPlusJSONResponse(problem(409, "Not recording", "The meeting is not in progress.")), nil
	case err != nil:
		return StopMeeting503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "stop meeting failed", err, "meeting_id", request.MeetingId)}, nil
	}
	return StopMeeting202JSONResponse(meetingResponse(m, h.now())), nil
}

func (h *Server) ListMeetings(ctx context.Context, request ListMeetingsRequestObject) (ListMeetingsResponseObject, error) {
	f := meeting.Filter{Limit: 50}
	if request.Params.DeviceId != nil {
		f.DeviceID = *request.Params.DeviceId
	}
	if request.Params.State != nil {
		f.State = meeting.State(*request.Params.State)
	}
	if request.Params.Q != nil {
		f.Query = strings.TrimSpace(*request.Params.Q)
	}
	if request.Params.Limit != nil {
		f.Limit = max(1, min(*request.Params.Limit, 200))
	}
	items, err := h.meetings.List(ctx, f)
	if err != nil {
		return ListMeetings503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "list meetings failed", err)}, nil
	}
	now := h.now()
	out := MeetingList{Items: make([]Meeting, 0, len(items))}
	for _, m := range items {
		out.Items = append(out.Items, meetingResponse(m, now))
	}
	return ListMeetings200JSONResponse(out), nil
}

func (h *Server) GetMeeting(ctx context.Context, request GetMeetingRequestObject) (GetMeetingResponseObject, error) {
	m, err := h.meetings.Get(ctx, request.MeetingId)
	if errors.Is(err, meeting.ErrNotFound) {
		return GetMeeting404ApplicationProblemPlusJSONResponse{MeetingNotFoundApplicationProblemPlusJSONResponse: meetingNotFound()}, nil
	}
	if err != nil {
		return GetMeeting503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "get meeting failed", err, "meeting_id", request.MeetingId)}, nil
	}
	return GetMeeting200JSONResponse(meetingResponse(m, h.now())), nil
}

// UpdateMeeting renames speakers and flags the meeting to keep (RM-31, RM-45).
func (h *Server) UpdateMeeting(ctx context.Context, request UpdateMeetingRequestObject) (UpdateMeetingResponseObject, error) {
	var speakers map[string]string
	var keep *bool
	if request.Body != nil {
		if request.Body.Speakers != nil {
			speakers = *request.Body.Speakers
		}
		keep = request.Body.Keep
	}
	m, err := h.meetings.Patch(ctx, request.MeetingId, speakers, keep)
	switch {
	case errors.Is(err, meeting.ErrNotFound):
		return UpdateMeeting404ApplicationProblemPlusJSONResponse{MeetingNotFoundApplicationProblemPlusJSONResponse: meetingNotFound()}, nil
	case errors.Is(err, meeting.ErrInvalid):
		return UpdateMeeting409ApplicationProblemPlusJSONResponse(problem(409, "No transcript", "There is no transcript whose speakers could be renamed.")), nil
	case err != nil:
		return UpdateMeeting503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "update meeting failed", err, "meeting_id", request.MeetingId)}, nil
	}
	return UpdateMeeting200JSONResponse(meetingResponse(m, h.now())), nil
}

// GetMeetingTranscript is the txt/srt download (RM-41).
func (h *Server) GetMeetingTranscript(ctx context.Context, request GetMeetingTranscriptRequestObject) (GetMeetingTranscriptResponseObject, error) {
	m, err := h.meetings.Get(ctx, request.MeetingId)
	if errors.Is(err, meeting.ErrNotFound) {
		return GetMeetingTranscript404ApplicationProblemPlusJSONResponse{MeetingNotFoundApplicationProblemPlusJSONResponse: meetingNotFound()}, nil
	}
	if err != nil {
		return GetMeetingTranscript503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "get meeting failed", err, "meeting_id", request.MeetingId)}, nil
	}
	if m.Transcript == nil {
		return GetMeetingTranscript404ApplicationProblemPlusJSONResponse{MeetingNotFoundApplicationProblemPlusJSONResponse: MeetingNotFoundApplicationProblemPlusJSONResponse(problem(404, "No transcript", "The meeting has no transcript."))}, nil
	}
	if request.Params.Format != nil && *request.Params.Format == Srt {
		return GetMeetingTranscript200TextResponse(m.Transcript.SRT()), nil
	}
	return GetMeetingTranscript200TextResponse(m.Transcript.TXT()), nil
}

// TranscribeMeeting queues the transcription again (RM-33).
func (h *Server) TranscribeMeeting(ctx context.Context, request TranscribeMeetingRequestObject) (TranscribeMeetingResponseObject, error) {
	m, err := h.meetings.Retranscribe(ctx, request.MeetingId)
	switch {
	case errors.Is(err, meeting.ErrNotFound):
		return TranscribeMeeting404ApplicationProblemPlusJSONResponse{MeetingNotFoundApplicationProblemPlusJSONResponse: meetingNotFound()}, nil
	case errors.Is(err, meeting.ErrInvalid):
		return TranscribeMeeting409ApplicationProblemPlusJSONResponse(problem(409, "Cannot transcribe now", "The meeting is in progress, already transcribing, or has no audio.")), nil
	case err != nil:
		return TranscribeMeeting503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "transcribe meeting failed", err, "meeting_id", request.MeetingId)}, nil
	}
	return TranscribeMeeting202JSONResponse(meetingResponse(m, h.now())), nil
}

// SummarizeMeeting regenerates the digest (RM-32).
func (h *Server) SummarizeMeeting(ctx context.Context, request SummarizeMeetingRequestObject) (SummarizeMeetingResponseObject, error) {
	m, err := h.meetings.Resummarize(ctx, request.MeetingId)
	switch {
	case errors.Is(err, meeting.ErrNotFound):
		return SummarizeMeeting404ApplicationProblemPlusJSONResponse{MeetingNotFoundApplicationProblemPlusJSONResponse: meetingNotFound()}, nil
	case errors.Is(err, meeting.ErrInvalid):
		return SummarizeMeeting409ApplicationProblemPlusJSONResponse(problem(409, "No transcript", "There is no transcript to summarize.")), nil
	case err != nil:
		return SummarizeMeeting503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "summarize meeting failed", err, "meeting_id", request.MeetingId)}, nil
	}
	return SummarizeMeeting202JSONResponse(meetingResponse(m, h.now())), nil
}

func (h *Server) DeleteMeeting(ctx context.Context, request DeleteMeetingRequestObject) (DeleteMeetingResponseObject, error) {
	err := h.meetings.Delete(ctx, request.MeetingId)
	switch {
	case errors.Is(err, meeting.ErrNotFound):
		return DeleteMeeting404ApplicationProblemPlusJSONResponse{MeetingNotFoundApplicationProblemPlusJSONResponse: meetingNotFound()}, nil
	case errors.Is(err, meeting.ErrBusy):
		return DeleteMeeting409ApplicationProblemPlusJSONResponse(problem(409, "In progress", "Stop the meeting before deleting it.")), nil
	case err != nil:
		return DeleteMeeting503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "delete meeting failed", err, "meeting_id", request.MeetingId)}, nil
	}
	return DeleteMeeting204Response{}, nil
}

// ReportMeeting is the unit's confirmation (RM-52), authenticated with its secret.
func (h *Server) ReportMeeting(ctx context.Context, request ReportMeetingRequestObject) (ReportMeetingResponseObject, error) {
	if request.Body == nil {
		return ReportMeeting404ApplicationProblemPlusJSONResponse{MeetingNotFoundApplicationProblemPlusJSONResponse: meetingNotFound()}, nil
	}
	var reason meeting.EndReason
	if request.Body.Reason != nil {
		reason = meeting.EndReason(*request.Body.Reason)
	}
	err := h.meetings.Report(ctx, request.DeviceId, request.Params.XDeviceSecret, request.Body.MeetingId, string(request.Body.State), reason)
	switch {
	case errors.Is(err, device.ErrUnauthorized):
		return ReportMeeting401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse: UnauthorizedApplicationProblemPlusJSONResponse(problem(401, "Unauthorized", "Invalid device credentials."))}, nil
	case errors.Is(err, meeting.ErrNotFound), errors.Is(err, meeting.ErrInvalid):
		return ReportMeeting404ApplicationProblemPlusJSONResponse{MeetingNotFoundApplicationProblemPlusJSONResponse: meetingNotFound()}, nil
	case err != nil:
		return ReportMeeting503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "meeting report failed", err, "device_id", request.DeviceId)}, nil
	}
	return ReportMeeting204Response{}, nil
}

func meetingNotFound() MeetingNotFoundApplicationProblemPlusJSONResponse {
	return MeetingNotFoundApplicationProblemPlusJSONResponse(problem(404, "Meeting not found", "The meeting does not exist."))
}

func meetingResponse(m meeting.Meeting, now time.Time) Meeting {
	out := Meeting{
		Id: m.ID, DeviceId: m.DeviceID, State: MeetingState(m.State), RequestedBy: MeetingRequestedBy(m.RequestedBy),
		RequestedAt: m.RequestedAt, StartedAt: optionalTime(m.StartedAt), EndedAt: optionalTime(m.EndedAt),
		DurationMs: meeting.Live(m, now).Milliseconds(), AudioBytes: m.AudioBytes,
		HasAudio: m.AudioPath != "" && m.DeletedAt.IsZero(), Keep: m.Keep, DeletedAt: optionalTime(m.DeletedAt),
	}
	if m.EndReason != "" {
		reason := MeetingEndReason(m.EndReason)
		out.EndReason = &reason
	}
	if m.TranscriptError != "" {
		out.TranscriptError = &m.TranscriptError
	}
	if m.Transcript != nil {
		out.Transcript = transcriptResponse(*m.Transcript)
	}
	if m.Summary != nil {
		sum := *m.Summary
		out.Summary = &MeetingSummary{Text: sum.Text, Agreements: orEmpty(sum.Agreements), Actions: orEmpty(sum.Actions), Model: sum.Model, GeneratedAt: sum.GeneratedAt, Language: optionalString(sum.Language)}
	}
	return out
}

func transcriptResponse(t meeting.Transcript) *MeetingTranscript {
	out := &MeetingTranscript{Text: t.Text, Diarized: t.Diarized, Language: optionalString(t.Language), Model: optionalString(t.Model), Segments: make([]MeetingSegment, 0, len(t.Segments))}
	for _, s := range t.Segments {
		out.Segments = append(out.Segments, MeetingSegment{Start: s.Start, End: s.End, Speaker: s.Speaker, Text: s.Text})
	}
	if len(t.Speakers) > 0 {
		speakers := t.Speakers
		out.Speakers = &speakers
	}
	return out
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ── raw audio endpoints (outside the generated router: streaming and Range) ──

// MeetingAudioUpload takes the agent's Ogg/Opus stream (design §3.3):
// PUT /v1/meetings/{id}/audio with X-Agent-Secret, optional Content-Range
// "bytes <offset>-*/*" to resume. Long-lived: the caller must lift the
// server's read deadline for it.
func MeetingAudioUpload(meetings MeetingService, agentSecret string) http.Handler {
	return agentRoute(agentSecret, func(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
		offset, ok := rangeOffset(r.Header.Get("Content-Range"))
		if !ok {
			writeProblem(w, 400, "Bad Content-Range", `Use "bytes <offset>-*/*" or omit the header.`)
			return
		}
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Time{})
		_ = rc.SetWriteDeadline(time.Time{})
		size, err := meetings.Audio(r.Context(), id, offset, r.Body)
		switch {
		case errors.Is(err, meeting.ErrNotFound):
			writeProblem(w, 404, "Meeting not found", "The meeting does not exist.")
		case errors.Is(err, meeting.ErrOffset):
			w.Header().Set("X-Audio-Bytes", strconv.FormatInt(size, 10))
			writeProblem(w, 409, "Offset mismatch", "Resume from X-Audio-Bytes.")
		case errors.Is(err, meeting.ErrNotOpen):
			writeProblem(w, 409, "Not accepting audio", "The meeting is not in progress or another upload is open.")
		case err != nil:
			// The stream broke: what arrived is kept; the agent resumes from X-Audio-Bytes.
			w.Header().Set("X-Audio-Bytes", strconv.FormatInt(size, 10))
			writeProblem(w, 499, "Upload interrupted", err.Error())
		default:
			w.Header().Set("X-Audio-Bytes", strconv.FormatInt(size, 10))
			w.WriteHeader(http.StatusNoContent)
		}
	})
}

// MeetingAudioOffset tells the agent how much audio the server holds, so an
// interrupted upload resumes from there (design §3.3). HEAD /v1/meetings/{id}/audio.
func MeetingAudioOffset(meetings MeetingService) func(string) http.Handler {
	return func(agentSecret string) http.Handler {
		return agentRoute(agentSecret, func(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
			m, err := meetings.Get(r.Context(), id)
			if err != nil {
				writeProblem(w, 404, "Meeting not found", "The meeting does not exist.")
				return
			}
			w.Header().Set("X-Audio-Bytes", strconv.FormatInt(m.AudioBytes, 10))
			w.Header().Set("X-Meeting-State", string(m.State))
			w.WriteHeader(http.StatusNoContent)
		})
	}
}

// MeetingAgentStop is the agent's stop (design §3.3): by silence (RM-23) or
// by voice (RM-21). POST /v1/meetings/{id}/stop {"reason":"silence"|"voice"}.
func MeetingAgentStop(meetings MeetingService, agentSecret string) http.Handler {
	return agentRoute(agentSecret, func(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
		var body struct {
			Reason meeting.EndReason `json:"reason"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil || (body.Reason != meeting.EndSilence && body.Reason != meeting.EndVoice) {
			writeProblem(w, 400, "Bad reason", `The agent stops with {"reason":"silence"} or {"reason":"voice"}.`)
			return
		}
		m, err := meetings.Stop(r.Context(), id, body.Reason)
		switch {
		case errors.Is(err, meeting.ErrNotFound):
			writeProblem(w, 404, "Meeting not found", "The meeting does not exist.")
		case errors.Is(err, meeting.ErrNotRecording):
			writeProblem(w, 409, "Not recording", "The meeting is not in progress.")
		case err != nil:
			writeProblem(w, 503, "Unavailable", err.Error())
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(meetingResponse(m, time.Now()))
		}
	})
}

// MeetingAgentStart is the voice start (RM-04): the conversation agent asks
// for a meeting on its unit. POST /v1/meetings {"deviceId":"…"} → 202.
func MeetingAgentStart(meetings MeetingService, agentSecret string) http.Handler {
	expected := sha256.Sum256([]byte(agentSecret))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := sha256.Sum256([]byte(r.Header.Get("X-Agent-Secret")))
		if agentSecret == "" || subtle.ConstantTimeCompare(expected[:], provided[:]) != 1 {
			writeProblem(w, 401, "Unauthorized", "Invalid agent credentials.")
			return
		}
		var body struct {
			DeviceID string `json:"deviceId"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil || strings.TrimSpace(body.DeviceID) == "" {
			writeProblem(w, 400, "Bad request", `Send {"deviceId":"<unit>"}.`)
			return
		}
		m, err := meetings.Start(r.Context(), strings.TrimSpace(body.DeviceID), meeting.OriginVoice)
		switch {
		case errors.Is(err, meeting.ErrBusy):
			writeProblem(w, 409, "Already recording", "The unit is already recording a meeting.")
		case errors.Is(err, meeting.ErrProfile), errors.Is(err, meeting.ErrNotAdopted), errors.Is(err, meeting.ErrAbsent):
			writeProblem(w, 422, "Cannot record", err.Error())
		case errors.Is(err, meeting.ErrNotFound):
			writeProblem(w, 404, "Device not found", "The unit does not exist.")
		case err != nil:
			writeProblem(w, 503, "Unavailable", err.Error())
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(meetingResponse(m, time.Now()))
		}
	})
}

// MeetingAgentWarn relays the agent's "silence in ~30 s" to the unit's ring
// (RM-13). POST /v1/meetings/{id}/warn, no body.
func MeetingAgentWarn(meetings MeetingService, agentSecret string) http.Handler {
	return agentRoute(agentSecret, func(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
		err := meetings.Warn(r.Context(), id)
		switch {
		case errors.Is(err, meeting.ErrNotFound):
			writeProblem(w, 404, "Meeting not found", "The meeting does not exist.")
		case errors.Is(err, meeting.ErrNotRecording):
			writeProblem(w, 409, "Not recording", "The meeting is not recording.")
		case err != nil:
			writeProblem(w, 503, "Unavailable", err.Error())
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
}

// agentRoute authenticates the agent (X-Agent-Secret, design §3.3) and
// resolves the meeting id for the raw routes.
func agentRoute(agentSecret string, next func(http.ResponseWriter, *http.Request, uuid.UUID)) http.Handler {
	expected := sha256.Sum256([]byte(agentSecret))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := sha256.Sum256([]byte(r.Header.Get("X-Agent-Secret")))
		if agentSecret == "" || subtle.ConstantTimeCompare(expected[:], provided[:]) != 1 {
			writeProblem(w, 401, "Unauthorized", "Invalid agent credentials.")
			return
		}
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeProblem(w, 404, "Meeting not found", "The meeting does not exist.")
			return
		}
		next(w, r, id)
	})
}

// MeetingAudioDownload serves the stored audio with Range support (RM-41);
// admin-only by path (/v1/admin/…), like the rest of the control room.
func MeetingAudioDownload(meetings MeetingService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeProblem(w, 404, "Meeting not found", "The meeting does not exist.")
			return
		}
		path, err := meetings.AudioFile(r.Context(), id)
		if err != nil {
			writeProblem(w, 404, "Meeting not found", "No audio for this meeting.")
			return
		}
		w.Header().Set("Content-Type", "audio/ogg")
		w.Header().Set("Content-Disposition", `inline; filename="`+id.String()+`.ogg"`)
		w.Header().Set("Cache-Control", "private, no-store")
		http.ServeFile(w, r, path)
	})
}

// rangeOffset parses "bytes <offset>-*/*"; no header means offset 0.
func rangeOffset(header string) (int64, bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0, true
	}
	rest, ok := strings.CutPrefix(header, "bytes ")
	if !ok {
		return 0, false
	}
	start, _, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, false
	}
	offset, err := strconv.ParseInt(strings.TrimSpace(start), 10, 64)
	if err != nil || offset < 0 {
		return 0, false
	}
	return offset, true
}

func writeProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Problem{Type: "about:blank", Title: title, Status: status, Detail: &detail})
}
