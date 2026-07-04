//! LED ring UI for the XVF3800 — invocation-state feedback + direction of arrival.
//!
//! The ring makes the invocation state legible at a glance:
//!   IDLE   — waiting for the wake word: a slow dim breath ("armed, not listening")
//!   WAKING — wake word heard, session connecting: one pixel orbiting ("one sec")
//!   ACTIVE — session open: the DoA beam points at the talker ("I'm listening")
//!
//! The state is set by app.zig's main loop (setState). During ACTIVE we restore
//! the stock behaviour: lock the beam direction for the duration of an utterance
//! (approximated from the mic's own voice level, since we don't have the agent's
//! speech signal on-device) so it doesn't wander. Muting turns the ring off.

const std = @import("std");
const c = @import("csdk.zig");
const xvf = @import("xvf_dfu.zig");
const mic = @import("mic_src.zig");

const log = std.log.scoped(.xvf_ui);

pub const State = enum(u8) { idle, waking, active };

var ui_state = std.atomic.Value(u8).init(@intFromEnum(State.idle));

/// Set the invocation state shown on the ring. Called from app.zig.
pub fn setState(s: State) void {
    ui_state.store(@intFromEnum(s), .release);
}

fn currentState() State {
    return @enumFromInt(ui_state.load(.acquire));
}

/// The XVF LED ring takes colour bytes in BGR order (calibrated on hardware:
/// byte0=blue, byte1=green, byte2=red). Author colours as rgb() and the swap
/// lives here only.
fn rgb(r: u8, g: u8, b: u8) [3]u8 {
    return .{ b, g, r };
}

const OFF = [3]u8{ 0, 0, 0 };
const BEAM = rgb(0, 90, 20); // ACTIVE: green LED pointing at the talker
const HALO = rgb(0, 18, 4); // its two neighbours
const HELD = rgb(0, 6, 2); // ACTIVE, nobody talking: dim held direction

const VOICE_ON: u32 = 6000; // start-of-utterance level (hysteresis)
const VOICE_OFF: u32 = 3000; // end-of-utterance level
const DIR_OFFSET: u8 = 6; // physical LED-0 vs azimuth-0 alignment (+180°)

const BREATH_PERIOD: u32 = 40; // ~3.2s at 80ms/frame

fn ring(idx: u8, center: [3]u8, halo: [3]u8) void {
    var pix = [_][3]u8{OFF} ** 12;
    pix[idx] = center;
    if (center[0] != halo[0] or center[1] != halo[1] or center[2] != halo[2]) {
        pix[(idx + 1) % 12] = halo;
        pix[(idx + 11) % 12] = halo;
    }
    xvf.setLeds(pix);
}

/// Triangle wave 2..14 over BREATH_PERIOD frames (no float — Xtensa runtime
/// lacks the 128-bit division std.math float formatting pulls in).
fn breath(frame: u32) u8 {
    const half = BREATH_PERIOD / 2;
    const p = frame % BREATH_PERIOD;
    const tri = if (p < half) p else BREATH_PERIOD - p; // 0..half..0
    return @intCast(2 + tri * 12 / half);
}

/// IDLE: whole ring breathing a dim teal — clearly "standby", not the bright beam.
fn renderIdle(frame: u32) void {
    const b = breath(frame);
    xvf.setLeds(.{rgb(0, b, b / 2)} ** 12);
}

/// WAKING: one blue pixel orbiting with a dim trail — "heard you, connecting".
fn renderWaking(frame: u32) void {
    const idx: u8 = @intCast((frame / 2) % 12); // ~2s per revolution
    var pix = [_][3]u8{OFF} ** 12;
    pix[idx] = rgb(10, 40, 90); // bright blue head
    pix[(idx + 11) % 12] = rgb(2, 10, 22); // trailing dim
    xvf.setLeds(pix);
}

/// ACTIVE: DoA beam. `last`/`speaking` persist across frames via the caller.
fn renderActive(last: *u8, speaking: *bool) void {
    const lvl = mic.level();
    if (!speaking.* and lvl > VOICE_ON) {
        speaking.* = true;
        if (xvf.readBeamLed()) |idx| last.* = (idx + DIR_OFFSET) % 12; // lock at utterance start
    } else if (speaking.* and lvl < VOICE_OFF) {
        speaking.* = false;
    }
    if (speaking.*) {
        ring(last.*, BEAM, HALO); // bright beam while you talk
    } else {
        ring(last.*, HELD, HELD); // dim, holding the last direction
    }
}

fn uiTask(_: ?*anyopaque) callconv(.c) void {
    var last: u8 = 0; // locked beam direction (ACTIVE)
    var speaking = false;
    var frame: u32 = 0;
    var was_muted: ?bool = null;
    while (true) : (frame +%= 1) {
        const muted = xvf.readMuted();
        mic.setMuted(muted); // GPIO30 mute doesn't silence our ASR beam — do it in software
        if (was_muted == null or was_muted.? != muted) {
            was_muted = muted;
            // Telemetry: a muted XVF streams all-zero I2S — indistinguishable
            // from a dead channel without this line (cost us a 12-minute hunt).
            log.info("mute: {s}", .{if (muted) "on" else "off"});
        }

        if (muted) {
            xvf.setLeds(.{OFF} ** 12);
            speaking = false;
        } else switch (currentState()) {
            .idle => {
                renderIdle(frame);
                speaking = false;
            },
            .waking => {
                renderWaking(frame);
                speaking = false;
            },
            .active => renderActive(&last, &speaking),
        }
        c.vTaskDelay(80);
    }
}

/// Start the LED UI task. Call after the XVF is up (post ensureMaster).
pub fn start() void {
    _ = c.xTaskCreatePinnedToCore(uiTask, "xvf_ui", 3072, null, 3, null, 0);
}
