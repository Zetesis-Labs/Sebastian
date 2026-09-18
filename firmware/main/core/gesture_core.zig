//! MUTE button gestures (meeting recordings, RM-02/20/50) — pure, host-tested.
//!
//! The XVF3800 exposes the button level in GPI_READ_VALUES (byte 0, active
//! low) and toggles its own mute GPO on every press edge. The ring task feeds
//! the sampled level here every frame (80 ms) and acts on the verdicts:
//!
//!   short press (≤ SHORT_MAX_MS)               → .mute_tap: the XVF's toggle stands
//!   long press alone (≥ LONG_MIN_MS)           → .long_alone: undo the XVF's toggle (nothing happens)
//!   short, then long within GAP_MAX_MS         → .record_toggle: undo both toggles (RM-02:
//!                                                the short one did not mean "mute")
//!   anything in between (0.5–1.5 s)            → .ignored, toggle undone: a clumsy press
//!
//! A short press is only reported once the gap window has elapsed without a
//! second press, so a mute tap never fires and then turns into a gesture.

const std = @import("std");

pub const SHORT_MAX_MS: u32 = 500;
pub const LONG_MIN_MS: u32 = 1500;
pub const GAP_MAX_MS: u32 = 1000;

pub const Verdict = enum {
    none,
    mute_tap,
    long_alone,
    record_toggle,
    ignored,
};

const Phase = enum { idle, pressed, short_done, second_pressed };

pub const Detector = struct {
    phase: Phase = .idle,
    press_started_ms: u32 = 0,
    short_released_ms: u32 = 0,

    /// Feed the sampled button level. `pressed` = the GPI bit is low.
    pub fn feed(self: *Detector, pressed: bool, now_ms: u32) Verdict {
        switch (self.phase) {
            .idle => {
                if (pressed) {
                    self.phase = .pressed;
                    self.press_started_ms = now_ms;
                }
                return .none;
            },
            .pressed => {
                if (pressed) return .none;
                const held = now_ms -% self.press_started_ms;
                if (held <= SHORT_MAX_MS) {
                    self.phase = .short_done;
                    self.short_released_ms = now_ms;
                    return .none;
                }
                self.phase = .idle;
                return if (held >= LONG_MIN_MS) .long_alone else .ignored;
            },
            .short_done => {
                if (pressed) {
                    if (now_ms -% self.short_released_ms <= GAP_MAX_MS) {
                        self.phase = .second_pressed;
                        self.press_started_ms = now_ms;
                        return .none;
                    }
                    // Too late to be a gesture: the tap stands, a new press begins.
                    self.phase = .pressed;
                    self.press_started_ms = now_ms;
                    return .mute_tap;
                }
                if (now_ms -% self.short_released_ms > GAP_MAX_MS) {
                    self.phase = .idle;
                    return .mute_tap;
                }
                return .none;
            },
            .second_pressed => {
                if (pressed) return .none;
                const held = now_ms -% self.press_started_ms;
                self.phase = .idle;
                if (held >= LONG_MIN_MS) return .record_toggle;
                // short + short: two ordinary taps; the first already stood.
                if (held <= SHORT_MAX_MS) {
                    self.phase = .short_done;
                    self.short_released_ms = now_ms;
                    return .mute_tap;
                }
                return .ignored; // the first tap stands; the odd second press is undone
            },
        }
    }
};

/// How many XVF mute toggles the verdict must undo so the mute state ends
/// where the user meant it (RM-02: the short one "does not change the mute").
pub fn togglesToUndo(v: Verdict) u8 {
    return switch (v) {
        .none, .mute_tap => 0,
        .long_alone, .ignored => 1,
        .record_toggle => 2,
    };
}
