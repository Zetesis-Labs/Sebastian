> **Anexo del informe de implementación** ([`IMPLEMENTATION.md`](../../IMPLEMENTATION.md)). Texto íntegro de la exploración multi-agente del 2026-07-02 (8 agentes en paralelo + contraste cruzado). Donde este anexo contradiga las **Decisiones congeladas** del informe principal, prevalece el informe.

# agent-modernization — Agente Python — migración a livekit-agents 1.6.4: AgentSession/Realtime, turn handling, MCP, dispatch explícito + token server, y lado agente del protocolo de invocación (IDLE/ARMED/ATTENDING/ENGAGED/LINGER)

**Veredicto:** viable — Todas las APIs necesarias existen y fueron verificadas contra el código fuente del tag livekit-agents@1.6.4 y docs oficiales (jul-2026): AgentServer+dispatch por token, set_audio_enabled como gate, RPC bidireccional, MCPToolset y StopResponse como hook DDSD. El único recorte honesto: adaptive interruption y preemptive_generation NO aplican al modelo Realtime S2S (solo pipeline), y el gate DDSD real exige apagar el VAD de servidor de OpenAI (fase P1).

**Esfuerzo:** M — 4-5 días-persona para el área agente: upgrade+reestructura agent.py (1,5 d), token server + dispatch + despliegue (1 d), protocolo state/wake/LINGER + MCP HA (1,5 d), pruebas end-to-end con la placa y ajuste de la ventana LINGER (1 d). El lado firmware (HTTP token fetch + handlers RPC + gate) es otra área: +2-3 d no incluidos.

## Hallazgos
- agent.py actual usa APIs 1.2 ya retiradas de los ejemplos oficiales: RoomInputOptions, WorkerOptions(entrypoint_fnc=...), y ctx.connect() manual tras session.start; graba WAV a /tmp incondicionalmente  
  _agent/agent.py:60-79_
- pyproject fija livekit-agents[openai]~=1.2; la última en PyPI es 1.6.4 (python >=3.10,<3.15 — compatible con el requires-python >=3.13 del proyecto)  
  _agent/pyproject.toml:8 + https://pypi.org/project/livekit-agents/_
- Forma actual de arrancar: AgentServer() + @server.rtc_session(agent_name=...) + cli.run_app(server) (Worker renombrado en 1.3.3, retrocompatible). session.start() conecta el JobContext internamente (task _job_ctx_connect): ctx.connect() explícito sobra  
  _https://github.com/livekit/agents/releases/tag/livekit-agents%401.3.3 y agent_session.py del tag 1.6.4 (async def start → job_ctx.connect())_
- Adaptive Interruption Handling, preemptive_generation y resume_false_interruption son SOLO pipeline STT-LLM-TTS: los docs exigen 'LLM is not a realtime model'. Con OpenAI Realtime la interrupción la gestiona su server VAD (interrupt_response=True). No prometer esas features en este stack S2S  
  _https://docs.livekit.io/agents/logic/turns/adaptive-interruption-handling/_
- Turn Detector v1.0 (inference.TurnDetector(), audio-nativo, soporta español) SÍ combina con un RealtimeModel sin STT paralelo, siempre que se desactive la detección del modelo (turn_detection=None). v1 corre en LiveKit Inference (Cloud); v1-mini en CPU local para self-host  
  _https://docs.livekit.io/agents/logic/turns/turn-detector/_
- openai.realtime.RealtimeModel en 1.6.4: default model='gpt-realtime', voz default 'marin', semantic_vad por defecto (create_response/interrupt_response/eagerness), temperature deprecado, update_options(turn_detection=...) en runtime, y say() soportado vía capabilities.supports_say  
  _livekit-plugins-openai/realtime/realtime_model.py del tag 1.6.4 (firma __init__ y update_options)_
- session.input.set_audio_enabled(False) es el gate oficial de escucha (patrón push_to_talk.py): corta el audio hacia el modelo sin cerrar la sesión Realtime — es la palanca de LINGER/ARMED en el lado agente  
  _https://github.com/livekit/agents/blob/livekit-agents%401.6.4/examples/voice_agents/push_to_talk.py y voice/io.py (AgentInput.set_audio_enabled)_
- StopResponse lanzado en Agent.on_user_turn_completed descarta el turno sin generar respuesta — hook perfecto para el gate DDSD de LINGER — pero solo gobierna cuando la detección de turno es del lado cliente; con semantic_vad de servidor la respuesta la crea OpenAI y no se puede vetar  
  _voice/agent_activity.py del tag 1.6.4 (_user_turn_completed: except StopResponse → return antes de _generate_reply)_
- MCP en 1.6.4: tools=[mcp.MCPToolset(id=..., mcp_server=mcp.MCPServerHTTP(url, transport_type='streamable_http', headers={...}, allowed_tools=[...]))]. Home Assistant expone su MCP en http://<ha>:8123/api/mcp (Streamable HTTP) con long-lived access token como Bearer  
  _livekit-agents/llm/mcp.py del tag 1.6.4 + https://www.home-assistant.io/integrations/mcp_server/_
- Dispatch explícito: @server.rtc_session(agent_name='sebastian') desactiva el auto-dispatch; RoomAgentDispatch dentro del AccessToken despacha al agente cuando el device crea la sala (se ignora si la sala ya existe) — elimina el workaround sala-fresca+reset. AgentDispatchService.create_dispatch queda para recuperación/ops  
  _https://docs.livekit.io/agents/worker/agent-dispatch/_
- SDK Python rtc verificado: perform_rpc(destination_identity, method, payload, response_timeout), register_rpc_method como decorador (RpcInvocationData: caller_identity, payload, límite 15KiB), publish_data(payload, reliable, topic), send_text. El C SDK vendorizado 0.3.10 ya expone el otro extremo: livekit_room_rpc_register/rpc_invoke y livekit_room_publish_data  
  _python-sdks/livekit-rtc/participant.py:256-476 + firmware/managed_components/livekit__livekit/include/livekit.h:423-454_
- Eventos de sesión reales: agent_state_changed (old_state/new_state; AgentState=initializing|idle|listening|thinking|speaking), user_state_changed (speaking|listening|away), user_input_transcribed (transcript,is_final,speaker_id), session_usage_updated ($/min; metrics_collected deprecado) — base directa del espejo state{} hacia el firmware  
  _voice/events.py del tag 1.6.4:300-320 + https://docs.livekit.io/agents/build/events/_
- El token embebido actual es de sandbox Cloud (iss API5JKjV7RkL9AE, identity esp32-respeaker, room sebastian, exp ~1 mes): sin rotación y sin RoomAgentDispatch — reflash para cualquier cambio  
  _firmware/main/secrets.zig:3_

## Diseño

# Modernización del agente a livekit-agents 1.6.4

## 1. Decisiones clave (verificadas contra el tag 1.6.4, no de memoria)
- **Estructura nueva**: `AgentServer()` + `@server.rtc_session(agent_name="sebastian")` + `cli.run_app(server)`. `WorkerOptions` sigue funcionando pero los ejemplos oficiales ya no lo usan. `await ctx.connect()` desaparece: `session.start()` conecta el JobContext internamente.
- **I/O de sala**: `RoomInputOptions` → `room_io.RoomOptions(participant_identity="esp32-respeaker", audio_input=room_io.AudioInputOptions(noise_cancellation=BVC()))`. `participant_identity` fija el RoomIO al device (nadie más alimenta al modelo). `close_on_disconnect=True` (default) cierra la sesión si el device cae — encaja con el ciclo de dispatch (§4).
- **Honestidad 1.6**: `interruption.mode="adaptive"`, `preemptive_generation` y `resume_false_interruption` son **solo pipeline STT-LLM-TTS** ("LLM is not a realtime model"). Con OpenAI Realtime S2S la interrupción la hace su server VAD (`interrupt_response=True`). Lo que SÍ aplica a Realtime: Turn Detector v1.0 audio-nativo (`inference.TurnDetector()`, español) si se apaga la detección del modelo.
- Modelo P0: `openai.realtime.RealtimeModel()` con `semantic_vad` de serie (defaults actuales: model `gpt-realtime`, voz `marin`). Medir $/min con `session_usage_updated` antes de tocar nada.

## 2. Turn handling por fases
- **P0 (ENGAGED simple)**: semantic_vad de servidor; OpenAI crea las respuestas. `on_user_turn_completed` NO puede vetarlas → LINGER = ventana temporal sin gate (como el follow-up de Alexa).
- **P1 (gate DDSD real)**: `RealtimeModel(turn_detection=None, input_audio_transcription=AudioTranscription(model="gpt-4o-transcribe"))` + `AgentSession(vad=silero.VAD.load(), turn_detection=inference.TurnDetector())`. Con detección en cliente el framework invoca `Sebastian.on_user_turn_completed(turn_ctx, new_message)`; lanzar `StopResponse` descarta el turno sin responder (verificado en `agent_activity.py::_user_turn_completed`). Ahí vive el gate DDSD: DoA estable (topic `doa`) + coherencia léxica (`user_input_transcribed`) + energía. STT paralelo (p.ej. Deepgram) solo si el transcript del Realtime llega tarde para el gate léxico.
- El modo (P0/P1) se elige por env var al construir; `session.update_options(turn_detection=...)` existe pero el VAD del modelo se fija al crearlo.

## 3. MCP — Home Assistant
- En el constructor del Agent: `tools=[mcp.MCPToolset(id="home-assistant", mcp_server=mcp.MCPServerHTTP("http://<ha>:8123/api/mcp", transport_type="streamable_http", headers={"Authorization": f"Bearer {HA_TOKEN}"}, allowed_tools=[...]))]`. (`mcp_servers=` sigue en la firma pero los docs empujan a `tools`.) HA: integración "MCP Server", auth por long-lived access token; `allowed_tools` para acotar superficie (luces/escenas sí, config no).

## 4. Dispatch explícito (mata el workaround sala-fresca+reset)
- `agent_name="sebastian"` en el decorador desactiva el auto-dispatch.
- **Recomendado: `RoomAgentDispatch` dentro del token del device** (`.with_room_config(RoomConfiguration(agents=[RoomAgentDispatch(agent_name="sebastian")]))`). El dispatch ocurre al CREARSE la sala → ciclo determinista: device conecta → sala "sebastian" se crea → agente entra; device cae → sesión se cierra → sala se vacía y muere → siguiente boot re-crea y re-despacha. La sala estable deja de ser un problema: su ciclo de vida es el del device.
- `AgentDispatchService.create_dispatch` queda como endpoint admin del token server para recuperación (si el agente muere con el device dentro, el token no re-despacha porque la sala ya existe).
- Ops: para probar sin placa, `lk dispatch create --agent-name sebastian --room test` (el playground ya no auto-invoca al agente).

## 5. Token server (renovación realista para ESP32)
- FastAPI + livekit-api (snippet 3), API key/secret del proyecto Cloud real por env (adiós token sandbox).
- **Recomendación: HTTP GET en boot**: el firmware hace `GET /token?device=esp32-respeaker` (header `X-Device-Secret`, secreto por dispositivo — solo da derecho a pedir tokens, rotable en servidor) antes de `livekit_room_connect(url, token)`. Respuesta `{url, token, ttl_s}`. **TTL 24 h**: LiveKit no expulsa al participante cuando el JWT caduca en vivo; solo hace falta token válido al (re)conectar, y el firmware repite el GET en cada ciclo de conexión. Cachear el último token en NVS como fallback si el token server está caído.
- Descartados: token de larga vida embebido (statu quo: reflash, sin rotación) y TTLs de minutos (el C SDK 0.3.10 no expone hook de token-refresh en caliente).
- Grants mínimos: `room_join, room="sebastian", can_publish, can_subscribe, can_publish_data`. Nada de `room_create/room_admin`.

## 6. Protocolo dispositivo↔agente (mapa de eventos)
| Canal | Dir | Nombre | Payload | Uso |
|---|---|---|---|---|
| RPC | dev→ag | `wake` | `{score,doa,ts}` | microWakeWord disparó (C: `livekit_room_rpc_invoke`) |
| RPC | ag→dev | `state` | `{state:listening\|thinking\|speaking\|idle}` | espejo de `agent_state_changed` → LEDs + gate del firmware |
| RPC | ag→dev | `announce` | `{chime:true}` | cortesía previa a proactividad |
| RPC | ag→dev | `set_volume` | `{level}` | whisper mode (P2) |
| data lossy | dev→ag | topic `doa` | `{az,rms}` ~5 Hz | telemetría para el gate DDSD (`ctx.room.on("data_received")`) |
- Python: `ctx.room.local_participant.register_rpc_method("wake")` (handler recibe `rtc.RpcInvocationData`) y `perform_rpc(destination_identity=..., method=..., payload=..., response_timeout=...)`; `publish_data(payload, reliable=, topic=)` si un broadcast basta. En el device ya existe el otro extremo: `livekit_room_rpc_register` (`livekit.h:446`).

## 7. LINGER — pausar/reanudar sin cerrar sesión
- Palanca real: `session.input.set_audio_enabled(bool)` (patrón oficial push_to_talk). NO cierra el WS con OpenAI: corta el flujo de audio → 0 tokens en ARMED; el plugin gestiona solo reconexión y `max_session_duration`.
- Ciclo: en `agent_state_changed` `speaking→listening` armar timer de 8 s; `user_state_changed→speaking` lo cancela (la conversación sigue); al expirar → `set_audio_enabled(False)` + RPC `state:idle` (el device vuelve a ARMED: silencio + wake word). RPC `wake` → `session.interrupt()` (barge-in si hablaba) + `set_audio_enabled(True)`.
- El gate DDSD (P1, §2) veta turnos dentro de LINGER con `StopResponse`; en P0 la ventana responde a todo durante 8 s — asumido y documentado.

## 8. Pasos sobre este repo
1. `agent/pyproject.toml`: `livekit-agents[openai]~=1.6.4`, `livekit-plugins-noise-cancellation~=0.2.6`; añadir `livekit-api`, `fastapi`, `uvicorn[standard]`; `uv sync`.
2. Reescribir `agent/agent.py` (snippets 1+2): AgentServer, RoomOptions, protocolo, LINGER; grabación WAV solo con `SEBASTIAN_DEBUG_REC=1`; quitar el saludo incondicional de arranque (con wake word no se saluda en cada dispatch).
3. Crear `agent/token_server.py` (snippet 3) + `.env` con `LIVEKIT_URL/API_KEY/API_SECRET` del proyecto Cloud real y `SEBASTIAN_DEVICE_SECRET`.
4. Interfaz con firmware (otra área): GET del token en boot con `esp_http_client`; registrar handlers RPC `state/announce/set_volume`; invocar `wake` desde microWakeWord.
5. Validar: `uv run agent.py console` (sin sala) → `dev`; `lk room list` debe mostrar agente+device en "sebastian" sin ningún `lk room delete`.
6. Instrumentar `session_usage_updated` y decidir modelo por $/min real (gpt-realtime vs mini vs Gemini) — queda desacoplado por env var.

## Código
**agent/agent.py** — Esqueleto modernizado a 1.6.4: AgentServer + dispatch explícito, RealtimeModel P0/P1, MCP de Home Assistant, gate cerrado al nacer (ARMED)

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

**agent/protocol.py** — Lado agente de la máquina de estados: espejo state{} por RPC, wake entrante, ventana LINGER con set_audio_enabled y proactividad con cortesía

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

**agent/token_server.py** — Token server FastAPI: token por dispositivo con grants mínimos, TTL 24h y el dispatch explícito embebido (RoomAgentDispatch) — el firmware hace GET en boot

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

## Riesgos
- **C SDK 0.3.10 en Developer Preview: la superficie RPC/data puede cambiar entre bumps y el flujo de token por HTTP añade código Zig nuevo en el boot** → Fijar la versión del componente; probar el arranque degradado (token server caído → token cacheado en NVS + reintentos); revisar changelog antes de cada bump
- **En P0 (semantic_vad de servidor) la ventana LINGER responde a CUALQUIER voz (TV, conversación cruzada): falsos positivos garantizados en entorno doméstico** → Ventana corta (5-8 s) y LED tenue que lo haga visible; priorizar el paso a P1 (turn_detection=None + inference.TurnDetector + StopResponse) donde el gate DDSD sí puede vetar
- **El dispatch por token solo actúa al crear la sala: si el proceso agente muere con el device conectado, no hay re-dispatch automático** → Endpoint admin en el token server que llama a AgentDispatchService.create_dispatch; watchdog barato (cron que compara participantes de la sala con `lk room participants`)
- **inference.TurnDetector v1 corre en LiveKit Inference (Cloud): dependencia de plan/cuota y de red; en self-host no está** → v1-mini corre local en CPU; dejar el selector de turn detector por config para el escenario self-host del roadmap (cortes)
- **Transcripciones del Realtime llegan tarde y sin parciales: el gate léxico DDSD y el speaker-ID pueden quedarse sin señal a tiempo** → Añadir STT paralelo (Deepgram/Speechmatics) solo si la medición lo justifica — coste extra por minuto; empezar con señales acústicas (DoA/energía)
- **Agente 24/7 en la sala + WS Realtime siempre abierto: minutos de participante en LiveKit Cloud y reconexiones OpenAI aunque el gasto de tokens en ARMED sea 0** → Medir con session_usage_updated + facturación Cloud; si duele, timeout de sala tras N min en ARMED (reconexión ~1-2 s) o self-host en cortes (ya previsto en ROADMAP P2)

## Preguntas abiertas
- ¿Existe proyecto LiveKit Cloud propio con API key/secret accesibles? El token actual es de sandbox (iss API5JKjV7RkL9AE, demo-ja18uvd7.livekit.cloud) y sin claves no hay token server ni dispatch explícito.
- ¿Dónde corre el token server y el worker 24/7: el Mac de desarrollo o ya directamente un Deployment en cortes (el GitOps de Mileto existe)? Afecta a cómo se expone el endpoint /token al ESP32 (LAN vs TLS público).
- ¿Se acepta el coste de un STT paralelo en P1 para el gate léxico y el speaker-ID, o el DDSD arranca solo con señales acústicas (DoA/energía)?
- ¿El device mantiene la sala LiveKit conectada 24/7 en ARMED o se decide ya el timeout de sala + reconexión al wake (economía de minutos Cloud)?
- ¿Fijar gpt-realtime o arrancar directamente con gpt-realtime-mini / Gemini native audio para abaratar? La medición con session_usage_updated decidirá, pero hay que elegir el default del .env.

## Fuentes
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