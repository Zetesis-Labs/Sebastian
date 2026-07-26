//! USB-microphone mode (profile mode = usb_mic).
//!
//! The XVF3800 comms beam exposed to the host as a standard USB Audio Class
//! microphone — 48 kHz mono 16-bit, no WiFi, no wake word, no agent. TinyUSB
//! claims the S3's USB PHY, so the USB-Serial-JTAG console/flasher disappears
//! while this mode runs; switch profiles with the boot selector (double-tap
//! mute after plugging) or reflash via BOOT-button download mode
//! (docs/USB_MIC.md).
//!
//! The capture path IS the production mic path: mic_src's vtable is driven by
//! the UAC mic task instead of esp_capture, so slot selection, gain shift, soft
//! limiter, clock resync, channel self-heal and the physical mute button (via
//! xvf_ui) behave exactly like an agent session. The usb_mic built-in profile
//! keeps the beamformer ADAPTIVE (no speaker → no echo → nothing needs AEC
//! convergence) so it tracks the talker.

const std = @import("std");
const board = @import("board.zig");
const control = @import("control.zig");
const mic_src = @import("mic_src.zig");
const profile = @import("profile.zig");
const xvf_ui = @import("xvf_ui.zig");
const c = @import("csdk.zig");

const log = std.log.scoped(.usb_mic);

const UNITY_GAIN: u32 = 32768; // Q15

var mic_iface: ?*c.esp_capture_audio_src_iface_t = null;
// Host-side controls (macOS input volume/mute), applied AFTER the capture so
// the device keeps draining I2S while muted — same rule as the session gates.
var host_muted = std.atomic.Value(bool).init(false);
var host_gain = std.atomic.Value(u32).init(UNITY_GAIN);

// ── Convivencia (USB↔agent arbitration) ─────────────────────────────────────
// The UAC component only calls inputCb while the host has the capture stream
// open, so the last-callback timestamp IS the "the PC is using the mic"
// signal. Ownership is granted by the agent's main loop (core/arbiter_core
// hysteresis); until then inputCb feeds silence without touching I2S.
var usb_owns = std.atomic.Value(bool).init(false);
// Milliseconds, u32 with wrap-safe compares: Xtensa only has 32-bit atomics.
// Initialized half a range away so boot (t≈0) never reads as "capturing".
var last_read_ms = std.atomic.Value(u32).init(0x8000_0000);

fn nowMs() u32 {
    return @truncate(@as(u64, @intCast(c.esp_timer_get_time())) / 1000);
}

/// True while the host reads mic audio (sampled against the last callback).
pub fn hostCapturing() bool {
    return nowMs() -% last_read_ms.load(.monotonic) < 200;
}

/// Register the shared mic source (agent path creates it for esp_capture;
/// the pure usb_mic mode creates it itself).
pub fn bindMicSource(src: *c.esp_capture_audio_src_if_t) void {
    mic_iface = @ptrCast(@alignCast(src));
}

/// Take the mic for the USB host. Caller must have released the other I2S
/// consumer first (wakeword stopped / session closed). The vtable start()
/// resets the resync flag, so the first read re-latches onto the XVF clock.
pub fn grant() void {
    const iface = mic_iface orelse return;
    _ = iface.start.?(iface);
    mic_src.setLive(true);
    usb_owns.store(true, .release);
    log.info("mic granted to USB host", .{});
}

/// Return the mic to the agent side. inputCb goes back to silence.
pub fn revoke() void {
    usb_owns.store(false, .release);
    mic_src.setLive(false);
    if (mic_iface) |iface| _ = iface.stop.?(iface);
    log.info("mic returned to the agent", .{});
}

fn applyHostControls(samples: []i16) void {
    if (host_muted.load(.monotonic)) {
        @memset(samples, 0);
        return;
    }
    const gain = host_gain.load(.monotonic);
    if (gain == UNITY_GAIN) return;
    for (samples) |*s| {
        s.* = @intCast((@as(i32, s.*) * @as(i32, @intCast(gain))) >> 15);
    }
}

fn inputCb(buf: [*]u8, len: usize, bytes_read: *usize, _: ?*anyopaque) callconv(.c) c.esp_err_t {
    last_read_ms.store(nowMs(), .monotonic);

    const samples = @as([*]i16, @ptrCast(@alignCast(buf)))[0 .. len / 2];
    bytes_read.* = len;

    // Not our mic (agent owns it, or nothing bound yet): silence, never I2S —
    // the arbiter in the agent loop decides when we get the real capture.
    const iface = mic_iface orelse {
        @memset(samples, 0);
        return c.ESP_OK;
    };
    if (!usb_owns.load(.acquire)) {
        @memset(samples, 0);
        return c.ESP_OK;
    }

    var frame = std.mem.zeroes(c.esp_capture_stream_frame_t);
    frame.data = buf;
    frame.size = @intCast(len);
    if (iface.read_frame.?(iface, &frame) != c.ESP_CAPTURE_ERR_OK) {
        // Keep the stream alive on a capture hiccup: a silent frame is a click,
        // an error return would stall the UAC pipeline.
        @memset(samples, 0);
    } else {
        applyHostControls(samples);
    }
    return c.ESP_OK;
}

fn setMuteCb(mute: u32, _: ?*anyopaque) callconv(.c) void {
    host_muted.store(mute != 0, .monotonic);
    log.info("host mute: {s}", .{if (mute != 0) "on" else "off"});
}

fn setVolumeCb(volume: u32, _: ?*anyopaque) callconv(.c) void {
    const clamped = @min(volume, 100);
    host_gain.store(clamped * UNITY_GAIN / 100, .monotonic);
    log.info("host volume: {d}", .{volume});
}

// ── Network side (F3/F4) ─────────────────────────────────────────────────────
// Audio rides USB; WiFi here is telemetry + control plane only. The agent's
// hard rule (WIFI_PS_NONE for real-time audio) does not apply — modem sleep
// stays on. The capture-health counters that the agent drains per session
// window land in Grafana on a fixed tick instead, so a degraded desk mic is
// visible from the sofa.

const TELEMETRY_PERIOD_MS: u32 = 60_000;

fn telemetryTask(_: ?*anyopaque) callconv(.c) void {
    while (true) {
        c.vTaskDelay(TELEMETRY_PERIOD_MS);
        const rs = mic_src.takeReadStats();
        log.info("usb mic health: level={d} short_reads={d} pad_samples={d} timeouts={d} heals={d} heap_int={d}", .{
            mic_src.level(),                rs.short_reads,
            rs.pad_samples,                 rs.timeouts,
            rs.heals,                       c.heap_caps_get_free_size(c.MALLOC_CAP_INTERNAL),
        });
    }
}

fn startNetwork() void {
    if (!c.sebastian_net_connect()) {
        log.warn("wifi unavailable — mic keeps working offline, control plane off", .{});
        return;
    }
    _ = c.esp_wifi_set_ps(c.WIFI_PS_MIN_MODEM);
    c.sebastian_syslog_start();
    _ = c.xTaskCreatePinnedToCore(telemetryTask, "usb_telemetry", 2048, null, 2, null, 0);
    control.start();
    log.info("network up: telemetry + control-plane poll active", .{});
}

/// Initialize the TinyUSB UAC device. Called from the COMMON boot path in
/// both modes (convivencia: the mic interface must exist whenever a computer
/// sits on the other end of the cable). This is the moment the USB PHY stops
/// being serial — the provisioning window must have elapsed before this.
pub fn initUac() bool {
    var uac = std.mem.zeroes(c.uac_device_config_t);
    uac.input_cb = inputCb;
    uac.set_mute_cb = setMuteCb;
    uac.set_volume_cb = setVolumeCb;
    if (c.uac_device_init(&uac) != c.ESP_OK) {
        log.err("UAC device init failed — USB mic unavailable this boot", .{});
        return false;
    }
    return true;
}

/// Pure usb_mic profile (no agent at all): the USB host owns the mic
/// permanently. initUac() has already run in the common boot path.
pub fn run(xvf_ok: bool) void {
    const src = mic_src.create(board.recordHandle()) orelse {
        log.err("mic source create failed — halting", .{});
        return;
    };
    bindMicSource(src);
    grant();
    xvf_ui.setState(.active); // ring shows the DoA beam — "I'm listening"
    profile.bootNoteOk();

    if (xvf_ok) {
        log.info("BOOT OK — USB mic: 48 kHz mono, comms beam", .{});
    } else {
        log.err("BOOT DEGRADED — XVF config incomplete, capture may be silent", .{});
    }

    // After the mic is live: audio never waits on the network. A profile with
    // `wifi: false` stays a fully offline dumb mic.
    if (profile.wifiEnabled()) startNetwork();
}
