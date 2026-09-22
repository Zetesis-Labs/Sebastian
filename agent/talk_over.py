"""Pure decision: may the user's voice cut Sebastián off right now?

Split out of agent.py so the rule can be tested against real field timings
without LiveKit, a device or a model.

Background — why this exists at all. With turn_detection="realtime_llm" the
framework discards local VAD interruptions, so audio_input.py runs its own
silero over the frames the model gets. The first version asked only "is the
agent speaking?", which is not the same question as "is the user talking OVER
him": a sentence that ENDED moments ago still has its VAD window open, and the
realtime model starts answering fast enough to walk straight into it. Measured
on 2026-09-22, room sebastian-01a0c8e8:

    11:38:08,814  vad speaking=True      window opens
    11:38:11,395  agent state: speaking  he starts INSIDE the open window
    11:38:11,437  interrupt               42 ms later, on 2.6 s-old speech
    11:38:13,016  vad speaking=False     window closes 1.6 s after the cut

He cut himself off and the user heard nothing. The session that worked minutes
earlier (sebastian-01a0c8e6) differed only in that the window had closed 194 ms
before he started. A 194 ms race decided whether Sebastián answered at all.
"""

import os

MIN_SPEECH_S = float(os.getenv("SEBASTIAN_TALKOVER_MIN_S", "0.4"))
# The agent's own "speaking" state is local and instant; the audio it judges has
# crossed device -> SFU -> agent and is a few hundred ms old. So a voice window
# that opens just after he starts may hold audio captured just BEFORE. This is
# the width of that blind spot, not a tuning knob for sensitivity.
GRACE_S = float(os.getenv("SEBASTIAN_TALKOVER_GRACE_S", "0.5"))


def should_interrupt(
    *,
    full_duplex: bool | None,
    agent_speaking_since: float | None,
    speech_started_at: float | None,
    speech_duration: float,
    min_speech_s: float = MIN_SPEECH_S,
    grace_s: float = GRACE_S,
) -> bool:
    """True only for speech that began after Sebastián did, while he still talks.

    `full_duplex` is the device's EFFECTIVE mode as declared in the pre-roll
    header. None means an older firmware said nothing: stay out, since a unit
    whose mic is gated cannot be talked over and any hit there is a false one.
    All times share one monotonic clock.
    """
    if not full_duplex:
        return False
    if agent_speaking_since is None or speech_started_at is None:
        return False
    if speech_duration < min_speech_s:
        return False
    return speech_started_at > agent_speaking_since + grace_s
