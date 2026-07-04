# Informe de implementación — Sebastian

Síntesis normativa de la exploración técnica multi-agente del **2026-07-02**
(8 áreas investigadas en paralelo contra el código real del repo, los
componentes vendorizados y las docs/tags oficiales de julio de 2026, más un
contraste cruzado que detectó 10 contradicciones y 10 huecos). Desarrolla el
[ROADMAP.md](ROADMAP.md) hasta nivel ejecutable.

**Cómo leer esto:** este documento resuelve los conflictos y fija las
decisiones; el detalle completo de cada área (diseños ≤120 líneas, snippets
listos para pegar, riesgos, fuentes) vive en los anexos de
[`docs/implementation/`](docs/implementation/). Donde un anexo contradiga las
Decisiones congeladas (§2), prevalece este informe.

---

## 1. Veredicto global

Las 8 áreas son **viables** (ninguna bloqueada). Esfuerzo total estimado:
**~25–35 días-persona**, con dos validadores baratos (10 min y 2 días) que
condicionan todo lo demás.

| # | Área | Veredicto | Esfuerzo | Anexo |
|---|------|-----------|----------|-------|
| 1 | microWakeWord + LiveKit en el S3 (spike) | viable con riesgos — memoria cabe holgada (todo PSRAM-able); la única incógnita es el p95 de CPU conviviendo con ráfagas WiFi/DTLS | M (spike 2 d, +3–4 d integración) | [01](docs/implementation/01-fw-wakeword-spike.md) |
| 2 | Gate + pre-roll + protocolo device↔agent | viable con riesgos — toda la API existe y está **verificada en el código vendorizado**; pre-roll por track inviable → va por data stream | M (6–8 d) | [02](docs/implementation/02-fw-gate-preroll-protocol.md) |
| 3 | Entrenamiento wake word "Sebastián" es | viable — el trainer del repo ya cubre el pipeline completo; faltan 2 datasets y los negativos adversariales | M (3–5 d) | [03](docs/implementation/03-wakeword-training.md) |
| 4 | Agente en livekit-agents 1.6.4 | viable — todas las APIs verificadas contra el tag 1.6.4; adaptive interruption/preemptive NO aplican a S2S (honestidad) | M (4–5 d) | [04](docs/implementation/04-agent-modernization.md) |
| 5 | Referencia AEC del XVF3800 | viable con riesgos — arquitectura confirmada con cita XMOS; el test decisivo cuesta **10 minutos** | M (2–3 d) | [05](docs/implementation/05-aec-verification.md) |
| 6 | Matriz modelo de voz + costes | viable — Gemini/Nova S2S son el suelo de precio (~$10–38/mes); el pipeline NO es la vía barata (el TTS domina) | S (1,5–2 d) | [06](docs/implementation/06-model-cost-matrix.md) |
| 7 | Gate DDSD (LINGER) + speaker ID | viable con riesgos — primitivos exactos en 1.6.4 (`turn_detection="manual"` + `commit/clear_user_turn`); +0,5–0,9 s de latencia en follow-ups | L (13–15 d + 2 sem pasivas) | [07](docs/implementation/07-ddsd-speaker-id.md) |
| 8 | Infra: self-host LiveKit + deploy en cortes | viable con riesgos — encaja con el GitOps existente; BVC es Cloud-only y hostNetwork en nodo único | M (3,5–5 d) | [08](docs/implementation/08-infra-selfhost.md) |
| — | Contraste cruzado (contradicciones/gaps/orden) | — | — | [09](docs/implementation/09-cross-check.md) |

---

## 2. Decisiones congeladas

Resuelven las contradicciones del contraste cruzado. Son **normativas** para la
implementación; cambiarlas exige actualizar este documento.

**D1 — Canal device→agent = `publish_data` (user packets) con reintentos; agent→device = RPC.**
El C SDK 0.3.10 **no implementa RPC saliente** desde el dispositivo
(`livekit_room_rpc_invoke` solo existe en un comentario de `livekit.h:210`;
`core/rpc_manager.c` solo trae register/handle). Verificado en implementación,
no en header. El "ack" del evento `wake` es el RPC `sebastian.state` que
devuelve el agente. ⚠️ El snippet `protocol.py` del anexo 04 registra `wake`
como RPC entrante — corregir a `room.on("data_received")` sobre el topic
`sebastian.evt`. Si un 0.3.x futuro añade rpc_invoke, migrar `wake` a RPC.

**D2 — Sample rate: detector a 48 kHz directo; pre-roll a 16 kHz.**
El frontend de microWakeWord es frecuencia-normalizado y ESPHome lo alimenta
a 48 kHz **en esta misma placa** (`respeaker.yaml`) → el tap del detector va a
48 kHz sin decimar (cero riesgo de mismatch con el training). El ring de
pre-roll sí se decima ×3 a 16 kHz (64 KB PSRAM por 2 s; media-de-3 como
placeholder, FIR de esp-dsp después): es el formato que consumen openWakeWord,
ECAPA y el STT en el agente. Corrige la línea del ROADMAP que asumía decimar
para el detector.

**D3 — Gate vs hard-mute: dos flags, un solo refactor.**
`muted_flag` de `mic_src` se separa en `gate` (razones OR-eadas: `state`
ARMED, `half_duplex`) y `hard_mute` (botón físico: corta también el tap del
wake word y el ring — privacidad real). `xvf_ui.zig` deja de poseer el mute
(hoy fuerza `setMuted` cada 80 ms y pisaría la máquina de estados). Dos áreas
llegaron por separado a la misma corrección → hacerla **una sola vez, antes**
que el spike y que el gate. Corrige el ROADMAP ("reusar setMuted() como gate").

**D4 — Pre-roll por data stream, nunca inyectado al track.**
Inyectar audio viejo al track es inviable: el pipeline reescribe los pts por
contador (`gmf_audio_src.c:118`), la cola src→encoder es de 3×10 ms y el
jitter buffer del agente flushearía el backlog. En su lugar: snapshot del ring
→ data stream `sebastian.preroll` (header `SBPR` + s16le@16k, ~64 KB chunked,
<200 ms en WiFi) → el agente re-verifica la wake word sobre ese PCM y antepone
los frames con una `PrerollInput(io.AudioInput)` encadenada a
`session.input.audio`. Es el mismo patrón que el "pre-connect audio buffer"
oficial de LiveKit (su topic nativo exige attributes que el C SDK no puede
enviar → topic propio).

**D5 — Mecanismo del gate LINGER = `turn_detection="manual"` + `commit_user_turn()`/`clear_user_turn()`** (anexo 07), no `StopResponse` (anexo 04).
Un único code path para ENGAGED y LINGER, verificado contra
`agent_session.py` L1304/1311 del tag 1.6.4. Exige `RealtimeModel(...,
turn_detection=None, input_audio_transcription=None)` + STT paralelo (si el
Realtime transcribe él mismo, el STT paralelo se ignora —
`agent_activity.py:1958`). `StopResponse` en `on_user_turn_completed` queda
como plan B si el spike de 1 día del modo manual encuentra esquinas. En P0
(sin gate): `semantic_vad` de servidor y LINGER = ventana temporal simple.

**D6 — Modelo por factory desde el día 1; P0 `gpt-realtime-mini`, candidato P1 Gemini 2.5 native audio.**
Cambiar ya `gpt-realtime` → `gpt-realtime-mini` (1 línea, −70 % coste). La
factory (`agent/models.py`, anexo 06) hace del modelo una env var. **Evitar
`gpt-realtime-2`** hasta que se cierren las issues del plugin (#5768 pérdida
de mensajes, #5808 truncado silencioso). El gate DDSD se implementa **agnóstico
al modelo**; `proactivity=True` de Gemini es un experimento de 1 semana y un
generador de weak labels, nunca una dependencia (solo existe en native-audio
2.5 preview, no en 3.1). Instrumentar `session_usage_updated` desde el primer
día y decidir con $/min real.

**D7 — Token server + explicit dispatch se adelantan a P0** (resuelve la
desalineación de fases entre anexos). Sin él no hay `RoomAgentDispatch`, no
muere el workaround sala-fresca+reset, y no es posible la fase IDLE que hace
viable el free tier. En P0–P1 corre donde corra el agente (Mac/LAN, FastAPI
de ~40 líneas); a cortes en P2. El firmware hace `GET /token` en boot
(TTL 24 h, secreto por dispositivo, token cacheado en NVS como fallback) —
LiveKit no expulsa al participante cuando el JWT caduca en vivo.

**D8 — El "silencio ≈ 0 coste" del ROADMAP es falso; la economía se decide con IDLE.**
Opus DTX solo existe a 8/12/16 kHz (publicamos a 48 kHz) y el SDK no lo
expone: ARMED cuesta pocos kbps de red **y minutos de participante Cloud**
(86.400/mes conectado 24/7 vs 5.000 del free tier). Decisión: implementar
**IDLE (desconexión de sala tras N min en ARMED + reconexión al wake, ~1–2 s)**
al final de P0, tras el token server. Con uso real (~180 min/mes) el free
tier sobra. Ship ($50/mes) queda de colchón; self-host lo elimina en P2.

**D9 — Orden BVC/AEC: cerrar el hallazgo AEC ANTES de cualquier A/B sin BVC.**
El beam ASR (mux 7,3) **no** tiene supresión de eco residual — no es "la señal
ya limpia" que asumía el plan self-host. `noise_cancellation.BVC()` es
Cloud-only y falla en seco contra server self-hosted → gate por env var
(`LIVEKIT_CLOUD=0`) desde ya; la comparación beam-solo vs
`livekit-plugins-dtln` se hace después de cerrar AEC (Fase D).

**D10 — Turn detector parametrizado v1/v1-mini por config desde el día 1.**
`inference.TurnDetector` v1 (audio-nativo, es) corre solo en LiveKit Inference
(Cloud); en self-host solo hay v1-mini local (CPU). Si el gate de P1 se
acopla a v1 sin selector, la migración a cortes lo rompe.

**D11 — Arena del detector dimensionada por manifest, no hardcodeada.**
El spike usa 26.080 B (okay_nabu); el trainer propio emite
`tensor_arena_size=30000`. El componente firmware lee el JSON del manifest y
hace probing 1×/1,5×/2× como `streaming_model.cpp`.

---

## 3. Contrato del protocolo device↔agent (normativo)

Congela los supuestos de los anexos 02, 04 y 07. Versionado: campo `ver` en
todos los payloads JSON; identidad del agente = convención `agent_name=
"sebastian"` **y** verificación `kind==AGENT` + estado ACTIVE en
`on_participant_info`; el device valida `sender_identity` en RPC/datos.

| Mensaje | Dir | Canal | Payload |
|---|---|---|---|
| `wake` | dev→ag | `publish_data` **reliable**, topic `sebastian.evt` (retry+backoff propio: el SDK no bufferiza si engine≠CONNECTED) | `{ver,type:"wake",wake_id,score,doa_deg,speaker_hint:null}` |
| `button` | dev→ag | ídem | `{type:"button",kind:"tap"\|"long"\|"double"}` |
| `state` espejo | dev→ag | ídem | `{type:"state",state:"armed"}` |
| telemetría DoA | dev→ag | `publish_data` **lossy**, topic `sebastian.doa`, ~5 Hz | binario `<f32 azimuth_rad, u32 mic_level>` (+u8 state) |
| pre-roll | dev→ag | **data stream** bytes, topic `sebastian.preroll` | header 12 B `SBPR` (ver u8, wake_id u32, sr u16=16000) + s16le |
| `state` | ag→dev | **RPC** `sebastian.state` (ack síncrono) | `{state:"listening"\|"thinking"\|"speaking"\|"linger"\|"idle", ttl_ms?}` |
| `led` / `volume` | ag→dev | RPC `sebastian.led` / `sebastian.volume` | `{pattern,r,g,b}` / `{level}` |
| `announce` | ag→dev | RPC `sebastian.announce` | req `{chime:true}` → resp `{busy:bool}` (cortesía: `mic_level` delata conversación) |

Reglas duras del firmware: los handlers RPC corren en la tarea de `esp_peer`
con la invocación en stack → **parsear, encolar, `send_result` antes de
retornar**, jamás bloquear; payloads ≤1 KB. JSON entrante con
`std.json.parseFromSliceLeaky` + `FixedBufferAllocator`; saliente con
`std.fmt.bufPrint` (plantillas comptime — el Stringify del fork 0.16 es
inestable post-writergate).

### Máquina de estados (fuente de verdad repartida, reconciliación definida)

```
        WW local / botón-tap (device)          turno aceptado (agente)
ARMED ─────────────────────► ATTENDING ─────────────────────► ENGAGED
  ▲  gate cerrado            │ gate ABIERTO ya (optimista)      │ speaking ⇒ half_duplex
  │  (publica silencio)      │ + wake evt + pre-roll stream     ▼
  ├── watchdog 8 s ◄─────────┘ veto agente ("idle")           LINGER (ttl 8 s, gate abierto,
  └────── expira ttl (device) / "idle" (agente) ◄──────────────┘        gate DDSD en el agente)
```

- El **device** es dueño de: apertura optimista al wake (sin RTT — el pre-roll
  cubre el hueco), watchdog de 8 s sin respuesta del agente, expiración de ttl,
  botón. El **agente** es dueño de: veto tras re-verificar la wake word,
  transiciones dentro de ENGAGED/LINGER, ttl de LINGER.
- **Reconciliación**: si el agente desaparece (participant disconnect) o la
  sala cae con el gate abierto → el device cierra el gate y vuelve a ARMED.
  Al reconectar, el device publica su `state` espejo y el agente responde con
  el suyo. IDLE (desconectar sala) entra al final de P0 (D8); hasta entonces
  IDLE≡ARMED.
- Falso positivo del wake = fuga acotada de ~200–500 ms de audio hasta el veto
  (apertura optimista). Mitigación: LED encendido siempre que el gate esté
  abierto (honestidad visible) y modo opcional "apertura solo tras ack".

---

## 4. Plan integrado (orden del contraste cruzado)

**Fase A — validadores baratos, en paralelo (semana 1)**
1. **AEC Test 0** (10 min, anexo 05): mux `AUDIO_MGR_OP_R ← (5,0)` (far-end
   passthrough) + reproducir TTS + grabar con `record.py`. Se oye el TTS →
   la referencia AEC llega eléctricamente al XVF. Decide si existe camino a
   full-duplex/barge-in; un NO recoloca medio plan (half-duplex permanente).
   Después, lectura de `AEC_AECCONVERGED` (33,3) y tests A/B del playbook.
2. **Spike microWakeWord** (2 d, anexo 01): componente `firmware/components/
   wakeword/` con pins exactos de ESPHome (`esp-tflite-micro 1.3.3~1`,
   `esp-nn 1.1.2`, `esp-micro-speech-features ^1.2.3`), shim `extern "C"`
   (`mww_init/feed/reset`), modelo `okay_nabu` probado, tap a 48 kHz.
   GO: p95(frontend+Invoke) <10 ms/hop, heap interna >40 KB estable, 0
   artefactos en 10 min de conversación, ≥8/10 detecciones a 3 m. Incluye la
   **medición de heap estacionaria** (gap #5). Plan B escalonado:
   `feature_step_size` 20 ms → afinidad core 1 → PTT temporal.
3. **Entrenamiento "Sebastián"** arranca en paralelo (anexo 03): auditar
   `personal_samples/` (los 42 `mix_*` entran como
   positivos ×3.0 — escucharlos), `prepare_datasets.py` (wham_16k está vacío,
   chime_16k no existe), generar los ~5.000 negativos adversariales
   (`sebastiana`, `san sebastián`, `bastión`, `se bastan`, `es bastante`,
   `la estación`…), y lanzar
   `MWW_LANGUAGE=es MWW_CALIBRATION_TARGET_FAPH=0.5 ./train_microwakeword_macos.sh "sebastián" 50000 100 --language es`
   (~8–14 h M-series). No entrenar modelo aparte para "oye sebastián": el
   modelo streaming dispara con el sufijo. Aceptación: recall ≥95 % @1 m /
   ≥90 % @3 m / ≥80 % @3 m con TV; FAPH ≤0,5 sobre ≥10 h de TV/podcast es.
4. **Paso 0 del agente** (anexo 04): crear el proyecto LiveKit Cloud propio
   con API key/secret (gap #1 — el token actual es de un sandbox que **expira
   ~agosto 2026**) + upgrade a `livekit-agents ~=1.6.4` + `AgentServer` +
   `@server.rtc_session(agent_name="sebastian")`.

**Fase B — congelar contratos (días 3–5, tras el GO del spike)**
Las §2 y §3 de este documento son ese congelado: D1 (publish_data), D2
(48 k/16 k), D5 (gate manual) y el refactor único D3, que se implementa aquí,
antes de que las dos áreas de firmware toquen `mic_src`.

**Fase C — implementación P0 (semanas 2–3)**
`invocation.zig` + gate + pre-roll + RPC entrantes (anexo 02, snippets
listos) contra el agente ya modernizado, usando **botón-tap como wake
sintético** (valida gate, pre-roll y protocolo antes de integrar el modelo);
en paralelo la **factory de modelos** (anexo 06 — antes que el DDSD porque
fija el modelo); token server + dispatch explícito (D7); al final: integrar
`sebastin.tflite` (convergen spike + training) y E2E con wake real; IDLE (D8).

**Fase D — P1 barge-in (semanas 4–5)**
Cierre AEC según árbol del anexo 05 — caso esperado: referencia OK + residuo
no lineal → **switch dinámico del mux por I2C** (`OP_R ← (6,3)` comms con
supresión residual solo mientras el agente habla, `← (7,3)` al terminar; 5
bytes I2C sin reflashear) + reconstruir el volumen real (el `set_out_vol(35)`
actual cae en un codec noop — el control efectivo hay que cablearlo al
AIC3104) + subidas graduales monitorizando `AECCONVERGED`. Al cerrar:
desactivar `half_duplex`. Semana de experimento **Gemini Proactive Audio**
como calibrador de UX y generador de weak labels.

**Fase E — DDSD + speaker ID (P1/P2, ~13–15 d + 2 semanas pasivas)**
Anexo 07: `readAzimuthRad()` (el `readBeamLed` actual cuantiza a 30°; el
criterio del gate es ±20°) + telemetría 5 Hz → gate de fusión (acústica
ΔDoA/duración/hablante + juez LLM `gemini-2.5-flash-lite` o `claude-haiku-4.5`
— gpt-4.1-mini descartado por TTFT ~4× — con el prompt exacto del anexo) →
`commit/clear_user_turn`. **Shadow mode** 1–2 semanas (solo-log) → 50–100
ejemplos etiquetados → calibrar. Speaker ID local con ECAPA-TDNN (192-dim,
coseno ≥0,45 / zona gris 0,30) enrolado **por el mismo canal XVF vía LiveKit**.
Métricas: falsos disparos <1/día, pérdida de follow-ups <10 %. Presupuesto de
latencia asumido: follow-up contesta en ~1,1–1,7 s (vs ~0,8–1,2 s con wake).

**Fase F — infra self-host (P2, 3,5–5 d)**
Anexo 08: chart `livekit/livekit-server` en cortes (hostNetwork — NodePort de
Talos no cubre 50000+; sin Redis ni TURN en réplica única LAN; rango UDP
50000–50100), `ws://10.0.0.151:7880` directo (el media va DTLS-SRTP igual;
wss con cert-manager como mejora — el SDK C ya adjunta el CA bundle), token
service y agente como Deployment en `manifests/sebastian/` de Mileto con
Application ArgoCD dedicada (patrón langfuse/herschel: el monorepo ZP no se
entera) + ExternalSecret desde Infisical path `/sebastian`. Antes: decidir
registry (ghcr.io vs Harbor de px-socrates) y CI del repo Sebastian, y
comprobar el ingress firewall de Talos (abrir UDP 50000–50100 + TCP 7880/7881
si hay NetworkRuleConfig). A/B beam-solo vs `livekit-plugins-dtln` **después**
del cierre AEC (D9).

### Economía (snapshot jul-2026, recalcular con `session_usage_updated`)

| Concepto | Cifra |
|---|---|
| Conversación (uso moderado, 15/día): gpt-realtime-2 / mini / Gemini / Nova / pipeline | $41 / $12,8 / $9,6 / $9,6 / $15–26 al mes |
| LINGER sin gate DDSD (moderado) | +$0,16–3,46/mes según modelo — **el gate se justifica por UX/privacidad, no por coste** (salvo con realtime-2) |
| LiveKit Cloud 24/7 conectado | 86.400 min/mes ≫ free tier 5.000 → IDLE (D8) o Ship $50/mes o self-host $0 |

---

## 5. Huecos detectados → dueño asignado

| Gap (contraste cruzado) | Resolución |
|---|---|
| Proyecto LiveKit Cloud propio con API keys (el sandbox expira ~ago-2026) | **Fase A.4** — primera tarea del área agente |
| Especificación única del protocolo | **§3 de este documento** (normativa) |
| Máquina de estados sin dueño/reconciliación | **§3** — dueños y reconciliación definidos |
| Motor de re-verificación de la wake word en el agente | openWakeWord sobre el PCM 16 k del pre-roll como primera opción; medir el presupuesto de veto (200–500 ms) en Fase C; STT+match textual como fallback sin dependencia nueva |
| Presupuesto de memoria consolidado del firmware | El spike (Fase A.2) mide la heap estacionaria; el GO incluye margen para el ring de pre-roll (64 KB PSRAM) y el StreamBuffer |
| Refactor único `mic_src`/`xvf_ui` (gate vs hard_mute) | **D3**, se ejecuta en Fase B antes que ambas áreas de firmware |
| Dónde corren agente y token server en P0/P1 y cómo ve el ESP32 `/token` | Mac/LAN en P0–P1 (HTTP en claro dentro de la LAN, secreto por dispositivo), cortes en P2 (F) |
| CI/registry de las imágenes | Propuesta: ghcr.io + GH Actions en el repo Sebastian; decidir vs Harbor antes de Fase F |
| Política de privacidad y retención de audio | Definir antes del shadow mode (Fase E): grabaciones tras env var (ya en ROADMAP), WAVs de enrolamiento y logs DDSD en disco local con retención 30 días, borrado documentado; el pre-roll de falsos positivos se descarta en el agente tras el veto |
| Decisión IDLE adelantada | **D8** — al final de Fase C |

---

## 6. Riesgos transversales

- **Pins de dependencias en cascada**: C SDK 0.3.10, `esp-tflite-micro/esp-nn`
  (precedente real de rotura upstream: ESPHome PR #15628), `livekit-agents`,
  modelo de Speechmatics (invalida speaker IDs enrolados). Un solo owner de
  bumps; congelar versiones y leer changelogs.
- **Modelos en preview**: `gemini-2.5-flash-native-audio-preview-12-2025`
  (único con `proactivity`), Nova 2 Sonic reciente. La factory (D6) hace del
  cambio un deploy de 1 variable; no acoplar arquitectura a ninguno.
- **Los cálculos de coste son snapshot jul-2026** sobre precios de preview:
  instrumentar `session_usage_updated` desde el primer despliegue de Fase C.
- **"Sebastián" es nombre común**: las menciones legítimas en TV/conversación
  dispararán el detector — se mitiga en la máquina de estados y la
  re-verificación, no bajando el recall del modelo.
- **Fuga de audio en falsos positivos** (apertura optimista): acotada y
  visible (LED); documentarla en la política de privacidad.

---

## 7. Actualizaciones pendientes al ROADMAP.md

1. Línea "reusar `setMuted()` como gate" → separar `gate` vs `hard_mute` (D3).
2. Supuesto "Opus DTX ≈ 0 coste de red" → falso a 48 kHz; la economía real es
   IDLE/minutos Cloud (D8).
3. "Decimar 48→16 kHz para el detector" → el detector come 48 kHz directo;
   16 kHz solo para pre-roll/agente (D2).
4. Re-etiquetar el gate DDSD como feature de **privacidad/UX** (no de ahorro):
   con modelos baratos el LINGER sin gate cuesta <$4/mes (anexo 06).
5. El protocolo usa publish_data para dev→agent (no RPC saliente) (D1).
