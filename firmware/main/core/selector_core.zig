//! Boot profile selector — pure logic, host-tested.
//!
//! The only input on the enclosure is the mute button, and the XVF exposes it
//! as a LATCHED state (each press flips GPIO30), not as press events. Both
//! machines below therefore work on the sampled mute STATE and treat each
//! state CHANGE as one press:
//!
//!   Trigger  — armed right after the XVF is up: two presses ("double tap")
//!              within a short window enter the selector; otherwise boot
//!              continues normally. A unit that merely boots muted produces
//!              zero edges, so it never triggers by accident.
//!   Selector — each press advances the highlighted profile (wrapping);
//!              leaving the button alone for CONFIRM_SILENCE_MS confirms it.
//!              An absolute timeout confirms too, so a flaky button can't
//!              hold the boot hostage.
//!
//! The imperative shell (selector.zig) samples the button every TICK_MS and
//! renders the ring; everything decision-shaped lives here.

pub const TICK_MS: u32 = 50;
pub const TRIGGER_WINDOW_MS: u32 = 2000;
pub const TRIGGER_EDGES: u8 = 2;
pub const CONFIRM_SILENCE_MS: u32 = 5000;
pub const SELECTOR_TIMEOUT_MS: u32 = 30000;

const TRIGGER_WINDOW_TICKS: u32 = TRIGGER_WINDOW_MS / TICK_MS;
const CONFIRM_SILENCE_TICKS: u32 = CONFIRM_SILENCE_MS / TICK_MS;
const SELECTOR_TIMEOUT_TICKS: u32 = SELECTOR_TIMEOUT_MS / TICK_MS;

pub const Trigger = struct {
    prev: ?bool = null,
    edges: u8 = 0,
    ticks: u32 = 0,

    /// Feed one button-state sample. True once the double tap happened.
    pub fn feed(self: *Trigger, muted: bool) bool {
        if (self.prev) |p| {
            if (p != muted) self.edges += 1;
        }
        self.prev = muted;
        self.ticks += 1;
        return self.edges >= TRIGGER_EDGES;
    }

    pub fn expired(self: *const Trigger) bool {
        return self.ticks >= TRIGGER_WINDOW_TICKS;
    }
};

pub const Selector = struct {
    selected: usize,
    count: usize,
    prev: ?bool = null,
    silence_ticks: u32 = 0,
    total_ticks: u32 = 0,

    /// Feed one button-state sample. Returns the chosen index on confirmation
    /// (silence after the last press, or the absolute timeout), null meanwhile.
    pub fn feed(self: *Selector, muted: bool) ?usize {
        var pressed = false;
        if (self.prev) |p| pressed = p != muted;
        self.prev = muted;
        self.total_ticks += 1;

        if (pressed) {
            self.selected = (self.selected + 1) % self.count;
            self.silence_ticks = 0;
        } else {
            self.silence_ticks += 1;
            if (self.silence_ticks >= CONFIRM_SILENCE_TICKS) return self.selected;
        }
        // Checked on every tick (also right after a press): a flaky button
        // toggling forever must not hold the boot hostage.
        if (self.total_ticks >= SELECTOR_TIMEOUT_TICKS) return self.selected;
        return null;
    }
};
