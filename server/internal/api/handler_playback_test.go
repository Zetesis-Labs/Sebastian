package api

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zetesis-labs/sebastian/server/internal/device"
	"github.com/zetesis-labs/sebastian/server/internal/playback"
	"github.com/zetesis-labs/sebastian/server/internal/session"
)

type stubPlayback struct {
	started  playback.Started
	err      error
	active   *playback.Playback
	stopErr  error
	stopped  []string
	joined   playback.Playback
	joinErr  error
	pending  string
	joinedID uuid.UUID
}

func (s *stubPlayback) Start(context.Context, string) (playback.Started, error) {
	return s.started, s.err
}

func (s *stubPlayback) Stop(_ context.Context, deviceID string) error {
	s.stopped = append(s.stopped, deviceID)
	return s.stopErr
}

func (s *stubPlayback) Active(context.Context, string) (playback.Playback, bool) {
	if s.active == nil {
		return playback.Playback{}, false
	}
	return *s.active, true
}

func (s *stubPlayback) PendingCommand(context.Context, string) string { return s.pending }

func (s *stubPlayback) Join(_ context.Context, _, _ string, id uuid.UUID) (playback.Playback, error) {
	s.joinedID = id
	return s.joined, s.joinErr
}

func playbackHandler(p *stubPlayback, sessions SessionService) *Server {
	if sessions == nil {
		sessions = &capturingSessions{}
	}
	return NewHandler(sessions, &stubDevices{desired: "agente"}, &stubMeetings{}, stubReadiness{}, testLogger(), time.Second).WithPlayback(p)
}

func TestStartPlaybackTellsTheSourceWhereToPublish(t *testing.T) {
	at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	id := uuid.New()
	stub := &stubPlayback{started: playback.Started{
		Playback: playback.Playback{ID: id, Room: "sebastian-play-x", StartedAt: at},
		Delivery: playback.DeliveryLAN, ServerURL: "ws://10.0.100.10:7880", SourceToken: "source-jwt",
	}}
	got, _ := playbackHandler(stub, nil).StartPlayback(context.Background(), StartPlaybackRequestObject{DeviceId: "e072a1f96ef0"})
	body, ok := got.(StartPlayback201JSONResponse)
	if !ok {
		t.Fatalf("got %#v", got)
	}
	if body.Id != id || body.Room != "sebastian-play-x" || body.ServerUrl != "ws://10.0.100.10:7880" ||
		body.SourceToken != "source-jwt" || body.Delivery != PlaybackStartedDeliveryLan || !body.StartedAt.Equal(at) {
		t.Fatalf("body = %+v", body)
	}
}

func TestStartPlaybackMapsRefusals(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{device.ErrNotFound, "StartPlayback404"},
		{playback.ErrBusy, "StartPlayback409"},
		{playback.ErrProfile, "StartPlayback422"},
		{playback.ErrNotAdopted, "StartPlayback422"},
		{playback.ErrAbsent, "StartPlayback422"},
		{fmt.Errorf("boom"), "StartPlayback503"},
	}
	for _, c := range cases {
		got, _ := playbackHandler(&stubPlayback{err: c.err}, nil).StartPlayback(context.Background(), StartPlaybackRequestObject{DeviceId: "e072a1f96ef0"})
		if name := fmt.Sprintf("%T", got); !strings.Contains(name, c.want) {
			t.Errorf("%v → %s, want %s", c.err, name, c.want)
		}
	}
}

func TestGetPlaybackSaysWhatIsOn(t *testing.T) {
	at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	on := &playback.Playback{ID: uuid.New(), StartedAt: at}
	got, _ := playbackHandler(&stubPlayback{active: on}, nil).GetPlayback(context.Background(), GetPlaybackRequestObject{DeviceId: "e072a1f96ef0"})
	if body, ok := got.(GetPlayback200JSONResponse); !ok || body.Id != on.ID || !body.StartedAt.Equal(at) {
		t.Fatalf("got %#v", got)
	}
	got, _ = playbackHandler(&stubPlayback{}, nil).GetPlayback(context.Background(), GetPlaybackRequestObject{DeviceId: "e072a1f96ef0"})
	if _, ok := got.(GetPlayback404ApplicationProblemPlusJSONResponse); !ok {
		t.Fatalf("nothing on: got %#v", got)
	}
}

func TestStopPlayback(t *testing.T) {
	stub := &stubPlayback{}
	got, _ := playbackHandler(stub, nil).StopPlayback(context.Background(), StopPlaybackRequestObject{DeviceId: "e072a1f96ef0"})
	if _, ok := got.(StopPlayback204Response); !ok || len(stub.stopped) != 1 {
		t.Fatalf("got %#v, stopped %v", got, stub.stopped)
	}
	got, _ = playbackHandler(&stubPlayback{stopErr: playback.ErrNotFound}, nil).StopPlayback(context.Background(), StopPlaybackRequestObject{DeviceId: "e072a1f96ef0"})
	if _, ok := got.(StopPlayback404ApplicationProblemPlusJSONResponse); !ok {
		t.Fatalf("nothing on: got %#v", got)
	}
}

func TestAPlaybackSessionJoinsTheRoomOfThePlaybackOn(t *testing.T) {
	sessions := &capturingSessions{}
	id := uuid.New()
	stub := &stubPlayback{joined: playback.Playback{ID: id, Room: "sebastian-play-x"}}
	kind := SessionRequestKindPlayback
	got, _ := playbackHandler(stub, sessions).CreateSession(context.Background(), CreateSessionRequestObject{
		Params: CreateSessionParams{XDeviceId: "e072a1f96ef0", XDeviceSecret: "s"},
		Body:   &CreateSessionJSONRequestBody{Kind: &kind, PlaybackId: &id},
	})
	if _, ok := got.(CreateSession201JSONResponse); !ok {
		t.Fatalf("got %#v", got)
	}
	if stub.joinedID != id || sessions.got.Kind != session.KindPlayback || sessions.got.PlaybackRoom != "sebastian-play-x" {
		t.Fatalf("joined %v, credentials %+v", stub.joinedID, sessions.got)
	}
}

func TestAPlaybackSessionForAPlaybackThatIsOffIsRefused(t *testing.T) {
	kind := SessionRequestKindPlayback
	id := uuid.New()
	for err, want := range map[error]string{playback.ErrNotFound: "CreateSession404", playback.ErrUnauthorized: "CreateSession401", device.ErrNotFound: "CreateSession401"} {
		sessions := &capturingSessions{}
		got, _ := playbackHandler(&stubPlayback{joinErr: err}, sessions).CreateSession(context.Background(), CreateSessionRequestObject{
			Params: CreateSessionParams{XDeviceId: "e072a1f96ef0", XDeviceSecret: "s"},
			Body:   &CreateSessionJSONRequestBody{Kind: &kind, PlaybackId: &id},
		})
		if name := fmt.Sprintf("%T", got); !strings.Contains(name, want) {
			t.Errorf("%v → %s, want %s", err, name, want)
		}
		if sessions.got.DeviceID != "" {
			t.Errorf("%v: no session must be created", err)
		}
	}
}

func TestPollCarriesThePlayback(t *testing.T) {
	got, _ := playbackHandler(&stubPlayback{pending: "start:abc"}, nil).GetDesiredProfile(context.Background(), GetDesiredProfileRequestObject{DeviceId: "e072a1f96ef0"})
	if text := got.(GetDesiredProfile200TextResponse); text.Headers.XPlayback == nil || *text.Headers.XPlayback != "start:abc" {
		t.Fatalf("X-Playback = %v", text.Headers.XPlayback)
	}
	got, _ = playbackHandler(&stubPlayback{}, nil).GetDesiredProfile(context.Background(), GetDesiredProfileRequestObject{DeviceId: "e072a1f96ef0"})
	if text := got.(GetDesiredProfile200TextResponse); text.Headers.XPlayback != nil {
		t.Fatal("no playback, no header")
	}
}

func TestStopPlaybackDoesNotClaimAStopThatFailed(t *testing.T) {
	got, _ := playbackHandler(&stubPlayback{stopErr: fmt.Errorf("boom")}, nil).StopPlayback(context.Background(), StopPlaybackRequestObject{DeviceId: "e072a1f96ef0"})
	if _, ok := got.(StopPlayback503ApplicationProblemPlusJSONResponse); !ok {
		t.Fatalf("got %#v", got)
	}
	unconfigured := NewHandler(&capturingSessions{}, &stubDevices{desired: "agente"}, &stubMeetings{}, stubReadiness{}, testLogger(), time.Second)
	got, _ = unconfigured.StopPlayback(context.Background(), StopPlaybackRequestObject{DeviceId: "e072a1f96ef0"})
	if _, ok := got.(StopPlayback503ApplicationProblemPlusJSONResponse); !ok {
		t.Fatalf("no playback service: got %#v", got)
	}
}

func TestAPlaybackSessionWithoutItsPlaybackIsRefused(t *testing.T) {
	kind := SessionRequestKindPlayback
	for name, h := range map[string]*Server{
		"no playback id": playbackHandler(&stubPlayback{}, nil),
		"no service":     NewHandler(&capturingSessions{}, &stubDevices{desired: "agente"}, &stubMeetings{}, stubReadiness{}, testLogger(), time.Second),
	} {
		sessions := &capturingSessions{}
		h.sessions = sessions
		got, _ := h.CreateSession(context.Background(), CreateSessionRequestObject{
			Params: CreateSessionParams{XDeviceId: "e072a1f96ef0", XDeviceSecret: "s"},
			Body:   &CreateSessionJSONRequestBody{Kind: &kind},
		})
		if _, ok := got.(CreateSession404ApplicationProblemPlusJSONResponse); !ok {
			t.Errorf("%s: got %#v", name, got)
		}
		if sessions.got.DeviceID != "" {
			t.Errorf("%s: a session was created", name)
		}
	}
}
