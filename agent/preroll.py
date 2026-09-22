"""Pure parser for the device pre-roll stream header.

The firmware sends, at the mic hand-off, a byte stream framed as:

    "SBPR" | version(u8) | flags(u8) | sample_rate(u16 LE) |
    sample_count(u32 LE) | wake_id(u32 LE) | pcm(sample_count * 2 bytes, s16 LE)

`flags` was a reserved byte, always zero, so an older firmware reports nothing
and `full_duplex` comes back None — "no opinion", which is NOT the same as
half-duplex and must not be read as one.

Extracted from agent.py so the framing is unit-testable without LiveKit. Returns
None on any malformed input (the caller logs); never raises on bad bytes.
"""

MAGIC = b"SBPR"
HEADER_BYTES = 16
VERSION = 1
SAMPLE_RATE = 16000

FLAG_DECLARED = 0x01
FLAG_FULL_DUPLEX = 0x02


def duplex_from_flags(flags: int) -> bool | None:
    """True/False once the device states its EFFECTIVE mode, None if it can't.

    Only the device knows: firmware downgrades a full-duplex request to half
    when the AEC config failed or the beam is adaptive (fullDuplexAllowed in
    app.zig), so the desired config is not an answer.
    """
    if not flags & FLAG_DECLARED:
        return None
    return bool(flags & FLAG_FULL_DUPLEX)


class ParsedPreRoll:
    __slots__ = ("wake_id", "sample_rate", "pcm", "full_duplex")

    def __init__(self, wake_id: int, sample_rate: int, pcm: bytes, full_duplex: bool | None = None) -> None:
        self.wake_id = wake_id
        self.sample_rate = sample_rate
        self.pcm = pcm
        self.full_duplex = full_duplex


def parse_header(payload: bytes) -> ParsedPreRoll | None:
    """Validate + decode the pre-roll frame. None if malformed."""
    if len(payload) < HEADER_BYTES or payload[:4] != MAGIC:
        return None

    version = payload[4]
    flags = payload[5]
    sample_rate = int.from_bytes(payload[6:8], "little")
    sample_count = int.from_bytes(payload[8:12], "little")
    wake_id = int.from_bytes(payload[12:16], "little")
    pcm = payload[HEADER_BYTES:]

    if version != VERSION or sample_rate != SAMPLE_RATE or len(pcm) != sample_count * 2:
        return None

    return ParsedPreRoll(wake_id, sample_rate, pcm, duplex_from_flags(flags))
