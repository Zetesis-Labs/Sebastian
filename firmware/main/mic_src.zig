//! Custom esp_capture audio source for the XVF3800 mic.
//!
//! The XVF3800 (inthost firmware) is the I2S master and streams 48 kHz 32-bit
//! stereo. The LEFT slot is the fully-processed comms output (AEC + beamforming +
//! NS + residual-echo suppression + limiter) and the RIGHT slot is the raw ASR beam
//! (post-AEC but skipping the post-processing). Which slot we consume is an
//! install-time choice in config.zig (mic_channel), resolved at comptime.
//!
//! We read i2s_rx directly inside read_frame(): the esp_capture pipeline pulls a
//! frame, and we block on exactly that many I2S samples. This makes the capture
//! consumer-paced (no producer task, no ring buffer, no clock-domain drift — the
//! XVF's 48 kHz clock and LiveKit's consumer are the same loop), which is what
//! removes the periodic "helicopter"/warble a free-running ring buffer produced.

const std = @import("std");
const c = @import("csdk.zig");
const board = @import("board.zig");
const cfg = @import("config.zig");
const gate_core = @import("core/mic_gate.zig");
const pcm = @import("xvf_pcm.zig");
const wakeword = @import("wakeword.zig");

const log = std.log.scoped(.mic_src);

const MAX_SAMPLES = 1024; // >= one frame (960 @ 48k/20ms)
const BYTES_PER_MONO_SAMPLE = 2;
const BYTES_PER_STEREO_I2S_SAMPLE = 8;

const Src = extern struct {
    base: c.esp_capture_audio_src_iface_t,
    info: c.esp_capture_audio_info_t,
    samples_acc: u64,
    started: bool,
};

var instance: Src = undefined;
var pcm_codecs = [_]c_uint{c.ESP_CAPTURE_FMT_ID_PCM};
// Shared across tasks (audio capture writes, UI/session read): use atomics for
// visibility instead of plain globals. Monotonic ordering is enough — these are
// independent scalars, not a lock protecting other state.
var mic_level = std.atomic.Value(u32).init(0);
var gate = gate_core.Gate{};

/// Smoothed peak of the captured voice (0..32767). Used by the LED UI to detect
/// when someone is speaking so it can lock the beam direction per utterance.
pub fn level() u32 {
    return mic_level.load(.monotonic);
}

/// When true, read_frame() sends silence to the room instead of the captured
/// audio (used by the mute button — the XVF GPIO30 mute doesn't silence our ASR
/// beam). Can also gate the mic while the agent speaks (half-duplex), if enabled.
pub fn setMuted(m: bool) void {
    gate.setMuted(m);
}
pub fn isMuted() bool {
    return gate.muted;
}

var live_flag = std.atomic.Value(bool).init(false);

/// Until the wake→live handoff the wake task still owns I2S (it keeps filling
/// the pre-roll ring through connect); the capture pipeline gets silence
/// without touching the bus. app.zig flips this at the handoff event.
pub fn setLive(v: bool) void {
    live_flag.store(v, .monotonic);
}

/// Full-duplex is a RUNTIME decision, not just config.full_duplex: it is only
/// safe once applyConfig actually fixed the beam so the AEC cancels the echo.
/// app.zig only enables this when full-duplex is requested, fixed_beam is true,
/// and AEC config succeeded. Defaults false (safe) until app.zig confirms.
pub fn setFullDuplex(v: bool) void {
    gate.setFullDuplex(v);
}

// Half-duplex gate (the fallback when full_duplex_active is false — i.e. an
// adaptive beam, where the AEC can't converge, or an AEC-config failure): while
// the agent speaks its own voice would come back through the mic as crisp phantom
// user turns and the room feeds on itself. The agent announces speaking over the
// data channel; we send silence for the duration plus a short reverb tail.
var barge_flag = std.atomic.Value(bool).init(false);

pub fn setAgentSpeaking(v: bool) void {
    if (gate.setAgentSpeaking(v)) wakeword.bargeReset(); // fresh detector window per burst
}

/// True once when "Sebastián" was heard over the agent's speech. app.zig
/// polls this from the session loop and relays the interrupt to the agent.
pub fn takeBargeRequest() bool {
    return barge_flag.swap(false, .monotonic);
}

fn gatedByAgent() bool {
    return gate.gatedByAgent();
}
// Interleaved L/R 32-bit I2S scratch. Lives in PSRAM (allocated once in
// create()): i2s_channel_read copies from the internal DMA descriptors into
// this buffer via CPU memcpy, so it need not be internal/DMA-capable, and
// moving its 8KB out of internal SRAM widens the DMA-heap margin at the TLS
// handshake. Empty slice until create() allocates it.
var read_buf: []i32 = &.{};
var resynced = std.atomic.Value(bool).init(false);

fn frameSampleCount(frame: *const c.esp_capture_stream_frame_t) ?usize {
    if (frame.size <= 0) return 0;
    if (@mod(frame.size, BYTES_PER_MONO_SAMPLE) != 0) {
        log.err("mic frame has invalid byte size: {d}", .{frame.size});
        return null;
    }

    const samples: usize = @intCast(@divTrunc(frame.size, BYTES_PER_MONO_SAMPLE));
    if (samples <= MAX_SAMPLES) return samples;

    log.err("mic frame too large: {d} samples (max {d})", .{ samples, MAX_SAMPLES });
    return null;
}

fn resyncRxOnce() void {
    if (resynced.load(.monotonic)) return;

    resynced.store(true, .monotonic);
    _ = c.i2s_channel_disable(board.i2sRx());
    _ = c.i2s_channel_enable(board.i2sRx());
}

fn open(_: *c.esp_capture_audio_src_iface_t) callconv(.c) c_int {
    return c.ESP_CAPTURE_ERR_OK;
}

fn getSupportCodecs(_: *c.esp_capture_audio_src_iface_t, codecs: *[*c]const c_uint, num: *u8) callconv(.c) c_int {
    codecs.* = &pcm_codecs[0];
    num.* = 1;
    return c.ESP_CAPTURE_ERR_OK;
}

fn setFixedCaps(_: *c.esp_capture_audio_src_iface_t, caps: *const c.esp_capture_audio_info_t) callconv(.c) c_int {
    instance.info = caps.*;
    return c.ESP_CAPTURE_ERR_OK;
}

fn negotiateCaps(_: *c.esp_capture_audio_src_iface_t, in_cap: *c.esp_capture_audio_info_t, out_caps: *c.esp_capture_audio_info_t) callconv(.c) c_int {
    if (in_cap.format_id != c.ESP_CAPTURE_FMT_ID_PCM) {
        return c.ESP_CAPTURE_ERR_NOT_SUPPORTED;
    }
    out_caps.* = in_cap.*;
    instance.info = in_cap.*;
    return c.ESP_CAPTURE_ERR_OK;
}

fn start(_: *c.esp_capture_audio_src_iface_t) callconv(.c) c_int {
    instance.started = true;
    instance.samples_acc = 0;
    resynced.store(false, .monotonic);
    log.info("mic source started (direct read, {s} slot, 48k)", .{@tagName(cfg.mic_channel)});
    return c.ESP_CAPTURE_ERR_OK;
}

fn readI2s(samples: usize) ?usize {
    var bytes_read: usize = 0;
    const bytes_to_read = samples * BYTES_PER_STEREO_I2S_SAMPLE;
    if (c.i2s_channel_read(board.i2sRx(), read_buf.ptr, bytes_to_read, &bytes_read, 200) != c.ESP_OK) {
        _ = stat_timeouts.fetchAdd(1, .monotonic);
        return null;
    }
    return bytes_read / BYTES_PER_STEREO_I2S_SAMPLE;
}

fn fillSilence(out: [*]i16, samples: usize) void {
    var i: usize = 0;
    while (i < samples) : (i += 1) out[i] = 0;
}

fn writeSilentFrame(frame: *c.esp_capture_stream_frame_t, samples: usize) void {
    const out: [*]i16 = @ptrCast(@alignCast(frame.data));
    fillSilence(out, samples);
    mic_level.store(0, .monotonic);
}

fn updatePeak(next_peak: u32) void {
    // Single writer (the audio task): load/compute/store is race-free here; the
    // cross-task reader only needs a coherent snapshot.
    const cur = mic_level.load(.monotonic);
    if (next_peak > cur) {
        mic_level.store(next_peak, .monotonic);
        return;
    }
    mic_level.store(cur - (cur >> 4), .monotonic);
}

// ── Channel self-heal ─────────────────────────────────────────────────────────
// The I2S slave sometimes re-latches wrong after a hand-off/resync: either
// dead (all-zero) or pinned near full-scale by a huge DC (the real audio gets
// crushed by the limiter → the agent hears mush, the level-based VAD never
// sees silence, sessions run to the max cap). Neither state recovers on its
// own — detect it and resync until the signal looks sane again. Telemetry
// caught both states in the field; this is the cure.

const HEAL_BAD_FRAMES: u32 = 50; // ~1s of 20ms frames before forcing a resync
var heal_bad_frames: u32 = 0;
var heal_count: u32 = 0;

fn channelLooksBad(peak: u32, mean: i32) bool {
    return gate_core.channelLooksBad(peak, mean);
}

fn healChannelIfBad(peak: u32, mean: i32) void {
    if (!channelLooksBad(peak, mean)) {
        heal_bad_frames = 0;
        return;
    }
    heal_bad_frames += 1;
    if (heal_bad_frames < HEAL_BAD_FRAMES) return;
    heal_bad_frames = 0;
    heal_count += 1;
    _ = stat_heals.fetchAdd(1, .monotonic);
    log.warn("mic channel degenerate (peak={d} dc={d}) — self-heal resync #{d}", .{ peak, mean, heal_count });
    _ = c.i2s_channel_disable(board.i2sRx());
    _ = c.i2s_channel_enable(board.i2sRx());
}

// Capture-health telemetry: short I2S reads (frame tail padded), read timeouts
// and self-heal resyncs are the fingerprints of the micro-cut bursts heard in
// the field recordings (razor-cut gaps + ~200ms holes). They used to be
// invisible — the padding was silent and only the heal logged. app.zig drains
// these each AEC-log window (takeReadStats) so they land in Grafana.
var stat_short_reads = std.atomic.Value(u32).init(0);
var stat_pad_samples = std.atomic.Value(u32).init(0);
var stat_timeouts = std.atomic.Value(u32).init(0);
var stat_heals = std.atomic.Value(u32).init(0);

pub const ReadStats = struct {
    short_reads: u32,
    pad_samples: u32,
    timeouts: u32,
    heals: u32,

    pub fn healthy(self: ReadStats) bool {
        return self.short_reads == 0 and self.timeouts == 0 and self.heals == 0;
    }
};

/// Capture-health counters since the last call, then reset.
pub fn takeReadStats() ReadStats {
    return .{
        .short_reads = stat_short_reads.swap(0, .monotonic),
        .pad_samples = stat_pad_samples.swap(0, .monotonic),
        .timeouts = stat_timeouts.swap(0, .monotonic),
        .heals = stat_heals.swap(0, .monotonic),
    };
}

// Echo telemetry: peak of the captured beam while the agent speaks. The gate
// forces mic_level to 0, so the residual the AEC failed to cancel is otherwise
// invisible. app.zig drains this each AEC-log window (takeGatedPeak).
var gated_peak_max = std.atomic.Value(u32).init(0);

fn trackGatedPeak(got: usize) void {
    var peak = gated_peak_max.load(.monotonic);
    var i: usize = 0;
    while (i < got) : (i += 1) {
        const s = pcm.convert(read_buf[i * 2 + pcm.SLOT]);
        const a: u32 = @abs(@as(i32, s));
        if (a > peak) peak = a;
    }
    gated_peak_max.store(peak, .monotonic);
}

/// Peak residual echo seen while the agent spoke since the last call, then reset.
pub fn takeGatedPeak() u32 {
    return gated_peak_max.swap(0, .monotonic);
}

fn agentGateActive() bool {
    return gate.agentGateActive();
}

fn detectBargeInFromGatedAudio(got: usize) void {
    if (gate.muted) return;
    if (got == 0) return;

    trackGatedPeak(got); // residual echo the AEC left, invisible in mic_level
    if (!wakeword.bargeFeed(read_buf[0 .. got * 2])) return;

    gate.agent_speaking = false;
    gate.speak_hangover = 0;
    barge_flag.store(true, .monotonic);
    log.info("barge-in: wake word heard over agent speech", .{});
}

fn writeGatedFrame(out: [*]i16, got: usize, total: usize, agent_gated: bool) void {
    if (agent_gated) detectBargeInFromGatedAudio(got);
    fillSilence(out, total);
    mic_level.store(0, .monotonic);
}

fn writeCapturedSamples(out: [*]i16, got: usize, total: usize) void {
    var i: usize = 0;
    var peak: u32 = 0;
    var sum: i64 = 0;
    while (i < got) : (i += 1) {
        const sample = pcm.convert(read_buf[i * 2 + pcm.SLOT]);
        out[i] = sample;
        sum += sample;
        const abs_sample: u32 = @abs(@as(i32, sample));
        if (abs_sample > peak) peak = abs_sample;
    }

    updatePeak(peak);
    if (got > 0) healChannelIfBad(peak, @intCast(@divTrunc(sum, @as(i64, @intCast(got)))));

    // Short read: the driver stalled mid-frame. Padding with zeros was audible
    // as a razor-cut micro-gap ("saturated" click) in the published voice —
    // hold the last sample instead (TROUBLESHOOTING §7) and count it.
    if (i < total) {
        _ = stat_short_reads.fetchAdd(1, .monotonic);
        _ = stat_pad_samples.fetchAdd(@intCast(total - got), .monotonic);
        const hold: i16 = if (got > 0) out[got - 1] else 0;
        while (i < total) : (i += 1) out[i] = hold;
    }
}

fn updatePts(frame: *c.esp_capture_stream_frame_t, samples: usize) void {
    const rate: u64 = if (instance.info.sample_rate == 0) 48000 else instance.info.sample_rate;
    frame.pts = @truncate(instance.samples_acc * 1000 / rate);
    instance.samples_acc += samples;
}

fn readLiveSamples(frame: *c.esp_capture_stream_frame_t, total: usize) c_int {
    // The board enabled the RX channel before the XVF was clocking; re-sync once
    // in this task's context so it latches onto the running clock.
    resyncRxOnce();

    const out: [*]i16 = @ptrCast(@alignCast(frame.data));
    const got = readI2s(total) orelse return c.ESP_CAPTURE_ERR_INTERNAL;
    const agent_gated = agentGateActive();

    if (gate.muted or agent_gated) {
        writeGatedFrame(out, got, total, agent_gated);
        return c.ESP_CAPTURE_ERR_OK;
    }

    writeCapturedSamples(out, got, total);
    return c.ESP_CAPTURE_ERR_OK;
}

fn readFrame(_: *c.esp_capture_audio_src_iface_t, frame: *c.esp_capture_stream_frame_t) callconv(.c) c_int {
    if (!instance.started) return c.ESP_CAPTURE_ERR_NOT_SUPPORTED;

    const total = frameSampleCount(frame) orelse return c.ESP_CAPTURE_ERR_NOT_SUPPORTED;
    if (total == 0) return c.ESP_CAPTURE_ERR_OK;

    // Pre-handoff: the wake task owns I2S and is filling the pre-roll ring.
    // Feed the pipeline silence without touching the bus — two concurrent
    // i2s_channel_read callers would steal frames from each other.
    if (!live_flag.load(.monotonic)) {
        writeSilentFrame(frame, total);
        updatePts(frame, total);
        return c.ESP_CAPTURE_ERR_OK;
    }

    // readLiveSamples keeps draining I2S (so the DMA never overflows), but when
    // muted — by the button OR by the half-duplex gate while the agent speaks —
    // sends SILENCE to the room. The XVF's GPIO30 mute does NOT silence the ASR
    // beam we tap, so without this the agent hears its own speaker echo and
    // talks to itself. Muting in software here is the robust fix.
    // Full-duplex (runtime, gated on a verified-fixed beam — see setFullDuplex)
    // bypasses the half-duplex gate: the mic stays live while the agent speaks and
    // the AEC-cancelled audio is forwarded so the user can talk over it naturally;
    // interruption then rides the agent's own VAD, not the wake word. Half-duplex
    // (full_duplex_active=false, incl. the AEC-config-failed fallback) gates the
    // mic to silence during agent speech and keeps the wake-word barge-in below.
    // The button mute (muted_flag) always applies.
    const result = readLiveSamples(frame, total);
    if (result != c.ESP_CAPTURE_ERR_OK) return result;

    updatePts(frame, total);
    return c.ESP_CAPTURE_ERR_OK;
}

fn stop(_: *c.esp_capture_audio_src_iface_t) callconv(.c) c_int {
    instance.started = false;
    return c.ESP_CAPTURE_ERR_OK;
}

fn close(_: *c.esp_capture_audio_src_iface_t) callconv(.c) c_int {
    return c.ESP_CAPTURE_ERR_OK;
}

/// Build the mic source. record_handle is unused (we read i2s_rx directly).
pub fn create(_: c.esp_codec_dev_handle_t) ?*c.esp_capture_audio_src_if_t {
    if (read_buf.len == 0) {
        const n = MAX_SAMPLES * 2;
        const p = c.heap_caps_malloc(n * @sizeOf(i32), c.MALLOC_CAP_SPIRAM | c.MALLOC_CAP_8BIT) orelse {
            log.err("mic_src: failed to allocate {d}B read_buf in PSRAM", .{n * @sizeOf(i32)});
            return null;
        };
        read_buf = @as([*]i32, @ptrCast(@alignCast(p)))[0..n];
    }
    instance = .{
        .base = .{
            .open = open,
            .get_support_codecs = getSupportCodecs,
            .set_fixed_caps = setFixedCaps,
            .negotiate_caps = negotiateCaps,
            .start = start,
            .read_frame = readFrame,
            .stop = stop,
            .close = close,
        },
        .info = .{},
        .samples_acc = 0,
        .started = false,
    };
    return @ptrCast(&instance.base);
}
