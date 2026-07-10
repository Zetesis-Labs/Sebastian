"""Token server for the Sebastian device.

Mints a short-lived LiveKit access token on demand so the firmware doesn't carry
a static JWT that expires (720h → reflash), and dispatches the named agent
("sebastian") into the room **explicitly via the server API** before returning.

Design (both per LiveKit's dispatch docs + forum guidance, 2026-07-08):
- **Explicit-only dispatch**: create_dispatch creates the room if missing, so
  the "token RoomConfiguration only applies at room creation" gotcha can't
  exist. Dispatch failure → 503 (a token into an agentless room would just die
  by silence timeout).
- **A UNIQUE room per session** (`sebastian-<hex>`): room reuse was the root of
  a whole failure family — stale agent sessions ignoring a re-wake's pre-roll
  (the zombie loop), duplicate dispatch races (two instances of the same named
  agent both answering the user with overlapping audio), and the participant
  guard + zombie-room self-heal we wrote to contain them. Fresh room = no shared
  state = all of that logic deleted. Orphaned rooms (token fetched, device never
  joined) close themselves on the empty-room timeout.

The device does a plain HTTP GET at each session open and gets back two lines:

    <serverUrl>\n<token>

Plaintext (not JSON) keeps the firmware parser trivial: split on the first
newline. The URL is a wss:// address and the token is a dot-separated JWT, so
neither contains a newline.

Run:
    uv run token_server.py                              # binds 127.0.0.1:8787
    SEBASTIAN_TOKEN_HOST=0.0.0.0 uv run token_server.py  # serve the device on the LAN
    SEBASTIAN_TOKEN_PORT=9000 uv run token_server.py

Reads LIVEKIT_URL / LIVEKIT_API_KEY / LIVEKIT_API_SECRET from .env (same as the
agent). Serve it on the LAN the device is on; point the firmware at it via
`token_server_url` in secrets.zig.
"""

import logging
import os
import secrets
from datetime import timedelta
from typing import AsyncIterator

from aiohttp import web
from dotenv import load_dotenv
from livekit import api

import telemetry

load_dotenv()

log = logging.getLogger("sebastian.token-server")

ROOM_PREFIX = "sebastian"
IDENTITY = "esp32-respeaker"
AGENT_NAME = "sebastian"
TOKEN_TTL = timedelta(hours=1)  # refetched per session, so short is fine

# Bind to loopback by default: this mints LiveKit tokens, so don't expose it to
# the whole LAN unless asked. Set SEBASTIAN_TOKEN_HOST=0.0.0.0 to serve the device.
HOST = os.getenv("SEBASTIAN_TOKEN_HOST", "127.0.0.1")
PORT = int(os.getenv("SEBASTIAN_TOKEN_PORT", "8787"))
LIVEKIT_URL = os.environ["LIVEKIT_URL"]
API_KEY = os.environ["LIVEKIT_API_KEY"]
API_SECRET = os.environ["LIVEKIT_API_SECRET"]


def _session_room() -> str:
    # Unique room per session: fresh state every wake, so no stale agent can
    # ever collide with a new session and duplicate dispatch is impossible.
    return f"{ROOM_PREFIX}-{secrets.token_hex(4)}"


def _mint(room: str) -> str:
    grant = api.VideoGrants(room_join=True, room=room)
    jwt: str = (
        api.AccessToken(API_KEY, API_SECRET)
        .with_identity(IDENTITY)
        .with_name(IDENTITY)
        .with_grants(grant)
        .with_ttl(TOKEN_TTL)
        .to_jwt()
    )
    return jwt


async def handle_token(request: web.Request) -> web.Response:
    # No dispatch → no token: a token into an agentless room would just die by
    # silence timeout; a 503 makes the firmware fail fast instead. Dispatch is
    # awaited BEFORE returning so the agent is on its way when the device joins
    # (create_dispatch creates the room).
    room = _session_room()
    try:
        await request.app["lk"].agent_dispatch.create_dispatch(
            api.CreateAgentDispatchRequest(agent_name=AGENT_NAME, room=room)
        )
    except Exception as e:
        log.error("dispatch failed — refusing token: %r", e)
        return web.Response(status=503, text="agent dispatch failed")
    log.info("token + dispatch issued room=%s", room)
    body = f"{LIVEKIT_URL}\n{_mint(room)}"
    return web.Response(text=body, content_type="text/plain")


async def handle_health(request: web.Request) -> web.Response:
    return web.Response(text="ok")


async def _lk_client(app: web.Application) -> AsyncIterator[None]:
    app["lk"] = api.LiveKitAPI()
    yield
    await app["lk"].aclose()


def main() -> None:
    # INFO to stdout (dev terminal) AND, via the OTel handler telemetry.setup()
    # attaches to the root logger, shipped to Loki under service_name
    # "sebastian-token-server" — same per-component stream prod gets from promtail.
    logging.basicConfig(level=logging.INFO)
    telemetry.setup("sebastian-token-server")
    app = web.Application()
    app.cleanup_ctx.append(_lk_client)
    app.add_routes([web.get("/token", handle_token), web.get("/health", handle_health)])
    log.info(
        "%s rooms=%s-* agent=%s on %s:%s",
        LIVEKIT_URL, ROOM_PREFIX, AGENT_NAME, HOST, PORT,
    )
    web.run_app(app, host=HOST, port=PORT)


if __name__ == "__main__":
    main()
