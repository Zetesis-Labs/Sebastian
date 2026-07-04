//! XVF3800 I2S sample conversion, shared by the two consumers of the mic:
//! mic_src.zig (LiveKit session) and wakeword.zig (idle detection).
//!
//! Both read the same interleaved 48 kHz stereo 32-bit stream and must apply
//! the exact same slot selection, gain and limiting — if they diverge, the
//! wake word model and the agent hear different audio.

const std = @import("std");
const cfg = @import("config.zig");

/// Stereo slot index of the configured channel (config.zig) in the
/// interleaved I2S stream. Comptime — zero runtime cost.
pub const SLOT: usize = if (cfg.mic_channel == .left) 0 else 1;

/// Per-channel gain shift, 32-bit slot → 16-bit PCM. RIGHT/ASR sits well at 14;
/// LEFT/comms is hotter (AGC + limiter) so it needs one more shift to land at
/// the same level without slamming the soft limiter.
pub const SHIFT: u5 = if (cfg.mic_channel == .left) 15 else 14;

/// Convert one raw 32-bit slot value to 16-bit PCM: gain shift + soft limit.
pub fn convert(raw: i32) i16 {
    return softClip(raw >> SHIFT);
}

/// Soft peak limiter: below the knee samples pass through linearly; above it,
/// peaks are compressed smoothly toward (but never reaching) full-scale instead
/// of hard-clipping. Hard clipping is what sounds harsh/"tin can" on loud
/// syllables; this keeps the loudness while taming the peaks.
pub fn softClip(x: i32) i16 {
    const knee: f32 = 24000.0;
    const lim: f32 = 32767.0;
    const a: f32 = @floatFromInt(@abs(x));
    if (a <= knee) return @intCast(x); // safe: |x| <= 24000 < i16 max
    const sign: f32 = if (x < 0) -1.0 else 1.0;
    const comp = knee + (lim - knee) * std.math.tanh((a - knee) / (lim - knee));
    return @intFromFloat(sign * comp);
}
