import asyncio
import contextlib
import logging
import os
import wave

from livekit import agents, rtc
from livekit.plugins import noise_cancellation
import preroll
from tasks import spawn as _spawn

log = logging.getLogger("sebastian.agent.audio")

REC_PATH = "/tmp/sebastian_rx.wav"
PREROLL_PATH = "/tmp/sebastian_preroll.wav"
PREROLL_TOPIC = "sebastian.preroll"
DEVICE_IDENTITY = os.getenv("SEBASTIAN_DEVICE_IDENTITY", "esp32-respeaker")
LIVE_SAMPLE_RATE = 24000
LIVE_FRAME_MS = 50
PREROLL_WAIT_TIMEOUT = 6.0
RECORD = os.getenv("SEBASTIAN_RECORD") == "1"
GATE_SILENCE_PEAK = 100

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


def setup_recorder(ctx: agents.JobContext) -> None:
    @ctx.room.on("track_subscribed")
    def _on_track(track: rtc.Track, publication: rtc.TrackPublication, participant: rtc.RemoteParticipant) -> None:
        if track.kind == rtc.TrackKind.KIND_AUDIO and "esp32" in participant.identity:
            _spawn(_record_track(track, REC_PATH))


class SebastianAudioInput(agents.io.AudioInput):
    def __init__(self, room: rtc.Room) -> None:
        super().__init__(label="SebastianMic")
        self._room = room
        # Bounded so a stalled consumer applies backpressure instead of growing
        # unbounded in memory. ~1000 * 50ms frames is generous headroom for the
        # pre-roll burst at hand-off while still capping the queue.
        self._queue: asyncio.Queue[rtc.AudioFrame | None] = asyncio.Queue(maxsize=1000)
        self._preroll_ready = asyncio.Event()
        self.preroll_ready = (
            self._preroll_ready
        )  # exposed: entrypoint skips the greeting on it
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

    async def _consume_preroll(
        self, reader: rtc.ByteStreamReader, participant: str
    ) -> None:
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
            wake_id,
            participant,
            duration_ms,
            len(self._preroll_frames),
        )
        if RECORD:
            _write_wav(PREROLL_PATH, pcm, sample_rate)
            log.info("[preroll] wrote %s", PREROLL_PATH)

    @staticmethod
    def _parse_preroll(
        payload: bytes, participant: str
    ) -> tuple[int, int, bytes] | None:
        parsed = preroll.parse_header(payload)
        if parsed is None:
            log.warning(
                "[preroll] invalid/malformed stream from %s: %s bytes",
                participant,
                len(payload),
            )
            return None
        return parsed.wake_id, parsed.sample_rate, parsed.pcm

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

    def _on_track_subscribed(self, track: rtc.Track, publication: rtc.TrackPublication, participant: rtc.RemoteParticipant) -> None:
        if (
            track.kind != rtc.TrackKind.KIND_AUDIO
            or participant.identity != DEVICE_IDENTITY
        ):
            return

        if self._track_task:
            self._track_task.cancel()
        if self._stream:
            _spawn(self._stream.aclose())

        self._stream = rtc.AudioStream.from_track(
            track=track,
            sample_rate=LIVE_SAMPLE_RATE,
            num_channels=1,
            frame_size_ms=LIVE_FRAME_MS,
            noise_cancellation=noise_cancellation.BVC(),
        )
        self._track_task = asyncio.create_task(
            self._forward_track(participant.identity)
        )

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
                        log.info(
                            "[audio] dropped %s leading gate-silence frames", dropped
                        )
                await self._queue.put(ev.frame)
        except asyncio.CancelledError:
            raise
        finally:
            log.info("[audio] live stream stopped from=%s", participant)
