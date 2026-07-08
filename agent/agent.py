"""Sebastian voice agent.

Joins the same LiveKit room as the ESP32-S3 device and drives the conversation
with a speech-to-speech realtime model. The ReSpeaker XVF3800 already does
beamforming + AEC on-device, so we add background-voice cancellation on top.

Turn-taking is handled server-side by the selected RealtimeModel. LiveKit's own
turn detector does not apply to a RealtimeModel (it would be ignored), so the
session is told turn detection lives in the model.

Run with:
    uv run agent.py dev        # local dev, hot-reload
    uv run agent.py start      # production worker

Set SEBASTIAN_RECORD=1 to dump the incoming mic track to /tmp for debugging.
"""

import asyncio
import contextlib
import logging
import os
from pathlib import Path

import aiohttp

from dotenv import load_dotenv
from livekit import agents, api, rtc
from livekit.agents import (
    Agent,
    AgentSession,
    RunContext,
    function_tool,
    get_job_context,
    mcp,
)
from livekit.agents.voice.agent_session import TurnHandlingOptions
from livekit.agents.voice.room_io import RoomOptions
from livekit.plugins import google, openai
from openai.types.beta.realtime.session import TurnDetection

import telemetry
from audio_input import SebastianAudioInput, setup_recorder, RECORD
from instrumentation import instrument_session
from tasks import spawn as _spawn
from typing import Any

load_dotenv(Path(__file__).with_name(".env"))
telemetry.setup()

log = logging.getLogger("sebastian.agent")


m_jobs = telemetry.counter(
    "sebastian_agent_jobs_total", "Jobs accepted (one per device session)"
)
m_barge = telemetry.counter(
    "sebastian_agent_barge_ins_total", "Wake-word interrupts while speaking"
)

BARGE_TOPIC = "sebastian.barge_in"
DEVICE_IDENTITY = os.getenv("SEBASTIAN_DEVICE_IDENTITY", "esp32-respeaker")
MODEL_PROVIDER = os.getenv("SEBASTIAN_MODEL_PROVIDER", "gemini").strip().lower()
GEMINI_MODEL = os.getenv(
    "SEBASTIAN_GEMINI_MODEL",
    "gemini-2.5-flash-native-audio-preview-12-2025",
)
GEMINI_VOICE = os.getenv("SEBASTIAN_GEMINI_VOICE", os.getenv("SEBASTIAN_VOICE", "Puck"))
GEMINI_LANGUAGE = os.getenv("SEBASTIAN_GEMINI_LANGUAGE", "").strip()
OPENAI_REALTIME_MODEL = os.getenv(
    "SEBASTIAN_OPENAI_REALTIME_MODEL", "gpt-realtime-mini"
)
OPENAI_VOICE = os.getenv(
    "SEBASTIAN_OPENAI_VOICE", os.getenv("SEBASTIAN_VOICE", "alloy")
)

HA_MCP_URL = os.getenv("SEBASTIAN_HA_MCP_URL", "").strip()
HA_TOKEN = os.getenv("SEBASTIAN_HA_TOKEN")


def _ha_mcp_servers() -> list:
    if not HA_TOKEN or not HA_MCP_URL:
        log.warning(
            "[mcp] SEBASTIAN_HA_TOKEN/SEBASTIAN_HA_MCP_URL not set — Home Assistant control disabled"
        )
        return []
    log.info("[mcp] Home Assistant MCP enabled: %s", HA_MCP_URL)
    return [
        mcp.MCPServerHTTP(
            url=HA_MCP_URL,
            transport_type="sse",
            headers={"Authorization": f"Bearer {HA_TOKEN}"},
            timeout=10,
        )
    ]


GOODBYE_GRACE_S = 3.0


async def _delete_room_after_goodbye(job_ctx: agents.JobContext) -> None:
    await asyncio.sleep(GOODBYE_GRACE_S)
    try:
        await job_ctx.api.room.delete_room(
            api.DeleteRoomRequest(room=job_ctx.room.name)
        )
        log.info("session ended by user request — room deleted")
    except aiohttp.ServerDisconnectedError:
        log.info("session ended by user request — room deleted")
    except Exception as e:
        log.warning("end_session: delete_room failed: %r", e)


class Sebastian(Agent):
    def __init__(self) -> None:
        super().__init__(
            instructions=(
                "Eres Sebastián, un asistente de voz que vive en un altavoz "
                "ESP32-S3 con un array de 4 micrófonos. Hablas en español, de "
                "forma natural y breve, como en una conversación. No uses "
                "markdown ni listas: solo se te va a escuchar, no a leer. "
                "Puedes controlar la casa (luces, enchufes, sensores…) con las "
                "herramientas de Home Assistant: cuando te pidan encender o apagar "
                "algo, úsalas de verdad y confirma en una frase corta lo que hiciste. "
                "Si una herramienta falla o no encuentras el dispositivo, dilo con "
                "naturalidad en vez de inventarte que lo hiciste. "
                "Para CUALQUIER pregunta sobre el estado de la casa (luces, "
                "persianas, sensores, temperatura…) consulta primero GetLiveContext "
                "y responde solo con esos datos. No puedes ver: nunca describas el "
                "aspecto físico de la casa ni inventes lo que no te dé una herramienta. "
                "Si te interrumpen mientras hablas o te piden parar ('para', "
                "'cállate', 'espera', 'ya vale'), deja de hablar INMEDIATAMENTE y "
                "no digas absolutamente nada: ni 'vale', ni 'adiós'. Te quedas "
                "escuchando en silencio; la conversación sigue abierta. "
                "Solo cuando el usuario se despida explícitamente ('adiós', 'hasta "
                "luego', 'nada más, gracias') llama a end_session y despídete con "
                "una sola palabra. Parar no es despedirse: ante la duda, calla y "
                "espera. "
                "Nunca pronuncies tu propio nombre, Sebastián: el altavoz lo "
                "interpreta como una orden de interrupción y te cortaría a ti mismo."
            )
        )

    @function_tool
    async def end_session(self, context: RunContext) -> str:
        """Termina la sesión de voz por completo. Llámala SOLO cuando el usuario
        se despida explícitamente ('adiós', 'hasta luego', 'nada más') o dé la
        conversación por terminada. NO la llames cuando pida parar o callar
        ('para', 'cállate'): eso solo interrumpe el habla, la sesión sigue.
        Para volver a hablar tras cerrarla hará falta la palabra de activación."""
        _ = context
        _spawn(_delete_room_after_goodbye(get_job_context()))
        log.info("end_session tool called — closing room in %.1fs", GOODBYE_GRACE_S)
        return "Sesión terminándose. Despídete con una sola palabra."


def _build_gemini_realtime_model() -> google.realtime.RealtimeModel:
    log.info(
        "[model] provider=gemini model=%s voice=%s language=%s",
        GEMINI_MODEL,
        GEMINI_VOICE,
        GEMINI_LANGUAGE or "<auto>",
    )
    kwargs: dict[str, Any] = dict(
        model=GEMINI_MODEL,
        voice=GEMINI_VOICE,
        proactivity=True,
        enable_affective_dialog=True,
    )
    if GEMINI_LANGUAGE:
        kwargs["language"] = GEMINI_LANGUAGE
    return google.realtime.RealtimeModel(**kwargs)


def _build_openai_realtime_model() -> openai.realtime.RealtimeModel:
    log.info("[model] provider=openai model=%s voice=%s", OPENAI_REALTIME_MODEL, OPENAI_VOICE)
    return openai.realtime.RealtimeModel(
        model=OPENAI_REALTIME_MODEL,
        voice=OPENAI_VOICE,
        turn_detection=TurnDetection(type="semantic_vad", eagerness="low"),
    )


def _build_realtime_model() -> Any:
    if MODEL_PROVIDER == "gemini":
        return _build_gemini_realtime_model()
    if MODEL_PROVIDER == "openai":
        return _build_openai_realtime_model()
    raise ValueError(
        "SEBASTIAN_MODEL_PROVIDER must be 'gemini' or 'openai' "
        f"(got {MODEL_PROVIDER!r})"
    )


def _build_session() -> AgentSession:
    realtime = _build_realtime_model()
    return AgentSession(
        llm=realtime,
        turn_handling=TurnHandlingOptions(turn_detection="realtime_llm"),
        mcp_servers=_ha_mcp_servers(),
        # Default 3.0s exists for the framework's SOFTWARE AEC to warm up; our
        # echo path is hardware (XVF AEC + comms-channel residual suppressor,
        # always on), so those 3s only delay barge-in: interruptions were dead
        # for the first 3s of every reply ("cutting it off takes forever").
        aec_warmup_duration=0.0,
    )


def _setup_barge_in(ctx: agents.JobContext, session: AgentSession) -> None:
    @ctx.room.on("data_received")
    def _on_data(packet: rtc.DataPacket) -> None:
        if packet.topic != BARGE_TOPIC:
            return
        m_barge.add(1)
        log.info("barge-in from device — interrupting")
        try:
            session.interrupt()
        except Exception as e:
            log.warning("barge-in interrupt failed: %r", e)


async def entrypoint(ctx: agents.JobContext) -> None:
    m_jobs.add(1)
    log.info("job accepted room=%s", ctx.job.room.name)
    mic_input = SebastianAudioInput(ctx.room)
    ctx.add_shutdown_callback(mic_input.aclose)
    if RECORD:
        setup_recorder(ctx)
    session = _build_session()
    instrument_session(session)
    _setup_barge_in(ctx, session)
    session.input.audio = mic_input
    await session.start(
        room=ctx.room,
        agent=Sebastian(),
        room_options=RoomOptions(audio_input=False),
    )
    await ctx.connect()
    await ctx.wait_for_participant(identity=DEVICE_IDENTITY)
    with contextlib.suppress(asyncio.TimeoutError):
        await asyncio.wait_for(mic_input.preroll_ready.wait(), timeout=1.2)
    if mic_input.preroll_ready.is_set():
        log.info("device joined — pre-roll incoming, skipping greeting")
        return
    log.info("device joined — greeting")
    await session.generate_reply(
        instructions="Saluda al usuario y preséntate en una frase."
    )


if __name__ == "__main__":
    opts = agents.WorkerOptions(entrypoint_fnc=entrypoint, agent_name="sebastian")
    if os.getenv("SEBASTIAN_DEBUG") == "1":
        opts.job_executor_type = agents.JobExecutorType.THREAD
    agents.cli.run_app(opts)
