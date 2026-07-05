> **Implementation report annex** ([`IMPLEMENTATION.md`](../../IMPLEMENTATION.md)). Full text of the multi-agent exploration from 2026-07-02 (8 agents in parallel + cross-checking). Where this annex contradicts the **Frozen decisions** of the main report, the report prevails.

# model-cost-matrix — Voice model decision matrix (S2S vs pipeline) for Sebastian: real costs for home use and compatibility with livekit-agents 1.6.x

**Verdict:** viable — There are at least two cheap S2S routes (Gemini 2.5 native audio and Nova 2 Sonic, ~$10-38/month) fully supported in livekit-agents 1.6.4, and the abstraction to change models via config is a small change to the current `agent.py`. The only problematic candidate today is gpt-realtime-2 (open issues in the LiveKit plugin) and the pipeline is not the cheapest option as assumed: TTS dominates its cost.

**Effort:** S — 1.5-2 person-days: 0.5 d factory + config + upgrade deps; 0.5 d test the 3 S2S plugins against the real device; 0.5-1 d usage telemetry and real $/min table. (The DDSD gate and state machine are separate work, already planned in P1.)

## Findings
- Current prices (Jul-2026) per 1M audio tokens: gpt-realtime-2 $32 in / $64 out (cached in $0.40); gpt-realtime-mini $10 in / $20 out (text $0.60/$2.40); Gemini 2.5 flash native audio and gemini-3.1-flash-live-preview $3 in / $12 out; Nova 2 Sonic $3 in / $12 out. Metering: OpenAI 1 tok/100 ms in and 1 tok/50 ms out (600/1200 tok per min); Gemini and Nova ~25 tok/s (≈1500 tok/min).  
  _https://developers.openai.com/api/docs/pricing , https://ai.google.dev/gemini-api/docs/pricing , https://aws.amazon.com/nova/pricing/_
- In OpenAI Realtime each Response re-sends ALL history as input and is billed cumulatively; automatic caching (audio cached at $0.40/1M, −98.75%) makes it tolerable but requires static and truncated history (`token_limits.post_instructions` / `retention_ratio`). Gemini Live and Nova maintain the session server-side and bill the streamed audio.  
  _https://developers.openai.com/api/docs/guides/realtime-costs_
- Calculated monthly cost (only conversation audio tokens): moderate (337.5 min in / 450 min out): gpt-realtime-2 $41, gpt-realtime-mini $12.8, Gemini $9.6, Nova 2 Sonic $9.6, pipeline $15-26. Intense (1200/1800 min): $161 / $50 / $38 / $38 / $58-101. The pipeline is NOT the cheap route: TTS (Cartesia $50/1M chars ≈ $0.045/min) costs 2.5× Gemini's audio-out.  
  _https://livekit.com/pricing/inference_
- LINGER (8 s post-response, only audio-in): moderate +$0.81-3.46/month without gate, +$0.16-0.69 with DDSD gate (80% cut); intense +$2.9-12.3 without gate, +$0.58-2.46 with gate. The DDSD gate is only economically relevant with gpt-realtime-2; with Gemini/Nova/mini the extra cost without gate is already <$4/month — the gate is justified by UX/privacy, not cost.  
  _own calculation on verified prices (script in the report)_
- Proactive Audio (`proactivity=True` in the google plugin of livekit) only works in gemini-2.5-flash-native-audio-preview-12-2025; it is NOT supported in Gemini 3.1 models. Also, it saves spurious audio-OUT (the model decides not to respond) but the LINGER audio-IN is billed the same: it is complementary to the DDSD gate, not a substitute.  
  _https://docs.livekit.io/agents/models/realtime/plugins/gemini/_
- Maturity of livekit-agents 1.6.4 plugins (PyPI Jun-24-2026): openai realtime mature for gpt-realtime/mini but with open issues for gpt-realtime-2 (#5768 multi-message generation discards messages, #5808 silent truncation on response incomplete, #5906 reasoning.effort not exposed); google realtime mature incl. proactivity (3.1 with limitations: no async tools, no update_agent); aws realtime supports Nova 2 Sonic as default (`livekit-plugins-aws[realtime]`) with installation issues (#3244) and TypeError (#3629).  
  _https://github.com/livekit/agents/issues/5768_
- Nova 2 Sonic supports Spanish with polyglot voices (language switching in the same conversation) and tool calling; `mcp_servers` in AgentSession is deprecated in 1.6.x in favor of `MCPToolset` in `tools=`, which works with any model (realtime or pipeline) because bridging is done by the agent.  
  _https://docs.aws.amazon.com/nova/latest/nova2-userguide/sonic-language-support.html , https://docs.livekit.io/agents/logic/tools/mcp/_
- The current `agent.py` (79 lines) already uses `AgentSession(llm=openai.realtime.RealtimeModel(...))`: the abstraction by config is just a factory that returns different kwargs (`llm=` vs `stt=`/`llm=`/`tts=`); the rest (`Agent`, `RoomInputOptions`, BVC) does not change.  
  _agent/agent.py:62-71_

## Design

# Voice model decision matrix — Sebastian

## 1. Verified prices (Jul-2026) and metering

| Model | Audio in $/1M | Audio out $/1M | Metering | $/min in | $/min out |
|---|---|---|---|---|---|
| gpt-realtime-2 | 32 (cached 0.40) | 64 | 1 tok/100ms in, 1 tok/50ms out | 0.0192 | 0.0768 |
| gpt-realtime-mini | 10 (cached ~0.30) | 20 | ditto | 0.006 | 0.024 |
| gemini-2.5-flash-native-audio / 3.1-flash-live-preview | 3 | 12 | ~25 tok/s | 0.005 | 0.018 |
| Nova 2 Sonic (Bedrock) | 3 | 12 | ~25 tok/s | 0.0045 | 0.018 |
| Pipeline (LiveKit Inference) | STT Deepgram Nova-3 multi 0.0058/min | TTS Cartesia $50/1M chars ≈ 0.045/min · Inworld $25/1M ≈ 0.024/min | LLM gpt-4.1-mini $0.40/$1.60 per 1M | — | — |

Key OpenAI rule: each Response re-bills ALL history (assistant audio included, as input);
automatic caching leaves it at $0.40/1M if history is static → add ~10-20% to base figures and
enable truncation (`token_limits.post_instructions`, `retention_ratio≈0.8`). Gemini/Nova: stateful session, no explicit re-billing.

## 2. Monthly cost scenarios (30 days, audio tokens only)

(a) moderate = 15 conv/day × 45s in + 60s out → 337.5 min in / 450 min out
(b) intense = 40 conv/day × 60s in + 90s out → 1,200 min in / 1,800 min out

| Model | (a) base | (b) base | LINGER (a) without/with gate | LINGER (b) without/with gate |
|---|---|---|---|---|
| gpt-realtime-2 | **$41** (+15% hist ≈ $47) | **$161** (≈$185) | +$3.46 / +$0.69 | +$12.3 / +$2.46 |
| gpt-realtime-mini | **$12.8** | **$50** | +$1.08 / +$0.22 | +$3.84 / +$0.77 |
| Gemini native audio | **$9.6** | **$38** | +$0.81 / +$0.16 | +$2.88 / +$0.58 |
| Nova 2 Sonic | **$9.6** | **$38** | +$0.81 / +$0.16 | +$2.88 / +$0.58 |
| Pipeline (Deepgram+4.1-mini+Cartesia) | **$26** | **$101** | STT only: +$1.04 | +$3.71 |
| Pipeline with Inworld TTS | **$15.5** | **$58** | ditto | ditto |

LINGER = 8s × number of responses (3/conv moderate, 4/conv intense) = 180 / 640 min-in/month. DDSD gate cuts 80%.
**Cost conclusion**: the DDSD gate is only profitable with gpt-realtime-2; on the rest it's a UX/privacy issue.
And the pipeline is NOT the cheap route (TTS dominates): Gemini/Nova S2S are already the price floor.

## 3. Features that matter to Sebastian (livekit-agents 1.6.4 plugins state)

| Feature | gpt-realtime-2 | gpt-rt-mini | Gemini 2.5 NA | Gemini 3.1 live | Nova 2 Sonic | Pipeline |
|---|---|---|---|---|---|---|
| Tools + MCP (MCPToolset agent) | Yes (+own server-side MCP) | Yes | Yes (async tools) | Yes (no async) | Yes | Yes (total) |
| es-ES Voice | Good (marin/cedar, sometimes neutral accent) | Okay | Very good (30+ HD voices, es-ES) | ditto | Good (polyglot, es) | The best (ElevenLabs/Cartesia, clonable) |
| Own turn detection | semantic_vad | semantic_vad | Native VAD + LK turn detector | ditto | 3 levels (HIGH/MED/LOW) | LiveKit Turn Detector v1.0 native-audio (es) |
| Proactive Audio | No | No | **Yes (proactivity=True)** | **No** | No | Equivalent via DDSD gate |
| Barge-in / Adaptive Interruption | Yes | Yes | Yes | Yes | Yes | Yes |
| User transcriptions | Opt-in (gpt-realtime-whisper $0.017/min extra) | ditto | Included | Included | Included (text tokens) | Native (they are the STT) |
| Typical voice-to-voice latency | ~500-800 ms | ~500 ms | ~600 ms | ~500 ms | ~300-500 ms | ~700-1200 ms |
| 1.6.x Plugin | **Open issues** (#5768/#5808/#5906) | Mature | Mature | Doc limitations | Works, rough edges (#3244/#3629) | Most mature |

## 4. Pipeline as control alternative

Pros: LLM is only invoked if the DDSD gate approves (LINGER costs only STT, ~$1-4/month); partials STT
free for the lexical signal of the gate; memory/RAG/speaker-ID trivial to inject into the prompt; es-ES TTS superior.
Cons: NOT cheaper than Gemini/Nova (TTS dominates); +1 latency hop; prosody/emotion inferior to native S2S.

## 5. Recommendation per phase

- **P0 (development)**: switch NOW `gpt-realtime` → `gpt-realtime-mini` (1 line, same plugin, −70% cost) and
  set up the factory below. Iterate the gate/state with **pipeline** in parallel because it gives partials STT (DDSD lexical signal) and readable logs.
- **P1 (product at home)**: **gemini-2.5-flash-native-audio-preview-12-2025 with proactivity=True** — better real $/min
  ($9.6-38/month), Proactive Audio covers LINGER without custom logic, native es-ES, mature plugin. Plan B: Nova 2 Sonic (same price,
  lower latency, less tested plugin). Avoid gpt-realtime-2 until #5768/#5808 are closed (4× cost with no clear advantage for a home).
- **Always**: measure with `SessionUsageUpdatedEvent` and decide with own real $/min, not with these estimates.

### Abstraction in agent.py (what changes in AgentSession)

Factory by `SEBASTIAN_MODEL` env; only the kwargs of `AgentSession` change — `Agent`, `RoomInputOptions(BVC)` and
`MCPToolset` in `tools=` are invariant (see snippet). With S2S `llm=RealtimeModel` is passed; with pipeline
`stt=`/`llm=`/`tts=` + `turn_detection=MultilingualModel()` are passed. Note 1.6.x: `mcp_servers=` is deprecated → `MCPToolset` in `tools=`.

Concrete steps:
1. `agent/pyproject.toml`: `livekit-agents[openai,google,aws,deepgram,cartesia,turn-detector]~=1.6.4` (aws with `[realtime]` extra).
2. Create `agent/models.py` with the factory (snippet) and `agent/config.py` reading `SEBASTIAN_MODEL`, `SEBASTIAN_VOICE`.
3. In `entrypoint`: `session = build_session(os.environ.get("SEBASTIAN_MODEL", "gemini"))`; recorder after env var (fix already listed in ROADMAP).
4. Add listener `session.on("usage_updated")` that logs tokens in/out by type → CSV → decide by real $/min after 1 week.
5. DDSD Gate: implement it model-agnostic (cuts audio BEFORE the AgentSession via custom audio node), so it works equally for S2S and pipeline.

## Code
**agent/models.py (new)** — Factory that abstracts model choice by config: what changes in AgentSession in each case (llm=RealtimeModel vs stt/llm/tts)

```python
import os
from livekit.agents import AgentSession
from livekit.plugins import openai, google, aws, deepgram, cartesia
from livekit.plugins.turn_detector.multilingual import MultilingualModel

def build_session(kind: str | None = None, **common) -> AgentSession:
    kind = kind or os.environ.get("SEBASTIAN_MODEL", "gemini")
    voice = os.environ.get("SEBASTIAN_VOICE")
    match kind:
        case "openai":          # P0: iterate cheap with the plugin already in use
            return AgentSession(
                llm=openai.realtime.RealtimeModel(
                    model="gpt-realtime-mini", voice=voice or "marin"),
                **common)
        case "gemini":          # P1: Proactive Audio = LINGER almost free
            return AgentSession(
                llm=google.beta.realtime.RealtimeModel(
                    model="gemini-2.5-flash-native-audio-preview-12-2025",
                    voice=voice or "Puck",
                    proactivity=True),   # only 2.5 native-audio models, NOT 3.1
                **common)
        case "nova":            # plan B same price as Gemini
            return AgentSession(llm=aws.realtime.RealtimeModel(), **common)
        case "pipeline":        # total control: LLM only runs if gate approves
            return AgentSession(
                stt=deepgram.STT(model="nova-3", language="multi"),
                llm=openai.LLM(model="gpt-4.1-mini"),
                tts=cartesia.TTS(voice=voice or "<voice-id-es>"),
                turn_detection=MultilingualModel(),  # v1.0 native-audio, es
                preemptive_generation=True,
                **common)
        case _:
            raise ValueError(f"Unknown SEBASTIAN_MODEL: {kind}")
```

**agent/agent.py (entrypoint, minimal change)** — Use of the factory + real cost telemetry with SessionUsageUpdatedEvent; Agent/BVC/MCP do not change between modes

```python
session = build_session()  # SEBASTIAN_MODEL decides S2S vs pipeline

@session.on("usage_updated")  # log tokens by type -> real $/min
def _on_usage(ev):
    log_usage_csv(ev.usage)   # audio_in/out, text, cached -> decide with data

await session.start(
    room=ctx.room,
    agent=Sebastian(),  # tools=[MCPToolset(...)] here: mcp_servers= is deprecated in 1.6.x
    room_input_options=RoomInputOptions(noise_cancellation=noise_cancellation.BVC()),
)
```

## Risks
- **gpt-realtime-2 with livekit openai plugin loses messages (multi-message generation #5768) and silently truncates responses (#5808)** → Do not use in P0/P1; if evaluating is desired, pin gpt-realtime (v1) or mini and subscribe to the issues before reconsidering
- **All three cheap models are preview (gemini-2.5-flash-native-audio-preview-12-2025, gemini-3.1-flash-live-preview, Nova 2 in recent Bedrock): prices and model-ids can change without notice** → The factory by config makes changing models a 1-variable deploy; re-verify prices before P1 (calculations are Jul-2026 snapshot)
- **Proactive Audio does not exist in Gemini 3.1: if Google retires 2.5 native audio, the 'free' LINGER is lost** → Implement custom DDSD gate regardless (model-agnostic) as primary layer; proactivity remains as reinforcement, not dependency
- **Cost estimates sensitive to assumptions (number of turns/conv for LINGER and re-billed OpenAI history; chars/min of TTS)** → Instrument `SessionUsageUpdatedEvent` from day 1 and recalculate with real data; in OpenAI enable context truncation and keep history static to not break the cache
- **es-ES quality of voices not verifiable by specs (accent, naturalness)** → Homemade blind test of 10 phrases with the 3-4 finalist voices before fixing P1
- **`livekit-plugins-aws[realtime]` has installation friction/runtime errors (#3244, #3629)** → Treat as plan B; smoke-test on the day of the factory and discard without cost if it fails

## Open questions
- How many real turns does an average conversation have at home? (determines the weight of LINGER and re-billed history — measure in P0)
- Is OpenAI's input transcription billed (gpt-realtime-whisper $0.017/min) if `input_audio_transcription` is activated, or is it included in some tier? Verify in the first invoice
- Does AssemblyAI Universal-Streaming already support Spanish in streaming via LiveKit Inference? (would be the gate's STT at $0.0025/min, half of Deepgram)
- Can LiveKit's Turn Detector v1.0 native-audio be used as a DDSD signal during LINGER without active STT session in S2S mode?
- Cost of LiveKit Cloud participant minutes in ARMED 24/7 (outside the scope of this matrix; ROADMAP already considers room timeout or self-host in outages)

## Sources
- https://developers.openai.com/api/docs/pricing
- https://developers.openai.com/api/docs/guides/realtime-costs
- https://developers.openai.com/api/docs/models/gpt-realtime-mini
- https://ai.google.dev/gemini-api/docs/pricing
- https://docs.livekit.io/agents/models/realtime/plugins/gemini/
- https://docs.livekit.io/agents/integrations/realtime/nova-sonic/
- https://docs.livekit.io/agents/logic/tools/mcp/
- https://livekit.com/pricing/inference
- https://aws.amazon.com/nova/pricing/
- https://aws.amazon.com/blogs/aws/introducing-amazon-nova-2-sonic-next-generation-speech-to-speech-model-for-conversational-ai/
- https://docs.aws.amazon.com/nova/latest/nova2-userguide/sonic-language-support.html
- https://github.com/livekit/agents/issues/5684
- https://github.com/livekit/agents/issues/5768
- https://github.com/livekit/agents/issues/5808
- https://github.com/livekit/agents/issues/5906
- https://github.com/livekit/agents/issues/3244
- https://github.com/livekit/agents/issues/3629
- https://pypi.org/project/livekit-agents/
- https://pypi.org/project/livekit-plugins-aws/