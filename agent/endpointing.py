"""Server-side endpointing (ROADMAP §5 #3): the agent decides when the
conversation is over and tells the device to close.

Amplitude is not voice. The device's level-based silence timeout cut sessions
mid-pause (soft speech reads as silence) and let background noise hold them
open. The agent sees the real signals — transcribed turns, its own speech
state, whether its last utterance expects an answer — so the idle decision
lives here and the firmware timeout demotes to a last-resort watchdog (60s,
session_core.zig), for when this worker is dead or mute.

The close travels as a data-channel command ("close" on sebastian.agent_state)
and the DEVICE disconnects itself: a server-side room delete can leave the
ESP32 client stuck CONNECTED forever (esp-webrtc-solution#186 family), so
delete_room runs only afterwards as room cleanup, never as the mechanism.

Side effect worth knowing: a phantom session (wake with no user in it) never
produces a turn, so the idle window closes it silently — the same signal
sebastian_agent_sessions_ended_total{short=} tracks, now with a bounded cost.
"""

import asyncio
import logging
import os
import time
from dataclasses import dataclass
from typing import Any

import aiohttp
from livekit import api
from livekit.agents import AgentSession, JobContext

import telemetry
from tasks import spawn as _spawn

log = logging.getLogger("sebastian.agent.endpointing")

m_close = telemetry.counter(
    "sebastian_agent_idle_close_total",
    "Sessions closed by agent-side idle endpointing",
)

AGENT_STATE_TOPIC = "sebastian.agent_state"  # the channel instrumentation.py publishes on

# Base idle window: matches the 12s the old firmware timeout trained users on,
# but measured on turns and speech instead of mic amplitude.
IDLE_CLOSE_S = float(os.getenv("SEBASTIAN_IDLE_CLOSE_S", "12"))
# When the agent's last utterance was a question, the user is *expected* to be
# thinking — give them room before hanging up on them.
QUESTION_IDLE_CLOSE_S = float(os.getenv("SEBASTIAN_QUESTION_IDLE_CLOSE_S", "25"))
CLOSE_GRACE_S = 3.0  # device teardown time before the room cleanup
POLL_S = 0.5


async def close_device_session(
    ctx: JobContext, *, reason: str, pre_grace_s: float = 0.0
) -> None:
    """The one close primitive — idle close and end_session both go through it.

    Publish "close" → the device breaks its session loop and disconnects itself
    → delete the room afterwards as cleanup (harmless if it is already gone).
    pre_grace_s leaves time for a goodbye still draining on the speaker.
    """
    if pre_grace_s > 0:
        await asyncio.sleep(pre_grace_s)
    try:
        await ctx.room.local_participant.publish_data(
            b"close", topic=AGENT_STATE_TOPIC, reliable=True
        )
    except Exception as e:
        log.warning("close (%s): publish failed: %r", reason, e)
    await asyncio.sleep(CLOSE_GRACE_S)
    try:
        await ctx.api.room.delete_room(api.DeleteRoomRequest(room=ctx.room.name))
        log.info("session closed (%s) — room cleaned up", reason)
    except aiohttp.ServerDisconnectedError:
        log.info("session closed (%s) — room cleaned up", reason)
    except Exception as e:
        log.warning("close (%s): delete_room failed: %r", reason, e)


@dataclass
class _IdleState:
    last_activity: float
    expects_answer: bool = False

    def touch(self) -> None:
        self.last_activity = time.monotonic()


def setup_endpointing(ctx: JobContext, session: AgentSession) -> None:
    """Close the session after a sustained idle window (module docstring)."""
    state = _IdleState(last_activity=time.monotonic())

    @session.on("conversation_item_added")
    def _on_item(ev: Any) -> None:
        state.touch()
        role = getattr(ev.item, "role", None)
        text = (getattr(ev.item, "text_content", None) or "").strip()
        if role is not None and str(role) == "assistant" and text:
            state.expects_answer = text.rstrip().endswith(("?", "?»", '?"'))

    @session.on("agent_state_changed")
    def _on_state(ev: Any) -> None:
        _ = ev
        state.touch()

    @session.on("user_state_changed")
    def _on_user_state(ev: Any) -> None:
        _ = ev
        state.touch()

    async def _watch() -> None:
        while True:
            await asyncio.sleep(POLL_S)
            # Anything non-idle is activity: the user mid-utterance (their turn
            # item only lands AFTER the turn completes — a slow speaker must not
            # be cut off), the agent thinking/speaking, or a speech still
            # draining (the same idle test the announce courtesy uses — a
            # queued announce fires at 3s idle, well inside our window).
            if (
                getattr(session, "user_state", None) == "speaking"
                or getattr(session, "current_speech", None) is not None
                or getattr(session, "agent_state", None) not in (None, "listening")
            ):
                state.touch()
                continue
            idle_s = time.monotonic() - state.last_activity
            window = QUESTION_IDLE_CLOSE_S if state.expects_answer else IDLE_CLOSE_S
            if idle_s < window:
                continue
            m_close.add(1, {"expects_answer": str(state.expects_answer)})
            log.info(
                "idle close: %.1fs without activity (window=%.0fs expects_answer=%s)",
                idle_s,
                window,
                state.expects_answer,
            )
            await close_device_session(ctx, reason="idle")
            return

    _spawn(_watch())
