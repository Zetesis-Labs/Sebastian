"""Which participant is the speaker (fleet block 1, RF-12).

The control room dispatches the agent with the session's metadata; its
`device_id` is the unit's LiveKit identity (its MAC). The env fallback keeps
`agent.py dev` usable against a room joined by hand.
"""
from __future__ import annotations

import json
import os

DEFAULT_DEVICE_IDENTITY = os.getenv("SEBASTIAN_DEVICE_IDENTITY", "esp32-respeaker")


def device_identity_from_metadata(metadata: str | None, default: str = DEFAULT_DEVICE_IDENTITY) -> str:
    """The identity named by the dispatch metadata, else `default`.

    Pure: malformed or empty metadata never raises — the agent then waits for
    the default identity exactly as before the fleet work.
    """
    if not metadata:
        return default
    try:
        body = json.loads(metadata)
    except ValueError:
        return default
    if not isinstance(body, dict):
        return default
    device_id = body.get("device_id")
    if isinstance(device_id, str) and device_id.strip():
        return device_id.strip()
    return default
