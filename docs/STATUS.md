# Estado del proyecto (2026-07-03)

**Wake word "Sebastián" FUNCIONANDO on-device y validado en
hardware.** El dispositivo escucha en local con un modelo TFLite-Micro de 62 KB
(coste cero en reposo) y solo abre la sesión LiveKit al oír su nombre; la cierra
por silencio local y vuelve a modo detección. Ciclo completo verificado:
detección (ambas pronunciaciones) → sesión → cierre → re-detección.

## 2026-07-03 — conversación natural, observabilidad y devcontainer

- **Pre-roll event-driven**: el ring (12 s PSRAM) graba hasta el instante del
  handoff (sala CONNECTED + agente dentro), con ventana anclada 2 s antes del
  wake y reintentos del envío. "Sebastián, enciende la luz del salón" de corrido
  llega entero **sin esperar al aro verde**. El agente descarta el silencio de
  puerta inicial para no partir el turno. (`pre_roll.zig`, `app.zig`, `agent.py`)
- **Protocolo de datos device↔agente**: el agente publica su estado
  (`sebastian.agent_state`) y el device publica el barge-in (`sebastian.barge_in`).
- **Half-duplex + LINGER**: el AEC no converge en sesión, así que el micro se
  silencia mientras el agente habla (+400 ms de cola) — mata el bucle de eco/auto-
  charla; su voz cuenta como actividad de sesión, el cierre pasa a silencio real y
  el tope de 90 s → red de seguridad de 10 min. (`mic_src.zig`, `app.zig`)
- **Barge-in estilo Alexa**: "Sebastián" sobre la voz del agente lo corta (el wake
  model vigila el audio gateado → `interrupt()`); el agente tiene prohibido decir
  su propio nombre. `end_session` por voz ("cállate"/"para"/despedida) cierra la sala.
- **Home Assistant MCP** con grounding anti-alucinación (GetLiveContext obligatorio
  para el estado de la casa).
- **Observabilidad**: serial → `tools/telemetry/bridge.py` → OTLP → stack LGTM
  (Grafana) + MCP de Grafana; el **agente exporta como `sebastian-agent`** con la
  transcripción por turno en Loki; heartbeat (`serial_age`) distingue vivo-callado
  de muerto. (`tools/telemetry/`, `agent/telemetry.py`)
- **Devcontainer** (`docs/DEVCONTAINER.md`): entorno entero en contenedor (build
  ESP-IDF+Zig, agente con F5, token, LGTM), todo en el desplegable de Run and Debug
  (patrón Nixon), venv del agente en volumen nombrado. Flash desde el host (auto-
  reset nativo) — por TCP el USB-JTAG no reenvía el reset (download mode manual).
- **Bug de campo cazado**: `@min(comptime, x)` estrecha el tipo a u9 → overflow;
  crasheaba el barge-in cada sesión. Regla: reproducir la lógica pura en host con
  el zig del toolchain antes de flashear.

La base previa (voz bidireccional, beam ASR, LED/mute) sigue como estaba —
commits en `origin/main`:

- `feat(firmware): two-way LiveKit voice on XVF3800 + XIAO ESP32-S3`
- `fix(firmware): use XVF ASR beam (right slot) to kill the tin-can double-NS`
- `feat(firmware): LED-ring DoA + mute UI, and boot health/error hardening`

## Qué funciona

- **Wake word on-device** (`docs/WAKE_WORD.md`): microWakeWord entrenado en el
  M4 Pro con TTS de 9 voces Piper españolas + 120 grabaciones reales vía XVF.
  Recall 99.3 %, <1 falso positivo/hora. Componente C/C++ `firmware/components/mww`
  (TFLM + microfrontend) + task Zig `wakeword.zig`. El loop de `app.zig` gatea la
  sesión LiveKit con la detección — **arquitectura por activación, no always-on**.
- **Firmware Zig** sobre ESP-IDF v5.4, con **bindings `extern` escritos a mano**
  (no `@cImport`): compila y enlaza limpio para Xtensa. El antiguo blocker
  translate-c↔newlib está resuelto — Zig ya no parsea cabeceras C.
- **XVF3800 en firmware "inthost" master 1.0.7**: nuestro propio firmware lo
  DFU-flashea por I2C en el primer arranque (`xvf_dfu.zig`, sin ESPHome) y lo
  des-mutea (GPIO30). Arranques posteriores lo detectan ya en 1.0.7 y lo saltan.
- **Reloj I2S**: el XVF es **master a 48 kHz** (32-bit, estéreo); el ESP es
  **esclavo** sobre **dos puertos I2S separados** (RX micro en `I2S_NUM_1`, TX
  altavoz en `I2S_NUM_0`) compartiendo BCLK/WS. El canal RX se comparte entre el
  task de wake word (idle) y `mic_src` (sesión activa) con resync en cada hand-off.
- **Captura de micro**: lectura directa del I2S marcada por el consumidor
  (consumer-paced, sin ring buffer), tomando el **beam RIGHT/ASR** del XVF
  (canal seleccionable en instalación vía `config.zig`); se publica **Opus a
  48 kHz**. La **NS la hace la BVC del agente** (única pasada).
- **Altavoz**: `av_render` → I2S TX → **AIC3104** → altavoz.
- **UI**: **anillo de LEDs apuntando a quien habla** (DoA) + **botón de mute**
  (anillo apagado al silenciar).
- **Health banner de arranque**: el boot reporta `BOOT OK` / `DEGRADED`
  (incluye la carga del modelo de wake word).

## Resuelto recientemente

- **Cierre por silencio simple**: el timeout fijo de sesión se sustituyó por
  VAD local sobre `mic_src.level()` con mínimo de 20 s, cierre tras 12 s de
  silencio y máximo de seguridad de 90 s. Esto evita cortar frases por un
  contador fijo y deja LINGER/DDSD como mejora semántica posterior.
- **AEC FUNCIONANDO (2026-07-02, `docs/AEC.md`)**: el build de fábrica traía
  `AEC_FAR_EXTGAIN=0.0` → el AEC nunca adaptaba (creía el altavoz mudo). Fix:
  `FAR_EXTGAIN=1.0` por I2C en el boot, verificado por readback y reflejado en
  el boot health (`xvf_aec.applyConfig`). `AECCONVERGED=1` verificado; el agente
  ya no se transcribe a sí mismo a plena escala. Colateral: el volumen software
  era un no-op desde siempre (set_vol noop suprimía el sw-vol de esp_codec_dev)
  — ahora el mando es real. El diagnóstico dejó la tabla completa de comandos
  I2C del XVF (`docs/xvf3800_command_map.txt`), una sonda de referencia in-band
  (`xvf_aec.probeReference`) y acceso I2C serializado (lock en `xvf_dfu.xfer` —
  el protocolo de control es write+read en dos transacciones y xvf_ui sondea
  DoA cada 80ms).
- La sesión se abre por wake word con **token fresco por sesión + dispatch
  explícito del agente** (ver `docs/BUILD_AND_RUN.md`).

## Puntos abiertos (no bloqueantes)

1. **Afinado fino del AEC** (`docs/AEC.md`): medir el delay real con chirp si
   queremos exprimir ERLE; probar volumen >60; supresión de eco residual del
   canal comms si hiciera falta.

## Notas operativas recurrentes

- **Exactamente un proceso de agente.** Varios procesos hacen que "hable solo":
  `pkill -9 -f "agent.py"` y arrancar uno solo.
- **Dispatch explícito por token (ya NO hay "sala fresca + reset").** El token
  server crea el dispatch del agente por API en cada token, así que re-despertar
  a una sala viva ya mete al agente (bug del "no responde tras re-wake" resuelto).
- **Placa en `/dev/cu.usbmodem101`.** Si no flashea (boot-loop / esptool no
  conecta): **power-cycle** físico del USB, o mantener **BOOT** pulsado al
  enchufar (modo download manual). La placa no se brickea. Para flashear desde el
  **devcontainer** por TCP: `make serial-share` en el host + download mode manual
  (el auto-reset del USB-JTAG no cruza rfc2217).
- **Si el bus I2C entero da probe timeout** (XVF y AIC3104 mudos): el XVF se ha
  colgado — power-cycle físico del USB. Suele pasar tras muchos reflasheos seguidos.

Ver [BUILD_AND_RUN.md](BUILD_AND_RUN.md) para la operativa completa,
[WAKE_WORD.md](WAKE_WORD.md) para el sistema de wake word y
[TROUBLESHOOTING.md](TROUBLESHOOTING.md) para el playbook de depuración.
