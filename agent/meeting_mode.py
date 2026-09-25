"""Meeting mode — the agent captures and delivers (spec 13, design 14 block C).

The control room dispatches a meeting session with `mode: meeting` and
`meeting_id` in the job metadata. In this mode the agent is not an assistant:
no LLM, no TTS, nothing played on the speaker (RM-12). It subscribes to the
unit's mic, encodes Ogg/Opus with an `ffmpeg` subprocess and streams the
result to the server, which owns the recording (RM-15). The stream is spooled
on local disk so a dropped link resumes from the byte the server declares
(design §3.3) instead of restarting the file. Its VAD is the silence net
(RM-23, decision 5): it warns the server 30 s before and then stops it.
"""
from __future__ import annotations

import asyncio
import contextlib
import io
import json
import logging
import os
import tempfile
import time
import wave
from collections.abc import AsyncIterator, Awaitable, Callable, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Protocol

import aiohttp
from livekit import api as lk_api
from livekit import rtc
from livekit.agents import JobContext
from livekit.agents import vad as agents_vad

from tasks import spawn
from text_match import norm

log = logging.getLogger("sebastian.agent.meeting")

API_URL = os.getenv("SEBASTIAN_API_URL", "http://127.0.0.1:8787").rstrip("/")
AGENT_SECRET = os.getenv("SEBASTIAN_AGENT_SECRET", "")
SILENCE_S = float(os.getenv("SEBASTIAN_MEETING_SILENCE_S", "600"))
WARN_BEFORE_S = 30.0
JOIN_GRACE_S = 45.0
SAMPLE_RATE = 48000
FRAME_MS = 20
BITRATE = "48k"
FFMPEG = os.getenv("SEBASTIAN_FFMPEG", "ffmpeg")
BARGE_TOPIC = "sebastian.barge_in"
COMMAND_WINDOW_S = 4.0
COMMAND_MODEL = os.getenv("SEBASTIAN_COMMAND_MODEL", "whisper-1")
CHUNK = 64 * 1024
FINISHED_STATES = frozenset({"transcribing", "ready", "no_transcript", "cut"})


@dataclass(frozen=True)
class MeetingJob:
    meeting_id: str
    silence_s: float = SILENCE_S  # RM-23: the unit's own window, from its ficha


def meeting_from_metadata(metadata: str | None) -> MeetingJob | None:
    """T-C1 (pure): `mode: meeting` with a meeting id → meeting; else conversation."""
    if not metadata:
        return None
    try:
        body = json.loads(metadata)
    except ValueError:
        return None
    if not isinstance(body, dict) or body.get("mode") != "meeting":
        return None
    meeting_id = body.get("meeting_id")
    if not isinstance(meeting_id, str) or not meeting_id.strip():
        return None
    silence = body.get("silence_s")
    if isinstance(silence, (int, float)) and not isinstance(silence, bool) and silence > 0:
        return MeetingJob(meeting_id=meeting_id.strip(), silence_s=float(silence))
    return MeetingJob(meeting_id=meeting_id.strip())


class SilenceNet:
    """RM-23 (pure): the clock since the last voice → "warn" once, then "stop"."""

    def __init__(self, stop_after_s: float, warn_before_s: float = WARN_BEFORE_S) -> None:
        self.stop_after_s = stop_after_s
        self.warn_before_s = min(warn_before_s, stop_after_s)
        self.last_voice: float | None = None
        self.warned = False
        self.stopped = False

    def observe(self, now: float, speaking: bool) -> str | None:
        if self.last_voice is None or speaking:
            self.last_voice = now
            self.warned = False
            return None
        if self.stopped:
            return None
        quiet = now - self.last_voice
        if quiet >= self.stop_after_s:
            self.stopped = True
            return "stop"
        if not self.warned and quiet >= self.stop_after_s - self.warn_before_s:
            self.warned = True
            return "warn"
        return None


class Spool:
    """The encoded stream on local disk: the uploader follows it as it grows and
    a resume re-reads it from the offset the server declares."""

    def __init__(self, path: Path) -> None:
        self.path = path
        self._file = open(path, "wb")
        self.size = 0
        self.closed = False
        self._grew = asyncio.Condition()

    async def append(self, data: bytes) -> None:
        self._file.write(data)
        self._file.flush()
        self.size += len(data)
        async with self._grew:
            self._grew.notify_all()

    async def close(self) -> None:
        self._file.close()
        self.closed = True
        async with self._grew:
            self._grew.notify_all()

    async def follow(self, offset: int) -> AsyncIterator[bytes]:
        with open(self.path, "rb") as f:
            f.seek(offset)
            while True:
                data = f.read(CHUNK)
                if data:
                    yield data
                    continue
                if self.closed:
                    return
                position = f.tell()
                async with self._grew:
                    await self._grew.wait_for(lambda: self.closed or self.size > position)


class OpusEncoder:
    """`ffmpeg`: s16le mono PCM in → Ogg/Opus at BITRATE out, one page per
    second, so the server file grows as the meeting goes and a cut keeps what
    was said up to it (RM-15)."""

    def __init__(self, sink: Callable[[bytes], Awaitable[None]], sample_rate: int = SAMPLE_RATE, ffmpeg: str = FFMPEG) -> None:
        self._sink = sink
        self._sample_rate = sample_rate
        self._ffmpeg = ffmpeg
        self._proc: asyncio.subprocess.Process | None = None
        self._pump: asyncio.Task[None] | None = None

    async def start(self) -> None:
        self._proc = await asyncio.create_subprocess_exec(
            self._ffmpeg, "-hide_banner", "-loglevel", "error",
            "-f", "s16le", "-ar", str(self._sample_rate), "-ac", "1", "-i", "pipe:0",
            "-c:a", "libopus", "-b:a", BITRATE, "-application", "voip", "-frame_duration", "20",
            "-page_duration", "1000000", "-flush_packets", "1", "-f", "ogg", "pipe:1",
            stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE,
        )
        self._pump = asyncio.create_task(self._drain())

    async def _drain(self) -> None:
        assert self._proc is not None and self._proc.stdout is not None
        while True:
            data = await self._proc.stdout.read(CHUNK)
            if not data:
                return
            await self._sink(data)

    async def write(self, pcm: bytes) -> None:
        assert self._proc is not None and self._proc.stdin is not None
        self._proc.stdin.write(pcm)
        await self._proc.stdin.drain()

    async def finish(self) -> None:
        if self._proc is None:
            return
        assert self._proc.stdin is not None and self._proc.stderr is not None
        with contextlib.suppress(Exception):
            self._proc.stdin.close()
        if self._pump is not None:
            await self._pump
        errors = (await self._proc.stderr.read()).decode(errors="replace").strip()
        code = await self._proc.wait()
        if code != 0 or errors:
            log.warning("[encoder] ffmpeg exit=%s: %s", code, errors)


class ServerAPI:
    """The server's agent-facing routes (design §3.3), all under X-Agent-Secret."""

    def __init__(self, http: aiohttp.ClientSession, base_url: str, secret: str, meeting_id: str) -> None:
        self._http = http
        self._audio = f"{base_url}/v1/meetings/{meeting_id}/audio"
        self._base = f"{base_url}/v1/meetings/{meeting_id}"
        self._headers = {"X-Agent-Secret": secret}

    async def audio_offset(self) -> tuple[int, str] | None:
        """How much the server holds and the meeting's state, or None if unreachable."""
        async with self._http.head(self._audio, headers=self._headers) as res:
            if res.status != 204:
                log.warning("[upload] HEAD audio → %s", res.status)
                return None
            return int(res.headers.get("X-Audio-Bytes", "0")), res.headers.get("X-Meeting-State", "")

    def put_audio(self, offset: int, body: AsyncIterator[bytes]) -> Any:
        headers = {**self._headers, "Content-Type": "audio/ogg", "Content-Range": f"bytes {offset}-*/*"}
        return self._http.put(self._audio, data=body, headers=headers, timeout=aiohttp.ClientTimeout(total=None))

    async def warn(self) -> None:
        async with self._http.post(f"{self._base}/warn", headers=self._headers) as res:
            log.info("[silence] warn → %s", res.status)

    async def stop(self, reason: str) -> None:
        async with self._http.post(f"{self._base}/stop", headers=self._headers, json={"reason": reason}) as res:
            log.info("[silence] stop(%s) → %s", reason, res.status)


def http_session() -> aiohttp.ClientSession:
    """aiohttp silently retries an idempotent request (PUT) once when the server
    closes the connection — with the already-drained streaming body, so the
    server would see an empty upload. Only its private flag turns that off."""
    session = aiohttp.ClientSession(connector=aiohttp.TCPConnector(force_close=True))
    session._retry_connection = False
    return session


class Uploader:
    """Streams the spool to the server; on any break it asks the server how
    much it holds and resumes from there (T-C3). Ends when the spool is closed
    and the server has acknowledged everything."""

    def __init__(self, api: ServerAPI, spool: Spool, backoff_s: float = 1.0) -> None:
        self._api = api
        self._spool = spool
        self._backoff = backoff_s

    async def run(self) -> bool:
        attempt = 0
        while True:
            try:
                held = await self._api.audio_offset()
                if held is not None:
                    offset, state = held
                    if state in FINISHED_STATES:
                        log.error("[upload] the meeting is %s — the server takes no more audio", state)
                        return False
                    if offset > self._spool.size and self._spool.closed:
                        log.error("[upload] the server holds %d bytes, we have %d — giving up", offset, self._spool.size)
                        return False
                    async with self._api.put_audio(offset, self._spool.follow(offset)) as res:
                        if res.status == 204:
                            log.info("[upload] complete: %s bytes", res.headers.get("X-Audio-Bytes"))
                            return True
                        if res.status in (401, 404):
                            log.error("[upload] refused (%s) — nothing to resume", res.status)
                            return False
                        log.warning("[upload] PUT → %s, retrying", res.status)
            except (aiohttp.ClientError, asyncio.TimeoutError, OSError) as e:
                log.warning("[upload] link broke: %r — resuming", e)
            attempt += 1
            await asyncio.sleep(min(self._backoff * 2 ** min(attempt, 6), 15.0))


class Encoder(Protocol):
    async def start(self) -> None: ...
    async def write(self, pcm: bytes) -> None: ...
    async def finish(self) -> None: ...


class Delivery(Protocol):
    async def run(self) -> bool: ...


async def run_pipeline(
    frames: AsyncIterator[rtc.AudioFrame | bytes],
    encoder: Encoder,
    spool: Spool,
    uploader: Delivery,
    on_frame: Callable[[Any], None] | None = None,
) -> bool:
    """Capture → encode → spool → upload, until the frames end. T-C4: this is
    the whole of what the agent does in a meeting — there is no speech path."""
    await encoder.start()
    delivery = asyncio.create_task(uploader.run())
    try:
        async for frame in frames:
            pcm = frame if isinstance(frame, bytes) else bytes(frame.data)
            await encoder.write(pcm)
            if on_frame is not None:
                on_frame(frame)
    finally:
        await encoder.finish()
        await spool.close()
    return await delivery


class SilenceAPI(Protocol):
    async def warn(self) -> None: ...
    async def stop(self, reason: str) -> None: ...


async def watch_silence(
    events: AsyncIterator[agents_vad.VADEvent],
    net: SilenceNet,
    api: SilenceAPI,
    clock: Callable[[], float] = time.monotonic,
) -> None:
    speaking = False
    async for ev in events:
        if ev.type != agents_vad.VADEventType.INFERENCE_DONE:
            continue
        if ev.speaking != speaking:
            speaking = ev.speaking
            log.info("[silence] voice %s", "on" if speaking else "off")
        was_warned = net.warned
        verdict = net.observe(clock(), ev.speaking)
        if was_warned and not net.warned:
            log.info("[silence] voice again — the clock restarts")
        if verdict == "warn":
            log.info("[silence] %.0f s without voice — warning the unit", net.stop_after_s - net.warn_before_s)
            await api.warn()
        elif verdict == "stop":
            log.info("[silence] %.0f s without voice — stopping (RM-23)", net.stop_after_s)
            await api.stop("silence")


class MicCapture:
    """The unit's mic as PCM frames; ends when the unit leaves or the room drops."""

    def __init__(self, room: rtc.Room, device_identity: str) -> None:
        self._room = room
        self._identity = device_identity
        self._queue: asyncio.Queue[rtc.AudioFrame | None] = asyncio.Queue(maxsize=500)
        self._task: asyncio.Task[None] | None = None
        self._ended = False
        room.on("track_subscribed", self._on_track)
        room.on("participant_disconnected", self._on_left)
        room.on("disconnected", lambda *_: self.end())
        for participant in room.remote_participants.values():
            if participant.identity != device_identity:
                continue
            for pub in participant.track_publications.values():
                if pub.track is not None and pub.track.kind == rtc.TrackKind.KIND_AUDIO:
                    self._on_track(pub.track, pub, participant)

    def _on_track(self, track: rtc.Track, publication: rtc.TrackPublication, participant: rtc.RemoteParticipant) -> None:
        if track.kind != rtc.TrackKind.KIND_AUDIO or participant.identity != self._identity:
            return
        if self._task is not None:
            self._task.cancel()
        stream = rtc.AudioStream.from_track(track=track, sample_rate=SAMPLE_RATE, num_channels=1, frame_size_ms=FRAME_MS)
        self._task = spawn(self._pump(stream))

    async def _pump(self, stream: rtc.AudioStream) -> None:
        log.info("[capture] mic stream started from=%s", self._identity)
        try:
            async for ev in stream:
                if self._ended:
                    return
                await self._queue.put(ev.frame)
        finally:
            await stream.aclose()
            log.info("[capture] mic stream stopped")

    def _on_left(self, participant: rtc.RemoteParticipant) -> None:
        if participant.identity == self._identity:
            log.info("[capture] the unit left the room")
            self.end()

    def end(self) -> None:
        if self._ended:
            return
        self._ended = True
        with contextlib.suppress(asyncio.QueueFull):
            self._queue.put_nowait(None)

    async def frames(self) -> AsyncIterator[rtc.AudioFrame | bytes]:
        while True:
            frame = await self._queue.get()
            if frame is None:
                return
            yield frame


async def run_meeting(ctx: JobContext, job: MeetingJob, device_identity: str, vad: agents_vad.VAD) -> None:
    """The shell: join, capture until the unit leaves, deliver, clean the room."""
    log.info("meeting mode: meeting=%s device=%s silence=%.0fs — capture only, no assistant (RM-12)", job.meeting_id, device_identity, job.silence_s)
    if not AGENT_SECRET:
        log.error("SEBASTIAN_AGENT_SECRET is not set — the server will refuse the audio")
    mic = MicCapture(ctx.room, device_identity)
    await ctx.connect()
    try:
        await asyncio.wait_for(ctx.wait_for_participant(identity=device_identity), timeout=JOIN_GRACE_S)
    except asyncio.TimeoutError:
        log.info("the unit never joined within %.0f s — leaving", JOIN_GRACE_S)
        await _delete_room(ctx)
        return
    spool = Spool(Path(tempfile.gettempdir()) / f"sebastian-meeting-{job.meeting_id}.ogg")
    vad_stream = vad.stream()
    started = time.monotonic()
    speaker = Speaker(ctx.room)
    async with http_session() as http:
        api = ServerAPI(http, API_URL, AGENT_SECRET, job.meeting_id)
        silence = spawn(watch_silence(vad_stream, SilenceNet(job.silence_s), api))
        window = CommandWindow()
        voice = VoiceCommands(window, api, transcribe_command, speaker.say, lambda: started)

        @ctx.room.on("data_received")
        def _on_data(packet: rtc.DataPacket) -> None:
            if packet.topic == BARGE_TOPIC:
                voice.on_wake()

        def on_frame(frame: rtc.AudioFrame) -> None:
            vad_stream.push_frame(frame)
            window.feed(bytes(frame.data))

        try:
            ok = await run_pipeline(mic.frames(), OpusEncoder(spool.append), spool, Uploader(api, spool), on_frame=on_frame)
        finally:
            silence.cancel()
            await vad_stream.aclose()
            await speaker.aclose()
    if ok:
        spool.path.unlink(missing_ok=True)
    else:
        log.warning("[upload] audio kept at %s for manual recovery", spool.path)
    await _delete_room(ctx)


async def _delete_room(ctx: JobContext) -> None:
    try:
        await ctx.api.room.delete_room(lk_api.DeleteRoomRequest(room=ctx.room.name))
    except Exception as e:
        log.info("room cleanup: %r", e)


# ── voice (block F, RM-04/05/21) ─────────────────────────────────────────────

STOP_PHRASES = (
    "para la grabacion", "parar la grabacion", "para de grabar", "deja de grabar", "dejar de grabar",
    "termina la grabacion", "terminar la grabacion", "termina de grabar", "acaba la grabacion",
    "deten la grabacion", "detener la grabacion", "para grabacion", "stop recording", "stop the recording",
)
START_PHRASES = (
    "graba la reunion", "grabar la reunion", "graba esta reunion", "graba reunion", "empieza a grabar",
    "empezar a grabar", "graba esto", "grabame esto", "empieza la grabacion", "inicia la grabacion",
    "comienza a grabar", "start recording",
)


def meeting_intent(text: str) -> str | None:
    """T-F1 (pure): what the sentence asks about the recording, or None."""
    words = " ".join(norm(text).split())
    if any(p in words for p in STOP_PHRASES):
        return "stop"
    if any(p in words for p in START_PHRASES):
        return "start"
    return None


def command_reply(intent: str | None, recorded_s: float) -> tuple[str, str]:
    """T-F2 (pure): what the recording agent does and says to a sentence heard
    after the wake word: only the stop order acts (RM-21); anything else gets
    the fixed reminder."""
    if intent == "stop":
        minutes = int(recorded_s // 60)
        length = "menos de un minuto" if minutes < 1 else ("un minuto" if minutes == 1 else f"{minutes} minutos")
        return "stop", f"Grabación guardada, {length}."
    if intent == "start":
        return "keep", "Ya estoy grabando."
    return "keep", "Estoy grabando; dime que pare si quieres hablar."


@dataclass(frozen=True)
class VoiceStart:
    say: str
    close: bool  # the conversation ends so the unit can start the recording


NOT_ASKED_TO_RECORD = VoiceStart(
    "No he entendido que quieras grabar. Si quieres, dime: graba la reunión.", False
)


RECORDING_STEMS = ("grab", "grav", "record", "reunion")


def voice_start_asked(user_turns: Sequence[str]) -> bool:
    """T-F1c (pure): the model's start_meeting_recording call is honored only
    when the user's latest turn talks about recording — Gemini once called it
    on a pre-roll transcribed as Tamil (cortes, 2026-09-25). Stems, not exact
    phrases: the transcript writes "graves", "grava" and paraphrases."""
    if not user_turns:
        return False
    words = norm(user_turns[-1]).split()
    return any(word.startswith(RECORDING_STEMS) for word in words)


def voice_start_reply(status: int, detail: str = "") -> VoiceStart:
    """T-F1b (pure): the server's answer to a start by voice → what to say (RM-04/05/44)."""
    if status == 202:
        return VoiceStart("Grabando. Para parar, pide que pare la grabación con la palabra de activación, o pulsa el botón: corta y luego larga.", True)
    if status == 409:
        return VoiceStart("Ya estoy grabando esta reunión.", True)
    if status == 422:
        return VoiceStart("No puedo grabar ahora: este altavoz no está en perfil agente o no está adoptado.", False)
    return VoiceStart(f"No puedo grabar ahora: el control room no responde{f' ({detail})' if detail else ''}.", False)


async def request_meeting_by_voice(device_id: str) -> VoiceStart:
    """RM-04: the conversation agent asks the control room to record on its unit."""
    try:
        async with http_session() as http:
            async with http.post(f"{API_URL}/v1/meetings", headers={"X-Agent-Secret": AGENT_SECRET}, json={"deviceId": device_id}, timeout=aiohttp.ClientTimeout(total=10)) as res:
                detail = ""
                if res.status >= 400:
                    with contextlib.suppress(Exception):
                        detail = str((await res.json()).get("detail", ""))
                log.info("[voice] start meeting on %s → %s %s", device_id, res.status, detail)
                return voice_start_reply(res.status, detail)
    except (aiohttp.ClientError, asyncio.TimeoutError, OSError) as e:
        log.warning("[voice] start meeting failed: %r", e)
        return voice_start_reply(0, str(e))


class CommandWindow:
    """The seconds after the wake word: armed by the barge-in, filled by the
    mic frames, handed over once full."""

    def __init__(self, seconds: float = COMMAND_WINDOW_S, sample_rate: int = SAMPLE_RATE) -> None:
        self._need = int(seconds * sample_rate) * 2
        self._buf = bytearray()
        self.sample_rate = sample_rate
        self.armed = False
        self.ready = asyncio.Event()

    def arm(self) -> bool:
        if self.armed:
            return False
        self._buf.clear()
        self.ready.clear()
        self.armed = True
        return True

    def feed(self, pcm: bytes) -> None:
        if not self.armed:
            return
        self._buf += pcm
        if len(self._buf) >= self._need:
            self.armed = False
            self.ready.set()

    def take(self) -> bytes:
        data = bytes(self._buf)
        self._buf.clear()
        return data


def pcm_to_wav(pcm: bytes, sample_rate: int) -> io.BytesIO:
    buf = io.BytesIO()
    with wave.open(buf, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(sample_rate)
        w.writeframes(pcm)
    buf.seek(0)
    buf.name = "command.wav"
    return buf


async def transcribe_command(pcm: bytes, sample_rate: int) -> str:
    from openai import AsyncOpenAI

    client = AsyncOpenAI()
    try:
        result = await asyncio.wait_for(
            client.audio.transcriptions.create(model=COMMAND_MODEL, file=pcm_to_wav(pcm, sample_rate), prompt="para la grabación, deja de grabar"),
            timeout=15,
        )
        return result.text or ""
    finally:
        await client.close()


class Speaker:
    """The recording agent's only voice: short confirmations over a track of
    its own (no AgentSession in meeting mode)."""

    def __init__(self, room: rtc.Room) -> None:
        self._room = room
        self._source: rtc.AudioSource | None = None
        self._tts: Any = None

    async def say(self, text: str) -> None:
        from livekit.plugins import openai as openai_plugin

        if self._tts is None:
            self._tts = openai_plugin.TTS()
        log.info("[voice] saying: %r", text)
        async for ev in self._tts.synthesize(text):
            if self._source is None:
                self._source = rtc.AudioSource(ev.frame.sample_rate, ev.frame.num_channels)
                track = rtc.LocalAudioTrack.create_audio_track("sebastian-voice", self._source)
                await self._room.local_participant.publish_track(track, rtc.TrackPublishOptions(source=rtc.TrackSource.SOURCE_MICROPHONE))
            await self._source.capture_frame(ev.frame)
        if self._source is not None:
            await self._source.wait_for_playout()

    async def aclose(self) -> None:
        if self._tts is not None:
            with contextlib.suppress(Exception):
                await self._tts.aclose()


class VoiceCommands:
    """RM-21: after the unit relays a wake word, hear the next seconds,
    understand the order and answer; only "stop" changes anything."""

    def __init__(
        self,
        window: CommandWindow,
        api: SilenceAPI,
        transcribe: Callable[[bytes, int], Awaitable[str]],
        speak: Callable[[str], Awaitable[None]],
        started_at: Callable[[], float],
    ) -> None:
        self.window = window
        self._api = api
        self._transcribe = transcribe
        self._speak = speak
        self._started_at = started_at
        self.stopped = False

    def on_wake(self) -> None:
        if self.window.arm():
            log.info("[voice] wake word — listening %.0f s for the order", COMMAND_WINDOW_S)
            spawn(self.handle())

    async def handle(self) -> None:
        try:
            await asyncio.wait_for(self.window.ready.wait(), timeout=COMMAND_WINDOW_S + 5)
        except asyncio.TimeoutError:
            self.window.armed = False
            return
        pcm = self.window.take()
        try:
            text = await self._transcribe(pcm, self.window.sample_rate)
        except Exception as e:
            log.warning("[voice] command transcription failed: %r", e)
            return
        intent = meeting_intent(text)
        action, phrase = command_reply(intent, time.monotonic() - self._started_at())
        log.info("[voice] heard %r → %s", text, action)
        with contextlib.suppress(Exception):
            await self._speak(phrase)
        if action == "stop" and not self.stopped:
            self.stopped = True
            await self._api.stop("voice")
