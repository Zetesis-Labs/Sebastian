//! Pure mic gate state machine. Hardware code owns I2S; this owns decisions.

pub const SPEAK_HANGOVER_FRAMES: u32 = 20; // ~400ms at 20ms frames

pub const Gate = struct {
    muted: bool = false,
    full_duplex_active: bool = false,
    agent_speaking: bool = false,
    speak_hangover: u32 = 0,

    pub fn setMuted(self: *Gate, value: bool) void {
        self.muted = value;
    }

    pub fn setFullDuplex(self: *Gate, value: bool) void {
        self.full_duplex_active = value;
    }

    /// Returns true when this call starts a fresh speaking burst.
    pub fn setAgentSpeaking(self: *Gate, value: bool) bool {
        const fresh_burst = value and !self.agent_speaking;
        if (self.agent_speaking and !value) self.speak_hangover = SPEAK_HANGOVER_FRAMES;
        self.agent_speaking = value;
        return fresh_burst;
    }

    pub fn gatedByAgent(self: *Gate) bool {
        if (self.agent_speaking) return true;
        if (self.speak_hangover > 0) {
            self.speak_hangover -= 1;
            return true;
        }
        return false;
    }

    pub fn agentGateActive(self: *Gate) bool {
        return !self.full_duplex_active and self.gatedByAgent();
    }
};

pub fn channelLooksBad(peak: u32, mean: i32) bool {
    if (peak == 0) return true; // dead: not even room noise
    return @abs(mean) > 15000; // pinned: massive DC, audio crushed by softClip
}
