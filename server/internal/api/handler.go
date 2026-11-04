package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/zetesis-labs/sebastian/server/internal/announce"
	"github.com/zetesis-labs/sebastian/server/internal/device"
	"github.com/zetesis-labs/sebastian/server/internal/session"
)

type SessionService interface {
	Create(context.Context, session.Credentials) (session.Created, error)
}

type ReadinessChecker interface {
	Ping(context.Context) error
}

type DeviceService interface {
	Reconcile(ctx context.Context, poll device.Poll) (device.PollResult, error)
	List(context.Context) ([]device.Device, error)
	Get(ctx context.Context, id string) (device.Detail, error)
	SetDesiredProfile(ctx context.Context, id, name string) error
	Rename(ctx context.Context, id, name string) error
	SetMeetingLimits(ctx context.Context, id string, silenceMin, maxHours int) error
	AssignAgentProfile(ctx context.Context, id string, profileID uuid.UUID) error
	SetDesiredConfig(ctx context.Context, id string, config map[string]any) (string, error)
	ClearDesiredConfig(ctx context.Context, id string) error
	DeviceConfig(ctx context.Context, id, secret string) (json.RawMessage, error)
	ReportRunningConfig(ctx context.Context, id, secret string, doc map[string]any) error
	RegenerateSecret(ctx context.Context, id string) (string, error)
	EnrollChallenge(id string) (string, error)
	Enroll(ctx context.Context, id, nonce, mac, deviceSecret string) error
	Adopt(ctx context.Context, id string, req device.AdoptRequest) (device.Job, error)
	Forget(ctx context.Context, id string, req device.AdoptRequest) (device.Job, error)
	Job(id uuid.UUID) (device.Job, error)
	ControlRoom() device.ControlRoom
	DiscoveryEnabled() bool
}

type Server struct {
	sessions         SessionService
	devices          DeviceService
	meetings         MeetingService
	announcements    AnnouncementService
	playback         PlaybackService
	agents           AgentProfileService
	readiness        ReadinessChecker
	logger           *slog.Logger
	readinessTimeout time.Duration
	now              func() time.Time
}

func NewHandler(
	sessions SessionService,
	devices DeviceService,
	meetings MeetingService,
	readiness ReadinessChecker,
	logger *slog.Logger,
	readinessTimeout time.Duration,
) *Server {
	return &Server{
		sessions:         sessions,
		devices:          devices,
		meetings:         meetings,
		readiness:        readiness,
		logger:           logger,
		readinessTimeout: readinessTimeout,
		now:              time.Now,
	}
}

func (h *Server) GetDesiredProfile(ctx context.Context, request GetDesiredProfileRequestObject) (GetDesiredProfileResponseObject, error) {
	poll := device.Poll{ID: request.DeviceId}
	if request.Params.Current != nil {
		poll.Reported = *request.Params.Current
	}
	if request.Params.Cfg != nil {
		poll.ConfigVersion = *request.Params.Cfg
	}
	if request.Params.Fw != nil {
		poll.Firmware = *request.Params.Fw
	}
	if request.Params.Ev != nil {
		poll.Event = *request.Params.Ev
	}
	if request.Params.XDeviceSecret != nil {
		poll.Secret, poll.IP = *request.Params.XDeviceSecret, ClientIP(ctx)
	}
	result, err := h.devices.Reconcile(ctx, poll)
	if err != nil {
		h.logger.ErrorContext(ctx, "device reconcile failed", "device_id", request.DeviceId, "error", err)
		return GetDesiredProfile503ApplicationProblemPlusJSONResponse(problem(
			503, "Devices unavailable", "The device state could not be read.",
		)), nil
	}
	response := GetDesiredProfile200TextResponse{Body: result.DesiredProfile}
	if result.DesiredConfigVersion != "" {
		response.Headers.XDesiredConfig = &result.DesiredConfigVersion
	}
	if h.meetings != nil {
		if order := h.meetings.PendingCommand(ctx, request.DeviceId); order != "" {
			response.Headers.XMeeting = &order
		}
	}
	if h.announcements != nil {
		if order := h.announcements.PendingCommand(ctx, request.DeviceId); order != "" {
			response.Headers.XAnnounce = &order
		}
	}
	if h.playback != nil {
		if order := h.playback.PendingCommand(ctx, request.DeviceId); order != "" {
			response.Headers.XPlayback = &order
		}
	}
	return response, nil
}

func (h *Server) ListDevices(ctx context.Context, _ ListDevicesRequestObject) (ListDevicesResponseObject, error) {
	items, err := h.devices.List(ctx)
	if err != nil {
		h.logger.ErrorContext(ctx, "list devices failed", "error", err)
		return ListDevices503ApplicationProblemPlusJSONResponse{
			UnavailableApplicationProblemPlusJSONResponse: UnavailableApplicationProblemPlusJSONResponse(problem(
				503, "Devices unavailable", "The device inventory could not be read.",
			)),
		}, nil
	}
	response := make([]Device, 0, len(items))
	for _, item := range items {
		response = append(response, deviceResponse(item))
	}
	return ListDevices200JSONResponse{Items: response}, nil
}

func (h *Server) SetDesiredProfile(ctx context.Context, request SetDesiredProfileRequestObject) (SetDesiredProfileResponseObject, error) {
	name := ""
	if request.Body != nil && request.Body.Name != nil {
		name = *request.Body.Name
	}
	err := h.devices.SetDesiredProfile(ctx, request.DeviceId, name)
	if errors.Is(err, device.ErrNotFound) {
		return SetDesiredProfile404ApplicationProblemPlusJSONResponse(problem(
			404, "Device not found", "The device has never contacted this server.",
		)), nil
	}
	if err != nil {
		h.logger.ErrorContext(ctx, "set desired profile failed", "device_id", request.DeviceId, "error", err)
		return SetDesiredProfile503ApplicationProblemPlusJSONResponse{
			UnavailableApplicationProblemPlusJSONResponse: UnavailableApplicationProblemPlusJSONResponse(problem(
				503, "Devices unavailable", "The desired profile could not be stored.",
			)),
		}, nil
	}
	return SetDesiredProfile204Response{}, nil
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func deviceResponse(item device.Device) Device {
	return Device{
		Id:                    item.ID,
		DisplayName:           item.DisplayName,
		Enabled:               item.Enabled,
		State:                 DeviceState(item.State),
		MeetingSilenceMin:     optionalInt(item.MeetingSilenceMin),
		MeetingMaxHours:       optionalInt(item.MeetingMaxHours),
		AdoptedAt:             optionalTime(item.AdoptedAt),
		DesiredProfile:        optional(item.DesiredProfile),
		ReportedProfile:       optional(item.ReportedProfile),
		ProfileReportedAt:     optionalTime(item.ProfileReportedAt),
		Firmware:              optional(item.Firmware),
		Ip:                    optional(item.IP),
		LastIp:                optional(item.LastIP),
		ControlRoom:           optional(item.ControlRoom),
		LastError:             optional(item.LastError),
		LastEvent:             optional(item.LastEvent),
		LastEventAt:           optionalTime(item.LastEventAt),
		ReportedConfigVersion: optional(item.ReportedConfig),
		DesiredConfigVersion:  optional(item.DesiredConfig),
		SeenOnLanAt:           optionalTime(item.SeenOnLanAt),
		AgentProfileId:        optionalUUID(item.AgentProfileID),
		AgentProfileName:      optional(item.AgentProfileName),
	}
}

func optionalUUID(value uuid.UUID) *uuid.UUID {
	if value == uuid.Nil {
		return nil
	}
	return &value
}

func (h *Server) GetLiveness(context.Context, GetLivenessRequestObject) (GetLivenessResponseObject, error) {
	return GetLiveness200JSONResponse{Status: Ok}, nil
}

func (h *Server) GetReadiness(ctx context.Context, _ GetReadinessRequestObject) (GetReadinessResponseObject, error) {
	ctx, cancel := context.WithTimeout(ctx, h.readinessTimeout)
	defer cancel()
	if err := h.readiness.Ping(ctx); err != nil {
		h.logger.WarnContext(ctx, "readiness check failed", "error", err)
		return GetReadiness503ApplicationProblemPlusJSONResponse(problem(
			503,
			"Service unavailable",
			"A required dependency is unavailable.",
		)), nil
	}
	return GetReadiness200JSONResponse{Status: Ok}, nil
}

func (h *Server) CreateSession(ctx context.Context, request CreateSessionRequestObject) (CreateSessionResponseObject, error) {
	credentials := session.Credentials{
		DeviceID: request.Params.XDeviceId,
		Secret:   request.Params.XDeviceSecret,
	}
	if request.Body != nil && request.Body.Kind != nil && *request.Body.Kind == SessionRequestKindMeeting {
		credentials.Kind = session.KindMeeting
		if request.Body.MeetingId != nil {
			credentials.MeetingID = *request.Body.MeetingId
		}
	}
	var claimed *announce.Announcement
	if request.Body != nil && request.Body.Kind != nil && *request.Body.Kind == SessionRequestKindAnnounce {
		a, resp := h.claimAnnouncement(ctx, request, &credentials)
		if resp != nil {
			return resp, nil
		}
		claimed = &a
	}
	if request.Body != nil && request.Body.Kind != nil && *request.Body.Kind == SessionRequestKindPlayback {
		if resp := h.joinPlayback(ctx, request, &credentials); resp != nil {
			return resp, nil
		}
	}
	created, err := h.sessions.Create(ctx, credentials)
	if err != nil && claimed != nil {
		// Not said: back in the queue, for the unit's next poll to retry.
		h.announcements.Restore(*claimed)
	}
	if errors.Is(err, session.ErrUnauthorized) {
		return CreateSession401ApplicationProblemPlusJSONResponse(problem(
			401,
			"Unauthorized",
			"The device credentials are invalid.",
		)), nil
	}
	if err != nil {
		h.logger.ErrorContext(ctx, "session creation failed", "device_id", request.Params.XDeviceId, "error", err)
		return CreateSession503ApplicationProblemPlusJSONResponse(problem(
			503,
			"Session unavailable",
			"The session could not be created.",
		)), nil
	}
	return CreateSession201JSONResponse{
		Id:        created.ID,
		Room:      created.Room,
		ServerUrl: created.ServerURL,
		Token:     created.Token,
		ExpiresAt: created.ExpiresAt,
		Listen:    created.Listen,
	}, nil
}

// claimAnnouncement fills an announce session with the announcement the unit
// was ordered to fetch; a non-nil response refuses the session.
func (h *Server) claimAnnouncement(ctx context.Context, request CreateSessionRequestObject, credentials *session.Credentials) (announce.Announcement, CreateSessionResponseObject) {
	gone := CreateSession404ApplicationProblemPlusJSONResponse(problem(404, "Announcement not found", "The announcement is unknown to this unit, already said or expired."))
	if h.announcements == nil || request.Body.AnnouncementId == nil {
		return announce.Announcement{}, gone
	}
	a, err := h.announcements.Claim(ctx, credentials.DeviceID, credentials.Secret, *request.Body.AnnouncementId)
	switch {
	case errors.Is(err, announce.ErrUnauthorized), errors.Is(err, device.ErrNotFound):
		// A forgotten unit gets 401 like any session, so it enrols again.
		return a, CreateSession401ApplicationProblemPlusJSONResponse(problem(401, "Unauthorized", "The device credentials are invalid."))
	case errors.Is(err, announce.ErrNotFound):
		h.logger.WarnContext(ctx, "announce session refused: the announcement was already said or expired", "device_id", credentials.DeviceID, "announcement", *request.Body.AnnouncementId)
		return a, gone
	case err != nil:
		h.logger.ErrorContext(ctx, "announcement claim failed", "device_id", credentials.DeviceID, "error", err)
		return a, CreateSession503ApplicationProblemPlusJSONResponse(problem(503, "Session unavailable", "The session could not be created."))
	}
	credentials.Kind = session.KindAnnounce
	credentials.Announcement = session.Announced{Text: a.Text, Reply: a.Reply}
	return a, nil
}

func problem(status int, title, detail string) Problem {
	return Problem{
		Type:   "about:blank",
		Title:  title,
		Status: status,
		Detail: &detail,
	}
}

func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

func optionalInt(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}
