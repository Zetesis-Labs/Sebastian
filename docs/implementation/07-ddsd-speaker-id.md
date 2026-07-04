> **Anexo del informe de implementación** ([`IMPLEMENTATION.md`](../../IMPLEMENTATION.md)). Texto íntegro de la exploración multi-agente del 2026-07-02 (8 agentes en paralelo + contraste cruzado). Donde este anexo contradiga las **Decisiones congeladas** del informe principal, prevalece el informe.

# ddsd-speaker-id — Gate DDSD (device-directed speech detection) para la ventana LINGER + Speaker ID por hogar (ROADMAP señales 3 y 7, fases P1/P2)

**Veredicto:** viable con riesgos — Todas las piezas existen y están verificadas: livekit-agents 1.6.4 trae turn_detection="manual" + commit_user_turn()/clear_user_turn() (el punto exacto donde vive el gate) y STT paralelo con diarización junto a un RealtimeModel; el atajo Gemini Proactive Audio es un parámetro del plugin google. Riesgos principales: ~0,5-0,9 s de latencia añadida al follow-up con el gate en serie, y que Proactive Audio solo existe en modelos native-audio 2.5 (preview, no en 3.1).

**Esfuerzo:** L — ~13-15 días de una persona: atajo Gemini 1 d; firmware DoA/telemetría 2 d; migración 1.6.4 + turn manual + STT paralelo 3 d; juez + fusión 2 d; shadow mode + etiquetado + calibración 3 d (más ~2 semanas de recogida pasiva en paralelo); speaker ID local + enrolamiento 3 d. El atajo Proactive Audio da una demo evaluable el primer día.

## Hallazgos
- livekit-agents 1.6.4 ya incluye los primitivos exactos del gate: turn_detection="manual" y los métodos AgentSession.clear_user_turn()/commit_user_turn() — el audio fluye al Realtime pero NO genera respuesta hasta el commit; clear descarta el buffer. El gate es 'decidir cuál de los dos llamar'.  
  _https://raw.githubusercontent.com/livekit/agents/livekit-agents%401.6.4/livekit-agents/livekit/agents/voice/agent_session.py (L1304, L1311)_
- Con RealtimeModel + STT paralelo, el STT se ignora si el realtime transcribe él mismo ('skip stt transcription if user_transcription is enabled on the realtime model'). Para la rama léxica con Speechmatics hay que pasar input_audio_transcription=None al RealtimeModel de OpenAI (su default es gpt-4o-transcribe).  
  _https://raw.githubusercontent.com/livekit/agents/livekit-agents%401.6.4/livekit-agents/livekit/agents/voice/agent_activity.py (L1958) y realtime_model.py del plugin openai (L412)_
- El evento user_input_transcribed trae transcript, is_final, language y speaker_id — la diarización llega integrada al bucle de eventos del agente; además MultiSpeakerAdapter (en 1.6.4, livekit/agents/stt/multi_speaker_adapter.py) etiqueta primario vs fondo y puede suprimir hablantes de fondo.  
  _https://docs.livekit.io/reference/agents/events/ y https://github.com/livekit/agents/blob/main/examples/voice_agents/speaker_id_multi_speaker.py_
- Speechmatics da speaker ID consistente ENTRE sesiones: enable_diarization=True + known_speakers=[SpeakerIdentifier(label, speaker_identifiers)]; los identifiers se obtienen con el mensaje GetSpeakers de una sesión previa (enrolamiento). Caveat: atados a la versión del modelo de Speechmatics — re-enrolar cuando actualicen.  
  _https://docs.speechmatics.com/integrations-and-sdks/livekit/stt y https://docs.speechmatics.com/speech-to-text/realtime/speaker-identification_
- Atajo Gemini verificado en el plugin: google.realtime.RealtimeModel(proactivity=True) mapea a types.ProactivityConfig(proactive_audio=True) y fuerza api_version=v1alpha. Solo modelos native-audio 2.5 (gemini-live-2.5-flash-native-audio GA en Vertex; gemini-2.5-flash-native-audio-preview-12-2025 en Gemini API); NO soportado en gemini-3.1-flash-live-preview. Escuchando cobra solo input audio (~25-32 tok/s × $3/M ≈ $0,27-0,35/h; en ventanas LINGER de 8 s es despreciable); output solo si responde. Live API soporta español (97 idiomas), pero la doc no acota la calidad de la decisión proactiva por idioma.  
  _https://raw.githubusercontent.com/livekit/agents/main/livekit-plugins/livekit-plugins-google/livekit/plugins/google/realtime/realtime_api.py (L148, L1181, L464) + https://ai.google.dev/gemini-api/docs/live-api/capabilities + https://ai.google.dev/gemini-api/docs/pricing_
- Apple DDSD follow-ups (arXiv 2411.00023): solo texto ASR — 1-best + n-best(8) con costes AM+LM como incertidumbre, prompt 'Query 1 | Query 2' (SIEMPRE con el primer turno), Vicuna-7B con prompting/LoRA/class-head; contexto + n-best dan 20-40% menos falsas alarmas @ 10% falsos rechazos (mejor config: FA 4,8%). SELMA (2501.19377): un solo LLM audio+texto con LoRA para wake+DDSD+ASR: -64% EER en voice-trigger, -22% en DDSD vs modelos dedicados.  
  _https://arxiv.org/html/2411.00023v1 y https://arxiv.org/abs/2501.19377_
- En el firmware, readBeamLed() ya lee el azimut del beam auto-select como f32 en radianes (resid 33 / cmd 75, respuesta {status, 4×f32}) pero lo cuantiza a índice LED 0-11 (pasos de 30°): para el criterio ±20° del gate hay que exportar los radianes crudos y publicarlos ~5 Hz por user packet. mic_level (RMS con decay >>4) y los umbrales de voz 6000/3000 ya existen y están validados en xvf_ui.zig.  
  _firmware/main/xvf_dfu.zig:177-195, firmware/main/mic_src.zig:40-46, firmware/main/xvf_ui.zig:24-26,49-51_
- Speaker ID local: speechbrain/spkrec-ecapa-voxceleb (ECAPA-TDNN, 16 kHz, embedding 192-dim, EER 0,8% en VoxCeleb1-test, verificación por coseno) corre en CPU junto al agente; API actual EncoderClassifier.from_hparams(...).encode_batch(wav).  
  _https://huggingface.co/speechbrain/spkrec-ecapa-voxceleb_
- Juez LLM: benchmarks 2026 miden gemini-2.5-flash-lite y claude-haiku-4.5 con TTFT <600 ms (los más rápidos entre APIs mainstream), mientras gpt-4.1-mini ronda ~2,4 s de TTFT en prompts medios (~4× Haiku) — para el presupuesto de 200-400 ms del juez hay que usar flash-lite o haiku con salida JSON de ~20 tokens, no gpt-4.1-mini.  
  _https://www.kunalganglani.com/blog/llm-api-latency-benchmarks-2026_
- La infraestructura de datos para la evaluación ya está a medias en el repo: agent.py graba el mic entrante a WAV (16 kHz) y record.py graba standalone sin coste de OpenAI; el ROADMAP ya pide poner la grabación tras env var — el shadow mode es una extensión natural.  
  _agent/agent.py:37-58, agent/record.py, ROADMAP.md:166_

## Diseño

# Gate DDSD en LINGER + Speaker ID — diseño sobre este repo

## 0. Prerrequisito firmware (~2 días)
1. `xvf_dfu.zig`: añadir `readAzimuthRad() ?f32` que devuelva el f32 crudo del beam auto-select
   (hoy `readBeamLed` lo cuantiza a 12 LEDs = 30°/paso; el criterio ±20° exige el ángulo continuo).
2. Task ~5 Hz → user packet lossy (topic `doa`) con `{azimuth_rad: f32, mic_level: u32, state: u8}`
   (el C SDK ≥0.3.7 ya trae user packets). En LINGER el gate de `mic_src` queda ABIERTO, LED tenue.

## 1. Topología del gate (agente, livekit-agents 1.6.4)
```
audio ──► Realtime OpenAI [bufferiza, NO genera]            ┌─► juez LLM (200-400 ms)
  │                                                         │
  ├─► Speechmatics STT (diarization) ─► final + speaker_id ─┤
  ├─► ring PCM 16 kHz ─► ECAPA embedding (paralelo) ────────┼─► FUSIÓN ─► commit_user_turn()
packets `doa` ─► ring 10 s ─► ΔDoA, RMS, duración ──────────┘        └──► clear_user_turn()
```
- `AgentSession(llm=openai.realtime.RealtimeModel(input_audio_transcription=None), stt=speechmatics.STT(...), turn_detection="manual")`.
  Clave verificada: si el Realtime transcribe él mismo, el STT paralelo se ignora (agent_activity 1.6.4 L1958) → desactivarlo.
- `turn_detection="manual"` permanente: en ENGAGED se commitea al final de enunciado (EOU de Speechmatics)
  sin gate; en LINGER solo si el gate pasa. Un único code path, sin alternar modos (ver snippet 3).

## 2. Rama acústica (señales ya existentes)
- ΔDoA = |mediana(azimuth durante el enunciado) − azimuth del turno anterior|: ≤20° fuerte, ≤45° neutro, >45° débil.
- Forma de comando: 0,4 s ≤ dur ≤ 8 s Y RMS medio ≥ umbral de voz (mismos niveles que `xvf_ui.zig`, ~3000).
- Hablante: `speaker_id` de Speechmatics == turno anterior, o cos(ECAPA_utt, ECAPA_prev) ≥ 0,45.
- A = FUERTE si (mismo hablante Y ΔDoA≤20° Y forma de comando); DÉBIL si (otro hablante Y ΔDoA>45°); si no NEUTRO.

## 3. Rama léxica — juez LLM (prompt exacto = snippet 1)
- Entrada: último intercambio + transcript final (+ alternativas/confidences si el STT las da — receta Apple) + señales acústicas.
- Modelo: `gemini-2.5-flash-lite` o `claude-haiku-4.5` (TTFT <600 ms medido en 2026; gpt-4.1-mini ~4× más lento: descartado).
  JSON forzado, max_tokens≈20 → 200-400 ms. Pre-lanzar con el parcial interino y relanzar solo si el final difiere.
- En serie ANTES del commit (no paralelo-con-cancelación): el Realtime no ha empezado a generar, no hay nada que cancelar
  y no se paga generación descartada. V2 opcional: commit especulativo + `interrupt()` si el juez llega "no" antes del
  primer frame TTS — gana ~0,4 s pero puede fugar un arranque de voz; solo tras medir.

## 4. Fusión (reglas v1)
| Juez (directed, conf) | Acústica | Acción |
|---|---|---|
| sí, ≥0,75 | FUERTE o NEUTRO | `commit_user_turn()` → ENGAGED |
| sí, 0,50–0,75 | FUERTE | commit |
| sí, ≥0,75 | DÉBIL (otro hablante + otra dirección) | clear — discrepancia, LINGER sigue |
| no, o conf <0,50 | cualquiera | `clear_user_turn()`; si FUERTE, renovar LINGER +4 s |
Wake word siempre bypassa el gate. LINGER expira sin voz → RPC al firmware → ARMED (mic publica silencio).

## 5. Presupuesto de latencia (fin de voz → primera palabra)
Final STT 0,3-0,5 s + juez 0,2-0,4 s (ECAPA y acústica en paralelo, no suman) → commit a ~0,5-0,9 s.
El Realtime ya tiene el audio en su buffer: TTFB ~0,5-0,8 s desde el commit → follow-up contesta en ~1,1-1,7 s
(vs ~0,8-1,2 s de un turno con wake). Aceptable para v1; optimizar con juez sobre parciales.

## 6. Receta Apple DDSD (2411.00023) + SELMA (2501.19377) — 10 líneas
1. Features: solo texto ASR — 1-best del follow-up + n-best (n=8) con costes AM+LM como incertidumbre.
2. El prompt incluye SIEMPRE el primer turno: "Query 1: … | Query 2: …" — es la señal más rentable.
3. Vicuna-7B en 3 sabores: prompting directo, LoRA sobre el prompt, class-head sobre el embedding final.
4. Contexto + n-best ⇒ 20-40% menos falsas alarmas @ 10% falsos rechazos (mejor config: FA 4,8%).
5. El n-best deja al juez "ver" los errores del ASR → pasa alternativas/confidences de Speechmatics al prompt.
6. SELMA: encoder de audio + LLM con LoRA en ambos, un solo modelo para wake + DDSD + ASR.
7. El modelado conjunto gana: −64% EER en voice-trigger y −22% en DDSD vs modelos dedicados.
8. Para Sebastian (sin GPU): fusión tardía reglas+juez ≈ SELMA pragmático con las mismas ideas.
9. El primer turno + follow-up juntos es lo que convierte "frase suelta" en "réplica coherente" — no clasificar aislado.
10. Futuro: destilar juez+acústica a un clasificador local (LoRA 3B) con los datos del shadow mode.

## 7. Atajo Gemini Proactive Audio — probar PRIMERO (~1 día)
- `google.realtime.RealtimeModel(model="gemini-2.5-flash-native-audio-preview-12-2025", proactivity=True, language="es-ES")`.
  El plugin lo mapea a `ProactivityConfig(proactive_audio=True)` y fuerza `api_version="v1alpha"` solo.
- Límites: SOLO modelos native-audio 2.5 (también `gemini-live-2.5-flash-native-audio`, GA en Vertex); NO existe en
  gemini-3.1-flash-live. Caja negra: sin umbrales, sin confidence, sin speaker ID. Cambia el modelo principal a Gemini.
- Coste escuchando: solo input audio ≈ $0,27-0,35/h; en ventanas LINGER de 8 s, despreciable. Español soportado por
  el Live API; la calidad de la decisión proactiva en es-ES hay que medirla (la doc no la acota).
- Recomendación: SÍ — 1 semana con proactivity=True para validar la UX del LINGER y generar weak labels (sus
  decisiones etiquetan datos). El gate propio se construye igual: da independencia de modelo, umbrales y speaker ID.

## 8. Speaker ID por hogar
- Opción A (SaaS, 1 día): Speechmatics `known_speakers=[SpeakerIdentifier(label="ruben", speaker_identifiers=[…])]` →
  `user_input_transcribed.speaker_id` estable entre sesiones. Enrolamiento: sesión de 3-5 frases → `GetSpeakers`
  devuelve los identifiers → guardarlos. Caveat: ligados a la versión del modelo Speechmatics (re-enrolar en upgrades).
- Opción B (local, recomendada, snippet 2): ECAPA-TDNN de speechbrain en el proceso del agente (CPU). Enrolamiento CLI:
  "di 5 frases" grabadas VIA LIVEKIT (mismo canal XVF post-AEC que producción) → media normalizada → `speakers.json`.
  Decisión: cos ≥0,45 identificado / 0,30-0,45 probable / <0,30 desconocido. Calibrar umbrales con las voces de casa.
- Uso: rama acústica del gate, memoria por persona (P2), política de invitados (desconocido ⇒ umbral léxico 0,9 y
  sin acciones sensibles como cerraduras).

## 9. Datos de arranque y evaluación
1. Shadow mode 1-2 semanas: LINGER activo pero solo-log — el gate calcula y NO commitea; solo la wake word dispara.
   Por enunciado: WAV (extender el recorder de `agent.py`, tras env var como ya pide el ROADMAP), serie DoA,
   mic_level, duración, transcript, speaker, y la salida de cada rama + fusión.
2. Etiquetado: CLI que reproduce el WAV y muestra el intercambio previo → tecla d(irigido)/n(o)/x(dudoso).
   Meta: 50-100 ejemplos, ≥30% positivos (forzar follow-ups reales en el uso diario; incluir TV y conversación).
3. Métricas objetivo: falsos disparos <1/día de uso, pérdida de follow-ups <10%, AUC por rama (dice qué umbral mover).
4. Bucle barato: la rama léxica se re-evalúa offline en segundos (replay del set contra el juez) tras cada cambio de
   prompt/umbral; el harness de simulación de agents 1.6 sirve para regresión en CI.

## Orden de ejecución
1) Atajo Gemini (1 d) → calibra expectativas de UX. 2) Firmware DoA+telemetría (2 d). 3) Migración 1.6.4 + manual
turn + STT paralelo (3 d). 4) Juez + fusión (2 d). 5) Shadow + etiquetado + calibración (3 d + 2 sem pasivas).
6) Speaker ID opción B + enrolamiento (3 d).

## Código
**agent/prompts/ddsd_judge.txt (nuevo)** — Snippet 1 — Prompt exacto del juez LLM (clasificación binaria, few-shot, salida JSON). Modelo: gemini-2.5-flash-lite o claude-haiku-4.5, temperature=0, max_tokens=20, respuesta JSON forzada.

```text
Eres el filtro de atención de "Sebastián", un altavoz inteligente. Tras cada
respuesta el micrófono queda abierto unos segundos y puede captar habla que NO
va dirigida a Sebastián: conversación entre personas, televisión, hablar solo.

Decide si el NUEVO ENUNCIADO va dirigido a Sebastián como continuación del
último intercambio. El texto viene de un ASR y puede tener errores; si hay
alternativas, úsalas como pista de incertidumbre.

Criterios: órdenes o preguntas al asistente cuentan como dirigidas AUNQUE
cambien de tema ("apaga la luz"). Comentarios a otra persona, respuestas a
terceros, vocativos con otro nombre y murmullos incompletos NO son dirigidos.

ÚLTIMO INTERCAMBIO
Usuario: {ultimo_turno_usuario}
Sebastián: {ultima_respuesta}

NUEVO ENUNCIADO (ASR): "{transcript}"
Alternativas ASR: {nbest_o_vacio}
Señales: mismo_hablante={true|false|desconocido}, direccion_estable={true|false}, duracion_s={x.x}

Ejemplos:
1. Sebastián: "El temporizador queda en 10 minutos." / Nuevo: "mejor ponlo en quince"
   → {"directed": true, "confidence": 0.97}
2. Sebastián: "Mañana lloverá por la tarde en Bilbao." / Nuevo: "pues coge tú el paraguas anda"
   → {"directed": false, "confidence": 0.85}
3. Sebastián: "He apagado la luz del salón." / Nuevo: "y sube la persiana del cuarto"
   → {"directed": true, "confidence": 0.9}
4. Sebastián: "La receta lleva 200 gramos de harina." / Nuevo: "cariño tráeme la harina del armario"
   → {"directed": false, "confidence": 0.8}
5. Sebastián: "Son las nueve y cuarto." / Nuevo: "eh... nada déjalo"
   → {"directed": false, "confidence": 0.6}
6. Sebastián: "No he encontrado nada con ese nombre." / Nuevo: "busca camping cerca de Somiedo"
   → {"directed": true, "confidence": 0.92}

Responde SOLO con JSON: {"directed": <bool>, "confidence": <0.0-1.0>}
```

**agent/speaker_id.py (nuevo)** — Snippet 2 — Speaker ID local con ECAPA-TDNN (speechbrain) junto al agente: embedding por enunciado, enrolamiento a JSON y decisión por coseno con zona de 'desconocido'.

```python
# ECAPA-TDNN (speechbrain/spkrec-ecapa-voxceleb): 16 kHz, embedding 192-dim, CPU.
import json
from pathlib import Path

import numpy as np
import torch
from livekit import rtc
from speechbrain.inference.speaker import EncoderClassifier

_enc = EncoderClassifier.from_hparams(
    source="speechbrain/spkrec-ecapa-voxceleb",
    savedir=Path.home() / ".sebastian/ecapa",
    run_opts={"device": "cpu"},
)
DB = Path.home() / ".sebastian/speakers.json"  # {"ruben": [192 floats], ...}


def embed(pcm_i16_16k: np.ndarray) -> np.ndarray:
    wav = torch.from_numpy(pcm_i16_16k.astype(np.float32) / 32768.0).unsqueeze(0)
    e = _enc.encode_batch(wav).squeeze().numpy()  # (192,)
    return e / np.linalg.norm(e)


def identify(e: np.ndarray, thr: float = 0.45, gray: float = 0.30) -> tuple[str, float]:
    db = json.loads(DB.read_text()) if DB.exists() else {}
    name, score = max(
        ((n, float(np.dot(e, np.asarray(v)))) for n, v in db.items()),
        key=lambda x: x[1], default=("desconocido", 0.0),
    )
    if score >= thr:
        return name, score          # identificado
    if score >= gray:
        return f"{name}?", score    # probable: no abre políticas sensibles
    return "desconocido", score


async def utterance_embedding(track: rtc.Track, max_s: float = 3.0) -> np.ndarray:
    """2-3 s del enunciado en LINGER → embedding. En producción, tapear el
    AudioStream compartido del gate en vez de abrir uno nuevo por enunciado."""
    buf: list[np.ndarray] = []
    stream = rtc.AudioStream(track, sample_rate=16000, num_channels=1)
    async for ev in stream:
        buf.append(np.frombuffer(ev.frame.data, dtype=np.int16))
        if sum(len(b) for b in buf) >= int(16000 * max_s):
            break
    await stream.aclose()
    return embed(np.concatenate(buf))

# Enrolamiento (CLI): 3-5 frases por miembro GRABADAS VIA LIVEKIT (mismo canal
# XVF post-AEC que producción, p.ej. con record.py) → media de embeddings
# normalizada → DB[nombre]. Coste inferencia: ~22 M params, decenas de ms por
# enunciado de 2-3 s en un core x86 moderno — corre en el mismo pod del agente.
```

**agent/linger_gate.py (nuevo)** — Snippet 3 — Esqueleto del gate en livekit-agents 1.6.4: RealtimeModel sin transcripción propia + Speechmatics diarization en paralelo, turn manual permanente, fusión y commit/clear.

```python
# Gate DDSD para LINGER (livekit-agents 1.6.4)
import asyncio, math, struct
from livekit import rtc
from livekit.agents import AgentSession
from livekit.plugins import openai, speechmatics

session = AgentSession(
    # Sin transcripción del Realtime: si transcribe él, el STT paralelo se ignora
    # (agent_activity.py L1958 en 1.6.4). El default del plugin es gpt-4o-transcribe.
    llm=openai.realtime.RealtimeModel(input_audio_transcription=None, turn_detection=None),
    stt=speechmatics.STT(
        enable_diarization=True,
        end_of_utterance_silence_trigger=0.4,
        known_speakers=load_known_speakers(),  # [SpeakerIdentifier(label, speaker_identifiers)]
    ),
    turn_detection="manual",  # commit explícito SIEMPRE; un solo code path ENGAGED/LINGER
)

@session.on("user_input_transcribed")
def on_transcript(ev):
    if not ev.is_final:
        return
    if state.mode == "ENGAGED":
        session.commit_user_turn()            # endpointing = EOU de Speechmatics
    elif state.mode == "LINGER":
        asyncio.create_task(gate(ev))

async def gate(ev):
    utt = doa_ring.utterance_stats()          # mediana azimut, RMS, dur (packets "doa")
    same = (ev.speaker_id == state.prev_speaker) if ev.speaker_id else \
           cos(await utterance_embedding(mic_track), state.prev_emb) >= 0.45
    dd = abs(ang_diff(utt.azimuth, state.prev_azimuth))
    strong = same and dd <= math.radians(20) and 0.4 <= utt.dur <= 8 and utt.rms >= 3000
    weak = (not same) and dd > math.radians(45)
    judge = await judge_llm(state.last_user, state.last_agent, ev.transcript,
                            same, dd, utt.dur)              # snippet 1; 200-400 ms
    if judge["directed"] and judge["confidence"] >= (0.5 if strong else 0.75) and not weak:
        state.to_engaged()                    # RPC al firmware: LED "atendiendo"
        session.commit_user_turn(transcript_timeout=2.0)    # el Realtime genera AHORA
    else:
        session.clear_user_turn()             # descarta el buffer de audio del Realtime
        if strong:
            state.renew_linger(4.0)           # voz "hacia" Sebastian: renueva la ventana

@ctx.room.on("data_received")                 # telemetría del firmware ~5 Hz
def on_data(pkt: rtc.DataPacket):
    if pkt.topic == "doa":
        azimuth_rad, mic_level = struct.unpack("<fI", pkt.data[:8])
        doa_ring.push(azimuth_rad, mic_level)
```

## Riesgos
- **Latencia del gate en serie (~0,5-0,9 s extra por follow-up) hace la conversación menos fluida que un turno con wake word** → Pre-lanzar el juez con el transcript parcial (interim) y relanzar solo si el final difiere; ECAPA y acústica siempre en paralelo al juez. Si tras medir sigue molestando, v2 con commit especulativo + interrupt() antes del primer frame TTS (aceptando riesgo de fuga de un arranque de voz).
- **Gemini Proactive Audio solo existe en modelos native-audio 2.5 en preview (no en 3.1-flash-live): deprecable, caja negra sin umbrales, y obliga a usar Gemini como modelo principal (cambia voz/latencia vs OpenAI Realtime)** → Tratarlo como experimento de UX y generador de weak labels de 1 semana, nunca como dependencia arquitectónica; el gate propio es el plan de registro.
- **50-100 ejemplos etiquetados no generalizan (TV/radio, visitas, acentos): el gate puede disparar en falso o perder follow-ups fuera del set** → Shadow mode permanente (loguear decisiones aunque el gate esté en producción), umbrales iniciales conservadores, y fallo seguro: ante duda no responder pero mantener LINGER — la wake word siempre funciona como respaldo.
- **Embeddings ECAPA sobre el beam del XVF (post-AEC, canal procesado) pueden desplazar el espacio de embeddings y degradar el speaker ID** → Enrolar por el MISMO canal (grabación vía LiveKit con record.py, no con el micro del móvil) y calibrar el umbral cosine in-domain con las voces reales de casa.
- **Los speaker_identifiers de Speechmatics están ligados a la versión de su modelo: un upgrade silencioso rompe la identificación entre sesiones** → Guardar los WAVs de enrolamiento y automatizar el re-enrolamiento (re-generar identifiers vía GetSpeakers); o preferir la opción B local (ECAPA) que no depende del proveedor.
- **Comportamiento de turn_detection="manual" con RealtimeModel (server VAD off, transcripciones del STT paralelo durante el buffering) tiene esquinas poco documentadas** → Spike de 1 día al inicio de la migración a 1.6.4 que valide: finales de user_input_transcribed en modo manual, commit_user_turn dispara generación del Realtime, clear_user_turn limpia el buffer.

## Preguntas abiertas
- ¿user_input_transcribed emite finales durante turn_detection="manual" con RealtimeModel antes del commit? El código de 1.6.4 sugiere que sí (el STT node corre en continuo), pero hay que confirmarlo en el spike.
- ¿Speechmatics real-time expone n-best o al menos word-confidences utilizables en el prompt del juez (receta Apple)? Si no, 1-best + confidence media.
- ¿Qué tal decide Proactive Audio en es-ES? La doc del Live API no restringe idioma pero tampoco publica métricas; medir FA/FR en la semana de prueba.
- ¿Los user packets del C SDK v0.3.x llegan con topic visible en room.on("data_received") de Python end-to-end desde el firmware Zig? (Los headers los traen desde v0.3.7; falta la prueba integrada.)
- Coste mensual real de Speechmatics RT con diarización en las ventanas LINGER vs ECAPA local — decide si la opción A sobrevive más allá del prototipo.
- Cuando gemini-2.5-flash-native-audio-preview-12-2025 pase a GA o se depreque, ¿mantiene proactivity y a qué precio?

## Fuentes
- https://docs.livekit.io/reference/agents/events/
- https://docs.livekit.io/agents/build/turns/
- https://docs.livekit.io/agents/models/realtime/plugins/gemini/
- https://docs.livekit.io/agents/models/realtime/plugins/openai/
- https://raw.githubusercontent.com/livekit/agents/livekit-agents%401.6.4/livekit-agents/livekit/agents/voice/agent_session.py
- https://raw.githubusercontent.com/livekit/agents/livekit-agents%401.6.4/livekit-agents/livekit/agents/voice/agent_activity.py
- https://raw.githubusercontent.com/livekit/agents/livekit-agents%401.6.4/livekit-agents/livekit/agents/stt/__init__.py
- https://raw.githubusercontent.com/livekit/agents/main/livekit-plugins/livekit-plugins-google/livekit/plugins/google/realtime/realtime_api.py
- https://raw.githubusercontent.com/livekit/agents/main/livekit-plugins/livekit-plugins-google/livekit/plugins/google/realtime/api_proto.py
- https://raw.githubusercontent.com/livekit/agents/main/livekit-plugins/livekit-plugins-openai/livekit/plugins/openai/realtime/realtime_model.py
- https://github.com/livekit/agents/blob/main/examples/voice_agents/speaker_id_multi_speaker.py
- https://ai.google.dev/gemini-api/docs/live-api/capabilities
- https://ai.google.dev/gemini-api/docs/pricing
- https://arxiv.org/abs/2411.00023
- https://arxiv.org/html/2411.00023v1
- https://arxiv.org/abs/2501.19377
- https://docs.speechmatics.com/integrations-and-sdks/livekit/stt
- https://docs.speechmatics.com/speech-to-text/realtime/speaker-identification
- https://huggingface.co/speechbrain/spkrec-ecapa-voxceleb
- https://www.kunalganglani.com/blog/llm-api-latency-benchmarks-2026