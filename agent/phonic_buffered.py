"""TEMPORARY — keep the audio livekit-plugins-phonic drops before Phonic is ready.

The plugin's push_audio discards every frame pushed before Phonic answers
`ready_to_start_conversation`, 1–1.5 s after connecting: the pre-roll with the
wake word and the start of the command, so "Okay Nabu, <command>" said in one
breath never reaches the model. This session holds those frames and sends
them, in order, ahead of the first frame pushed once Phonic is ready.

It relies on the plugin's private `_ready_to_start`, `_socket` and
`_session_should_close`; tests/test_phonic_buffered.py fails if they change.
Delete this module once the plugin keeps pre-ready audio itself.
"""
from __future__ import annotations

import logging
from collections import deque

from livekit import rtc
from livekit.plugins import phonic

log = logging.getLogger("sebastian.agent.phonic")

# Pre-roll (2.5 s) + Phonic's connect-to-ready (~1.5 s) with margin.
HOLD_SECONDS = 6.0


class PreReadyBuffer:
    """Frames in arrival order, capped by duration; the oldest go first."""

    def __init__(self, max_seconds: float) -> None:
        self._max = max_seconds
        self._frames: deque[rtc.AudioFrame] = deque()
        self._seconds = 0.0
        self.dropped_seconds = 0.0

    def __len__(self) -> int:
        return len(self._frames)

    @staticmethod
    def _duration(f: rtc.AudioFrame) -> float:
        return f.samples_per_channel / f.sample_rate

    def push(self, f: rtc.AudioFrame) -> None:
        self._frames.append(f)
        self._seconds += self._duration(f)
        while self._seconds > self._max + 1e-9 and self._frames:
            old = self._frames.popleft()
            self._seconds -= self._duration(old)
            self.dropped_seconds = round(self.dropped_seconds + self._duration(old), 3)

    def drain(self) -> list[rtc.AudioFrame]:
        frames = list(self._frames)
        self._frames.clear()
        self._seconds = 0.0
        return frames


class BufferedPhonicSession(phonic.realtime.RealtimeSession):
    def __init__(self, model: phonic.realtime.RealtimeModel) -> None:
        super().__init__(model)
        self._pre_ready = PreReadyBuffer(HOLD_SECONDS)

    def push_audio(self, frame: rtc.AudioFrame) -> None:
        if self._session_should_close.is_set():
            return
        if not self._ready_to_start.is_set() or not self._socket:
            self._pre_ready.push(frame)
            return
        if len(self._pre_ready):
            held = self._pre_ready.drain()
            log.info(
                "[phonic] sending %.2fs held until ready (%d frames, %.2fs dropped over the cap)",
                sum(f.samples_per_channel / f.sample_rate for f in held), len(held), self._pre_ready.dropped_seconds,
            )
            for f in held:
                super().push_audio(f)
        super().push_audio(frame)


class BufferedPhonicRealtimeModel(phonic.realtime.RealtimeModel):
    def session(self, *, turn_detection_disabled: bool = False) -> phonic.realtime.RealtimeSession:
        sess = BufferedPhonicSession(self)
        self._sessions.add(sess)
        return sess
