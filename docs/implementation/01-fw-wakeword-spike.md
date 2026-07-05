> **Implementation Report Appendix** ([`IMPLEMENTATION.md`](../../IMPLEMENTATION.md)). Full text of the multi-agent exploration from 2026-07-02 (8 agents in parallel + cross-checking). Where this appendix contradicts the **Frozen Decisions** of the main report, the report prevails.

# fw-wakeword-spike — ESP32-S3 Firmware — feasibility of microWakeWord coexisting with LiveKit/WebRTC (P0 spike of the invocation model)

**Verdict:** feasible with risks — The memory fits with almost guaranteed clearance: mWW is ~100% allocatable in PSRAM (arena 26 KB, ring, stack) and there is precedent on this exact hardware (homeassistant/respeaker.yaml runs 5 models + VAD under ESPHome with the mic at 48 kHz). The only real unknown is the p95 CPU of the inference coexisting with WiFi/DTLS/Opus bursts — without a public example, but bounded and measurable in a 2-day spike.

**Effort:** M — spike: 2 person-days (day 1: component+shim+dry measurement; day 2: coexistence+stress+decision). If GO, production integration (gate/hard_mute, state machine, trained es-ES model): +3–4 days.

## Findings
- Direct precedent in the repo itself and same hardware: homeassistant/respeaker.yaml runs ESPHome micro_wake_word with 5 models + VAD over the XVF3800 with the I2S mic at 48 kHz/32-bit stereo (same board, same GPIO43/44 pins as the Zig firmware)  
  _homeassistant/respeaker.yaml:1432 and 1231–1241_
- ESPHome DOES NOT decimate: it passes the real mic sample rate (48 kHz) to FrontendPopulateState(); the frontend (30 ms window / 10 ms step / 40 channels / 125–7500 Hz, PCAN + log) is frequency-normalized → feeding the detector at 48 kHz direct is valid, it is tested on this hardware and eliminates the risk of mismatch with the training  
  _https://raw.githubusercontent.com/esphome/esphome/dev/esphome/components/micro_wake_word/micro_wake_word.cpp (line 314: FrontendPopulateState(..., get_sample_rate()))_
- The features frontend is pure C with kissfft int16 (esphome/esp-micro-speech-features 1.2.3; C extern API: FrontendPopulateState/FrontendProcessSamples/FrontendReset) → bindable directly from csdk.zig without shim; only the TFLM inference is C++ and needs an extern "C" shim  
  _https://github.com/esphome-libs/esp-micro-speech-features (include/frontend.h, src/kiss_fftr.c)_
- Exact numbers for the mWW stack (ESPHome dev, jul-2026): pinned esp-tflite-micro 1.3.3~1 + esp-nn 1.1.2 + esp-micro-speech-features 1.2.3; tensor arena 26 080 B + var-arena 1 024 B allocated in PSRAM (external RAMAllocator with fallback, probing 1x/1.5x/2x); okay_nabu.tflite 60 KB model + vad.tflite 34 KB; inference task 3 072 B prio 3; inference <10 ms per hop on S3; up to 4 concurrent models on an S3  
  _https://raw.githubusercontent.com/esphome/esphome/dev/esphome/components/micro_wake_word/__init__.py + models/v2/okay_nabu.json + streaming_model.cpp + https://www.kevinahrendt.com/micro-wake-word_
- The LiveKit stack already lives mostly in PSRAM: the stacks for the media tasks are created with xTaskCreatePinnedToCoreWithCaps(MALLOC_CAP_SPIRAM) — aenc_0 40 KB + lk_peer_pub/sub 25 KB×2 on core 1; Adec 40 KB + buffer_in 6 KB on core 0; AUD_SRC 40 KB without affinity — and mbedTLS uses EXTERNAL_MEM_ALLOC, WiFi/LWIP prefer-SPIRAM → core 0 has a natural gap for the low priority WW task  
  _firmware/managed_components/tempotian__media_lib_sal/port/media_lib_os_freertos.c + livekit__livekit/core/system.c:33-67 + firmware/sdkconfig_
- The expensive cost of audio is already paid: Opus encode 48 kHz with complexity 0 by default (Espressif official benchmark on S3: 24.9 % CPU / 29.4 KB heap for 48 k stereo; our publish is mono) runs today on core 1 with the full-duplex conversation validated in hardware  
  _firmware/managed_components/espressif__esp_audio_codec/include/encoder/impl/esp_opus_enc.h:103 + https://components.espressif.com/components/espressif/esp_audio_codec/versions/2.4.1/readme_
- Current static budget: 3.20 MB binary in 4 MiB factory partition (~1.0 MB free) and 3.94 MB of unassigned flash (table ends at 0x410000 of 8 MB); IRAM text 104 KB; DRAM static 54 KB; DRAM heap ~180 KB at boot according to map. mWW adds ~0.4–0.55 MB of flash and only 10–25 KB of internal heap (FrontendState; the rest goes to PSRAM)  
  _firmware/build/sebastian.map (iram0/dram0 segments) + firmware/partitions.csv_
- sdkconfig already has CONFIG_SPIRAM_FETCH_INSTRUCTIONS and SPIRAM_ALLOW_STACK_EXTERNAL_MEMORY but lacks CONFIG_SPIRAM_RODATA — respeaker.yaml explicitly enables both "considerably speeds up mWW"; SPIRAM_MALLOC_ALWAYSINTERNAL=16384 will make the frontend's small allocs fall into internal RAM  
  _firmware/sdkconfig + homeassistant/respeaker.yaml:81-85_
- The hook point exists and does not break pacing: readFrame already converts to i16 mono 48 k consumer-paced; the WW tap must go BEFORE the mute silence overwrite (mic_src.zig), which requires separating the current muted_flag into gate (ARMED: publishes silence, WW keeps listening) vs hard_mute (button: also cuts the tap)  
  _firmware/main/mic_src.zig:190-195_
- esp-dsp and esp-sr 2.1.5 are already vendored as transitive dependencies of esp_capture (no cost since they are not used, the linker discards them) → dsps_fird_s16 available for the ÷3 decimator without adding dependencies; esp-nn is NOT yet in the binary  
  _firmware/managed_components/espressif__esp-dsp + espressif__esp_capture/idf_component.yml + map grep (0 refs esp-nn)_

## Design

# Spike P0 — microWakeWord + LiveKit coexisting on the XIAO ESP32-S3

## Finding that simplifies everything
ESPHome **does not decimate**: it passes the real mic sample rate to `FrontendPopulateState()` and the frontend
(30 ms window / 10 ms step / 40 channels / 125–7500 Hz) is frequency-normalized. The
`homeassistant/respeaker.yaml` config in this repo runs 5 models + VAD like this **on this exact hardware**.
- **Option A (spike):** feed the tap at 48 kHz as-is (FFT 2048 int16, ~5-8 % of a core). Zero risk of mismatch with training; identical to the tested config.
- **Option B (future optimization):** ÷3 decimator → FFT 512 (~1.5 %). See below.

## Spike Architecture
```
I2S RX 48k/32b stereo ──readFrame (AUD_SRC, prio 15)──► i16 mono 48k ──► published frame (Opus)
                                   │ tap: memcpy + xStreamBufferSend(timeout 0, drop if full)
                                   ▼
                 StreamBuffer 8 KB (PSRAM) ──► ww task (core 0, prio 5, stack 8 KB)
                                   │  FrontendProcessSamples (C, kissfft)  → 40 feats/10 ms
                                   │  → int8 (x*256/666−128) → TFLM Invoke (esp-nn) → window 5, cutoff 0.97
                                   ▼
                        detected → state machine (ARMED→ATTENDING)
```
- Tap **before** the silence overwrite in `readFrame` (mic_src.zig:190). Separate `muted_flag` into two: `gate` (state machine: publishes silence, WW keeps listening) and `hard_mute` (physical button: also cuts the tap → real privacy).
- The consumer-paced pacing does not change: the tap is O(n) and never blocks; the inference lives in its own task.
- Spike with `okay_nabu.tflite` + `vad.tflite` (English, tested) — separates the integration risk from the es-ES trainer risk. The "Sebastián" model comes in later with the same V2 manifest.

## Build Integration (snippet 3)
1. New IDF component `firmware/components/wakeword/` (C++17) with exact pins from ESPHome dev: `esp-tflite-micro 1.3.3~1`, `esp-nn 1.1.2` (pin: breakage precedent from silent bump, PR esphome#15628), `esp-micro-speech-features ^1.2.3`. Embedded models with EMBED_FILES.
2. `main/CMakeLists.txt`: add `wakeword` to REQUIRES — `cmake/zig.cmake` already propagates the graph includes to the Zig build automatically; `csdk.zig` adds the `extern fn` manually (project style). libstdc++ is already linked today (map) — the shim does not add a new runtime.
3. `partitions.csv`: factory 4M→6M (there is 3.94 MB unassigned; without OTA it doesn't break anything).
4. `sdkconfig.defaults`: `+CONFIG_SPIRAM_RODATA=y` (respeaker.yaml:84, speeds up mWW); only during the spike `+CONFIG_FREERTOS_GENERATE_RUN_TIME_STATS=y`.

## C↔Zig Shim (snippets 1 and 2)
The frontend is pure C (directly bindable), only TFLM requires C++. The `extern "C"` shim exposes `mww_init / mww_feed / mww_reset` and traces `streaming_model.cpp`: same 20 ops in `MicroMutableOpResolver`, `MicroResourceVariables(20)` with 1 KB var-arena, 26 080 B arena from the manifest (1.5x/2x probing if it fails), int8 input, sliding window of 5 with cutoff.

## Estimated Budget (the spike turns it into measured)
| Resource | Today | mWW adds | Note |
|---|---|---|---|
| App Flash | 3.20 / 4.19 MB | +0.40–0.55 MB | TFLM+esp-nn ~300–450 K, frontend ~20 K, models 95 K → expand partition to 6M |
| Static DRAM | 54 KB (.data+.bss) | ~0 | all dynamic |
| Internal Heap | ~180 KB boot; with room 60–120 KB free (MEASURE) | +10–25 KB | FrontendState (FFT@48k) falls to internal via SPIRAM_MALLOC_ALWAYSINTERNAL=16384 |
| 8 MB PSRAM | ≪1 MB (LK stacks ~180 K WithCaps, mbedTLS, fifos, ws 20 K) | +43 KB (arena 27 + ring 8 + stack 8) | future pre-roll 1.5 s@48k = 144 KB; plenty of room |
| CPU core 0 | WiFi/lwIP + Adec (~6 %) + render | frontend ~5-8 % + inference+VAD **20–60 % (THE unknown)** | criterion: p95 < 10 ms/hop |
| CPU core 1 | Opus enc cx0 (bench 24.9 % stereo; mono less) + SRTP/peers | 0 | no changes |

## Option B — 48→16 kHz decimator (if the 48 k frontend is bothersome, and for future 16 k uses)
Polyphase FIR ÷3: 144 taps (48/phase), Q15, passband 0–7.3 kHz (covers the 7 500 Hz of the filterbank), stopband ≥8.2 kHz @ −60 dB. Cost: 16 000 out/s × 144 MAC ≈ 2.3 MMAC/s → **1–2 % of a core** with `dsps_fird_s16` from esp-dsp (already vendored, aes3 optimized); latency ≈1.5 ms. Execute in the ww task (not in readFrame). A CIC+compensator would be even cheaper but the complexity is not worth it at this cost.

## Spike Plan (2 days)
**Day 1 — integration and dry measurement**
1. Component + deps + clean build; note delta of `idf.py size-components`.
2. Shim + bindings + ww task; boot **without** `joinRoom` (comment out in app.zig): validate "okay nabu" detections at 1/3/5 m; instrument each hop with `esp_timer_get_time()` → p50/p95 of frontend+Invoke; compare arena in PSRAM vs `MALLOC_CAP_INTERNAL`.
3. Metrics baseline: `heap_caps_get_free_size` + `heap_caps_get_minimum_free_size` (INTERNAL/SPIRAM/DMA), `uxTaskGetStackHighWaterMark` of all tasks.
**Day 2 — coexistence**
4. Full stack: room + agent conversing full-duplex + active WW. Telemetry task (prio 1, every 5 s): heaps, watermarks, StreamBuffer drops, `vTaskGetRunTimeStats` → idle% per core with/without WW.
5. Stress ≥10 min: agent talking (worst case: Adec+render active), periodic detections, forced reconnection (cut AP for 10 s → DTLS spike), mute button.
6. Record numbers and decide. If GO: next PR = gate/hard_mute flags + train "Sebastián".

## GO (all) / NO-GO (any) Criteria
- GO: p95(frontend+Invoke) < 10 ms per 10 ms hop; stable free internal heap > 40 KB (minimum > 25 KB during reconnection); 0 new audio artifacts (helicopter/underrun/warble) in 10 min of conversation; ≥8/10 detections at 3 m with background noise; 0 watchdogs and stack watermarks > 512 B.
- NO-GO: p95 > 10 ms with internal arena AND in PSRAM; internal heap < 20 KB or with a downward trend; artifacts that appear only with WW active.

## Escalated Plan B (without re-architecture)
1st `feature_step_size` 20 ms (half the Invokes, V1-style models); 2nd ww affinity to core 1 (encoder gaps) or without affinity; 3rd **PTT**: long-press of the button as manual gate (already in ROADMAP) and temporary wake word on the agent (openWakeWord on the published track — cost: privacy/network) until resolved.

## Code
**firmware/components/wakeword/mww_shim.h + mww_shim.cpp (new)** — Minimal C↔C++ Shim (firmware/components/wakeword/ component): extern C API for Zig + streaming inference essence traced from ESPHome streaming_model.cpp

```cpp
// mww_shim.h — API C mínima para Zig
#pragma once
#include <stdint.h>
#include <stddef.h>
#include <stdbool.h>
#ifdef __cplusplus
extern "C" {
#endif
typedef struct { bool detected; uint8_t prob; } mww_result_t;
int  mww_init(const uint8_t *model, size_t arena_size, int sample_rate,
              float cutoff, int sliding_window);      // manifest V2: 26080, 0.97, 5
mww_result_t mww_feed(const int16_t *pcm, size_t n);  // PCM mono i16, consume todo
void mww_reset(void);
#ifdef __cplusplus
}
#endif

// mww_shim.cpp — esencia (calca streaming_model.cpp de ESPHome)
#include "frontend_util.h"  // esp-micro-speech-features: C puro (kissfft int16)
#include "tensorflow/lite/micro/micro_interpreter.h"
#include "tensorflow/lite/micro/micro_mutable_op_resolver.h"

static tflite::MicroMutableOpResolver<20> ops; // las 20 de ESPHome: CallOnce, VarHandle,
// ReadVariable, AssignVariable, Reshape, StridedSlice, Concatenation, Conv2D,
// DepthwiseConv2D, Mul, Add, Mean, FullyConnected, Logistic, Quantize,
// AveragePool2D, MaxPool2D, Pad, Pack, SplitV

int mww_init(const uint8_t *model, size_t arena_size, int rate, float cutoff, int win) {
    arena     = (uint8_t *)heap_caps_malloc(arena_size, MALLOC_CAP_SPIRAM | MALLOC_CAP_8BIT);
    var_arena = (uint8_t *)heap_caps_malloc(1024, MALLOC_CAP_SPIRAM | MALLOC_CAP_8BIT);
    ma  = tflite::MicroAllocator::Create(var_arena, 1024);
    mrv = tflite::MicroResourceVariables::Create(ma, 20);
    interp = new tflite::MicroInterpreter(tflite::GetModel(model), ops, arena, arena_size, mrv);
    if (interp->AllocateTensors() != kTfLiteOk) return -1;   // si falla: reintentar 1.5x/2x
    FrontendFillConfigWithDefaults(&fcfg);                   // = preprocessor_settings.h:
    fcfg.window.size_ms = 30; fcfg.window.step_size_ms = 10; // 40 canales, 125–7500 Hz,
    fcfg.filterbank.num_channels = 40; /* …noise_reduction, PCAN 0.95/80, log_scale… */
    return FrontendPopulateState(&fcfg, &fstate, rate) ? 0 : -2; // rate = 48000 (opción A)
}

mww_result_t mww_feed(const int16_t *pcm, size_t n) {
    while (n > 0) {
        size_t read = 0;
        struct FrontendOutput o = FrontendProcessSamples(&fstate, pcm, n, &read);
        pcm += read; n -= read;
        if (o.size != 40) continue;                    // aún no hay hop de 10 ms completo
        int8_t f[40];
        for (int i = 0; i < 40; i++) {                 // escala exacta de ESPHome (div 666)
            int32_t v = ((int32_t)o.values[i] * 256) / 666 - 128;
            f[i] = (int8_t)std::clamp(v, (int32_t)-128, (int32_t)127);
        }
        memcpy(interp->input(0)->data.int8, f, 40);    // strided según feature_step_size
        int64_t t0 = esp_timer_get_time();
        interp->Invoke();                              // kernels esp-nn
        stats_note(esp_timer_get_time() - t0);         // p50/p95 → criterio go/no-go
        uint8_t p = interp->output(0)->data.uint8[0];
        last = slide_and_threshold(p);                 // ventana 5, cutoff*255, como ESPHome
    }
    return last;
}
```

**firmware/main/csdk.zig + mic_src.zig + ww.zig (new)** — Zig side: extern bindings by hand (csdk.zig style), tap in readFrame BEFORE the mute overwrite (separating gate vs hard_mute) and inference task on core 0

```zig
// csdk.zig — añadir bindings (estilo del proyecto: extern a mano, sin @cImport)
pub const mww_result_t = extern struct { detected: bool, prob: u8 };
pub extern fn mww_init(model: [*]const u8, arena: usize, rate: c_int, cutoff: f32, win: c_int) c_int;
pub extern fn mww_feed(pcm: [*]const i16, n: usize) mww_result_t;
pub extern fn mww_reset() void;
pub extern const _binary_okay_nabu_tflite_start: u8; // EMBED_FILES del componente

// mic_src.zig — tap en readFrame, ANTES del overwrite de silencio (hoy línea ~190)
fn readFrame(_: *c.esp_capture_audio_src_iface_t, frame: *c.esp_capture_stream_frame_t) callconv(.c) c_int {
    // …frameSampleCount + resyncRxOnce + readI2s igual que hoy…
    writeCapturedSamples(out, got, total);           // convertir SIEMPRE (i32→i16 + softClip)
    if (!hard_muted) _ = c.xStreamBufferSend(ww_sb, out, got * 2, 0); // tap 48 k; drop si lleno
    if (gated or hard_muted) {                       // gate (ARMED) vs botón (privacidad total)
        fillSilence(out, total);                     // a la sala va silencio; el WW sigue oyendo
        mic_level = 0;
    }
    updatePts(frame, total);
    return c.ESP_CAPTURE_ERR_OK;
}

// ww.zig — tarea de inferencia: core 0, prio 5 (< AUD_SRC 15 y ARender 20)
var ww_sb: c.StreamBufferHandle_t = null;
fn wwTask(_: ?*anyopaque) callconv(.c) void {
    var buf: [480]i16 = undefined;                   // 10 ms @ 48 kHz por iteración
    while (true) {
        const bytes = c.xStreamBufferReceive(ww_sb, &buf, buf.len * 2, c.portMAX_DELAY);
        const r = c.mww_feed(&buf, bytes / 2);
        if (r.detected) onWake(r.prob);              // ARMED→ATTENDING: abrir gate + pre-roll
    }
}
pub fn start() !void {
    ww_sb = c.xStreamBufferCreateWithCaps(8192, 960, c.MALLOC_CAP_SPIRAM); // ~85 ms margen
    if (c.mww_init(&_binary_okay_nabu_tflite_start, 26080, 48000, 0.97, 5) != 0)
        return error.WakeWordInitFailed;
    _ = c.xTaskCreatePinnedToCore(wwTask, "ww", 8192, null, 5, null, 0);
}
```

**firmware/components/wakeword/{idf_component.yml,CMakeLists.txt} + main/CMakeLists.txt + partitions.csv + sdkconfig.defaults** — Build integration: new component with exact pins, REQUIRES in main (zig.cmake inherits includes only), expanded partition and sdkconfig like respeaker.yaml

```text
# firmware/components/wakeword/idf_component.yml — pins exactos (= ESPHome dev;
# precedente de rotura por bump silencioso de esp-nn → PR esphome#15628)
dependencies:
  espressif/esp-tflite-micro: "1.3.3~1"
  espressif/esp-nn: "1.1.2"
  esphome/esp-micro-speech-features: "^1.2.3"

# firmware/components/wakeword/CMakeLists.txt
idf_component_register(
  SRCS "mww_shim.cpp"
  INCLUDE_DIRS "include"
  REQUIRES esp-tflite-micro esp-micro-speech-features
  EMBED_FILES "models/okay_nabu.tflite" "models/vad.tflite")

# firmware/main/CMakeLists.txt — añadir a REQUIRES (cmake/zig.cmake propaga los
# include dirs del grafo al build de app.zig automáticamente, sin tocar build.zig):
  REQUIRES … esp_netif esp_wifi wakeword

# firmware/partitions.csv — hoy factory=4M y 3.94 MB de flash sin asignar (8 MB):
- factory,  app,  factory, 0x10000, 4M,
+ factory,  app,  factory, 0x10000, 6M,

# firmware/sdkconfig.defaults — receta de homeassistant/respeaker.yaml:81-85:
+ CONFIG_SPIRAM_RODATA=y            # con FETCH_INSTRUCTIONS ya activo: acelera mWW
# Solo durante el spike (medición idle% por core):
+ CONFIG_FREERTOS_GENERATE_RUN_TIME_STATS=y
```

## Risks
- **CPU spike collision: Invoke (2–10 ms) coinciding with WiFi/lwIP/DTLS bursts on core 0 → jitter in the audio pipeline or delayed detections** → ww task at prio 5 (below AUD_SRC 15 and ARender 20); measure idle% per core with RUN_TIME_STATS; valves: step 20 ms (half the Invokes), affinity to core 1 or no affinity
- **PSRAM cache contention: TFLM arena + LK stacks + mbedTLS heap all behind a 64 KB octal dcache → slower inference than published** → Enable CONFIG_SPIRAM_RODATA (respeaker.yaml recipe); the spike compares PSRAM arena vs MALLOC_CAP_INTERNAL (26 KB fit in internal if needed)
- **Version drift of esp-tflite-micro/esp-nn breaks arena size or inference (already happened upstream: ESPHome had to pin after a silent bump)** → Exact pins in idf_component.yml of the component (1.3.3~1 / 1.1.2, those of ESPHome dev) + 1.5x/2x arena probing as streaming_model.cpp does
- **The tap breaks consumer-paced pacing if the StreamBuffer fills up or the send blocks** → xStreamBufferSend with timeout 0 and drop (never block readFrame); drop counter in spike telemetry; 8 KB buffer ≈ 85 ms of margin
- **The es-ES "Sebastián" model from the local trainer does not reach usable quality (false positives/negatives) even if integration works** → Separate risks: spike with okay_nabu (tested); for production, two-stage verification already planned in ROADMAP (loose on-device threshold + re-verification with pre-roll on the agent) + VAD
- **Privacy semantics: reusing muted_flag as a gate would leave the WW listening with the physical button pressed** → Separate gate flags (ARMED: silence to the room, tap active) vs hard_mute (button: also cuts the tap and turns off LED) in mic_src before integrating the state machine

## Open Questions
- Real p95 of FrontendProcessSamples+Invoke with arena in PSRAM under active WebRTC traffic? There is no public number for this combination — it is exactly what the spike measures (the 20–60 % core estimation comes from <10 ms/hop published and the precedent of 4 concurrent models on S3).
- How much free internal heap is left today in steady state with the room connected and the agent talking? (estimation by map: 60–120 KB; measure with heap_caps_get_free_size before touching anything).
- Current pin of esp-nn in ESPHome dev at the time of implementation? (__init__.py says 1.1.2 today; PR #15628 mentions 1.2.1 — verify and copy the exact pin).
- Which core does AUD_SRC (readFrame) end up on? system.c does not set its affinity — confirm with RUN_TIME_STATS to decide the affinity of the ww task.
- Is it worth forcing FrontendState to PSRAM (alloc hook) if the 10–25 KB internal get tight, or is it enough to lower SPIRAM_MALLOC_ALWAYSINTERNAL?
- Is the 1.5 s pre-roll ring (144 KB PSRAM) filled from the same WW tap or from the already decimated ww task? (decision of the gate+pre-roll phase, not the spike).

## Sources
- https://github.com/kahrendt/microWakeWord
- https://esphome.io/components/micro_wake_word/
- https://raw.githubusercontent.com/esphome/esphome/dev/esphome/components/micro_wake_word/micro_wake_word.cpp
- https://raw.githubusercontent.com/esphome/esphome/dev/esphome/components/micro_wake_word/streaming_model.cpp
- https://raw.githubusercontent.com/esphome/esphome/dev/esphome/components/micro_wake_word/streaming_model.h
- https://raw.githubusercontent.com/esphome/esphome/dev/esphome/components/micro_wake_word/preprocessor_settings.h
- https://raw.githubusercontent.com/esphome/esphome/dev/esphome/components/micro_wake_word/__init__.py
- https://raw.githubusercontent.com/esphome/esphome/dev/esphome/components/microphone/microphone_source.cpp
- https://raw.githubusercontent.com/esphome/esphome/dev/esphome/core/helpers.h
- https://github.com/esphome-libs/esp-micro-speech-features
- https://components.espressif.com/components/esphome/esp-micro-speech-features
- https://components.espressif.com/components/espressif/esp-tflite-micro
- https://components.espressif.com/components/espressif/esp_audio_codec/versions/2.4.1/readme
- https://github.com/esphome/micro-wake-word-models
- https://raw.githubusercontent.com/esphome/micro-wake-word-models/main/models/v2/okay_nabu.json
- https://github.com/esphome/esphome/pull/15628
- https://www.home-assistant.io/blog/2024/02/21/voice-chapter-6/
- https://www.kevinahrendt.com/micro-wake-word
- https://github.com/livekit/client-sdk-esp32
- https://livekit.com/blog/livekit-sdk-for-esp32-bringing-voice-ai-to-embedded-devices