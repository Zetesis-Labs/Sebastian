"""Sebastian control plane — the §9 execution layer, first slice.

The unifying idea (ROADMAP §9): the device is a LiveKit room; every feature is a
server-side participant acting on it. This service is the front door — it owns
the device's *modes* and exposes them over HTTP (this file) and, later, MCP (the
agent as a client). Both faces call the same logic; single source of truth.

First mode shipped: **announce(text)** — make the device speak a line, triggered
from anywhere (Grafana alert, HA automation, a curl). Flow:

    POST /announce {"text": "..."}  ->  find the device's active room
                                    ->  send it on topic sebastian.announce
                                    ->  the agent (in the room) speaks it

Zero firmware. Works while the device has a session; a proactive announcement to
an *idle* device needs the always-connected endpoint mode (ROADMAP §9 / backlog).
Until then, no active room -> 409 (honest, not a silent no-op).

Run (on the LAN the device/agent are on):
    SEBASTIAN_CONTROL_HOST=10.0.0.115 uv run control_plane.py   # default :8790
Reads LIVEKIT_URL / _API_KEY / _API_SECRET from .env (same as the agent).
"""

import json
import logging
import os
from typing import AsyncIterator

from aiohttp import web
from dotenv import load_dotenv
from livekit import api

import telemetry

load_dotenv()

log = logging.getLogger("sebastian.control-plane")

ANNOUNCE_TOPIC = "sebastian.announce"
DEVICE_IDENTITY = os.getenv("SEBASTIAN_DEVICE_IDENTITY", "esp32-respeaker")
ROOM_PREFIX = "sebastian"

HOST = os.getenv("SEBASTIAN_CONTROL_HOST", "127.0.0.1")
PORT = int(os.getenv("SEBASTIAN_CONTROL_PORT", "8790"))


async def _device_room(lk: api.LiveKitAPI) -> str | None:
    """The room where the device AND the agent are both present, else None.

    Per-session unique rooms today, so we discover it: list sebastian-* rooms and
    find the one holding both. Requiring the agent too matters: data packets are
    not queued for future participants, so an announce sent into a room where the
    agent hasn't joined yet is silently lost (hit in the field: the first announce
    fired the instant the device appeared, before the agent — nobody heard it).
    Cheap at home scale (~1 room); trivial once always-connected makes it a
    single persistent room.
    """
    rooms = await lk.room.list_rooms(api.ListRoomsRequest())
    for room in rooms.rooms:
        if not room.name.startswith(ROOM_PREFIX):
            continue
        parts = await lk.room.list_participants(
            api.ListParticipantsRequest(room=room.name)
        )
        has_device = any(p.identity == DEVICE_IDENTITY for p in parts.participants)
        has_agent = any(
            p.kind == api.ParticipantInfo.Kind.AGENT for p in parts.participants
        )
        if has_device and has_agent:
            return room.name
    return None


async def handle_announce(request: web.Request) -> web.Response:
    try:
        body = await request.json()
        text = str(body["text"]).strip()
    except Exception:
        return web.json_response({"error": "body must be {\"text\": \"...\"}"}, status=400)
    if not text:
        return web.json_response({"error": "empty text"}, status=400)

    lk: api.LiveKitAPI = request.app["lk"]
    room = await _device_room(lk)
    if room is None:
        # No session → the device isn't reachable. This is exactly the gap the
        # always-connected endpoint mode closes (ROADMAP §9).
        log.warning("announce refused: device idle (no active room)")
        return web.json_response(
            {"error": "device idle — no active session to announce into"},
            status=409,
        )
    await lk.room.send_data(
        api.SendDataRequest(
            room=room,
            data=json.dumps({"text": text}).encode(),
            topic=ANNOUNCE_TOPIC,
            kind=api.DataPacket.Kind.RELIABLE,
            destination_identities=[],  # broadcast; the agent is the only consumer
        )
    )
    log.info("announce → room=%s: %r", room, text)
    return web.json_response({"ok": True, "room": room})


async def handle_health(request: web.Request) -> web.Response:
    return web.Response(text="ok")


async def _lk_client(app: web.Application) -> AsyncIterator[None]:
    app["lk"] = api.LiveKitAPI()
    yield
    await app["lk"].aclose()


def main() -> None:
    # INFO to stdout + shipped to Loki under service_name "sebastian-control-plane"
    # via the OTel handler telemetry.setup() attaches to the root logger.
    logging.basicConfig(level=logging.INFO)
    telemetry.setup("sebastian-control-plane")
    app = web.Application()
    app.cleanup_ctx.append(_lk_client)
    app.add_routes(
        [web.post("/announce", handle_announce), web.get("/health", handle_health)]
    )
    log.info("announce on %s:%s topic=%s", HOST, PORT, ANNOUNCE_TOPIC)
    web.run_app(app, host=HOST, port=PORT)


if __name__ == "__main__":
    main()
