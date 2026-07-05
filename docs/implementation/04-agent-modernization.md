> **Implementation Report Annex** ([`IMPLEMENTATION.md`](../../IMPLEMENTATION.md)). Full text of the multi-agent exploration from 2026-07-02 (8 parallel agents + cross-checking). Where this annex contradicts the **Frozen Decisions** of the main report, the main report prevails.

# agent-modernization — Python Agent — migration to livekit-agents 1.6.4: AgentSession/Realtime, turn handling, MCP, explicit dispatch + token server, and agent-side invocation protocol (IDLE/ARMED/ATTENDING/ENGAGED/LINGER)

**Verdict:** viable — All necessary APIs exist and were verified against the source code of the livekit-agents@1.6.4 tag and official docs (Jul-2026): AgentServer+dispatch by token, set_audio_enabled as gate, bidirectional RPC, MCPToolset, and StopResponse as DDSD hook. The only honest compromise: adaptive interruption and preemptive_generation DO NOT apply to the Realtime S2S model (only pipeline), and the real DDSD gate requires turning off OpenAI's server VAD (P1 phase).

**Effort:** M — 4-5 person-days for the agent area: upgrade+restructure agent.py (1.5 d), token server + dispatch + deployment (1 d), state/wake/LINGER protocol + MCP HA (1.5 d), end-to-end testing with the board and LINGER window adjustment (1 d). The firmware side (HTTP token fetch + RPC handlers + gate) is another area: +2-3 d not included.

## Findings
- Current agent.py uses 1.2 APIs already removed from official examples: RoomInputOptions, WorkerOptions(entrypoint_fnc=...), and manual ctx.connect() after session.start; it records WAV to /tmp unconditionally  
  _agent/agent.py:60-79_
- pyproject pins livekit-agents[openai]~=1.2; the latest on PyPI is 1.6.4 (python >=3.10,<3.15 — compatible with the project's requires-python >=3.13)  
  _agent/pyproject.toml:8 + https://pypi.org/project/livekit-agents/_
- Current startup way: AgentServer() + @server.rtc_session(agent_name=...) + cli.run_app(server) (Worker renamed in 1.3.3, backwards compatible). session.start() connects the JobContext internally (task _job_ctx_connect): explicit ctx.connect() is superfluous  
  _https://github.com/livekit/agents/releases/tag/livekit-agents%401.3.3 and agent_session.py from tag 1.6.4 (async def start → job_ctx.connect())_
- Adaptive Interruption Handling, preemptive_generation, and resume_false_interruption are ONLY STT-LLM-TTS pipeline: the docs require 'LLM is not a realtime model'. With OpenAI Realtime the interruption is handled by its server VAD (interrupt_response=True). Do not promise these features in this S2S stack  
  _https://docs.livekit.io/agents/logic/turns/adaptive-interruption-handling/_
- Turn Detector v1.0 (inference.TurnDetector(), audio-native, supports Spanish) DOES combine with a RealtimeModel without parallel STT, provided that the model's detection is disabled (turn_detection=None). v1 runs on LiveKit Inference (Cloud); v1-mini on local CPU for self-host  
  _https://docs.livekit.io/agents/logic/turns/turn-detector/_
- openai.realtime.RealtimeModel in 1.6.4: default model='gpt-realtime', default voice 'marin', semantic_vad by default (create_response/interrupt_response/eagerness), temperature deprecated, update_options(turn_detection=...) at runtime, and say() supported via capabilities.supports_say  
  _livekit-plugins-openai/realtime/realtime_model.py from tag 1.6.4 (__init__ signature and update_options)_
- session.input.set_audio_enabled(False) is the official listening gate (push_to_talk.py pattern): cuts audio to the model without closing the Realtime session — it is the LINGER/ARMED lever on the agent side  
  _https://github.com/livekit/agents/blob/livekit-agents%401.6.4/examples/voice_agents/push_to_talk.py and voice/io.py (AgentInput.set_audio_enabled)_
- StopResponse thrown in Agent.on_user_turn_completed discards the turn without generating a response — perfect hook for the LINGER DDSD gate — but only governs when turn detection is on the client side; with server semantic_vad the response is created by OpenAI and cannot be vetoed  
  _voice/agent_activity.py from tag 1.6.4 (_user_turn_completed: except StopResponse → return before _generate_reply)_
- MCP in 1.6.4: tools=[mcp.MCPToolset(id=..., mcp_server=mcp.MCPServerHTTP(url, transport_type='streamable_http', headers={...}, allowed_tools=[...]))]. Home Assistant exposes its MCP at http://<ha>:8123/api/mcp (Streamable HTTP) with long-lived access token as Bearer  
  _livekit-agents/llm/mcp.py from tag 1.6.4 + https://www.home-assistant.io/integrations/mcp_server/_
- Explicit dispatch: @server.rtc_session(agent_name='sebastian') disables auto-dispatch; RoomAgentDispatch inside the AccessToken dispatches the agent when the device creates the room (ignored if the room already exists) — eliminates the fresh-room+reset workaround. AgentDispatchService.create_dispatch remains for recovery/ops  
  _https://docs.livekit.io/agents/worker/agent-dispatch/_
- Python rtc SDK verified: perform_rpc(destination_identity, method, payload, response_timeout), register_rpc_method as decorator (RpcInvocationData: caller_identity, payload, 15KiB limit), publish_data(payload, reliable, topic), send_text. The vendored C SDK 0.3.10 already exposes the other end: livekit_room_rpc_register/rpc_invoke and livekit_room_publish_data  
  _python-sdks/livekit-rtc/participant.py:256-476 + firmware/managed_components/livekit__livekit/include/livekit.h:423-454_
- Real session events: agent_state_changed (old_state/new_state; AgentState=initializing|idle|listening|thinking|speaking), user_state_changed (speaking|listening|away), user_input_transcribed (transcript,is_final,speaker_id), session_usage_updated ($/min; metrics_collected deprecated) — direct basis of the state{} mirror to the firmware  
  _voice/events.py from tag 1.6.4:300-320 + https://docs.livekit.io/agents/build/events/_
- The current embedded token is from the Cloud sandbox (iss API5JKjV7RkL9AE, identity esp32-respeaker, room sebastian, exp ~1 month): no rotation and no RoomAgentDispatch — reflash for any change  
  _firmware/main/secrets.zig:3_

## Design

# Agent modernization to livekit-agents 1.6.4

## 1. Key decisions (verified against tag 1.6.4, not from memory)
- **New structure**: `AgentServer()` + `@server.rtc_session(agent_name="sebastian")` + `cli.run_app(server)`. `WorkerOptions` still works but official examples no longer use it. `await ctx.connect()` disappears: `session.start()` connects the JobContext internally.
- **Room I/O**: `RoomInputOptions` → `room_io.RoomOptions(participant_identity="esp32-respeaker", audio_input=room_io.AudioInputOptions(noise_cancellation=BVC()))`. `participant_identity` binds the RoomIO to the device (no one else feeds the model). `close_on_disconnect=True` (default) closes the session if the device drops — fits the dispatch cycle (§4).
- **1.6 Honesty**: `interruption.mode="adaptive"`, `preemptive_generation` and `resume_false_interruption` are **only STT-LLM-TTS pipeline** ("LLM is not a realtime model"). With OpenAI Realtime S2S, interruption is done by its server VAD (`interrupt_response=True`). What DOES apply to Realtime: audio-native Turn Detector v1.0 (`inference.TurnDetector()`, Spanish) if the model's detection is turned off.
- P0 Model: `openai.realtime.RealtimeModel()` with standard `semantic_vad` (current defaults: model `gpt-realtime`, voice `marin`). Measure $/min with `session_usage_updated` before touching anything.

## 2. Turn handling by phases
- **P0 (simple ENGAGED)**: server semantic_vad; OpenAI creates the responses. `on_user_turn_completed` CANNOT veto them → LINGER = time window without gate (like Alexa's follow-up).
- **P1 (real DDSD gate)**: `RealtimeModel(turn_detection=None, input_audio_transcription=AudioTranscription(model="gpt-4o-transcribe"))` + `AgentSession(vad=silero.VAD.load(), turn_detection=inference.TurnDetector())`. With client-side detection the framework invokes `Sebastian.on_user_turn_completed(turn_ctx, new_message)`; raising `StopResponse` discards the turn without responding (verified in `agent_activity.py::_user_turn_completed`). The DDSD gate lives there: stable DoA (topic `doa`) + lexical coherence (`user_input_transcribed`) + energy. Parallel STT (e.g., Deepgram) only if the Realtime transcript arrives late for the lexical gate.
- The mode (P0/P1) is chosen by env var at build time; `session.update_options(turn_detection=...)` exists but the model's VAD is fixed at creation.

## 3. MCP — Home Assistant
- In the Agent's constructor: `tools=[mcp.MCPToolset(id="home-assistant", mcp_server=mcp.MCPServerHTTP("http://<ha>:8123/api/mcp", transport_type="streamable_http", headers={"Authorization": f"Bearer {HA_TOKEN}"}, allowed_tools=[...]))]`. (`mcp_servers=` remains in the signature but docs push to `tools`.) HA: "MCP Server" integration, auth via long-lived access token; `allowed_tools` to narrow surface (lights/scenes yes, config no).

## 4. Explicit dispatch (kills the fresh-room+reset workaround)
- `agent_name="sebastian"` in the decorator disables auto-dispatch.
- **Recommended: `RoomAgentDispatch` inside the device token** (`.with_room_config(RoomConfiguration(agents=[RoomAgentDispatch(agent_name="sebastian")]))`). Dispatch occurs when the room is CREATED → deterministic cycle: device connects → "sebastian" room is created → agent enters; device drops → session closes → room empties and dies → next boot re-creates and re-dispatches. The stable room stops being a problem: its lifecycle is that of the device.
- `AgentDispatchService.create_dispatch` remains as an admin endpoint of the token server for recovery (if the agent dies with the device inside, the token doesn't re-dispatch because the room already exists).
- Ops: to test without board, `lk dispatch create --agent-name sebastian --room test` (the playground no longer auto-invokes the agent).

## 5. Token server (realistic renewal for ESP32)
- FastAPI + livekit-api (snippet 3), project's real Cloud API key/secret via env (goodbye sandbox token).
- **Recommendation: HTTP GET on boot**: the firmware does `GET /token?device=esp32-respeaker` (header `X-Device-Secret`, per-device secret — only gives right to request tokens, rotatable on server) before `livekit_room_connect(url, token)`. Response `{url, token, ttl_s}`. **TTL 24 h**: LiveKit does not kick the participant out when the JWT expires live; a valid token is only needed when (re)connecting, and the firmware repeats the GET on each connection cycle. Cache the latest token in NVS as a fallback if the token server is down.
- Discarded: embedded long-lived token (status quo: reflash, no rotation) and minute-long TTLs (the C SDK 0.3.10 does not expose a hot token-refresh hook).
- Minimum grants: `room_join, room="sebastian", can_publish, can_subscribe, can_publish_data`. No `room_create/room_admin`.

## 6. Device↔agent protocol (event map)
| Channel | Dir | Name | Payload | Usage |
|---|---|---|---|---|
| RPC | dev→ag | `wake` | `{score,doa,ts}` | microWakeWord triggered (C: `livekit_room_rpc_invoke`) |
| RPC | ag→dev | `state` | `{state:listening\|thinking\|speaking\|idle}` | mirror of `agent_state_changed` → LEDs + firmware gate |
| RPC | ag→dev | `announce` | `{chime:true}` | courtesy prior to proactivity |
| RPC | ag→dev | `set_volume` | `{level}` | whisper mode (P2) |
| data lossy | dev→ag | topic `doa` | `{az,rms}` ~5 Hz | telemetry for the DDSD gate (`ctx.room.on("data_received")`) |
- Python: `ctx.room.local_participant.register_rpc_method("wake")` (handler receives `rtc.RpcInvocationData`) and `perform_rpc(destination_identity=..., method=..., payload=..., response_timeout=...)`; `publish_data(payload, reliable=, topic=)` if a broadcast is enough. On the device the other end already exists: `livekit_room_rpc_register` (`livekit.h:446`).

## 7. LINGER — pause/resume without closing session
- Real lever: `session.input.set_audio_enabled(bool)` (official push_to_talk pattern). DOES NOT close the WS with OpenAI: cuts the audio stream → 0 tokens in ARMED; the plugin only manages reconnection and `max_session_duration`.
- Cycle: on `agent_state_changed` `speaking→listening` arm an 8 s timer; `user_state_changed→speaking` cancels it (the conversation continues); upon expiration → `set_audio_enabled(False)` + RPC `state:idle` (the device returns to ARMED: silence + wake word). RPC `wake` → `session.interrupt()` (barge-in if speaking) + `set_audio_enabled(True)`.
- The DDSD gate (P1, §2) vetoes turns within LINGER with `StopResponse`; in P0 the window responds to everything for 8 s — assumed and documented.

## 8. Steps on this repo
1. `agent/pyproject.toml`: `livekit-agents[openai]~=1.6.4`, `livekit-plugins-noise-cancellation~=0.2.6`; add `livekit-api`, `fastapi`, `uvicorn[standard]`; `uv sync`.
2. Rewrite `agent/agent.py` (snippets 1+2): AgentServer, RoomOptions, protocol, LINGER; WAV recording only with `SEBASTIAN_DEBUG_REC=1`; remove the unconditional startup greeting (with wake word there is no greeting on every dispatch).
3. Create `agent/token_server.py` (snippet 3) + `.env` with `LIVEKIT_URL/API_KEY/API_SECRET` from the real Cloud project and `SEBASTIAN_DEVICE_SECRET`.
4. Interface with firmware (another area): GET of the token on boot with `esp_http_client`; register RPC handlers `state/announce/set_volume`; invoke `wake` from microWakeWord.
5. Validate: `uv run agent.py console` (without room) → `dev`; `lk room list` should show agent+device in "sebastian" without any `lk room delete`.
6. Instrument `session_usage_updated` and decide model based on real $/min (gpt-realtime vs mini vs Gemini) — left decoupled by env var.

## Code
**agent/agent.py** — Skeleton modernized to 1.6.4: AgentServer + explicit dispatch, RealtimeModel P0/P1, Home Assistant MCP, gate closed at birth (ARMED)

```python
"""Sebastian — livekit-agents 1.6.4 (AgentServer + dispatch explícito)."""
import os
from dotenv import load_dotenv
from livekit.agents import (Agent, AgentServer, AgentSession, JobContext,
                            cli, mcp, room_io)
from livekit.agents.llm import ChatContext, ChatMessage, StopResponse
from livekit.plugins import noise_cancellation, openai
from protocol import wire_device_protocol  # snippet 2

load_dotenv()
DEVICE = os.getenv("SEBASTIAN_DEVICE_IDENTITY", "esp32-respeaker")


class Sebastian(Agent):
    def __init__(self) -> None:
        super().__init__(
            instructions=("Eres Sebastián, un asistente de voz que vive en un "
                          "altavoz. Español, natural y breve; sin markdown ni "
                          "listas: solo se te escucha."),
            tools=[mcp.MCPToolset(
                id="home-assistant",
                mcp_server=mcp.MCPServerHTTP(
                    os.environ["HA_MCP_URL"],  # http://<ha>:8123/api/mcp
                    transport_type="streamable_http",
                    headers={"Authorization": f"Bearer {os.environ['HA_TOKEN']}"},
                    # allowed_tools=[...]  # acotar superficie de HA
                ),
            )],
        )

    async def on_user_turn_completed(self, turn_ctx: ChatContext,
                                     new_message: ChatMessage) -> None:
        # Hook del gate DDSD (P1). SOLO gobierna con turn detection en cliente:
        #   RealtimeModel(turn_detection=None) + AgentSession(turn_detection=
        #   inference.TurnDetector(), vad=silero.VAD.load())
        # TODO P1: if in_linger and not ddsd.addressed_to_me(new_message, doa):
        #     raise StopResponse()  # descarta el turno sin responder
        pass


server = AgentServer()


@server.rtc_session(agent_name="sebastian")
async def entrypoint(ctx: JobContext) -> None:
    ctx.log_context_fields = {"room": ctx.room.name}
    session = AgentSession(
        # P0: semantic_vad de servidor (default del plugin, model gpt-realtime)
        llm=openai.realtime.RealtimeModel(voice="marin"),
        user_away_timeout=15.0,
        # TODO medir coste: @session.on("session_usage_updated")
    )
    wire_device_protocol(ctx, session, DEVICE)
    await session.start(
        agent=Sebastian(), room=ctx.room,
        room_options=room_io.RoomOptions(
            participant_identity=DEVICE,  # solo el ESP32 alimenta al modelo
            audio_input=room_io.AudioInputOptions(
                noise_cancellation=noise_cancellation.BVC()),  # única pasada NS
        ),
    )
    session.input.set_audio_enabled(False)  # nace en ARMED: gate cerrado
    # NOTA: ctx.connect() ya no se llama — session.start() conecta solo.


if __name__ == "__main__":
    cli.run_app(server)
```

**agent/protocol.py** — Agent side of the state machine: state{} mirror via RPC, incoming wake, LINGER window with set_audio_enabled, and proactivity with courtesy

```python
import asyncio, json
from livekit import rtc
from livekit.agents import (AgentSession, AgentStateChangedEvent, JobContext,
                            UserStateChangedEvent)

LINGER_S = 8.0  # ventana follow-up sin wake word


def wire_device_protocol(ctx: JobContext, session: AgentSession, device: str) -> None:
    loop = asyncio.get_running_loop()
    linger: asyncio.TimerHandle | None = None

    async def send_state(state: str) -> None:  # → LEDs + gate del firmware
        try:
            await ctx.room.local_participant.perform_rpc(
                destination_identity=device, method="state",
                payload=json.dumps({"state": state}), response_timeout=2.0)
        except rtc.RpcError:
            pass  # device reconectando; se re-publica en el siguiente cambio

    def cancel_linger() -> None:
        nonlocal linger
        if linger is not None:
            linger.cancel(); linger = None

    def close_gate() -> None:  # fin de LINGER → ARMED
        session.input.set_audio_enabled(False)
        asyncio.create_task(send_state("idle"))

    @session.on("agent_state_changed")
    def _agent_state(ev: AgentStateChangedEvent) -> None:
        nonlocal linger
        asyncio.create_task(send_state(ev.new_state))  # listening|thinking|speaking|idle
        cancel_linger()
        if ev.old_state == "speaking" and ev.new_state == "listening":
            linger = loop.call_later(LINGER_S, close_gate)

    @session.on("user_state_changed")
    def _user_state(ev: UserStateChangedEvent) -> None:
        if ev.new_state == "speaking":
            cancel_linger()  # el usuario siguió hablando dentro de LINGER

    @ctx.room.local_participant.register_rpc_method("wake")
    async def _wake(data: rtc.RpcInvocationData) -> str:
        evt = json.loads(data.payload)  # {"score":0.93,"doa":120,"ts":...}
        cancel_linger()
        session.interrupt()                    # wake durante TTS = barge-in
        session.input.set_audio_enabled(True)  # ATTENDING
        # TODO 2ª etapa: re-verificar pre-roll antes de aceptar el turno
        await send_state("listening")
        return json.dumps({"ok": True})

    @ctx.room.on("data_received")
    def _data(pkt: rtc.DataPacket) -> None:
        if pkt.topic == "doa":
            pass  # TODO P1: alimentar el gate DDSD (DoA estable + léxico + energía)


async def announce(ctx: JobContext, session: AgentSession, device: str, text: str) -> None:
    """Proactividad con cortesía: chime+LED en el device, después la voz."""
    await ctx.room.local_participant.perform_rpc(
        destination_identity=device, method="announce",
        payload=json.dumps({"chime": True}), response_timeout=3.0)
    session.input.set_audio_enabled(True)  # abrir por si hay réplica
    session.say(text)  # OpenAI Realtime soporta say (capabilities.supports_say)
```

**agent/token_server.py** — FastAPI token server: per-device token with minimum grants, 24h TTL, and embedded explicit dispatch (RoomAgentDispatch) — the firmware does a GET on boot

```python
"""GET /token?device=esp32-<unit> (header X-Device-Secret) -> {url, token, ttl_s}.
El RoomAgentDispatch del token despacha al agente 'sebastian' al crearse la sala."""
import datetime as dt, hmac, os
from fastapi import FastAPI, Header, HTTPException
from livekit.api import (AccessToken, RoomAgentDispatch, RoomConfiguration,
                         VideoGrants)

app = FastAPI()
ROOM = "sebastian"
TTL = dt.timedelta(hours=24)  # solo debe ser válido al (re)conectar
DEVICE_SECRETS = {"esp32-respeaker": os.environ["SEBASTIAN_DEVICE_SECRET"]}


@app.get("/token")
def issue_token(device: str, x_device_secret: str = Header(...)) -> dict:
    secret = DEVICE_SECRETS.get(device)
    if not secret or not hmac.compare_digest(secret, x_device_secret):
        raise HTTPException(status_code=401)
    token = (
        AccessToken()  # LIVEKIT_API_KEY / LIVEKIT_API_SECRET del entorno
        .with_identity(device)
        .with_name(device)
        .with_ttl(TTL)
        .with_grants(VideoGrants(
            room_join=True, room=ROOM,
            can_publish=True, can_subscribe=True, can_publish_data=True,
        ))
        .with_room_config(RoomConfiguration(
            agents=[RoomAgentDispatch(agent_name="sebastian")],
        ))
        .to_jwt()
    )
    return {"url": os.environ["LIVEKIT_URL"], "token": token,
            "ttl_s": int(TTL.total_seconds())}

# TODO endpoint admin (auth aparte): re-dispatch de recuperación vía
# livekit.api.LiveKitAPI().agent_dispatch.create_dispatch(
#     api.CreateAgentDispatchRequest(agent_name="sebastian", room=ROOM))
```

## Risks
- **C SDK 0.3.10 in Developer Preview: the RPC/data surface can change between bumps and the HTTP token flow adds new Zig code on boot** → Pin the component version; test degraded startup (token server down → cached token in NVS + retries); review changelog before each bump
- **In P0 (server semantic_vad) the LINGER window responds to ANY voice (TV, cross-conversation): guaranteed false positives in a domestic environment** → Short window (5-8 s) and dim LED to make it visible; prioritize the move to P1 (turn_detection=None + inference.TurnDetector + StopResponse) where the DDSD gate can indeed veto
- **Dispatch by token only acts when creating the room: if the agent process dies with the device connected, there is no automatic re-dispatch** → Admin endpoint on the token server calling AgentDispatchService.create_dispatch; cheap watchdog (cron comparing room participants with `lk room participants`)
- **inference.TurnDetector v1 runs on LiveKit Inference (Cloud): dependency on plan/quota and network; in self-host it's not there** → v1-mini runs locally on CPU; leave the turn detector selector by config for the self-host roadmap scenario (outages)
- **Realtime transcripts arrive late and without partials: the DDSD lexical gate and speaker-ID may lack signal in time** → Add parallel STT (Deepgram/Speechmatics) only if measurement justifies it — extra cost per minute; start with acoustic signals (DoA/energy)
- **24/7 Agent in the room + Realtime WS always open: participant minutes in LiveKit Cloud and OpenAI reconnections even if token spend in ARMED is 0** → Measure with session_usage_updated + Cloud billing; if it hurts, room timeout after N min in ARMED (reconnection ~1-2 s) or self-host during outages (already planned in P2 ROADMAP)

## Open questions
- Is there a custom LiveKit Cloud project with accessible API key/secret? The current token is from the sandbox (iss API5JKjV7RkL9AE, demo-ja18uvd7.livekit.cloud) and without keys there is no token server or explicit dispatch.
- Where does the token server and the 24/7 worker run: the dev Mac or directly a Deployment on Miletus (the Mileto GitOps exists)? Affects how the /token endpoint is exposed to the ESP32 (LAN vs public TLS).
- Is the cost of a parallel STT in P1 accepted for the lexical gate and speaker-ID, or does DDSD start only with acoustic signals (DoA/energy)?
- Does the device keep the LiveKit room connected 24/7 in ARMED or is the room timeout + reconnection on wake already decided (Cloud minutes economy)?
- Pin gpt-realtime or start directly with gpt-realtime-mini / Gemini native audio to reduce costs? Measurement with session_usage_updated will decide, but the default in the .env must be chosen.

## Sources
- https://github.com/livekit/agents/releases
- https://github.com/livekit/agents/releases/tag/livekit-agents%401.3.3
- https://github.com/livekit/agents/releases/tag/livekit-agents%401.6.0
- https://github.com/livekit/agents/releases/tag/livekit-agents%401.6.1
- https://docs.livekit.io/agents/logic/turns/adaptive-interruption-handling/
- https://docs.livekit.io/agents/logic/turns/turn-detector/
- https://docs.livekit.io/agents/build/turns/
- https://docs.livekit.io/agents/build/sessions/
- https://docs.livekit.io/agents/build/events/
- https://docs.livekit.io/agents/models/realtime/
- https://docs.livekit.io/agents/models/realtime/plugins/openai/
- https://docs.livekit.io/agents/logic/tools/mcp/
- https://docs.livekit.io/agents/worker/agent-dispatch/
- https://docs.livekit.io/agents/server/options/
- https://docs.livekit.io/home/client/data/rpc/
- https://github.com/livekit/agents/blob/livekit-agents%401.6.4/examples/voice_agents/basic_agent.py
- https://github.com/livekit/agents/blob/livekit-agents%401.6.4/examples/voice_agents/push_to_talk.py
- https://github.com/livekit/agents/blob/livekit-agents%401.6.4/examples/voice_agents/realtime_turn_detector.py
- https://github.com/livekit/agents/blob/livekit-agents%401.6.4/livekit-agents/livekit/agents/voice/agent_session.py
- https://github.com/livekit/agents/blob/livekit-agents%401.6.4/livekit-agents/livekit/agents/voice/turn.py
- https://github.com/livekit/agents/blob/livekit-agents%401.6.4/livekit-agents/livekit/agents/voice/agent_activity.py
- https://github.com/livekit/agents/blob/livekit-agents%401.6.4/livekit-agents/livekit/agents/llm/mcp.py
- https://github.com/livekit/agents/blob/livekit-agents%401.6.4/livekit-plugins/livekit-plugins-openai/livekit/plugins/openai/realtime/realtime_model.py
- https://github.com/livekit/python-sdks/blob/main/livekit-rtc/livekit/rtc/participant.py
- https://github.com/livekit/python-sdks/blob/main/livekit-api/livekit/api/access_token.py
- https://www.home-assistant.io/integrations/mcp_server/
- https://pypi.org/project/livekit-agents/
- https://pypi.org/project/livekit-plugins-noise-cancellation/