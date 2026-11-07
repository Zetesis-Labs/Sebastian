package airplay

import "time"

type phase int

const (
	phaseIdle phase = iota
	phaseOpening
	phasePlaying
	// phaseSilenced: someone stopped the playback (by voice, from the panel)
	// while the AirPlay source keeps sending. Opening again would undo the
	// stop, so it waits for the stream to end.
	phaseSilenced
)

type action int

const (
	actionNone  action = iota
	actionOpen         // start the playback and join its room
	actionLeave        // leave the room; the playback is already over
	actionStop         // end the playback and leave the room
)

// session decides what the bridge does about the speaker's playback, from
// what the receiver outputs and what the control room says.
type session struct {
	quiet time.Duration // silence that ends the stream
	retry time.Duration // wait before opening again after a failure

	phase     phase
	lastAudio time.Time
	retryAt   time.Time
	// owed: the room was lost with the playback still on, and only opening
	// again or ending it will settle that: the unit would sit in an empty room.
	owed bool
}

func (s *session) audio(now time.Time) action {
	s.lastAudio = now
	if s.phase == phaseIdle && !now.Before(s.retryAt) {
		s.phase = phaseOpening
		s.owed = false
		return actionOpen
	}
	return actionNone
}

func (s *session) opened(ok bool, now time.Time) {
	if s.phase != phaseOpening {
		return
	}
	if ok {
		s.phase = phasePlaying
		return
	}
	s.phase = phaseIdle
	s.retryAt = now.Add(s.retry)
}

func (s *session) tick(now time.Time) action {
	if now.Sub(s.lastAudio) < s.quiet {
		return actionNone
	}
	switch s.phase {
	case phasePlaying:
		s.phase = phaseIdle
		return actionStop
	case phaseSilenced:
		s.phase = phaseIdle
	case phaseIdle:
		if s.owed {
			s.owed = false
			return actionStop
		}
	}
	return actionNone
}

func (s *session) stoppedElsewhere() action {
	if s.phase != phasePlaying {
		return actionNone
	}
	s.phase = phaseSilenced
	return actionLeave
}

func (s *session) lost(now time.Time) action {
	if s.phase != phasePlaying {
		return actionNone
	}
	s.phase = phaseIdle
	s.retryAt = now.Add(s.retry)
	s.owed = true
	return actionLeave
}

// shutdown also ends what an open in flight may have started.
func (s *session) shutdown() action {
	if s.phase != phasePlaying && s.phase != phaseOpening {
		return actionNone
	}
	s.phase = phaseIdle
	return actionStop
}

func (s *session) publishing() bool { return s.phase == phasePlaying }
