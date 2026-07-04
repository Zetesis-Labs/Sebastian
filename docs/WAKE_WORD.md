# Wake word: "Sebastián"

Detección de wake word **on-device** con [microWakeWord](https://github.com/kahrendt/microWakeWord)
(streaming CNN sobre TFLite-Micro). El dispositivo escucha en local con un modelo
de 62 KB y **solo abre la sesión LiveKit (y por tanto la sesión Realtime de pago)
cuando oye su nombre**. En reposo el coste es cero.

Responde a su nombre, **"Sebastián"** (es), entrenado en el modelo.

## Arquitectura

```
                    IDLE (siempre)                      ACTIVO (tras wake word)
┌─────────────────────────────────────┐   ┌──────────────────────────────────────┐
│ XVF3800 ──I2S 48k──▶ wakeword.zig   │   │ XVF3800 ──I2S 48k──▶ mic_src.zig     │
│   decimación 3:1 → 16k mono         │   │   → esp_capture → Opus → LiveKit     │
│   → mww.cpp (frontend + CNN 62KB)   │   │ LiveKit ──▶ av_render → AIC3104      │
│   → prob > 0.62 (media móvil de 4)  │   │ silencio → cerrar sesión → IDLE     │
└─────────────────────────────────────┘   └──────────────────────────────────────┘
```

- **`firmware/components/mww/`** — componente ESP-IDF en C/C++:
  - `mww.cpp` + `mww.h`: shim `extern "C"` sobre TFLite-Micro
    (`MicroMutableOpResolver` con las 13 ops del modelo, `MicroResourceVariables`
    para el estado streaming, arena de 48 KB en SRAM interna).
  - `microfrontend/`: el audio-frontend C de TFLM (ventana 30 ms/paso 10 ms,
    filterbank mel de 40 canales 125–7500 Hz, noise reduction, PCAN, log-scale).
    Copiado de `esphome/esp-micro-speech-features` (mismo código que
    `pymicro-features`, el que se usó en el entrenamiento).
- **`firmware/main/wakeword.zig`** — task FreeRTOS de detección:
  - Lee I2S RX directo (48 kHz estéreo 32-bit), toma el slot configurado en
    `config.zig` (RIGHT/ASR por defecto), decima 3:1 → 16 kHz mono int16 con el
    mismo `softClip` que `mic_src.zig`.
  - Embebe el modelo con `@embedFile("sebastian.tflite")`.
  - `detected` es un atomic que `app_main` sondea; `stop()` hace el hand-off
    limpio del canal I2S (disable/enable) hacia `mic_src`.
- **`firmware/main/app.zig`** — loop principal:
  `esperar wake word → abrir sala LiveKit → silencio local (o desconexión) → cerrar → repetir`.

## Modelo

| Propiedad | Valor |
|---|---|
| Fichero | `wakeword/sebastian.tflite` (copiado a `firmware/main/` para embeber) |
| Tamaño | 62 KB (int8) |
| Input | `[1, 2, 40]` int8 — 2 frames de features de 10 ms por Invoke (stride 20 ms) |
| Output | `[1, 1]` uint8 — probabilidad (escala 1/256) |
| Cuantización input | scale 0.10196079, zero-point −128 |
| Recall (validación) | 99.29 % |
| Falsos positivos | 0.93/hora en ruido ambiente |
| Umbral | media móvil de 4 probabilidades > 0.62 |
| Arena TFLM | 48 KB SRAM interna (el JSON decía 30 KB pero las resource variables van aparte) |

## Entrenamiento

Entrenado en el M4 Pro (MPS) con
[microWakeWord-Trainer-AppleSilicon](https://github.com/TaterTotterson/microWakeWord-Trainer-AppleSilicon).
Ver `wakeword/README.md` para reproducirlo.

**Datos positivos (18 220 muestras):**
- 18 100 TTS "Sebastián" — 9 voces Piper en español
  (acentos AR/ES/MX), generadas con piper-sample-generator.
- 120 grabaciones reales vía el propio XVF3800 (78 "Sebastián"
  del usuario + 42 de una segunda persona), troceadas con `wakeword/split_wakeword.py`.

**Datos negativos:** AudioSet (3 tars), FMA small, WHAM noise — ~20 GB,
descargados y convertidos a 16 kHz por el trainer.

## Los 5 bugs de integración (para no repetirlos)

La validación host (Python, `pymicro-features` + el mismo `.tflite`) daba
prob 0.996 en los 4 clips de prueba mientras el device daba 0 %. Causas, en
orden de descubrimiento:

1. **Resource variables**: el modelo streaming usa ops `VAR_HANDLE`; el
   `MicroInterpreter` necesita un `MicroResourceVariables::Create(...)` explícito
   o falla en `AllocateTensors()`.
2. **Contrato de stride**: input `[1,2,40]` = el modelo consume **2 frames
   frescos por Invoke** (20 ms). Invocar con ventana deslizante solapada hace
   avanzar el estado interno al doble de velocidad con datos duplicados y mata la
   detección.
3. **Escala de features ×10**: `pymicro-features` — lo que vio el
   entrenamiento — expone el uint16 del microfrontend **dividido entre 25.6, no
   entre 256**. Con ÷256 las features llegan ×10 más pequeñas (espectro plano) y
   la probabilidad nunca sube. Verificado numéricamente compilando el frontend C
   en el host y comparando frame a frame.
4. **Estado del CNN sin resetear entre armados** (el peor de diagnosticar): las
   resource variables del modelo streaming sobreviven a `FrontendReset()`. Sin
   `MicroResourceVariables::ResetAll()` en `mww_reset()`, el contexto de la
   última detección queda "caliente": al rearmar tras cerrar la sesión, el
   modelo dispara a ~99 % sin que suene nada → bucle infinito
   detección→sesión→cierre→re-detección. El síntoma para el usuario es "me
   responde a cualquier palabra" (la sesión siempre está abierta) y, cuando el
   estado queda saturado en la otra dirección, "no me detecta nunca". En el
   host no se reproduce porque allí se llama a `reset_all_variables()` por clip.

5. **Aliasing en la decimación 48k→16k** (el más caro de encontrar): decimar
   cogiendo 1 de cada 3 muestras sin filtro pliega la energía >8 kHz de la voz
   real (sibilantes, ruido de sala) sobre el espectro útil → el modelo cae a
   ~0 % con voz en vivo, mientras el **playback de grabaciones/TTS sigue
   detectando al 97-99 %** (viene limitado a 8 kHz de origen: no hay nada que
   aliasear). Ese patrón asimétrico despista hacia ganancias/niveles — todo el
   audio de entrenamiento se remuestreó con filtro, el firmware no. El
   discriminador que lo destapó: la misma voz en vivo por el camino de sesión
   (resampler correcto de LiveKit) daba 1.00 en el modelo host. Fix: FIR
   paso-bajo de 19 taps (6.8 kHz, Q15) antes de la decimación, con historia
   entre chunks.

Además: la detección es sobre la **media móvil** de la ventana de probabilidades
(semántica microWakeWord/ESPHome), no "todas por encima del umbral".

## Diagnóstico en runtime

El task loguea cada 5 s pico de PCM y probabilidad máxima:

```
wakeword: 5s window: pcm peak=32365 max prob=4%
```

- `pcm peak` bajo (<3000) con voz cerca ⇒ problema de audio/canal I2S.
- `pcm peak` alto y `max prob` 0 % con el wake word ⇒ problema de features/modelo.
- Detección correcta ⇒ `WAKE WORD DETECTED` y transición de sesión.

## Validación host (sin flashear)

Reproduce el pipeline exacto del firmware en Python contra cualquier WAV:
modelo + cuantización + stride de 2 frames + media móvil. Útil antes de tocar
el device. El script está en la sección "Validación" de `wakeword/README.md`.

## Limitaciones y siguientes pasos

- El cierre de sesión usa un VAD simple basado en `mic_src.level()`: mínimo de
  20 s, cierre tras 12 s de silencio local y máximo de seguridad de 90 s.
  LINGER/DDSD será la versión semántica posterior.
- La decimación 48k→16k es 3:1 sin filtro anti-aliasing. Para wake word es
  suficiente (la energía útil de la voz queda por debajo de 8 kHz); no reutilizar
  para STT sin añadir filtro.
- El XVF puede colgarse del bus I2C tras muchos reflasheos seguidos (probe
  timeout en todo el bus) — se recupera con un power cycle físico del USB.
