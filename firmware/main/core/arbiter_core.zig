//! USB↔agent microphone arbitration ("modo convivencia") — pure, host-tested.
//!
//! One I2S RX, sequential ownership: the agent (wake word / session) holds the
//! mic until the USB host actually captures from the UAC interface, then
//! yields; when the host stops, the agent takes it back. The signal is
//! usage-derived — the UAC component only invokes our input callback while the
//! host has the stream open, so "the PC is using the mic" needs no
//! configuration at all.
//!
//! Hysteresis, because hosts flap: macOS opens the stream just to draw the
//! input-level meter in Sound Settings, and apps re-open it around device
//! switches. Yield only after a sustained burst of callbacks; return only
//! after a comfortably long silence.

pub const TICK_MS: u32 = 100;
/// Sustained capture before the agent yields (3 ticks ≈ 300 ms).
pub const YIELD_AFTER_MS: u32 = 300;
/// Capture silence before the agent takes the mic back. Long on purpose: a
/// Teams call re-negotiating or a brief app switch must not bounce the mic.
pub const RESUME_AFTER_MS: u32 = 3000;

const YIELD_TICKS: u32 = YIELD_AFTER_MS / TICK_MS;
const RESUME_TICKS: u32 = RESUME_AFTER_MS / TICK_MS;

pub const Owner = enum { agent, usb };

pub const Transition = enum { grant_usb, revoke_usb };

pub const Arbiter = struct {
    owner: Owner = .agent,
    capture_ticks: u32 = 0,
    silence_ticks: u32 = 0,

    /// Feed one sample of "did the host read audio within the last tick?".
    /// Returns the transition to execute, if any.
    pub fn feed(self: *Arbiter, host_capturing: bool) ?Transition {
        switch (self.owner) {
            .agent => {
                if (!host_capturing) {
                    self.capture_ticks = 0;
                    return null;
                }
                self.capture_ticks += 1;
                if (self.capture_ticks < YIELD_TICKS) return null;
                self.owner = .usb;
                self.silence_ticks = 0;
                return .grant_usb;
            },
            .usb => {
                if (host_capturing) {
                    self.silence_ticks = 0;
                    return null;
                }
                self.silence_ticks += 1;
                if (self.silence_ticks < RESUME_TICKS) return null;
                self.owner = .agent;
                self.capture_ticks = 0;
                return .revoke_usb;
            },
        }
    }
};
