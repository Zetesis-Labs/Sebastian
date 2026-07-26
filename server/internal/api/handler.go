package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/zetesis-labs/sebastian/server/internal/device"
	"github.com/zetesis-labs/sebastian/server/internal/recording"
	"github.com/zetesis-labs/sebastian/server/internal/session"
)

type SessionService interface {
	Create(context.Context, session.Credentials) (session.Created, error)
}

type ReadinessChecker interface {
	Ping(context.Context) error
}

type RecordingService interface {
	List(context.Context, int) ([]recording.Recording, error)
	Get(context.Context, uuid.UUID) (recording.Recording, error)
	Register(context.Context, recording.Registration) (recording.Recording, error)
	Summary(context.Context) (recording.Summary, error)
}

type DeviceService interface {
	Reconcile(ctx context.Context, id, reported string) (string, error)
	List(context.Context) ([]device.Device, error)
	SetDesired(ctx context.Context, id, name string) error
}

type Server struct {
	sessions         SessionService
	recordings       RecordingService
	devices          DeviceService
	readiness        ReadinessChecker
	logger           *slog.Logger
	legacyEnabled    bool
	readinessTimeout time.Duration
}

func NewHandler(
	sessions SessionService,
	recordings RecordingService,
	devices DeviceService,
	readiness ReadinessChecker,
	logger *slog.Logger,
	legacyEnabled bool,
	readinessTimeout time.Duration,
) *Server {
	return &Server{
		sessions:         sessions,
		recordings:       recordings,
		devices:          devices,
		readiness:        readiness,
		logger:           logger,
		legacyEnabled:    legacyEnabled,
		readinessTimeout: readinessTimeout,
	}
}

func (h *Server) GetDesiredProfile(ctx context.Context, request GetDesiredProfileRequestObject) (GetDesiredProfileResponseObject, error) {
	reported := ""
	if request.Params.Current != nil {
		reported = *request.Params.Current
	}
	desired, err := h.devices.Reconcile(ctx, request.DeviceId, reported)
	if err != nil {
		h.logger.ErrorContext(ctx, "device reconcile failed", "device_id", request.DeviceId, "error", err)
		return GetDesiredProfile503ApplicationProblemPlusJSONResponse(problem(
			503, "Devices unavailable", "The device state could not be read.",
		)), nil
	}
	return GetDesiredProfile200TextResponse(desired), nil
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
	err := h.devices.SetDesired(ctx, request.DeviceId, name)
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

func deviceResponse(item device.Device) Device {
	response := Device{Id: item.ID, DisplayName: item.DisplayName, Enabled: item.Enabled}
	if item.DesiredProfile != "" {
		response.DesiredProfile = &item.DesiredProfile
	}
	if item.ReportedProfile != "" {
		response.ReportedProfile = &item.ReportedProfile
	}
	if !item.ProfileReportedAt.IsZero() {
		response.ProfileReportedAt = &item.ProfileReportedAt
	}
	return response
}

func (h *Server) ListRecordings(ctx context.Context, request ListRecordingsRequestObject) (ListRecordingsResponseObject, error) {
	limit := 50
	if request.Params.Limit != nil {
		limit = max(1, min(*request.Params.Limit, 100))
	}
	items, err := h.recordings.List(ctx, limit)
	if err != nil {
		h.logger.ErrorContext(ctx, "list recordings failed", "error", err)
		return ListRecordings503ApplicationProblemPlusJSONResponse{
			UnavailableApplicationProblemPlusJSONResponse: UnavailableApplicationProblemPlusJSONResponse(problem(
				503, "Recordings unavailable", "The recording catalogue could not be read.",
			)),
		}, nil
	}
	response := make([]Recording, 0, len(items))
	for _, item := range items {
		response = append(response, recordingResponse(item))
	}
	return ListRecordings200JSONResponse{Items: response}, nil
}

func (h *Server) GetRecording(ctx context.Context, request GetRecordingRequestObject) (GetRecordingResponseObject, error) {
	item, err := h.recordings.Get(ctx, request.RecordingId)
	if errors.Is(err, recording.ErrNotFound) {
		return GetRecording404ApplicationProblemPlusJSONResponse(problem(
			404, "Recording not found", "The requested recording does not exist.",
		)), nil
	}
	if err != nil {
		h.logger.ErrorContext(ctx, "get recording failed", "recording_id", request.RecordingId, "error", err)
		return GetRecording503ApplicationProblemPlusJSONResponse{
			UnavailableApplicationProblemPlusJSONResponse: UnavailableApplicationProblemPlusJSONResponse(problem(
				503, "Recording unavailable", "The recording could not be read.",
			)),
		}, nil
	}
	return GetRecording200JSONResponse(recordingResponse(item)), nil
}

func (h *Server) RegisterRecording(ctx context.Context, request RegisterRecordingRequestObject) (RegisterRecordingResponseObject, error) {
	if request.Body == nil {
		return RegisterRecording503ApplicationProblemPlusJSONResponse{
			UnavailableApplicationProblemPlusJSONResponse: UnavailableApplicationProblemPlusJSONResponse(problem(
				503, "Recording unavailable", "The recording metadata was not provided.",
			)),
		}, nil
	}
	created, err := h.recordings.Register(ctx, recording.Registration{
		Room:        request.Body.Room,
		Kind:        recording.Kind(request.Body.Kind),
		FileName:    request.Body.FileName,
		ObjectURL:   request.Body.ObjectUrl,
		ContentType: request.Body.ContentType,
		ByteSize:    request.Body.ByteSize,
		DurationMs:  request.Body.DurationMs,
		CapturedAt:  request.Body.CapturedAt,
		Transcript:  request.Body.Transcript,
	})
	if errors.Is(err, recording.ErrNotFound) {
		return RegisterRecording404ApplicationProblemPlusJSONResponse(problem(
			404, "Session not found", "The recording room does not match a known session.",
		)), nil
	}
	if errors.Is(err, recording.ErrConflict) {
		return RegisterRecording409ApplicationProblemPlusJSONResponse(problem(
			409, "Recording already registered", "The object URL is already present in the catalogue.",
		)), nil
	}
	if err != nil {
		h.logger.ErrorContext(ctx, "register recording failed", "room", request.Body.Room, "error", err)
		return RegisterRecording503ApplicationProblemPlusJSONResponse{
			UnavailableApplicationProblemPlusJSONResponse: UnavailableApplicationProblemPlusJSONResponse(problem(
				503, "Recording unavailable", "The recording could not be registered.",
			)),
		}, nil
	}
	return RegisterRecording201JSONResponse(recordingResponse(created)), nil
}

func (h *Server) GetRecordingsSummary(ctx context.Context, _ GetRecordingsSummaryRequestObject) (GetRecordingsSummaryResponseObject, error) {
	summary, err := h.recordings.Summary(ctx)
	if err != nil {
		h.logger.ErrorContext(ctx, "summarize recordings failed", "error", err)
		return GetRecordingsSummary503ApplicationProblemPlusJSONResponse{
			UnavailableApplicationProblemPlusJSONResponse: UnavailableApplicationProblemPlusJSONResponse(problem(
				503, "Recordings unavailable", "The recording summary could not be calculated.",
			)),
		}, nil
	}
	return GetRecordingsSummary200JSONResponse{
		Count: summary.Count, TotalBytes: summary.TotalBytes,
		TotalDurationMs: summary.TotalDurationMs, LastCapturedAt: summary.LastCapturedAt,
	}, nil
}

func recordingResponse(item recording.Recording) Recording {
	return Recording{
		Id: item.ID, SessionId: item.SessionID, Room: item.Room,
		Kind: RecordingKind(item.Kind), FileName: item.FileName, ObjectUrl: item.ObjectURL,
		ContentType: item.ContentType, ByteSize: item.ByteSize, DurationMs: item.DurationMs,
		CapturedAt: item.CapturedAt, CreatedAt: item.CreatedAt, Transcript: item.Transcript,
	}
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

func (h *Server) GetLegacyToken(ctx context.Context, _ GetLegacyTokenRequestObject) (GetLegacyTokenResponseObject, error) {
	if !h.legacyEnabled {
		return GetLegacyToken404ApplicationProblemPlusJSONResponse(problem(
			404,
			"Not found",
			"The legacy token endpoint is disabled.",
		)), nil
	}
	created, err := h.sessions.Create(ctx, session.Credentials{Legacy: true})
	if err != nil {
		h.logger.ErrorContext(ctx, "legacy session creation failed", "error", err)
		return GetLegacyToken503ApplicationProblemPlusJSONResponse(problem(
			503,
			"Session unavailable",
			"The session could not be created.",
		)), nil
	}
	return GetLegacyToken200TextResponse(fmt.Sprintf("%s\n%s", created.ServerURL, created.Token)), nil
}

func (h *Server) CreateSession(ctx context.Context, request CreateSessionRequestObject) (CreateSessionResponseObject, error) {
	created, err := h.sessions.Create(ctx, session.Credentials{
		DeviceID: request.Params.XDeviceId,
		Secret:   request.Params.XDeviceSecret,
	})
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
	}, nil
}

func problem(status int, title, detail string) Problem {
	return Problem{
		Type:   "about:blank",
		Title:  title,
		Status: status,
		Detail: &detail,
	}
}
