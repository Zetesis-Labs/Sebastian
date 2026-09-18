package api

import (
	"context"
	"errors"
	"strings"

	"github.com/zetesis-labs/sebastian/server/internal/device"
)

// Fleet endpoints (docs/implementation/11-fleet-adoption-control-room.md §6).

func (h *Server) unavailable(ctx context.Context, msg string, err error, kv ...any) UnavailableApplicationProblemPlusJSONResponse {
	h.logger.ErrorContext(ctx, msg, append(kv, "error", err)...)
	return UnavailableApplicationProblemPlusJSONResponse(problem(503, "Devices unavailable", "The device inventory could not be updated."))
}

func notFound() DeviceNotFoundApplicationProblemPlusJSONResponse {
	return DeviceNotFoundApplicationProblemPlusJSONResponse(problem(404, "Device not found", "The device is not in this control room's inventory."))
}

func (h *Server) GetControlRoom(_ context.Context, _ GetControlRoomRequestObject) (GetControlRoomResponseObject, error) {
	room := h.devices.ControlRoom()
	response := ControlRoom{
		Name:                room.Name,
		ApiUrl:              room.APIURL,
		DiscoveryEnabled:    h.devices.DiscoveryEnabled(),
		AdoptPort:           room.AdoptPort,
		OrgSecretConfigured: room.OrgSecret != "",
		OrgSecret:           optional(room.OrgSecret),
		SyslogIp:            optional(room.SyslogIP),
	}
	if room.SyslogIP != "" {
		port := room.SyslogPort
		response.SyslogPort = &port
	}
	return GetControlRoom200JSONResponse(response), nil
}

func (h *Server) GetDevice(ctx context.Context, request GetDeviceRequestObject) (GetDeviceResponseObject, error) {
	detail, err := h.devices.Get(ctx, request.DeviceId)
	if errors.Is(err, device.ErrNotFound) {
		return GetDevice404ApplicationProblemPlusJSONResponse{DeviceNotFoundApplicationProblemPlusJSONResponse: notFound()}, nil
	}
	if err != nil {
		return GetDevice503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "get device failed", err, "device_id", request.DeviceId)}, nil
	}
	sessions := make([]DeviceSession, 0, len(detail.Sessions))
	for _, s := range detail.Sessions {
		sessions = append(sessions, DeviceSession{Id: s.ID, Room: s.Room, CreatedAt: s.CreatedAt, ExpiresAt: s.ExpiresAt, RecordingCount: s.RecordingCount})
	}
	base := deviceResponse(detail.Device)
	response := DeviceDetail{
		Id: base.Id, DisplayName: base.DisplayName, Enabled: base.Enabled, State: base.State,
		AdoptedAt: base.AdoptedAt, DesiredProfile: base.DesiredProfile, ReportedProfile: base.ReportedProfile,
		ProfileReportedAt: base.ProfileReportedAt, Firmware: base.Firmware, Ip: base.Ip, ControlRoom: base.ControlRoom,
		LastError: base.LastError, ReportedConfigVersion: base.ReportedConfigVersion, DesiredConfigVersion: base.DesiredConfigVersion,
		SeenOnLanAt: base.SeenOnLanAt, HasDeviceSecret: detail.HasDeviceSecret, Sessions: sessions,
	}
	if detail.DesiredConfig != nil {
		var cfg DeviceConfig
		if err := jsonUnmarshal(detail.DesiredConfig, &cfg); err == nil {
			response.DesiredConfig = &cfg
		}
	}
	return GetDevice200JSONResponse(response), nil
}

func (h *Server) UpdateDevice(ctx context.Context, request UpdateDeviceRequestObject) (UpdateDeviceResponseObject, error) {
	if request.Body == nil || strings.TrimSpace(request.Body.DisplayName) == "" {
		return UpdateDevice404ApplicationProblemPlusJSONResponse{DeviceNotFoundApplicationProblemPlusJSONResponse: notFound()}, nil
	}
	err := h.devices.Rename(ctx, request.DeviceId, strings.TrimSpace(request.Body.DisplayName))
	if errors.Is(err, device.ErrNotFound) {
		return UpdateDevice404ApplicationProblemPlusJSONResponse{DeviceNotFoundApplicationProblemPlusJSONResponse: notFound()}, nil
	}
	if err != nil {
		return UpdateDevice503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "rename device failed", err, "device_id", request.DeviceId)}, nil
	}
	return UpdateDevice204Response{}, nil
}

func adoptRequest(body *AdoptionRequest) device.AdoptRequest {
	var req device.AdoptRequest
	if body == nil {
		return req
	}
	if body.Ip != nil {
		req.IP = strings.TrimSpace(*body.Ip)
	}
	if body.DeviceSecret != nil {
		req.DeviceSecret = strings.TrimSpace(*body.DeviceSecret)
	}
	if body.Config != nil {
		req.Config = map[string]any(*body.Config)
	}
	return req
}

func jobResponse(job device.Job) AdoptionJob {
	response := AdoptionJob{
		Id: job.ID, DeviceId: job.DeviceID, Kind: AdoptionJobKind(job.Kind), Phase: AdoptionPhase(job.Phase),
		Ip: optional(job.IP), Error: optional(job.Error), DeviceSecret: optional(job.DeviceSecret),
		StartedAt: job.StartedAt, UpdatedAt: job.UpdatedAt,
	}
	return response
}

func (h *Server) AdoptDevice(ctx context.Context, request AdoptDeviceRequestObject) (AdoptDeviceResponseObject, error) {
	job, err := h.devices.Adopt(ctx, request.DeviceId, adoptRequest(request.Body))
	switch {
	case errors.Is(err, device.ErrNoAddress):
		return AdoptDevice400ApplicationProblemPlusJSONResponse(problem(400, "No address", err.Error())), nil
	case errors.Is(err, device.ErrNoOrgSecret):
		return AdoptDevice400ApplicationProblemPlusJSONResponse(problem(400, "No organization secret", "Configure SEBASTIAN_ORG_SECRET on this control room, or supply the device secret.")), nil
	case err != nil:
		if strings.Contains(err.Error(), "SEBASTIAN_PUBLIC_API_URL") {
			return AdoptDevice400ApplicationProblemPlusJSONResponse(problem(400, "Control room URL not configured", err.Error())), nil
		}
		return AdoptDevice503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "adopt failed to start", err, "device_id", request.DeviceId)}, nil
	}
	return AdoptDevice202JSONResponse(jobResponse(job)), nil
}

func (h *Server) ForgetDevice(ctx context.Context, request ForgetDeviceRequestObject) (ForgetDeviceResponseObject, error) {
	job, err := h.devices.Forget(ctx, request.DeviceId, adoptRequest(request.Body))
	switch {
	case errors.Is(err, device.ErrNotFound):
		return ForgetDevice404ApplicationProblemPlusJSONResponse{DeviceNotFoundApplicationProblemPlusJSONResponse: notFound()}, nil
	case errors.Is(err, device.ErrNoAddress):
		return ForgetDevice400ApplicationProblemPlusJSONResponse(problem(400, "No address", err.Error())), nil
	case err != nil:
		return ForgetDevice503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "forget failed to start", err, "device_id", request.DeviceId)}, nil
	}
	return ForgetDevice202JSONResponse(jobResponse(job)), nil
}

func (h *Server) GetAdoptionJob(_ context.Context, request GetAdoptionJobRequestObject) (GetAdoptionJobResponseObject, error) {
	job, err := h.devices.Job(request.JobId)
	if err != nil {
		return GetAdoptionJob404ApplicationProblemPlusJSONResponse(problem(404, "Job not found", "Unknown or expired adoption job.")), nil
	}
	return GetAdoptionJob200JSONResponse(jobResponse(job)), nil
}

func (h *Server) SetDesiredConfig(ctx context.Context, request SetDesiredConfigRequestObject) (SetDesiredConfigResponseObject, error) {
	if request.Body == nil {
		return SetDesiredConfig404ApplicationProblemPlusJSONResponse{DeviceNotFoundApplicationProblemPlusJSONResponse: notFound()}, nil
	}
	version, err := h.devices.SetDesiredConfig(ctx, request.DeviceId, map[string]any(*request.Body))
	if errors.Is(err, device.ErrNotFound) {
		return SetDesiredConfig404ApplicationProblemPlusJSONResponse{DeviceNotFoundApplicationProblemPlusJSONResponse: notFound()}, nil
	}
	if err != nil {
		return SetDesiredConfig503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "set desired config failed", err, "device_id", request.DeviceId)}, nil
	}
	return SetDesiredConfig200JSONResponse{Version: version}, nil
}

func (h *Server) ClearDesiredConfig(ctx context.Context, request ClearDesiredConfigRequestObject) (ClearDesiredConfigResponseObject, error) {
	err := h.devices.ClearDesiredConfig(ctx, request.DeviceId)
	if errors.Is(err, device.ErrNotFound) {
		return ClearDesiredConfig404ApplicationProblemPlusJSONResponse{DeviceNotFoundApplicationProblemPlusJSONResponse: notFound()}, nil
	}
	if err != nil {
		return ClearDesiredConfig503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "clear desired config failed", err, "device_id", request.DeviceId)}, nil
	}
	return ClearDesiredConfig204Response{}, nil
}

func (h *Server) RegenerateDeviceSecret(ctx context.Context, request RegenerateDeviceSecretRequestObject) (RegenerateDeviceSecretResponseObject, error) {
	secret, err := h.devices.RegenerateSecret(ctx, request.DeviceId)
	if errors.Is(err, device.ErrNotFound) {
		return RegenerateDeviceSecret404ApplicationProblemPlusJSONResponse{DeviceNotFoundApplicationProblemPlusJSONResponse: notFound()}, nil
	}
	if err != nil {
		return RegenerateDeviceSecret503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "regenerate secret failed", err, "device_id", request.DeviceId)}, nil
	}
	return RegenerateDeviceSecret200JSONResponse{DeviceSecret: secret}, nil
}

// GetDeviceConfig is device-facing: the unit fetches the desired document
// when its poll says the version changed.
func (h *Server) GetDeviceConfig(ctx context.Context, request GetDeviceConfigRequestObject) (GetDeviceConfigResponseObject, error) {
	raw, err := h.devices.DeviceConfig(ctx, request.DeviceId, request.Params.XDeviceSecret)
	switch {
	case errors.Is(err, device.ErrUnauthorized):
		return GetDeviceConfig401ApplicationProblemPlusJSONResponse(problem(401, "Unauthorized", "The device credentials are invalid.")), nil
	case errors.Is(err, device.ErrNotFound):
		return GetDeviceConfig404ApplicationProblemPlusJSONResponse(problem(404, "No desired config", "This control room holds no desired config for the device.")), nil
	case err != nil:
		return GetDeviceConfig503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "device config failed", err, "device_id", request.DeviceId)}, nil
	}
	var cfg DeviceConfig
	if err := jsonUnmarshal(raw, &cfg); err != nil {
		return GetDeviceConfig503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "device config malformed", err, "device_id", request.DeviceId)}, nil
	}
	return GetDeviceConfig200JSONResponse(cfg), nil
}
