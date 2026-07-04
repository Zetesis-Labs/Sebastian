# Firmware (Zig sobre ESP-IDF)

La capa de aplicación del firmware del **ESP32-S3** está escrita en **Zig** sobre
**ESP-IDF v5.4** y el **LiveKit C SDK** (`client-sdk-esp32`). Se eligió Zig por su
seguridad de memoria (Rubén descartó C para esta capa). El núcleo WebRTC/red sigue
en C —es el SDK de LiveKit, que no se reescribe—; Zig cubre la capa de aplicación:
bring-up de placa, fuente de micro, ruta de altavoz, DFU del XVF y la lógica de
sala.

El objeto Zig lo construye `cmake/zig.cmake` usando el **fork Zig de Espressif**
(`0.16.0-xtensa`, backend LLVM Xtensa), que se descarga automáticamente en el
build. El resultado (`obj/app_zig.o`) se enlaza dentro del componente `main` de
ESP-IDF, que exporta `app_main`.

## Por qué bindings `extern` escritos a mano (y no `@cImport`)

Este es el punto de diseño más importante. Zig ofrece `@cImport` (translate-c)
para generar bindings a partir de cabeceras C, pero **se abandonó**: translate-c
**invierte el orden de los include dirs** y **se atraganta con las cabeceras de
newlib de ESP-IDF** (el orden de `#include_next` en `sys/reent.h`, `wint_t` sin
definir en `sys/_types.h`, etc.).

En su lugar, `csdk.zig` **declara a mano** solo las funciones y structs que la app
usa, como `extern`, transcribiendo los layouts exactos de las structs desde las
cabeceras de IDF 5.4 / esp32s3 y de LiveKit (que están bajo
`firmware/managed_components/`). Con esto **Zig nunca parsea cabeceras C**, así
que hay cero problemas de translate-c: el lado C lo compila y enlaza ESP-IDF, y
aquí solo se declaran los prototipos y layouts que se llaman.

El **riesgo** de este enfoque es que los layouts de las structs (sobre todo las
anidadas y los offsets) hay que **cuadrarlos a mano** contra las cabeceras: si una
cabecera de IDF cambia un campo, hay que actualizar `csdk.zig` en paralelo. Por
eso el fichero avisa "Keep in sync".

## Módulos (`main/*.zig`)

### `app.zig` — punto de entrada

Contiene `app_main` y orquesta el arranque en este orden:

1. `livekit_system_init()`.
2. `board.init()` — bring-up de hardware.
3. `xvf_dfu.ensureMaster(i2cBus)` + `xvf_dfu.unmute()` — pone el XVF3800 en el
   firmware I2S-master y lo desmutea.
4. Registra los codecs de audio por defecto
   (`esp_audio_dec_register_default` / `esp_audio_enc_register_default`).
5. `buildCapturer()` — construye la fuente de micro (`mic_src`) y abre el pipeline
   `esp_capture`.
6. `buildRenderer()` — construye la ruta de altavoz (`av_render` sobre I2S), a
   48 kHz / 2 canales / 32-bit, con FIFO acotada (`allow_drop_data = true`) para
   no acumular latencia.
7. Conecta el WiFi (`lk_example_network_connect`), **desactiva el modem sleep**
   (`WIFI_PS_NONE`, imprescindible para audio en tiempo real), arranca **SNTP**
   (para tener reloj válido para TLS) y llama a `joinRoom()`.

`joinRoom()` crea y conecta la sala LiveKit: **publica** Opus 48 kHz mono desde el
capturer y **suscribe** el audio entrante al renderer.

Define además un **panic handler** propio (`panicFn`) que llama a `abort()` para
que ESP-IDF vuelque un backtrace y reinicie, en vez de quedarse colgado. También
fija `std_options` (nivel de log y `logFn` → `log.zig`).

### `board.zig` — bring-up de hardware

- **I2C master** (SDA=5, SCL=6), con un scan de bus para diagnóstico.
- **AIC3104** por I2C (dirección `0x18`): volúmenes de DAC y ruteo de salida
  (HP / line-out).
- **I2S**: el ESP32 se configura como **SLAVE** del reloj de 48 kHz que genera el
  XVF3800, en **dos puertos separados**:
  - **TX (altavoz)** en `I2S_NUM_0`, `DOUT=44`.
  - **RX (micro)** en `I2S_NUM_1`, `DIN=43`.
  - Ambos 32-bit estéreo, esclavos, **compartiendo BCLK=8 y WS=7**.
  - Son dos puertos separados porque un único canal dúplex compartido **corrompía
    el DMA y provocaba crashes**.
- Crea los dispositivos `esp_codec_dev` de reproducción (`play_dev`) y de grabación
  (`rec_dev`) con un **`codec_if` no-op** (el AIC3104 se configura directo por I2C
  y el XVF se autoconfigura, así que `esp_codec_dev` solo mueve datos I2S).
- Volumen de altavoz fijado a **35** (moderado: demasiado alto realimenta al micro).

### `csdk.zig` — bindings `extern` a mano

Todas las declaraciones `extern` al ABI C de ESP-IDF / LiveKit: I2C, I2S,
`esp_codec_dev` y su vtable, `esp_capture` y la interfaz de fuente de audio,
`av_render`, sala/sandbox de LiveKit, FreeRTOS (`xTaskCreatePinnedToCore`),
`esp_timer`, `abort`, etc. Existe por la razón explicada arriba (evitar
translate-c sobre las cabeceras de newlib). Los layouts están transcritos de las
cabeceras de IDF v5.4 para esp32s3 (`SOC_I2S_HW_VERSION_2`).

### `mic_src.zig` — fuente de audio `esp_capture` a medida

Implementa la vtable `esp_capture_audio_src_if_t`
(`open` / `negotiate_caps` / `start` / `read_frame` / `stop` / `close`).

La decisión clave: **lee `i2s_rx` directamente dentro de `read_frame()`**, al ritmo
del consumidor (consumer-paced). El pipeline pide un frame de N muestras y se
bloquea exactamente sobre N muestras de I2S — **sin tarea productora y sin ring
buffer**, de modo que no hay deriva entre el reloj del XVF y el del consumidor. Una
versión anterior con ring buffer de rueda libre derivaba y producía un
"helicóptero"/warble periódico.

Toma el **slot DERECHO** del I2S (el *raw ASR beam* del XVF), convierte de 32-bit a
16-bit con un **desplazamiento fijo a la derecha** (`SHIFT`, actualmente **13**,
calibrado para que la voz caiga alrededor de **-18 dBFS** sin recortar), y publica
la fuente como **48 kHz** (LiveKit reamostrea). En el **primer `read_frame()`** hace
un disable+enable del canal RX para engancharse al reloj ya en marcha del XVF (la
placa habilitó el canal antes de que el XVF estuviera relojando).

### `xvf_dfu.zig` — actualización del XVF3800

Flashea el XVF3800 al firmware **inthost** (I2S-master) sobre I2C (protocolo DFU de
XMOS, dirección `0x2C`, `resid` 240) y lo **desmutea** (el XVF arranca con su GPIO
de mute interno asertado). El binario del firmware va **embebido** en el objeto vía
`@embedFile`. El chip guarda una imagen de fábrica intacta, así que un upgrade
fallido no lo puede brickear. Ver `docs/XVF3800.md` para el detalle del protocolo.

### `log.zig` — puente de logging

Redirige `std.log` de Zig a `esp_log_write` de ESP-IDF, con lo que se conservan los
format strings comprobados en tiempo de compilación y el scoped logging de Zig,
pero la salida aparece por el monitor serie.

## Sistema de build

`cmake/zig.cmake` (incluido desde `main/CMakeLists.txt` tras
`idf_component_register`):

1. Detecta host, descarga y verifica (SHA256) el **fork Zig de Espressif**
   (`0.16.0-xtensa`).
2. **Cosecha** los include dirs y defines del grafo de componentes de ESP-IDF vía
   generator expression (agnóstico de versión, sin rutas de IDF hardcodeadas).
3. Compila `main/app.zig` (y sus imports) a `obj/app_zig.o` y lo enlaza en el
   componente `main`.

El binario del firmware del XVF
(`firmware/main/xvf_fw/xvf_master_1.0.7.bin`, ~868 KB) se **embebe en tiempo de
compilación** con `@embedFile`.

Estructura del componente:

```
main/
  app.zig      ← app_main; media (capturer/renderer), WiFi/SNTP, join a la sala
  board.zig    ← I2C, AIC3104, I2S doble puerto slave, esp_codec_dev (codec_if no-op)
  csdk.zig     ← bindings extern a mano (IDF + codec_dev + capture/render + livekit)
  mic_src.zig  ← fuente esp_capture a medida (lee i2s_rx directo, slot derecho)
  xvf_dfu.zig  ← DFU del XVF3800 por I2C + firmware embebido (@embedFile)
  log.zig      ← puente std.log → esp_log_write
  placeholder.c← vacío (mantiene el componente IDF no vacío)
```

## Build & flash

Requiere ESP-IDF v5.4+ exportado (`. ~/esp/esp-idf/export.sh`).

```bash
cd firmware
idf.py set-target esp32s3
idf.py menuconfig          # Sebastian/LiveKit → Sandbox ID; example_utils → WiFi
idf.py build
idf.py -p /dev/cu.usbmodem101 flash monitor
```

Las credenciales (WiFi, Sandbox ID / token) quedan en `sdkconfig` y `secrets.zig`
(gitignoreados), nunca en `sdkconfig.defaults`.

## Backend / agente

El dispositivo se une a una sala de LiveKit Cloud usando un **token de sandbox**
(servidor de tokens alojado por LiveKit, sin montar backend propio). El
[agente Python](../agent/) entra a la misma sala y pone STT/LLM/TTS.
