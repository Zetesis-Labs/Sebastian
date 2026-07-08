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
import json
import logging
import os
import time
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
from audio_input import SebastianAudioInput, setup_recorder, setup_output_recorder, RECORD
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
m_end = telemetry.counter(
    "sebastian_agent_sessions_ended_total",
    "Sessions ended by the user (end_session); 'short' label flags phantom candidates",
)

m_announce = telemetry.counter(
    "sebastian_agent_announcements_total", "Server-pushed announcements spoken"
)

BARGE_TOPIC = "sebastian.barge_in"
ANNOUNCE_TOPIC = "sebastian.announce"
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
PHANTOM_SESSION_S = 12.0  # a user-closed session shorter than this ≈ likely phantom wake


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
        self._started = time.monotonic()
        super().__init__(
            instructions=(
                "Eres Sebastián, un asistente de voz que vive en un altavoz "
                "ESP32-S3 con un array de 4 micrófonos. Responde en el mismo "
                "idioma en que te hablen, de forma natural y breve, como en una "
                "conversación. No uses markdown ni listas: solo se te va a "
                "escuchar, no a leer. "
                "Puedes controlar la casa (luces, enchufes, sensores…) con las "
                "herramientas de Home Assistant: cuando te pidan encender o apagar "
                "algo, úsalas de verdad y confirma en una frase corta lo que hiciste. "
                "Si una herramienta falla o no encuentras el dispositivo, dilo con "
                "naturalidad en vez de inventarte que lo hiciste. "
                "Para CUALQUIER pregunta sobre el estado de la casa (luces, "
                "persianas, sensores, temperatura…) consulta primero GetLiveContext "
                "y responde solo con esos datos. No puedes ver: nunca describas el "
                "aspecto físico de la casa ni inventes lo que no te dé una herramienta. "
                "Cuando el usuario te ordene parar o callar, o dé la conversación "
                "por terminada o se despida —en el idioma que sea, y entendiendo la "
                "intención, no palabras sueltas: 'para', 'cállate', 'ya vale', "
                "'stop', 'déjalo', 'adiós', 'gracias, nada más'…— llama a "
                "end_session. Distingue: si es una orden de PARAR/CALLAR, ciérrala "
                "SIN decir nada (ni 'vale' ni 'adiós'); si es una DESPEDIDA, "
                "despídete con una sola palabra y ciérrala. En ambos casos la "
                "sesión termina y el usuario tendrá que volver a activarte. Ojo con "
                "la ambigüedad: 'enciende la luz para el salón' NO es una orden de "
                "parar; solo cierra cuando la intención real sea detenerte o "
                "terminar. "
                "Nunca pronuncies tu propio nombre, Sebastián: el altavoz lo "
                "interpreta como una orden de interrupción y te cortaría a ti mismo."
            )
        )

    @function_tool
    async def end_session(self, context: RunContext) -> str:
        """Cierra la sesión de voz por completo. Llámala cuando el usuario te
        pida parar/callar, dé la conversación por terminada o se despida (en
        cualquier idioma, por intención). Para volver a hablar tras cerrarla hará
        falta la palabra de activación."""
        _ = context
        dur = time.monotonic() - self._started
        short = dur < PHANTOM_SESSION_S
        m_end.add(1, {"short": str(short)})
        # Session-level phantom signal (language-agnostic, §7): a real
        # interaction lasts more than a few seconds. A very short session the
        # user had to shut down is a strong false-positive-wake candidate.
        log.info(
            "end_session after %.1fs%s — closing room",
            dur, "  [likely-phantom]" if short else "",
        )
        _spawn(_delete_room_after_goodbye(get_job_context()))
        return "Sesión terminándose."


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


ANNOUNCE_WAIT_S = 60.0  # max courtesy wait for the session to go idle


def _setup_announce(ctx: agents.JobContext, session: AgentSession) -> None:
    """Control-plane announce (§9): the control plane sends text on
    ANNOUNCE_TOPIC; the agent speaks it. The first server→device push mode.

    Courtesy delivery (§4 'proactivity with courtesy'): an announcement must not
    be DROPPED, but firing generate_reply() while a generation is in flight is
    the known orphaned-reply collision (5s timeout, nothing spoken — exactly how
    the first field test failed, mid-story). So: wait until the session is
    demonstrably idle, then speak."""

    async def _speak_when_idle(text: str) -> None:
        # SUSTAINED idle, not instant: with per-session rooms, "device present"
        # happens right after a wake — i.e. exactly while the user is giving
        # their opening command. An announce fired at that instant races the
        # user's turn and Gemini garbles both (field: it said "Anuncio en
        # curso." instead of the announcement). Require ~3s of continuous
        # idle so the conversation has actually settled.
        deadline = asyncio.get_event_loop().time() + ANNOUNCE_WAIT_S
        idle_since: float | None = None
        while asyncio.get_event_loop().time() < deadline:
            state = getattr(session, "agent_state", None)
            speech = getattr(session, "current_speech", None)
            now = asyncio.get_event_loop().time()
            if speech is None and state in (None, "listening"):
                if idle_since is None:
                    idle_since = now
                if now - idle_since >= 3.0:
                    m_announce.add(1)
                    log.info("announce → speaking: %r", text)
                    # generate_reply (not say): the realtime model has no
                    # separate TTS; a natural delivery reads better than verbatim.
                    session.generate_reply(
                        instructions=f"Anuncia esto al usuario, de forma natural y "
                        f"breve, sin añadir preguntas ni ofrecer ayuda: {text}"
                    )
                    return
            else:
                idle_since = None
            await asyncio.sleep(0.25)
        log.warning("announce DROPPED: session busy for %.0fs: %r", ANNOUNCE_WAIT_S, text)

    @ctx.room.on("data_received")
    def _on_data(packet: rtc.DataPacket) -> None:
        if packet.topic != ANNOUNCE_TOPIC:
            return
        try:
            text = json.loads(bytes(packet.data).decode())["text"].strip()
        except Exception as e:
            log.warning("announce: bad payload: %r", e)
            return
        if not text:
            return
        log.info("announce queued (waiting for idle): %r", text)
        _spawn(_speak_when_idle(text))




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
    _setup_announce(ctx, session)
    session.input.audio = mic_input
    await session.start(
        room=ctx.room,
        agent=Sebastian(),
        room_options=RoomOptions(audio_input=False),
    )
    if RECORD:
        # Both directions on tape: the mic recorder (setup_recorder) only hears
        # the device; this tee captures what Sebastian says.
        out_tee = setup_output_recorder(session, ctx.room.name)
        if out_tee is not None:
            ctx.add_shutdown_callback(out_tee.aclose)
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
