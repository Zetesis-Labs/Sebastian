package meeting

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/zetesis-labs/sebastian/server/internal/device"
)

// Store persists meetings. Update carries the domain event the transition
// produced ("" = none) so the store can leave it in the outbox atomically.
type Store interface {
	Insert(ctx context.Context, m Meeting) error
	Update(ctx context.Context, m Meeting, event string) error
	Get(ctx context.Context, id uuid.UUID) (Meeting, error)
	// Active is the meeting still owning the unit's mic, if any (RM-05).
	Active(ctx context.Context, deviceID string) (Meeting, bool, error)
	InProgress(ctx context.Context) ([]Meeting, error)
	List(ctx context.Context, f Filter) ([]Meeting, error)
	// Drop removes a request the unit never confirmed: nothing to keep.
	Drop(ctx context.Context, id uuid.UUID) error
	// Delete keeps the row as a trace (RM-46) but clears its content.
	Delete(ctx context.Context, id uuid.UUID, at time.Time) error
}

type Filter struct {
	DeviceID string
	State    State
	Limit    int
}

// Units is what we need to know about a speaker: the device service.
type Units interface {
	Get(ctx context.Context, id string) (device.Detail, error)
	Authenticate(ctx context.Context, id, secret string) error
	Address(id string) string // LAN address, "" when not seen
	ControlRoom() device.ControlRoom
}

// Commander delivers a signed order over the LAN (adoption.Client).
type Commander interface {
	Command(ctx context.Context, ip, cmd string, meetingID uuid.UUID, secret string) error
}

var (
	ErrNotFound     = errors.New("meeting: not found")
	ErrNotAdopted   = errors.New("meeting: the unit is not adopted here")
	ErrProfile      = errors.New("meeting: the unit is not in the agente profile") // RM-44/54
	ErrAbsent       = errors.New("meeting: the unit is not contacting")            // RM-03
	ErrBusy         = errors.New("meeting: a meeting is already in progress")      // RM-05
	ErrOffset       = errors.New("meeting: audio offset does not match the file")
	ErrNotOpen      = errors.New("meeting: not accepting audio")
	ErrUnauthorized = device.ErrUnauthorized
)

const (
	CmdStart = "record-start"
	CmdStop  = "record-stop"
	CmdWarn  = "record-warn"
	// agenteProfile is the only profile that records (spec §3.4).
	agenteProfile = "agente"
	// audioFlushEvery bounds how often a streaming upload touches the DB.
	audioFlushEvery = 5 * time.Second
	copyChunk       = 64 << 10
)

type Service struct {
	store  Store
	units  Units
	cmd    Commander
	dir    string
	limits Limits
	logger *slog.Logger
	now    func() time.Time
	// OnTranscribe is block D's hook; nil until then.
	OnTranscribe func(Meeting)

	mu   sync.Mutex
	open map[uuid.UUID]struct{} // meetings with an upload in flight
}

func NewService(store Store, units Units, cmd Commander, dir string, limits Limits, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: store, units: units, cmd: cmd, dir: dir, limits: limits, logger: logger, now: time.Now, open: map[uuid.UUID]struct{}{}}
}

// Start asks the unit to record (RM-01/03/05/06/44).
func (s *Service) Start(ctx context.Context, deviceID string, origin Origin) (Meeting, error) {
	detail, err := s.units.Get(ctx, deviceID)
	if err != nil {
		return Meeting{}, err
	}
	if !detail.Adopted {
		return Meeting{}, ErrNotAdopted
	}
	if detail.ReportedProfile != agenteProfile {
		return Meeting{}, ErrProfile
	}
	if detail.State != device.StateAdopted {
		return Meeting{}, ErrAbsent
	}
	if _, busy, err := s.store.Active(ctx, deviceID); err != nil {
		return Meeting{}, err
	} else if busy {
		return Meeting{}, ErrBusy
	}
	now := s.now().UTC()
	m := Meeting{ID: uuid.Must(uuid.NewV7()), DeviceID: deviceID, State: StateRequested, RequestedBy: origin, RequestedAt: now, CreatedAt: now, UpdatedAt: now}
	if err := s.store.Insert(ctx, m); err != nil {
		return Meeting{}, err
	}
	// A gesture start comes from the unit itself: it starts with the id we
	// answer; there is nothing to order.
	if origin != OriginGesture {
		s.deliver(ctx, m, CmdStart)
	}
	return m, nil
}

// StartFromUnit is the gesture path (RM-02): the unit authenticates and asks.
func (s *Service) StartFromUnit(ctx context.Context, deviceID, secret string) (Meeting, error) {
	if err := s.units.Authenticate(ctx, deviceID, secret); err != nil {
		return Meeting{}, err
	}
	return s.Start(ctx, deviceID, OriginGesture)
}

// Stop ends a meeting (RM-20…22); the reason records who asked.
func (s *Service) Stop(ctx context.Context, id uuid.UUID, reason EndReason) (Meeting, error) {
	m, err := s.store.Get(ctx, id)
	if err != nil {
		return Meeting{}, err
	}
	if !InProgress(m.State) {
		return Meeting{}, ErrNotRecording
	}
	return s.apply(ctx, m, Event{Kind: EvStop, At: s.now().UTC(), Reason: reason})
}

// Warn tells the unit the silence net is about to fire (RM-13/23): the agent
// calls it ~30 s before its stop. Best effort over the LAN; a warning the
// poll would carry later is worthless, so it is never kept pending.
func (s *Service) Warn(ctx context.Context, id uuid.UUID) error {
	m, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if m.State != StateRecording {
		return ErrNotRecording
	}
	s.deliver(ctx, m, CmdWarn)
	return nil
}

// Report is the unit's confirmation (RM-52): "recording" or "stopped" + why.
func (s *Service) Report(ctx context.Context, deviceID, secret string, id uuid.UUID, state string, reason EndReason) error {
	if err := s.units.Authenticate(ctx, deviceID, secret); err != nil {
		return err
	}
	m, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if m.DeviceID != deviceID {
		return ErrNotFound
	}
	now := s.now().UTC()
	switch state {
	case "recording":
		_, err = s.apply(ctx, m, Event{Kind: EvDeviceRecording, At: now})
	case "stopped":
		if !InProgress(m.State) {
			return nil
		}
		if reason == "" {
			reason = EndGesture
		}
		_, err = s.apply(ctx, m, Event{Kind: EvStop, At: now, Reason: reason})
	default:
		return ErrInvalid
	}
	return err
}

// PendingCommand is the order the unit must still hear, carried by the poll
// header when the LAN command could not reach it (RM-06).
func (s *Service) PendingCommand(ctx context.Context, deviceID string) string {
	m, ok, err := s.store.Active(ctx, deviceID)
	if err != nil || !ok {
		return ""
	}
	return CommandFor(m)
}

// Audio appends the agent's stream to the meeting's file as it arrives
// (RM-15). offset must equal what the file already holds, so an interrupted
// upload resumes without gaps (design §3.3). A clean end of stream closes the
// recording; an aborted one leaves it to the audio clock (RM-25).
func (s *Service) Audio(ctx context.Context, id uuid.UUID, offset int64, r io.Reader) (int64, error) {
	m, err := s.store.Get(ctx, id)
	if err != nil {
		return 0, err
	}
	if !InProgress(m.State) {
		return m.AudioBytes, ErrNotOpen
	}
	s.mu.Lock()
	if _, busy := s.open[id]; busy {
		s.mu.Unlock()
		return m.AudioBytes, ErrNotOpen
	}
	s.open[id] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.open, id)
		s.mu.Unlock()
	}()

	path := s.path(id)
	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return m.AudioBytes, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return m.AudioBytes, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return m.AudioBytes, err
	}
	if offset != info.Size() {
		return info.Size(), ErrOffset
	}
	total := info.Size()
	buf := make([]byte, copyChunk)
	lastFlush := s.now()
	// The upload outlives stops and ticks: every event applies to the meeting
	// as it is now, never to the copy read when the stream opened.
	flush := func(kind EventKind, at time.Time) error {
		cur, err := s.store.Get(ctx, id)
		if err != nil {
			return err
		}
		if cur.AudioPath == "" {
			cur.AudioPath = filepath.Base(path)
		}
		_, err = s.apply(ctx, cur, Event{Kind: kind, At: at, Bytes: total})
		return err
	}
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				return total, err
			}
			total += int64(n)
			if now := s.now(); now.Sub(lastFlush) >= audioFlushEvery {
				if err := flush(EvAudio, now.UTC()); err != nil {
					return total, err
				}
				lastFlush = now
			}
		}
		if readErr == io.EOF {
			if total == info.Size() {
				// An empty body is a probe or a client's blind retry, never a close.
				return total, nil
			}
			if err := flush(EvAudio, s.now().UTC()); err != nil {
				return total, err
			}
			return total, flush(EvAudioClosed, s.now().UTC())
		}
		if readErr != nil {
			// The connection dropped: keep what we have; the audio clock decides.
			if err := flush(EvAudio, s.now().UTC()); err != nil {
				return total, err
			}
			return total, readErr
		}
	}
}

// Tick runs the time-based safety nets over every meeting in progress.
func (s *Service) Tick(ctx context.Context) {
	items, err := s.store.InProgress(ctx)
	if err != nil {
		s.logger.WarnContext(ctx, "meetings tick: list failed", "error", err)
		return
	}
	now := s.now().UTC()
	for _, m := range items {
		if _, err := s.apply(ctx, m, Event{Kind: EvTick, At: now}); err != nil {
			s.logger.WarnContext(ctx, "meetings tick failed", "meeting", m.ID, "error", err)
		}
	}
}

// Run ticks every `every` until ctx ends.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Tick(ctx)
		}
	}
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (Meeting, error) {
	return s.store.Get(ctx, id)
}

func (s *Service) List(ctx context.Context, f Filter) ([]Meeting, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	return s.store.List(ctx, f)
}

// AudioFile is the stored audio, for the admin download (RM-41).
func (s *Service) AudioFile(ctx context.Context, id uuid.UUID) (string, error) {
	m, err := s.store.Get(ctx, id)
	if err != nil {
		return "", err
	}
	if m.AudioPath == "" || !m.DeletedAt.IsZero() {
		return "", ErrNotFound
	}
	return s.path(id), nil
}

// Delete removes audio and content irreversibly, keeping the trace (RM-46).
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	m, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if InProgress(m.State) {
		return ErrBusy
	}
	if err := os.Remove(s.path(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return s.store.Delete(ctx, id, s.now().UTC())
}

// Retranscribe queues the transcription again (RM-33).
func (s *Service) Retranscribe(ctx context.Context, id uuid.UUID) (Meeting, error) {
	m, err := s.store.Get(ctx, id)
	if err != nil {
		return Meeting{}, err
	}
	return s.apply(ctx, m, Event{Kind: EvTranscribe, At: s.now().UTC()})
}

// Transcribed / TranscriptFailed close block D's loop.
func (s *Service) Transcribed(ctx context.Context, m Meeting) (Meeting, error) {
	return s.apply(ctx, m, Event{Kind: EvTranscribed, At: s.now().UTC()})
}

func (s *Service) TranscriptFailed(ctx context.Context, m Meeting) (Meeting, error) {
	return s.apply(ctx, m, Event{Kind: EvTranscriptFail, At: s.now().UTC()})
}

func (s *Service) path(id uuid.UUID) string { return filepath.Join(s.dir, id.String()+".ogg") }

// apply is the one place the core meets the world: persist, then act.
func (s *Service) apply(ctx context.Context, m Meeting, ev Event) (Meeting, error) {
	before := m.State
	next, actions, err := Next(m, ev, s.limits)
	if err != nil {
		return m, err
	}
	next.UpdatedAt = ev.At
	if slices.Contains(actions, ActDrop) {
		return next, s.store.Drop(ctx, next.ID)
	}
	if err := s.store.Update(ctx, next, transitionEvent(before, next.State)); err != nil {
		return m, err
	}
	for _, a := range actions {
		switch a {
		case ActCommandStop:
			s.deliver(ctx, next, CmdStop)
		case ActEnqueueTranscribe:
			if s.OnTranscribe != nil {
				s.OnTranscribe(next)
			}
		}
	}
	return next, nil
}

// transitionEvent names the domain event a state change produces (design §2).
func transitionEvent(from, to State) string {
	if from == to {
		return ""
	}
	switch to {
	case StateRecording:
		return "meeting.started"
	case StateClosing, StateCut:
		return "meeting.ended"
	case StateReady:
		return "meeting.transcribed"
	case StateNoTranscript:
		return "meeting.transcript_failed"
	}
	return ""
}

// deliver sends the order over the LAN, signed with the organization secret
// (RM-07). If it cannot (no address, no secret, no reply) the poll header
// carries it on the unit's next contact (RM-06): CommandFor keeps it pending.
func (s *Service) deliver(ctx context.Context, m Meeting, cmd string) {
	ip := s.units.Address(m.DeviceID)
	secret := s.units.ControlRoom().OrgSecret
	if ip == "" || secret == "" || s.cmd == nil {
		s.logger.InfoContext(ctx, "meeting order deferred to the poll", "meeting", m.ID, "cmd", cmd, "device", m.DeviceID, "lan", ip != "")
		return
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	go func() {
		defer cancel()
		if err := s.cmd.Command(cctx, ip, cmd, m.ID, secret); err != nil {
			s.logger.WarnContext(cctx, "meeting order not delivered over the LAN — the poll will carry it", "meeting", m.ID, "cmd", cmd, "error", err)
		}
	}()
}

// Live is the duration shown while recording (RM-14).
func Live(m Meeting, now time.Time) time.Duration {
	if m.State == StateRecording || m.State == StateClosing {
		return now.Sub(m.StartedAt)
	}
	return time.Duration(m.DurationMs) * time.Millisecond
}

func (s *Service) String() string { return fmt.Sprintf("meetings(dir=%s)", s.dir) }
