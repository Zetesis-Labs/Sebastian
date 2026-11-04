package api

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/zetesis-labs/sebastian/server/internal/announce"
	"github.com/zetesis-labs/sebastian/server/internal/device"
)

// Announcements a unit says on behalf of the control room (internal/announce).

type AnnouncementService interface {
	Create(ctx context.Context, deviceID, text string, reply bool) (announce.Announcement, announce.Delivery, error)
	Claim(ctx context.Context, deviceID, secret string, id uuid.UUID) (announce.Announcement, error)
	PendingCommand(ctx context.Context, deviceID string) string
	Restore(a announce.Announcement)
}

func (h *Server) WithAnnouncements(announcements AnnouncementService) *Server {
	h.announcements = announcements
	return h
}

func (h *Server) CreateAnnouncement(ctx context.Context, request CreateAnnouncementRequestObject) (CreateAnnouncementResponseObject, error) {
	if h.announcements == nil {
		return CreateAnnouncement503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "announcements not configured", errors.New("no announcement service"))}, nil
	}
	reply := request.Body.Reply != nil && *request.Body.Reply
	a, delivery, err := h.announcements.Create(ctx, request.DeviceId, request.Body.Text, reply)
	switch {
	case errors.Is(err, device.ErrNotFound):
		return CreateAnnouncement404ApplicationProblemPlusJSONResponse{DeviceNotFoundApplicationProblemPlusJSONResponse: notFound()}, nil
	case errors.Is(err, announce.ErrBusy):
		return CreateAnnouncement409ApplicationProblemPlusJSONResponse(problem(409, "Recording a meeting", "The unit is recording a meeting; announce when it ends.")), nil
	case errors.Is(err, announce.ErrInvalid):
		return CreateAnnouncement422ApplicationProblemPlusJSONResponse(problem(422, "Unsayable text", "The text must have between 1 and 500 characters.")), nil
	case errors.Is(err, announce.ErrProfile):
		return CreateAnnouncement422ApplicationProblemPlusJSONResponse(problem(422, "Profile without voice", "In the micro-usb profile the unit has no agent; switch it to agente.")), nil
	case errors.Is(err, announce.ErrNotAdopted):
		return CreateAnnouncement422ApplicationProblemPlusJSONResponse(problem(422, "Not adopted here", "Only the control room that adopted the unit can make it speak.")), nil
	case errors.Is(err, announce.ErrAbsent):
		return CreateAnnouncement422ApplicationProblemPlusJSONResponse(problem(422, "Unit not contacting", "The unit has not contacted this control room lately; it cannot be asked to speak.")), nil
	case err != nil:
		return CreateAnnouncement503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "create announcement failed", err, "device_id", request.DeviceId)}, nil
	}
	return CreateAnnouncement202JSONResponse(Announcement{
		Id:        a.ID,
		Text:      a.Text,
		Reply:     a.Reply,
		Delivery:  AnnouncementDelivery(delivery),
		ExpiresAt: a.CreatedAt.Add(announce.TTL),
	}), nil
}
