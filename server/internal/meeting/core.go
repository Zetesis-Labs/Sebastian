// Package meeting governs meeting recordings from the speaker
// (docs/implementation/13-meeting-recordings-functional-spec.md, design in
// 14-meeting-recordings-technical-design.md). This file is the functional
// core: the state machine of §3.3 and its safety nets (RM-05, RM-23…26) as a
// pure function; the shell (HTTP, files, the device command, timers) lives in
// meeting.go.
package meeting

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

type State string

const (
	StateRequested    State = "requested"     // asked; the unit has not confirmed yet
	StateRecording    State = "recording"     // the unit publishes audio and we store it
	StateClosing      State = "closing"       // stop asked; the file is being closed
	StateTranscribing State = "transcribing"  // audio stored; transcription in progress
	StateReady        State = "ready"         // audio + transcript
	StateNoTranscript State = "no_transcript" // audio only; retryable
	StateCut          State = "cut"           // the unit stopped sending audio
)

// Origin is who asked (RM-01) — for a start — and, for a stop, why (RM-20…26).
type Origin string

const (
	OriginGesture   Origin = "gesture"
	OriginDashboard Origin = "dashboard"
	OriginVoice     Origin = "voice"
)

type EndReason string

const (
	EndGesture     EndReason = "gesture"
	EndDashboard   EndReason = "dashboard"
	EndVoice       EndReason = "voice"
	EndSilence     EndReason = "silence"      // RM-23
	EndMaxDuration EndReason = "max_duration" // RM-24
	EndDeviceLost  EndReason = "device_lost"  // RM-25
	EndRoomLost    EndReason = "room_lost"    // RM-26
)

// Limits are the per-unit knobs of RM-23/24 and the fixed safety windows.
type Limits struct {
	MaxDuration time.Duration // RM-24
}

const (
	// ConfirmWindow is how long a request may wait for the unit (RM-03: 30 s).
	ConfirmWindow = 30 * time.Second
	// AudioSilenceWindow is how long recording survives without audio (RM-25).
	AudioSilenceWindow = 30 * time.Second
	// CloseWindow is how long closing waits for the file before moving on.
	CloseWindow = 30 * time.Second
)

type Meeting struct {
	ID          uuid.UUID
	DeviceID    string
	SessionID   *uuid.UUID
	State       State
	RequestedBy Origin
	RequestedAt time.Time
	StartedAt   time.Time
	EndedAt     time.Time
	EndReason   EndReason
	AudioPath   string
	AudioBytes  int64
	LastAudioAt time.Time
	DurationMs  int64
	Keep        bool
	DeletedAt   time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// EventKind is what can happen to a meeting.
type EventKind string

const (
	EvDeviceRecording EventKind = "device_recording" // the unit confirmed it records (RM-52)
	EvStop            EventKind = "stop"             // someone or something asks to stop (Reason says who)
	EvAudio           EventKind = "audio"            // bytes arrived from the agent (Bytes = total so far)
	EvAudioClosed     EventKind = "audio_closed"     // the agent closed the upload; the file is complete
	EvTranscribed     EventKind = "transcribed"
	EvTranscriptFail  EventKind = "transcript_failed"
	EvTranscribe      EventKind = "transcribe" // (re)try the transcription
	EvTick            EventKind = "tick"       // time passes; the safety nets fire here
)

type Event struct {
	Kind   EventKind
	At     time.Time
	Reason EndReason // for EvStop
	Bytes  int64     // for EvAudio
}

// Action is what the shell must do after a transition.
type Action string

const (
	ActCommandStop       Action = "command_stop"       // tell the unit to stop (LAN cmd or poll header)
	ActCloseFile         Action = "close_file"         // the audio file is complete or abandoned: finalize it
	ActEnqueueTranscribe Action = "enqueue_transcribe" // block D picks it up
	ActDrop              Action = "drop"               // a request the unit never confirmed: forget it
)

var (
	ErrNotRecording = errors.New("meeting: not recording")
	ErrInvalid      = errors.New("meeting: event not valid in this state")
)

// Next applies ev to m. It never touches the world: the returned actions do.
func Next(m Meeting, ev Event, limits Limits) (Meeting, []Action, error) {
	switch m.State {
	case StateRequested:
		return nextRequested(m, ev)
	case StateRecording:
		return nextRecording(m, ev, limits)
	case StateClosing:
		return nextClosing(m, ev)
	case StateTranscribing:
		return nextTranscribing(m, ev)
	case StateReady, StateNoTranscript, StateCut:
		return nextFinal(m, ev)
	}
	return m, nil, ErrInvalid
}

func nextRequested(m Meeting, ev Event) (Meeting, []Action, error) {
	switch ev.Kind {
	case EvDeviceRecording:
		m.State = StateRecording
		m.StartedAt = ev.At
		m.LastAudioAt = ev.At
		return m, nil, nil
	case EvStop:
		// Cancelled before the unit confirmed: nothing was recorded.
		return m, []Action{ActDrop}, nil
	case EvTick:
		if ev.At.Sub(m.RequestedAt) >= ConfirmWindow {
			return m, []Action{ActDrop}, nil
		}
		return m, nil, nil
	case EvAudio:
		// Audio before the confirmation: the unit is recording, count it as such.
		m.State = StateRecording
		m.StartedAt = ev.At
		m.LastAudioAt = ev.At
		m.AudioBytes = ev.Bytes
		return m, nil, nil
	}
	return m, nil, ErrInvalid
}

func nextRecording(m Meeting, ev Event, limits Limits) (Meeting, []Action, error) {
	switch ev.Kind {
	case EvAudio:
		m.AudioBytes = ev.Bytes
		m.LastAudioAt = ev.At
		return m, nil, nil
	case EvStop:
		m = stop(m, ev.At, ev.Reason)
		if unitAlreadyStopped(ev.Reason) {
			return m, nil, nil
		}
		return m, []Action{ActCommandStop}, nil
	case EvAudioClosed:
		// The agent hung up without a stop: the unit is gone (RM-25).
		m = stop(m, ev.At, EndDeviceLost)
		m.State = StateCut
		return m, []Action{ActCloseFile, ActEnqueueTranscribe}, nil
	case EvTick:
		if limits.MaxDuration > 0 && ev.At.Sub(m.StartedAt) >= limits.MaxDuration {
			m = stop(m, ev.At, EndMaxDuration)
			return m, []Action{ActCommandStop}, nil
		}
		if ev.At.Sub(m.LastAudioAt) >= AudioSilenceWindow {
			m = stop(m, ev.At, EndDeviceLost)
			m.State = StateCut
			return m, []Action{ActCommandStop, ActCloseFile, ActEnqueueTranscribe}, nil
		}
		return m, nil, nil
	case EvDeviceRecording:
		return m, nil, nil // a repeated confirmation
	}
	return m, nil, ErrInvalid
}

// unitAlreadyStopped: a stop the unit itself produced needs no order back.
func unitAlreadyStopped(reason EndReason) bool {
	return reason == EndGesture || reason == EndRoomLost || reason == EndDeviceLost
}

// stop closes the recording window; the actions depend on who stopped it.
func stop(m Meeting, at time.Time, reason EndReason) Meeting {
	m.State = StateClosing
	m.EndedAt = at
	m.EndReason = reason
	m.DurationMs = at.Sub(m.StartedAt).Milliseconds()
	return m
}

func nextClosing(m Meeting, ev Event) (Meeting, []Action, error) {
	switch ev.Kind {
	case EvAudio:
		m.AudioBytes = ev.Bytes
		m.LastAudioAt = ev.At
		return m, nil, nil
	case EvAudioClosed:
		m.State = StateTranscribing
		return m, []Action{ActCloseFile, ActEnqueueTranscribe}, nil
	case EvTick:
		if ev.At.Sub(m.EndedAt) >= CloseWindow {
			m.State = StateTranscribing
			return m, []Action{ActCloseFile, ActEnqueueTranscribe}, nil
		}
		return m, nil, nil
	case EvStop, EvDeviceRecording:
		return m, nil, nil // already stopping
	}
	return m, nil, ErrInvalid
}

func nextTranscribing(m Meeting, ev Event) (Meeting, []Action, error) {
	switch ev.Kind {
	case EvTranscribed:
		m.State = StateReady
		return m, nil, nil
	case EvTranscriptFail:
		m.State = StateNoTranscript
		return m, nil, nil
	case EvTick, EvAudioClosed, EvAudio:
		return m, nil, nil
	}
	return m, nil, ErrInvalid
}

func nextFinal(m Meeting, ev Event) (Meeting, []Action, error) {
	switch ev.Kind {
	case EvTranscribe:
		m.State = StateTranscribing
		return m, []Action{ActEnqueueTranscribe}, nil
	case EvTranscribed:
		m.State = StateReady
		return m, nil, nil
	case EvTranscriptFail:
		m.State = StateNoTranscript
		return m, nil, nil
	case EvTick:
		return m, nil, nil
	}
	return m, nil, ErrInvalid
}

// StopReason maps who asked for the stop to the reason recorded (RM-20…22).
func StopReason(origin Origin) EndReason {
	switch origin {
	case OriginGesture:
		return EndGesture
	case OriginVoice:
		return EndVoice
	}
	return EndDashboard
}

// InProgress is true while the meeting still owns the unit's mic (RM-05).
func InProgress(s State) bool {
	return s == StateRequested || s == StateRecording || s == StateClosing
}

// CommandFor is what the unit must be told for a meeting in this state, if
// anything: the pending order carried by the LAN command or the poll header.
func CommandFor(m Meeting) string {
	switch m.State {
	case StateRequested:
		return "start:" + m.ID.String()
	case StateClosing:
		if unitAlreadyStopped(m.EndReason) {
			return ""
		}
		return "stop:" + m.ID.String()
	}
	return ""
}
