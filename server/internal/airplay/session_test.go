package airplay

import (
	"testing"
	"time"
)

const (
	testQuiet = 15 * time.Second
	testRetry = 10 * time.Second
)

var t0 = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return t0.Add(d) }

func newTestSession() *session { return &session{quiet: testQuiet, retry: testRetry} }

func playingSession() *session {
	s := newTestSession()
	s.audio(t0)
	s.opened(true, t0)
	return s
}

func TestFirstAudioOpensOnce(t *testing.T) {
	s := newTestSession()
	if got := s.audio(t0); got != actionOpen {
		t.Fatalf("first audio = %v, want open", got)
	}
	if got := s.audio(at(time.Second)); got != actionNone {
		t.Fatalf("audio while opening = %v, want none", got)
	}
	if s.publishing() {
		t.Fatal("publishing before the room is open")
	}
}

func TestPublishesOnceOpen(t *testing.T) {
	s := playingSession()
	if !s.publishing() {
		t.Fatal("not publishing with the room open")
	}
	if got := s.audio(at(time.Second)); got != actionNone {
		t.Fatalf("audio while playing = %v, want none", got)
	}
}

func TestQuietEndsThePlayback(t *testing.T) {
	s := playingSession()
	if got := s.tick(at(testQuiet - time.Second)); got != actionNone {
		t.Fatalf("a short pause = %v, want none", got)
	}
	if got := s.tick(at(testQuiet)); got != actionStop {
		t.Fatalf("end of the stream = %v, want stop", got)
	}
	if got := s.audio(at(testQuiet + time.Second)); got != actionOpen {
		t.Fatalf("a new stream = %v, want open", got)
	}
}

func TestStreamOverWhileOpeningStillStops(t *testing.T) {
	s := newTestSession()
	s.audio(t0)
	s.opened(true, at(testQuiet))
	if got := s.tick(at(testQuiet)); got != actionStop {
		t.Fatalf("got %v, want stop: the playback was left on with nothing to play", got)
	}
}

func TestStoppedElsewhereStaysSilentUntilTheStreamEnds(t *testing.T) {
	s := playingSession()
	if got := s.stoppedElsewhere(); got != actionLeave {
		t.Fatalf("stopped elsewhere = %v, want leave", got)
	}
	if s.publishing() {
		t.Fatal("publishing after someone stopped the playback")
	}
	for d := time.Second; d < testQuiet; d += time.Second {
		if got := s.audio(at(d)); got != actionNone {
			t.Fatalf("audio at %s = %v, want none: it would undo the stop", d, got)
		}
	}
	last := at(testQuiet - time.Second)
	if got := s.tick(last.Add(testQuiet - time.Second)); got != actionNone {
		t.Fatalf("a short pause = %v, want none", got)
	}
	if got := s.tick(last.Add(testQuiet)); got != actionNone {
		t.Fatalf("end of the stream = %v, want none: the playback is already over", got)
	}
	if got := s.audio(last.Add(testQuiet + time.Second)); got != actionOpen {
		t.Fatalf("a new stream = %v, want open", got)
	}
}

func TestFailedOpenRetriesWhileTheStreamLasts(t *testing.T) {
	s := newTestSession()
	s.audio(t0)
	s.opened(false, t0)
	if s.publishing() {
		t.Fatal("publishing without a room")
	}
	if got := s.audio(at(testRetry - time.Second)); got != actionNone {
		t.Fatalf("audio before the retry = %v, want none", got)
	}
	if got := s.audio(at(testRetry)); got != actionOpen {
		t.Fatalf("audio at the retry = %v, want open", got)
	}
}

func TestLostRoomReopens(t *testing.T) {
	s := playingSession()
	if got := s.lost(at(time.Second)); got != actionLeave {
		t.Fatalf("lost = %v, want leave", got)
	}
	if got := s.audio(at(2 * time.Second)); got != actionNone {
		t.Fatalf("audio right after = %v, want none", got)
	}
	if got := s.audio(at(time.Second + testRetry)); got != actionOpen {
		t.Fatalf("audio after the wait = %v, want open", got)
	}
}

func TestLostRoomThenStreamOverEndsThePlayback(t *testing.T) {
	s := playingSession()
	s.lost(at(time.Second))
	if got := s.tick(at(testQuiet - time.Second)); got != actionNone {
		t.Fatalf("while the stream may go on = %v, want none", got)
	}
	if got := s.tick(at(testQuiet)); got != actionStop {
		t.Fatalf("stream over = %v, want stop: the unit would sit in an empty room", got)
	}
	if got := s.tick(at(2 * testQuiet)); got != actionNone {
		t.Fatalf("a second tick = %v, want none: it is settled", got)
	}
}

func TestReopeningSettlesALostRoom(t *testing.T) {
	s := playingSession()
	s.lost(at(time.Second))
	s.audio(at(testRetry + time.Second))
	s.opened(false, at(testRetry+time.Second))
	if got := s.tick(at(testRetry + time.Second + testQuiet)); got != actionNone {
		t.Fatalf("got %v, want none: the failed open already ended it", got)
	}
}

func TestEventsOutOfPhaseDoNothing(t *testing.T) {
	s := newTestSession()
	if got := s.stoppedElsewhere(); got != actionNone {
		t.Fatalf("stoppedElsewhere when idle = %v", got)
	}
	if got := s.lost(t0); got != actionNone {
		t.Fatalf("lost when idle = %v", got)
	}
	if got := s.tick(t0); got != actionNone {
		t.Fatalf("tick when idle = %v", got)
	}
	s.opened(true, t0)
	if s.publishing() {
		t.Fatal("a late open result started publishing")
	}
}

func TestShutdown(t *testing.T) {
	s := playingSession()
	if got := s.shutdown(); got != actionStop {
		t.Fatalf("shutdown while playing = %v, want stop", got)
	}
	s = newTestSession()
	s.audio(t0)
	if got := s.shutdown(); got != actionStop {
		t.Fatalf("shutdown while opening = %v, want stop", got)
	}
	s = newTestSession()
	if got := s.shutdown(); got != actionNone {
		t.Fatalf("shutdown when idle = %v, want none", got)
	}
}
