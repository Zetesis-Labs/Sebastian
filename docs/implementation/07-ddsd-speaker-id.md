> **Implementation report annex** ([`IMPLEMENTATION.md`](../../IMPLEMENTATION.md)). Full text of the multi-agent exploration from 2026-07-02 (8 agents in parallel + cross-checking). Where this annex contradicts the **Frozen decisions** of the main report, the main report prevails.

# ddsd-speaker-id — DDSD (device-directed speech detection) gate for the LINGER window + Speaker ID per household (ROADMAP signals 3 and 7, phases P1/P2)

**Verdict:** viable with risks — All pieces exist and are verified: livekit-agents 1.6.4 brings turn_detection="manual" + commit_user_turn()/clear_user_turn() (the exact point where the gate lives) and parallel STT with diarization alongside a RealtimeModel; the Gemini Proactive Audio shortcut is a parameter of the google plugin. Main risks: ~0.5-0.9 s of added latency to the follow-up with the gate in series, and that Proactive Audio only exists in native-audio 2.5 models (preview, not in 3.1).

**Effort:** L — ~13-15 person-days: Gemini shortcut 1 d; DoA firmware/telemetry 2 d; 1.6.4 migration + manual turn + parallel STT 3 d; judge + fusion 2 d; shadow mode + labeling + calibration 3 d (plus ~2 weeks of passive collection in parallel); local speaker ID + enrollment 3 d. The Proactive Audio shortcut gives an evaluable demo on the first day.

## Findings
- livekit-agents 1.6.4 already includes the exact primitives for the gate: turn_detection="manual" and the AgentSession.clear_user_turn()/commit_user_turn() methods — the audio flows to Realtime but DOES NOT generate a response until commit; clear discards the buffer. The gate is 'deciding which of the two to call'.  
  _https://raw.githubusercontent.com/livekit/agents/livekit-agents%401.6.4/livekit-agents/livekit/agents/voice/agent_session.py (L1304, L1311)_
- With RealtimeModel + parallel STT, the STT is ignored if the realtime transcribes itself ('skip stt transcription if user_transcription is enabled on the realtime model'). For the lexical branch with Speechmatics, we must pass input_audio_transcription=None to OpenAI's RealtimeModel (its default is gpt-4o-transcribe).  
  _https://raw.githubusercontent.com/livekit/agents/livekit-agents%401.6.4/livekit-agents/livekit/agents/voice/agent_activity.py (L1958) and realtime_model.py from the openai plugin (L412)_
- The user_input_transcribed event brings transcript, is_final, language, and speaker_id — diarization arrives integrated into the agent's event loop; additionally, MultiSpeakerAdapter (in 1.6.4, livekit/agents/stt/multi_speaker_adapter.py) labels primary vs background and can suppress background speakers.  
  _https://docs.livekit.io/reference/agents/events/ and https://github.com/livekit/agents/blob/main/examples/voice_agents/speaker_id_multi_speaker.py_
- Speechmatics provides consistent speaker ID ACROSS sessions: enable_diarization=True + known_speakers=[SpeakerIdentifier(label, speaker_identifiers)]; identifiers are obtained with the GetSpeakers message from a previous session (enrollment). Caveat: tied to the Speechmatics model version — re-enroll when they update.  
  _https://docs.speechmatics.com/integrations-and-sdks/livekit/stt and https://docs.speechmatics.com/speech-to-text/realtime/speaker-identification_
- Gemini shortcut verified in the plugin: google.realtime.RealtimeModel(proactivity=True) maps to types.ProactivityConfig(proactive_audio=True) and forces api_version=v1alpha. Only native-audio 2.5 models (gemini-live-2.5-flash-native-audio GA in Vertex; gemini-2.5-flash-native-audio-preview-12-2025 in Gemini API); NOT supported in gemini-3.1-flash-live-preview. While listening, it only charges for input audio (~25-32 tok/s × $3/M ≈ $0.27-0.35/h; in 8 s LINGER windows it is negligible); output only if it responds. Live API supports Spanish (97 languages), but the doc does not bound the quality of the proactive decision by language.  
  _https://raw.githubusercontent.com/livekit/agents/main/livekit-plugins/livekit-plugins-google/livekit/plugins/google/realtime/realtime_api.py (L148, L1181, L464) + https://ai.google.dev/gemini-api/docs/live-api/capabilities + https://ai.google.dev/gemini-api/docs/pricing_
- Apple DDSD follow-ups (arXiv 2411.00023): ASR text only — 1-best + n-best(8) with AM+LM costs as uncertainty, 'Query 1 | Query 2' prompt (ALWAYS with the first turn), Vicuna-7B with prompting/LoRA/class-head; context + n-best give 20-40% fewer false alarms @ 10% false rejections (best config: FA 4.8%). SELMA (2501.19377): a single audio+text LLM with LoRA for wake+DDSD+ASR: -64% EER on voice-trigger, -22% on DDSD vs dedicated models.  
  _https://arxiv.org/html/2411.00023v1 and https://arxiv.org/abs/2501.19377_
- In the firmware, readBeamLed() already reads the auto-select beam's azimuth as f32 in radians (resid 33 / cmd 75, response {status, 4×f32}) but quantizes it to LED index 0-11 (30° steps): for the gate's ±20° criterion, we need to export the raw radians and publish them ~5 Hz per user packet. mic_level (RMS with decay >>4) and the voice thresholds 6000/3000 already exist and are validated in xvf_ui.zig.  
  _firmware/main/xvf_dfu.zig:177-195, firmware/main/mic_src.zig:40-46, firmware/main/xvf_ui.zig:24-26,49-51_
- Local speaker ID: speechbrain/spkrec-ecapa-voxceleb (ECAPA-TDNN, 16 kHz, 192-dim embedding, 0.8% EER on VoxCeleb1-test, cosine verification) runs on CPU alongside the agent; current API EncoderClassifier.from_hparams(...).encode_batch(wav).  
  _https://huggingface.co/speechbrain/spkrec-ecapa-voxceleb_
- LLM Judge: 2026 benchmarks measure gemini-2.5-flash-lite and claude-haiku-4.5 with TTFT <600 ms (the fastest among mainstream APIs), while gpt-4.1-mini hovers around ~2.4 s of TTFT on medium prompts (~4× Haiku) — for the judge's 200-400 ms budget we need to use flash-lite or haiku with ~20 tokens JSON output, not gpt-4.1-mini.  
  _https://www.kunalganglani.com/blog/llm-api-latency-benchmarks-2026_
- The data infrastructure for evaluation is already halfway in the repo: agent.py records incoming mic to WAV (16 kHz) and record.py records standalone without OpenAI cost; the ROADMAP already asks to put recording behind an env var — the shadow mode is a natural extension.  
  _agent/agent.py:37-58, agent/record.py, ROADMAP.md:166_

## Design

# DDSD Gate in LINGER + Speaker ID — design on this repo

## 0. Firmware prerequisite (~2 days)
1. `xvf_dfu.zig`: add `readAzimuthRad() ?f32` returning the raw f32 of the auto-select beam
   (today `readBeamLed` quantizes it to 12 LEDs = 30°/step; the ±20° criterion requires the continuous angle).
2. Task ~5 Hz → lossy user packet (topic `doa`) with `{azimuth_rad: f32, mic_level: u32, state: u8}`
   (the C SDK ≥0.3.7 already supports user packets). In LINGER the `mic_src` gate remains OPEN, dim LED.

## 1. Gate topology (agent, livekit-agents 1.6.4)
```
audio ──► Realtime OpenAI [buffers, DOES NOT generate]      ┌─► LLM judge (200-400 ms)
  │                                                         │
  ├─► Speechmatics STT (diarization) ─► final + speaker_id ─┤
  ├─► PCM 16 kHz ring ─► ECAPA embedding (parallel) ────────┼─► FUSION ─► commit_user_turn()
packets `doa` ─► 10 s ring ─► ΔDoA, RMS, duration ──────────┘        └──► clear_user_turn()
```
- `AgentSession(llm=openai.realtime.RealtimeModel(input_audio_transcription=None), stt=speechmatics.STT(...), turn_detection="manual")`.
  Verified key: if Realtime transcribes itself, the parallel STT is ignored (agent_activity 1.6.4 L1958) → disable it.
- Permanent `turn_detection="manual"`: in ENGAGED it is committed at the end of utterance (Speechmatics EOU)
  without gate; in LINGER only if the gate passes. A single code path, without alternating modes (see snippet 3).

## 2. Acoustic branch (already existing signals)
- ΔDoA = |median(azimuth during utterance) − previous turn's azimuth|: ≤20° strong, ≤45° neutral, >45° weak.
- Command shape: 0.4 s ≤ dur ≤ 8 s AND average RMS ≥ voice threshold (same levels as `xvf_ui.zig`, ~3000).
- Speaker: Speechmatics `speaker_id` == previous turn, or cos(ECAPA_utt, ECAPA_prev) ≥ 0.45.
- A = STRONG if (same speaker AND ΔDoA≤20° AND command shape); WEAK if (other speaker AND ΔDoA>45°); else NEUTRAL.

## 3. Lexical branch — LLM judge (exact prompt = snippet 1)
- Input: last exchange + final transcript (+ alternatives/confidences if STT provides them — Apple recipe) + acoustic signals.
- Model: `gemini-2.5-flash-lite` or `claude-haiku-4.5` (TTFT <600 ms measured in 2026; gpt-4.1-mini ~4× slower: discarded).
  Forced JSON, max_tokens≈20 → 200-400 ms. Pre-launch with the interim partial and relaunch only if the final differs.
- In series BEFORE the commit (not parallel-with-cancellation): the Realtime has not started generating, there is nothing to cancel
  and no discarded generation is paid. Optional V2: speculative commit + `interrupt()` if the judge arrives "no" before the
  first TTS frame — saves ~0.4 s but could leak a voice startup; only after measuring.

## 4. Fusion (v1 rules)
| Judge (directed, conf) | Acoustics | Action |
|---|---|---|
| yes, ≥0.75 | STRONG or NEUTRAL | `commit_user_turn()` → ENGAGED |
| yes, 0.50–0.75 | STRONG | commit |
| yes, ≥0.75 | WEAK (other speaker + other direction) | clear — discrepancy, LINGER continues |
| no, or conf <0.50 | any | `clear_user_turn()`; if STRONG, renew LINGER +4 s |
Wake word always bypasses the gate. LINGER expires without voice → RPC to firmware → ARMED (mic publishes silence).

## 5. Latency budget (end of speech → first word)
Final STT 0.3-0.5 s + judge 0.2-0.4 s (ECAPA and acoustics in parallel, don't add up) → commit at ~0.5-0.9 s.
The Realtime already has the audio in its buffer: TTFB ~0.5-0.8 s from the commit → follow-up answers in ~1.1-1.7 s
(vs ~0.8-1.2 s for a turn with wake). Acceptable for v1; optimize with judge on partials.

## 6. Apple DDSD recipe (2411.00023) + SELMA (2501.19377) — 10 lines
1. Features: ASR text only — follow-up 1-best + n-best (n=8) with AM+LM costs as uncertainty.
2. The prompt ALWAYS includes the first turn: "Query 1: … | Query 2: …" — it is the most profitable signal.
3. Vicuna-7B in 3 flavors: direct prompting, LoRA on the prompt, class-head on the final embedding.
4. Context + n-best ⇒ 20-40% fewer false alarms @ 10% false rejections (best config: FA 4.8%).
5. The n-best lets the judge "see" ASR errors → pass alternatives/confidences from Speechmatics to the prompt.
6. SELMA: audio encoder + LLM with LoRA on both, a single model for wake + DDSD + ASR.
7. Joint modeling wins: −64% EER on voice-trigger and −22% on DDSD vs dedicated models.
8. For Sebastian (no GPU): late fusion rules+judge ≈ pragmatic SELMA with the same ideas.
9. The first turn + follow-up together is what turns a "loose phrase" into a "coherent reply" — do not classify isolated.
10. Future: distill judge+acoustics to a local classifier (LoRA 3B) with the shadow mode data.

## 7. Gemini Proactive Audio shortcut — try FIRST (~1 day)
- `google.realtime.RealtimeModel(model="gemini-2.5-flash-native-audio-preview-12-2025", proactivity=True, language="es-ES")`.
  The plugin maps it to `ProactivityConfig(proactive_audio=True)` and forces `api_version="v1alpha"` only.
- Limits: ONLY native-audio 2.5 models (also `gemini-live-2.5-flash-native-audio`, GA in Vertex); DOES NOT exist in
  gemini-3.1-flash-live. Black box: no thresholds, no confidence, no speaker ID. Changes the main model to Gemini.
- Listening cost: input audio only ≈ $0.27-0.35/h; in 8 s LINGER windows, negligible. Spanish supported by
  the Live API; the quality of the proactive decision in es-ES must be measured (the doc does not bound it).
- Recommendation: YES — 1 week with proactivity=True to validate LINGER UX and generate weak labels (its
  decisions label data). The custom gate is built identically: provides independence of model, thresholds, and speaker ID.

## 8. Speaker ID per household
- Option A (SaaS, 1 day): Speechmatics `known_speakers=[SpeakerIdentifier(label="ruben", speaker_identifiers=[…])]` →
  `user_input_transcribed.speaker_id` stable across sessions. Enrollment: 3-5 phrase session → `GetSpeakers`
  returns the identifiers → save them. Caveat: tied to the Speechmatics model version (re-enroll on upgrades).
- Option B (local, recommended, snippet 2): ECAPA-TDNN from speechbrain in the agent's process (CPU). CLI Enrollment:
  "say 5 phrases" recorded VIA LIVEKIT (same post-AEC XVF channel as production) → normalized mean → `speakers.json`.
  Decision: cos ≥0.45 identified / 0.30-0.45 probable / <0.30 unknown. Calibrate thresholds with the household's voices.
- Use: acoustic branch of the gate, per-person memory (P2), guest policy (unknown ⇒ 0.9 lexical threshold and
  no sensitive actions like locks).

## 9. Bootstrap data and evaluation
1. Shadow mode 1-2 weeks: LINGER active but log-only — the gate calculates and DOES NOT commit; only the wake word triggers.
   Per utterance: WAV (extend the `agent.py` recorder, behind an env var as ROADMAP already asks), DoA series,
   mic_level, duration, transcript, speaker, and the output of each branch + fusion.
2. Labeling: CLI that plays the WAV and shows the previous exchange → key d(irected)/n(o)/x(doubtful).
   Goal: 50-100 examples, ≥30% positive (force real follow-ups in daily use; include TV and conversation).
3. Target metrics: false triggers <1/day of use, loss of follow-ups <10%, AUC per branch (tells which threshold to move).
4. Cheap loop: the lexical branch is re-evaluated offline in seconds (replay of the set against the judge) after each
   prompt/threshold change; the agents 1.6 simulation harness is useful for CI regression.

## Execution order
1) Gemini shortcut (1 d) → calibrates UX expectations. 2) Firmware DoA+telemetry (2 d). 3) 1.6.4 migration + manual
turn + parallel STT (3 d). 4) Judge + fusion (2 d). 5) Shadow + labeling + calibration (3 d + 2 passive wks).
6) Option B speaker ID + enrollment (3 d).

## Code
**agent/prompts/ddsd_judge.txt (new)** — Snippet 1 — Exact prompt for the LLM judge (binary classification, few-shot, JSON output). Model: gemini-2.5-flash-lite or claude-haiku-4.5, temperature=0, max_tokens=20, forced JSON response.

```text
You are the attention filter of "Sebastián", a smart speaker. After each
response the microphone stays open for a few seconds and can capture speech that IS NOT
directed at Sebastián: conversation between people, television, talking to oneself.

Decide if the NEW UTTERANCE is directed at Sebastián as a continuation of the
last exchange. The text comes from an ASR and may have errors; if there are
alternatives, use them as an uncertainty clue.

Criteria: commands or questions to the assistant count as directed EVEN IF
they change the subject ("turn off the light"). Comments to another person, replies to
third parties, vocatives with another name, and incomplete mumbles ARE NOT directed.

LAST EXCHANGE
User: {ultimo_turno_usuario}
Sebastián: {ultima_respuesta}

NEW UTTERANCE (ASR): "{transcript}"
ASR Alternatives: {nbest_o_vacio}
Signals: mismo_hablante={true|false|desconocido}, direccion_estable={true|false}, duracion_s={x.x}

Examples:
1. Sebastián: "The timer is set for 10 minutes." / New: "better set it to fifteen"
   → {"directed": true, "confidence": 0.97}
2. Sebastián: "Tomorrow it will rain in the afternoon in Bilbao." / New: "well grab the umbrella then"
   → {"directed": false, "confidence": 0.85}
3. Sebastián: "I have turned off the living room light." / New: "and raise the bedroom blind"
   → {"directed": true, "confidence": 0.9}
4. Sebastián: "The recipe needs 200 grams of flour." / New: "honey bring me the flour from the cupboard"
   → {"directed": false, "confidence": 0.8}
5. Sebastián: "It is a quarter past nine." / New: "uh... never mind leave it"
   → {"directed": false, "confidence": 0.6}
6. Sebastián: "I haven't found anything with that name." / New: "search for a campsite near Somiedo"
   → {"directed": true, "confidence": 0.92}

Reply ONLY with JSON: {"directed": <bool>, "confidence": <0.0-1.0>}
```

**agent/speaker_id.py (new)** — Snippet 2 — Local Speaker ID with ECAPA-TDNN (speechbrain) alongside the agent: per-utterance embedding, JSON enrollment, and cosine decision with an 'unknown' zone.

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

**agent/linger_gate.py (new)** — Snippet 3 — Skeleton of the gate in livekit-agents 1.6.4: RealtimeModel without its own transcription + Speechmatics diarization in parallel, permanent manual turn, fusion and commit/clear.

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

## Risks
- **Latency of the gate in series (~0.5-0.9 s extra per follow-up) makes the conversation less fluid than a turn with wake word** → Pre-launch the judge with the partial transcript (interim) and relaunch only if the final differs; ECAPA and acoustics always in parallel with the judge. If after measuring it remains bothersome, v2 with speculative commit + interrupt() before the first TTS frame (accepting the risk of leaking a voice startup).
- **Gemini Proactive Audio only exists in native-audio 2.5 models in preview (not in 3.1-flash-live): deprecable, black box without thresholds, and forces using Gemini as the main model (changes voice/latency vs OpenAI Realtime)** → Treat it as a UX experiment and 1-week weak label generator, never as an architectural dependency; the custom gate is the system of record.
- **50-100 labeled examples do not generalize (TV/radio, visits, accents): the gate could falsely trigger or miss follow-ups outside the set** → Permanent shadow mode (log decisions even if the gate is in production), conservative initial thresholds, and fail-safe: when in doubt, do not respond but keep LINGER — the wake word always works as a fallback.
- **ECAPA embeddings over the XVF beam (post-AEC, processed channel) can shift the embedding space and degrade the speaker ID** → Enroll via the SAME channel (recording via LiveKit with record.py, not with the phone's microphone) and calibrate the in-domain cosine threshold with the real household voices.
- **Speechmatics speaker_identifiers are tied to its model version: a silent upgrade breaks identification across sessions** → Save the enrollment WAVs and automate re-enrollment (re-generate identifiers via GetSpeakers); or prefer the local Option B (ECAPA) that does not depend on the provider.
- **Behavior of turn_detection="manual" with RealtimeModel (server VAD off, parallel STT transcriptions during buffering) has poorly documented edges** → 1-day spike at the beginning of the 1.6.4 migration to validate: user_input_transcribed finals in manual mode, commit_user_turn triggers Realtime generation, clear_user_turn clears the buffer.

## Open questions
- Does user_input_transcribed emit finals during turn_detection="manual" with RealtimeModel before the commit? The 1.6.4 code suggests so (the STT node runs continuously), but it must be confirmed in the spike.
- Does real-time Speechmatics expose n-best or at least word-confidences usable in the judge prompt (Apple recipe)? If not, 1-best + average confidence.
- How well does Proactive Audio decide in es-ES? The Live API doc does not restrict language but does not publish metrics either; measure FA/FR in the trial week.
- Do user packets from C SDK v0.3.x arrive with visible topic in room.on("data_received") in Python end-to-end from the Zig firmware? (The headers are included since v0.3.7; the integrated test is missing.)
- Real monthly cost of Speechmatics RT with diarization in LINGER windows vs local ECAPA — decides if Option A survives beyond the prototype.
- When gemini-2.5-flash-native-audio-preview-12-2025 moves to GA or is deprecated, does it maintain proactivity and at what price?

## Sources
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