from preroll import HEADER_BYTES, MAGIC, parse_header


def _frame(version: int = 1, sample_rate: int = 16000, sample_count: int | None = None, wake_id: int = 42, pcm: bytes | None = None) -> bytes:
    if pcm is None:
        pcm = b"\x00\x00" * (sample_count if sample_count is not None else 4)
    if sample_count is None:
        sample_count = len(pcm) // 2
    return (
        MAGIC
        + bytes([version, 0])
        + sample_rate.to_bytes(2, "little")
        + sample_count.to_bytes(4, "little")
        + wake_id.to_bytes(4, "little")
        + pcm
    )


def test_parse_valid_frame() -> None:
    pcm = b"\x01\x02" * 8
    parsed = parse_header(_frame(sample_count=8, pcm=pcm, wake_id=7))
    assert parsed is not None
    assert parsed.wake_id == 7
    assert parsed.sample_rate == 16000
    assert parsed.pcm == pcm


def test_parse_empty_pcm_is_valid() -> None:
    parsed = parse_header(_frame(sample_count=0, pcm=b""))
    assert parsed is not None
    assert parsed.pcm == b""


def test_rejects_bad_magic() -> None:
    frame = _frame(sample_count=4)
    assert parse_header(b"XXXX" + frame[4:]) is None


def test_rejects_short_or_empty_header() -> None:
    assert parse_header(b"") is None
    assert parse_header(b"SBPR") is None
    assert parse_header(MAGIC + b"\x00" * (HEADER_BYTES - 5)) is None


def test_rejects_wrong_version_and_rate() -> None:
    assert parse_header(_frame(version=2, sample_count=4)) is None
    assert parse_header(_frame(sample_rate=48000, sample_count=4)) is None


def test_rejects_pcm_length_mismatch() -> None:
    # header claims 8 samples (16 bytes) but only 4 bytes of pcm follow
    assert parse_header(_frame(sample_count=8, pcm=b"\x00\x00\x00\x00")) is None
