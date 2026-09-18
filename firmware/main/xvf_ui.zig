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
const gesture = @import("core/gesture_core.zig");

const log = std.log.scoped(.xvf_ui);

pub const State = enum(u8) { idle, waking, active, usb };

/// Meeting recording as the ring shows it (RM-10/13/51): solid red while
/// recording, slow red blink 30 s before a silence cut, fast red blink when
/// the control room is gone. Nothing else on the ring is ever red.
pub const Recording = enum(u8) { off, on, warn, no_room };

var ui_state = std.atomic.Value(u8).init(@intFromEnum(State.idle));
var rec_state = std.atomic.Value(u8).init(@intFromEnum(Recording.off));

pub fn setRecording(r: Recording) void {
    rec_state.store(@intFromEnum(r), .release);
}

fn currentRecording() Recording {
    return @enumFromInt(rec_state.load(.acquire));
}

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
const USB_BEAM = rgb(90, 55, 0); // USB: same beam, amber — the PC has the mic
const REC = rgb(110, 0, 0); // recording: solid red (RM-10)
const REC_DIM = rgb(14, 0, 0); // recording + mute: red, every other LED dim (RM-13)
const USB_HALO = rgb(18, 11, 0);
const USB_HELD = rgb(6, 4, 0);

const VOICE_ON: u32 = 6000; // start-of-utterance level (hysteresis)
const VOICE_OFF: u32 = 3000; // end-of-utterance level
const DIR_OFFSET: u8 = 6; // physical LED-0 vs azimuth-0 alignment (+180°)

const BREATH_PERIOD: u32 = 40; // ~3.2s at 80ms/frame

// A hung XVF answers nothing, so each 80ms frame stalls on the I2C timeout and
// the task watchdog (5s) reboots the device mid-session. The ring is the least
// critical consumer of the bus: it steps aside and lets the session live on.
const DEGRADED_AFTER: u32 = 12;
const DEGRADED_DELAY: u32 = 2000;

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

/// Adoption consent (RF-34/64): a factory unit blinks the whole ring amber
/// while a control room waits for the MUTE button.
fn renderConsent(frame: u32) void {
    const on = (frame / 4) % 2 == 0; // ~3 Hz at 80 ms/frame
    xvf.setLeds(.{if (on) USB_BEAM else OFF} ** 12);
}

/// Meeting recording (RM-10/13/51).
fn renderRecording(r: Recording, muted: bool, frame: u32) void {
    switch (r) {
        .off => {},
        .on => {
            if (!muted) {
                xvf.setLeds(.{REC} ** 12);
                return;
            }
            var pix: [12][3]u8 = undefined;
            for (&pix, 0..) |*p, i| p.* = if (i % 2 == 0) REC else REC_DIM;
            xvf.setLeds(pix);
        },
        .warn => xvf.setLeds(.{if ((frame / 6) % 2 == 0) REC else OFF} ** 12), // ~1 Hz
        .no_room => xvf.setLeds(.{if ((frame / 2) % 2 == 0) REC else OFF} ** 12), // ~3 Hz
    }
}

/// Adoption accepted (RF-64): solid amber for the 2 s before the restart.
fn renderAccepted() void {
    xvf.setLeds(.{USB_BEAM} ** 12);
}

/// WAKING: one blue pixel orbiting with a dim trail — "heard you, connecting".
fn renderWaking(frame: u32) void {
    const idx: u8 = @intCast((frame / 2) % 12); // ~2s per revolution
    var pix = [_][3]u8{OFF} ** 12;
    pix[idx] = rgb(10, 40, 90); // bright blue head
    pix[(idx + 11) % 12] = rgb(2, 10, 22); // trailing dim
    xvf.setLeds(pix);
}

/// ACTIVE/USB: DoA beam (green = agent listening, amber = the PC has the
/// mic). `last`/`speaking` persist across frames via the caller.
fn renderActive(last: *u8, speaking: *bool, beam: [3]u8, halo: [3]u8, held: [3]u8) void {
    const lvl = mic.level();
    if (!speaking.* and lvl > VOICE_ON) {
        speaking.* = true;
        if (xvf.readBeamLed()) |idx| last.* = (idx + DIR_OFFSET) % 12; // lock at utterance start
    } else if (speaking.* and lvl < VOICE_OFF) {
        speaking.* = false;
    }
    if (speaking.*) {
        ring(last.*, beam, halo); // bright beam while you talk
    } else {
        ring(last.*, held, held); // dim, holding the last direction
    }
}

fn uiTask(_: ?*anyopaque) callconv(.c) void {
    var last: u8 = 0; // locked beam direction (ACTIVE)
    var speaking = false;
    var frame: u32 = 0;
    var was_muted: ?bool = null;
    var detector = gesture.Detector{};
    var degraded = false;
    while (true) : (frame +%= 1) {
        if (xvf.consecutiveFailures() >= DEGRADED_AFTER) {
            if (!degraded) {
                degraded = true;
                log.warn("XVF not answering on I2C — ring paused, probing every {d}ms", .{DEGRADED_DELAY});
            }
            c.vTaskDelay(DEGRADED_DELAY);
            _ = xvf.readMuted(); // lone recovery probe; clears the counter when the XVF returns
            continue;
        }
        if (degraded) {
            degraded = false;
            log.info("XVF answering again — ring resumed", .{});
        }
        const muted = xvf.readMuted();
        // MUTE gestures (RM-02/20/50): GPI byte 0 is the button, active low;
        // the XVF toggles the mute GPO on every press, so verdicts that were
        // not a mute tap undo those toggles.
        if (xvf.readGpi()) |gpi| {
            const now_ms: u32 = @intCast(@divTrunc(c.esp_timer_get_time(), 1000) & 0xffff_ffff);
            const verdict = detector.feed((gpi[0] & 0x01) == 0, now_ms);
            actOnGesture(verdict);
        }
        mic.setMuted(muted); // GPIO30 mute doesn't silence our ASR beam — do it in software
        const consent = c.sebastian_adopt_consent_pending();
        if (consent and was_muted != null and was_muted.? != muted) c.sebastian_adopt_consent_grant();
        if (was_muted == null or was_muted.? != muted) {
            was_muted = muted;
            // Telemetry: a muted XVF streams all-zero I2S — indistinguishable
            // from a dead channel without this line (cost us a 12-minute hunt).
            log.info("mute: {s}", .{if (muted) "on" else "off"});
        }

        const recording = currentRecording();
        if (c.sebastian_adopt_accepted()) {
            renderAccepted();
            speaking = false;
        } else if (recording != .off) {
            renderRecording(recording, muted, frame);
            speaking = false;
        } else if (consent) {
            renderConsent(frame);
            speaking = false;
        } else if (muted) {
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
            .active => renderActive(&last, &speaking, BEAM, HALO, HELD),
            .usb => renderActive(&last, &speaking, USB_BEAM, USB_HALO, USB_HELD),
        }
        c.vTaskDelay(80);
    }
}

fn actOnGesture(verdict: gesture.Verdict) void {
    if (verdict == .none) return;
    var undo = gesture.togglesToUndo(verdict);
    while (undo > 0) : (undo -= 1) xvf.setMute(!xvf.readMuted());
    switch (verdict) {
        .mute_tap => {},
        .long_alone, .ignored => log.info("mute button: {s} press, nothing done", .{if (verdict == .long_alone) "long" else "odd"}),
        .record_toggle => {
            if (!c.sebastian_meeting_is_recordable()) {
                log.info("record gesture in a profile without recording — ignored (RM-54)", .{});
                return;
            }
            const cmd: [*:0]const u8 = if (c.sebastian_meeting_is_active()) "record-stop" else "record-start";
            log.info("record gesture → {s}", .{cmd});
            c.sebastian_meeting_push(cmd, "", 'g');
        },
        .none => {},
    }
}

/// Start the LED UI task. Call after the XVF is up (post ensureMaster).
pub fn start() void {
    _ = c.xTaskCreatePinnedToCore(uiTask, "xvf_ui", 3072, null, 3, null, 0);
}
