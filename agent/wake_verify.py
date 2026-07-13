"""Two-stage wake word verification, server side (ROADMAP §7 #1).

The board threshold is a single knob pulled in opposite directions: raise it
and genuine wakes get missed (a real "Sebastián" scored 81%), lower it and the
TV opens sessions. The split: the BOARD keeps recall (threshold loosened to
0.60, wakeword.zig), and THIS module supplies precision — every board fire is
re-verified against the pre-roll audio with real compute, and a fire that was
not "Sebastián" is aborted silently through the same device-initiated close
primitive the endpointing uses.

FAIL-OPEN by design: a session is killed only when the ASR heard *clear speech
that clearly is not the wake word*. Empty, short or ambiguous transcripts pass,
and any transcription error passes. The pre-split behavior is therefore the
floor — this can only remove confident phantoms, never add misses. (The ASR
prompt biases toward hearing "Sebastián", which errs the same direction.)

Every rejected fire saves its audio clip: that is the §7 hard-negative dataset
for retraining microWakeWord, building itself during normal use.
"""

import asyncio
import difflib
import io
import logging
import os
import time
import unicodedata
import wave
from datetime import datetime

from livekit.agents import AgentSession, JobContext
from openai import AsyncOpenAI

import telemetry
from endpointing import close_device_session
from tasks import spawn as _spawn

log = logging.getLogger("sebastian.agent.wake_verify")

m_verify = telemetry.counter(
    "sebastian_agent_wake_verify_total",
    "Board wake fires by server-side verification verdict",
)

ENABLED = os.getenv("SEBASTIAN_WAKE_VERIFY", "1") != "0"
# What a REJECT does. kill: interrupt + device-initiated close (~3s back to
# idle — the phantom antidote). shadow: verdict + clip + metric only, no
# action — for measuring precision without risking real sessions. off: skip
# verification entirely. Runtime-degradable via env, no code change.
ACTION = os.getenv("SEBASTIAN_WAKE_VERIFY_ACTION", "kill")
# Tail of the pre-roll to transcribe. The board fires ON the wake word but the
# pre-roll keeps filling through token fetch + connect (~2s), so the fire sits
# ~2s BEFORE the tail end — 5s covers wake + lead-in (field: 3.5s left whisper
# only the post-fire fragment and a phantom slipped through as "unclear").
# 8s, not 5: a slow connect pushes the fire further from the tail end and the
# wake word falls OUT of the window — field 2026-07-13: a legitimate session
# transcribed with no trace of the wake word and was falsely REJECTed.
WINDOW_S = float(os.getenv("SEBASTIAN_WAKE_VERIFY_WINDOW_S", "8.0"))
ASR_MODEL = os.getenv("SEBASTIAN_WAKE_VERIFY_MODEL", "whisper-1")
ASR_TIMEOUT_S = 5.0
PREROLL_WAIT_S = 6.0  # no pre-roll by then = greeting path, nothing to verify
# Reject only when the transcript carries at least this much alphabetic
# content — the "clear speech" bar of the fail-open policy. 10, not 12: the
# field phantom "de todo esto" (10 chars) is clear speech and must not pass.
MIN_SPEECH_CHARS = 10
# Relative default suits dev (repo cwd, writable); prod mounts an emptyDir and
# points this at it via the chart (the rootfs is read-only there).
PHANTOM_DIR = os.getenv("SEBASTIAN_PHANTOM_DIR", "phantoms")
# Keep the newest N clips — the hard-negative dataset is valuable but bounded
# (prod backs it with a size-limited emptyDir; never grow without limit).
PHANTOM_KEEP = int(os.getenv("SEBASTIAN_PHANTOM_KEEP", "200"))


def _normalize(text: str) -> str:
    decomposed = unicodedata.normalize("NFD", text.lower())
    return "".join(c for c in decomposed if not unicodedata.combining(c))


def matches_wake_word(transcript: str) -> bool:
    """True if the transcript plausibly contains "Sebastián" (generous)."""
    text = _normalize(transcript)
    if "sebas" in text:
        return True
    words = [w.strip(".,;:!?¡¿\"'") for w in text.split()]
    return any(
        difflib.SequenceMatcher(None, w, "sebastian").ratio() >= 0.72
        for w in words
        if len(w) >= 5
    )


def is_clear_speech(transcript: str) -> bool:
    return sum(c.isalpha() for c in _normalize(transcript)) >= MIN_SPEECH_CHARS


def _tail_wav(pcm: bytes, sample_rate: int) -> io.BytesIO:
    tail = pcm[-int(WINDOW_S * sample_rate) * 2 :]
    buf = io.BytesIO()
    with wave.open(buf, "wb") as wav:
        wav.setnchannels(1)
        wav.setsampwidth(2)
        wav.setframerate(sample_rate)
        wav.writeframes(tail)
    buf.seek(0)
    buf.name = "preroll_tail.wav"  # the SDK infers the format from the name
    return buf


def _save_phantom_clip(buf: io.BytesIO, room_name: str) -> None:
    os.makedirs(PHANTOM_DIR, exist_ok=True)
    stamp = datetime.now().strftime("%Y%m%d-%H%M%S")
    path = os.path.join(PHANTOM_DIR, f"{stamp}_{room_name}.wav")
    with open(path, "wb") as f:
        f.write(buf.getbuffer())
    log.info("[wake-verify] hard negative saved: %s", path)
    # Prune oldest beyond the cap (names sort chronologically by stamp).
    clips = sorted(
        os.path.join(PHANTOM_DIR, n)
        for n in os.listdir(PHANTOM_DIR)
        if n.endswith(".wav")
    )
    for old in clips[:-PHANTOM_KEEP]:
        os.remove(old)


async def _transcribe(buf: io.BytesIO) -> str:
    client = AsyncOpenAI()
    try:
        result = await asyncio.wait_for(
            client.audio.transcriptions.create(
                model=ASR_MODEL,
                file=buf,
                # Bias toward hearing the name — errs toward PASS (fail-open).
                prompt="Sebastián",
            ),
            timeout=ASR_TIMEOUT_S,
        )
        return result.text or ""
    finally:
        await client.close()


def setup_wake_verify(ctx: JobContext, session: AgentSession, mic_input) -> None:
    """Re-verify the board's wake fire on the pre-roll; silent-abort phantoms.

    Runs in parallel with the model already processing the audio: a genuine
    wake pays zero latency. A phantom might get a syllable out before the
    verdict lands (~1-2s); interrupt() cuts it and the close tears down.
    """
    if not ENABLED or ACTION == "off":
        return

    async def _verify() -> None:
        try:
            await asyncio.wait_for(mic_input.preroll_ready.wait(), PREROLL_WAIT_S)
        except asyncio.TimeoutError:
            m_verify.add(1, {"verdict": "skipped"})  # greeting path (no wake fire)
            return
        pcm = getattr(mic_input, "preroll_pcm", b"")
        rate = getattr(mic_input, "preroll_sample_rate", 16000)
        if not pcm:
            m_verify.add(1, {"verdict": "skipped"})
            return

        buf = _tail_wav(pcm, rate)
        t0 = time.monotonic()
        try:
            transcript = await _transcribe(buf)
        except Exception as e:
            m_verify.add(1, {"verdict": "error"})
            log.warning("[wake-verify] ASR failed (fail-open, session continues): %r", e)
            return
        elapsed = time.monotonic() - t0

        if matches_wake_word(transcript) or not is_clear_speech(transcript):
            m_verify.add(1, {"verdict": "pass"})
            # Transcript at DEBUG only: living-room speech must not land in Loki.
            log.info("[wake-verify] pass in %.1fs (%d chars)", elapsed, len(transcript))
            log.debug("[wake-verify] pass transcript: %r", transcript[:120])
            return

        m_verify.add(1, {"verdict": "reject"})
        log.info(
            "[wake-verify] REJECT in %.1fs — clear speech, no wake word (%d chars)",
            elapsed,
            len(transcript),
        )
        log.debug("[wake-verify] reject transcript: %r", transcript[:120])
        # Act FIRST (the phantom dies here, ~3s after the false fire), save
        # after — and guarded, so persisting the clip can never break the kill
        # (field 2026-07-13: a read-only fs killed this task between the log
        # and the close, leaving phantoms streaming to the model for 12s).
        if ACTION == "kill":
            try:
                session.interrupt()  # cut any reply already in flight
            except Exception:
                pass
            await close_device_session(ctx, reason="phantom")
        try:
            _save_phantom_clip(buf, ctx.room.name)
        except OSError as e:
            log.warning("[wake-verify] phantom clip not saved: %r", e)

    _spawn(_verify())
