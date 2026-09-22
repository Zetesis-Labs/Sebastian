package meeting

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var t0 = time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)

func requested() Meeting {
	return Meeting{ID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), DeviceID: "68ee", State: StateRequested, RequestedBy: OriginDashboard, RequestedAt: t0}
}

func recording() Meeting {
	m, _, _ := Next(requested(), Event{Kind: EvDeviceRecording, At: t0.Add(2 * time.Second)}, Limits{})
	return m
}

func actions(a []Action) string {
	s := make([]string, len(a))
	for i, x := range a {
		s[i] = string(x)
	}
	return strings.Join(s, ",")
}

// T-A1: the state table of spec §3.3 with the safety nets of RM-05/23/24/25.
func TestStateTable(t *testing.T) {
	limits := Limits{MaxDuration: 3 * time.Hour}
	cases := []struct {
		name    string
		from    Meeting
		ev      Event
		state   State
		actions string
		reason  EndReason
		err     error
	}{
		{"requested: the unit confirms → recording", requested(), Event{Kind: EvDeviceRecording, At: t0.Add(2 * time.Second)}, StateRecording, "", "", nil},
		{"requested: audio arrives first → recording anyway", requested(), Event{Kind: EvAudio, At: t0.Add(2 * time.Second), Bytes: 4096}, StateRecording, "", "", nil},
		{"requested: cancelled → dropped", requested(), Event{Kind: EvStop, At: t0.Add(2 * time.Second), Reason: EndDashboard}, StateRequested, "drop", "", nil},
		{"requested: 29 s without confirmation → still waiting", requested(), Event{Kind: EvTick, At: t0.Add(29 * time.Second)}, StateRequested, "", "", nil},
		{"requested: 30 s without confirmation → dropped (RM-03)", requested(), Event{Kind: EvTick, At: t0.Add(30 * time.Second)}, StateRequested, "drop", "", nil},
		{"recording: stop from the dashboard → closing, tell the unit (RM-22)", recording(), Event{Kind: EvStop, At: t0.Add(time.Minute), Reason: EndDashboard}, StateClosing, "command_stop", EndDashboard, nil},
		{"recording: stop by voice → closing, tell the unit (RM-21)", recording(), Event{Kind: EvStop, At: t0.Add(time.Minute), Reason: EndVoice}, StateClosing, "command_stop", EndVoice, nil},
		{"recording: stop by gesture → closing, the unit already stopped (RM-20)", recording(), Event{Kind: EvStop, At: t0.Add(time.Minute), Reason: EndGesture}, StateClosing, "", EndGesture, nil},
		{"recording: silence reported → closing, tell the unit (RM-23)", recording(), Event{Kind: EvStop, At: t0.Add(time.Minute), Reason: EndSilence}, StateClosing, "command_stop", EndSilence, nil},
		{"recording: the unit lost its control room → closing, nothing to order (RM-26)", recording(), Event{Kind: EvStop, At: t0.Add(time.Minute), Reason: EndRoomLost}, StateClosing, "", EndRoomLost, nil},
		{"recording: audio keeps it alive", recording(), Event{Kind: EvAudio, At: t0.Add(20 * time.Second), Bytes: 100}, StateRecording, "", "", nil},
		{"recording: 30 s without audio → cut (RM-25)", recording(), Event{Kind: EvTick, At: t0.Add(2*time.Second + AudioSilenceWindow)}, StateCut, "command_stop,close_file,enqueue_transcribe", EndDeviceLost, nil},
		{"recording: the agent hangs up → cut, and the unit still gets the stop (RM-25)", recording(), Event{Kind: EvAudioClosed, At: t0.Add(time.Minute)}, StateCut, "command_stop,close_file,enqueue_transcribe", EndDeviceLost, nil},
		{"recording: max duration → closing by limit (RM-24)", recording(), Event{Kind: EvTick, At: t0.Add(2*time.Second + 3*time.Hour)}, StateClosing, "command_stop", EndMaxDuration, nil},
		{"recording: repeated confirmation is harmless", recording(), Event{Kind: EvDeviceRecording, At: t0.Add(3 * time.Second)}, StateRecording, "", "", nil},
		{"recording: transcribe makes no sense", recording(), Event{Kind: EvTranscribe, At: t0}, StateRecording, "", "", ErrInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, acts, err := Next(c.from, c.ev, limits)
			if !errors.Is(err, c.err) {
				t.Fatalf("err = %v, want %v", err, c.err)
			}
			if got.State != c.state || actions(acts) != c.actions || got.EndReason != c.reason {
				t.Fatalf("got state=%s actions=%q reason=%q, want %s %q %q", got.State, actions(acts), got.EndReason, c.state, c.actions, c.reason)
			}
		})
	}
}

func TestRecordingKeepsTheAudioClockAndTheDuration(t *testing.T) {
	m := recording()
	m, _, _ = Next(m, Event{Kind: EvAudio, At: t0.Add(40 * time.Second), Bytes: 12345}, Limits{})
	if m.AudioBytes != 12345 || !m.LastAudioAt.Equal(t0.Add(40*time.Second)) {
		t.Fatalf("audio not accounted: %+v", m)
	}
	// 30 s after the last audio, not after the start, is what cuts (RM-25).
	m2, acts, _ := Next(m, Event{Kind: EvTick, At: t0.Add(69 * time.Second)}, Limits{})
	if m2.State != StateRecording || len(acts) != 0 {
		t.Fatalf("cut too early: %s %v", m2.State, acts)
	}
	m3, _, _ := Next(m, Event{Kind: EvStop, At: t0.Add(62 * time.Second), Reason: EndVoice}, Limits{})
	if m3.DurationMs != 60_000 {
		t.Fatalf("duration = %d ms, want 60000 (from the confirmation at +2 s)", m3.DurationMs)
	}
}

func TestClosingWaitsForTheFileThenTranscribes(t *testing.T) {
	m, _, _ := Next(recording(), Event{Kind: EvStop, At: t0.Add(time.Minute), Reason: EndDashboard}, Limits{})
	late, _, _ := Next(m, Event{Kind: EvAudio, At: t0.Add(61 * time.Second), Bytes: 999}, Limits{})
	if late.State != StateClosing || late.AudioBytes != 999 {
		t.Fatalf("late audio while closing must still count: %+v", late)
	}
	closed, acts, _ := Next(late, Event{Kind: EvAudioClosed, At: t0.Add(62 * time.Second)}, Limits{})
	if closed.State != StateTranscribing || actions(acts) != "close_file,enqueue_transcribe" {
		t.Fatalf("closing+audio_closed = %s %v", closed.State, acts)
	}
	stuck, acts, _ := Next(m, Event{Kind: EvTick, At: t0.Add(time.Minute + CloseWindow)}, Limits{})
	if stuck.State != StateTranscribing || actions(acts) != "close_file,enqueue_transcribe" {
		t.Fatalf("closing must not wait forever for the agent: %s %v", stuck.State, acts)
	}
}

func TestTranscriptionOutcomesAndRetry(t *testing.T) {
	m := Meeting{State: StateTranscribing}
	ready, _, _ := Next(m, Event{Kind: EvTranscribed}, Limits{})
	failed, _, _ := Next(m, Event{Kind: EvTranscriptFail}, Limits{})
	if ready.State != StateReady || failed.State != StateNoTranscript {
		t.Fatalf("outcomes: %s %s", ready.State, failed.State)
	}
	retry, acts, err := Next(failed, Event{Kind: EvTranscribe}, Limits{})
	if err != nil || retry.State != StateTranscribing || actions(acts) != "enqueue_transcribe" {
		t.Fatalf("retry (RM-33): %s %v %v", retry.State, acts, err)
	}
	cut := Meeting{State: StateCut}
	if again, _, err := Next(cut, Event{Kind: EvTranscribe}, Limits{}); err != nil || again.State != StateTranscribing {
		t.Fatalf("a cut meeting is transcribed too (RM-25): %s %v", again.State, err)
	}
	if _, _, err := Next(ready, Event{Kind: EvStop, Reason: EndDashboard}, Limits{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("stopping a finished meeting must be invalid")
	}
}

// A failure in the agent (no ffmpeg, an upload the server rejects) closes the
// audio stream while the unit is still happily recording. If that does not
// order a stop, the unit records forever and answers "busy" to every later
// meeting — which is what wedged unit 68ee8f4d8dd4 on 2026-09-22.
func TestAgentHangupStillStopsTheUnit(t *testing.T) {
	cut, actions, err := Next(recording(), Event{Kind: EvAudioClosed, At: t0.Add(time.Minute)}, Limits{})
	if err != nil {
		t.Fatalf("EvAudioClosed must be accepted while recording: %v", err)
	}
	if cut.State != StateCut {
		t.Fatalf("an agent hangup cuts the meeting, got %q", cut.State)
	}
	if !slices.Contains(actions, ActCommandStop) {
		t.Fatalf("the unit must be told to stop, got %v", actions)
	}
}

func TestOneMeetingPerUnitAndTheCommandItNeeds(t *testing.T) {
	for _, s := range []State{StateRequested, StateRecording, StateClosing} {
		if !InProgress(s) {
			t.Fatalf("%s must count as in progress (RM-05)", s)
		}
	}
	for _, s := range []State{StateTranscribing, StateReady, StateNoTranscript, StateCut} {
		if InProgress(s) {
			t.Fatalf("%s must not block a new meeting", s)
		}
	}
	r := requested()
	if CommandFor(r) != "start:"+r.ID.String() {
		t.Fatalf("a requested meeting orders a start: %q", CommandFor(r))
	}
	byDashboard, _, _ := Next(recording(), Event{Kind: EvStop, At: t0.Add(time.Minute), Reason: EndDashboard}, Limits{})
	if CommandFor(byDashboard) != "stop:"+r.ID.String() {
		t.Fatalf("a stop from the dashboard must reach the unit: %q", CommandFor(byDashboard))
	}
	byGesture, _, _ := Next(recording(), Event{Kind: EvStop, At: t0.Add(time.Minute), Reason: EndGesture}, Limits{})
	if CommandFor(byGesture) != "" {
		t.Fatalf("the unit that stopped by gesture needs no order: %q", CommandFor(byGesture))
	}
	if CommandFor(recording()) != "" {
		t.Fatal("a recording meeting has nothing pending")
	}
	if StopReason(OriginGesture) != EndGesture || StopReason(OriginVoice) != EndVoice || StopReason(OriginDashboard) != EndDashboard {
		t.Fatal("stop reasons follow the origin")
	}
}
