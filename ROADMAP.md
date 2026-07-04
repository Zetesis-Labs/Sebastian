# ROADMAP — Sebastian como agente de voz avanzado

Resultado de la auditoría de arquitectura (2026-07-02). Define el camino desde el
estado actual (voz bidireccional funcionando, sin modelo de invocación) hasta un
agente de voz doméstico a la altura de Alexa+ / Gemini for Home, apoyado en lo
que ya existe en el repo.

**Tesis:** el audio far-field (XVF3800) y el transporte (LiveKit/WebRTC) ya están
bien resueltos. Lo que falta no es señal ni red: es (1) un **modelo de
invocación** — hoy el micro se publica a la nube 24/7 y el agente responde a
todo — y (2) un **agente** que pase de prototipo (79 líneas) a producto
(herramientas, memoria, turn detection, dispatch).

---

## 1. Estado de partida (auditoría)

### Sólido — no tocar

- Cadena de audio: XVF master I2S, dos puertos esclavos separados, captura
  consumer-paced sin ring buffer, beam ASR crudo + única pasada de NS (BVC).
- DFU del XVF por I2C desde Zig propio, con imagen de fábrica como red de seguridad.
- Higiene de secretos (nada sensible trackeado).

### Hallazgos accionables

| # | Hallazgo | Dónde |
|---|----------|-------|
| 1 | **Sin invocación**: audio publicado siempre → privacidad, coste y UX. Gap nº 1. | `mic_src.zig` |
| 2 | **AEC/barge-in sin cerrar**: volumen capado a 35/100 por eco residual. El canal RIGHT/ASR es post-AEC pero sin supresión de eco residual; la referencia AEC del XVF entra por la línea I2S del altavoz (GPIO44 compartida — ver `num_channels: 1 # Try mono for XVF AEC` en `homeassistant/respeaker.yaml`). Verificar convergencia del AEC vía protocolo de control (resid AEC). | `board.zig`, `docs/STATUS.md` |
| 3 | **`agent.py` es un prototipo**: sin tools, sin memoria, sin turn detection, grabación a `/tmp` incondicional, dispatch por workaround (sala fresca + reset). | `agent/agent.py` |
| 4 | **livekit-agents ~=1.2 vs 1.6.4 actual**: turn detector de texto deprecado (sustituido por Turn Detector v1.0 audio-nativo, con español), Adaptive Interruption Handling GA, `MCPToolset`. Actualizar antes de construir encima. | `agent/pyproject.toml` |
| 5 | **Token sandbox estático embebido** en el binario: sin rotación, reflash para cambiarlo. | `firmware/main/secrets.zig` |
| 6 | **`wakeword/` entero gitignoreado y sin trackear**: los datasets sí, pero los scripts propios (`download_es_voices.py`, `split_wakeword.py`) deben versionarse. | `.gitignore` |
| 7 | C SDK en Developer Preview (v0.3.10). A favor: desde v0.3.7 trae **RPC, data streams y user packets** — el canal de control dispositivo↔agente ya existe. | `managed_components/livekit__livekit` |

---

## 2. Arquitectura objetivo: el motor de atención (invocación)

La invocación no es un gatillo, es un **sistema de atención por capas** (lo que
Alexa+ resuelve con su fusión on-device y Google con Continued Conversation /
Gemini Live). Sebastian puede jugar esa liga solo con audio porque expone algo
que un Echo no: **DoA y beam control del XVF por I2C**.

### Máquina de estados (firmware, espejo en el agente)

```
IDLE ──actividad──► ARMED ──wake──► ATTENDING ──turno──► ENGAGED
                      ▲   (LED beam)     │                   │
                      │                   └─── respuesta ────┤
                      └──── timeout ◄── LINGER (follow-up) ◄─┘
```

- **ARMED** — sala LiveKit conectada, `mic_src` publica **silencio** (reusar
  `setMuted()` como gate controlado por la máquina de estados). Opus DTX ≈ 0
  coste de red. Wake word corre on-device; nada de voz sale del dispositivo.
- **ATTENDING** — wake detectada: gate abierto **con pre-roll** (~1,5 s en PSRAM
  incluyendo la wake word), LED al hablante y **beam lock** en esa dirección
  (comando I2C existente, ver `lock_beam()` del componente formatBCE).
- **ENGAGED** — conversación con turn detection semántico y barge-in.
- **LINGER** — ~8 s de escucha post-respuesta **sin wake word** con gate DDSD en
  el agente (abajo). LED tenue = "te sigo escuchando". Paridad con el blue-bar
  de Alexa+ y Continued Conversation.

### Las siete señales de invocación

1. **microWakeWord "Sebastián" on-device.** Trainer ya en `wakeword/` con voces
   Piper es-ES/es-MX. Modelos V2 (MixedNet): arena ~23 KB, <10 ms por hop de
   20 ms; hasta 3 modelos + VAD en un S3. Requiere decimar el beam ASR
   48 kHz→16 kHz (que además alimenta el pre-roll).
2. **Verificación en dos etapas**: umbral laxo on-device (recall) +
   re-verificación en el agente con el pre-roll (openWakeWord/clasificador).
   Sensible de lejos sin falsos positivos.
3. **Follow-up con gate DDSD** (LINGER): fusión en el agente de (a) acústica —
   DoA estable, mismo hablante —, (b) léxico — ¿el parcial STT es coherente como
   réplica? (LLM barato <100 ms) —, (c) prosodia/energía. Receta = Apple
   arXiv 2411.00023. Atajo evaluable: **Proactive Audio** de Gemini Live da esta
   capa "gratis".
4. **Botón**: tap = despertar sin voz; long-press = push-to-talk.
5. **Proactividad con cortesía** (RPC agente→dispositivo): chime + LED antes de
   hablar; si `mic_level` indica conversación en la sala, el aviso espera o se
   degrada a LED. Timers/calendario/avisos de Home Assistant.
6. **DoA como telemetría continua** (user packets lossy ~5 Hz): alimenta el gate
   DDSD, la UI del anillo y deja lista la arbitración multi-dispositivo (gana el
   mejor score wake+SNR).
7. **Speaker ID en el agente** (embedding del pre-roll): memoria e instrucciones
   por persona, umbral endurecido ante voces desconocidas, políticas ("un
   invitado no abre la puerta"). Vía STT con diarización (Speechmatics) o
   embeddings propios (ECAPA).

### Extras con personalidad

- **Wake word "para"/"stop"** activa solo durante TTS/timer (multi-modelo
  microWakeWord, como el modelo `stop` interno de HA Voice PE).
- **Whisper mode**: enunciado con RMS bajo + horario nocturno → respuesta a
  volumen proporcional (`esp_codec_dev_set_out_vol`).

### Economía del "siempre conectado"

Mic gated ≈ 0 red, pero LiveKit Cloud factura minutos de participante. Opciones:
(a) timeout de sala tras N min en ARMED + reconexión al despertar (~1–2 s), o
(b) **self-host de LiveKit en cortes** (GitOps Mileto/ArgoCD/Infisical ya
existente): coste marginal cero y la voz no transita nubes de terceros salvo el
tramo del modelo. El agente Python se despliega como Deployment en cortes.

---

## 3. El agente a la altura de 2026

1. **Upgrade a livekit-agents 1.6.x** y activar: Turn Detector v1.0
   (audio-nativo, español; `v1-mini` en CPU si self-host), Adaptive Interruption
   Handling (auto-resume en falsas interrupciones; requiere cerrar el hallazgo
   AEC #2), `preemptive_generation`.
2. **Modelo desacoplado por config**: hoy OpenAI Realtime; alternativas 2026 —
   `gpt-realtime-2` (~$0,18–0,46/min), `gpt-realtime-mini` (~70% menos),
   Gemini native audio (~10× más barato, Proactive Audio), Nova 2 Sonic
   (~$0,015/min, español, tools asíncronas). Medir con
   `SessionUsageUpdatedEvent` y decidir por $/min real.
3. **Herramientas vía MCP** (`MCPToolset`): Home Assistant primero (ya está en
   el proyecto); timers/alarmas del dispositivo por RPC (suenan aunque caiga
   internet); búsqueda Zetesis (mcp-typesense) como memoria documental.
4. **Memoria por persona** keyed por speaker ID (mem0/Zep o Payload+Typesense
   propio). Instrucciones dinámicas: quién habla, hora, contexto.
5. **Operación**: token server propio (`livekit-api`) + **explicit dispatch**
   (`agent_name`) — elimina "sala fresca + reset" y "proceso único a mano";
   grabación de verificación tras env var; evals con el framework de simulación
   de 1.6.0 en CI.

---

## 4. Fases

### P0 — Cimientos (invocación básica real)

- [x] **Spike: microWakeWord + stack LiveKit conviviendo en el S3** — HECHO y
      validado en hardware. microWakeWord (62KB, TFLM, arena 48KB SRAM) corre en
      un core libre y gatea la sesión LiveKit. Ver `docs/WAKE_WORD.md`.
- [x] Entrenar "Sebastián" con el trainer de `wakeword/` (Piper es).
- [x] Upgrade `livekit-agents` a 1.6.x — pin explícito `~=1.6` + turn detection
      por **semantic VAD** de OpenAI (multilingüe; el turn detector de LiveKit no
      compone con RealtimeModel). Recorder gated tras `SEBASTIAN_RECORD=1`.
- [x] Gate + pre-roll — HECHO y validado E2E (2026-07-03): ring de 12s en PSRAM
      **event-driven** — graba hasta el instante del handoff (sala CONNECTED +
      agente dentro), ventana anclada al wake (2s antes + todo el connect),
      reintentos del envío cada 500ms, y el agente descarta el silencio de puerta
      que caía entre pre-roll y directo. "Sebastián, enciende la luz del salón"
      de corrido llega entero sin esperar al verde. La sala sigue siendo
      por-sesión (no siempre-conectada); la costura restante es ~10-200ms en el
      handoff (un lector de I2S a la vez).
- [x] Protocolo data dispositivo↔agente — HECHO (2026-07-03): entrante
      `sebastian.agent_state` (estados del agente → gate half-duplex del micro +
      actividad de sesión) y saliente `sebastian.barge_in` (wake word sobre su
      voz → `interrupt()`). Falta solo volumen/LED por RPC como extra.
- [x] Token server + explicit dispatch con `agent_name` — HECHO y validado en
      hardware. `agent/token_server.py` mintea token fresco por sesión con el
      dispatch embebido; el firmware lo pide vía shim `token_http.c` + `token.zig`.
      Fuera el JWT estático y el baile de "sala fresca + reset".

### P1 — Paridad con la competencia (conversación natural)

- [x] Cerrar referencia AEC (hallazgo #2) — HECHO (commit 2f611f8, `docs/AEC.md`):
      causa raíz `FAR_EXTGAIN=0.0` de fábrica; fix por I2C con readback,
      `AECCONVERGED=1` verificado, volumen software real recuperado. Pendiente el
      afinado fino (delay por chirp, volumen >60, supresión residual si hace falta).
- [x] Barge-in — HECHO (2026-07-03) al estilo Alexa: el wake model vigila el
      audio gateado mientras el agente habla; "Sebastián" le corta al instante.
      (El barge-in acústico natural queda supeditado a que el AEC converja.)
- [x] LINGER (núcleo) — HECHO (2026-07-03): la voz del agente cuenta como
      actividad (estado por data channel), cierre por silencio real de
      conversación, tope 90s → red de seguridad de 10 min. Pendiente validar en
      uso real que los cierres salen por `silence_timeout` y no por el tope.
- [x] `end_session` por voz ("cállate"/"para"/despedida) — tool que borra la
      sala; el device vuelve solo a idle.
- [x] MCP de Home Assistant — HECHO y validado (luces, GetLiveContext); con
      grounding anti-alucinación en las instrucciones. Timers on-device pendientes.
- [ ] Half-duplex fuera cuando el AEC converja (proyecto AEC, abajo).

### P2 — Diferenciación (lo que un Echo no hace)

- [ ] Speaker ID + memoria por persona.
- [ ] Proactividad con política de cortesía.
- [ ] Whisper mode.
- [ ] Beam lock por enunciado (DoA).
- [ ] Multi-dispositivo con arbitraje por DoA/score.
- [ ] Self-host LiveKit + agente desplegado en cortes (GitOps).

### DevEx / observabilidad (2026-07-03)

- [x] **Observabilidad**: serial → `bridge.py` → OTLP → stack LGTM (Grafana) + MCP
      de Grafana; agente exporta como `sebastian-agent` con transcripción por turno
      en Loki; heartbeat (`serial_age`) distingue vivo-callado de muerto. `docs`
      en `tools/telemetry/README.md`. **Pendiente**: vitales del firmware
      (heap/RSSI/temperatura/niveles en sesión) + alertas — ver Pendiente #4.
- [x] **Devcontainer** (`docs/DEVCONTAINER.md`): build ESP-IDF+Zig + agente (F5) +
      token + LGTM dentro; todas las acciones en Run and Debug (patrón Nixon);
      venv del agente en volumen nombrado (no en el mount, evita venvs incompletos);
      Pylance vía `pyrightconfig.json` en la raíz. Flash: host nativo o TCP con
      download mode manual (el USB-JTAG no reenvía el reset por rfc2217).

### Arreglos inmediatos (independientes de fase)

- [x] Afinar `.gitignore`: solo `wakeword/trainer/` ignorado, scripts + modelo versionados.
- [x] Grabación de `agent.py` tras variable de entorno (`SEBASTIAN_RECORD=1`).
- [ ] Documentar en `config.zig` los estados de invocación al nivel de detalle
      de la decisión LEFT/RIGHT.

### Pendiente (actualizado 2026-07-03, orden recomendado)

1. [ ] **Validación en uso real del día**: cierres por `silence_timeout` (no
       `max_duration`), tormenta SCTP debería morir con los cierres limpios
       (`sebastian_sctp_init_total`), turnos fantasma ≈ 0 con el half-duplex.
       Solo requiere usar el aparato y mirar el dashboard.
2. [ ] **Fiabilidad del wake word**: bajar umbral 0.62 → ~0.50 y medir falsos
       positivos con el proxy "sesión sin turno de usuario" (transcripciones).
       Si no basta, re-entrenar con capturas reales (los pre-rolls son dataset).
3. [ ] **Harness de determinismo**: tests Zig de host para la lógica pura
       (decimador, softClip, ventana pre-roll, troceo barge — la clase de bug que
       crasheó 5 veces hoy, cazable en segundos sin hardware), simulador de
       device en Python (participante falso: pre-roll + audio + asserts sobre
       transcripciones/estados) y `make check` pre-flasheo.
4. [ ] **Lote telemetría firmware** (la mitad que falta de "telemetría nivel dios":
       el lado agente + heartbeat ya están): niveles de audio EN sesión, heap libre,
       RSSI, temperatura + alertas Grafana (reboot, SCTP, canal sordo, serial mudo).
5. [ ] **Proyecto AEC** — PLANIFICADO (2026-07-03, exploración multi-agente):
       plan por fases en `docs/implementation/10-aec-fullduplex-plan.md`.
       Hallazgo clave: el "fix" FAR_EXTGAIN fue probablemente placebo (la escala
       es dB, 0.0 = unidad); candidato nº1 = REF_GAIN=8.0 clippeando la
       referencia interna a full-scale. Si converge → fuera half-duplex →
       full-duplex natural + volumen alto sin eco.
6. [ ] **Volumen por voz** (RPC agente→device; la tubería de datos ya existe en
       ambos sentidos) + LED por estado del agente ya recibido.
7. [ ] **Sensibilidad del wake en frase continua** (se solapa con #2).
8. [ ] **AGC del agente**: re-framer a 10ms antes del APM si se recupera.
9. [ ] **PSRAM del pre-roll degrada en silencio**: reflejarlo en boot health.
10. [ ] Timers on-device por RPC (suenan sin internet).

---

## 5. Riesgos y caveats honestos

- **microWakeWord + WebRTC sin precedente público** — validar con el spike P0
  antes de comprometer el resto. WakeNet (ESP-SR) no es plan B hoy: su pipeline
  self-serve no soporta español custom.
- **client-sdk-esp32 en Developer Preview** — APIs sujetas a cambio; fijar
  versión y revisar changelogs antes de cada bump.
- Los datos de mercado (versiones, precios, features de Alexa+/Gemini) son un
  snapshot de 2026-07; re-verificar antes de decisiones de coste.
