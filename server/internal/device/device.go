// Package device owns the device inventory, the desired-state reconciliation
// (firmware profile + config) and the fleet view: what this control room knows
// joined with what mDNS sees on the LAN, and the adoption of units over the
// network (docs/implementation/11-fleet-adoption-control-room.md).
//
// The "device profile" here is the firmware-side personality (NVS profile:
// agent / usb_mic) — a concept distinct from agent_profiles, which configure
// the cloud LiveKit agent.
package device

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/zetesis-labs/sebastian/server/internal/adoption"
	"github.com/zetesis-labs/sebastian/server/internal/discovery"
)

var (
	ErrNotFound    = errors.New("device: not found")
	ErrNoAddress   = errors.New("device: no address — not seen on the LAN and no ip given")
	ErrNoOrgSecret = errors.New("device: this control room has no organization secret configured")
	ErrJobNotFound = errors.New("device: adoption job not found")
)

// State is what a control room shows for a unit (functional spec §3.2).
type State string

const (
	StateAdopted          State = "adopted"
	StateJoining          State = "joining"
	StateAbsent           State = "absent"
	StateMoved            State = "moved"
	StateLeaving          State = "leaving"
	StateManagedElsewhere State = "managed_elsewhere"
	StateUnadopted        State = "unadopted"
	StateOrphan           State = "orphan"
	StateRegistered       State = "registered"
)

// absentAfter: bound here, silent for more than three poll periods (30 s each).
const absentAfter = 90 * time.Second

// joiningFor: after an adoption the unit reboots and polls within ~40 s; until
// then (and at most this long) it is "joining", not "absent".
const joiningFor = 3 * time.Minute

type Device struct {
	ID                string
	DisplayName       string
	Enabled           bool
	DesiredProfile    string
	ReportedProfile   string
	ProfileReportedAt time.Time // zero value = the device never polled
	Adopted           bool      // holds a device secret issued by this control room
	AdoptedAt         time.Time
	ReportedFirmware  string
	ReportedConfig    string // config version the device runs (from its poll)
	DesiredConfig     string // version of the stored desired config

	// Derived / from the LAN announce.
	State       State
	IP          string
	Firmware    string
	ControlRoom string
	LastError   string
	SeenOnLanAt time.Time
}

type Session struct {
	ID             uuid.UUID
	Room           string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	RecordingCount int64
}

type Detail struct {
	Device
	HasDeviceSecret bool
	DesiredConfig   json.RawMessage // nil when none
	Sessions        []Session
}

// Poll is what the firmware reports on every reconciliation poll.
type Poll struct {
	ID            string
	Reported      string // running profile
	ConfigVersion string
	Firmware      string
}

type PollResult struct {
	DesiredProfile       string
	DesiredConfigVersion string
}

type Store interface {
	// TouchPoll upserts the device (auto-registration on first contact),
	// records what it reports running, and returns the desired state.
	TouchPoll(ctx context.Context, poll Poll) (PollResult, error)
	List(ctx context.Context) ([]Device, error)
	Get(ctx context.Context, id string) (Device, error)
	Sessions(ctx context.Context, id string) ([]Session, error)
	SetDesiredProfile(ctx context.Context, id, name string) error
	DesiredConfig(ctx context.Context, id string) (json.RawMessage, string, error)
	SetDesiredConfig(ctx context.Context, id string, config json.RawMessage, version string) error
	ClearDesiredConfig(ctx context.Context, id string) error
	// CredentialDigests returns the current and the pending (regenerated, not
	// yet confirmed) secret digests; nil when unset.
	CredentialDigests(ctx context.Context, id string) (current, pending []byte, err error)
	// MarkAdopted registers or updates the unit as adopted by this control room
	// with the given secret digest (and a default agent profile so it can open
	// sessions).
	MarkAdopted(ctx context.Context, id string, digest []byte) error
	SetPendingSecret(ctx context.Context, id string, digest []byte) error
	Forget(ctx context.Context, id string) error
	Rename(ctx context.Context, id, name string) error
}

// Adopter is the network side (adoption.Client); swapped in tests.
type Adopter interface {
	Adopt(ctx context.Context, ip, cfg, secret string, progress func(adoption.Phase)) error
}

// ControlRoom is this server's identity as seen by devices and the installer.
type ControlRoom struct {
	Name       string
	APIURL     string // what devices use: origin of their tokenServerUrl
	SyslogIP   string
	SyslogPort int
	OrgSecret  string
	AdoptPort  int
}

func (c ControlRoom) origin() string { return strings.TrimRight(strings.TrimSpace(c.APIURL), "/") }

// Job is an adoption or forget run, polled by the dashboard.
type Job struct {
	ID           uuid.UUID
	DeviceID     string
	Kind         string // adopt | forget
	IP           string
	Phase        string // starting | waiting_consent | queued | adopted | forgotten | failed
	Error        string
	DeviceSecret string // adopt only, shown once
	StartedAt    time.Time
	UpdatedAt    time.Time
}

const jobTTL = 15 * time.Minute

type Service struct {
	store     Store
	lan       discovery.Browser
	adopter   Adopter
	room      ControlRoom
	logger    *slog.Logger
	now       func() time.Time
	newSecret func() string
	newNonce  func() string

	mu             sync.Mutex
	jobs           map[uuid.UUID]*Job
	pendingSecrets map[string]string // device id → plaintext until the device confirms
	enrolls        map[string]enrollChallenge
	forgotten      map[string]time.Time // recently forgotten: their stale announce is "leaving", not "registered"
}

func NewService(store Store, lan discovery.Browser, adopter Adopter, room ControlRoom, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:          store,
		lan:            lan,
		adopter:        adopter,
		room:           room,
		logger:         logger,
		now:            time.Now,
		newSecret:      randomSecret,
		newNonce:       randomSecret,
		jobs:           map[uuid.UUID]*Job{},
		pendingSecrets: map[string]string{},
		enrolls:        map[string]enrollChallenge{},
		forgotten:      map[string]time.Time{},
	}
}

func randomSecret() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// DigestSecret is the stored form of a device secret (same as session.DigestSecret).
func DigestSecret(secret string) []byte {
	d := sha256.Sum256([]byte(secret))
	return d[:]
}

func (s *Service) ControlRoom() ControlRoom { return s.room }
func (s *Service) DiscoveryEnabled() bool   { return s.lan != nil && s.lan.Enabled() }
func (s *Service) Reconcile(ctx context.Context, poll Poll) (PollResult, error) {
	return s.store.TouchPoll(ctx, poll)
}

func (s *Service) view(rows []Device) []Device {
	var seen []discovery.Seen
	if s.lan != nil {
		seen = s.lan.Snapshot()
	}
	now := s.now()
	s.mu.Lock()
	leaving := map[string]bool{}
	for id, at := range s.forgotten {
		if now.Sub(at) > joiningFor {
			delete(s.forgotten, id)
			continue
		}
		leaving[id] = true
	}
	s.mu.Unlock()
	return fleetView(rows, seen, s.room.origin(), leaving, now)
}

func (s *Service) List(ctx context.Context) ([]Device, error) {
	rows, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	return s.view(rows), nil
}

func (s *Service) Get(ctx context.Context, id string) (Detail, error) {
	row, err := s.store.Get(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	merged := s.view([]Device{row})
	for _, d := range merged {
		if d.ID == id {
			row = d
			break
		}
	}
	cfg, _, err := s.store.DesiredConfig(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	sessions, err := s.store.Sessions(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	return Detail{Device: row, HasDeviceSecret: row.Adopted, DesiredConfig: cfg, Sessions: sessions}, nil
}

func (s *Service) SetDesiredProfile(ctx context.Context, id, name string) error {
	return s.store.SetDesiredProfile(ctx, id, name)
}

func (s *Service) Rename(ctx context.Context, id, name string) error {
	return s.store.Rename(ctx, id, name)
}

// ── desired config ──────────────────────────────────────────────────────────

func (s *Service) SetDesiredConfig(ctx context.Context, id string, config map[string]any) (string, error) {
	version, canonical, err := ConfigVersion(config)
	if err != nil {
		return "", err
	}
	if err := s.store.SetDesiredConfig(ctx, id, canonical, version); err != nil {
		return "", err
	}
	return version, nil
}

func (s *Service) ClearDesiredConfig(ctx context.Context, id string) error {
	return s.store.ClearDesiredConfig(ctx, id)
}

// DeviceConfig is the device-facing read: authenticates with the current or
// the pending secret, and returns the desired document with its version (and
// the pending secret, so a regenerated secret reaches the unit).
func (s *Service) DeviceConfig(ctx context.Context, id, secret string) (json.RawMessage, error) {
	if err := s.authenticate(ctx, id, secret); err != nil {
		return nil, err
	}
	raw, version, err := s.store.DesiredConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if raw == nil {
		doc = map[string]any{"schema": "sebastian.config.v1"}
		version = ""
	} else if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("desired config for %s is not an object: %w", id, err)
	}
	s.mu.Lock()
	pending, hasPending := s.pendingSecrets[id]
	s.mu.Unlock()
	if raw == nil && !hasPending {
		return nil, ErrNotFound
	}
	if hasPending {
		ad, _ := doc["adoption"].(map[string]any)
		if ad == nil {
			ad = map[string]any{}
		}
		ad["deviceSecret"] = pending
		doc["adoption"] = ad
	}
	doc["configVersion"] = version
	return json.Marshal(doc)
}

var ErrUnauthorized = errors.New("device: invalid credentials")

func (s *Service) authenticate(ctx context.Context, id, secret string) error {
	current, pending, err := s.store.CredentialDigests(ctx, id)
	if err != nil {
		return err
	}
	digest := DigestSecret(secret)
	if len(current) > 0 && string(current) == string(digest) {
		return nil
	}
	if len(pending) > 0 && string(pending) == string(digest) {
		// First contact with the regenerated secret: it becomes the secret.
		if err := s.store.MarkAdopted(ctx, id, digest); err != nil {
			return err
		}
		s.mu.Lock()
		delete(s.pendingSecrets, id)
		s.mu.Unlock()
		return nil
	}
	return ErrUnauthorized
}

// RegenerateSecret issues a new per-device secret. It reaches the device
// inside its next desired config; the old one works until then.
func (s *Service) RegenerateSecret(ctx context.Context, id string) (string, error) {
	if _, err := s.store.Get(ctx, id); err != nil {
		return "", err
	}
	secret := s.newSecret()
	if err := s.store.SetPendingSecret(ctx, id, DigestSecret(secret)); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.pendingSecrets[id] = secret
	s.mu.Unlock()
	// Bump the desired config version so the device fetches the document that
	// carries the new secret even if nothing else changed.
	raw, _, err := s.store.DesiredConfig(ctx, id)
	if err != nil {
		return "", err
	}
	doc := map[string]any{"schema": "sebastian.config.v1"}
	if raw != nil {
		_ = json.Unmarshal(raw, &doc)
	}
	doc["secretRotation"] = s.now().UTC().Format(time.RFC3339Nano)
	if _, err := s.SetDesiredConfig(ctx, id, doc); err != nil {
		return "", err
	}
	return secret, nil
}

// ── enrolment ───────────────────────────────────────────────────────────────

// EnrollChallenge hands a device the nonce it must sign with the organization
// secret. One outstanding challenge per device; stale ones are pruned here.
func (s *Service) EnrollChallenge(id string) (string, error) {
	if s.room.OrgSecret == "" {
		return "", ErrNoOrgSecret
	}
	nonce := s.newNonce()
	now := s.now()
	s.mu.Lock()
	for k, ch := range s.enrolls {
		if now.Sub(ch.Issued) > enrollTTL {
			delete(s.enrolls, k)
		}
	}
	s.enrolls[id] = enrollChallenge{Nonce: nonce, Issued: now}
	s.mu.Unlock()
	return nonce, nil
}

// Enroll verifies the proof and, like a network adoption, binds the unit to
// this control room with a fresh device secret. The nonce is single-use.
func (s *Service) Enroll(ctx context.Context, id, nonce, mac string) (string, error) {
	if s.room.OrgSecret == "" {
		return "", ErrNoOrgSecret
	}
	s.mu.Lock()
	ch := s.enrolls[id]
	delete(s.enrolls, id)
	s.mu.Unlock()
	if !enrollValid(ch, nonce, mac, s.room.OrgSecret, id, s.now()) {
		s.logger.Warn("enrolment denied", "device_id", id)
		return "", ErrUnauthorized
	}
	secret := s.newSecret()
	if err := s.store.MarkAdopted(ctx, id, DigestSecret(secret)); err != nil {
		return "", err
	}
	s.mu.Lock()
	delete(s.pendingSecrets, id)
	s.mu.Unlock()
	s.logger.Info("device enrolled", "device_id", id)
	return secret, nil
}

// ── adoption ────────────────────────────────────────────────────────────────

type AdoptRequest struct {
	IP           string
	DeviceSecret string         // sign with this instead of the organization secret
	Config       map[string]any // operator overrides merged over the control room's config
}

func (s *Service) addressFor(id, ip string) (string, error) {
	if ip != "" {
		if !discovery.Reachable(ip) {
			return "", fmt.Errorf("%w: %q is not an IP address", ErrNoAddress, ip)
		}
		return ip, nil
	}
	if s.lan != nil {
		for _, sn := range s.lan.Snapshot() {
			if sn.ID == id && sn.IP != "" {
				return sn.IP, nil
			}
		}
	}
	return "", ErrNoAddress
}

// Adopt starts an adoption job and returns immediately; poll Job for progress.
func (s *Service) Adopt(ctx context.Context, id string, req AdoptRequest) (Job, error) {
	if s.room.OrgSecret == "" && req.DeviceSecret == "" {
		return Job{}, ErrNoOrgSecret
	}
	if s.room.origin() == "" {
		return Job{}, errors.New("device: SEBASTIAN_PUBLIC_API_URL is not configured — devices could not reach this control room")
	}
	ip, err := s.addressFor(id, req.IP)
	if err != nil {
		return Job{}, err
	}
	deviceSecret := s.newSecret()
	cfg, err := adoptionConfig(s.room, deviceSecret, req.Config)
	if err != nil {
		return Job{}, err
	}
	signWith := s.room.OrgSecret
	if req.DeviceSecret != "" {
		signWith = req.DeviceSecret
	}
	job := s.startJob(id, "adopt", ip)
	go s.runAdopt(context.WithoutCancel(ctx), job, ip, cfg, signWith, deviceSecret)
	return *job, nil
}

func (s *Service) runAdopt(ctx context.Context, job *Job, ip, cfg, signWith, deviceSecret string) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	err := s.adopter.Adopt(ctx, ip, cfg, signWith, func(p adoption.Phase) {
		s.updateJob(job, func(j *Job) { j.Phase = phaseOf(p) })
	})
	if err != nil {
		s.logger.Warn("adoption failed", "device_id", job.DeviceID, "ip", ip, "error", err)
		s.updateJob(job, func(j *Job) { j.Phase = "failed"; j.Error = errorCode(err) })
		return
	}
	if err := s.store.MarkAdopted(ctx, job.DeviceID, DigestSecret(deviceSecret)); err != nil {
		s.logger.Error("device adopted but not recorded", "device_id", job.DeviceID, "error", err)
		s.updateJob(job, func(j *Job) { j.Phase = "failed"; j.Error = "store" })
		return
	}
	s.logger.Info("device adopted", "device_id", job.DeviceID, "ip", ip)
	s.updateJob(job, func(j *Job) { j.Phase = "adopted"; j.DeviceSecret = deviceSecret })
}

// Forget returns the unit to factory over the network, then drops it from the
// inventory (history kept). The device is told with the organization secret or
// its own device secret; if it cannot be reached the inventory is still cleaned
// (the operator asked for that), and the job says so.
func (s *Service) Forget(ctx context.Context, id string, req AdoptRequest) (Job, error) {
	if _, err := s.store.Get(ctx, id); err != nil {
		return Job{}, err
	}
	ip, _ := s.addressFor(id, req.IP)
	job := s.startJob(id, "forget", ip)
	signWith := s.room.OrgSecret
	if req.DeviceSecret != "" {
		signWith = req.DeviceSecret
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
		defer cancel()
		var netErr error
		if ip == "" {
			netErr = ErrNoAddress
		} else {
			netErr = s.adopter.Adopt(ctx, ip, forgetConfig(), signWith, func(p adoption.Phase) {
				s.updateJob(job, func(j *Job) { j.Phase = phaseOf(p) })
			})
		}
		if err := s.store.Forget(ctx, id); err != nil {
			s.updateJob(job, func(j *Job) { j.Phase = "failed"; j.Error = "store" })
			return
		}
		s.mu.Lock()
		delete(s.pendingSecrets, id)
		s.forgotten[id] = s.now()
		s.mu.Unlock()
		s.updateJob(job, func(j *Job) {
			j.Phase = "forgotten"
			if netErr != nil {
				j.Error = "device_unreachable:" + errorCode(netErr)
			}
		})
	}()
	return *job, nil
}
