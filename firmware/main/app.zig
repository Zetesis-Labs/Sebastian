//! Sebastian firmware entry point.
//!
//! Boot → board bring-up → XVF DFU → codec init → audio pipeline → WiFi/SNTP →
//! load wake word model → idle detection loop.
//!
//! Idle loop (low power, no LiveKit):
//!   wakeword task reads I2S directly and feeds the 62KB streaming CNN.
//!   On detection → keep filling local pre-roll while opening LiveKit →
//!   hand off the mic when the room and agent are ready → silence timeout →
//!   close room → back to wake word detection.
//!
//! The XVF I2S RX channel is shared: the wakeword task owns it during idle,
//! mic_src.readFrame() owns it during the active session. Each hand-off includes
//! an i2s_channel_disable/enable resync to re-latch onto the running clock.

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
const token = @import("token.zig");
const c = @import("csdk.zig");

const log = std.log.scoped(.sebastian);

// Session timing constants + the pure keepalive/silence logic live in
// core/session_core.zig (host-testable). These aliases keep the loop/watchdog
// call sites here readable.
const SESSION_TICK_MS: u32 = session_core.TICK_MS;
const SESSION_MAX_TICKS: u32 = session_core.MAX_TICKS;
const SESSION_VOICE_LEVEL: u32 = 3000;
const AGENT_AUDIO_LEVEL: u32 = 1000; // render peak (>>8) over the auto_clear zero floor = agent speaking
const AEC_LOG_PERIOD_TICKS: u32 = 500;
const AEC_LOG_OFFSET_TICKS: u32 = 250;
const PREROLL_RETRY_TICKS: u32 = 50; // 500ms between pre-roll send retries
const PREROLL_MAX_ATTEMPTS: u32 = 8; // ~4s of retries before giving up

var room: c.livekit_room_handle_t = null;
var agent_ready = std.atomic.Value(bool).init(false);
var agent_speaking = std.atomic.Value(bool).init(false);
// Server-side endpointing: the agent decided the conversation is over and asked
// us to close. The disconnect must stay device-initiated — a server-side room
// delete can leave this client stuck CONNECTED forever (#186 family).
var close_requested = std.atomic.Value(bool).init(false);
// Peak |render sample| (>>8) since the session loop last read it. Fed by the
// av_render reference callback (the speaker output), it is a data-channel-
// INDEPENDENT proof that the agent is talking — the keepalive that survives a
// dead SCTP link. Reset by the session loop each tick via swap.
var render_peak = std.atomic.Value(u32).init(0);
var wake_seq: u32 = 0;

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

const SessionLoopState = struct {
    timing: session_core.Timing = .{},
    render_peak_window: u32 = 0,
    echo_window: u32 = 0,
    last_level: u32 = 0,
    handoff_done: bool = false,
    preroll_pending: bool = false,
    preroll_attempts: u32 = 0,
    marked_active: bool = false,

    fn observeActivity(self: *SessionLoopState) void {
        self.observeMicLevel();
        self.observeAgentDataChannel();
        self.observeRenderPeak();
    }

    fn observeMicLevel(self: *SessionLoopState) void {
        self.last_level = mic_src.level();
        if (self.last_level >= SESSION_VOICE_LEVEL) self.timing.markVoiceActivity();
    }

    fn observeAgentDataChannel(self: *SessionLoopState) void {
        if (agent_speaking.load(.acquire)) self.timing.markVoiceActivity();
    }

    fn observeRenderPeak(self: *SessionLoopState) void {
        const rpeak = render_peak.swap(0, .monotonic);
        self.render_peak_window = @max(self.render_peak_window, rpeak);
        if (rpeak >= AGENT_AUDIO_LEVEL) self.timing.noteAgentAudio();
        if (!self.timing.agentAudioActive()) return;

        self.timing.markVoiceActivity();
        self.echo_window = @max(self.echo_window, self.last_level);
    }

    fn shouldLogAec(self: SessionLoopState) bool {
        return self.timing.tick % AEC_LOG_PERIOD_TICKS == AEC_LOG_OFFSET_TICKS;
    }

    fn logAecDiagnostics(self: *SessionLoopState) void {
        xvf_aec.logState();
        log.info("echo: gated_peak={d} live_echo={d} render_peak={d} keepalive={}", .{
            mic_src.takeGatedPeak(),
            self.echo_window,
            self.render_peak_window,
            self.timing.agentAudioActive(),
        });
        self.render_peak_window = 0;
        self.echo_window = 0;
    }

    fn silenceExpired(self: SessionLoopState) bool {
        return self.timing.silenceExpired();
    }

    fn maxDurationReached(self: SessionLoopState) bool {
        return self.timing.maxDurationReached();
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
    agent_ready.store(active, .release);
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

    const state = bytes[0..data.payload.size];
    if (std.mem.eql(u8, state, "interrupted")) {
        // Barge-in: the agent aborted mid-speech, but SECONDS of its reply can
        // still sit in the render FIFO (the model generates faster than
        // realtime), so the speaker keeps narrating after the model went quiet
        // ("no para"). Dump everything queued; the stream stays open for new
        // audio (av_render_flush, not reset).
        agent_speaking.store(false, .release);
        mic_src.setAgentSpeaking(false);
        if (speaker_render != null) _ = c.av_render_flush(speaker_render);
        log.info("agent interrupted — render FIFO flushed", .{});
        return;
    }
    if (std.mem.eql(u8, state, "close")) {
        close_requested.store(true, .release);
        return;
    }
    const speaking = std.mem.eql(u8, state, "speaking");
    agent_speaking.store(speaking, .release);
    mic_src.setAgentSpeaking(speaking); // half-duplex: gate the mic while it talks
    log.info("agent state: {s}", .{state});
}

fn connectNetwork() AppError!void {
    // NVS-provisioned WiFi (web installer), falling back to the compiled default.
    if (!c.sebastian_net_connect()) return error.NetworkConnectFailed;
    try requireEspOk(c.esp_wifi_set_ps(c.WIFI_PS_NONE), error.WifiPowerSaveFailed);
    startSntp();
}

fn openSession(audio: AudioPipeline, conn: token.Connection) AppError!void {
    agent_ready.store(false, .release);

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
    render_peak.store(0, .monotonic); // discard any speaker tail from the previous session
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

fn publishBargeRequest(session: *SessionLoopState) void {
    if (!mic_src.takeBargeRequest()) return;

    agent_speaking.store(false, .release);
    session.timing.markVoiceActivity(); // the user is about to talk
    publishBargeIn();
}

fn connectionEnded(state: c_int) bool {
    return state == c.LIVEKIT_CONNECTION_STATE_DISCONNECTED or
        state == c.LIVEKIT_CONNECTION_STATE_FAILED;
}

fn markRoomActive(session: *SessionLoopState, state: c_int) void {
    if (session.marked_active) return;
    if (state != c.LIVEKIT_CONNECTION_STATE_CONNECTED) return;

    session.marked_active = true;
    xvf_ui.setState(.active); // ring: DoA beam — I'm listening
}

fn completeWakeHandoff(session: *SessionLoopState, state: c_int, wake_id: u32) void {
    if (session.handoff_done) return;
    if (state != c.LIVEKIT_CONNECTION_STATE_CONNECTED) return;
    if (!agent_ready.load(.acquire)) return;

    session.handoff_done = true;
    wakeword.stop();
    mic_src.setLive(true);
    log.info("mic handoff wake_id={d} pre_roll_ms={d}", .{ wake_id, pre_roll.availableMs() });
    session.preroll_pending = !pre_roll.send(room, wake_id);
}

fn retryPrerollIfNeeded(session: *SessionLoopState, wake_id: u32) void {
    if (!session.preroll_pending) return;
    if (session.timing.tick % PREROLL_RETRY_TICKS != 0) return;

    session.preroll_attempts += 1;
    if (pre_roll.send(room, wake_id)) {
        session.preroll_pending = false;
        log.info("pre-roll retry ok (attempt {d})", .{session.preroll_attempts});
        return;
    }
    if (session.preroll_attempts < PREROLL_MAX_ATTEMPTS) return;

    session.preroll_pending = false;
    log.warn("pre-roll given up after {d} retries", .{session.preroll_attempts});
}

fn runActiveSession(wake_id: u32) void {
    var session = SessionLoopState{};
    // A "close" that raced the end of the previous session must not kill this
    // one at tick 0 (same hygiene as the agent_speaking gate in teardown).
    close_requested.store(false, .release);
    while (true) : (session.timing.tick += 1) {
        c.vTaskDelay(SESSION_TICK_MS);
        session.observeActivity();
        publishBargeRequest(&session);
        if (session.shouldLogAec()) session.logAecDiagnostics();

        const state = c.livekit_room_get_state(room);
        markRoomActive(&session, state);
        completeWakeHandoff(&session, state, wake_id);
        retryPrerollIfNeeded(&session, wake_id);

        if (connectionEnded(state)) {
            log.info("room disconnected early (state={})", .{state});
            break;
        }
        if (close_requested.load(.acquire)) {
            // "agent close" is a bridge.py close-reason label — keep the phrase.
            log.info("session agent close: quiet_ms={d}", .{
                (session.timing.tick - session.timing.last_voice_tick) * SESSION_TICK_MS,
            });
            break;
        }
        if (session.silenceExpired()) {
            log.info("session silence timeout: level={d} threshold={d} quiet_ms={d}", .{
                session.last_level,
                SESSION_VOICE_LEVEL,
                (session.timing.tick - session.timing.last_voice_tick) * SESSION_TICK_MS,
            });
            break;
        }
        if (session.maxDurationReached()) {
            log.info("session max duration reached ({d} ms) — closing", .{SESSION_MAX_TICKS * SESSION_TICK_MS});
            break;
        }
    }
}

fn teardownActiveSession() void {
    wakeword.stop(); // no-op if the handoff already stopped it
    mic_src.setLive(false);
    agent_speaking.store(false, .release);
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
        log.info("wake word model loaded (62KB, recall=99.3%)", .{});
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
