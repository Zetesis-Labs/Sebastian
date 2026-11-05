"""The music a source plays on the speaker (server internal/playback).

The agent is never in the playback room: the unit leaves it to talk and
comes back. From the conversation, the agent can only stop it.
"""

import asyncio
import logging

import aiohttp

from meeting_mode import AGENT_SECRET, API_URL, http_session

log = logging.getLogger("sebastian.playback")


def stop_reply(status: int) -> str:
    """The control room's answer to a stop → what the agent says (pure)."""
    if status == 204:
        return "Música parada."
    if status == 404:
        return "No está sonando nada."
    return "No he podido parar la música."


async def stop_playback(device_id: str) -> str:
    try:
        async with http_session() as http:
            async with http.post(
                f"{API_URL}/v1/playback/stop",
                headers={"X-Agent-Secret": AGENT_SECRET},
                json={"deviceId": device_id},
                timeout=aiohttp.ClientTimeout(total=10),
            ) as res:
                log.info("[voice] stop music on %s → %s", device_id, res.status)
                return stop_reply(res.status)
    except (aiohttp.ClientError, asyncio.TimeoutError, OSError) as e:
        log.warning("[voice] stop music failed: %r", e)
        return stop_reply(0)
