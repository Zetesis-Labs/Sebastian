// Package airplay publishes what an AirPlay source sends into the speaker's
// playback room. A shairport-sync child receives AirPlay and outputs PCM; the
// bridge opens the playback when audio appears, publishes it, and ends the
// playback when the audio stops. One process serves one speaker.
package airplay

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

const (
	sampleRate    = 48000
	channels      = 2
	frameDuration = 20 * time.Millisecond
	frameSamples  = sampleRate / 50 * channels // interleaved

	// quietAfter is how long without audio ends the stream. A pause shorter
	// than this keeps the room, so resuming does not lose the first seconds.
	quietAfter = 15 * time.Second
	retryAfter = 10 * time.Second
	// checkEvery is how often a playing bridge asks whether its playback is
	// still on: the server does not tell a source that someone stopped it.
	checkEvery = 5 * time.Second
	callLimit  = 10 * time.Second
)

// Control is the part of the control room the bridge needs.
type Control interface {
	Start(ctx context.Context) (Started, error)
	Active(ctx context.Context) (string, error)
	Stop(ctx context.Context) error
}

// Room is a joined playback room to publish into.
type Room interface {
	Write(pcm []int16) error
	Close()
}

// Join enters a playback's room; lost is called if the connection gives up.
type Join func(ctx context.Context, started Started, lost func()) (Room, error)

type Bridge struct {
	control Control
	join    Join
	logger  *slog.Logger
	now     func() time.Time
	ticks   <-chan time.Time

	session    session
	room       Room
	playbackID string
	joins      uint64 // tells a late goodbye from an old room apart from the current one
	checking   bool
	lastCheck  time.Time
	writeError bool
}

func NewBridge(control Control, join Join, logger *slog.Logger) *Bridge {
	return &Bridge{
		control: control,
		join:    join,
		logger:  logger,
		now:     time.Now,
		session: session{quiet: quietAfter, retry: retryAfter},
	}
}

type opening struct {
	started Started
	room    Room
	err     error
}

type check struct {
	playbackID string
	activeID   string
	err        error
}

// Run serves frames until ctx ends or the receiver stops.
func (b *Bridge) Run(ctx context.Context, frames <-chan []int16) error {
	ticks := b.ticks
	if ticks == nil {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		ticks = ticker.C
	}

	// A previous run may have died with a playback on: the speaker would sit
	// in an empty room. This instance is the speaker's only AirPlay source.
	b.stopPlayback()

	opened := make(chan opening, 1)
	checked := make(chan check, 1)
	lost := make(chan uint64, 1)

	for {
		select {
		case <-ctx.Done():
			b.apply(b.session.shutdown())
			return ctx.Err()

		case frame, ok := <-frames:
			if !ok {
				b.apply(b.session.shutdown())
				return errors.New("the AirPlay receiver stopped")
			}
			if b.session.audio(b.now()) == actionOpen {
				b.logger.Info("AirPlay audio: opening the playback")
				b.joins++
				go b.open(ctx, b.joins, opened, lost)
			}
			if b.session.publishing() {
				b.write(frame)
			}

		case res := <-opened:
			b.session.opened(res.err == nil, b.now())
			if res.err != nil {
				b.logger.Error("could not open the playback", "error", res.err)
				continue
			}
			b.room, b.playbackID = res.room, res.started.ID
			b.lastCheck = b.now()
			b.logger.Info("playback on", "playback", res.started.ID, "delivery", res.started.Delivery)

		case <-ticks:
			now := b.now()
			if action := b.session.tick(now); action != actionNone {
				b.logger.Info("AirPlay audio ended", "playback", b.playbackID)
				b.apply(action)
			}
			if b.session.publishing() && !b.checking && now.Sub(b.lastCheck) >= checkEvery {
				b.checking, b.lastCheck = true, now
				go b.check(ctx, b.playbackID, checked)
			}

		case res := <-checked:
			b.checking = false
			switch {
			case res.err != nil:
				// The control room may be restarting; a playback it lost
				// shows up as gone once it answers.
				b.logger.Warn("could not check the playback", "error", res.err)
			case res.playbackID == b.playbackID && res.activeID != res.playbackID:
				b.logger.Info("playback stopped elsewhere: silent until the AirPlay audio ends", "playback", res.playbackID)
				b.apply(b.session.stoppedElsewhere())
			}

		case join := <-lost:
			if join == b.joins && b.session.publishing() {
				b.logger.Warn("lost the room", "playback", b.playbackID)
				b.apply(b.session.lost(b.now()))
			}
		}
	}
}

func (b *Bridge) open(ctx context.Context, join uint64, done chan<- opening, lost chan<- uint64) {
	callCtx, cancel := context.WithTimeout(ctx, callLimit)
	defer cancel()
	started, err := b.control.Start(callCtx)
	if err != nil {
		done <- opening{err: err}
		return
	}
	room, err := b.join(callCtx, started, func() {
		select {
		case lost <- join:
		default:
		}
	})
	if err != nil {
		// Nobody would feed it, and the stream may end before the next try.
		b.stopPlayback()
	}
	done <- opening{started: started, room: room, err: err}
}

func (b *Bridge) check(ctx context.Context, playbackID string, done chan<- check) {
	callCtx, cancel := context.WithTimeout(ctx, callLimit)
	defer cancel()
	activeID, err := b.control.Active(callCtx)
	done <- check{playbackID: playbackID, activeID: activeID, err: err}
}

func (b *Bridge) write(frame []int16) {
	err := b.room.Write(frame)
	if err != nil && !b.writeError {
		b.logger.Error("could not publish audio", "error", err)
	}
	b.writeError = err != nil
}

func (b *Bridge) apply(action action) {
	switch action {
	case actionLeave:
		b.leaveRoom()
	case actionStop:
		b.leaveRoom()
		b.stopPlayback()
	}
}

func (b *Bridge) leaveRoom() {
	if b.room != nil {
		b.room.Close()
		b.room = nil
	}
	b.playbackID = ""
	b.writeError = false
}

func (b *Bridge) stopPlayback() {
	// Not the run's context: this also runs on the way out.
	ctx, cancel := context.WithTimeout(context.Background(), callLimit)
	defer cancel()
	if err := b.control.Stop(ctx); err != nil {
		b.logger.Warn("could not stop the playback", "error", err)
	}
}
