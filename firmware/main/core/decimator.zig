//! Pure 48 kHz stereo → 16 kHz mono decimator used by wake word and barge-in.

const std = @import("std");
const pcm = @import("../xvf_pcm.zig");

pub const PAIRS_48K = 480; // stereo pairs per 10ms at 48kHz
pub const SAMPLES_16K = 160; // mono samples per 10ms at 16kHz
pub const FIR_TAPS = 19;

pub const FIR_Q15 = [FIR_TAPS]i32{ 91, 104, -15, -434, -923, -655, 1210, 4534, 7849, 9246, 7849, 4534, 1210, -655, -923, -434, -15, 104, 91 };

pub const Result = struct {
    samples: usize,
    peak: i32,
    mean: i32,
};

pub const Decimator = struct {
    history: [FIR_TAPS]i32 = [_]i32{0} ** FIR_TAPS,
    phase: u2 = 0,
    pending: i16 = 0,

    pub fn reset(self: *Decimator) void {
        @memset(self.history[0..], 0);
        self.phase = 0;
        self.pending = 0;
    }

    /// Anti-aliased 3:1 decimation of the configured slot.
    ///
    /// `stereo` is interleaved L/R 32-bit I2S samples. `out` receives s16 PCM.
    pub fn decimate(self: *Decimator, stereo: []const i32, out: []i16) Result {
        const got_pairs = @min(PAIRS_48K, stereo.len / 2);
        var peak: i32 = 0;
        var sum: i64 = 0;
        var out_samples: usize = 0;

        for (0..got_pairs) |i| {
            std.mem.copyForwards(i32, self.history[0 .. FIR_TAPS - 1], self.history[1..FIR_TAPS]);
            self.history[FIR_TAPS - 1] = stereo[i * 2 + pcm.SLOT] >> pcm.SHIFT;

            if (self.phase == 0) {
                var acc: i64 = 0;
                inline for (0..FIR_TAPS) |t| {
                    acc += @as(i64, FIR_Q15[t]) * @as(i64, self.history[FIR_TAPS - 1 - t]);
                }
                self.pending = pcm.softClip(@intCast(acc >> 15));
            }

            self.phase = if (self.phase == 2) 0 else self.phase + 1;
            if (self.phase == 0 and out_samples < out.len) {
                const s = self.pending;
                out[out_samples] = s;
                out_samples += 1;
                sum += s;
                peak = @max(peak, @as(i32, @intCast(@abs(s))));
            }
        }

        const mean: i32 = if (out_samples > 0) @intCast(@divTrunc(sum, @as(i64, @intCast(out_samples)))) else 0;
        return .{ .samples = out_samples, .peak = peak, .mean = mean };
    }
};
