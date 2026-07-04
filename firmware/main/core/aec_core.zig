//! Pure helpers for XVF AEC command encoding and defensive telemetry math.

const std = @import("std");

pub const F32Pair = struct {
    first: f32,
    second: f32,
};

pub fn floatsClose(actual: f32, expected: f32, eps: f32) bool {
    if (std.math.isNan(actual) or std.math.isNan(expected)) return false;
    return @abs(actual - expected) <= eps;
}

/// Float → scaled integer for logging. NaN/null/silly I2C values must never
/// panic in @intFromFloat on-device.
pub fn scaled(v: ?f32, mul: f32) i32 {
    const x = v orelse return -1;
    const m = x * mul;
    if (std.math.isNan(m)) return -2;
    if (m >= 2147483000.0) return std.math.maxInt(i32);
    if (m <= -2147483000.0) return std.math.minInt(i32);
    return @intFromFloat(m);
}

pub fn milli(v: ?f32) i32 {
    return scaled(v, 1000.0);
}

pub fn writeI32LE(out: *[4]u8, value: i32) void {
    std.mem.writeInt(i32, out, value, .little);
}

pub fn readI32LE(in: *const [4]u8) i32 {
    return std.mem.readInt(i32, in, .little);
}

pub fn writeF32LE(out: *[4]u8, value: f32) void {
    const bytes: [4]u8 = @bitCast(value);
    out.* = bytes;
}

pub fn readF32LE(in: *const [4]u8) f32 {
    return @bitCast(std.mem.readInt(u32, in, .little));
}

pub fn readF32PairLE(in: *const [8]u8) F32Pair {
    return .{
        .first = readF32LE(in[0..4]),
        .second = readF32LE(in[4..8]),
    };
}

pub fn makeI32Write(resid: u8, cmd: u8, value: i32) [7]u8 {
    var b: [4]u8 = undefined;
    writeI32LE(&b, value);
    return [_]u8{ resid, cmd, 4, b[0], b[1], b[2], b[3] };
}

pub fn makeF32PairWrite(resid: u8, cmd: u8, a: f32, b: f32) [11]u8 {
    var ba: [4]u8 = undefined;
    var bb: [4]u8 = undefined;
    writeF32LE(&ba, a);
    writeF32LE(&bb, b);
    return [_]u8{ resid, cmd, 8, ba[0], ba[1], ba[2], ba[3], bb[0], bb[1], bb[2], bb[3] };
}
