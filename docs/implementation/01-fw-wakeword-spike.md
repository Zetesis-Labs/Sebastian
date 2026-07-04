> **Anexo del informe de implementación** ([`IMPLEMENTATION.md`](../../IMPLEMENTATION.md)). Texto íntegro de la exploración multi-agente del 2026-07-02 (8 agentes en paralelo + contraste cruzado). Donde este anexo contradiga las **Decisiones congeladas** del informe principal, prevalece el informe.

# fw-wakeword-spike — Firmware ESP32-S3 — viabilidad de microWakeWord conviviendo con LiveKit/WebRTC (spike P0 del modelo de invocación)

**Veredicto:** viable con riesgos — La memoria cabe con holgura casi garantizada: mWW es ~100% alojable en PSRAM (arena 26 KB, ring, stack) y existe precedente en este mismo hardware (homeassistant/respeaker.yaml corre 5 modelos + VAD bajo ESPHome con el mic a 48 kHz). El único desconocido real es el p95 de CPU de la inferencia conviviendo con ráfagas WiFi/DTLS/Opus — sin ejemplo público, pero acotado y medible en un spike de 2 días.

**Esfuerzo:** M — spike: 2 días-persona (día 1: componente+shim+medida en seco; día 2: convivencia+estrés+decisión). Si GO, integración productiva (gate/hard_mute, máquina de estados, modelo es-ES entrenado): +3–4 días.

## Hallazgos
- Precedente directo en el propio repo y mismo hardware: homeassistant/respeaker.yaml corre ESPHome micro_wake_word con 5 modelos + VAD sobre el XVF3800 con el mic I2S a 48 kHz/32-bit estéreo (misma placa, mismos pines GPIO43/44 que el firmware Zig)  
  _homeassistant/respeaker.yaml:1432 y 1231–1241_
- ESPHome NO decima: pasa el sample rate real del mic (48 kHz) a FrontendPopulateState(); el frontend (ventana 30 ms / paso 10 ms / 40 canales / 125–7500 Hz, PCAN + log) es frecuencia-normalizado → alimentar el detector a 48 kHz directo es válido, está probado en este hardware y elimina el riesgo de mismatch con el training  
  _https://raw.githubusercontent.com/esphome/esphome/dev/esphome/components/micro_wake_word/micro_wake_word.cpp (línea 314: FrontendPopulateState(..., get_sample_rate()))_
- El frontend de features es C puro con kissfft int16 (esphome/esp-micro-speech-features 1.2.3; API extern C: FrontendPopulateState/FrontendProcessSamples/FrontendReset) → bindeable directo desde csdk.zig sin shim; solo la inferencia TFLM es C++ y necesita shim extern "C"  
  _https://github.com/esphome-libs/esp-micro-speech-features (include/frontend.h, src/kiss_fftr.c)_
- Números exactos del stack mWW (ESPHome dev, jul-2026): esp-tflite-micro 1.3.3~1 + esp-nn 1.1.2 pineados + esp-micro-speech-features 1.2.3; tensor arena 26 080 B + var-arena 1 024 B alojadas en PSRAM (RAMAllocator externo con fallback, probing 1x/1.5x/2x); modelo okay_nabu.tflite 60 KB + vad.tflite 34 KB; tarea de inferencia 3 072 B prio 3; inferencia <10 ms por hop en S3; hasta 4 modelos concurrentes en un S3  
  _https://raw.githubusercontent.com/esphome/esphome/dev/esphome/components/micro_wake_word/__init__.py + models/v2/okay_nabu.json + streaming_model.cpp + https://www.kevinahrendt.com/micro-wake-word_
- El stack LiveKit ya vive mayormente en PSRAM: los stacks de las tareas media se crean con xTaskCreatePinnedToCoreWithCaps(MALLOC_CAP_SPIRAM) — aenc_0 40 KB + lk_peer_pub/sub 25 KB×2 en core 1; Adec 40 KB + buffer_in 6 KB en core 0; AUD_SRC 40 KB sin afinidad — y mbedTLS usa EXTERNAL_MEM_ALLOC, WiFi/LWIP prefer-SPIRAM → core 0 tiene hueco natural para la tarea WW a prioridad baja  
  _firmware/managed_components/tempotian__media_lib_sal/port/media_lib_os_freertos.c + livekit__livekit/core/system.c:33-67 + firmware/sdkconfig_
- El coste caro de audio ya está pagado: Opus encode 48 kHz con complexity 0 por defecto (benchmark oficial Espressif en S3: 24.9 % CPU / 29.4 KB heap para 48 k estéreo; nuestro publish es mono) corre hoy en core 1 con la conversación full-duplex validada en hardware  
  _firmware/managed_components/espressif__esp_audio_codec/include/encoder/impl/esp_opus_enc.h:103 + https://components.espressif.com/components/espressif/esp_audio_codec/versions/2.4.1/readme_
- Presupuesto estático actual: binario 3.20 MB en partición factory de 4 MiB (~1.0 MB libre) y 3.94 MB de flash sin asignar (tabla acaba en 0x410000 de 8 MB); IRAM text 104 KB; DRAM estática 54 KB; heap DRAM ~180 KB al boot según mapa. mWW añade ~0.4–0.55 MB de flash y solo 10–25 KB de heap interna (FrontendState; el resto va a PSRAM)  
  _firmware/build/sebastian.map (segmentos iram0/dram0) + firmware/partitions.csv_
- sdkconfig ya tiene CONFIG_SPIRAM_FETCH_INSTRUCTIONS y SPIRAM_ALLOW_STACK_EXTERNAL_MEMORY pero falta CONFIG_SPIRAM_RODATA — respeaker.yaml activa ambas explícitamente "considerably speeds up mWW"; SPIRAM_MALLOC_ALWAYSINTERNAL=16384 hará que los allocs pequeños del frontend caigan en RAM interna  
  _firmware/sdkconfig + homeassistant/respeaker.yaml:81-85_
- El punto de enganche existe y no rompe el pacing: readFrame ya convierte a i16 mono 48 k consumer-paced; el tap WW debe ir ANTES del overwrite de silencio del mute (mic_src.zig), lo que exige separar el flag actual muted_flag en gate (ARMED: publica silencio, WW sigue oyendo) vs hard_mute (botón: corta también el tap)  
  _firmware/main/mic_src.zig:190-195_
- esp-dsp y esp-sr 2.1.5 ya están vendorizados como deps transitivas de esp_capture (sin coste al no usarse, el linker los descarta) → dsps_fird_s16 disponible para el decimador ÷3 sin añadir dependencias; esp-nn NO está aún en el binario  
  _firmware/managed_components/espressif__esp-dsp + espressif__esp_capture/idf_component.yml + grep del mapa (0 refs esp-nn)_

## Diseño

# Spike P0 — microWakeWord + LiveKit conviviendo en el XIAO ESP32-S3

## Hallazgo que simplifica todo
ESPHome **no decima**: pasa el sample rate real del mic a `FrontendPopulateState()` y el frontend
(30 ms ventana / 10 ms paso / 40 canales / 125–7500 Hz) es frecuencia-normalizado. La config
`homeassistant/respeaker.yaml` de este repo corre así 5 modelos + VAD **en este mismo hardware**.
- **Opción A (spike):** alimentar el tap a 48 kHz tal cual (FFT 2048 int16, ~5-8 % de un core). Cero riesgo de mismatch con training; idéntico a la config probada.
- **Opción B (optimización futura):** decimador ÷3 → FFT 512 (~1.5 %). Ver abajo.

## Arquitectura del spike
```
I2S RX 48k/32b estéreo ──readFrame (AUD_SRC, prio 15)──► i16 mono 48k ──► frame publicado (Opus)
                                   │ tap: memcpy + xStreamBufferSend(timeout 0, drop si lleno)
                                   ▼
                 StreamBuffer 8 KB (PSRAM) ──► tarea ww (core 0, prio 5, stack 8 KB)
                                   │  FrontendProcessSamples (C, kissfft)  → 40 feats/10 ms
                                   │  → int8 (x*256/666−128) → TFLM Invoke (esp-nn) → ventana 5, cutoff 0.97
                                   ▼
                        detected → máquina de estados (ARMED→ATTENDING)
```
- Tap **antes** del overwrite de silencio en `readFrame` (mic_src.zig:190). Separar `muted_flag` en dos: `gate` (máquina de estados: publica silencio, WW sigue oyendo) y `hard_mute` (botón físico: corta también el tap → privacidad real).
- El pacing consumer-paced no cambia: el tap es O(n) y nunca bloquea; la inferencia vive en su tarea.
- Spike con `okay_nabu.tflite` + `vad.tflite` (inglés, probados) — separa el riesgo integración del riesgo trainer es-ES. El modelo "Sebastián" entra después con el mismo manifest V2.

## Integración build (snippet 3)
1. Componente IDF nuevo `firmware/components/wakeword/` (C++17) con pins exactos de ESPHome dev: `esp-tflite-micro 1.3.3~1`, `esp-nn 1.1.2` (pin: precedente de rotura por bump silencioso, PR esphome#15628), `esp-micro-speech-features ^1.2.3`. Modelos embebidos con EMBED_FILES.
2. `main/CMakeLists.txt`: añadir `wakeword` a REQUIRES — `cmake/zig.cmake` ya propaga los includes del grafo al build Zig automáticamente; `csdk.zig` añade los `extern fn` a mano (estilo del proyecto). libstdc++ ya está enlazada hoy (mapa) — el shim no añade runtime nuevo.
3. `partitions.csv`: factory 4M→6M (hay 3.94 MB sin asignar; sin OTA no rompe nada).
4. `sdkconfig.defaults`: `+CONFIG_SPIRAM_RODATA=y` (respeaker.yaml:84, acelera mWW); solo durante el spike `+CONFIG_FREERTOS_GENERATE_RUN_TIME_STATS=y`.

## Shim C↔Zig (snippets 1 y 2)
El frontend es C puro (bindeable directo), solo TFLM exige C++. El shim `extern "C"` expone `mww_init / mww_feed / mww_reset` y calca `streaming_model.cpp`: mismas 20 ops en `MicroMutableOpResolver`, `MicroResourceVariables(20)` con var-arena 1 KB, arena 26 080 B del manifest (probing 1.5x/2x si falla), entrada int8, ventana deslizante de 5 con cutoff.

## Presupuesto estimado (el spike lo convierte en medido)
| Recurso | Hoy | mWW añade | Nota |
|---|---|---|---|
| Flash app | 3.20 / 4.19 MB | +0.40–0.55 MB | TFLM+esp-nn ~300–450 K, frontend ~20 K, modelos 95 K → ampliar partición a 6M |
| DRAM estática | 54 KB (.data+.bss) | ~0 | todo dinámico |
| Heap interna | ~180 KB boot; con sala 60–120 KB libres (MEDIR) | +10–25 KB | FrontendState (FFT@48k) cae interna por SPIRAM_MALLOC_ALWAYSINTERNAL=16384 |
| PSRAM 8 MB | ≪1 MB (stacks LK ~180 K WithCaps, mbedTLS, fifos, ws 20 K) | +43 KB (arena 27 + ring 8 + stack 8) | pre-roll futuro 1.5 s@48k = 144 KB; sobra |
| CPU core 0 | WiFi/lwIP + Adec (~6 %) + render | frontend ~5-8 % + inferencia+VAD **20–60 % (LA incógnita)** | criterio: p95 < 10 ms/hop |
| CPU core 1 | Opus enc cx0 (bench 24.9 % estéreo; mono menos) + SRTP/peers | 0 | sin cambios |

## Opción B — decimador 48→16 kHz (si el frontend a 48 k molesta, y para usos futuros a 16 k)
FIR polifásico ÷3: 144 taps (48/fase), Q15, passband 0–7.3 kHz (cubre los 7 500 Hz del filterbank), stopband ≥8.2 kHz @ −60 dB. Coste: 16 000 out/s × 144 MAC ≈ 2.3 MMAC/s → **1–2 % de un core** con `dsps_fird_s16` de esp-dsp (ya vendorizado, optimizado aes3); latencia ≈1.5 ms. Ejecutar en la tarea ww (no en readFrame). Un CIC+compensador sería aún más barato pero no compensa la complejidad a este coste.

## Plan de spike (2 días)
**Día 1 — integración y medida en seco**
1. Componente + deps + build limpio; anotar delta de `idf.py size-components`.
2. Shim + bindings + tarea ww; boot **sin** `joinRoom` (comentar en app.zig): validar detecciones "okay nabu" a 1/3/5 m; instrumentar cada hop con `esp_timer_get_time()` → p50/p95 de frontend+Invoke; comparar arena en PSRAM vs `MALLOC_CAP_INTERNAL`.
3. Base de métricas: `heap_caps_get_free_size` + `heap_caps_get_minimum_free_size` (INTERNAL/SPIRAM/DMA), `uxTaskGetStackHighWaterMark` de todas las tareas.
**Día 2 — convivencia**
4. Stack completo: sala + agente conversando full-duplex + WW activo. Tarea telemetría (prio 1, cada 5 s): heaps, watermarks, drops del StreamBuffer, `vTaskGetRunTimeStats` → idle% por core con/sin WW.
5. Estrés ≥10 min: agente hablando (peor caso: Adec+render activos), detecciones periódicas, reconexión forzada (cortar AP 10 s → pico DTLS), botón mute.
6. Registrar números y decidir. Si GO: siguiente PR = flags gate/hard_mute + entrenar "Sebastián".

## Criterios GO (todos) / NO-GO (cualquiera)
- GO: p95(frontend+Invoke) < 10 ms por hop de 10 ms; heap interna libre estable > 40 KB (mínimo > 25 KB durante reconexión); 0 artefactos de audio nuevos (helicóptero/underrun/warble) en 10 min de conversación; ≥8/10 detecciones a 3 m con ruido de fondo; 0 watchdogs y watermarks de stack > 512 B.
- NO-GO: p95 > 10 ms con arena interna Y en PSRAM; heap interna < 20 KB o con tendencia bajista; artefactos que aparecen solo con WW activo.

## Plan B escalonado (sin re-arquitectura)
1º `feature_step_size` 20 ms (mitad de Invokes, modelos V1-style); 2º afinidad de ww a core 1 (huecos del encoder) o sin afinidad; 3º **PTT**: long-press del botón como gate manual (ya en ROADMAP) y wake word temporal en el agente (openWakeWord sobre el track publicado — coste: privacidad/red) hasta resolver.

## Código
**firmware/components/wakeword/mww_shim.h + mww_shim.cpp (nuevos)** — Shim C↔C++ mínimo (componente firmware/components/wakeword/): API extern C para Zig + esencia de la inferencia streaming calcada de ESPHome streaming_model.cpp

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

**firmware/main/csdk.zig + mic_src.zig + ww.zig (nuevo)** — Lado Zig: bindings extern a mano (estilo csdk.zig), tap en readFrame ANTES del overwrite de mute (separando gate vs hard_mute) y tarea de inferencia en core 0

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

**firmware/components/wakeword/{idf_component.yml,CMakeLists.txt} + main/CMakeLists.txt + partitions.csv + sdkconfig.defaults** — Integración de build: componente nuevo con pins exactos, REQUIRES en main (zig.cmake hereda includes solo), partición ampliada y sdkconfig como respeaker.yaml

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

## Riesgos
- **Colisión de picos de CPU: Invoke (2–10 ms) coincidiendo con ráfagas WiFi/lwIP/DTLS en core 0 → jitter en el pipeline de audio o detecciones tardías** → Tarea ww a prio 5 (por debajo de AUD_SRC 15 y ARender 20); medir idle% por core con RUN_TIME_STATS; válvulas: step 20 ms (mitad de Invokes), afinidad a core 1 o sin afinidad
- **Contención de caché PSRAM: arena TFLM + stacks LK + heap mbedTLS todo tras un dcache de 64 KB octal → inferencia más lenta de lo publicado** → Activar CONFIG_SPIRAM_RODATA (receta respeaker.yaml); el spike compara arena PSRAM vs MALLOC_CAP_INTERNAL (26 KB caben en interna si hace falta)
- **Drift de versiones esp-tflite-micro/esp-nn rompe el tamaño de arena o la inferencia (ya ocurrió upstream: ESPHome tuvo que pinear tras un bump silencioso)** → Pins exactos en idf_component.yml del componente (1.3.3~1 / 1.1.2, los de ESPHome dev) + probing de arena 1.5x/2x como hace streaming_model.cpp
- **El tap rompe el pacing consumer-paced si el StreamBuffer se llena o el envío bloquea** → xStreamBufferSend con timeout 0 y drop (nunca bloquear readFrame); contador de drops en la telemetría del spike; buffer 8 KB ≈ 85 ms de margen
- **El modelo es-ES "Sebastián" del trainer local no alcanza calidad utilizable (falsos positivos/negativos) aunque la integración funcione** → Separar riesgos: spike con okay_nabu (probado); para producción, verificación en dos etapas ya prevista en ROADMAP (umbral laxo on-device + re-verificación con pre-roll en el agente) + VAD
- **Semántica de privacidad: reusar muted_flag como gate dejaría el WW oyendo con el botón físico pulsado** → Separar flags gate (ARMED: silencio a la sala, tap activo) vs hard_mute (botón: corta también el tap y apaga LED) en mic_src antes de integrar la máquina de estados

## Preguntas abiertas
- ¿p95 real de FrontendProcessSamples+Invoke con arena en PSRAM bajo tráfico WebRTC activo? No hay número público de esta combinación — es exactamente lo que el spike mide (la estimación 20–60 % de un core viene de <10 ms/hop publicado y del precedente de 4 modelos concurrentes en S3).
- ¿Cuánta heap interna libre queda hoy en estado estacionario con la sala conectada y el agente hablando? (estimación por mapa: 60–120 KB; medir con heap_caps_get_free_size antes de tocar nada).
- ¿Pin vigente de esp-nn en ESPHome dev al momento de implementar? (__init__.py dice 1.1.2 hoy; el PR #15628 menciona 1.2.1 — verificar y copiar el pin exacto).
- ¿En qué core acaba AUD_SRC (readFrame)? system.c no le fija afinidad — confirmar con RUN_TIME_STATS para decidir la afinidad de la tarea ww.
- ¿Merece la pena forzar FrontendState a PSRAM (hook de alloc) si los 10–25 KB internos aprietan, o basta con bajar SPIRAM_MALLOC_ALWAYSINTERNAL?
- ¿El anillo de pre-roll de 1.5 s (144 KB PSRAM) se llena desde el mismo tap del WW o desde la tarea ww ya decimada? (decisión de la fase gate+pre-roll, no del spike).

## Fuentes
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