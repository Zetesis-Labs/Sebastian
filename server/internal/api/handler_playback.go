package api

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/zetesis-labs/sebastian/server/internal/device"
	"github.com/zetesis-labs/sebastian/server/internal/playback"
	"github.com/zetesis-labs/sebastian/server/internal/session"
)

// Playback of a source on a unit (internal/playback).

type PlaybackService interface {
	Start(ctx context.Context, deviceID string) (playback.Started, error)
	Stop(ctx context.Context, deviceID string) error
	Active(ctx context.Context, deviceID string) (playback.Playback, bool)
	PendingCommand(ctx context.Context, deviceID string) string
	Join(ctx context.Context, deviceID, secret string, id uuid.UUID) (playback.Playback, error)
}

func (h *Server) WithPlayback(p PlaybackService) *Server {
	h.playback = p
	return h
}

func (h *Server) StartPlayback(ctx context.Context, request StartPlaybackRequestObject) (StartPlaybackResponseObject, error) {
	if h.playback == nil {
		return StartPlayback503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "playback not configured", errors.New("no playback service"))}, nil
	}
	started, err := h.playback.Start(ctx, request.DeviceId)
	switch {
	case errors.Is(err, device.ErrNotFound):
		return StartPlayback404ApplicationProblemPlusJSONResponse{DeviceNotFoundApplicationProblemPlusJSONResponse: notFound()}, nil
	case errors.Is(err, playback.ErrBusy):
		return StartPlayback409ApplicationProblemPlusJSONResponse(problem(409, "Recording a meeting", "The unit is recording a meeting; play when it ends.")), nil
	case errors.Is(err, playback.ErrProfile):
		return StartPlayback422ApplicationProblemPlusJSONResponse(problem(422, "Profile without sessions", "In the micro-usb profile the unit opens no sessions; switch it to agente.")), nil
	case errors.Is(err, playback.ErrNotAdopted):
		return StartPlayback422ApplicationProblemPlusJSONResponse(problem(422, "Not adopted here", "Only the control room that adopted the unit can make it play.")), nil
	case errors.Is(err, playback.ErrAbsent):
		return StartPlayback422ApplicationProblemPlusJSONResponse(problem(422, "Unit not contacting", "The unit has not contacted this control room lately; it cannot be asked to play.")), nil
	case err != nil:
		return StartPlayback503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "start playback failed", err, "device_id", request.DeviceId)}, nil
	}
	p := started.Playback
	return StartPlayback201JSONResponse{
		Id: p.ID, StartedAt: p.StartedAt, Room: p.Room,
		ServerUrl: started.ServerURL, SourceToken: started.SourceToken,
		Delivery: PlaybackStartedDelivery(started.Delivery),
	}, nil
}

func (h *Server) GetPlayback(ctx context.Context, request GetPlaybackRequestObject) (GetPlaybackResponseObject, error) {
	if h.playback != nil {
		if p, on := h.playback.Active(ctx, request.DeviceId); on {
			return GetPlayback200JSONResponse{Id: p.ID, StartedAt: p.StartedAt}, nil
		}
	}
	return GetPlayback404ApplicationProblemPlusJSONResponse(problem(404, "Nothing playing", "The unit has no playback on.")), nil
}

func (h *Server) StopPlayback(ctx context.Context, request StopPlaybackRequestObject) (StopPlaybackResponseObject, error) {
	if h.playback == nil {
		return StopPlayback503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "playback not configured", errors.New("no playback service"))}, nil
	}
	err := h.playback.Stop(ctx, request.DeviceId)
	switch {
	case errors.Is(err, playback.ErrNotFound):
		return StopPlayback404ApplicationProblemPlusJSONResponse(problem(404, "Nothing playing", "The unit has no playback on.")), nil
	case err != nil:
		return StopPlayback503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "stop playback failed", err, "device_id", request.DeviceId)}, nil
	}
	return StopPlayback204Response{}, nil
}

// joinPlayback fills a playback session with the room of the playback the
// unit was ordered into; a non-nil response refuses the session.
func (h *Server) joinPlayback(ctx context.Context, request CreateSessionRequestObject, credentials *session.Credentials) CreateSessionResponseObject {
	off := CreateSession404ApplicationProblemPlusJSONResponse(problem(404, "Playback not on", "The playback is unknown to this unit or already stopped."))
	if h.playback == nil || request.Body.PlaybackId == nil {
		return off
	}
	p, err := h.playback.Join(ctx, credentials.DeviceID, credentials.Secret, *request.Body.PlaybackId)
	switch {
	case errors.Is(err, playback.ErrUnauthorized), errors.Is(err, device.ErrNotFound):
		return CreateSession401ApplicationProblemPlusJSONResponse(problem(401, "Unauthorized", "The device credentials are invalid."))
	case errors.Is(err, playback.ErrNotFound):
		h.logger.InfoContext(ctx, "playback session refused: the playback is off", "device_id", credentials.DeviceID, "playback", *request.Body.PlaybackId)
		return off
	case err != nil:
		h.logger.ErrorContext(ctx, "playback join failed", "device_id", credentials.DeviceID, "error", err)
		return CreateSession503ApplicationProblemPlusJSONResponse(problem(503, "Session unavailable", "The session could not be created."))
	}
	credentials.Kind = session.KindPlayback
	credentials.PlaybackRoom = p.Room
	return nil
}
