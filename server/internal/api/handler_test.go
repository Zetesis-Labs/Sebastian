package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zetesis-labs/sebastian/server/internal/device"
	"github.com/zetesis-labs/sebastian/server/internal/recording"
	"github.com/zetesis-labs/sebastian/server/internal/session"
)

type stubSessions struct {
	created session.Created
	err     error
}

func (s stubSessions) Create(context.Context, session.Credentials) (session.Created, error) {
	return s.created, s.err
}

type stubReadiness struct{ err error }

func (s stubReadiness) Ping(context.Context) error { return s.err }

type stubRecordings struct {
	items      []recording.Recording
	registered recording.Recording
	err        error
}

func (s stubRecordings) List(context.Context, int) ([]recording.Recording, error) {
	return s.items, s.err
}

func (s stubRecordings) Get(context.Context, uuid.UUID) (recording.Recording, error) {
	return s.registered, s.err
}

func (s stubRecordings) Register(context.Context, recording.Registration) (recording.Recording, error) {
	return s.registered, s.err
}

func (s stubRecordings) Summary(context.Context) (recording.Summary, error) {
	return recording.Summary{Count: int64(len(s.items))}, s.err
}

type stubDevices struct {
	desired  string
	items    []device.Device
	err      error
	reported string
}

func (s *stubDevices) Reconcile(_ context.Context, _, reported string) (string, error) {
	s.reported = reported
	return s.desired, s.err
}

func (s *stubDevices) List(context.Context) ([]device.Device, error) {
	return s.items, s.err
}

func (s *stubDevices) SetDesired(context.Context, string, string) error {
	return s.err
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestCreateSessionMapsAuthenticationFailure(t *testing.T) {
	handler := NewHandler(stubSessions{err: session.ErrUnauthorized}, nil, nil, stubReadiness{}, testLogger(), false, time.Second)

	response, err := handler.CreateSession(context.Background(), CreateSessionRequestObject{
		Params: CreateSessionParams{XDeviceId: "device", XDeviceSecret: "secret"},
	})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if _, ok := response.(CreateSession401ApplicationProblemPlusJSONResponse); !ok {
		t.Fatalf("response = %T, want 401 response", response)
	}
}

func TestLegacyTokenKeepsFirmwareContract(t *testing.T) {
	handler := NewHandler(stubSessions{created: session.Created{
		ID: uuid.New(), ServerURL: "ws://livekit:7880", Token: "jwt",
	}}, nil, nil, stubReadiness{}, testLogger(), true, time.Second)

	response, err := handler.GetLegacyToken(context.Background(), GetLegacyTokenRequestObject{})
	if err != nil {
		t.Fatalf("GetLegacyToken() error = %v", err)
	}
	value, ok := response.(GetLegacyToken200TextResponse)
	if !ok || string(value) != "ws://livekit:7880\njwt" {
		t.Fatalf("response = %#v", response)
	}
}

func TestReadinessReportsDependencyFailure(t *testing.T) {
	handler := NewHandler(stubSessions{}, nil, nil, stubReadiness{err: errors.New("down")}, testLogger(), false, time.Second)
	response, err := handler.GetReadiness(context.Background(), GetReadinessRequestObject{})
	if err != nil {
		t.Fatalf("GetReadiness() error = %v", err)
	}
	if _, ok := response.(GetReadiness503ApplicationProblemPlusJSONResponse); !ok {
		t.Fatalf("response = %T, want 503 response", response)
	}
}

func TestListRecordingsMapsDomainModel(t *testing.T) {
	id := uuid.New()
	handler := NewHandler(stubSessions{}, stubRecordings{items: []recording.Recording{{
		ID: id, SessionID: uuid.New(), Room: "sebastian-room", Kind: recording.KindModel,
		FileName: "model.wav", ObjectURL: "https://storage.example/model.wav", ContentType: "audio/wav",
	}}}, nil, stubReadiness{}, testLogger(), false, time.Second)

	response, err := handler.ListRecordings(context.Background(), ListRecordingsRequestObject{})
	if err != nil {
		t.Fatalf("ListRecordings() error = %v", err)
	}
	list, ok := response.(ListRecordings200JSONResponse)
	if !ok || len(list.Items) != 1 || list.Items[0].Id != id || list.Items[0].Kind != Model {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestRegisterRecordingMapsConflict(t *testing.T) {
	handler := NewHandler(stubSessions{}, stubRecordings{err: recording.ErrConflict}, nil, stubReadiness{}, testLogger(), false, time.Second)

	response, err := handler.RegisterRecording(context.Background(), RegisterRecordingRequestObject{
		Body: &RecordingRegistration{},
	})
	if err != nil {
		t.Fatalf("RegisterRecording() error = %v", err)
	}
	if _, ok := response.(RegisterRecording409ApplicationProblemPlusJSONResponse); !ok {
		t.Fatalf("response = %T, want 409 response", response)
	}
}

func TestGetDesiredProfileReturnsPlaintextAndReportsCurrent(t *testing.T) {
	devices := &stubDevices{desired: "agente"}
	handler := NewHandler(stubSessions{}, nil, devices, stubReadiness{}, testLogger(), false, time.Second)

	current := "micro-usb"
	response, err := handler.GetDesiredProfile(context.Background(), GetDesiredProfileRequestObject{
		DeviceId: "e072a1f96ef0",
		Params:   GetDesiredProfileParams{Current: &current},
	})
	if err != nil {
		t.Fatalf("GetDesiredProfile() error = %v", err)
	}
	value, ok := response.(GetDesiredProfile200TextResponse)
	if !ok || string(value) != "agente" {
		t.Fatalf("response = %#v, want plaintext 'agente'", response)
	}
	if devices.reported != "micro-usb" {
		t.Fatalf("reported = %q, want the current query param recorded", devices.reported)
	}
}

func TestSetDesiredProfileMapsUnknownDevice(t *testing.T) {
	handler := NewHandler(stubSessions{}, nil, &stubDevices{err: device.ErrNotFound}, stubReadiness{}, testLogger(), false, time.Second)

	name := "agente"
	response, err := handler.SetDesiredProfile(context.Background(), SetDesiredProfileRequestObject{
		DeviceId: "unknown",
		Body:     &SetDesiredProfileJSONRequestBody{Name: &name},
	})
	if err != nil {
		t.Fatalf("SetDesiredProfile() error = %v", err)
	}
	if _, ok := response.(SetDesiredProfile404ApplicationProblemPlusJSONResponse); !ok {
		t.Fatalf("response = %T, want 404 response", response)
	}
}

func TestListDevicesOmitsUnsetProfileFields(t *testing.T) {
	handler := NewHandler(stubSessions{}, nil, &stubDevices{items: []device.Device{
		{ID: "e072a1f96ef0", DisplayName: "e072a1f96ef0", Enabled: true, ReportedProfile: "micro-usb"},
	}}, stubReadiness{}, testLogger(), false, time.Second)

	response, err := handler.ListDevices(context.Background(), ListDevicesRequestObject{})
	if err != nil {
		t.Fatalf("ListDevices() error = %v", err)
	}
	list, ok := response.(ListDevices200JSONResponse)
	if !ok || len(list.Items) != 1 {
		t.Fatalf("unexpected response: %#v", response)
	}
	item := list.Items[0]
	if item.DesiredProfile != nil || item.ProfileReportedAt != nil {
		t.Fatalf("unset fields must be omitted: %#v", item)
	}
	if item.ReportedProfile == nil || *item.ReportedProfile != "micro-usb" {
		t.Fatalf("reportedProfile = %v, want micro-usb", item.ReportedProfile)
	}
}
