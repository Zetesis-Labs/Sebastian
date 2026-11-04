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
import time
from collections.abc import AsyncIterator
from pathlib import Path

import aiohttp
from dotenv import load_dotenv
from livekit import agents, rtc
from livekit.agents import (
    Agent,
    AgentSession,
    RunContext,
    function_tool,
    get_job_context,
    mcp,
    tts,
)
from livekit.agents.voice.agent_session import TurnHandlingOptions
from livekit.agents.voice.room_io import RoomOptions
from google.genai import types as genai_types
from livekit.plugins import google, openai, phonic, silero
from openai.types.beta.realtime.session import TurnDetection

import talk_over
import telemetry
from audio_input import SebastianAudioInput, setup_recorder, setup_output_recorder, RECORD, RECORD_TRACK
from device_identity import device_identity_from_metadata
from announce import (
    CHIME_SAMPLE_RATE,
    announce_voice,
    announcement_from_metadata,
    announcement_from_payload,
    chime_pcm,
)
from endpointing import AGENT_STATE_TOPIC, close_device_session, setup_endpointing
from instrumentation import instrument_session
from meeting_mode import (
    AGENT_SECRET,
    API_URL,
    NOT_ASKED_TO_RECORD,
    meeting_from_metadata,
    request_meeting_by_voice,
    run_meeting,
    voice_start_asked,
)
from wake_verify import setup_wake_verify
from phonic_buffered import BufferedPhonicRealtimeModel
from model_choice import (
    ENV_KEYS,
    CredentialsUnavailable,
    ModelChoice,
    UnknownProvider,
    api_key_for,
    choice_from_metadata,
    fetch_profile_credentials,
    panel_key_for,
)
from tasks import spawn as _spawn
from turns import user_turns
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
m_announce_failed = telemetry.counter(
    "sebastian_agent_announcements_failed_total", "Server-pushed announcements not said (by reason)"
)
m_model_unavailable = telemetry.counter(
    "sebastian_agent_model_unavailable_total",
    "Sessions refused because the agent profile's model could not be built (by provider, reason)",
)

BARGE_TOPIC = "sebastian.barge_in"
ANNOUNCE_TOPIC = "sebastian.announce"
MODEL_PROVIDER = os.getenv("SEBASTIAN_MODEL_PROVIDER", "gemini").strip().lower()
GEMINI_MODEL = os.getenv(
    "SEBASTIAN_GEMINI_MODEL",
    "gemini-2.5-flash-native-audio-preview-12-2025",
)
GEMINI_VOICE = os.getenv("SEBASTIAN_GEMINI_VOICE", os.getenv("SEBASTIAN_VOICE", "Puck"))
# Native-audio models REJECT a language code (field-tested: 1007 "Unsupported
# language code 'es-ES'" kills the session at setup) — they are auto-only, so
# the Cyrillic/Afrikaans transcript drift has no config fix on this model.
# Only set this env when running a half-cascade model.
GEMINI_LANGUAGE = os.getenv("SEBASTIAN_GEMINI_LANGUAGE", "").strip()
OPENAI_REALTIME_MODEL = os.getenv(
    "SEBASTIAN_OPENAI_REALTIME_MODEL", "gpt-realtime-mini"
)
OPENAI_VOICE = os.getenv(
    "SEBASTIAN_OPENAI_VOICE", os.getenv("SEBASTIAN_VOICE", "alloy")
)
# Empty = Phonic's current default model; the API names them "phonic-1", "phonic-0.5".
PHONIC_MODEL = os.getenv("SEBASTIAN_PHONIC_MODEL", "").strip()
PHONIC_VOICE = os.getenv("SEBASTIAN_PHONIC_VOICE", os.getenv("SEBASTIAN_VOICE", "eduardo"))
PHONIC_LANGUAGE = os.getenv("SEBASTIAN_PHONIC_LANGUAGE", "es").strip()

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
# The realtime model can call a tool a few ms before the user's transcript lands.
VOICE_START_SETTLE_S = 1.5
PHANTOM_SESSION_S = 12.0  # a user-closed session shorter than this ≈ likely phantom wake


class Sebastian(Agent):
    def __init__(self, device_id: str) -> None:
        self._started = time.monotonic()
        self._device_id = device_id
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
                "Si te piden grabar la reunión (grabar esto, empezar a grabar, "
                "grabar la reunión), llama a start_meeting_recording y di "
                "EXACTAMENTE la frase que devuelve, nada más; después la sesión "
                "se cierra sola y el altavoz empieza a grabar. "
                "Nunca pronuncies tu propio nombre, Sebastián: el altavoz lo "
                "interpreta como una orden de interrupción y te cortaría a ti mismo."
            )
        )

    @function_tool
    async def start_meeting_recording(self, context: RunContext) -> str:
        """Empieza a grabar una reunión con este altavoz. Llámala cuando el
        usuario pida grabar la reunión, grabar esto o empezar a grabar. Devuelve
        la frase exacta que debes decir; tras decirla, la sesión se cierra sola."""
        if not await _user_asked_to_record(context.session):
            return NOT_ASKED_TO_RECORD.say
        outcome = await request_meeting_by_voice(self._device_id)
        if outcome.close:
            # The unit starts recording once this conversation is over (the
            # order waits in its mailbox); leave time for the confirmation.
            _spawn(close_device_session(get_job_context(), reason="meeting", pre_grace_s=GOODBYE_GRACE_S + 3))
        return outcome.say

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
        # Same close primitive as the idle endpointing: device-initiated
        # disconnect, delete_room only as cleanup. The pre-grace lets the
        # goodbye finish draining on the speaker before the device tears down.
        _spawn(
            close_device_session(
                get_job_context(), reason="user_request", pre_grace_s=GOODBYE_GRACE_S
            )
        )
        return "Sesión terminándose."


def _user_turns(session: AgentSession) -> list[str]:
    return user_turns(
        (
            getattr(item, "role", None) if getattr(item, "type", None) == "message" else None,
            getattr(item, "text_content", None) or "",
        )
        for item in session.history.items
    )


async def _user_asked_to_record(session: AgentSession) -> bool:
    if voice_start_asked(_user_turns(session)):
        return True
    await asyncio.sleep(VOICE_START_SETTLE_S)
    turns = _user_turns(session)
    if voice_start_asked(turns):
        return True
    log.warning("[voice] start_meeting_recording ignored: no start phrase in %r", turns[-1:])
    return False


def _build_gemini_realtime_model(choice: ModelChoice, api_key: str) -> google.realtime.RealtimeModel:
    model = choice.model or GEMINI_MODEL
    voice = choice.voice or GEMINI_VOICE
    language = choice.language or GEMINI_LANGUAGE
    if language and "native-audio" in model:
        log.warning("[model] gemini %s rejects a language code; ignoring %r", model, language)
        language = ""
    log.info("[model] provider=gemini model=%s voice=%s language=%s", model, voice, language or "<auto>")
    kwargs: dict[str, Any] = dict(
        model=model,
        voice=voice,
        api_key=api_key,
        proactivity=True,
        enable_affective_dialog=True,
        # Less sensitive speech-start detection: with the TV in the room,
        # Gemini's default AAD committed noise blips as user turns ("Ma",
        # "sê. Dis waar.") that truncated replies within seconds of starting.
        # Real interruptions don't need this hair-trigger — the talk-over
        # detector (audio_input.py) covers the instant-cut path.
        realtime_input_config=genai_types.RealtimeInputConfig(
            automatic_activity_detection=genai_types.AutomaticActivityDetection(
                start_of_speech_sensitivity=genai_types.StartSensitivity.START_SENSITIVITY_LOW,
            ),
        ),
    )
    if language:
        kwargs["language"] = language
    return google.realtime.RealtimeModel(**kwargs)


def _build_openai_realtime_model(choice: ModelChoice, api_key: str) -> openai.realtime.RealtimeModel:
    model = choice.model or OPENAI_REALTIME_MODEL
    voice = choice.voice or OPENAI_VOICE
    log.info("[model] provider=openai model=%s voice=%s", model, voice)
    return openai.realtime.RealtimeModel(
        model=model,
        voice=voice,
        api_key=api_key,
        # eagerness=low is an ENDPOINTING choice (don't commit the user's turn
        # while they pause to think). interrupt_response must be explicit: the
        # field-observed default let talk-over speech pass WITHOUT the server
        # interrupting the ongoing reply — no input_speech_started reached the
        # framework, so playback (and the device render FIFO) narrated on.
        turn_detection=TurnDetection(
            type="semantic_vad", eagerness="low", interrupt_response=True
        ),
    )


def _build_phonic_realtime_model(choice: ModelChoice, api_key: str) -> phonic.realtime.RealtimeModel:
    model = choice.model or PHONIC_MODEL
    voice = choice.voice or PHONIC_VOICE
    language = choice.language or PHONIC_LANGUAGE
    log.info("[model] provider=phonic model=%s voice=%s language=%s", model or "<default>", voice, language)
    kwargs: dict[str, Any] = dict(api_key=api_key, voice=voice, default_language=language)
    if model:
        kwargs["phonic_model"] = model
    return BufferedPhonicRealtimeModel(**kwargs)


_BUILDERS = {
    "gemini": _build_gemini_realtime_model,
    "openai": _build_openai_realtime_model,
    "phonic": _build_phonic_realtime_model,
}


def _build_announce_tts(choice: ModelChoice, api_key: str) -> tts.TTS | None:
    """The voice announcements are said in: the profile's own. Phonic speaks
    given text itself (supports_say), so its session needs no TTS; OpenAI and
    Gemini realtime cannot, and use their provider's TTS with the same voice
    and the profile's key."""
    voice = announce_voice(choice.provider)
    if voice == "openai_tts":
        return openai.TTS(voice=choice.voice or OPENAI_VOICE, api_key=api_key)
    if voice == "gemini_tts":
        return google.beta.GeminiTTS(voice_name=choice.voice or GEMINI_VOICE, api_key=api_key)
    return None


def _build_session(choice: ModelChoice, api_key: str) -> AgentSession:
    realtime = _BUILDERS[choice.provider](choice, api_key)
    return AgentSession(
        llm=realtime,
        # TTS is used only by session.say() for announcements: a cold realtime
        # session returns empty generations for out-of-band replies, so
        # announcements are said verbatim (see _build_announce_tts).
        tts=_build_announce_tts(choice, api_key),
        # NOTE on interruptions: with turn_detection="realtime_llm" the
        # framework hard-ignores every local interruption path (VAD events,
        # transcript activity — see agent_activity.on_vad_inference_done), and
        # the server's semantic_vad only rules at END of utterance. Instant
        # talk-over interruption is therefore OURS: SebastianAudioInput runs
        # silero on the mic frames and on_talk_over calls session.interrupt()
        # (wired in entrypoint), the same call the wake-word barge-in uses.
        turn_handling=TurnHandlingOptions(turn_detection="realtime_llm"),
        mcp_servers=_ha_mcp_servers(),
        # Default 3.0s exists for the framework's SOFTWARE AEC to warm up; our
        # echo path is hardware (XVF AEC + comms-channel residual suppressor,
        # always on), so those 3s only delay barge-in: interruptions were dead
        # for the first 3s of every reply ("cutting it off takes forever").
        aec_warmup_duration=0.0,
    )


ANNOUNCE_WAIT_S = 60.0  # max courtesy wait for the session to go idle


async def _chime_frames() -> AsyncIterator[rtc.AudioFrame]:
    pcm = chime_pcm()
    step = CHIME_SAMPLE_RATE // 100 * 2  # 10 ms of mono 16-bit
    for i in range(0, len(pcm), step):
        chunk = pcm[i : i + step]
        yield rtc.AudioFrame(chunk, CHIME_SAMPLE_RATE, 1, len(chunk) // 2)


async def _say_announcement(session: AgentSession, text: str) -> None:
    """Chime, then the text verbatim in the profile's voice. A chime that
    fails must not cost the announcement itself."""
    log.info("announce → speaking: %r", text)
    try:
        # Not "": LiveKit skips empty text and the transcript segment never ends.
        await session.say(" ", audio=_chime_frames(), add_to_chat_ctx=False).wait_for_playout()
    except Exception as e:
        log.warning("announce chime failed, saying the text anyway: %r", e)
    await session.say(text).wait_for_playout()
    m_announce.add(1)


async def _speak_when_idle(session: AgentSession, text: str) -> None:
    """Courtesy announce delivery (§4 'proactivity with courtesy').

    An announcement must not be DROPPED, but firing generate_reply() while a
    generation is in flight is the known orphaned-reply collision (5s timeout,
    nothing spoken). And SUSTAINED idle, not instant: an announce fired the
    moment a session opens races the user's opening command and Gemini garbles
    both (field: "Anuncio en curso."). Require ~3s of continuous idle."""
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
                # say(), NOT generate_reply: after 4 field cases, every
                # announce routed through Gemini was a lottery (empty/garbled
                # generations: 0.007s, "Anuncio en curso.", "Let me check").
                # Deterministic verbatim delivery is what an announcement wants.
                try:
                    await _say_announcement(session, text)
                except Exception:
                    m_announce_failed.add(1, {"reason": "error"})
                    log.exception("announce FAILED in the conversation: %r", text)
                return
        else:
            idle_since = None
        await asyncio.sleep(0.25)
    m_announce_failed.add(1, {"reason": "busy"})
    log.warning("announce DROPPED: session busy for %.0fs: %r", ANNOUNCE_WAIT_S, text)


def _setup_announce(ctx: agents.JobContext, session: AgentSession, queue: list[str] | None = None) -> None:
    """Per-session announce wiring: an announce pushed on the data channel
    while a conversation is open is spoken once the session goes idle. An idle
    speaker gets its own announce session instead (see entrypoint)."""

    @ctx.room.on("data_received")
    def _on_data(packet: rtc.DataPacket) -> None:
        if packet.topic != ANNOUNCE_TOPIC:
            return
        announcement = announcement_from_payload(bytes(packet.data))
        if announcement is None:
            m_announce_failed.add(1, {"reason": "bad_payload"})
            log.warning("announce: bad payload, ignored")
            return
        if queue is not None:
            # An announce session that closes once said: speak it before closing.
            log.info("announce queued behind this session's own: %r", announcement.text)
            queue.append(announcement.text)
            return
        log.info("announce queued (waiting for idle): %r", announcement.text)
        _spawn(_speak_when_idle(session, announcement.text))


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


async def _resolve_model(ctx: agents.JobContext) -> tuple[ModelChoice | None, str | None]:
    """The speaker's agent profile and its API key. A session never starts on a
    guessed model: an unknown provider, an unreadable stored key or no key at
    all refuse it, loudly."""
    try:
        choice = choice_from_metadata(ctx.job.metadata, MODEL_PROVIDER)
    except UnknownProvider as e:
        log.error("[model] session refused: %s", e)
        m_model_unavailable.add(1, {"provider": "unknown", "reason": "unknown_provider"})
        return None, None
    panel_key: str | None = None
    if choice.profile_id:
        if not AGENT_SECRET:
            # Without it the stored key cannot be asked for, and using the
            # environment one instead could be another account: refuse.
            log.error("[model] session refused: SEBASTIAN_AGENT_SECRET not set, the key of profile %s is unknowable", choice.profile_id)
            m_model_unavailable.add(1, {"provider": choice.provider, "reason": "no_agent_secret"})
            return choice, None
        try:
            async with aiohttp.ClientSession() as http:
                creds = await fetch_profile_credentials(http, API_URL, AGENT_SECRET, choice.profile_id)
            panel_key = panel_key_for(choice, creds)
        except CredentialsUnavailable as e:
            log.error("[model] session refused: the key of profile %s is unusable: %s", choice.profile_id, e)
            m_model_unavailable.add(1, {"provider": choice.provider, "reason": "credentials_unavailable"})
            return choice, None
    api_key = api_key_for(choice.provider, panel_key, os.environ)
    if api_key is None:
        log.error(
            "[model] session refused: no API key for provider=%s (profile %s): store one in the panel or set %s",
            choice.provider, choice.profile_id or "<none>", ENV_KEYS[choice.provider],
        )
        m_model_unavailable.add(1, {"provider": choice.provider, "reason": "no_api_key"})
        return choice, None
    log.info(
        "[model] profile=%s provider=%s key=%s", choice.profile_id or "<none>", choice.provider,
        "panel" if panel_key else "environment",
    )
    return choice, api_key


async def entrypoint(ctx: agents.JobContext) -> None:
    m_jobs.add(1)
    device_identity = device_identity_from_metadata(ctx.job.metadata)
    log.info("job accepted room=%s device=%s", ctx.job.room.name, device_identity)
    meeting = meeting_from_metadata(ctx.job.metadata)
    if meeting is not None:
        # A meeting recording (design 14 block C): no assistant at all (RM-12).
        await run_meeting(ctx, meeting, device_identity, silero.VAD.load())
        return
    announcement = announcement_from_metadata(ctx.job.metadata)
    choice, api_key = await _resolve_model(ctx)
    if choice is None or api_key is None:
        if announcement is not None:
            m_announce_failed.add(1, {"reason": "model_unavailable"})
            log.error("announce NOT said, no model for this profile: %r", announcement.text)
        await ctx.connect()
        await close_device_session(ctx, reason="model_unavailable")
        return
    mic_input = SebastianAudioInput(ctx.room, vad=silero.VAD.load(), device_identity=device_identity)
    ctx.add_shutdown_callback(mic_input.aclose)
    if RECORD and RECORD_TRACK:
        # Raw-track tap — off by default: it starts at the handoff (no
        # pre-roll), so _model.wav is what you want to listen to.
        setup_recorder(ctx)
    session = _build_session(choice, api_key)
    instrument_session(session)
    _setup_barge_in(ctx, session)
    # More announcements can reach an announce session that only speaks (the
    # server sees the unit talking with an agent): they are said before it closes.
    queued: list[str] | None = [] if announcement is not None and not announcement.reply else None
    _setup_announce(ctx, session, queued)
    setup_endpointing(ctx, session, device_identity)
    if announcement is None:
        # An announce session opens without a wake word: nothing to verify.
        setup_wake_verify(ctx, session, mic_input)
    session.input.audio = mic_input

    async def _flush_device_render() -> None:
        # The item.interrupted→flush relay in instrumentation fires when the
        # item FINALIZES — observed 56s after the interrupt. The device FIFO
        # keeps narrating all that time, so flush it here, immediately.
        try:
            await ctx.room.local_participant.publish_data(
                b"interrupted", topic=AGENT_STATE_TOPIC, reliable=True
            )
        except Exception as e:
            log.warning("device flush publish failed: %r", e)

    speaking_since: float | None = None

    @session.on("agent_state_changed")
    def _track_speaking_start(ev) -> None:
        # When he STARTED, not merely that he is speaking now: talk_over needs
        # it to tell a barge-in from the tail of the turn he is replying to.
        nonlocal speaking_since
        speaking_since = time.monotonic() if str(ev.new_state) == "speaking" else None

    def _on_talk_over(speech_started_at: float, speech_duration: float) -> None:
        # Voice that began while Sebastian was already talking → cut him off
        # NOW. The framework won't do this itself in realtime_llm mode (see
        # _build_session): interrupt() truncates the agent-side playout and
        # the direct publish flushes what the device already buffered.
        if not talk_over.should_interrupt(
            full_duplex=mic_input.full_duplex,
            agent_speaking_since=speaking_since,
            speech_started_at=speech_started_at,
            speech_duration=speech_duration,
        ):
            return
        log.info("talk-over detected — interrupting agent speech")
        try:
            session.interrupt()
        except Exception as e:
            log.warning("talk-over interrupt failed: %r", e)
        _spawn(_flush_device_render())

    mic_input.on_talk_over = _on_talk_over
    await session.start(
        room=ctx.room,
        agent=Sebastian(device_identity),
        room_options=RoomOptions(audio_input=False),
    )
    if RECORD:
        # Tee the agent's OUTGOING audio to a WAV too — the mic recorder only
        # captures device→agent, so without this the recording has no Sebastian.
        out_tee = setup_output_recorder(session, ctx.room.name)
        if out_tee is not None:
            ctx.add_shutdown_callback(out_tee.aclose)
    await ctx.connect()
    await ctx.wait_for_participant(identity=device_identity)
    if announcement is not None:
        log.info("announce session (reply=%s)", announcement.reply)
        try:
            await _say_announcement(session, announcement.text)
            while queued:
                await _say_announcement(session, queued.pop(0))
        except Exception:
            m_announce_failed.add(1, {"reason": "error"})
            log.exception("announce FAILED: %r", announcement.text)
            await close_device_session(ctx, reason="announce_failed")
            return
        if not announcement.reply:
            await close_device_session(ctx, reason="announce", pre_grace_s=GOODBYE_GRACE_S)
        return
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
