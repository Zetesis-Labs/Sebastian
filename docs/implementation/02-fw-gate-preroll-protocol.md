> **Implementation report annex** ([`IMPLEMENTATION.md`](../../IMPLEMENTATION.md)). Full text of the multi-agent exploration from 2026-07-02 (8 agents in parallel + cross-check). Where this annex contradicts the **Frozen decisions** of the main report, the main report prevails.

# fw-gate-preroll-protocol — Publish gate + pre-roll + invocation state machine in Zig firmware, and device↔agent protocol over LiveKit C SDK 0.3.10

**Verdict:** viable with risks — All the necessary API surface exists and is verified in the vendored code (publish_data, incoming RPC, data streams) and in livekit-agents 1.6.4 (perform_rpc, byte streams, chainable io.AudioInput). The real risks are the Developer Preview condition of the C SDK (no outgoing RPC from the device, no buffering of data packets) and that the "inject pre-roll to track" option is unviable with the current pipeline, which forces the data-stream route.

**Effort:** M — 6–8 person-days: 3–4 firmware (bindings, invocation.zig, mic_src, xvf_ui), 2–3 agent (byte stream handler, PrerollInput, RPCs, WW verification), 1 E2E integration with button-tap as synthetic wake. Does not include training the wake word nor the complete migration to livekit-agents 1.6.4.

## Findings
- The vendored C SDK 0.3.10 DOES NOT have outgoing RPC: livekit.h references livekit_room_rpc_invoke in a comment but neither the header nor rpc_manager.h/c implement it (only register/unregister/handle_packet). Device→agent direction is limited to publish_data (user packets) and data streams; agent→device uses RPC.  
  _firmware/managed_components/livekit__livekit/include/livekit.h:210-212 vs core/rpc_manager.h:44-56_
- RPC handlers are invoked synchronously from the esp_peer task with the invocation on the stack: send_result MUST be called before returning (no deferred response) and the handler's ctx arrives NULL. This forces a parse→enqueue→respond pattern.  
  _firmware/managed_components/livekit__livekit/core/rpc_manager.c:134-146_
- The capture pipeline rewrites the pts by frame counter after each read_frame — the updatePts of mic_src.zig is ignored. There is no interface to 'advance pts': the pre-roll option (a) lacks a timestamp lever.  
  _firmware/managed_components/espressif__esp_capture/impl/capture_gmf_path/src/elements/gmf_audio_src.c:118_
- The source→encoder queue is only 3 frames of 10 ms ((audio_frame_size+32)*3): draining 2 s of pre-roll is limited by the Opus encoder CPU, it cannot be an instant burst. Also read_frame delivers 10 ms frames (480 samples @48k) → decimate ×3 gives 160 samples @16k, exactly the microWakeWord hop.  
  _firmware/managed_components/espressif__esp_capture/impl/capture_gmf_path/src/elements/gmf_audio_src.c:150,169_
- LiveKit already has the 'pre-roll by byte stream' pattern as a product feature (pre-connect audio buffer, topic lk.agent.pre-connect-audio-buffer, PCM s16 or Opus), but it requires trackId/sampleRate/channels attributes that the C SDK's livekit_data_stream_options_t cannot send (no attributes field in the writer) → it must be replicated with its own topic and custom handler in the agent.  
  _https://github.com/livekit/agents/blob/main/livekit-agents/livekit/agents/voice/room_io/_pre_connect_audio.py and core/data_stream_writer.c (grep attributes: empty)_
- livekit-agents 1.6.4 (PyPI 2026-06-24) provides the entire agent side: local_participant.perform_rpc(destination_identity, method, payload≤15KiB), room.register_byte_stream_handler(topic), and io.AudioInput is a chainable async-iterator via source= with on_attached/on_detached — perfect foundation to prepend the pre-roll to session.input.audio.  
  _https://docs.livekit.io/transport/data/rpc/ and https://github.com/livekit/agents/blob/main/livekit-agents/livekit/agents/voice/io.py_
- The assumed 'silence ≈ 0 network' of the ROADMAP is not strictly fulfilled: esp_opus_enc's DTX can only be activated at 8/12/16 kHz (we publish at 48 kHz) and also enable_dtx=false by default without the SDK exposing it. The ARMED silence will still cost a few kbps.  
  _firmware/managed_components/espressif__esp_audio_codec/include/encoder/impl/esp_opus_enc.h:89-105_
- publish_data fails outright if the engine is not CONNECTED ('TODO: Implement buffering for reliable packets'): wake/button events need their own retry with backoff in the invocation task.  
  _firmware/managed_components/livekit__livekit/core/engine.c:1291-1294_
- xvf_ui.zig currently owns the mute: it forces mic.setMuted(xvf.readMuted()) every 80 ms from its task — if not refactored, it will trample the state machine's gate every cycle.  
  _firmware/main/xvf_ui.zig:42-43_
- Project Zig = Espressif fork 0.16.0-xtensa: std.json.parseFromSliceLeaky + FixedBufferAllocator remains the heapless parsing route, but serialization (std.json.Stringify) became unstable after writergate (regressions reported in 0.15/0.16-dev) → emit JSON with std.fmt.bufPrint and comptime templates.  
  _docs/FIRMWARE.md:11 and https://github.com/ziglang/zig/issues/24468_

## Design

# Gate + pre-roll + invocation (firmware) and device↔agent protocol

## 1. State machine — `firmware/main/invocation.zig` (new)

```
        local WW / button-tap (device)         turn detected (agent)
ARMED ─────────────────────► ATTENDING ─────────────────────► ENGAGED
  ▲  gate closed             │ gate OPEN already (optimistic)   │ gate open
  │  (publishes silence)     │ + wake evt + pre-roll stream     │ speaking ⇒ half_duplex
  │                          │                                  ▼
  ├── watchdog 8 s ◄─────────┘ agent veto (state:"idle")      LINGER (ttl 8 s, gate open)
  └────────── ttl expires (device) / state:"idle" (agent) ◄─────┘
```

| Transition | Owner | Mechanism |
|---|---|---|
| ARMED→ATTENDING | device | microWakeWord or button tap → `onWakeDetected()` opens gate without RTT |
| ATTENDING→ENGAGED | agent | RPC `sebastian.state {"state":"listening"}` after re-verifying WW with the pre-roll |
| ATTENDING→ARMED | both | agent veto (`"idle"`) or device watchdog 8 s without RPC |
| ENGAGED internal | agent | `state: listening/thinking/speaking` (speaking ⇒ reason `half_duplex` ON) |
| ENGAGED→LINGER | agent | `{"state":"linger","ttl_ms":8000}` — gate stays open; the DDSD lives in the agent |
| LINGER→ATTENDING | agent | DDSD accepts the reply → `"listening"` (renews ttl) |
| LINGER→ARMED | device | ttl expires → closes gate and publishes evt `{"type":"state","state":"armed"}` |
| *→ARMED | device | button long-press / room disconnection |

IDLE (disconnect room, Cloud economy) is postponed: in P0 IDLE≡ARMED. The
`invocation` task (core 1, prio 5) consumes a FreeRTOS queue (RPC/button/WW events)
and runs timeouts; RPC handlers only parse+enqueue (§4).

## 2. Gate: replaces `muted_flag` in mic_src

`gate: std.atomic.Value(u8)` with OR-ed reasons: `button` (physical mute —
also freezes ring and WW: real privacy), `state` (ARMED) and `half_duplex`
(agent speaking; necessary until AEC finding #2 is closed). `readFrame` emits
silence if `gate != 0` — same path as the current `muted_flag`; pts is not
touched (gmf_audio_src.c:118 recalculates it by counter and ignores ours).

`xvf_ui.zig` stops owning the mute: its task changes from `mic.setMuted(readMuted())`
to `invocation.setButtonMute(muted)`, and paints patterns according to state: ARMED = current dim
· ATTENDING/listening = bright beam locked to the wake's DoA ·
thinking = spinner · speaking = pulse · LINGER = pulsing dim · button-mute = off.
In `xvf_dfu.zig` add `readAzimuthDeg()` (same resid 33 / cmd 75, beam
auto-select in radians) for `wake.doa_deg` and `doa` telemetry.

## 3. Pre-roll: analysis and decision

Ring in PSRAM: `heap_caps_malloc(64 KB, MALLOC_CAP_SPIRAM)` = 2 s @16 kHz mono
i16 (+64 KB staging), fed from `readFrame` decimating ×3 every 10 ms
frame (480 samples 48k → 160 @16k = exact microWakeWord hop).

- **(a) Inject to track draining faster than real-time — DISCARDED.**
  (1) no pts lever: the pipeline rewrites it by frame counter;
  (2) the src→encoder queue is 3×10 ms → the "burst" is limited by the Opus encoder
  CPU, a CPU spike exactly when the WW needs headroom; (3) the ~2 s
  backlog reaches the jitter buffer (NetEQ) of the agent's Rust SDK, which in response
  accelerates or FLUSHES — we would lose precisely the pre-roll. Non-deterministic.
- **(b) Pre-roll via data stream — RECOMMENDED.** Deterministic: reliable channel,
  automatic chunking (15000 B → 5 chunks ≈ 64 KB, <200 ms on WiFi), the track
  always remains real-time and the agent receives the exact PCM to re-verify
  the WW (ROADMAP signal #2) + STT context. It is the same pattern as the
  official LiveKit "pre-connect audio buffer"; the native topic
  `lk.agent.pre-connect-audio-buffer` is not usable (requires attributes that the C
  SDK cannot send) → custom topic `sebastian.preroll` + custom handler.
- **(c) No pre-roll:** loses the wake word (no re-verification) and the
  start of the command. Only a runtime degradation if (b) fails.

Flow (b): `onWakeDetected` → 1) opens gate (optimistic, no RTT; pre-roll covers
[-2 s, opening] → seamless with live); 2) snapshot ring→staging (memcpy
PSRAM); 3) `publish_data` reliable `sebastian.evt` with `wake{...}`; 4)
`data_stream_open/write/close` on `sebastian.preroll`: binary header 12 B
(magic "SBPR", ver u8, wake_id u32, sr u16=16000) + s16le. Agent:
`register_byte_stream_handler` → openWakeWord/classifier on the PCM → if OK,
prepends the frames with `PrerollInput(io.AudioInput)` chained to
`session.input.audio` and replies RPC `state:"listening"`; if KO, veto `"idle"`.

## 4. Protocol (0.3.10: no outgoing RPC from device — verified)

| Msg | Dir | Channel | Rationale |
|---|---|---|---|
| `wake {wake_id,score,doa_deg,speaker_hint:null}` | dev→ag | publish_data reliable, topic `sebastian.evt` | only guaranteed outgoing channel; the "ack" is the agent's `state` RPC |
| `button {type:"tap"\|"long"\|"double"}` | dev→ag | ditto | critical event |
| `state {state:"armed"}` (mirror) | dev→ag | ditto | the agent follows the gate |
| `doa {deg,level}` @5 Hz | dev→ag | publish_data **lossy**, topic `sebastian.doa` | lossy telemetry (DDSD, multi-device) |
| pre-roll | dev→ag | data stream bytes `sebastian.preroll` | 64 KB chunked reliable |
| `state {state, ttl_ms?}` | ag→dev | **RPC** `sebastian.state` | synchronous ack: the agent knows the gate changed |
| `led {pattern,r,g,b}` / `volume {level}` | ag→dev | RPC `sebastian.led` / `sebastian.volume` | response = applied state |
| `announce {chime}` | ag→dev | RPC `sebastian.announce` | synchronous response `{"busy":true}` if `mic_level` gives away conversation (signal #5) |

Hard constraints: the RPC handler runs in the esp_peer task with the invocation
on the stack (`send_result` before returning, ctx=NULL) → parse, enqueue,
respond, never block. `publish_data` fails if engine≠CONNECTED (no
buffering) → retries with backoff in the invocation task. Agent identity:
capture in `on_participant_info` (kind==AGENT, state ACTIVE) for
`destination_identities` and sender filter in RPC/data.

## 5. JSON in Zig (0.16-xtensa fork)

Incoming (≤ ~200 B): `std.json.parseFromSliceLeaky(Msg, fba.allocator(), payload,
.{ .ignore_unknown_fields = true })` with `FixedBufferAllocator` over a 1 KB
static buffer — zero heap. Outgoing: NO stringify (unstable post-writergate);
`std.fmt.bufPrint` with comptime templates — the 4 messages have fixed shape.

## 6. Implementation order

1. `csdk.zig`: bindings of snippet-a + typing the opaque callbacks of `livekit_room_options_t`.
2. `invocation.zig` (snippet-b): states+gate+queue+timeouts; `app.zig` initializes it after `joinRoom` and registers the 4 RPCs.
3. `mic_src.zig` (snippet-c): scratch + decimate ×3 + `feed16k` + `gateOpen`; remove public `muted_flag`/`setMuted` (keep `level()`).
4. `xvf_ui.zig`: button→`setButtonMute` + patterns per state; `xvf_dfu.zig`: `readAzimuthDeg()`.
5. Agent (1.6.4): `register_byte_stream_handler` + `PrerollInput` + `perform_rpc` + WW verification of pre-roll.
6. E2E with button-tap as synthetic wake (still without WW): validates full gate, pre-roll and protocol before the microWakeWord spike.

## Code
**firmware/main/csdk.zig** — (a) Minimum extern bindings to add: publish_data + incoming RPC + data streams + PSRAM + queues, transcribed from the vendored 0.3.10 headers

```zig
// --- Data packets (device→agent: wake/button/doa) — livekit.h:382-423 ---
pub const livekit_data_payload_t = extern struct { bytes: [*]u8, size: usize };
pub const livekit_data_publish_options_t = extern struct {
    payload: *livekit_data_payload_t,
    topic: [*:0]const u8, // char* in C; the SDK does not mutate it
    lossy: bool,
    destination_identities: ?[*][*:0]const u8 = null,
    destination_identities_count: c_int = 0,
};
pub extern fn livekit_room_publish_data(handle: livekit_room_handle_t, options: *livekit_data_publish_options_t) c_int;

// --- Incoming RPC (agent→device) — livekit_rpc.h:69-111, livekit.h:446 ---
pub const LIVEKIT_RPC_RESULT_OK: c_int = 0;
pub const livekit_rpc_result_t = extern struct {
    id: [*:0]const u8,
    code: c_int,
    error_message: ?[*:0]const u8 = null,
    payload: ?[*:0]const u8 = null,
};
pub const livekit_rpc_invocation_t = extern struct {
    id: [*:0]u8,
    method: [*:0]u8,
    caller_identity: [*:0]u8,
    payload: ?[*:0]u8, // NULL or valid cstring (guaranteed by SDK)
    send_result: *const fn (*const livekit_rpc_result_t, ?*anyopaque) callconv(.c) bool,
    ctx: ?*anyopaque,
};
pub const livekit_rpc_handler_t = *const fn (*const livekit_rpc_invocation_t, ?*anyopaque) callconv(.c) void;
pub extern fn livekit_room_rpc_register(handle: livekit_room_handle_t, method: [*:0]const u8, handler: livekit_rpc_handler_t) c_int;

// --- Outgoing data stream (pre-roll) — livekit_data_stream.h:79-90, livekit.h:545-568 ---
pub const livekit_data_stream_handle_t = ?*anyopaque;
pub const livekit_data_stream_options_t = extern struct {
    topic: [*:0]const u8,
    is_text: bool = false,
    total_length: u64 = 0,
    has_total_length: bool = false,
};
pub extern fn livekit_room_data_stream_open(h: livekit_room_handle_t, o: *const livekit_data_stream_options_t, s: *livekit_data_stream_handle_t) c_int;
pub extern fn livekit_room_data_stream_write(h: livekit_room_handle_t, s: livekit_data_stream_handle_t, data: [*]const u8, size: usize) c_int;
pub extern fn livekit_room_data_stream_close(h: livekit_room_handle_t, s: livekit_data_stream_handle_t) c_int;

// --- Type currently opaque callbacks in livekit_room_options_t (livekit.h:117-126,175-188) ---
pub const livekit_data_received_t = extern struct {
    payload: livekit_data_payload_t,
    topic: ?[*:0]u8,
    sender_identity: ?[*:0]u8,
};
pub const livekit_participant_info_t = extern struct {
    sid: ?[*:0]const u8, identity: ?[*:0]const u8, name: ?[*:0]const u8,
    metadata: ?[*:0]const u8, kind: c_int, state: c_int,
};
pub const LIVEKIT_PARTICIPANT_KIND_AGENT: c_int = 4;
pub const LIVEKIT_PARTICIPANT_STATE_ACTIVE: c_int = 2;

// --- PSRAM + FreeRTOS queue (for invocation.zig) ---
pub const MALLOC_CAP_SPIRAM: u32 = 1 << 10;
pub extern fn heap_caps_malloc(size: usize, caps: u32) ?*anyopaque;
pub const QueueHandle_t = ?*anyopaque;
pub extern fn xQueueGenericCreate(len: u32, item_size: u32, qtype: u8) QueueHandle_t; // xQueueCreate
pub extern fn xQueueGenericSend(q: QueueHandle_t, item: *const anyopaque, ticks: u32, pos: c_int) c_int; // xQueueSend: pos=0
pub extern fn xQueueReceive(q: QueueHandle_t, item: *anyopaque, ticks: u32) c_int;
```

**firmware/main/invocation.zig** — (b) State machine skeleton: atomic gate with reasons, pre-roll ring in PSRAM, optimistic opening and non-blocking RPC handler

```zig
//! invocation.zig — ARMED/ATTENDING/ENGAGED/LINGER states, gate and pre-roll.
const std = @import("std");
const c = @import("csdk.zig");

pub const State = enum(u8) { armed, attending, engaged, linger };
pub const Reason = struct { // gate bitmask (0 == open)
    pub const button: u8 = 1 << 0; // physical mute: also freezes ring and WW
    pub const state: u8 = 1 << 1; // closed by state (ARMED)
    pub const half_duplex: u8 = 1 << 2; // agent speaking (until AEC #2 is closed)
};
const Event = union(enum) {
    rpc_state: struct { st: State, ttl_ms: u32 },
    wake: struct { score: f32, doa_deg: u16 },
    button_tap: void,
};

var gate = std.atomic.Value(u8).init(Reason.state); // starts in ARMED
var state = std.atomic.Value(u8).init(@intFromEnum(State.armed));
var room: c.livekit_room_handle_t = null;
var evq: c.QueueHandle_t = null;
var wake_id: u32 = 0;

const RING = 2 * 16000; // 2 s @16 kHz mono i16 = 64 KB
var ring: [*]i16 = undefined;
var staging: [*]i16 = undefined; // snapshot to send without the live feed overwriting it
var wr = std.atomic.Value(u32).init(0); // index [0,RING); only AUD_SRC writes

pub fn init(r: c.livekit_room_handle_t) void {
    room = r;
    ring = @ptrCast(@alignCast(c.heap_caps_malloc(RING * 2, c.MALLOC_CAP_SPIRAM).?));
    staging = @ptrCast(@alignCast(c.heap_caps_malloc(RING * 2, c.MALLOC_CAP_SPIRAM).?));
    evq = c.xQueueGenericCreate(8, @sizeOf(Event), 0);
    _ = c.livekit_room_rpc_register(room, "sebastian.state", onRpcState);
    // ditto sebastian.led / sebastian.volume / sebastian.announce
    _ = c.xTaskCreatePinnedToCore(task, "invocation", 6144, null, 5, null, 1);
}

pub fn gateOpen() bool { return gate.load(.acquire) == 0; }
pub fn currentState() State { return @enumFromInt(state.load(.acquire)); }

/// From mic_src.readFrame (AUD_SRC task), already decimated to 16 kHz.
pub fn feed16k(samples: []const i16) void {
    if (gate.load(.acquire) & Reason.button != 0) return; // hard-mute = privacy
    var w = wr.load(.monotonic);
    for (samples) |s| { ring[w] = s; w = (w + 1) % RING; }
    wr.store(w, .release);
    // future: wakeword.feed(samples) — same hop of 160 samples
}

/// Local WW or button-tap: OPTIMISTIC opening (no RTT) + event + pre-roll.
pub fn onWakeDetected(score: f32, doa_deg: u16) void {
    clearReason(Reason.state); // gate OPEN NOW: pre-roll covers [-2 s, here]
    state.store(@intFromEnum(State.attending), .release);
    const end = wr.load(.acquire);
    for (0..RING) |i| staging[i] = ring[(end + i) % RING]; // old→new
    const ev = Event{ .wake = .{ .score = score, .doa_deg = doa_deg } };
    _ = c.xQueueGenericSend(evq, &ev, 0, 0); // task: publishWake() + sendPreroll()
}

fn onRpcState(inv: *const c.livekit_rpc_invocation_t, _: ?*anyopaque) callconv(.c) void {
    // Runs in the esp_peer task, invocation on stack: enqueue and respond NOW.
    if (parseStateJson(inv.payload)) |ev| _ = c.xQueueGenericSend(evq, &ev, 0, 0);
    var res = c.livekit_rpc_result_t{ .id = inv.id, .code = c.LIVEKIT_RPC_RESULT_OK, .payload = "{\"ok\":true}" };
    _ = inv.send_result(&res, inv.ctx);
}
// task(): xQueueReceive + apply transitions/reasons + watchdogs 8s/ttl +
// publishWake/sendPreroll ("sebastian.preroll" data stream: SBPR header + staging)
```

**firmware/main/mic_src.zig** — (c) Gate hook in readFrame: always convert to scratch (level+ring+WW live even if the gate closes), decimate ×3 and publish voice or silence

```zig
// Replaces muted_flag/setMuted: the gate lives in invocation.zig.
const invocation = @import("invocation.zig");

var scratch: [MAX_SAMPLES]i16 = undefined; // mono 48k after SHIFT + softClip
var deci: [MAX_SAMPLES / 3 + 1]i16 = undefined; // 16 kHz for ring + WW

fn readFrame(_: *c.esp_capture_audio_src_iface_t, frame: *c.esp_capture_stream_frame_t) callconv(.c) c_int {
    if (!instance.started) return c.ESP_CAPTURE_ERR_NOT_SUPPORTED;
    const total = frameSampleCount(frame) orelse return c.ESP_CAPTURE_ERR_NOT_SUPPORTED;
    if (total == 0) return c.ESP_CAPTURE_ERR_OK;
    resyncRxOnce();
    const got = readI2s(total) orelse return c.ESP_CAPTURE_ERR_INTERNAL;

    // ALWAYS convert: mic_level, pre-roll and (future) WW run with closed gate.
    writeCapturedSamples(&scratch, got, total); // same as today but to scratch

    // 48k→16k ×3. Placeholder: average of 3 (enough for pre-roll/STT);
    // change to esp-dsp anti-alias FIR when microWakeWord enters.
    const n16 = total / 3;
    var i: usize = 0;
    while (i < n16) : (i += 1) {
        const a = @as(i32, scratch[i * 3]) + scratch[i * 3 + 1] + scratch[i * 3 + 2];
        deci[i] = @intCast(@divTrunc(a, 3));
    }
    invocation.feed16k(deci[0..n16]); // PSRAM ring (10 ms frames → 160 samples)

    const out: [*]i16 = @ptrCast(@alignCast(frame.data));
    if (invocation.gateOpen()) {
        @memcpy(out[0..total], scratch[0..total]);
    } else {
        fillSilence(out, total); // closed: silence to the room; pts keeps running
    }
    updatePts(frame, total); // harmless: gmf_audio_src recalculates pts by counter
    return c.ESP_CAPTURE_ERR_OK;
}
```

## Risks
- **C SDK in Developer Preview: publish_data without buffering (fails if engine≠CONNECTED) and APIs subject to change; current pin is 0.3.10.** → Retry with backoff in the invocation task for wake/button; freeze the version and review changelog before every bump (if a future 0.3.x adds outgoing rpc_invoke, migrate wake to RPC with real ack).
- **Blocking or slow RPC handler crashes the esp_peer task (audio and signaling share process).** → Mandatory pattern parse→enqueue→immediate send_result; payloads ≤1 KB; validate sender_identity against the agent's identity.
- **The 64 KB pre-roll burst shares the same DTLS/SCTP as the upstream Opus audio: possible momentary jitter at the start of the turn.** → Send the stream from the invocation task right after opening the gate (the live feed is still phrase-start silence); if jitter is observed, chunk the write()s with 10 ms pauses or reduce the pre-roll to 1.5 s (48 KB).
- **WW false positive opens the optimistic gate for ~200-500 ms until the agent's veto: brief audio leak to the cloud.** → Reasonable on-device threshold + fast veto (re-verification only over the WW segment), LED always on while the gate is open (visible honesty), and button/config for 'open only after ack' mode if the user prefers.
- **xvf_ui tramples the gate: currently forces mic.setMuted(readMuted()) every 80 ms.** → Refactor included in the design (step 4): xvf_ui only reports the button via invocation.setButtonMute and reads the state to paint.
- **Half-duplex while AEC finding #2 remains open: no barge-in in P0 and LINGER misses overlaps with the TTS tail.** → Keep the half_duplex reason activatable by config; when closing AEC #2 (P1), deactivate it and enable Adaptive Interruption Handling in the agent.
- **Silence in ARMED is not free (Opus DTX inapplicable at 48 kHz and not exposed by the SDK): network consumption and LiveKit Cloud minutes 24/7.** → Accept it in P0 (few kbps); real plan = IDLE phase with room disconnection + token server, or self-hosted LiveKit during outages (already in ROADMAP P2).
- **std.json from the 0.16-xtensa Zig fork could have holes (writergate).** → Only parseFromSliceLeaky is used (parsing); if the fork fails, a trivial manual parser for 4 fixed-shape messages (~100 lines).

## Open questions
- Pre-roll format: raw s16le (64 KB, zero CPU) as proposed by the design, or compress it (second Opus encoder via esp_audio_codec, ~4x less network but +RAM/CPU)? Confirm with real WiFi measurement.
- WW re-verification in the agent: openWakeWord on the 16 kHz PCM, or STT of the pre-roll + text match of 'Sebastián'? (latency of the veto vs dependencies).
- Agent identity fixed by convention (agent_name in explicit dispatch, e.g. 'sebastian-agent') or only discovered by on_participant_info kind==AGENT? The design uses both (convention + verification).
- Should the IDLE phase (room disconnection + reconnection on wake) be advanced to P0 to cut the cost of LiveKit Cloud minutes, knowing it requires the token server?
- Should the gate in LINGER lower the sensitivity (e.g. require minimum mic_level before considering a reply) to avoid sending 8 s of room noise after each turn?
- Confirm on hardware that the 64 KB PSRAM→PSRAM snapshot (memcpy in the invocation task) does not steal time from AUD_SRC — if it competes, do the copy in 8 KB chunks.

## Sources
- https://docs.livekit.io/agents/multimodality/audio/
- https://github.com/livekit/agents/blob/main/livekit-agents/livekit/agents/voice/room_io/_pre_connect_audio.py
- https://github.com/livekit/agents/blob/main/livekit-agents/livekit/agents/voice/io.py
- https://docs.livekit.io/transport/data/rpc/
- https://docs.livekit.io/reference/python/livekit/rtc/room.html
- https://pypi.org/project/livekit-agents/
- https://github.com/livekit/agents/releases
- https://ziglang.org/download/0.15.1/release-notes.html
- https://github.com/ziglang/zig/issues/24468