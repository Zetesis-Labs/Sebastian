//! Wake word detection task for Sebastian.
//!
//! Embeds okay_nabu.tflite (59KB) and runs the microWakeWord streaming CNN
//! on 16kHz mono PCM derived from the XVF3800 I2S stream. The task owns the
//! I2S RX channel while it runs; on exit it resyncs the channel so that
//! mic_src.readFrame() can take over cleanly when the LiveKit session opens.
//!
//! Call sequence (app_main):
//!   init()     — load model once at boot (tensor arena in SRAM)
//!   start()    — spawn the FreeRTOS detection task
//!   (poll detected)
//!   stop()     — block until the task has exited (it exits on its own after
//!                a detection; stop() also aborts a running task). Idempotent.

const std = @import("std");
const c = @import("csdk.zig");
const board = @import("board.zig");
const cfg = @import("config.zig");
const decimator = @import("core/decimator.zig");
const pre_roll = @import("pre_roll.zig");
const mic_src = @import("mic_src.zig");

const log = std.log.scoped(.wakeword);

// ── C shim bindings (implemented in components/mww/mww.cpp) ─────────────────

extern fn mww_init(data: [*]const u8, len: usize, cutoff: f32, window: c_int) bool;
extern fn mww_feed(pcm_16k: [*]const i16, n: c_int) bool;
extern fn mww_reset() void;
extern fn mww_last_prob() f32;

// ── Model constants (from wakeword/okay_nabu.json) ──────────────────────────

const MODEL = @embedFile("okay_nabu.tflite");
// Detection threshold. The stock "okay_nabu" model (Kevin Ahrendt / ESPHome) is
// well separated — its recommended cutoff is 0.97. We still run the two-stage
// split (ROADMAP §7 #1): the board favors RECALL and PRECISION lives server-side
// (agent/wake_verify.py re-verifies each fire against the pre-roll). 0.7 keeps
// recall headroom over 0.97 without the over-sensitivity 0.60 caused on the old
// poorly-separated custom model. Tune from the phantom-clip dataset.
const PROBABILITY_CUTOFF: f32 = 0.70;
// okay_nabu.json: sliding_window_size = 5 (the old custom model used 4).
const SLIDING_WINDOW: c_int = 5;

// ── I2S decimation ───────────────────────────────────────────────────────────
// XVF streams 48kHz stereo 32-bit; the model wants 16kHz mono. We read one
// 10ms chunk at a time and decimate 3:1 through an anti-aliasing FIR.
//
// The FIR is NOT optional. All training audio was resampled with a proper
// filter; naive pick-every-3rd folds the voice's >8kHz energy (sibilants,
// room noise) over the spectrum and the model collapses to ~0% on live
// speech — while band-limited playback (recordings, TTS) still detects,
// which is exactly the confusing failure pattern we debugged. 19-tap
// lowpass at 6.8kHz, Q15: flat to 4kHz, -27dB @ 10kHz, -55dB @ 12kHz.

const PAIRS_48K = decimator.PAIRS_48K; // stereo pairs per 10ms at 48kHz
const STEREO_BYTES = 8; // bytes per stereo pair (2 × 32-bit)
const SAMPLES_16K = decimator.SAMPLES_16K; // mono samples per 10ms at 16kHz (= 480/3)

// ── Shared state ─────────────────────────────────────────────────────────────

/// Set by the detection task when the wake word fires; cleared by start().
pub var detected = std.atomic.Value(bool).init(false);

var ww_running = std.atomic.Value(bool).init(false);
var ww_exited = std.atomic.Value(bool).init(true);

// Buffers — only touched by the detection task and bargeFeed, which are
// time-multiplexed (detection runs while idle; bargeFeed while a session gates
// the mic), never concurrently. i2s_buf lives in PSRAM (allocated once in
// init()): i2s_channel_read memcpy's into it from the internal DMA descriptors,
// so it need not be internal RAM — freeing its ~3.8KB back to the DMA heap.
var i2s_buf: []i32 = &.{}; // interleaved L/R 32-bit, PSRAM-backed
var pcm_buf: [SAMPLES_16K]i16 = undefined;
var decim = decimator.Decimator{};

// ── Public API ───────────────────────────────────────────────────────────────

/// Load the model into the tensor arena. Call once at boot.
pub fn init() bool {
    if (i2s_buf.len == 0) {
        const n = PAIRS_48K * 2;
        const p = c.heap_caps_malloc(n * @sizeOf(i32), c.MALLOC_CAP_SPIRAM | c.MALLOC_CAP_8BIT) orelse {
            log.err("wakeword: failed to allocate {d}B i2s_buf in PSRAM", .{n * @sizeOf(i32)});
            return false;
        };
        i2s_buf = @as([*]i32, @ptrCast(@alignCast(p)))[0..n];
    }
    return mww_init(MODEL.ptr, MODEL.len, PROBABILITY_CUTOFF, SLIDING_WINDOW);
}

/// Spawn the detection task. Returns immediately; poll detected.
pub fn start() void {
    detected.store(false, .release);
    ww_exited.store(false, .release);
    ww_running.store(true, .release);
    _ = c.xTaskCreatePinnedToCore(wwTask, "wakeword", 8 * 1024, null, 2, null, 0);
}

/// Block until the detection task has exited. Idempotent.
pub fn stop() void {
    ww_running.store(false, .release);
    while (!ww_exited.load(.acquire)) {
        c.vTaskDelay(10);
    }
}

// ── Session-time barge-in detection ──────────────────────────────────────────
// While the agent speaks the mic is gated (half-duplex), but the wake model
// is idle and the I2S is still being drained by mic_src — so "Okay Nabu"
// spoken OVER the agent can still be detected and used as an interrupt, the
// same way Alexa's own name barges in. Shares the decimator and model state
// with the idle task; only valid while that task is stopped (session active).

/// Reset detector state for a fresh barge-in window. Call at each gate-on.
pub fn bargeReset() void {
    resetDecimator();
    mww_reset();
}

/// Feed gated stereo I2S pairs (any length) through the detector.
/// Returns true when the wake word fires.
pub fn bargeFeed(stereo: []const i32) bool {
    var hit = false;
    var off: usize = 0;
    const pairs_total = stereo.len / 2;
    while (off < pairs_total) {
        // usize annotation is load-bearing: @min with a comptime bound narrows
        // the result to u9 (0..511) and `pairs * 2` overflows at pairs >= 256 —
        // a guaranteed panic on every 960-pair session frame (field-crashed 3x).
        const pairs: usize = @min(PAIRS_48K, pairs_total - off);
        @memcpy(i2s_buf[0 .. pairs * 2], stereo[off * 2 .. (off + pairs) * 2]);
        const chunk = decimateChunk(pairs);
        if (mww_feed(&pcm_buf, @intCast(chunk.samples))) hit = true;
        off += pairs;
    }
    return hit;
}

// ── Detection task ───────────────────────────────────────────────────────────

fn resyncRx() void {
    _ = c.i2s_channel_disable(board.i2sRx());
    _ = c.i2s_channel_enable(board.i2sRx());
}

fn readChunk48k() ?usize {
    var bytes_read: usize = 0;
    const ret = c.i2s_channel_read(
        board.i2sRx(),
        i2s_buf.ptr,
        PAIRS_48K * STEREO_BYTES,
        &bytes_read,
        200,
    );
    if (ret != c.ESP_OK) return null;
    return bytes_read / STEREO_BYTES;
}

fn resetDecimator() void {
    decim.reset();
}

/// Anti-aliased 3:1 decimation of the configured slot into pcm_buf. The FIR
/// runs on the linear (post gain-shift, pre softClip) signal; only the
/// decimated outputs are computed. Returns mono sample count, the chunk's
/// peak and mean (telemetry + channel-health detection).
fn decimateChunk(got_pairs: usize) decimator.Result {
    return decim.decimate(i2s_buf[0 .. got_pairs * 2], pcm_buf[0..]);
}

/// Telemetry: log any inference above 30% immediately (each near-trigger is
/// visible in the serial), plus a 5s summary of audio peak and max probability
/// so "no signal" and "model never fires" are distinguishable in the field.
const Telemetry = struct {
    max_prob: f32 = 0,
    peak_pcm: i32 = 0,
    frames: u32 = 0,
    feed_max_us: i64 = 0, // slowest mww_feed — if > chunk cadence (10ms), we drop audio
    gap_max_us: i64 = 0, // longest wall-clock gap between i2s reads (DMA holds 30ms)
    dc_worst: i32 = 0, // largest |DC mean| seen — degenerate-channel indicator

    fn tick(self: *Telemetry, chunk_peak: i32, chunk_mean: i32, feed_us: i64, gap_us: i64) void {
        const prob = mww_last_prob();
        if (prob > 0.30 and prob > self.max_prob) {
            const pct: u32 = @intFromFloat(prob * 100.0);
            log.info("prob spike: {d}%", .{pct});
        }
        self.max_prob = @max(self.max_prob, prob);
        self.peak_pcm = @max(self.peak_pcm, chunk_peak);
        self.feed_max_us = @max(self.feed_max_us, feed_us);
        self.gap_max_us = @max(self.gap_max_us, gap_us);
        if (@abs(chunk_mean) > @abs(self.dc_worst)) self.dc_worst = chunk_mean;
        self.frames += 1;
        if (self.frames >= 500) { // 500 × 10ms = 5s
            const pct: u32 = @intFromFloat(self.max_prob * 100.0);
            log.info("5s window: pcm peak={d} max prob={d}% feed_max={d}us gap_max={d}us dc={d}", .{
                self.peak_pcm, pct, self.feed_max_us, self.gap_max_us, self.dc_worst,
            });
            self.* = .{};
        }
    }
};

fn wwTask(_: ?*anyopaque) callconv(.c) void {
    // The RX channel may have been left enabled before the XVF clock was
    // running (boot) or abandoned by the previous session — re-latch it.
    resyncRx();
    mww_reset();
    resetDecimator();
    pre_roll.reset();
    log.info("detection active ({s} channel)", .{@tagName(cfg.mic_channel)});

    var telemetry = Telemetry{};
    var fired = false;
    var last_read_us: i64 = c.esp_timer_get_time();
    var bad_chunks: u32 = 0;
    var heal_threshold: u32 = 100; // ~1s; backs off after an ineffective heal

    while (ww_running.load(.acquire)) {
        const got_pairs = readChunk48k() orelse continue;
        const t_read = c.esp_timer_get_time();
        const gap_us = t_read - last_read_us;
        last_read_us = t_read;

        const chunk = decimateChunk(got_pairs);

        // Channel self-heal: a bad re-latch (dead all-zero, or DC-pinned with
        // the audio crushed) never recovers alone — resync and reset the
        // detector state until the signal looks sane. NOT while muted: a muted
        // XVF streams all-zero I2S on purpose (749 pointless heals against the
        // mute button taught us that). Backoff after an ineffective heal so a
        // genuinely stuck channel logs every ~10s, not every second.
        const muted = mic_src.isMuted();
        if (!muted and (chunk.peak == 0 or @abs(chunk.mean) > 15000)) {
            bad_chunks += 1;
            if (bad_chunks >= heal_threshold) {
                bad_chunks = 0;
                heal_threshold = 1000; // next heal only after ~10s more of bad audio
                log.warn("wake channel degenerate (peak={d} dc={d}) — self-heal resync", .{ chunk.peak, chunk.mean });
                resyncRx();
                resetDecimator();
                mww_reset();
                continue;
            }
        } else {
            bad_chunks = 0;
            if (!muted) heal_threshold = 100; // healthy again → fast reaction restored
        }

        pre_roll.feed(pcm_buf[0..chunk.samples]);

        const t0 = c.esp_timer_get_time();
        const hit = !fired and mww_feed(&pcm_buf, @intCast(chunk.samples));
        telemetry.tick(chunk.peak, chunk.mean, c.esp_timer_get_time() - t0, gap_us);

        if (hit) {
            log.info("WAKE WORD DETECTED", .{});
            pre_roll.markWake(); // send window: 2s before this instant → handoff
            detected.store(true, .release);
            fired = true;
            // Keep draining I2S into the local pre-roll ring until app_main
            // stops us. That captures the user's first words while token.fetch()
            // runs, without opening LiveKit in IDLE.
        }
    }

    // Deliberately NO resyncRx() here: mic_src re-latches once in its own
    // task at the first live read, and wwTask resyncs at entry on the way
    // back. A second disable/enable at exit doubled the glitch window right
    // at the wake→live seam (razor-cut first words in the recordings).
    log.info("detection task done", .{});
    ww_exited.store(true, .release);
    c.vTaskDelete(null);
}
