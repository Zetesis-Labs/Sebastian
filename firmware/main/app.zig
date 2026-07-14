//! Sebastian firmware entry point.
//!
//! Boot → board bring-up → XVF DFU → codec init → audio pipeline → WiFi/SNTP →
//! load wake word model → idle detection loop.
//!
//! Idle loop (low power, no LiveKit):
//!   wakeword task reads I2S directly and feeds the okay_nabu streaming CNN.
//!   On detection → keep filling local pre-roll while opening LiveKit →
//!   hand off the mic when the room and agent are ready → silence timeout →
//!   close room → back to wake word detection.
//!
//! The XVF I2S RX channel is shared: the wakeword task owns it during idle,
//! mic_src.readFrame() owns it during the active session. Each hand-off includes
//! an i2s_channel_disable/enable resync to re-latch onto the running clock.
//!
//! Session choreography lives in core/session_reducer.zig (pure, host-tested):
//! the LiveKit callbacks push events into a FreeRTOS queue, the session loop
//! consumes them (the queue timeout IS the 10ms tick) and executes the actions
//! the reducer returns. This file is only the imperative shell.

const std = @import("std");
const board = @import("board.zig");
const cfg = @import("config.zig");
const mic_src = @import("mic_src.zig");
const xvf_dfu = @import("xvf_dfu.zig");
const xvf_ui = @import("xvf_ui.zig");
const wakeword = @import("wakeword.zig");
const xvf_aec = @import("xvf_aec.zig");
const pre_roll = @import("pre_roll.zig");
const session_core = @import("core/session_core.zig");
const reducer = @import("core/session_reducer.zig");
const token = @import("token.zig");
const c = @import("csdk.zig");

const log = std.log.scoped(.sebastian);

// Session timing constants + the pure keepalive/silence logic live in
// core/session_core.zig; the session choreography (thresholds, phases, retry
// cadence) in core/session_reducer.zig — both host-testable. These aliases
// keep the loop/watchdog call sites here readable.
const SESSION_TICK_MS: u32 = session_core.TICK_MS;
const SESSION_MAX_TICKS: u32 = session_core.MAX_TICKS;

var room: c.livekit_room_handle_t = null;
// Peak |render sample| (>>8) since the session loop last read it. Fed by the
// av_render reference callback (the speaker output), it is a data-channel-
// INDEPENDENT proof that the agent is talking — the keepalive that survives a
// dead SCTP link. Stays an atomic (NOT a queue event): the render thread
// produces it every few ms, so it is sampled into each tick via swap.
var render_peak = std.atomic.Value(u32).init(0);
var wake_seq: u32 = 0;

// Discrete session events (agent state, participant info) flow through this
// queue from the LiveKit callback threads into the session loop. Created once
// at boot, reset at each session open so nothing raced from the previous
// session leaks into the next one (the old tick-0 "close" hygiene).
const EVENT_QUEUE_LEN: c_uint = 16;
var event_queue: c.QueueHandle_t = null;

fn pushEvent(event: reducer.Event) void {
    if (c.xQueueGenericSend(event_queue, &event, 0, 0) != 1) {
        log.warn("session event queue full — event dropped", .{});
    }
}

const CaptureHandle = *anyopaque;
const RenderHandle = *anyopaque;

const AppError = error{
    LiveKitSystemInitFailed,
    DecoderRegistrationFailed,
    EncoderRegistrationFailed,
    MicSourceCreateFailed,
    CaptureOpenFailed,
    RendererAllocationFailed,
    RenderOpenFailed,
    NetworkConnectFailed,
    WifiPowerSaveFailed,
    TokenFetchFailed,
    RoomCreateFailed,
    RoomConnectFailed,
};

const AudioPipeline = struct {
    mic: CaptureHandle,
    speaker: RenderHandle,
};

const BootHealth = struct {
    xvf: bool = false,
    codecs: bool = false,
    audio: bool = false,
    ww: bool = false,

    fn ok(self: BootHealth) bool {
        return self.xvf and self.codecs and self.audio and self.ww;
    }
};

fn requireOk(result: c_int, comptime err: AppError) AppError!void {
    if (result == 0) return;
    return err;
}

fn requireLiveKitOk(result: c_int, comptime err: AppError) AppError!void {
    if (result == c.LIVEKIT_ERR_NONE) return;
    return err;
}

fn requireEspOk(result: c.esp_err_t, comptime err: AppError) AppError!void {
    if (result == c.ESP_OK) return;
    return err;
}

fn initLiveKitSystem() AppError!void {
    try requireOk(c.livekit_system_init(), error.LiveKitSystemInitFailed);
}

fn registerAudioCodecs() AppError!void {
    try requireOk(c.esp_audio_dec_register_default(), error.DecoderRegistrationFailed);
    try requireOk(c.esp_audio_enc_register_default(), error.EncoderRegistrationFailed);
}

fn buildCapturer() AppError!CaptureHandle {
    const src = mic_src.create(board.recordHandle());
    if (src == null) {
        log.err("mic source create failed", .{});
        return error.MicSourceCreateFailed;
    }

    var cfg_cap = std.mem.zeroes(c.esp_capture_cfg_t);
    cfg_cap.sync_mode = c.ESP_CAPTURE_SYNC_MODE_AUDIO;
    cfg_cap.audio_src = src;
    var handle: c.esp_capture_handle_t = null;
    if (c.esp_capture_open(&cfg_cap, &handle) != 0 or handle == null) {
        log.err("esp_capture_open failed", .{});
        return error.CaptureOpenFailed;
    }
    log.info("mic capture pipeline ready", .{});
    return handle.?;
}

/// av_render "reference output" callback: the PCM being played through the
/// speaker (48 kHz, 2ch, 32-bit). Non-silent output = the agent is speaking;
/// on underrun the I2S driver's auto_clear feeds exact zeros, so there is no
/// noise floor to confuse it. Runs in the render thread — subsample and keep it
/// cheap. Publishes the peak so the session loop can keep the session alive
/// while the agent talks, with no dependency on the agent_state data channel.
fn renderRefCb(data: ?[*]u8, size: c_int, _: ?*anyopaque) callconv(.c) c_int {
    const d = data orelse return 0;
    const count: usize = @intCast(@divTrunc(@max(0, size), 4)); // 32-bit samples
    var peak: u32 = 0;
    var i: usize = 0;
    while (i < count) : (i += 8) { // subsample every 8th sample
        const s = std.mem.readInt(i32, d[i * 4 ..][0..4], .little);
        const a: u32 = @abs(s) >> 8;
        if (a > peak) peak = a;
    }
    _ = render_peak.fetchMax(peak, .monotonic);
    return 0;
}

// Stashed for onDataReceived: the "interrupted" barge-in flush needs the render
// handle from a C callback that has no access to the AudioPipeline.
var speaker_render: c.av_render_handle_t = null;

fn buildRenderer() AppError!RenderHandle {
    var i2s_cfg = std.mem.zeroes(c.i2s_render_cfg_t);
    i2s_cfg.play_handle = board.playbackHandle();
    i2s_cfg.cb = @ptrCast(&renderRefCb); // speaker-output tap → agent-audio keepalive
    i2s_cfg.fixed_clock = true;
    const audio_render = c.av_render_alloc_i2s_render(&i2s_cfg);
    if (audio_render == null) {
        log.err("av_render_alloc_i2s_render failed", .{});
        return error.RendererAllocationFailed;
    }

    var cfg_rend = std.mem.zeroes(c.av_render_cfg_t);
    cfg_rend.audio_render = audio_render;
    cfg_rend.audio_raw_fifo_size = 4 * 4096;
    cfg_rend.audio_render_fifo_size = 24 * 1024;
    cfg_rend.allow_drop_data = true;
    const handle = c.av_render_open(&cfg_rend);
    if (handle == null) {
        log.err("av_render_open failed", .{});
        return error.RenderOpenFailed;
    }

    var frame = std.mem.zeroes(c.av_render_audio_frame_info_t);
    frame.sample_rate = 48000;
    frame.channel = 2;
    frame.bits_per_sample = 32;
    // av_render bug: set_fixed_frame_info stores the info but always returns
    // ESP_MEDIA_ERR_WRONG_STATE (ret is never set to 0) — ignore the code.
    _ = c.av_render_set_fixed_frame_info(handle, &frame);
    speaker_render = handle;
    log.info("speaker render pipeline ready", .{});
    return handle.?;
}

fn buildAudioPipeline() AppError!AudioPipeline {
    const mic = try buildCapturer();
    const speaker = try buildRenderer();
    return .{ .mic = mic, .speaker = speaker };
}

fn startSntp() void {
    c.esp_sntp_setoperatingmode(0);
    c.esp_sntp_setservername(0, "pool.ntp.org");
    c.esp_sntp_init();
    c.vTaskDelay(3000);
}

fn onStateChanged(state: c_int, _: ?*anyopaque) callconv(.c) void {
    log.info("room state: {s}", .{std.mem.span(c.livekit_connection_state_str(state))});
}

fn onParticipantInfo(info: *const c.livekit_participant_info_t, _: ?*anyopaque) callconv(.c) void {
    if (info.kind != c.LIVEKIT_PARTICIPANT_KIND_AGENT) return;

    const active = info.state == c.LIVEKIT_PARTICIPANT_STATE_ACTIVE;
    pushEvent(.{ .agent_active = active });
    if (info.identity) |identity| {
        log.info("agent participant: {s} state={d}", .{ std.mem.span(identity), info.state });
    } else {
        log.info("agent participant state={d}", .{info.state});
    }
}

var barge_payload = [_]u8{ 'b', 'a', 'r', 'g', 'e' }; // static: publish is async, the pointer must outlive the call

fn publishBargeIn() void {
    var payload = c.livekit_data_payload_t{ .bytes = &barge_payload, .size = barge_payload.len };
    var opts = c.livekit_data_publish_options_t{ .payload = &payload, .topic = "sebastian.barge_in" };
    if (c.livekit_room_publish_data(room, &opts) != c.LIVEKIT_ERR_NONE) {
        log.warn("barge-in publish failed", .{});
    }
}

fn onDataReceived(data: *const c.livekit_data_received_t, _: ?*anyopaque) callconv(.c) void {
    const topic = data.topic orelse return;
    const bytes = data.payload.bytes orelse return;
    if (!std.mem.eql(u8, std.mem.span(topic), "sebastian.agent_state")) return;

    // Classify here (the raw payload is only valid during this callback) and
    // let the session loop react: it blocks on the queue, so the mic gate and
    // the render flush land within a context switch, not a poll tick.
    const state = bytes[0..data.payload.size];
    if (std.mem.eql(u8, state, "interrupted")) {
        pushEvent(.{ .agent_state = .interrupted });
        return;
    }
    if (std.mem.eql(u8, state, "close")) {
        pushEvent(.{ .agent_state = .close });
        return;
    }
    const speaking = std.mem.eql(u8, state, "speaking");
    pushEvent(.{ .agent_state = if (speaking) .speaking else .quiet });
    log.info("agent state: {s}", .{state});
}

fn connectNetwork() AppError!void {
    // NVS-provisioned WiFi (web installer), falling back to the compiled default.
    if (!c.sebastian_net_connect()) return error.NetworkConnectFailed;
    try requireEspOk(c.esp_wifi_set_ps(c.WIFI_PS_NONE), error.WifiPowerSaveFailed);
    startSntp();
}

fn openSession(audio: AudioPipeline, conn: token.Connection) AppError!void {
    var opts = std.mem.zeroes(c.livekit_room_options_t);
    opts.subscribe.kind = c.LIVEKIT_MEDIA_TYPE_AUDIO;
    opts.subscribe.renderer = audio.speaker;
    opts.publish.kind = c.LIVEKIT_MEDIA_TYPE_AUDIO;
    opts.publish.audio_encode.codec = c.LIVEKIT_AUDIO_CODEC_OPUS;
    opts.publish.audio_encode.sample_rate = 48000;
    opts.publish.audio_encode.channel_count = 1;
    opts.publish.capturer = audio.mic;
    opts.on_state_changed = onStateChanged;
    opts.on_participant_info = onParticipantInfo;
    opts.on_data_received = onDataReceived;

    try requireLiveKitOk(c.livekit_room_create(&room, &opts), error.RoomCreateFailed);
    // If connect fails, destroy the room we just created. Without this the
    // half-open room leaks with its tasks alive (websocket, peer, SCTP retry
    // loops) and the next session creates a new room over the same handle —
    // the source of the endless "SCTP: Send INIT chunk" storms.
    errdefer {
        _ = c.livekit_room_close(room);
        _ = c.livekit_room_destroy(room);
        room = null;
    }
    logHeap("pre-connect");
    try requireLiveKitOk(c.livekit_room_connect(room, conn.server_url, conn.token), error.RoomConnectFailed);
    logHeap("post-connect");
    log.info("session open", .{});
}

fn closeSession() void {
    if (room == null) return;
    _ = c.livekit_room_close(room);
    _ = c.livekit_room_destroy(room);
    room = null;
    log.info("session closed", .{});
}

// Free internal-RAM telemetry. The scarce pool is internal DMA-capable RAM: the
// TLS hardware-AES DMA and the WebRTC/WiFi stacks all draw from it, and it is
// what starved during the WSS handshake (the 15KB-probe-buffer regression). Log
// it at boot and around room_connect so the real headroom is a measured number,
// not a guess. See ROADMAP Pending #4.
fn logHeap(label: []const u8) void {
    const int_free = c.heap_caps_get_free_size(c.MALLOC_CAP_INTERNAL);
    const int_big = c.heap_caps_get_largest_free_block(c.MALLOC_CAP_INTERNAL);
    const dma_free = c.heap_caps_get_free_size(c.MALLOC_CAP_INTERNAL | c.MALLOC_CAP_DMA);
    const dma_big = c.heap_caps_get_largest_free_block(c.MALLOC_CAP_INTERNAL | c.MALLOC_CAP_DMA);
    log.info("heap[{s}] internal free={d} largest={d} | dma free={d} largest={d}", .{
        label, int_free, int_big, dma_free, dma_big,
    });
}

// ── Session watchdog ──────────────────────────────────────────────────────────
// livekit_room_close can block forever on a wedged connection (observed: main
// loop frozen, device mute until manual reset). A separate task enforces a
// hard deadline armed around every session; if the main loop doesn't disarm
// in time, reboot clean (~8s back to listening) instead of hanging for good.

var wdg_deadline_s = std.atomic.Value(i32).init(0); // 0 = disarmed (Xtensa: 32-bit atomics only)

fn nowSeconds() i32 {
    return @intCast(@divTrunc(c.esp_timer_get_time(), 1_000_000));
}

fn wdgArm(seconds: i32) void {
    wdg_deadline_s.store(nowSeconds() + seconds, .release);
}

fn wdgDisarm() void {
    wdg_deadline_s.store(0, .release);
}

fn wdgTask(_: ?*anyopaque) callconv(.c) void {
    while (true) {
        c.vTaskDelay(1000);
        const deadline = wdg_deadline_s.load(.acquire);
        if (deadline != 0 and nowSeconds() > deadline) {
            log.err("session watchdog expired — restarting", .{});
            c.vTaskDelay(200); // let the log flush
            c.esp_restart();
        }
    }
}

fn logStageError(comptime stage: []const u8, err: anyerror) void {
    log.err("{s} failed: {s}", .{ stage, @errorName(err) });
}

fn confirmXvfMaster() bool {
    xvf_dfu.ensureMaster(board.i2cBus()) catch |err| {
        logStageError("XVF master firmware confirmation", err);
        return false;
    };
    return true;
}

fn unmuteXvf() bool {
    xvf_dfu.unmute() catch |err| {
        logStageError("XVF unmute", err);
        return false;
    };
    return true;
}

fn fullDuplexAllowed(aec_configured: bool) bool {
    if (!cfg.full_duplex) return false;
    if (!aec_configured) {
        // Both paths need the verified XVF config: path A for the converged
        // linear AEC, path B because FAR_END_DSP_ENABLE=1 is also what feeds
        // the far-end reference to the comms residual suppressor.
        log.err("full-duplex requested but AEC config failed — forcing half-duplex", .{});
        return false;
    }
    if (cfg.mic_channel == .left) {
        // Path B (comms beam): the non-linear residual suppressor cancels the
        // loudspeaker echo WITHOUT a converged linear AEC and WITH the adaptive
        // beam (hardware-validated, probeDualChannel: echo-rise −2 568 vs
        // +101 655 raw). A fixed beam is not a prerequisite on this channel —
        // full-duplex WITH talker tracking.
        return true;
    }
    // Path A (raw ASR beam): only the converged linear AEC protects the mic,
    // and it only converges on a fixed beam.
    if (!cfg.fixed_beam) {
        log.err("full-duplex requested but fixed_beam=false — forcing half-duplex", .{});
        return false;
    }
    return true;
}

fn configureFullDuplex(aec_configured: bool) void {
    mic_src.setFullDuplex(fullDuplexAllowed(aec_configured));
}

fn startWakeDetection() void {
    xvf_ui.setState(.idle); // ring: dim breath — armed, not listening
    log.info("waiting for wake word…", .{});
    wakeword.start();
}

fn waitForWakeWord() void {
    while (!wakeword.detected.load(.acquire)) {
        c.vTaskDelay(50);
    }
    xvf_ui.setState(.waking); // ring: orbiting pixel — heard you, connecting
    log.info("wake word confirmed — fetching token", .{});
}

fn fetchConnectionAfterWake() ?token.Connection {
    return token.fetch() catch |err| {
        wakeword.stop(); // stop task and resync I2S for the next idle cycle
        logStageError("token fetch", err);
        c.vTaskDelay(2000);
        return null;
    };
}

fn nextWakeId() u32 {
    wake_seq +%= 1;
    return wake_seq;
}

fn openSessionForWake(audio: AudioPipeline, conn: token.Connection, wake_id: u32) bool {
    log.info("opening session wake_id={d} pre_roll_ms={d}", .{ wake_id, pre_roll.availableMs() });
    // Session hygiene BEFORE the room exists: drop any event raced from the end
    // of the previous session (a late "close" must not kill this one at tick 0)
    // and any speaker tail still in the render-peak accumulator.
    _ = c.xQueueGenericReset(event_queue, 0);
    render_peak.store(0, .monotonic);
    // Budget: max session + connect/close margin. Disarmed after teardown;
    // if anything below wedges, the watchdog reboots us back to listening.
    wdgArm(@as(i32, SESSION_MAX_TICKS * SESSION_TICK_MS / 1000) + 45);
    openSession(audio, conn) catch |err| {
        logStageError("session open", err);
        wakeword.stop();
        wdgDisarm();
        c.vTaskDelay(2000); // brief cooldown before retrying wake word
        return false;
    };
    return true;
}

fn classifyConnection(state: c_int) reducer.Connection {
    if (state == c.LIVEKIT_CONNECTION_STATE_CONNECTED) return .connected;
    if (state == c.LIVEKIT_CONNECTION_STATE_DISCONNECTED or
        state == c.LIVEKIT_CONNECTION_STATE_FAILED) return .ended;
    return .connecting;
}

fn sampleTick(raw_state: *c_int) reducer.Event {
    raw_state.* = c.livekit_room_get_state(room);
    return .{ .tick = .{
        .connection = classifyConnection(raw_state.*),
        .mic_level = mic_src.level(),
        .render_peak = render_peak.swap(0, .monotonic),
        .barge_request = mic_src.takeBargeRequest(),
    } };
}

fn completeWakeHandoff(st: *reducer.State, wake_id: u32) void {
    // Timed: the handoff seam is where start-of-session micro-cuts happen
    // (recordings show razor-cut frames right here). stop_ms is how long the
    // live mic waited for the wake task to exit; send_ms is the pre-roll
    // data-channel burst that competes with the first live audio frames.
    const t0 = c.esp_timer_get_time();
    wakeword.stop();
    const t_stop = c.esp_timer_get_time();
    mic_src.setLive(true);
    const sent = pre_roll.send(room, wake_id);
    const t_send = c.esp_timer_get_time();
    log.info("mic handoff wake_id={d} pre_roll_ms={d} stop_ms={d} send_ms={d}", .{
        wake_id,
        pre_roll.availableMs(),
        @divTrunc(t_stop - t0, 1000),
        @divTrunc(t_send - t_stop, 1000),
    });
    _ = st.step(.{ .preroll_sent = sent });
}

fn retryPreroll(st: *reducer.State, wake_id: u32) void {
    if (pre_roll.send(room, wake_id)) {
        log.info("pre-roll retry ok (attempt {d})", .{st.preroll_attempts});
        _ = st.step(.{ .preroll_sent = true });
        return;
    }
    _ = st.step(.{ .preroll_sent = false });
    if (st.preroll == .given_up) {
        log.warn("pre-roll given up after {d} retries", .{st.preroll_attempts});
    }
}

fn logAecDiagnostics(win: reducer.AecLog) void {
    xvf_aec.logState();
    log.info("echo: gated_peak={d} live_echo={d} render_peak={d} keepalive={}", .{
        mic_src.takeGatedPeak(),
        win.echo_window,
        win.render_peak_window,
        win.keepalive,
    });

    // Capture health — nonzero means the published voice had micro-cuts
    // this window (short reads / timeouts / heals). warn so Grafana's log
    // panel surfaces it; silent when healthy.
    const rs = mic_src.takeReadStats();
    if (!rs.healthy()) {
        log.warn("mic capture: short_reads={d} pad_samples={d} timeouts={d} heals={d}", .{
            rs.short_reads, rs.pad_samples, rs.timeouts, rs.heals,
        });
    }
}

fn applyActions(st: *reducer.State, actions: reducer.Actions, wake_id: u32) void {
    // Latency-sensitive first: the half-duplex gate and the barge-in flush.
    if (actions.mic_gate) |speaking| mic_src.setAgentSpeaking(speaking);
    if (actions.flush_render) {
        // Barge-in: the agent aborted mid-speech, but SECONDS of its reply can
        // still sit in the render FIFO (the model generates faster than
        // realtime), so the speaker keeps narrating after the model went quiet
        // ("no para"). Dump everything queued; the stream stays open for new
        // audio (av_render_flush, not reset).
        if (speaker_render != null) _ = c.av_render_flush(speaker_render);
        log.info("agent interrupted — render FIFO flushed", .{});
    }
    if (actions.publish_barge) publishBargeIn();
    if (actions.ui_active) xvf_ui.setState(.active); // ring: DoA beam — I'm listening
    if (actions.handoff) completeWakeHandoff(st, wake_id);
    if (actions.retry_preroll) retryPreroll(st, wake_id);
    if (actions.log_aec) |win| logAecDiagnostics(win);
}

fn logSessionClose(st: *const reducer.State, reason: reducer.CloseReason, raw_state: c_int) void {
    // These lines feed tools/telemetry/bridge.py (RE_CLOSE_REASON) — the
    // phrases "disconnected early", "agent close", "silence timeout" and
    // "max duration" are load-bearing.
    const quiet_ms = (st.timing.tick - st.timing.last_voice_tick) * SESSION_TICK_MS;
    switch (reason) {
        .disconnected => log.info("room disconnected early (state={})", .{raw_state}),
        .agent_close => log.info("session agent close: quiet_ms={d}", .{quiet_ms}),
        .silence => log.info("session silence timeout: level={d} threshold={d} quiet_ms={d}", .{
            st.last_level, reducer.VOICE_LEVEL, quiet_ms,
        }),
        .max_duration => log.info("session max duration reached ({d} ms) — closing", .{SESSION_MAX_TICKS * SESSION_TICK_MS}),
    }
}

fn runActiveSession(wake_id: u32) void {
    var st = reducer.State{};
    var raw_state: c_int = c.LIVEKIT_CONNECTION_STATE_DISCONNECTED;
    while (true) {
        var event: reducer.Event = undefined;
        // The queue wait doubles as the tick clock: a discrete event wakes the
        // loop immediately (the old poll added up to a full tick of gate/flush
        // lag), a timeout becomes the tick carrying the sampled observations.
        if (c.xQueueReceive(event_queue, &event, SESSION_TICK_MS) != 1) {
            event = sampleTick(&raw_state);
        }
        applyActions(&st, st.step(event), wake_id);
        switch (st.phase) {
            .done => |reason| return logSessionClose(&st, reason, raw_state),
            else => {},
        }
    }
}

fn teardownActiveSession() void {
    wakeword.stop(); // no-op if the handoff already stopped it
    mic_src.setLive(false);
    mic_src.setAgentSpeaking(false); // never carry a stale gate into the next session
    wdgArm(25); // close budget — livekit_room_close has hung forever before
    closeSession();
    wdgDisarm();
    xvf_aec.logState(); // post-session: did the AEC converge this session?
    // Let the speaker's render FIFO drain before re-arming detection — the agent
    // says its own name and a tail leaking into the mic would immediately re-trigger.
    c.vTaskDelay(1500);
}

fn runWakeCycle(audio: AudioPipeline) void {
    startWakeDetection();
    waitForWakeWord();

    // The wake task keeps draining I2S into the pre-roll ring through token fetch
    // and connect; LiveKit remains disconnected here, so IDLE costs zero minutes.
    const conn = fetchConnectionAfterWake() orelse return;
    const wake_id = nextWakeId();
    if (!openSessionForWake(audio, conn, wake_id)) return;

    runActiveSession(wake_id);
    teardownActiveSession();
}

export fn app_main() callconv(.c) void {
    var health = BootHealth{};

    initLiveKitSystem() catch |err| {
        logStageError("livekit system init", err);
        return;
    };

    board.init() catch |err| {
        log.err("board init failed: {s} — halting", .{@errorName(err)});
        return;
    };

    // Start the serial provisioning receiver early: it must be listening even if
    // WiFi never comes up (bad/absent creds) so the web installer can fix it.
    c.sebastian_provisioning_start();
    // Pull mode/audio config from NVS (defaults from config.zig) before it is
    // read: the XVF/AEC apply below and the boot self-tests depend on it.
    cfg.load();

    const xvf_master = confirmXvfMaster();
    const xvf_unmuted = unmuteXvf();
    xvf_ui.start();
    // applyConfig writes the gains + fixes the beam, all readback-verified; it
    // returns false if any of that didn't take (e.g. the beam is not actually
    // fixed → the AEC can't converge).
    const aec_configured = xvf_aec.applyConfig();
    health.xvf = xvf_master and xvf_unmuted and aec_configured;
    configureFullDuplex(aec_configured);
    xvf_aec.logConfig(); // reference-chain snapshot (AEC diagnostics)
    // AEC convergence self-test (config.probe_aec_on_boot): plays a session-level
    // tone and reports converged — tests the REF_GAIN fix with no session/human.
    if (cfg.probe_aec_on_boot) xvf_aec.probeReference();
    if (cfg.probe_dual_channel_on_boot) xvf_aec.probeDualChannel();
    if (cfg.probe_output_gain_on_boot) xvf_aec.probeOutputGain();

    registerAudioCodecs() catch |err| {
        logStageError("audio codec registration", err);
        return;
    };
    health.codecs = true;

    const audio = buildAudioPipeline() catch |err| {
        logStageError("audio pipeline", err);
        return;
    };
    health.audio = true;

    connectNetwork() catch |err| {
        logStageError("network", err);
        return;
    };

    // Network is up: start mirroring logs to the remote syslog server, so the
    // device's serial output reaches Loki even though nothing reads its UART in
    // prod (power + WiFi only). No-op if syslog_ip is unprovisioned.
    c.sebastian_syslog_start();

    if (wakeword.init()) {
        health.ww = true;
        log.info("wake word model loaded (okay_nabu, 59KB)", .{});
    } else {
        log.err("wake word model init failed — halting", .{});
        return;
    }
    _ = pre_roll.init();

    if (health.ok()) {
        log.info("BOOT OK — XVF master, codecs, mic+speaker, model loaded", .{});
    } else {
        log.err("BOOT DEGRADED — xvf={} codecs={} audio={} ww={}", .{
            health.xvf, health.codecs, health.audio, health.ww,
        });
    }
    logHeap("boot");

    event_queue = c.xQueueGenericCreate(EVENT_QUEUE_LEN, @sizeOf(reducer.Event), 0);
    if (event_queue == null) {
        log.err("session event queue allocation failed — halting", .{});
        return;
    }

    // Watchdog on core 1, above the main loop's priority: it must run even if
    // the main task wedges inside a blocking LiveKit call.
    _ = c.xTaskCreatePinnedToCore(wdgTask, "session_wdg", 3072, null, 5, null, 1);

    while (true) {
        runWakeCycle(audio);
    }
}

fn panicFn(msg: []const u8, _: ?usize) noreturn {
    log.err("PANIC: {s}", .{msg});
    c.abort();
}

pub const std_options: std.Options = .{
    .log_level = .info,
    .logFn = @import("log.zig").espLogFn,
};
pub const panic = std.debug.FullPanic(panicFn);
