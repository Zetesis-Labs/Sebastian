"""The talk-over rule, checked against the timings actually logged on hardware.

The two session cases below are transcribed from /tmp/agent.log of 2026-09-22;
they are the regression this rule exists for. Times are seconds on one monotonic
clock, offsets kept exactly as measured.
"""

from talk_over import should_interrupt


def _decide(**kw) -> bool:
    base = dict(full_duplex=True, speech_duration=2.0)
    return should_interrupt(**{**base, **kw})


def test_speech_that_started_before_he_did_never_interrupts() -> None:
    """Room sebastian-01a0c8e8: he cut himself off on 2.6 s-old speech."""
    assert not _decide(speech_started_at=8.814, agent_speaking_since=11.395)


def test_the_session_that_worked_is_still_left_alone() -> None:
    """Room sebastian-01a0c8e6: window closed 194 ms before he started."""
    assert not _decide(speech_started_at=54.835, agent_speaking_since=55.929)


def test_real_barge_in_still_cuts_him_off() -> None:
    assert _decide(speech_started_at=12.5, agent_speaking_since=11.395)


def test_speech_inside_the_grace_window_is_treated_as_the_previous_turn() -> None:
    # Audio reaching the agent lags his own state by a few hundred ms, so a
    # window opening 200 ms after he starts may predate him.
    assert not _decide(speech_started_at=11.595, agent_speaking_since=11.395)
    assert _decide(speech_started_at=11.996, agent_speaking_since=11.395)


def test_half_duplex_never_interrupts_even_on_genuine_barge_in() -> None:
    # The mic is gated at the device, so anything heard here is echo or stale.
    assert not _decide(full_duplex=False, speech_started_at=12.5, agent_speaking_since=11.395)


def test_firmware_that_declares_nothing_is_left_alone() -> None:
    # None is "no opinion", not "half duplex" — but staying out is the safe read.
    assert not _decide(full_duplex=None, speech_started_at=12.5, agent_speaking_since=11.395)


def test_a_cough_is_too_short_to_count() -> None:
    assert not _decide(speech_started_at=12.5, agent_speaking_since=11.395, speech_duration=0.1)


def test_nothing_to_interrupt_while_he_is_quiet() -> None:
    assert not _decide(speech_started_at=12.5, agent_speaking_since=None)


def test_no_speech_window_yet() -> None:
    assert not _decide(speech_started_at=None, agent_speaking_since=11.395)
