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
import json
import logging
import os
import tempfile
import time
from collections.abc import AsyncIterator, Awaitable, Callable
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Protocol

import aiohttp
from livekit import api as lk_api
from livekit import rtc
from livekit.agents import JobContext
from livekit.agents import vad as agents_vad

from tasks import spawn

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
CHUNK = 64 * 1024
FINISHED_STATES = frozenset({"transcribing", "ready", "no_transcript", "cut"})


@dataclass(frozen=True)
class MeetingJob:
    meeting_id: str


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
    log.info("meeting mode: meeting=%s device=%s — capture only, no assistant (RM-12)", job.meeting_id, device_identity)
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
    async with http_session() as http:
        api = ServerAPI(http, API_URL, AGENT_SECRET, job.meeting_id)
        silence = spawn(watch_silence(vad_stream, SilenceNet(SILENCE_S), api))
        try:
            ok = await run_pipeline(mic.frames(), OpusEncoder(spool.append), spool, Uploader(api, spool), on_frame=vad_stream.push_frame)
        finally:
            silence.cancel()
            await vad_stream.aclose()
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
