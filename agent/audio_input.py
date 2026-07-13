import asyncio
import contextlib
import logging
import os
import wave
from collections.abc import Callable
from datetime import datetime

from livekit import agents, rtc
from livekit.agents import vad as agents_vad
from livekit.plugins import noise_cancellation
import preroll
from tasks import spawn as _spawn

log = logging.getLogger("sebastian.agent.audio")


def _bvc_if_available():
    """BVC noise cancellation, only where it can actually run.

    BVC is a LiveKit Cloud service: against a self-hosted SFU it fails with
    `not authenticated` + `failed to initialize the audio filter` and the model
    hears RAW far-field audio (the device intentionally publishes the raw ASR
    beam with no on-chip NS, counting on BVC as the single NS pass — so losing
    it silently degrades transcription badly). Default: BVC on Cloud, none on
    self-hosted. Override with SEBASTIAN_BVC=1/0 to force either way.
    """
    override = os.getenv("SEBASTIAN_BVC")
    if override is not None:
        enabled = override == "1"
    else:
        enabled = ".livekit.cloud" in os.getenv("LIVEKIT_URL", "")
    if not enabled:
        log.warning("[audio] BVC disabled (self-hosted SFU) — model hears the raw beam")
        return None
    return noise_cancellation.BVC()

PREROLL_PATH = "/tmp/sebastian_preroll.wav"
PREROLL_TOPIC = "sebastian.preroll"
DEVICE_IDENTITY = os.getenv("SEBASTIAN_DEVICE_IDENTITY", "esp32-respeaker")
LIVE_SAMPLE_RATE = 24000
LIVE_FRAME_MS = 50
PREROLL_WAIT_TIMEOUT = 6.0
# Per-session mic recording — the local "Egress" for forensics + a dataset
# (failure replay, wake-word hard-negatives, double-talk ground truth; ROADMAP
# §2/§6). One timestamped WAV per session, named with the room for correlation
# with the agent/SFU logs. On by default when a dir is set; SEBASTIAN_RECORD=0
# disables. Full room-composite Egress (both directions → storage) is the
# Cortes-time version. Privacy: local only, gate before any remote deployment.
RECORD_DIR = os.getenv("SEBASTIAN_RECORD_DIR", "recordings")
RECORD = os.getenv("SEBASTIAN_RECORD", "1") != "0"
# Raw-track tap (_mic.wav): starts at the handoff, so it MISSES the pre-roll —
# misleading to listen to. The model-input tee (_model.wav) is the ground truth;
# enable this only to bisect device/SFU defects vs agent-pipeline defects
# (agent/record.py covers the ad-hoc case too).
RECORD_TRACK = os.getenv("SEBASTIAN_RECORD_TRACK", "0") == "1"
GATE_SILENCE_PEAK = 100
# Sustained speech (s) before the talk-over callback fires. Long enough that
# residual echo blips and TV noise don't trigger it; short enough to feel
# instant. Tune with SEBASTIAN_TALKOVER_MIN_S.
TALK_OVER_MIN_SPEECH_S = float(os.getenv("SEBASTIAN_TALKOVER_MIN_S", "0.4"))

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
            os.makedirs(RECORD_DIR, exist_ok=True)
            stamp = datetime.now().strftime("%Y%m%d-%H%M%S")
            path = os.path.join(RECORD_DIR, f"{stamp}_{ctx.room.name}_mic.wav")
            _spawn(_record_track(track, path))


class RecordingAudioOutput(agents.io.AudioOutput):
    """Tee on the agent's OUTGOING audio: what Sebastian says, to a WAV.

    The mic recorder above only captures the device→agent direction; without
    this, recordings have no Sebastian voice in them. Installed by wrapping
    session.output.audio after session.start (next_in_chain forwards frames and
    playback events untouched)."""

    def __init__(self, next_out: agents.io.AudioOutput, path: str) -> None:
        super().__init__(
            label="RecordingTee",
            capabilities=agents.io.AudioOutputCapabilities(pause=next_out.can_pause),
            next_in_chain=next_out,
            sample_rate=None,
        )
        self._path = path
        self._wav: wave.Wave_write | None = None

    async def capture_frame(self, frame: rtc.AudioFrame) -> None:
        await super().capture_frame(frame)
        if self._wav is None:
            self._wav = wave.open(self._path, "wb")
            self._wav.setnchannels(frame.num_channels)
            self._wav.setsampwidth(2)
            self._wav.setframerate(frame.sample_rate)
            log.info("[recorder] writing agent audio to %s", self._path)
        self._wav.writeframes(bytes(frame.data))
        assert self._next_in_chain is not None
        await self._next_in_chain.capture_frame(frame)

    def flush(self) -> None:
        super().flush()
        if self._wav is not None:
            # keep the file valid after every segment; reopen-append isn't
            # needed — wave rewrites the header on close only, so flush the fd
            with contextlib.suppress(Exception):
                self._wav._file.flush()  # type: ignore[attr-defined]
        assert self._next_in_chain is not None
        self._next_in_chain.flush()

    def clear_buffer(self) -> None:
        assert self._next_in_chain is not None
        self._next_in_chain.clear_buffer()

    async def aclose(self) -> None:
        if self._wav is not None:
            with contextlib.suppress(Exception):
                self._wav.close()
            self._wav = None


def setup_output_recorder(session: agents.AgentSession, room_name: str) -> RecordingAudioOutput | None:
    """Wrap the session's audio output with the WAV tee. Call after session.start."""
    cur = session.output.audio
    if cur is None:
        log.warning("[recorder] no audio output to record")
        return None
    os.makedirs(RECORD_DIR, exist_ok=True)
    stamp = datetime.now().strftime("%Y%m%d-%H%M%S")
    tee = RecordingAudioOutput(cur, os.path.join(RECORD_DIR, f"{stamp}_{room_name}_agent.wav"))
    session.output.audio = tee
    return tee


class SebastianAudioInput(agents.io.AudioInput):
    def __init__(self, room: rtc.Room, vad: agents_vad.VAD | None = None) -> None:
        super().__init__(label="SebastianMic")
        self._room = room
        self._model_wav: wave.Wave_write | None = None
        self._model_frames = 0
        # Talk-over detector: with turn_detection="realtime_llm" the framework
        # DISCARDS local VAD interruptions (agent_activity.on_vad_inference_done
        # returns early) and semantic_vad only rules at END of utterance — so
        # full-duplex FELT half-duplex: you couldn't shut the agent up mid-reply.
        # We run silero over the same frames the model gets and let agent.py
        # decide (session.interrupt(), same call the wake-word barge-in uses).
        self.on_talk_over: Callable[[], None] | None = None  # set by agent.py
        self._vad_stream = vad.stream() if vad is not None else None
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

        if self._vad_stream is not None:
            vad_task = asyncio.create_task(self._consume_vad())
            self._tasks.add(vad_task)
            vad_task.add_done_callback(self._tasks.discard)

        # Endpoint mode creates this input lazily (at wake), long after the
        # device's mic track was subscribed — that event already fired, so
        # attach to any existing device audio track now.
        for participant in room.remote_participants.values():
            if participant.identity != DEVICE_IDENTITY:
                continue
            for pub in participant.track_publications.values():
                track = pub.track
                if track is not None and track.kind == rtc.TrackKind.KIND_AUDIO:
                    self._on_track_subscribed(track, pub, participant)

    def on_attached(self) -> None:
        self._attached = True

    def on_detached(self) -> None:
        self._attached = False

    async def __anext__(self) -> rtc.AudioFrame:
        frame = await self._queue.get()
        if frame is None:
            raise StopAsyncIteration
        self._record_model_frame(frame)
        return frame

    async def _consume_vad(self) -> None:
        """Fire on_talk_over after sustained speech. agent.py gates on the
        agent actually speaking, so a callback while he is quiet is a no-op —
        which also debounces: after the interrupt he stops speaking."""
        assert self._vad_stream is not None
        log.info("[talk-over] vad consumer started")
        last_speaking = False
        async for ev in self._vad_stream:
            if ev.type != agents_vad.VADEventType.INFERENCE_DONE:
                continue
            if ev.speaking != last_speaking:
                last_speaking = ev.speaking
                log.info(
                    "[talk-over] vad speaking=%s dur=%.2fs", ev.speaking, ev.speech_duration
                )
            if not ev.speaking or ev.speech_duration < TALK_OVER_MIN_SPEECH_S:
                continue
            if self.on_talk_over is not None:
                self.on_talk_over()

    def _record_model_frame(self, frame: rtc.AudioFrame) -> None:
        """Tee of EXACTLY what the model consumes: pre-roll injected, gate
        silence trimmed, 24 kHz — the ground truth for debugging what Sebastian
        heard. The track-tap recording (_mic.wav) starts at the handoff and
        misses the pre-roll, so it can't answer that question."""
        if not RECORD:
            return
        if self._model_wav is None:
            os.makedirs(RECORD_DIR, exist_ok=True)
            stamp = datetime.now().strftime("%Y%m%d-%H%M%S")
            path = os.path.join(RECORD_DIR, f"{stamp}_{self._room.name}_model.wav")
            self._model_wav = wave.open(path, "wb")
            self._model_wav.setnchannels(frame.num_channels)
            self._model_wav.setsampwidth(2)
            self._model_wav.setframerate(frame.sample_rate)
            log.info("[recorder] writing model-input audio to %s", path)
        self._model_wav.writeframes(bytes(frame.data))
        self._model_frames += 1
        # wave only patches sizes on close; flush the fd so a killed process
        # still leaves recoverable PCM (same trick as RecordingAudioOutput).
        if self._model_frames % 100 == 0:
            with contextlib.suppress(Exception):
                self._model_wav._file.flush()  # type: ignore[attr-defined]

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
        if self._vad_stream is not None:
            await self._vad_stream.aclose()
            self._vad_stream = None
        if self._model_wav is not None:
            with contextlib.suppress(Exception):
                self._model_wav.close()
            self._model_wav = None
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
            noise_cancellation=_bvc_if_available(),
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
                if self._vad_stream is not None:
                    self._vad_stream.push_frame(ev.frame)
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
