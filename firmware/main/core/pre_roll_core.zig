//! Pure pre-roll helpers, shared by firmware and host tests.

const std = @import("std");

pub const SAMPLE_RATE: u16 = 16000;
pub const SECONDS: usize = 12;
pub const SAMPLE_CAPACITY: usize = SAMPLE_RATE * SECONDS;
pub const HEADER_BYTES: usize = 16;
pub const WAKE_LEAD_SAMPLES: usize = SAMPLE_RATE * 2;
/// Hard cap on what one send may carry. The publisher data channel buffers
/// sends in a 100 KB cache (peer.c); a slow connect grew this window to
/// 140-198 KB in the field and the sessions that followed ran on a corrupted
/// heap. 2.5 s = 80 KB keeps clear of the cache and, because the window is
/// the NEWEST samples, still holds the command spoken while connecting.
pub const MAX_WINDOW_SAMPLES: usize = @as(usize, SAMPLE_RATE) * 5 / 2;

pub fn windowSamples(wake_mark: u64, total_written: u64, filled: usize) usize {
    if (total_written < wake_mark) return 0;
    const lead: u64 = @min(wake_mark, WAKE_LEAD_SAMPLES);
    const since_mark = total_written - wake_mark;
    const window: u64 = @min(@as(u64, filled), since_mark + lead);
    return @intCast(@min(window, @as(u64, MAX_WINDOW_SAMPLES)));
}

pub fn availableMs(sample_count: usize) u32 {
    return @intCast(sample_count * 1000 / SAMPLE_RATE);
}

pub fn startIndex(write_idx: usize, count: usize) usize {
    return (write_idx + SAMPLE_CAPACITY - (count % SAMPLE_CAPACITY)) % SAMPLE_CAPACITY;
}

fn putU16LE(out: *[HEADER_BYTES]u8, offset: usize, value: u16) void {
    out[offset] = @truncate(value);
    out[offset + 1] = @truncate(value >> 8);
}

fn putU32LE(out: *[HEADER_BYTES]u8, offset: usize, value: u32) void {
    out[offset] = @truncate(value);
    out[offset + 1] = @truncate(value >> 8);
    out[offset + 2] = @truncate(value >> 16);
    out[offset + 3] = @truncate(value >> 24);
}

pub fn makeHeader(wake_id: u32, sample_count: u32) [HEADER_BYTES]u8 {
    var out = [_]u8{0} ** HEADER_BYTES;
    out[0] = 'S';
    out[1] = 'B';
    out[2] = 'P';
    out[3] = 'R';
    out[4] = 1; // version
    out[5] = 0; // reserved
    putU16LE(&out, 6, SAMPLE_RATE);
    putU32LE(&out, 8, sample_count);
    putU32LE(&out, 12, wake_id);
    return out;
}

pub const WindowState = struct {
    write_idx: usize = 0,
    filled: usize = 0,
    total_written: u64 = 0,
    wake_mark: u64 = 0,

    pub fn reset(self: *WindowState) void {
        self.* = .{};
    }

    pub fn noteSample(self: *WindowState) void {
        self.write_idx = (self.write_idx + 1) % SAMPLE_CAPACITY;
        if (self.filled < SAMPLE_CAPACITY) self.filled += 1;
        self.total_written += 1;
    }

    pub fn markWake(self: *WindowState) void {
        self.wake_mark = self.total_written;
    }

    pub fn windowSamples(self: WindowState) usize {
        return pre_roll_core.windowSamples(self.wake_mark, self.total_written, self.filled);
    }

    pub fn availableMs(self: WindowState) u32 {
        return pre_roll_core.availableMs(self.windowSamples());
    }

    pub fn startIndex(self: WindowState) usize {
        return pre_roll_core.startIndex(self.write_idx, self.windowSamples());
    }
};

const pre_roll_core = @This();
