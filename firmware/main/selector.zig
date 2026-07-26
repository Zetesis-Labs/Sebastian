//! Boot profile selector — imperative shell over core/selector_core.zig.
//!
//! Runs once during boot, after the XVF is confirmed (button readable over
//! I2C, LED ring writable) and BEFORE xvf_ui starts (exclusive LED access).
//! Double-tap the mute button right after plugging in → the ring shows one
//! colored arc per provisioned profile; each further press advances the
//! highlighted arc; leaving the button alone confirms and boot continues
//! straight into the chosen profile (persisted, no restart).

const std = @import("std");
const c = @import("csdk.zig");
const core = @import("core/selector_core.zig");
const profile = @import("profile.zig");
const xvf = @import("xvf_dfu.zig");

const log = std.log.scoped(.selector);

/// XVF LED ring wire order is BGR (calibrated on hardware — see xvf_ui.rgb).
fn rgb(r: u8, g: u8, b: u8) [3]u8 {
    return .{ b, g, r };
}

const OFF = [3]u8{ 0, 0, 0 };
// One hue per profile index, matched to the built-in order (0 = agente → blue,
// 1 = micro-usb → amber); extra provisioned profiles take the next hues.
const PALETTE = [_][3]u8{
    rgb(10, 40, 90), // azul
    rgb(90, 55, 0), // ámbar
    rgb(0, 90, 20), // verde
    rgb(80, 0, 70), // magenta
    rgb(0, 70, 70), // cian
    rgb(60, 60, 60), // blanco
};

fn colorOf(index: usize) [3]u8 {
    return PALETTE[index % PALETTE.len];
}

fn dimmed(col: [3]u8) [3]u8 {
    return .{ col[0] / 8, col[1] / 8, col[2] / 8 };
}

/// Ring split into `profile.count` arcs; the selected one at full brightness.
fn render(selected: usize) void {
    var pix = [_][3]u8{OFF} ** 12;
    for (0..12) |led| {
        const idx = led * profile.count / 12;
        pix[led] = if (idx == selected) colorOf(idx) else dimmed(colorOf(idx));
    }
    xvf.setLeds(pix);
}

fn confirmFlash(index: usize) void {
    for (0..3) |_| {
        xvf.setLeds(.{colorOf(index)} ** 12);
        c.vTaskDelay(120);
        xvf.setLeds(.{OFF} ** 12);
        c.vTaskDelay(120);
    }
}

/// Watch for the double tap during the trigger window; no-op without it.
pub fn maybeRun() void {
    var trig = core.Trigger{};
    while (!trig.expired()) {
        if (trig.feed(xvf.readMuted())) return runSelector();
        c.vTaskDelay(core.TICK_MS);
    }
}

fn runSelector() void {
    log.info("profile selector: {d} profile(s), active '{s}'", .{
        profile.count, profile.nameOf(profile.active),
    });
    var sel = core.Selector{ .selected = profile.active, .count = profile.count };
    var rendered: ?usize = null;
    while (true) {
        if (sel.feed(xvf.readMuted())) |chosen| {
            profile.selectByIndex(chosen);
            log.info("profile selected: '{s}'", .{profile.nameOf(chosen)});
            confirmFlash(chosen);
            return;
        }
        if (rendered != sel.selected) {
            render(sel.selected);
            rendered = sel.selected;
        }
        c.vTaskDelay(core.TICK_MS);
    }
}
