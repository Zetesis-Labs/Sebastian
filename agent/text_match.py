"""Pure text-matching helpers for phantom-turn detection.

Extracted from agent.py (no LiveKit/telemetry deps) so the logic is unit-testable
without importing — and running the side effects of — the full agent.
"""

import re
import unicodedata

# Common Spanish function words — a cheap "does this look Spanish?" signal used to
# flag mic echo transcribed in the wrong language.
ES_HINTS = {
    "que", "de", "la", "el", "en", "y", "no", "los", "se", "por", "un",
    "una", "para", "con", "es", "si", "como", "esta", "hola", "gracias",
}


def norm(t: str) -> str:
    """Lowercase, strip accents (NFKD), collapse punctuation to spaces."""
    t = "".join(c for c in unicodedata.normalize("NFKD", t.lower()) if not unicodedata.combining(c))
    return re.sub(r"[^\w\s]", " ", t)


def shingles(t: str) -> set[str]:
    """3-word shingles of the normalized text (or the bare words if <3)."""
    w = norm(t).split()
    if len(w) < 3:
        return set(w)
    return {" ".join(w[i : i + 3]) for i in range(len(w) - 2)}


def containment(user_text: str, ref_text: str) -> float:
    """Fraction of the user's shingles that also appear in the reference text."""
    u, r = shingles(user_text), shingles(ref_text)
    return len(u & r) / len(u) if u else 0.0


def looks_spanish(t: str) -> bool:
    """Heuristic: non-Latin script → not Spanish; else enough function words."""
    if any(ord(ch) > 0x24F for ch in t):  # cyrillic / CJK / etc. → clearly not
        return False
    words = norm(t).split()
    if len(words) < 3:
        return True  # too short to judge — benefit of the doubt
    return sum(w in ES_HINTS for w in words) / len(words) >= 0.15
