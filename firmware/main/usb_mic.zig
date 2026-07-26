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
const mic_src = @import("mic_src.zig");
const profile = @import("profile.zig");
const xvf_ui = @import("xvf_ui.zig");
const c = @import("csdk.zig");

const log = std.log.scoped(.usb_mic);

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

/// Bring up the UAC device over the already-initialized board/XVF. Returns
/// after init — the UAC component's tasks own the runtime from here.
pub fn run(xvf_ok: bool) void {
    const src = mic_src.create(board.recordHandle()) orelse {
        log.err("mic source create failed — halting", .{});
        return;
    };
    mic_iface = @ptrCast(@alignCast(src));
    if (mic_iface.start.?(mic_iface) != c.ESP_CAPTURE_ERR_OK) {
        log.err("mic source start failed — halting", .{});
        return;
    }
    mic_src.setLive(true); // no wake task in this mode: the UAC mic task owns I2S
    xvf_ui.setState(.active); // ring shows the DoA beam — "I'm listening"

    var uac = std.mem.zeroes(c.uac_device_config_t);
    uac.input_cb = inputCb;
    uac.set_mute_cb = setMuteCb;
    uac.set_volume_cb = setVolumeCb;
    if (c.uac_device_init(&uac) != c.ESP_OK) {
        log.err("UAC device init failed — halting", .{});
        return;
    }
    profile.bootNoteOk();

    if (xvf_ok) {
        log.info("BOOT OK — USB mic: 48 kHz mono, comms beam", .{});
    } else {
        log.err("BOOT DEGRADED — XVF config incomplete, capture may be silent", .{});
    }
}
