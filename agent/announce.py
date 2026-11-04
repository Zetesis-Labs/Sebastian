"""Announcements the control room asks a speaker to say.

Two ways in: an announce session (the unit opened a room only to say it, the
dispatch metadata carries {"mode": "announce", "announcement": {...}}) or a
payload on the data channel while a conversation is already open. Both are
spoken verbatim in the voice of the speaker's agent profile, after a chime.
"""

import functools
import json
import math
import struct
from dataclasses import dataclass

MAX_TEXT = 500
CHIME_SAMPLE_RATE = 48000

# Realtime models that can speak given text verbatim (RealtimeCapabilities.
# supports_say) need no TTS; the others use their provider's TTS with the
# profile's voice and key.
_ANNOUNCE_VOICES = {"phonic": "realtime", "openai": "openai_tts", "gemini": "gemini_tts"}


@dataclass(frozen=True)
class Announcement:
    text: str
    reply: bool


def _announcement(raw: object, reply: object = False) -> Announcement | None:
    if not isinstance(raw, str):
        return None
    text = raw.strip()[:MAX_TEXT]
    if not text:
        return None
    return Announcement(text=text, reply=reply is True)


def announcement_from_metadata(metadata: str | None) -> Announcement | None:
    try:
        meta = json.loads(metadata or "")
    except ValueError:
        return None
    if not isinstance(meta, dict) or meta.get("mode") != "announce":
        return None
    body = meta.get("announcement")
    if not isinstance(body, dict):
        return None
    return _announcement(body.get("text"), body.get("reply", False))


def announcement_from_payload(data: bytes) -> Announcement | None:
    try:
        body = json.loads(data.decode())
    except (UnicodeDecodeError, ValueError):
        return None
    if not isinstance(body, dict):
        return None
    return _announcement(body.get("text"))


def announce_voice(provider: str) -> str:
    try:
        return _ANNOUNCE_VOICES[provider]
    except KeyError:
        raise ValueError(f"no announcement voice for provider {provider!r}") from None


@functools.cache
def chime_pcm() -> bytes:
    """A soft rising arpeggio (D5, A5, D6), mono 16-bit at CHIME_SAMPLE_RATE:
    overlapping bell-like notes that swell in and ring out, each a slightly
    detuned pair of sines, so it starts and ends in silence (no click)."""
    rate = CHIME_SAMPLE_RATE
    notes = ((0.00, 587.33), (0.09, 880.00), (0.18, 1174.66))
    attack, decay, total, tail = 0.03, 0.22, 0.78, 0.06
    n = int(total * rate)
    mix = [0.0] * n
    for start, freq in notes:
        first = int(start * rate)
        for i in range(n - first):
            t = i / rate
            env = (0.5 - 0.5 * math.cos(math.pi * t / attack)) if t < attack else math.exp(-(t - attack) / decay)
            w = 2 * math.pi * freq * t
            mix[first + i] += env * (0.5 * math.sin(w) + 0.5 * math.sin(w * 1.003) + 0.12 * math.sin(2 * w))
    peak = max(abs(s) for s in mix)
    gain = 0.24 * 32767 / peak
    fade = int(tail * rate)
    samples = [int(gain * s * min(1.0, (n - 1 - i) / fade)) for i, s in enumerate(mix)]
    return struct.pack(f"<{n}h", *samples)
