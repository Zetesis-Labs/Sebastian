"""The temporary Phonic shim keeps the audio the plugin would drop before Phonic is ready."""
import asyncio
import inspect

from livekit import rtc
from livekit.plugins import phonic

from phonic_buffered import BufferedPhonicSession, PreReadyBuffer


def frame(tag: int, ms: int = 20, rate: int = 16000) -> rtc.AudioFrame:
    samples = rate * ms // 1000
    return rtc.AudioFrame(data=bytes([tag % 256]) * samples * 2, sample_rate=rate, num_channels=1, samples_per_channel=samples)


def tags(frames: list[rtc.AudioFrame]) -> list[int]:
    return [bytes(f.data)[0] for f in frames]


def test_the_buffer_keeps_frames_in_order():
    buf = PreReadyBuffer(max_seconds=1.0)
    for i in range(3):
        buf.push(frame(i))
    assert len(buf) == 3
    assert tags(buf.drain()) == [0, 1, 2]
    assert len(buf) == 0


def test_the_buffer_drops_the_oldest_beyond_its_cap():
    buf = PreReadyBuffer(max_seconds=0.1)
    for i in range(8):
        buf.push(frame(i, ms=20))
    assert tags(buf.drain()) == [3, 4, 5, 6, 7]
    assert buf.dropped_seconds == 0.06


def session_offline(sent: list[rtc.AudioFrame], monkeypatch) -> BufferedPhonicSession:
    monkeypatch.setattr(phonic.realtime.RealtimeSession, "push_audio", lambda self, f: sent.append(f))
    session = object.__new__(BufferedPhonicSession)
    session._ready_to_start = asyncio.Event()
    session._session_should_close = asyncio.Event()
    session._socket = None
    session._pre_ready = PreReadyBuffer(max_seconds=6.0)
    return session


def test_audio_before_ready_is_held_and_sent_first_once_ready(monkeypatch):
    sent: list[rtc.AudioFrame] = []
    session = session_offline(sent, monkeypatch)
    session.push_audio(frame(1))
    session.push_audio(frame(2))
    assert sent == []

    session._socket = object()
    session._ready_to_start.set()
    session.push_audio(frame(3))
    session.push_audio(frame(4))
    assert tags(sent) == [1, 2, 3, 4]


def test_nothing_is_held_or_sent_once_the_session_closes(monkeypatch):
    sent: list[rtc.AudioFrame] = []
    session = session_offline(sent, monkeypatch)
    session._session_should_close.set()
    session.push_audio(frame(1))
    assert sent == [] and len(session._pre_ready) == 0


def test_the_plugin_still_works_the_way_this_shim_assumes():
    """If this fails, livekit-plugins-phonic changed: check whether it now keeps
    pre-ready audio itself (then delete phonic_buffered.py) or adapt the shim."""
    init = inspect.getsource(phonic.realtime.RealtimeSession.__init__)
    push = inspect.getsource(phonic.realtime.RealtimeSession.push_audio)
    make = inspect.getsource(phonic.realtime.RealtimeModel.session)
    assert "self._ready_to_start = asyncio.Event()" in init
    assert "self._session_should_close = asyncio.Event()" in init
    assert "self._socket" in init
    assert "not self._ready_to_start.is_set()" in push and "return" in push
    assert "RealtimeSession(self)" in make and "self._sessions.add(sess)" in make
