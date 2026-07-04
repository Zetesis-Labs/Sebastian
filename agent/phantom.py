import time
from collections import deque
import logging

import telemetry
from text_match import containment as _containment
from text_match import looks_spanish as _looks_spanish
from text_match import norm as _norm

log = logging.getLogger("sebastian.agent.phantom")

ECHO_TAIL_S = 2.5  # reverb + render FIFO after speaking→listening
OVERLAP_WINDOW_S = 20.0  # how far back to compare against the agent's own words
OVERLAP_PHANTOM = 0.55  # near-literal echo: phantom even if the timing missed it
OVERLAP_WEAK = 0.30  # partial echo: only counts combined with echo timing

m_phantom = telemetry.counter(
    "sebastian_agent_phantom_turns_total",
    "User turns classified as agent echo, by reason",
)
m_user_during_speech = telemetry.counter(
    "sebastian_agent_user_turns_during_speech_total",
    "User turns arriving while the agent speaks",
)


class PhantomDetector:
    def __init__(self) -> None:
        self.speaking_since: float | None = None
        self.last_speaking_end: float = 0.0
        self.recent: deque[tuple[float, str]] = deque()

    def on_state(self, new_state: str) -> None:
        now = time.monotonic()
        if new_state == "speaking":
            self.speaking_since = now
        elif self.speaking_since is not None:
            self.last_speaking_end = now
            self.speaking_since = None

    def check(self, role: str, text: str) -> None:
        now = time.monotonic()
        if role == "assistant":
            self.recent.append((now, _norm(text)))
            while self.recent and now - self.recent[0][0] > OVERLAP_WINDOW_S:
                self.recent.popleft()
            return

        reasons = []
        if self.speaking_since is not None:
            reasons.append("during_speech")
            m_user_during_speech.add(1)
        elif now - self.last_speaking_end <= ECHO_TAIL_S:
            reasons.append("echo_tail")

        ref = " ".join(t for ts, t in self.recent if now - ts <= OVERLAP_WINDOW_S)
        ov = _containment(text, ref)

        if ov >= OVERLAP_WEAK:
            reasons.append("overlap")
        if not _looks_spanish(text):
            reasons.append("language")

        timing = "during_speech" in reasons or "echo_tail" in reasons
        if ov >= OVERLAP_PHANTOM or (timing and len(reasons) >= 2):
            primary = "overlap" if ov >= OVERLAP_PHANTOM else reasons[0]
            m_phantom.add(1, {"reason": primary})
            log.warning(
                "PHANTOM turn (reasons=%s ov=%.2f): %s", reasons, ov, text[:120]
            )
