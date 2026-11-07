package airplay

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeControl struct {
	mu       sync.Mutex
	starts   int
	stops    int
	active   string
	startErr error
}

func (f *fakeControl) Start(context.Context) (Started, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return Started{}, f.startErr
	}
	f.starts++
	f.active = "p1"
	return Started{ID: "p1", ServerURL: "ws://lk", SourceToken: "tok", Delivery: "lan"}, nil
}

func (f *fakeControl) Active(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active, nil
}

func (f *fakeControl) Stop(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	f.active = ""
	return nil
}

func (f *fakeControl) stopElsewhere() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active = ""
}

func (f *fakeControl) counts() (starts, stops int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, f.stops
}

type fakeRoom struct {
	writes atomic.Int64
	closed atomic.Bool
	lost   func()
}

func (r *fakeRoom) Write([]int16) error { r.writes.Add(1); return nil }
func (r *fakeRoom) Close()              { r.closed.Store(true) }

type rig struct {
	t       *testing.T
	control *fakeControl
	frames  chan []int16
	ticks   chan time.Time
	clock   atomic.Int64

	mu      sync.Mutex
	rooms   []*fakeRoom
	joinErr error

	cancel context.CancelFunc
	done   chan error
}

func startRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, control: &fakeControl{}, frames: make(chan []int16), ticks: make(chan time.Time), done: make(chan error, 1)}
	r.clock.Store(t0.UnixNano())
	join := func(_ context.Context, _ Started, lost func()) (Room, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.joinErr != nil {
			return nil, r.joinErr
		}
		room := &fakeRoom{lost: lost}
		r.rooms = append(r.rooms, room)
		return room, nil
	}
	b := NewBridge(r.control, join, slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.now = func() time.Time { return time.Unix(0, r.clock.Load()) }
	b.ticks = r.ticks
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	t.Cleanup(cancel)
	go func() { r.done <- b.Run(ctx, r.frames) }()
	r.eventually("the leftover stop at boot", func() bool { _, stops := r.control.counts(); return stops == 1 })
	return r
}

func (r *rig) eventually(what string, cond func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			r.t.Fatalf("never happened: %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// advance ticks first: a tick is only taken once the loop has finished with
// the last frame, which otherwise could read the moved clock as its arrival.
func (r *rig) advance(d time.Duration) { r.tick(); r.clock.Add(int64(d)) }
func (r *rig) frame()                  { r.frames <- make([]int16, frameSamples) }
func (r *rig) tick()                   { r.ticks <- time.Time{} }

func (r *rig) room(i int) *fakeRoom {
	r.t.Helper()
	var room *fakeRoom
	r.eventually("the room is joined", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		if len(r.rooms) <= i {
			return false
		}
		room = r.rooms[i]
		return true
	})
	return room
}

// playing feeds audio until the bridge publishes into room i.
func (r *rig) playing(i int) *fakeRoom {
	r.t.Helper()
	r.frame()
	room := r.room(i)
	r.eventually("publishing", func() bool { r.frame(); return room.writes.Load() > 0 })
	return room
}

func TestAudioOpensPublishesAndEnds(t *testing.T) {
	r := startRig(t)
	room := r.playing(0)
	if starts, _ := r.control.counts(); starts != 1 {
		t.Fatalf("starts = %d, want a single playback", starts)
	}

	r.advance(quietAfter)
	r.tick()
	r.eventually("the room is left and the playback ended", func() bool {
		_, stops := r.control.counts()
		return room.closed.Load() && stops == 2
	})

	r.advance(time.Second)
	r.playing(1)
	if starts, _ := r.control.counts(); starts != 2 {
		t.Fatalf("starts = %d, want a new playback for the new stream", starts)
	}
}

func TestStopElsewhereStaysSilentUntilTheStreamEnds(t *testing.T) {
	r := startRig(t)
	room := r.playing(0)

	r.control.stopElsewhere()
	r.advance(checkEvery)
	r.tick()
	r.eventually("the room is left", room.closed.Load)

	for range 5 {
		r.advance(time.Second)
		r.frame()
	}
	starts, stops := r.control.counts()
	if starts != 1 {
		t.Fatalf("starts = %d: the audio still flowing undid the stop", starts)
	}
	if stops != 1 {
		t.Fatalf("stops = %d: only the one at boot; the playback was over already", stops)
	}

	r.advance(quietAfter)
	r.tick()
	r.advance(time.Second)
	r.playing(1)
}

func TestLostRoomReopensTheSamePlayback(t *testing.T) {
	r := startRig(t)
	room := r.playing(0)

	room.lost()
	r.eventually("the lost room is left", room.closed.Load)
	r.advance(retryAfter)
	r.playing(1)
}

func TestLostFromARoomAlreadyLeftIsIgnored(t *testing.T) {
	r := startRig(t)
	first := r.playing(0)
	r.advance(quietAfter)
	r.tick()
	r.eventually("the room is left", first.closed.Load)
	r.advance(time.Second)
	second := r.playing(1)

	first.lost()
	r.frame()
	r.tick()
	r.frame()
	if second.closed.Load() {
		t.Fatal("the old room's goodbye closed the new one")
	}
}

func TestFailedJoinEndsThePlaybackItOpened(t *testing.T) {
	r := startRig(t)
	r.mu.Lock()
	r.joinErr = errors.New("no LiveKit")
	r.mu.Unlock()

	r.frame()
	r.eventually("the orphan playback is ended", func() bool { _, stops := r.control.counts(); return stops == 2 })
}

func TestShutdownEndsThePlayback(t *testing.T) {
	r := startRig(t)
	room := r.playing(0)

	r.cancel()
	if err := <-r.done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if _, stops := r.control.counts(); stops != 2 || !room.closed.Load() {
		t.Fatalf("stops = %d, closed = %v", stops, room.closed.Load())
	}
}

func TestReceiverEndEndsThePlaybackAndTheRun(t *testing.T) {
	r := startRig(t)
	room := r.playing(0)

	close(r.frames)
	if err := <-r.done; err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want the receiver's failure", err)
	}
	if _, stops := r.control.counts(); stops != 2 || !room.closed.Load() {
		t.Fatalf("stops = %d, closed = %v", stops, room.closed.Load())
	}
}
