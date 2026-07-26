//! Control-plane client (F4): desired-profile reconciliation.
//!
//! A small task polls the Sebastian server every POLL_PERIOD_MS:
//!   GET {origin(token_url)}/api/devices/{mac}/desired-profile?current={active}
//! Plaintext contract, same philosophy as the token fetch: the body is the
//! desired profile name (empty = nothing desired). The `current` query param
//! doubles as the device's state report — the server learns what each unit is
//! running without a separate POST.
//!
//! When desired ≠ active and the name matches a provisioned profile, the
//! device persists it and reboots (~8 s) into the new personality. A reboot is
//! deliberate — hot-swapping LiveKit ↔ TinyUSB stacks is fragile; the boot
//! path is the one code path that is always exercised.
//!
//! Runs in BOTH modes (that is the point: steady-state usb_mic mode becomes
//! reachable over the network, no replug windows, no button).

const std = @import("std");
const c = @import("csdk.zig");
const profile = @import("profile.zig");
const url_core = @import("core/url_core.zig");

const log = std.log.scoped(.control);

const POLL_PERIOD_MS: u32 = 30_000;

extern fn token_http_get(url: [*:0]const u8, out: [*]u8, out_size: usize) c_int;
extern fn sebastian_get_token_url(out: [*]u8, size: usize) bool;

var token_url: [256]u8 = undefined;
var url_buf: [384]u8 = undefined;
var resp_buf: [64]u8 = undefined;

fn deviceId(out: *[12]u8) bool {
    var mac: [6]u8 = undefined;
    if (c.esp_read_mac(&mac, c.ESP_MAC_WIFI_STA) != c.ESP_OK) return false;
    const hex = "0123456789abcdef";
    for (mac, 0..) |b, i| {
        out[i * 2] = hex[b >> 4];
        out[i * 2 + 1] = hex[b & 0xF];
    }
    return true;
}

fn pollOnce() void {
    if (!sebastian_get_token_url(&token_url, token_url.len)) return;
    var id: [12]u8 = undefined;
    if (!deviceId(&id)) return;

    const active_name = profile.nameOf(profile.active);
    const url = std.fmt.bufPrintZ(&url_buf, "{s}/v1/devices/{s}/desired-profile?current={s}", .{
        url_core.origin(std.mem.sliceTo(&token_url, 0)), id, active_name,
    }) catch return;

    const n = token_http_get(url.ptr, &resp_buf, resp_buf.len);
    if (n <= 0) return; // server unreachable / no desired set — try next tick
    const desired = std.mem.trim(u8, resp_buf[0..@intCast(n)], " \t\r\n");
    if (desired.len == 0 or std.mem.eql(u8, desired, active_name)) return;

    const index = profile.indexOf(desired) orelse {
        log.warn("desired profile '{s}' not provisioned on this unit — ignoring", .{desired});
        return;
    };
    log.info("control plane: switching profile '{s}' → '{s}' (reboot)", .{ active_name, desired });
    profile.selectByIndex(index);
    c.vTaskDelay(500); // let the log reach syslog before the reset
    c.esp_restart();
}

fn pollTask(_: ?*anyopaque) callconv(.c) void {
    while (true) {
        c.vTaskDelay(POLL_PERIOD_MS);
        pollOnce();
    }
}

/// Start the reconciliation poll. Call once the network is up (either mode).
pub fn start() void {
    // esp_http_client runs on this task: 4KB is the measured floor for a plain
    // HTTP GET (internal RAM is the scarce pool — see usb_mic/convivencia).
    _ = c.xTaskCreatePinnedToCore(pollTask, "ctl_poll", 4096, null, 2, null, 0);
    log.info("control-plane poll every {d}s", .{POLL_PERIOD_MS / 1000});
}
