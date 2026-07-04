import asyncio
import logging
from dataclasses import dataclass
from typing import Any

from livekit.agents import AgentSession, get_job_context
import telemetry
from phantom import PhantomDetector
from tasks import spawn as _spawn

log = logging.getLogger("sebastian.agent.instrumentation")

m_turns = telemetry.counter("sebastian_agent_turns_total", "Conversation items by role")
m_tools = telemetry.counter(
    "sebastian_agent_tool_calls_total", "Tool executions, by tool name"
)
m_state = telemetry.counter(
    "sebastian_agent_state_changes_total", "Agent state transitions"
)
m_errors = telemetry.counter("sebastian_agent_errors_total", "Session error events")

NUDGE_AFTER_S = 3.0
AGENT_STATE_TOPIC = "sebastian.agent_state"


@dataclass
class NudgeState:
    task: asyncio.Task[None] | None = None
    armed: bool = True

def instrument_session(session: AgentSession) -> None:
    # Armed only until the agent's FIRST response of the session. Nudging every
    # unanswered turn guaranteed a reply to echo phantom turns too — the agent
    # never shut up and the room fed back on itself (field-tested the hard way).
    nudge = NudgeState()
    phantom = PhantomDetector()

    def _cancel_nudge() -> None:
        if nudge.task is not None:
            nudge.task.cancel()
            nudge.task = None

    async def _nudge_response() -> None:
        # A realtime model can leave the session's opening command unanswered
        # when the pre-roll injection cancels the greeting mid-generation.
        await asyncio.sleep(NUDGE_AFTER_S)
        nudge.task = None
        log.warning(
            "no response %.1fs after first user turn — nudging generate_reply",
            NUDGE_AFTER_S,
        )
        try:
            session.generate_reply()
        except Exception as e:
            log.warning("nudge failed (session closing?): %r", e)

    def _publish_state(state: str) -> None:
        # The device gates its mic while the agent speaks (half-duplex): the
        # XVF AEC never converges in-session, so the speaker echo comes back
        # as crisp phantom user turns and the room talks to itself.
        async def _pub() -> None:
            try:
                room = get_job_context().room
                await room.local_participant.publish_data(
                    state.encode(), topic=AGENT_STATE_TOPIC, reliable=True
                )
            except Exception as e:
                log.debug("agent state publish failed: %r", e)

        _spawn(_pub())

    @session.on("agent_state_changed")
    def _on_state(ev: Any) -> None:
        state = str(ev.new_state)
        if state in ("thinking", "speaking"):
            _cancel_nudge()
            nudge.armed = False
        phantom.on_state(state)
        _publish_state(state)
        m_state.add(1, {"state": state})
        log.info("agent state: %s", ev.new_state)

    @session.on("conversation_item_added")
    def _on_item(ev: Any) -> None:
        role = getattr(ev.item, "role", None)
        if role is None:  # AgentHandoff and other non-message items
            return
        text = (getattr(ev.item, "text_content", None) or "").strip()
        if text:
            phantom.check(str(role), text)
        if str(role) == "user" and text and nudge.armed:
            _cancel_nudge()
            nudge.task = asyncio.create_task(_nudge_response())
        m_turns.add(1, {"role": str(role)})
        log.info("turn [%s]: %s", role, text[:300])

    @session.on("function_tools_executed")
    def _on_tools(ev: Any) -> None:
        for call in ev.function_calls:
            m_tools.add(1, {"tool": call.name})
            log.info("tool executed: %s", call.name)

    @session.on("error")
    def _on_error(ev: Any) -> None:
        m_errors.add(1)
        log.error("session error: %s", ev.error)
