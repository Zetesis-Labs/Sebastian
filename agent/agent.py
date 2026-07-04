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
import re
import time
import unicodedata
import wave
from collections import deque
from pathlib import Path

import aiohttp

from dotenv import load_dotenv
from livekit import agents, api, rtc
from livekit.agents import Agent, AgentSession, RunContext, function_tool, get_job_context, mcp
from livekit.agents.voice.agent_session import TurnHandlingOptions
from livekit.agents.voice.room_io import RoomOptions
from livekit.plugins import google, noise_cancellation, openai
from openai.types.beta.realtime.session import TurnDetection

import telemetry

load_dotenv(Path(__file__).with_name(".env"))
telemetry.setup()

log = logging.getLogger("sebastian.agent")

m_jobs = telemetry.counter("sebastian_agent_jobs_total", "Jobs accepted (one per device session)")
m_turns = telemetry.counter("sebastian_agent_turns_total", "Conversation items by role")
m_tools = telemetry.counter("sebastian_agent_tool_calls_total", "Tool executions, by tool name")
m_state = telemetry.counter("sebastian_agent_state_changes_total", "Agent state transitions")
m_errors = telemetry.counter("sebastian_agent_errors_total", "Session error events")
m_barge = telemetry.counter("sebastian_agent_barge_ins_total", "Wake-word interrupts while speaking")
m_phantom = telemetry.counter("sebastian_agent_phantom_turns_total", "User turns classified as agent echo, by reason")
m_user_during_speech = telemetry.counter("sebastian_agent_user_turns_during_speech_total", "User turns arriving while the agent speaks")

# ── Phantom-turn detection ────────────────────────────────────────────────────
# Without AEC convergence the agent's speech returns through the mic as spurious
# "user" turns. This turns that echo into a number (Fase 0 of the AEC project):
# a user turn is echo if it arrives during/just after agent speech AND either
# repeats what the agent just said or is transcribed in a non-Spanish language.
# With the half-duplex gate ON it should be ~0 by construction — a nonzero count
# is the "before" baseline and the metric that must stay 0 in full-duplex.
ECHO_TAIL_S = 2.5       # reverb + render FIFO after speaking→listening
OVERLAP_WINDOW_S = 20.0  # how far back to compare against the agent's own words
OVERLAP_PHANTOM = 0.55   # near-literal echo: phantom even if the timing missed it
OVERLAP_WEAK = 0.30      # partial echo: only counts combined with echo timing
_ES_HINTS = {"que", "de", "la", "el", "en", "y", "no", "los", "se", "por", "un",
             "una", "para", "con", "es", "si", "como", "esta", "hola", "gracias"}


def _norm(t: str) -> str:
    t = "".join(c for c in unicodedata.normalize("NFKD", t.lower()) if not unicodedata.combining(c))
    return re.sub(r"[^\w\s]", " ", t)


def _shingles(t: str) -> set[str]:
    w = _norm(t).split()
    if len(w) < 3:
        return set(w)
    return {" ".join(w[i : i + 3]) for i in range(len(w) - 2)}


def _containment(user_text: str, ref_text: str) -> float:
    u, r = _shingles(user_text), _shingles(ref_text)
    return len(u & r) / len(u) if u else 0.0


def _looks_spanish(t: str) -> bool:
    if any(ord(ch) > 0x24F for ch in t):  # cyrillic / CJK / etc. → clearly not
        return False
    words = _norm(t).split()
    if len(words) < 3:
        return True  # too short to judge — benefit of the doubt
    return sum(w in _ES_HINTS for w in words) / len(words) >= 0.15

REC_PATH = "/tmp/sebastian_rx.wav"
PREROLL_PATH = "/tmp/sebastian_preroll.wav"
PREROLL_TOPIC = "sebastian.preroll"
AGENT_STATE_TOPIC = "sebastian.agent_state"
BARGE_TOPIC = "sebastian.barge_in"
DEVICE_IDENTITY = os.getenv("SEBASTIAN_DEVICE_IDENTITY", "esp32-respeaker")
LIVE_SAMPLE_RATE = 24000
LIVE_FRAME_MS = 50
# The device sends the pre-roll at the mic handoff, right as the track starts;
# the ring can now be up to 12s (~384KB), so give the transfer real headroom.
# Live frames buffer while we wait — this delays first processing, loses nothing.
PREROLL_WAIT_TIMEOUT = 6.0
RECORD = os.getenv("SEBASTIAN_RECORD") == "1"
MODEL_PROVIDER = os.getenv("SEBASTIAN_MODEL_PROVIDER", "gemini").strip().lower()
GEMINI_MODEL = os.getenv(
    "SEBASTIAN_GEMINI_MODEL",
    "gemini-2.5-flash-native-audio-preview-12-2025",
)
GEMINI_VOICE = os.getenv("SEBASTIAN_GEMINI_VOICE", os.getenv("SEBASTIAN_VOICE", "Puck"))
# Empty = let the model auto-detect. The native-audio models reject explicit
# BCP-47 codes like "es-ES" (APIError 1007 → session closes → agent goes silent);
# they infer language from the audio + system prompt. Set a code only for a model
# that accepts one (e.g. the half-cascade models).
GEMINI_LANGUAGE = os.getenv("SEBASTIAN_GEMINI_LANGUAGE", "").strip()
OPENAI_REALTIME_MODEL = os.getenv("SEBASTIAN_OPENAI_REALTIME_MODEL", "gpt-realtime-mini")
OPENAI_VOICE = os.getenv("SEBASTIAN_OPENAI_VOICE", os.getenv("SEBASTIAN_VOICE", "alloy"))

# Home Assistant MCP server (control the house). URL is the HA "MCP Server"
# integration SSE endpoint; auth is a Long-Lived Access Token (HA → profile →
# Long-Lived Access Tokens). Both from env so no secret lives in the repo.
HA_MCP_URL = os.getenv("SEBASTIAN_HA_MCP_URL", "https://rallo.nexolabs.dev/mcp_server/sse")
HA_TOKEN = os.getenv("SEBASTIAN_HA_TOKEN")


def _ha_mcp_servers() -> list:
    if not HA_TOKEN:
        log.warning("[mcp] SEBASTIAN_HA_TOKEN not set — Home Assistant control disabled")
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


GOODBYE_GRACE_S = 3.0  # let the goodbye TTS drain before the room dies


async def _delete_room_after_goodbye(job_ctx: agents.JobContext) -> None:
    await asyncio.sleep(GOODBYE_GRACE_S)
    try:
        await job_ctx.api.room.delete_room(api.DeleteRoomRequest(room=job_ctx.room.name))
        log.info("session ended by user request — room deleted")
    except aiohttp.ServerDisconnectedError:
        # Deleting the room tears down our own connection mid-request — the
        # deletion itself succeeded (the device sees ROOM_DELETED).
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
                "Si el usuario pide que te calles, que pares, dice que ya está o se "
                "despide ('cállate', 'para', 'ya vale', 'adiós', 'gracias, nada más'), "
                "llama a end_session y despídete con una sola palabra. "
                "Nunca pronuncies tu propio nombre, Sebastián: el altavoz lo "
                "interpreta como una orden de interrupción y te cortaría a ti mismo."
            )
        )

    @function_tool
    async def end_session(self, context: RunContext) -> str:
        """Termina la sesión de voz por completo. Llámala cuando el usuario pida
        que te calles o que pares, dé la conversación por terminada o se despida.
        Para volver a hablar tendrá que decir la palabra de activación."""
        _ = context
        asyncio.create_task(_delete_room_after_goodbye(get_job_context()))
        log.info("end_session tool called — closing room in %.1fs", GOODBYE_GRACE_S)
        return "Sesión terminándose. Despídete con una sola palabra."


async def _record_track(track: rtc.Track, path: str) -> None:
    """Write the incoming mic PCM to a WAV so we can actually listen to what the
    agent receives (16 kHz mono)."""
    stream = rtc.AudioStream(track, sample_rate=16000, num_channels=1)
    wav = wave.open(path, "wb")
    wav.setnchannels(1)
    wav.setsampwidth(2)
    wav.setframerate(16000)
    log.info("[recorder] writing incoming mic audio to %s", path)
    try:
        async for ev in stream:
            wav.writeframes(bytes(ev.frame.data))
    finally:
        wav.close()


def _setup_recorder(ctx: agents.JobContext) -> None:
    @ctx.room.on("track_subscribed")
    def _on_track(track, publication, participant):  # noqa: ANN001
        if track.kind == rtc.TrackKind.KIND_AUDIO and "esp32" in participant.identity:
            asyncio.create_task(_record_track(track, REC_PATH))


GATE_SILENCE_PEAK = 100  # gate silence ≈ 0 after Opus; real room noise ≥ 1400


def _frame_peak(frame: rtc.AudioFrame) -> int:
    mv = memoryview(frame.data)
    samples = mv if mv.format == "h" else mv.cast("B").cast("h")
    return max(abs(s) for s in samples) if len(samples) else 0


def _write_wav(path: str, pcm: bytes, sample_rate: int) -> None:
    wav = wave.open(path, "wb")
    try:
        wav.setnchannels(1)
        wav.setsampwidth(2)
        wav.setframerate(sample_rate)
        wav.writeframes(pcm)
    finally:
        wav.close()


class SebastianAudioInput(agents.io.AudioInput):
    def __init__(self, room: rtc.Room) -> None:
        super().__init__(label="SebastianMic")
        self._room = room
        self._queue: asyncio.Queue[rtc.AudioFrame | None] = asyncio.Queue()
        self._preroll_ready = asyncio.Event()
        self.preroll_ready = self._preroll_ready  # exposed: entrypoint skips the greeting on it
        self._preroll_frames: list[rtc.AudioFrame] = []
        self._preroll_consumed = False
        self._attached = True
        self._stream: rtc.AudioStream | None = None
        self._track_task: asyncio.Task[None] | None = None
        self._tasks: set[asyncio.Task[None]] = set()

        room.on("track_subscribed", self._on_track_subscribed)
        room.register_byte_stream_handler(PREROLL_TOPIC, self._on_preroll)

    def on_attached(self) -> None:
        self._attached = True

    def on_detached(self) -> None:
        self._attached = False

    async def __anext__(self) -> rtc.AudioFrame:
        frame = await self._queue.get()
        if frame is None:
            raise StopAsyncIteration
        return frame

    async def aclose(self) -> None:
        self._room.off("track_subscribed", self._on_track_subscribed)
        with contextlib.suppress(ValueError):
            self._room.unregister_byte_stream_handler(PREROLL_TOPIC)

        for task in list(self._tasks):
            task.cancel()
        if self._track_task:
            self._track_task.cancel()
        await asyncio.gather(*self._tasks, return_exceptions=True)
        if self._track_task:
            await asyncio.gather(self._track_task, return_exceptions=True)
        if self._stream:
            await self._stream.aclose()
            self._stream = None
        await self._queue.put(None)

    def _on_preroll(self, reader: rtc.ByteStreamReader, participant: str) -> None:
        task = asyncio.create_task(self._consume_preroll(reader, participant))
        self._tasks.add(task)
        task.add_done_callback(self._tasks.discard)

    async def _consume_preroll(self, reader: rtc.ByteStreamReader, participant: str) -> None:
        payload = bytearray()
        async for chunk in reader:
            payload.extend(chunk)

        parsed = self._parse_preroll(bytes(payload), participant)
        if parsed is None:
            return

        wake_id, sample_rate, pcm = parsed
        if self._preroll_consumed:
            log.info("[preroll] late stream ignored wake_id=%s", wake_id)
            return

        self._preroll_frames = self._pcm_to_frames(pcm, sample_rate)
        self._preroll_ready.set()
        duration_ms = len(pcm) // 2 * 1000 // sample_rate if sample_rate else 0
        log.info(
            "[preroll] ready wake_id=%s from=%s duration_ms=%s frames=%s",
            wake_id, participant, duration_ms, len(self._preroll_frames),
        )
        if RECORD:
            _write_wav(PREROLL_PATH, pcm, sample_rate)
            log.info("[preroll] wrote %s", PREROLL_PATH)

    def _parse_preroll(
        self, payload: bytes, participant: str
    ) -> tuple[int, int, bytes] | None:
        if len(payload) < 16 or payload[:4] != b"SBPR":
            log.warning("[preroll] invalid stream from %s: %s bytes", participant, len(payload))
            return None

        version = payload[4]
        sample_rate = int.from_bytes(payload[6:8], "little")
        sample_count = int.from_bytes(payload[8:12], "little")
        wake_id = int.from_bytes(payload[12:16], "little")
        pcm = payload[16:]
        expected = sample_count * 2
        if version != 1 or sample_rate != 16000 or len(pcm) != expected:
            log.warning(
                "[preroll] malformed stream wake_id=%s version=%s rate=%s pcm=%s expected=%s",
                wake_id, version, sample_rate, len(pcm), expected,
            )
            return None

        return wake_id, sample_rate, pcm

    def _pcm_to_frames(self, pcm: bytes, sample_rate: int) -> list[rtc.AudioFrame]:
        frame_samples = sample_rate * LIVE_FRAME_MS // 1000
        audio_stream = agents.utils.audio.AudioByteStream(
            sample_rate, 1, samples_per_channel=frame_samples
        )
        resampler = rtc.AudioResampler(
            input_rate=sample_rate,
            output_rate=LIVE_SAMPLE_RATE,
            num_channels=1,
        )
        frames: list[rtc.AudioFrame] = []
        for frame in audio_stream.push(pcm):
            frames.extend(resampler.push(frame))
        for frame in audio_stream.flush():
            frames.extend(resampler.push(frame))
        frames.extend(resampler.flush())
        return frames

    def _on_track_subscribed(self, track, publication, participant):  # noqa: ANN001
        if track.kind != rtc.TrackKind.KIND_AUDIO or participant.identity != DEVICE_IDENTITY:
            return

        if self._track_task:
            self._track_task.cancel()
        if self._stream:
            asyncio.create_task(self._stream.aclose())

        self._stream = rtc.AudioStream.from_track(
            track=track,
            sample_rate=LIVE_SAMPLE_RATE,
            num_channels=1,
            frame_size_ms=LIVE_FRAME_MS,
            noise_cancellation=noise_cancellation.BVC(),
        )
        self._track_task = asyncio.create_task(self._forward_track(participant.identity))

    async def _forward_track(self, participant: str) -> None:
        try:
            with contextlib.suppress(asyncio.TimeoutError):
                await asyncio.wait_for(
                    self._preroll_ready.wait(), timeout=PREROLL_WAIT_TIMEOUT
                )

            for frame in self._preroll_frames:
                if self._attached:
                    await self._queue.put(frame)
            if self._preroll_frames:
                log.info("[preroll] injected frames=%s", len(self._preroll_frames))
            self._preroll_consumed = True

            stream = self._stream
            if stream is None:
                return
            log.info("[audio] live stream started from=%s", participant)
            # Between connect and the mic handoff the device publishes gated
            # silence; buffered, it lands BETWEEN the pre-roll and live speech
            # — a fake mid-utterance pause that can split the user's turn.
            # Drop it: gate silence is near-digital-zero, real room noise
            # through the XVF beam idles at peak 1400+.
            leading_silence = True
            dropped = 0
            async for ev in stream:
                if not self._attached:
                    continue
                if leading_silence:
                    if _frame_peak(ev.frame) < GATE_SILENCE_PEAK:
                        dropped += 1
                        continue
                    leading_silence = False
                    if dropped:
                        log.info("[audio] dropped %s leading gate-silence frames", dropped)
                await self._queue.put(ev.frame)
        except asyncio.CancelledError:
            raise
        finally:
            log.info("[audio] live stream stopped from=%s", participant)


def _build_gemini_realtime_model() -> google.realtime.RealtimeModel:
    log.info(
        "[model] provider=gemini model=%s voice=%s language=%s",
        GEMINI_MODEL,
        GEMINI_VOICE,
        GEMINI_LANGUAGE or "<auto>",
    )
    kwargs = dict(
        model=GEMINI_MODEL,
        voice=GEMINI_VOICE,
        # Proactive audio lets Gemini decide not to answer when the follow-up
        # audio is not directed at the device. That is a good fit for LINGER.
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
        # Semantic VAD: end-of-turn is decided from meaning, not just silence.
        # Multilingual (Spanish) and tolerant of mid-sentence pauses. eagerness
        # trades latency for patience — "low" waits longer before assuming the
        # user is done (better far-field, where people pause across a room).
        turn_detection=TurnDetection(type="semantic_vad", eagerness="low"),
    )


def _build_realtime_model():
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
    # The RealtimeModel owns turn detection; tell the session so it doesn't try
    # to attach LiveKit's own detector (which a RealtimeModel would ignore).
    return AgentSession(
        llm=realtime,
        turn_handling=TurnHandlingOptions(turn_detection="realtime_llm"),
        mcp_servers=_ha_mcp_servers(),
    )


NUDGE_AFTER_S = 3.0


def _instrument_session(session: AgentSession) -> None:
    # Armed only until the agent's FIRST response of the session. Nudging every
    # unanswered turn guaranteed a reply to echo phantom turns too — the agent
    # never shut up and the room fed back on itself (field-tested the hard way).
    nudge: dict = {"task": None, "armed": True}
    phantom = {"speaking_since": None, "last_speaking_end": 0.0, "recent": deque()}

    def _phantom_on_state(new_state: str) -> None:
        now = time.monotonic()
        if new_state == "speaking":
            phantom["speaking_since"] = now
        elif phantom["speaking_since"] is not None:
            phantom["last_speaking_end"] = now
            phantom["speaking_since"] = None

    def _phantom_check(role: str, text: str) -> None:
        now = time.monotonic()
        if role == "assistant":
            phantom["recent"].append((now, _norm(text)))
            while phantom["recent"] and now - phantom["recent"][0][0] > OVERLAP_WINDOW_S:
                phantom["recent"].popleft()
            return
        reasons = []
        if phantom["speaking_since"] is not None:
            reasons.append("during_speech")
            m_user_during_speech.add(1)
        elif now - phantom["last_speaking_end"] <= ECHO_TAIL_S:
            reasons.append("echo_tail")
        ref = " ".join(t for ts, t in phantom["recent"] if now - ts <= OVERLAP_WINDOW_S)
        ov = _containment(text, ref)
        if ov >= OVERLAP_WEAK:
            reasons.append("overlap")
        if not _looks_spanish(text):
            reasons.append("language")
        timing = "during_speech" in reasons or "echo_tail" in reasons
        if ov >= OVERLAP_PHANTOM or (timing and len(reasons) >= 2):
            primary = "overlap" if ov >= OVERLAP_PHANTOM else reasons[0]
            m_phantom.add(1, {"reason": primary})
            log.warning("PHANTOM turn (reasons=%s ov=%.2f): %s", reasons, ov, text[:120])

    def _cancel_nudge() -> None:
        if nudge["task"] is not None:
            nudge["task"].cancel()
            nudge["task"] = None

    async def _nudge_response() -> None:
        # A realtime model can leave the session's opening command unanswered
        # when the pre-roll injection cancels the greeting mid-generation.
        await asyncio.sleep(NUDGE_AFTER_S)
        nudge["task"] = None
        log.warning("no response %.1fs after first user turn — nudging generate_reply", NUDGE_AFTER_S)
        try:
            session.generate_reply()
        except Exception as e:
            log.warning("nudge failed (session closing?): %r", e)

    def _publish_state(state: str) -> None:
        # The device gates its mic while the agent speaks (half-duplex): the
        # XVF AEC never converges in-session, so the speaker echo comes back
        # as crisp phantom user turns and the room talks to itself.
        async def _pub() -> None:
            try:
                room = get_job_context().room
                await room.local_participant.publish_data(
                    state.encode(), topic=AGENT_STATE_TOPIC, reliable=True
                )
            except Exception as e:
                log.debug("agent state publish failed: %r", e)

        asyncio.create_task(_pub())

    @session.on("agent_state_changed")
    def _on_state(ev) -> None:  # noqa: ANN001
        state = str(ev.new_state)
        if state in ("thinking", "speaking"):
            _cancel_nudge()
            nudge["armed"] = False
        _phantom_on_state(state)
        _publish_state(state)
        m_state.add(1, {"state": state})
        log.info("agent state: %s", ev.new_state)

    @session.on("conversation_item_added")
    def _on_item(ev) -> None:  # noqa: ANN001
        role = getattr(ev.item, "role", None)
        if role is None:  # AgentHandoff and other non-message items
            return
        text = (getattr(ev.item, "text_content", None) or "").strip()
        if text:
            _phantom_check(str(role), text)
        if str(role) == "user" and text and nudge["armed"]:
            _cancel_nudge()
            nudge["task"] = asyncio.create_task(_nudge_response())
        m_turns.add(1, {"role": str(role)})
        log.info("turn [%s]: %s", role, text[:300])

    @session.on("function_tools_executed")
    def _on_tools(ev) -> None:  # noqa: ANN001
        for call in ev.function_calls:
            m_tools.add(1, {"tool": call.name})
            log.info("tool executed: %s", call.name)

    @session.on("error")
    def _on_error(ev) -> None:  # noqa: ANN001
        m_errors.add(1)
        log.error("session error: %s", ev.error)


def _setup_barge_in(ctx: agents.JobContext, session: AgentSession) -> None:
    # The device runs the wake model on the gated mic while the agent speaks:
    # "Sebastián" over the agent's voice arrives here as a data packet.
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


async def entrypoint(ctx: agents.JobContext):
    m_jobs.add(1)
    log.info("job accepted room=%s", ctx.job.room.name)
    mic_input = SebastianAudioInput(ctx.room)
    ctx.add_shutdown_callback(mic_input.aclose)
    if RECORD:
        _setup_recorder(ctx)
    session = _build_session()
    _instrument_session(session)
    _setup_barge_in(ctx, session)
    session.input.audio = mic_input
    await session.start(
        room=ctx.room,
        agent=Sebastian(),
        room_options=RoomOptions(audio_input=False),
    )
    await ctx.connect()
    # With the dispatch created at token time the agent usually beats the
    # device into the room — greeting an empty room loses the first words.
    await ctx.wait_for_participant(identity=DEVICE_IDENTITY)
    # If a pre-roll is coming the user is already mid-command: greeting on top
    # of it just gets cancelled by the injected speech (and the cancellation
    # cascade can leave the turn unanswered). Greet only on a silent wake.
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
    # agent_name makes this an explicit-dispatch worker: it no longer joins every
    # fresh room automatically. It's dispatched to the device's room via the
    # RoomConfiguration embedded in the token (see token_server.py) — which
    # removes the old "delete the room + reset the board" workaround.
    opts = agents.WorkerOptions(entrypoint_fnc=entrypoint, agent_name="sebastian")
    # Jobs run in a child PROCESS by default; a debugger can't step into the
    # entrypoint there. SEBASTIAN_DEBUG=1 (set by the VS Code launch config)
    # runs jobs in a THREAD of this process so F5 breakpoints hit directly.
    if os.getenv("SEBASTIAN_DEBUG") == "1":
        opts.job_executor_type = agents.JobExecutorType.THREAD
    agents.cli.run_app(opts)
