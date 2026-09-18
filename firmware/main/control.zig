//! Control-plane client: desired-profile + desired-config reconciliation, and
//! the fleet announce/adoption bring-up (docs/implementation/11-fleet-adoption-control-room.md).
//!
//! A small task polls the Sebastian server every POLL_PERIOD_MS:
//!   GET {origin(token_url)}/v1/devices/{mac}/desired-profile?current={active}&cfg={ver}&fw={fw}
//! Plaintext contract, same philosophy as the token fetch: the body is the
//! desired profile name (empty = nothing desired). The query params double as
//! the device's state report — the server learns what each unit runs (profile,
//! config version, firmware) without a separate POST. The response header
//! `X-Desired-Config` carries the version of the config the control room wants;
//! when it differs from the stored one the device fetches
//!   GET {origin}/v1/devices/{mac}/config   (X-Device-Id / X-Device-Secret)
//! stores it through provisioning.c and reboots.
//!
//! The device secret is born with the unit (provisioning.c). A unit whose
//! control room does not hold it yet (provisioned from the installer, or the
//! control room answered 401) enrols before its poll —
//!   GET/POST {origin}/v1/devices/{mac}/enroll   (HMAC over the server's nonce)
//! handing its secret over, so it is born adopted (RF-03/51).
//!
//! Every change reboots (~8 s) — deliberately: hot-swapping LiveKit ↔ TinyUSB
//! stacks is fragile; the boot path is the one code path that is always
//! exercised. Never during a conversation: the reboot waits for the session
//! teardown (applyPendingRestart), RF-65.
//!
//! Runs in BOTH modes (that is the point: steady-state usb_mic mode becomes
//! reachable over the network, no replug windows, no button).

const std = @import("std");
const c = @import("csdk.zig");
const profile = @import("profile.zig");
const url_core = @import("core/url_core.zig");

const log = std.log.scoped(.control);

const POLL_PERIOD_MS: u32 = 30_000;
const CONFIG_BODY_SIZE: usize = 4096;

extern fn sebastian_get_token_url(out: [*]u8, size: usize) bool;
extern fn sebastian_fw_version() [*:0]const u8;
extern fn sebastian_adopt_session_is_active() bool;

var token_url: [256]u8 = undefined;
var origin_z: [256]u8 = undefined;
var url_buf: [384]u8 = undefined;
var resp_buf: [64]u8 = undefined;
var hdr_buf: [32]u8 = undefined;
var cfg_ver: [24]u8 = undefined;
var dev_secret: [129]u8 = undefined;
var org_secret: [129]u8 = undefined;
var id_z: [13]u8 = undefined;
var prof_z: [profile.NAME_MAX + 1]u8 = undefined;
var cfg_body: ?[*]u8 = null; // PSRAM, allocated on first use
var restart_pending = std.atomic.Value(bool).init(false);

/// The unit's id everywhere (mDNS, control room, sessions): WiFi MAC, lowercase hex.
pub fn deviceId(out: *[13]u8) bool {
    var mac: [6]u8 = undefined;
    if (c.esp_read_mac(&mac, c.ESP_MAC_WIFI_STA) != c.ESP_OK) return false;
    const hex = "0123456789abcdef";
    for (mac, 0..) |b, i| {
        out[i * 2] = hex[b >> 4];
        out[i * 2 + 1] = hex[b & 0xF];
    }
    out[12] = 0;
    return true;
}

fn originZ() ?[*:0]const u8 {
    if (!sebastian_get_token_url(&token_url, token_url.len)) return null;
    const o = url_core.origin(std.mem.sliceTo(&token_url, 0));
    const z = std.fmt.bufPrintZ(&origin_z, "{s}", .{o}) catch return null;
    return z.ptr;
}

fn secretZ() ?[*:0]const u8 {
    if (!c.sebastian_get_device_secret(&dev_secret, dev_secret.len)) return null;
    return @ptrCast(&dev_secret);
}

fn cfgVersion() []const u8 {
    if (!c.sebastian_get_cfg_version(&cfg_ver, cfg_ver.len)) cfg_ver[0] = 0;
    return std.mem.sliceTo(&cfg_ver, 0);
}

fn announceResult(status: c_int, n: c_int) void {
    var buf: [16]u8 = undefined;
    const text = if (n >= 0) "ok" else if (status > 0)
        (std.fmt.bufPrintZ(&buf, "http:{d}", .{status}) catch "http")
    else
        "timeout";
    c.sebastian_announce_set("err", @ptrCast(text.ptr));
}

/// Reboot now, or right after the current conversation ends (RF-39/65).
fn requestRestart(reason: []const u8) void {
    if (sebastian_adopt_session_is_active()) {
        log.info("control plane: {s} — reboot deferred until the conversation ends", .{reason});
        restart_pending.store(true, .release);
        return;
    }
    log.info("control plane: {s} (reboot)", .{reason});
    c.vTaskDelay(500); // let the log reach syslog before the reset
    c.esp_restart();
}

/// Called by app.zig after each session teardown.
pub fn applyPendingRestart() void {
    if (!restart_pending.load(.acquire)) return;
    log.info("control plane: applying the deferred reboot", .{});
    c.vTaskDelay(500);
    c.esp_restart();
}

fn fetchAndApplyConfig(origin: [*:0]const u8, version: []const u8) void {
    const body = cfg_body orelse blk: {
        const p: ?[*]u8 = @ptrCast(c.heap_caps_malloc(CONFIG_BODY_SIZE, c.MALLOC_CAP_SPIRAM));
        cfg_body = p;
        break :blk p orelse return;
    };
    const url = std.fmt.bufPrintZ(&url_buf, "{s}/v1/devices/{s}/config", .{ origin, std.mem.sliceTo(&id_z, 0) }) catch return;
    var status: c_int = 0;
    const n = c.sebastian_http_get_auth(url.ptr, @ptrCast(&id_z), secretZ(), null, null, 0, body, CONFIG_BODY_SIZE, &status);
    if (n <= 0) {
        log.warn("desired config {s}: fetch failed (rc={d} http={d})", .{ version, n, status });
        return;
    }
    var why: [32]u8 = undefined;
    if (!c.sebastian_provisioning_apply(@ptrCast(body), &why, why.len)) {
        log.err("desired config {s} rejected: {s}", .{ version, std.mem.sliceTo(&why, 0) });
        var err: [48]u8 = undefined;
        const text = std.fmt.bufPrintZ(&err, "cfg-rejected:{s}", .{std.mem.sliceTo(&why, 0)}) catch return;
        c.sebastian_announce_set("err", text.ptr);
        return;
    }
    c.sebastian_announce_set("cfg", @ptrCast(cfgVersion().ptr));
    var reason: [64]u8 = undefined;
    const text = std.fmt.bufPrint(&reason, "desired config {s} stored", .{version}) catch "desired config stored";
    requestRestart(text);
}

/// Born adopted: when the control room of the token URL does not hold our
/// secret yet and we carry the organization secret, hand it over. Retried on
/// every poll until it succeeds.
fn enrollIfNeeded() void {
    if (c.sebastian_is_bound()) return;
    if (!c.sebastian_get_org_secret(&org_secret, org_secret.len)) return;
    const origin = originZ() orelse return;
    const secret = secretZ() orelse return;
    const rc = c.sebastian_enroll(origin, @ptrCast(&id_z), @ptrCast(&org_secret), secret);
    if (rc != 0) {
        log.warn("enrolment with {s} failed (rc={d}) — retrying on the next poll", .{ origin, rc });
        return;
    }
    c.sebastian_mark_bound();
    log.info("enrolled with {s}: it holds our device secret now", .{origin});
}

fn pollOnce() void {
    enrollIfNeeded();
    const origin = originZ() orelse return;
    const active_name = profile.nameOf(profile.active);
    const url = std.fmt.bufPrintZ(&url_buf, "{s}/v1/devices/{s}/desired-profile?current={s}&cfg={s}&fw={s}", .{
        origin, std.mem.sliceTo(&id_z, 0), active_name, cfgVersion(), sebastian_fw_version(),
    }) catch return;

    var status: c_int = 0;
    const n = c.sebastian_http_get_auth(url.ptr, @ptrCast(&id_z), secretZ(), "X-Desired-Config", &hdr_buf, hdr_buf.len, &resp_buf, resp_buf.len, &status);
    announceResult(status, n);
    if (status == 401 and c.sebastian_is_bound()) {
        // The control room does not know our secret (rebuilt, or we were
        // forgotten there): enrol again on the next poll if we can.
        log.warn("control room answered 401 — will re-enrol", .{});
        c.sebastian_clear_bound();
    }
    if (n < 0) return; // server unreachable — try next tick

    const desired = std.mem.trim(u8, resp_buf[0..@intCast(n)], " \t\r\n");
    if (desired.len > 0 and !std.mem.eql(u8, desired, active_name)) {
        if (profile.indexOf(desired)) |index| {
            profile.selectByIndex(index);
            var reason: [80]u8 = undefined;
            const text = std.fmt.bufPrint(&reason, "switching profile '{s}' → '{s}'", .{ active_name, desired }) catch "switching profile";
            requestRestart(text);
            return;
        }
        log.warn("desired profile '{s}' not provisioned on this unit — ignoring", .{desired});
    }

    const wanted = std.mem.trim(u8, std.mem.sliceTo(&hdr_buf, 0), " \t\r\n");
    if (wanted.len > 0 and !std.mem.eql(u8, wanted, cfgVersion())) {
        log.info("desired config {s} (running {s}) — fetching", .{ wanted, cfgVersion() });
        fetchAndApplyConfig(origin, wanted);
    }
}

fn pollTask(_: ?*anyopaque) callconv(.c) void {
    enrollIfNeeded();
    while (true) {
        c.vTaskDelay(POLL_PERIOD_MS);
        pollOnce();
    }
}

/// Start the reconciliation poll, the mDNS announce and the adoption listener.
/// Call once the network is up (either mode). The unit announces itself even
/// when it has no control room: that is how an unadopted unit gets found.
pub fn start() void {
    if (!deviceId(&id_z)) return;
    _ = c.sebastian_ensure_device_secret();
    _ = std.fmt.bufPrintZ(&prof_z, "{s}", .{profile.nameOf(profile.active)}) catch {};
    const cr: [*:0]const u8 = originZ() orelse "";
    c.sebastian_announce_start(@ptrCast(&id_z), @ptrCast(&prof_z), cr);
    c.sebastian_adopt_start();
    // esp_http_client runs on this task: 4KB is the measured floor for a plain
    // HTTP GET (internal RAM is the scarce pool — see usb_mic/convivencia).
    _ = c.xTaskCreatePinnedToCore(pollTask, "ctl_poll", 4096, null, 2, null, 0);
    log.info("control-plane poll every {d}s", .{POLL_PERIOD_MS / 1000});
}
