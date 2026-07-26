//! Provisioning profiles — Zig side of profiles.c.
//!
//! A profile is a named capability bundle in NVS: which app the device boots
//! into (agent / USB mic) plus optional overrides of the config.zig audio keys.
//! Load order at boot: cfg.load() (legacy keys / compiled defaults) →
//! profile.load() → profile.applyActive() (profile fields win when set).
//!
//! The list lives here as a fixed global so the boot selector can iterate it
//! without allocation; profiles.c owns parsing, storage and the boot-fail
//! counter.

const std = @import("std");
const cfg = @import("config.zig");

const log = std.log.scoped(.profile);

pub const NAME_MAX = 24;
pub const MAX = 6;

pub const Mode = enum(u8) { agent = 0, usb_mic = 1 };

pub const CProfile = extern struct {
    name: [NAME_MAX]u8,
    mode: u8,
    full_duplex: i8,
    fixed_beam: i8,
    beam_az: i32,
    wifi: i8,
    telemetry: i8,
};

// Keep in sync with profiles.h (abi_check.c asserts the C half).
comptime {
    std.debug.assert(@sizeOf(CProfile) == 36);
    std.debug.assert(@offsetOf(CProfile, "name") == 0);
    std.debug.assert(@offsetOf(CProfile, "mode") == 24);
    std.debug.assert(@offsetOf(CProfile, "full_duplex") == 25);
    std.debug.assert(@offsetOf(CProfile, "fixed_beam") == 26);
    std.debug.assert(@offsetOf(CProfile, "beam_az") == 28);
    std.debug.assert(@offsetOf(CProfile, "wifi") == 32);
    std.debug.assert(@offsetOf(CProfile, "telemetry") == 33);
}

extern fn sebastian_profiles_load(out: [*]CProfile, max: c_int) c_int;
extern fn sebastian_profiles_active_index(list: [*]const CProfile, count: c_int) c_int;
extern fn sebastian_profiles_set_active(name: [*:0]const u8) bool;
extern fn sebastian_boot_note_start() i32;
extern fn sebastian_boot_note_ok() void;

pub var list: [MAX]CProfile = undefined;
pub var count: usize = 0;
pub var active: usize = 0;

pub fn load() void {
    count = @intCast(@max(0, sebastian_profiles_load(&list, MAX)));
    active = @intCast(@max(0, sebastian_profiles_active_index(&list, @intCast(count))));
    if (active >= count) active = 0;
    log.info("{d} profile(s), active: {s} (mode={s})", .{
        count, nameOf(active), @tagName(activeMode()),
    });
}

pub fn nameOf(index: usize) []const u8 {
    const raw: [*:0]const u8 = @ptrCast(&list[index].name);
    return std.mem.span(raw);
}

pub fn activeMode() Mode {
    if (list[active].mode > @intFromEnum(Mode.usb_mic)) return .agent;
    return @enumFromInt(list[active].mode);
}

/// Overlay the active profile's set fields onto the runtime config. Unset
/// fields (-1 / INT32_MIN sentinels) keep whatever cfg.load() produced.
pub fn applyActive() void {
    const p = &list[active];
    if (p.full_duplex >= 0) cfg.full_duplex = p.full_duplex == 1;
    if (p.fixed_beam >= 0) cfg.fixed_beam = p.fixed_beam == 1;
    if (p.beam_az != std.math.minInt(i32)) cfg.fixed_beam_azimuth_deg = @floatFromInt(p.beam_az);
    log.info("profile '{s}' applied: full_duplex={} fixed_beam={} beam_az={d}", .{
        nameOf(active), cfg.full_duplex, cfg.fixed_beam, cfg.fixed_beam_azimuth_deg,
    });
}

/// Persist the selector's choice and switch the in-memory active index so the
/// boot continues straight into the chosen profile (no restart needed).
pub fn selectByIndex(index: usize) void {
    if (index >= count) return;
    const raw: [*:0]const u8 = @ptrCast(&list[index].name);
    if (!sebastian_profiles_set_active(raw)) {
        log.err("could not persist profile '{s}' — continuing unselected", .{nameOf(index)});
        return;
    }
    active = index;
}

/// Three straight failed boots: leave the (possibly broken) chosen profile and
/// fall back to the first agent profile — the mode that is always administrable
/// (serial provisioning + web installer). In-memory ON PURPOSE, never persisted:
/// a transient failure must not rewrite the provisioned choice (field-validated
/// — WiFi being down during agent boots persisted 'agente' over 'micro-usb').
/// While failures last the fallback re-fires every boot; one healthy boot
/// clears the counter and the provisioned profile returns.
pub fn forceAgentFallback() void {
    for (0..count) |i| {
        if (list[i].mode == @intFromEnum(Mode.agent)) {
            log.err("boot-failure fallback: forcing agent profile '{s}' for this boot", .{nameOf(i)});
            active = i;
            return;
        }
    }
    log.err("boot-failure fallback: no agent profile provisioned — keeping '{s}'", .{nameOf(active)});
}

pub fn bootNoteStart() i32 {
    return sebastian_boot_note_start();
}

pub fn bootNoteOk() void {
    sebastian_boot_note_ok();
}
