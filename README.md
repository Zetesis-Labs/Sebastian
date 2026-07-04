# Sebastian

Altavoz de voz **conversacional bidireccional** sobre **Seeed ReSpeaker XVF3800
+ XIAO ESP32-S3**, conectado a **LiveKit Cloud**. El usuario habla, el
dispositivo captura y limpia la voz por hardware (array de 4 micrófonos con
beamforming + AEC + supresión de ruido en el XMOS XVF3800), la publica en una
sala de LiveKit, y un **agente Python (OpenAI Realtime)** responde por el
altavoz. Todo el bucle es voz-a-voz.

- **Repositorio:** `github.com/Zetesis-Labs/Sebastian`

## Estado: FUNCIONA

La **conversación de voz bidireccional funciona y está validada en hardware**: el
agente habla por el altavoz y oye al usuario de forma inteligible. El **anillo de
LEDs apunta a quien habla** (dirección de llegada / DoA) y el **botón de mute**
funciona (el anillo se apaga al silenciar). Detalle en
[docs/STATUS.md](docs/STATUS.md).

```
┌─────────────────────────┐         WebRTC          ┌──────────────────┐
│ ESP32-S3 (firmware Zig)  │  ◄───────────────────►  │  LiveKit Cloud   │
│ ReSpeaker XVF3800        │      sala / Opus        │      (sala)      │
│  · 4 mics → XVF3800      │                         └────────┬─────────┘
│  · altavoz ← AIC3104     │                                  │ dispatch
└─────────────────────────┘                         ┌────────▼─────────┐
                                                     │ Agente (Python)  │
                                                     │ OpenAI Realtime  │
                                                     └──────────────────┘
```

## Cómo encaja (resumen)

- El **XVF3800 es el master del I2S** (genera el reloj a **48 kHz**, 32-bit,
  estéreo) y el **ESP32-S3 es esclavo**, sobre **dos puertos I2S separados** (RX
  micro / TX altavoz) para no corromper el DMA.
- El firmware **DFU-flashea el XVF** a su firmware **"inthost" (I2S-master)** por
  I2C **desde nuestro propio código Zig** (sin ESPHome ni herramientas externas)
  y lo des-mutea en el arranque.
- El micrófono usa el **beam RIGHT/ASR crudo del XVF** (sin NS on-chip) y la
  **cancelación de ruido BVC del agente** hace la única pasada de supresión de
  ruido — así se evita el artefacto "de lata" del doble NS.
- El audio de micro se publica como **Opus a 48 kHz**; LiveKit reamostrea aguas
  abajo.

Ver [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) para el detalle completo.

## Decisiones clave

- **Firmware en Zig**: seguridad de memoria, `comptime`, C-interop nativo. El
  núcleo WebRTC/red sigue en C (el SDK de LiveKit, no reescribible); Zig cubre la
  capa de aplicación (bring-up de placa, fuente de micro, ruta de altavoz, DFU
  del XVF, lógica de sala).
- **Agente en Python**: es como se hacen los agentes de LiveKit (framework
  `agents` con baterías: VAD, turn-detection, cancelación de ruido BVC).
- **LiveKit Cloud + token de sandbox**: sin backend de tokens propio.

> ⚠️ Es un proyecto **bleeding edge**: primer LiveKit-en-ESP32 en Zig (fork de
> Espressif `0.16-xtensa`, backend LLVM Xtensa).

## Documentación

| Documento | Qué cubre |
|---|---|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Arquitectura del sistema completa: flujo voz-a-voz, buses I2C/I2S, pipelines de micro y altavoz, decisión de canal, agente. |
| [docs/HARDWARE.md](docs/HARDWARE.md) | Placa ReSpeaker XVF3800 + XIAO ESP32-S3: componentes, pinout, direcciones I2C, topología de audio. |
| [docs/FIRMWARE.md](docs/FIRMWARE.md) | Firmware Zig sobre ESP-IDF v5.4: módulos, bindings `extern` a mano, sistema de build. |
| [docs/XVF3800.md](docs/XVF3800.md) | El chip XVF3800: familias de firmware, DFU por I2C, protocolo, mute, los dos canales de salida. |
| [docs/BUILD_AND_RUN.md](docs/BUILD_AND_RUN.md) | Guía de operador: compilar, flashear, ejecutar el agente, dispatch y verificación de audio. |
| [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) | Playbook síntoma → causa raíz → solución de la sesión de bring-up. |
| [docs/STATUS.md](docs/STATUS.md) | Estado actual del proyecto y puntos abiertos de calidad. |

## Estructura

| Carpeta | Qué |
|---|---|
| [`firmware/`](firmware/) | Firmware del ESP32-S3. App en **Zig** sobre ESP-IDF + LiveKit C SDK. |
| [`agent/`](agent/) | Agente de voz en **Python** (`livekit-agents` + OpenAI Realtime). |
| [`docs/`](docs/) | Documentación (ver tabla anterior). |

## Arranque rápido

Ver [docs/BUILD_AND_RUN.md](docs/BUILD_AND_RUN.md) para la guía completa. En corto:

1. **Firmware**: `source ~/esp/esp-idf/export.sh && cd firmware && idf.py build && idf.py -p /dev/cu.usbmodem101 flash monitor`
2. **Agente** (exactamente uno): `cd agent && uv sync && uv run agent.py dev`
3. **Dispatch** a sala fresca: `lk room delete sebastian` y resetear la placa.

Los secretos (WiFi, token de LiveKit, clave de OpenAI) van en ficheros
gitignoreados; nunca se commitean.
