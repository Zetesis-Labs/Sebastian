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
	Stop(ctx context.Context, id uuid.UUID, reason meeting.EndReason) (meeting.Meeting, error)
	Report(ctx context.Context, deviceID, secret string, id uuid.UUID, state string, reason meeting.EndReason) error
	PendingCommand(ctx context.Context, deviceID string) string
	Audio(ctx context.Context, id uuid.UUID, offset int64, r io.Reader) (int64, error)
	AudioFile(ctx context.Context, id uuid.UUID) (string, error)
	Get(ctx context.Context, id uuid.UUID) (meeting.Meeting, error)
	List(ctx context.Context, f meeting.Filter) ([]meeting.Meeting, error)
	Delete(ctx context.Context, id uuid.UUID) error
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
	return out
}

// ── raw audio endpoints (outside the generated router: streaming and Range) ──

// MeetingAudioUpload takes the agent's Ogg/Opus stream (design §3.3):
// PUT /v1/meetings/{id}/audio with X-Agent-Secret, optional Content-Range
// "bytes <offset>-*/*" to resume. Long-lived: the caller must lift the
// server's read deadline for it.
func MeetingAudioUpload(meetings MeetingService, agentSecret string) http.Handler {
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
