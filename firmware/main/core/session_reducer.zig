//! Pure session-lifecycle reducer: the choreography of an active session as a
//! replayable state machine.
//!
//! Every field bug so far (premature close under AEC silence, the tick-0 close
//! race, the stale half-duplex gate) was a SEQUENCE bug — the arithmetic lived
//! in session_core.zig and was tested, but the wiring of observations into
//! decisions lived in app.zig's poll loop and was not. This module owns that
//! wiring: app.zig feeds it events (discrete ones from the LiveKit callbacks
//! via a FreeRTOS queue, plus a 10ms tick carrying the sampled levels) and
//! executes the returned actions against the C SDK. No I/O here, so a field
//! incident's event sequence pastes straight into core_test.zig.
//!
//! Deliberate deviations from the old poll loop, both ≤ one tick: a
//! data-channel "close" ends the session the moment the event is consumed
//! instead of at the next 10ms tick, and a handoff whose initial pre-roll send
//! fails on a tick ≡ 0 (mod 50) no longer gets the old accidental same-tick
//! retry (the first retry waits for the next cadence slot). Everything else —
//! observation order, close priority, retry cadence — is a 1:1 port.

const session_core = @import("session_core.zig");

/// Smoothed mic peak at/above this counts as the user speaking.
pub const VOICE_LEVEL: u32 = 3000;
/// Render peak (>>8) over the auto_clear zero floor = agent speaking.
pub const AGENT_AUDIO_LEVEL: u32 = 1000;
pub const AEC_LOG_PERIOD_TICKS: u32 = 500;
pub const AEC_LOG_OFFSET_TICKS: u32 = 250;
pub const PREROLL_RETRY_TICKS: u32 = 50; // 500ms between pre-roll send retries
pub const PREROLL_MAX_ATTEMPTS: u32 = 8; // ~4s of retries before giving up

/// livekit_room_get_state classified by the shell each tick. The connection is
/// still POLLED (not event-driven): the vendored SDK's on_state_changed has
/// missed transitions before (#186 family), the poll is the source of truth.
pub const Connection = enum { connecting, connected, ended };

/// sebastian.agent_state data channel, classified by the shell. Any payload
/// that is not one of the special strings maps to .quiet (matches the old
/// callback: only exactly "speaking" raised the half-duplex gate).
pub const AgentState = enum { speaking, quiet, interrupted, close };

pub const CloseReason = enum { disconnected, agent_close, silence, max_duration };

/// Observations the shell samples at each 10ms tick. These stay polled rather
/// than event-driven on purpose: mic level and render peak are produced on hot
/// audio paths (atomics with fetchMax, sampled here), and the barge flag is a
/// once-per-tick swap.
pub const Tick = struct {
    connection: Connection,
    mic_level: u32 = 0,
    render_peak: u32 = 0,
    barge_request: bool = false,
};

pub const Event = union(enum) {
    tick: Tick,
    agent_state: AgentState,
    /// Agent participant became active/inactive (on_participant_info).
    agent_active: bool,
    /// Result of the pre-roll send the shell just executed (initial or retry).
    preroll_sent: bool,
};

pub const AecLog = struct {
    echo_window: u32,
    render_peak_window: u32,
    keepalive: bool,
};

/// What the shell must do after a step. Field order is not execution order —
/// the shell applies gate/flush first (latency-sensitive), then the rest.
pub const Actions = struct {
    /// First CONNECTED tick: xvf_ui.setState(.active).
    ui_active: bool = false,
    /// Room connected AND agent active: stop wakeword, mic_src.setLive(true),
    /// send the pre-roll, then feed back .preroll_sent with the result.
    handoff: bool = false,
    /// Re-send the pre-roll, then feed back .preroll_sent with the result.
    retry_preroll: bool = false,
    /// Wake word heard over agent speech: publish sebastian.barge_in.
    publish_barge: bool = false,
    /// mic_src.setAgentSpeaking — the half-duplex gate.
    mic_gate: ?bool = null,
    /// Agent aborted mid-speech: dump the queued reply (av_render_flush).
    flush_render: bool = false,
    /// Periodic AEC/echo diagnostics window.
    log_aec: ?AecLog = null,
};

pub const Preroll = enum {
    /// Not attempted yet, or already delivered — nothing pending either way.
    idle,
    /// A send failed; retry on the PREROLL_RETRY_TICKS cadence.
    pending,
    /// PREROLL_MAX_ATTEMPTS retries failed; stop trying this session.
    given_up,
};

pub const Phase = union(enum) {
    /// Room created, waiting for the first CONNECTED tick.
    connecting,
    /// CONNECTED; waiting for the agent participant before the mic handoff.
    waiting_agent,
    /// Mic handed off; the conversation is running.
    live,
    /// Terminal. The shell tears the session down; further events are ignored.
    done: CloseReason,
};

pub const State = struct {
    phase: Phase = .connecting,
    timing: session_core.Timing = .{},
    agent_ready: bool = false,
    agent_speaking: bool = false,
    preroll: Preroll = .idle,
    preroll_attempts: u32 = 0,
    // Diagnostics accumulated between AEC log windows.
    render_peak_window: u32 = 0,
    echo_window: u32 = 0,
    last_level: u32 = 0,

    pub fn step(self: *State, event: Event) Actions {
        if (self.phase == .done) return .{};
        switch (event) {
            .tick => |t| return self.stepTick(t),
            .agent_state => |s| return self.stepAgentState(s),
            .agent_active => |active| {
                self.agent_ready = active;
                return .{};
            },
            .preroll_sent => |ok| {
                self.notePrerollResult(ok);
                return .{};
            },
        }
    }

    fn stepAgentState(self: *State, s: AgentState) Actions {
        switch (s) {
            .speaking, .quiet => {
                self.agent_speaking = (s == .speaking);
                return .{ .mic_gate = self.agent_speaking };
            },
            .interrupted => {
                // Not user activity: the silence timer keeps running from the
                // agent's last audible tick, exactly like the old callback.
                self.agent_speaking = false;
                return .{ .mic_gate = false, .flush_render = true };
            },
            .close => {
                // Server-side endpointing. The disconnect must stay
                // device-initiated — a server-side room delete can leave this
                // client stuck CONNECTED forever (#186 family).
                self.phase = .{ .done = .agent_close };
                return .{};
            },
        }
    }

    fn stepTick(self: *State, t: Tick) Actions {
        var out = Actions{};

        // Observation order is load-bearing (ported 1:1): the render-peak
        // keepalive reads last_level for the echo window, so mic level goes
        // first; the data-channel keepalive marks activity while the agent
        // holds the speaking state.
        self.last_level = t.mic_level;
        if (t.mic_level >= VOICE_LEVEL) self.timing.markVoiceActivity();
        if (self.agent_speaking) self.timing.markVoiceActivity();
        self.render_peak_window = @max(self.render_peak_window, t.render_peak);
        if (t.render_peak >= AGENT_AUDIO_LEVEL) self.timing.noteAgentAudio();
        if (self.timing.agentAudioActive()) {
            self.timing.markVoiceActivity();
            self.echo_window = @max(self.echo_window, self.last_level);
        }

        if (t.barge_request) {
            // mic_src already dropped its own gate at detection; no mic_gate
            // action here (1:1 with the old publishBargeRequest).
            self.agent_speaking = false;
            self.timing.markVoiceActivity(); // the user is about to talk
            out.publish_barge = true;
        }

        if (self.timing.tick % AEC_LOG_PERIOD_TICKS == AEC_LOG_OFFSET_TICKS) {
            out.log_aec = .{
                .echo_window = self.echo_window,
                .render_peak_window = self.render_peak_window,
                .keepalive = self.timing.agentAudioActive(),
            };
            self.render_peak_window = 0;
            self.echo_window = 0;
        }

        // Phase transitions gate on THIS tick's connection snapshot, so a
        // handoff can never fire against a connection that already dropped.
        if (self.phase == .connecting and t.connection == .connected) {
            self.phase = .waiting_agent;
            out.ui_active = true;
        }
        if (self.phase == .waiting_agent and t.connection == .connected and self.agent_ready) {
            self.phase = .live;
            out.handoff = true;
        }
        if (self.phase == .live and self.preroll == .pending and
            self.timing.tick % PREROLL_RETRY_TICKS == 0)
        {
            self.preroll_attempts += 1;
            out.retry_preroll = true;
        }

        // Close decisions, in the original priority order. Timing.tick is only
        // advanced on ticks that DON'T close, so the shell's close logs read
        // the same tick the decision was made on.
        if (t.connection == .ended) {
            self.phase = .{ .done = .disconnected };
        } else if (self.timing.silenceExpired()) {
            self.phase = .{ .done = .silence };
        } else if (self.timing.maxDurationReached()) {
            self.phase = .{ .done = .max_duration };
        } else {
            self.timing.tick += 1;
        }
        return out;
    }

    fn notePrerollResult(self: *State, ok: bool) void {
        if (ok) {
            self.preroll = .idle;
            return;
        }
        self.preroll = if (self.preroll_attempts >= PREROLL_MAX_ATTEMPTS)
            .given_up
        else
            .pending;
    }
};
