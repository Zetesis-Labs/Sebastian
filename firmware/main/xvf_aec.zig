//! AEC reference-chain diagnostics over the XVF3800 control interface.
//!
//! The XVF's AEC needs the far-end reference (what the speaker plays) on its
//! I2S input to have anything to cancel. Before tuning delays or gains, these
//! reads answer the prior question: is the reference arriving, and has the AEC
//! ever converged against it?
//!
//! Command IDs extracted from the official command map (libcommand_map.dylib,
//! reSpeaker_XVF3800_USB_4MIC_ARRAY host_control) — resid/cmd/type verified
//! against the XVF3800 User Guide appendix:
//!
//!   AEC servicer (resid 33): AECPATHCHANGE=0, AECCONVERGED=3, FAR_EXTGAIN=5,
//!     RT60=9, NUM_FARENDS=72
//!   Audio manager (resid 35): MIC_GAIN=0, REF_GAIN=1, I2S_INACTIVE=24,
//!     SYS_DELAY=26
//!
//! Wire protocol (same as xvf_dfu): write [resid, cmd|0x80, n+1], read n+1
//! bytes where byte 0 is a status code (0 = OK) and the rest is the payload,
//! little-endian.

const std = @import("std");
const core = @import("core/aec_core.zig");
const dfu = @import("xvf_dfu.zig");
const board = @import("board.zig");
const c = @import("csdk.zig");
const cfg = @import("config.zig");

const log = std.log.scoped(.xvf_aec);

const READ_BIT: u8 = 0x80;
const RESID_AEC: u8 = 33;
const RESID_AUDIO_MGR: u8 = 35;

const AEC_AECPATHCHANGE: u8 = 0;
const AEC_AECSILENCELEVEL: u8 = 2; // 2×f32 (set,cur): power threshold for the adaptive filter; high ⇒ AEC treats far-end as silence
const AEC_AECCONVERGED: u8 = 3;
const AEC_FAR_EXTGAIN: u8 = 5;
const AEC_RT60: u8 = 9;
const SHF_BYPASS: u8 = 70;
const AEC_NUM_FARENDS: u8 = 72;
const AEC_CURRENT_IDLE_TIME: u8 = 77; // CPU-profiling idle counter (10ns ticks), NOT far-end activity
const AEC_SPENERGY_VALUES: u8 = 80;
// Adaptive-filter read (hidden cmds): write FAR_MIC_INDEX to trigger, read
// LENGTH, then loop START_OFFSET + COEFFS (15 floats/read). The tap magnitudes
// vs index tell us if/where the AEC is modelling the echo.
const AEC_FAR_MIC_INDEX: u8 = 90;
const AEC_FILTER_COEFF_START_OFFSET: u8 = 91;
const AEC_FILTER_COEFFS: u8 = 92;
const AEC_FILTER_LENGTH: u8 = 93;
const AEC_FILTER_ABORT: u8 = 94;
// Fixed (non-adaptive) beams: with these ON the beamformer stops tracking, so
// the mic→echo path is stationary — the test for the "jittering filter" cause.
const AEC_FIXEDBEAMSONOFF: u8 = 37;
const AEC_FIXEDBEAMSAZIMUTH: u8 = 81;
const AEC_FIXEDBEAMSELEVATION: u8 = 82;

const MGR_MIC_GAIN: u8 = 0;
const MGR_REF_GAIN: u8 = 1;
const MGR_I2S_INPUT_PACKED: u8 = 10;
const MGR_SELECTED_CHANNELS: u8 = 12;
const MGR_OP_R: u8 = 19;
const MGR_OP_ALL: u8 = 23;
const MGR_I2S_INACTIVE: u8 = 24;
const MGR_FAR_END_DSP_ENABLE: u8 = 25;
const MGR_SYS_DELAY: u8 = 26;
const MGR_I2S_DAC_DSP_ENABLE: u8 = 27;

// Audio-manager output mux categories (decoded from the official command map).
const MUX_SILENCE: u8 = 0;
const MUX_MICS_W_GAIN: u8 = 3;
const MUX_FAR_END: u8 = 4;
const MUX_FAR_END_SYSDELAY: u8 = 5;
const MUX_FAR_END_W_GAIN: u8 = 12;

const STATUS_RETRY: u8 = 0x40; // servicer busy fetching from the DSP — re-issue

fn readBytes(resid: u8, cmd: u8, comptime n: usize) ?[n]u8 {
    const req = [_]u8{ resid, cmd | READ_BIT, n + 1 };
    var resp: [n + 1]u8 = undefined;
    var attempt: u32 = 0;
    while (attempt < 30) : (attempt += 1) {
        if (!dfu.ctrlTransfer(&req, &resp)) return null;
        if (resp[0] == 0) return resp[1 .. n + 1].*;
        if (resp[0] != STATUS_RETRY) break;
        c.vTaskDelay(5);
    }
    log.warn("cmd {d}/{d} status={d}", .{ resid, cmd, resp[0] });
    return null;
}

fn readI32(resid: u8, cmd: u8) ?i32 {
    const b = readBytes(resid, cmd, 4) orelse return null;
    return core.readI32LE(&b);
}

fn readF32(resid: u8, cmd: u8) ?f32 {
    const b = readBytes(resid, cmd, 4) orelse return null;
    return core.readF32LE(&b);
}

fn readU8(resid: u8, cmd: u8) ?u8 {
    const b = readBytes(resid, cmd, 1) orelse return null;
    return b[0];
}

fn writeI32(resid: u8, cmd: u8, v: i32) bool {
    const req = core.makeI32Write(resid, cmd, v);
    return dfu.ctrlWriteOnly(&req);
}

fn writeF32Pair(resid: u8, cmd: u8, a: f32, b: f32) bool {
    const req = core.makeF32PairWrite(resid, cmd, a, b);
    return dfu.ctrlWriteOnly(&req);
}

fn floatsClose(actual: f32, expected: f32, eps: f32) bool {
    return core.floatsClose(actual, expected, eps);
}

const F32Pair = core.F32Pair;

fn readF32Pair(resid: u8, cmd: u8) ?F32Pair {
    const b = readBytes(resid, cmd, 8) orelse return null;
    return core.readF32PairLE(&b);
}

fn writeF32PairVerified(resid: u8, cmd: u8, a: f32, b: f32, comptime name: []const u8) bool {
    if (!writeF32Pair(resid, cmd, a, b)) {
        log.err(name ++ " write failed", .{});
        return false;
    }
    const rb = readF32Pair(resid, cmd) orelse {
        log.err(name ++ " readback failed", .{});
        return false;
    };
    const eps: f32 = 0.01; // radians (~0.6°) — float round-trip tolerance
    if (!floatsClose(rb.first, a, eps) or !floatsClose(rb.second, b, eps)) {
        log.err(name ++ " readback mismatch", .{});
        return false;
    }
    return true;
}

/// Read the adaptive AEC filter for one (far,mic) pair and log a summary. The
/// echo model lives in these taps: a flat filter (peak≈0, nonzero≈0) means the
/// AEC never adapted; a peak at index 0 means the reference lags the echo
/// (acausal — SYS_DELAY has the wrong sign/magnitude); a healthy peak at a
/// positive index means the path works and only the delay/convergence flag is
/// off. Read-only + ABORT to release the DSP snapshot.
fn logFilterSummary(far: i32, mic: i32) void {
    var idx: [8]u8 = undefined;
    core.writeI32LE(idx[0..4], far);
    core.writeI32LE(idx[4..8], mic);
    const trig = [_]u8{ RESID_AEC, AEC_FAR_MIC_INDEX, 8, idx[0], idx[1], idx[2], idx[3], idx[4], idx[5], idx[6], idx[7] };
    if (!dfu.ctrlWriteOnly(&trig)) {
        log.err("filter: far_mic_index write failed", .{});
        return;
    }
    c.vTaskDelay(50); // let the DSP snapshot the filter

    const length = readI32(RESID_AEC, AEC_FILTER_LENGTH) orelse {
        log.err("filter: length read failed", .{});
        return;
    };
    const n: usize = @intCast(@max(0, @min(length, 480))); // cap the read
    var peak: f32 = 0;
    var peak_idx: usize = 0;
    var nonzero: u32 = 0;
    var off: usize = 0;
    while (off < n) : (off += 15) {
        if (!writeI32(RESID_AEC, AEC_FILTER_COEFF_START_OFFSET, @intCast(off))) break;
        const b = readBytes(RESID_AEC, AEC_FILTER_COEFFS, 60) orelse break; // 15×f32
        const cnt = @min(@as(usize, 15), n - off);
        for (0..cnt) |i| {
            const f = core.readF32LE(b[i * 4 ..][0..4]);
            const a = @abs(f);
            if (std.math.isNan(a)) continue;
            if (a > 0.0005) nonzero += 1;
            if (a > peak) {
                peak = a;
                peak_idx = off + i;
            }
        }
    }
    _ = writeI32(RESID_AEC, AEC_FILTER_ABORT, 1);
    log.info("filter far={d} mic={d}: length={d} read={d} peak_idx={d} peak={d}m nonzero={d}", .{
        far, mic, length, n, peak_idx, milli(peak), nonzero,
    });
}

/// Float → milli-units integer for logging. MUST be overflow-safe: these
/// floats come from I2C reads, and one garbage/huge value fed to a bare
/// @intFromFloat panicked and REBOOTED the device mid-session (confirmed by
/// backtrace). NaN → -2, out-of-range clamps.
fn scaled(v: ?f32, mul: f32) i32 {
    return core.scaled(v, mul);
}

fn milli(v: ?f32) i32 {
    return core.milli(v);
}

/// Write a float command and confirm it took by reading it back. An ACKed
/// write that didn't take leaves the AEC misconfigured in a way no later
/// symptom points back here.
fn writeF32Verified(resid: u8, cmd: u8, v: f32, comptime name: []const u8) bool {
    const b: [4]u8 = @bitCast(v);
    const req = [_]u8{ resid, cmd, 4, b[0], b[1], b[2], b[3] };
    if (!dfu.ctrlWriteOnly(&req)) {
        log.err(name ++ " write failed", .{});
        return false;
    }
    const rb = readF32(resid, cmd) orelse {
        log.err(name ++ " readback failed", .{});
        return false;
    };
    if (!floatsClose(rb, v, 0.01)) {
        log.err(name ++ " readback {d}m != {d}m", .{ milli(rb), milli(v) });
        return false;
    }
    return true;
}

fn writeU8Verified(resid: u8, cmd: u8, v: u8, comptime name: []const u8) bool {
    const req = [_]u8{ resid, cmd, 1, v };
    if (!dfu.ctrlWriteOnly(&req)) {
        log.err(name ++ " write failed", .{});
        return false;
    }
    const rb = readU8(resid, cmd) orelse {
        log.err(name ++ " readback failed", .{});
        return false;
    };
    if (rb != v) {
        log.err(name ++ " readback {d} != {d}", .{ rb, v });
        return false;
    }
    return true;
}

fn writeI32Verified(resid: u8, cmd: u8, v: i32, comptime name: []const u8) bool {
    if (!writeI32(resid, cmd, v)) {
        log.err(name ++ " write failed", .{});
        return false;
    }
    const rb = readI32(resid, cmd) orelse {
        log.err(name ++ " readback failed", .{});
        return false;
    };
    if (rb != v) {
        log.err(name ++ " readback {d} != {d}", .{ rb, v });
        return false;
    }
    return true;
}

/// Fix the factory AEC configuration to meet the XMOS convergence preconditions.
///
/// Two factory gains violate the tuning guide simultaneously, and their fix was
/// never tested together:
///   - REF_GAIN=8.0 (default 1.5): ×8 at full-scale playback digitally clips the
///     AEC's internal reference → the linear filter can't correlate → no converge.
///   - MIC_GAIN=90 (default 10): the mic must sit ≥6 dB BELOW the reference or the
///     filter coefficients exceed 0 dB and destabilise. 90 puts it far above.
/// REF_GAIN=1.0 + MIC_GAIN=10 satisfies both. NOTE: mic_gain 90→10 drops the
/// captured ASR level 9× — xvf_pcm SHIFT must be re-tuned for production.
///
/// FAR_EXTGAIN is dB = external gain past the reference tap; our tap is after the
/// ESP sw-vol and the AIC3104 is 0 dB → 0.0 (factory) is correct. The 1.0 we
/// shipped earlier was a placebo from the FAR_EXTGAIN misdiagnosis.
pub fn applyConfig() bool {
    if (!writeF32Verified(RESID_AUDIO_MGR, MGR_REF_GAIN, 1.0, "REF_GAIN")) return false;
    if (!writeF32Verified(RESID_AEC, AEC_FAR_EXTGAIN, 0.0, "FAR_EXTGAIN")) return false;
    // MIC_GAIN restored to factory 90 EXPLICITLY: the XVF keeps its config across
    // esp_restart, so the diagnostic mic_gain=10 would linger and starve the ASR
    // level. 90 is the level xvf_pcm SHIFT is tuned for. mic_gain=10 was tested
    // for AEC convergence and did NOT help — ASR level wins.
    if (!writeF32Verified(RESID_AUDIO_MGR, MGR_MIC_GAIN, 90.0, "MIC_GAIN")) return false;

    // Far-end DSP path. With this off the far-end activity detector reads no
    // energy (spenergy stays 0) and the AEC never adapts — converged=0 and the
    // loudspeaker couples straight into the mic. It was 1 during every successful
    // convergence run, but only as a leftover manual write: the XVF reverts to its
    // build default (0) on POWER-CYCLE (not esp_restart), so unplugging the unit
    // silently killed the AEC. Pin it here, fail closed like the beam writes.
    if (!writeU8Verified(RESID_AUDIO_MGR, MGR_FAR_END_DSP_ENABLE, 1, "FAR_END_DSP_ENABLE")) return false;

    // Fixed beam. The adaptive beamformer continuously changes the mic→echo path,
    // which prevents the AEC from ever locking a stable echo model (confirmed on
    // hardware: adaptive → never converges; fixed → converges in ~1s, filter peak
    // ~0.025 at a stable tap). Freezing it is the prerequisite for full-duplex, so
    // these writes MUST fail closed: if the beam is not actually fixed the AEC
    // won't converge, and a caller that trusts our `true` would open the mic on an
    // uncancelled echo. Always written explicitly (the XVF persists config across
    // esp_restart) so a unit that once had it on reverts cleanly when off.
    if (cfg.fixed_beam) {
        const az = cfg.fixed_beam_azimuth_deg * std.math.pi / 180.0;
        if (!writeF32PairVerified(RESID_AEC, AEC_FIXEDBEAMSAZIMUTH, az, az, "FIXEDBEAMSAZIMUTH")) return false;
        if (!writeF32PairVerified(RESID_AEC, AEC_FIXEDBEAMSELEVATION, 0.0, 0.0, "FIXEDBEAMSELEVATION")) return false;
    }
    if (!writeI32Verified(RESID_AEC, AEC_FIXEDBEAMSONOFF, if (cfg.fixed_beam) 1 else 0, "FIXEDBEAMSONOFF")) return false;

    log.info("AEC config applied & verified: ref_gain=1.0 far_extgain=0.0 mic_gain=90.0 far_end_dsp=1 fixed_beam={}", .{cfg.fixed_beam});
    return true;
}

// NOTE (2026-07-03): the first self-test's "routing problem" was WRONG — primary
// XMOS docs (multi-agent research) show AEC_CURRENT_IDLE_TIME is a CPU-profiling
// counter (10ns ticks), not far-end activity, so "never resets" meant nothing;
// and the far_end_w_gain mux the probe read IS the AEC's input, so the reference
// does reach it. inthost is an INT build (far-end by I2S slot 0, native, no
// source selector). Real cause = operating conditions: the untested gain combo
// above + probe excitation — a pure 1 kHz tone at 0.85 FS is single-band + hits
// speaker THD; XMOS converges on WHITE NOISE at moderate level (probe below).

/// One-shot configuration snapshot. Call at boot after the XVF is confirmed.
pub fn logConfig() void {
    const farends = readI32(RESID_AEC, AEC_NUM_FARENDS) orelse -1;
    const sys_delay = readI32(RESID_AUDIO_MGR, MGR_SYS_DELAY) orelse -999;
    const i2s_inactive = readU8(RESID_AUDIO_MGR, MGR_I2S_INACTIVE) orelse 255;
    const ref_gain = readF32(RESID_AUDIO_MGR, MGR_REF_GAIN);
    const mic_gain = readF32(RESID_AUDIO_MGR, MGR_MIC_GAIN);
    const far_extgain = readF32(RESID_AEC, AEC_FAR_EXTGAIN);
    log.info("config: farends={d} sys_delay={d} i2s_inactive={d} ref_gain={d}m mic_gain={d}m far_extgain={d}m", .{
        farends, sys_delay, i2s_inactive, milli(ref_gain), milli(mic_gain), milli(far_extgain),
    });

    const input_packed = readU8(RESID_AUDIO_MGR, MGR_I2S_INPUT_PACKED) orelse 255;
    if (readBytes(RESID_AUDIO_MGR, MGR_OP_ALL, 12)) |op| {
        log.info("routing: input_packed={d} op_all={d} {d} {d} {d} {d} {d} {d} {d} {d} {d} {d} {d}", .{
            input_packed, op[0], op[1], op[2], op[3], op[4], op[5], op[6], op[7], op[8], op[9], op[10], op[11],
        });
    } else {
        log.info("routing: input_packed={d} op_all=<read failed>", .{input_packed});
    }

    // Kill switches that would leave the AEC inert even with signal present.
    const bypass = readU8(RESID_AEC, SHF_BYPASS) orelse 255;
    const far_dsp = readU8(RESID_AUDIO_MGR, MGR_FAR_END_DSP_ENABLE) orelse 255;
    const dac_dsp = readU8(RESID_AUDIO_MGR, MGR_I2S_DAC_DSP_ENABLE) orelse 255;
    const sel = readBytes(RESID_AUDIO_MGR, MGR_SELECTED_CHANNELS, 2) orelse [2]u8{ 255, 255 };
    log.info("switches: shf_bypass={d} far_end_dsp_enable={d} i2s_dac_dsp_enable={d} selected_channels={d},{d}", .{
        bypass, far_dsp, dac_dsp, sel[0], sel[1],
    });

    // Adaptive-filter silence threshold (33/2, set+cur floats). Default ~1e-9;
    // scaled ×1e9 so a high value (ReSpeaker raising it = AEC treats far-end as
    // silence, never adapting) is visible where milli-units would read 0.
    if (readBytes(RESID_AEC, AEC_AECSILENCELEVEL, 8)) |b| {
        const set: f32 = @bitCast(std.mem.readInt(u32, b[0..4], .little));
        const cur: f32 = @bitCast(std.mem.readInt(u32, b[4..8], .little));
        log.info("aec detector: silence_level_set={d}n silence_level_cur={d}n (×1e9)", .{ scaled(set, 1e9), scaled(cur, 1e9) });
    }

    logPpConfig();
}

// ── Post-processor (comms channel) inventory ─────────────────────────────────
// The PP is the DSP we currently bypass by tapping the raw ASR beam: residual
// echo suppression (linear tail + NON-LINEAR — the full-duplex-with-tracking
// candidate), AGC (distance normalization), NS, limiter. Dumped read-only at
// boot so we know what THIS build ships (Seeed changed defaults before:
// REF_GAIN=8.0) before we start exploiting it.
const RESID_PP: u8 = 17;
const PP_AGCONOFF: u8 = 10;
const PP_AGCMAXGAIN: u8 = 11;
const PP_AGCDESIREDLEVEL: u8 = 12;
const PP_AGCGAIN: u8 = 13;
const PP_AGCTIME: u8 = 14;
const PP_LIMITONOFF: u8 = 19;
const PP_LIMITPLIMIT: u8 = 20;
const PP_MIN_NS: u8 = 21;
const PP_MIN_NN: u8 = 22;
const PP_ECHOONOFF: u8 = 23;
const PP_GAMMA_E: u8 = 24;
const PP_GAMMA_ETAIL: u8 = 25;
const PP_GAMMA_ENL: u8 = 26;
const PP_NLATTENONOFF: u8 = 27;
const PP_NLAEC_MODE: u8 = 28;
const PP_FMIN_SPEINDEX: u8 = 30;
const PP_DTSENSITIVE: u8 = 31;
const PP_ATTNS_MODE: u8 = 32;
const PP_ATTNS_NOMINAL: u8 = 33;
const PP_ATTNS_SLOPE: u8 = 34;

fn logPpConfig() void {
    log.info("pp echo: echoonoff={d} gamma_e={d}m gamma_etail={d}m gamma_enl={d}m nlatten={d} nlaec_mode={d} dtsensitive={d}", .{
        readI32(RESID_PP, PP_ECHOONOFF) orelse -1,
        milli(readF32(RESID_PP, PP_GAMMA_E)),
        milli(readF32(RESID_PP, PP_GAMMA_ETAIL)),
        milli(readF32(RESID_PP, PP_GAMMA_ENL)),
        readI32(RESID_PP, PP_NLATTENONOFF) orelse -1,
        readI32(RESID_PP, PP_NLAEC_MODE) orelse -1,
        readI32(RESID_PP, PP_DTSENSITIVE) orelse -1,
    });
    log.info("pp agc: on={d} maxgain={d}m desired={d}m gain={d}m time={d}m limiter={d} plimit={d}m", .{
        readI32(RESID_PP, PP_AGCONOFF) orelse -1,
        milli(readF32(RESID_PP, PP_AGCMAXGAIN)),
        milli(readF32(RESID_PP, PP_AGCDESIREDLEVEL)),
        milli(readF32(RESID_PP, PP_AGCGAIN)),
        milli(readF32(RESID_PP, PP_AGCTIME)),
        readI32(RESID_PP, PP_LIMITONOFF) orelse -1,
        milli(readF32(RESID_PP, PP_LIMITPLIMIT)),
    });
    log.info("pp ns: min_ns={d}m min_nn={d}m attns_mode={d} attns_nominal={d}m attns_slope={d}m fmin_speindex={d}m", .{
        milli(readF32(RESID_PP, PP_MIN_NS)),
        milli(readF32(RESID_PP, PP_MIN_NN)),
        readI32(RESID_PP, PP_ATTNS_MODE) orelse -1,
        milli(readF32(RESID_PP, PP_ATTNS_NOMINAL)),
        milli(readF32(RESID_PP, PP_ATTNS_SLOPE)),
        milli(readF32(RESID_PP, PP_FMIN_SPEINDEX)),
    });
}

// ── Dual-channel probe (camino B) ────────────────────────────────────────────
const DualResult = struct {
    lpeak: u32 = 0,
    rpeak: u32 = 0,
    lsum: u64 = 0,
    rsum: u64 = 0,
    n: u64 = 0,
    werr: u32 = 0,
    rerr: u32 = 0,

    fn lmean(self: DualResult) u32 {
        return if (self.n == 0) 0 else @intCast(self.lsum / self.n);
    }
    fn rmean(self: DualResult) u32 {
        return if (self.n == 0) 0 else @intCast(self.rsum / self.n);
    }
};

/// Play (optional) and measure the peak+mean magnitude of BOTH I2S slots at once.
/// LEFT slot = comms beam (post-processed), RIGHT slot = raw ASR beam.
fn measureDual(chunks: u32, amp: f32, noise: bool) DualResult {
    var phase: f32 = 0;
    var res = DualResult{};
    for (0..chunks) |_| {
        var written: usize = 0;
        var got: usize = 0;
        if (amp > 0.0) {
            if (noise) fillNoise(amp) else fillTone(&phase, amp);
            if (c.i2s_channel_write(board.i2sTx(), &tone_buf, @sizeOf(@TypeOf(tone_buf)), &written, 100) != c.ESP_OK) res.werr += 1;
        }
        if (c.i2s_channel_read(board.i2sRx(), &probe_rx_buf, @sizeOf(@TypeOf(probe_rx_buf)), &got, 100) != c.ESP_OK) {
            res.rerr += 1;
            continue;
        }
        const pairs = got / 8;
        for (0..pairs) |i| {
            const l: u32 = @abs(probe_rx_buf[i * 2 + 0] >> 8);
            const r: u32 = @abs(probe_rx_buf[i * 2 + 1] >> 8);
            res.lpeak = @max(res.lpeak, l);
            res.rpeak = @max(res.rpeak, r);
            res.lsum += l;
            res.rsum += r;
            res.n += 1;
        }
    }
    return res;
}

fn logDual(comptime tag: []const u8, base: DualResult, echo: DualResult) void {
    const l_rise: i64 = @as(i64, echo.lmean()) - @as(i64, base.lmean());
    const r_rise: i64 = @as(i64, echo.rmean()) - @as(i64, base.rmean());
    log.info(tag ++ ": echo-rise L(comms)={d} R(asr)={d} | L(peak={d} mean={d}) R(peak={d} mean={d}) werr={d}", .{
        l_rise, r_rise, echo.lpeak, echo.lmean(), echo.rpeak, echo.rmean(), echo.werr,
    });
}

/// Camino B decisivo: ¿la supresión residual (no lineal) del canal comms mata el
/// eco que el beam ASR crudo deja pasar, SIN que el AEC lineal converja?
/// Fase A = beam ADAPTATIVO (peor caso, el AEC no puede fijar): si L-rise << R-rise,
/// full-duplex con tracking es viable. Fase B = beam FIJO tras converger (mejor
/// caso, referencia). AGC+limiter fuera durante el test para comparar niveles
/// reales; todo se restaura al terminar.
pub fn probeDualChannel() void {
    const saved_agc = readI32(RESID_PP, PP_AGCONOFF) orelse 1;
    const saved_lim = readI32(RESID_PP, PP_LIMITONOFF) orelse 1;
    _ = writeI32(RESID_PP, PP_AGCONOFF, 0);
    _ = writeI32(RESID_PP, PP_LIMITONOFF, 0);
    log.info("dualch: AGC/limiter off (was {d}/{d})", .{ saved_agc, saved_lim });

    _ = c.i2s_channel_disable(board.i2sRx());
    _ = c.i2s_channel_enable(board.i2sRx());

    // Fase A: beam adaptativo, AEC virgen (boot) — el peor caso del eco.
    _ = writeI32(RESID_AEC, AEC_FIXEDBEAMSONOFF, 0);
    c.vTaskDelay(100);
    const base = measureDual(40, 0.0, false); // ~0.8s de ambiente
    log.info("dualch base: L(peak={d} mean={d}) R(peak={d} mean={d})", .{
        base.lpeak, base.lmean(), base.rpeak, base.rmean(),
    });
    const echo_a = measureDual(150, PROBE_NOISE_AMP, true); // ~3s ruido
    logDual("dualch A(adaptive)", base, echo_a);

    // Fase B: beam fijo + 3s de convergencia, y medir de nuevo.
    _ = writeI32(RESID_AEC, AEC_FIXEDBEAMSONOFF, 1);
    c.vTaskDelay(100);
    _ = measureDual(150, PROBE_NOISE_AMP, true); // converger (~1s basta, damos 3)
    const echo_b = measureDual(150, PROBE_NOISE_AMP, true);
    logDual("dualch B(fixed+converged)", base, echo_b);
    log.info("dualch: converged={d}", .{readI32(RESID_AEC, AEC_AECCONVERGED) orelse -1});

    // Restaurar TODO al estado de producción.
    _ = writeI32(RESID_AEC, AEC_FIXEDBEAMSONOFF, if (cfg.fixed_beam) 1 else 0);
    _ = writeI32(RESID_PP, PP_AGCONOFF, saved_agc);
    _ = writeI32(RESID_PP, PP_LIMITONOFF, saved_lim);
    _ = c.i2s_channel_disable(board.i2sTx());
    _ = c.i2s_channel_enable(board.i2sTx());
    _ = c.i2s_channel_disable(board.i2sRx());
    _ = c.i2s_channel_enable(board.i2sRx());
    log.info("dualch: done (restored)", .{});
}

// ── Output-gain actuator probe ───────────────────────────────────────────────
/// ¿Es FAR_EXTGAIN (dB) un volumen maestro utilizable? Reproduce el mismo ruido
/// a 0 dB y a −12 dB y compara el nivel de eco en el MICRO PRE-AEC (mux
/// mics_w_gain, sin el confounder de la cancelación). Si el eco cae ≈4× (−12 dB),
/// el knob escala el camino al DAC de forma coherente con la referencia del AEC
/// → sirve de actuador para el auto-nivelado de headroom del eco (cualquier
/// altavoz enchufado, autocalibrante). Reversible: restaura OP_R y la ganancia.
pub fn probeOutputGain() void {
    const saved = readBytes(RESID_AUDIO_MGR, MGR_OP_R, 2) orelse {
        log.err("outgain: OP_R read failed — skipping", .{});
        return;
    };
    if (!writeOpR(MUX_MICS_W_GAIN, 0)) {
        log.err("outgain: OP_R switch failed — skipping", .{});
        return;
    }
    _ = c.i2s_channel_disable(board.i2sRx());
    _ = c.i2s_channel_enable(board.i2sRx());

    const base = measureDual(40, 0.0, false);
    const at_0db = measureDual(150, PROBE_NOISE_AMP, true);
    var at_12db = DualResult{};
    if (writeF32Verified(RESID_AEC, AEC_FAR_EXTGAIN, -12.0, "FAR_EXTGAIN")) {
        c.vTaskDelay(50);
        at_12db = measureDual(150, PROBE_NOISE_AMP, true);
    }
    _ = writeF32Verified(RESID_AEC, AEC_FAR_EXTGAIN, 0.0, "FAR_EXTGAIN");

    log.info("outgain: mic(pre-AEC) ambient={d} | extgain 0dB → {d} | −12dB → {d} (≈/4 si el knob actúa sobre el DAC)", .{
        base.rmean(), at_0db.rmean(), at_12db.rmean(),
    });

    // The restore MUST land: OP_R left on mics_w_gain feeds raw mic (gain 90 →
    // saturated) to wake+ASR — the unit goes deaf until a power-cycle.
    if (!writeOpR(saved[0], saved[1])) {
        log.err("outgain: OP_R RESTORE FAILED — retrying", .{});
        c.vTaskDelay(50);
        if (!writeOpR(saved[0], saved[1])) log.err("outgain: OP_R restore failed twice — mic path is raw mic, power-cycle the XVF", .{});
    }
    _ = c.i2s_channel_disable(board.i2sTx());
    _ = c.i2s_channel_enable(board.i2sTx());
    _ = c.i2s_channel_disable(board.i2sRx());
    _ = c.i2s_channel_enable(board.i2sRx());
    log.info("outgain: done (restored)", .{});
}

// ── Reference probe ──────────────────────────────────────────────────────────
// Definitive far-end test: temporarily route the XVF's own far-end signal to
// the RIGHT I2S output, play a tone through the speaker (same TX line the XVF
// taps as reference), and measure the RIGHT slot on our I2S RX. Tone present →
// the reference reaches the XVF's audio manager. Silence → it never arrives.

const TONE_PAIRS = 960; // 20ms of 48kHz stereo pairs per chunk
var tone_buf: [TONE_PAIRS * 2]i32 = undefined;
var probe_rx_buf: [TONE_PAIRS * 2]i32 = undefined;

/// Write OP_R and read it back: a write can ACK without taking effect, and a
/// probe measurement against the wrong mux is worse than no measurement.
fn writeOpR(category: u8, channel: u8) bool {
    const req = [_]u8{ RESID_AUDIO_MGR, MGR_OP_R, 2, category, channel };
    if (!dfu.ctrlWriteOnly(&req)) return false;
    const rb = readBytes(RESID_AUDIO_MGR, MGR_OP_R, 2) orelse return false;
    if (rb[0] != category or rb[1] != channel) {
        log.err("OP_R readback mismatch: wanted {d},{d} got {d},{d}", .{ category, channel, rb[0], rb[1] });
        return false;
    }
    return true;
}

/// Tell the AEC how much external gain sits between its reference tap and the
/// actual loudspeaker output. Factory value on this build is 0.0 — if the
/// scale is linear (REF_GAIN/MIC_GAIN are), that reads as "the speaker is
/// always silent" and the AEC never adapts.
pub fn setFarExtGain(v: f32) bool {
    const b: [4]u8 = @bitCast(v);
    const req = [_]u8{ RESID_AEC, AEC_FAR_EXTGAIN, 4, b[0], b[1], b[2], b[3] };
    return dfu.ctrlWriteOnly(&req);
}

// Level cases use a quiet tone (clean single-bin peak to map the mux). The
// convergence phase uses WHITE NOISE at a MODERATE level: XMOS converges on
// broadband noise, not a pure tone, and a full-scale tone hits the speaker's
// non-linear region (THD the linear AEC can't model) — so a tone gave a false
// converged=0. Noise at ~−15 dBFS stays in the linear region and excites all
// bands, the actual XMOS convergence stimulus.
const PROBE_LEVEL_AMP: f32 = 0.15;
const PROBE_NOISE_AMP: f32 = 0.18;

fn fillTone(phase: *f32, amp: f32) void {
    const step: f32 = 2.0 * std.math.pi * 1000.0 / 48000.0;
    for (0..TONE_PAIRS) |i| {
        const s: i32 = @intFromFloat(std.math.sin(phase.*) * amp * 2147483647.0);
        tone_buf[i * 2 + 0] = s;
        tone_buf[i * 2 + 1] = s;
        phase.* += step;
        if (phase.* > 2.0 * std.math.pi) phase.* -= 2.0 * std.math.pi;
    }
}

var rng_state: u32 = 0x2545F491;
fn fillNoise(amp: f32) void {
    for (0..TONE_PAIRS) |i| {
        rng_state ^= rng_state << 13;
        rng_state ^= rng_state >> 17;
        rng_state ^= rng_state << 5;
        const norm: f32 = @as(f32, @floatFromInt(rng_state)) / 2147483648.0 - 1.0; // [-1,1)
        const s: i32 = @intFromFloat(norm * amp * 2147483647.0);
        tone_buf[i * 2 + 0] = s;
        tone_buf[i * 2 + 1] = s;
    }
}

const ToneResult = struct {
    peak: u32 = 0,
    write_errs: u32 = 0,
    read_errs: u32 = 0,
    short_reads: u32 = 0,

    /// A peak of 0 only means "silence" if the I2S transfers actually worked.
    fn valid(self: ToneResult, chunks: u32) bool {
        return self.write_errs == 0 and self.read_errs == 0 and self.short_reads < chunks / 4;
    }
};

/// Play the tone for `chunks` × 20ms while measuring the RX right-slot peak
/// (>> 8 to compare against 24-bit-ish magnitudes). Transfer failures are
/// counted, not swallowed — peak=0 with errors is a broken probe, not silence.
fn playAndMeasure(chunks: u32, amp: f32, noise: bool) ToneResult {
    var phase: f32 = 0;
    var res = ToneResult{};
    for (0..chunks) |_| {
        // Fresh out-params per iteration — don't trust every driver error path
        // to write them.
        var written: usize = 0;
        var got: usize = 0;
        if (noise) fillNoise(amp) else fillTone(&phase, amp);
        if (c.i2s_channel_write(board.i2sTx(), &tone_buf, @sizeOf(@TypeOf(tone_buf)), &written, 100) != c.ESP_OK or
            written != @sizeOf(@TypeOf(tone_buf)))
        {
            res.write_errs += 1;
        }
        if (c.i2s_channel_read(board.i2sRx(), &probe_rx_buf, @sizeOf(@TypeOf(probe_rx_buf)), &got, 100) != c.ESP_OK) {
            res.read_errs += 1;
            continue;
        }
        if (got < @sizeOf(@TypeOf(probe_rx_buf))) res.short_reads += 1;
        const pairs = got / 8;
        for (0..pairs) |i| {
            const r: u32 = @abs(probe_rx_buf[i * 2 + 1] >> 8);
            res.peak = @max(res.peak, r);
        }
    }
    return res;
}

/// One-shot boot-time probe. Reversible: saves and restores the OP_R mux.
/// Expected: silence≈0 always; far_end≈tone level if the reference arrives.
pub fn probeReference() void {
    const saved = readBytes(RESID_AUDIO_MGR, MGR_OP_R, 2) orelse {
        log.err("probe: OP_R read failed — skipping", .{});
        return;
    };

    _ = c.i2s_channel_disable(board.i2sRx());
    _ = c.i2s_channel_enable(board.i2sRx());

    const cases = [_]struct { name: []const u8, cat: u8 }{
        .{ .name = "silence", .cat = MUX_SILENCE },
        .{ .name = "far_end", .cat = MUX_FAR_END },
        .{ .name = "far_end_sysdelay", .cat = MUX_FAR_END_SYSDELAY },
        .{ .name = "far_end_w_gain", .cat = MUX_FAR_END_W_GAIN },
        // Acoustic echo BEFORE cancellation — baseline for the residual below.
        .{ .name = "mic_echo(pre-AEC)", .cat = MUX_MICS_W_GAIN },
    };
    for (cases) |case| {
        if (!writeOpR(case.cat, 0)) {
            log.err("probe: OP_R set failed for {s} — sample skipped", .{case.name});
            continue;
        }
        c.vTaskDelay(50); // let the mux take effect
        const r = playAndMeasure(40, PROBE_LEVEL_AMP, false); // ~0.8s of tone
        log.info("probe: OP_R={s} rx_right_peak={d} valid={} (werr={d} rerr={d} short={d})", .{
            case.name, r.peak, r.valid(40), r.write_errs, r.read_errs, r.short_reads,
        });
    }

    _ = writeOpR(saved[0], saved[1]);

    // Experiment A: freeze the beamformer. The filter never locks (jittering
    // peak at ~0.003) — the leading cause is the adaptive beam tracking, which
    // keeps changing the mic→echo path so the AEC has a non-stationary target.
    // Fixed beams (pointed front) make that path stationary; if the filter now
    // locks, the beam was the culprit. Saved and restored so the device reverts.
    const saved_fixed = readI32(RESID_AEC, AEC_FIXEDBEAMSONOFF) orelse 0;
    _ = writeF32Pair(RESID_AEC, AEC_FIXEDBEAMSAZIMUTH, 0.0, 0.0);
    _ = writeF32Pair(RESID_AEC, AEC_FIXEDBEAMSELEVATION, 0.0, 0.0);
    _ = writeI32(RESID_AEC, AEC_FIXEDBEAMSONOFF, 1);
    log.info("probe: FIXED BEAMS on (was={d}) — stationary echo-path test", .{saved_fixed});

    // Convergence experiment: broadband white noise at ~−15 dBFS (the XMOS
    // stimulus, linear region), up to 25s, polling AECCONVERGED each second so
    // we capture WHEN it converges. OP_R is restored to the AEC residual, so the
    // residual peak here is the echo left after cancellation.
    log.info("probe: playing convergence noise (up to 90s, amp={d}m)", .{milli(PROBE_NOISE_AMP)});
    var converged_at: i32 = -1;
    var gaps: u32 = 0; // reference dropouts (I2S TX underruns) that disrupt NLMS
    for (0..90) |sec| {
        const r = playAndMeasure(50, PROBE_NOISE_AMP, true); // ~1s
        gaps += r.write_errs + r.short_reads;
        if (sec % 15 == 14) logFilterSummary(0, 0); // watch the filter grow
        if ((readI32(RESID_AEC, AEC_AECCONVERGED) orelse 0) == 1) {
            converged_at = @intCast(sec + 1);
            break;
        }
    }
    log.info("probe: converged_at={d}s (-1 = never) ref_gaps={d}", .{ converged_at, gaps });
    logState();
    // Definitive instrument: what does the adaptive filter look like now?
    logFilterSummary(0, 0);
    logFilterSummary(0, 1);
    const residual = playAndMeasure(100, PROBE_NOISE_AMP, true); // 2s more, now the residual
    log.info("probe: post-convergence residual peak={d} valid={} (right slot = AEC residual)", .{
        residual.peak, residual.valid(100),
    });

    // Restore the beamformer to its prior (adaptive) state.
    _ = writeI32(RESID_AEC, AEC_FIXEDBEAMSONOFF, saved_fixed);
    log.info("probe: fixed beams restored to {d}", .{saved_fixed});

    // Leave both channels re-synced (direct TX writes upset av_render otherwise).
    _ = c.i2s_channel_disable(board.i2sTx());
    _ = c.i2s_channel_enable(board.i2sTx());
    _ = c.i2s_channel_disable(board.i2sRx());
    _ = c.i2s_channel_enable(board.i2sRx());
    log.info("probe: done, OP_R restored to {d},{d}", .{ saved[0], saved[1] });
}

/// AEC runtime state. Call during/after speaker playback: if the reference
/// chain works, `converged` flips to 1 (and latches, per XMOS: never reset once
/// set) once the AEC has seen far-end audio. rt60 is the room reverb estimate
/// in ms. The config reads (far_extgain, ref_gain, i2s_inactive) are cheap and
/// caught in every sample so a mid-session revert or a clipping ref_gain shows
/// up in Grafana — the whole boot-vs-session mystery hinges on these values.
pub fn logState() void {
    const converged = readI32(RESID_AEC, AEC_AECCONVERGED) orelse -1;
    const path_change = readI32(RESID_AEC, AEC_AECPATHCHANGE) orelse -1;
    const rt60 = readF32(RESID_AEC, AEC_RT60);
    const idle = readI32(RESID_AEC, AEC_CURRENT_IDLE_TIME) orelse -1;
    const far_extgain = readF32(RESID_AEC, AEC_FAR_EXTGAIN);
    const ref_gain = readF32(RESID_AUDIO_MGR, MGR_REF_GAIN);
    const i2s_inactive = readU8(RESID_AUDIO_MGR, MGR_I2S_INACTIVE) orelse 255;
    // Speech energy per beam: non-zero when the mics are alive at the AEC input.
    var sp = [4]i32{ -1, -1, -1, -1 };
    if (readBytes(RESID_AEC, AEC_SPENERGY_VALUES, 16)) |b| {
        for (0..4) |i| {
            const f: f32 = @bitCast(std.mem.readInt(u32, b[i * 4 ..][0..4], .little));
            sp[i] = milli(f); // overflow-safe — this exact line rebooted the device
        }
    }
    log.info("state: converged={d} path_change={d} rt60={d}ms idle={d} far_extgain={d}m ref_gain={d}m i2s_inactive={d} spenergy={d} {d} {d} {d}", .{
        converged, path_change, milli(rt60), idle, milli(far_extgain), milli(ref_gain), i2s_inactive, sp[0], sp[1], sp[2], sp[3],
    });
}
