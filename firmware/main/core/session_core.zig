//! Pure session timing: when to keep a session alive and when to close it.
//!
//! This is the logic that caused the field bug — with the AEC cancelling the
//! loudspeaker echo the mic reads "silence" while the agent talks, so the agent's
//! own speech MUST count as activity (the keepalive) or the silence timeout fires
//! mid-reply. Extracted from app.zig with no I/O so the caller feeds it observed
//! events (voice / agent-audio) and reads the decisions — and so it is testable
//! on the host with a simulated tick clock.

pub const TICK_MS: u32 = 10;
pub const MIN_ACTIVE_TICKS: u32 = 20 * 1000 / TICK_MS; // don't close in the first 20s
// Watchdog, not the primary close: the agent ends idle conversations first
// (data-channel "close", agent/endpointing.py — it sees transcribed turns and
// context, not amplitude). This only fires when the agent is dead or mute.
pub const SILENCE_TICKS: u32 = 60 * 1000 / TICK_MS;
pub const MAX_TICKS: u32 = 600 * 1000 / TICK_MS; // 10min safety cap
pub const AGENT_AUDIO_HANGOVER_TICKS: u32 = 2500 / TICK_MS; // inter-sentence gaps + echo tail

pub const Timing = struct {
    tick: u32 = 0,
    last_voice_tick: u32 = 0,
    last_agent_audio_tick: u32 = 0,
    agent_audio_seen: bool = false,

    /// Mark this tick as active (user voice OR agent keepalive).
    pub fn markVoiceActivity(self: *Timing) void {
        self.last_voice_tick = self.tick;
    }

    /// Record that the agent produced audio this tick (feeds the keepalive).
    pub fn noteAgentAudio(self: *Timing) void {
        self.last_agent_audio_tick = self.tick;
        self.agent_audio_seen = true;
    }

    /// True while the agent is speaking or within the hangover just after it.
    pub fn agentAudioActive(self: Timing) bool {
        return self.agent_audio_seen and
            self.tick - self.last_agent_audio_tick < AGENT_AUDIO_HANGOVER_TICKS;
    }

    /// The session has been quiet long enough to close (and past the min-active
    /// window). Because agent audio calls markVoiceActivity, this only fires at a
    /// real end of conversation, not while the agent is still talking.
    pub fn silenceExpired(self: Timing) bool {
        return self.tick >= MIN_ACTIVE_TICKS and
            self.tick - self.last_voice_tick >= SILENCE_TICKS;
    }

    pub fn maxDurationReached(self: Timing) bool {
        return self.tick >= MAX_TICKS;
    }
};
