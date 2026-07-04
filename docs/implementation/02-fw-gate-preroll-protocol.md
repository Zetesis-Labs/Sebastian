> **Anexo del informe de implementación** ([`IMPLEMENTATION.md`](../../IMPLEMENTATION.md)). Texto íntegro de la exploración multi-agente del 2026-07-02 (8 agentes en paralelo + contraste cruzado). Donde este anexo contradiga las **Decisiones congeladas** del informe principal, prevalece el informe.

# fw-gate-preroll-protocol — Gate de publicación + pre-roll + máquina de estados de invocación en firmware Zig, y protocolo dispositivo↔agente sobre LiveKit C SDK 0.3.10

**Veredicto:** viable con riesgos — Toda la superficie de API necesaria existe y está verificada en el código vendorizado (publish_data, RPC entrante, data streams) y en livekit-agents 1.6.4 (perform_rpc, byte streams, io.AudioInput encadenable). Los riesgos reales son la condición Developer Preview del C SDK (sin RPC saliente desde el device, sin buffering de data packets) y que la opción "inyectar pre-roll al track" es inviable con el pipeline actual, lo que obliga a la vía data-stream.

**Esfuerzo:** M — 6–8 días de una persona: 3–4 firmware (bindings, invocation.zig, mic_src, xvf_ui), 2–3 agente (byte stream handler, PrerollInput, RPCs, verificación WW), 1 integración E2E con botón-tap como wake sintético. No incluye entrenar la wake word ni la migración completa a livekit-agents 1.6.4.

## Hallazgos
- El C SDK 0.3.10 vendorizado NO tiene RPC saliente: livekit.h referencia livekit_room_rpc_invoke en un comentario pero ni el header ni rpc_manager.h/c lo implementan (solo register/unregister/handle_packet). Dirección device→agent queda limitada a publish_data (user packets) y data streams; agent→device usa RPC.  
  _firmware/managed_components/livekit__livekit/include/livekit.h:210-212 vs core/rpc_manager.h:44-56_
- Los handlers RPC se invocan síncronos desde la tarea de esp_peer con la invocación en stack: send_result DEBE llamarse antes de retornar (no hay respuesta diferida) y el ctx del handler llega NULL. Obliga a patrón parsear→encolar→responder.  
  _firmware/managed_components/livekit__livekit/core/rpc_manager.c:134-146_
- El pipeline de captura reescribe el pts por contador de frames tras cada read_frame — el updatePts de mic_src.zig se ignora. No existe interfaz para 'adelantar pts': la opción (a) de pre-roll no tiene palanca de timestamps.  
  _firmware/managed_components/espressif__esp_capture/impl/capture_gmf_path/src/elements/gmf_audio_src.c:118_
- La cola source→encoder es de solo 3 frames de 10 ms ((audio_frame_size+32)*3): drenar 2 s de pre-roll queda limitado por la CPU del encoder Opus, no puede ser un burst instantáneo. Además read_frame entrega frames de 10 ms (480 muestras @48k) → decimar ×3 da 160 muestras @16k, exactamente el hop de microWakeWord.  
  _firmware/managed_components/espressif__esp_capture/impl/capture_gmf_path/src/elements/gmf_audio_src.c:150,169_
- LiveKit ya tiene el patrón 'pre-roll por byte stream' como feature de producto (pre-connect audio buffer, topic lk.agent.pre-connect-audio-buffer, PCM s16 u Opus), pero exige attributes trackId/sampleRate/channels que livekit_data_stream_options_t del C SDK no puede enviar (sin campo attributes en el writer) → hay que replicarlo con topic propio y handler propio en el agente.  
  _https://github.com/livekit/agents/blob/main/livekit-agents/livekit/agents/voice/room_io/_pre_connect_audio.py y core/data_stream_writer.c (grep attributes: vacío)_
- livekit-agents 1.6.4 (PyPI 2026-06-24) da todo el lado agente: local_participant.perform_rpc(destination_identity, method, payload≤15KiB), room.register_byte_stream_handler(topic), y io.AudioInput es un async-iterator encadenable vía source= con on_attached/on_detached — base perfecta para anteponer el pre-roll a session.input.audio.  
  _https://docs.livekit.io/transport/data/rpc/ y https://github.com/livekit/agents/blob/main/livekit-agents/livekit/agents/voice/io.py_
- El supuesto 'silencio ≈ 0 red' del ROADMAP no se cumple tal cual: el DTX de esp_opus_enc solo puede activarse a 8/12/16 kHz (publicamos a 48 kHz) y además enable_dtx=false por defecto sin que el SDK lo exponga. El silencio de ARMED seguirá costando unos pocos kbps.  
  _firmware/managed_components/espressif__esp_audio_codec/include/encoder/impl/esp_opus_enc.h:89-105_
- publish_data falla en seco si el engine no está CONNECTED ('TODO: Implement buffering for reliable packets'): los eventos wake/button necesitan reintento propio con backoff en la tarea de invocación.  
  _firmware/managed_components/livekit__livekit/core/engine.c:1291-1294_
- xvf_ui.zig hoy es dueño del mute: fuerza mic.setMuted(xvf.readMuted()) cada 80 ms desde su task — si no se refactoriza, pisará el gate de la máquina de estados cada ciclo.  
  _firmware/main/xvf_ui.zig:42-43_
- Zig del proyecto = fork Espressif 0.16.0-xtensa: std.json.parseFromSliceLeaky + FixedBufferAllocator sigue siendo la vía de parseo sin heap, pero la serialización (std.json.Stringify) quedó inestable tras writergate (regresiones reportadas en 0.15/0.16-dev) → emitir JSON con std.fmt.bufPrint y plantillas comptime.  
  _docs/FIRMWARE.md:11 y https://github.com/ziglang/zig/issues/24468_

## Diseño

# Gate + pre-roll + invocación (firmware) y protocolo device↔agente

## 1. Máquina de estados — `firmware/main/invocation.zig` (nuevo)

```
        WW local / botón-tap (device)          turno detectado (agente)
ARMED ─────────────────────► ATTENDING ─────────────────────► ENGAGED
  ▲  gate cerrado            │ gate ABIERTO ya (optimista)      │ gate abierto
  │  (publica silencio)      │ + wake evt + pre-roll stream     │ speaking ⇒ half_duplex
  │                          │                                  ▼
  ├── watchdog 8 s ◄─────────┘ veto agente (state:"idle")     LINGER (ttl 8 s, gate abierto)
  └────────── expira ttl (device) / state:"idle" (agente) ◄─────┘
```

| Transición | Dueño | Mecanismo |
|---|---|---|
| ARMED→ATTENDING | device | microWakeWord o botón tap → `onWakeDetected()` abre gate sin RTT |
| ATTENDING→ENGAGED | agente | RPC `sebastian.state {"state":"listening"}` tras re-verificar WW con el pre-roll |
| ATTENDING→ARMED | ambos | veto del agente (`"idle"`) o watchdog device 8 s sin RPC |
| ENGAGED interno | agente | `state: listening/thinking/speaking` (speaking ⇒ razón `half_duplex` ON) |
| ENGAGED→LINGER | agente | `{"state":"linger","ttl_ms":8000}` — gate sigue abierto; el DDSD vive en el agente |
| LINGER→ATTENDING | agente | DDSD acepta la réplica → `"listening"` (renueva ttl) |
| LINGER→ARMED | device | expira ttl → cierra gate y publica evt `{"type":"state","state":"armed"}` |
| *→ARMED | device | botón long-press / desconexión de sala |

IDLE (desconectar sala, economía Cloud) se pospone: en P0 IDLE≡ARMED. La tarea
`invocation` (core 1, prio 5) consume una cola FreeRTOS (eventos de RPC/botón/WW)
y corre timeouts; los handlers RPC solo parsean+encolan (§4).

## 2. Gate: sustituye `muted_flag` de mic_src

`gate: std.atomic.Value(u8)` con razones OR-eadas: `button` (mute físico —
congela también ring y WW: privacidad real), `state` (ARMED) y `half_duplex`
(agente hablando; necesario hasta cerrar el hallazgo AEC #2). `readFrame` emite
silencio si `gate != 0` — mismo camino que el `muted_flag` actual; el pts no se
toca (gmf_audio_src.c:118 lo recalcula por contador e ignora el nuestro).

`xvf_ui.zig` deja de poseer el mute: su task pasa de `mic.setMuted(readMuted())`
a `invocation.setButtonMute(muted)`, y pinta patrones según estado: ARMED = dim
actual · ATTENDING/listening = beam brillante lockeado al DoA del wake ·
thinking = spinner · speaking = pulso · LINGER = dim pulsante · button-mute = off.
En `xvf_dfu.zig` añadir `readAzimuthDeg()` (mismo resid 33 / cmd 75, beam
auto-select en radianes) para `wake.doa_deg` y la telemetría `doa`.

## 3. Pre-roll: análisis y decisión

Ring en PSRAM: `heap_caps_malloc(64 KB, MALLOC_CAP_SPIRAM)` = 2 s @16 kHz mono
i16 (+64 KB staging), alimentado desde `readFrame` decimando ×3 cada frame de
10 ms (480 muestras 48k → 160 @16k = hop exacto de microWakeWord).

- **(a) Inyectar al track drenando más rápido que tiempo real — DESCARTADA.**
  (1) no hay palanca de pts: el pipeline lo reescribe por contador de frames;
  (2) la cola src→encoder es de 3×10 ms → el "burst" queda limitado por la CPU
  del encoder Opus, pico de CPU justo cuando el WW necesita margen; (3) los ~2 s
  de backlog llegan al jitter buffer (NetEQ) del SDK Rust del agente, que ante
  eso acelera o FLUSHEA — perderíamos precisamente el pre-roll. No determinista.
- **(b) Pre-roll por data stream — RECOMENDADA.** Determinista: canal reliable,
  chunking automático (15000 B → 5 chunks ≈ 64 KB, <200 ms en WiFi), el track
  queda siempre tiempo-real y el agente recibe el PCM exacto para re-verificar
  la WW (señal #2 del ROADMAP) + contexto STT. Es el mismo patrón que el
  "pre-connect audio buffer" oficial de LiveKit; el topic nativo
  `lk.agent.pre-connect-audio-buffer` no es usable (exige attributes que el C
  SDK no puede enviar) → topic propio `sebastian.preroll` + handler propio.
- **(c) Sin pre-roll:** pierde la wake word (no hay re-verificación) y el
  arranque del comando. Solo degradación runtime si (b) falla.

Flujo (b): `onWakeDetected` → 1) abre gate (optimista, sin RTT; el pre-roll cubre
[-2 s, apertura] → sin costura con el vivo); 2) snapshot ring→staging (memcpy
PSRAM); 3) `publish_data` reliable `sebastian.evt` con `wake{...}`; 4)
`data_stream_open/write/close` en `sebastian.preroll`: header binario 12 B
(magic "SBPR", ver u8, wake_id u32, sr u16=16000) + s16le. Agente:
`register_byte_stream_handler` → openWakeWord/clasificador sobre el PCM → si OK,
antepone los frames con `PrerollInput(io.AudioInput)` encadenada a
`session.input.audio` y responde RPC `state:"listening"`; si KO, veto `"idle"`.

## 4. Protocolo (0.3.10: sin RPC saliente del device — verificado)

| Msg | Dir | Canal | Racional |
|---|---|---|---|
| `wake {wake_id,score,doa_deg,speaker_hint:null}` | dev→ag | publish_data reliable, topic `sebastian.evt` | único canal saliente garantizado; el "ack" es el RPC `state` del agente |
| `button {type:"tap"\|"long"\|"double"}` | dev→ag | ídem | evento crítico |
| `state {state:"armed"}` (espejo) | dev→ag | ídem | el agente sigue el gate |
| `doa {deg,level}` @5 Hz | dev→ag | publish_data **lossy**, topic `sebastian.doa` | telemetría perdible (DDSD, multi-device) |
| pre-roll | dev→ag | data stream bytes `sebastian.preroll` | 64 KB chunked reliable |
| `state {state, ttl_ms?}` | ag→dev | **RPC** `sebastian.state` | ack síncrono: el agente sabe que el gate cambió |
| `led {pattern,r,g,b}` / `volume {level}` | ag→dev | RPC `sebastian.led` / `sebastian.volume` | respuesta = estado aplicado |
| `announce {chime}` | ag→dev | RPC `sebastian.announce` | respuesta síncrona `{"busy":true}` si `mic_level` delata conversación (señal #5) |

Restricciones duras: el handler RPC corre en la tarea esp_peer con la invocación
en stack (`send_result` antes de retornar, ctx=NULL) → parsear, encolar,
responder, jamás bloquear. `publish_data` falla si engine≠CONNECTED (sin
buffering) → reintentos con backoff en la tarea invocation. Identidad del agente:
capturar en `on_participant_info` (kind==AGENT, state ACTIVE) para
`destination_identities` y filtro de remitente en RPC/datos.

## 5. JSON en Zig (fork 0.16-xtensa)

Entrante (≤ ~200 B): `std.json.parseFromSliceLeaky(Msg, fba.allocator(), payload,
.{ .ignore_unknown_fields = true })` con `FixedBufferAllocator` sobre buffer
estático de 1 KB — cero heap. Saliente: NO stringify (inestable post-writergate);
`std.fmt.bufPrint` con plantillas comptime — los 4 mensajes tienen forma fija.

## 6. Orden de implementación

1. `csdk.zig`: bindings del snippet-a + tipar los callbacks opacos de `livekit_room_options_t`.
2. `invocation.zig` (snippet-b): estados+gate+cola+timeouts; `app.zig` lo inicializa tras `joinRoom` y registra los 4 RPC.
3. `mic_src.zig` (snippet-c): scratch + decimador ×3 + `feed16k` + `gateOpen`; eliminar `muted_flag`/`setMuted` público (conservar `level()`).
4. `xvf_ui.zig`: botón→`setButtonMute` + patrones por estado; `xvf_dfu.zig`: `readAzimuthDeg()`.
5. Agente (1.6.4): `register_byte_stream_handler` + `PrerollInput` + `perform_rpc` + verificación WW del pre-roll.
6. E2E con botón-tap como wake sintético (aún sin WW): valida gate, pre-roll y protocolo completos antes del spike microWakeWord.

## Código
**firmware/main/csdk.zig** — (a) Bindings extern mínimos a añadir: publish_data + RPC entrante + data streams + PSRAM + colas, transcritos de los headers vendorizados 0.3.10

```zig
// --- Data packets (device→agent: wake/button/doa) — livekit.h:382-423 ---
pub const livekit_data_payload_t = extern struct { bytes: [*]u8, size: usize };
pub const livekit_data_publish_options_t = extern struct {
    payload: *livekit_data_payload_t,
    topic: [*:0]const u8, // char* en C; el SDK no lo muta
    lossy: bool,
    destination_identities: ?[*][*:0]const u8 = null,
    destination_identities_count: c_int = 0,
};
pub extern fn livekit_room_publish_data(handle: livekit_room_handle_t, options: *livekit_data_publish_options_t) c_int;

// --- RPC entrante (agent→device) — livekit_rpc.h:69-111, livekit.h:446 ---
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
    payload: ?[*:0]u8, // NULL o cstring válido (garantizado por el SDK)
    send_result: *const fn (*const livekit_rpc_result_t, ?*anyopaque) callconv(.c) bool,
    ctx: ?*anyopaque,
};
pub const livekit_rpc_handler_t = *const fn (*const livekit_rpc_invocation_t, ?*anyopaque) callconv(.c) void;
pub extern fn livekit_room_rpc_register(handle: livekit_room_handle_t, method: [*:0]const u8, handler: livekit_rpc_handler_t) c_int;

// --- Data stream saliente (pre-roll) — livekit_data_stream.h:79-90, livekit.h:545-568 ---
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

// --- Tipar callbacks hoy opacos en livekit_room_options_t (livekit.h:117-126,175-188) ---
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

// --- PSRAM + cola FreeRTOS (para invocation.zig) ---
pub const MALLOC_CAP_SPIRAM: u32 = 1 << 10;
pub extern fn heap_caps_malloc(size: usize, caps: u32) ?*anyopaque;
pub const QueueHandle_t = ?*anyopaque;
pub extern fn xQueueGenericCreate(len: u32, item_size: u32, qtype: u8) QueueHandle_t; // xQueueCreate
pub extern fn xQueueGenericSend(q: QueueHandle_t, item: *const anyopaque, ticks: u32, pos: c_int) c_int; // xQueueSend: pos=0
pub extern fn xQueueReceive(q: QueueHandle_t, item: *anyopaque, ticks: u32) c_int;
```

**firmware/main/invocation.zig** — (b) Esqueleto de la máquina de estados: gate atómico con razones, ring de pre-roll en PSRAM, apertura optimista y handler RPC no bloqueante

```zig
//! invocation.zig — estados ARMED/ATTENDING/ENGAGED/LINGER, gate y pre-roll.
const std = @import("std");
const c = @import("csdk.zig");

pub const State = enum(u8) { armed, attending, engaged, linger };
pub const Reason = struct { // bitmask del gate (0 == abierto)
    pub const button: u8 = 1 << 0; // mute físico: congela también ring y WW
    pub const state: u8 = 1 << 1; // cerrado por estado (ARMED)
    pub const half_duplex: u8 = 1 << 2; // agente hablando (hasta cerrar AEC #2)
};
const Event = union(enum) {
    rpc_state: struct { st: State, ttl_ms: u32 },
    wake: struct { score: f32, doa_deg: u16 },
    button_tap: void,
};

var gate = std.atomic.Value(u8).init(Reason.state); // arranca en ARMED
var state = std.atomic.Value(u8).init(@intFromEnum(State.armed));
var room: c.livekit_room_handle_t = null;
var evq: c.QueueHandle_t = null;
var wake_id: u32 = 0;

const RING = 2 * 16000; // 2 s @16 kHz mono i16 = 64 KB
var ring: [*]i16 = undefined;
var staging: [*]i16 = undefined; // snapshot para enviar sin que el vivo lo pise
var wr = std.atomic.Value(u32).init(0); // índice [0,RING); solo escribe AUD_SRC

pub fn init(r: c.livekit_room_handle_t) void {
    room = r;
    ring = @ptrCast(@alignCast(c.heap_caps_malloc(RING * 2, c.MALLOC_CAP_SPIRAM).?));
    staging = @ptrCast(@alignCast(c.heap_caps_malloc(RING * 2, c.MALLOC_CAP_SPIRAM).?));
    evq = c.xQueueGenericCreate(8, @sizeOf(Event), 0);
    _ = c.livekit_room_rpc_register(room, "sebastian.state", onRpcState);
    // ídem sebastian.led / sebastian.volume / sebastian.announce
    _ = c.xTaskCreatePinnedToCore(task, "invocation", 6144, null, 5, null, 1);
}

pub fn gateOpen() bool { return gate.load(.acquire) == 0; }
pub fn currentState() State { return @enumFromInt(state.load(.acquire)); }

/// Desde mic_src.readFrame (tarea AUD_SRC), ya decimado a 16 kHz.
pub fn feed16k(samples: []const i16) void {
    if (gate.load(.acquire) & Reason.button != 0) return; // hard-mute = privacidad
    var w = wr.load(.monotonic);
    for (samples) |s| { ring[w] = s; w = (w + 1) % RING; }
    wr.store(w, .release);
    // futuro: wakeword.feed(samples) — mismo hop de 160 muestras
}

/// WW local o botón-tap: apertura OPTIMISTA (sin RTT) + evento + pre-roll.
pub fn onWakeDetected(score: f32, doa_deg: u16) void {
    clearReason(Reason.state); // gate abierto YA: el pre-roll cubre [-2 s, aquí]
    state.store(@intFromEnum(State.attending), .release);
    const end = wr.load(.acquire);
    for (0..RING) |i| staging[i] = ring[(end + i) % RING]; // viejo→nuevo
    const ev = Event{ .wake = .{ .score = score, .doa_deg = doa_deg } };
    _ = c.xQueueGenericSend(evq, &ev, 0, 0); // task: publishWake() + sendPreroll()
}

fn onRpcState(inv: *const c.livekit_rpc_invocation_t, _: ?*anyopaque) callconv(.c) void {
    // Corre en la tarea esp_peer, invocación en stack: encolar y responder AHORA.
    if (parseStateJson(inv.payload)) |ev| _ = c.xQueueGenericSend(evq, &ev, 0, 0);
    var res = c.livekit_rpc_result_t{ .id = inv.id, .code = c.LIVEKIT_RPC_RESULT_OK, .payload = "{\"ok\":true}" };
    _ = inv.send_result(&res, inv.ctx);
}
// task(): xQueueReceive + aplicar transiciones/razones + watchdogs 8s/ttl +
// publishWake/sendPreroll (data stream "sebastian.preroll": header SBPR + staging)
```

**firmware/main/mic_src.zig** — (c) Hook del gate en readFrame: convertir siempre a scratch (nivel+ring+WW viven aunque el gate cierre), decimar ×3 y publicar voz o silencio

```zig
// Sustituye a muted_flag/setMuted: el gate vive en invocation.zig.
const invocation = @import("invocation.zig");

var scratch: [MAX_SAMPLES]i16 = undefined; // mono 48k tras SHIFT + softClip
var deci: [MAX_SAMPLES / 3 + 1]i16 = undefined; // 16 kHz para ring + WW

fn readFrame(_: *c.esp_capture_audio_src_iface_t, frame: *c.esp_capture_stream_frame_t) callconv(.c) c_int {
    if (!instance.started) return c.ESP_CAPTURE_ERR_NOT_SUPPORTED;
    const total = frameSampleCount(frame) orelse return c.ESP_CAPTURE_ERR_NOT_SUPPORTED;
    if (total == 0) return c.ESP_CAPTURE_ERR_OK;
    resyncRxOnce();
    const got = readI2s(total) orelse return c.ESP_CAPTURE_ERR_INTERNAL;

    // SIEMPRE convertir: mic_level, pre-roll y (futuro) WW corren con gate cerrado.
    writeCapturedSamples(&scratch, got, total); // igual que hoy pero a scratch

    // 48k→16k ×3. Placeholder: media de 3 (suficiente para pre-roll/STT);
    // cambiar por FIR anti-alias de esp-dsp cuando entre microWakeWord.
    const n16 = total / 3;
    var i: usize = 0;
    while (i < n16) : (i += 1) {
        const a = @as(i32, scratch[i * 3]) + scratch[i * 3 + 1] + scratch[i * 3 + 2];
        deci[i] = @intCast(@divTrunc(a, 3));
    }
    invocation.feed16k(deci[0..n16]); // ring PSRAM (frames de 10 ms → 160 muestras)

    const out: [*]i16 = @ptrCast(@alignCast(frame.data));
    if (invocation.gateOpen()) {
        @memcpy(out[0..total], scratch[0..total]);
    } else {
        fillSilence(out, total); // cerrado: silencio al room; pts sigue corriendo
    }
    updatePts(frame, total); // inocuo: gmf_audio_src recalcula pts por contador
    return c.ESP_CAPTURE_ERR_OK;
}
```

## Riesgos
- **C SDK en Developer Preview: publish_data sin buffering (falla si engine≠CONNECTED) y APIs sujetas a cambio; el pin actual es 0.3.10.** → Reintento con backoff en la tarea invocation para wake/button; congelar la versión y revisar changelog antes de cada bump (si un 0.3.x futuro añade rpc_invoke saliente, migrar wake a RPC con ack real).
- **Handler RPC bloqueante o lento tumba la tarea de esp_peer (audio y señalización comparten proceso).** → Patrón obligatorio parsear→encolar→send_result inmediato; payloads ≤1 KB; validar sender_identity contra la identidad del agente.
- **El burst de 64 KB del pre-roll comparte el mismo DTLS/SCTP que el audio Opus en subida: posible jitter puntual en el arranque del turno.** → Enviar el stream desde la tarea invocation justo tras abrir el gate (el vivo aún es silencio de arranque de frase); si se observa jitter, trocear los write() con pausas de 10 ms o bajar el pre-roll a 1,5 s (48 KB).
- **Falso positivo del WW abre el gate optimista durante ~200-500 ms hasta el veto del agente: fuga breve de audio a la nube.** → Umbral on-device razonable + veto rápido (re-verificación solo sobre el tramo de la WW), LED encendido siempre que el gate esté abierto (honestidad visible), y botón/config para modo 'apertura solo tras ack' si el usuario lo prefiere.
- **xvf_ui pisa el gate: hoy fuerza mic.setMuted(readMuted()) cada 80 ms.** → Refactor incluido en el diseño (paso 4): xvf_ui solo informa del botón vía invocation.setButtonMute y lee el estado para pintar.
- **Half-duplex mientras el hallazgo AEC #2 siga abierto: sin barge-in en P0 y el LINGER pierde solapes con la cola del TTS.** → Mantener razón half_duplex activable por config; al cerrar AEC #2 (P1), desactivarla y habilitar Adaptive Interruption Handling en el agente.
- **Silencio en ARMED no es gratis (DTX de Opus inaplicable a 48 kHz y no expuesto por el SDK): consumo de red y de minutos LiveKit Cloud 24/7.** → Aceptarlo en P0 (pocos kbps); plan real = fase IDLE con desconexión de sala + token server, o self-host de LiveKit en cortes (ya en ROADMAP P2).
- **std.json del fork Zig 0.16-xtensa podría tener huecos (writergate).** → Solo se usa parseFromSliceLeaky (parseo); si el fork falla, parser manual trivial para 4 mensajes de forma fija (~100 líneas).

## Preguntas abiertas
- ¿Formato del pre-roll: s16le crudo (64 KB, cero CPU) como propone el diseño, o comprimirlo (segundo encoder Opus vía esp_audio_codec, ~4x menos red pero +RAM/CPU)? Confirmar con medición WiFi real.
- ¿Re-verificación WW en el agente: openWakeWord sobre el PCM de 16 kHz, o STT del pre-roll + match textual de 'Sebastián'? (latencia del veto vs dependencias).
- ¿Identidad del agente fija por convención (agent_name en explicit dispatch, p.ej. 'sebastian-agent') o solo descubierta por on_participant_info kind==AGENT? El diseño usa ambas (convención + verificación).
- ¿Se adelanta la fase IDLE (desconexión de sala + reconexión al despertar) a P0 para cortar el coste de minutos de LiveKit Cloud, sabiendo que exige el token server?
- ¿El gate en LINGER debe bajar la sensibilidad (p.ej. exigir mic_level mínimo antes de considerar réplica) para no enviar 8 s de ruido de sala tras cada turno?
- Confirmar en hardware que el snapshot de 64 KB PSRAM→PSRAM (memcpy en la tarea invocation) no roba tiempo a AUD_SRC — si compite, hacer el copy por trozos de 8 KB.

## Fuentes
- https://docs.livekit.io/agents/multimodality/audio/
- https://github.com/livekit/agents/blob/main/livekit-agents/livekit/agents/voice/room_io/_pre_connect_audio.py
- https://github.com/livekit/agents/blob/main/livekit-agents/livekit/agents/voice/io.py
- https://docs.livekit.io/transport/data/rpc/
- https://docs.livekit.io/reference/python/livekit/rtc/room.html
- https://pypi.org/project/livekit-agents/
- https://github.com/livekit/agents/releases
- https://ziglang.org/download/0.15.1/release-notes.html
- https://github.com/ziglang/zig/issues/24468