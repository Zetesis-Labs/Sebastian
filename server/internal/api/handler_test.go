package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
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
	limits   [2]int
	desired  string
	items    []device.Device
	err      error
	reported string
	poll     device.Poll
	job      device.Job
	config   []byte
	secret   string
	running  map[string]any
}

func (s *stubDevices) Reconcile(_ context.Context, poll device.Poll) (device.PollResult, error) {
	s.reported = poll.Reported
	s.poll = poll
	return device.PollResult{DesiredProfile: s.desired, DesiredConfigVersion: "v1"}, s.err
}

func (s *stubDevices) List(context.Context) ([]device.Device, error) {
	return s.items, s.err
}

func (s *stubDevices) Get(context.Context, string) (device.Detail, error) {
	if len(s.items) == 0 {
		return device.Detail{}, device.ErrNotFound
	}
	return device.Detail{Device: s.items[0], Sessions: []device.Session{}}, s.err
}

func (s *stubDevices) SetDesiredProfile(context.Context, string, string) error { return s.err }
func (s *stubDevices) Rename(context.Context, string, string) error            { return s.err }
func (s *stubDevices) SetMeetingLimits(_ context.Context, _ string, silence, hours int) error {
	s.limits = [2]int{silence, hours}
	return s.err
}
func (s *stubDevices) SetDesiredConfig(context.Context, string, map[string]any) (string, error) {
	return "abc123", s.err
}
func (s *stubDevices) ClearDesiredConfig(context.Context, string) error { return s.err }
func (s *stubDevices) DeviceConfig(_ context.Context, _, secret string) (json.RawMessage, error) {
	if s.secret != "" && secret != s.secret {
		return nil, device.ErrUnauthorized
	}
	if s.config == nil {
		return nil, device.ErrNotFound
	}
	return s.config, s.err
}
func (s *stubDevices) ReportRunningConfig(_ context.Context, _, secret string, doc map[string]any) error {
	if secret != s.secret {
		return device.ErrUnauthorized
	}
	s.running = doc
	return s.err
}
func (s *stubDevices) RegenerateSecret(context.Context, string) (string, error) {
	return "new-secret", s.err
}
func (s *stubDevices) EnrollChallenge(string) (string, error) {
	if s.secret == "" {
		return "", device.ErrNoOrgSecret
	}
	return "nonce", s.err
}
func (s *stubDevices) Enroll(_ context.Context, _, nonce, mac, deviceSecret string) error {
	if s.secret == "" {
		return device.ErrNoOrgSecret
	}
	if nonce != "nonce" || mac != "proof" || deviceSecret == "" {
		return device.ErrUnauthorized
	}
	return s.err
}
func (s *stubDevices) Adopt(context.Context, string, device.AdoptRequest) (device.Job, error) {
	return s.job, s.err
}
func (s *stubDevices) Forget(context.Context, string, device.AdoptRequest) (device.Job, error) {
	return s.job, s.err
}
func (s *stubDevices) Job(uuid.UUID) (device.Job, error) {
	if s.job.ID == (uuid.UUID{}) {
		return device.Job{}, device.ErrJobNotFound
	}
	return s.job, nil
}
func (s *stubDevices) ControlRoom() device.ControlRoom {
	return device.ControlRoom{Name: "test", APIURL: "http://cr:8787"}
}
func (s *stubDevices) DiscoveryEnabled() bool { return false }

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestCreateSessionMapsAuthenticationFailure(t *testing.T) {
	handler := NewHandler(stubSessions{err: session.ErrUnauthorized}, nil, nil, nil, stubReadiness{}, testLogger(), time.Second)

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

func TestReadinessReportsDependencyFailure(t *testing.T) {
	handler := NewHandler(stubSessions{}, nil, nil, nil, stubReadiness{err: errors.New("down")}, testLogger(), time.Second)
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
	}}}, nil, nil, stubReadiness{}, testLogger(), time.Second)

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
	handler := NewHandler(stubSessions{}, stubRecordings{err: recording.ErrConflict}, nil, nil, stubReadiness{}, testLogger(), time.Second)

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
	handler := NewHandler(stubSessions{}, nil, devices, nil, stubReadiness{}, testLogger(), time.Second)

	current := "micro-usb"
	response, err := handler.GetDesiredProfile(context.Background(), GetDesiredProfileRequestObject{
		DeviceId: "e072a1f96ef0",
		Params:   GetDesiredProfileParams{Current: &current},
	})
	if err != nil {
		t.Fatalf("GetDesiredProfile() error = %v", err)
	}
	value, ok := response.(GetDesiredProfile200TextResponse)
	if !ok || value.Body != "agente" {
		t.Fatalf("response = %#v, want plaintext 'agente'", response)
	}
	if devices.reported != "micro-usb" {
		t.Fatalf("reported = %q, want the current query param recorded", devices.reported)
	}
}

func TestSetDesiredProfileMapsUnknownDevice(t *testing.T) {
	handler := NewHandler(stubSessions{}, nil, &stubDevices{err: device.ErrNotFound}, nil, stubReadiness{}, testLogger(), time.Second)

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
	}}, nil, stubReadiness{}, testLogger(), time.Second)

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

func TestGetDesiredProfileCarriesTheDesiredConfigHeaderAndReportsFirmware(t *testing.T) {
	devices := &stubDevices{desired: "agente"}
	handler := NewHandler(nil, nil, devices, nil, stubReadiness{}, testLogger(), time.Second)
	cfg, fw, ev := "abc", "v1.2", "adopt-denied:10.0.0.77"
	response, err := handler.GetDesiredProfile(context.Background(), GetDesiredProfileRequestObject{
		DeviceId: "68ee", Params: GetDesiredProfileParams{Cfg: &cfg, Fw: &fw, Ev: &ev},
	})
	if err != nil {
		t.Fatal(err)
	}
	text, ok := response.(GetDesiredProfile200TextResponse)
	if !ok || text.Body != "agente" || text.Headers.XDesiredConfig == nil || *text.Headers.XDesiredConfig != "v1" {
		t.Fatalf("unexpected response %#v", response)
	}
	if devices.poll.ConfigVersion != "abc" || devices.poll.Firmware != "v1.2" || devices.poll.Event != "adopt-denied:10.0.0.77" {
		t.Fatalf("poll not forwarded: %+v", devices.poll)
	}
}

func TestGetDeviceConfigMapsAuthAndAbsence(t *testing.T) {
	devices := &stubDevices{secret: "s3cret-s3cret-s3cret-s3cret-s3cret"}
	handler := NewHandler(nil, nil, devices, nil, stubReadiness{}, testLogger(), time.Second)
	response, _ := handler.GetDeviceConfig(context.Background(), GetDeviceConfigRequestObject{DeviceId: "68ee", Params: GetDeviceConfigParams{XDeviceSecret: "wrong"}})
	if _, ok := response.(GetDeviceConfig401ApplicationProblemPlusJSONResponse); !ok {
		t.Fatalf("expected 401, got %#v", response)
	}
	response, _ = handler.GetDeviceConfig(context.Background(), GetDeviceConfigRequestObject{DeviceId: "68ee", Params: GetDeviceConfigParams{XDeviceSecret: devices.secret}})
	if _, ok := response.(GetDeviceConfig404ApplicationProblemPlusJSONResponse); !ok {
		t.Fatalf("expected 404 without a desired config, got %#v", response)
	}
	devices.config = []byte(`{"schema":"sebastian.config.v1","configVersion":"v1"}`)
	response, _ = handler.GetDeviceConfig(context.Background(), GetDeviceConfigRequestObject{DeviceId: "68ee", Params: GetDeviceConfigParams{XDeviceSecret: devices.secret}})
	body, ok := response.(GetDeviceConfig200JSONResponse)
	if !ok || body["configVersion"] != "v1" {
		t.Fatalf("expected the document, got %#v", response)
	}
}

func TestEnrollDeviceMapsUnavailableAuthAndSuccess(t *testing.T) {
	devices := &stubDevices{}
	handler := NewHandler(nil, nil, devices, nil, stubReadiness{}, testLogger(), time.Second)
	response, _ := handler.GetEnrollmentChallenge(context.Background(), GetEnrollmentChallengeRequestObject{DeviceId: "68ee"})
	if _, ok := response.(GetEnrollmentChallenge404ApplicationProblemPlusJSONResponse); !ok {
		t.Fatalf("expected 404 without an organization secret, got %#v", response)
	}
	devices.secret = "s3cret-s3cret-s3cret-s3cret-s3cret"
	response, _ = handler.GetEnrollmentChallenge(context.Background(), GetEnrollmentChallengeRequestObject{DeviceId: "68ee"})
	if challenge, ok := response.(GetEnrollmentChallenge200JSONResponse); !ok || challenge.Nonce != "nonce" {
		t.Fatalf("expected the nonce, got %#v", response)
	}
	enrolled, _ := handler.EnrollDevice(context.Background(), EnrollDeviceRequestObject{DeviceId: "68ee", Body: &EnrollmentRequest{Nonce: "nonce", Mac: "bad", DeviceSecret: "unit"}})
	if _, ok := enrolled.(EnrollDevice401ApplicationProblemPlusJSONResponse); !ok {
		t.Fatalf("expected 401, got %#v", enrolled)
	}
	enrolled, _ = handler.EnrollDevice(context.Background(), EnrollDeviceRequestObject{DeviceId: "68ee", Body: &EnrollmentRequest{Nonce: "nonce", Mac: "proof", DeviceSecret: "unit"}})
	if body, ok := enrolled.(EnrollDevice201JSONResponse); !ok || body.ControlRoom == "" {
		t.Fatalf("expected the control room name, got %#v", enrolled)
	}
}

func TestReportRunningConfigMapsAuthAndRecords(t *testing.T) {
	devices := &stubDevices{secret: "s3cret-s3cret-s3cret-s3cret-s3cret"}
	handler := NewHandler(nil, nil, devices, nil, stubReadiness{}, testLogger(), time.Second)
	body := DeviceConfig{"schema": "sebastian.config.v1", "mode": "half_duplex"}
	response, _ := handler.ReportRunningConfig(context.Background(), ReportRunningConfigRequestObject{DeviceId: "68ee", Params: ReportRunningConfigParams{XDeviceSecret: "wrong"}, Body: &body})
	if _, ok := response.(ReportRunningConfig401ApplicationProblemPlusJSONResponse); !ok {
		t.Fatalf("expected 401, got %#v", response)
	}
	response, _ = handler.ReportRunningConfig(context.Background(), ReportRunningConfigRequestObject{DeviceId: "68ee", Params: ReportRunningConfigParams{XDeviceSecret: devices.secret}, Body: &body})
	if _, ok := response.(ReportRunningConfig204Response); !ok || devices.running["mode"] != "half_duplex" {
		t.Fatalf("expected 204 and the document recorded, got %#v", response)
	}
}

func TestAdoptDeviceMapsNoAddressAndStartsAJob(t *testing.T) {
	devices := &stubDevices{err: device.ErrNoAddress}
	handler := NewHandler(nil, nil, devices, nil, stubReadiness{}, testLogger(), time.Second)
	response, _ := handler.AdoptDevice(context.Background(), AdoptDeviceRequestObject{DeviceId: "dddd"})
	if _, ok := response.(AdoptDevice400ApplicationProblemPlusJSONResponse); !ok {
		t.Fatalf("expected 400, got %#v", response)
	}
	devices.err = nil
	devices.job = device.Job{ID: uuid.New(), DeviceID: "dddd", Kind: "adopt", Phase: "starting"}
	response, _ = handler.AdoptDevice(context.Background(), AdoptDeviceRequestObject{DeviceId: "dddd"})
	job, ok := response.(AdoptDevice202JSONResponse)
	if !ok || job.Phase != "starting" || job.DeviceSecret != nil {
		t.Fatalf("expected a started job without secret, got %#v", response)
	}
}

func TestListDevicesExposesTheFleetState(t *testing.T) {
	devices := &stubDevices{items: []device.Device{{ID: "cccc", DisplayName: "cccc", Enabled: true, State: device.StateOrphan, IP: "10.0.0.130", ControlRoom: "http://10.0.100.10:8787", LastError: "timeout"}}}
	handler := NewHandler(nil, nil, devices, nil, stubReadiness{}, testLogger(), time.Second)
	response, _ := handler.ListDevices(context.Background(), ListDevicesRequestObject{})
	list := response.(ListDevices200JSONResponse)
	if len(list.Items) != 1 || list.Items[0].State != "orphan" || *list.Items[0].Ip != "10.0.0.130" || *list.Items[0].LastError != "timeout" {
		t.Fatalf("unexpected %#v", list.Items)
	}
}

// RM-23/24: the ficha reads and writes the unit's meeting limits.
func TestDeviceMeetingLimitsTravel(t *testing.T) {
	devices := &stubDevices{items: []device.Device{{ID: "68ee", DisplayName: "68ee", Enabled: true, State: device.StateAdopted, MeetingSilenceMin: 15, MeetingMaxHours: 2}}}
	h := NewHandler(nil, nil, devices, &stubMeetings{}, stubReadiness{}, testLogger(), time.Second)
	got, _ := h.GetDevice(context.Background(), GetDeviceRequestObject{DeviceId: "68ee"})
	detail := got.(GetDevice200JSONResponse)
	if detail.MeetingSilenceMin == nil || *detail.MeetingSilenceMin != 15 || detail.MeetingMaxHours == nil || *detail.MeetingMaxHours != 2 {
		t.Fatalf("detail limits: %+v %+v", detail.MeetingSilenceMin, detail.MeetingMaxHours)
	}
	silence := 20
	if res, _ := h.UpdateDevice(context.Background(), UpdateDeviceRequestObject{DeviceId: "68ee", Body: &DeviceUpdate{MeetingSilenceMin: &silence}}); reflect.TypeOf(res).Name() != "UpdateDevice204Response" || devices.limits != [2]int{20, 2} {
		t.Fatalf("patch keeps the other limit: %T %v", res, devices.limits)
	}
}
