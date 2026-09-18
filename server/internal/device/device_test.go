package device

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/zetesis-labs/sebastian/server/internal/adoption"
	"github.com/zetesis-labs/sebastian/server/internal/discovery"
)

var now = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

func TestDeriveStateFunctionalSpecTable(t *testing.T) {
	self := "http://10.0.0.188:8787"
	adopted := &Device{ID: "a", Adopted: true, ProfileReportedAt: now.Add(-10 * time.Second)}
	silent := &Device{ID: "a", Adopted: true, ProfileReportedAt: now.Add(-5 * time.Minute)}
	registered := &Device{ID: "a", Adopted: false, ProfileReportedAt: now}
	cases := []struct {
		name string
		row  *Device
		seen *discovery.Seen
		want State
	}{
		{"adopted here and polling", adopted, nil, StateAdopted},
		{"adopted here, silent, not on LAN", silent, nil, StateAbsent},
		{"adopted here and announcing us", silent, &discovery.Seen{ControlRoom: self, LastError: "ok"}, StateAdopted},
		{"announces us but never adopted (legacy /token)", registered, &discovery.Seen{ControlRoom: self}, StateRegistered},
		{"polls without secret, not on LAN", registered, nil, StateRegistered},
		{"on LAN, no control room", nil, &discovery.Seen{ControlRoom: ""}, StateUnadopted},
		{"on LAN, another control room, healthy", nil, &discovery.Seen{ControlRoom: "http://10.0.100.10:8787", LastError: "ok"}, StateManagedElsewhere},
		{"on LAN, another control room, failing", nil, &discovery.Seen{ControlRoom: "http://10.0.100.10:8787", LastError: "timeout"}, StateOrphan},
		{"denied adoption attempt is not an orphan", nil, &discovery.Seen{ControlRoom: "http://10.0.100.10:8787", LastError: "adopt-denied:10.0.0.77"}, StateManagedElsewhere},
		{"trailing slash on the announced origin", adopted, &discovery.Seen{ControlRoom: self + "/"}, StateAdopted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveState(tc.row, tc.seen, self, now); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

// ── fakes ───────────────────────────────────────────────────────────────────

type fakeStore struct {
	devices  map[string]*Device
	configs  map[string]json.RawMessage
	versions map[string]string
	digests  map[string][2][]byte
	forgot   []string
	renamed  map[string]string
}

func newFakeStore(devices ...Device) *fakeStore {
	f := &fakeStore{devices: map[string]*Device{}, configs: map[string]json.RawMessage{}, versions: map[string]string{}, digests: map[string][2][]byte{}, renamed: map[string]string{}}
	for i := range devices {
		d := devices[i]
		f.devices[d.ID] = &d
	}
	return f
}

func (f *fakeStore) TouchPoll(_ context.Context, p Poll) (PollResult, error) {
	d, ok := f.devices[p.ID]
	if !ok {
		d = &Device{ID: p.ID, DisplayName: p.ID, Enabled: true}
		f.devices[p.ID] = d
	}
	d.ReportedProfile, d.ReportedConfig, d.ReportedFirmware, d.ProfileReportedAt = p.Reported, p.ConfigVersion, p.Firmware, now
	return PollResult{DesiredProfile: d.DesiredProfile, DesiredConfigVersion: f.versions[p.ID]}, nil
}
func (f *fakeStore) List(context.Context) ([]Device, error) {
	out := []Device{}
	for _, d := range f.devices {
		out = append(out, *d)
	}
	return out, nil
}
func (f *fakeStore) Get(_ context.Context, id string) (Device, error) {
	d, ok := f.devices[id]
	if !ok {
		return Device{}, ErrNotFound
	}
	return *d, nil
}
func (f *fakeStore) Sessions(context.Context, string) ([]Session, error) { return nil, nil }
func (f *fakeStore) SetDesiredProfile(_ context.Context, id, name string) error {
	d, ok := f.devices[id]
	if !ok {
		return ErrNotFound
	}
	d.DesiredProfile = name
	return nil
}
func (f *fakeStore) DesiredConfig(_ context.Context, id string) (json.RawMessage, string, error) {
	if _, ok := f.devices[id]; !ok {
		return nil, "", ErrNotFound
	}
	return f.configs[id], f.versions[id], nil
}
func (f *fakeStore) SetDesiredConfig(_ context.Context, id string, cfg json.RawMessage, v string) error {
	if _, ok := f.devices[id]; !ok {
		return ErrNotFound
	}
	f.configs[id], f.versions[id] = cfg, v
	f.devices[id].DesiredConfig = v
	return nil
}
func (f *fakeStore) ClearDesiredConfig(_ context.Context, id string) error {
	delete(f.configs, id)
	delete(f.versions, id)
	return nil
}
func (f *fakeStore) CredentialDigests(_ context.Context, id string) ([]byte, []byte, error) {
	if _, ok := f.devices[id]; !ok {
		return nil, nil, ErrNotFound
	}
	d := f.digests[id]
	return d[0], d[1], nil
}
func (f *fakeStore) MarkAdopted(_ context.Context, id string, digest []byte) error {
	d, ok := f.devices[id]
	if !ok {
		d = &Device{ID: id, DisplayName: id, Enabled: true}
		f.devices[id] = d
	}
	d.Adopted, d.AdoptedAt = true, now
	f.digests[id] = [2][]byte{digest, nil}
	return nil
}
func (f *fakeStore) SetPendingSecret(_ context.Context, id string, digest []byte) error {
	d := f.digests[id]
	f.digests[id] = [2][]byte{d[0], digest}
	return nil
}
func (f *fakeStore) Forget(_ context.Context, id string) error {
	f.forgot = append(f.forgot, id)
	delete(f.devices, id)
	return nil
}
func (f *fakeStore) Rename(_ context.Context, id, name string) error {
	f.renamed[id] = name
	return nil
}

type fakeAdopter struct {
	calls   []fakeCall
	fail    error
	phases  []adoption.Phase
	verify  string // expected secret
	lastCfg string
}

type fakeCall struct{ ip, cfg, secret string }

func (a *fakeAdopter) Adopt(_ context.Context, ip, cfg, secret string, progress func(adoption.Phase)) error {
	a.calls = append(a.calls, fakeCall{ip, cfg, secret})
	a.lastCfg = cfg
	for _, p := range a.phases {
		progress(p)
	}
	if a.verify != "" && secret != a.verify {
		return adoption.ErrDenied
	}
	return a.fail
}

func newTestService(store *fakeStore, lan discovery.Browser, adopter *fakeAdopter) *Service {
	room := ControlRoom{Name: "casa", APIURL: "http://10.0.0.188:8787", SyslogIP: "10.0.0.188", SyslogPort: 514, OrgSecret: "org-secret", AdoptPort: 5688}
	s := NewService(store, lan, adopter, room, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.now = func() time.Time { return now }
	s.newSecret = func() string { return strings.Repeat("ab", 32) }
	return s
}

func waitJob(t *testing.T, s *Service, job Job) Job {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		j, err := s.Job(job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if j.Phase == "adopted" || j.Phase == "failed" || j.Phase == "forgotten" {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return Job{}
}

// ── enrolment ───────────────────────────────────────────────────────────────

func TestEnrollBindsAUnitThatProvesTheOrganizationSecret(t *testing.T) {
	const id = "e072a1f96ef0"
	store := newFakeStore()
	s := newTestService(store, nil, &fakeAdopter{})
	s.newNonce = func() string { return strings.Repeat("0f", 32) }

	nonce, err := s.EnrollChallenge(id)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := s.Enroll(context.Background(), id, nonce, adoption.Sign("org-secret", nonce, id))
	if err != nil {
		t.Fatal(err)
	}
	if secret != strings.Repeat("ab", 32) {
		t.Fatalf("secret %q", secret)
	}
	d := store.devices[id]
	if d == nil || !d.Adopted || string(store.digests[id][0]) != string(DigestSecret(secret)) {
		t.Fatalf("unit not adopted with its secret: %+v", d)
	}
	if _, err := s.Enroll(context.Background(), id, nonce, adoption.Sign("org-secret", nonce, id)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("nonce reused: %v", err)
	}
}

func TestEnrollRejectsBadProofExpiryAndMissingOrgSecret(t *testing.T) {
	const id = "e072a1f96ef0"
	store := newFakeStore()
	s := newTestService(store, nil, &fakeAdopter{})

	nonce, _ := s.EnrollChallenge(id)
	if _, err := s.Enroll(context.Background(), id, nonce, adoption.Sign("wrong", nonce, id)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong secret: %v", err)
	}
	nonce, _ = s.EnrollChallenge(id)
	if _, err := s.Enroll(context.Background(), "other", nonce, adoption.Sign("org-secret", nonce, id)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("proof for another id: %v", err)
	}
	nonce, _ = s.EnrollChallenge(id)
	s.now = func() time.Time { return now.Add(enrollTTL + time.Second) }
	if _, err := s.Enroll(context.Background(), id, nonce, adoption.Sign("org-secret", nonce, id)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expired nonce: %v", err)
	}
	if len(store.devices) != 0 {
		t.Fatalf("a denied enrolment must not register anything: %v", store.devices)
	}

	s.room.OrgSecret = ""
	if _, err := s.EnrollChallenge(id); !errors.Is(err, ErrNoOrgSecret) {
		t.Fatalf("no org secret: %v", err)
	}
}

// ── list / merge ────────────────────────────────────────────────────────────

func TestListMergesInventoryWithTheLAN(t *testing.T) {
	store := newFakeStore(
		Device{ID: "aaaa", DisplayName: "Salón", Enabled: true, Adopted: true, ProfileReportedAt: now.Add(-5 * time.Second)},
		Device{ID: "bbbb", DisplayName: "bbbb", Enabled: true, Adopted: true, ProfileReportedAt: now.Add(-10 * time.Minute)},
	)
	lan := discovery.Static{On: true, Items: []discovery.Seen{
		{ID: "aaaa", IP: "10.0.0.125", Firmware: "v9", ControlRoom: "http://10.0.0.188:8787", LastError: "ok", SeenAt: now},
		{ID: "cccc", IP: "10.0.0.130", ControlRoom: "http://10.0.100.10:8787", LastError: "timeout", SeenAt: now},
		{ID: "dddd", IP: "10.0.0.131", ControlRoom: "", SeenAt: now},
	}}
	s := newTestService(store, lan, &fakeAdopter{})
	list, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Device{}
	var order []string
	for _, d := range list {
		got[d.ID] = d
		order = append(order, d.ID)
	}
	if got["aaaa"].State != StateAdopted || got["aaaa"].IP != "10.0.0.125" || got["aaaa"].Firmware != "v9" {
		t.Fatalf("aaaa = %+v", got["aaaa"])
	}
	if got["bbbb"].State != StateAbsent {
		t.Fatalf("bbbb = %s", got["bbbb"].State)
	}
	if got["cccc"].State != StateOrphan || got["cccc"].DisplayName != "cccc" {
		t.Fatalf("cccc = %+v", got["cccc"])
	}
	if got["dddd"].State != StateUnadopted {
		t.Fatalf("dddd = %s", got["dddd"].State)
	}
	if strings.Join(order, ",") != "aaaa,cccc,dddd,bbbb" {
		t.Fatalf("order %v (ours first, then orphan, unadopted, absent last)", order)
	}
}

// ── adoption ────────────────────────────────────────────────────────────────

func TestAdoptSendsTheControlRoomConfigSignedWithTheOrgSecret(t *testing.T) {
	store := newFakeStore()
	lan := discovery.Static{On: true, Items: []discovery.Seen{{ID: "dddd", IP: "10.0.0.131", SeenAt: now}}}
	adopter := &fakeAdopter{verify: "org-secret"}
	s := newTestService(store, lan, adopter)

	job, err := s.Adopt(context.Background(), "dddd", AdoptRequest{})
	if err != nil {
		t.Fatal(err)
	}
	done := waitJob(t, s, job)
	if done.Phase != "adopted" || done.DeviceSecret != strings.Repeat("ab", 32) {
		t.Fatalf("job %+v", done)
	}
	if len(adopter.calls) != 1 || adopter.calls[0].ip != "10.0.0.131" {
		t.Fatalf("calls %+v", adopter.calls)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(adopter.lastCfg), &cfg); err != nil {
		t.Fatal(err)
	}
	lk := cfg["livekit"].(map[string]any)
	ad := cfg["adoption"].(map[string]any)
	tel := cfg["telemetry"].(map[string]any)
	if lk["tokenServerUrl"] != "http://10.0.0.188:8787/token" || ad["orgSecret"] != "org-secret" || ad["deviceSecret"] != strings.Repeat("ab", 32) || tel["syslogIp"] != "10.0.0.188" {
		t.Fatalf("config %s", adopter.lastCfg)
	}
	d, _ := store.Get(context.Background(), "dddd")
	if !d.Adopted {
		t.Fatal("device must be recorded as adopted")
	}
}

func TestAdoptByIPWhenNotDiscovered(t *testing.T) {
	store := newFakeStore()
	adopter := &fakeAdopter{}
	s := newTestService(store, discovery.Static{}, adopter)
	if _, err := s.Adopt(context.Background(), "eeee", AdoptRequest{}); !errors.Is(err, ErrNoAddress) {
		t.Fatalf("expected ErrNoAddress, got %v", err)
	}
	if _, err := s.Adopt(context.Background(), "eeee", AdoptRequest{IP: "not-an-ip"}); !errors.Is(err, ErrNoAddress) {
		t.Fatalf("expected ErrNoAddress for a bad ip, got %v", err)
	}
	job, err := s.Adopt(context.Background(), "eeee", AdoptRequest{IP: "10.0.100.40"})
	if err != nil {
		t.Fatal(err)
	}
	if waitJob(t, s, job).Phase != "adopted" || adopter.calls[0].ip != "10.0.100.40" {
		t.Fatalf("calls %+v", adopter.calls)
	}
}

func TestAdoptWithDeviceSecretHandover(t *testing.T) {
	store := newFakeStore()
	adopter := &fakeAdopter{verify: "handover-secret"}
	s := newTestService(store, discovery.Static{}, adopter)
	job, err := s.Adopt(context.Background(), "ffff", AdoptRequest{IP: "10.0.0.140", DeviceSecret: "handover-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if waitJob(t, s, job).Phase != "adopted" {
		t.Fatal("handover with the device secret must adopt")
	}
}

func TestAdoptReportsDeniedAndDoesNotRecord(t *testing.T) {
	store := newFakeStore()
	adopter := &fakeAdopter{verify: "someone-elses-secret"}
	s := newTestService(store, discovery.Static{}, adopter)
	job, _ := s.Adopt(context.Background(), "gggg", AdoptRequest{IP: "10.0.0.141"})
	done := waitJob(t, s, job)
	if done.Phase != "failed" || done.Error != "auth" || done.DeviceSecret != "" {
		t.Fatalf("job %+v", done)
	}
	if _, err := store.Get(context.Background(), "gggg"); !errors.Is(err, ErrNotFound) {
		t.Fatal("a denied adoption must not create the device")
	}
}

func TestAdoptProgressPhasesReachTheJob(t *testing.T) {
	store := newFakeStore()
	adopter := &fakeAdopter{phases: []adoption.Phase{adoption.PhaseHello, adoption.PhaseWaitingConsent}, fail: adoption.ErrConsentTimeout}
	s := newTestService(store, discovery.Static{}, adopter)
	job, _ := s.Adopt(context.Background(), "hhhh", AdoptRequest{IP: "10.0.0.142"})
	done := waitJob(t, s, job)
	if done.Phase != "failed" || done.Error != "consent_timeout" {
		t.Fatalf("job %+v", done)
	}
}

func TestForgetTellsTheDeviceThenDropsIt(t *testing.T) {
	store := newFakeStore(Device{ID: "aaaa", Adopted: true})
	lan := discovery.Static{On: true, Items: []discovery.Seen{{ID: "aaaa", IP: "10.0.0.125", SeenAt: now}}}
	adopter := &fakeAdopter{}
	s := newTestService(store, lan, adopter)
	job, err := s.Forget(context.Background(), "aaaa", AdoptRequest{})
	if err != nil {
		t.Fatal(err)
	}
	done := waitJob(t, s, job)
	if done.Phase != "forgotten" || done.Error != "" {
		t.Fatalf("job %+v", done)
	}
	if !strings.Contains(adopter.lastCfg, `"tokenServerUrl":""`) || !strings.Contains(adopter.lastCfg, `"orgSecret":""`) {
		t.Fatalf("forget config %s", adopter.lastCfg)
	}
	if len(store.forgot) != 1 {
		t.Fatal("inventory must drop the device")
	}
}

func TestForgetUnreachableDeviceStillCleansTheInventory(t *testing.T) {
	store := newFakeStore(Device{ID: "aaaa", Adopted: true})
	s := newTestService(store, discovery.Static{}, &fakeAdopter{})
	job, _ := s.Forget(context.Background(), "aaaa", AdoptRequest{})
	done := waitJob(t, s, job)
	if done.Phase != "forgotten" || done.Error != "device_unreachable:no_address" {
		t.Fatalf("job %+v", done)
	}
}

// ── desired config + secrets ────────────────────────────────────────────────

func TestDesiredConfigVersionIsContentAddressed(t *testing.T) {
	store := newFakeStore(Device{ID: "aaaa"})
	s := newTestService(store, discovery.Static{}, &fakeAdopter{})
	v1, err := s.SetDesiredConfig(context.Background(), "aaaa", map[string]any{"mode": "full_duplex", "audio": map[string]any{"fullDuplex": true}})
	if err != nil {
		t.Fatal(err)
	}
	v2, _ := s.SetDesiredConfig(context.Background(), "aaaa", map[string]any{"audio": map[string]any{"fullDuplex": true}, "mode": "full_duplex", "configVersion": "ignored"})
	if v1 != v2 || len(v1) != 12 {
		t.Fatalf("same document must yield the same version: %s vs %s", v1, v2)
	}
	v3, _ := s.SetDesiredConfig(context.Background(), "aaaa", map[string]any{"mode": "half_duplex"})
	if v3 == v1 {
		t.Fatal("a different document must yield a different version")
	}
}

func TestDeviceConfigAuthenticatesAndCarriesTheVersion(t *testing.T) {
	store := newFakeStore(Device{ID: "aaaa"})
	s := newTestService(store, discovery.Static{}, &fakeAdopter{})
	_ = store.MarkAdopted(context.Background(), "aaaa", DigestSecret("device-secret"))
	if _, err := s.DeviceConfig(context.Background(), "aaaa", "wrong"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
	if _, err := s.DeviceConfig(context.Background(), "aaaa", "device-secret"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound without a desired config, got %v", err)
	}
	version, _ := s.SetDesiredConfig(context.Background(), "aaaa", map[string]any{"mode": "half_duplex"})
	raw, err := s.DeviceConfig(context.Background(), "aaaa", "device-secret")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	if doc["configVersion"] != version || doc["schema"] != "sebastian.config.v1" || doc["mode"] != "half_duplex" {
		t.Fatalf("document %s", raw)
	}
}

func TestRegenerateSecretTravelsInTheConfigUntilConfirmed(t *testing.T) {
	store := newFakeStore(Device{ID: "aaaa"})
	s := newTestService(store, discovery.Static{}, &fakeAdopter{})
	_ = store.MarkAdopted(context.Background(), "aaaa", DigestSecret("old"))
	s.newSecret = func() string { return "new-secret-new-secret-new-secret-1234" }

	secret, err := s.RegenerateSecret(context.Background(), "aaaa")
	if err != nil || secret != "new-secret-new-secret-new-secret-1234" {
		t.Fatalf("secret %q err %v", secret, err)
	}
	raw, err := s.DeviceConfig(context.Background(), "aaaa", "old")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"deviceSecret":"new-secret-new-secret-new-secret-1234"`) {
		t.Fatalf("the pending secret must travel in the config: %s", raw)
	}
	// The device now authenticates with the new one: promoted, old one dead.
	if _, err := s.DeviceConfig(context.Background(), "aaaa", secret); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeviceConfig(context.Background(), "aaaa", "old"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("the old secret must stop working once the new one is confirmed")
	}
	raw, _ = s.DeviceConfig(context.Background(), "aaaa", secret)
	if strings.Contains(string(raw), "new-secret-new-secret") {
		t.Fatal("a confirmed secret must not keep travelling in the config")
	}
}
