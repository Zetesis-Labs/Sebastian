//! Sebastian as a plug-and-play USB microphone (UAC).
//!
//! Alternate app root (CONFIG_SEBASTIAN_USB_MIC, selected in cmake/zig.cmake):
//! instead of joining a LiveKit room, the XVF3800 comms beam is exposed to the
//! host as a standard USB Audio Class microphone — 48 kHz mono 16-bit, no WiFi,
//! no wake word, no agent. TinyUSB claims the S3's USB PHY, so the
//! USB-Serial-JTAG console/flasher disappears while this firmware runs;
//! reflashing needs the BOOT-button download mode (docs/USB_MIC.md).
//!
//! The capture path IS the production mic path: mic_src's vtable is driven by
//! the UAC mic task instead of esp_capture, so slot selection, gain shift, soft
//! limiter, clock resync, channel self-heal and the physical mute button (via
//! xvf_ui) behave exactly like an agent session. With no speaker there is no
//! echo to cancel, so the beamformer stays ADAPTIVE and tracks the talker.

const std = @import("std");
const board = @import("board.zig");
const cfg = @import("config.zig");
const mic_src = @import("mic_src.zig");
const xvf_dfu = @import("xvf_dfu.zig");
const xvf_ui = @import("xvf_ui.zig");
const xvf_aec = @import("xvf_aec.zig");
const c = @import("csdk.zig");

const log = std.log.scoped(.sebastian_usb);

const UNITY_GAIN: u32 = 32768; // Q15

var mic_iface: *c.esp_capture_audio_src_iface_t = undefined;
// Host-side controls (macOS input volume/mute), applied AFTER the capture so
// the device keeps draining I2S while muted — same rule as the session gates.
var host_muted = std.atomic.Value(bool).init(false);
var host_gain = std.atomic.Value(u32).init(UNITY_GAIN);

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
    var frame = std.mem.zeroes(c.esp_capture_stream_frame_t);
    frame.data = buf;
    frame.size = @intCast(len);
    const rc = mic_iface.read_frame.?(mic_iface, &frame);

    const samples = @as([*]i16, @ptrCast(@alignCast(buf)))[0 .. len / 2];
    if (rc != c.ESP_CAPTURE_ERR_OK) {
        // Keep the stream alive on a capture hiccup: a silent frame is a click,
        // an error return would stall the UAC pipeline.
        @memset(samples, 0);
    } else {
        applyHostControls(samples);
    }
    bytes_read.* = len;
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

fn logStageError(comptime stage: []const u8, err: anyerror) void {
    log.err("{s} failed: {s}", .{ stage, @errorName(err) });
}

export fn app_main() callconv(.c) void {
    board.init() catch |err| {
        log.err("board init failed: {s} — halting", .{@errorName(err)});
        return;
    };

    // No speaker output in USB-mic mode → no echo → the AEC never needs to
    // converge. An adaptive beam that tracks the talker beats a fixed one here.
    cfg.fixed_beam = false;

    var xvf_ok = true;
    xvf_dfu.ensureMaster(board.i2cBus()) catch |err| {
        logStageError("XVF master firmware confirmation", err);
        xvf_ok = false;
    };
    xvf_dfu.unmute() catch |err| {
        logStageError("XVF unmute", err);
        xvf_ok = false;
    };
    xvf_ui.start(); // physical mute button → software gate + LED ring
    if (!xvf_aec.applyConfig()) xvf_ok = false;

    const src = mic_src.create(board.recordHandle()) orelse {
        log.err("mic source create failed — halting", .{});
        return;
    };
    mic_iface = @ptrCast(@alignCast(src));
    if (mic_iface.start.?(mic_iface) != c.ESP_CAPTURE_ERR_OK) {
        log.err("mic source start failed — halting", .{});
        return;
    }
    mic_src.setLive(true); // no wake task here: the UAC mic task owns I2S
    xvf_ui.setState(.active); // ring shows the DoA beam — "I'm listening"

    var uac = std.mem.zeroes(c.uac_device_config_t);
    uac.input_cb = inputCb;
    uac.set_mute_cb = setMuteCb;
    uac.set_volume_cb = setVolumeCb;
    if (c.uac_device_init(&uac) != c.ESP_OK) {
        log.err("UAC device init failed — halting", .{});
        return;
    }

    if (xvf_ok) {
        log.info("BOOT OK — USB mic: 48 kHz mono, comms beam, adaptive tracking", .{});
    } else {
        log.err("BOOT DEGRADED — XVF config incomplete, capture may be silent", .{});
    }
}

fn panicFn(msg: []const u8, _: ?usize) noreturn {
    log.err("PANIC: {s}", .{msg});
    c.abort();
}

pub const std_options: std.Options = .{
    .log_level = .info,
    .logFn = @import("log.zig").espLogFn,
};
pub const panic = std.debug.FullPanic(panicFn);
