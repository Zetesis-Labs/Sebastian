# Arquitectura del sistema — Sebastian

Sebastian es un **altavoz conversacional de voz**. El usuario habla, el dispositivo captura y limpia la voz por hardware, la transporta en tiempo real a la nube, un agente la entiende y responde con voz, y esa respuesta vuelve a sonar por el altavoz. Todo el bucle es voz-a-voz.

- **Hardware:** array de 4 micrófonos Seeed ReSpeaker XVF3800 + XIAO ESP32-S3.
- **Backend:** LiveKit Cloud + un agente Python que usa OpenAI Realtime (speech-to-speech).
- **Firmware (capa de aplicación):** Zig sobre ESP-IDF v5.4 + el LiveKit C SDK (`client-sdk-esp32`).
- **Repositorio:** `github.com/Zetesis-Labs/Sebastian`

---

## 1. Flujo de datos de alto nivel

```
4 micrófonos
    │
    ▼
XVF3800 (DSP de voz XMOS: beamforming + AEC + supresión de ruido)
    │  I2S (48 kHz, 32-bit, estéreo)
    ▼
ESP32-S3
    │  Opus / WebRTC
    ▼
LiveKit Cloud (room)
    │
    ▼
Agente Python (OpenAI Realtime, speech-to-speech)
    │  vuelve al room
    ▼
LiveKit Cloud (room)
    │  Opus / WebRTC
    ▼
ESP32-S3
    │  I2S
    ▼
Códec AIC3104 (DAC)
    │
    ▼
Altavoz
```

El principio de diseño clave es que **la limpieza de audio pesada ocurre en el silicio del XVF3800**, no en el ESP32. El ESP32-S3 solo mueve PCM entre I2S y la red; no procesa señal.

---

## 2. Topología de hardware y buses

El XVF3800 es el cerebro de audio: a él se conectan físicamente los 4 micrófonos y en él corren beamforming, AEC (cancelación de eco acústico) y supresión de ruido. El AIC3104 es un códec pasivo (DAC) para la salida al altavoz, que se configura por I2C solo para volúmenes y enrutado (no genera reloj).

### 2.1 Diagrama de buses

```
                                   I2C bus (I2C_NUM_0)
                        SDA = GPIO5  ·  SCL = GPIO6  (pull-ups internos)
        ┌───────────────────────┬─────────────────────────┬─────────────────────┐
        │                       │                         │                     │
   ┌────┴────┐             ┌────┴─────┐              ┌─────┴──────┐              │
   │ ESP32-S3│             │ XVF3800  │              │  AIC3104   │              │
   │ (master │             │  0x2C    │              │   0x18     │              │
   │  I2C)   │             │ DSP voz  │              │   códec    │              │
   └─┬───┬───┘             └────┬─────┘              └─────┬──────┘              │
     │   │                      │                          │                     │
     │   │        ┌─────────────┴──────────────────────────┴─────────────┐      │
     │   │        │   Reloj I2S compartido (lo GENERA el XVF3800)         │      │
     │   │        │   WS/LRCLK = GPIO7   BCLK = GPIO8   MCLK = GPIO9 (NC) │      │
     │   │        └───────────────────────────────────────────────────────┘     │
     │   │
     │   │  ═══ I2S RX (micrófono) — I2S_NUM_1, ESP = SLAVE ═══
     │   └───── DIN = GPIO43 ◄──────────── XVF3800 (MASTER del reloj)
     │
     │  ═══ I2S TX (altavoz) — I2S_NUM_0, ESP = SLAVE ═══
     └───────── DOUT = GPIO44 ──────────► AIC3104 (SLAVE) ──► altavoz
```

Nota: en el escaneo I2C también puede aparecer un expansor `0x21` (PCAL6416A) presente en la placa; no participa en la ruta de audio.

### 2.2 Bus I2C

| Señal | GPIO |
|-------|------|
| SDA   | 5    |
| SCL   | 6    |

| Dispositivo | Dirección I2C | Rol |
|-------------|---------------|-----|
| XVF3800 (DSP de voz XMOS) | `0x2C` | Cerebro de audio: beamforming, AEC, supresión de ruido. Los 4 micrófonos se conectan a él. Se autoconfigura. |
| TLV320AIC3104 (códec) | `0x18` | DAC pasivo para la salida al altavoz. Se configura por I2C solo para volúmenes/enrutado; **no** genera reloj. |

El ESP32-S3 es el **master del bus I2C** (`i2c_new_master_bus`, `I2C_NUM_0`, pull-ups internos habilitados).

### 2.3 Bus I2S (reloj compartido)

| Señal | GPIO | Dirección | Notas |
|-------|------|-----------|-------|
| WS / LRCLK | 7  | del XVF al ESP/AIC | word-select, compartido |
| BCLK       | 8  | del XVF al ESP/AIC | bit-clock, compartido |
| MCLK       | 9  | — | **SIN USAR** (`I2S_GPIO_UNUSED`) |
| DOUT       | 44 | ESP → AIC3104 | audio al altavoz (TX) |
| DIN        | 43 | XVF3800 → ESP | audio del micrófono (RX) |

**Master/slave del reloj I2S — punto crítico:**

- El **XVF3800** (corriendo el firmware "inthost") es el **MASTER** del I2S: genera el reloj de 48 kHz, con slots de 32 bits y formato estéreo.
- El **ESP32-S3** es **SLAVE** del I2S.
- El **AIC3104** también es slave, sobre el mismo reloj.

El formato de bus es I2S estándar Philips (`bit_shift = true`), 48 kHz, slots de 32 bits, estéreo (dos slots: izquierdo y derecho).

### 2.4 Dos periféricos I2S separados

El ESP usa **DOS periféricos I2S independientes**, ambos como slave, compartiendo BCLK/WS:

| Periférico | Uso | Pin de datos | Rol |
|------------|-----|--------------|-----|
| `I2S_NUM_1` | RX (micrófono) | DIN = GPIO43 | slave |
| `I2S_NUM_0` | TX (altavoz)   | DOUT = GPIO44 | slave |

**Por qué dos puertos y no un canal dúplex único:** un único canal I2S dúplex compartido corrompía el DMA y provocaba cuelgues. Separando micrófono (RX) y altavoz (TX) en dos periféricos distintos, cada uno con su tarea consumidora, el DMA deja de corromperse. Ambos siguen siendo esclavos del mismo reloj BCLK/WS que impone el XVF3800.

---

## 3. Pipeline de audio — MICRÓFONO (uplink)

La captura vive en `firmware/main/mic_src.zig`, una **fuente de audio `esp_capture` a medida**.

```
XVF3800 (beam ASR crudo, slot DERECHO)
   │  I2S RX (48 kHz, 32-bit, estéreo) en I2S_NUM_1
   ▼
mic_src.zig · read_frame()   ← lee i2s_rx DIRECTAMENTE, ritmo marcado por el consumidor
   │   · extrae el slot DERECHO
   │   · convierte 32-bit → 16-bit (desplazamiento a la derecha)
   ▼
fuente esp_capture  →  Opus 48 kHz mono
   │
   ▼
LiveKit (publica; remuestrea aguas abajo)
```

Características de diseño:

- **Lectura directa marcada por el consumidor:** `read_frame()` lee `i2s_rx` directamente. El pipeline pide una trama y el código se bloquea leyendo exactamente esa cantidad de muestras I2S. **No hay ring buffer**, así que no hay deriva entre el dominio de reloj del XVF y el del consumidor: el reloj de 48 kHz del XVF y el consumidor de LiveKit son el mismo bucle. Esto elimina el warble/"helicóptero" periódico que producía un ring buffer de libre marcha.
- **Selección de canal:** extrae el **slot DERECHO** (el beam ASR crudo, ver §5).
- **Conversión de formato:** cada muestra de 32 bits se convierte a 16 bits mediante desplazamiento a la derecha antes de publicar.
- **Publicación:** la fuente publica Opus a **48 kHz mono**; LiveKit remuestrea aguas abajo.
- **Re-sincronización de reloj:** en la primera `read_frame()` se hace `disable` + `enable` del canal RX para engancharse al reloj ya en marcha del XVF (la placa habilitó el canal antes de que el XVF estuviese generando reloj).

---

## 4. Pipeline de audio — ALTAVOZ (downlink)

La reproducción se construye en `firmware/main/app.zig` (`buildRenderer`).

```
Agente (audio) vía LiveKit
   │
   ▼
av_render  (audio_raw_fifo / audio_render_fifo, allow_drop_data)
   │  48 kHz, 32-bit, estéreo
   ▼
i2s render sobre play_dev (esp_codec_dev envolviendo i2s_tx, I2S_NUM_0)
   │  I2S TX (DOUT = GPIO44)
   ▼
AIC3104 (DAC)
   │
   ▼
Altavoz  (volumen 35/100)
```

Características de diseño:

- **Cadena:** `av_render` → un i2s render sobre `play_dev` (un `esp_codec_dev` que envuelve `i2s_tx`) → AIC3104 → altavoz.
- **Formato:** renderizado a **48 kHz, muestras de 32 bits**, para alinearse con los slots I2S de 32 bits del XVF. Renderizar a 16 bits producía salida desalineada y ruidosa.
- **Volumen moderado (35/100):** subirlo demasiado realimenta acústicamente el micrófono (feedback).
- **Latencia acotada:** `allow_drop_data` limita la latencia de reproducción descartando datos en lugar de acumular latencia con el tiempo (`audio_render_fifo` ~0,4 s).

---

## 5. Decisión de canal: IZQUIERDO vs DERECHO

Este es un punto de diseño deliberado y merece explicación honesta.

### 5.1 Lo que documenta el datasheet de XMOS

El XVF3800 emite **dos slots de salida** por I2S (a 48 kHz, 32 bits). Según el datasheet de XMOS existen dos rutas de salida:

- **Salida "ASR"** — tomada directamente del beamformer. **Puentea (bypass) el post-procesador**, por lo que **NO** lleva supresión de ruido. Tiene una ganancia fija configurable, pensada para alimentar motores de reconocimiento de voz (ASR).
- **Salida "de comunicación"** — pasa por el post-procesado completo: reducción de reverberación + supresión de ruido + ecualización + ganancia + limitador. Pensada para escucha humana.

### 5.2 El mapeo a slots I2S de la placa ReSpeaker

Sobre el I2S de la placa ReSpeaker:

- **Slot IZQUIERDO** = salida procesada / de comunicación (**con** supresión de ruido).
- **Slot DERECHO** = beam ASR crudo (**sin** supresión de ruido).

> **Advertencia honesta:** este mapeo izquierdo/derecho proviene de la **wiki de ReSpeaker**, no de una cita primaria verificada de XMOS. XMOS **no** recomienda explícitamente qué canal usar para qué caso. Trátese como empírico, no como dogma del fabricante.

### 5.3 Qué hace este proyecto y por qué

Este proyecto usa el **slot DERECHO (beam ASR crudo, sin NS)**, decidido **empíricamente**:

- Usar el slot IZQUIERDO (con NS en el chip) apilaba esa supresión de ruido con la propia cancelación de ruido BVC del agente. Dos pasadas de NS en serie producían un artefacto de "doble NS" con sonido a **lata** ("tin-can").
- Alimentar el beam crudo (derecho) y dejar que el **BVC del agente haga una única pasada de NS** suena limpio.

En resumen: **una sola supresión de ruido** en toda la cadena, ejecutada por el agente. El firmware entrega el beam crudo para no duplicarla.

> Nota de coherencia del código: algún comentario en `app.zig`/`buildCapturer` menciona el slot IZQUIERDO; la implementación real en `mic_src.zig` toma el slot **DERECHO** (`read_buf[i*2 + 1]`), que es el comportamiento vigente y correcto según esta decisión.

---

## 6. Lado del agente (Python)

Fichero: `agent/agent.py`, sobre `livekit-agents`.

- **Clase `Sebastian(Agent)`** con instrucciones en **español** (asistente de voz breve y natural, sin markdown ni listas porque solo se le escucha).
- **Modelo:** OpenAI Realtime (`openai.realtime.RealtimeModel`), voz `"alloy"`, speech-to-speech.
- **Cancelación de ruido:** `noise_cancellation.BVC()` en las `RoomInputOptions` — esta es la **única** pasada de NS de la cadena (ver §5).
- **Verificación de audio:** graba el track de micrófono entrante a `/tmp/sebastian_rx.wav` (16 kHz mono) para poder escuchar lo que realmente recibe el agente. Solo graba tracks de audio cuyo `participant.identity` contiene `esp32`.
- **Arranque:** conecta al room, y `generate_reply` saluda y se presenta en una frase.

Restricciones operativas:

- Debe correr **un único proceso de agente**. Procesos duplicados provocan que "hable solo".
- El agente se **auto-despacha solo a un room nuevo (fresh)**.

Modos de ejecución:

```bash
uv run agent.py dev      # desarrollo local, hot-reload
uv run agent.py start    # worker de producción
```

---

## 7. Secuencia de arranque del firmware

`app_main()` en `firmware/main/app.zig`:

1. `livekit_system_init()`.
2. `board.init()` — I2C, escaneo I2C, configuración del AIC3104, los dos canales I2S (TX/RX slave), y los `esp_codec_dev` de reproducción y captura.
3. `xvf_dfu.ensureMaster()` — pone al XVF en el firmware que lo hace **master del I2S**.
4. `xvf_dfu.unmute()` — el XVF arranca en mute; se limpia para que el micrófono transmita.
5. Registro de codecs de audio (decoder/encoder por defecto).
6. `buildCapturer()` — crea la fuente de micrófono a medida (`mic_src`).
7. `buildRenderer()` — crea el pipeline de reproducción (`av_render` → I2S TX → AIC3104).
8. WiFi (sin modem-sleep, `WIFI_PS_NONE`, para audio en tiempo real) → SNTP (reloj válido antes del TLS) → `joinRoom()`.
9. `joinRoom()` — publica audio Opus 48 kHz mono desde el capturer y se suscribe al audio del room hacia el renderer.

---

## 8. Decisiones de diseño resumidas

| Decisión | Motivo |
|----------|--------|
| Toda la limpieza pesada en el XVF3800 | El ESP32 solo mueve PCM; no procesa señal. |
| XVF3800 como master del I2S; ESP como slave | El firmware "inthost" del XVF impone el reloj de 48 kHz / 32-bit. |
| Dos periféricos I2S (RX en `NUM_1`, TX en `NUM_0`) | Un canal dúplex único corrompía el DMA y provocaba cuelgues. |
| Captura marcada por el consumidor, sin ring buffer | Evita la deriva de dominio de reloj (warble/"helicóptero"). |
| Render a 32 bits | Alinea con los slots de 32 bits del XVF; 16 bits daba salida ruidosa. |
| Volumen de altavoz moderado (35/100) | Evita realimentación acústica en el micrófono. |
| Slot DERECHO (beam ASR crudo) + BVC del agente | Una sola pasada de NS; evita el "tin-can" del doble NS. |
| Un único proceso de agente, room fresco | Duplicados hacen que "hable solo". |
