//! One-shot DFU updater for the XVF3800 over I2C (address 0x2C).
//!
//! The board needs the XVF running the "inthost" (I2S master) firmware so it
//! drives the I2S clock and streams the mic. We ship that firmware embedded and
//! push it over the XMOS control protocol (resid 240) if the chip reports a
//! different version. Protocol ported from the ReSpeaker reference driver.
//!
//! The XVF keeps a factory image (ALTERNATE_FACTORY) that we never touch, so a
//! failed upgrade cannot brick the chip.

const std = @import("std");
const c = @import("csdk.zig");

const log = std.log.scoped(.xvf_dfu);

const XVF_ADDR: u16 = 0x2C;

const RESID_DFU: u8 = 240;
const READ_BIT: u8 = 0x80;
const CMD_DNLOAD: u8 = 1;
const CMD_GETSTATUS: u8 = 3;
const CMD_SETALTERNATE: u8 = 64;
const CMD_GETVERSION: u8 = 88;
const CMD_REBOOT: u8 = 89;
const ALTERNATE_UPGRADE: u8 = 1;
const MAX_XFER: usize = 128;

// DFU state machine values (from the XMOS firmware enum).
const STATE_DFU_IDLE: u8 = 2;
const STATE_DNLOAD_IDLE: u8 = 5;
const STATE_MANIFEST_WAIT_RESET: u8 = 8;

const FirmwareVersion = [3]u8;
const TARGET = FirmwareVersion{ 1, 0, 7 };

const firmware = @embedFile("xvf_fw/xvf_master_1.0.7.bin");

var dev: c.i2c_master_dev_handle_t = null;

pub const Error = error{
    I2cDeviceAddFailed,
    VersionReadFailed,
    DfuSetAlternateFailed,
    DfuNotReady,
    DfuBlockWriteFailed,
    DfuFinalBlockFailed,
    DfuRebootNotReady,
    DfuVerifyReadFailed,
    DfuVersionMismatch,
    UnmuteFailed,
};

fn millis() i64 {
    return @divTrunc(c.esp_timer_get_time(), 1000);
}

fn write(buf: []const u8) bool {
    return c.i2c_master_transmit(dev, buf.ptr, buf.len, 1000) == c.ESP_OK;
}

fn read(buf: []u8) bool {
    return c.i2c_master_receive(dev, buf.ptr, buf.len, 1000) == c.ESP_OK;
}

// The XVF control protocol is a two-transaction exchange (write request, read
// response). Multiple tasks talk to the chip (xvf_ui polls DoA every 80ms,
// app_main runs AEC diagnostics), and an interleaved exchange can hand one
// task the response to another task's command — with a valid status byte, so
// the corruption is silent. Every exchange must hold this lock end to end.
// Atomic + 1-tick sleep is fine at two contenders; if more tasks start talking
// to the XVF, migrate to a FreeRTOS mutex (priority inheritance, no busy wait).
var ctrl_lock = std.atomic.Value(bool).init(false);

fn lockCtrl() void {
    while (ctrl_lock.swap(true, .acquire)) c.vTaskDelay(1);
}

fn unlockCtrl() void {
    ctrl_lock.store(false, .release);
}

/// One atomic control exchange: write `req`; if `resp` is given, read it in
/// the same critical section.
fn xfer(req: []const u8, resp: ?[]u8) bool {
    lockCtrl();
    defer unlockCtrl();
    if (!write(req)) return false;
    if (resp) |r| return read(r);
    return true;
}

/// Atomic write-request + read-response for other modules (xvf_aec).
pub fn ctrlTransfer(req: []const u8, resp: []u8) bool {
    return xfer(req, resp);
}

/// Atomic write-only command for other modules.
pub fn ctrlWriteOnly(req: []const u8) bool {
    return xfer(req, null);
}

/// Read the running firmware version. Returns null on I2C/control failure.
fn getVersion() ?FirmwareVersion {
    var attempt: u32 = 0;
    while (attempt < 10) : (attempt += 1) {
        const req = [_]u8{ RESID_DFU, CMD_GETVERSION | READ_BIT, 4 };
        var resp: [4]u8 = undefined;
        if (xfer(&req, &resp) and resp[0] == 0) {
            return .{ resp[1], resp[2], resp[3] };
        }
        c.vTaskDelay(50);
    }
    return null;
}

/// Poll DFU status until the chip is in a ready state (or timeout).
fn waitReady(timeout_ms: i64) bool {
    const start = millis();
    while (millis() - start < timeout_ms) {
        const req = [_]u8{ RESID_DFU, CMD_GETSTATUS | READ_BIT, 6 };
        var resp: [6]u8 = undefined;
        if (xfer(&req, &resp)) {
            const state = resp[5];
            if (state == STATE_DFU_IDLE or state == STATE_DNLOAD_IDLE or state == STATE_MANIFEST_WAIT_RESET) {
                return true;
            }
            const delay_ms: u32 = @as(u32, resp[2]) | (@as(u32, resp[3]) << 8) | (@as(u32, resp[4]) << 16);
            c.vTaskDelay(@max(delay_ms, 2));
        } else {
            c.vTaskDelay(5);
        }
    }
    return false;
}

/// Unmute the mic. The XVF boots with its internal mute GPIO (30) asserted, so
/// it clocks the I2S bus but streams silence until we clear it. Command routes
/// through the GPO servicer: {resid=20, WRITE_VALUE=1, len=2, gpio=30, value=0}.
pub fn unmute() Error!void {
    const req = [_]u8{ 20, 1, 2, 30, 0 };
    if (xfer(&req, null)) {
        log.info("XVF mic unmute sent (GPIO30=0)", .{});
    } else {
        log.err("XVF unmute failed", .{});
        return error.UnmuteFailed;
    }
    readMuteStatus();
}

/// Read back the mute GPIO to confirm the unmute took. Bit is gpo_values[1]&0x01
/// in the GPO read response {status, gpo0..gpo4}.
pub fn readMuteStatus() void {
    const req = [_]u8{ 20, 0 | READ_BIT, 6 };
    var resp: [6]u8 = undefined;
    if (xfer(&req, &resp)) {
        const muted = (resp[2] & 0x01) != 0;
        log.info("XVF mute readback: {s} (status=0x{X} gpo={X} {X} {X} {X} {X})", .{ if (muted) "MUTED" else "UNMUTED", resp[0], resp[1], resp[2], resp[3], resp[4], resp[5] });
    } else {
        log.err("XVF mute readback failed", .{});
    }
}

// --- GPO servicer (LED ring + mute), same 0x2C I2C control interface ----------
const RESID_GPO: u8 = 20;
const GPO_READ: u8 = 0;
const GPO_WRITE: u8 = 1;
const GPO_LED_RING: u8 = 18;

/// True if the XVF mic is currently muted (GPIO 30 asserted).
pub fn readMuted() bool {
    const req = [_]u8{ RESID_GPO, GPO_READ | READ_BIT, 6 };
    var resp: [6]u8 = undefined;
    if (xfer(&req, &resp)) return (resp[2] & 0x01) != 0;
    return false;
}

/// Mute/unmute the mic at the source (XVF GPIO 30). Muting makes the XVF stream
/// silence, so the agent hears nothing.
pub fn setMute(muted: bool) void {
    const req = [_]u8{ RESID_GPO, GPO_WRITE, 2, 30, if (muted) 1 else 0 };
    _ = xfer(&req, null);
}

/// Paint the 12 LEDs individually (GPO servicer cmd 18, 48-byte payload =
/// 12 × {R, G, B, 0}).
pub fn setLeds(pix: [12][3]u8) void {
    var req: [3 + 48]u8 = undefined;
    req[0] = RESID_GPO;
    req[1] = GPO_LED_RING;
    req[2] = 48;
    var i: usize = 0;
    while (i < 12) : (i += 1) {
        req[3 + i * 4 + 0] = pix[i][0];
        req[3 + i * 4 + 1] = pix[i][1];
        req[3 + i * 4 + 2] = pix[i][2];
        req[3 + i * 4 + 3] = 0;
    }
    _ = xfer(&req, null);
}

/// All 12 LEDs one colour.
pub fn setLedRing(r: u8, g: u8, b: u8) void {
    setLeds(.{.{ r, g, b }} ** 12);
}

// --- AEC servicer: beam direction (DoA) for the LED ring ----------------------
const RESID_AEC: u8 = 33;
const AEC_AZIMUTH: u8 = 75;

/// Read the auto-selected beam azimuth and map it to an LED index (0-11, one per
/// 30°). Returns null when the chip has no fresh source to localise (silence) or
/// on I2C failure — the caller should hold the last direction. Response is
/// {status, 4×f32 radians: beam1, beam2, free-run, auto-select}.
pub fn readBeamLed() ?u8 {
    var attempt: u8 = 0;
    while (attempt < 8) : (attempt += 1) {
        const req = [_]u8{ RESID_AEC, AEC_AZIMUTH | READ_BIT, 17 };
        var resp: [17]u8 = undefined;
        if (!xfer(&req, &resp)) return null;
        if (resp[0] == 0) { // CTRL_DONE
            const off = 1 + 3 * 4; // beam index 3 = auto-select
            const u: u32 = @as(u32, resp[off]) | (@as(u32, resp[off + 1]) << 8) |
                (@as(u32, resp[off + 2]) << 16) | (@as(u32, resp[off + 3]) << 24);
            const radians: f32 = @bitCast(u);
            // Guard before @intFromFloat: this float comes from an I2C read and
            // a garbage value would panic-reboot the device (same crash class
            // as the AEC telemetry). Azimuth is [-2π, 2π]; reject anything else.
            if (std.math.isNan(radians) or radians < -7.0 or radians > 7.0) return null;
            const idx_f = @round(radians * (6.0 / std.math.pi)); // 12 LEDs / 2π
            const i: i32 = @mod(@as(i32, @intFromFloat(idx_f)), 12);
            return @intCast(i);
        }
        c.vTaskDelay(1); // CTRL_WAIT / retry
    }
    return null;
}

fn setAlternate() bool {
    const req = [_]u8{ RESID_DFU, CMD_SETALTERNATE, 1, ALTERNATE_UPGRADE };
    return xfer(&req, null);
}

fn reboot() bool {
    const req = [_]u8{ RESID_DFU, CMD_REBOOT, 1, 0 };
    return xfer(&req, null);
}

/// Send one DNLOAD block. `data` is up to MAX_XFER bytes; the request always
/// carries a full 128-byte payload (zero-padded), with byte [3] = valid count.
fn sendBlock(data: []const u8) bool {
    var req = [_]u8{0} ** (5 + MAX_XFER); // 133 bytes
    req[0] = RESID_DFU;
    req[1] = CMD_DNLOAD;
    req[2] = 130; // MAX_XFER + 2
    req[3] = @intCast(data.len);
    req[4] = 0;
    @memcpy(req[5 .. 5 + data.len], data);
    return xfer(&req, null);
}

fn isTarget(version: FirmwareVersion) bool {
    return std.mem.eql(u8, &version, &TARGET);
}

fn addDevice(bus: c.i2c_master_bus_handle_t) Error!void {
    var dev_cfg = std.mem.zeroes(c.i2c_device_config_t);
    dev_cfg.dev_addr_length = c.I2C_ADDR_BIT_LEN_7;
    dev_cfg.device_address = XVF_ADDR;
    dev_cfg.scl_speed_hz = 100000;
    if (c.i2c_master_bus_add_device(bus, &dev_cfg, &dev) == c.ESP_OK) return;

    log.err("could not add XVF I2C device", .{});
    return error.I2cDeviceAddFailed;
}

fn readVersionOrError(comptime context: []const u8, comptime err: Error) Error!FirmwareVersion {
    return getVersion() orelse {
        log.err("{s}", .{context});
        return err;
    };
}

fn requireReady(timeout_ms: i64, comptime context: []const u8, comptime err: Error) Error!void {
    if (waitReady(timeout_ms)) return;

    log.err("{s}", .{context});
    return err;
}

fn beginUpgrade() Error!void {
    log.info("DFU: updating XVF to {d}.{d}.{d} ({d} bytes)...", .{ TARGET[0], TARGET[1], TARGET[2], firmware.len });
    if (setAlternate()) return;

    log.err("DFU set-alternate failed", .{});
    return error.DfuSetAlternateFailed;
}

fn writeFirmwareImage() Error!void {
    var written: usize = 0;
    var last_log: i64 = millis();
    while (written < firmware.len) {
        try requireReady(4000, "DFU: not ready during download", error.DfuNotReady);

        const n = @min(MAX_XFER, firmware.len - written);
        if (!sendBlock(firmware[written .. written + n])) {
            log.err("DFU: block write failed at {d}", .{written});
            return error.DfuBlockWriteFailed;
        }

        written += n;
        if (millis() - last_log > 1000) {
            last_log = millis();
            log.info("DFU progress: {d}%", .{written * 100 / firmware.len});
        }
    }
}

fn finishDownload() Error!void {
    try requireReady(4000, "DFU: not ready before final block", error.DfuNotReady);
    if (sendBlock(firmware[0..0])) return;

    log.err("DFU: final block failed", .{});
    return error.DfuFinalBlockFailed;
}

fn verifyAfterReboot() Error!void {
    try requireReady(8000, "DFU: not ready before reboot", error.DfuRebootNotReady);
    log.info("DFU download done — rebooting XMOS...", .{});
    _ = reboot();

    c.vTaskDelay(4000); // let the XVF reboot into the new firmware
    const after = try readVersionOrError("DFU: could not read version after reboot", error.DfuVerifyReadFailed);
    if (isTarget(after)) {
        log.info("DFU complete — XVF now {d}.{d}.{d}", .{ after[0], after[1], after[2] });
        return;
    }

    log.err("DFU verify mismatch — XVF reports {d}.{d}.{d}", .{ after[0], after[1], after[2] });
    return error.DfuVersionMismatch;
}

fn updateFirmware() Error!void {
    try beginUpgrade();
    try writeFirmwareImage();
    try finishDownload();
    try verifyAfterReboot();
}

/// Ensure the XVF3800 runs the target (master) firmware. Blocking; runs once at
/// boot. Returns only after the target version is confirmed, either because it
/// was already running or because DFU completed and verified.
pub fn ensureMaster(bus: c.i2c_master_bus_handle_t) Error!void {
    try addDevice(bus);

    const before = try readVersionOrError("could not read XVF version — skipping DFU", error.VersionReadFailed);
    log.info("XVF firmware version: {d}.{d}.{d}", .{ before[0], before[1], before[2] });
    if (isTarget(before)) {
        log.info("XVF already on target {d}.{d}.{d} — no DFU needed", .{ TARGET[0], TARGET[1], TARGET[2] });
        return;
    }

    try updateFirmware();
}
