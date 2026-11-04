import json
import struct

import pytest

from announce import (
    CHIME_SAMPLE_RATE,
    MAX_TEXT,
    Announcement,
    announce_voice,
    announcement_from_metadata,
    announcement_from_payload,
    chime_pcm,
)


def _meta(**announcement):
    return json.dumps({"device_id": "e072a1f96ef0", "mode": "announce", "announcement": announcement})


def test_an_announce_session_carries_its_text_and_whether_it_waits_for_a_reply():
    assert announcement_from_metadata(_meta(text="La lavadora ha terminado", reply=True)) == Announcement(
        text="La lavadora ha terminado", reply=True
    )


def test_an_announcement_without_reply_only_speaks():
    assert announcement_from_metadata(_meta(text="Hola")) == Announcement(text="Hola", reply=False)


@pytest.mark.parametrize(
    "metadata",
    [
        None,
        "",
        "no es json",
        json.dumps(["announce"]),
        json.dumps({"device_id": "e072a1f96ef0"}),
        json.dumps({"mode": "meeting", "meeting_id": "x"}),
        json.dumps({"mode": "announce"}),
        _meta(text="   "),
        _meta(text=7),
    ],
)
def test_anything_else_is_not_an_announcement(metadata):
    assert announcement_from_metadata(metadata) is None


def test_the_text_is_trimmed_and_capped():
    assert announcement_from_metadata(_meta(text="  hola  ")).text == "hola"
    assert len(announcement_from_metadata(_meta(text="a" * (MAX_TEXT + 50))).text) == MAX_TEXT


def test_an_announcement_pushed_into_a_conversation_is_read_from_the_data_payload():
    assert announcement_from_payload(b'{"text": " Ya ha llegado el paquete "}') == Announcement(
        text="Ya ha llegado el paquete", reply=False
    )
    assert announcement_from_payload(b"{}") is None
    assert announcement_from_payload(b"\xff") is None


@pytest.mark.parametrize(
    ("provider", "voice"),
    [("phonic", "realtime"), ("openai", "openai_tts"), ("gemini", "gemini_tts")],
)
def test_each_provider_says_announcements_in_its_own_voice(provider, voice):
    assert announce_voice(provider) == voice


def test_an_unknown_provider_has_no_announcement_voice():
    with pytest.raises(ValueError):
        announce_voice("elevenlabs")


def test_the_chime_is_short_mono_16_bit_and_starts_and_ends_in_silence():
    pcm = chime_pcm()
    samples = struct.unpack(f"<{len(pcm) // 2}h", pcm)
    duration_s = len(samples) / CHIME_SAMPLE_RATE
    assert 0.2 <= duration_s <= 0.8
    assert abs(samples[0]) < 200 and abs(samples[-1]) < 200
    peak = max(abs(s) for s in samples)
    assert 4000 < peak < 20000, "audible, but well below full scale"


def test_the_chime_swells_in_and_rings_out_instead_of_beeping():
    pcm = chime_pcm()
    samples = struct.unpack(f"<{len(pcm) // 2}h", pcm)
    peak = max(abs(s) for s in samples)
    ms = CHIME_SAMPLE_RATE // 1000
    assert max(abs(s) for s in samples[: 5 * ms]) < peak / 3, "no hard onset"
    assert max(abs(s) for s in samples[-len(samples) // 4 :]) < peak / 3, "it decays, not a flat tone"
