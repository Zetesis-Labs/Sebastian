"""Block C of docs/implementation/14 (T-C1..T-C5): the agent captures and delivers."""
import asyncio
import time
import shutil
from pathlib import Path

import pytest
from aiohttp import web

from meeting_mode import (
    CommandWindow,
    MeetingJob,
    VoiceCommands,
    command_reply,
    meeting_intent,
    voice_start_reply,
    http_session,
    OpusEncoder,
    ServerAPI,
    SilenceNet,
    Spool,
    Uploader,
    meeting_from_metadata,
    run_pipeline,
    watch_silence,
)


# ── T-C1: the mode comes from the dispatch metadata (pure) ─────────────────

def test_meeting_mode_needs_mode_and_id() -> None:
    assert meeting_from_metadata('{"device_id":"68ee","mode":"meeting","meeting_id":"abc-1"}') == MeetingJob("abc-1")
    assert meeting_from_metadata('{"mode":"meeting","meeting_id":"abc-1","silence_s":900}') == MeetingJob("abc-1", 900.0)
    assert meeting_from_metadata('{"mode":"meeting","meeting_id":"abc-1","silence_s":"x"}') == MeetingJob("abc-1")
    assert meeting_from_metadata('{"device_id":"68ee"}') is None
    assert meeting_from_metadata('{"mode":"meeting"}') is None
    assert meeting_from_metadata('{"mode":"meeting","meeting_id":"  "}') is None
    assert meeting_from_metadata('{"mode":"conversation","meeting_id":"abc"}') is None
    assert meeting_from_metadata(None) is None
    assert meeting_from_metadata("not json") is None
    assert meeting_from_metadata("[1]") is None


# ── T-C2: ffmpeg yields a valid, incremental Ogg/Opus stream ───────────────

def _tone(seconds: float, rate: int = 48000) -> bytes:
    import math
    n = int(seconds * rate)
    return b"".join(int(8000 * math.sin(2 * math.pi * 440 * i / rate)).to_bytes(2, "little", signed=True) for i in range(n))


@pytest.mark.skipif(shutil.which("ffmpeg") is None, reason="ffmpeg not installed")
def test_encoder_streams_ogg_pages_as_audio_arrives() -> None:
    async def run() -> tuple[list[bytes], list[int]]:
        pages: list[bytes] = []
        seen_before_finish: list[int] = []

        async def sink(data: bytes) -> None:
            pages.append(data)

        enc = OpusEncoder(sink)
        await enc.start()
        pcm = _tone(3.0)
        step = 48000 * 2 // 10  # 100 ms
        for i in range(0, len(pcm), step):
            await enc.write(pcm[i : i + step])
            await asyncio.sleep(0.01)
        await asyncio.sleep(0.3)
        seen_before_finish.append(sum(map(len, pages)))
        await enc.finish()
        return pages, seen_before_finish

    pages, before = asyncio.run(run())
    out = b"".join(pages)
    assert out.startswith(b"OggS"), "not an Ogg stream"
    assert b"OpusHead" in out[:200], "not Opus inside the Ogg"
    assert out.count(b"OggS") >= 4, "3 s should give at least header pages + 3 audio pages"
    assert before[0] > 0, "pages must flow while recording, not only at the end (RM-15)"
    assert 10_000 < len(out) < 40_000, f"3 s at 48 kbit/s is ~18 KB, got {len(out)}"


# ── T-C3: the upload resumes from the byte the server declares ─────────────

def test_upload_resumes_from_the_offset_the_server_holds(tmp_path: Path) -> None:
    payload = bytes(range(256)) * 40  # 10 KB
    state = {"have": 5, "puts": [], "bodies": [], "attempt": 0}

    async def head(request: web.Request) -> web.Response:
        return web.Response(status=204, headers={"X-Audio-Bytes": str(state["have"]), "X-Meeting-State": state.get("state", "recording")})

    async def put(request: web.Request) -> web.Response:
        state["attempt"] += 1
        state["puts"].append(request.headers.get("Content-Range"))
        assert request.headers["X-Agent-Secret"] == "s3cret"
        if state["attempt"] == 1:
            # The link drops mid-stream: the server kept 3 more bytes.
            await request.content.readexactly(3)
            state["have"] += 3
            assert request.transport is not None
            request.transport.close()
            raise web.HTTPServiceUnavailable()
        body = await request.content.read()
        state["bodies"].append(body)
        state["have"] += len(body)
        return web.Response(status=204, headers={"X-Audio-Bytes": str(state["have"])})

    async def run() -> bool:
        app = web.Application()
        app.router.add_route("HEAD", "/v1/meetings/{id}/audio", head)
        app.router.add_route("PUT", "/v1/meetings/{id}/audio", put)
        runner = web.AppRunner(app)
        await runner.setup()
        site = web.TCPSite(runner, "127.0.0.1", 0)
        await site.start()
        port = site._server.sockets[0].getsockname()[1]  # type: ignore[union-attr]
        spool = Spool(tmp_path / "m.ogg")
        await spool.append(payload)
        await spool.close()
        async with http_session() as http:
            api = ServerAPI(http, f"http://127.0.0.1:{port}", "s3cret", "m1")
            ok = await Uploader(api, spool, backoff_s=0.01).run()
        await runner.cleanup()
        return ok

    assert asyncio.run(run()) is True
    assert state["puts"] == ["bytes 5-*/*", "bytes 8-*/*"]
    assert state["bodies"] == [payload[8:]]
    assert state["have"] == len(payload)

    state.update(have=0, puts=[], attempt=5, state="cut")
    assert asyncio.run(run()) is False, "a finished meeting takes no more audio: give up, keep the spool"
    assert state["puts"] == []


# ── T-C4: the pipeline only captures and delivers — nothing to say, nothing to reply ──

def test_pipeline_encodes_every_frame_and_closes_the_upload(tmp_path: Path) -> None:
    class Passthrough:
        def __init__(self, sink):
            self.sink, self.finished = sink, False

        async def start(self) -> None: ...

        async def write(self, pcm: bytes) -> None:
            await self.sink(pcm)

        async def finish(self) -> None:
            self.finished = True

    class FakeUploader:
        def __init__(self, spool: Spool):
            self.spool, self.got = spool, b""

        async def run(self) -> bool:
            async for chunk in self.spool.follow(0):
                self.got += chunk
            return True

    async def frames():
        for i in range(5):
            yield bytes([i]) * 100
            await asyncio.sleep(0)

    async def run():
        spool = Spool(tmp_path / "m.ogg")
        enc = Passthrough(spool.append)
        up = FakeUploader(spool)
        seen = []
        ok = await run_pipeline(frames(), enc, spool, up, on_frame=seen.append)
        return ok, enc, up, seen, spool

    ok, enc, up, seen, spool = asyncio.run(run())
    assert ok and enc.finished and spool.closed
    assert up.got == b"".join(bytes([i]) * 100 for i in range(5))
    assert len(seen) == 5


# ── T-C5: the silence net (RM-23, decision 5) ──────────────────────────────

def test_silence_net_warns_thirty_seconds_before_and_stops_once() -> None:
    net = SilenceNet(stop_after_s=600, warn_before_s=30)
    assert net.observe(0, True) is None
    assert net.observe(100, False) is None
    assert net.observe(569, False) is None
    assert net.observe(570, False) == "warn"
    assert net.observe(580, False) is None, "warn fires once"
    assert net.observe(590, True) is None, "voice resets the clock"
    assert net.observe(1159, False) is None
    assert net.observe(1160, False) == "warn"
    assert net.observe(1190, False) == "stop"
    assert net.observe(1300, False) is None, "stop fires once"


def test_silence_net_starts_its_clock_at_the_first_observation() -> None:
    net = SilenceNet(stop_after_s=60)
    assert net.observe(1000, False) is None
    assert net.observe(1030, False) == "warn"
    assert net.observe(1060, False) == "stop"


def test_silence_watcher_relays_warn_and_stop_to_the_server() -> None:
    from livekit.agents import vad as agents_vad

    class FakeAPI:
        def __init__(self):
            self.calls = []

        async def warn(self) -> None:
            self.calls.append("warn")

        async def stop(self, reason: str) -> None:
            self.calls.append(f"stop:{reason}")

    def ev(speaking: bool):
        return agents_vad.VADEvent(type=agents_vad.VADEventType.INFERENCE_DONE, samples_index=0, timestamp=0.0, speech_duration=0.0, silence_duration=0.0, speaking=speaking)

    async def events():
        for e in (ev(True), ev(False), ev(False), ev(False)):
            yield e

    clock = iter([0.0, 40.0, 100.0, 130.0])
    api = FakeAPI()
    asyncio.run(watch_silence(events(), SilenceNet(120, 30), api, clock=lambda: next(clock)))
    assert api.calls == ["warn", "stop:silence"]


# ── T-F1: the sentences of RM-04/21, however whisper writes them ──────────

def test_meeting_intent_recognizes_the_spec_variants() -> None:
    for text in ("Sebastián, para la grabación.", "deja de grabar", "Termina la grabación, por favor", "para de grabar ya", "Detén la grabación", "PARA LA GRABACIÓN"):
        assert meeting_intent(text) == "stop", text
    for text in ("Sebastián, graba la reunión", "empieza a grabar", "graba esto", "Grabar la reunión de hoy", "inicia la grabación"):
        assert meeting_intent(text) == "start", text
    for text in ("qué hora es", "enciende la luz del salón", "", "la grabación de ayer fue larga"):
        assert meeting_intent(text) is None, text


def test_voice_start_reply_follows_the_server_answer() -> None:
    ok = voice_start_reply(202)
    assert ok.close and ok.say.startswith("Grabando.") and "Sebastián" not in ok.say
    busy = voice_start_reply(409)
    assert busy.close and "Ya estoy grabando" in busy.say
    no = voice_start_reply(422)
    assert not no.close and no.say.startswith("No puedo grabar ahora")
    down = voice_start_reply(0, "timeout")
    assert not down.close and "timeout" in down.say


# ── T-F2: in meeting mode only the stop order acts; the rest gets the reminder ──

def test_command_reply_stops_or_reminds() -> None:
    assert command_reply("stop", 30) == ("stop", "Grabación guardada, menos de un minuto.")
    assert command_reply("stop", 61) == ("stop", "Grabación guardada, un minuto.")
    assert command_reply("stop", 12 * 60 + 5) == ("stop", "Grabación guardada, 12 minutos.")
    assert command_reply("start", 100) == ("keep", "Ya estoy grabando.")
    assert command_reply(None, 100) == ("keep", "Estoy grabando; dime que pare si quieres hablar.")


def test_voice_commands_hear_the_window_then_act() -> None:
    class FakeAPI:
        def __init__(self):
            self.calls: list[str] = []

        async def warn(self) -> None: ...

        async def stop(self, reason: str) -> None:
            self.calls.append(reason)

    async def run(heard: str):
        api = FakeAPI()
        said: list[str] = []
        got: list[int] = []

        async def transcribe(pcm: bytes, rate: int) -> str:
            got.append(len(pcm))
            return heard

        async def speak(text: str) -> None:
            said.append(text)

        window = CommandWindow(seconds=0.01, sample_rate=1000)  # 20 bytes
        start = time.monotonic()
        voice = VoiceCommands(window, api, transcribe, speak, lambda: start)
        voice.on_wake()
        voice.on_wake()  # a second wake while listening is ignored
        for _ in range(3):
            window.feed(b"\x00" * 10)
            await asyncio.sleep(0)
        await asyncio.sleep(0.05)
        return api.calls, said, got

    calls, said, got = asyncio.run(run("Sebastián, para la grabación"))
    assert calls == ["voice"] and said == ["Grabación guardada, menos de un minuto."] and got == [20]
    calls, said, _ = asyncio.run(run("qué tiempo hace"))
    assert calls == [] and said == ["Estoy grabando; dime que pare si quieres hablar."]
