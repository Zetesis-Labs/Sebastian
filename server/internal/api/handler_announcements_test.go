package api

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zetesis-labs/sebastian/server/internal/announce"
	"github.com/zetesis-labs/sebastian/server/internal/device"
	"github.com/zetesis-labs/sebastian/server/internal/session"
)

type stubAnnouncements struct {
	created  announce.Announcement
	delivery announce.Delivery
	err      error
	claimed  announce.Announcement
	claimErr error
	pending  string
	restored []announce.Announcement
}

func (s *stubAnnouncements) Create(_ context.Context, deviceID, text string, reply bool) (announce.Announcement, announce.Delivery, error) {
	if s.err != nil {
		return announce.Announcement{}, "", s.err
	}
	a := s.created
	a.DeviceID, a.Text, a.Reply = deviceID, text, reply
	return a, s.delivery, nil
}

func (s *stubAnnouncements) Claim(context.Context, string, string, uuid.UUID) (announce.Announcement, error) {
	return s.claimed, s.claimErr
}

func (s *stubAnnouncements) PendingCommand(context.Context, string) string { return s.pending }

func (s *stubAnnouncements) Restore(a announce.Announcement) { s.restored = append(s.restored, a) }

type capturingSessions struct{ got session.Credentials }

func (c *capturingSessions) Create(_ context.Context, credentials session.Credentials) (session.Created, error) {
	c.got = credentials
	return session.Created{ID: uuid.New(), Room: "r", ServerURL: "ws://lk", Token: "t", Listen: credentials.Announcement.Reply}, nil
}

func announceHandler(a *stubAnnouncements, sessions SessionService) *Server {
	if sessions == nil {
		sessions = &capturingSessions{}
	}
	return NewHandler(sessions, &stubDevices{desired: "agente"}, &stubMeetings{}, stubReadiness{}, testLogger(), time.Second).WithAnnouncements(a)
}

func TestCreateAnnouncementAnswersHowItWillBeDelivered(t *testing.T) {
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	stub := &stubAnnouncements{created: announce.Announcement{ID: uuid.New(), CreatedAt: at}, delivery: announce.DeliveryLAN}
	reply := true
	got, _ := announceHandler(stub, nil).CreateAnnouncement(context.Background(), CreateAnnouncementRequestObject{
		DeviceId: "e072a1f96ef0", Body: &CreateAnnouncementJSONRequestBody{Text: "La lavadora ha terminado", Reply: &reply},
	})
	body, ok := got.(CreateAnnouncement202JSONResponse)
	if !ok {
		t.Fatalf("got %#v", got)
	}
	if body.Delivery != AnnouncementDeliveryLan || !body.Reply || body.Text != "La lavadora ha terminado" || !body.ExpiresAt.Equal(at.Add(announce.TTL)) {
		t.Fatalf("body = %+v", body)
	}
}

func TestCreateAnnouncementMapsRefusals(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{device.ErrNotFound, "CreateAnnouncement404"},
		{announce.ErrBusy, "CreateAnnouncement409"},
		{announce.ErrInvalid, "CreateAnnouncement422"},
		{announce.ErrProfile, "CreateAnnouncement422"},
		{announce.ErrNotAdopted, "CreateAnnouncement422"},
		{announce.ErrAbsent, "CreateAnnouncement422"},
		{fmt.Errorf("boom"), "CreateAnnouncement503"},
	}
	for _, c := range cases {
		got, _ := announceHandler(&stubAnnouncements{err: c.err}, nil).CreateAnnouncement(context.Background(), CreateAnnouncementRequestObject{
			DeviceId: "e072a1f96ef0", Body: &CreateAnnouncementJSONRequestBody{Text: "Hola"},
		})
		if name := fmt.Sprintf("%T", got); !strings.Contains(name, c.want) {
			t.Errorf("%v → %s, want %s", c.err, name, c.want)
		}
	}
}

func TestAnAnnounceSessionCarriesTheClaimedAnnouncement(t *testing.T) {
	sessions := &capturingSessions{}
	stub := &stubAnnouncements{claimed: announce.Announcement{ID: uuid.New(), Text: "Hola", Reply: true}}
	kind := SessionRequestKindAnnounce
	id := stub.claimed.ID
	got, _ := announceHandler(stub, sessions).CreateSession(context.Background(), CreateSessionRequestObject{
		Params: CreateSessionParams{XDeviceId: "e072a1f96ef0", XDeviceSecret: "s"},
		Body:   &CreateSessionJSONRequestBody{Kind: &kind, AnnouncementId: &id},
	})
	created, ok := got.(CreateSession201JSONResponse)
	if !ok || !created.Listen {
		t.Fatalf("got %#v", got)
	}
	if sessions.got.Kind != session.KindAnnounce || sessions.got.Announcement != (session.Announced{Text: "Hola", Reply: true}) {
		t.Fatalf("credentials = %+v", sessions.got)
	}
}

func TestAnAnnounceSessionWithoutItsAnnouncementIsRefused(t *testing.T) {
	kind := SessionRequestKindAnnounce
	id := uuid.New()
	for err, want := range map[error]string{announce.ErrNotFound: "CreateSession404", announce.ErrUnauthorized: "CreateSession401"} {
		sessions := &capturingSessions{}
		got, _ := announceHandler(&stubAnnouncements{claimErr: err}, sessions).CreateSession(context.Background(), CreateSessionRequestObject{
			Params: CreateSessionParams{XDeviceId: "e072a1f96ef0", XDeviceSecret: "s"},
			Body:   &CreateSessionJSONRequestBody{Kind: &kind, AnnouncementId: &id},
		})
		if name := fmt.Sprintf("%T", got); !strings.Contains(name, want) {
			t.Errorf("%v → %s, want %s", err, name, want)
		}
		if sessions.got.DeviceID != "" {
			t.Errorf("%v: no session must be created", err)
		}
	}
}

func TestPollCarriesThePendingAnnouncement(t *testing.T) {
	h := announceHandler(&stubAnnouncements{pending: "start:abc"}, nil)
	got, _ := h.GetDesiredProfile(context.Background(), GetDesiredProfileRequestObject{DeviceId: "e072a1f96ef0"})
	if text := got.(GetDesiredProfile200TextResponse); text.Headers.XAnnounce == nil || *text.Headers.XAnnounce != "start:abc" {
		t.Fatalf("X-Announce = %v", text.Headers.XAnnounce)
	}
	h = announceHandler(&stubAnnouncements{}, nil)
	got, _ = h.GetDesiredProfile(context.Background(), GetDesiredProfileRequestObject{DeviceId: "e072a1f96ef0"})
	if text := got.(GetDesiredProfile200TextResponse); text.Headers.XAnnounce != nil {
		t.Fatal("no announcement, no header")
	}
}

type failingSessions struct{}

func (failingSessions) Create(context.Context, session.Credentials) (session.Created, error) {
	return session.Created{}, session.ErrUnavailable
}

func TestAnAnnouncementGoesBackWhenItsSessionCannotBeCreated(t *testing.T) {
	stub := &stubAnnouncements{claimed: announce.Announcement{ID: uuid.New(), Text: "Hola"}}
	kind := SessionRequestKindAnnounce
	id := stub.claimed.ID
	got, _ := announceHandler(stub, failingSessions{}).CreateSession(context.Background(), CreateSessionRequestObject{
		Params: CreateSessionParams{XDeviceId: "e072a1f96ef0", XDeviceSecret: "s"},
		Body:   &CreateSessionJSONRequestBody{Kind: &kind, AnnouncementId: &id},
	})
	if _, ok := got.(CreateSession503ApplicationProblemPlusJSONResponse); !ok {
		t.Fatalf("got %#v", got)
	}
	if len(stub.restored) != 1 || stub.restored[0].ID != id {
		t.Fatalf("restored = %+v", stub.restored)
	}
}

func TestAForgottenUnitClaimingAnAnnouncementIsTold401(t *testing.T) {
	kind := SessionRequestKindAnnounce
	id := uuid.New()
	got, _ := announceHandler(&stubAnnouncements{claimErr: device.ErrNotFound}, &capturingSessions{}).CreateSession(context.Background(), CreateSessionRequestObject{
		Params: CreateSessionParams{XDeviceId: "olvidada", XDeviceSecret: "s"},
		Body:   &CreateSessionJSONRequestBody{Kind: &kind, AnnouncementId: &id},
	})
	if _, ok := got.(CreateSession401ApplicationProblemPlusJSONResponse); !ok {
		t.Fatalf("got %#v", got)
	}
}
